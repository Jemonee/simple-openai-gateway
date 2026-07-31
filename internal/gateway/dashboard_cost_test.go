package gateway

import "testing"

func TestOfficialUsageCostUsesInputCacheAndOutputCatalogPrices(t *testing.T) {
	got := officialUsageCost("gpt-5.6-sol", 1_000_000, 800_000, 1_000_000, 100_000, 100_000)
	const want = int64(34_675_000)
	if got != want {
		t.Fatalf("officialUsageCost() = %d, want %d", got, want)
	}
	if got := officialUsageCost("unlisted-model", 1_000_000, 1_000_000, 1_000_000, 0, 0); got != 0 {
		t.Fatalf("unlisted officialUsageCost() = %d, want 0", got)
	}
}
