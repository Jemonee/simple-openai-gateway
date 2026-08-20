package gateway

import "math"

func throughputGenerationMS(outputTokens, durationMS, latencyMS, firstTokenMS int64, persistedTokensPerSecond float64) float64 {
	if outputTokens <= 0 {
		return 0
	}
	// Stored firstTokenMS is measured after the response-header latency, while
	// durationMS includes the full upstream request. Remove both non-generation
	// windows before calculating completion-token throughput.
	if firstTokenMS > 0 && latencyMS >= 0 && durationMS > latencyMS+firstTokenMS {
		return float64(durationMS - latencyMS - firstTokenMS)
	}
	if persistedTokensPerSecond > 0 && !math.IsInf(persistedTokensPerSecond, 0) && !math.IsNaN(persistedTokensPerSecond) {
		return float64(outputTokens) * 1000 / persistedTokensPerSecond
	}
	return 0
}

func normalizedTokensPerSecond(outputTokens, durationMS, latencyMS, firstTokenMS int64, persistedTokensPerSecond float64) (float64, int64) {
	generationMS := throughputGenerationMS(outputTokens, durationMS, latencyMS, firstTokenMS, persistedTokensPerSecond)
	if generationMS <= 0 {
		return 0, 0
	}
	value := float64(outputTokens) * 1000 / generationMS
	if value <= 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, 0
	}
	return value, max(int64(math.Round(generationMS)), int64(1))
}

func throughputSQL(prefix string, successPredicate string) (string, string) {
	outputTokens := prefix + "output_tokens"
	durationMS := prefix + "duration_ms"
	firstTokenMS := prefix + "first_token_ms"
	persistedTPS := prefix + "tokens_per_second"
	latencyMS := prefix + "latency_ms"
	stableTiming := firstTokenMS + " > 0 AND " + latencyMS + " >= 0 AND " + durationMS + " > " + latencyMS + " + " + firstTokenMS
	validSample := successPredicate + " AND " + outputTokens + " > 0 AND ((" + stableTiming + ") OR " + persistedTPS + " > 0)"
	generationMS := "CASE WHEN " + successPredicate + " AND " + outputTokens + " > 0 AND " + stableTiming +
		" THEN " + durationMS + " - " + latencyMS + " - " + firstTokenMS +
		" WHEN " + successPredicate + " AND " + outputTokens + " > 0 AND " + persistedTPS + " > 0" +
		" THEN 1000.0 * " + outputTokens + " / " + persistedTPS + " END"
	average := "COALESCE(1.0 * SUM(CASE WHEN " + validSample + " THEN " + outputTokens + " ELSE 0 END) * 1000 / NULLIF(SUM(" + generationMS + "), 0), 0)"
	sampleCount := "SUM(CASE WHEN " + validSample + " THEN 1 ELSE 0 END)"
	return average, sampleCount
}
