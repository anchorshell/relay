package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func TestCapacitySnapshotReturnsBackendUsageAndRateLimitedState(t *testing.T) {
	ctx := context.Background()
	e, st, tracker, endpoint := usageStatsTestAPI(t)
	now := time.Now().UTC()
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodDay,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 5, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	snapshot := capacitySnapshotResponse(t, e)
	model := capacityModel(t, snapshot, endpoint.UUID)
	if model.CapacityState != "rate-limited" {
		t.Fatalf("expected capacity snapshot to mark 5/5 endpoint as rate-limited, got %q", model.CapacityState)
	}
	row := capacityLimitRow(t, model, models.MetricRequests, models.PeriodDay)
	if row.Used != 5 || row.Configured != 5 || row.Effective != 5 || row.Remaining != 0 {
		t.Fatalf("expected endpoint RPD row 5/5 remaining 0, got %#v", row)
	}
	if !row.Blocked || !row.BlockedUntil.Equal(row.ResetAt) || row.BlockedReason != "requests/day" {
		t.Fatalf("expected exhausted row to carry its authoritative block boundary, got %#v", row)
	}
	if model.CooldownUntil == nil || !model.CooldownUntil.Equal(row.ResetAt) {
		t.Fatalf("expected exhausted row reset to drive cooldown_until %s, got %v", row.ResetAt, model.CooldownUntil)
	}
}

func TestCapacityCooldownUntilUsesLatestExhaustedPolicyBoundary(t *testing.T) {
	now := time.Now().UTC()
	minuteReset := now.Add(20 * time.Second)
	dayReset := now.Add(8 * time.Hour)
	current := now.Add(5 * time.Second)
	rows := []ExternalCapacityRow{
		{Effective: 6, Used: 7, ResetAt: minuteReset},
		{Effective: 10, Used: 10, ResetAt: dayReset},
	}

	got := capacityCooldownUntil(&current, rows, now)
	if got == nil || !got.Equal(dayReset) {
		t.Fatalf("expected endpoint cooldown to last until every exhausted policy releases at %s, got %v", dayReset, got)
	}
}

func TestPreviewCapacitySnapshotMarksExternalRowsPreview(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	sawPreview := false
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{}).WithExternalCapacityRowHooks(func(_ context.Context, input ExternalCapacityRowsInput) ([]ExternalCapacityRow, error) {
		sawPreview = input.Preview
		return nil, nil
	})
	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-preview-capacity", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy2",
		UpstreamModel: "dummy2",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodDay,
		LimitValue: 15,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := api.buildCapacitySnapshotWithOptions(ctx, http.Header{}, sch, nil, capacitySnapshotOptions{Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sawPreview {
		t.Fatal("expected external capacity hook to receive preview=true")
	}
	model := capacityModel(t, snapshot, endpoint.UUID)
	row := capacityLimitRow(t, model, models.MetricRequests, models.PeriodDay)
	if row.Used != 0 || row.Remaining != 15 || row.Percent != 0 {
		t.Fatalf("expected preview capacity to ignore persisted live usage, got %#v", row)
	}
	if model.CapacityState != "healthy" {
		t.Fatalf("expected preview model to start healthy after ignoring persisted usage, got %s", model.CapacityState)
	}
}

func TestCapacitySnapshotRuntimeUsageExcludesQueuedSerialReservations(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	sch.Start()
	defer sch.Stop()
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-runtime-capacity", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy-runtime-capacity",
		UpstreamModel: "dummy-runtime-capacity",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "runtime-capacity-lane", Enabled: true, DefaultMaxWaitMS: 60000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodDay,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan scheduler.Permit, 2)
	errs := make(chan error, 2)
	for _, requestID := range []string{"capacity-runtime-first", "capacity-runtime-second"} {
		requestID := requestID
		go func() {
			permit, err := sch.Submit(ctx, scheduler.SubmitRequest{
				Meta:       router.RequestMeta{RequestID: requestID, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
				Candidates: []router.Candidate{candidate},
				BodyBytes:  32,
				EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}()
	}
	first := receiveAdminPermit(t, permits, errs, time.Second)
	assertNoAdminPermit(t, permits, errs, 100*time.Millisecond)

	snapshot, err := api.buildCapacitySnapshot(ctx, http.Header{}, sch, sch.Items())
	if err != nil {
		t.Fatal(err)
	}
	row := capacityLimitRow(t, capacityModel(t, snapshot, endpoint.UUID), models.MetricRequests, models.PeriodDay)
	if row.Used != 0 || row.Reserved != 1 || row.Remaining != 4 || row.Percent != 20 {
		t.Fatalf("expected active request only in capacity runtime reservations, got %#v", row)
	}

	if err := sch.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receiveAdminPermit(t, permits, errs, time.Second)
	if err := sch.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
}

func TestCapacitySnapshotUserScopedRowsDoNotDriveModelCooldown(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{}).WithExternalCapacityRowHooks(func(_ context.Context, input ExternalCapacityRowsInput) ([]ExternalCapacityRow, error) {
		windowStart, resetAt := capacityWindow(input.Now, models.PeriodMinute)
		return []ExternalCapacityRow{{
			Key:         "user:org:user:requests:minute",
			Label:       "Your RPM",
			ScopeType:   "user",
			ScopeID:     "org:user",
			ActorID:     "user",
			Metric:      string(models.MetricRequests),
			Period:      string(models.PeriodMinute),
			Configured:  2,
			Effective:   2,
			Used:        2,
			Remaining:   0,
			Percent:     100,
			WindowStart: windowStart,
			ResetAt:     resetAt,
			Source:      "test",
			UserScoped:  true,
		}}, nil
	})
	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-user-capacity", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy3",
		UpstreamModel: "dummy3",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	api.Register(e)
	model := capacityModel(t, capacitySnapshotResponse(t, e), endpoint.UUID)
	if model.CapacityState != "healthy" || model.CooldownUntil != nil {
		t.Fatalf("expected user scoped row not to affect model state, got state=%s cooldown=%v", model.CapacityState, model.CooldownUntil)
	}
}

func TestCapacitySnapshotAttachesExternalUserRowsByTarget(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-external-targets", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy",
		UpstreamModel: "dummy",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "external-targets-lane", Enabled: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{}).WithExternalCapacityRowHooks(func(_ context.Context, input ExternalCapacityRowsInput) ([]ExternalCapacityRow, error) {
		windowStart, resetAt := capacityWindow(input.Now, models.PeriodMinute)
		dayWindowStart, dayResetAt := capacityWindow(input.Now, models.PeriodDay)
		return []ExternalCapacityRow{
			{
				Key:         "user:org:user:requests:minute",
				Label:       "Your RPM",
				ScopeType:   "user",
				ScopeID:     "org:user",
				TargetType:  "global",
				ActorID:     "user",
				Metric:      string(models.MetricRequests),
				Period:      string(models.PeriodMinute),
				Configured:  10,
				Effective:   10,
				Used:        1,
				Remaining:   9,
				Percent:     10,
				WindowStart: windowStart,
				ResetAt:     resetAt,
				Source:      "test",
				UserScoped:  true,
			},
			{
				Key:         "user_model:org:user:model:requests:minute",
				Label:       "Your model RPM",
				ScopeType:   "user_model",
				ScopeID:     "org:user:model:" + endpoint.UUID,
				TargetType:  "model",
				TargetKey:   strings.ToUpper(endpoint.UUID),
				ActorID:     "user",
				Metric:      string(models.MetricRequests),
				Period:      string(models.PeriodMinute),
				Configured:  2,
				Effective:   2,
				Used:        1,
				Remaining:   1,
				Percent:     50,
				WindowStart: windowStart,
				ResetAt:     resetAt,
				Source:      "test",
				UserScoped:  true,
			},
			{
				Key:         "user_provider:org:user:provider:requests:minute",
				Label:       "Your provider RPM",
				ScopeType:   "user_provider",
				ScopeID:     "org:user:provider:" + provider.UUID,
				TargetType:  "provider",
				TargetKey:   strings.ToUpper(provider.UUID),
				ActorID:     "user",
				Metric:      string(models.MetricRequests),
				Period:      string(models.PeriodMinute),
				Configured:  3,
				Effective:   3,
				Used:        1,
				Remaining:   2,
				Percent:     33.33,
				WindowStart: windowStart,
				ResetAt:     resetAt,
				Source:      "test",
				UserScoped:  true,
			},
			{
				Key:         "user_model:org:user:lane:spend:day",
				Label:       "Your model SPD",
				ScopeType:   "user_model",
				ScopeID:     "org:user:model:" + lane.UUID,
				TargetType:  "model",
				TargetKey:   strings.ToUpper(lane.UUID),
				ActorID:     "user",
				Metric:      string(models.MetricSpend),
				Period:      string(models.PeriodDay),
				Configured:  2_500_000,
				Effective:   2_500_000,
				Used:        500_000,
				Remaining:   2_000_000,
				Percent:     20,
				WindowStart: dayWindowStart,
				ResetAt:     dayResetAt,
				Source:      "test",
				UserScoped:  true,
			},
			{
				Key:         "user_model:org:user:other:requests:minute",
				Label:       "Other model RPM",
				ScopeType:   "user_model",
				ScopeID:     "org:user:model:other",
				TargetType:  "model",
				TargetKey:   "other-model",
				ActorID:     "user",
				Metric:      string(models.MetricRequests),
				Period:      string(models.PeriodMinute),
				Configured:  1,
				Effective:   1,
				Used:        0,
				Remaining:   1,
				Percent:     0,
				WindowStart: windowStart,
				ResetAt:     resetAt,
				Source:      "test",
				UserScoped:  true,
			},
		}, nil
	})

	snapshot, err := api.buildCapacitySnapshot(ctx, http.Header{}, sch, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := capacityModel(t, snapshot, endpoint.UUID)
	rowsByKey := map[string]ExternalCapacityRow{}
	for _, row := range model.LimitRows {
		if row.UserScoped {
			rowsByKey[row.Key] = row
		}
	}
	for _, key := range []string{
		"user:org:user:requests:minute",
		"user_model:org:user:model:requests:minute",
		"user_provider:org:user:provider:requests:minute",
		"user_model:org:user:lane:spend:day",
	} {
		if _, ok := rowsByKey[key]; !ok {
			t.Fatalf("expected external row %q to attach to model, got %#v", key, model.LimitRows)
		}
	}
	if _, ok := rowsByKey["user_model:org:user:other:requests:minute"]; ok {
		t.Fatalf("did not expect unrelated model row to attach, got %#v", model.LimitRows)
	}
	if rowsByKey["user_model:org:user:lane:spend:day"].Metric != string(models.MetricSpend) || rowsByKey["user_model:org:user:lane:spend:day"].Used != 500_000 {
		t.Fatalf("expected spend row to preserve backend micros usage, got %#v", rowsByKey["user_model:org:user:lane:spend:day"])
	}
}

func TestCapacitySnapshotDoesNotTurnGroupFIFOWaitIntoEndpointCooldown(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-group-fifo", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	cooldownUntil := time.Now().UTC().Add(5 * time.Second)
	blocking := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy",
		UpstreamModel: "dummy",
		Enabled:       true,
		HealthStatus:  models.HealthCoolingDown,
		CooldownUntil: &cooldownUntil,
	}
	if err := st.Create(ctx, &blocking); err != nil {
		t.Fatal(err)
	}
	healthy := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy3",
		UpstreamModel: "dummy3",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &healthy); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "group-fifo-regression", Enabled: true, DefaultMaxWaitMS: 60000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: blocking.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: healthy.ID, ManualRank: 3, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	blockingCtx, cancelBlocking := context.WithCancel(ctx)
	defer cancelBlocking()
	healthyCtx, cancelHealthy := context.WithCancel(ctx)
	defer cancelHealthy()
	errCh := make(chan error, 2)
	go func() {
		_, err := sch.Submit(blockingCtx, scheduler.SubmitRequest{
			Meta:       router.RequestMeta{RequestID: "blocking-dummy", IncomingModel: lane.Name, Lane: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
			Candidates: []router.Candidate{{Endpoint: blocking, Lane: &lane, Rank: 1}},
			EnqueuedAt: time.Now().UTC(),
		})
		errCh <- err
	}()
	waitForSchedulerTask(t, sch, "blocking-dummy", blocking.UUID)

	go func() {
		_, err := sch.Submit(healthyCtx, scheduler.SubmitRequest{
			Meta:       router.RequestMeta{RequestID: "group-delayed-dummy3", IncomingModel: lane.Name, Lane: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
			Candidates: []router.Candidate{{Endpoint: healthy, Lane: &lane, Rank: 3}},
			EnqueuedAt: time.Now().UTC(),
		})
		errCh <- err
	}()
	waitForSchedulerTask(t, sch, "group-delayed-dummy3", healthy.UUID)

	e := echo.New()
	api.Register(e)
	snapshot := capacitySnapshotResponse(t, e)
	blockingModel := capacityModel(t, snapshot, blocking.UUID)
	if blockingModel.CapacityState != "cooling-down" || blockingModel.CooldownUntil == nil {
		t.Fatalf("expected blocking endpoint to remain cooling down, got state=%s cooldown=%v", blockingModel.CapacityState, blockingModel.CooldownUntil)
	}
	healthyModel := capacityModel(t, snapshot, healthy.UUID)
	if healthyModel.CapacityState != "healthy" || healthyModel.CooldownUntil != nil {
		t.Fatalf("expected group FIFO-delayed endpoint to remain healthy, got state=%s cooldown=%v", healthyModel.CapacityState, healthyModel.CooldownUntil)
	}

	cancelBlocking()
	cancelHealthy()
	for i := 0; i < 2; i++ {
		select {
		case <-errCh:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for queued submissions to cancel")
		}
	}
}

func TestCapacitySnapshotContextUnavailableExternalRowsDoNotFailGlobalSnapshot(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{}).WithExternalCapacityRowHooks(func(_ context.Context, _ ExternalCapacityRowsInput) ([]ExternalCapacityRow, error) {
		return nil, ErrCapacityContextUnavailable
	})
	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-context-unavailable", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy",
		UpstreamModel: "dummy",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	api.Register(e)
	snapshot := capacitySnapshotResponse(t, e)
	if snapshot.ExternalRowsStatus != externalRowsStatusContextUnavailable {
		t.Fatalf("expected context unavailable external rows status, got %q", snapshot.ExternalRowsStatus)
	}
	model := capacityModel(t, snapshot, endpoint.UUID)
	if model.CapacityState != "healthy" || model.CooldownUntil != nil {
		t.Fatalf("expected global model state to remain healthy, got state=%s cooldown=%v rows=%#v", model.CapacityState, model.CooldownUntil, model.LimitRows)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	rec := httptest.NewRecorder()
	events, err := api.capacitySnapshotProducer(sch)(e.NewContext(req, rec))
	if err != nil {
		t.Fatalf("expected websocket snapshot producer to keep streaming global capacity, got error %v", err)
	}
	if len(events) != 1 || events[0].Type != "realtime_snapshot" {
		t.Fatalf("expected realtime snapshot event, got %#v", events)
	}
	if events[0].Payload["external_rows_status"] != externalRowsStatusContextUnavailable {
		t.Fatalf("expected context-unavailable payload status, got %#v", events[0].Payload)
	}
}

func TestCapacitySnapshotIncludesScopedQueueItems(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{}).WithExternalQueueScopeHooks(func(*echo.Context, QueueScopeInput) (ExternalQueueScope, error) {
		return ExternalQueueScope{ActorID: "user-a"}, nil
	})

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-scoped-queue", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	cooldownUntil := time.Now().UTC().Add(10 * time.Second)
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy",
		UpstreamModel: "dummy",
		Enabled:       true,
		HealthStatus:  models.HealthCoolingDown,
		CooldownUntil: &cooldownUntil,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	submit := func(requestID string, userUUID string) (context.CancelFunc, <-chan error) {
		submitCtx, cancel := context.WithCancel(ctx)
		errCh := make(chan error, 1)
		go func() {
			_, err := sch.Submit(submitCtx, scheduler.SubmitRequest{
				Meta:            router.RequestMeta{RequestID: requestID, IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 60000},
				Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
				TrustedMetadata: map[string]string{"user_uuid": userUUID},
				EnqueuedAt:      time.Now().UTC(),
			})
			errCh <- err
		}()
		waitForSchedulerTask(t, sch, requestID, endpoint.UUID)
		return cancel, errCh
	}

	cancelA, errA := submit("queue-user-a", "user-a")
	cancelB, errB := submit("queue-user-b", "user-b")
	defer cancelA()
	defer cancelB()

	e := echo.New()
	api.Register(e)
	snapshot := capacitySnapshotResponse(t, e)
	if len(snapshot.QueueItems) != 1 {
		t.Fatalf("expected one scoped queue item, got %d: %#v", len(snapshot.QueueItems), snapshot.QueueItems)
	}
	if snapshot.QueueItems[0].RequestID != "queue-user-a" || snapshot.QueueItems[0].ActorID != "user-a" {
		t.Fatalf("expected scoped user-a queue item, got %#v", snapshot.QueueItems[0])
	}
	if snapshot.Queue.QueueDepthGlobal != 1 || snapshot.Queue.States["waiting"] != 1 {
		t.Fatalf("expected scoped queue aggregate to match visible items, got %#v", snapshot.Queue)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	rec := httptest.NewRecorder()
	events, err := api.capacitySnapshotProducer(sch)(e.NewContext(req, rec))
	if err != nil {
		t.Fatalf("expected websocket snapshot producer to include scoped queue items, got error %v", err)
	}
	if len(events) != 1 || events[0].Type != "realtime_snapshot" {
		t.Fatalf("expected realtime snapshot event, got %#v", events)
	}
	queueItems, ok := events[0].Payload["queue_items"].([]any)
	if !ok || len(queueItems) != 1 {
		t.Fatalf("expected one scoped queue item in websocket payload, got %#v", events[0].Payload["queue_items"])
	}

	cancelA()
	cancelB()
	for _, errCh := range []<-chan error{errA, errB} {
		select {
		case <-errCh:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for queued submission to cancel")
		}
	}
}

func TestMarkUserCapacityBlocksFromSchedulerUserDefer(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	blockedUntil := now.Add(12 * time.Second)
	rows := []ExternalCapacityRow{{
		Key:         "user:org:user:requests:minute",
		Label:       "Your RPM",
		ScopeType:   "user",
		ScopeID:     "org:user",
		ActorID:     "user",
		Metric:      string(models.MetricRequests),
		Period:      string(models.PeriodMinute),
		Configured:  2,
		Effective:   2,
		Used:        1,
		Remaining:   1,
		Percent:     50,
		WindowStart: now.Add(-time.Minute),
		ResetAt:     now.Add(48 * time.Second),
		Source:      "test",
		UserScoped:  true,
	}}

	rows = markUserCapacityBlocks(rows, []scheduler.TaskSnapshot{{
		RequestID:         "req-user-deferred",
		ActorID:           "user",
		State:             "waiting",
		PredictedEligible: blockedUntil,
		UserEligible:      blockedUntil,
		UserLimitReason:   "requests/minute",
		DeferScope:        "user",
		DeferReason:       "user_limit_exceeded",
	}}, now)

	if len(rows) != 1 || !rows[0].Blocked || !rows[0].BlockedUntil.Equal(blockedUntil) {
		t.Fatalf("expected matching user row to be blocked until %s, got %#v", blockedUntil, rows)
	}
	if rows[0].Used != 1 || rows[0].Remaining != 1 || rows[0].Percent != 50 {
		t.Fatalf("expected blocked row to preserve backend usage values, got %#v", rows[0])
	}
}

func TestMarkUserCapacityBlocksKeepsAPIKeysExact(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	blockedUntil := now.Add(time.Minute)
	rows := []ExternalCapacityRow{
		{
			Key:        "api-key-a",
			ScopeType:  "api_key",
			ScopeID:    "org-a:key-a",
			ActorID:    "user-a",
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			ResetAt:    blockedUntil,
			UserScoped: true,
		},
		{
			Key:        "api-key-b",
			ScopeType:  "api_key",
			ScopeID:    "org-a:key-b",
			ActorID:    "user-a",
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			ResetAt:    blockedUntil,
			UserScoped: true,
		},
	}

	rows = markUserCapacityBlocks(rows, []scheduler.TaskSnapshot{{
		RequestID:         "req-key-a-deferred",
		ActorID:           "user-a",
		State:             "waiting",
		PredictedEligible: blockedUntil,
		UserEligible:      blockedUntil,
		UserLimitReason:   "requests/minute",
		DeferScope:        "api_key",
		DeferScopeID:      "org-a:key-a",
		DeferReason:       "api_key_limit_exceeded",
	}}, now)

	if !rows[0].Blocked {
		t.Fatalf("expected exact key A row to be blocked, got %#v", rows[0])
	}
	if rows[1].Blocked {
		t.Fatalf("key B owned by the same user inherited key A's block: %#v", rows[1])
	}
}

func TestCapacityRequestHeadersNormalizeBackendVisibility(t *testing.T) {
	e := echo.New()
	organizationContext := e.NewContext(
		httptest.NewRequest(http.MethodGet, "/api/capacity-snapshot?limit_visibility=organization", nil),
		httptest.NewRecorder(),
	)
	if got := capacityRequestHeaders(organizationContext).Get(capacityVisibilityHeader); got != "organization" {
		t.Fatalf("organization visibility header = %q", got)
	}

	untrustedContext := e.NewContext(
		httptest.NewRequest(http.MethodGet, "/api/capacity-snapshot?limit_visibility=anything", nil),
		httptest.NewRecorder(),
	)
	untrustedContext.Request().Header.Set(capacityVisibilityHeader, "organization")
	if got := capacityRequestHeaders(untrustedContext).Get(capacityVisibilityHeader); got != "mine" {
		t.Fatalf("untrusted visibility was not overwritten: %q", got)
	}
}

func TestCapacitySnapshotUsesPolicyState(t *testing.T) {
	ctx := context.Background()
	e, st, tracker, endpoint := usageStatsTestAPI(t)
	now := time.Now().UTC()
	policy := models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricSpend,
		Period:     models.PeriodDay,
		LimitValue: 10,
		Enabled:    true,
		Source:     "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLimitPolicyStates(ctx, []models.LimitPolicyState{{
		PolicyID:      policy.ID,
		PolicyUUID:    policy.UUID,
		WindowStart:   now.Add(-24 * time.Hour),
		WindowEnd:     now,
		UsedValue:     2,
		ReservedValue: 0,
		PolicyVersion: policy.UpdatedAt.UTC().UnixNano(),
		StateVersion:  1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricSpend, 3, now); err != nil {
		t.Fatal(err)
	}
	row := capacityLimitRow(t, capacityModel(t, capacitySnapshotResponse(t, e), endpoint.UUID), models.MetricSpend, models.PeriodDay)
	if row.Used != 3 {
		t.Fatalf("expected exact runtime state to win over the persisted checkpoint, got %#v", row)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricSpend, 3, now); err != nil {
		t.Fatal(err)
	}
	row = capacityLimitRow(t, capacityModel(t, capacitySnapshotResponse(t, e), endpoint.UUID), models.MetricSpend, models.PeriodDay)
	if row.Used != 6 {
		t.Fatalf("expected updated runtime state to remain authoritative, got %#v", row)
	}
}

func TestPublishExternalCapacityChangesEmitsActorScopedAddAndRemoval(t *testing.T) {
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{
		OrganizationUUID: "organization-a",
		UserUUID:         "actor-a",
	})
	st := testutil.NewStore(t)
	for _, table := range []string{"providers", "endpoints", "routing_lanes", "lane_memberships"} {
		if err := st.DB().Exec("ALTER TABLE " + table + " ADD COLUMN organization_uuid TEXT").Error; err != nil {
			t.Fatalf("add tenant column to %s: %v", table, err)
		}
	}
	hub := telemetry.NewHub()
	api := New(st, nil, hub, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{Name: "External Capacity Provider", Slug: "external-capacity-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, Name: "external-capacity-model", UpstreamModel: "external-capacity-model",
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	row := ExternalCapacityRow{
		Key: "api_key:organization-a:key-a:requests:minute", Label: "API key …key-a RPM",
		ScopeType: "api_key", ScopeID: "organization-a:key-a", TargetType: "global",
		ActorID: "actor-a", Metric: "requests", Period: "minute",
		Configured: 2, Effective: 2, Remaining: 2, Source: "pro_api_key_limit", UserScoped: true,
	}
	if err := api.PublishExternalCapacityChanges(ctx, []ExternalCapacityChange{{Row: row}}); err != nil {
		t.Fatal(err)
	}
	if err := api.PublishExternalCapacityChanges(ctx, []ExternalCapacityChange{{Row: row, Removed: true}}); err != nil {
		t.Fatal(err)
	}

	events := hub.Events()
	if len(events) != 2 {
		t.Fatalf("capacity mutation events=%d, want 2: %#v", len(events), events)
	}
	added := events[0]
	if added.Type != "capacity_limit_state" ||
		added.Payload["organization_uuid"] != "organization-a" ||
		added.Payload["actor_id"] != "actor-a" ||
		added.Payload["endpoint_id"] != endpoint.UUID ||
		added.Payload["user_scoped"] != true {
		t.Fatalf("unexpected capacity add event: %#v", added)
	}
	rows, ok := added.Payload["rows"].([]ExternalCapacityRow)
	if !ok || len(rows) != 1 || rows[0].Key != row.Key || rows[0].Configured != 2 {
		t.Fatalf("unexpected capacity add rows: %#v", added.Payload["rows"])
	}
	removed := events[1]
	keys, ok := removed.Payload["removed_keys"].([]string)
	if removed.Type != "capacity_limit_state" || !ok || len(keys) != 1 || keys[0] != row.Key {
		t.Fatalf("unexpected capacity removal event: %#v", removed)
	}
}
