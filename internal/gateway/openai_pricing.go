package gateway

const (
	OpenAIPriceCatalogSource    = "https://openai.com/api/pricing/"
	OpenAIPriceCatalogCurrency  = "USD"
	OpenAIPriceCatalogUnit      = "per_1m_tokens"
	OpenAIPriceCatalogTier      = "standard_short_context"
	OpenAIPriceCatalogVersion   = "2026-07-27"
	OpenAIPriceCatalogUpdatedAt = "2026-07-27"
)

type OfficialModelPrice struct {
	InputPriceMicros       int64  `json:"inputPriceMicros"`
	OutputPriceMicros      int64  `json:"outputPriceMicros"`
	CachedInputPriceMicros *int64 `json:"cachedInputPriceMicros"`
	CacheWritePriceMicros  *int64 `json:"cacheWritePriceMicros"`
	Source                 string `json:"source"`
	Currency               string `json:"currency"`
	Unit                   string `json:"unit"`
	ContextTier            string `json:"contextTier"`
	CatalogVersion         string `json:"catalogVersion"`
	UpdatedAt              string `json:"updatedAt"`
}

type officialPriceEntry struct {
	input       int64
	output      int64
	cachedInput *int64
	cacheWrite  *int64
}

var openAIStandardTextPrices = map[string]officialPriceEntry{
	"gpt-5.6-sol":         {input: 5_000_000, output: 30_000_000, cachedInput: priceMicros(500_000), cacheWrite: priceMicros(6_250_000)},
	"gpt-5.6-terra":       {input: 2_500_000, output: 15_000_000, cachedInput: priceMicros(250_000), cacheWrite: priceMicros(3_125_000)},
	"gpt-5.6-luna":        {input: 1_000_000, output: 6_000_000, cachedInput: priceMicros(100_000), cacheWrite: priceMicros(1_250_000)},
	"gpt-5.5":             {input: 5_000_000, output: 30_000_000, cachedInput: priceMicros(500_000)},
	"gpt-5.5-pro":         {input: 30_000_000, output: 180_000_000},
	"gpt-5.4":             {input: 2_500_000, output: 15_000_000, cachedInput: priceMicros(250_000)},
	"gpt-5.4-mini":        {input: 750_000, output: 4_500_000, cachedInput: priceMicros(75_000)},
	"gpt-5.4-nano":        {input: 200_000, output: 1_250_000, cachedInput: priceMicros(20_000)},
	"gpt-5.4-pro":         {input: 30_000_000, output: 180_000_000},
	"gpt-5.2":             {input: 1_750_000, output: 14_000_000, cachedInput: priceMicros(175_000)},
	"gpt-5.2-pro":         {input: 21_000_000, output: 168_000_000},
	"gpt-5.1":             {input: 1_250_000, output: 10_000_000, cachedInput: priceMicros(125_000)},
	"gpt-5":               {input: 1_250_000, output: 10_000_000, cachedInput: priceMicros(125_000)},
	"gpt-5-mini":          {input: 250_000, output: 2_000_000, cachedInput: priceMicros(25_000)},
	"gpt-5-nano":          {input: 50_000, output: 400_000, cachedInput: priceMicros(5_000)},
	"gpt-5-pro":           {input: 15_000_000, output: 120_000_000},
	"codex-auto-review":   {input: 5_000_000, output: 3_000_000, cachedInput: priceMicros(500_000)},
	"gpt-5.3-codex-spark": {input: 3_150_000, output: 25_200_000, cachedInput: priceMicros(315_000)},
	"gpt-5-codex":         {input: 1_250_000, output: 10_000_000, cachedInput: priceMicros(125_000)},
	"gpt-5.1-codex":       {input: 1_250_000, output: 10_000_000, cachedInput: priceMicros(125_000)},
	"gpt-5.2-codex":       {input: 1_750_000, output: 14_000_000, cachedInput: priceMicros(175_000)},
	"gpt-4.1":             {input: 2_000_000, output: 8_000_000, cachedInput: priceMicros(500_000)},
	"gpt-4.1-mini":        {input: 400_000, output: 1_600_000, cachedInput: priceMicros(100_000)},
	"gpt-4.1-nano":        {input: 100_000, output: 400_000, cachedInput: priceMicros(25_000)},
	"gpt-4o":              {input: 2_500_000, output: 10_000_000, cachedInput: priceMicros(1_250_000)},
	"gpt-4o-mini":         {input: 150_000, output: 600_000, cachedInput: priceMicros(75_000)},
	"o4-mini":             {input: 1_100_000, output: 4_400_000, cachedInput: priceMicros(275_000)},
	"o3":                  {input: 2_000_000, output: 8_000_000, cachedInput: priceMicros(500_000)},
	"o3-mini":             {input: 1_100_000, output: 4_400_000, cachedInput: priceMicros(550_000)},
	"o3-pro":              {input: 20_000_000, output: 80_000_000},
	"o1":                  {input: 15_000_000, output: 60_000_000, cachedInput: priceMicros(7_500_000)},
	"o1-mini":             {input: 1_100_000, output: 4_400_000, cachedInput: priceMicros(550_000)},
}

func priceMicros(value int64) *int64 {
	return &value
}

func copyPriceMicros(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func OpenAIOfficialPrice(modelID string) *OfficialModelPrice {
	entry, ok := openAIStandardTextPrices[modelID]
	if !ok {
		return nil
	}
	return &OfficialModelPrice{
		InputPriceMicros:       entry.input,
		OutputPriceMicros:      entry.output,
		CachedInputPriceMicros: copyPriceMicros(entry.cachedInput),
		CacheWritePriceMicros:  copyPriceMicros(entry.cacheWrite),
		Source:                 OpenAIPriceCatalogSource,
		Currency:               OpenAIPriceCatalogCurrency,
		Unit:                   OpenAIPriceCatalogUnit,
		ContextTier:            OpenAIPriceCatalogTier,
		CatalogVersion:         OpenAIPriceCatalogVersion,
		UpdatedAt:              OpenAIPriceCatalogUpdatedAt,
	}
}
