package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/observability"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
)

var ErrCapacityContextUnavailable = errors.New("capacity context unavailable")

const (
	externalRowsStatusOK                 = "ok"
	externalRowsStatusContextUnavailable = "context_unavailable"
	externalRowsStatusError              = "error"
	capacityVisibilityHeader             = "X-Relay-Capacity-Visibility"
)

type CapacitySnapshot struct {
	GeneratedAt        time.Time                `json:"generated_at"`
	Sequence           uint64                   `json:"sequence"`
	ExternalRowsStatus string                   `json:"external_rows_status,omitempty"`
	Queue              scheduler.Snapshot       `json:"queue"`
	QueueItems         []scheduler.TaskSnapshot `json:"queue_items"`
	Models             []ModelCapacitySnapshot  `json:"models"`
}

type ModelCapacitySnapshot struct {
	EndpointID         string                `json:"endpoint_id"`
	ProviderID         string                `json:"provider_id"`
	CapacityState      string                `json:"capacity_state"`
	HealthStatus       models.HealthStatus   `json:"health_status"`
	CooldownUntil      *time.Time            `json:"cooldown_until,omitempty"`
	CooldownReason     string                `json:"cooldown_reason,omitempty"`
	CooldownStatusCode int                   `json:"cooldown_status_code,omitempty"`
	LimitRows          []ExternalCapacityRow `json:"limit_rows"`
}

type capacitySnapshotOptions struct {
	Preview bool
}

func (a *API) capacitySnapshot(c *echo.Context) error {
	queueItems, err := a.scopedQueueItems(c)
	if err != nil {
		return err
	}
	snapshot, err := a.buildCapacitySnapshot(c.Request().Context(), capacityRequestHeaders(c), a.scheduler, queueItems)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, snapshot)
}

func (a *API) capacitySnapshotProducer(sch *scheduler.Scheduler) func(c *echo.Context) ([]telemetry.Event, error) {
	return a.capacitySnapshotProducerWithOptions(sch, capacitySnapshotOptions{})
}

func (a *API) capacitySnapshotProducerWithOptions(sch *scheduler.Scheduler, opts capacitySnapshotOptions) func(c *echo.Context) ([]telemetry.Event, error) {
	return func(c *echo.Context) ([]telemetry.Event, error) {
		queueItems, err := a.scopedQueueItemsForScheduler(c, sch)
		if err != nil {
			return nil, err
		}
		snapshot, err := a.buildCapacitySnapshotWithOptions(c.Request().Context(), capacityRequestHeaders(c), sch, queueItems, opts)
		if err != nil {
			return nil, err
		}
		return []telemetry.Event{{
			Type:      "realtime_snapshot",
			Timestamp: snapshot.GeneratedAt,
			Payload:   realtimeSnapshotPayload(snapshot),
		}}, nil
	}
}

func capacityRequestHeaders(c *echo.Context) http.Header {
	headers := c.Request().Header.Clone()
	visibility := strings.ToLower(strings.TrimSpace(c.QueryParam("limit_visibility")))
	if visibility != "organization" {
		visibility = "mine"
	}
	headers.Set(capacityVisibilityHeader, visibility)
	return headers
}

func (a *API) buildCapacitySnapshot(ctx context.Context, headers http.Header, sch *scheduler.Scheduler, queueItems []scheduler.TaskSnapshot) (CapacitySnapshot, error) {
	return a.buildCapacitySnapshotWithOptions(ctx, headers, sch, queueItems, capacitySnapshotOptions{})
}

func (a *API) buildCapacitySnapshotWithOptions(ctx context.Context, headers http.Header, sch *scheduler.Scheduler, queueItems []scheduler.TaskSnapshot, opts capacitySnapshotOptions) (CapacitySnapshot, error) {
	ctx, span := observability.Tracer().Start(ctx, "relay.capacity_snapshot")
	defer span.End()
	now := time.Now().UTC()
	providers, err := a.store.ListProviders(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}
	endpoints, err := a.store.ListEndpoints(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}
	lanes, err := a.store.ListLanes(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}
	memberships, err := a.store.ListLaneMemberships(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}
	policies, err := a.store.ListLimitPolicies(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}
	observed, err := a.store.ListObservedLimits(ctx)
	if err != nil {
		return CapacitySnapshot{}, err
	}

	policyIDs := make([]uint, 0, len(policies))
	for _, policy := range policies {
		if policy.Enabled && policy.ID != 0 {
			policyIDs = append(policyIDs, policy.ID)
		}
	}
	var runtimeStates []models.LimitPolicyState
	if sch != nil {
		runtimeStates = sch.CurrentLimitPolicyStatesForContext(ctx, policies, now)
	}
	var persistedStates []models.LimitPolicyState
	if !opts.Preview {
		persistedStates, err = a.store.ListLimitPolicyStates(ctx, policyIDs)
		if err != nil {
			return CapacitySnapshot{}, err
		}
	}
	states := mergeLimitPolicyStates(runtimeStates, persistedStates)
	span.SetAttributes(
		attribute.Int("anchorshell.relay.capacity.policy_count", len(policies)),
		attribute.Int("anchorshell.relay.capacity.state_count", len(states)),
		attribute.Bool("anchorshell.relay.capacity.preview", opts.Preview),
	)
	providerByID := make(map[uint]models.Provider, len(providers))
	for _, provider := range providers {
		providerByID[provider.ID] = provider
	}
	lanesForEndpoint := capacityLanesForEndpoint(lanes, memberships)

	externalRows, externalRowsStatus := a.externalCapacityRows(ctx, headers, now, sch, opts.Preview)
	if sch != nil {
		externalRows = markUserCapacityBlocks(externalRows, sch.Items(), now)
	}
	if queueItems == nil {
		queueItems = []scheduler.TaskSnapshot{}
	}

	snapshot := CapacitySnapshot{
		GeneratedAt:        now,
		Sequence:           a.capacitySequence.Add(1),
		ExternalRowsStatus: externalRowsStatus,
		QueueItems:         queueItems,
		Models:             make([]ModelCapacitySnapshot, 0, len(endpoints)),
	}
	snapshot.Queue = snapshotFromQueueItems(queueItems)
	for _, endpoint := range endpoints {
		health := endpoint.HealthStatus
		cooldownUntil := endpoint.CooldownUntil
		cooldownReason := endpoint.CooldownReason
		cooldownStatusCode := endpoint.CooldownStatusCode
		if sch != nil {
			derivedHealth, derivedUntil, err := sch.DeriveEndpointHealth(ctx, endpoint, lanesForEndpoint[endpoint.ID], now)
			if err != nil {
				return CapacitySnapshot{}, err
			}
			health = derivedHealth
			cooldownUntil = derivedUntil
		}
		rows := capacityRowsForEndpoint(endpoint, providerByID[endpoint.ProviderID], policies, observed, states, now)
		rows = append(rows, externalRowsForEndpoint(endpoint, lanesForEndpoint[endpoint.ID], externalRows)...)
		cooldownUntil = capacityCooldownUntil(cooldownUntil, rows, now)
		state := capacityState(endpoint, health, cooldownUntil, rows, now)
		if cooldownReason == "" ||
			(health != models.HealthCoolingDown && health != models.HealthRateLimited && health != models.HealthUnhealthy) ||
			cooldownUntil == nil || !cooldownUntil.After(now) ||
			!sameOptionalTime(endpoint.CooldownUntil, cooldownUntil) {
			cooldownReason = ""
			cooldownStatusCode = 0
		}
		snapshot.Models = append(snapshot.Models, ModelCapacitySnapshot{
			EndpointID:         endpoint.UUID,
			ProviderID:         endpoint.ProviderUUID,
			CapacityState:      state,
			HealthStatus:       health,
			CooldownUntil:      cooldownUntil,
			CooldownReason:     cooldownReason,
			CooldownStatusCode: cooldownStatusCode,
			LimitRows:          rows,
		})
	}
	return snapshot, nil
}

func capacitySnapshotPayload(snapshot CapacitySnapshot) map[string]any {
	var payload map[string]any
	body, err := json.Marshal(snapshot)
	if err != nil {
		return map[string]any{}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]any{}
	}
	return payload
}

func (a *API) externalCapacityRows(ctx context.Context, headers http.Header, now time.Time, sch *scheduler.Scheduler, preview bool) ([]ExternalCapacityRow, string) {
	if len(a.externalCapacityHooks) == 0 {
		return nil, externalRowsStatusOK
	}
	runtimeUsed := func(scope CapacityScope, metric string, period string) int64 {
		if sch == nil {
			return 0
		}
		metricValue := models.Metric(strings.TrimSpace(metric))
		periodValue := models.Period(strings.TrimSpace(period))
		if metricValue == "" || periodValue == "" {
			return 0
		}
		return sch.CurrentRuntimeUsedForContext(ctx, limits.ScopeRef{
			Type: models.ScopeType(strings.TrimSpace(scope.Type)),
			Key:  strings.TrimSpace(scope.ID),
		}, metricValue, periodValue, now)
	}
	input := ExternalCapacityRowsInput{
		Headers:     headers.Clone(),
		Now:         now,
		RuntimeUsed: runtimeUsed,
		Preview:     preview,
	}
	var rows []ExternalCapacityRow
	status := externalRowsStatusOK
	for _, hook := range a.externalCapacityHooks {
		if hook == nil {
			continue
		}
		next, err := hook(ctx, input)
		if err != nil {
			if errors.Is(err, ErrCapacityContextUnavailable) {
				if status == externalRowsStatusOK {
					status = externalRowsStatusContextUnavailable
				}
				continue
			}
			status = externalRowsStatusError
			continue
		}
		rows = append(rows, next...)
	}
	return rows, status
}

func markUserCapacityBlocks(rows []ExternalCapacityRow, items []scheduler.TaskSnapshot, now time.Time) []ExternalCapacityRow {
	if len(rows) == 0 || len(items) == 0 {
		return rows
	}
	for rowIndex := range rows {
		row := rows[rowIndex]
		if !row.UserScoped || strings.TrimSpace(row.ActorID) == "" {
			continue
		}
		var blockedUntil time.Time
		blockedReason := ""
		for _, item := range items {
			if !userCapacityBlockApplies(row, item, now) {
				continue
			}
			until := item.UserEligible
			if until.IsZero() {
				until = item.PredictedEligible
			}
			if until.IsZero() {
				until = row.ResetAt
			}
			if blockedUntil.IsZero() || until.After(blockedUntil) {
				blockedUntil = until.UTC()
				blockedReason = strings.TrimSpace(item.UserLimitReason)
			}
		}
		if blockedUntil.IsZero() {
			continue
		}
		rows[rowIndex].Blocked = true
		rows[rowIndex].BlockedUntil = blockedUntil
		if blockedReason == "" {
			blockedReason = strings.Join([]string{row.Metric, row.Period}, "/")
		}
		rows[rowIndex].BlockedReason = blockedReason
	}
	return rows
}

func userCapacityBlockApplies(row ExternalCapacityRow, item scheduler.TaskSnapshot, now time.Time) bool {
	if !strings.EqualFold(strings.TrimSpace(item.ActorID), strings.TrimSpace(row.ActorID)) {
		return false
	}
	if !isUserScopedCapacityDefer(item.DeferScope) {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(item.DeferScope), "api_key") &&
		!strings.EqualFold(strings.TrimSpace(item.DeferScopeID), strings.TrimSpace(row.ScopeID)) {
		return false
	}
	if !capacityBlockTargetMatches(row, item) {
		return false
	}
	switch item.State {
	case "completed", "failed", "cancelled", "in_flight":
		return false
	}
	if !userLimitReasonMatches(row, item.UserLimitReason) {
		return false
	}
	until := item.UserEligible
	if until.IsZero() {
		until = item.PredictedEligible
	}
	if until.IsZero() {
		return row.ResetAt.IsZero() || row.ResetAt.After(now)
	}
	return until.After(now)
}

func isUserScopedCapacityDefer(scope string) bool {
	switch strings.TrimSpace(scope) {
	case "user", "user_model", "user_provider", "api_key", "api_key_model", "api_key_provider":
		return true
	default:
		return false
	}
}

func capacityBlockTargetMatches(row ExternalCapacityRow, item scheduler.TaskSnapshot) bool {
	targetType := strings.TrimSpace(row.TargetType)
	targetKey := strings.TrimSpace(row.TargetKey)
	if targetType == "" || targetType == "global" || targetKey == "" {
		return strings.TrimSpace(item.DeferScope) == "user" || strings.TrimSpace(item.DeferScope) == "api_key"
	}
	switch targetType {
	case "model":
		return (strings.TrimSpace(item.DeferScope) == "user_model" || strings.TrimSpace(item.DeferScope) == "api_key_model") &&
			(targetKey == item.EndpointUUID ||
				targetKey == item.EndpointName ||
				targetKey == item.UpstreamModel ||
				targetKey == item.IncomingModel)
	case "provider":
		return (strings.TrimSpace(item.DeferScope) == "user_provider" || strings.TrimSpace(item.DeferScope) == "api_key_provider") && strings.EqualFold(targetKey, item.ProviderUUID)
	default:
		return false
	}
}

func userLimitReasonMatches(row ExternalCapacityRow, reason string) bool {
	reason = strings.TrimSpace(strings.ToLower(reason))
	if reason == "" {
		return true
	}
	return strings.Contains(reason, strings.ToLower(row.Metric)+"/"+strings.ToLower(row.Period))
}

func capacityScopes(providers []models.Provider, endpoints []models.Endpoint, lanes []models.RoutingLane) []limits.ScopeRef {
	scopes := make([]limits.ScopeRef, 0, 1+len(providers)+len(endpoints)+len(lanes))
	scopes = append(scopes, limits.ScopeRef{Type: models.ScopeGlobal, ID: 0})
	for _, provider := range providers {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeProvider, ID: provider.ID})
	}
	for _, endpoint := range endpoints {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID})
	}
	for _, lane := range lanes {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeLane, ID: lane.ID})
	}
	return scopes
}

func mergeLimitPolicyStates(runtimeStates, persistedStates []models.LimitPolicyState) map[uint]models.LimitPolicyState {
	out := make(map[uint]models.LimitPolicyState, len(runtimeStates)+len(persistedStates))
	for _, state := range persistedStates {
		if state.PolicyID != 0 {
			out[state.PolicyID] = state
		}
	}
	// An active runtime has been hydrated from request_logs and therefore owns
	// the exact rolling-window view. Prefer it even when its value is lower than
	// an older persisted snapshot whose events have since expired.
	for _, state := range runtimeStates {
		if state.PolicyID != 0 {
			out[state.PolicyID] = state
		}
	}
	return out
}

func capacityLanesForEndpoint(lanes []models.RoutingLane, memberships []models.LaneMembership) map[uint][]models.RoutingLane {
	out := make(map[uint][]models.RoutingLane)
	lanesByID := make(map[uint]models.RoutingLane, len(lanes))
	for _, lane := range lanes {
		lanesByID[lane.ID] = lane
	}
	for _, membership := range memberships {
		lane, ok := lanesByID[membership.LaneID]
		if !ok {
			continue
		}
		out[membership.EndpointID] = append(out[membership.EndpointID], lane)
	}
	return out
}

func capacityRowsForEndpoint(endpoint models.Endpoint, provider models.Provider, policies []models.LimitPolicy, observed []models.ObservedLimit, states map[uint]models.LimitPolicyState, now time.Time) []ExternalCapacityRow {
	rows := make([]ExternalCapacityRow, 0)
	for _, metric := range []models.Metric{models.MetricRequests, models.MetricTokens, models.MetricSpend} {
		for _, period := range []models.Period{models.PeriodSecond, models.PeriodMinute, models.PeriodHour, models.PeriodDay, models.PeriodMonth} {
			if metric == models.MetricSpend && period == models.PeriodSecond {
				continue
			}
			policy, ok := capacityConfiguredPolicy(endpoint, provider, policies, metric, period)
			if !ok || policy.LimitValue <= 0 {
				continue
			}
			source := strings.TrimSpace(policy.Source)
			if source == "" {
				source = "configured"
			}
			effective := policy.LimitValue
			observedValue := capacityObservedLimit(observed, policy.ScopeType, policy.ScopeID, metric, period, now)
			if observedValue > 0 && observedValue < effective {
				effective = observedValue
				source = "observed"
			}
			state := states[policy.ID]
			row := capacityRow(policy.ScopeType, policy.ScopeUUID, "", metric, period, policy.LimitValue, effective, state.UsedValue, state.ReservedValue, source, false, now)
			if !state.WindowStart.IsZero() {
				row.WindowStart = state.WindowStart.UTC()
			}
			if state.NextEligibleAt != nil && state.NextEligibleAt.After(now) {
				row.ResetAt = state.NextEligibleAt.UTC()
			}
			if row.Effective > 0 && row.Used+row.Reserved >= row.Effective {
				row.Blocked = true
				row.BlockedUntil = row.ResetAt.UTC()
				row.BlockedReason = strings.Join([]string{row.Metric, row.Period}, "/")
			}
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Key < rows[j].Key
	})
	return rows
}

func externalRowsForEndpoint(endpoint models.Endpoint, lanes []models.RoutingLane, rows []ExternalCapacityRow) []ExternalCapacityRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]ExternalCapacityRow, 0, len(rows))
	for _, row := range rows {
		if externalRowAppliesToEndpoint(row, endpoint, lanes) {
			out = append(out, row)
		}
	}
	return out
}

// PublishExternalCapacityChanges emits the same authoritative delta shape used
// by core limit-policy mutations for capacity rows owned by extensions. The
// change is mapped to every applicable endpoint once, then the telemetry hub
// routes the actor-scoped event to existing organization realtime streams.
func (a *API) PublishExternalCapacityChanges(ctx context.Context, changes []ExternalCapacityChange) error {
	if a == nil || a.telemetry == nil || len(changes) == 0 {
		return nil
	}
	tenant, ok := tenancy.ScopeFromContext(ctx)
	if !ok || strings.TrimSpace(tenant.OrganizationUUID) == "" {
		return ErrCapacityContextUnavailable
	}
	endpoints, err := a.store.ListEndpoints(ctx)
	if err != nil {
		return err
	}
	lanes, err := a.store.ListLanes(ctx)
	if err != nil {
		return err
	}
	memberships, err := a.store.ListLaneMemberships(ctx)
	if err != nil {
		return err
	}
	lanesByEndpoint := capacityLanesForEndpoint(lanes, memberships)
	for _, change := range changes {
		row := change.Row
		if strings.TrimSpace(row.Key) == "" {
			continue
		}
		for _, endpoint := range endpoints {
			if !externalRowAppliesToEndpoint(row, endpoint, lanesByEndpoint[endpoint.ID]) {
				continue
			}
			payload := map[string]any{
				"organization_uuid": tenant.OrganizationUUID,
				"actor_id":          row.ActorID,
				"endpoint_id":       endpoint.UUID,
				"provider_id":       endpoint.ProviderUUID,
				"user_scoped":       row.UserScoped,
			}
			if change.Removed {
				payload["removed_keys"] = []string{row.Key}
			} else {
				payload["rows"] = []ExternalCapacityRow{row}
			}
			a.telemetry.Publish(telemetry.Event{
				Type:    "capacity_limit_state",
				Payload: payload,
			})
		}
	}
	return nil
}

func externalRowAppliesToEndpoint(row ExternalCapacityRow, endpoint models.Endpoint, lanes []models.RoutingLane) bool {
	targetType := strings.TrimSpace(row.TargetType)
	targetKey := strings.TrimSpace(row.TargetKey)
	if targetType == "" || targetType == "global" || targetKey == "" {
		return true
	}
	switch targetType {
	case "provider":
		return strings.EqualFold(targetKey, endpoint.ProviderUUID)
	case "model":
		if strings.EqualFold(targetKey, endpoint.UUID) ||
			strings.EqualFold(targetKey, endpoint.Name) ||
			strings.EqualFold(targetKey, endpoint.UpstreamModel) {
			return true
		}
		for _, lane := range lanes {
			if strings.EqualFold(targetKey, lane.UUID) || strings.EqualFold(targetKey, lane.Name) {
				return true
			}
		}
	}
	return false
}

func capacityConfiguredPolicy(endpoint models.Endpoint, provider models.Provider, policies []models.LimitPolicy, metric models.Metric, period models.Period) (models.LimitPolicy, bool) {
	scopes := []struct {
		scopeType models.ScopeType
		scopeID   uint
		scopeUUID string
	}{
		{models.ScopeEndpoint, endpoint.ID, endpoint.UUID},
		{models.ScopeProvider, provider.ID, provider.UUID},
		{models.ScopeGlobal, 0, "global"},
	}
	for _, scope := range scopes {
		for _, policy := range policies {
			if !policy.Enabled || policy.Metric != metric || policy.Period != period || policy.ScopeType != scope.scopeType || policy.ScopeID != scope.scopeID {
				continue
			}
			return policy, true
		}
	}
	return models.LimitPolicy{}, false
}

func capacityObservedLimit(observed []models.ObservedLimit, scopeType models.ScopeType, scopeID uint, metric models.Metric, period models.Period, now time.Time) int64 {
	var best int64
	for _, item := range observed {
		if !item.Enabled || item.ScopeType != scopeType || item.ScopeID != scopeID || item.Metric != metric || item.Period != period || item.ObservedValue <= 0 {
			continue
		}
		if item.ExpiresAt != nil && !item.ExpiresAt.After(now) {
			continue
		}
		if best == 0 || item.ObservedValue < best {
			best = item.ObservedValue
		}
	}
	return best
}

func capacityRow(scopeType models.ScopeType, scopeID string, actorID string, metric models.Metric, period models.Period, configured, effective, used, reserved int64, source string, userScoped bool, now time.Time) ExternalCapacityRow {
	if effective <= 0 {
		effective = configured
	}
	remaining := effective - used - reserved
	if remaining < 0 {
		remaining = 0
	}
	percent := 0.0
	if effective > 0 {
		percent = math.Min(100, math.Max(0, float64(used+reserved)/float64(effective)*100))
	}
	windowStart, resetAt := capacityWindow(now, period)
	label := limitAbbrev(string(metric), string(period))
	if userScoped {
		label = "Your " + label
	}
	return ExternalCapacityRow{
		Key:         strings.Join([]string{string(scopeType), scopeID, string(metric), string(period), actorID}, ":"),
		Label:       label,
		ScopeType:   string(scopeType),
		ScopeID:     scopeID,
		ActorID:     actorID,
		Metric:      string(metric),
		Period:      string(period),
		Configured:  configured,
		Effective:   effective,
		Used:        used,
		Reserved:    reserved,
		Remaining:   remaining,
		Percent:     percent,
		WindowStart: windowStart,
		ResetAt:     resetAt,
		Source:      source,
		UserScoped:  userScoped,
	}
}

func capacityWindow(now time.Time, period models.Period) (time.Time, time.Time) {
	duration := periodDuration(period)
	if duration <= 0 {
		duration = time.Minute
	}
	windowStart := now.UTC().Truncate(duration)
	return windowStart, windowStart.Add(duration)
}

func periodDuration(period models.Period) time.Duration {
	switch period {
	case models.PeriodSecond:
		return time.Second
	case models.PeriodMinute:
		return time.Minute
	case models.PeriodHour:
		return time.Hour
	case models.PeriodDay:
		return 24 * time.Hour
	case models.PeriodMonth:
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

func limitAbbrev(metric string, period string) string {
	switch metric {
	case string(models.MetricRequests):
		switch period {
		case string(models.PeriodSecond):
			return "RPS"
		case string(models.PeriodMinute):
			return "RPM"
		case string(models.PeriodHour):
			return "RPH"
		case string(models.PeriodDay):
			return "RPD"
		case string(models.PeriodMonth):
			return "RPMO"
		}
	case string(models.MetricTokens):
		switch period {
		case string(models.PeriodSecond):
			return "TPS"
		case string(models.PeriodMinute):
			return "TPM"
		case string(models.PeriodHour):
			return "TPH"
		case string(models.PeriodDay):
			return "TPD"
		case string(models.PeriodMonth):
			return "TPMO"
		}
	case string(models.MetricSpend):
		switch period {
		case string(models.PeriodMinute):
			return "SPM"
		case string(models.PeriodHour):
			return "SPH"
		case string(models.PeriodDay):
			return "SPD"
		case string(models.PeriodMonth):
			return "SPMO"
		}
	}
	return strings.ToUpper(metric)
}

func capacityState(endpoint models.Endpoint, health models.HealthStatus, cooldownUntil *time.Time, rows []ExternalCapacityRow, now time.Time) string {
	if !endpoint.Enabled {
		return "unavailable"
	}
	switch health {
	case models.HealthUnhealthy:
		return "unhealthy"
	case models.HealthRateLimited:
		return "rate-limited"
	case models.HealthCoolingDown:
		return "cooling-down"
	}
	for _, row := range rows {
		if row.UserScoped {
			continue
		}
		if row.Effective > 0 && row.Used+row.Reserved >= row.Effective {
			return "rate-limited"
		}
	}
	if cooldownUntil != nil && cooldownUntil.After(now) {
		return "cooling-down"
	}
	return "healthy"
}

func capacityCooldownUntil(current *time.Time, rows []ExternalCapacityRow, now time.Time) *time.Time {
	var nextReset *time.Time
	if current != nil && current.After(now) {
		value := current.UTC()
		nextReset = &value
	}
	for _, row := range rows {
		if row.UserScoped || row.Effective <= 0 || row.Used+row.Reserved < row.Effective || !row.ResetAt.After(now) {
			continue
		}
		resetAt := row.ResetAt.UTC()
		// An endpoint is routable only after every exhausted resource policy has
		// released capacity. Show the latest boundary, not the first partial
		// release, so the UI countdown matches scheduler eligibility.
		if nextReset == nil || resetAt.After(*nextReset) {
			nextReset = &resetAt
		}
	}
	return nextReset
}
