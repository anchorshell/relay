package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
)

func TestCapacityTelemetryPublishesCoreRollingIncreaseAndExpiry(t *testing.T) {
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(nil)
	s := New(nil, nil, tracker, hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	limitValue := int64(5)
	endpoint := models.Endpoint{ID: 7, UUID: "endpoint-a", ProviderUUID: "provider-a"}
	limit := limits.EffectiveLimit{
		PolicyID:   11,
		PolicyUUID: "policy-a",
		Metric:     models.MetricRequests,
		Period:     models.PeriodSecond,
		Configured: &limitValue,
		Effective:  &limitValue,
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		ScopeUUID:  endpoint.UUID,
	}
	for index := 1; index <= 5; index++ {
		usedAt := time.Now().UTC()
		if err := tracker.Commit(context.Background(), "task-a", []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, 1, 0, 0, usedAt); err != nil {
			t.Fatal(err)
		}
		s.publishCapacityUsage(&task{
			meta:            router.RequestMeta{RequestID: "request-a"},
			trustedMetadata: map[string]string{"organization_uuid": "org-a", "user_uuid": "user-a"},
			candidate:       router.Candidate{Endpoint: endpoint},
			evaluation:      limits.CandidateEvaluation{EffectiveLimits: []limits.EffectiveLimit{limit}},
		}, 1, 0, 0, usedAt, time.Now().UTC())
		waitForCapacityUsed(t, hub, false, int64(index), 250*time.Millisecond)
		time.Sleep(25 * time.Millisecond)
	}

	var publishedCooldown bool
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["health_status"] != models.HealthRateLimited {
			continue
		}
		if cooldownUntil, ok := event.Payload["cooldown_until"].(time.Time); ok && cooldownUntil.After(time.Now().UTC()) {
			publishedCooldown = true
			break
		}
	}
	if !publishedCooldown {
		t.Fatalf("expected exhausted limit delta to include its cooldown boundary; events=%#v", hub.Events())
	}

	waitForCapacitySequence(t, hub, false, []int64{1, 2, 3, 4, 5, 4, 3, 2, 1, 0}, 2*time.Second)
}

func TestCapacityTelemetryRefreshesExternalRollingExpiryAuthoritatively(t *testing.T) {
	hub := telemetry.NewHub()
	s := New(nil, nil, limits.NewTracker(nil), hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	usedAt := time.Now().UTC()
	expiresAt := usedAt.Add(time.Second)
	s.WithExternalLimitHooks(func(ctx context.Context, _ ExternalLimitInput) ([]ExternalLimit, error) {
		scope, ok := tenancy.ScopeFromContext(ctx)
		if !ok || scope.OrganizationUUID != "org-a" {
			t.Errorf("external refresh lost tenant scope: %#v ok=%v", scope, ok)
		}
		used := int64(0)
		resetAt := time.Time{}
		if time.Now().UTC().Before(expiresAt) {
			used = 1
			resetAt = expiresAt
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: "user_model", ID: "org-a:user-a:model:model-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodSecond),
			LimitValue: 5,
			Used:       used,
			ResetAt:    resetAt,
		}}, nil
	})

	limitValue := int64(5)
	limit := limits.EffectiveLimit{
		Metric:     models.MetricRequests,
		Period:     models.PeriodSecond,
		Configured: &limitValue,
		Effective:  &limitValue,
		ScopeType:  models.ScopeType("user_model"),
		ScopeUUID:  "org-a:user-a:model:model-a",
		ResetAt:    expiresAt,
	}
	s.publishCapacityUsage(&task{
		meta:            router.RequestMeta{RequestID: "request-a", IncomingModel: "model-a"},
		trustedMetadata: map[string]string{"organization_uuid": "org-a", "user_uuid": "user-a"},
		candidate: router.Candidate{Endpoint: models.Endpoint{
			ID: 7, UUID: "endpoint-a", ProviderUUID: "provider-a", UpstreamModel: "model-a",
		}},
		evaluation: limits.CandidateEvaluation{EffectiveLimits: []limits.EffectiveLimit{limit}},
	}, 1, 0, 0, usedAt, time.Now().UTC())

	waitForCapacityUsed(t, hub, true, 1, 250*time.Millisecond)
	waitForCapacityUsed(t, hub, true, 0, 2*time.Second)
}

func TestAPIKeyCapacityTelemetryPublishesRollingExpiryDecrease(t *testing.T) {
	hub := telemetry.NewHub()
	s := New(nil, nil, limits.NewTracker(nil), hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	usedAt := time.Now().UTC()
	expiresAt := usedAt.Add(100 * time.Millisecond)
	scopeID := "org-a:key-a"
	s.WithExternalLimitHooks(func(ctx context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		scope, ok := tenancy.ScopeFromContext(ctx)
		if !ok || scope.OrganizationUUID != "org-a" {
			t.Errorf("API-key expiry refresh lost tenant scope: %#v ok=%v", scope, ok)
		}
		if input.RequestID != "" {
			t.Errorf("API-key expiry refresh reused cacheable request ID %q", input.RequestID)
		}
		used := int64(0)
		resetAt := time.Time{}
		if time.Now().UTC().Before(expiresAt) {
			used = 1
			resetAt = expiresAt
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: "api_key", ID: scopeID},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodSecond),
			LimitValue: 2,
			Used:       used,
			ResetAt:    resetAt,
		}}, nil
	})

	limitValue := int64(2)
	limit := limits.EffectiveLimit{
		Metric: models.MetricRequests, Period: models.PeriodSecond,
		Configured: &limitValue, Effective: &limitValue, ScopeType: models.ScopeType("api_key"),
		ScopeUUID: scopeID, ResetAt: expiresAt, ActorScoped: true, CapacityKeyNamespace: "api_key",
	}
	s.publishCapacityUsage(&task{
		meta: router.RequestMeta{RequestID: "request-api-key-expiry"},
		trustedMetadata: map[string]string{
			"organization_uuid": "org-a",
			"user_uuid":         "user-a",
			"api_key_uuid":      "key-a",
		},
		candidate: router.Candidate{Endpoint: models.Endpoint{
			ID: 7, UUID: "endpoint-a", ProviderUUID: "provider-a",
		}},
		evaluation: limits.CandidateEvaluation{EffectiveLimits: []limits.EffectiveLimit{limit}},
	}, 1, 0, 0, usedAt, time.Now().UTC())

	waitForCapacityUsed(t, hub, true, 1, 250*time.Millisecond)
	waitForCapacityUsed(t, hub, true, 0, 2*time.Second)
}

func TestCapacityTelemetryPublishesDispatchReservationsImmediately(t *testing.T) {
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(nil)
	s := New(nil, nil, tracker, hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	now := time.Now().UTC()
	limitValue := int64(5)
	endpoint := models.Endpoint{ID: 7, UUID: "endpoint-a", ProviderUUID: "provider-a"}
	core := limits.EffectiveLimit{
		PolicyID: 11, PolicyUUID: "policy-a", Metric: models.MetricRequests, Period: models.PeriodMinute,
		Configured: &limitValue, Effective: &limitValue, ScopeType: models.ScopeEndpoint,
		ScopeID: endpoint.ID, ScopeUUID: endpoint.UUID,
	}
	user := limits.EffectiveLimit{
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		Configured: &limitValue, Effective: &limitValue, ScopeType: models.ScopeType("user_model"),
		ScopeUUID: "org-a:user-a:model:model-a", Used: 2, Reserved: 1, ResetAt: now.Add(time.Minute),
	}
	tracker.ReserveRuntime("task-a", []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 1, now)
	s.publishCapacityReservation(&task{
		id:              "task-a",
		meta:            router.RequestMeta{RequestID: "request-a", IncomingModel: "model-a"},
		trustedMetadata: map[string]string{"organization_uuid": "org-a", "user_uuid": "user-a"},
		candidate:       router.Candidate{Endpoint: endpoint},
		evaluation:      limits.CandidateEvaluation{EffectiveLimits: []limits.EffectiveLimit{core, user}},
	}, now)

	assertCapacityState := func(userScoped bool, wantUsed, wantReserved int64) {
		t.Helper()
		for _, event := range hub.Events() {
			if event.Type != "capacity_limit_state" || event.Payload["user_scoped"] != userScoped {
				continue
			}
			rows, _ := event.Payload["rows"].([]map[string]any)
			for _, row := range rows {
				if row["used"] == wantUsed && row["reserved"] == wantReserved {
					return
				}
			}
		}
		t.Fatalf("missing dispatch capacity state user_scoped=%v used=%d reserved=%d; events=%#v", userScoped, wantUsed, wantReserved, hub.Events())
	}
	assertCapacityState(false, int64(0), int64(1))
	assertCapacityState(true, int64(2), int64(2))
}

func TestAPIKeyCapacityUsagePublishesUserScopedRealtimeDelta(t *testing.T) {
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(nil)
	s := New(nil, nil, tracker, hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	now := time.Now().UTC()
	limitValue := int64(2)
	scopeID := "org-a:key-a"
	endpoint := models.Endpoint{ID: 7, UUID: "endpoint-a", ProviderUUID: "provider-a"}
	apiKeyLimit := limits.EffectiveLimit{
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		Configured: &limitValue, Effective: &limitValue, ScopeType: models.ScopeType("api_key"),
		ScopeUUID: scopeID, ResetAt: now.Add(time.Minute), ActorScoped: true, CapacityKeyNamespace: "api_key",
	}
	s.publishCapacityUsage(&task{
		id:              "task-api-key",
		meta:            router.RequestMeta{RequestID: "request-api-key", IncomingModel: "model-a"},
		trustedMetadata: map[string]string{"organization_uuid": "org-a", "user_uuid": "user-a", "api_key_uuid": "key-a"},
		candidate:       router.Candidate{Endpoint: endpoint},
		evaluation:      limits.CandidateEvaluation{EffectiveLimits: []limits.EffectiveLimit{apiKeyLimit}},
	}, 1, 0, 0, now, now.Add(time.Second))

	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["user_scoped"] != true {
			continue
		}
		if event.Payload["actor_id"] != "user-a" || event.Payload["request_id"] != "request-api-key" {
			t.Fatalf("API-key capacity event identity = %#v", event.Payload)
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			if row["key"] == "api_key:"+scopeID+":requests:minute" &&
				row["scope_type"] == models.ScopeType("api_key") &&
				row["used"] == int64(1) &&
				row["percent"] == float64(50) &&
				row["user_scoped"] == true {
				return
			}
		}
	}
	t.Fatalf("missing API-key capacity websocket delta; events=%#v", hub.Events())
}

func TestAPIKeyCapacityUsageRebasesAfterRollingWindowExpires(t *testing.T) {
	hub := telemetry.NewHub()
	s := New(nil, nil, limits.NewTracker(nil), hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	windowStart := time.Now().UTC()
	limitValue := int64(2)
	scopeID := "org-a:key-a"
	eventContext := capacityTelemetryContext{
		organizationUUID: "org-a",
		actorID:          "user-a",
		requestID:        "request-a",
		endpointUUID:     "endpoint-a",
		providerUUID:     "provider-a",
	}
	input := ExternalLimitInput{
		RequestID: "request-a",
		Metadata: map[string]string{
			"organization_uuid": "org-a",
			"user_uuid":         "user-a",
			"api_key_uuid":      "key-a",
		},
	}
	limit := limits.EffectiveLimit{
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		Configured: &limitValue, Effective: &limitValue, ScopeType: models.ScopeType("api_key"),
		ScopeUUID: scopeID, ResetAt: windowStart.Add(time.Hour), ActorScoped: true, CapacityKeyNamespace: "api_key",
	}
	row := s.capacityTelemetry.recordExternal(eventContext, input, limit, 2, windowStart, windowStart)
	if row["used"] != int64(2) {
		t.Fatalf("initial API-key usage = %#v, want 2", row["used"])
	}

	// Model the queued request being admitted after the old release boundary
	// but before the telemetry timer callback has refreshed its cached state.
	// The request's evaluation is authoritative for the new window.
	nextWindow := windowStart.Add(2 * time.Hour)
	limit.Used = 0
	limit.ResetAt = nextWindow.Add(time.Minute)
	row = s.capacityTelemetry.recordExternal(eventContext, input, limit, 1, nextWindow, nextWindow)
	if row["used"] != int64(1) || row["remaining"] != int64(1) || row["percent"] != float64(50) {
		t.Fatalf("API-key usage after rolling expiry = %#v, want used=1 remaining=1 percent=50", row)
	}
}

func TestPublishLimitPolicyStateUpdatesConfiguredCapacityAndRemovesRow(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(st)
	s := New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	provider := models.Provider{Name: "Provider", Slug: "provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "Model", UpstreamModel: "model", Enabled: true, HealthStatus: models.HealthHealthy}
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

	policy.LimitValue = 50
	if err := st.Save(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLimitPolicyState(ctx, policy, false); err != nil {
		t.Fatal(err)
	}

	var updated bool
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["endpoint_id"] != endpoint.UUID {
			continue
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			if row["configured"] == int64(50) && row["effective"] == int64(50) && row["used"] == int64(35) && row["percent"] == float64(70) {
				updated = true
			}
		}
	}
	if !updated {
		t.Fatalf("expected authoritative 35/50 capacity row after policy edit, events=%#v", hub.Events())
	}

	policy.LimitValue = 30
	if err := st.Save(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLimitPolicyState(ctx, policy, false); err != nil {
		t.Fatal(err)
	}
	var exhaustedWithBoundary bool
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" || event.Payload["endpoint_id"] != endpoint.UUID {
			continue
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			blocked, _ := row["blocked"].(bool)
			resetAt, _ := row["reset_at"].(time.Time)
			cooldownUntil, _ := event.Payload["cooldown_until"].(time.Time)
			if row["configured"] == int64(30) && row["used"] == int64(35) && blocked && resetAt.After(time.Now().UTC()) && cooldownUntil.Equal(resetAt) {
				exhaustedWithBoundary = true
			}
		}
	}
	if !exhaustedWithBoundary {
		t.Fatalf("expected lowering a policy below current usage to publish one blocked row and countdown boundary, events=%#v", hub.Events())
	}

	if err := s.PublishLimitPolicyState(ctx, policy, true); err != nil {
		t.Fatal(err)
	}
	wantKey := strings.Join([]string{string(models.ScopeEndpoint), endpoint.UUID, string(models.MetricRequests), string(models.PeriodDay), ""}, ":")
	for _, event := range hub.Events() {
		keys, _ := event.Payload["removed_keys"].([]string)
		if event.Type == "capacity_limit_state" && event.Payload["endpoint_id"] == endpoint.UUID && len(keys) == 1 && keys[0] == wantKey {
			return
		}
	}
	t.Fatalf("expected capacity row removal for %s, events=%#v", wantKey, hub.Events())
}

func TestPublishLimitPolicyStateCountdownMeansTimeUntilAdmission(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	hub := telemetry.NewHub()
	tracker := limits.NewTracker(st)
	s := New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)

	provider := models.Provider{Name: "Provider", Slug: "provider-ready-boundary", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "Model", UpstreamModel: "model", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID, Metric: models.MetricRequests,
		Period: models.PeriodMinute, LimitValue: 13, Enabled: true, Source: "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, age := range []time.Duration{
		50 * time.Second,
		45 * time.Second,
		40 * time.Second,
		35 * time.Second,
		30 * time.Second,
		25 * time.Second,
		20 * time.Second,
	} {
		if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 1, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	latestBoundary := func(configured int64) time.Time {
		t.Helper()
		var found time.Time
		for _, event := range hub.Events() {
			if event.Type != "capacity_limit_state" || event.Payload["endpoint_id"] != endpoint.UUID {
				continue
			}
			rows, _ := event.Payload["rows"].([]map[string]any)
			for _, row := range rows {
				if row["configured"] != configured {
					continue
				}
				resetAt, _ := row["reset_at"].(time.Time)
				if resetAt.After(found) {
					found = resetAt.UTC()
				}
			}
		}
		if found.IsZero() {
			t.Fatalf("missing reset_at for configured limit %d; events=%#v", configured, hub.Events())
		}
		return found
	}

	policy.LimitValue = 5
	if err := st.Save(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLimitPolicyState(ctx, policy, false); err != nil {
		t.Fatal(err)
	}
	forFive := latestBoundary(5)
	if want := now.Add(20 * time.Second); !forFive.Equal(want) {
		t.Fatalf("7/5 cooldown boundary = %s, want admission boundary %s", forFive, want)
	}

	policy.LimitValue = 3
	if err := st.Save(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLimitPolicyState(ctx, policy, false); err != nil {
		t.Fatal(err)
	}
	forThree := latestBoundary(3)
	if want := now.Add(30 * time.Second); !forThree.Equal(want) {
		t.Fatalf("7/3 cooldown boundary = %s, want admission boundary %s", forThree, want)
	}
	if !forThree.After(forFive) {
		t.Fatalf("lowering the limit must move readiness later: 7/5=%s 7/3=%s", forFive, forThree)
	}
}

func TestScheduledEndpointHealthSyncPublishesReadyAfterCooldown(t *testing.T) {
	st := testutil.NewStore(t)
	hub := telemetry.NewHub()
	provider := models.Provider{UUID: "11111111-1111-4111-8111-111111111111", Name: "Provider", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	until := time.Now().UTC().Add(100 * time.Millisecond)
	endpoint := models.Endpoint{
		UUID: "22222222-2222-4222-8222-222222222222", ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "Model", Enabled: true,
		HealthStatus: models.HealthCoolingDown, CooldownUntil: &until, CooldownReason: "upstream_rate_limited",
	}
	if err := st.Create(context.Background(), &endpoint); err != nil {
		t.Fatal(err)
	}
	tracker := limits.NewTracker(st)
	s := New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 30_000, 100, 300_000)
	t.Cleanup(s.Stop)
	s.ScheduleEndpointHealthSync("", endpoint.ID, until)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range hub.Events() {
			if event.Type == "endpoint_health_change" && event.Payload["health_status"] == models.HealthHealthy {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for endpoint ready event")
}

func waitForCapacityUsed(t *testing.T, hub *telemetry.Hub, userScoped bool, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, event := range hub.Events() {
			if event.Type != "capacity_limit_state" {
				continue
			}
			rows, _ := event.Payload["rows"].([]map[string]any)
			if row, ok := event.Payload["row"].(map[string]any); ok {
				rows = append(rows, row)
			}
			for _, row := range rows {
				if row["user_scoped"] != userScoped {
					continue
				}
				if used, ok := row["used"].(int64); ok && used == want {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for user_scoped=%v used=%d; events=%#v", userScoped, want, hub.Events())
}

func waitForCapacitySequence(t *testing.T, hub *telemetry.Hub, userScoped bool, want []int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := capacityUsedValues(hub, userScoped)
		if containsInt64Sequence(got, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for capacity sequence %v; got %v", want, capacityUsedValues(hub, userScoped))
}

func capacityUsedValues(hub *telemetry.Hub, userScoped bool) []int64 {
	values := make([]int64, 0)
	for _, event := range hub.Events() {
		if event.Type != "capacity_limit_state" {
			continue
		}
		rows, _ := event.Payload["rows"].([]map[string]any)
		for _, row := range rows {
			if row["user_scoped"] != userScoped {
				continue
			}
			if used, ok := row["used"].(int64); ok {
				values = append(values, used)
			}
		}
	}
	return values
}

func containsInt64Sequence(values, want []int64) bool {
	if len(want) == 0 {
		return true
	}
	for start := 0; start+len(want) <= len(values); start++ {
		matches := true
		for index := range want {
			if values[start+index] != want[index] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
