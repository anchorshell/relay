package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/pkg/characterization"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testRoutingStrategyFunc func(context.Context, router.RequestMeta) ([]router.Candidate, error)

func (f testRoutingStrategyFunc) SelectCandidates(ctx context.Context, meta router.RequestMeta) ([]router.Candidate, error) {
	return f(ctx, meta)
}

func TestDisabledObservationStatesRemainEmpty(t *testing.T) {
	var log models.RequestLog
	applyCharacterizationToLog(&log, characterization.DisabledHandle())
	applyGuardrailSummaryToLog(&log, guardrails.Summary{})
	if log.PrimaryAction != nil || log.ActionConfidence != nil || log.CharacterizationVersion != nil || log.CharacterizationJSON != nil {
		t.Fatalf("disabled characterization was stored: %#v", log)
	}
	if log.GuardrailStatus != "" || log.GuardrailResultsJSON != "[]" || log.GuardrailResponseBodiesJSON != "" {
		t.Fatalf("empty guardrail summary was stored as a state: %#v", log)
	}
}

func TestRequestLogTelemetryOmitsInspectorOnlyPayloads(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	events, cancel := hub.Subscribe()
	defer cancel()
	encoded, err := json.Marshal(characterization.Characterization{
		Version: characterization.Version, PrimaryAction: characterization.ActionExplain,
		TargetObjects:            []characterization.Object{"document", "source_code"},
		OutputObjects:            []characterization.Object{"answer", "document"},
		Domains:                  []characterization.Domain{"software", "general"},
		RequiredCapabilities:     []characterization.Capability{"needs_coder"},
		ContextBurden:            characterization.ContextBurdenResult{Tier: "small"},
		ClassificationDurationMS: 0.8, ClassifierStatus: characterization.StatusComplete,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	queuedAt := time.Now().UTC().Add(-12 * time.Second)
	finishedAt := queuedAt.Add(12 * time.Second)
	server := &Scheduler{telemetry: hub}
	server.publishRequestLog(models.RequestLog{
		RequestID: "compact-event", PrimaryAction: optionalString("explain"), CharacterizationJSON: &raw,
		GuardrailStatus: "passed", GuardrailPreDurationMS: 447, GuardrailPostDurationMS: 434,
		WaitMS: 23, LatencyMS: 10_900, QueuedAt: &queuedAt, FinishedAt: &finishedAt,
		GuardrailResultsJSON: `[{"decision":"allow"}]`, GuardrailResponseBodiesJSON: `[{"body":{"large":true}}]`,
	}, nil)
	select {
	case body := <-events:
		if len(body) >= 4*1024 {
			t.Fatalf("request_log websocket payload is unexpectedly large: %d bytes", len(body))
		}
		var event telemetry.Event
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"characterization_json", "guardrail_results_json", "guardrail_response_bodies_json", "action_confidence"} {
			if _, exists := event.Payload[forbidden]; exists {
				t.Fatalf("websocket payload retained %s: %s", forbidden, body)
			}
		}
		compact, ok := event.Payload["characterization"].(map[string]any)
		if !ok || compact["primary_action"] != "explain" || event.Payload["guardrail_status"] != "passed" {
			t.Fatalf("compact websocket payload is incomplete: %s", body)
		}
		for key, expected := range map[string]float64{
			"wait_ms": 23, "characterization_duration_ms": 0.8, "guardrail_pre_duration_ms": 447, "provider_latency_ms": 10_019,
			"guardrail_post_duration_ms": 434, "total_time_ms": 10_923.8,
		} {
			if event.Payload[key] != expected {
				t.Fatalf("websocket timing %s=%v, want %v: %s", key, event.Payload[key], expected, body)
			}
		}
		targets, _ := compact["target_objects"].([]any)
		if len(targets) != 1 || targets[0] != "document" {
			t.Fatalf("websocket emitted more than the pill needs: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request_log telemetry")
	}
}

func TestRequestLogProviderLatencyIsZeroWhenPreDispatchGuardrailBlocks(t *testing.T) {
	characterizationJSON := `{"classification_duration_ms":0.4}`
	log := models.RequestLog{
		GuardrailStatus:        "blocked_pre",
		GuardrailPreDurationMS: 295,
		LatencyMS:              298,
		WaitMS:                 6,
		CharacterizationJSON:   &characterizationJSON,
	}
	if got := requestLogProviderLatencyMS(log); got != 0 {
		t.Fatalf("provider latency=%d, want 0 when provider dispatch was blocked", got)
	}
	if got := requestLogTotalTimeMS(log); got != 301.4 {
		t.Fatalf("total time=%v, want queue + characterization + pre guardrail only", got)
	}
}

func TestCatalogRefreshUsesConfiguredRoutingStrategy(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	endpoint := models.Endpoint{UUID: "authorized-endpoint", UpstreamModel: "model", RouteKind: models.RouteKindChat, Enabled: true}
	invocations := 0
	strategy := testRoutingStrategyFunc(func(routeCtx context.Context, meta router.RequestMeta) ([]router.Candidate, error) {
		invocations++
		if meta.IncomingModel != "model" {
			t.Fatalf("unexpected refreshed request metadata: %#v", meta)
		}
		if tenancy.OrganizationUUID(routeCtx) != "org-a" {
			t.Fatalf("trusted routing scope was not preserved")
		}
		return []router.Candidate{{Endpoint: endpoint}}, nil
	})
	sch := New(st, limits.NewResolver(st, tracker), tracker, telemetry.NewHub(), nil, 30000, 1000, 300000).
		WithRoutingStrategy(strategy)
	item := &task{
		meta:              router.RequestMeta{IncomingModel: "model", RouteKind: models.RouteKindChat},
		trustedMetadata:   map[string]string{"organization_uuid": "org-a", "user_uuid": "user-a"},
		catalogGeneration: st.CatalogGeneration(ctx) + 1,
	}
	if err := sch.refreshTaskCatalogIfChanged(sch.taskContext(item), item); err != nil {
		t.Fatal(err)
	}
	if invocations != 1 || len(item.candidates) != 1 || item.candidates[0].Endpoint.UUID != endpoint.UUID {
		t.Fatalf("configured routing strategy was bypassed: invocations=%d candidates=%#v", invocations, item.candidates)
	}
}

type schedulerSQLRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *schedulerSQLRecorder) LogMode(logger.LogLevel) logger.Interface { return r }
func (r *schedulerSQLRecorder) Info(context.Context, string, ...any)     {}
func (r *schedulerSQLRecorder) Warn(context.Context, string, ...any)     {}
func (r *schedulerSQLRecorder) Error(context.Context, string, ...any)    {}
func (r *schedulerSQLRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	r.mu.Lock()
	r.statements = append(r.statements, strings.ToLower(statement))
	r.mu.Unlock()
}

func (r *schedulerSQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

func (r *schedulerSQLRecorder) reset() {
	r.mu.Lock()
	r.statements = nil
	r.mu.Unlock()
}

func newScheduler(t *testing.T) (*Scheduler, *limits.Tracker, *storeWrap) {
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	s := New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	s.Start()
	t.Cleanup(s.Stop)
	return s, tracker, &storeWrap{st}
}

type storeWrap struct{ *store.Store }

func TestTaskContextPreservesTraceParentWithoutRequestValues(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	type contextKey string
	const privateRequestValue contextKey = "private-request-value"
	s := &Scheduler{backgroundCtx: context.WithValue(context.Background(), privateRequestValue, "background-only")}
	task := &task{spanContext: spanContext}

	ctx := s.taskContext(task)
	if got := trace.SpanContextFromContext(ctx); got.TraceID() != spanContext.TraceID() ||
		got.SpanID() != spanContext.SpanID() || got.TraceFlags() != spanContext.TraceFlags() {
		t.Fatalf("span context = %#v, want %#v", got, spanContext)
	}
	if got := ctx.Value(privateRequestValue); got != "background-only" {
		t.Fatalf("background value = %v", got)
	}
}

func TestHeuristicEstimatorUsesRequestBodyAndOutputLimit(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"Hello, world!"}],"max_completion_tokens":5}`)

	input, output := (HeuristicEstimator{}).Estimate(router.RequestMeta{}, body, int64(len(body)))
	if input != 4 {
		t.Fatalf("expected prompt-content input estimate 4, got %d", input)
	}
	if output != 5 {
		t.Fatalf("expected max_completion_tokens output estimate, got %d", output)
	}
}

func TestHeuristicEstimatorDoesNotCountJSONWrapperAsPrompt(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"wait_1"}],"model":"dummy"}`)

	input, output := (HeuristicEstimator{}).Estimate(router.RequestMeta{}, body, int64(len(body)))
	if input != 3 {
		t.Fatalf("expected only message content to be estimated, got %d", input)
	}
	if output != defaultEstimatedOutputTokens {
		t.Fatalf("expected default output estimate %d, got %d", defaultEstimatedOutputTokens, output)
	}
}

func TestDummyCandidateEstimateMatchesDummyProviderUsage(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Name: "dummy1", UpstreamModel: "dummy1", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{
		EndpointID:                       endpoint.ID,
		Currency:                         "USD",
		InputCostMicrosPer1MTokens:       100_000_000_000,
		OutputCostMicrosPer1MTokens:      10_000_000_000,
		CachedInputCostMicrosPer1MTokens: 0,
	}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"model":"dummy","messages":[{"role":"user","content":"wait_5"}]}`)
	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "dummy-estimate", IncomingModel: "dummy", AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		Body:       body,
		BodyBytes:  int64(len(body)),
		EnqueuedAt: time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.EstimatedInput != 1 || permit.EstimatedOutput != 4 {
		t.Fatalf("expected dummy estimate 1/4 tokens, got %d/%d", permit.EstimatedInput, permit.EstimatedOutput)
	}
	if permit.EstimatedCost != 140_000 {
		t.Fatalf("expected dummy estimated cost 140000 micros, got %d", permit.EstimatedCost)
	}
}

func TestCharacterizationCannotChangeRoutingLimitsOrPriority(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, UUID: "11111111-1111-4111-8111-111111111111", Name: "stable", UpstreamModel: "stable", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	base := SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "without-characterization", IncomingModel: "stable", Priority: 17, AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}}, BodyBytes: 64,
		EnqueuedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	}
	without, err := s.DryRunSubmit(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	prepared := characterization.Prepare(
		characterization.NormalizedRequest{EstimatedInputTokens: 16, Messages: []characterization.NormalizedMessage{{Role: characterization.RoleUser, Sequence: 0, ContentParts: []characterization.ContentPart{{Kind: characterization.PartText, Text: "Diagnose this code."}}}}},
		"chat", characterization.HarnessHints{}, characterization.DefaultThresholds(),
	)
	manager := characterization.NewManager(characterization.Config{Enabled: true, Thresholds: characterization.DefaultThresholds()})
	t.Cleanup(manager.Stop)
	base.Meta.RequestID = "with-characterization"
	base.Characterization = manager.Submit(base.Meta.RequestID, prepared)
	with, err := s.DryRunSubmit(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if with.Endpoint.UUID != without.Endpoint.UUID || with.EstimatedInput != without.EstimatedInput || with.EstimatedOutput != without.EstimatedOutput || with.EstimatedCost != without.EstimatedCost || with.FallbackCount != without.FallbackCount {
		t.Fatalf("characterization changed routing or accounting: without=%#v with=%#v", without, with)
	}
}

func TestSubmitPassesPreviewFlagToExternalLimits(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, ProviderUUID: "44444444-4444-4444-8444-444444444444", Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	sawPreview := false
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		sawPreview = input.Preview
		return nil, nil
	})

	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "preview-flag", IncomingModel: "dummy", AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		Preview:    true,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawPreview {
		t.Fatal("expected external limit hook to receive preview=true")
	}
	if err := s.CompleteSynthetic(ctx, permit, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
}

func TestAPIKeyExternalLimitScopesReuseRequestDeferral(t *testing.T) {
	for _, scope := range []string{deferScopeAPIKey, deferScopeAPIKeyModel, deferScopeAPIKeyProvider} {
		if !isRequestScopedDefer(scope) {
			t.Fatalf("%q should be request scoped", scope)
		}
	}
	if isCandidateScopedDefer(deferScopeAPIKey) || isCandidateScopedDefer(deferScopeAPIKeyModel) {
		t.Fatal("key-wide and key-model limits must not be candidate scoped")
	}
	if !isCandidateScopedDefer(deferScopeAPIKeyProvider) {
		t.Fatal("key-provider limit must preserve provider fallback")
	}
	if requestScopedDeferPriority(deferScopeAPIKey) != requestScopedDeferPriority(deferScopeUser) ||
		requestScopedDeferPriority(deferScopeAPIKeyModel) != requestScopedDeferPriority(deferScopeUserModel) ||
		requestScopedDeferPriority(deferScopeAPIKeyProvider) != requestScopedDeferPriority(deferScopeUserProvider) {
		t.Fatal("API-key scope priorities diverged from user-policy behavior")
	}
	for scope, reason := range map[string]string{
		deferScopeAPIKey:         "api_key_limit_exceeded",
		deferScopeAPIKeyModel:    "api_key_model_limit_exceeded",
		deferScopeAPIKeyProvider: "api_key_provider_limit_exceeded",
	} {
		if got := externalLimitDeferReason(ExternalLimit{DeferScope: scope}); got != reason {
			t.Fatalf("%s reason = %q, want %q", scope, got, reason)
		}
	}

	s, _, _ := newScheduler(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	s.WithExternalLimitHooks(func(context.Context, ExternalLimitInput) ([]ExternalLimit, error) {
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeAPIKey, ID: "org-a:key-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 1,
			Used:       1,
			ResetAt:    now.Add(time.Minute),
			DeferScope: deferScopeAPIKey,
		}}, nil
	})
	_, _, deferScope, deferScopeID, _, _, _, err := s.evaluateExternalLimits(
		context.Background(),
		SubmitRequest{},
		router.Candidate{},
		1,
		0,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if deferScope != deferScopeAPIKey || deferScopeID != "org-a:key-a" {
		t.Fatalf("API-key deferral identity = %q/%q, want %q/%q", deferScope, deferScopeID, deferScopeAPIKey, "org-a:key-a")
	}
}

func TestCompletionUsesResolvedCandidateIdentitiesAndBatchedPolicyState(t *testing.T) {
	for _, withLane := range []bool{false, true} {
		name := "without-lane"
		if withLane {
			name = "with-lane"
		}
		t.Run(name, func(t *testing.T) {
			st := testutil.NewStore(t)
			ctx := context.Background()
			pacingDisabled := false
			provider := models.Provider{Name: "completion-provider", Slug: "completion-provider", Enabled: true, HealthStatus: models.HealthHealthy}
			if err := st.Create(ctx, &provider); err != nil {
				t.Fatal(err)
			}
			endpoint := models.Endpoint{
				ProviderID: provider.ID, ProviderUUID: provider.UUID,
				Name: "completion-endpoint", UpstreamModel: "dummy", RouteKind: models.RouteKindChat,
				Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy, Pacing: &pacingDisabled,
			}
			if err := st.Create(ctx, &endpoint); err != nil {
				t.Fatal(err)
			}

			candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
			policies := []models.LimitPolicy{
				{ScopeType: models.ScopeGlobal, Metric: models.MetricRequests, Period: models.PeriodMinute, LimitValue: 100, Enabled: true},
				{ScopeType: models.ScopeProvider, ScopeID: provider.ID, ScopeUUID: provider.UUID, Metric: models.MetricTokens, Period: models.PeriodHour, LimitValue: 10000, Enabled: true},
				{ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID, ScopeUUID: endpoint.UUID, Metric: models.MetricSpend, Period: models.PeriodDay, LimitValue: 1000000, Enabled: true},
			}
			if withLane {
				lane := models.RoutingLane{Name: "completion-lane", Enabled: true}
				if err := st.Create(ctx, &lane); err != nil {
					t.Fatal(err)
				}
				candidate.Lane = &lane
				policies = append(policies, models.LimitPolicy{
					ScopeType: models.ScopeLane, ScopeID: lane.ID, ScopeUUID: lane.UUID,
					Metric: models.MetricRequests, Period: models.PeriodHour, LimitValue: 100, Enabled: true,
				})
			}
			for i := range policies {
				if err := st.Create(ctx, &policies[i]); err != nil {
					t.Fatal(err)
				}
			}
			recorder := &schedulerSQLRecorder{}
			st.DB().Logger = recorder
			tracker := limits.NewTracker(st)
			resolver := limits.NewResolver(st, tracker)
			s := New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
			s.Start()
			t.Cleanup(s.Stop)

			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: "bounded-completion-" + name, IncomingModel: "dummy", RouteKind: models.RouteKindChat, AllowFallback: true},
				Candidates: []router.Candidate{candidate},
				BodyBytes:  64,
				EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}

			recorder.reset()
			if err := s.Complete(ctx, permit, http.StatusOK, 1, 4); err != nil {
				t.Fatal(err)
			}

			stateUpserts := 0
			for _, statement := range recorder.snapshot() {
				if strings.Contains(statement, "usage_rollups") {
					t.Fatalf("completion referenced the removed usage_rollups table: %s", statement)
				}
				if strings.Contains(statement, "select") && strings.Contains(statement, "uuid") &&
					(strings.Contains(statement, "from `providers`") || strings.Contains(statement, "from `endpoints`") || strings.Contains(statement, "from `routing_lanes`")) {
					t.Fatalf("completion re-resolved a candidate public identity: %s", statement)
				}
				if strings.Contains(statement, "insert into `limit_policy_states`") {
					stateUpserts++
				}
			}
			if stateUpserts != 1 {
				t.Fatalf("policy-state batch statements = %d, want 1; statements=%#v", stateUpserts, recorder.snapshot())
			}

			log, err := st.GetRequestLog(ctx, "bounded-completion-"+name)
			if err != nil {
				t.Fatal(err)
			}
			if log.ProviderUUID == nil || *log.ProviderUUID != provider.UUID || log.EndpointUUID == nil || *log.EndpointUUID != endpoint.UUID {
				t.Fatalf("request log did not retain resolved provider/endpoint identities: %#v", log)
			}
			if withLane && (log.LaneUUID == nil || *log.LaneUUID != candidate.Lane.UUID) {
				t.Fatalf("request log did not retain resolved lane identity: %#v", log.LaneUUID)
			}
		})
	}
}

func TestHeuristicEstimatorPreservesExplicitEstimates(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"ignored"}],"max_tokens":123}`)

	input, output := (HeuristicEstimator{}).Estimate(router.RequestMeta{
		EstimatedInputTokens:  55,
		EstimatedOutputTokens: 66,
	}, body, int64(len(body)))
	if input != 55 || output != 66 {
		t.Fatalf("expected explicit estimates to win, got %d/%d", input, output)
	}
}

func TestHeuristicEstimatorFallsBackToByteLength(t *testing.T) {
	input, output := (HeuristicEstimator{}).Estimate(router.RequestMeta{}, nil, 13)
	if input != 3 {
		t.Fatalf("expected byte fallback input estimate 3, got %d", input)
	}
	if output != defaultEstimatedOutputTokens {
		t.Fatalf("expected default output floor %d, got %d", defaultEstimatedOutputTokens, output)
	}
}

func TestDryRunDoesNotReserveEstimatedTokensWhenThresholdModeIsEnabled(t *testing.T) {
	s, _, st := newScheduler(t)
	s.resolver.SetEstimatedUsageReservations(false, true)
	ctx := context.Background()
	pacingDisabled := false
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy, Pacing: &pacingDisabled}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricTokens,
		Period:     models.PeriodHour,
		LimitValue: 100,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	first, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "token-threshold-one", EstimatedInputTokens: 70, EstimatedOutputTokens: 10, AllowFallback: true},
		Candidates: []router.Candidate{candidate},
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.SelectedAt.Equal(base) {
		t.Fatalf("expected first dry-run request to be immediate, got %s", first.SelectedAt)
	}

	secondAt := base.Add(time.Second)
	second, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "token-threshold-two", EstimatedInputTokens: 70, EstimatedOutputTokens: 10, AllowFallback: true},
		Candidates: []router.Candidate{candidate},
		EnqueuedAt: secondAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.SelectedAt.Equal(secondAt) {
		t.Fatalf("expected threshold token mode not to reserve first estimate, got selected_at %s want %s", second.SelectedAt, secondAt)
	}
}

func TestDryRunDoesNotReserveEstimatedSpendWhenThresholdModeIsEnabled(t *testing.T) {
	s, _, st := newScheduler(t)
	s.resolver.SetEstimatedUsageReservations(true, false)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD", FlatRequestCostMicros: 800}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricSpend,
		Period:     models.PeriodHour,
		LimitValue: 1_000,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	base := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	first, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "spend-threshold-one", EstimatedInputTokens: 10, EstimatedOutputTokens: 10, AllowFallback: true},
		Candidates: []router.Candidate{candidate},
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.SelectedAt.Equal(base) {
		t.Fatalf("expected first dry-run request to be immediate, got %s", first.SelectedAt)
	}

	secondAt := base.Add(time.Second)
	second, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "spend-threshold-two", EstimatedInputTokens: 10, EstimatedOutputTokens: 10, AllowFallback: true},
		Candidates: []router.Candidate{candidate},
		EnqueuedAt: secondAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.SelectedAt.Equal(secondAt) {
		t.Fatalf("expected threshold spend mode not to reserve first estimate, got selected_at %s want %s", second.SelectedAt, secondAt)
	}
}

func TestLimitReservationSettingChangeReleasesQueuedSpendTask(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD", FlatRequestCostMicros: 20}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricSpend,
		Period:     models.PeriodMinute,
		LimitValue: 1_000,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricSpend, 990, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	permitCh := make(chan Permit, 1)
	errCh := make(chan error, 1)
	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta: router.RequestMeta{
				RequestID:             "spend-reservation-toggle",
				IncomingModel:         "dummy",
				AllowFallback:         true,
				MaxWaitMS:             70000,
				EstimatedInputTokens:  1,
				EstimatedOutputTokens: 1,
			},
			Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:  32,
			EnqueuedAt: time.Now().UTC(),
		})
		if err != nil {
			errCh <- err
			return
		}
		permitCh <- permit
	}()

	select {
	case permit := <-permitCh:
		t.Fatalf("request dispatched before reservation setting changed: %#v", permit)
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := st.UpsertSetting(ctx, store.SettingReserveEstimatedSpendForLimits, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadLimitSettingsAndReevaluate(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case permit := <-permitCh:
		if permit.Endpoint.ID != endpoint.ID {
			t.Fatalf("expected endpoint %d, got %d", endpoint.ID, permit.Endpoint.ID)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected waiting request to dispatch after reservation setting changed")
	}
}

func TestSixthRequestQueuesInsteadOfMarkingEndpointUnhealthy(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 5, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	for i := 0; i < 5; i++ {
		permit, err := s.Submit(ctx, SubmitRequest{Meta: router.RequestMeta{RequestID: time.Now().String(), AllowFallback: true}, Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, permit, 200, 10, 10); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan Permit, 1)
	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{Meta: router.RequestMeta{RequestID: "sixth", AllowFallback: true, MaxWaitMS: 30000}, Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC()})
		if err == nil {
			done <- permit
		}
	}()
	select {
	case <-done:
		t.Fatal("expected sixth request to wait, not dispatch immediately")
	case <-time.After(100 * time.Millisecond):
	}
	if endpoint.HealthStatus == models.HealthUnhealthy {
		t.Fatal("endpoint should not be marked unhealthy for minute-cap exhaustion")
	}
}

func TestWaitBudgetFallbackChoosesNextCandidate(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()
	preferred := models.Endpoint{ID: 1, ProviderID: 1, Name: "preferred", Enabled: true, ManualRank: 1}
	fallback := models.Endpoint{ID: 2, ProviderID: 1, Name: "fallback", Enabled: true, ManualRank: 2}
	for _, ep := range []models.Endpoint{preferred, fallback} {
		if err := st.Create(ctx, &ep); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: ep.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	far := time.Now().UTC().Add(45 * time.Minute)
	preferred.CooldownUntil = &far
	if err := st.Save(ctx, &preferred); err != nil {
		t.Fatal(err)
	}
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta: router.RequestMeta{RequestID: "fallback", AllowFallback: true, MaxWaitMS: 30000},
		Candidates: []router.Candidate{
			{Endpoint: preferred, Rank: 1, FallbackCount: 0},
			{Endpoint: fallback, Rank: 2, FallbackCount: 1},
		},
		BodyBytes:  128,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.Endpoint.ID != fallback.ID {
		t.Fatalf("expected fallback endpoint %d, got %d", fallback.ID, permit.Endpoint.ID)
	}
	tracker.AddConcurrency([]limits.ScopeRef{{Type: models.ScopeEndpoint, ID: fallback.ID}}, -1)
}

func TestDryRunFallsThroughWhenPreferredPacedWaitExceedsMaxWait(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()
	preferred := models.Endpoint{ID: 1, ProviderID: 1, Name: "preferred", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	fallback := models.Endpoint{ID: 2, ProviderID: 1, Name: "fallback", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy}
	for _, ep := range []models.Endpoint{preferred, fallback} {
		if err := st.Create(ctx, &ep); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: ep.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: preferred.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 5, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC()
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: preferred.ID}}, models.MetricRequests, 1, base); err != nil {
		t.Fatal(err)
	}
	candidates := []router.Candidate{
		{Endpoint: preferred, Rank: 1, FallbackCount: 0},
		{Endpoint: fallback, Rank: 2, FallbackCount: 1},
	}

	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:     "paced-cooldown-fallback",
			AllowFallback: true,
			MaxWaitMS:     5000,
		},
		Candidates: candidates,
		BodyBytes:  64,
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.Endpoint.ID != fallback.ID {
		t.Fatalf("expected fallback endpoint after paced preferred wait exceeded max wait, got endpoint %d", permit.Endpoint.ID)
	}
	if len(permit.CandidateTrace) < 2 || permit.CandidateTrace[0].EndpointID != preferred.ID || permit.CandidateTrace[0].Decision != "queued" {
		t.Fatalf("expected preferred endpoint to be queued beyond max wait, got %#v", permit.CandidateTrace)
	}
}

func TestDryRunFallsThroughWhenPreferredDailyLimitExceedsWaitBudget(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	preferred := models.Endpoint{ID: 1, ProviderID: 1, Name: "preferred", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	fallback := models.Endpoint{ID: 2, ProviderID: 1, Name: "fallback", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy}
	for _, ep := range []models.Endpoint{preferred, fallback} {
		if err := st.Create(ctx, &ep); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: ep.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: preferred.ID,
		Metric: models.MetricRequests, Period: models.PeriodDay,
		LimitValue: 1, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC()
	candidates := []router.Candidate{
		{Endpoint: preferred, Rank: 1, FallbackCount: 0},
		{Endpoint: fallback, Rank: 2, FallbackCount: 1},
	}
	first, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "daily-first", AllowFallback: true, MaxWaitMS: 60000},
		Candidates: candidates,
		BodyBytes:  64,
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Endpoint.ID != preferred.ID {
		t.Fatalf("first request should use preferred endpoint, got endpoint %d", first.Endpoint.ID)
	}

	second, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "daily-second", AllowFallback: true, MaxWaitMS: 60000},
		Candidates: candidates,
		BodyBytes:  64,
		EnqueuedAt: base.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Endpoint.ID != fallback.ID {
		t.Fatalf("second request should fall through after preferred daily cap exceeds wait budget, got endpoint %d", second.Endpoint.ID)
	}
	if len(second.CandidateTrace) < 2 || second.CandidateTrace[0].EndpointID != preferred.ID || second.CandidateTrace[0].Decision != "queued" {
		t.Fatalf("expected preferred endpoint to be queued beyond wait budget, got %#v", second.CandidateTrace)
	}
}

func TestLimitScopeHookCanRemoveGlobalLimitForTrustedUser(t *testing.T) {
	s, _, st := newScheduler(t)
	s.WithLimitScopeHooks(func(_ context.Context, input LimitScopeInput) ([]LimitScope, error) {
		if input.Metadata["user_uuid"] != "user-1" {
			return input.Scopes, nil
		}
		scopes := make([]LimitScope, 0, len(input.Scopes))
		for _, scope := range input.Scopes {
			if scope.Type != string(models.ScopeGlobal) {
				scopes = append(scopes, scope)
			}
		}
		return scopes, nil
	})

	ctx := context.Background()
	endpoint := models.Endpoint{
		ID:            1,
		UUID:          "11111111-1111-4111-8111-111111111111",
		ProviderID:    1,
		ProviderUUID:  "22222222-2222-4222-8222-222222222222",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "model",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeGlobal,
		ScopeID:    0,
		Metric:     models.MetricRequests,
		Period:     models.PeriodSecond,
		LimitValue: 1,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	candidates := []router.Candidate{{Endpoint: endpoint, Rank: 1}}
	for _, requestID := range []string{"trusted-one", "trusted-two"} {
		permit, err := s.DryRunSubmit(ctx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: requestID, AllowFallback: true},
			Candidates:      candidates,
			BodyBytes:       64,
			EnqueuedAt:      base,
			TrustedMetadata: map[string]string{"user_uuid": "user-1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !permit.SelectedAt.Equal(base) {
			t.Fatalf("expected trusted request %s to ignore global limit, got %s", requestID, permit.SelectedAt)
		}
	}

	first, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "ordinary-one", AllowFallback: true},
		Candidates: candidates,
		BodyBytes:  64,
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.SelectedAt.Equal(base) {
		t.Fatalf("expected first ordinary request to be immediate, got %s", first.SelectedAt)
	}
	second, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "ordinary-two", AllowFallback: true},
		Candidates: candidates,
		BodyBytes:  64,
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.SelectedAt.After(base) {
		t.Fatalf("expected second ordinary request to observe global limit, got %s", second.SelectedAt)
	}
}

func TestExternalLimitQueuesInsteadOfRejecting(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{
		ID:            1,
		UUID:          "33333333-3333-4333-8333-333333333333",
		ProviderID:    1,
		ProviderUUID:  "44444444-4444-4444-8444-444444444444",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "model",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	resetAt := base.Add(time.Minute)
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-1" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: "user", ID: "user-1"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 1,
			Used:       1,
			ResetAt:    resetAt,
		}}, nil
	})

	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:            router.RequestMeta{RequestID: "external-queued", AllowFallback: true, MaxWaitMS: 120000},
		Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:       64,
		EnqueuedAt:      base,
		TrustedMetadata: map[string]string{"user_uuid": "user-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !permit.SelectedAt.Equal(resetAt) {
		t.Fatalf("expected external limit to queue until %s, got %s", resetAt, permit.SelectedAt)
	}
	if len(permit.AppliedLimitState) == 0 {
		t.Fatal("expected external limit in applied limit state")
	}
}

func TestUserProviderExternalLimitFallsThroughToFallbackProvider(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	providerA := "44444444-4444-4444-8444-444444444444"
	providerB := "55555555-5555-4555-8555-555555555555"
	endpointA := models.Endpoint{
		ID:            1,
		UUID:          "33333333-3333-4333-8333-333333333333",
		ProviderID:    1,
		ProviderUUID:  providerA,
		Name:          "primary",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy",
	}
	endpointB := models.Endpoint{
		ID:            2,
		UUID:          "66666666-6666-4666-8666-666666666666",
		ProviderID:    2,
		ProviderUUID:  providerB,
		Name:          "fallback",
		Enabled:       true,
		ManualRank:    2,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy",
	}
	if err := st.Create(ctx, &endpointA); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &endpointB); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	resetAt := base.Add(time.Minute)
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.ProviderID != providerA {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:       LimitScope{Type: deferScopeUserProvider, ID: "org-1:user-a:provider:" + providerA},
			Metric:      string(models.MetricRequests),
			Period:      string(models.PeriodMinute),
			LimitValue:  2,
			Used:        2,
			ResetAt:     resetAt,
			DeferScope:  deferScopeUserProvider,
			DeferReason: deferReasonUserProviderLimit,
		}}, nil
	})

	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:     "provider-fallback",
			IncomingModel: "dummy",
			AllowFallback: true,
			MaxWaitMS:     120000,
		},
		Candidates: []router.Candidate{
			{Endpoint: endpointA, Rank: 1},
			{Endpoint: endpointB, Rank: 2, FallbackCount: 1},
		},
		BodyBytes:       64,
		EnqueuedAt:      base,
		TrustedMetadata: map[string]string{"user_uuid": "user-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.Endpoint.UUID != endpointB.UUID {
		t.Fatalf("expected fallback provider endpoint %s, got %s", endpointB.UUID, permit.Endpoint.UUID)
	}
	if !permit.SelectedAt.Equal(base) {
		t.Fatalf("expected fallback provider to dispatch immediately, got selected_at %s", permit.SelectedAt)
	}
}

func TestLiveCompletionDoesNotDoubleCountExternalUsedSnapshot(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()
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

	base := time.Now().UTC()
	resetAt := base.Add(time.Minute)
	userScope := limits.ScopeRef{Type: models.ScopeType(deferScopeUser), Key: "org-1:user-a"}
	var persistedUsed int64
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       persistedUsed,
			ResetAt:    resetAt,
		}}, nil
	})

	first, err := s.Submit(ctx, SubmitRequest{
		Meta:            router.RequestMeta{RequestID: "user-a-first", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
		Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:       64,
		EnqueuedAt:      base,
		TrustedMetadata: map[string]string{"user_uuid": "user-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, base.Add(time.Second)); got != 0 {
		t.Fatalf("expected live completion not to commit external user usage into scheduler tracker, got %d", got)
	}

	persistedUsed = 1
	secondAt := base.Add(time.Second)
	second, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:            router.RequestMeta{RequestID: "user-a-second", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
		Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:       64,
		EnqueuedAt:      secondAt,
		TrustedMetadata: map[string]string{"user_uuid": "user-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.SelectedAt.Equal(secondAt) {
		t.Fatalf("expected second request to be allowed with persisted used=1 of 2, got selected_at %s", second.SelectedAt)
	}
}

func TestLiveExternalUserRPMAllowsTwoThenDefersThird(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()
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

	base := time.Now().UTC().Add(-5 * time.Second)
	resetAt := base.Add(time.Minute)
	globalScope := limits.ScopeRef{Type: models.ScopeGlobal, ID: 0}
	userScope := limits.ScopeRef{Type: models.ScopeType(deferScopeUser), Key: "org-1:user-a"}
	var persistedUsed int64
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       persistedUsed,
			ResetAt:    resetAt,
		}}, nil
	})

	for i := 0; i < 2; i++ {
		requestAt := time.Now().UTC()
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: "user-a-allowed-" + time.Duration(i).String(), IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:       64,
			EnqueuedAt:      requestAt,
			TrustedMetadata: map[string]string{"user_uuid": "user-a"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if permit.Waited > 10*time.Millisecond {
			t.Fatalf("request %d should have dispatched immediately, waited %s", i+1, permit.Waited)
		}
		if err := s.Complete(ctx, permit, http.StatusOK, 1, 4); err != nil {
			t.Fatal(err)
		}
		persistedUsed++
	}
	if got := tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, base.Add(2*time.Second)); got != 0 {
		t.Fatalf("expected live external usage to come from persisted snapshots only, got scheduler tracker used=%d", got)
	}

	userCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(userCtx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: "user-a-third", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:       64,
			EnqueuedAt:      time.Now().UTC(),
			TrustedMetadata: map[string]string{"user_uuid": "user-a"},
		})
		done <- err
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for deferred third request to cancel")
		}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, item := range s.Items() {
			if item.RequestID != "user-a-third" {
				continue
			}
			if item.State == "waiting" && item.DeferScope == deferScopeUser {
				observedAt := time.Now().UTC()
				if got := tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, observedAt); got != 0 {
					t.Fatalf("expected deferred third request not to reserve user capacity, got %d", got)
				}
				if got := tracker.Used(globalScope, models.MetricRequests, models.PeriodMinute, observedAt); got != 2 {
					t.Fatalf("expected user defer not to add global usage beyond the two completed requests, got %d", got)
				}
				status, until, err := s.DeriveEndpointHealth(ctx, endpoint, nil, observedAt)
				if err != nil {
					t.Fatal(err)
				}
				if status != models.HealthHealthy || until != nil {
					t.Fatalf("expected user defer not to create endpoint cooldown, got status=%s until=%v", status, until)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected third request to defer on user RPM limit")
}

func TestReevaluateExternalLimitsWakesUserDeferredRequestAfterPolicyChange(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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

	resetAt := time.Now().UTC().Add(30 * time.Second)
	var used atomic.Int64
	used.Store(2)
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       used.Load(),
			ResetAt:    resetAt,
		}}, nil
	})

	userCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type submitResult struct {
		permit Permit
		err    error
	}
	done := make(chan submitResult, 1)
	go func() {
		permit, err := s.Submit(userCtx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: "user-a-recheck", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:       64,
			EnqueuedAt:      time.Now().UTC(),
			TrustedMetadata: map[string]string{"user_uuid": "user-a"},
		})
		done <- submitResult{permit: permit, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		for _, item := range s.Items() {
			if item.RequestID == "user-a-recheck" && item.State == "waiting" && item.DeferScope == deferScopeUser {
				goto deferred
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for request to defer on user limit")
		}
		time.Sleep(10 * time.Millisecond)
	}

deferred:
	used.Store(0)
	if err := s.ReevaluateExternalLimits(ctx); err != nil {
		t.Fatalf("re-evaluate external limits: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("expected deferred request to dispatch after limit changed, got %v", result.err)
		}
		if result.permit.TaskID == "" {
			t.Fatal("expected permit after external limit re-evaluation")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deferred request to dispatch after limit changed")
	}
}

func TestUserDeferredReservationScopesExcludeResourceScopesWhenResourceEligibleInFuture(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	globalScope := limits.ScopeRef{Type: models.ScopeGlobal, ID: 0}
	endpointScope := limits.ScopeRef{Type: models.ScopeEndpoint, ID: 1}
	userScope := limits.ScopeRef{Type: models.ScopeType(deferScopeUser), Key: "org-1:user-a"}

	got := reservationScopesForDecision(
		[]limits.ScopeRef{globalScope, endpointScope},
		[]limits.ScopeRef{userScope},
		deferScopeUser,
		now.Add(5*time.Second),
		now,
	)
	if len(got) != 1 || got[0] != userScope {
		t.Fatalf("expected only user external scope for user defer, got %#v", got)
	}
}

func TestExternalUserLimitDeferDoesNotCooldownEndpointOrBlockOtherUsers(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()
	pacing := false
	endpoint := models.Endpoint{
		ID:            1,
		UUID:          "33333333-3333-4333-8333-333333333333",
		ProviderID:    1,
		ProviderUUID:  "44444444-4444-4444-8444-444444444444",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy",
		Pacing:        &pacing,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeGlobal,
		ScopeID:    0,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 20,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	resetAt := base.Add(time.Minute)
	globalScope := limits.ScopeRef{Type: models.ScopeGlobal, ID: 0}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{globalScope}, models.MetricRequests, 2, base.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: "user", ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       2,
			ResetAt:    resetAt,
		}}, nil
	})

	userACtx, cancelUserA := context.WithCancel(ctx)
	userADone := make(chan error, 1)
	go func() {
		_, err := s.Submit(userACtx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: "user-a-third", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:       64,
			EnqueuedAt:      base,
			TrustedMetadata: map[string]string{"user_uuid": "user-a"},
		})
		userADone <- err
	}()
	defer func() {
		cancelUserA()
		select {
		case <-userADone:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for deferred user request to cancel")
		}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		var found bool
		for _, item := range s.Items() {
			if item.RequestID != "user-a-third" {
				continue
			}
			found = true
			if item.State == "waiting" && item.DeferScope == deferScopeUser && item.DeferReason == deferReasonUserLimitExceeded {
				goto userADeferred
			}
		}
		if time.Now().After(deadline) {
			if found {
				t.Fatal("timed out waiting for user A request to become user-deferred")
			}
			t.Fatal("timed out waiting for user A request to enter the queue")
		}
		time.Sleep(10 * time.Millisecond)
	}

userADeferred:
	userScope := limits.ScopeRef{Type: models.ScopeType(deferScopeUser), Key: "org-1:user-a"}
	if got := tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, base); got != 0 {
		t.Fatalf("expected user-deferred request not to reserve future user capacity, got %d", got)
	}
	if got := tracker.Used(globalScope, models.MetricRequests, models.PeriodMinute, base); got != 2 {
		t.Fatalf("expected global request usage to remain 2 before user B dispatch, got %d", got)
	}
	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, nil, base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthHealthy {
		t.Fatalf("expected user-scoped defer not to derive endpoint cooldown, got %s until %v", status, until)
	}
	if until != nil {
		t.Fatalf("expected no endpoint cooldown time for user-scoped defer, got %s", *until)
	}

	userBAt := base.Add(2 * time.Second)
	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:            router.RequestMeta{RequestID: "user-b-first", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
		Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:       64,
		EnqueuedAt:      userBAt,
		TrustedMetadata: map[string]string{"user_uuid": "user-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !permit.SelectedAt.Equal(userBAt) {
		t.Fatalf("expected user B to dispatch immediately at %s, got %s", userBAt, permit.SelectedAt)
	}
}

func TestUserLimitMetadataSurvivesResourceWaitPrecedence(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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

	base := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	resetAt := base
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       2,
			ResetAt:    resetAt,
		}}, nil
	})

	tests := []struct {
		name          string
		userWait      time.Duration
		resourceWait  time.Duration
		expectedReady time.Duration
	}{
		{name: "user earlier than resource", userWait: 10 * time.Second, resourceWait: 30 * time.Second, expectedReady: 30 * time.Second},
		{name: "user equal to resource", userWait: 30 * time.Second, resourceWait: 30 * time.Second, expectedReady: 30 * time.Second},
		{name: "user later than resource", userWait: 45 * time.Second, resourceWait: 30 * time.Second, expectedReady: 45 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetAt = base.Add(tt.userWait)
			cooldownUntil := base.Add(tt.resourceWait)
			endpoint.CooldownUntil = &cooldownUntil
			if err := st.Save(ctx, &endpoint); err != nil {
				t.Fatal(err)
			}

			selection, err := s.selectCandidate(ctx, SubmitRequest{
				Meta:            router.RequestMeta{RequestID: "user-a-mixed-wait", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
				Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
				BodyBytes:       64,
				EnqueuedAt:      base,
				TrustedMetadata: map[string]string{"user_uuid": "user-a"},
			}, base, 120000)
			if err != nil {
				t.Fatal(err)
			}
			if selection.deferScope != deferScopeUser || selection.deferReason != deferReasonUserLimitExceeded {
				t.Fatalf("expected user defer metadata to survive resource wait, got scope=%q reason=%q", selection.deferScope, selection.deferReason)
			}
			if !selection.userEligibleAt.Equal(resetAt) {
				t.Fatalf("expected user eligible at %s, got %s", resetAt, selection.userEligibleAt)
			}
			if selection.userLimitReason != "requests/minute" {
				t.Fatalf("expected user limit reason requests/minute, got %q", selection.userLimitReason)
			}
			if !selection.resourceEligibleAt.Equal(cooldownUntil) {
				t.Fatalf("expected resource eligible at %s, got %s", cooldownUntil, selection.resourceEligibleAt)
			}
			expectedEligible := base.Add(tt.expectedReady)
			if !selection.evaluation.EligibleAt.Equal(expectedEligible) {
				t.Fatalf("expected merged eligible at %s, got %s", expectedEligible, selection.evaluation.EligibleAt)
			}
		})
	}
}

func TestUserDeferredRequestDispatchesAfterReset(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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

	startedAt := time.Now().UTC()
	resetAt := startedAt.Add(150 * time.Millisecond)
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		used := int64(0)
		if time.Now().UTC().Before(resetAt) {
			used = 2
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodSecond),
			LimitValue: 2,
			Used:       used,
			ResetAt:    resetAt,
		}}, nil
	})

	submitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	permit, err := s.Submit(submitCtx, SubmitRequest{
		Meta:            router.RequestMeta{RequestID: "user-a-after-reset", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 2000},
		Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:       64,
		EnqueuedAt:      startedAt,
		TrustedMetadata: map[string]string{"user_uuid": "user-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.SelectedAt.Before(resetAt) {
		t.Fatalf("expected request to dispatch after user reset at %s, got %s", resetAt, permit.SelectedAt)
	}
}

func TestUserDeferredSelectionDoesNotInheritDryRunGroupTail(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	userReset := now.Add(10 * time.Second)

	s.mu.Lock()
	s.dryRunLaneTails["model:dummy"] = now.Add(time.Minute)
	s.mu.Unlock()

	selection := &queuedSelection{
		candidate: router.Candidate{Endpoint: endpoint, Rank: 1},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: userReset,
			Reason:     "requests/minute",
		},
		resourceEligibleAt: now,
		deferScope:         deferScopeUser,
		deferReason:        deferReasonUserLimitExceeded,
	}
	s.applyGroupFIFOSelection(SubmitRequest{
		Meta: router.RequestMeta{IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
	}, selection, now, true)

	if !selection.evaluation.EligibleAt.Equal(userReset) {
		t.Fatalf("expected user defer to ignore dry-run group tail, got %s", selection.evaluation.EligibleAt)
	}
	if !selection.resourceEligibleAt.Equal(now) {
		t.Fatalf("expected user defer to keep resource eligibility at %s, got %s", now, selection.resourceEligibleAt)
	}
}

func TestGroupFIFODoesNotOverwriteResourceEligibility(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	blocking := models.Endpoint{
		ID:            1,
		UUID:          "22222222-2222-4222-8222-222222222222",
		ProviderID:    1,
		ProviderUUID:  "44444444-4444-4444-8444-444444444444",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy",
	}
	if err := st.Create(ctx, &blocking); err != nil {
		t.Fatal(err)
	}
	healthy := models.Endpoint{
		ID:            2,
		UUID:          "33333333-3333-4333-8333-333333333333",
		ProviderID:    1,
		ProviderUUID:  "44444444-4444-4444-8444-444444444444",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy3",
	}
	if err := st.Create(ctx, &healthy); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	groupTail := now.Add(5 * time.Second)
	lane := models.RoutingLane{ID: 9, UUID: "99999999-9999-4999-8999-999999999999", Name: "agentic", Enabled: true}

	s.mu.Lock()
	s.tasks["group-fifo-tail"] = &task{
		id: "group-fifo-tail",
		meta: router.RequestMeta{
			RequestID:     "group-fifo-tail",
			IncomingModel: "agentic",
			Lane:          "agentic",
		},
		candidate: router.Candidate{Endpoint: blocking, Lane: &lane, Rank: 1},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: groupTail,
			Reason:     "endpoint cooldown",
		},
		resourceEligibleAt: groupTail,
		enqueuedAt:         now,
		state:              "waiting",
	}
	s.mu.Unlock()

	selection := &queuedSelection{
		candidate:          router.Candidate{Endpoint: healthy, Lane: &lane, Rank: 3},
		evaluation:         limits.CandidateEvaluation{EligibleAt: now},
		resourceEligibleAt: now,
	}
	s.applyGroupFIFOSelection(SubmitRequest{
		Meta: router.RequestMeta{IncomingModel: "agentic", Lane: "agentic", AllowFallback: true, MaxWaitMS: 60000},
	}, selection, now, false)

	if !selection.evaluation.EligibleAt.Equal(groupTail) {
		t.Fatalf("expected group FIFO to delay dispatch until %s, got %s", groupTail, selection.evaluation.EligibleAt)
	}
	if !selection.resourceEligibleAt.Equal(now) {
		t.Fatalf("expected resource eligibility to remain %s, got %s", now, selection.resourceEligibleAt)
	}
}

func TestDerivedEndpointHealthIgnoresFutureResourceTimeOnUserDeferredTask(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)

	s.mu.Lock()
	s.tasks["user-deferred-future-resource"] = &task{
		id: "user-deferred-future-resource",
		meta: router.RequestMeta{
			RequestID:     "user-deferred-future-resource",
			IncomingModel: "dummy",
		},
		candidate: router.Candidate{Endpoint: endpoint, Rank: 1},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: now.Add(time.Minute),
			Reason:     "requests/minute",
		},
		resourceEligibleAt: now.Add(5 * time.Second),
		deferScope:         deferScopeUser,
		deferReason:        deferReasonUserLimitExceeded,
		enqueuedAt:         now,
		state:              "waiting",
	}
	s.mu.Unlock()

	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthHealthy {
		t.Fatalf("expected user-scoped defer not to derive endpoint cooldown, got %s until %v", status, until)
	}
	if until != nil {
		t.Fatalf("expected no endpoint cooldown time for user-scoped defer, got %s", *until)
	}
}

func TestDerivedEndpointHealthIgnoresGroupFIFOWait(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{
		ID:            1,
		UUID:          "33333333-3333-4333-8333-333333333333",
		ProviderID:    1,
		ProviderUUID:  "44444444-4444-4444-8444-444444444444",
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		UpstreamModel: "dummy3",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	groupTail := now.Add(5 * time.Second)

	s.mu.Lock()
	s.tasks["group-fifo-wait"] = &task{
		id: "group-fifo-wait",
		meta: router.RequestMeta{
			RequestID:     "group-fifo-wait",
			IncomingModel: "agentic",
			Lane:          "agentic",
		},
		candidate: router.Candidate{Endpoint: endpoint, Rank: 3},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: groupTail,
			Reason:     "group queue",
		},
		resourceEligibleAt: now,
		enqueuedAt:         now,
		state:              "waiting",
	}
	s.mu.Unlock()

	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthHealthy {
		t.Fatalf("expected group FIFO wait not to derive endpoint cooldown, got %s until %v", status, until)
	}
	if until != nil {
		t.Fatalf("expected no endpoint cooldown time for group FIFO wait, got %s", *until)
	}
}

func TestDerivedEndpointHealthUsesEarliestQueuedResourceSlot(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	firstSlot := now.Add(12 * time.Second)
	secondSlot := now.Add(24 * time.Second)

	s.mu.Lock()
	s.tasks["next-slot"] = &task{
		id: "next-slot",
		meta: router.RequestMeta{
			RequestID:     "next-slot",
			IncomingModel: "agentic",
		},
		candidate: router.Candidate{Endpoint: endpoint, Rank: 1},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: firstSlot,
			Reason:     "requests/minute",
		},
		resourceEligibleAt: firstSlot,
		enqueuedAt:         now,
		state:              "waiting",
	}
	s.tasks["queue-tail"] = &task{
		id: "queue-tail",
		meta: router.RequestMeta{
			RequestID:     "queue-tail",
			IncomingModel: "agentic",
		},
		candidate: router.Candidate{Endpoint: endpoint, Rank: 1},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: secondSlot,
			Reason:     "requests/minute",
		},
		resourceEligibleAt: secondSlot,
		enqueuedAt:         now,
		state:              "waiting",
	}
	s.mu.Unlock()

	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthCoolingDown {
		t.Fatalf("expected endpoint to cool down until next slot, got %s until %v", status, until)
	}
	if until == nil || !until.Equal(firstSlot) {
		t.Fatalf("expected first resource slot %s, got %v", firstSlot, until)
	}
}

func TestSyntheticCompletionCountsExternalUserScopeForNextPreviewRequest(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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

	base := time.Now().UTC()
	resetAt := base.Add(time.Minute)
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "preview-user" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:preview-user"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       0,
			ResetAt:    resetAt,
		}}, nil
	})

	for i := 0; i < 2; i++ {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: "preview-allowed-" + time.Duration(i).String(), IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:       64,
			EnqueuedAt:      base.Add(time.Duration(i) * time.Second),
			TrustedMetadata: map[string]string{"user_uuid": "preview-user"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteSynthetic(ctx, permit, http.StatusOK, 1, 4); err != nil {
			t.Fatal(err)
		}
	}

	userCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := s.Submit(userCtx, SubmitRequest{
			Meta:            router.RequestMeta{RequestID: "preview-blocked-third", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates:      []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:       64,
			EnqueuedAt:      base.Add(2 * time.Second),
			TrustedMetadata: map[string]string{"user_uuid": "preview-user"},
		})
		done <- err
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for blocked preview request to cancel")
		}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, item := range s.Items() {
			if item.RequestID != "preview-blocked-third" {
				continue
			}
			if item.State == "waiting" && item.DeferScope == deferScopeUser {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected third synthetic request to wait on user external limit")
}

func TestUserDeferredTaskDoesNotSetGroupFIFOTail(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
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
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)

	s.mu.Lock()
	s.tasks["user-deferred-group-tail"] = &task{
		id: "user-deferred-group-tail",
		meta: router.RequestMeta{
			RequestID:     "user-deferred-group-tail",
			IncomingModel: "dummy",
			AllowFallback: true,
			MaxWaitMS:     120000,
		},
		candidate: router.Candidate{Endpoint: endpoint, Rank: 1},
		evaluation: limits.CandidateEvaluation{
			EligibleAt: now.Add(time.Minute),
			Reason:     "requests/minute",
		},
		resourceEligibleAt: now.Add(5 * time.Second),
		deferScope:         deferScopeUser,
		deferReason:        deferReasonUserLimitExceeded,
		enqueuedAt:         now.Add(-time.Second),
		state:              "waiting",
	}
	s.mu.Unlock()

	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "user-b-first", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  64,
		EnqueuedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !permit.SelectedAt.Equal(now) {
		t.Fatalf("expected other user request not to wait behind user defer, got selected_at %s", permit.SelectedAt)
	}
}

func TestDryRunPreservesRankOrderForPacedFallbackEndpoint(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	unpaced := false
	paced := true
	primary := models.Endpoint{ID: 1, ProviderID: 1, Name: "primary", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy, Pacing: &unpaced}
	second := models.Endpoint{ID: 2, ProviderID: 1, Name: "second", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy, Pacing: &paced}
	third := models.Endpoint{ID: 3, ProviderID: 1, Name: "third", Enabled: true, ManualRank: 3, HealthStatus: models.HealthHealthy, Pacing: &paced}
	for _, ep := range []models.Endpoint{primary, second, third} {
		if err := st.Create(ctx, &ep); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: ep.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: primary.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 2, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: second.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 10, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	candidates := []router.Candidate{
		{Endpoint: primary, Rank: 1, FallbackCount: 0},
		{Endpoint: second, Rank: 2, FallbackCount: 1},
		{Endpoint: third, Rank: 3, FallbackCount: 2},
	}
	selected := make([]uint, 0, 5)
	for i := 0; i < 5; i++ {
		permit, err := s.DryRunSubmit(ctx, SubmitRequest{
			Meta: router.RequestMeta{
				RequestID:     "ranked-paced-fallback-" + time.Duration(i).String(),
				AllowFallback: true,
				MaxWaitMS:     5000,
			},
			Candidates: candidates,
			BodyBytes:  64,
			EnqueuedAt: base.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, permit.Endpoint.ID)
	}
	want := []uint{primary.ID, primary.ID, second.ID, second.ID, third.ID}
	for i := range want {
		if selected[i] != want[i] {
			t.Fatalf("selection %d: expected endpoint %d, got %d (all selections: %#v)", i+1, want[i], selected[i], selected)
		}
	}
}

func TestDryRunFallsThroughWhenPersistedUsageHitsPreferredHardCaps(t *testing.T) {
	for _, tc := range []struct {
		name         string
		metric       models.Metric
		limit        int64
		logCount     int
		logTokens    int64
		logSpend     int64
		inputTokens  int64
		outputTokens int64
		requestCost  int64
	}{
		{
			name:         "requests per day",
			metric:       models.MetricRequests,
			limit:        10,
			logCount:     10,
			inputTokens:  100,
			outputTokens: 50,
		},
		{
			name:         "tokens per day",
			metric:       models.MetricTokens,
			limit:        5_000,
			logCount:     1,
			logTokens:    5_000,
			inputTokens:  1_500,
			outputTokens: 600,
		},
		{
			name:         "spend per day",
			metric:       models.MetricSpend,
			limit:        5_000,
			logCount:     1,
			logSpend:     5_000,
			inputTokens:  100,
			outputTokens: 50,
			requestCost:  1_000,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tracker, st := newScheduler(t)
			ctx := context.Background()
			preferred := models.Endpoint{ID: 1, ProviderID: 1, Name: "preferred", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
			fallback := models.Endpoint{ID: 2, ProviderID: 1, Name: "fallback", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy}
			for _, ep := range []models.Endpoint{preferred, fallback} {
				if err := st.Create(ctx, &ep); err != nil {
					t.Fatal(err)
				}
				if err := st.Create(ctx, &models.PricingPolicy{
					EndpointID:            ep.ID,
					Currency:              "USD",
					FlatRequestCostMicros: tc.requestCost,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Create(ctx, &models.LimitPolicy{
				ScopeType: models.ScopeEndpoint, ScopeID: preferred.ID,
				Metric: tc.metric, Period: models.PeriodDay,
				LimitValue: tc.limit, Enabled: true, Source: "configured",
			}); err != nil {
				t.Fatal(err)
			}

			base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
			for i := 0; i < tc.logCount; i++ {
				startedAt := base.Add(-time.Duration(i+1) * time.Minute)
				tokens := tc.logTokens
				if tokens == 0 {
					tokens = tc.inputTokens + tc.outputTokens
				}
				if err := st.Create(ctx, &models.RequestLog{
					RequestID:             tc.name + "-" + time.Duration(i).String(),
					EndpointID:            &preferred.ID,
					ProviderID:            &preferred.ProviderID,
					TaskState:             "completed",
					StatusCode:            200,
					StartedAt:             &startedAt,
					FinishedAt:            &startedAt,
					ActualTotalTokens:     tokens,
					EstimatedInputTokens:  tc.inputTokens,
					EstimatedOutputTokens: tc.outputTokens,
					ActualCostMicros:      tc.logSpend,
					EstimatedCostMicros:   tc.logSpend,
					CreatedAt:             startedAt,
					UpdatedAt:             startedAt,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := tracker.HydrateRecentUsage(ctx, base); err != nil {
				t.Fatal(err)
			}

			permit, err := s.DryRunSubmit(ctx, SubmitRequest{
				Meta: router.RequestMeta{
					RequestID:             "hard-cap-preview",
					AllowFallback:         true,
					MaxWaitMS:             60000,
					EstimatedInputTokens:  tc.inputTokens,
					EstimatedOutputTokens: tc.outputTokens,
				},
				Candidates: []router.Candidate{
					{Endpoint: preferred, Rank: 1, FallbackCount: 0},
					{Endpoint: fallback, Rank: 2, FallbackCount: 1},
				},
				BodyBytes:  64,
				EnqueuedAt: base,
			})
			if err != nil {
				t.Fatal(err)
			}
			if permit.Endpoint.ID != fallback.ID {
				t.Fatalf("expected fallback endpoint after persisted %s cap, got endpoint %d", tc.metric, permit.Endpoint.ID)
			}
			if len(permit.CandidateTrace) < 2 || permit.CandidateTrace[0].EndpointID != preferred.ID || permit.CandidateTrace[0].Decision != "queued" {
				t.Fatalf("expected preferred endpoint to be queued beyond wait budget, got %#v", permit.CandidateTrace)
			}
		})
	}
}

func TestDryRunFallbackWaitsBehindEarlierGroupQueue(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	lane := models.RoutingLane{Name: "primary-fifo", Enabled: true, DefaultMaxWaitMS: 60000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	preferred := models.Endpoint{ID: 1, ProviderID: 1, Name: "preferred", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	fallback := models.Endpoint{ID: 2, ProviderID: 1, Name: "fallback", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy}
	for _, ep := range []models.Endpoint{preferred, fallback} {
		if err := st.Create(ctx, &ep); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: ep.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: preferred.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 10, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: preferred.ID,
		Metric: models.MetricRequests, Period: models.PeriodDay,
		LimitValue: 6, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	candidates := []router.Candidate{
		{Endpoint: preferred, Lane: &lane, Rank: 1, FallbackCount: 0},
		{Endpoint: fallback, Lane: &lane, Rank: 2, FallbackCount: 1},
	}
	var lastPreferredSelectedAt time.Time
	for i := 0; i < 6; i++ {
		permit, err := s.DryRunSubmit(ctx, SubmitRequest{
			Meta: router.RequestMeta{
				RequestID:             "group-fifo-preferred-" + time.Duration(i).String(),
				Lane:                  lane.Name,
				AllowFallback:         true,
				MaxWaitMS:             lane.DefaultMaxWaitMS,
				EstimatedInputTokens:  100,
				EstimatedOutputTokens: 50,
			},
			Candidates: candidates,
			EnqueuedAt: base.Add(time.Duration(i) * 250 * time.Millisecond),
		})
		if err != nil {
			t.Fatal(err)
		}
		if permit.Endpoint.ID != preferred.ID {
			t.Fatalf("request %d should select preferred endpoint, got %d", i+1, permit.Endpoint.ID)
		}
		lastPreferredSelectedAt = permit.SelectedAt
	}

	permit, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:             "group-fifo-fallback",
			Lane:                  lane.Name,
			AllowFallback:         true,
			MaxWaitMS:             lane.DefaultMaxWaitMS,
			EstimatedInputTokens:  100,
			EstimatedOutputTokens: 50,
		},
		Candidates: candidates,
		EnqueuedAt: base.Add(6 * 250 * time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if permit.Endpoint.ID != fallback.ID {
		t.Fatalf("seventh request should fall through to fallback after preferred daily cap, got %d", permit.Endpoint.ID)
	}
	if permit.SelectedAt.Before(lastPreferredSelectedAt) {
		t.Fatalf("fallback selected at %s before earlier group queue tail %s", permit.SelectedAt, lastPreferredSelectedAt)
	}
	if permit.Waited <= 0 {
		t.Fatal("expected fallback request to wait behind earlier group queue")
	}
	if len(permit.CandidateTrace) < 2 || permit.CandidateTrace[0].EndpointID != preferred.ID || permit.CandidateTrace[0].Decision != "queued" {
		t.Fatalf("expected preferred endpoint to be queued beyond wait budget, got %#v", permit.CandidateTrace)
	}
}

func TestRejectsWhenAllCandidatesExceedBudget(t *testing.T) {
	s, _, st := newScheduler(t)
	var rejectedEvent RequestRejectedEvent
	s.WithRequestRejectedHooks(func(_ context.Context, event RequestRejectedEvent) {
		rejectedEvent = event
	})
	ctx := context.Background()
	lane := models.RoutingLane{Name: "all-resources-exhausted", Enabled: true, DefaultMaxWaitMS: 30000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	a := models.Endpoint{ID: 1, ProviderID: 1, Name: "a", Enabled: true, ManualRank: 1}
	b := models.Endpoint{ID: 2, ProviderID: 1, Name: "b", Enabled: true, ManualRank: 2}
	for _, ep := range []models.Endpoint{a, b} {
		if err := st.Create(ctx, &ep); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: ep.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
		cooldown := time.Now().UTC().Add(time.Hour)
		ep.CooldownUntil = &cooldown
		if err := st.Save(ctx, &ep); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "queued", Lane: lane.Name, AllowFallback: true, MaxWaitMS: 30000},
		Candidates: []router.Candidate{{Endpoint: a, Lane: &lane, Rank: 1}, {Endpoint: b, Lane: &lane, Rank: 2, FallbackCount: 1}},
		BodyBytes:  64, EnqueuedAt: time.Now().UTC(),
		TrustedMetadata: map[string]string{
			"organization_uuid": "org-a",
			"user_uuid":         "user-a",
		},
	})
	var rejection Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected scheduler Rejection when all candidates exceed budget, got %T %v", err, err)
	}
	if len(s.Items()) != 0 {
		t.Fatalf("expected rejected request not to enter queue, got %d", len(s.Items()))
	}
	log, err := st.GetRequestLog(ctx, "queued")
	if err != nil {
		t.Fatalf("expected exhausted routing request to be persisted: %v", err)
	}
	if log.StatusCode != http.StatusTooManyRequests || log.TaskState != "failed" {
		t.Fatalf("expected persisted 429 failure, got status=%d state=%q", log.StatusCode, log.TaskState)
	}
	if log.EndpointID != nil || log.ProviderID != nil || log.SelectedUpstreamModel != "" {
		t.Fatalf("expected rejected request to have no selected upstream, got endpoint=%v provider=%v model=%q", log.EndpointID, log.ProviderID, log.SelectedUpstreamModel)
	}
	if log.LaneUUID == nil || *log.LaneUUID != lane.UUID {
		t.Fatalf("expected rejected request to retain routing group UUID %q, got %v", lane.UUID, log.LaneUUID)
	}
	if log.ActualInputTokens != 0 || log.ActualOutputTokens != 0 || log.ActualTotalTokens != 0 || log.ActualCostMicros != 0 {
		t.Fatalf("expected rejected request to record zero actual usage, got %#v", log)
	}
	if log.ErrorText != rejection.Reason || log.CandidateTraceJSON == "" || log.CandidateTraceJSON == "[]" {
		t.Fatalf("expected rejection reason and candidate trace, got reason=%q trace=%q", log.ErrorText, log.CandidateTraceJSON)
	}
	foundEvent := false
	for _, event := range s.telemetry.Events() {
		if event.Type != "request_log" || event.Payload["request_id"] != "queued" {
			continue
		}
		foundEvent = true
		if event.Payload["status_code"] != http.StatusTooManyRequests || event.Payload["task_state"] != "failed" {
			t.Fatalf("expected live rejected request log event, got %#v", event.Payload)
		}
		eventLaneUUID, ok := event.Payload["lane_id"].(*string)
		if !ok || eventLaneUUID == nil || *eventLaneUUID != lane.UUID {
			t.Fatalf("expected live rejected request to retain routing group UUID %q, got %#v", lane.UUID, event.Payload["lane_id"])
		}
	}
	if !foundEvent {
		t.Fatal("expected rejected request to be published to live request logs")
	}
	if rejectedEvent.RequestID != "queued" || rejectedEvent.StatusCode != http.StatusTooManyRequests || rejectedEvent.State != "failed" {
		t.Fatalf("expected rejected request hook to receive the terminal 429, got %#v", rejectedEvent)
	}
	if rejectedEvent.Metadata == nil {
		t.Fatal("expected rejected request hook to preserve trusted metadata")
	}
}

func TestSubmitCancellationBeforeTaskCreationEmitsTerminalEvent(t *testing.T) {
	s, _, st := newScheduler(t)
	requestID := "cancelled-before-task"
	var rejectedEvent RequestRejectedEvent
	s.WithRequestRejectedHooks(func(_ context.Context, event RequestRejectedEvent) {
		rejectedEvent = event
	})
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	enqueuedAt := time.Now().UTC().Add(-time.Second)
	s.recordSubmitRejection(cancelledCtx, SubmitRequest{
		Meta:            router.RequestMeta{RequestID: requestID},
		EnqueuedAt:      enqueuedAt,
		TrustedMetadata: map[string]string{"actor_id": "test-user"},
	}, enqueuedAt, context.Canceled)

	if rejectedEvent.RequestID != requestID || rejectedEvent.State != "cancelled" || rejectedEvent.StatusCode != 499 {
		t.Fatalf("unexpected cancellation event: %#v", rejectedEvent)
	}
	log, err := st.GetRequestLog(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if log.TaskState != "cancelled" || log.StatusCode != 499 || log.ErrorText != "client canceled request" {
		t.Fatalf("unexpected cancellation log: %#v", log)
	}
}

func TestReservationsForceBurstRequestsIntoFutureSlots(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID,
		Metric: models.MetricRequests, Period: models.PeriodMinute,
		LimitValue: 1, Enabled: true, Source: "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	firstDone := make(chan Permit, 1)
	secondDone := make(chan Permit, 1)

	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{Meta: router.RequestMeta{RequestID: "first", AllowFallback: true}, Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC()})
		if err == nil {
			firstDone <- permit
		}
	}()
	first := <-firstDone

	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{Meta: router.RequestMeta{RequestID: "second", AllowFallback: true}, Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC()})
		if err == nil {
			secondDone <- permit
		}
	}()

	select {
	case <-secondDone:
		t.Fatal("expected second request to remain queued behind the first reservation")
	case <-time.After(100 * time.Millisecond):
	}

	if err := s.Complete(ctx, first, 200, 10, 10); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentBurstPacesFivePerMinute(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	permits := make(chan Permit, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: time.Now().String(), AllowFallback: true, MaxWaitMS: 120000},
				Candidates: []router.Candidate{candidate},
				BodyBytes:  32,
				EnqueuedAt: time.Now().UTC(),
			})
			if err == nil {
				permits <- permit
			}
		}(i)
	}

	time.Sleep(150 * time.Millisecond)
	if got := len(permits); got != 1 {
		t.Fatalf("expected 1 immediate permit with paced 5 RPM, got %d", got)
	}
	if queued := len(s.Items()); queued != 8 {
		t.Fatalf("expected all 8 tasks tracked (1 active, 7 queued), got %d", queued)
	}

	for len(permits) > 0 {
		permit := <-permits
		if err := s.Complete(context.Background(), permit, 200, 10, 10); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
}

func TestRapidEndpointRPMFiveDoesNotAdmitSixthImmediateRequest(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	pacing := false
	provider := models.Provider{
		Name:         "test",
		Slug:         "test",
		BaseURL:      "https://upstream.example.test",
		AuthMode:     "none",
		Enabled:      true,
		HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		ProviderUUID:  provider.UUID,
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		Pacing:        &pacing,
		UpstreamModel: "dummy",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 2, 18, 0, 0, 0, time.UTC)
	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	for i := 0; i < 5; i++ {
		permit, err := s.DryRunSubmit(ctx, SubmitRequest{
			Meta:       router.RequestMeta{RequestID: "endpoint-allowed-" + time.Duration(i).String(), IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
			Candidates: []router.Candidate{candidate},
			BodyBytes:  32,
			EnqueuedAt: base,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !permit.SelectedAt.Equal(base) {
			t.Fatalf("request %d should reserve immediate endpoint capacity, got %s", i+1, permit.SelectedAt)
		}
	}

	sixth, err := s.DryRunSubmit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "endpoint-sixth", IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
		Candidates: []router.Candidate{candidate},
		BodyBytes:  32,
		EnqueuedAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sixth.SelectedAt.After(base) {
		t.Fatalf("sixth request should wait after 5 RPM endpoint capacity is full, got %s", sixth.SelectedAt)
	}
	if len(sixth.CandidateTrace) == 0 || sixth.CandidateTrace[0].Reason != "requests/minute" {
		t.Fatalf("expected sixth request to wait on requests/minute, got %#v", sixth.CandidateTrace)
	}
}

func TestConcurrentEndpointRPMFiveAdmitsAtMostFiveImmediateRequests(t *testing.T) {
	s, tracker, st := newScheduler(t)
	s.SetSerialGroupDispatch(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pacing := false
	provider := models.Provider{
		Name:         "test",
		Slug:         "test",
		BaseURL:      "https://upstream.example.test",
		AuthMode:     "none",
		Enabled:      true,
		HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		ProviderUUID:  provider.UUID,
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
		Pacing:        &pacing,
		UpstreamModel: "dummy",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	permits := make(chan Permit, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: "endpoint-concurrent-" + time.Duration(i).String(), IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
				Candidates: []router.Candidate{candidate},
				BodyBytes:  32,
				EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}(i)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(permits) < 5 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(permits); got != 5 {
		t.Fatalf("expected exactly 5 immediate permits for 5 RPM endpoint cap, got %d", got)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 5 {
		t.Fatalf("expected endpoint runtime usage to stop at 5, got %d", got)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(permits); got != 5 {
		t.Fatalf("sixth request should remain queued, got %d immediate permits", got)
	}

	cancel()
	wg.Wait()
	close(permits)
	for permit := range permits {
		if err := s.Complete(context.Background(), permit, http.StatusOK, 1, 4); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSerialGroupDispatchDefaultAllowsOneInFlightPerGroup(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	lane := models.RoutingLane{ID: 1, Name: "agentic", Enabled: true, DefaultMaxWaitMS: 60000}
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan Permit, 3)
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: "serial-same-group-" + time.Duration(i).String(), Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
				Candidates: []router.Candidate{candidate},
				BodyBytes:  32,
				EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}(i)
	}

	first := receivePermit(t, permits, errs, time.Second)
	assertNoPermit(t, permits, errs, 150*time.Millisecond)
	assertTaskStates(t, s, map[string]int{"in_flight": 1, "ready": 2})

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermitWithScheduler(t, permits, errs, s, time.Second)
	assertNoPermit(t, permits, errs, 150*time.Millisecond)
	assertTaskStates(t, s, map[string]int{"in_flight": 1, "ready": 1})

	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	third := receivePermit(t, permits, errs, time.Second)
	assertTaskStates(t, s, map[string]int{"in_flight": 1})

	if err := s.CompleteSynthetic(ctx, third, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	waitForSubmitters(t, &wg)
}

func TestAuthoritativeCompletionPostProcessingRunsAsynchronously(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	s := New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000).
		WithAuthoritativeFinalization(true)
	s.Start()
	t.Cleanup(s.Stop)

	endpoint := models.Endpoint{ProviderID: 1, Name: "async-post", UpstreamModel: "async-post", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.RequestLog{RequestID: "async-post-request", TaskState: "in_flight"}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	callbackName := "test:block-async-completion-enrichment"
	if err := st.DB().Callback().Update().Before("gorm:update").Register(callbackName, func(db *gorm.DB) {
		if db.Statement.Table != "request_logs" {
			return
		}
		enteredOnce.Do(func() { close(entered) })
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DB().Callback().Update().Remove(callbackName) })

	finished := time.Now().UTC()
	post := &completionPost{
		task: &task{
			meta:      router.RequestMeta{RequestID: "async-post-request"},
			candidate: router.Candidate{Endpoint: endpoint},
		},
		log: models.RequestLog{
			RequestID:    "async-post-request",
			TaskState:    "completed",
			StatusCode:   http.StatusOK,
			FinishedAt:   &finished,
			UpdatedAt:    finished,
			EndpointID:   &endpoint.ID,
			EndpointUUID: optionalString(endpoint.UUID),
		},
		persist:         true,
		persistDeferred: true,
	}
	returned := make(chan error, 1)
	go func() { returned <- s.enqueueCompletionPost(ctx, post) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue waited for deferred database enrichment")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("completion post worker did not start enrichment")
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for {
		var stored models.RequestLog
		if err := st.DB().Where("request_id = ?", "async-post-request").Take(&stored).Error; err == nil && stored.TaskState == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deferred request-log enrichment did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuthoritativeFinalizationOutlivesRequestCancellation(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	endpoint := models.Endpoint{
		ProviderID: 1, Name: "detached-finalization", UpstreamModel: "detached-finalization",
		Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var finalized RequestFinalizedEvent
	s := New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000).
		WithRequestFinalizedHooks(func(hookCtx context.Context, event RequestFinalizedEvent) error {
			close(entered)
			<-release
			if err := hookCtx.Err(); err != nil {
				return fmt.Errorf("finalization context followed request cancellation: %w", err)
			}
			finalized = event
			return nil
		}).
		WithAuthoritativeFinalization(true)
	s.Start()
	t.Cleanup(s.Stop)

	permit, err := s.Submit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID: "detached-finalization", IncomingModel: endpoint.UpstreamModel,
			Streaming: true,
		},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	requestCtx, cancelRequest := context.WithCancel(ctx)
	completed := make(chan error, 1)
	go func() {
		completed <- s.Complete(requestCtx, permit, http.StatusOK, 1, 4)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("authoritative finalization hook was not called")
	}
	cancelRequest()
	close(release)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion did not finish")
	}
	if finalized.State != "completed" || finalized.RequestID != "detached-finalization" {
		t.Fatalf("unexpected finalization event: %#v", finalized)
	}
}

func TestAtomicGroupHandoffPreAdmitsNextWithoutSecondAdmission(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	endpoint := models.Endpoint{ProviderID: 1, Name: "handoff", UpstreamModel: "handoff", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "handoff-group", Enabled: true, DefaultMaxWaitMS: 30000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}

	var admissions atomic.Int64
	var handoffs atomic.Int64
	var finalized atomic.Int64
	s := New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000).
		WithDispatchAdmissionHooks(func(context.Context, DispatchAdmissionInput) (DispatchAdmissionDecision, error) {
			admissions.Add(1)
			return DispatchAdmissionDecision{Allowed: true}, nil
		}).
		WithFinalizeAndAdmitNextHooks(func(_ context.Context, input FinalizeAndAdmitNextInput) (FinalizeAndAdmitNextDecision, error) {
			handoffs.Add(1)
			if input.Current.RequestID != "handoff-first" || input.Next.RequestID != "handoff-second" {
				t.Fatalf("unexpected handoff pair: current=%q next=%q", input.Current.RequestID, input.Next.RequestID)
			}
			return FinalizeAndAdmitNextDecision{Handled: true, Allowed: true}, nil
		}).
		WithRequestFinalizedHooks(func(context.Context, RequestFinalizedEvent) error {
			finalized.Add(1)
			return nil
		}).
		WithAuthoritativeFinalization(true)
	s.Start()
	t.Cleanup(s.Stop)

	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	submit := func(requestID string) {
		go func() {
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: requestID, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 30000},
				Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}()
	}
	submit("handoff-first")
	first := receivePermit(t, permits, errs, time.Second)
	submit("handoff-second")
	assertNoPermit(t, permits, errs, 75*time.Millisecond)
	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermit(t, permits, errs, time.Second)
	if admissions.Load() != 1 {
		t.Fatalf("next request was admitted twice instead of using atomic handoff: admissions=%d", admissions.Load())
	}
	if handoffs.Load() != 1 || finalized.Load() != 0 {
		t.Fatalf("unexpected finalization path after handoff: handoffs=%d finalized=%d", handoffs.Load(), finalized.Load())
	}
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	if finalized.Load() != 1 {
		t.Fatalf("last group request should use ordinary finalization, got %d calls", finalized.Load())
	}
}

func TestWaitingGroupHeadCannotBeOvertakenAfterAdmissionDeferral(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	lane := models.RoutingLane{Name: "deferred-fifo", Enabled: true, DefaultMaxWaitMS: 30000}
	endpoint := models.Endpoint{ProviderID: 1, Name: "deferred-fifo", UpstreamModel: "deferred-fifo", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	var firstDenied atomic.Bool
	s.WithDispatchAdmissionHooks(func(_ context.Context, input DispatchAdmissionInput) (DispatchAdmissionDecision, error) {
		if input.RequestID == "deferred-fifo-first" && !firstDenied.Swap(true) {
			return DispatchAdmissionDecision{Allowed: false, EligibleAt: time.Now().UTC().Add(100 * time.Millisecond), Reason: "test defer"}, nil
		}
		return DispatchAdmissionDecision{Allowed: true}, nil
	})
	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	submit := func(requestID string) {
		go func() {
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: requestID, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 30000},
				Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}()
	}
	submit("deferred-fifo-first")
	var firstTaskID string
	deadline := time.Now().Add(time.Second)
	for firstTaskID == "" && time.Now().Before(deadline) {
		for _, item := range s.Items() {
			if item.RequestID == "deferred-fifo-first" && item.State == "waiting" {
				firstTaskID = item.TaskID
				break
			}
		}
		if firstTaskID == "" {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if firstTaskID == "" {
		t.Fatalf("first request did not enter deferred state: %#v", s.Items())
	}
	submit("deferred-fifo-second")
	first := receivePermit(t, permits, errs, time.Second)
	if first.TaskID != firstTaskID {
		t.Fatalf("later same-group request overtook deferred FIFO head: first=%q got=%q", firstTaskID, first.TaskID)
	}
	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermit(t, permits, errs, time.Second)
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
}

func TestAuthoritativeFinalizationCarriesCanonicalRequestAttribution(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	provider := models.Provider{Name: "canonical-provider", Slug: "canonical-provider", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "canonical-endpoint",
		UpstreamModel: "canonical-upstream", Enabled: true, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "canonical-lane", Enabled: true, DefaultMaxWaitMS: 30000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}

	var finalized RequestFinalizedEvent
	s := New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000).
		WithRequestFinalizedHooks(func(_ context.Context, event RequestFinalizedEvent) error {
			finalized = event
			return nil
		}).
		WithAuthoritativeFinalization(true)
	s.Start()
	t.Cleanup(s.Stop)

	enqueuedAt := time.Now().UTC().Add(-250 * time.Millisecond)
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID: "canonical-finalization", Lane: lane.Name, IncomingModel: lane.Name,
			RouteKind: models.RouteKindChat, Priority: 7, AllowFallback: true, MaxWaitMS: 30000,
		},
		Candidates: []router.Candidate{{Endpoint: endpoint, Lane: &lane, Rank: 1, FallbackCount: 2}},
		BodyBytes:  64,
		EnqueuedAt: enqueuedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAtWithCost(ctx, permit, http.StatusOK, 5, 9, 17, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if finalized.LaneStorageID != lane.ID || finalized.LaneID != lane.UUID || finalized.EndpointStorageID != endpoint.ID || finalized.EndpointID != endpoint.UUID || finalized.ProviderStorageID != provider.ID || finalized.ProviderID != provider.UUID {
		t.Fatalf("finalized candidate attribution was incomplete: %#v", finalized)
	}
	if finalized.Priority != 7 || finalized.FallbackCount != 2 || finalized.QueuedAt.IsZero() || finalized.StartedAt.IsZero() || finalized.FinishedAt.IsZero() || finalized.WaitMS < 0 || finalized.LatencyMS < 0 {
		t.Fatalf("finalized timing and execution attribution was incomplete: %#v", finalized)
	}
	if finalized.CandidateTraceJSON == "" || finalized.CandidateTraceJSON == "[]" || finalized.LimitImpactJSON == "" || finalized.AppliedOverridesJSON == "" {
		t.Fatalf("finalized JSON attribution was incomplete: trace=%q limits=%q overrides=%q", finalized.CandidateTraceJSON, finalized.LimitImpactJSON, finalized.AppliedOverridesJSON)
	}
}

func TestSerialGroupDispatchWaitedIncludesPriorInFlightDuration(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	lane := models.RoutingLane{ID: 1, Name: "agentic", Enabled: true, DefaultMaxWaitMS: 60000}
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	firstDone := make(chan Permit, 1)
	secondDone := make(chan Permit, 1)
	errs := make(chan error, 2)
	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta:       router.RequestMeta{RequestID: "serial-wait-first", Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
			Candidates: []router.Candidate{candidate},
			BodyBytes:  32,
			EnqueuedAt: time.Now().UTC(),
		})
		if err != nil {
			errs <- err
			return
		}
		firstDone <- permit
	}()
	first := receivePermit(t, firstDone, errs, time.Second)

	secondEnqueuedAt := time.Now().UTC()
	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta:       router.RequestMeta{RequestID: "serial-wait-second", Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
			Candidates: []router.Candidate{candidate},
			BodyBytes:  32,
			EnqueuedAt: secondEnqueuedAt,
		})
		if err != nil {
			errs <- err
			return
		}
		secondDone <- permit
	}()
	assertNoPermit(t, secondDone, errs, 75*time.Millisecond)

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermit(t, secondDone, errs, time.Second)
	if second.Waited < 75*time.Millisecond {
		t.Fatalf("expected second same-group request to wait behind first, waited %s", second.Waited)
	}
}

func TestSerialDispatchReselectsPreferredAfterActivePacingExpires(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	lane := models.RoutingLane{Name: "serial-reselect-preferred", Enabled: true, DefaultMaxWaitMS: 7000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	preferred := models.Endpoint{ProviderID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	fallback := models.Endpoint{ProviderID: 1, Name: "dummy3", UpstreamModel: "dummy3", Enabled: true, ManualRank: 3, HealthStatus: models.HealthHealthy}
	for _, endpoint := range []*models.Endpoint{&preferred, &fallback} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    preferred.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	candidates := []router.Candidate{
		{Endpoint: preferred, Lane: &lane, Rank: 1, FallbackCount: 0},
		{Endpoint: fallback, Lane: &lane, Rank: 3, FallbackCount: 1},
	}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	submit := func(requestID string) {
		go func() {
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: requestID, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: lane.DefaultMaxWaitMS},
				Candidates: candidates,
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
	submit("serial-reselect-preferred-first")
	first := receivePermit(t, permits, errs, time.Second)
	if first.Endpoint.ID != preferred.ID {
		t.Fatalf("expected first request to use preferred endpoint, got %d", first.Endpoint.ID)
	}
	submit("serial-reselect-preferred-second")
	assertNoPermit(t, permits, errs, 100*time.Millisecond)
	queued := taskSnapshotByRequestID(t, s, "serial-reselect-preferred-second")
	if queued.EndpointID != fallback.ID {
		t.Fatalf("expected queued request to initially hold fallback while preferred pacing exceeds wait budget, got %d", queued.EndpointID)
	}
	var storedPreferred models.Endpoint
	if err := st.FindByID(ctx, &storedPreferred, preferred.ID); err != nil {
		t.Fatal(err)
	}
	storedPreferred.HealthStatus = models.HealthHealthy
	storedPreferred.CooldownUntil = nil
	if err := st.SaveEndpointState(ctx, storedPreferred); err != nil {
		t.Fatal(err)
	}
	first.SelectedAt = time.Now().UTC().Add(-15 * time.Second)
	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermitWithScheduler(t, permits, errs, s, time.Second)
	if second.Endpoint.ID != preferred.ID {
		t.Fatalf("expected dispatch-front reselection to return to preferred endpoint, got %d", second.Endpoint.ID)
	}
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
}

func TestSerialDispatchReselectsAvailableFallbackWhenPreferredExceedsWaitBudget(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	lane := models.RoutingLane{Name: "serial-reselect-fallback", Enabled: true, DefaultMaxWaitMS: 7000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	preferred := models.Endpoint{ProviderID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	capped := models.Endpoint{ProviderID: 1, Name: "dummy2", UpstreamModel: "dummy2", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy}
	healthy := models.Endpoint{ProviderID: 1, Name: "dummy3", UpstreamModel: "dummy3", Enabled: true, ManualRank: 3, HealthStatus: models.HealthHealthy}
	for _, endpoint := range []*models.Endpoint{&preferred, &capped, &healthy} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
		if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
	}
	candidates := []router.Candidate{
		{Endpoint: preferred, Lane: &lane, Rank: 1, FallbackCount: 0},
		{Endpoint: capped, Lane: &lane, Rank: 2, FallbackCount: 1},
		{Endpoint: healthy, Lane: &lane, Rank: 3, FallbackCount: 2},
	}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	submit := func(requestID string) {
		go func() {
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: requestID, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: lane.DefaultMaxWaitMS},
				Candidates: candidates,
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
	submit("serial-reselect-fallback-first")
	first := receivePermit(t, permits, errs, time.Second)
	if first.Endpoint.ID != preferred.ID {
		t.Fatalf("expected first request to use preferred endpoint, got %d", first.Endpoint.ID)
	}
	submit("serial-reselect-fallback-second")
	assertNoPermit(t, permits, errs, 100*time.Millisecond)
	queued := taskSnapshotByRequestID(t, s, "serial-reselect-fallback-second")
	if queued.EndpointID != preferred.ID {
		t.Fatalf("expected queued request to initially bind preferred endpoint before limits change, got %d", queued.EndpointID)
	}

	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    preferred.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    capped.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodDay,
		LimitValue: 1,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: capped.ID}}, models.MetricRequests, 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermitWithScheduler(t, permits, errs, s, time.Second)
	if second.Endpoint.ID != healthy.ID {
		t.Fatalf("expected dispatch-front reselection to skip preferred and capped fallback, got %d", second.Endpoint.ID)
	}
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitRejectsUserLimitWhenEveryCandidateExceedsWaitBudget(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	now := time.Now().UTC()
	lane := models.RoutingLane{Name: "user-limit-reject", Enabled: true, DefaultMaxWaitMS: 50, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	first := models.Endpoint{ProviderID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	second := models.Endpoint{ProviderID: 1, Name: "dummy2", UpstreamModel: "dummy2", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy}
	for _, endpoint := range []*models.Endpoint{&first, &second} {
		if err := st.Create(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
	}
	s.WithExternalLimitHooks(func(context.Context, ExternalLimitInput) ([]ExternalLimit, error) {
		return []ExternalLimit{{
			Scope:       LimitScope{Type: deferScopeUser, ID: "org:user"},
			Metric:      string(models.MetricRequests),
			Period:      string(models.PeriodDay),
			LimitValue:  1,
			Used:        1,
			ResetAt:     now.Add(23 * time.Hour),
			DeferScope:  deferScopeUser,
			DeferReason: deferReasonUserLimitExceeded,
		}}, nil
	})

	submitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, err := s.Submit(submitCtx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:     "user-limit-reject",
			Lane:          lane.Name,
			IncomingModel: lane.Name,
			AllowFallback: true,
			MaxWaitMS:     lane.DefaultMaxWaitMS,
		},
		Candidates: []router.Candidate{
			{Endpoint: first, Lane: &lane, Rank: 1, FallbackCount: 0},
			{Endpoint: second, Lane: &lane, Rank: 2, FallbackCount: 1},
		},
		BodyBytes:  32,
		EnqueuedAt: now,
	})
	var rejection Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected user limit to reject with scheduler Rejection, got %T %v", err, err)
	}
	if rejection.RetryAfter <= time.Duration(lane.DefaultMaxWaitMS)*time.Millisecond {
		t.Fatalf("expected retry_after to exceed max wait, got %s", rejection.RetryAfter)
	}
	if items := s.Items(); len(items) != 0 {
		t.Fatalf("expected exhausted request not to enter queue, got %#v", items)
	}
}

func TestSubmitRejectsUserRPDWhenAllRoutingGroupCandidatesExceedMaxWait(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	now := time.Now().UTC()
	lane := models.RoutingLane{Name: "user-rpd-reject", Enabled: true, DefaultMaxWaitMS: 60_000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	endpoints := []models.Endpoint{
		{ProviderID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy},
		{ProviderID: 1, Name: "dummy2", UpstreamModel: "dummy2", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy},
		{ProviderID: 1, Name: "dummy3", UpstreamModel: "dummy3", Enabled: true, ManualRank: 3, HealthStatus: models.HealthHealthy},
	}
	candidates := make([]router.Candidate, 0, len(endpoints))
	for idx := range endpoints {
		if err := st.Create(ctx, &endpoints[idx]); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, router.Candidate{
			Endpoint:      endpoints[idx],
			Lane:          &lane,
			Rank:          idx + 1,
			FallbackCount: idx,
		})
	}
	resetAt := now.Add(79411 * time.Second)
	s.WithExternalLimitHooks(func(context.Context, ExternalLimitInput) ([]ExternalLimit, error) {
		return []ExternalLimit{{
			Scope:       LimitScope{Type: deferScopeUser, ID: "org:user"},
			Metric:      string(models.MetricRequests),
			Period:      string(models.PeriodDay),
			LimitValue:  10,
			Used:        10,
			ResetAt:     resetAt,
			DeferScope:  deferScopeUser,
			DeferReason: deferReasonUserLimitExceeded,
		}}, nil
	})

	_, err := s.Submit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:     "user-rpd-reject",
			Lane:          lane.Name,
			IncomingModel: lane.Name,
			AllowFallback: true,
			MaxWaitMS:     lane.DefaultMaxWaitMS,
		},
		Candidates: candidates,
		BodyBytes:  32,
		EnqueuedAt: now,
	})
	var rejection Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected user RPD exhaustion to reject with scheduler Rejection, got %T %v", err, err)
	}
	if rejection.RetryAfter <= time.Duration(lane.DefaultMaxWaitMS)*time.Millisecond {
		t.Fatalf("expected retry_after to exceed group max wait, got %s", rejection.RetryAfter)
	}
	if got := len(s.Items()); got != 0 {
		t.Fatalf("expected no queued task for over-budget user RPD exhaustion, got %d", got)
	}
}

func TestDispatchFrontRejectsQueuedRequestWhenReselectionExceedsWaitBudget(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	lane := models.RoutingLane{Name: "queued-user-limit-reject", Enabled: true, DefaultMaxWaitMS: 50, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	var exhausted atomic.Bool
	s.WithExternalLimitHooks(func(context.Context, ExternalLimitInput) ([]ExternalLimit, error) {
		if !exhausted.Load() {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:       LimitScope{Type: deferScopeUser, ID: "org:user"},
			Metric:      string(models.MetricRequests),
			Period:      string(models.PeriodDay),
			LimitValue:  1,
			Used:        1,
			ResetAt:     time.Now().UTC().Add(23 * time.Hour),
			DeferScope:  deferScopeUser,
			DeferReason: deferReasonUserLimitExceeded,
		}}, nil
	})
	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	submit := func(requestID string) {
		go func() {
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: requestID, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: lane.DefaultMaxWaitMS},
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

	submit("queued-user-limit-reject-first")
	first := receivePermit(t, permits, errs, time.Second)
	submit("queued-user-limit-reject-second")
	assertNoPermit(t, permits, errs, 100*time.Millisecond)
	exhausted.Store(true)

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	select {
	case permit := <-permits:
		t.Fatalf("expected queued request to reject after dispatch-front reselection, got permit %#v", permit)
	case err := <-errs:
		var rejection Rejection
		if !errors.As(err, &rejection) {
			t.Fatalf("expected scheduler Rejection, got %T %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queued request rejection")
	}
	if items := s.Items(); len(items) != 0 {
		t.Fatalf("expected rejected queued request to leave scheduler, got %#v", items)
	}
}

func TestSerialQueuedTasksDoNotInflateRuntimeRollups(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	lane := models.RoutingLane{Name: "runtime-fifo", Enabled: true, DefaultMaxWaitMS: 60000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	for _, requestID := range []string{"runtime-fifo-first", "runtime-fifo-second"} {
		requestID := requestID
		go func() {
			permit, err := s.Submit(ctx, SubmitRequest{
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
	first := receivePermit(t, permits, errs, time.Second)
	assertNoPermit(t, permits, errs, 100*time.Millisecond)
	assertTaskStates(t, s, map[string]int{"in_flight": 1, "ready": 1})

	scope := limits.ScopeRef{Type: models.ScopeLane, ID: lane.ID}
	now := time.Now().UTC()
	runtimeUsed := s.CurrentRuntimeUsedForContext(ctx, scope, models.MetricRequests, models.PeriodMinute, now)
	if runtimeUsed != 1 {
		t.Fatalf("expected only active request in runtime state, got %d", runtimeUsed)
	}
	reservedUsed := s.tracker.Used(scope, models.MetricRequests, models.PeriodMinute, now)
	if reservedUsed != 1 {
		t.Fatalf("expected queued same-group request not to reserve usage before dispatch, got %d", reservedUsed)
	}

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	second := receivePermit(t, permits, errs, time.Second)
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
}

func TestSerialGroupDispatchAllowsDifferentGroupsInParallel(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	firstLane := models.RoutingLane{ID: 1, Name: "agentic-a", Enabled: true, DefaultMaxWaitMS: 60000}
	secondLane := models.RoutingLane{ID: 2, Name: "agentic-b", Enabled: true, DefaultMaxWaitMS: 60000}
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, lane := range []models.RoutingLane{firstLane, secondLane} {
		lane := lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: "serial-different-" + lane.Name, Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
				Candidates: []router.Candidate{{Endpoint: endpoint, Lane: &lane, Rank: 1}},
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

	first := receivePermit(t, permits, errs, time.Second)
	second := receivePermit(t, permits, errs, time.Second)
	assertTaskStates(t, s, map[string]int{"in_flight": 2})

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	waitForSubmitters(t, &wg)
}

func TestDirectEndpointCallsRemainParallelWithoutSharedPolicies(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ProviderID: 1, Name: "direct-parallel", UpstreamModel: "direct-parallel", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta: router.RequestMeta{
					RequestID:     fmt.Sprintf("direct-parallel-%d", index),
					IncomingModel: endpoint.UpstreamModel,
					MaxWaitMS:     30000,
				},
				Candidates: []router.Candidate{candidate}, BodyBytes: 32, EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}(index)
	}

	first := receivePermit(t, permits, errs, 5*time.Second)
	second := receivePermit(t, permits, errs, 5*time.Second)
	assertTaskStates(t, s, map[string]int{"in_flight": 2})
	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	waitForSubmitters(t, &wg)
}

func TestRemovingDeferredGroupTailRecomputesFutureEligibility(t *testing.T) {
	s, _, _ := newScheduler(t)
	now := time.Now().UTC()
	lane := models.RoutingLane{ID: 17, Name: "tail-recompute", Enabled: true}
	candidate := router.Candidate{Endpoint: models.Endpoint{ID: 1}, Lane: &lane}
	first := &task{id: "tail-first", candidate: candidate, state: "waiting", evaluation: limits.CandidateEvaluation{EligibleAt: now.Add(time.Second)}}
	deferredTail := &task{id: "tail-deferred", candidate: candidate, state: "waiting", evaluation: limits.CandidateEvaluation{EligibleAt: now.Add(time.Minute)}}
	key := candidateGroupKey(router.RequestMeta{}, candidate)
	first.meta = router.RequestMeta{RequestID: first.id}
	deferredTail.meta = router.RequestMeta{RequestID: deferredTail.id}
	queue := newGroupTaskQueue()
	queue.append(first.id)
	queue.append(deferredTail.id)
	s.tasks[first.id] = first
	s.tasks[deferredTail.id] = deferredTail
	s.groupTasks[key] = queue
	s.groupQueued[key] = 2
	s.groupFIFOTails[key] = deferredTail.evaluation.EligibleAt

	s.mu.Lock()
	s.removeTaskLocked(deferredTail)
	tail := s.groupFIFOTails[key]
	s.mu.Unlock()
	if !tail.Equal(first.evaluation.EligibleAt) {
		t.Fatalf("group tail=%s, want remaining request eligibility %s", tail, first.evaluation.EligibleAt)
	}
}

func TestSerialGroupDispatchCanBeDisabledForSameGroupParallel(t *testing.T) {
	s, _, st := newScheduler(t)
	s.SetSerialGroupDispatch(false)
	ctx := context.Background()

	lane := models.RoutingLane{ID: 1, Name: "agentic", Enabled: true, DefaultMaxWaitMS: 60000}
	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	candidate := router.Candidate{Endpoint: endpoint, Lane: &lane, Rank: 1}
	permits := make(chan Permit, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:       router.RequestMeta{RequestID: "serial-default-" + time.Duration(i).String(), Lane: lane.Name, IncomingModel: lane.Name, AllowFallback: true, MaxWaitMS: 60000},
				Candidates: []router.Candidate{candidate},
				BodyBytes:  32,
				EnqueuedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			permits <- permit
		}(i)
	}

	first := receivePermit(t, permits, errs, time.Second)
	second := receivePermit(t, permits, errs, time.Second)
	assertTaskStates(t, s, map[string]int{"in_flight": 2})

	if err := s.CompleteSynthetic(ctx, first, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSynthetic(ctx, second, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	waitForSubmitters(t, &wg)
}

func receivePermit(t *testing.T, permits <-chan Permit, errs <-chan error, timeout time.Duration) Permit {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case permit := <-permits:
		return permit
	case err := <-errs:
		t.Fatal(err)
	case <-timer.C:
		t.Fatalf("timed out waiting for permit after %s", timeout)
	}
	return Permit{}
}

func receivePermitWithScheduler(t *testing.T, permits <-chan Permit, errs <-chan error, s *Scheduler, timeout time.Duration) Permit {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case permit := <-permits:
		return permit
	case err := <-errs:
		t.Fatal(err)
	case <-timer.C:
		counts := make(map[string]int)
		for _, item := range s.Items() {
			counts[item.State]++
		}
		s.mu.Lock()
		readyLen := len(s.ready)
		delayedLen := len(s.delayed)
		serial := s.serialGroupDispatch
		s.mu.Unlock()
		t.Fatalf("timed out waiting for permit after %s with task counts %#v ready_heap=%d delayed_heap=%d serial=%v", timeout, counts, readyLen, delayedLen, serial)
	}
	return Permit{}
}

func assertNoPermit(t *testing.T, permits <-chan Permit, errs <-chan error, wait time.Duration) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case permit := <-permits:
		t.Fatalf("unexpected permit before serial group released: %#v", permit)
	case err := <-errs:
		t.Fatal(err)
	case <-timer.C:
	}
}

func assertTaskStates(t *testing.T, s *Scheduler, expected map[string]int) {
	t.Helper()
	counts := make(map[string]int)
	for _, item := range s.Items() {
		counts[item.State]++
	}
	for state, want := range expected {
		if counts[state] != want {
			t.Fatalf("expected %d %s tasks, got counts %#v", want, state, counts)
		}
	}
	for state, got := range counts {
		if _, ok := expected[state]; !ok && got != 0 {
			t.Fatalf("unexpected %s tasks in counts %#v", state, counts)
		}
	}
}

func taskSnapshotByRequestID(t *testing.T, s *Scheduler, requestID string) TaskSnapshot {
	t.Helper()
	for _, item := range s.Items() {
		if item.RequestID == requestID {
			return item
		}
	}
	t.Fatalf("task %q not found in scheduler items", requestID)
	return TaskSnapshot{}
}

func waitForSubmitters(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submitters")
	}
}

func TestConcurrentExternalUserRPMTwoAdmitsAtMostTwoImmediateRequests(t *testing.T) {
	s, tracker, st := newScheduler(t)
	s.SetSerialGroupDispatch(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

	userScope := limits.ScopeRef{Type: models.ScopeType(deferScopeUser), Key: "org-1:user-a"}
	resetAt := time.Now().UTC().Add(time.Minute)
	s.WithExternalLimitHooks(func(_ context.Context, input ExternalLimitInput) ([]ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []ExternalLimit{{
			Scope:      LimitScope{Type: deferScopeUser, ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       0,
			ResetAt:    resetAt,
		}}, nil
	})

	candidate := router.Candidate{Endpoint: endpoint, Rank: 1}
	permits := make(chan Permit, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			permit, err := s.Submit(ctx, SubmitRequest{
				Meta:            router.RequestMeta{RequestID: "user-concurrent-" + time.Duration(i).String(), IncomingModel: "dummy", AllowFallback: true, MaxWaitMS: 120000},
				Candidates:      []router.Candidate{candidate},
				BodyBytes:       64,
				EnqueuedAt:      time.Now().UTC(),
				TrustedMetadata: map[string]string{"user_uuid": "user-a"},
			})
			if err == nil {
				permits <- permit
			}
		}(i)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(permits) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(permits); got != 2 {
		t.Fatalf("expected exactly 2 immediate permits for 2 RPM user cap, got %d", got)
	}
	if got := tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 2 {
		t.Fatalf("expected user runtime reservations to stop at 2, got %d", got)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(permits); got != 2 {
		t.Fatalf("third user request should remain deferred, got %d immediate permits", got)
	}
	var foundUserDefer bool
	for _, item := range s.Items() {
		if item.DeferScope == deferScopeUser && item.DeferReason == deferReasonUserLimitExceeded {
			foundUserDefer = true
			break
		}
	}
	if !foundUserDefer {
		t.Fatal("expected third user request to be user-deferred")
	}
	status, until, err := s.DeriveEndpointHealth(context.Background(), endpoint, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthHealthy || until != nil {
		t.Fatalf("expected user defer not to create endpoint cooldown, got status=%s until=%v", status, until)
	}

	cancel()
	wg.Wait()
	close(permits)
	for permit := range permits {
		if err := s.Complete(context.Background(), permit, http.StatusOK, 1, 4); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancelInFlightPermitClearsTaskAndConcurrency(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{
		ID: 1, UUID: "00000000-0000-0000-0000-000000000001",
		ProviderID: 1, ProviderUUID: "00000000-0000-0000-0000-000000000002",
		Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	requestID := "cancel-in-flight"
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta: router.RequestMeta{RequestID: requestID, Lane: "agentic", AllowFallback: true},
		Candidates: []router.Candidate{{
			Endpoint: endpoint,
			Lane:     &models.RoutingLane{ID: 1, UUID: "00000000-0000-0000-0000-000000000003", Name: "agentic"},
			Rank:     1,
		}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := tracker.Concurrency(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}); got != 1 {
		t.Fatalf("expected endpoint concurrency 1 after dispatch, got %d", got)
	}

	if err := s.Cancel(permit.TaskID); err != nil {
		t.Fatal(err)
	}

	if got := tracker.Concurrency(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}); got != 0 {
		t.Fatalf("expected endpoint concurrency 0 after cancel, got %d", got)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 0 {
		t.Fatalf("expected cancelled in-flight request without actual usage not to count as durable usage, got %d", got)
	}
	if items := s.Items(); len(items) != 0 {
		t.Fatalf("expected no tracked tasks after cancel, got %d", len(items))
	}

	log, err := st.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if log.TaskState != "cancelled" {
		t.Fatalf("expected cancelled request log, got %q", log.TaskState)
	}
	if log.StatusCode != 499 {
		t.Fatalf("expected 499 status code for cancelled request, got %d", log.StatusCode)
	}
	if log.StartedAt == nil {
		t.Fatal("expected started_at to be recorded for cancelled in-flight request")
	}
	var cancelledEvent *telemetry.Event
	for _, event := range s.telemetry.Events() {
		if event.Type == "request_cancelled" && event.Payload["request_id"] == requestID {
			copy := event
			cancelledEvent = &copy
		}
	}
	if cancelledEvent == nil {
		t.Fatalf("cancelled realtime event is incomplete: %#v", cancelledEvent)
	}
	laneID, _ := cancelledEvent.Payload["lane_id"].(*string)
	providerID, _ := cancelledEvent.Payload["provider_id"].(*string)
	if cancelledEvent.Payload["state"] != "cancelled" ||
		cancelledEvent.Payload["status_code"] != 499 ||
		laneID == nil || *laneID != "00000000-0000-0000-0000-000000000003" ||
		providerID == nil || *providerID != "00000000-0000-0000-0000-000000000002" ||
		cancelledEvent.Payload["finished_at"] == nil {
		t.Fatalf("cancelled realtime event is incomplete: %#v", cancelledEvent)
	}
}

func TestInFlightRequestRemainsOperationalOnlyUntilTerminal(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "in-flight-operational", AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	items := s.Items()
	if len(items) != 1 || items[0].State != "in_flight" {
		t.Fatalf("expected in-flight task in operational scheduler view, got %#v", items)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 1 {
		t.Fatalf("expected in-flight request to participate in scheduler guardrails, got %d", got)
	}
	if got := tracker.CurrentRuntimeUsed(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 1 {
		t.Fatalf("expected in-flight request to remain visible as provisional runtime usage, got %d", got)
	}

	if err := s.Complete(ctx, permit, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 1 {
		t.Fatalf("expected completed 2xx request to count as durable usage, got %d", got)
	}
}

func TestCompletedProviderErrorsCommitRequestUsageWithoutFabricatingTokens(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "completed-400-no-token-usage", AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, permit, http.StatusBadRequest, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 1 {
		t.Fatalf("expected completed 400 provider response to count one request, got %d", got)
	}
	log, err := st.GetRequestLog(ctx, "completed-400-no-token-usage")
	if err != nil {
		t.Fatal(err)
	}
	if log.ActualTotalTokens != 0 || log.ActualInputTokens != 0 || log.ActualOutputTokens != 0 || log.ActualCostMicros != 0 {
		t.Fatalf("expected non-2xx completion without actual usage not to store estimates as actuals, got in=%d out=%d total=%d cost=%d", log.ActualInputTokens, log.ActualOutputTokens, log.ActualTotalTokens, log.ActualCostMicros)
	}

	permit, err = s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "completed-500-with-actual", AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, permit, http.StatusInternalServerError, 2, 3); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 2 {
		t.Fatalf("expected both completed provider responses to count request usage, got %d", got)
	}
	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricTokens, models.PeriodMinute, time.Now().UTC()); got != 5 {
		t.Fatalf("expected terminal request with actual usage to count actual tokens only, got %d", got)
	}
}

func TestCompletedRequestsPersistLimitCheckpointsForRestart(t *testing.T) {
	st := &storeWrap{testutil.NewStore(t)}
	for _, table := range []string{
		"providers", "credentials", "endpoints", "routing_lanes", "lane_memberships",
		"limit_policies", "limit_policy_states", "limit_policy_state_segments",
		"observed_limits", "observed_limit_state_segments", "pricing_policies",
		"request_logs", "request_log_diagnostics",
	} {
		if err := st.DB().Exec("ALTER TABLE " + table + " ADD COLUMN organization_uuid text").Error; err != nil {
			t.Fatalf("add organization_uuid to %s: %v", table, err)
		}
	}
	if err := st.DB().Exec("ALTER TABLE request_logs ADD COLUMN user_uuid text").Error; err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "22222222-2222-4222-8222-222222222222",
	})
	provider := models.Provider{Name: "Checkpoint Provider", Slug: "checkpoint-provider", BaseURL: "http://checkpoint.invalid", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	pacing := false
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "checkpoint-restart", UpstreamModel: "checkpoint-restart",
		Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy, Pacing: &pacing,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	for _, period := range []models.Period{models.PeriodMinute, models.PeriodDay} {
		policy := models.LimitPolicy{
			ScopeType: models.ScopeEndpoint, ScopeID: endpoint.ID,
			Metric: models.MetricRequests, Period: period,
			LimitValue: 100, Enabled: true, Source: "configured",
		}
		if err := st.Create(ctx, &policy); err != nil {
			t.Fatal(err)
		}
	}
	tracker := limits.NewTracker(st.Store)
	if err := tracker.HydrateRecentUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resolver := limits.NewResolver(st.Store, tracker)
	s := New(st.Store, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	s.Start()
	t.Cleanup(s.Stop)

	complete := func(requestID string, status int, mutateTaskAttribution bool) {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta:       router.RequestMeta{RequestID: requestID, IncomingModel: endpoint.Name, AllowFallback: true},
			Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
			BodyBytes:  32, EnqueuedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if mutateTaskAttribution {
			// The issued permit is the immutable dispatch decision. Simulate a
			// later task-state transition replacing cached attribution so restart
			// persistence must still use the policy carried by the permit.
			s.mu.Lock()
			item := s.tasks[permit.TaskID]
			item.limitScopes = []limits.ScopeRef{{Type: models.ScopeGlobal}}
			item.evaluation.EffectiveLimits = nil
			s.mu.Unlock()
		}
		// Completion can race the HTTP client disconnecting after the provider
		// response is written. Durable limit checkpoints must use the scheduler's
		// tenant-scoped runtime context, not this cancelled request context.
		completionCtx, cancelCompletion := context.WithCancel(ctx)
		cancelCompletion()
		if err := s.Complete(completionCtx, permit, status, 0, 0); err != nil {
			t.Fatal(err)
		}
	}

	complete("checkpoint-restart-400", http.StatusBadRequest, false)
	after400 := limits.NewTracker(st.Store)
	if err := after400.HydrateRecentUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := after400.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 1 {
		t.Fatalf("restart usage after completed 400 = %d, want 1", got)
	}

	complete("checkpoint-restart-200", http.StatusOK, true)
	after200 := limits.NewTracker(st.Store)
	if err := after200.HydrateRecentUsage(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := after200.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 2 {
		t.Fatalf("restart usage after completed 400 and 200 = %d, want 2", got)
	}
}

func TestConcurrentCompletionAndCancellationHaveOneTerminalOwner(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ProviderID: 1, Name: "terminal-race", UpstreamModel: "terminal-race", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	requestID := "completion-cancellation-race"
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: requestID, IncomingModel: endpoint.UpstreamModel},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs <- s.Complete(ctx, permit, http.StatusOK, 1, 4)
	}()
	go func() {
		defer wg.Done()
		<-start
		errs <- s.Cancel(permit.TaskID)
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if got := tracker.Concurrency(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}); got != 0 {
		t.Fatalf("expected exactly one terminal path to release endpoint concurrency, got %d", got)
	}
	if items := s.Items(); len(items) != 0 {
		t.Fatalf("expected terminal task to be removed, got %d", len(items))
	}
	log, err := st.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if log.TaskState != "completed" && log.TaskState != "cancelled" {
		t.Fatalf("expected one valid terminal state, got %q", log.TaskState)
	}
}

func TestDeleteInFlightPermitCancelsActiveRequestAndPreventsLateCompletion(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	requestID := "delete-in-flight"
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: requestID, AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	activeCtx, activeCancel := context.WithCancel(context.Background())
	if !s.AttachCancel(permit.TaskID, activeCancel) {
		t.Fatal("expected active cancel to attach to in-flight task")
	}

	if !s.Delete(permit.TaskID) {
		t.Fatal("expected delete to find in-flight task")
	}
	select {
	case <-activeCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected delete to cancel active request context")
	}

	if err := s.Complete(ctx, permit, http.StatusOK, 1, 4); err != nil {
		t.Fatal(err)
	}

	log, err := st.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if log.TaskState != "cancelled" {
		t.Fatalf("expected late completion not to overwrite cancellation, got %q", log.TaskState)
	}
	if log.StatusCode != 499 {
		t.Fatalf("expected cancelled request to remain 499, got %d", log.StatusCode)
	}
	if log.ErrorText != "queue item cancelled" {
		t.Fatalf("expected queue cancellation reason to remain, got %q", log.ErrorText)
	}
}

func TestFailPersistsRequestLogWithCancelledContext(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	requestID := "fail-with-cancelled-context"
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: requestID, AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Fail(cancelledCtx, permit, http.StatusBadGateway, "upstream request failed"); err != nil {
		t.Fatal(err)
	}

	log, err := st.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if log.TaskState != "failed" {
		t.Fatalf("expected failed request log, got %q", log.TaskState)
	}
	if log.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 status code, got %d", log.StatusCode)
	}
	if log.FinishedAt == nil {
		t.Fatal("expected finished_at to be recorded")
	}
}

func TestDerivedEndpointHealthCoolingDownWhileFirstRequestInFlight(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	lane := models.RoutingLane{Name: "cooldown-test", Enabled: true, DefaultMaxWaitMS: 90000}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
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
		Period:     models.PeriodMinute,
		LimitValue: 1,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "first-in-flight", Lane: lane.Name, AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Lane: &lane, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, []models.RoutingLane{lane}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthCoolingDown {
		t.Fatalf("expected cooling_down while first request is in flight, got %s", status)
	}
	if until == nil || !until.After(time.Now().UTC()) {
		t.Fatal("expected a future cooldown_until while first request is in flight")
	}
}

func TestCompletionPublishesPacingCooldownWithoutAnotherQueuedRequest(t *testing.T) {
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	hub := telemetry.NewHub()
	s := New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 30_000, 1000, 300_000)
	s.Start()
	t.Cleanup(s.Stop)
	ctx := context.Background()

	provider := models.Provider{
		UUID:         "11111111-1111-4111-8111-111111111111",
		Name:         "Provider",
		Enabled:      true,
		HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	pacing := true
	endpoint := models.Endpoint{
		UUID:          "22222222-2222-4222-8222-222222222222",
		ProviderID:    provider.ID,
		ProviderUUID:  provider.UUID,
		Name:          "Model",
		UpstreamModel: "model",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
		Pacing:        &pacing,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodSecond,
		LimitValue: 2,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	permit, err := s.Submit(ctx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:     "completion-pacing-cooldown",
			IncomingModel: "model",
			AllowFallback: true,
		},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSynthetic(ctx, permit, http.StatusOK, 1, 1); err != nil {
		t.Fatal(err)
	}

	var cooldownUntil time.Time
	for _, event := range hub.Events() {
		if event.Type != "endpoint_health_change" ||
			event.Payload["endpoint_id"] != endpoint.UUID ||
			event.Payload["health_status"] != models.HealthCoolingDown {
			continue
		}
		value, ok := event.Payload["cooldown_until"].(*time.Time)
		if ok && value != nil {
			cooldownUntil = value.UTC()
		}
	}
	if cooldownUntil.IsZero() || !cooldownUntil.After(time.Now().UTC()) {
		t.Fatalf("completion did not publish a future pacing cooldown; events=%#v", hub.Events())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range hub.Events() {
			if event.Type == "endpoint_health_change" &&
				event.Payload["endpoint_id"] == endpoint.UUID &&
				event.Payload["health_status"] == models.HealthHealthy {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pacing cooldown did not publish its ready transition; events=%#v", hub.Events())
}

func TestCompletionPublishesTenantScopedRateLimitedPacingState(t *testing.T) {
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	hub := telemetry.NewHub()
	const organizationUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	runtimeCtx := context.Background()
	s := New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 5_000, 1000, 300_000)
	s.Start()
	t.Cleanup(s.Stop)

	provider := models.Provider{
		UUID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		Name:         "Provider",
		Enabled:      true,
		HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(runtimeCtx, &provider); err != nil {
		t.Fatal(err)
	}
	pacing := true
	endpoint := models.Endpoint{
		UUID:          "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		ProviderID:    provider.ID,
		ProviderUUID:  provider.UUID,
		Name:          "Model",
		UpstreamModel: "model",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
		Pacing:        &pacing,
	}
	if err := st.Create(runtimeCtx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(runtimeCtx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(runtimeCtx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	permit, err := s.Submit(runtimeCtx, SubmitRequest{
		Meta: router.RequestMeta{
			RequestID:     "tenant-rate-limited-pacing",
			IncomingModel: "model",
			AllowFallback: true,
		},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Public core's SQLite schema is intentionally single-tenant. Attach the
	// downstream trusted metadata after admission so this unit test exercises
	// event routing without pretending the OSS tables own tenant columns.
	s.mu.Lock()
	s.tasks[permit.TaskID].trustedMetadata = map[string]string{
		"organization_uuid": organizationUUID,
		"user_uuid":         "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
	}
	s.mu.Unlock()
	if err := s.CompleteSynthetic(runtimeCtx, permit, http.StatusOK, 1, 1); err != nil {
		t.Fatal(err)
	}

	for _, event := range hub.Events() {
		if event.Type != "endpoint_health_change" || event.Payload["endpoint_id"] != endpoint.UUID {
			continue
		}
		if event.Payload["health_status"] != models.HealthRateLimited {
			continue
		}
		if event.Payload["organization_uuid"] != organizationUUID {
			t.Fatalf("rate-limited event organization_uuid = %#v, want %q", event.Payload["organization_uuid"], organizationUUID)
		}
		until, ok := event.Payload["cooldown_until"].(*time.Time)
		if !ok || until == nil || !until.After(time.Now().UTC().Add(10*time.Second)) {
			t.Fatalf("rate-limited event cooldown_until = %#v, want the paced RPM boundary", event.Payload["cooldown_until"])
		}
		return
	}
	t.Fatalf("completion did not publish a tenant-scoped rate-limited pacing event; events=%#v", hub.Events())
}

func TestCancelWhileWaitingAfterRequeueDoesNotCountUsage(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{
		ID: 1, UUID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		ProviderID: 1, ProviderUUID: "ffffffff-ffff-4fff-8fff-ffffffffffff",
		Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	requestID := "cancel-waiting-after-requeue"
	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: requestID, AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const organizationUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	s.mu.Lock()
	s.tasks[permit.TaskID].trustedMetadata = map[string]string{"organization_uuid": organizationUUID}
	s.mu.Unlock()

	requeueDone := make(chan error, 1)
	go func() {
		_, err := s.Requeue(context.Background(), permit, time.Minute, "test requeue")
		requeueDone <- err
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		items := s.Items()
		if len(items) == 1 && items[0].State == "waiting" {
			if items[0].StartedAt != nil {
				t.Fatal("expected waiting task after requeue to clear started_at")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for task to requeue into waiting state")
		}
		time.Sleep(10 * time.Millisecond)
	}

	healthEventDeadline := time.Now().Add(time.Second)
	for {
		foundHealthEvent := false
		for _, event := range s.telemetry.Events() {
			if event.Type == "endpoint_health_change" &&
				event.Payload["endpoint_id"] == endpoint.UUID &&
				event.Payload["health_status"] == models.HealthRateLimited &&
				event.Payload["organization_uuid"] == organizationUUID {
				foundHealthEvent = true
				break
			}
		}
		if foundHealthEvent {
			break
		}
		if time.Now().After(healthEventDeadline) {
			t.Fatalf("timed out waiting for direct requeue to publish its tenant-scoped cooldown; events=%#v", s.telemetry.Events())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Restore the single-tenant test context before request-log finalization.
	s.mu.Lock()
	s.tasks[permit.TaskID].trustedMetadata = nil
	s.mu.Unlock()
	if err := s.Cancel(permit.TaskID); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-requeueDone:
		if err == nil {
			t.Fatal("expected requeue to end with cancellation after waiting task was cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for requeue goroutine to finish")
	}

	if got := tracker.Used(limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 0 {
		t.Fatalf("expected waiting cancel after requeue to leave RPM usage at 0, got %d", got)
	}

	log, err := st.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if log.StartedAt != nil {
		t.Fatal("expected waiting cancel log to omit started_at after requeue")
	}
}

func TestRequeuePreservesPersistedUpstreamCooldownSource(t *testing.T) {
	s, _, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	permit, err := s.Submit(ctx, SubmitRequest{
		Meta:       router.RequestMeta{RequestID: "requeue-preserves-upstream-source", AllowFallback: true},
		Candidates: []router.Candidate{{Endpoint: endpoint, Rank: 1}},
		BodyBytes:  32,
		EnqueuedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	cooldownUntil := time.Now().UTC().Add(30 * time.Second)
	if err := st.DB().WithContext(ctx).
		Model(&models.Endpoint{}).
		Where("id = ?", endpoint.ID).
		Updates(map[string]any{
			"health_status":        models.HealthCoolingDown,
			"cooldown_until":       cooldownUntil,
			"cooldown_reason":      "upstream_rate_limited",
			"cooldown_status_code": http.StatusTooManyRequests,
		}).Error; err != nil {
		t.Fatal(err)
	}

	requeueDone := make(chan error, 1)
	go func() {
		_, err := s.Requeue(context.Background(), permit, time.Minute, "upstream rate limited")
		requeueDone <- err
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		items := s.Items()
		if len(items) == 1 && items[0].State == "waiting" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for task to requeue")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var updated models.Endpoint
	if err := st.FindByID(ctx, &updated, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated.CooldownReason != "upstream_rate_limited" || updated.CooldownStatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected requeue to preserve upstream 429 cooldown source, got reason=%q status=%d", updated.CooldownReason, updated.CooldownStatusCode)
	}

	if err := s.Cancel(permit.TaskID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-requeueDone:
		if err == nil {
			t.Fatal("expected requeue to stop after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for requeue to stop")
	}
}

func TestDerivedEndpointHealthCoolingDownWithinGroupWait(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{ID: 1, Name: "reasoning", Enabled: true, DefaultMaxWaitMS: 60000}
	base := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)

	for _, scope := range []limits.ScopeRef{
		{Type: models.ScopeGlobal, ID: 0},
		{Type: models.ScopeProvider, ID: endpoint.ProviderID},
		{Type: models.ScopeEndpoint, ID: endpoint.ID},
		{Type: models.ScopeLane, ID: lane.ID},
	} {
		if err := tracker.RecordWindow(ctx, []limits.ScopeRef{scope}, models.MetricRequests, 5, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, []models.RoutingLane{lane}, base.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthCoolingDown {
		t.Fatalf("expected cooling_down, got %s", status)
	}
	if until == nil || !until.After(base.Add(10*time.Second)) {
		t.Fatal("expected future eligible time for cooling_down endpoint")
	}
}

func TestDerivedEndpointHealthRateLimitedBeyondWaitBudget(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	endpoint := models.Endpoint{ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{ID: 1, Name: "reasoning", Enabled: true, DefaultMaxWaitMS: 60000}
	base := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)

	for _, scope := range []limits.ScopeRef{
		{Type: models.ScopeGlobal, ID: 0},
		{Type: models.ScopeProvider, ID: endpoint.ProviderID},
		{Type: models.ScopeEndpoint, ID: endpoint.ID},
		{Type: models.ScopeLane, ID: lane.ID},
	} {
		if err := tracker.RecordWindow(ctx, []limits.ScopeRef{scope}, models.MetricRequests, 10, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Create(ctx, &models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodDay,
		LimitValue: 10,
		Enabled:    true,
		Source:     "configured",
	}); err != nil {
		t.Fatal(err)
	}

	status, until, err := s.DeriveEndpointHealth(ctx, endpoint, []models.RoutingLane{lane}, base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthRateLimited {
		t.Fatalf("expected rate_limited, got %s", status)
	}
	if until == nil || !until.After(base.Add(time.Minute)) {
		t.Fatal("expected long wait for rate_limited endpoint")
	}
}

func TestDerivedEndpointHealthRecomputesPersistedRateLimitedState(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	until := time.Now().UTC().Add(time.Hour)
	endpoint := models.Endpoint{
		ID:            1,
		ProviderID:    1,
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthRateLimited,
		CooldownUntil: &until,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
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
	now := time.Now().UTC()
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricSpend, 280_005, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	status, cooldownUntil, err := s.DeriveEndpointHealth(ctx, endpoint, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if status != models.HealthHealthy {
		t.Fatalf("expected stale persisted rate_limited state to recompute healthy, got %s until %v", status, cooldownUntil)
	}
	if cooldownUntil != nil {
		t.Fatalf("expected stale cooldown to clear, got %v", cooldownUntil)
	}
}

func TestLimitIncreaseReleasesStaleRateLimitedTask(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	staleCooldown := time.Now().UTC().Add(50 * time.Second)
	pacingDisabled := false
	endpoint := models.Endpoint{
		ID: 1, ProviderID: 1, Enabled: true, ManualRank: 1,
		HealthStatus: models.HealthRateLimited, CooldownUntil: &staleCooldown, Pacing: &pacingDisabled,
	}
	lane := models.RoutingLane{Name: "limit-refresh", Enabled: true, DefaultMaxWaitMS: 70000, AllowFallback: true}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	policy := models.LimitPolicy{
		ScopeType:  models.ScopeEndpoint,
		ScopeID:    endpoint.ID,
		Metric:     models.MetricRequests,
		Period:     models.PeriodMinute,
		LimitValue: 5,
		Enabled:    true,
		Source:     "configured",
	}
	if err := st.Create(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	// Recreate an endpoint at 5/5 RPM with roughly fifty seconds left in its
	// old window. Raising the limit to 15 makes it immediately eligible because
	// the new four-second pacing interval has already elapsed.
	usedAt := time.Now().UTC().Add(-10 * time.Second)
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: endpoint.ID}}, models.MetricRequests, 5, usedAt); err != nil {
		t.Fatal(err)
	}

	permitCh := make(chan Permit, 1)
	errCh := make(chan error, 1)
	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta: router.RequestMeta{
				RequestID:             "limit-refresh-third",
				Lane:                  lane.Name,
				IncomingModel:         lane.Name,
				AllowFallback:         true,
				MaxWaitMS:             lane.DefaultMaxWaitMS,
				EstimatedInputTokens:  10,
				EstimatedOutputTokens: 10,
			},
			Candidates: []router.Candidate{{Endpoint: endpoint, Lane: &lane, Rank: 1}},
			BodyBytes:  32,
			EnqueuedAt: time.Now().UTC(),
		})
		if err != nil {
			errCh <- err
			return
		}
		permitCh <- permit
	}()

	select {
	case permit := <-permitCh:
		t.Fatalf("request dispatched before limit increase: %#v", permit)
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(100 * time.Millisecond):
	}

	policy.LimitValue = 15
	if err := st.Save(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	s.ReloadLimitSettings(ctx)
	if err := s.ReevaluateLimitScopes(ctx, limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID}); err != nil {
		t.Fatal(err)
	}

	select {
	case permit := <-permitCh:
		if permit.Endpoint.ID != endpoint.ID {
			t.Fatalf("expected endpoint %d, got %d", endpoint.ID, permit.Endpoint.ID)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected waiting request to dispatch after limit increase")
	}

	var updated models.Endpoint
	if err := st.FindByID(ctx, &updated, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated.HealthStatus != models.HealthHealthy || updated.CooldownUntil != nil {
		t.Fatalf("expected endpoint to leave rate_limited and clear its stale cooldown after limit increase, got status=%s until=%v", updated.HealthStatus, updated.CooldownUntil)
	}
}

func TestLimitIncreaseReevaluatesQueuedRequestWhenAlternateCandidateChanges(t *testing.T) {
	s, tracker, st := newScheduler(t)
	ctx := context.Background()

	provider := models.Provider{Name: "limit-refresh-provider", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	pacingDisabled := false
	preferred := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "preferred",
		UpstreamModel: "preferred", Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy, Pacing: &pacingDisabled,
	}
	waiting := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "waiting",
		UpstreamModel: "waiting", Enabled: true, ManualRank: 2, HealthStatus: models.HealthHealthy, Pacing: &pacingDisabled,
	}
	if err := st.Create(ctx, &preferred); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &waiting); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "alternate-limit-refresh", Enabled: true, DefaultMaxWaitMS: 70000, AllowFallback: true}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	for rank, endpointID := range []uint{preferred.ID, waiting.ID} {
		if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, EndpointID: endpointID, ManualRank: rank + 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	preferredPolicy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: preferred.ID, Metric: models.MetricRequests,
		Period: models.PeriodHour, LimitValue: 1, Enabled: true, Source: "configured",
	}
	waitingPolicy := models.LimitPolicy{
		ScopeType: models.ScopeEndpoint, ScopeID: waiting.ID, Metric: models.MetricRequests,
		Period: models.PeriodMinute, LimitValue: 1, Enabled: true, Source: "configured",
	}
	if err := st.Create(ctx, &preferredPolicy); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &waitingPolicy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: preferred.ID}}, models.MetricRequests, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := tracker.RecordWindow(ctx, []limits.ScopeRef{{Type: models.ScopeEndpoint, ID: waiting.ID}}, models.MetricRequests, 1, now); err != nil {
		t.Fatal(err)
	}

	type result struct {
		permit Permit
		err    error
	}
	done := make(chan result, 1)
	go func() {
		permit, err := s.Submit(ctx, SubmitRequest{
			Meta: router.RequestMeta{
				RequestID: "alternate-limit-refresh-request", Lane: lane.Name, IncomingModel: lane.Name,
				AllowFallback: true, MaxWaitMS: lane.DefaultMaxWaitMS,
			},
			Candidates: []router.Candidate{
				{Endpoint: preferred, Lane: &lane, Rank: 1},
				{Endpoint: waiting, Lane: &lane, Rank: 2, FallbackCount: 1},
			},
			BodyBytes:  32,
			EnqueuedAt: now,
		})
		done <- result{permit: permit, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		items := s.Items()
		if len(items) == 1 && items[0].RequestID == "alternate-limit-refresh-request" && items[0].State == "waiting" {
			if items[0].EndpointID != waiting.ID {
				t.Fatalf("expected request to wait on endpoint %d before the edit, got %#v", waiting.ID, items[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for request to queue on the alternate endpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}

	preferredPolicy.LimitValue = 2
	if err := st.Save(ctx, &preferredPolicy); err != nil {
		t.Fatal(err)
	}
	if err := s.ReevaluateLimitScopes(ctx, limits.ScopeRef{Type: models.ScopeEndpoint, ID: preferred.ID}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("expected queued request to dispatch after alternate limit increase: %v", got.err)
		}
		if got.permit.Endpoint.ID != preferred.ID {
			t.Fatalf("expected newly available preferred endpoint %d, got %d", preferred.ID, got.permit.Endpoint.ID)
		}
	case <-time.After(time.Second):
		t.Fatalf("queued request was not re-evaluated after an alternate candidate limit increased: %#v", s.Items())
	}
}
