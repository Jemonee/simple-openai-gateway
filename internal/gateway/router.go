package gateway

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrModelNotFound       = errors.New("requested model does not exist or is disabled")
	ErrNoAvailableChannel  = errors.New("no channel is currently available for this model")
	ErrAffinityUnavailable = errors.New("the channel for previous_response_id is unavailable")
)

type RouteCandidate struct {
	Channel               Channel
	Mapping               ChannelModel
	Cost                  int64
	RecentSuccessRate     float64
	RecentSuccessCount    int64
	RecentAttemptCount    int64
	RecentLatencyMS       float64
	RecentFirstTokenMS    float64
	RecentTokensPerSecond float64
	RecentCacheHitRate    float64
	RecentCacheSamples    int64
	RecentCacheRate       float64
	RecentCacheTokens     int64
	RecentRouteCount      int64
	RecentRouteShare      float64
	RouteSampleSize       int64
	MetricsLoaded         bool
}

type RouteDecisionCandidate struct {
	ChannelID          uint64  `json:"channelId"`
	ChannelName        string  `json:"channelName"`
	ChannelModelID     uint64  `json:"channelModelId"`
	UpstreamModel      string  `json:"upstreamModel"`
	Priority           int     `json:"priority"`
	Weight             int     `json:"weight"`
	ExpectedCostMicros int64   `json:"expectedCostMicros"`
	SuccessRate        float64 `json:"successRate"`
	LatencyMS          float64 `json:"latencyMs"`
	FirstTokenMS       float64 `json:"firstTokenMs"`
	TokensPerSecond    float64 `json:"tokensPerSecond"`
	PriceScore         float64 `json:"priceScore"`
	EfficiencyScore    float64 `json:"efficiencyScore"`
	QualityScore       float64 `json:"qualityScore"`
	TargetRouteShare   float64 `json:"targetRouteShare"`
	BalanceMultiplier  float64 `json:"balanceMultiplier"`
	CacheHitRate       float64 `json:"cacheHitRate"`
	CacheSampleCount   int64   `json:"cacheSampleCount"`
	CacheRate          float64 `json:"cacheRate"`
	CacheTokenCount    int64   `json:"cacheTokenCount"`
	RecentRouteCount   int64   `json:"recentRouteCount"`
	RecentRouteShare   float64 `json:"recentRouteShare"`
	RouteSampleSize    int64   `json:"routeSampleSize"`
	Expectation        float64 `json:"expectation"`
	Probability        float64 `json:"probability"`
	Selected           bool    `json:"selected"`
}

type RouteDecision struct {
	Strategy   string                   `json:"strategy"`
	Mode       string                   `json:"mode"`
	Weights    RouteDecisionWeights     `json:"weights"`
	Candidates []RouteDecisionCandidate `json:"candidates"`
}

type RouteDecisionWeights struct {
	Price      float64 `json:"price"`
	Efficiency float64 `json:"efficiency"`
	Quality    float64 `json:"quality"`
	Balance    float64 `json:"balance"`
}

type RoutePlan struct {
	Model                    GatewayModel
	Candidates               []RouteCandidate
	InitialSelection         RouteSelection
	Affinity                 bool
	SessionAffinity          bool
	SessionAffinityMappingID uint64
	RefreshSessionAffinity   bool
}

type RouteSelection struct {
	PreviousChannelID   uint64
	PreviousChannelName string
	Reason              string
	Detail              string
	Decision            *RouteDecision
}

type weightedPriorityKey struct {
	ModelID  uint64
	Priority int
}

type weightedPriorityState struct {
	Weights map[uint64]int64
	Current map[uint64]int64
}

type Router struct {
	store          *Store
	access         *ClientAccessService
	configProvider func() *config.ApplicationConfig
	random         func(int) int
	weightedMu     sync.Mutex
	weightedStates map[weightedPriorityKey]*weightedPriorityState
}

func NewRouter(store *Store, access *ClientAccessService, configManager *config.ApplicationConfigManager) *Router {
	router := &Router{
		store:          store,
		access:         access,
		random:         secureIntn,
		weightedStates: make(map[weightedPriorityKey]*weightedPriorityState),
	}
	if configManager != nil {
		router.configProvider = configManager.GetConfig
	}
	return router
}

func secureIntn(limit int) int {
	if limit <= 1 {
		return 0
	}
	var raw [8]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return int(time.Now().UnixNano() % int64(limit))
	}
	return int(binary.LittleEndian.Uint64(raw[:]) % uint64(limit))
}

func (r *Router) Plan(ctx context.Context, token *ClientToken, modelName string, inputTokens int64, declaredOutput int64, previousResponseID string, sessionKey string) (*RoutePlan, error) {
	var model GatewayModel
	if err := r.store.db.WithContext(ctx).Where("name = ? AND enabled = ?", modelName, true).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModelNotFound
		}
		return nil, err
	}
	if err := r.access.AuthorizeModel(ctx, token, model.ID); err != nil {
		return nil, err
	}
	previousSessionRoute, err := r.previousSessionRoute(ctx, token.ID, sessionKey)
	if err != nil {
		return nil, err
	}
	outputTokens := declaredOutput
	if outputTokens <= 0 {
		outputTokens = r.recentOutputMedian(ctx, model.Name)
	}
	var candidates []RouteCandidate
	modelSwitched := previousSessionRoute != nil &&
		previousSessionRoute.Successful &&
		previousSessionRoute.RequestedModel != "" &&
		previousSessionRoute.RequestedModel != model.Name
	if modelSwitched {
		candidates, err = r.availableCandidates(ctx, model.ID, model.RoutingStrategy, inputTokens, outputTokens)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			return nil, ErrNoAvailableChannel
		}
		r.orderCandidatesWithoutWeightedAdvance(model.RoutingStrategy, candidates)
		if sessionKey != "" {
			affinity, affinityErr := r.sessionAffinity(ctx, token.ID, model.ID, sessionKey)
			if affinityErr != nil {
				return nil, affinityErr
			}
			if affinity != nil && pinCandidate(candidates, affinity.ChannelModelID) {
				return &RoutePlan{
					Model:                    model,
					Candidates:               candidates,
					SessionAffinity:          true,
					SessionAffinityMappingID: affinity.ChannelModelID,
					InitialSelection: RouteSelection{
						Reason:   SelectionReasonSessionAffinity,
						Detail:   previousSessionRoute.RequestedModel + " -> " + model.Name,
						Decision: deterministicRouteDecision(model.RoutingStrategy, "session_affinity", candidates),
					},
				}, nil
			}
		}
		if pinChannelCandidate(candidates, previousSessionRoute.ChannelID) {
			return &RoutePlan{
				Model:                  model,
				Candidates:             candidates,
				SessionAffinity:        true,
				RefreshSessionAffinity: true,
				InitialSelection: RouteSelection{
					PreviousChannelID:   previousSessionRoute.ChannelID,
					PreviousChannelName: previousSessionRoute.ChannelName,
					Reason:              SelectionReasonModelSwitch,
					Detail:              previousSessionRoute.RequestedModel + " -> " + model.Name,
					Decision:            deterministicRouteDecision(model.RoutingStrategy, "session_affinity", candidates),
				},
			}, nil
		}
	}

	if previousResponseID != "" {
		candidate, err := r.affinityCandidate(ctx, model.ID, previousResponseID, inputTokens, outputTokens)
		if err != nil {
			return nil, err
		}
		plan := &RoutePlan{
			Model:            model,
			Candidates:       []RouteCandidate{*candidate},
			InitialSelection: RouteSelection{Reason: SelectionReasonResponseAffinity, Decision: deterministicRouteDecision(model.RoutingStrategy, "response_affinity", []RouteCandidate{*candidate})},
			Affinity:         true,
		}
		annotateModelSwitch(plan, previousSessionRoute)
		return plan, nil
	}

	if candidates == nil {
		candidates, err = r.availableCandidates(ctx, model.ID, model.RoutingStrategy, inputTokens, outputTokens)
		if err != nil {
			return nil, err
		}
	}
	if len(candidates) == 0 {
		return nil, ErrNoAvailableChannel
	}
	initialSelection := RouteSelection{Reason: SelectionReasonInitialRoute}
	if sessionKey != "" {
		affinity, affinityErr := r.sessionAffinity(ctx, token.ID, model.ID, sessionKey)
		if affinityErr != nil {
			return nil, affinityErr
		}
		if affinity != nil {
			r.orderCandidatesWithoutWeightedAdvance(model.RoutingStrategy, candidates)
			if pinCandidate(candidates, affinity.ChannelModelID) {
				initialSelection = RouteSelection{Reason: SelectionReasonSessionAffinity, Decision: deterministicRouteDecision(model.RoutingStrategy, "session_affinity", candidates)}
			} else {
				decision := r.orderCandidates(model.RoutingStrategy, candidates)
				initialSelection = r.unavailableSessionSelection(ctx, token.ID, model, sessionKey, affinity.ChannelModelID)
				initialSelection.Decision = decision
			}
			plan := &RoutePlan{
				Model:                    model,
				Candidates:               candidates,
				InitialSelection:         initialSelection,
				SessionAffinity:          true,
				SessionAffinityMappingID: affinity.ChannelModelID,
			}
			annotateModelSwitch(plan, previousSessionRoute)
			return plan, nil
		}
	}
	decision := r.orderCandidates(model.RoutingStrategy, candidates)
	initialSelection.Decision = decision
	plan := &RoutePlan{Model: model, Candidates: candidates, InitialSelection: initialSelection}
	annotateModelSwitch(plan, previousSessionRoute)
	return plan, nil
}

type sessionRouteSnapshot struct {
	RequestedModel string
	ChannelID      uint64
	ChannelName    string
	Successful     bool
}

func (r *Router) previousSessionRoute(ctx context.Context, tokenID uint64, sessionKey string) (*sessionRouteSnapshot, error) {
	if tokenID == 0 || sessionKey == "" {
		return nil, nil
	}
	var snapshot sessionRouteSnapshot
	db := r.store.db.WithContext(ctx)
	err := db.Table("relay_session_states AS state").
		Select("request.requested_model, attempt.channel_id, attempt.channel_name, attempt.success AS successful").
		Joins("JOIN relay_request_logs AS request ON request.id = state.latest_request_id").
		Joins("JOIN relay_attempt_logs AS attempt ON attempt.request_id = request.id").
		Where("state.token_id = ? AND state.session_id = ?", tokenID, sessionKey).
		Order("attempt.created_at DESC, attempt.id DESC").
		Take(&snapshot).Error
	if err == nil {
		return &snapshot, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Legacy state and route-stage failures may not point at a request with attempts.
	err = db.Table("relay_attempt_logs AS attempt").
		Select("request.requested_model, attempt.channel_id, attempt.channel_name, attempt.success AS successful").
		Joins("JOIN relay_request_logs AS request ON request.id = attempt.request_id").
		Where("request.token_id = ? AND request.codex_session_id = ?", tokenID, sessionKey).
		Order("request.created_at DESC, attempt.created_at DESC, attempt.id DESC").
		Take(&snapshot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func annotateModelSwitch(plan *RoutePlan, previous *sessionRouteSnapshot) {
	if plan == nil || previous == nil || previous.RequestedModel == "" || previous.RequestedModel == plan.Model.Name || len(plan.Candidates) == 0 {
		return
	}
	plan.InitialSelection.PreviousChannelID = previous.ChannelID
	plan.InitialSelection.PreviousChannelName = previous.ChannelName
	plan.InitialSelection.Reason = SelectionReasonModelSwitch
	plan.InitialSelection.Detail = previous.RequestedModel + " -> " + plan.Model.Name
}

func (r *Router) unavailableSessionSelection(ctx context.Context, tokenID uint64, model GatewayModel, sessionKey string, channelModelID uint64) RouteSelection {
	selection := RouteSelection{Reason: SelectionReasonAffinityTargetMissing}
	var mapping ChannelModel
	if err := r.store.db.WithContext(ctx).First(&mapping, channelModelID).Error; err == nil && mapping.ModelID == model.ID {
		selection.PreviousChannelID = mapping.ChannelID
		var channel Channel
		if err := r.store.db.WithContext(ctx).First(&channel, mapping.ChannelID).Error; err == nil {
			selection.PreviousChannelName = channel.Name
			switch {
			case !channel.Enabled:
				selection.Reason = SelectionReasonChannelDisabled
			case !mapping.Enabled:
				selection.Reason = SelectionReasonMappingDisabled
			case channel.CircuitOpenUntil != nil && channel.CircuitOpenUntil.After(time.Now()):
				selection.Reason = SelectionReasonCircuitOpen
				selection.Detail = channel.CircuitOpenUntil.UTC().Format(time.RFC3339)
			}
		}
	}
	if selection.PreviousChannelName == "" {
		var attempt RelayAttemptLog
		err := r.store.db.WithContext(ctx).Table("relay_attempt_logs AS a").
			Select("a.*").
			Joins("JOIN relay_request_logs AS request ON request.id = a.request_id").
			Where("request.token_id = ? AND request.requested_model = ? AND request.codex_session_id = ? AND a.channel_model_id = ?", tokenID, model.Name, sessionKey, channelModelID).
			Order("a.created_at DESC, a.id DESC").First(&attempt).Error
		if err == nil {
			selection.PreviousChannelID = attempt.ChannelID
			selection.PreviousChannelName = attempt.ChannelName
		}
	}
	return selection
}

func (r *Router) sessionAffinity(ctx context.Context, tokenID uint64, modelID uint64, sessionKey string) (*SessionAffinity, error) {
	var affinity SessionAffinity
	err := r.store.db.WithContext(ctx).Where(
		"token_id = ? AND model_id = ? AND session_hash = ? AND expires_at > ?",
		tokenID, modelID, hashSecret(sessionKey), time.Now(),
	).First(&affinity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &affinity, nil
}

func pinCandidate(candidates []RouteCandidate, channelModelID uint64) bool {
	for index := range candidates {
		if candidates[index].Mapping.ID != channelModelID {
			continue
		}
		if index > 0 {
			candidate := candidates[index]
			copy(candidates[1:index+1], candidates[:index])
			candidates[0] = candidate
		}
		return true
	}
	return false
}

func pinChannelCandidate(candidates []RouteCandidate, channelID uint64) bool {
	if channelID == 0 {
		return false
	}
	for index := range candidates {
		if candidates[index].Channel.ID != channelID {
			continue
		}
		if index > 0 {
			candidate := candidates[index]
			copy(candidates[1:index+1], candidates[:index])
			candidates[0] = candidate
		}
		return true
	}
	return false
}

func (r *Router) availableCandidates(ctx context.Context, modelID uint64, strategy string, inputTokens int64, outputTokens int64) ([]RouteCandidate, error) {
	var mappings []ChannelModel
	if err := r.store.db.WithContext(ctx).Where("model_id = ? AND enabled = ?", modelID, true).Find(&mappings).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	channelIDs := make([]uint64, 0, len(mappings))
	channelModelIDs := make([]uint64, 0, len(mappings))
	seenChannels := make(map[uint64]struct{}, len(mappings))
	for _, mapping := range mappings {
		channelModelIDs = append(channelModelIDs, mapping.ID)
		if _, exists := seenChannels[mapping.ChannelID]; exists {
			continue
		}
		seenChannels[mapping.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, mapping.ChannelID)
	}
	var channels []Channel
	if len(channelIDs) > 0 {
		if err := r.store.db.WithContext(ctx).Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
			return nil, err
		}
	}
	channelsByID := make(map[uint64]Channel, len(channels))
	for _, channel := range channels {
		channelsByID[channel.ID] = channel
	}
	recentSuccess, err := loadRecentSuccessMetrics(ctx, r.store.db, channelIDs, channelModelIDs, now)
	if err != nil {
		return nil, err
	}
	routableMappingIDs := make([]uint64, 0, len(mappings))
	maxRoutablePriority := 0
	if strategy == RoutingPriorityWeighted {
		for _, mapping := range mappings {
			channel, exists := channelsByID[mapping.ChannelID]
			if exists && channel.Enabled && (channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(now)) {
				maxRoutablePriority = max(maxRoutablePriority, mapping.Priority)
			}
		}
	}
	for _, mapping := range mappings {
		channel, exists := channelsByID[mapping.ChannelID]
		eligiblePriority := strategy != RoutingPriorityWeighted || mapping.Priority == maxRoutablePriority
		if exists && channel.Enabled && eligiblePriority && (channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(now)) {
			routableMappingIDs = append(routableMappingIDs, mapping.ID)
		}
	}
	recentRouting, err := loadRecentRoutingMetrics(ctx, r.store.db, routableMappingIDs, now)
	if err != nil {
		return nil, err
	}
	candidates := make([]RouteCandidate, 0, len(mappings))
	for _, mapping := range mappings {
		channel, exists := channelsByID[mapping.ChannelID]
		if !exists {
			continue
		}
		if !channel.Enabled || (channel.CircuitOpenUntil != nil && channel.CircuitOpenUntil.After(now)) {
			continue
		}
		metric := recentSuccess.ByChannelModel[mapping.ID]
		routingMetric := recentRouting[mapping.ID]
		expectedCachedTokens := int64(math.Round(float64(max(inputTokens, 0)) * routingMetric.CacheRate))
		usage := Usage{InputTokens: inputTokens, OutputTokens: outputTokens, CachedTokens: expectedCachedTokens}
		candidates = append(candidates, RouteCandidate{
			Channel:               channel,
			Mapping:               mapping,
			Cost:                  CalculateCostMicros(mapping, usage),
			RecentSuccessRate:     metric.rate(),
			RecentSuccessCount:    metric.Successes,
			RecentAttemptCount:    metric.Attempts,
			RecentLatencyMS:       routingMetric.LatencyMS,
			RecentFirstTokenMS:    routingMetric.FirstTokenMS,
			RecentTokensPerSecond: routingMetric.TokensPerSecond,
			RecentCacheHitRate:    routingMetric.CacheHitRate,
			RecentCacheSamples:    routingMetric.CacheSampleCount,
			RecentCacheRate:       routingMetric.CacheRate,
			RecentCacheTokens:     routingMetric.CacheTokenCount,
			RecentRouteCount:      routingMetric.RouteCount,
			RecentRouteShare:      routingMetric.RouteShare,
			RouteSampleSize:       routingMetric.RouteSampleSize,
			MetricsLoaded:         true,
		})
	}
	return candidates, nil
}

func (r *Router) affinityCandidate(ctx context.Context, modelID uint64, previousResponseID string, inputTokens int64, outputTokens int64) (*RouteCandidate, error) {
	var affinity ResponseAffinity
	err := r.store.db.WithContext(ctx).Where("response_hash = ? AND expires_at > ?", hashSecret(previousResponseID), time.Now()).First(&affinity).Error
	if err != nil {
		return nil, ErrAffinityUnavailable
	}
	var mapping ChannelModel
	if err := r.store.db.WithContext(ctx).Where("id = ? AND model_id = ? AND enabled = ?", affinity.ChannelModelID, modelID, true).First(&mapping).Error; err != nil {
		return nil, ErrAffinityUnavailable
	}
	var channel Channel
	if err := r.store.db.WithContext(ctx).First(&channel, mapping.ChannelID).Error; err != nil {
		return nil, ErrAffinityUnavailable
	}
	if !channel.Enabled || (channel.CircuitOpenUntil != nil && channel.CircuitOpenUntil.After(time.Now())) {
		return nil, ErrAffinityUnavailable
	}
	usage := Usage{InputTokens: inputTokens, OutputTokens: outputTokens}
	return &RouteCandidate{Channel: channel, Mapping: mapping, Cost: CalculateCostMicros(mapping, usage)}, nil
}

func (r *Router) recentOutputMedian(ctx context.Context, modelName string) int64 {
	var values []int64
	_ = r.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Where("requested_model = ? AND output_tokens > 0 AND status_code BETWEEN 200 AND 299", modelName).
		Order("created_at desc").Limit(31).Pluck("output_tokens", &values).Error
	if len(values) == 0 {
		return 1024
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[len(values)/2]
}

func (r *Router) orderCandidates(strategy string, candidates []RouteCandidate) *RouteDecision {
	if len(candidates) > 0 && candidates[0].MetricsLoaded {
		return r.expectationProbabilityOrder(strategy, candidates)
	}
	switch strategy {
	case RoutingLowestCost:
		sortCandidatesByCost(candidates)
		r.weightedProbabilityOrder(candidates, costProbabilityWeights(candidates))
	case RoutingLowestLatency:
		sortCandidatesByLatency(candidates)
		r.weightedProbabilityOrder(candidates, latencyProbabilityWeights(candidates))
	default:
		r.weightedPriorityOrder(candidates)
	}
	return nil
}

func (r *Router) orderCandidatesWithoutWeightedAdvance(strategy string, candidates []RouteCandidate) {
	if len(candidates) > 0 && candidates[0].MetricsLoaded {
		decision, _ := r.scoredRouteDecision(strategy, candidates)
		expectations := make(map[uint64]float64, len(decision.Candidates))
		for _, candidate := range decision.Candidates {
			expectations[candidate.ChannelModelID] = candidate.Expectation
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return expectations[candidates[i].Mapping.ID] > expectations[candidates[j].Mapping.ID]
		})
		return
	}
	switch strategy {
	case RoutingLowestCost:
		sortCandidatesByCost(candidates)
		sortCandidatesByProbabilityWeight(candidates, costProbabilityWeights(candidates))
	case RoutingLowestLatency:
		sortCandidatesByLatency(candidates)
		sortCandidatesByProbabilityWeight(candidates, latencyProbabilityWeights(candidates))
	default:
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Mapping.Priority != candidates[j].Mapping.Priority {
				return candidates[i].Mapping.Priority > candidates[j].Mapping.Priority
			}
			iWeight := effectivePriorityWeight(candidates[i])
			jWeight := effectivePriorityWeight(candidates[j])
			if iWeight != jWeight {
				return iWeight > jWeight
			}
			return routeCandidateKey(candidates[i]) < routeCandidateKey(candidates[j])
		})
	}
}

func (r *Router) weightedPriorityOrder(candidates []RouteCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Mapping.Priority > candidates[j].Mapping.Priority })
	ordered := make([]RouteCandidate, 0, len(candidates))
	r.weightedMu.Lock()
	defer r.weightedMu.Unlock()
	if r.weightedStates == nil {
		r.weightedStates = make(map[weightedPriorityKey]*weightedPriorityState)
	}
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].Mapping.Priority == candidates[start].Mapping.Priority {
			end++
		}
		group := append([]RouteCandidate(nil), candidates[start:end]...)
		ordered = append(ordered, r.smoothWeightedGroup(group)...)
		start = end
	}
	copy(candidates, ordered)
}

func (r *Router) smoothWeightedGroup(group []RouteCandidate) []RouteCandidate {
	key := weightedPriorityKey{ModelID: group[0].Mapping.ModelID, Priority: group[0].Mapping.Priority}
	state := r.weightedStates[key]
	if !weightedStateMatches(state, group) {
		// Availability or weight changes start a fresh schedule for the active group.
		state = &weightedPriorityState{
			Weights: make(map[uint64]int64, len(group)),
			Current: make(map[uint64]int64, len(group)),
		}
		for _, candidate := range group {
			state.Weights[routeCandidateKey(candidate)] = effectivePriorityWeight(candidate)
		}
		r.weightedStates[key] = state
	}

	// Smooth weighted round-robin adds each weight, selects the largest balance,
	// then charges the selected candidate the group's total weight.
	totalWeight := int64(0)
	selectedIndexes := make([]int, 0, len(group))
	maxCurrent := int64(0)
	for index, candidate := range group {
		candidateKey := routeCandidateKey(candidate)
		weight := state.Weights[candidateKey]
		state.Current[candidateKey] += weight
		totalWeight += weight
		current := state.Current[candidateKey]
		if len(selectedIndexes) == 0 || current > maxCurrent {
			maxCurrent = current
			selectedIndexes = []int{index}
		} else if current == maxCurrent {
			selectedIndexes = append(selectedIndexes, index)
		}
	}
	selectedIndex := selectedIndexes[0]
	if len(selectedIndexes) > 1 && r.random != nil {
		selectedIndex = selectedIndexes[r.random(len(selectedIndexes))]
	}
	selectedKey := routeCandidateKey(group[selectedIndex])
	state.Current[selectedKey] -= totalWeight

	selected := group[selectedIndex]
	remaining := append(group[:selectedIndex:selectedIndex], group[selectedIndex+1:]...)
	sort.SliceStable(remaining, func(i, j int) bool {
		leftKey := routeCandidateKey(remaining[i])
		rightKey := routeCandidateKey(remaining[j])
		if state.Current[leftKey] != state.Current[rightKey] {
			return state.Current[leftKey] > state.Current[rightKey]
		}
		if state.Weights[leftKey] != state.Weights[rightKey] {
			return state.Weights[leftKey] > state.Weights[rightKey]
		}
		return leftKey < rightKey
	})
	return append([]RouteCandidate{selected}, remaining...)
}

func weightedStateMatches(state *weightedPriorityState, group []RouteCandidate) bool {
	if state == nil || len(state.Weights) != len(group) {
		return false
	}
	for _, candidate := range group {
		weight, ok := state.Weights[routeCandidateKey(candidate)]
		if !ok || weight != effectivePriorityWeight(candidate) {
			return false
		}
	}
	return true
}

const routeProbabilityScale int64 = 10_000

func candidateSuccessBasisPoints(candidate RouteCandidate) int64 {
	rate := candidate.RecentSuccessRate
	if candidate.RecentAttemptCount == 0 {
		rate = 1
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		rate = 1
	}
	rate = min(max(rate, 0), 1)
	return max(int64(math.Round(rate*float64(routeProbabilityScale))), 1)
}

func effectivePriorityWeight(candidate RouteCandidate) int64 {
	return int64(max(candidate.Mapping.Weight, 1)) * candidateSuccessBasisPoints(candidate)
}

func sortCandidatesByCost(candidates []RouteCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Cost == candidates[j].Cost {
			return candidates[i].Channel.LatencyEWMA < candidates[j].Channel.LatencyEWMA
		}
		return candidates[i].Cost < candidates[j].Cost
	})
}

func sortCandidatesByLatency(candidates []RouteCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		iUnknown := candidates[i].Channel.LatencyEWMA <= 0
		jUnknown := candidates[j].Channel.LatencyEWMA <= 0
		if iUnknown != jUnknown {
			return iUnknown
		}
		return candidates[i].Channel.LatencyEWMA < candidates[j].Channel.LatencyEWMA
	})
}

func costProbabilityWeights(candidates []RouteCandidate) []int64 {
	weights := make([]int64, len(candidates))
	if len(candidates) == 0 {
		return weights
	}
	minimumCost := max(candidates[0].Cost, 0)
	for index, candidate := range candidates {
		cost := max(candidate.Cost, 0)
		quality := (float64(minimumCost) + 1) / (float64(cost) + 1)
		weights[index] = qualityAdjustedWeight(candidate, quality)
	}
	return weights
}

func latencyProbabilityWeights(candidates []RouteCandidate) []int64 {
	weights := make([]int64, len(candidates))
	bestKnownLatency := float64(0)
	for _, candidate := range candidates {
		latency := candidate.Channel.LatencyEWMA
		if latency > 0 && (bestKnownLatency == 0 || latency < bestKnownLatency) {
			bestKnownLatency = latency
		}
	}
	if bestKnownLatency == 0 {
		bestKnownLatency = 1
	}
	for index, candidate := range candidates {
		latency := candidate.Channel.LatencyEWMA
		if latency <= 0 {
			latency = bestKnownLatency
		}
		weights[index] = qualityAdjustedWeight(candidate, min(bestKnownLatency/latency, 1))
	}
	return weights
}

func qualityAdjustedWeight(candidate RouteCandidate, quality float64) int64 {
	quality = min(max(quality, 0), 1)
	return max(int64(math.Round(quality*float64(candidateSuccessBasisPoints(candidate)))), 1)
}

func (r *Router) weightedProbabilityOrder(candidates []RouteCandidate, weights []int64) {
	if len(candidates) <= 1 || len(weights) != len(candidates) {
		return
	}
	totalWeight := int64(0)
	for _, weight := range weights {
		totalWeight += max(weight, 1)
	}
	if totalWeight <= 1 {
		return
	}
	point := 0
	if r.random != nil {
		point = r.random(int(totalWeight))
	}
	if point < 0 {
		point = -point
	}
	point %= int(totalWeight)
	cumulative := int64(0)
	selectedIndex := 0
	for index, weight := range weights {
		cumulative += max(weight, 1)
		if int64(point) < cumulative {
			selectedIndex = index
			break
		}
	}
	if selectedIndex == 0 {
		return
	}
	selected := candidates[selectedIndex]
	copy(candidates[1:selectedIndex+1], candidates[:selectedIndex])
	candidates[0] = selected
}

func sortCandidatesByProbabilityWeight(candidates []RouteCandidate, weights []int64) {
	if len(weights) != len(candidates) {
		return
	}
	weightsByCandidate := make(map[uint64]int64, len(candidates))
	for index, candidate := range candidates {
		weightsByCandidate[routeCandidateKey(candidate)] = weights[index]
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return weightsByCandidate[routeCandidateKey(candidates[i])] > weightsByCandidate[routeCandidateKey(candidates[j])]
	})
}

func routeCandidateKey(candidate RouteCandidate) uint64 {
	if candidate.Mapping.ID != 0 {
		return candidate.Mapping.ID
	}
	return candidate.Channel.ID
}

func (r *Router) RecordAffinity(ctx context.Context, responseID string, channelModelID uint64) {
	if responseID == "" {
		return
	}
	record := ResponseAffinity{
		ResponseHash:   hashSecret(responseID),
		ChannelModelID: channelModelID,
		ExpiresAt:      time.Now().Add(30 * 24 * time.Hour),
	}
	_ = r.store.db.WithContext(ctx).Save(&record).Error
}

func (r *Router) RecordSessionAffinity(ctx context.Context, tokenID uint64, modelID uint64, sessionKey string, channelModelID uint64) {
	if tokenID == 0 || modelID == 0 || sessionKey == "" || channelModelID == 0 {
		return
	}
	now := time.Now()
	record := SessionAffinity{
		TokenID:        tokenID,
		ModelID:        modelID,
		SessionHash:    hashSecret(sessionKey),
		ChannelModelID: channelModelID,
		ExpiresAt:      now.Add(30 * 24 * time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_ = r.store.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "token_id"}, {Name: "model_id"}, {Name: "session_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"channel_model_id", "expires_at", "updated_at",
		}),
	}).Create(&record).Error
}

func (r *Router) RecordSessionAffinityAfterSuccess(ctx context.Context, tokenID uint64, modelID uint64, sessionKey string, requestStartedAt time.Time, previousChannelModelID uint64, successfulChannelModelID uint64) {
	if tokenID == 0 || modelID == 0 || sessionKey == "" || successfulChannelModelID == 0 {
		return
	}
	var current SessionAffinity
	err := r.store.db.WithContext(ctx).Where("token_id = ? AND model_id = ? AND session_hash = ? AND expires_at > ?", tokenID, modelID, hashSecret(sessionKey), time.Now()).First(&current).Error
	if previousChannelModelID == 0 {
		if err == nil && (requestStartedAt.IsZero() || !requestStartedAt.After(current.UpdatedAt)) {
			return
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return
		}
	} else {
		if err != nil {
			return
		}
		if current.ChannelModelID != previousChannelModelID {
			return
		}
	}
	if previousChannelModelID != 0 && previousChannelModelID != successfulChannelModelID && r.sessionMappingAvailable(ctx, modelID, previousChannelModelID) {
		return
	}
	r.RecordSessionAffinity(ctx, tokenID, modelID, sessionKey, successfulChannelModelID)
}

func (r *Router) sessionMappingAvailable(ctx context.Context, modelID uint64, channelModelID uint64) bool {
	var mapping ChannelModel
	err := r.store.db.WithContext(ctx).Where("id = ? AND model_id = ? AND enabled = ?", channelModelID, modelID, true).First(&mapping).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false
	}
	if err != nil {
		return true
	}
	var channel Channel
	if err := r.store.db.WithContext(ctx).First(&channel, mapping.ChannelID).Error; err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	return channel.Enabled && (channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(time.Now()))
}
