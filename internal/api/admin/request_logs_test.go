package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

func TestRequestLogsRejectInjectedSortParameters(t *testing.T) {
	e, st, _ := credentialTestAPI(t)
	if err := st.Create(context.Background(), &models.RequestLog{RequestID: "safe-log", TaskState: "completed"}); err != nil {
		t.Fatal(err)
	}

	injectedSort := adminRequest(t, e, http.MethodGet, "/api/logs/requests?sort=created_at%20desc%3B%20DROP%20TABLE%20credentials%3B", "")
	if injectedSort.Code != http.StatusBadRequest {
		t.Fatalf("expected injected sort to be rejected, got %d: %s", injectedSort.Code, injectedSort.Body.String())
	}
	if strings.Contains(injectedSort.Body.String(), "DROP TABLE") {
		t.Fatalf("expected sanitized sort error, got %s", injectedSort.Body.String())
	}

	injectedDirection := adminRequest(t, e, http.MethodGet, "/api/logs/requests?sort=created_at&direction=desc%3B%20DROP%20TABLE%20credentials%3B", "")
	if injectedDirection.Code != http.StatusBadRequest {
		t.Fatalf("expected injected sort direction to be rejected, got %d: %s", injectedDirection.Code, injectedDirection.Body.String())
	}
	if strings.Contains(injectedDirection.Body.String(), "DROP TABLE") {
		t.Fatalf("expected sanitized direction error, got %s", injectedDirection.Body.String())
	}
}

func TestRequestLogsRejectNonIntegerLimit(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	resp := adminRequest(t, e, http.MethodGet, "/api/logs/requests?limit=1%20OR%201=1", "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected injected limit to be rejected, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestRequestLogsReturnPaginatedResponse(t *testing.T) {
	e, st, _ := credentialTestAPI(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := st.Create(context.Background(), &models.RequestLog{
			RequestID: fmt.Sprintf("api-paged-log-%03d", i),
			TaskState: "completed",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	resp := adminRequest(t, e, http.MethodGet, "/api/logs/requests?limit=2&offset=1", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected paginated logs status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var page store.RequestLogListPage
	if err := json.Unmarshal(resp.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Limit != 2 || page.Offset != 1 || page.NextOffset != 3 || page.HasMore {
		t.Fatalf("unexpected pagination metadata: %#v", page)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected two page items, got %d", len(page.Items))
	}
}

func TestRequestLogsRejectNegativeOffset(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	resp := adminRequest(t, e, http.MethodGet, "/api/logs/requests?offset=-1", "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected negative offset to be rejected, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestRecentFlowActivityReturnsCompactRequestRows(t *testing.T) {
	e, st, _ := credentialTestAPI(t)
	if err := st.DB().Exec("ALTER TABLE request_logs ADD COLUMN api_key_uuid text").Error; err != nil {
		t.Fatal(err)
	}
	laneID := "lane-public-id"
	endpointID := "endpoint-public-id"
	providerID := "provider-public-id"
	apiKeyUUID := "53124747-8b57-4f3c-9488-936b30361d0a"
	bodySentinel := "stored request body sentinel"
	traceSentinel := "candidate trace sentinel"
	characterizationJSON := `{"classification_duration_ms":0.5}`
	if err := st.Create(context.Background(), &models.RequestLog{
		RequestID:               "compact-flow-log",
		LaneUUID:                &laneID,
		EndpointUUID:            &endpointID,
		ProviderUUID:            &providerID,
		IncomingModel:           "incoming-model",
		SelectedUpstreamModel:   "upstream-model",
		StatusCode:              http.StatusOK,
		TaskState:               "completed",
		EstimatedInputTokens:    10,
		EstimatedOutputTokens:   20,
		WaitMS:                  17,
		LatencyMS:               2600,
		CharacterizationJSON:    &characterizationJSON,
		GuardrailStatus:         "passed",
		GuardrailPreDurationMS:  454,
		GuardrailPostDurationMS: 368,
		CandidateTraceJSON:      traceSentinel,
		RequestBodyJSON:         bodySentinel,
		UpstreamRequestJSON:     bodySentinel,
		ResponseBodyJSON:        bodySentinel,
		CreatedAt:               time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Table("request_logs").Where("request_id = ?", "compact-flow-log").Update("api_key_uuid", apiKeyUUID).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.Create(context.Background(), &models.RequestLog{
		RequestID: "queued-flow-log",
		TaskState: "queued",
		CreatedAt: time.Now().UTC().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	resp := adminRequest(t, e, http.MethodGet, "/api/stats/recent-flow-activity?limit=5", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected recent flow status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, forbidden := range []string{
		bodySentinel,
		traceSentinel,
		"candidate_trace_json",
		"request_body_json",
		"upstream_request_json",
		"response_body_json",
		"characterization_json",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("recent flow activity leaked %q in %s", forbidden, body)
		}
	}

	var payload RecentFlowActivityResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Requests) != 1 {
		t.Fatalf("expected one terminal compact request, got %d", len(payload.Requests))
	}
	if payload.Requests[0].RequestID != "compact-flow-log" ||
		payload.Requests[0].APIKeyUUID != apiKeyUUID ||
		payload.Requests[0].SelectedUpstreamModel != "upstream-model" ||
		payload.Requests[0].EstimatedInputTokens != 10 ||
		payload.Requests[0].EstimatedOutputTokens != 20 ||
		payload.Requests[0].CharacterizationMS != 0.5 ||
		payload.Requests[0].GuardrailPreMS != 454 ||
		payload.Requests[0].ProviderLatencyMS != 1778 ||
		payload.Requests[0].GuardrailPostMS != 368 ||
		payload.Requests[0].TotalTimeMS != 2617.5 {
		t.Fatalf("unexpected compact request row: %#v", payload.Requests[0])
	}
}

func TestRecentFlowActivityRejectsNonIntegerLimit(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	resp := adminRequest(t, e, http.MethodGet, "/api/stats/recent-flow-activity?limit=1%20OR%201=1", "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected injected limit to be rejected, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestRecentFlowActivityFiltersBySelectedTarget(t *testing.T) {
	e, st, _ := credentialTestAPI(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Scoped Flow Provider", Slug: "scoped-flow-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	laneA := models.RoutingLane{Name: "scoped-flow-a", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	laneB := models.RoutingLane{Name: "scoped-flow-b", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	for _, lane := range []*models.RoutingLane{&laneA, &laneB} {
		if err := st.Create(ctx, lane); err != nil {
			t.Fatal(err)
		}
	}
	endpointA := models.Endpoint{ProviderID: provider.ID, Name: "Scoped Model A", UpstreamModel: "scoped-model-a", RouteKind: models.RouteKindChat, Enabled: true}
	endpointB := models.Endpoint{ProviderID: provider.ID, Name: "Scoped Model B", UpstreamModel: "scoped-model-b", RouteKind: models.RouteKindChat, Enabled: true}
	for _, endpoint := range []*models.Endpoint{&endpointA, &endpointB} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	laneAID := laneA.ID
	laneBID := laneB.ID
	providerID := provider.ID
	endpointAID := endpointA.ID
	endpointBID := endpointB.ID
	for _, log := range []*models.RequestLog{
		{
			RequestID:  "scoped-lane-a-model-a",
			LaneID:     &laneAID,
			EndpointID: &endpointAID,
			ProviderID: &providerID,
			TaskState:  "completed",
			CreatedAt:  now.Add(-3 * time.Minute),
			UpdatedAt:  now.Add(-3 * time.Minute),
		},
		{
			RequestID:  "scoped-lane-a-model-b",
			LaneID:     &laneAID,
			EndpointID: &endpointBID,
			ProviderID: &providerID,
			TaskState:  "completed",
			CreatedAt:  now.Add(-2 * time.Minute),
			UpdatedAt:  now.Add(-2 * time.Minute),
		},
		{
			RequestID:  "scoped-lane-b-model-a",
			LaneID:     &laneBID,
			EndpointID: &endpointAID,
			ProviderID: &providerID,
			TaskState:  "completed",
			CreatedAt:  now.Add(-1 * time.Minute),
			UpdatedAt:  now.Add(-1 * time.Minute),
		},
	} {
		if err := st.Create(ctx, log); err != nil {
			t.Fatal(err)
		}
	}

	laneResp := adminRequest(t, e, http.MethodGet, "/api/stats/recent-flow-activity?limit=10&lane_id="+laneA.UUID, "")
	if laneResp.Code != http.StatusOK {
		t.Fatalf("expected lane-scoped status 200, got %d: %s", laneResp.Code, laneResp.Body.String())
	}
	var lanePayload RecentFlowActivityResponse
	if err := json.Unmarshal(laneResp.Body.Bytes(), &lanePayload); err != nil {
		t.Fatal(err)
	}
	if len(lanePayload.Requests) != 2 {
		t.Fatalf("expected two lane-scoped completions, got %#v", lanePayload.Requests)
	}
	if lanePayload.Requests[0].RequestID != "scoped-lane-a-model-b" || lanePayload.Requests[1].RequestID != "scoped-lane-a-model-a" {
		t.Fatalf("unexpected lane-scoped completions: %#v", lanePayload.Requests)
	}

	modelResp := adminRequest(t, e, http.MethodGet, "/api/stats/recent-flow-activity?limit=10&lane_id="+laneA.UUID+"&endpoint_id="+endpointA.UUID, "")
	if modelResp.Code != http.StatusOK {
		t.Fatalf("expected model-scoped status 200, got %d: %s", modelResp.Code, modelResp.Body.String())
	}
	var modelPayload RecentFlowActivityResponse
	if err := json.Unmarshal(modelResp.Body.Bytes(), &modelPayload); err != nil {
		t.Fatal(err)
	}
	if len(modelPayload.Requests) != 1 || modelPayload.Requests[0].RequestID != "scoped-lane-a-model-a" {
		t.Fatalf("expected one selected model completion, got %#v", modelPayload.Requests)
	}

	injected := adminRequest(t, e, http.MethodGet, "/api/stats/recent-flow-activity?lane_id=1%20OR%201=1", "")
	if injected.Code != http.StatusBadRequest {
		t.Fatalf("expected injected lane id to be rejected, got %d: %s", injected.Code, injected.Body.String())
	}
}

func TestAdminRequestLogViewsApplyExternalQueryScopes(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{}).
		WithExternalQueryScopeHooks(func(_ *echo.Context, input QueryScopeInput) ([]func(*gorm.DB) *gorm.DB, error) {
			switch input.Resource {
			case "request_logs", "usage_series", "recent_flow_activity", "recent_model_usage":
				return []func(*gorm.DB) *gorm.DB{
					func(db *gorm.DB) *gorm.DB {
						return db.Where("request_logs.request_id <> ?", "hidden-request")
					},
				}, nil
			default:
				return nil, nil
			}
		})
	e := echo.New()
	api.Register(e)

	now := time.Now().UTC()
	for _, requestID := range []string{"visible-request", "hidden-request"} {
		log := models.RequestLog{
			RequestID:  requestID,
			TaskState:  "completed",
			StatusCode: http.StatusOK,
			QueuedAt:   &now,
			StartedAt:  &now,
			FinishedAt: &now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := st.Create(ctx, &log); err != nil {
			t.Fatal(err)
		}
	}

	requestList := adminRequest(t, e, http.MethodGet, "/api/logs/requests", "")
	if requestList.Code != http.StatusOK {
		t.Fatalf("expected request logs status 200, got %d: %s", requestList.Code, requestList.Body.String())
	}
	if strings.Contains(requestList.Body.String(), "hidden-request") {
		t.Fatalf("request logs ignored external scope: %s", requestList.Body.String())
	}

	detail := adminRequest(t, e, http.MethodGet, "/api/logs/requests/hidden-request", "")
	if detail.Code == http.StatusOK {
		t.Fatalf("expected hidden scoped detail to be unavailable, got %d: %s", detail.Code, detail.Body.String())
	}

	series := adminRequest(t, e, http.MethodGet, "/api/stats/usage-series?metric=requests&window=hour", "")
	if series.Code != http.StatusOK {
		t.Fatalf("expected usage series status 200, got %d: %s", series.Code, series.Body.String())
	}
	var usage UsageSeriesResponse
	if err := json.Unmarshal(series.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, point := range usage.Points {
		total += point.Value
	}
	if total != 1 {
		t.Fatalf("expected scoped usage series total 1, got %d from %#v", total, usage.Points)
	}

	recentFlow := adminRequest(t, e, http.MethodGet, "/api/stats/recent-flow-activity", "")
	if recentFlow.Code != http.StatusOK {
		t.Fatalf("expected recent flow status 200, got %d: %s", recentFlow.Code, recentFlow.Body.String())
	}
	if strings.Contains(recentFlow.Body.String(), "hidden-request") {
		t.Fatalf("recent flow ignored external scope: %s", recentFlow.Body.String())
	}
}
