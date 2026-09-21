package admin

import (
	"context"
	"net/http"
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

func TestUpdateLimitPolicyPublishesAuthoritativeCapacityDelta(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(st)
	sch := scheduler.New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 30_000, 1000, 300_000)
	t.Cleanup(sch.Stop)
	api := New(st, sch, hub, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{Name: "Dummy Provider", Slug: "policy-delta-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "dummy-policy-delta", UpstreamModel: "dummy-policy-delta", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID, Metric: models.MetricRequests,
		Period: models.PeriodDay, LimitValue: 40, Enabled: true, Source: "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 35, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	api.Register(e)
	resp := adminRequest(t, e, http.MethodPut, "/api/limit-policies/"+policy.UUID, `{"limit_value":50}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected policy update status 200, got %d: %s", resp.Code, resp.Body.String())
	}

	foundExpanded := false
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["endpoint_id"] != endpoint.UUID {
			continue
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			if row["configured"] == int64(50) && row["used"] == int64(35) && row["percent"] == float64(70) {
				foundExpanded = true
			}
		}
	}
	if !foundExpanded {
		t.Fatalf("expected policy update to publish authoritative 35/50 capacity row, events=%#v", hub.Events())
	}

	resp = adminRequest(t, e, http.MethodPut, "/api/limit-policies/"+policy.UUID, `{"limit_value":30}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected lowering policy status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	foundBlocked := false
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["endpoint_id"] != endpoint.UUID {
			continue
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			if row["configured"] != int64(30) || row["used"] != int64(35) || row["blocked"] != true {
				continue
			}
			resetAt, resetOK := row["reset_at"].(time.Time)
			cooldownUntil, cooldownOK := event.Payload["cooldown_until"].(time.Time)
			if !resetOK || resetAt.IsZero() || !cooldownOK || !cooldownUntil.Equal(resetAt) {
				t.Fatalf("blocked policy delta missing matching cooldown boundary: event=%#v", event)
			}
			foundBlocked = true
		}
	}
	if !foundBlocked {
		t.Fatalf("expected lowering policy to publish blocked 35/30 capacity row, events=%#v", hub.Events())
	}

	resp = adminRequest(t, e, http.MethodPut, "/api/limit-policies/"+policy.UUID, `{"limit_value":60}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected raising policy status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["endpoint_id"] != endpoint.UUID {
			continue
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			if row["configured"] == int64(60) && row["used"] == int64(35) && row["blocked"] == false {
				return
			}
		}
	}
	t.Fatalf("expected raising policy to publish unblocked 35/60 capacity row, events=%#v", hub.Events())
}

func TestUpdateLimitPolicyReleasesQueuedRequestImmediately(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, hub, nil, 60_000, 1000, 300_000)
	sch.Start()
	t.Cleanup(sch.Stop)
	api := New(st, sch, hub, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{Name: "Dummy Provider", Slug: "policy-release-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	pacingDisabled := false
	endpoint := models.Endpoint{
		ProviderID: provider.ID, Name: "dummy-policy-release", UpstreamModel: "dummy-policy-release",
		Enabled: true, HealthStatus: models.HealthHealthy, Pacing: &pacingDisabled,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "policy-release-lane", Enabled: true, AllowFallback: true, DefaultMaxWaitMS: 60_000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID, Metric: models.MetricRequests,
		Period: models.PeriodMinute, LimitValue: 3, Enabled: true, Source: "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	usedAt := time.Now().UTC().Add(-10 * time.Second)
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 3, usedAt); err != nil {
		t.Fatal(err)
	}

	type submitResult struct {
		permit scheduler.Permit
		err    error
	}
	done := make(chan submitResult, 1)
	go func() {
		permit, err := sch.Submit(ctx, scheduler.SubmitRequest{
			Meta: router.RequestMeta{
				RequestID: "policy-release-request", Lane: lane.Name, IncomingModel: lane.Name,
				AllowFallback: true, MaxWaitMS: lane.DefaultMaxWaitMS,
			},
			Candidates: []router.Candidate{{Endpoint: endpoint, Lane: &lane, Rank: 1}},
			BodyBytes:  32,
			EnqueuedAt: time.Now().UTC(),
		})
		done <- submitResult{permit: permit, err: err}
	}()
	waitForSchedulerTask(t, sch, "policy-release-request", endpoint.UUID)

	e := echo.New()
	api.Register(e)
	resp := adminRequest(t, e, http.MethodPut, "/api/limit-policies/"+policy.UUID, `{"limit_value":13}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected policy update status 200, got %d: %s", resp.Code, resp.Body.String())
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("queued request failed after limit increase: %v", got.err)
		}
		if got.permit.Endpoint.ID != endpoint.ID {
			t.Fatalf("expected endpoint %d, got %d", endpoint.ID, got.permit.Endpoint.ID)
		}
		if got.permit.Waited >= 5*time.Second {
			t.Fatalf("limit update retained stale cooldown; request waited %s", got.permit.Waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("queued request did not dispatch after API limit increase: %#v", sch.Items())
	}

	var updated models.Endpoint
	if err := st.FindByID(ctx, &updated, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated.HealthStatus != models.HealthHealthy || updated.CooldownUntil != nil {
		t.Fatalf("expected API limit update to clear endpoint cooldown, got status=%s until=%v", updated.HealthStatus, updated.CooldownUntil)
	}
}

func TestUpdateLimitPolicyReleasesPartitionedRuntimeRequestImmediately(t *testing.T) {
	const organizationUUID = "2374509b-42bd-427a-92ae-16d7400794bc"
	const userUUID = "08fbdd3f-35b7-45c1-bb78-6a6d4dfab667"
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{
		OrganizationUUID: organizationUUID,
		UserUUID:         userUUID,
	})
	st := testutil.NewStore(t)
	// Pro installs tenant columns around the public schema. Recreate the subset
	// used by this partitioned-runtime regression in the public SQLite fixture.
	for _, table := range []string{
		"providers", "endpoints", "routing_lanes", "lane_memberships",
		"limit_policies", "observed_limits", "pricing_policies", "request_logs",
		"limit_policy_states", "limit_policy_state_segments", "observed_limit_state_segments",
	} {
		if err := st.DB().Exec("ALTER TABLE " + table + " ADD COLUMN organization_uuid TEXT").Error; err != nil {
			t.Fatalf("add tenant column to %s: %v", table, err)
		}
	}
	hub := telemetry.NewHub()

	provider := models.Provider{Name: "Dummy Provider", Slug: "partitioned-policy-release-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	pacingDisabled := false
	endpoint := models.Endpoint{
		ProviderID: provider.ID, Name: "partitioned-dummy-policy-release", UpstreamModel: "partitioned-dummy-policy-release",
		Enabled: true, HealthStatus: models.HealthHealthy, Pacing: &pacingDisabled,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "partitioned-policy-release-lane", Enabled: true, AllowFallback: true, DefaultMaxWaitMS: 60_000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID, Metric: models.MetricRequests,
		Period: models.PeriodMinute, LimitValue: 3, Enabled: true, Source: "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}

	registry := scheduler.NewRuntimeRegistry(func(runtimeCtx context.Context, _ string) (*scheduler.Scheduler, error) {
		tracker := limits.NewTracker(st)
		if err := tracker.RecordWindow(runtimeCtx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 3, time.Now().UTC().Add(-10*time.Second)); err != nil {
			return nil, err
		}
		resolver := limits.NewResolver(st, tracker)
		runtime := scheduler.New(st, resolver, tracker, hub, nil, 60_000, 1000, 300_000).WithBackgroundContext(runtimeCtx)
		runtime.Start()
		return runtime, nil
	})
	t.Cleanup(registry.Stop)
	api := New(st, registry, hub, nil, adminTestToken(), SystemInfo{}).
		WithExternalTenantScopeHooks(func(*echo.Context) (TenantScope, error) {
			return TenantScope{OrganizationUUID: organizationUUID, UserUUID: userUUID}, nil
		})

	type submitResult struct {
		permit scheduler.Permit
		err    error
	}
	done := make(chan submitResult, 1)
	go func() {
		permit, err := registry.Submit(ctx, scheduler.SubmitRequest{
			PartitionKey: organizationUUID,
			TrustedMetadata: map[string]string{
				"organization_uuid": organizationUUID,
				"user_uuid":         userUUID,
			},
			Meta: router.RequestMeta{
				RequestID: "partitioned-policy-release-request", Lane: lane.Name, IncomingModel: lane.Name,
				AllowFallback: true, MaxWaitMS: lane.DefaultMaxWaitMS,
			},
			Candidates: []router.Candidate{{Endpoint: endpoint, Lane: &lane, Rank: 1}},
			BodyBytes:  32,
			EnqueuedAt: time.Now().UTC(),
		})
		done <- submitResult{permit: permit, err: err}
	}()
	waitForSchedulerTask(t, registry, "partitioned-policy-release-request", endpoint.UUID)

	e := echo.New()
	api.Register(e)
	resp := adminRequest(t, e, http.MethodPut, "/api/limit-policies/"+policy.UUID, `{"limit_value":13}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected policy update status 200, got %d: %s", resp.Code, resp.Body.String())
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("partitioned queued request failed after limit increase: %v", got.err)
		}
		if got.permit.Endpoint.ID != endpoint.ID {
			t.Fatalf("expected endpoint %d, got %d", endpoint.ID, got.permit.Endpoint.ID)
		}
		if got.permit.Waited >= 5*time.Second {
			t.Fatalf("partitioned limit update retained stale cooldown; request waited %s", got.permit.Waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("partitioned queued request did not dispatch after API limit increase: %#v", registry.Items())
	}
}
