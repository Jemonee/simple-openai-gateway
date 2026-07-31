export type RoutingStrategy = 'priority_weighted' | 'lowest_cost' | 'lowest_latency'
export type CostSource = 'upstream' | 'estimated_fallback' | 'mixed' | 'failed_zero'
export type RelayOutcome = 'success' | 'canceled' | 'failed' | 'processing'
export type PayloadLogDetail = 'default' | 'summary' | 'none'

export interface AdminSession {
  /** Persistent administrator identifier. */
  id: number
  /** Administrator login name displayed in the console. */
  username: string
}

export interface ChannelModel {
  /** Persistent channel-model mapping identifier. */
  id: number
  /** Channel owning this mapping. */
  channelId: number
  /** Public model exposed by the gateway. */
  modelId: number
  /** Model identifier sent to this upstream provider. */
  upstreamModel: string
  /** Higher values are attempted before lower priority groups. */
  priority: number
  /** Relative selection weight inside the same priority group. */
  weight: number
  /** Input price in micro-USD per one million tokens. */
  inputPriceMicros: number
  /** Output price in micro-USD per one million tokens. */
  outputPriceMicros: number
  /** Cached input price in micro-USD per one million tokens, or null to use input price. */
  cachedInputPriceMicros: number | null
  /** Cache-write input price in micro-USD per one million tokens, or null to use input price. */
  cacheWritePriceMicros: number | null
  /** Persisted price multiplier in basis points; 10000 represents 1.00x. */
  priceMultiplierBasisPoints: number
  /** Whether this mapping can receive new requests. */
  enabled: boolean
  /** Whether a level-three circuit event disabled this mapping pending manual reopening. */
  circuitDisabled: boolean

  /** Successes divided by attempts for this channel-model mapping during the last 30 minutes; 1 without samples. */
  recentSuccessRate: number
  /** Successful upstream attempts for this mapping during the last 30 minutes. */
  recentSuccessCount: number
  /** All upstream attempts for this mapping during the last 30 minutes. */
  recentAttemptCount: number
  /** Mapping creation timestamp in RFC 3339 format. */
  createdAt: string
  /** Mapping update timestamp in RFC 3339 format. */
  updatedAt: string
}

export interface ChannelLatencyPoint {
  /** Timestamp of the successful upstream attempt in RFC 3339 format. */
  recordedAt: string
  /** Time to receive the upstream response headers in milliseconds. */
  latencyMs: number
}

export interface ChannelMetrics {
  /** Chronological latency points for the latest 48 successful attempts within five days. */
  latencySeries: ChannelLatencyPoint[]
  /** Most recent successful upstream latency in milliseconds, or zero without a sample. */
  latestLatencyMs: number
  /** Mean time from response headers to the first generated output token across sampled successful streaming attempts. */
  averageFirstTokenMs: number
  /** Successful streaming attempts with a recorded first output token within five days. */
  firstTokenSampleCount: number
  /** Mean time to upstream response headers across sampled successful attempts. */
  averageLatencyMs: number
  /** Total successful attempts with a positive latency sample within five days. */
  latencySampleCount: number
  /** Mean response-body duration after response-header latency is excluded across sampled successful attempts. */
  averageDurationMs: number
  /** Successful attempts with a recorded full-response duration within five days. */
  durationSampleCount: number
  /** Upstream-reported input tokens from successful attempts within five days. */
  inputTokens: number
  /** Upstream-reported cached input tokens included in inputTokens within five days. */
  cachedTokens: number
  /** cachedTokens divided by inputTokens, or zero when no input usage is known. */
  cacheHitRate: number
  /** Successes divided by all channel attempts during the last 30 minutes; 1 without samples. */
  recentSuccessRate: number
  /** Successful attempts across every channel model during the last 30 minutes. */
  recentSuccessCount: number
  /** All attempts across every channel model during the last 30 minutes. */
  recentAttemptCount: number
}

export interface Channel {
  /** Persistent channel identifier. */
  id: number
  /** Administrator-facing provider name. */
  name: string
  /** OpenAI-compatible upstream base URL, normally ending in /v1. */
  baseUrl: string
  /** Whether new requests may be routed to the channel. */
  enabled: boolean
  /** Whether Chat streaming requests may include stream_options.include_usage. */
  supportsStreamUsage: boolean
  /** Channel-wide official-price multiplier in basis points; 10000 represents 1.00x. */
  priceMultiplierBasisPoints: number
  /** Number of consecutive retryable failures. */
  consecutiveFailures: number
  /** Channel circuit level: 0 closed, 1 temporary, 2 extended; 3 is retained only for legacy channel state. */
  circuitLevel: number
  /** Circuit reopening timestamp, or null when the circuit is closed. */
  circuitOpenUntil: string | null
  /** Successful request latency EWMA in milliseconds. */
  latencyEwmaMs: number
  /** Last health observation timestamp. */
  lastHealthAt: string | null
  /** Last retryable upstream error without request content. */
  lastError: string
  /** Whether an encrypted upstream API key is stored. */
  apiKeyConfigured: boolean
  /** Public-to-upstream model mappings configured for the channel. */
  models: ChannelModel[]
  /** Performance, cache, and rolling 30-minute success metrics derived from detailed attempt logs. */
  metrics: ChannelMetrics
  /** Channel creation timestamp in RFC 3339 format. */
  createdAt: string
  /** Channel update timestamp in RFC 3339 format. */
  updatedAt: string
}

export type CircuitResolution = '' | 'automatic_recovery' | 'escalated' | 'manual_reopen' | 'mapping_removed' | 'manual_reset'

export interface CircuitRecord {
  /** Persistent circuit event identifier. */
  id: number
  /** Channel involved when the event occurred. */
  channelId: number
  /** Channel-model mapping that triggered the event. */
  channelModelId: number
  /** Public model identifier captured for the triggering mapping. */
  modelId: number
  /** Channel name snapshot retained after configuration changes. */
  channelName: string
  /** Public model name snapshot retained after configuration changes. */
  modelName: string
  /** Upstream model name snapshot retained after configuration changes. */
  upstreamModel: string
  /** Circuit level opened by this event. */
  level: 1 | 2 | 3
  /** Consecutive failure count observed when the event opened. */
  failureCount: number
  /** Whether an account or credential failure bypassed the normal failure threshold. */
  immediate: boolean
  /** Truncated upstream failure text captured for diagnosis. */
  message: string
  /** Automatic routing exclusion deadline for level one or two, otherwise null. */
  openUntil: string | null
  /** Resolution timestamp, or null while recovery or manual reopening is pending. */
  resolvedAt: string | null
  /** How the event left its pending state. */
  resolution: CircuitResolution
  /** Event creation timestamp in RFC 3339 format. */
  createdAt: string
  /** Whether the original mapping still exists. */
  mappingExists: boolean
  /** Whether the original mapping currently accepts requests. */
  mappingEnabled: boolean
  /** Whether the original mapping is currently disabled by level-three circuit handling. */
  mappingCircuitDisabled: boolean
}

export interface CircuitRecordPage {
  /** Circuit events on the requested page. */
  items: CircuitRecord[]
  /** Number of events matching the active filters. */
  total: number
  /** Global number of level-three mappings still awaiting manual reopening. */
  pendingManual: number
  /** One-based page number returned by the backend. */
  page: number
  /** Maximum number of events returned on this page. */
  pageSize: number
}

export interface ChannelModelDiscoveryRequest {
  /** Existing channel identifier, or zero when testing an unsaved channel. */
  channelId: number
  /** OpenAI-compatible base URL currently entered in the channel form. */
  baseUrl: string
  /** Replacement or unsaved upstream key; blank reuses the stored encrypted key. */
  apiKey: string
}

export interface OfficialModelPrice {
  /** Official regular-input price in micro-USD per one million tokens. */
  inputPriceMicros: number
  /** Official output price in micro-USD per one million tokens. */
  outputPriceMicros: number
  /** Official cached-input price in micro-USD per one million tokens. */
  cachedInputPriceMicros: number | null
  /** Official cache-write price in micro-USD per one million tokens, or null when the catalog shows no price. */
  cacheWritePriceMicros: number | null
  /** Official catalog source URL embedded by this gateway build. */
  source: string
  /** ISO currency code used by every catalog price. */
  currency: 'USD'
  /** Billing unit used by every catalog price. */
  unit: 'per_1m_tokens'
  /** OpenAI processing and context tier represented by this price. */
  contextTier: 'standard_short_context'
  /** Immutable version identifier for the embedded price catalog. */
  catalogVersion: string
  /** Catalog review date in YYYY-MM-DD format. */
  updatedAt: string
}

export interface UpstreamModel {
  /** Model identifier returned by the upstream /models endpoint. */
  id: string
  /** Upstream owner label, or an empty string when omitted by the provider. */
  ownedBy: string
  /** Upstream Unix creation timestamp, or zero when omitted by the provider. */
  created: number
  /** Public model identifier registered for this upstream model. */
  publicModelId: number
  /** Whether the public model was automatically created by this discovery request. */
  publicModelCreated: boolean
  /** Exact-match OpenAI Standard short-context text-token price, or null for an unlisted model. */
  officialPrice: OfficialModelPrice | null
}

export interface ChannelModelDiscovery {
  /** Deduplicated upstream models sorted by model identifier. */
  models: UpstreamModel[]
  /** Time to receive the upstream response in milliseconds. */
  latencyMs: number
  /** HTTP status returned by the upstream models endpoint. */
  status: number
  /** Completion timestamp of this discovery request in RFC 3339 format. */
  fetchedAt: string
}

export interface GatewayModel {
  /** Persistent public model identifier. */
  id: number
  /** Model name accepted by public OpenAI-compatible endpoints. */
  name: string
  /** Candidate ordering strategy used for this model. */
  routingStrategy: RoutingStrategy
  /** Whether the model is listed and accepts requests. */
  enabled: boolean
  /** Total public requests recorded for this model across the whole site. */
  requestCount: number
  /** Model creation timestamp in RFC 3339 format. */
  createdAt: string
  /** Model update timestamp in RFC 3339 format. */
  updatedAt: string
}

export interface ClientToken {
  /** Persistent client token identifier. */
  id: number
  /** Administrator-facing token name. */
  name: string
  /** Redacted prefix used to identify the token after issuance. */
  keyPrefix: string
  /** Whether the token may authenticate public API requests. */
  enabled: boolean
  /** Whether every enabled public model is allowed. */
  allowAllModels: boolean
  /** Maximum accepted requests in each fixed one-minute window. */
  rpm: number
  /** Maximum concurrent in-flight requests. */
  maxConcurrency: number
  /** Last successful authentication timestamp, or null when unused. */
  lastUsedAt: string | null
  /** Explicit model permissions when allowAllModels is false. */
  modelIds: number[]
  /** Long-term daily statistics retained after detailed logs expire. */
  statistics: TokenStatistics
  /** Token creation timestamp in RFC 3339 format. */
  createdAt: string
  /** Token update timestamp in RFC 3339 format. */
  updatedAt: string
}

export interface TokenStatistics {
  /** Total requests recorded for this logical client token. */
  requests: number
  /** Requests completed with a final 2xx status. */
  successes: number
  /** Total reported or estimated input tokens. */
  inputTokens: number
  /** Non-cached input tokens billed at the regular input price. */
  normalInputTokens: number
  /** Total reported or estimated output tokens. */
  outputTokens: number
  /** Cached input tokens included in inputTokens. */
  cachedTokens: number
  /** Cache-write input tokens included in inputTokens. */
  cacheWriteTokens: number
  /** Gateway-local token count of request bodies actually sent upstream, including retries. */
  sentTokens: number
  /** Total estimated cost in micro-USD. */
  estimatedCostMicros: number
  /** Total upstream-reported cost in micro-USD, with estimate fallback when the upstream omits cost. */
  upstreamCostMicros: number
  /** Mean time from final upstream response headers to the first generated output token in milliseconds. */
  averageFirstTokenMs: number
  /** Requests with an observed first output token. */
  firstTokenSampleCount: number
  /** Mean time from gateway ingress to final upstream response headers in milliseconds. */
  averageLatencyMs: number
  /** Requests with an observed final upstream response header. */
  latencySampleCount: number
  /** Mean response-body duration after final response-header latency is excluded in milliseconds. */
  averageDurationMs: number
  /** Requests included in the duration average. */
  durationSampleCount: number
  /** Total upstream attempts across requests. */
  attempts: number
}

export interface IssuedClientToken {
  /** Persisted token metadata. */
  token: ClientToken
  /** One-time plaintext sk- token that cannot be retrieved later. */
  secret: string
}

export type CodexProviderMode = 'existing' | 'new'
export type CodexTokenMode = 'existing' | 'new'

export interface CodexProviderConfiguration {
  /** Stable model provider key used by Codex configuration. */
  name: string
  /** Provider display name stored inside its configuration table. */
  displayName: string
  /** OpenAI-compatible Responses API base URL. */
  baseUrl: string
  /** Codex wire protocol configured for this provider. */
  wireApi: string
  /** Whether Codex reads OPENAI_API_KEY from auth.json for this provider. */
  requiresOpenAIAuth: boolean
}

export interface CodexLocalConfiguration {
  /** Absolute path of the current OS user's Codex config.toml. */
  configPath: string
  /** Absolute path of the current OS user's Codex auth.json. */
  authPath: string
  /** Whether config.toml existed when the snapshot was loaded. */
  configExists: boolean
  /** Whether auth.json existed when the snapshot was loaded. */
  authExists: boolean
  /** Provider selected by the top-level model_provider setting. */
  modelProvider: string
  /** Model selected by the top-level model setting. */
  model: string
  /** Provider tables currently present in config.toml. */
  providers: CodexProviderConfiguration[]
  /** Whether auth.json contains an OPENAI_API_KEY value. */
  authConfigured: boolean
  /** Redacted prefix of the OPENAI_API_KEY value, or blank when absent. */
  authKeyPrefix: string
  /** Gateway token matching the auth.json key hash, or zero when no match exists. */
  authTokenId: number
}

export interface CodexConfigurationSaveRequest {
  /** Whether the save updates an existing provider table or creates a new one. */
  providerMode: CodexProviderMode
  /** Provider table key selected or entered by the administrator. */
  providerName: string
  /** Enabled public model Codex should request. */
  model: string
  /** OpenAI-compatible gateway base URL written to the provider table. */
  baseUrl: string
  /** Whether auth.json reuses its current matching token or receives a newly issued token. */
  tokenMode: CodexTokenMode
  /** Existing gateway token ID when tokenMode is existing. */
  tokenId: number
  /** Name assigned to the newly issued gateway token when tokenMode is new. */
  newTokenName: string
}

export interface DashboardDaily {
  /** UTC+8 calendar date in YYYY-MM-DD format. */
  date: string
  /** Requests received on the date. */
  requests: number
  /** Requests that reached a protocol-level successful completion. */
  successes: number
  /** Requests canceled by downstream clients on the date. */
  canceledCount: number
  /** Input tokens reported or estimated on the date. */
  inputTokens: number
  /** Output tokens reported or estimated on the date. */
  outputTokens: number
  /** Estimated cost in micro-USD. */
  estimatedCostMicros: number
  /** Upstream cost in micro-USD, with estimate fallback when the upstream omits cost. */
  upstreamCostMicros: number
  /** Mean time from response headers to the first generated output token for sampled requests on the date. */
  averageFirstTokenMs: number
  /** Requests with an observed first output token on the date. */
  firstTokenSampleCount: number
  /** Mean time to final upstream response headers for sampled requests on the date. */
  averageLatencyMs: number
  /** Requests with an observed final upstream response header on the date. */
  latencySampleCount: number
  /** Mean response-body duration after response-header latency is excluded on the date. */
  averageDurationMs: number
  /** Requests included in the duration average on the date. */
  durationSampleCount: number
}

export interface DashboardHourly {
  /** UTC+8 hour bucket in RFC 3339 format. */
  hour: string
  /** Requests received during the hour. */
  requests: number
  /** Requests that reached a protocol-level successful completion during the hour. */
  successes: number
}

export interface DashboardCostRatio {
  /** Effective upstream-to-official cost ratio, rounded to two decimals. */
  ratio: number
  /** Requests in the selected range that used this effective ratio. */
  requests: number
  /** Share of requests with a calculable official-price baseline represented by this ratio. */
  share: number
}

export interface DashboardBreakdown {
  /** Channel or public model label. */
  name: string
  /** Request-level records represented by this row; retries never add another count. */
  requests: number
  /** Requests that completed successfully within this breakdown row. */
  successes: number
  /** Requests canceled by downstream clients within this breakdown row. */
  canceledCount: number
  /** Successful requests divided by completed non-canceled requests. */
  successRate: number
  /** Input tokens attributed to this channel or public model. */
  inputTokens: number
  /** Cached input tokens attributed to this channel or public model. */
  cachedTokens: number
  /** Cached input divided by total input for this channel or public model. */
  cacheHitRate: number
  /** Output tokens attributed to this channel or public model. */
  outputTokens: number
  /** Estimated cost in micro-USD. */
  estimatedCostMicros: number
  /** Upstream cost in micro-USD, with estimate fallback when the upstream omits cost. */
  upstreamCostMicros: number
}

export interface DashboardSummary {
  /** Total requests in the selected natural-day range. */
  requests: number
  /** Requests canceled by downstream clients in the selected range. */
  canceledCount: number
  /** Successful requests divided by completed success/failure requests; cancellations are excluded. */
  successRate: number
  /** Total input tokens in the selected natural-day range. */
  inputTokens: number
  /** Total output tokens in the selected natural-day range. */
  outputTokens: number
  /** Non-cached input tokens in the selected range. */
  normalInputTokens: number
  /** Cached input tokens in the selected range. */
  cachedTokens: number
  /** Cache-write input tokens in the selected range. */
  cacheWriteTokens: number
  /** Cached input divided by total input, or zero without usage data. */
  cacheHitRate: number
  /** Total estimated cost in micro-USD. */
  estimatedCostMicros: number
  /** Primary total cost in micro-USD, reported by upstream or estimated when absent. */
  upstreamCostMicros: number
  /** Cost calculated from the embedded official OpenAI catalog for the selected usage. */
  officialCostMicros: number
  /** Estimated cost divided by official catalog cost, or zero without a catalog match. */
  estimatedCostRatio: number
  /** Upstream cost divided by official catalog cost, or zero without a catalog match. */
  upstreamCostRatio: number
  /** Mean time from response headers to the first generated output token in milliseconds. */
  averageFirstTokenMs: number
  /** Requests with an observed first output token. */
  firstTokenSampleCount: number
  /** Mean time from gateway ingress to final upstream response headers in milliseconds. */
  averageLatencyMs: number
  /** Requests with an observed final upstream response header. */
  latencySampleCount: number
  /** Mean response-body duration after response-header latency is excluded in milliseconds. */
  averageDurationMs: number
  /** Requests included in the duration average. */
  durationSampleCount: number
  /** Daily metrics for each date in the selected range, including zero-value dates. */
  daily: DashboardDaily[]
  /** Hourly request metrics from the start of the selected range through the current UTC+8 hour. */
  hourly: DashboardHourly[]
  /** Five most-used effective upstream cost ratios in the selected range. */
  costRatios: DashboardCostRatio[]
  /** Highest-usage channel breakdown from detailed logs in the selected range. */
  channels: DashboardBreakdown[]
  /** Highest-usage public model breakdown from detailed logs in the selected range. */
  models: DashboardBreakdown[]
}

export type AttemptSelectionReason =
  | 'initial_route'
  | 'response_affinity'
  | 'session_affinity'
  | 'model_switch'
  | 'channel_disabled'
  | 'mapping_disabled'
  | 'circuit_open'
  | 'affinity_target_missing'
  | 'retryable_status'
  | 'transport_error'
  | 'response_error'
  | 'upstream_application_error'
  | 'gateway_preparation_error'
  | 'circuit_opened'
  | ''

export interface RouteDecisionCandidate {
  /** Candidate channel identifier at decision time. */
  channelId: number
  /** Candidate channel name captured at decision time. */
  channelName: string
  /** Candidate channel-model mapping identifier. */
  channelModelId: number
  /** Upstream model name for this candidate. */
  upstreamModel: string
  /** Administrator-configured routing priority. */
  priority: number
  /** Administrator-configured base routing weight. */
  weight: number
  /** Cache-adjusted estimated request cost in micro-USD. */
  expectedCostMicros: number
  /** Recent successful attempts divided by recent completed attempts. */
  successRate: number
  /** Recent mean response-header latency in milliseconds. */
  latencyMs: number
  /** Recent mean time from response headers to the first generated output token in milliseconds. */
  firstTokenMs: number
  /** Recent aggregate generated output tokens per second after the first token. */
  tokensPerSecond: number
  /** Normalized price advantage score used by this decision. */
  priceScore: number
  /** Composite first-token, response-latency, and throughput score used by this decision. */
  efficiencyScore: number
  /** Composite recent success and cache-quality score used by this decision. */
  qualityScore: number
  /** Base target share before recent-route balancing is applied. */
  targetRouteShare: number
  /** Confidence-weighted recent-route correction multiplier. */
  balanceMultiplier: number
  /** Recent calls with cached input divided by calls with reported input usage. */
  cacheHitRate: number
  /** Recent calls with reported input usage used for cache-hit calculation. */
  cacheSampleCount: number
  /** Recent cached input tokens divided by input tokens. */
  cacheRate: number
  /** Recent reported input tokens used for cache-rate calculation. */
  cacheTokenCount: number
  /** Calls routed to this mapping within the latest site-wide routing sample. */
  recentRouteCount: number
  /** Share of the latest site-wide routing sample routed to this mapping. */
  recentRouteShare: number
  /** Actual number of calls available in the latest routing sample, up to 100. */
  routeSampleSize: number
  /** Composite expectation score before normalization. */
  expectation: number
  /** Normalized probability used for random selection. */
  probability: number
  /** Whether the random draw selected this candidate. */
  selected: boolean
}

export interface RouteDecisionWeights {
  /** Price contribution applied to the composite route score, from zero to one. */
  price: number
  /** Efficiency contribution applied to the composite route score, from zero to one. */
  efficiency: number
  /** Success and cache-quality contribution, from zero to one. */
  quality: number
  /** Share of the final probability reserved for recent-route balancing. */
  balance: number
}

export interface RouteDecision {
  /** Model routing strategy active for this decision. */
  strategy: RoutingStrategy
  /** Probability draw or deterministic affinity mode. */
  mode: 'probability' | 'session_affinity' | 'response_affinity'
  /** Runtime scoring proportions captured with this decision. */
  weights: RouteDecisionWeights
  /** Eligible candidates and the exact inputs used by the decision. */
  candidates: RouteDecisionCandidate[]
}

export interface RelayAttemptLog {
  /** Persistent attempt identifier. */
  id: number
  /** Parent public request identifier. */
  requestId: string
  /** Selected channel identifier. */
  channelId: number
  /** Channel name captured when the attempt was made. */
  channelName: string
  /** OpenAI-compatible channel base URL captured when the attempt was made. */
  channelBaseUrl: string
  /** Selected channel-model mapping identifier. */
  channelModelId: number
  /** Model identifier sent upstream. */
  upstreamModel: string
  /** Actual upstream API path, relative from and including /v1. */
  apiPath: string
  /** Channel identifier used immediately before this selection, or zero for an initial route. */
  previousChannelId: number
  /** Historical name of the channel used immediately before this selection. */
  previousChannelName: string
  /** Stable reason code describing why this channel was selected; blank on legacy rows. */
  selectionReason: AttemptSelectionReason
  /** Sanitized, bounded diagnostic detail for the selection reason. */
  selectionDetail: string
  /** Explainable snapshot of the initial route decision; absent on legacy and retry attempts. */
  routeDecision?: RouteDecision
  /** Payload retention detail captured when this public request entered the gateway. */
  payloadLogDetail: PayloadLogDetail
  /** Transformed request body or a gateway delta envelope relative to the public request. */
  requestBody: string
  /** Whether requestBody was truncated at the four MiB retention limit. */
  requestBodyTruncated: boolean
  /** Full upstream response body retained for this attempt, including SSE events. */
  responseBody: string
  /** Whether responseBody was truncated at the four MiB retention limit. */
  responseBodyTruncated: boolean
  /** Upstream HTTP status, or zero for a transport error. */
  statusCode: number
  /** Business outcome independent of the upstream HTTP status. */
  outcome: RelayOutcome
  /** Input tokens charged or estimated for this attempt. */
  inputTokens: number
  /** Non-cached input tokens charged or estimated for this attempt. */
  normalInputTokens: number
  /** Output tokens charged or estimated for this attempt. */
  outputTokens: number
  /** Cached input tokens within inputTokens. */
  cachedTokens: number
  /** Cache-write input tokens within inputTokens. */
  cacheWriteTokens: number
  /** Gateway-local token count of the request body sent for this network attempt. */
  sentTokens: number
  /** Attempt estimated cost in micro-USD. */
  estimatedCostMicros: number
  /** Attempt upstream cost in micro-USD, falling back to an estimate for successful or canceled attempts. */
  upstreamCostMicros: number
  /** Monetary origin; abnormal failed attempts are always failed_zero. */
  costSource: CostSource
  /** upstream, estimated_tiktoken, mixed, or empty when unknown. */
  usageSource: string
  /** Time from upstream response headers to the first generated output token, or zero without a sample. */
  firstTokenMs: number
  /** Time to upstream response headers in milliseconds. */
  latencyMs: number
  /** Time from upstream response headers until its response body ended. */
  durationMs: number
  /** Compatibility success flag; outcome carries the three-state result. */
  success: boolean
  /** Sanitized transport or status failure detail. */
  errorMessage: string
  /** Attempt creation timestamp in RFC 3339 format. */
  createdAt: string
}

export interface RelayRequestLog {
  /** Public request UUID also returned as X-Request-Id. */
  id: string
  /** Client token identifier used by the request. */
  tokenId: number
  /** Client token name captured when the request was received. */
  tokenName: string
  /** Redacted client token prefix captured when the request was received. */
  tokenKeyPrefix: string
  /** chat or responses public endpoint family. */
  endpoint: string
  /** Public API path, relative from and including /v1. */
  apiPath: string
  /** Requested reasoning effort, or an empty string when the client used the default. */
  reasoningEffort: string
  /** Public model requested by the client. */
  requestedModel: string
  /** Detected client family, such as codex or copilot. */
  clientKind: string
  /** Codex client session identifier when one could be extracted. */
  codexSessionId: string
  /** Payload field used to identify the Codex session, or unavailable. */
  codexSessionSource: string
  /** Latest real user text from this request, normalized and limited to ten Unicode characters. */
  sessionName: string
  /** Whether Codex classified this request as a context-compaction request. */
  isCompaction: boolean
  /** Allowlisted non-content API parameters retained for five-day diagnostics. */
  requestParameters: Record<string, unknown>
  /** Payload retention detail captured when this request entered the gateway. */
  payloadLogDetail: PayloadLogDetail
  /** Original request body or a gateway delta envelope with already-retained context omitted. */
  requestBody: string
  /** Whether requestBody was truncated at the four MiB retention limit. */
  requestBodyTruncated: boolean
  /** Final response body retained for the request, including SSE events. */
  responseBody: string
  /** Whether responseBody was truncated at the four MiB retention limit. */
  responseBodyTruncated: boolean
  /** Final HTTP status returned to the client. */
  statusCode: number
  /** Business outcome independent of the final HTTP status. */
  outcome: RelayOutcome
  /** Total input tokens across known attempts. */
  inputTokens: number
  /** Total non-cached input tokens across known attempts. */
  normalInputTokens: number
  /** Total output tokens across known attempts. */
  outputTokens: number
  /** Total cached input tokens across known attempts. */
  cachedTokens: number
  /** Total cache-write input tokens across known attempts. */
  cacheWriteTokens: number
  /** Gateway-local token count of request bodies actually sent upstream, including retries. */
  sentTokens: number
  /** Total estimated cost in micro-USD across known attempts. */
  estimatedCostMicros: number
  /** Total upstream cost in micro-USD across billable successful or canceled attempts, with estimate fallback. */
  upstreamCostMicros: number
  /** Aggregate monetary origin across billable attempts, or failed_zero for an abnormal failed request. */
  costSource: CostSource
  /** upstream, estimated_tiktoken, mixed, or empty when unknown. */
  usageSource: string
  /** Number of upstream attempts made. */
  attemptCount: number
  /** Time spent in gateway session resolution, routing, payload transformation, and request setup before the first upstream network call. */
  gatewayPreparationMs: number
  /** Time from final upstream response headers to the first generated output token, or zero without a sample. */
  firstTokenMs: number
  /** Time from gateway ingress to the final upstream response headers, or zero without a sample. */
  latencyMs: number
  /** Time from final upstream response headers until the response body ended. */
  durationMs: number
  /** Whether the client requested an SSE response. */
  stream: boolean
  /** Stable gateway or upstream error code. */
  errorCode: string
  /** Request creation timestamp in RFC 3339 format. */
  createdAt: string
  /** Ordered upstream attempts for this request. */
  attempts: RelayAttemptLog[]
  /** Fine-grained processing stages ordered by their offset from gateway ingress. */
  steps: RelayStepLog[]
}

export type RelayStepCategory = 'gateway' | 'upstream' | 'downstream' | 'storage'

export interface RelayStepLog {
  /** Persistent stage-row identifier. */
  id: number
  /** Public request UUID shared by all stages. */
  requestId: string
  /** Stable processing-stage code. */
  stage: string
  /** System boundary in which the stage ran. */
  category: RelayStepCategory
  /** One-based upstream attempt number, or zero for request-wide work. */
  attempt: number
  /** Stage start offset from gateway ingress in microseconds. */
  startedOffsetUs: number
  /** Measured stage duration in microseconds. */
  durationUs: number
  /** Stage-level completion outcome. */
  outcome: RelayOutcome
  /** Sanitized stage metadata without request content or credentials. */
  detail: string
  /** Stage start timestamp in RFC 3339 format. */
  createdAt: string
}

export interface LogAggregateSummary {
  /** Matching request count across the full filtered time range. */
  requestCount: number
  /** Matching requests that reached a protocol-level successful completion. */
  successCount: number
  /** Matching requests canceled by the downstream client before protocol completion. */
  canceledCount: number
  /** Successful matching requests divided by success/failure requests; cancellations are excluded. */
  successRate: number
  /** Total upstream attempts made by matching requests. */
  attemptCount: number
  /** Total input tokens across matching requests. */
  inputTokens: number
  /** Total non-cached input tokens across matching requests. */
  normalInputTokens: number
  /** Total output tokens across matching requests. */
  outputTokens: number
  /** Total cached input tokens across matching requests. */
  cachedTokens: number
  /** Total cache-write input tokens across matching requests. */
  cacheWriteTokens: number
  /** Gateway-local tokens sent upstream, including retries. */
  sentTokens: number
  /** Estimated cost for matching requests in micro-USD. */
  estimatedCostMicros: number
  /** Upstream cost for matching requests in micro-USD, with estimate fallback. */
  upstreamCostMicros: number
  /** Mean time from final upstream response headers to the first generated output token in milliseconds. */
  averageFirstTokenMs: number
  /** Matching requests with an observed first output token. */
  firstTokenSampleCount: number
  /** Mean time to final upstream response headers in milliseconds. */
  averageLatencyMs: number
  /** Matching requests with an observed final upstream response header. */
  latencySampleCount: number
  /** Mean response-body duration after final response-header latency is excluded in milliseconds. */
  averageDurationMs: number
  /** Matching requests included in the duration average. */
  durationSampleCount: number
}

export interface LogPage {
  /** Request logs for the selected page. */
  items: RelayRequestLog[]
  /** Aggregate metrics across every request matching the active filters. */
  summary: LogAggregateSummary
  /** Total matching request count. */
  total: number
  /** One-based page number. */
  page: number
  /** Maximum rows returned in this page. */
  pageSize: number
}

export interface LogPayloadCleanupResult {
  /** Exclusive UTC cutoff used to select request logs for cleanup. */
  cutoffAt: string
  /** Number of request-log rows whose parameter or payload fields were cleared. */
  requestLogsCleared: number
  /** Number of upstream-attempt rows whose payload fields were cleared. */
  attemptLogsCleared: number
}

export interface LogStorageUsage {
  /** UTC cutoff used by the cleanup action shown alongside this metric. */
  cutoffAt: string
  /** Combined bytes retained in request and attempt payload fields. */
  payloadBytes: number
  /** Bytes retained in request parameters and request/response bodies. */
  requestPayloadBytes: number
  /** Bytes retained in upstream-attempt request/response bodies. */
  attemptPayloadBytes: number
}

export interface SessionChannel {
  /** Persistent channel identifier. */
  channelId: number
  /** Current or historically captured channel name. */
  channelName: string
  /** Current or historically captured OpenAI-compatible base URL. */
  channelBaseUrl: string
  /** Channel-model mapping assigned to the session. */
  channelModelId: number
  /** Model identifier sent to the assigned upstream provider. */
  upstreamModel: string
  /** session_affinity, latest_successful_attempt, or latest_attempt. */
  assignmentSource: string
  /** Whether the live channel remains enabled. */
  enabled: boolean
  /** Whether the live channel-model mapping remains enabled. */
  mappingEnabled: boolean
  /** Circuit reopening timestamp, or null when the circuit is closed. */
  circuitOpenUntil: string | null
  /** Last assignment or attempt timestamp in RFC 3339 format. */
  lastUsedAt: string
  /** Ordered channel handoff records reconstructed from retained attempts. */
  migrationHistory: SessionChannelMigration[]
}

export interface SessionChannelMigration {
  /** Channel that handled the preceding attempt. */
  fromChannelId: number
  /** Historical name of the preceding channel. */
  fromChannelName: string
  /** Channel that became the next handoff target. */
  toChannelId: number
  /** Historical name of the handoff target. */
  toChannelName: string
  /** Stable routing reason code. */
  reason: string
  /** Bounded routing diagnostic detail. */
  detail: string
  /** Request containing the handoff. */
  requestId: string
  /** Time when the handoff attempt was recorded. */
  occurredAt: string
}

export interface CodexSessionSummary {
  /** Extracted Codex session identifier, blank for an unidentified request. */
  sessionId: string
  /** Automatically derived title or administrator-customized session title. */
  sessionName: string
  /** Payload field used to identify the session, or unavailable. */
  sessionSource: string
  /** Detected client family, such as codex or copilot. */
  clientKind: string
  /** Codex thread origin such as user or ambient_suggestions, or unavailable for legacy and generic clients. */
  threadSource: string
  /** Whether multiple requests can be reliably grouped into this session. */
  identified: boolean
  /** Request identifier used as the conservative group key when no session ID exists. */
  fallbackRequestId: string
  /** Client token identifier used by the session. */
  tokenId: number
  /** Client token name captured by the latest request. */
  tokenName: string
  /** Redacted client token prefix captured by the latest request. */
  tokenKeyPrefix: string
  /** Public model used by the latest request. */
  latestModel: string
  /** chat or responses endpoint used by the latest request. */
  latestEndpoint: string
  /** Requests retained for this session within the five-day window. */
  requestCount: number
  /** Context-compaction requests recorded for this session. */
  compactionCount: number
  /** Requests that reached a protocol-level successful completion. */
  successCount: number
  /** Requests canceled by downstream clients before protocol completion. */
  canceledCount: number
  /** Requests accepted by the gateway but not yet finalized. */
  processingCount: number
  /** Successful retained requests divided by success/failure requests; cancellations are excluded. */
  successRate: number
  /** Total upstream attempts made by retained requests. */
  attemptCount: number
  /** Total input tokens across retained requests. */
  inputTokens: number
  /** Total non-cached input tokens across retained requests. */
  normalInputTokens: number
  /** Total output tokens across retained requests. */
  outputTokens: number
  /** Cached input tokens included in inputTokens. */
  cachedTokens: number
  /** Cache-write input tokens included in inputTokens. */
  cacheWriteTokens: number
  /** Gateway-local token count of request bodies actually sent upstream, including retries. */
  sentTokens: number
  /** cachedTokens divided by inputTokens, or zero without input usage. */
  cacheHitRate: number
  /** Total estimated cost in micro-USD. */
  estimatedCostMicros: number
  /** Total upstream cost in micro-USD, with estimate fallback when absent. */
  upstreamCostMicros: number
  /** Mean time from final upstream response headers to the first generated output token in milliseconds. */
  averageFirstTokenMs: number
  /** Retained requests with an observed first output token. */
  firstTokenSampleCount: number
  /** Mean time to final upstream response headers in milliseconds. */
  averageLatencyMs: number
  /** Retained requests with an observed final upstream response header. */
  latencySampleCount: number
  /** Mean response-body duration after final response-header latency is excluded in milliseconds. */
  averageDurationMs: number
  /** Retained requests included in the duration average. */
  durationSampleCount: number
  /** Earliest retained request timestamp in RFC 3339 format. */
  firstSeenAt: string
  /** Latest retained request timestamp in RFC 3339 format. */
  lastSeenAt: string
  /** Current affinity assignment or latest historical channel, when available. */
  currentChannel: SessionChannel | null
}

export interface CodexSessionPage {
  /** Session aggregates for the selected page. */
  items: CodexSessionSummary[]
  /** Request-level aggregate metrics across every session matching the active filters. */
  summary: LogAggregateSummary
  /** Total matching session groups. */
  total: number
  /** One-based page number. */
  page: number
  /** Maximum session groups returned on this page. */
  pageSize: number
}

export interface ActiveSessionPage {
  /** Active user sessions for the selected page, populated with identity and latest-request fields. */
  items: CodexSessionSummary[]
  /** Total user sessions active within the server-defined activity window. */
  total: number
  /** One-based page number. */
  page: number
  /** Maximum active sessions returned on this page. */
  pageSize: number
}

export interface CodexSessionDetail {
  /** Full five-day aggregate for the selected session. */
  summary: CodexSessionSummary
  /** Retained request details for the selected detail page. */
  requests: RelayRequestLog[]
  /** Total retained request count for detail pagination. */
  requestTotal: number
  /** One-based request detail page number. */
  page: number
  /** Maximum request details returned on this page. */
  pageSize: number
}

export interface ApplicationSettings {
  /** HTTP listener configuration. */
  webConfig: {
    /** Address bound by the HTTP server after restart. */
    host: string
    /** TCP port bound by the HTTP server after restart. */
    port: string
  }
  /** Legacy node configuration preserved when settings are saved. */
  nodeConfig: {
    /** Shared node token preserved for backward-compatible deployments. */
    sharedToken: string
  }
  /** Runtime gateway limits and retention configuration. */
  gatewayConfig: {
    /** Maximum upstream channel attempts per public request, capped at three. */
    maxAttempts: number
    /** Maximum accepted public request body size in MiB. */
    requestBodyLimitMB: number
    /** Maximum wait for upstream response headers in seconds. */
    responseHeaderTimeoutSeconds: number
    /** Maximum idle interval between upstream stream reads in seconds. */
    streamIdleTimeoutSeconds: number
    /** Price contribution percentage used for new routing decisions. */
    routingPriceWeightPercent: number
    /** Efficiency contribution percentage used for new routing decisions. */
    routingEfficiencyWeightPercent: number
    /** Success and cache quality contribution percentage used for new routing decisions. */
    routingQualityWeightPercent: number
    /** Recent traffic balance contribution percentage used for new routing decisions. */
    routingBalanceWeightPercent: number
    /** Administrator login session lifetime in hours. */
    sessionTTLHours: number
    /** Whether the administrator cookie is restricted to HTTPS. */
    secureCookie: boolean
    /** Detail retained for request parameters and responses on newly entering calls. */
    payloadLogDetail: PayloadLogDetail
    /** Upstream model IDs enabled automatically when a new channel is discovered. */
    commonModelNames: string[]
  }
}
