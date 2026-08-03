package gateway

import (
	"time"

	"gorm.io/gorm"
)

const (
	RoutingPriorityWeighted                       = "priority_weighted"
	RoutingLowestCost                             = "lowest_cost"
	RoutingLowestLatency                          = "lowest_latency"
	SelectionReasonInitialRoute                   = "initial_route"
	SelectionReasonResponseAffinity               = "response_affinity"
	SelectionReasonSessionAffinity                = "session_affinity"
	SelectionReasonModelSwitch                    = "model_switch"
	SelectionReasonChannelDisabled                = "channel_disabled"
	SelectionReasonMappingDisabled                = "mapping_disabled"
	SelectionReasonCircuitOpen                    = "circuit_open"
	SelectionReasonAffinityTargetMissing          = "affinity_target_missing"
	SelectionReasonRetryableStatus                = "retryable_status"
	SelectionReasonTransportError                 = "transport_error"
	SelectionReasonResponseError                  = "response_error"
	SelectionReasonUpstreamApplicationError       = "upstream_application_error"
	SelectionReasonGatewayPreparationError        = "gateway_preparation_error"
	SelectionReasonCircuitOpened                  = "circuit_opened"
	CostSourceUpstream                            = "upstream"
	CostSourceFallback                            = "estimated_fallback"
	CostSourceMixed                               = "mixed"
	CostSourceFailedZero                          = "failed_zero"
	RelayOutcomeSuccess                           = "success"
	RelayOutcomeCanceled                          = "canceled"
	RelayOutcomeFailed                            = "failed"
	RelayOutcomeProcessing                        = "processing"
	CircuitLevelClosed                            = 0
	CircuitLevelTemporary                         = 1
	CircuitLevelExtended                          = 2
	CircuitLevelManual                            = 3
	DefaultPriceMultiplierBasisPoints       int64 = 10_000
	MaxPriceMultiplierBasisPoints           int64 = 1_000_000
)

type AdminUser struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	Username     string `gorm:"size:80;uniqueIndex;not null"`
	PasswordHash string `gorm:"size:255;not null"`
	Enabled      bool   `gorm:"not null;default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AdminSession struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	TokenHash string `gorm:"size:64;uniqueIndex;not null"`
	UserID    uint64 `gorm:"index;not null"`
	ExpiresAt time.Time
	CreatedAt time.Time
	LastSeen  time.Time
}

type Channel struct {
	ID                         uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                       string     `gorm:"size:120;not null" json:"name"`
	BaseURL                    string     `gorm:"size:1024;not null" json:"baseUrl"`
	APIKeyCipher               string     `gorm:"type:text;not null" json:"-"`
	Enabled                    bool       `gorm:"not null;default:true" json:"enabled"`
	SupportsStreamUsage        bool       `gorm:"not null;default:true" json:"supportsStreamUsage"`
	PriceMultiplierBasisPoints int64      `gorm:"not null;default:10000" json:"priceMultiplierBasisPoints"`
	ConsecutiveFailures        int        `gorm:"not null;default:0" json:"consecutiveFailures"`
	CircuitLevel               int        `gorm:"not null;default:0" json:"circuitLevel"`
	CircuitOpenUntil           *time.Time `json:"circuitOpenUntil"`
	LatencyEWMA                float64    `gorm:"not null;default:0" json:"latencyEwmaMs"`
	LastHealthAt               *time.Time `json:"lastHealthAt"`
	LastError                  string     `gorm:"type:text" json:"lastError"`
	CreatedAt                  time.Time  `json:"createdAt"`
	UpdatedAt                  time.Time  `json:"updatedAt"`
}

type GatewayModel struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name            string    `gorm:"size:160;uniqueIndex;not null" json:"name"`
	RoutingStrategy string    `gorm:"size:32;not null;default:priority_weighted" json:"routingStrategy"`
	Enabled         bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ChannelModel struct {
	ID                         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ChannelID                  uint64    `gorm:"uniqueIndex:idx_channel_model;not null" json:"channelId"`
	ModelID                    uint64    `gorm:"uniqueIndex:idx_channel_model;index;not null" json:"modelId"`
	UpstreamModel              string    `gorm:"size:200;not null" json:"upstreamModel"`
	Priority                   int       `gorm:"not null;default:0" json:"priority"`
	Weight                     int       `gorm:"not null;default:100" json:"weight"`
	InputPriceMicros           int64     `gorm:"not null;default:0" json:"inputPriceMicros"`
	OutputPriceMicros          int64     `gorm:"not null;default:0" json:"outputPriceMicros"`
	CachedInputPriceMicros     *int64    `json:"cachedInputPriceMicros"`
	CacheWritePriceMicros      *int64    `json:"cacheWritePriceMicros"`
	PriceMultiplierBasisPoints int64     `gorm:"not null;default:10000" json:"priceMultiplierBasisPoints"`
	Enabled                    bool      `gorm:"not null" json:"enabled"`
	CircuitDisabled            bool      `gorm:"not null;default:false;index" json:"circuitDisabled"`
	CreatedAt                  time.Time `json:"createdAt"`
	UpdatedAt                  time.Time `json:"updatedAt"`
	RecentSuccessRate          float64   `gorm:"-" json:"recentSuccessRate"`
	RecentSuccessCount         int64     `gorm:"-" json:"recentSuccessCount"`
	RecentAttemptCount         int64     `gorm:"-" json:"recentAttemptCount"`
}

type CircuitRecord struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ChannelID      uint64     `gorm:"index;not null" json:"channelId"`
	ChannelModelID uint64     `gorm:"index;not null" json:"channelModelId"`
	ModelID        uint64     `gorm:"index;not null" json:"modelId"`
	ChannelName    string     `gorm:"size:120;not null" json:"channelName"`
	ModelName      string     `gorm:"size:160;not null" json:"modelName"`
	UpstreamModel  string     `gorm:"size:200;not null" json:"upstreamModel"`
	Level          int        `gorm:"index;not null" json:"level"`
	FailureCount   int        `gorm:"not null" json:"failureCount"`
	Immediate      bool       `gorm:"not null;default:false" json:"immediate"`
	Message        string     `gorm:"type:text;not null" json:"message"`
	OpenUntil      *time.Time `json:"openUntil"`
	ResolvedAt     *time.Time `gorm:"index" json:"resolvedAt"`
	Resolution     string     `gorm:"size:40;not null;default:''" json:"resolution"`
	CreatedAt      time.Time  `gorm:"index;not null" json:"createdAt"`
}

type ClientToken struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name           string     `gorm:"size:120;not null" json:"name"`
	KeyHash        string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	KeyPrefix      string     `gorm:"size:24;not null" json:"keyPrefix"`
	Enabled        bool       `gorm:"not null;default:true" json:"enabled"`
	AllowAllModels bool       `gorm:"not null;default:false" json:"allowAllModels"`
	RPM            int        `gorm:"not null;default:60" json:"rpm"`
	MaxConcurrency int        `gorm:"not null;default:10" json:"maxConcurrency"`
	LastUsedAt     *time.Time `json:"lastUsedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type ClientTokenModel struct {
	TokenID uint64 `gorm:"primaryKey"`
	ModelID uint64 `gorm:"primaryKey;index"`
}

type RelayRequestLog struct {
	ID                    string    `gorm:"size:36;primaryKey" json:"id"`
	TokenID               uint64    `gorm:"index;not null" json:"tokenId"`
	TokenName             string    `gorm:"size:120" json:"tokenName"`
	TokenKeyPrefix        string    `gorm:"size:24" json:"tokenKeyPrefix"`
	Endpoint              string    `gorm:"size:40;not null" json:"endpoint"`
	RequestedModel        string    `gorm:"size:200;index;not null" json:"requestedModel"`
	ClientKind            string    `gorm:"size:32;index" json:"clientKind"`
	CodexSessionID        string    `gorm:"size:512;index" json:"codexSessionId"`
	CodexSessionSource    string    `gorm:"size:48;index" json:"codexSessionSource"`
	SessionName           string    `gorm:"size:80;index" json:"sessionName"`
	CodexPromptHash       string    `gorm:"size:64;index" json:"-"`
	CodexTitleRequest     bool      `gorm:"not null;default:false" json:"-"`
	CodexGeneratedTitle   string    `gorm:"size:80" json:"-"`
	IsCompaction          bool      `gorm:"not null;default:false;index" json:"isCompaction"`
	CompactionTrigger     string    `gorm:"size:16;index" json:"-"`
	RequestParametersJSON string    `gorm:"type:text" json:"-"`
	PayloadLogDetail      string    `gorm:"size:16;not null;default:default" json:"payloadLogDetail"`
	RequestBody           string    `gorm:"type:text" json:"requestBody"`
	RequestBodyTruncated  bool      `gorm:"not null;default:false" json:"requestBodyTruncated"`
	ResponseBody          string    `gorm:"type:text" json:"responseBody"`
	ResponseBodyTruncated bool      `gorm:"not null;default:false" json:"responseBodyTruncated"`
	StatusCode            int       `gorm:"index;not null" json:"statusCode"`
	Outcome               string    `gorm:"size:16;index;not null;default:failed" json:"outcome"`
	InputTokens           int64     `gorm:"not null;default:0" json:"inputTokens"`
	NormalInputTokens     int64     `gorm:"not null;default:0" json:"normalInputTokens"`
	OutputTokens          int64     `gorm:"not null;default:0" json:"outputTokens"`
	CachedTokens          int64     `gorm:"not null;default:0" json:"cachedTokens"`
	CacheWriteTokens      int64     `gorm:"not null;default:0" json:"cacheWriteTokens"`
	SentTokens            int64     `gorm:"not null;default:0" json:"sentTokens"`
	EstimatedCost         int64     `gorm:"not null;default:0" json:"estimatedCostMicros"`
	UpstreamCost          int64     `gorm:"not null;default:0" json:"upstreamCostMicros"`
	CostSource            string    `gorm:"size:32" json:"costSource"`
	UsageSource           string    `gorm:"size:32" json:"usageSource"`
	AttemptCount          int       `gorm:"not null;default:0" json:"attemptCount"`
	GatewayPreparationMS  int64     `gorm:"not null;default:0" json:"gatewayPreparationMs"`
	FirstTokenMS          int64     `gorm:"not null;default:0" json:"firstTokenMs"`
	FirstResponseMS       int64     `gorm:"not null;default:0" json:"firstResponseMs"`
	LatencyMS             int64     `gorm:"not null;default:0" json:"latencyMs"`
	DurationMS            int64     `gorm:"not null;default:0" json:"durationMs"`
	Stream                bool      `gorm:"not null;default:false" json:"stream"`
	ErrorCode             string    `gorm:"size:80" json:"errorCode"`
	CreatedAt             time.Time `gorm:"index" json:"createdAt"`
}

func (log *RelayRequestLog) BeforeCreate(_ *gorm.DB) error {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	} else {
		log.CreatedAt = log.CreatedAt.UTC()
	}
	if log.Outcome == "" {
		log.Outcome = relayRequestOutcome(log.StatusCode, log.ErrorCode)
	}
	if log.PayloadLogDetail == "" {
		log.PayloadLogDetail = "default"
	}
	return nil
}

// RelaySessionState keeps the small amount of state needed to name a session
// and remove context already retained by its preceding request logs.
type RelaySessionState struct {
	TokenID              uint64     `gorm:"primaryKey;autoIncrement:false;index:idx_relay_session_client_recent,priority:1" json:"tokenId"`
	SessionID            string     `gorm:"size:512;primaryKey" json:"sessionId"`
	Title                string     `gorm:"size:80;index" json:"title"`
	TitleCustomized      bool       `gorm:"not null;default:false" json:"titleCustomized"`
	ThreadSource         string     `gorm:"size:48;index;index:idx_relay_session_active,priority:1" json:"threadSource"`
	SessionSource        string     `gorm:"size:48;index" json:"sessionSource"`
	ClientKind           string     `gorm:"size:32;index" json:"clientKind"`
	ClientFingerprint    string     `gorm:"size:64;index;index:idx_relay_session_client_recent,priority:2" json:"-"`
	LatestRequestID      string     `gorm:"size:36" json:"latestRequestId"`
	LastActivityAt       *time.Time `gorm:"index:idx_relay_session_active,priority:2,sort:desc" json:"lastActivityAt"`
	CompactionCount      int64      `gorm:"not null;default:0" json:"compactionCount"`
	PrimaryModel         string     `gorm:"size:200;index" json:"primaryModel"`
	ContextWindowTokens  int64      `gorm:"not null;default:0" json:"contextWindowTokens"`
	ContextWindowSource  string     `gorm:"size:24;index" json:"contextWindowSource"`
	ContextWindowSamples int64      `gorm:"not null;default:0" json:"contextWindowSampleCount"`
	ContextSamplesJSON   string     `gorm:"type:text" json:"-"`
	RequestManifestJSON  string     `gorm:"type:text" json:"-"`
	PayloadManifestJSON  string     `gorm:"type:text" json:"-"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `gorm:"index;index:idx_relay_session_client_recent,priority:3,sort:desc" json:"updatedAt"`
}

// ModelAgentContextWindow retains a learned context-window profile after detailed request logs expire.
type ModelAgentContextWindow struct {
	TokenID                   uint64 `gorm:"primaryKey;autoIncrement:false"`
	ClientFingerprint         string `gorm:"size:64;primaryKey"`
	Model                     string `gorm:"size:200;primaryKey"`
	ContextWindowTokens       int64  `gorm:"not null;default:0"`
	CompactionThresholdTokens int64  `gorm:"not null;default:0"`
	SampleCount               int64  `gorm:"not null;default:0"`
	SamplesJSON               string `gorm:"type:text"`
	CreatedAt                 time.Time
	UpdatedAt                 time.Time `gorm:"index"`
}

// RelayChatSessionClaim maps one canonical Chat Completions history to the
// inferred session selected before the upstream request starts.
type RelayChatSessionClaim struct {
	TokenID             uint64    `gorm:"primaryKey;autoIncrement:false;index:idx_relay_claim_client_recent,priority:1"`
	ClientFingerprint   string    `gorm:"size:64;primaryKey;index:idx_relay_claim_client_recent,priority:2"`
	RequestHistoryHash  string    `gorm:"size:64;primaryKey"`
	SessionID           string    `gorm:"size:512;index;not null"`
	RequestManifestJSON string    `gorm:"type:text;not null"`
	CreatedAt           time.Time `gorm:"index"`
	UpdatedAt           time.Time `gorm:"index;index:idx_relay_claim_client_recent,priority:3,sort:desc"`
}

type RelayAttemptLog struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	RequestID      string `gorm:"size:36;index;not null" json:"requestId"`
	ChannelID      uint64 `gorm:"index;not null" json:"channelId"`
	ChannelName    string `gorm:"size:120" json:"channelName"`
	ChannelBaseURL string `gorm:"size:1024" json:"channelBaseUrl"`
	ChannelModelID uint64 `gorm:"not null" json:"channelModelId"`
	UpstreamModel  string `gorm:"size:200;not null" json:"upstreamModel"`
	APIPath        string `gorm:"-" json:"apiPath"`
	// PreviousChannelID identifies the channel used immediately before this selection, or zero for an initial route.
	PreviousChannelID uint64 `gorm:"not null;default:0" json:"previousChannelId"`
	// PreviousChannelName preserves the prior channel name even if that channel is later removed.
	PreviousChannelName string `gorm:"size:120" json:"previousChannelName"`
	// SelectionReason is the stable reason code explaining why this attempt's channel was selected.
	SelectionReason string `gorm:"size:48" json:"selectionReason"`
	// SelectionDetail contains a sanitized, bounded diagnostic detail for SelectionReason.
	SelectionDetail string `gorm:"size:512" json:"selectionDetail"`
	// RouteDecisionJSON preserves the candidate scores and probabilities used for the initial route.
	RouteDecisionJSON     string         `gorm:"type:text" json:"-"`
	RouteDecision         *RouteDecision `gorm:"-" json:"routeDecision,omitempty"`
	PayloadLogDetail      string         `gorm:"size:16;not null;default:default" json:"payloadLogDetail"`
	RequestBody           string         `gorm:"type:text" json:"requestBody"`
	RequestBodyTruncated  bool           `gorm:"not null;default:false" json:"requestBodyTruncated"`
	ResponseBody          string         `gorm:"type:text" json:"responseBody"`
	ResponseBodyTruncated bool           `gorm:"not null;default:false" json:"responseBodyTruncated"`
	StatusCode            int            `gorm:"not null" json:"statusCode"`
	InputTokens           int64          `gorm:"not null;default:0" json:"inputTokens"`
	NormalInputTokens     int64          `gorm:"not null;default:0" json:"normalInputTokens"`
	OutputTokens          int64          `gorm:"not null;default:0" json:"outputTokens"`
	CachedTokens          int64          `gorm:"not null;default:0" json:"cachedTokens"`
	CacheWriteTokens      int64          `gorm:"not null;default:0" json:"cacheWriteTokens"`
	SentTokens            int64          `gorm:"not null;default:0" json:"sentTokens"`
	EstimatedCost         int64          `gorm:"not null;default:0" json:"estimatedCostMicros"`
	UpstreamCost          int64          `gorm:"not null;default:0" json:"upstreamCostMicros"`
	CostSource            string         `gorm:"size:32" json:"costSource"`
	UsageSource           string         `gorm:"size:32" json:"usageSource"`
	FirstTokenMS          int64          `gorm:"not null;default:0" json:"firstTokenMs"`
	FirstResponseMS       int64          `gorm:"not null;default:0" json:"firstResponseMs"`
	LatencyMS             int64          `gorm:"not null;default:0" json:"latencyMs"`
	DurationMS            int64          `gorm:"not null;default:0" json:"durationMs"`
	Success               bool           `gorm:"not null;default:false" json:"success"`
	Outcome               string         `gorm:"size:16;index;not null;default:failed" json:"outcome"`
	ErrorMessage          string         `gorm:"type:text" json:"errorMessage"`
	CreatedAt             time.Time      `gorm:"index" json:"createdAt"`
}

// RelayStepLog is one measured stage in a relayed API request. Durations use
// microseconds so short gateway operations remain useful during optimization.
type RelayStepLog struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	RequestID       string    `gorm:"size:36;index;index:idx_relay_step_request_order,priority:1;not null" json:"requestId"`
	Stage           string    `gorm:"size:64;index;not null" json:"stage"`
	Category        string    `gorm:"size:24;index;not null" json:"category"`
	Attempt         int       `gorm:"not null;default:0" json:"attempt"`
	StartedOffsetUS int64     `gorm:"index:idx_relay_step_request_order,priority:2;not null;default:0" json:"startedOffsetUs"`
	DurationUS      int64     `gorm:"not null;default:0" json:"durationUs"`
	Outcome         string    `gorm:"size:16;index;not null" json:"outcome"`
	Detail          string    `gorm:"size:512" json:"detail"`
	CreatedAt       time.Time `gorm:"index;not null" json:"createdAt"`
}

func (log *RelayAttemptLog) BeforeCreate(_ *gorm.DB) error {
	if log.PayloadLogDetail == "" {
		log.PayloadLogDetail = "default"
	}
	if log.Outcome == "" || log.Outcome == RelayOutcomeFailed && log.Success {
		if log.Success {
			log.Outcome = RelayOutcomeSuccess
		} else {
			log.Outcome = RelayOutcomeFailed
		}
	}
	return nil
}

type TokenDailyStat struct {
	Date              string `gorm:"size:10;primaryKey"`
	TokenID           uint64 `gorm:"primaryKey;autoIncrement:false;index"`
	RequestCount      int64  `gorm:"not null;default:0"`
	SuccessCount      int64  `gorm:"not null;default:0"`
	CanceledCount     int64  `gorm:"not null;default:0"`
	InputTokens       int64  `gorm:"not null;default:0"`
	NormalInputTokens int64  `gorm:"not null;default:0"`
	OutputTokens      int64  `gorm:"not null;default:0"`
	CachedTokens      int64  `gorm:"not null;default:0"`
	CacheWriteTokens  int64  `gorm:"not null;default:0"`
	SentTokens        int64  `gorm:"not null;default:0"`
	EstimatedCost     int64  `gorm:"not null;default:0"`
	UpstreamCost      int64  `gorm:"not null;default:0"`
	FirstTokenMS      int64  `gorm:"not null;default:0"`
	FirstTokenSamples int64  `gorm:"not null;default:0"`
	LatencyMS         int64  `gorm:"not null;default:0"`
	LatencySamples    int64  `gorm:"not null;default:0"`
	DurationMS        int64  `gorm:"not null;default:0"`
	AttemptCount      int64  `gorm:"not null;default:0"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ChannelModelDailyStat struct {
	Date                string `gorm:"size:10;primaryKey"`
	ChannelID           uint64 `gorm:"primaryKey;autoIncrement:false;index"`
	ChannelModelID      uint64 `gorm:"primaryKey;autoIncrement:false;index"`
	RequestCount        int64  `gorm:"not null;default:0"`
	AttemptCount        int64  `gorm:"not null;default:0"`
	SuccessCount        int64  `gorm:"not null;default:0"`
	FailedCount         int64  `gorm:"not null;default:0"`
	CanceledCount       int64  `gorm:"not null;default:0"`
	InputTokens         int64  `gorm:"not null;default:0"`
	NormalInputTokens   int64  `gorm:"not null;default:0"`
	OutputTokens        int64  `gorm:"not null;default:0"`
	CachedTokens        int64  `gorm:"not null;default:0"`
	CacheWriteTokens    int64  `gorm:"not null;default:0"`
	SentTokens          int64  `gorm:"not null;default:0"`
	EstimatedCost       int64  `gorm:"not null;default:0"`
	UpstreamCost        int64  `gorm:"not null;default:0"`
	UpstreamCostCount   int64  `gorm:"not null;default:0"`
	FallbackCostCount   int64  `gorm:"not null;default:0"`
	MixedCostCount      int64  `gorm:"not null;default:0"`
	FailedZeroCostCount int64  `gorm:"not null;default:0"`
	FirstTokenMS        int64  `gorm:"not null;default:0"`
	FirstTokenSamples   int64  `gorm:"not null;default:0"`
	LatencyMS           int64  `gorm:"not null;default:0"`
	LatencySamples      int64  `gorm:"not null;default:0"`
	DurationMS          int64  `gorm:"not null;default:0"`
	DurationSamples     int64  `gorm:"not null;default:0"`
	Status1xxCount      int64  `gorm:"not null;default:0"`
	Status2xxCount      int64  `gorm:"not null;default:0"`
	Status3xxCount      int64  `gorm:"not null;default:0"`
	Status4xxCount      int64  `gorm:"not null;default:0"`
	Status5xxCount      int64  `gorm:"not null;default:0"`
	NoStatusCount       int64  `gorm:"not null;default:0"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type GatewayMigration struct {
	Name      string `gorm:"size:120;primaryKey"`
	AppliedAt time.Time
}

type ResponseAffinity struct {
	ResponseHash   string    `gorm:"size:64;primaryKey"`
	ChannelModelID uint64    `gorm:"index;not null"`
	ExpiresAt      time.Time `gorm:"index"`
	CreatedAt      time.Time
}

type SessionAffinity struct {
	TokenID        uint64    `gorm:"primaryKey;autoIncrement:false"`
	ModelID        uint64    `gorm:"primaryKey;autoIncrement:false"`
	SessionHash    string    `gorm:"size:64;primaryKey"`
	ChannelModelID uint64    `gorm:"index;not null"`
	ExpiresAt      time.Time `gorm:"index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
	CacheWriteTokens int64
	Source           string
}

func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}
