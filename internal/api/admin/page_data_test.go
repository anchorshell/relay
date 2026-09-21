package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func TestPageDataDoesNotAppendRequestLogs(t *testing.T) {
	e, st, _ := credentialTestAPI(t)
	if err := st.Create(context.Background(), &models.RequestLog{RequestID: "page-data-log", TaskState: "completed"}); err != nil {
		t.Fatal(err)
	}

	resp := adminRequest(t, e, http.MethodGet, "/api/page-data/requests", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected page data status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["request_logs"]; ok {
		t.Fatalf("page data should not append request logs: %s", resp.Body.String())
	}
}

func TestPageDataDashboardAndRealtimeIncludeLimitCatalog(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{})
	e := echo.New()
	api.Register(e)

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-page-data-limits", BaseURL: "https://example.com", Enabled: true}
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
	lane := models.RoutingLane{Name: "guarded-smart-group", Enabled: true, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	membership := models.LaneMembership{LaneID: lane.ID, LaneUUID: lane.UUID, EndpointID: endpoint.ID, EndpointUUID: endpoint.UUID, ManualRank: 1, Enabled: true}
	if err := st.Create(ctx, &membership); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 20,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if err := st.Create(ctx, &models.ObservedLimit{
		ScopeType:     models.ScopeEndpoint,
		ScopeID:       endpoint.ID,
		Metric:        models.MetricRequests,
		Period:        models.PeriodMinute,
		ObservedValue: 18,
		SourceHeader:  "x-ratelimit-limit-requests",
		ObservedAt:    time.Now().UTC(),
		ExpiresAt:     &expiresAt,
		Enabled:       true,
	}); err != nil {
		t.Fatal(err)
	}

	for _, page := range []string{"dashboard", "realtime"} {
		resp := adminRequest(t, e, http.MethodGet, "/api/page-data/"+page, "")
		if resp.Code != http.StatusOK {
			t.Fatalf("expected %s page data status 200, got %d: %s", page, resp.Code, resp.Body.String())
		}
		var body struct {
			Catalog struct {
				LimitPolicies  []models.LimitPolicy    `json:"limit_policies"`
				ObservedLimits []models.ObservedLimit  `json:"observed_limits"`
				Memberships    []models.LaneMembership `json:"memberships"`
			} `json:"catalog"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Catalog.LimitPolicies) != 1 || body.Catalog.LimitPolicies[0].ScopeUUID != endpoint.UUID {
			t.Fatalf("expected %s page data to include endpoint limit policy, got %s", page, resp.Body.String())
		}
		if len(body.Catalog.ObservedLimits) != 1 || body.Catalog.ObservedLimits[0].ScopeUUID != endpoint.UUID {
			t.Fatalf("expected %s page data to include endpoint observed limit, got %s", page, resp.Body.String())
		}
		if len(body.Catalog.Memberships) != 1 || body.Catalog.Memberships[0].EndpointUUID != endpoint.UUID {
			t.Fatalf("expected %s page data to include group membership, got %s", page, resp.Body.String())
		}
	}
}
