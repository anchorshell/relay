package limits

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/observability"
	"github.com/anchorshell/relay/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var periodDuration = map[models.Period]time.Duration{
	models.PeriodSecond: time.Second,
	models.PeriodMinute: time.Minute,
	models.PeriodHour:   time.Hour,
	models.PeriodDay:    24 * time.Hour,
	models.PeriodMonth:  30 * 24 * time.Hour,
}

var pacedRequestPeriods = map[models.Period]bool{
	models.PeriodSecond: true,
	models.PeriodMinute: true,
}

const maxTrackedDuration = 30 * 24 * time.Hour
const maxExactExpirationGroups = 60_001

// Version 3 invalidates checkpoints written before terminal completion used
// the exact policy attribution carried by the issued permit. Those older rows
// can be internally valid but stale after a fallback or other task-state
// transition. Hydration treats older versions as missing and performs one
// bounded request-log rebuild before writing the current format.
const checkpointFormatVersion = 3

type checkpointEvent struct {
	AtMS     int64 `json:"a"`
	ExpiryMS int64 `json:"e"`
	Value    int64 `json:"v"`
}

type checkpointPayload struct {
	Events []checkpointEvent `json:"events"`
}

type windowIdentity struct {
	Key    usageKey
	Period models.Period
}

type ScopeRef struct {
	Type models.ScopeType
	ID   uint
	Key  string
}

type event struct {
	atMS     int64
	expiryMS int64
	period   models.Period
	value    int64
}

type reservationEvent struct {
	taskID  string
	atMS    int64
	value   int64
	visible bool
}

type activeWindows map[usageKey]map[models.Period]struct{}

type usageKey struct {
	Scope  ScopeRef
	Metric models.Metric
}

func canonicalScopeRef(scope ScopeRef) ScopeRef {
	// Public/core scopes have a stable numeric identity. Their public key is
	// transport metadata and must not split tracker state. Pro-only external
	// user scopes have no numeric ID, so their string key remains authoritative.
	if scope.ID != 0 || scope.Type == models.ScopeGlobal {
		scope.Key = ""
	}
	return scope
}

func trackerUsageKey(scope ScopeRef, metric models.Metric) usageKey {
	return usageKey{Scope: canonicalScopeRef(scope), Metric: metric}
}

type Tracker struct {
	mu           sync.Mutex
	events       map[usageKey][]event
	active       activeWindows
	reservations map[usageKey][]reservationEvent
	concurrency  map[ScopeRef]int64
	store        *store.Store
	hydrated     bool
	strictActive bool
	policies     []models.LimitPolicy
	observed     []models.ObservedLimit
	nextExpiry   map[windowIdentity]int64
}

func NewTracker(st *store.Store) *Tracker {
	return &Tracker{
		events:       make(map[usageKey][]event),
		reservations: make(map[usageKey][]reservationEvent),
		concurrency:  make(map[ScopeRef]int64),
		active:       make(activeWindows),
		nextExpiry:   make(map[windowIdentity]int64),
		store:        st,
	}
}

func (t *Tracker) HydrateRecentUsage(ctx context.Context, now time.Time) error {
	if t.store == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	t.mu.Lock()
	if t.hydrated {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()

	ctx, span := observability.Tracer().Start(ctx, "relay.limit_state.rebuild")
	defer span.End()
	policies, err := t.store.ListLimitPolicies(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active policy load failed")
		return err
	}
	observed, err := t.store.ListObservedLimits(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "observed limit load failed")
		return err
	}
	active := make(activeWindows, len(policies)+len(observed))
	missingActive := make(activeWindows, len(policies)+len(observed))
	restored := make(map[windowIdentity]struct{}, len(policies)+len(observed))
	rebuilt := make(map[usageKey][]event, len(active))
	policyIDs := make([]uint, 0, len(policies))
	observedIDs := make([]uint, 0, len(observed))
	for _, policy := range policies {
		if policy.Enabled && policy.Metric != models.MetricConcurrency && policy.ID != 0 {
			policyIDs = append(policyIDs, policy.ID)
		}
	}
	for _, limit := range observed {
		if limit.Enabled && limit.Metric != models.MetricConcurrency && limit.ID != 0 {
			observedIDs = append(observedIDs, limit.ID)
		}
	}
	policySegments, err := t.store.ListLimitPolicyStateSegments(ctx, policyIDs)
	if err != nil {
		return err
	}
	observedSegments, err := t.store.ListObservedLimitStateSegments(ctx, observedIDs)
	if err != nil {
		return err
	}
	latestUsageAt, err := t.store.LatestUsageEventAt(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request-log watermark load failed")
		return err
	}
	policySegmentByID := make(map[uint]models.LimitPolicyStateSegment, len(policySegments))
	for _, segment := range policySegments {
		policySegmentByID[segment.PolicyID] = segment
	}
	observedSegmentByID := make(map[uint]models.ObservedLimitStateSegment, len(observedSegments))
	for _, segment := range observedSegments {
		observedSegmentByID[segment.ObservedLimitID] = segment
	}
	horizon := time.Duration(0)
	for _, policy := range policies {
		if !policy.Enabled || policy.Metric == models.MetricConcurrency {
			continue
		}
		duration := periodDuration[policy.Period]
		if duration <= 0 {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric)
		addActiveWindow(active, key, policy.Period)
		identity := windowIdentity{Key: key, Period: policy.Period}
		if _, exists := restored[identity]; exists {
			continue
		}
		segment, found := policySegmentByID[policy.ID]
		if found {
			if checkpointPredatesUsage(segment.UpdatedAt, latestUsageAt) {
				addActiveWindow(missingActive, key, policy.Period)
				if duration > horizon {
					horizon = duration
				}
				continue
			}
			if segment.FormatVersion != checkpointFormatVersion {
				addActiveWindow(missingActive, key, policy.Period)
				if duration > horizon {
					horizon = duration
				}
				continue
			}
			events, valid := decodeCheckpoint(segment.Payload, segment.Checksum, segment.FormatVersion, segment.PolicyVersion, policy.UpdatedAt.UTC().UnixNano(), policy.Period, now)
			if valid {
				rebuilt[key] = append(rebuilt[key], events...)
				restored[identity] = struct{}{}
				continue
			}
			// Corrupt or incompatible state fails closed for this policy only.
			rebuilt[key] = appendWindowEvent(rebuilt[key], newWindowEvent(policy.Period, max(policy.LimitValue, 1), now, duration))
			restored[identity] = struct{}{}
			continue
		}
		addActiveWindow(missingActive, key, policy.Period)
		if duration > horizon {
			horizon = duration
		}
	}
	for _, limit := range observed {
		if !limit.Enabled || limit.Metric == models.MetricConcurrency || (limit.ExpiresAt != nil && !limit.ExpiresAt.After(now)) {
			continue
		}
		duration := periodDuration[limit.Period]
		if duration <= 0 {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: limit.ScopeType, ID: limit.ScopeID}, limit.Metric)
		addActiveWindow(active, key, limit.Period)
		identity := windowIdentity{Key: key, Period: limit.Period}
		if _, exists := restored[identity]; exists {
			continue
		}
		segment, found := observedSegmentByID[limit.ID]
		if found {
			if checkpointPredatesUsage(segment.UpdatedAt, latestUsageAt) {
				addActiveWindow(missingActive, key, limit.Period)
				if duration > horizon {
					horizon = duration
				}
				continue
			}
			if segment.FormatVersion != checkpointFormatVersion {
				addActiveWindow(missingActive, key, limit.Period)
				if duration > horizon {
					horizon = duration
				}
				continue
			}
			events, valid := decodeCheckpoint(segment.Payload, segment.Checksum, segment.FormatVersion, segment.PolicyVersion, limit.UpdatedAt.UTC().UnixNano(), limit.Period, now)
			if valid {
				rebuilt[key] = append(rebuilt[key], events...)
				restored[identity] = struct{}{}
				continue
			}
			rebuilt[key] = appendWindowEvent(rebuilt[key], newWindowEvent(limit.Period, max(limit.ObservedValue, 1), now, duration))
			restored[identity] = struct{}{}
			continue
		}
		addActiveWindow(missingActive, key, limit.Period)
		if duration > horizon {
			horizon = duration
		}
	}
	span.SetAttributes(
		attribute.Int("anchorshell.relay.limit_state.policy_count", len(policies)),
		attribute.Int("anchorshell.relay.limit_state.state_rows", len(active)),
	)
	if horizon <= 0 {
		t.mu.Lock()
		t.events = rebuilt
		t.active = active
		t.policies = append([]models.LimitPolicy(nil), policies...)
		t.observed = append([]models.ObservedLimit(nil), observed...)
		t.hydrated = true
		t.strictActive = true
		t.rebuildNextExpiriesLocked(now)
		t.mu.Unlock()
		return nil
	}
	if horizon > maxTrackedDuration {
		horizon = maxTrackedDuration
	}
	cutoff := now.Add(-horizon)
	query := t.store.DB().WithContext(ctx).
		Model(&models.RequestLog{}).
		Select([]string{
			"id",
			"lane_id",
			"endpoint_id",
			"provider_id",
			"status_code",
			"task_state",
			"started_at",
			"queued_at",
			"created_at",
			"estimated_input_tokens",
			"estimated_output_tokens",
			"actual_input_tokens",
			"actual_output_tokens",
			"actual_total_tokens",
			"estimated_cost_micros",
			"actual_cost_micros",
		}).
		Where("task_state IN ?", []string{"completed", "failed", "cancelled"}).
		Where("(started_at >= ? OR (started_at IS NULL AND queued_at >= ?) OR (started_at IS NULL AND queued_at IS NULL AND created_at >= ?))", cutoff, cutoff, cutoff).
		Order("id asc")
	rows, err := query.Rows()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request-log state rebuild failed")
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var log models.RequestLog
		if err := t.store.DB().ScanRows(rows, &log); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "request-log state rebuild scan failed")
			return err
		}
		at := persistedUsageAt(log)
		if at.IsZero() || at.Before(cutoff) || at.After(now) || !persistedRequestConsumesUsage(log) {
			continue
		}
		scopes := persistedUsageScopes(log)
		appendHydratedEventTo(rebuilt, scopes, models.MetricRequests, 1, at, missingActive)
		if tokens := persistedTokenValue(log); tokens > 0 {
			appendHydratedEventTo(rebuilt, scopes, models.MetricTokens, tokens, at, missingActive)
		}
		if spend := persistedSpendValue(log); spend > 0 {
			appendHydratedEventTo(rebuilt, scopes, models.MetricSpend, spend, at, missingActive)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	t.mu.Lock()
	if t.hydrated {
		t.mu.Unlock()
		return nil
	}
	t.events = rebuilt
	t.active = active
	t.policies = append([]models.LimitPolicy(nil), policies...)
	t.observed = append([]models.ObservedLimit(nil), observed...)
	for key := range t.events {
		t.pruneLocked(key, now)
	}
	t.hydrated = true
	t.strictActive = true
	t.rebuildNextExpiriesLocked(now)
	t.mu.Unlock()
	return t.FlushCheckpoints(ctx, now)
}

func checkpointPredatesUsage(checkpointAt, latestUsageAt time.Time) bool {
	return !latestUsageAt.IsZero() && (checkpointAt.IsZero() || latestUsageAt.After(checkpointAt))
}

func (t *Tracker) Used(scope ScopeRef, metric models.Metric, period models.Period, now time.Time) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usedLocked(scope, metric, period, now)
}

func (t *Tracker) UsedExcludingTask(scope ScopeRef, metric models.Metric, period models.Period, now time.Time, excludedTaskID string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usedLockedExcludingTask(scope, metric, period, now, excludedTaskID)
}

func (t *Tracker) RecordWindow(ctx context.Context, scopes []ScopeRef, metric models.Metric, value int64, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.recordLocked(scopes, metric, value, now)
}

func (t *Tracker) Reserve(taskID string, scopes []ScopeRef, metric models.Metric, value int64, at time.Time) {
	t.reserve(taskID, scopes, metric, value, at, false)
}

func (t *Tracker) ReserveRuntime(taskID string, scopes []ScopeRef, metric models.Metric, value int64, at time.Time) {
	t.reserve(taskID, scopes, metric, value, at, true)
}

func (t *Tracker) reserve(taskID string, scopes []ScopeRef, metric models.Metric, value int64, at time.Time, visible bool) {
	if taskID == "" || value <= 0 || metric == models.MetricConcurrency {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, scope := range scopes {
		key := trackerUsageKey(scope, metric)
		if len(t.active[key]) == 0 && t.strictActive {
			continue
		}
		t.reservations[key] = append(t.reservations[key], reservationEvent{taskID: taskID, atMS: unixMilli(at), value: value, visible: visible})
	}
}

func (t *Tracker) Release(taskID string) {
	if taskID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseLocked(taskID)
}

func (t *Tracker) Commit(ctx context.Context, taskID string, scopes []ScopeRef, requests, tokens, spend int64, at time.Time) error {
	_ = ctx
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseLocked(taskID)
	if requests > 0 {
		if err := t.recordLocked(scopes, models.MetricRequests, requests, at); err != nil {
			return err
		}
	}
	if tokens > 0 {
		if err := t.recordLocked(scopes, models.MetricTokens, tokens, at); err != nil {
			return err
		}
	}
	if spend > 0 {
		if err := t.recordLocked(scopes, models.MetricSpend, spend, at); err != nil {
			return err
		}
	}
	return nil
}

func persistedUsageAt(log models.RequestLog) time.Time {
	switch {
	case log.StartedAt != nil && !log.StartedAt.IsZero():
		return log.StartedAt.UTC()
	case log.QueuedAt != nil && !log.QueuedAt.IsZero():
		return log.QueuedAt.UTC()
	case !log.CreatedAt.IsZero():
		return log.CreatedAt.UTC()
	default:
		return time.Time{}
	}
}

func persistedRequestConsumesUsage(log models.RequestLog) bool {
	return models.RequestLogCountsAsUsage(log)
}

func persistedUsageScopes(log models.RequestLog) []ScopeRef {
	scopes := []ScopeRef{{Type: models.ScopeGlobal, ID: 0}}
	if log.ProviderID != nil {
		scopes = append(scopes, ScopeRef{Type: models.ScopeProvider, ID: *log.ProviderID})
	}
	if log.EndpointID != nil {
		scopes = append(scopes, ScopeRef{Type: models.ScopeEndpoint, ID: *log.EndpointID})
	}
	if log.LaneID != nil {
		scopes = append(scopes, ScopeRef{Type: models.ScopeLane, ID: *log.LaneID})
	}
	return scopes
}

func persistedTokenValue(log models.RequestLog) int64 {
	if log.ActualTotalTokens > 0 {
		return log.ActualTotalTokens
	}
	if log.ActualInputTokens > 0 || log.ActualOutputTokens > 0 {
		return log.ActualInputTokens + log.ActualOutputTokens
	}
	if !models.RequestLogSuccessfulUsage(log) {
		return 0
	}
	return log.EstimatedInputTokens + log.EstimatedOutputTokens
}

func persistedSpendValue(log models.RequestLog) int64 {
	if log.ActualCostMicros > 0 {
		return log.ActualCostMicros
	}
	if !models.RequestLogSuccessfulUsage(log) {
		return 0
	}
	return log.EstimatedCostMicros
}

func (t *Tracker) appendHydratedEventLocked(scopes []ScopeRef, metric models.Metric, value int64, at time.Time, active activeWindows) {
	appendHydratedEventTo(t.events, scopes, metric, value, at, active)
}

func appendHydratedEventTo(target map[usageKey][]event, scopes []ScopeRef, metric models.Metric, value int64, at time.Time, active activeWindows) {
	if value <= 0 || metric == models.MetricConcurrency {
		return
	}
	for _, scope := range scopes {
		key := trackerUsageKey(scope, metric)
		periods := active[key]
		if len(periods) == 0 {
			continue
		}
		for period := range periods {
			target[key] = appendWindowEvent(target[key], newWindowEvent(period, value, at, periodDuration[period]))
		}
	}
}

func (t *Tracker) AddConcurrency(scopes []ScopeRef, delta int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, scope := range scopes {
		scope = canonicalScopeRef(scope)
		t.concurrency[scope] += delta
		if t.concurrency[scope] < 0 {
			t.concurrency[scope] = 0
		}
	}
}

func (t *Tracker) Concurrency(scope ScopeRef) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.concurrency[canonicalScopeRef(scope)]
}

// Idle reports whether the tracker has any in-flight reservation or
// concurrency state that must keep its owning runtime alive.
func (t *Tracker) Idle() bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, reservations := range t.reservations {
		if len(reservations) > 0 {
			return false
		}
	}
	for _, used := range t.concurrency {
		if used > 0 {
			return false
		}
	}
	return true
}

// CurrentRuntimeUsed returns committed usage plus visible in-flight
// reservations for one exact rolling policy window. It is runtime state, not
// a persisted analytics rollup.
func (t *Tracker) CurrentRuntimeUsed(scope ScopeRef, metric models.Metric, period models.Period, now time.Time) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if metric == models.MetricConcurrency {
		return t.concurrency[canonicalScopeRef(scope)]
	}
	duration := periodDuration[period]
	if duration <= 0 {
		return 0
	}
	key := trackerUsageKey(scope, metric)
	t.pruneLocked(key, now)
	return t.sumWindowLocked(t.runtimeEventsLocked(key, period), period, now, true)
}

// CurrentPolicyStates returns exact rolling-window state for configured core
// policies only. Committed usage and visible in-flight reservations are kept
// separate so capacity callers can derive remaining allowance without
// persisting a generic scope/metric/period matrix.
func (t *Tracker) CurrentPolicyStates(policies []models.LimitPolicy, now time.Time) []models.LimitPolicyState {
	return t.policyStates(policies, now, true)
}

// PersistablePolicyStates returns committed rolling-window usage for active
// configured policies. It intentionally excludes reservations, which remain
// runtime/lease state and must not become stale snapshots after a process
// exits.
func (t *Tracker) PersistablePolicyStates(policies []models.LimitPolicy, now time.Time) []models.LimitPolicyState {
	return t.policyStates(policies, now, false)
}

func (t *Tracker) policyStates(policies []models.LimitPolicy, now time.Time, includeVisibleReservations bool) []models.LimitPolicyState {
	t.mu.Lock()
	defer t.mu.Unlock()

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	states := make([]models.LimitPolicyState, 0, len(policies))
	for _, policy := range policies {
		if !policy.Enabled || policy.ID == 0 || policy.UUID == "" {
			continue
		}
		state := models.LimitPolicyState{
			PolicyID:      policy.ID,
			PolicyUUID:    policy.UUID,
			PolicyVersion: policy.UpdatedAt.UTC().UnixNano(),
			StateVersion:  1,
			UpdatedAt:     now,
		}
		if policy.Metric == models.MetricConcurrency {
			state.UsedValue = t.concurrency[canonicalScopeRef(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID})]
			states = append(states, state)
			continue
		}
		duration := periodDuration[policy.Period]
		if duration <= 0 {
			continue
		}
		// These bounds describe the exact rolling window, not a calendar bucket.
		state.WindowStart = now.Add(-duration)
		state.WindowEnd = now
		key := trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric)
		t.pruneLocked(key, now)
		windowEvents := make([]event, 0, len(t.events[key])+len(t.reservations[key]))
		for _, item := range t.events[key] {
			if item.period != policy.Period || item.expiryMS <= unixMilli(now) || item.atMS > unixMilli(now) {
				continue
			}
			windowEvents = append(windowEvents, item)
			state.UsedValue += item.value
		}
		if includeVisibleReservations {
			for _, reserved := range t.reservations[key] {
				at := time.UnixMilli(reserved.atMS).UTC()
				if !reserved.visible || at.Before(now.Add(-duration)) || at.After(now) {
					continue
				}
				state.ReservedValue += reserved.value
				windowEvents = append(windowEvents, newWindowEvent(policy.Period, reserved.value, at, duration))
			}
		}
		// NextEligibleAt is an admission boundary, not merely the next event
		// expiration. When usage is 7/3, for example, expiring one request still
		// leaves the policy blocked; the boundary is the fifth expiration, when
		// usage reaches 2/3 and one new unit can actually be admitted.
		if policy.LimitValue > 0 {
			next := policyAdmissionBoundary(windowEvents, policy.Period, state.UsedValue+state.ReservedValue, policy.LimitValue, 1, now)
			if !next.After(now) {
				states = append(states, state)
				continue
			}
			state.NextEligibleAt = &next
		}
		states = append(states, state)
	}
	return states
}

func policyAdmissionBoundary(events []event, period models.Period, used, limit, needed int64, now time.Time) time.Time {
	if used+needed <= limit {
		return time.Time{}
	}
	slices.SortFunc(events, func(a, b event) int {
		switch {
		case a.expiryMS < b.expiryMS:
			return -1
		case a.expiryMS > b.expiryMS:
			return 1
		default:
			return 0
		}
	})
	total := used
	for _, item := range events {
		if item.period != period || item.expiryMS <= unixMilli(now) {
			continue
		}
		total -= item.value
		if total+needed <= limit {
			return time.UnixMilli(item.expiryMS).UTC()
		}
	}
	if duration := periodDuration[period]; duration > 0 {
		return now.Add(duration)
	}
	return time.Time{}
}

func (t *Tracker) NextEligibleAt(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time) time.Time {
	return t.NextEligibleAtWithPacing(scope, metric, period, limit, needed, now, true)
}

func (t *Tracker) NextEligibleAtWithPacing(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time, pacingEnabled bool) time.Time {
	return t.NextEligibleAtWithPacingExcludingTask(scope, metric, period, limit, needed, now, pacingEnabled, "")
}

func (t *Tracker) NextEligibleAtExcludingTask(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time, excludedTaskID string) time.Time {
	return t.NextEligibleAtWithPacingExcludingTask(scope, metric, period, limit, needed, now, true, excludedTaskID)
}

func (t *Tracker) NextEligibleAtWithPacingExcludingTask(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time, pacingEnabled bool, excludedTaskID string) time.Time {
	return t.nextEligibleAtWithPacing(scope, metric, period, limit, needed, now, pacingEnabled, excludedTaskID, true)
}

func (t *Tracker) NextEligibleAtWithPacingCurrentWindowExcludingTask(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time, pacingEnabled bool, excludedTaskID string) time.Time {
	return t.nextEligibleAtWithPacing(scope, metric, period, limit, needed, now, pacingEnabled, excludedTaskID, false)
}

func (t *Tracker) nextEligibleAtWithPacing(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time, pacingEnabled bool, excludedTaskID string, includeFutureReservations bool) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextEligibleAtWithPacingLocked(scope, metric, period, limit, needed, now, pacingEnabled, excludedTaskID, includeFutureReservations)
}

func (t *Tracker) nextEligibleAtWithPacingLocked(scope ScopeRef, metric models.Metric, period models.Period, limit, needed int64, now time.Time, pacingEnabled bool, excludedTaskID string, includeFutureReservations bool) time.Time {
	if metric == models.MetricConcurrency {
		scope = canonicalScopeRef(scope)
		if t.concurrency[scope]+needed <= limit {
			return now
		}
		return now.Add(24 * time.Hour)
	}

	key := trackerUsageKey(scope, metric)
	duration := periodDuration[period]
	events := t.windowEventsLockedExcludingTask(key, period, now, excludedTaskID)
	used := t.sumWindowLocked(events, period, now, true)
	if includeFutureReservations {
		used = t.sumWindowLocked(events, period, now, false)
	}
	nextEligible := now
	if used+needed <= limit {
		nextEligible = now
	} else {
		total := used
		for _, evt := range events {
			if evt.period != period || evt.expiryMS <= unixMilli(now) || (!includeFutureReservations && evt.atMS > unixMilli(now)) {
				continue
			}
			total -= evt.value
			if total+needed <= limit {
				nextEligible = time.UnixMilli(evt.expiryMS).UTC()
				break
			}
		}
		if nextEligible.Equal(now) {
			nextEligible = now.Add(duration)
		}
	}
	if pacingEnabled && metric == models.MetricRequests {
		if spacing := pacingFloor(period, limit); spacing > 0 {
			spaced := t.nextSpacedEligibleAtLockedExcludingTask(key, period, spacing, now, excludedTaskID)
			if !includeFutureReservations {
				spaced = t.nextSpacedEligibleAtCurrentWindowLockedExcludingTask(key, period, spacing, now, excludedTaskID)
			}
			if spaced.After(nextEligible) {
				nextEligible = spaced
			}
		}
	}
	return nextEligible
}

// NextExpirationAt reports the next committed usage change for a policy
// window. It is deliberately distinct from NextEligibleAt: the first expiry
// advances a progress bar, while eligibility may require several expirations.
func (t *Tracker) NextExpirationAt(scope ScopeRef, metric models.Metric, period models.Period, now time.Time) time.Time {
	if t == nil || metric == models.MetricConcurrency {
		return time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	key := trackerUsageKey(scope, metric)
	t.pruneLocked(key, now)
	expiryMS := t.nextExpiry[windowIdentity{Key: key, Period: period}]
	if expiryMS <= unixMilli(now) {
		return time.Time{}
	}
	return time.UnixMilli(expiryMS).UTC()
}

func (t *Tracker) usedLocked(scope ScopeRef, metric models.Metric, period models.Period, now time.Time) int64 {
	return t.usedLockedExcludingTask(scope, metric, period, now, "")
}

func (t *Tracker) usedLockedExcludingTask(scope ScopeRef, metric models.Metric, period models.Period, now time.Time, excludedTaskID string) int64 {
	if metric == models.MetricConcurrency {
		return t.concurrency[canonicalScopeRef(scope)]
	}
	key := trackerUsageKey(scope, metric)
	duration := periodDuration[period]
	_ = duration
	events := t.windowEventsLockedExcludingTask(key, period, now, excludedTaskID)
	return t.sumWindowLocked(events, period, now, true)
}

func (t *Tracker) recordLocked(scopes []ScopeRef, metric models.Metric, value int64, now time.Time) error {
	for _, scope := range scopes {
		key := trackerUsageKey(scope, metric)
		periods := t.activePeriodsLocked(key)
		for _, period := range periods {
			t.appendEventLocked(key, period, value, now)
		}
		t.pruneLocked(key, now)
	}
	return nil
}

func addActiveWindow(active activeWindows, key usageKey, period models.Period) {
	if periodDuration[period] <= 0 {
		return
	}
	periods := active[key]
	if periods == nil {
		periods = make(map[models.Period]struct{})
		active[key] = periods
	}
	periods[period] = struct{}{}
}

func (t *Tracker) activePeriodsLocked(key usageKey) []models.Period {
	periods := t.active[key]
	if len(periods) == 0 && !t.strictActive {
		return []models.Period{
			models.PeriodSecond,
			models.PeriodMinute,
			models.PeriodHour,
			models.PeriodDay,
			models.PeriodMonth,
		}
	}
	out := make([]models.Period, 0, len(periods))
	for period := range periods {
		out = append(out, period)
	}
	return out
}

func unixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func bucketWidth(period models.Period) time.Duration {
	switch period {
	case models.PeriodHour:
		return time.Minute
	case models.PeriodDay:
		return 5 * time.Minute
	case models.PeriodMonth:
		return time.Hour
	default:
		return time.Millisecond
	}
}

func ceilMillis(value int64, width time.Duration) int64 {
	step := width.Milliseconds()
	if step <= 1 {
		return value
	}
	return ((value + step - 1) / step) * step
}

func newWindowEvent(period models.Period, value int64, at time.Time, duration time.Duration) event {
	atMS := unixMilli(at)
	expiryMS := ceilMillis(at.Add(duration).UTC().UnixMilli(), bucketWidth(period))
	return event{atMS: atMS, expiryMS: expiryMS, period: period, value: value}
}

func checkpointDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func decodeCheckpoint(payload []byte, checksum string, formatVersion int, policyVersion, expectedPolicyVersion int64, period models.Period, now time.Time) ([]event, bool) {
	if formatVersion != checkpointFormatVersion || policyVersion != expectedPolicyVersion || checksum == "" || checkpointDigest(payload) != checksum {
		return nil, false
	}
	var decoded checkpointPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, false
	}
	if len(decoded.Events) > maxExactExpirationGroups+1_024 {
		return nil, false
	}
	nowMS := unixMilli(now)
	events := make([]event, 0, len(decoded.Events))
	for _, item := range decoded.Events {
		if item.Value <= 0 || item.AtMS <= 0 || item.ExpiryMS <= item.AtMS {
			return nil, false
		}
		if item.ExpiryMS <= nowMS {
			continue
		}
		events = appendWindowEvent(events, event{atMS: item.AtMS, expiryMS: item.ExpiryMS, period: period, value: item.Value})
	}
	return events, true
}

func encodeCheckpoint(events []event, period models.Period, now time.Time) ([]byte, string, int64, int64, int64, error) {
	payload := checkpointPayload{Events: make([]checkpointEvent, 0, len(events))}
	nowMS := unixMilli(now)
	var startMS, endMS, nextMS int64
	for _, item := range events {
		if item.period != period || item.expiryMS <= nowMS || item.value <= 0 {
			continue
		}
		payload.Events = append(payload.Events, checkpointEvent{AtMS: item.atMS, ExpiryMS: item.expiryMS, Value: item.value})
		if startMS == 0 || item.atMS < startMS {
			startMS = item.atMS
		}
		if item.expiryMS > endMS {
			endMS = item.expiryMS
		}
		if nextMS == 0 || item.expiryMS < nextMS {
			nextMS = item.expiryMS
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", 0, 0, 0, err
	}
	return encoded, checkpointDigest(encoded), startMS, endMS, nextMS, nil
}

// FlushCheckpoints persists a bounded release schedule for every currently
// active policy. Normal startup can hydrate from these rows without reading
// request_logs, regardless of lifetime request volume.
func (t *Tracker) FlushCheckpoints(ctx context.Context, now time.Time) error {
	if t == nil || t.store == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	states := t.PersistablePolicyStates(t.currentPolicies(), now)
	t.mu.Lock()
	policies := append([]models.LimitPolicy(nil), t.policies...)
	observed := append([]models.ObservedLimit(nil), t.observed...)
	policySegments := make([]models.LimitPolicyStateSegment, 0, len(policies))
	observedSegments := make([]models.ObservedLimitStateSegment, 0, len(observed))
	for _, policy := range policies {
		if !policy.Enabled || policy.ID == 0 || policy.Metric == models.MetricConcurrency || periodDuration[policy.Period] <= 0 {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric)
		payload, checksum, startMS, endMS, nextMS, err := encodeCheckpoint(t.events[key], policy.Period, now)
		if err != nil {
			t.mu.Unlock()
			return err
		}
		policySegments = append(policySegments, models.LimitPolicyStateSegment{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, FormatVersion: checkpointFormatVersion,
			PolicyVersion: policy.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: policy.Period,
			SegmentStartMS: startMS, SegmentEndMS: endMS, NextExpiryMS: nextMS, Payload: payload, Checksum: checksum,
		})
	}
	for _, limit := range observed {
		if !limit.Enabled || limit.ID == 0 || limit.Metric == models.MetricConcurrency || periodDuration[limit.Period] <= 0 || (limit.ExpiresAt != nil && !limit.ExpiresAt.After(now)) {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: limit.ScopeType, ID: limit.ScopeID}, limit.Metric)
		payload, checksum, startMS, endMS, nextMS, err := encodeCheckpoint(t.events[key], limit.Period, now)
		if err != nil {
			t.mu.Unlock()
			return err
		}
		observedSegments = append(observedSegments, models.ObservedLimitStateSegment{
			ObservedLimitID: limit.ID, ObservedUUID: limit.UUID, FormatVersion: checkpointFormatVersion,
			PolicyVersion: limit.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: limit.Period,
			SegmentStartMS: startMS, SegmentEndMS: endMS, NextExpiryMS: nextMS, Payload: payload, Checksum: checksum,
		})
	}
	t.mu.Unlock()
	if err := t.store.UpsertLimitStateBundle(ctx, states, policySegments, observedSegments); err != nil {
		return fmt.Errorf("persist limit checkpoints: %w", err)
	}
	return nil
}

// FlushPolicyCheckpoints persists the exact policy set used by an
// authoritative completion. Unlike the startup-wide flush, it does not depend
// on the tracker having already cached the policy catalog; this keeps embedded
// and test schedulers correct while still writing state and release segments
// in one transaction.
func (t *Tracker) FlushPolicyCheckpoints(ctx context.Context, policies []models.LimitPolicy, now time.Time) error {
	if t == nil || t.store == nil || len(policies) == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	states := t.PersistablePolicyStates(policies, now)
	t.mu.Lock()
	segments := make([]models.LimitPolicyStateSegment, 0, len(policies))
	for _, policy := range policies {
		if !policy.Enabled || policy.ID == 0 || policy.Metric == models.MetricConcurrency || periodDuration[policy.Period] <= 0 {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric)
		payload, checksum, startMS, endMS, nextMS, err := encodeCheckpoint(t.events[key], policy.Period, now)
		if err != nil {
			t.mu.Unlock()
			return err
		}
		segments = append(segments, models.LimitPolicyStateSegment{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, FormatVersion: checkpointFormatVersion,
			PolicyVersion: policy.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: policy.Period,
			SegmentStartMS: startMS, SegmentEndMS: endMS, NextExpiryMS: nextMS, Payload: payload, Checksum: checksum,
		})
	}
	t.mu.Unlock()
	if err := t.store.UpsertLimitStateBundle(ctx, states, segments, nil); err != nil {
		return fmt.Errorf("persist completion limit checkpoints: %w", err)
	}
	return nil
}

// FlushCommittedUsageCheckpoints persists only the configured and observed
// windows touched by one terminal usage commit. The tracker-owned policy
// catalog is authoritative; fallbackPolicies exists for embedded schedulers
// that have not run startup hydration yet.
func (t *Tracker) FlushCommittedUsageCheckpoints(ctx context.Context, scopes []ScopeRef, fallbackPolicies []models.LimitPolicy, requests, tokens, spend int64, now time.Time) error {
	if t == nil || t.store == nil || (requests <= 0 && tokens <= 0 && spend <= 0) {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	scopeSet := make(map[ScopeRef]struct{}, len(scopes))
	for _, scope := range scopes {
		scopeSet[canonicalScopeRef(scope)] = struct{}{}
	}
	metricSelected := func(metric models.Metric) bool {
		switch metric {
		case models.MetricRequests:
			return requests > 0
		case models.MetricTokens:
			return tokens > 0
		case models.MetricSpend:
			return spend > 0
		default:
			return false
		}
	}
	policySelected := func(scopeType models.ScopeType, scopeID uint, metric models.Metric) bool {
		if !metricSelected(metric) {
			return false
		}
		_, ok := scopeSet[canonicalScopeRef(ScopeRef{Type: scopeType, ID: scopeID})]
		return ok
	}

	t.mu.Lock()
	policies := make([]models.LimitPolicy, 0, len(t.policies))
	seenPolicies := make(map[uint]struct{}, len(t.policies)+len(fallbackPolicies))
	for _, policy := range t.policies {
		if !policy.Enabled || policy.ID == 0 || !policySelected(policy.ScopeType, policy.ScopeID, policy.Metric) {
			continue
		}
		policies = append(policies, policy)
		seenPolicies[policy.ID] = struct{}{}
	}
	for _, policy := range fallbackPolicies {
		// fallbackPolicies are the exact configured policies copied into the
		// issued dispatch permit. Do not discard that authoritative attribution
		// merely because mutable task scopes changed before completion.
		if !policy.Enabled || policy.ID == 0 || !metricSelected(policy.Metric) {
			continue
		}
		if _, exists := seenPolicies[policy.ID]; exists {
			continue
		}
		policies = append(policies, policy)
		seenPolicies[policy.ID] = struct{}{}
	}
	observed := make([]models.ObservedLimit, 0, len(t.observed))
	for _, limit := range t.observed {
		if !limit.Enabled || limit.ID == 0 || !policySelected(limit.ScopeType, limit.ScopeID, limit.Metric) || (limit.ExpiresAt != nil && !limit.ExpiresAt.After(now)) {
			continue
		}
		observed = append(observed, limit)
	}
	t.mu.Unlock()

	states := t.PersistablePolicyStates(policies, now)
	t.mu.Lock()
	policySegments := make([]models.LimitPolicyStateSegment, 0, len(policies))
	for _, policy := range policies {
		if policy.Metric == models.MetricConcurrency || periodDuration[policy.Period] <= 0 {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric)
		payload, checksum, startMS, endMS, nextMS, err := encodeCheckpoint(t.events[key], policy.Period, now)
		if err != nil {
			t.mu.Unlock()
			return err
		}
		policySegments = append(policySegments, models.LimitPolicyStateSegment{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, FormatVersion: checkpointFormatVersion,
			PolicyVersion: policy.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: policy.Period,
			SegmentStartMS: startMS, SegmentEndMS: endMS, NextExpiryMS: nextMS, Payload: payload, Checksum: checksum,
		})
	}
	observedSegments := make([]models.ObservedLimitStateSegment, 0, len(observed))
	for _, limit := range observed {
		if limit.Metric == models.MetricConcurrency || periodDuration[limit.Period] <= 0 {
			continue
		}
		key := trackerUsageKey(ScopeRef{Type: limit.ScopeType, ID: limit.ScopeID}, limit.Metric)
		payload, checksum, startMS, endMS, nextMS, err := encodeCheckpoint(t.events[key], limit.Period, now)
		if err != nil {
			t.mu.Unlock()
			return err
		}
		observedSegments = append(observedSegments, models.ObservedLimitStateSegment{
			ObservedLimitID: limit.ID, ObservedUUID: limit.UUID, FormatVersion: checkpointFormatVersion,
			PolicyVersion: limit.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: limit.Period,
			SegmentStartMS: startMS, SegmentEndMS: endMS, NextExpiryMS: nextMS, Payload: payload, Checksum: checksum,
		})
	}
	t.mu.Unlock()

	if len(states) == 0 && len(policySegments) == 0 && len(observedSegments) == 0 {
		return nil
	}
	if err := t.store.UpsertLimitStateBundle(ctx, states, policySegments, observedSegments); err != nil {
		return fmt.Errorf("persist committed usage checkpoints: %w", err)
	}
	return nil
}

// RefreshPolicy applies a rare control-plane mutation without resetting
// unrelated tracker state. New windows rebuild only their bounded horizon and
// only through a streamed request-log cursor.
func (t *Tracker) RefreshPolicy(ctx context.Context, policy models.LimitPolicy, removed bool, now time.Time) error {
	if t == nil || t.store == nil || policy.ID == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if removed || !policy.Enabled {
		t.mu.Lock()
		kept := t.policies[:0]
		for _, existing := range t.policies {
			if existing.ID != policy.ID {
				kept = append(kept, existing)
			}
		}
		t.policies = kept
		t.rebuildActiveWindowsLocked(now)
		t.mu.Unlock()
		return t.store.DeleteLimitPolicyStateBundle(ctx, policy.ID)
	}
	key := trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric)
	var maxID uint
	if err := t.store.DB().WithContext(ctx).Model(&models.RequestLog{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
		return err
	}
	t.mu.Lock()
	alreadyActive := false
	for _, existing := range t.policies {
		if existing.ID != policy.ID && existing.Enabled && trackerUsageKey(ScopeRef{Type: existing.ScopeType, ID: existing.ScopeID}, existing.Metric) == key && existing.Period == policy.Period {
			alreadyActive = true
		}
	}
	replaced := false
	for index := range t.policies {
		if t.policies[index].ID == policy.ID {
			if t.policies[index].Enabled && trackerUsageKey(ScopeRef{Type: t.policies[index].ScopeType, ID: t.policies[index].ScopeID}, t.policies[index].Metric) == key && t.policies[index].Period == policy.Period {
				alreadyActive = true
			}
			t.policies[index] = policy
			replaced = true
			break
		}
	}
	if !replaced {
		t.policies = append(t.policies, policy)
	}
	t.strictActive = true
	t.rebuildActiveWindowsLocked(now)
	t.mu.Unlock()
	if alreadyActive || policy.Metric == models.MetricConcurrency {
		return t.FlushCheckpoints(ctx, now)
	}
	duration := periodDuration[policy.Period]
	if duration <= 0 {
		return nil
	}
	window := make(activeWindows)
	addActiveWindow(window, key, policy.Period)
	rebuilt := make(map[usageKey][]event, 1)
	cutoff := now.Add(-min(duration, maxTrackedDuration))
	rows, err := t.store.DB().WithContext(ctx).Model(&models.RequestLog{}).
		Where("id <= ? AND task_state IN ?", maxID, []string{"completed", "failed", "cancelled"}).
		Where("(started_at >= ? OR (started_at IS NULL AND queued_at >= ?) OR (started_at IS NULL AND queued_at IS NULL AND created_at >= ?))", cutoff, cutoff, cutoff).
		Order("id ASC").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var log models.RequestLog
		if err := t.store.DB().ScanRows(rows, &log); err != nil {
			return err
		}
		at := persistedUsageAt(log)
		if at.IsZero() || at.Before(cutoff) || at.After(now) || !persistedRequestConsumesUsage(log) {
			continue
		}
		scopes := persistedUsageScopes(log)
		appendHydratedEventTo(rebuilt, scopes, models.MetricRequests, 1, at, window)
		appendHydratedEventTo(rebuilt, scopes, models.MetricTokens, persistedTokenValue(log), at, window)
		appendHydratedEventTo(rebuilt, scopes, models.MetricSpend, persistedSpendValue(log), at, window)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	for _, item := range rebuilt[key] {
		t.events[key] = appendWindowEvent(t.events[key], item)
	}
	t.rebuildNextExpiriesLocked(now)
	t.mu.Unlock()
	return t.FlushCheckpoints(ctx, now)
}

func (t *Tracker) rebuildActiveWindowsLocked(now time.Time) {
	next := make(activeWindows, len(t.policies)+len(t.observed))
	for _, policy := range t.policies {
		if policy.Enabled && policy.Metric != models.MetricConcurrency {
			addActiveWindow(next, trackerUsageKey(ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}, policy.Metric), policy.Period)
		}
	}
	for _, limit := range t.observed {
		if limit.Enabled && limit.Metric != models.MetricConcurrency && (limit.ExpiresAt == nil || limit.ExpiresAt.After(now)) {
			addActiveWindow(next, trackerUsageKey(ScopeRef{Type: limit.ScopeType, ID: limit.ScopeID}, limit.Metric), limit.Period)
		}
	}
	for key, events := range t.events {
		keep := events[:0]
		for _, item := range events {
			if _, active := next[key][item.period]; active {
				keep = append(keep, item)
			}
		}
		if len(keep) == 0 {
			delete(t.events, key)
			delete(t.reservations, key)
		} else {
			t.events[key] = keep
		}
	}
	t.active = next
	t.rebuildNextExpiriesLocked(now)
}

func (t *Tracker) currentPolicies() []models.LimitPolicy {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]models.LimitPolicy(nil), t.policies...)
}

func (t *Tracker) rebuildNextExpiriesLocked(now time.Time) {
	clear(t.nextExpiry)
	nowMS := unixMilli(now)
	for key, events := range t.events {
		for _, item := range events {
			if item.expiryMS <= nowMS {
				continue
			}
			identity := windowIdentity{Key: key, Period: item.period}
			if current := t.nextExpiry[identity]; current == 0 || item.expiryMS < current {
				t.nextExpiry[identity] = item.expiryMS
			}
		}
	}
}

func appendWindowEvent(events []event, next event) []event {
	if next.value <= 0 || next.expiryMS <= 0 {
		return events
	}
	for i := len(events) - 1; i >= 0 && i >= len(events)-8; i-- {
		if events[i].period == next.period && events[i].expiryMS == next.expiryMS {
			events[i].value += next.value
			if next.atMS > events[i].atMS {
				events[i].atMS = next.atMS
			}
			return events
		}
	}
	if len(events) < maxExactExpirationGroups+1_024 {
		return append(events, next)
	}
	// The cap is above the number of distinct millisecond expirations possible
	// in an exact minute window. If clock irregularity reaches it, merge into
	// the latest matching period and move expiry later, never earlier.
	latest := -1
	for i := range events {
		if events[i].period == next.period && (latest < 0 || events[i].expiryMS > events[latest].expiryMS) {
			latest = i
		}
	}
	if latest >= 0 {
		events[latest].value += next.value
		if next.expiryMS > events[latest].expiryMS {
			events[latest].expiryMS = next.expiryMS
		}
		if next.atMS > events[latest].atMS {
			events[latest].atMS = next.atMS
		}
	}
	return events
}

func (t *Tracker) appendEventLocked(key usageKey, period models.Period, value int64, at time.Time) {
	next := newWindowEvent(period, value, at, periodDuration[period])
	t.events[key] = appendWindowEvent(t.events[key], next)
	identity := windowIdentity{Key: key, Period: period}
	if current := t.nextExpiry[identity]; current == 0 || next.expiryMS < current {
		t.nextExpiry[identity] = next.expiryMS
	}
}

func (t *Tracker) pruneLocked(key usageKey, now time.Time) {
	events := t.events[key]
	keep := events[:0]
	for _, evt := range events {
		// Clear every period represented by the old slice before rebuilding the
		// direct next-expiration index. Standalone trackers may not have an
		// active-policy map, but their rolling expiry timers must still advance.
		delete(t.nextExpiry, windowIdentity{Key: key, Period: evt.period})
		if evt.expiryMS > unixMilli(now) {
			keep = append(keep, evt)
		}
	}
	t.events[key] = keep
	for period := range t.active[key] {
		delete(t.nextExpiry, windowIdentity{Key: key, Period: period})
	}
	for _, item := range keep {
		identity := windowIdentity{Key: key, Period: item.period}
		if current := t.nextExpiry[identity]; current == 0 || item.expiryMS < current {
			t.nextExpiry[identity] = item.expiryMS
		}
	}
	reserved := t.reservations[key]
	keepReserved := reserved[:0]
	for _, evt := range reserved {
		if evt.atMS >= unixMilli(now.Add(-maxTrackedDuration)) {
			keepReserved = append(keepReserved, evt)
		}
	}
	t.reservations[key] = keepReserved
}

func (t *Tracker) releaseLocked(taskID string) {
	for key, events := range t.reservations {
		keep := events[:0]
		for _, evt := range events {
			if evt.taskID != taskID {
				keep = append(keep, evt)
			}
		}
		t.reservations[key] = keep
	}
}

func (t *Tracker) runtimeEventsLocked(key usageKey, period models.Period) []event {
	events := make([]event, 0, len(t.events[key])+len(t.reservations[key]))
	for _, evt := range t.events[key] {
		if evt.period == period {
			events = append(events, evt)
		}
	}
	duration := periodDuration[period]
	for _, reserved := range t.reservations[key] {
		if !reserved.visible {
			continue
		}
		events = append(events, newWindowEvent(period, reserved.value, time.UnixMilli(reserved.atMS).UTC(), duration))
	}
	return events
}

func (t *Tracker) windowEventsLocked(key usageKey, period models.Period, now time.Time) []event {
	return t.windowEventsLockedExcludingTask(key, period, now, "")
}

func (t *Tracker) windowEventsLockedExcludingTask(key usageKey, period models.Period, now time.Time, excludedTaskID string) []event {
	t.pruneLocked(key, now)
	events := make([]event, 0, len(t.events[key])+len(t.reservations[key]))
	for _, evt := range t.events[key] {
		if evt.period == period {
			events = append(events, evt)
		}
	}
	duration := periodDuration[period]
	for _, reserved := range t.reservations[key] {
		if excludedTaskID != "" && reserved.taskID == excludedTaskID {
			continue
		}
		events = append(events, newWindowEvent(period, reserved.value, time.UnixMilli(reserved.atMS).UTC(), duration))
	}
	slices.SortFunc(events, func(a, b event) int {
		switch {
		case a.expiryMS < b.expiryMS:
			return -1
		case a.expiryMS > b.expiryMS:
			return 1
		default:
			return 0
		}
	})
	return events
}

func (t *Tracker) sumWindowLocked(events []event, period models.Period, now time.Time, excludeFuture bool) int64 {
	var total int64
	nowMS := unixMilli(now)
	for _, evt := range events {
		if evt.period == period && evt.expiryMS > nowMS && (!excludeFuture || evt.atMS <= nowMS) {
			total += evt.value
		}
	}
	return total
}

func pacingFloor(period models.Period, limit int64) time.Duration {
	if limit <= 0 || !pacedRequestPeriods[period] {
		return 0
	}
	duration := periodDuration[period]
	if duration <= 0 {
		return 0
	}
	spacing := time.Duration(int64(duration) / limit)
	if spacing <= 0 {
		return 0
	}
	return spacing
}

func RequestPacingFloor(period models.Period, limit int64) time.Duration {
	return pacingFloor(period, limit)
}

func (t *Tracker) nextSpacedEligibleAtLocked(key usageKey, period models.Period, spacing time.Duration, now time.Time) time.Time {
	return t.nextSpacedEligibleAtLockedExcludingTask(key, period, spacing, now, "")
}

func (t *Tracker) nextSpacedEligibleAtLockedExcludingTask(key usageKey, period models.Period, spacing time.Duration, now time.Time, excludedTaskID string) time.Time {
	events := t.windowEventsLockedExcludingTask(key, period, now, excludedTaskID)
	if len(events) == 0 {
		return now
	}
	latestMS := int64(0)
	for _, evt := range events {
		if evt.atMS > latestMS {
			latestMS = evt.atMS
		}
	}
	latest := time.UnixMilli(latestMS).UTC().Add(spacing)
	if latest.After(now) {
		return latest
	}
	return now
}

func (t *Tracker) nextSpacedEligibleAtCurrentWindowLockedExcludingTask(key usageKey, period models.Period, spacing time.Duration, now time.Time, excludedTaskID string) time.Time {
	events := t.windowEventsLockedExcludingTask(key, period, now, excludedTaskID)
	latestMS := int64(0)
	for _, evt := range events {
		if evt.atMS > unixMilli(now) {
			continue
		}
		if evt.atMS > latestMS {
			latestMS = evt.atMS
		}
	}
	if latestMS == 0 {
		return now
	}
	spaced := time.UnixMilli(latestMS).UTC().Add(spacing)
	if spaced.After(now) {
		return spaced
	}
	return now
}
