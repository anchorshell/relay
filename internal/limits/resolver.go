package limits

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/observability"
	"github.com/anchorshell/relay/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type EffectiveLimit struct {
	PolicyID             uint             `json:"-"`
	PolicyUUID           string           `json:"policy_id,omitempty"`
	PolicyVersion        int64            `json:"policy_version,omitempty"`
	Metric               models.Metric    `json:"metric"`
	Period               models.Period    `json:"period"`
	Configured           *int64           `json:"configured"`
	Observed             *int64           `json:"observed"`
	Effective            *int64           `json:"effective"`
	ScopeType            models.ScopeType `json:"scope_type"`
	ScopeID              uint             `json:"-"`
	ScopeUUID            string           `json:"scope_id"`
	SourceHeader         string           `json:"source_header,omitempty"`
	Used                 int64            `json:"used"`
	Reserved             int64            `json:"reserved,omitempty"`
	ResetAt              time.Time        `json:"next_available_at,omitzero"`
	ActorScoped          bool             `json:"actor_scoped,omitempty"`
	CapacityKeyNamespace string           `json:"capacity_key_namespace,omitempty"`
	CapacityLabel        string           `json:"capacity_label,omitempty"`
}

type CandidateEvaluation struct {
	EligibleAt      time.Time        `json:"eligible_at"`
	RetryAfter      time.Duration    `json:"retry_after"`
	Reason          string           `json:"reason"`
	EffectiveLimits []EffectiveLimit `json:"effective_limits"`
}

type Resolver struct {
	store                     *store.Store
	tracker                   *Tracker
	useObservedLimits         bool
	usePersistedEndpointState bool
	reserveEstimatedTokens    atomic.Bool
	reserveEstimatedSpend     atomic.Bool
}

func NewResolver(st *store.Store, tracker *Tracker) *Resolver {
	r := &Resolver{
		store:                     st,
		tracker:                   tracker,
		useObservedLimits:         true,
		usePersistedEndpointState: true,
	}
	r.reserveEstimatedTokens.Store(true)
	r.reserveEstimatedSpend.Store(true)
	return r
}

func (r *Resolver) SetObservedLimitsEnabled(enabled bool) {
	r.useObservedLimits = enabled
}

func (r *Resolver) SetPersistedEndpointStateEnabled(enabled bool) {
	r.usePersistedEndpointState = enabled
}

func (r *Resolver) LoadSettings(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	r.SetEstimatedUsageReservations(
		r.store.GetSettingBool(ctx, store.SettingReserveEstimatedTokensForLimits, true),
		r.store.GetSettingBool(ctx, store.SettingReserveEstimatedSpendForLimits, true),
	)
}

func (r *Resolver) SetEstimatedUsageReservations(tokens, spend bool) {
	r.reserveEstimatedTokens.Store(tokens)
	r.reserveEstimatedSpend.Store(spend)
}

func (r *Resolver) ReserveEstimatedTokensForLimits() bool {
	return r == nil || r.reserveEstimatedTokens.Load()
}

func (r *Resolver) ReserveEstimatedSpendForLimits() bool {
	return r == nil || r.reserveEstimatedSpend.Load()
}

func (r *Resolver) Evaluate(ctx context.Context, scopes []ScopeRef, endpoint models.Endpoint, laneID uint, estimatedTokens, estimatedSpend int64, now time.Time) (CandidateEvaluation, error) {
	return r.evaluate(ctx, scopes, endpoint, laneID, estimatedTokens, estimatedSpend, now, r.usePersistedEndpointState, "", false)
}

func (r *Resolver) EvaluateIgnoringEndpointState(ctx context.Context, scopes []ScopeRef, endpoint models.Endpoint, laneID uint, estimatedTokens, estimatedSpend int64, now time.Time) (CandidateEvaluation, error) {
	return r.evaluate(ctx, scopes, endpoint, laneID, estimatedTokens, estimatedSpend, now, false, "", false)
}

func (r *Resolver) EvaluateExcludingTaskReservations(ctx context.Context, scopes []ScopeRef, endpoint models.Endpoint, laneID uint, estimatedTokens, estimatedSpend int64, now time.Time, excludedTaskID string) (CandidateEvaluation, error) {
	return r.evaluate(ctx, scopes, endpoint, laneID, estimatedTokens, estimatedSpend, now, r.usePersistedEndpointState, excludedTaskID, true)
}

func (r *Resolver) EvaluateIgnoringEndpointStateExcludingTaskReservations(ctx context.Context, scopes []ScopeRef, endpoint models.Endpoint, laneID uint, estimatedTokens, estimatedSpend int64, now time.Time, excludedTaskID string) (CandidateEvaluation, error) {
	return r.evaluate(ctx, scopes, endpoint, laneID, estimatedTokens, estimatedSpend, now, false, excludedTaskID, true)
}

func (r *Resolver) evaluate(ctx context.Context, scopes []ScopeRef, endpoint models.Endpoint, laneID uint, estimatedTokens, estimatedSpend int64, now time.Time, useEndpointState bool, excludedTaskID string, currentWindowOnly bool) (CandidateEvaluation, error) {
	ctx, span := observability.Tracer().Start(ctx, "relay.limit_state.admission")
	defer span.End()
	if useEndpointState {
		endpoint = r.mergePersistedEndpointState(ctx, endpoint)
	} else {
		endpoint.HealthStatus = models.HealthHealthy
		endpoint.CooldownUntil = nil
	}
	eval := CandidateEvaluation{EligibleAt: now}
	if endpoint.CooldownUntil != nil && endpoint.CooldownUntil.After(eval.EligibleAt) {
		eval.EligibleAt = endpoint.CooldownUntil.UTC()
		eval.Reason = "endpoint cooldown"
	}

	needed := map[models.Metric]int64{
		models.MetricRequests:    1,
		models.MetricTokens:      r.metricNeed(models.MetricTokens, estimatedTokens),
		models.MetricSpend:       r.metricNeed(models.MetricSpend, estimatedSpend),
		models.MetricConcurrency: 1,
	}
	resolvedScopes := r.evaluationScopes(ctx, scopes, endpoint, laneID)
	storeScopes := make([]store.LimitPolicyScope, 0, len(resolvedScopes))
	for _, scope := range resolvedScopes {
		storeScopes = append(storeScopes, store.LimitPolicyScope{ScopeType: scope.Type, ScopeID: scope.ID})
	}
	configuredLimits, observedLimits, err := r.store.FindLimitsForScopes(ctx, storeScopes)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "policy resolution failed")
		return CandidateEvaluation{}, err
	}
	metricSet := make(map[models.Metric]struct{}, len(configuredLimits))
	for _, policy := range configuredLimits {
		metricSet[policy.Metric] = struct{}{}
	}
	span.SetAttributes(
		attribute.Int("anchorshell.relay.limit_state.policy_count", len(configuredLimits)),
		attribute.Int("anchorshell.relay.limit_state.metric_count", len(metricSet)),
		attribute.Int("anchorshell.relay.limit_state.database_round_trips", 2),
	)
	configuredByScope := make(map[ScopeRef][]models.LimitPolicy, len(resolvedScopes))
	for _, policy := range configuredLimits {
		key := ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}
		configuredByScope[key] = append(configuredByScope[key], policy)
	}
	observedByScope := make(map[ScopeRef][]models.ObservedLimit, len(resolvedScopes))
	for _, observed := range observedLimits {
		key := ScopeRef{Type: observed.ScopeType, ID: observed.ScopeID}
		observedByScope[key] = append(observedByScope[key], observed)
	}

	var blockingScope models.ScopeType
	for _, scope := range resolvedScopes {
		scopeType := scope.Type
		scopeID := scope.ID
		configured := configuredByScope[ScopeRef{Type: scopeType, ID: scopeID}]
		observed := observedByScope[ScopeRef{Type: scopeType, ID: scopeID}]
		for _, metric := range []models.Metric{models.MetricRequests, models.MetricTokens, models.MetricSpend, models.MetricConcurrency} {
			for _, period := range []models.Period{models.PeriodSecond, models.PeriodMinute, models.PeriodHour, models.PeriodDay, models.PeriodMonth} {
				configuredPolicy := pickConfiguredPolicy(configured, metric, period)
				var cfgVal *int64
				if configuredPolicy != nil {
					value := configuredPolicy.LimitValue
					cfgVal = &value
				}
				var obsVal *int64
				var sourceHeader string
				if r.useObservedLimits {
					obsVal, sourceHeader = pickObserved(observed, metric, period, now)
				}
				effective := minPtr(cfgVal, obsVal)
				used := int64(0)
				if effective != nil {
					used = r.tracker.UsedExcludingTask(ScopeRef{Type: scopeType, ID: scopeID}, metric, period, now, excludedTaskID)
				}
				if effective == nil {
					continue
				}
				entry := EffectiveLimit{
					PolicyID:      policyID(configuredPolicy),
					PolicyUUID:    policyUUID(configuredPolicy),
					PolicyVersion: policyVersion(configuredPolicy),
					Metric:        metric,
					Period:        period,
					Configured:    cfgVal,
					Observed:      obsVal,
					Effective:     effective,
					ScopeType:     scopeType,
					ScopeID:       scopeID,
					ScopeUUID:     scope.Key,
					SourceHeader:  sourceHeader,
					Used:          used,
				}
				pacingEnabled := true
				if scopeType == models.ScopeEndpoint && metric == models.MetricRequests {
					pacingEnabled = endpoint.PacingEnabled()
				}
				next := r.tracker.NextEligibleAtWithPacingExcludingTask(ScopeRef{Type: scopeType, ID: scopeID}, metric, period, *effective, needed[metric], now, pacingEnabled, excludedTaskID)
				if currentWindowOnly {
					next = r.tracker.NextEligibleAtWithPacingCurrentWindowExcludingTask(ScopeRef{Type: scopeType, ID: scopeID}, metric, period, *effective, needed[metric], now, pacingEnabled, excludedTaskID)
				}
				if next.After(now) {
					entry.ResetAt = next.UTC()
				}
				eval.EffectiveLimits = append(eval.EffectiveLimits, entry)
				if next.After(eval.EligibleAt) {
					eval.EligibleAt = next
					eval.Reason = string(metric) + "/" + string(period)
					blockingScope = scopeType
				}
			}
		}
	}
	eval.RetryAfter = eval.EligibleAt.Sub(now)
	span.SetAttributes(attribute.Bool("anchorshell.relay.limit_state.allowed", !eval.EligibleAt.After(now)))
	if blockingScope != "" {
		span.SetAttributes(attribute.String("anchorshell.relay.limit_state.blocking_scope", string(blockingScope)))
	}
	return eval, nil
}

func (r *Resolver) evaluationScopes(ctx context.Context, scopes []ScopeRef, endpoint models.Endpoint, laneID uint) []ScopeRef {
	if len(scopes) == 0 {
		scopes = []ScopeRef{
			{Type: models.ScopeGlobal, ID: 0},
			{Type: models.ScopeProvider, ID: endpoint.ProviderID},
			{Type: models.ScopeEndpoint, ID: endpoint.ID},
		}
		if laneID != 0 {
			scopes = append(scopes, ScopeRef{Type: models.ScopeLane, ID: laneID})
		}
	}

	resolved := make([]ScopeRef, 0, len(scopes))
	seen := make(map[ScopeRef]bool, len(scopes))
	for _, scope := range scopes {
		identity := ScopeRef{Type: scope.Type, ID: scope.ID}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		scope.Key = r.scopeUUID(ctx, scope, endpoint)
		resolved = append(resolved, scope)
	}
	return resolved
}

func (r *Resolver) scopeUUID(ctx context.Context, scope ScopeRef, endpoint models.Endpoint) string {
	if scope.Key != "" {
		return scope.Key
	}
	switch scope.Type {
	case models.ScopeGlobal:
		return "global"
	case models.ScopeProvider:
		if scope.ID == endpoint.ProviderID && endpoint.ProviderUUID != "" {
			return endpoint.ProviderUUID
		}
	case models.ScopeEndpoint:
		if scope.ID == endpoint.ID && endpoint.UUID != "" {
			return endpoint.UUID
		}
	}
	if r.store != nil && scope.ID != 0 {
		if publicUUID, err := r.store.ScopeUUIDByID(ctx, scope.Type, scope.ID); err == nil {
			return publicUUID
		}
	}
	return ""
}

func (r *Resolver) metricNeed(metric models.Metric, estimate int64) int64 {
	switch metric {
	case models.MetricTokens:
		if r.ReserveEstimatedTokensForLimits() {
			return estimate
		}
		return 1
	case models.MetricSpend:
		if r.ReserveEstimatedSpendForLimits() {
			return estimate
		}
		return 1
	default:
		return estimate
	}
}

func (r *Resolver) mergePersistedEndpointState(ctx context.Context, endpoint models.Endpoint) models.Endpoint {
	if r.store == nil || endpoint.ID == 0 {
		return endpoint
	}
	var persisted models.Endpoint
	if err := r.store.FindByID(ctx, &persisted, endpoint.ID); err != nil {
		return endpoint
	}
	if persisted.CooldownUntil != nil && (endpoint.CooldownUntil == nil || persisted.CooldownUntil.After(*endpoint.CooldownUntil)) {
		endpoint.CooldownUntil = persisted.CooldownUntil
	}
	if persisted.HealthStatus != "" {
		endpoint.HealthStatus = persisted.HealthStatus
	}
	return endpoint
}

func pickConfiguredPolicy(items []models.LimitPolicy, metric models.Metric, period models.Period) *models.LimitPolicy {
	for _, item := range items {
		if item.Metric == metric && item.Period == period && item.Enabled {
			policy := item
			return &policy
		}
	}
	return nil
}

func pickConfigured(items []models.LimitPolicy, metric models.Metric, period models.Period) *int64 {
	policy := pickConfiguredPolicy(items, metric, period)
	if policy == nil {
		return nil
	}
	value := policy.LimitValue
	return &value
}

func policyID(policy *models.LimitPolicy) uint {
	if policy == nil {
		return 0
	}
	return policy.ID
}

func policyUUID(policy *models.LimitPolicy) string {
	if policy == nil {
		return ""
	}
	return policy.UUID
}

func policyVersion(policy *models.LimitPolicy) int64 {
	if policy == nil {
		return 0
	}
	return policy.UpdatedAt.UTC().UnixNano()
}

func pickObserved(items []models.ObservedLimit, metric models.Metric, period models.Period, now time.Time) (*int64, string) {
	var best *int64
	var source string
	for _, item := range items {
		if item.Metric != metric || item.Period != period || !item.Enabled {
			continue
		}
		if item.ExpiresAt != nil && item.ExpiresAt.Before(now) {
			continue
		}
		if best == nil || item.ObservedValue < *best {
			v := item.ObservedValue
			best = &v
			source = item.SourceHeader
		}
	}
	return best, source
}

func minPtr(a, b *int64) *int64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *a <= *b {
		return a
	}
	return b
}
