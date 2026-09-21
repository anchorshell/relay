package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func TestNormalizeLiveFlowSimulationPayloadDefaultsRequestCount(t *testing.T) {
	payload := liveFlowSimulationPayload{
		RequestCount:          0,
		ArrivalIntervalMS:     0,
		Priority:              0,
		EstimatedInputTokens:  0,
		EstimatedOutputTokens: 0,
	}

	normalizeLiveFlowSimulationPayload(&payload)

	if payload.RequestCount != defaultLiveFlowSimulationRequestCount {
		t.Fatalf("expected live flow request count %d, got %d", defaultLiveFlowSimulationRequestCount, payload.RequestCount)
	}
	if payload.ArrivalIntervalMS != liveFlowSimulationArrivalInterval {
		t.Fatalf("expected default arrival interval %d, got %d", liveFlowSimulationArrivalInterval, payload.ArrivalIntervalMS)
	}
	if payload.Priority != 50 {
		t.Fatalf("expected default priority 50, got %d", payload.Priority)
	}
	if payload.EstimatedInputTokens != 1500 || payload.EstimatedOutputTokens != 600 {
		t.Fatalf("expected default token estimates, got %d/%d", payload.EstimatedInputTokens, payload.EstimatedOutputTokens)
	}
}

func TestNormalizeLiveFlowSimulationPayloadCapsRequestCount(t *testing.T) {
	payload := liveFlowSimulationPayload{RequestCount: maxLiveFlowSimulationRequestCount + 1}

	normalizeLiveFlowSimulationPayload(&payload)

	if payload.RequestCount != maxLiveFlowSimulationRequestCount {
		t.Fatalf("expected capped live flow request count %d, got %d", maxLiveFlowSimulationRequestCount, payload.RequestCount)
	}
}

func TestLiveFlowPreviewPlannerReusesSessionScheduler(t *testing.T) {
	api := New(nil, nil, nil, nil, "", SystemInfo{})
	ctx := context.Background()
	payload := liveFlowSimulationPayload{
		PreviewSessionID: "session-1",
		Lane:             "dummy",
		RouteKind:        models.RouteKindChat,
	}

	first, id, err := api.liveFlowPreviewPlanner(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	if id != payload.PreviewSessionID {
		t.Fatalf("expected session id %q, got %q", payload.PreviewSessionID, id)
	}
	second, _, err := api.liveFlowPreviewPlanner(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("expected same preview session to reuse the dry-run scheduler")
	}

	otherLane := payload
	otherLane.Lane = "other"
	third, _, err := api.liveFlowPreviewPlanner(ctx, otherLane)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("expected lane change with same preview session id to create a fresh scheduler")
	}

	ephemeralA, ephemeralID, err := api.liveFlowPreviewPlanner(ctx, liveFlowSimulationPayload{Lane: "dummy", RouteKind: models.RouteKindChat})
	if err != nil {
		t.Fatal(err)
	}
	ephemeralB, _, err := api.liveFlowPreviewPlanner(ctx, liveFlowSimulationPayload{Lane: "dummy", RouteKind: models.RouteKindChat})
	if err != nil {
		t.Fatal(err)
	}
	if ephemeralID != "" {
		t.Fatalf("expected ephemeral planner to return empty session id, got %q", ephemeralID)
	}
	if ephemeralA == ephemeralB {
		t.Fatal("expected payload without session id to keep backwards-compatible ephemeral behavior")
	}
}

func TestDryRunPreviewSchedulerUsesRequestContextMetadataForExternalLimits(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	endpoint := models.Endpoint{
		ID:            1,
		UUID:          "33333333-3333-4333-8333-333333333333",
		ProviderID:    1,
		ProviderUUID:  "44444444-4444-4444-8444-444444444444",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	var externalSawMetadata bool
	api := New(st, nil, nil, nil, "", SystemInfo{}).
		WithRequestContextHooks(func(context.Context, RequestContextInput) (TrustedRequestContext, error) {
			return TrustedRequestContext{Metadata: map[string]string{"user_uuid": "preview-user"}}, nil
		}).
		WithExternalLimitHooks(func(_ context.Context, input scheduler.ExternalLimitInput) ([]scheduler.ExternalLimit, error) {
			if input.Metadata["user_uuid"] == "preview-user" {
				externalSawMetadata = true
			}
			return nil, nil
		})

	req := httptest.NewRequest(http.MethodPost, "/api/simulate/live-flow", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	echoCtx := echo.New().NewContext(req, rec)
	trusted, err := api.requestContext(ctx, echoCtx, models.RouteKindChat)
	if err != nil {
		t.Fatal(err)
	}

	planner, err := api.newDryRunScheduler(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.DryRunSubmit(ctx, scheduler.SubmitRequest{
		Meta:            router.RequestMeta{RequestID: "preview-user-first", IncomingModel: "dummy", AllowFallback: true},
		Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		TrustedMetadata: trusted.Metadata,
		EnqueuedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if !externalSawMetadata {
		t.Fatal("expected preview dry-run external limits to receive request context metadata")
	}
}

func TestForgetLiveFlowPreviewPlannerSessionDropsCachedScheduler(t *testing.T) {
	api := New(nil, nil, nil, nil, "", SystemInfo{})
	ctx := context.Background()
	payload := liveFlowSimulationPayload{
		PreviewSessionID: "session-1",
		Lane:             "dummy",
		RouteKind:        models.RouteKindChat,
	}

	first, _, err := api.liveFlowPreviewPlanner(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	api.forgetLiveFlowPreviewPlannerSession(payload.PreviewSessionID)
	second, _, err := api.liveFlowPreviewPlanner(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected reset/delete path to discard cached dry-run planner")
	}
}
