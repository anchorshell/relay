package scheduler

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
)

// ScheduleEndpointHealthSync asks the owning tenant runtime to recompute an
// endpoint exactly when a persisted cooldown expires. It only schedules a
// state publication; admission and routing decisions remain owned by the
// scheduler's existing health derivation.
func (s *Scheduler) ScheduleEndpointHealthSync(taskID string, endpointID uint, at time.Time) {
	if s == nil || endpointID == 0 || at.IsZero() {
		return
	}
	if s.isRegistry() {
		if runtime := s.runtimeForTask(taskID); runtime != nil {
			runtime.ScheduleEndpointHealthSync(taskID, endpointID, at)
		}
		return
	}
	if s.capacityTelemetry == nil {
		return
	}
	s.capacityTelemetry.schedule("health:"+strconv.FormatUint(uint64(endpointID), 10), at.UTC(), func() {
		s.syncEndpointHealth(endpointID)
	})
}

type capacityTelemetryContext struct {
	organizationUUID string
	actorID          string
	requestID        string
	endpointID       uint
	endpointUUID     string
	providerUUID     string
}

type capacityTimer struct {
	generation uint64
	timer      *time.Timer
}

type externalCapacityState struct {
	context capacityTelemetryContext
	input   ExternalLimitInput
	limit   limits.EffectiveLimit
	used    int64
	resetAt time.Time
}

type capacityTelemetryEmitter struct {
	scheduler *Scheduler

	mu         sync.Mutex
	stopped    bool
	generation uint64
	timers     map[string]capacityTimer
	external   map[string]*externalCapacityState
}

func newCapacityTelemetryEmitter(s *Scheduler) *capacityTelemetryEmitter {
	return &capacityTelemetryEmitter{
		scheduler: s,
		timers:    make(map[string]capacityTimer),
		external:  make(map[string]*externalCapacityState),
	}
}

func (e *capacityTelemetryEmitter) stop() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.stopped = true
	for key, entry := range e.timers {
		entry.timer.Stop()
		delete(e.timers, key)
	}
	e.external = make(map[string]*externalCapacityState)
	e.mu.Unlock()
}

func (e *capacityTelemetryEmitter) schedule(key string, at time.Time, callback func()) {
	if e == nil || strings.TrimSpace(key) == "" || at.IsZero() || callback == nil {
		return
	}
	delay := time.Until(at)
	if delay < 0 {
		delay = 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return
	}
	if previous, ok := e.timers[key]; ok {
		previous.timer.Stop()
	}
	e.generation++
	generation := e.generation
	timer := time.AfterFunc(delay, func() {
		e.mu.Lock()
		entry, ok := e.timers[key]
		if !ok || entry.generation != generation || e.stopped {
			e.mu.Unlock()
			return
		}
		delete(e.timers, key)
		e.mu.Unlock()
		callback()
	})
	e.timers[key] = capacityTimer{generation: generation, timer: timer}
}

func (e *capacityTelemetryEmitter) cancel(key string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if entry, ok := e.timers[key]; ok {
		entry.timer.Stop()
		delete(e.timers, key)
	}
	e.mu.Unlock()
}

func (s *Scheduler) publishCapacityUsage(task *task, requests, tokens, spend int64, usedAt, now time.Time) {
	if s == nil || task == nil || s.capacityTelemetry == nil || s.telemetry == nil {
		return
	}
	ctx := capacityTelemetryContext{
		organizationUUID: organizationUUIDFromMetadata(task.trustedMetadata),
		actorID:          actorIDFromMetadata(task.trustedMetadata),
		requestID:        task.meta.RequestID,
		endpointID:       task.candidate.Endpoint.ID,
		endpointUUID:     task.candidate.Endpoint.UUID,
		providerUUID:     task.candidate.Endpoint.ProviderUUID,
	}
	seen := make(map[string]bool, len(task.evaluation.EffectiveLimits))
	externalInput := externalCapacityInput(task)
	coreRows := make([]map[string]any, 0, len(task.evaluation.EffectiveLimits))
	userRows := make([]map[string]any, 0, len(task.evaluation.EffectiveLimits))
	for _, limit := range task.evaluation.EffectiveLimits {
		if limit.Effective == nil || *limit.Effective <= 0 || limit.Metric == models.MetricConcurrency {
			continue
		}
		value := capacityMetricValue(limit.Metric, requests, tokens, spend)
		if value <= 0 {
			continue
		}
		key := capacityLimitKey(limit, ctx.actorID)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if limit.PolicyID != 0 && limit.PolicyUUID != "" {
			if row := s.coreCapacityLimitRow(ctx, limit, now); len(row) > 0 {
				coreRows = append(coreRows, row)
			}
			continue
		}
		if isActorCapacityLimit(limit) {
			if row := s.capacityTelemetry.recordExternal(ctx, externalInput, limit, value, usedAt, now); len(row) > 0 {
				userRows = append(userRows, row)
			}
		}
	}
	s.publishCapacityLimitStates(ctx, coreRows, false)
	s.publishCapacityLimitStates(ctx, userRows, true)
}

// publishCapacityReservation publishes the state transition created when a
// request is admitted for dispatch. The scheduler already owns these
// reservations; this method only exposes their current state to realtime
// clients and does not participate in admission or routing.
func (s *Scheduler) publishCapacityReservation(task *task, now time.Time) {
	if s == nil || task == nil || s.capacityTelemetry == nil || s.telemetry == nil {
		return
	}
	ctx := capacityTelemetryContext{
		organizationUUID: organizationUUIDFromMetadata(task.trustedMetadata),
		actorID:          actorIDFromMetadata(task.trustedMetadata),
		requestID:        task.meta.RequestID,
		endpointID:       task.candidate.Endpoint.ID,
		endpointUUID:     task.candidate.Endpoint.UUID,
		providerUUID:     task.candidate.Endpoint.ProviderUUID,
	}
	seen := make(map[string]bool, len(task.evaluation.EffectiveLimits))
	coreRows := make([]map[string]any, 0, len(task.evaluation.EffectiveLimits))
	userRows := make([]map[string]any, 0, len(task.evaluation.EffectiveLimits))
	for _, limit := range task.evaluation.EffectiveLimits {
		if limit.Effective == nil || *limit.Effective <= 0 || limit.Metric == models.MetricConcurrency {
			continue
		}
		key := capacityLimitKey(limit, ctx.actorID)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if limit.PolicyID != 0 && limit.PolicyUUID != "" {
			if row := s.coreCapacityLimitRow(ctx, limit, now); len(row) > 0 {
				coreRows = append(coreRows, row)
			}
			continue
		}
		if !isActorCapacityLimit(limit) {
			continue
		}
		configured := *limit.Effective
		if limit.Configured != nil && *limit.Configured > 0 {
			configured = *limit.Configured
		}
		reserved := max(limit.Reserved, 0) + capacityMetricValue(
			limit.Metric,
			1,
			task.estimatedInput+task.estimatedOutput,
			task.estimatedCost,
		)
		row := capacityLimitPayload(limit, ctx.actorID, configured, *limit.Effective, max(limit.Used, 0), reserved, capacityResetAt(limit.ResetAt))
		if len(row) > 0 {
			userRows = append(userRows, row)
		}
	}
	s.publishCapacityLimitStates(ctx, coreRows, false)
	s.publishCapacityLimitStates(ctx, userRows, true)
}

// PublishLimitPolicyState publishes the authoritative capacity row affected by
// a configured policy mutation. Policy edits are rare control-plane events,
// but realtime clients still need the new denominator and percentage without
// waiting for another request or fetching a replacement snapshot.
func (s *Scheduler) PublishLimitPolicyState(ctx context.Context, policy models.LimitPolicy, removed bool) error {
	if s == nil {
		return nil
	}
	if s.isRegistry() {
		runtime, err := s.runtimeForContext(ctx, false)
		if err != nil {
			return err
		}
		if runtime == nil {
			return nil
		}
		return runtime.PublishLimitPolicyState(ctx, policy, removed)
	}
	if s.store == nil || s.tracker == nil {
		return nil
	}
	if err := s.tracker.RefreshPolicy(ctx, policy, removed, time.Now().UTC()); err != nil {
		return err
	}
	if s.telemetry == nil || s.capacityTelemetry == nil {
		return nil
	}
	if policy.ScopeType != models.ScopeGlobal && policy.ScopeType != models.ScopeProvider && policy.ScopeType != models.ScopeEndpoint {
		return nil
	}

	endpointIDs, err := s.endpointIDsForLimitScopes(ctx, []limits.ScopeRef{{Type: policy.ScopeType, ID: policy.ScopeID}})
	if err != nil {
		return err
	}
	endpoints, err := s.store.ListEndpoints(ctx)
	if err != nil {
		return err
	}

	scopeUUID := strings.TrimSpace(policy.ScopeUUID)
	if scopeUUID == "" && policy.ScopeType == models.ScopeGlobal {
		scopeUUID = "global"
	}
	configured := policy.LimitValue
	effective := configured
	var observed *int64
	var sourceHeader string
	if !removed && policy.Enabled {
		_, observedLimits, err := s.store.FindLimits(ctx, policy.ScopeType, policy.ScopeID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, candidate := range observedLimits {
			if !candidate.Enabled || candidate.Metric != policy.Metric || candidate.Period != policy.Period || candidate.ObservedValue <= 0 {
				continue
			}
			if candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(now) {
				continue
			}
			if observed == nil || candidate.ObservedValue < *observed {
				value := candidate.ObservedValue
				observed = &value
				sourceHeader = candidate.SourceHeader
			}
		}
		if observed != nil && *observed < effective {
			effective = *observed
		}
	}
	limit := limits.EffectiveLimit{
		PolicyID:      policy.ID,
		PolicyUUID:    policy.UUID,
		PolicyVersion: policy.UpdatedAt.UTC().UnixNano(),
		Metric:        policy.Metric,
		Period:        policy.Period,
		Configured:    &configured,
		Observed:      observed,
		Effective:     &effective,
		ScopeType:     policy.ScopeType,
		ScopeID:       policy.ScopeID,
		ScopeUUID:     scopeUUID,
		SourceHeader:  sourceHeader,
	}
	key := capacityLimitKey(limit, "")
	if key == "" {
		return nil
	}
	if removed || !policy.Enabled {
		s.capacityTelemetry.cancel("core:" + key)
	}

	now := time.Now().UTC()
	organizationUUID := tenancy.OrganizationUUID(ctx)
	for _, endpoint := range endpoints {
		if !endpointIDs[endpoint.ID] {
			continue
		}
		eventContext := capacityTelemetryContext{
			organizationUUID: organizationUUID,
			endpointID:       endpoint.ID,
			endpointUUID:     endpoint.UUID,
			providerUUID:     endpoint.ProviderUUID,
		}
		if removed || !policy.Enabled {
			s.publishCapacityLimitRemoval(eventContext, key)
			continue
		}
		row := s.coreCapacityLimitRow(eventContext, limit, now)
		s.publishCapacityLimitStates(eventContext, []map[string]any{row}, false)
	}
	return nil
}

func capacityResetAt(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func (s *Scheduler) coreCapacityLimitRow(eventContext capacityTelemetryContext, limit limits.EffectiveLimit, now time.Time) map[string]any {
	if s == nil || s.capacityTelemetry == nil || s.tracker == nil || s.telemetry == nil || limit.PolicyID == 0 || limit.PolicyUUID == "" {
		return nil
	}
	configured := int64(0)
	if limit.Configured != nil {
		configured = *limit.Configured
	}
	policy := models.LimitPolicy{
		ID:         limit.PolicyID,
		UUID:       limit.PolicyUUID,
		ScopeType:  limit.ScopeType,
		ScopeID:    limit.ScopeID,
		ScopeUUID:  limit.ScopeUUID,
		Metric:     limit.Metric,
		Period:     limit.Period,
		LimitValue: configured,
		Enabled:    true,
	}
	if limit.PolicyVersion > 0 {
		policy.UpdatedAt = time.Unix(0, limit.PolicyVersion).UTC()
	}
	states := s.tracker.CurrentPolicyStates([]models.LimitPolicy{policy}, now)
	if len(states) == 0 {
		return nil
	}
	state := states[0]
	effective := configured
	if limit.Effective != nil && *limit.Effective > 0 {
		effective = *limit.Effective
	}
	if effective != policy.LimitValue {
		policy.LimitValue = effective
		effectiveStates := s.tracker.CurrentPolicyStates([]models.LimitPolicy{policy}, now)
		if len(effectiveStates) > 0 {
			state = effectiveStates[0]
		}
	}
	row := capacityLimitPayload(limit, eventContext.actorID, configured, effective, state.UsedValue, state.ReservedValue, state.NextEligibleAt)

	timerKey := "core:" + capacityLimitKey(limit, eventContext.actorID)
	nextExpiry := s.tracker.NextExpirationAt(
		limits.ScopeRef{Type: limit.ScopeType, ID: limit.ScopeID},
		limit.Metric,
		limit.Period,
		now,
	)
	if nextExpiry.IsZero() {
		s.capacityTelemetry.cancel(timerKey)
		return row
	}
	// Rolling windows include an event exactly at their cutoff. Refresh just
	// beyond the boundary so the emitted row is the next state (5 -> 4), not a
	// duplicate boundary state (5 -> 5). reset_at remains the exact boundary.
	refreshAt := nextExpiry.UTC().Add(time.Millisecond)
	if !refreshAt.After(now) {
		refreshAt = now.Add(time.Millisecond)
	}
	s.capacityTelemetry.schedule(timerKey, refreshAt, func() {
		refreshNow := time.Now().UTC()
		row := s.coreCapacityLimitRow(eventContext, limit, refreshNow)
		s.publishCapacityLimitStates(eventContext, []map[string]any{row}, false)
		s.syncEndpointHealth(eventContext.endpointID)
	})
	return row
}

func (e *capacityTelemetryEmitter) recordExternal(eventContext capacityTelemetryContext, input ExternalLimitInput, limit limits.EffectiveLimit, value int64, usedAt, now time.Time) map[string]any {
	if e == nil || e.scheduler == nil || value <= 0 {
		return nil
	}
	duration := capacityPeriodDuration(limit.Period)
	if duration <= 0 {
		return nil
	}
	expiresAt := usedAt.UTC().Add(duration)
	if !expiresAt.After(now) {
		return nil
	}
	key := capacityLimitKey(limit, eventContext.actorID)
	if key == "" {
		return nil
	}

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	state := e.external[key]
	if state == nil {
		state = &externalCapacityState{context: eventContext, input: input, limit: limit, used: max(limit.Used, 0)}
		e.external[key] = state
	} else {
		state.context = eventContext
		state.input = input
		state.limit = limit
		// A request released from a rolling-window queue can complete before
		// the expiry refresh callback wins the race. Once the prior release
		// boundary has passed, rebase from the authoritative external snapshot
		// before adding this request. Within the same window, retain the
		// monotonic value so concurrent completions cannot erase each other.
		if !state.resetAt.IsZero() && !state.resetAt.After(now) {
			state.used = max(limit.Used, 0)
		} else if limit.Used > state.used {
			state.used = limit.Used
		}
	}
	state.used += value
	state.resetAt = earliestFutureTime(now, state.resetAt, limit.ResetAt.UTC(), expiresAt)
	snapshot := *state
	e.mu.Unlock()

	e.scheduleExternal(key)
	return e.externalCapacityRow(snapshot)
}

func (e *capacityTelemetryEmitter) scheduleExternal(key string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	state := e.external[key]
	if state == nil || state.resetAt.IsZero() || e.stopped || state.used <= 0 {
		e.mu.Unlock()
		e.cancel("external:" + key)
		return
	}
	// Pro's exact rolling state uses the same inclusive cutoff contract as the
	// core tracker, so query immediately after the advertised reset boundary.
	next := state.resetAt.Add(time.Millisecond)
	e.mu.Unlock()
	e.schedule("external:"+key, next, func() { e.refreshExternal(key) })
}

func (e *capacityTelemetryEmitter) refreshExternal(key string) {
	if e == nil || e.scheduler == nil {
		return
	}
	now := time.Now().UTC()
	e.mu.Lock()
	state := e.external[key]
	if state == nil || e.stopped {
		e.mu.Unlock()
		return
	}
	snapshot := *state
	e.mu.Unlock()

	ctx := tenancy.ContextWithMetadataScope(e.scheduler.backgroundContext(), snapshot.input.Metadata)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	refreshed, ok := e.scheduler.externalCapacityLimit(ctx, snapshot, now)
	if !ok {
		e.schedule("external:"+key, now.Add(time.Second), func() { e.refreshExternal(key) })
		return
	}
	e.mu.Lock()
	state = e.external[key]
	if state == nil || e.stopped {
		e.mu.Unlock()
		return
	}
	state.limit = refreshed.limit
	state.used = refreshed.used
	state.resetAt = refreshed.resetAt
	snapshot = *state
	e.mu.Unlock()

	row := e.externalCapacityRow(snapshot)
	e.scheduler.publishCapacityLimitStates(snapshot.context, []map[string]any{row}, true)
	if snapshot.used <= 0 {
		e.mu.Lock()
		if current := e.external[key]; current != nil && current.used <= 0 {
			delete(e.external, key)
		}
		e.mu.Unlock()
		e.cancel("external:" + key)
		return
	}
	e.scheduleExternal(key)
}

func (e *capacityTelemetryEmitter) externalCapacityRow(state externalCapacityState) map[string]any {
	if e == nil || e.scheduler == nil || e.scheduler.telemetry == nil || state.limit.Effective == nil {
		return nil
	}
	configured := *state.limit.Effective
	if state.limit.Configured != nil && *state.limit.Configured > 0 {
		configured = *state.limit.Configured
	}
	effective := *state.limit.Effective
	var next *time.Time
	if !state.resetAt.IsZero() {
		value := state.resetAt.UTC()
		next = &value
	}
	row := capacityLimitPayload(state.limit, state.context.actorID, configured, effective, state.used, 0, next)
	return row
}

type refreshedExternalCapacity struct {
	limit   limits.EffectiveLimit
	used    int64
	resetAt time.Time
}

func (s *Scheduler) externalCapacityLimit(ctx context.Context, state externalCapacityState, now time.Time) (refreshedExternalCapacity, bool) {
	// Expiry callbacks must bypass request-candidate caches. They can fire a
	// few milliseconds before Pro's canonical release boundary and immediately
	// retry at the corrected boundary; reusing the completed request ID would
	// otherwise return the first, now-stale snapshot and stop the timer.
	refreshInput := state.input
	refreshInput.RequestID = ""
	for _, hook := range s.externalLimitHooks {
		if hook == nil {
			continue
		}
		items, err := hook(ctx, refreshInput)
		if err != nil {
			return refreshedExternalCapacity{}, false
		}
		for _, item := range items {
			metric, metricOK := externalMetric(item.Metric)
			period, periodOK := externalPeriod(item.Period)
			ref := externalScopeRef(item.Scope)
			if !metricOK || !periodOK || metric != state.limit.Metric || period != state.limit.Period || ref.Type != state.limit.ScopeType || ref.Key != state.limit.ScopeUUID {
				continue
			}
			configured := item.LimitValue
			limit := state.limit
			limit.Configured = &configured
			limit.Effective = &configured
			limit.Used = max(item.Used, 0)
			limit.ResetAt = item.ResetAt.UTC()
			resetAt := time.Time{}
			if limit.Used > 0 && limit.ResetAt.After(now) {
				resetAt = limit.ResetAt
			}
			return refreshedExternalCapacity{limit: limit, used: limit.Used, resetAt: resetAt}, true
		}
	}
	return refreshedExternalCapacity{}, false
}

func externalCapacityInput(task *task) ExternalLimitInput {
	if task == nil {
		return ExternalLimitInput{}
	}
	return ExternalLimitInput{
		RequestID:       task.meta.RequestID,
		RouteKind:       string(task.meta.RouteKind),
		IncomingModel:   task.meta.IncomingModel,
		Lane:            task.meta.Lane,
		EndpointID:      task.candidate.Endpoint.UUID,
		EndpointName:    task.candidate.Endpoint.Name,
		ProviderID:      task.candidate.Endpoint.ProviderUUID,
		UpstreamModel:   task.candidate.Endpoint.UpstreamModel,
		LaneID:          candidateLaneUUID(task.candidate),
		Metadata:        cloneMetadata(task.trustedMetadata),
		EstimatedTokens: task.estimatedInput + task.estimatedOutput,
		EstimatedSpend:  task.estimatedCost,
		Preview:         task.preview,
	}
}

func earliestFutureTime(now time.Time, values ...time.Time) time.Time {
	var earliest time.Time
	for _, value := range values {
		value = value.UTC()
		if !value.After(now) || (!earliest.IsZero() && !value.Before(earliest)) {
			continue
		}
		earliest = value
	}
	return earliest
}

func (s *Scheduler) publishCapacityLimitStates(eventContext capacityTelemetryContext, rows []map[string]any, userScoped bool) {
	if s == nil || s.telemetry == nil {
		return
	}
	filtered := rows[:0]
	for _, row := range rows {
		if len(row) > 0 {
			filtered = append(filtered, row)
		}
	}
	if len(filtered) == 0 {
		return
	}
	actorID := ""
	requestID := ""
	if userScoped {
		actorID = eventContext.actorID
		requestID = eventContext.requestID
	}
	payload := map[string]any{
		"organization_uuid": eventContext.organizationUUID,
		"actor_id":          actorID,
		"request_id":        requestID,
		"endpoint_id":       eventContext.endpointUUID,
		"provider_id":       eventContext.providerUUID,
		"user_scoped":       userScoped,
		"rows":              filtered,
	}
	if !userScoped {
		var cooldownUntil time.Time
		for _, row := range filtered {
			blocked, _ := row["blocked"].(bool)
			if !blocked {
				continue
			}
			resetAt, ok := row["reset_at"].(time.Time)
			if ok && resetAt.After(cooldownUntil) {
				cooldownUntil = resetAt.UTC()
			}
		}
		if !cooldownUntil.IsZero() {
			// Keep model state and its exact rolling-window release boundary in
			// the same websocket delta. The browser should never have to wait for
			// a replacement snapshot to turn "rate limited" into a countdown.
			payload["health_status"] = models.HealthRateLimited
			payload["cooldown_until"] = cooldownUntil
			payload["cooldown_reason"] = "configured_limit"
			payload["cooldown_status_code"] = 0
		}
	}
	s.telemetry.Publish(telemetry.Event{
		Type:    "capacity_limit_state",
		Payload: payload,
	})
}

func (s *Scheduler) publishCapacityLimitRemoval(eventContext capacityTelemetryContext, key string) {
	if s == nil || s.telemetry == nil || strings.TrimSpace(key) == "" {
		return
	}
	s.telemetry.Publish(telemetry.Event{
		Type: "capacity_limit_state",
		Payload: map[string]any{
			"organization_uuid": eventContext.organizationUUID,
			"endpoint_id":       eventContext.endpointUUID,
			"provider_id":       eventContext.providerUUID,
			"user_scoped":       false,
			"removed_keys":      []string{key},
		},
	})
}

func capacityLimitPayload(limit limits.EffectiveLimit, actorID string, configured, effective, used, reserved int64, resetAt *time.Time) map[string]any {
	remaining := effective - used - reserved
	if remaining < 0 {
		remaining = 0
	}
	percent := 0.0
	if effective > 0 {
		percent = math.Min(100, math.Max(0, float64(used+reserved)/float64(effective)*100))
	}
	source := "configured"
	if limit.Observed != nil && (limit.Configured == nil || *limit.Observed < *limit.Configured) {
		source = "observed"
	}
	userScoped := isActorCapacityLimit(limit)
	if !userScoped {
		actorID = ""
	}
	label := strings.TrimSpace(limit.CapacityLabel)
	if label == "" {
		label = capacityLimitLabel(limit.Metric, limit.Period, limit.ScopeType)
	}
	row := map[string]any{
		"key":         capacityLimitKey(limit, actorID),
		"label":       label,
		"scope_type":  limit.ScopeType,
		"scope_id":    limit.ScopeUUID,
		"actor_id":    actorID,
		"metric":      limit.Metric,
		"period":      limit.Period,
		"configured":  configured,
		"effective":   effective,
		"used":        used,
		"reserved":    reserved,
		"remaining":   remaining,
		"percent":     percent,
		"user_scoped": userScoped,
		"blocked":     effective > 0 && used+reserved >= effective,
	}
	if !userScoped {
		row["source"] = source
	}
	if resetAt != nil && !resetAt.IsZero() {
		row["reset_at"] = resetAt.UTC()
	}
	return row
}

func capacityLimitLabel(metric models.Metric, period models.Period, scopeType models.ScopeType) string {
	prefix := ""
	switch scopeType {
	case models.ScopeType("user"):
		prefix = "Your "
	case models.ScopeType("user_model"):
		prefix = "Your model "
	case models.ScopeType("user_provider"):
		prefix = "Your provider "
	}
	label := ""
	switch metric {
	case models.MetricRequests:
		switch period {
		case models.PeriodSecond:
			label = "RPS"
		case models.PeriodMinute:
			label = "RPM"
		case models.PeriodHour:
			label = "RPH"
		case models.PeriodDay:
			label = "RPD"
		case models.PeriodMonth:
			label = "RPMO"
		}
	case models.MetricTokens:
		switch period {
		case models.PeriodSecond:
			label = "TPS"
		case models.PeriodMinute:
			label = "TPM"
		case models.PeriodHour:
			label = "TPH"
		case models.PeriodDay:
			label = "TPD"
		case models.PeriodMonth:
			label = "TPMO"
		}
	case models.MetricSpend:
		switch period {
		case models.PeriodMinute:
			label = "SPM"
		case models.PeriodHour:
			label = "SPH"
		case models.PeriodDay:
			label = "SPD"
		case models.PeriodMonth:
			label = "SPMO"
		}
	}
	if label != "" {
		return prefix + label
	}
	return prefix + strings.ToUpper(string(metric))
}

func capacityLimitKey(limit limits.EffectiveLimit, actorID string) string {
	scopeID := strings.TrimSpace(limit.ScopeUUID)
	if scopeID == "" && limit.ScopeType == models.ScopeGlobal {
		scopeID = "global"
	}
	if scopeID == "" || limit.Metric == "" || limit.Period == "" {
		return ""
	}
	if isActorCapacityLimit(limit) {
		// Pro snapshots deliberately normalize every user-owned policy to the
		// same stable key shape. ScopeUUID already contains the actor and any
		// model/provider target, so adding actorID here would create a second row
		// instead of updating the row delivered in the initial snapshot.
		prefix := strings.TrimSpace(limit.CapacityKeyNamespace)
		if prefix == "" {
			prefix = "user"
		}
		return strings.Join([]string{prefix, scopeID, string(limit.Metric), string(limit.Period)}, ":")
	}
	return strings.Join([]string{string(limit.ScopeType), scopeID, string(limit.Metric), string(limit.Period), ""}, ":")
}

func capacityMetricValue(metric models.Metric, requests, tokens, spend int64) int64 {
	switch metric {
	case models.MetricRequests:
		return requests
	case models.MetricTokens:
		return tokens
	case models.MetricSpend:
		return spend
	default:
		return 0
	}
}

func capacityPeriodDuration(period models.Period) time.Duration {
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

func isUserCapacityScope(scopeType models.ScopeType) bool {
	switch scopeType {
	case models.ScopeType("user"), models.ScopeType("user_model"), models.ScopeType("user_provider"):
		return true
	default:
		return false
	}
}

func isActorCapacityLimit(limit limits.EffectiveLimit) bool {
	return limit.ActorScoped || isUserCapacityScope(limit.ScopeType)
}
