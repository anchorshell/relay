package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
)

func TestUsageSeriesStatesQueriesTerminalUsageOnly(t *testing.T) {
	requestStates := usageSeriesStates(models.MetricRequests)
	if len(requestStates) != 3 || requestStates[0] != "completed" || requestStates[1] != "failed" || requestStates[2] != "cancelled" {
		t.Fatalf("expected request usage series to query terminal states only, got %#v", requestStates)
	}

	tokenStates := usageSeriesStates(models.MetricTokens)
	if len(tokenStates) != 3 || tokenStates[0] != "completed" || tokenStates[1] != "failed" || tokenStates[2] != "cancelled" {
		t.Fatalf("expected token usage series to query terminal states only, got %#v", tokenStates)
	}
}

func TestRecentModelUsageReturnsDistinctRows(t *testing.T) {
	e, st, _ := credentialTestAPI(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Recent Usage Provider", Slug: "recent-usage-provider", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "recent-usage-lane", Enabled: true, DefaultMaxWaitMS: 30000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "Recent Usage Model", UpstreamModel: "recent-usage-model", RouteKind: models.RouteKindChat, Enabled: true}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	laneID := lane.ID
	providerID := provider.ID
	endpointID := endpoint.ID
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		finishedAt := now.Add(time.Duration(i) * time.Second)
		if err := st.Create(ctx, &models.RequestLog{
			RequestID:  fmt.Sprintf("recent-model-usage-%d", i),
			LaneID:     &laneID,
			EndpointID: &endpointID,
			ProviderID: &providerID,
			TaskState:  "completed",
			FinishedAt: &finishedAt,
			CreatedAt:  finishedAt,
			UpdatedAt:  finishedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	resp := adminRequest(t, e, http.MethodGet, "/api/stats/recent-model-usage?limit=50", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected recent model usage status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var payload RecentModelUsageResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected one distinct model usage row, got %#v", payload.Items)
	}
	item := payload.Items[0]
	if item.EndpointUUID != endpoint.UUID || item.LaneUUID != lane.UUID || item.ProviderUUID != provider.UUID {
		t.Fatalf("unexpected recent model usage ids: %#v", item)
	}
	if item.RequestCount != 3 {
		t.Fatalf("expected request count 3, got %d", item.RequestCount)
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"request_body_json", "candidate_trace_json", "upstream_request_json", "response_body_json"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("recent model usage leaked %q in %s", forbidden, body)
		}
	}
}

func TestRecentModelUsageRejectsNonIntegerLimit(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	resp := adminRequest(t, e, http.MethodGet, "/api/stats/recent-model-usage?limit=1%20OR%201=1", "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected injected limit to be rejected, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestUsageStatsReturnsCurrentUsageFromRequestLogsAfterRestart(t *testing.T) {
	ctx := context.Background()
	e, st, _, endpoint := usageStatsTestAPI(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		started := now.Add(-time.Duration(i+1) * time.Minute)
		endpointID := endpoint.ID
		if err := st.Create(ctx, &models.RequestLog{
			RequestID:  fmt.Sprintf("request-log-usage-%d", i),
			EndpointID: &endpointID,
			TaskState:  "completed",
			StatusCode: http.StatusOK,
			StartedAt:  &started,
		}); err != nil {
			t.Fatal(err)
		}
	}

	summary := usageStatsSummary(t, e, endpoint.UUID, models.MetricRequests, models.PeriodDay)
	if summary.UsedValue != 5 {
		t.Fatalf("expected request-log-backed endpoint RPD usage after restart, got %d", summary.UsedValue)
	}
}

func TestUsageStatsUsesCanonicalRequestLogsInsteadOfRuntimeState(t *testing.T) {
	ctx := context.Background()
	e, st, tracker, endpoint := usageStatsTestAPI(t)
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	endpointID := endpoint.ID
	if err := st.Create(ctx, &models.RequestLog{
		RequestID:  "canonical-request-log-usage",
		EndpointID: &endpointID,
		TaskState:  "completed",
		StatusCode: http.StatusOK,
		StartedAt:  &started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 3, now); err != nil {
		t.Fatal(err)
	}

	summary := usageStatsSummary(t, e, endpoint.UUID, models.MetricRequests, models.PeriodDay)
	if summary.UsedValue != 1 {
		t.Fatalf("expected canonical request logs to be the only active stats source, got %d", summary.UsedValue)
	}
}

func TestUsageStatsRestartShapeKeepsCooldownAndRPDProgress(t *testing.T) {
	ctx := context.Background()
	e, st, _, endpoint := usageStatsTestAPI(t)
	now := time.Now().UTC()
	cooldownUntil := now.Add(23 * time.Hour)
	if err := st.DB().WithContext(ctx).Model(&models.Endpoint{}).Where("id = ?", endpoint.ID).Updates(map[string]any{
		"health_status":  models.HealthCoolingDown,
		"cooldown_until": cooldownUntil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		started := now.Add(-time.Duration(i+1) * time.Minute)
		endpointID := endpoint.ID
		if err := st.Create(ctx, &models.RequestLog{
			RequestID:  fmt.Sprintf("restart-shape-%d", i),
			EndpointID: &endpointID,
			TaskState:  "completed",
			StatusCode: http.StatusOK,
			StartedAt:  &started,
		}); err != nil {
			t.Fatal(err)
		}
	}

	summary := usageStatsSummary(t, e, endpoint.UUID, models.MetricRequests, models.PeriodDay)
	if summary.UsedValue != 5 {
		t.Fatalf("expected realtime RPD progress to show request-log-backed 5/5, got %d", summary.UsedValue)
	}

	resp := adminRequest(t, e, http.MethodGet, "/api/endpoints", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected endpoints status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var endpoints []models.Endpoint
	if err := json.Unmarshal(resp.Body.Bytes(), &endpoints); err != nil {
		t.Fatal(err)
	}
	for _, item := range endpoints {
		if item.UUID != endpoint.UUID {
			continue
		}
		if item.CooldownUntil == nil || item.HealthStatus == models.HealthHealthy {
			t.Fatalf("expected endpoint cooldown to remain visible, got status=%s cooldown=%v", item.HealthStatus, item.CooldownUntil)
		}
		return
	}
	t.Fatalf("expected endpoint %s in response: %s", endpoint.UUID, resp.Body.String())
}

func TestUsageSeriesCountsCompletedProviderResponsesAndActualTerminalUsage(t *testing.T) {
	started := time.Date(2026, 4, 10, 12, 1, 0, 0, time.UTC)

	if !usageSeriesIncludesLog(models.RequestLog{TaskState: "completed", StatusCode: http.StatusOK, StartedAt: &started}, models.MetricRequests) {
		t.Fatal("expected successful completed request to count toward request usage")
	}
	if usageSeriesIncludesLog(models.RequestLog{TaskState: "in_flight", StatusCode: http.StatusOK, StartedAt: &started}, models.MetricRequests) {
		t.Fatal("did not expect in-flight request to count toward durable request usage")
	}
	if usageSeriesIncludesLog(models.RequestLog{TaskState: "in_flight", StatusCode: http.StatusOK, StartedAt: &started, ActualInputTokens: 10}, models.MetricTokens) {
		t.Fatal("did not expect in-flight partial tokens to count toward durable token usage")
	}
	if !usageSeriesIncludesLog(models.RequestLog{TaskState: "completed", StatusCode: http.StatusUnauthorized, StartedAt: &started}, models.MetricRequests) {
		t.Fatal("expected completed 401 provider response to count toward request usage")
	}
	if !usageSeriesIncludesLog(models.RequestLog{TaskState: "completed", StatusCode: http.StatusTooManyRequests, StartedAt: &started}, models.MetricRequests) {
		t.Fatal("expected completed 429 provider response to count toward request usage")
	}
	if usageSeriesIncludesLog(models.RequestLog{TaskState: "failed", StatusCode: http.StatusBadGateway, StartedAt: &started}, models.MetricRequests) {
		t.Fatal("did not expect failed request without actual usage to count toward request usage")
	}
	if !usageSeriesIncludesLog(models.RequestLog{TaskState: "failed", StatusCode: http.StatusBadGateway, StartedAt: &started, ActualInputTokens: 10}, models.MetricTokens) {
		t.Fatal("expected failed request with actual tokens to count toward token usage")
	}
	if usageSeriesMetricValue(models.RequestLog{TaskState: "failed", StatusCode: http.StatusBadGateway, StartedAt: &started, ActualInputTokens: 10, EstimatedInputTokens: 99}, models.MetricTokens) != 10 {
		t.Fatal("expected failed request token usage to use actual tokens only")
	}
	if input, output := usageAnalyticsTokenValues(models.RequestLog{TaskState: "failed", StatusCode: http.StatusBadGateway, StartedAt: &started, ActualTotalTokens: 12, EstimatedInputTokens: 99}); input != 0 || output != 12 {
		t.Fatalf("expected actual_total_tokens-only usage to preserve total tokens, got input=%d output=%d", input, output)
	}
	if !usageSeriesIncludesLog(models.RequestLog{TaskState: "cancelled", StatusCode: 499, StartedAt: &started, ActualCostMicros: 42}, models.MetricSpend) {
		t.Fatal("expected cancelled request with actual cost to count toward spend usage")
	}
	if usageSeriesIncludesLog(models.RequestLog{TaskState: "cancelled", StatusCode: 499, StartedAt: &started}, models.MetricRequests) {
		t.Fatal("did not expect cancelled request without actual usage to count toward request usage")
	}
}

func TestUsageSeriesTimePrefersStartedQueuedFinishedCreated(t *testing.T) {
	created := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	finished := created.Add(3 * time.Minute)
	queued := created.Add(30 * time.Second)
	started := created.Add(90 * time.Second)

	if got := usageSeriesTime(models.RequestLog{CreatedAt: created}); !got.Equal(created) {
		t.Fatalf("expected created_at fallback, got %s", got)
	}
	if got := usageSeriesTime(models.RequestLog{CreatedAt: created, FinishedAt: &finished}); !got.Equal(finished) {
		t.Fatalf("expected finished_at fallback, got %s", got)
	}
	if got := usageSeriesTime(models.RequestLog{CreatedAt: created, FinishedAt: &finished, QueuedAt: &queued}); !got.Equal(queued) {
		t.Fatalf("expected queued_at to outrank finished_at, got %s", got)
	}
	if got := usageSeriesTime(models.RequestLog{CreatedAt: created, FinishedAt: &finished, QueuedAt: &queued, StartedAt: &started}); !got.Equal(started) {
		t.Fatalf("expected started_at to outrank queued_at, got %s", got)
	}
}

func TestUsageAnalyticsUsesActualValuesBeforeEstimates(t *testing.T) {
	logWithActuals := models.RequestLog{
		ActualInputTokens:     123,
		ActualOutputTokens:    456,
		EstimatedInputTokens:  999,
		EstimatedOutputTokens: 999,
		ActualCostMicros:      321,
		EstimatedCostMicros:   654,
	}

	input, output := usageAnalyticsTokenValues(logWithActuals)
	if input != 123 || output != 456 {
		t.Fatalf("expected actual token values, got %d/%d", input, output)
	}
	if cost := usageAnalyticsCostMicros(logWithActuals); cost != 321 {
		t.Fatalf("expected actual cost, got %d", cost)
	}

	logWithEstimates := models.RequestLog{
		TaskState:             "completed",
		StatusCode:            http.StatusOK,
		EstimatedInputTokens:  12,
		EstimatedOutputTokens: 34,
		EstimatedCostMicros:   56,
	}
	input, output = usageAnalyticsTokenValues(logWithEstimates)
	if input != 12 || output != 34 {
		t.Fatalf("expected estimated token values, got %d/%d", input, output)
	}
	if cost := usageAnalyticsCostMicros(logWithEstimates); cost != 56 {
		t.Fatalf("expected estimated cost, got %d", cost)
	}
}
