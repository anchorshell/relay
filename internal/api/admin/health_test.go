package admin

import (
	"context"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/testutil"
)

func TestDecorateEndpointHealthPersistsStaleRateLimitClear(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	hub := telemetry.NewHub()
	sch := scheduler.New(st, resolver, tracker, hub, nil, 30000, 1000, 300000)
	api := New(st, sch, hub, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{
		UUID: "11111111-1111-4111-8111-111111111111",
		Name: "Dummy Provider", Slug: "dummy-provider", BaseURL: "https://example.com", Enabled: true,
	}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	cooldownUntil := time.Now().UTC().Add(45 * time.Minute)
	endpoint := models.Endpoint{
		UUID:          "22222222-2222-4222-8222-222222222222",
		ProviderID:    provider.ID,
		ProviderUUID:  provider.UUID,
		Name:          "dummy1",
		UpstreamModel: "dummy1",
		Enabled:       true,
		HealthStatus:  models.HealthRateLimited,
		CooldownUntil: &cooldownUntil,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricSpend,
		Period:     models.PeriodHour,
		LimitValue: 1_000_000,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricSpend, 280_005, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	decorated, err := api.decorateEndpointHealth(ctx, []models.Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if len(decorated) != 1 || decorated[0].HealthStatus != models.HealthHealthy || decorated[0].CooldownUntil != nil {
		t.Fatalf("expected stale rate limit to decorate as healthy/null, got %#v", decorated)
	}

	var stored models.Endpoint
	if err := st.FindByID(ctx, &stored, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if stored.HealthStatus != models.HealthHealthy || stored.CooldownUntil != nil {
		t.Fatalf("expected stale DB cooldown to be cleared, got status=%s cooldown=%v", stored.HealthStatus, stored.CooldownUntil)
	}
	for _, event := range hub.Events() {
		if event.Type == "endpoint_health_change" &&
			event.Payload["endpoint_id"] == endpoint.UUID &&
			event.Payload["health_status"] == models.HealthHealthy {
			return
		}
	}
	t.Fatalf("expected REST health reconciliation to publish the ready transition; events=%#v", hub.Events())
}
