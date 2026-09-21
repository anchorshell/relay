package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func TestLiveFlowPreviewSnapshotProducerUsesResetSchedulerGeneration(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{})
	session, err := api.newLiveFlowPreviewEngineSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	firstScheduler := session.currentScheduler()
	if firstScheduler == nil {
		t.Fatal("expected initial preview scheduler")
	}
	firstGeneration := session.currentGeneration()
	if firstGeneration != 1 {
		t.Fatalf("expected initial generation 1, got %d", firstGeneration)
	}

	producer := api.liveFlowPreviewCapacitySnapshotProducer(session)
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/ws", nil), httptest.NewRecorder())
	initialEvents, err := producer(c)
	if err != nil {
		t.Fatal(err)
	}
	if got := previewGenerationFromPayload(t, initialEvents[0].Payload); got != firstGeneration {
		t.Fatalf("expected initial snapshot generation %d, got %d", firstGeneration, got)
	}

	if err := session.reset(api); err != nil {
		t.Fatal(err)
	}
	secondScheduler := session.currentScheduler()
	if secondScheduler == nil {
		t.Fatal("expected reset preview scheduler")
	}
	if secondScheduler == firstScheduler {
		t.Fatal("expected reset to replace the scheduler")
	}
	secondGeneration := session.currentGeneration()
	if secondGeneration != firstGeneration+1 {
		t.Fatalf("expected reset generation %d, got %d", firstGeneration+1, secondGeneration)
	}

	resetEvents, err := producer(c)
	if err != nil {
		t.Fatal(err)
	}
	if got := previewGenerationFromPayload(t, resetEvents[0].Payload); got != secondGeneration {
		t.Fatalf("expected reset snapshot generation %d, got %d", secondGeneration, got)
	}
}

func TestLiveFlowPreviewResetInvalidatesOldGenerationIDs(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{})
	session, err := api.newLiveFlowPreviewEngineSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	firstGeneration := session.currentGeneration()
	firstRequestID := liveFlowPreviewRequestID(session.id, firstGeneration, 1)
	if parsed := liveFlowPreviewGenerationFromRequestID(firstRequestID); parsed != firstGeneration {
		t.Fatalf("expected request id generation %d, got %d from %q", firstGeneration, parsed, firstRequestID)
	}

	if session.currentSchedulerForGeneration(firstGeneration) == nil {
		t.Fatalf("expected current scheduler for generation %d", firstGeneration)
	}
	if err := session.reset(api); err != nil {
		t.Fatal(err)
	}
	secondGeneration := session.currentGeneration()
	secondRequestID := liveFlowPreviewRequestID(session.id, secondGeneration, 1)
	if firstRequestID == secondRequestID {
		t.Fatalf("expected generation-specific request ids to differ, got %q", secondRequestID)
	}
	if session.currentSchedulerForGeneration(firstGeneration) != nil {
		t.Fatalf("expected old generation %d scheduler to be invalidated", firstGeneration)
	}
	if session.currentSchedulerForGeneration(secondGeneration) == nil {
		t.Fatalf("expected current scheduler for generation %d", secondGeneration)
	}
}

func TestLiveFlowPreviewSchedulerSerializesGroupDispatch(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-preview-serial", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy-preview-serial",
		UpstreamModel: "dummy-preview-serial",
		RouteKind:     models.RouteKindChat,
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "preview-serial-lane", Enabled: true, DefaultMaxWaitMS: 60000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{})
	session, err := api.newLiveFlowPreviewEngineSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	payload := liveFlowPreviewEnqueuePayload{
		Lane:                  lane.Name,
		RouteKind:             models.RouteKindChat,
		RequestCount:          4,
		ArrivalIntervalMS:     1,
		MaxWaitMS:             lane.DefaultMaxWaitMS,
		AllowFallback:         true,
		Priority:              50,
		EstimatedInputTokens:  10,
		EstimatedOutputTokens: 10,
		SyntheticServiceMS:    250,
	}
	if err := session.enqueue(ctx, api, payload, map[string]string{"user_uuid": "preview-user"}); err != nil {
		t.Fatal(err)
	}

	sch := session.currentScheduler()
	deadline := time.Now().Add(time.Second)
	for {
		counts := taskStateCounts(sch.Items())
		queued := counts["queued"] + counts["waiting"] + counts["ready"]
		if counts["in_flight"] == 1 && queued >= 2 {
			break
		}
		if counts["in_flight"] > 1 {
			t.Fatalf("expected at most one in-flight preview task, got counts %#v", counts)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for queued preview tasks behind one active task, got counts %#v", counts)
		}
		time.Sleep(10 * time.Millisecond)
	}

	api.externalQueueScopeHooks = []ExternalQueueScopeHook{
		func(*echo.Context, QueueScopeInput) (ExternalQueueScope, error) {
			return ExternalQueueScope{Deny: true}, nil
		},
	}
	producer := api.liveFlowPreviewCapacitySnapshotProducer(session)
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/ws", nil), httptest.NewRecorder())
	events, err := producer(c)
	if err != nil {
		t.Fatal(err)
	}
	queueItems, ok := events[0].Payload["queue_items"].([]any)
	if !ok || len(queueItems) == 0 {
		t.Fatalf("expected preview snapshot to use session scheduler items without queue scope filtering, got %#v", events[0].Payload["queue_items"])
	}

	for time.Now().Before(deadline.Add(time.Second)) {
		counts := taskStateCounts(sch.Items())
		if counts["in_flight"] > 1 {
			t.Fatalf("expected preview scheduler to keep one in-flight task, got counts %#v", counts)
		}
		if len(sch.Items()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for preview tasks to complete, final counts %#v", taskStateCounts(sch.Items()))
}

func TestLiveFlowPreviewStopOnlyStopsFutureEnqueue(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-preview-stop", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy-preview-stop",
		UpstreamModel: "dummy-preview-stop",
		RouteKind:     models.RouteKindChat,
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "preview-stop-lane", Enabled: true, DefaultMaxWaitMS: 60000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{})
	session, err := api.newLiveFlowPreviewEngineSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	payload := liveFlowPreviewEnqueuePayload{
		Lane:                  lane.Name,
		RouteKind:             models.RouteKindChat,
		RequestCount:          8,
		ArrivalIntervalMS:     100,
		MaxWaitMS:             lane.DefaultMaxWaitMS,
		AllowFallback:         true,
		Priority:              50,
		EstimatedInputTokens:  10,
		EstimatedOutputTokens: 10,
		SyntheticServiceMS:    200,
	}
	if err := session.enqueue(ctx, api, payload, map[string]string{"user_uuid": "preview-user"}); err != nil {
		t.Fatal(err)
	}

	sch := session.currentScheduler()
	deadline := time.Now().Add(time.Second)
	for {
		counts := taskStateCounts(sch.Items())
		if counts["in_flight"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for active preview task before stop, got counts %#v", counts)
		}
		time.Sleep(10 * time.Millisecond)
	}

	session.stopEnqueue()
	session.mu.Lock()
	stoppedSequence := session.sequence
	session.mu.Unlock()
	if stoppedSequence <= 0 || stoppedSequence >= int64(payload.RequestCount) {
		t.Fatalf("expected stop to interrupt enqueue before all requests were generated, sequence=%d count=%d", stoppedSequence, payload.RequestCount)
	}
	if len(sch.Items()) == 0 {
		t.Fatal("expected already generated preview tasks to remain scheduler-owned after stop")
	}
	if counts := taskStateCounts(sch.Items()); counts["in_flight"] != 1 {
		t.Fatalf("expected active preview task to keep running after stop, got counts %#v", counts)
	}

	time.Sleep(3 * time.Duration(payload.ArrivalIntervalMS) * time.Millisecond)
	session.mu.Lock()
	laterSequence := session.sequence
	session.mu.Unlock()
	if laterSequence != stoppedSequence {
		t.Fatalf("expected stop to freeze generated sequence at %d, got %d", stoppedSequence, laterSequence)
	}

	drainDeadline := time.Now().Add(time.Duration(stoppedSequence+1)*time.Duration(payload.SyntheticServiceMS)*time.Millisecond + 2*time.Second)
	for {
		if len(sch.Items()) == 0 {
			break
		}
		if time.Now().After(drainDeadline) {
			t.Fatalf("timed out waiting for stopped preview work to drain, final counts %#v", taskStateCounts(sch.Items()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
