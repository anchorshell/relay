package limits

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
	"gorm.io/gorm/logger"
)

type trackerSQLRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *trackerSQLRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (r *trackerSQLRecorder) Info(context.Context, string, ...any)     {}
func (r *trackerSQLRecorder) Warn(context.Context, string, ...any)     {}
func (r *trackerSQLRecorder) Error(context.Context, string, ...any)    {}
func (r *trackerSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.mu.Lock()
	r.statements = append(r.statements, statement)
	r.mu.Unlock()
}

func (r *trackerSQLRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.statements)
}

func (r *trackerSQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

func (r *trackerSQLRecorder) reset() {
	r.mu.Lock()
	r.statements = nil
	r.mu.Unlock()
}

func TestMinuteWindowRetainsEventsOlderThanSecond(t *testing.T) {
	tracker := NewTracker(nil)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 1}
	base := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	for _, offset := range []time.Duration{
		0,
		2 * time.Second,
		4 * time.Second,
		6 * time.Second,
		8 * time.Second,
	} {
		if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, base.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}

	next := tracker.NextEligibleAt(scope, models.MetricRequests, models.PeriodMinute, 5, 1, base.Add(10*time.Second))
	want := base.Add(1 * time.Minute)
	if !next.Equal(want) {
		t.Fatalf("expected next eligible at %s, got %s", want, next)
	}
}

func TestPolicyStatesOnlyTrackEnabledConfiguredPoliciesWithExactRollingReset(t *testing.T) {
	tracker := NewTracker(nil)
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 7, Key: "endpoint-public-id"}
	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, base.Add(-50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, base.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	tracker.ReserveRuntime("active", []ScopeRef{scope}, models.MetricRequests, 1, base)

	policies := []models.LimitPolicy{
		{ID: 1, UUID: "policy-active", ScopeType: models.ScopeEndpoint, ScopeID: 7, Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 2, Enabled: true, UpdatedAt: base.Add(-time.Hour)},
		{ID: 2, UUID: "policy-disabled", ScopeType: models.ScopeEndpoint, ScopeID: 7, Metric: models.MetricRequests, Period: models.PeriodDay, Enabled: false, UpdatedAt: base.Add(-time.Hour)},
		{ID: 3, UUID: "policy-other", ScopeType: models.ScopeProvider, ScopeID: 9, Metric: models.MetricTokens, Period: models.PeriodHour, Enabled: true, UpdatedAt: base.Add(-time.Hour)},
	}
	states := tracker.CurrentPolicyStates(policies, base)
	if len(states) != 2 {
		t.Fatalf("state rows = %d, want only two enabled policies: %#v", len(states), states)
	}
	state := states[0]
	if state.PolicyID != 1 || state.UsedValue != 2 || state.ReservedValue != 1 {
		t.Fatalf("unexpected active policy state: %#v", state)
	}
	if !state.WindowStart.Equal(base.Add(-time.Minute)) || !state.WindowEnd.Equal(base) {
		t.Fatalf("expected exact rolling bounds, got start=%s end=%s", state.WindowStart, state.WindowEnd)
	}
	// Two committed requests plus one visible reservation exceed the two-slot
	// policy. The first expiry still leaves 2/2 occupied; capacity becomes
	// available only when the second committed request expires.
	wantReset := base.Add(50 * time.Second)
	if state.NextEligibleAt == nil || !state.NextEligibleAt.Equal(wantReset) {
		t.Fatalf("next eligible = %v, want %s", state.NextEligibleAt, wantReset)
	}

	advanced := tracker.CurrentPolicyStates(policies, base.Add(11*time.Second))[0]
	if advanced.UsedValue != 1 || advanced.ReservedValue != 1 {
		t.Fatalf("expired event did not advance lazily: %#v", advanced)
	}
}

func TestPolicyStateNextEligibleAtWaitsUntilCapacityIsActuallyAvailable(t *testing.T) {
	tracker := NewTracker(nil)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 7, Key: "endpoint-public-id"}
	for _, age := range []time.Duration{
		50 * time.Second,
		45 * time.Second,
		40 * time.Second,
		35 * time.Second,
		30 * time.Second,
		25 * time.Second,
		20 * time.Second,
	} {
		if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	policy := models.LimitPolicy{
		ID: 1, UUID: "policy-rpm", ScopeType: models.ScopeEndpoint, ScopeID: scope.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 5,
		Enabled: true, UpdatedAt: now.Add(-time.Hour),
	}
	state := tracker.CurrentPolicyStates([]models.LimitPolicy{policy}, now)[0]
	wantForFive := now.Add(20 * time.Second) // 7 -> 4, so one request fits.
	if state.NextEligibleAt == nil || !state.NextEligibleAt.Equal(wantForFive) {
		t.Fatalf("7/5 next eligible = %v, want %s", state.NextEligibleAt, wantForFive)
	}

	policy.LimitValue = 3
	state = tracker.CurrentPolicyStates([]models.LimitPolicy{policy}, now)[0]
	wantForThree := now.Add(30 * time.Second) // 7 -> 2, so one request fits.
	if state.NextEligibleAt == nil || !state.NextEligibleAt.Equal(wantForThree) {
		t.Fatalf("7/3 next eligible = %v, want %s", state.NextEligibleAt, wantForThree)
	}

	if nextExpiry := tracker.NextExpirationAt(scope, models.MetricRequests, models.PeriodMinute, now); !nextExpiry.Equal(now.Add(10 * time.Second)) {
		t.Fatalf("next progress update = %s, want first expiry %s", nextExpiry, now.Add(10*time.Second))
	}
}

func TestTrackerCommitPerformsNoDatabaseIO(t *testing.T) {
	st := testutil.NewStore(t)
	recorder := &trackerSQLRecorder{}
	st.DB().Logger = recorder
	tracker := NewTracker(st)
	now := time.Now().UTC()
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 1, Key: "public-endpoint"}
	tracker.ReserveRuntime("request-1", []ScopeRef{scope}, models.MetricRequests, 1, now)
	if err := tracker.Commit(context.Background(), "request-1", []ScopeRef{scope}, 1, 10, 25, now); err != nil {
		t.Fatal(err)
	}
	if got := recorder.count(); got != 0 {
		t.Fatalf("Tracker.Commit executed %d SQL statements; persistence must happen after the tracker lock", got)
	}
	if used := tracker.Used(scope, models.MetricRequests, models.PeriodMinute, now); used != 1 {
		t.Fatalf("committed runtime usage = %d, want 1", used)
	}
}

func TestHydrateRecentUsageUsesOnlyActivePolicyHorizon(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	policy := models.LimitPolicy{
		ScopeType:  models.ScopeGlobal,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 10,
		Enabled:    true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	for i, startedAt := range []time.Time{base.Add(-2 * time.Minute), base.Add(-10 * time.Second)} {
		finishedAt := startedAt.Add(time.Second)
		if err := st.Create(ctx, &models.RequestLog{
			RequestID:  string(rune('a' + i)),
			TaskState:  "completed",
			StatusCode: 200,
			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			CreatedAt:  startedAt,
			UpdatedAt:  finishedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	recorder := &trackerSQLRecorder{}
	st.DB().Logger = recorder
	tracker := NewTracker(st)
	if err := tracker.HydrateRecentUsage(ctx, base); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(ScopeRef{Type: models.ScopeGlobal}, models.MetricRequests, models.PeriodMinute, base); got != 1 {
		t.Fatalf("hydrated minute usage = %d, want only the recent request", got)
	}
	var requestLogQuery string
	for _, statement := range recorder.snapshot() {
		normalized := strings.ToLower(statement)
		if strings.Contains(normalized, "from `request_logs`") && !strings.Contains(normalized, "max(coalesce(") {
			requestLogQuery = statement
			break
		}
	}
	if requestLogQuery == "" {
		t.Fatal("expected one bounded request_logs rebuild query")
	}
	if !strings.Contains(requestLogQuery, "2026-07-16 11:59:00") {
		t.Fatalf("request-log rebuild did not use the active one-minute horizon: %s", requestLogQuery)
	}
}

func TestCheckpointRestartHydrationUsesWatermarkWithoutReplayingRequestHistory(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	policy := models.LimitPolicy{
		ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 10, Enabled: true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	started := now.Add(-10 * time.Second)
	finished := started.Add(time.Second)
	if err := st.Create(ctx, &models.RequestLog{
		RequestID: "checkpoint-restart", TaskState: "completed", StatusCode: 200,
		StartedAt: &started, FinishedAt: &finished, CreatedAt: started, UpdatedAt: finished,
	}); err != nil {
		t.Fatal(err)
	}
	first := NewTracker(st)
	if err := first.HydrateRecentUsage(ctx, now); err != nil {
		t.Fatal(err)
	}

	recorder := &trackerSQLRecorder{}
	st.DB().Logger = recorder
	second := NewTracker(st)
	if err := second.HydrateRecentUsage(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, statement := range recorder.snapshot() {
		normalized := strings.ToLower(statement)
		if strings.Contains(normalized, "from `request_logs`") && !strings.Contains(normalized, "max(coalesce(") {
			t.Fatalf("normal restart replayed request history instead of using checkpoints: %s", statement)
		}
	}
	if got := second.Used(ScopeRef{Type: models.ScopeGlobal}, models.MetricRequests, models.PeriodMinute, now.Add(time.Second)); got != 1 {
		t.Fatalf("checkpoint usage=%d, want 1", got)
	}
}

func TestCurrentCheckpointOlderThanCanonicalUsageIsRebuilt(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	policy := models.LimitPolicy{
		ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodDay,
		LimitValue: 22, Enabled: true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	emptyPayload := []byte(`{"events":[]}`)
	if err := st.UpsertLimitStateBundle(ctx,
		[]models.LimitPolicyState{{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, PolicyVersion: policy.UpdatedAt.UTC().UnixNano(),
			WindowStart: now.Add(-24 * time.Hour), WindowEnd: now,
		}},
		[]models.LimitPolicyStateSegment{{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, FormatVersion: checkpointFormatVersion,
			PolicyVersion: policy.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: policy.Period,
			Payload: emptyPayload, Checksum: checkpointDigest(emptyPayload),
		}}, nil,
	); err != nil {
		t.Fatal(err)
	}
	checkpointAt := now.Add(-time.Minute)
	if err := st.DB().Model(&models.LimitPolicyStateSegment{}).
		Where("policy_id = ?", policy.ID).
		Update("updated_at", checkpointAt).Error; err != nil {
		t.Fatal(err)
	}
	started := now.Add(-10 * time.Second)
	finished := started.Add(time.Second)
	if err := st.Create(ctx, &models.RequestLog{
		RequestID: "usage-after-checkpoint", TaskState: "completed", StatusCode: 200,
		StartedAt: &started, FinishedAt: &finished, CreatedAt: started, UpdatedAt: finished,
	}); err != nil {
		t.Fatal(err)
	}

	tracker := NewTracker(st)
	if err := tracker.HydrateRecentUsage(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(ScopeRef{Type: models.ScopeGlobal}, models.MetricRequests, models.PeriodDay, now); got != 1 {
		t.Fatalf("stale checkpoint repair usage=%d, want 1", got)
	}
	segments, err := st.ListLimitPolicyStateSegments(ctx, []uint{policy.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || !segments[0].UpdatedAt.After(checkpointAt) {
		t.Fatalf("stale checkpoint was not rewritten: %#v", segments)
	}
}

func TestLegacyCheckpointPerformsOneBoundedRepairFromRequestHistory(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	policy := models.LimitPolicy{
		ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 10, Enabled: true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	started := now.Add(-10 * time.Second)
	finished := started.Add(time.Second)
	if err := st.Create(ctx, &models.RequestLog{
		RequestID: "legacy-checkpoint-repair", TaskState: "completed", StatusCode: 400,
		StartedAt: &started, FinishedAt: &finished, CreatedAt: started, UpdatedAt: finished,
	}); err != nil {
		t.Fatal(err)
	}
	emptyPayload := []byte(`{"events":[]}`)
	if err := st.UpsertLimitStateBundle(ctx,
		[]models.LimitPolicyState{{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, PolicyVersion: policy.UpdatedAt.UTC().UnixNano(),
			WindowStart: now.Add(-time.Minute), WindowEnd: now,
		}},
		[]models.LimitPolicyStateSegment{{
			PolicyID: policy.ID, PolicyUUID: policy.UUID, FormatVersion: checkpointFormatVersion - 1,
			PolicyVersion: policy.UpdatedAt.UTC().UnixNano(), StateVersion: 1, WindowPeriod: policy.Period,
			Payload: emptyPayload, Checksum: checkpointDigest(emptyPayload),
		}}, nil,
	); err != nil {
		t.Fatal(err)
	}

	tracker := NewTracker(st)
	if err := tracker.HydrateRecentUsage(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(ScopeRef{Type: models.ScopeGlobal}, models.MetricRequests, models.PeriodMinute, now); got != 1 {
		t.Fatalf("repaired checkpoint usage=%d, want completed 400 request to count", got)
	}
	segments, err := st.ListLimitPolicyStateSegments(ctx, []uint{policy.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].FormatVersion != checkpointFormatVersion {
		t.Fatalf("checkpoint was not upgraded after bounded repair: %#v", segments)
	}
}

func TestLongWindowBucketsNeverReleaseCapacityEarly(t *testing.T) {
	tracker := NewTracker(nil)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 7}
	at := time.Date(2026, 7, 19, 12, 0, 30, 0, time.UTC)
	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, at); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(scope, models.MetricRequests, models.PeriodHour, at.Add(time.Hour+29*time.Second)); got != 1 {
		t.Fatalf("hour bucket released early: used=%d", got)
	}
	if got := tracker.Used(scope, models.MetricRequests, models.PeriodHour, at.Add(time.Hour+31*time.Second)); got != 0 {
		t.Fatalf("hour bucket did not release at conservative boundary: used=%d", got)
	}
	if got := tracker.Used(scope, models.MetricRequests, models.PeriodMinute, at.Add(time.Minute+time.Millisecond)); got != 0 {
		t.Fatalf("exact minute event did not expire at its millisecond boundary: used=%d", got)
	}
}

func TestHydrateRecentUsageCountsCompletedProviderErrorsButNotPreDispatchFailures(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 19, 0, 0, 0, time.UTC)
	policy := models.LimitPolicy{
		ScopeType: models.ScopeGlobal, Metric: models.MetricRequests,
		Period: models.PeriodMinute, LimitValue: 10, Enabled: true,
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	for index, item := range []struct {
		state   string
		status  int
		started bool
	}{
		{state: "completed", status: 400, started: true},
		{state: "completed", status: 503, started: true},
		{state: "failed", status: 503, started: false},
	} {
		at := now.Add(-time.Duration(index+1) * time.Second)
		finishedAt := at.Add(100 * time.Millisecond)
		log := models.RequestLog{
			RequestID: fmt.Sprintf("provider-result-%d", index), TaskState: item.state, StatusCode: item.status,
			FinishedAt: &finishedAt, CreatedAt: at, UpdatedAt: finishedAt,
		}
		if item.started {
			log.StartedAt = &at
		}
		if err := st.Create(ctx, &log); err != nil {
			t.Fatal(err)
		}
	}

	tracker := NewTracker(st)
	if err := tracker.HydrateRecentUsage(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(ScopeRef{Type: models.ScopeGlobal}, models.MetricRequests, models.PeriodMinute, now); got != 2 {
		t.Fatalf("hydrated request usage = %d, want completed 400 and 503 only", got)
	}
}

func TestRequestVolumeKeepsOperationalStateBoundedByActivePolicies(t *testing.T) {
	for _, requestCount := range []int{1, 100, 1000} {
		t.Run(fmt.Sprintf("requests_%d", requestCount), func(t *testing.T) {
			st := testutil.NewStore(t)
			ctx := context.Background()
			base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
			policy := models.LimitPolicy{
				ScopeType:  models.ScopeGlobal,
				Metric:     models.MetricRequests,
				Period:     models.PeriodHour,
				LimitValue: 10_000,
				Enabled:    true,
			}
			if err := st.Create(ctx, &policy); err != nil {
				t.Fatal(err)
			}
			tracker := NewTracker(st)
			logs := make([]models.RequestLog, 0, requestCount)
			for i := 0; i < requestCount; i++ {
				at := base.Add(time.Duration(i) * time.Millisecond)
				requestID := fmt.Sprintf("bounded-state-%04d", i)
				if err := tracker.Commit(ctx, requestID, []ScopeRef{{Type: models.ScopeGlobal}}, 1, 0, 0, at); err != nil {
					t.Fatal(err)
				}
				finishedAt := at.Add(time.Millisecond)
				logs = append(logs, models.RequestLog{
					RequestID: requestID, TaskState: "completed", StatusCode: 200,
					StartedAt: &at, FinishedAt: &finishedAt, CreatedAt: at, UpdatedAt: finishedAt,
				})
			}
			if err := st.DB().WithContext(ctx).CreateInBatches(&logs, 50).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertLimitPolicyStates(ctx, tracker.PersistablePolicyStates([]models.LimitPolicy{policy}, base.Add(time.Second))); err != nil {
				t.Fatal(err)
			}

			var requestLogs, policyStates int64
			if err := st.DB().Model(&models.RequestLog{}).Count(&requestLogs).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.DB().Model(&models.LimitPolicyState{}).Count(&policyStates).Error; err != nil {
				t.Fatal(err)
			}
			if st.DB().Migrator().HasTable("usage_rollups") {
				t.Fatal("usage_rollups must not exist in the current schema")
			}
			if requestLogs != int64(requestCount) || policyStates != 1 {
				t.Fatalf("unexpected growth: request_logs=%d policy_states=%d", requestLogs, policyStates)
			}
		})
	}
}

func TestRequestRateUsesPacingFloorWithinMinute(t *testing.T) {
	tracker := NewTracker(nil)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 1}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, base); err != nil {
		t.Fatal(err)
	}

	next := tracker.NextEligibleAt(scope, models.MetricRequests, models.PeriodMinute, 5, 1, base)
	want := base.Add(12 * time.Second)
	if !next.Equal(want) {
		t.Fatalf("expected paced next eligible at %s, got %s", want, next)
	}
}

func TestRequestPacingOnlyAppliesToSecondAndMinutePeriods(t *testing.T) {
	tests := []struct {
		name   string
		period models.Period
		limit  int64
		want   time.Duration
	}{
		{name: "second", period: models.PeriodSecond, limit: 5, want: 200 * time.Millisecond},
		{name: "minute", period: models.PeriodMinute, limit: 5, want: 12 * time.Second},
		{name: "hour", period: models.PeriodHour, limit: 5, want: 0},
		{name: "day", period: models.PeriodDay, limit: 5, want: 0},
		{name: "month", period: models.PeriodMonth, limit: 5, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RequestPacingFloor(test.period, test.limit); got != test.want {
				t.Fatalf("RequestPacingFloor(%s, %d) = %s, want %s", test.period, test.limit, got, test.want)
			}
		})
	}
}

func TestRequestRateHourLimitDoesNotPaceBelowHardCap(t *testing.T) {
	tracker := NewTracker(nil)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 1}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, base); err != nil {
		t.Fatal(err)
	}

	next := tracker.NextEligibleAt(scope, models.MetricRequests, models.PeriodHour, 5, 1, base)
	if !next.Equal(base) {
		t.Fatalf("expected RPH request below the hard cap to be immediately eligible at %s, got %s", base, next)
	}

	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 4, base); err != nil {
		t.Fatal(err)
	}
	next = tracker.NextEligibleAt(scope, models.MetricRequests, models.PeriodHour, 5, 1, base)
	if want := base.Add(time.Hour); !next.Equal(want) {
		t.Fatalf("expected RPH hard cap to remain enforced until %s, got %s", want, next)
	}
}

func TestRequestRateCanDisablePacingFloorWithinMinute(t *testing.T) {
	tracker := NewTracker(nil)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 1}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

	if err := tracker.RecordWindow(context.Background(), []ScopeRef{scope}, models.MetricRequests, 1, base); err != nil {
		t.Fatal(err)
	}

	next := tracker.NextEligibleAtWithPacing(scope, models.MetricRequests, models.PeriodMinute, 5, 1, base, false)
	if !next.Equal(base) {
		t.Fatalf("expected unpaced request to be immediately eligible at %s, got %s", base, next)
	}
}

func TestReservationsCascadePacingFloor(t *testing.T) {
	tracker := NewTracker(nil)
	scope := ScopeRef{Type: models.ScopeEndpoint, ID: 1}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

	tracker.Reserve("first", []ScopeRef{scope}, models.MetricRequests, 1, base)
	firstNext := tracker.NextEligibleAt(scope, models.MetricRequests, models.PeriodMinute, 5, 1, base)
	if want := base.Add(12 * time.Second); !firstNext.Equal(want) {
		t.Fatalf("expected first paced reservation at %s, got %s", want, firstNext)
	}

	tracker.Reserve("second", []ScopeRef{scope}, models.MetricRequests, 1, firstNext)
	secondNext := tracker.NextEligibleAt(scope, models.MetricRequests, models.PeriodMinute, 5, 1, base)
	if want := base.Add(24 * time.Second); !secondNext.Equal(want) {
		t.Fatalf("expected second paced reservation at %s, got %s", want, secondNext)
	}
}
