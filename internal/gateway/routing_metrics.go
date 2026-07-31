package gateway

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"gorm.io/gorm"
)

const (
	routingMetricWindow               = 30 * time.Minute
	routingHistorySamplesPerCandidate = 20
	routingHistoryMinimumSampleSize   = 100
	routingHistoryMaximumSampleSize   = 1000
	routingColdStartExplorationShare  = 0.20
	routingBalanceResponse            = 0.50
	routingBalanceMinimumMultiplier   = 0.50
	routingBalanceMaximumMultiplier   = 1.50
)

type recentRoutingMetric struct {
	LatencyMS        float64
	FirstTokenMS     float64
	TokensPerSecond  float64
	CacheHitRate     float64
	CacheSampleCount int64
	CacheRate        float64
	CacheTokenCount  int64
	RouteCount       int64
	RouteShare       float64
	RouteSampleSize  int64
}

func loadRecentRoutingMetrics(ctx context.Context, db *gorm.DB, mappingIDs []uint64, now time.Time) (map[uint64]recentRoutingMetric, error) {
	metrics := make(map[uint64]recentRoutingMetric, len(mappingIDs))
	if len(mappingIDs) == 0 {
		return metrics, nil
	}
	for _, mappingID := range mappingIDs {
		metrics[mappingID] = recentRoutingMetric{}
	}
	type row struct {
		ChannelModelID    uint64
		RouteCount        int64
		LatencyTotal      int64
		LatencySamples    int64
		FirstTokenTotal   int64
		FirstTokenSamples int64
		OutputTokens      int64
		GenerationMS      int64
		InputTokens       int64
		CachedTokens      int64
		CacheHits         int64
		CacheSamples      int64
	}
	var rows []row
	err := db.WithContext(ctx).Model(&RelayAttemptLog{}).
		Select("channel_model_id, "+
			"COALESCE(SUM(CASE WHEN success = ? AND latency_ms > 0 THEN latency_ms ELSE 0 END), 0) AS latency_total, SUM(CASE WHEN success = ? AND latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, "+
			"COALESCE(SUM(CASE WHEN success = ? AND first_token_ms > 0 THEN first_token_ms ELSE 0 END), 0) AS first_token_total, SUM(CASE WHEN success = ? AND first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, "+
			"COALESCE(SUM(CASE WHEN success = ? AND output_tokens > 0 AND first_token_ms > 0 AND duration_ms > first_token_ms THEN output_tokens ELSE 0 END), 0) AS output_tokens, "+
			"COALESCE(SUM(CASE WHEN success = ? AND output_tokens > 0 AND first_token_ms > 0 AND duration_ms > first_token_ms THEN duration_ms - first_token_ms ELSE 0 END), 0) AS generation_ms, "+
			"COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, SUM(CASE WHEN input_tokens > 0 AND cached_tokens > 0 THEN 1 ELSE 0 END) AS cache_hits, SUM(CASE WHEN input_tokens > 0 THEN 1 ELSE 0 END) AS cache_samples",
			true, true, true, true, true, true).
		Where("channel_model_id IN ? AND created_at >= ?", mappingIDs, now.Add(-routingMetricWindow)).
		Group("channel_model_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, item := range rows {
		metric := recentRoutingMetric{}
		if item.LatencySamples > 0 {
			metric.LatencyMS = float64(item.LatencyTotal) / float64(item.LatencySamples)
		}
		if item.FirstTokenSamples > 0 {
			metric.FirstTokenMS = float64(item.FirstTokenTotal) / float64(item.FirstTokenSamples)
		}
		if item.GenerationMS > 0 {
			metric.TokensPerSecond = float64(item.OutputTokens) * 1000 / float64(item.GenerationMS)
		}
		if item.CacheSamples > 0 {
			metric.CacheHitRate = min(max(float64(item.CacheHits)/float64(item.CacheSamples), 0), 1)
			metric.CacheSampleCount = item.CacheSamples
		}
		if item.InputTokens > 0 {
			metric.CacheRate = min(max(float64(item.CachedTokens)/float64(item.InputTokens), 0), 1)
			metric.CacheTokenCount = item.InputTokens
		}
		metrics[item.ChannelModelID] = metric
	}
	var latest []RelayAttemptLog
	sampleLimit := routingHistorySampleSize(len(mappingIDs))
	if err := db.WithContext(ctx).Where("channel_model_id IN ?", mappingIDs).Order("created_at DESC, id DESC").Limit(sampleLimit).Find(&latest).Error; err != nil {
		return nil, err
	}
	mappingSet := make(map[uint64]struct{}, len(mappingIDs))
	for _, mappingID := range mappingIDs {
		mappingSet[mappingID] = struct{}{}
	}
	for _, attempt := range latest {
		if _, exists := mappingSet[attempt.ChannelModelID]; !exists {
			continue
		}
		metric := metrics[attempt.ChannelModelID]
		metric.RouteCount++
		metrics[attempt.ChannelModelID] = metric
	}
	for mappingID, metric := range metrics {
		metric.RouteSampleSize = int64(len(latest))
		if len(latest) > 0 {
			metric.RouteShare = float64(metric.RouteCount) / float64(len(latest))
		}
		metrics[mappingID] = metric
	}
	return metrics, nil
}

func routingHistorySampleSize(candidateCount int) int {
	return min(max(candidateCount*routingHistorySamplesPerCandidate, routingHistoryMinimumSampleSize), routingHistoryMaximumSampleSize)
}

func (r *Router) expectationProbabilityOrder(strategy string, candidates []RouteCandidate) *RouteDecision {
	decision, total := r.scoredRouteDecision(strategy, candidates)
	point := float64(0)
	if total > 0 && r.random != nil {
		point = float64(r.random(1_000_000)) / 1_000_000 * total
	}
	cumulative := float64(0)
	selectedIndex := 0
	for index := range decision.Candidates {
		decision.Candidates[index].Probability = decision.Candidates[index].Expectation / total
	}
	for index := range decision.Candidates {
		cumulative += decision.Candidates[index].Expectation
		if point < cumulative {
			selectedIndex = index
			break
		}
	}
	decision.Candidates[selectedIndex].Selected = true
	selected := candidates[selectedIndex]
	copy(candidates[1:selectedIndex+1], candidates[:selectedIndex])
	candidates[0] = selected
	return decision
}

func (r *Router) scoredRouteDecision(strategy string, candidates []RouteCandidate) (*RouteDecision, float64) {
	switch strategy {
	case RoutingLowestCost:
		sortCandidatesByCost(candidates)
	case RoutingLowestLatency:
		sortCandidatesByLatency(candidates)
	default:
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Mapping.Priority > candidates[j].Mapping.Priority })
	}
	weights := r.routingDecisionWeights()
	decision := &RouteDecision{Strategy: strategy, Mode: "probability", Weights: weights, Candidates: make([]RouteDecisionCandidate, len(candidates))}
	if len(candidates) == 0 {
		return decision, 0
	}
	minCost := candidates[0].Cost
	bestLatency := float64(0)
	bestFirstToken := float64(0)
	bestThroughput := float64(0)
	maxPriority := candidates[0].Mapping.Priority
	for _, candidate := range candidates {
		minCost = min(minCost, candidate.Cost)
		latency := observedCandidateLatency(candidate)
		if latency > 0 && (bestLatency == 0 || latency < bestLatency) {
			bestLatency = latency
		}
		if firstToken := candidate.RecentFirstTokenMS; firstToken > 0 && (bestFirstToken == 0 || firstToken < bestFirstToken) {
			bestFirstToken = firstToken
		}
		bestThroughput = max(bestThroughput, candidate.RecentTokensPerSecond)
		maxPriority = max(maxPriority, candidate.Mapping.Priority)
	}
	baseExpectations := make([]float64, len(candidates))
	baseTotal := float64(0)
	for index, candidate := range candidates {
		observedLatency := observedCandidateLatency(candidate)
		costAdvantage := (float64(max(minCost, 0)) + 1) / (float64(max(candidate.Cost, 0)) + 1)
		priceScore := min(max(costAdvantage, 0), 1)
		efficiencyScore := candidateEfficiencyScore(candidate, bestFirstToken, bestLatency, bestThroughput)
		qualityScore := candidateQualityScore(candidate)
		if strategy == RoutingLowestCost {
			priceScore *= priceScore
		}
		if strategy == RoutingLowestLatency {
			efficiencyScore *= efficiencyScore
		}
		weightFactor := float64(max(candidate.Mapping.Weight, 1)) / 100
		compositeScore := weights.Price*priceScore + weights.Efficiency*efficiencyScore + weights.Quality*qualityScore
		expectation := max(weightFactor*compositeScore, 0.000001)
		if strategy == RoutingPriorityWeighted && candidate.Mapping.Priority < maxPriority {
			expectation = 0
		}
		baseExpectations[index] = expectation
		baseTotal += expectation
		decision.Candidates[index] = RouteDecisionCandidate{
			ChannelID: candidate.Channel.ID, ChannelName: candidate.Channel.Name, ChannelModelID: candidate.Mapping.ID, UpstreamModel: candidate.Mapping.UpstreamModel,
			Priority: candidate.Mapping.Priority, Weight: candidate.Mapping.Weight, ExpectedCostMicros: candidate.Cost, SuccessRate: float64(candidateSuccessBasisPoints(candidate)) / float64(routeProbabilityScale),
			LatencyMS: observedLatency, FirstTokenMS: candidate.RecentFirstTokenMS, TokensPerSecond: candidate.RecentTokensPerSecond, PriceScore: priceScore, EfficiencyScore: efficiencyScore, QualityScore: qualityScore,
			CacheHitRate: candidate.RecentCacheHitRate, CacheSampleCount: candidate.RecentCacheSamples, CacheRate: candidate.RecentCacheRate, CacheTokenCount: candidate.RecentCacheTokens, RecentRouteCount: candidate.RecentRouteCount,
			RecentRouteShare: candidate.RecentRouteShare, RouteSampleSize: candidate.RouteSampleSize, Expectation: expectation,
		}
	}
	total := applyRouteBalance(candidates, decision, baseExpectations, baseTotal, weights.Balance)
	applyColdStartExploration(strategy, candidates, decision, maxPriority, total)
	return decision, total
}

func (r *Router) routingDecisionWeights() RouteDecisionWeights {
	var applicationConfig *config.ApplicationConfig
	if r != nil && r.configProvider != nil {
		applicationConfig = r.configProvider()
	}
	price, efficiency, quality, balance := config.EffectiveRoutingDecisionWeights(applicationConfig)
	return RouteDecisionWeights{Price: price, Efficiency: efficiency, Quality: quality, Balance: balance}
}

func observedCandidateLatency(candidate RouteCandidate) float64 {
	if candidate.RecentLatencyMS > 0 {
		return candidate.RecentLatencyMS
	}
	return candidate.Channel.LatencyEWMA
}

func lowerIsBetterScore(best float64, value float64) float64 {
	if best <= 0 || value <= 0 {
		return 0.5
	}
	return min(max(best/value, 0), 1)
}

func higherIsBetterScore(best float64, value float64) float64 {
	if best <= 0 || value <= 0 {
		return 0.5
	}
	return min(max(value/best, 0), 1)
}

func candidateEfficiencyScore(candidate RouteCandidate, bestFirstToken float64, bestLatency float64, bestThroughput float64) float64 {
	firstTokenScore := lowerIsBetterScore(bestFirstToken, candidate.RecentFirstTokenMS)
	latencyScore := lowerIsBetterScore(bestLatency, observedCandidateLatency(candidate))
	throughputScore := higherIsBetterScore(bestThroughput, candidate.RecentTokensPerSecond)
	return 0.45*firstTokenScore + 0.20*latencyScore + 0.35*throughputScore
}

func candidateQualityScore(candidate RouteCandidate) float64 {
	successScore := float64(candidateSuccessBasisPoints(candidate)) / float64(routeProbabilityScale)
	cacheHitScore := 0.5
	if candidate.RecentCacheSamples > 0 {
		cacheHitScore = min(max(candidate.RecentCacheHitRate, 0), 1)
	}
	cacheRateScore := 0.5
	if candidate.RecentCacheTokens > 0 {
		cacheRateScore = min(max(candidate.RecentCacheRate, 0), 1)
	}
	return 0.70*successScore + 0.18*cacheHitScore + 0.12*cacheRateScore
}

func applyRouteBalance(candidates []RouteCandidate, decision *RouteDecision, baseExpectations []float64, baseTotal float64, balanceWeight float64) float64 {
	if baseTotal <= 0 {
		return 0
	}
	corrected := make([]float64, len(candidates))
	correctedTotal := float64(0)
	activeCandidateCount := 0
	activeSampleSize := int64(0)
	for index, candidate := range candidates {
		if baseExpectations[index] > 0 {
			activeCandidateCount++
			activeSampleSize += candidate.RecentRouteCount
		}
	}
	desiredSampleSize := float64(routingHistorySampleSize(activeCandidateCount))
	for index, candidate := range candidates {
		if baseExpectations[index] <= 0 {
			continue
		}
		targetShare := baseExpectations[index] / baseTotal
		actualShare := float64(0)
		if activeSampleSize > 0 {
			actualShare = float64(candidate.RecentRouteCount) / float64(activeSampleSize)
		}
		multiplier := routeBalanceMultiplier(actualShare, activeSampleSize, targetShare, desiredSampleSize)
		corrected[index] = targetShare * multiplier
		correctedTotal += corrected[index]
		decision.Candidates[index].TargetRouteShare = targetShare
		decision.Candidates[index].BalanceMultiplier = multiplier
		decision.Candidates[index].RecentRouteShare = actualShare
		decision.Candidates[index].RouteSampleSize = activeSampleSize
	}
	balanceWeight = min(max(balanceWeight, 0), 1)
	for index := range candidates {
		if baseExpectations[index] <= 0 {
			decision.Candidates[index].Expectation = 0
			continue
		}
		baseProbability := baseExpectations[index] / baseTotal
		balancedProbability := baseProbability
		if correctedTotal > 0 {
			balancedProbability = corrected[index] / correctedTotal
		}
		decision.Candidates[index].Expectation = (1-balanceWeight)*baseProbability + balanceWeight*balancedProbability
	}
	return 1
}

func routeBalanceMultiplier(actualShare float64, sampleSize int64, targetShare float64, desiredSampleSize float64) float64 {
	if targetShare <= 0 || sampleSize <= 0 || desiredSampleSize <= 0 {
		return 1
	}
	sampleCount := float64(sampleSize)
	shareFloor := 1 / sampleCount
	deviation := (min(max(actualShare, 0), 1) - targetShare) / max(targetShare, shareFloor)
	rawMultiplier := min(max(math.Exp(-routingBalanceResponse*deviation), routingBalanceMinimumMultiplier), routingBalanceMaximumMultiplier)
	confidence := min(sampleCount/desiredSampleSize, 1)
	return 1 + (rawMultiplier-1)*confidence
}

func applyColdStartExploration(strategy string, candidates []RouteCandidate, decision *RouteDecision, maxPriority int, total float64) {
	if total <= 0 || len(candidates) == 0 {
		return
	}
	coldStartWeight := int64(0)
	for _, candidate := range candidates {
		if coldStartExplorationEligible(strategy, candidate, maxPriority) {
			coldStartWeight += int64(max(candidate.Mapping.Weight, 1))
		}
	}
	if coldStartWeight == 0 {
		return
	}

	// Reserve bounded traffic for candidates that cannot build quality metrics yet.
	// Configured weights still control how that exploration traffic is shared.
	for index, candidate := range candidates {
		probability := (1 - routingColdStartExplorationShare) * decision.Candidates[index].Expectation / total
		if coldStartExplorationEligible(strategy, candidate, maxPriority) {
			probability += routingColdStartExplorationShare * float64(max(candidate.Mapping.Weight, 1)) / float64(coldStartWeight)
		}
		decision.Candidates[index].Expectation = probability * total
	}
}

func coldStartExplorationEligible(strategy string, candidate RouteCandidate, maxPriority int) bool {
	if strategy == RoutingPriorityWeighted && candidate.Mapping.Priority < maxPriority {
		return false
	}
	return candidate.RecentAttemptCount == 0 && candidate.RecentCacheSamples == 0 && candidate.RecentCacheTokens == 0
}

func deterministicRouteDecision(strategy string, mode string, candidates []RouteCandidate) *RouteDecision {
	decision := &RouteDecision{Strategy: strategy, Mode: mode, Candidates: make([]RouteDecisionCandidate, 0, len(candidates))}
	for index, candidate := range candidates {
		decision.Candidates = append(decision.Candidates, RouteDecisionCandidate{ChannelID: candidate.Channel.ID, ChannelName: candidate.Channel.Name, ChannelModelID: candidate.Mapping.ID, UpstreamModel: candidate.Mapping.UpstreamModel, Priority: candidate.Mapping.Priority, Weight: candidate.Mapping.Weight, ExpectedCostMicros: candidate.Cost, SuccessRate: candidate.RecentSuccessRate, LatencyMS: candidate.RecentLatencyMS, CacheHitRate: candidate.RecentCacheHitRate, CacheSampleCount: candidate.RecentCacheSamples, CacheRate: candidate.RecentCacheRate, CacheTokenCount: candidate.RecentCacheTokens, RecentRouteCount: candidate.RecentRouteCount, RecentRouteShare: candidate.RecentRouteShare, RouteSampleSize: candidate.RouteSampleSize, Expectation: boolScore(index == 0), Probability: boolScore(index == 0), Selected: index == 0})
	}
	return decision
}

func boolScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
