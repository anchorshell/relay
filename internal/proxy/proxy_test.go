package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/body"
	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/internal/transport"
	"github.com/labstack/echo/v5"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type terminalGuardrailEngine struct{}

func (terminalGuardrailEngine) Evaluate(_ context.Context, input guardrails.Input, config guardrails.Config) (guardrails.Result, error) {
	return guardrails.Result{
		Decision:       guardrails.DecisionError,
		GuardrailUUID:  config.UUID,
		GuardrailName:  config.Name,
		Preset:         config.Preset,
		Stage:          input.Stage,
		HTTPStatus:     http.StatusServiceUnavailable,
		ProviderStatus: http.StatusUnprocessableEntity,
		ErrorCode:      "http_4xx",
		FailMode:       guardrails.FailClosed,
	}, nil
}

type recordingGuardrailEngine struct {
	stages       []guardrails.Stage
	requestTexts []string
	responseText []string
}

func (engine *recordingGuardrailEngine) Evaluate(_ context.Context, input guardrails.Input, config guardrails.Config) (guardrails.Result, error) {
	engine.stages = append(engine.stages, input.Stage)
	engine.requestTexts = append(engine.requestTexts, input.Request.Text)
	responseText := ""
	if input.Response != nil {
		responseText = input.Response.Text
	}
	engine.responseText = append(engine.responseText, responseText)
	return guardrails.Result{
		Decision:                    guardrails.DecisionAllow,
		GuardrailUUID:               config.UUID,
		GuardrailName:               config.Name,
		Preset:                      config.Preset,
		Stage:                       input.Stage,
		HTTPStatus:                  http.StatusOK,
		ProviderStatus:              http.StatusOK,
		ProviderResponseBody:        json.RawMessage(`{"results":[{"categories":{"criminal":true,"dangerous":false}}]}`),
		ProviderResponseContentType: "application/json",
		TrueResponseFields:          []guardrails.ResponseFieldMatch{{Path: "results[0].categories.criminal", Value: true}},
	}, nil
}

func TestStoreCapturedRequestBodiesPublishesAvailabilityDelta(t *testing.T) {
	st := testutil.NewStore(t)
	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	server := &Server{store: st, telemetry: hub}
	if err := st.DB().Exec("ALTER TABLE request_logs ADD COLUMN organization_uuid text").Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Exec("ALTER TABLE request_logs ADD COLUMN user_uuid text").Error; err != nil {
		t.Fatal(err)
	}
	log := models.RequestLog{RequestID: "payload-availability", TaskState: "completed", StatusCode: http.StatusOK}
	if err := st.Create(context.Background(), &log); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Table("request_logs").Where("request_id = ?", log.RequestID).Updates(map[string]any{
		"organization_uuid": "11111111-1111-4111-8111-111111111111",
		"user_uuid":         "22222222-2222-4222-8222-222222222222",
	}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.ContextWithScope(context.Background(), tenancy.Scope{
		OrganizationUUID: "11111111-1111-4111-8111-111111111111",
		UserUUID:         "22222222-2222-4222-8222-222222222222",
	})

	ctx, cancel := context.WithCancel(ctx)
	cancel() // The client can disconnect before post-response payload capture.
	server.storeCapturedRequestBodies(ctx, false, log.RequestID, []byte(`{"request":true}`), nil, []byte(`{"response":true}`))
	disabled, err := st.GetRequestLog(context.Background(), log.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.RequestBodyJSON != "" || disabled.ResponseBodyJSON != "" || len(hub.Events()) != 0 {
		t.Fatal("disabled capture must not store bodies or publish availability")
	}
	server.storeCapturedRequestBodies(ctx, true, log.RequestID, []byte(`{"request":true}`), nil, []byte(`{"response":true}`))

	events := hub.Events()
	if len(events) != 1 {
		t.Fatalf("expected one payload availability event, got %#v", events)
	}
	event := events[0]
	if event.Type != "request_log" ||
		event.Payload["request_id"] != log.RequestID ||
		event.Payload["request_bodies_stored"] != true ||
		event.Payload["organization_uuid"] != "11111111-1111-4111-8111-111111111111" ||
		event.Payload["actor_id"] != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("unexpected payload availability event: %#v", event)
	}
	stored, err := st.GetRequestLog(context.Background(), log.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RequestBodyJSON == "" || stored.ResponseBodyJSON == "" {
		t.Fatalf("expected captured bodies to remain persisted: %#v", stored)
	}
}

type noCandidateStrategy struct{}

func (noCandidateStrategy) SelectCandidates(context.Context, router.RequestMeta) ([]router.Candidate, error) {
	return nil, nil
}

func TestReadResponseBodyDoesNotPublishImmediateProgressBurst(t *testing.T) {
	hub := telemetry.NewHub()
	server := &Server{telemetry: hub}
	body, tokens, err := server.readResponseBody(
		bytes.NewBufferString(`{"ok":true}`),
		router.RequestMeta{Lane: "default", IncomingModel: "test"},
		"request-a",
		scheduler.Permit{TaskID: "task-a", EstimatedInput: 1},
		time.Now().UTC(),
		"downloading response",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || tokens <= 0 {
		t.Fatalf("expected buffered response and token estimate, got body=%q tokens=%d", body, tokens)
	}
	if events := hub.Events(); len(events) != 0 {
		t.Fatalf("expected the caller to publish one final progress event instead of a helper burst, got %#v", events)
	}
}

func TestApplyRoutingPolicyUsesRoutingGroupWaitBudget(t *testing.T) {
	lane := models.RoutingLane{ID: 42, Name: "agentic", DefaultMaxWaitMS: 5000, AllowFallback: true}
	meta := router.RequestMeta{
		AllowFallback: true,
		Overrides: map[string]any{
			"max_wait_ms":    int64(0),
			"allow_fallback": true,
		},
	}

	got := applyRoutingPolicy(meta, []router.Candidate{{Lane: &lane}})

	if got.MaxWaitMS != lane.DefaultMaxWaitMS {
		t.Fatalf("expected lane max wait %d, got %d", lane.DefaultMaxWaitMS, got.MaxWaitMS)
	}
	if got.Overrides["max_wait_ms"] != lane.DefaultMaxWaitMS {
		t.Fatalf("expected overrides max wait to be updated, got %#v", got.Overrides["max_wait_ms"])
	}
}

func TestApplyRoutingPolicyUsesConfiguredFallback(t *testing.T) {
	lane := models.RoutingLane{ID: 42, Name: "agentic", DefaultMaxWaitMS: 5000, AllowFallback: false}
	meta := router.RequestMeta{
		MaxWaitMS:     9000,
		AllowFallback: true,
		Overrides: map[string]any{
			"max_wait_ms":    int64(9000),
			"allow_fallback": true,
		},
	}

	got := applyRoutingPolicy(meta, []router.Candidate{{Lane: &lane}})

	if got.MaxWaitMS != 9000 {
		t.Fatalf("expected internally assigned max wait to be preserved, got %d", got.MaxWaitMS)
	}
	if got.AllowFallback {
		t.Fatal("expected configured no-fallback policy")
	}
}

func TestDetectUpstreamThrottleFake200AirforceJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}
	body := []byte(`{"id":"chatcmpl-e06a8958-1fc4-4458-90ae-0d0e86de77d7","object":"chat.completion","created":1775701803,"model":"minimax-m2.5","choices":[{"index":0,"message":{"role":"assistant","content":"Ratelimit Exceeded!\nPlease join: https://discord.gg/airforce"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)

	wait, reason, throttled := detectUpstreamThrottle(resp, body, []limits.EffectiveLimit{})
	if !throttled {
		t.Fatalf("expected fake-200 airforce payload to be treated as throttle")
	}
	if reason != "upstream rate limited" {
		t.Fatalf("expected upstream rate limited reason, got %q", reason)
	}
	if wait <= 0 {
		t.Fatalf("expected positive retry wait, got %s", wait)
	}
}

func TestUsageFromResponseForStatusOnlyFallsBackToEstimatesFor2xx(t *testing.T) {
	errorBody := []byte(`{"error":{"message":"bad request"}}`)
	if in, out := usageFromResponseForStatus(http.StatusBadRequest, errorBody, 12, 34); in != 0 || out != 0 {
		t.Fatalf("expected 400 without provider usage not to count estimates, got %d/%d", in, out)
	}
	if in, out := usageFromResponseForStatus(http.StatusInternalServerError, errorBody, 12, 34); in != 0 || out != 0 {
		t.Fatalf("expected 500 without provider usage not to count estimates, got %d/%d", in, out)
	}
	if in, out := usageFromResponseForStatus(http.StatusOK, errorBody, 12, 34); in != 12 || out != 34 {
		t.Fatalf("expected 2xx without provider usage to retain estimate fallback, got %d/%d", in, out)
	}

	usageBody := []byte(`{"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	if in, out := usageFromResponseForStatus(http.StatusBadRequest, usageBody, 12, 34); in != 2 || out != 3 {
		t.Fatalf("expected 400 with explicit provider usage to count actual usage, got %d/%d", in, out)
	}

	totalOnlyBody := []byte(`{"usage":{"total_tokens":7}}`)
	if in, out := usageFromResponseForStatus(http.StatusBadGateway, totalOnlyBody, 12, 34); in != 0 || out != 7 {
		t.Fatalf("expected total-only actual usage to be preserved for terminal error, got %d/%d", in, out)
	}
}

func TestDetectUpstreamThrottleFake200AirforceGzipJSON(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(`{"choices":[{"message":{"content":"Ratelimit Exceeded!\nPlease join: https://discord.gg/airforce"}}]}`)); err != nil {
		t.Fatalf("gzip write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close failed: %v", err)
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	wait, reason, throttled := detectUpstreamThrottle(resp, compressed.Bytes(), []limits.EffectiveLimit{})
	if !throttled {
		t.Fatalf("expected gzip fake-200 airforce payload to be treated as throttle")
	}
	if reason != "upstream rate limited" {
		t.Fatalf("expected upstream rate limited reason, got %q", reason)
	}
	if wait <= 0 {
		t.Fatalf("expected positive retry wait, got %s", wait)
	}
}

func TestDetectThrottleStatusAndDelayFake200AirforceSSE(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"message\":{\"content\":\"Ratelimit Exceeded!\\nPlease join: https://discord.gg/airforce\"}}]}\n\n")

	wait, reason, throttled := detectThrottleStatusAndDelay(http.StatusOK, http.Header{}, body, []limits.EffectiveLimit{})
	if !throttled {
		t.Fatalf("expected SSE-framed fake-200 airforce payload to be treated as throttle")
	}
	if reason != "upstream rate limited" {
		t.Fatalf("expected upstream rate limited reason, got %q", reason)
	}
	if wait <= 0 {
		t.Fatalf("expected positive retry wait, got %s", wait)
	}
}

func TestMarkEndpointThrottleEscalatesCooldown(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	endpoint := models.Endpoint{
		ID:             1,
		UUID:           "11111111-1111-4111-8111-111111111111",
		ProviderID:     1,
		ProviderUUID:   "22222222-2222-4222-8222-222222222222",
		CredentialID:   1,
		CredentialUUID: "77777777-7777-4777-8777-777777777777",
		Name:           "dummy",
		UpstreamModel:  "dummy",
		Enabled:        true,
		HealthStatus:   models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	sibling := models.Endpoint{
		ID:             2,
		UUID:           "33333333-3333-4333-8333-333333333333",
		ProviderID:     endpoint.ProviderID,
		ProviderUUID:   endpoint.ProviderUUID,
		CredentialID:   2,
		CredentialUUID: "88888888-8888-4888-8888-888888888888",
		Name:           "same-provider-other-credential",
		UpstreamModel:  "fallback",
		Enabled:        true,
		HealthStatus:   models.HealthHealthy,
	}
	if err := st.Create(ctx, &sibling); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: st, failures: map[uint]int{}}

	wait, err := server.markEndpointThrottle(ctx, endpoint, time.Second, "upstream rate limited", http.StatusTooManyRequests)
	if err != nil {
		t.Fatal(err)
	}
	if wait != upstreamThrottleInitialBackoff {
		t.Fatalf("expected first throttle backoff %s, got %s", upstreamThrottleInitialBackoff, wait)
	}
	var updated models.Endpoint
	if err := st.FindByID(ctx, &updated, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated.HealthStatus != models.HealthCoolingDown || updated.CooldownUntil == nil {
		t.Fatalf("expected endpoint cooling down after first throttle, got status=%s until=%v", updated.HealthStatus, updated.CooldownUntil)
	}
	if updated.CooldownReason != "upstream_rate_limited" || updated.CooldownStatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected stored 429 cooldown cause, got reason=%q status=%d", updated.CooldownReason, updated.CooldownStatusCode)
	}
	var unaffected models.Endpoint
	if err := st.FindByID(ctx, &unaffected, sibling.ID); err != nil {
		t.Fatal(err)
	}
	if unaffected.HealthStatus != models.HealthHealthy || unaffected.CooldownUntil != nil {
		t.Fatalf("provider exhaustion leaked to a different credential/endpoint: %#v", unaffected)
	}

	wait, err = server.markEndpointThrottle(ctx, endpoint, time.Second, "upstream rate limited", http.StatusTooManyRequests)
	if err != nil {
		t.Fatal(err)
	}
	if wait != upstreamThrottleSecondBackoff {
		t.Fatalf("expected second throttle backoff %s, got %s", upstreamThrottleSecondBackoff, wait)
	}

	wait, err = server.markEndpointThrottle(ctx, endpoint, 90*time.Second, "upstream rate limited", http.StatusTooManyRequests)
	if err != nil {
		t.Fatal(err)
	}
	if wait != 90*time.Second {
		t.Fatalf("expected longer provider retry-after to win, got %s", wait)
	}
}

func TestMarkEndpointFailureStoresServerStatusForRetryCooldown(t *testing.T) {
	st := testutil.NewStore(t)
	ctx := context.Background()
	endpoint := models.Endpoint{ID: 1, Name: "dummy", UpstreamModel: "dummy", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: st, failures: map[uint]int{}}

	if _, _, err := server.markEndpointFailure(ctx, endpoint, http.StatusServiceUnavailable, "Service Unavailable"); err != nil {
		t.Fatal(err)
	}
	var updated models.Endpoint
	if err := st.FindByID(ctx, &updated, endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if updated.CooldownReason != "upstream_server_error" || updated.CooldownStatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected stored 503 cooldown cause, got reason=%q status=%d", updated.CooldownReason, updated.CooldownStatusCode)
	}
}

func TestSafeForwardRequestHeadersStripsAuthCookieAndHopByHop(t *testing.T) {
	headers := http.Header{
		"Authorization":       []string{"Bearer inbound-admin-token"},
		"Cookie":              []string{"relay_admin_token=session"},
		"Connection":          []string{"upgrade"},
		"Proxy-Authorization": []string{"Basic secret"},
		"X-Api-Key":           []string{"sk-inbound"},
		"Accept":              []string{"application/json"},
		"OpenAI-Beta":         []string{"assistants=v2"},
	}

	got := safeForwardRequestHeaders(headers)
	for _, key := range []string{"Authorization", "Cookie", "Connection", "Proxy-Authorization", "X-Api-Key"} {
		if got.Get(key) != "" {
			t.Fatalf("expected %s to be stripped, got %q", key, got.Get(key))
		}
	}
	if got.Get("Accept") != "application/json" {
		t.Fatalf("expected Accept to be forwarded, got %q", got.Get("Accept"))
	}
	if got.Get("OpenAI-Beta") != "assistants=v2" {
		t.Fatalf("expected OpenAI-Beta to be forwarded, got %q", got.Get("OpenAI-Beta"))
	}
}

func TestSafeForwardResponseHeadersStripsSensitiveAndHopByHopHeaders(t *testing.T) {
	headers := http.Header{
		"Authorization":     []string{"Bearer provider-secret"},
		"Set-Cookie":        []string{"provider_session=secret"},
		"Transfer-Encoding": []string{"chunked"},
		"Content-Type":      []string{"application/json"},
	}

	got := safeForwardResponseHeaders(headers)
	for _, key := range []string{"Authorization", "Set-Cookie", "Transfer-Encoding"} {
		if got.Get(key) != "" {
			t.Fatalf("expected %s to be stripped, got %q", key, got.Get(key))
		}
	}
	if got.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type to be forwarded, got %q", got.Get("Content-Type"))
	}
}

func TestApplyProxyUsageHeadersSetsNumericUsageOnly(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	applyProxyUsageHeaders(c, 12, 34, 560000)

	headers := rec.Header()
	if headers.Get("X-Relay-Actual-Input-Tokens") != "12" {
		t.Fatalf("unexpected input token header: %q", headers.Get("X-Relay-Actual-Input-Tokens"))
	}
	if headers.Get("X-Relay-Actual-Output-Tokens") != "34" {
		t.Fatalf("unexpected output token header: %q", headers.Get("X-Relay-Actual-Output-Tokens"))
	}
	if headers.Get("X-Relay-Actual-Total-Tokens") != "46" {
		t.Fatalf("unexpected total token header: %q", headers.Get("X-Relay-Actual-Total-Tokens"))
	}
	if headers.Get("X-Relay-Actual-Cost-Micros") != "560000" {
		t.Fatalf("unexpected cost header: %q", headers.Get("X-Relay-Actual-Cost-Micros"))
	}
	for _, forbidden := range []string{"Authorization", "Cookie", "Set-Cookie"} {
		if headers.Get(forbidden) != "" {
			t.Fatalf("usage headers leaked %s: %q", forbidden, headers.Get(forbidden))
		}
	}
}

func TestUpstreamRequestAdapterReceivesNormalizedReasoningAndOwnBodyCopy(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	sch := scheduler.New(st, limits.NewResolver(st, tracker), tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	sch.Start()
	t.Cleanup(sch.Stop)

	provider := models.Provider{Name: "adapter", Slug: "adapter", BaseURL: "https://upstream.example.test", AuthMode: "none", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "reasoning", UpstreamModel: "upstream-reasoning",
		RouteKind: models.RouteKindChat, Enabled: true, ManualRank: 1, HealthStatus: models.HealthHealthy,
		ReasoningControlKind: "effort", AllowedReasoningEfforts: []string{"low", "medium", "high"}, MaximumReasoningEffort: "high",
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	var upstreamBody map[string]any
	server := NewServer(
		st, router.NewWaterfallStrategy(st), sch, body.NewStore(t.TempDir(), 1024, 1024),
		&transport.UpstreamTransport{Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(req.Body).Decode(&upstreamBody); err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
		})}, telemetry.NewHub(),
	).WithUpstreamRequestAdapters([]UpstreamRequestAdapter{
		func(_ context.Context, input UpstreamRequestInput) (map[string]any, error) {
			if input.ReasoningEffort != "high" || input.ProviderKey != "adapter" || input.EndpointID != endpoint.UUID {
				t.Fatalf("unexpected adapter input: %#v", input)
			}
			delete(input.Body, "reasoning_effort")
			input.Body["reasoning"] = map[string]any{"effort": input.ReasoningEffort}
			return input.Body, nil
		},
	})

	e := echo.New()
	server.Register(e.Group("/v1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"upstream-reasoning","reasoning_effort":"HIGH","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if upstreamBody["model"] != "upstream-reasoning" {
		t.Fatalf("upstream model was not replaced: %#v", upstreamBody)
	}
	if _, present := upstreamBody["reasoning_effort"]; present {
		t.Fatalf("adapter did not remove normalized field: %#v", upstreamBody)
	}
	reasoning, ok := upstreamBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("adapter did not map effort: %#v", upstreamBody)
	}
}

func TestUpstreamDispatcherOwnsSDKRequestButReturnsToCommonResponsePath(t *testing.T) {
	provider := models.Provider{ID: 11, UUID: "provider-public", Slug: "sdk-provider", BaseURL: "https://must-not-run.invalid", AuthMode: "none"}
	credential := models.Credential{ID: 22, UUID: "credential-public"}
	endpoint := models.Endpoint{ID: 33, UUID: "endpoint-public", ProviderID: provider.ID, UpstreamModel: "sdk-model", RouteKind: models.RouteKindChat}
	var received UpstreamDispatchInput
	server := &Server{
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("ordinary HTTP transport ran for handled SDK request")
			return nil, nil
		})},
		upstreamDispatchers: []UpstreamDispatcher{func(_ context.Context, input UpstreamDispatchInput) (*http.Response, bool, error) {
			received = input
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"sdk response"}}]}`)),
			}, true, nil
		}},
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	resp, _, _, encoded, err := server.dispatch(context.Background(), c, "chat/completions", provider, credential, endpoint,
		map[string]any{"model": "incoming", "stream": false, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}, "", 4, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if received.ProviderKey != provider.Slug || received.CredentialStorageID != credential.ID || received.EndpointID != endpoint.UUID || received.UpstreamModel != endpoint.UpstreamModel {
		t.Fatalf("unexpected dispatcher identity: %#v", received)
	}
	if received.Path != "chat/completions" || received.Streaming || !bytes.Equal(received.Body, encoded) {
		t.Fatalf("unexpected dispatcher request: %#v", received)
	}
	var request map[string]any
	if json.Unmarshal(received.Body, &request) != nil || request["model"] != endpoint.UpstreamModel {
		t.Fatalf("dispatcher did not receive the finalized upstream body: %s", received.Body)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(responseBody), "sdk response") {
		t.Fatalf("unexpected dispatcher response %q err=%v", responseBody, err)
	}
}

func TestRequestReasoningEffortRejectsConflictingRepresentations(t *testing.T) {
	_, err := requestReasoningEffort(map[string]any{
		"reasoning_effort": "high",
		"reasoning":        map[string]any{"effort": "low"},
	})
	if err == nil {
		t.Fatal("expected conflicting reasoning effort values to be rejected")
	}
}

func TestRequestCompletedHookRunsBeforeSchedulerReleasesExternalReservation(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(st)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	sch.Start()
	t.Cleanup(sch.Stop)

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
		Name:          "dummy",
		UpstreamModel: "dummy",
		RouteKind:     models.RouteKindChat,
		Enabled:       true,
		ManualRank:    1,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}

	userScope := limits.ScopeRef{Type: models.ScopeType("user"), Key: "org-1:user-a"}
	sch.WithExternalLimitHooks(func(_ context.Context, input scheduler.ExternalLimitInput) ([]scheduler.ExternalLimit, error) {
		if input.Metadata["user_uuid"] != "user-a" {
			return nil, nil
		}
		return []scheduler.ExternalLimit{{
			Scope:      scheduler.LimitScope{Type: "user", ID: "org-1:user-a"},
			Metric:     string(models.MetricRequests),
			Period:     string(models.PeriodMinute),
			LimitValue: 2,
			Used:       0,
			ResetAt:    time.Now().UTC().Add(time.Minute),
		}}, nil
	})

	var usageSeenInCompletionHook int64
	server := NewServer(
		st,
		router.NewWaterfallStrategy(st),
		sch,
		body.NewStore(t.TempDir(), 1024, 1024),
		&transport.UpstreamTransport{
			Base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":4,"total_tokens":5}}`)),
				}, nil
			}),
		},
		telemetry.NewHub(),
	).
		WithRequestContextHooks([]RequestContextHook{
			func(context.Context, RequestContextInput) (TrustedRequestContext, error) {
				return TrustedRequestContext{Metadata: map[string]string{"user_uuid": "user-a"}}, nil
			},
		}).
		WithRequestCompletedHooks([]RequestCompletedHook{
			func(_ context.Context, event RequestCompletedEvent) {
				usageSeenInCompletionHook = tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, event.FinishedAt)
			},
		})

	e := echo.New()
	server.Register(e.Group("/v1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"dummy","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if usageSeenInCompletionHook != 1 {
		t.Fatalf("expected completion hook to see in-flight user reservation 1, got %d", usageSeenInCompletionHook)
	}
	if got := tracker.Used(userScope, models.MetricRequests, models.PeriodMinute, time.Now().UTC()); got != 0 {
		t.Fatalf("expected live external user reservation to be released after completion, got %d", got)
	}
}

func TestWaitBudget429StoresRedactedClientAndDownstreamPayloads(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	if err := st.UpsertSetting(ctx, "store_requests", true); err != nil {
		t.Fatal(err)
	}
	tracker := limits.NewTracker(st)
	sch := scheduler.New(st, limits.NewResolver(st, tracker), tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	server := NewServer(
		st,
		noCandidateStrategy{},
		sch,
		body.NewStore(t.TempDir(), 1024, 1024),
		&transport.UpstreamTransport{},
		telemetry.NewHub(),
	)

	e := echo.New()
	server.Register(e.Group("/v1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"agentic","messages":[{"role":"user","content":"hello"}],"api_key":"sk-request-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d: %s", rec.Code, rec.Body.String())
	}
	var log models.RequestLog
	if err := st.DB().Where("status_code = ?", http.StatusTooManyRequests).First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.RequestBodyJSON, "sk-request-secret") || !strings.Contains(log.RequestBodyJSON, "[REDACTED]") {
		t.Fatalf("expected stored request payload to be redacted, got %q", log.RequestBodyJSON)
	}
	if log.ResponseBodyJSON == "" {
		t.Fatal("expected the client-facing 429 payload to be stored")
	}
	var delivered map[string]any
	var stored map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &delivered); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(log.ResponseBodyJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, delivered) {
		t.Fatalf("expected stored downstream payload to match 429 response, stored=%#v delivered=%#v", stored, delivered)
	}
	errorBody, ok := stored["error"].(map[string]any)
	if !ok || errorBody["type"] != "rate_limit_wait_budget_exceeded" {
		t.Fatalf("unexpected stored 429 payload: %#v", stored)
	}
}

func TestPreDispatchGuardrailFailureStoresClientAndDownstreamPayloads(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	if err := st.UpsertSetting(ctx, "store_requests", true); err != nil {
		t.Fatal(err)
	}
	provider := models.Provider{Name: "guarded-provider", Slug: "guarded-provider", BaseURL: "https://provider.example", AuthMode: "none", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "guarded-model", UpstreamModel: "guarded-model", RouteKind: models.RouteKindChat, Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "guarded-group", Enabled: true, AllowFallback: true, DefaultMaxWaitMS: 30000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, LaneUUID: lane.UUID, EndpointID: endpoint.ID, EndpointUUID: endpoint.UUID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	guardrail := models.Guardrail{
		Name:                   "failing moderation",
		Enabled:                true,
		PresetSlug:             "mistral-moderation",
		Priority:               10,
		HTTPMethod:             http.MethodPost,
		BaseURL:                "https://guardrail.example/check",
		AuthMode:               "none",
		TimeoutMS:              3000,
		MaxRequestBytes:        1024 * 1024,
		MaxResponseBytes:       1024 * 1024,
		NetworkAccessMode:      guardrails.NetworkPublicHTTPS,
		PreDispatchEnabled:     true,
		PreRequestTemplateJSON: `{"input":"{{request.text}}"}`,
		PreResponseRulesJSON:   `{"match_mode":"any","missing_path":"error","rules":[{"id":"flagged","source":"json_body","path":"$.flagged","operator":"equals","value":true}],"on_match":{"action":"block"},"on_no_match":{"action":"allow"}}`,
		PreFailurePolicyJSON:   `{"mode":"fail_closed","http_status":503,"error_type":"guardrail_error","error_code":"guardrail_unavailable","message":"The configured guardrail service is unavailable."}`,
	}
	if err := st.Create(ctx, &guardrail); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.GuardrailBinding{GuardrailID: guardrail.ID, GuardrailUUID: guardrail.UUID, RoutingLaneID: &lane.ID, RoutingLaneUUID: &lane.UUID, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	tracker := limits.NewTracker(st)
	sch := scheduler.New(st, limits.NewResolver(st, tracker), tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	sch.Start()
	t.Cleanup(sch.Stop)
	server := NewServer(st, router.NewWaterfallStrategy(st), sch, body.NewStore(t.TempDir(), 1024, 1024), &transport.UpstreamTransport{}, telemetry.NewHub()).
		WithGuardrails(guardrails.NewRuntime(st, terminalGuardrailEngine{}))
	e := echo.New()
	server.Register(e.Group("/v1"))
	requestBody := `{"model":"guarded-group","messages":[{"role":"user","content":"retain this request"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected guardrail status 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var log models.RequestLog
	if err := st.DB().Where("status_code = ?", http.StatusServiceUnavailable).First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.RequestBodyJSON, "retain this request") {
		t.Fatalf("pre-dispatch failure omitted retained request payload: %q", log.RequestBodyJSON)
	}
	if log.UpstreamRequestJSON != "" {
		t.Fatalf("pre-dispatch failure should not report an LLM upstream payload: %q", log.UpstreamRequestJSON)
	}
	var delivered map[string]any
	var stored map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &delivered); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(log.ResponseBodyJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, delivered) {
		t.Fatalf("stored guardrail failure differs from delivered response: stored=%#v delivered=%#v", stored, delivered)
	}
}

func TestEnabledPreAndPostGuardrailsRunAroundProviderDispatch(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	if err := st.UpsertSetting(ctx, "store_requests", true); err != nil {
		t.Fatal(err)
	}
	provider := models.Provider{Name: "guarded-provider", Slug: "guarded-provider", BaseURL: "https://provider.example", AuthMode: "none", Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, ProviderUUID: provider.UUID, Name: "guarded-model", UpstreamModel: "guarded-model", RouteKind: models.RouteKindChat, Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	lane := models.RoutingLane{Name: "guarded-both-stages", Enabled: true, AllowFallback: true, DefaultMaxWaitMS: 30000}
	if err := st.Create(ctx, &lane); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: lane.ID, LaneUUID: lane.UUID, EndpointID: endpoint.ID, EndpointUUID: endpoint.UUID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	rules := `{"match_mode":"any","missing_path":"error","rules":[{"id":"flagged","source":"json_body","path":"$.flagged","operator":"equals","value":true}],"on_match":{"action":"block"},"on_no_match":{"action":"allow"}}`
	failure := `{"mode":"fail_closed","http_status":503,"error_type":"guardrail_error","error_code":"guardrail_unavailable","message":"The configured guardrail service is unavailable."}`
	guardrail := models.Guardrail{
		Name:                    "both stages",
		Enabled:                 true,
		PresetSlug:              "mistral-moderation",
		Priority:                10,
		HTTPMethod:              http.MethodPost,
		BaseURL:                 "https://guardrail.example/check",
		AuthMode:                "none",
		TimeoutMS:               3000,
		MaxRequestBytes:         1024 * 1024,
		MaxResponseBytes:        1024 * 1024,
		NetworkAccessMode:       guardrails.NetworkPublicHTTPS,
		PreDispatchEnabled:      true,
		PostResponseEnabled:     true,
		PreRequestTemplateJSON:  `{"input":"{{request.text}}"}`,
		PostRequestTemplateJSON: `{"input":"{{response.text}}"}`,
		PreResponseRulesJSON:    rules,
		PostResponseRulesJSON:   rules,
		PreFailurePolicyJSON:    failure,
		PostFailurePolicyJSON:   failure,
	}
	if err := st.Create(ctx, &guardrail); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.GuardrailBinding{GuardrailID: guardrail.ID, GuardrailUUID: guardrail.UUID, RoutingLaneID: &lane.ID, RoutingLaneUUID: &lane.UUID, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	tracker := limits.NewTracker(st)
	sch := scheduler.New(st, limits.NewResolver(st, tracker), tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	sch.Start()
	t.Cleanup(sch.Stop)
	engine := &recordingGuardrailEngine{}
	server := NewServer(
		st,
		router.NewWaterfallStrategy(st),
		sch,
		body.NewStore(t.TempDir(), 1024, 1024),
		&transport.UpstreamTransport{Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"provider response"}}]}`)),
			}, nil
		})},
		telemetry.NewHub(),
	).WithGuardrails(guardrails.NewRuntime(st, engine))
	e := echo.New()
	server.Register(e.Group("/v1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"guarded-both-stages","messages":[{"role":"user","content":"client request"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(engine.stages, []guardrails.Stage{guardrails.StagePreDispatch, guardrails.StagePostResponse}) {
		t.Fatalf("guardrail stages=%#v", engine.stages)
	}
	if len(engine.requestTexts) != 2 || !strings.Contains(engine.requestTexts[0], "client request") || !strings.Contains(engine.requestTexts[1], "client request") {
		t.Fatalf("request text was not retained across stages: %#v", engine.requestTexts)
	}
	if len(engine.responseText) != 2 || engine.responseText[0] != "" || engine.responseText[1] != "provider response" {
		t.Fatalf("post-response hook did not receive the provider response: %#v", engine.responseText)
	}
	var log models.RequestLog
	if err := st.DB().Where("task_state = ?", "completed").First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if log.GuardrailStatus != "passed" {
		t.Fatalf("guardrail status=%q, want passed", log.GuardrailStatus)
	}
	var results []guardrails.AuditResult
	if err := json.Unmarshal([]byte(log.GuardrailResultsJSON), &results); err != nil {
		t.Fatalf("stored guardrail results are invalid: %v body=%q", err, log.GuardrailResultsJSON)
	}
	if len(results) != 2 {
		t.Fatalf("stored guardrail result count=%d, want 2", len(results))
	}
	var responses []guardrails.AuditResponse
	if err := json.Unmarshal([]byte(log.GuardrailResponseBodiesJSON), &responses); err != nil {
		t.Fatalf("stored guardrail responses are invalid: %v body=%q", err, log.GuardrailResponseBodiesJSON)
	}
	if len(responses) != 2 || len(responses[0].TrueFields) != 1 || responses[0].TrueFields[0].Path != "results[0].categories.criminal" {
		t.Fatalf("pre/post guardrail responses were not retained: %#v", responses)
	}
}

func TestIsDownstreamCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if isDownstreamCancelled(ctx) {
		t.Fatal("fresh context should not be treated as cancelled")
	}
	cancel()
	if !isDownstreamCancelled(ctx) {
		t.Fatal("cancelled downstream context should be detected")
	}
}

func TestCrossHostRedirectsAreNotFollowed(t *testing.T) {
	first := httptest.NewRequest(http.MethodGet, "https://api.example.test/v1/chat/completions", nil)
	next := httptest.NewRequest(http.MethodGet, "https://evil.example.test/redirected", nil)
	if err := sameOriginRedirectOnly(next, []*http.Request{first}); err != http.ErrUseLastResponse {
		t.Fatalf("expected cross-host redirect to be blocked, got %v", err)
	}

	sameHost := httptest.NewRequest(http.MethodGet, "https://api.example.test/other", nil)
	if err := sameOriginRedirectOnly(sameHost, []*http.Request{first}); err != nil {
		t.Fatalf("expected same-host redirect to be allowed, got %v", err)
	}
}

func TestSelfReferentialProxyTargetDetectsRoutedLoop(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost:11730/v1/chat/completions", nil)
	target, err := url.Parse("http://127.0.0.1:11730/v1/chat/completions")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	if !isSelfReferentialProxyTarget(req, target) {
		t.Fatal("expected loopback target with the same routed path to be detected")
	}
}

func TestSelfReferentialProxyTargetAllowsBuiltInDummyEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://localhost:11730/v1/chat/completions", nil)
	target, err := url.Parse("http://127.0.0.1:11730/v1/dummy/chat/completions")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	if isSelfReferentialProxyTarget(req, target) {
		t.Fatal("expected built-in dummy endpoint path to be allowed")
	}
}

func TestDispatchRejectsSelfReferentialProviderBeforeHTTP(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:11730/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	server := &Server{httpClient: &http.Client{Transport: failingRoundTripper{t: t}}}

	_, _, _, _, err := server.dispatch(
		context.Background(),
		c,
		"chat/completions",
		models.Provider{BaseURL: "http://localhost:11730/v1"},
		models.Credential{},
		models.Endpoint{UpstreamModel: "dummy"},
		map[string]any{"model": "dummy"},
		"",
		0,
		0,
	)
	if !errors.Is(err, errSelfReferentialProvider) {
		t.Fatalf("expected self-referential provider error, got %v", err)
	}
}

func TestFormatStoredPayloadRedactsSecretFields(t *testing.T) {
	payload := []byte(`{"messages":[{"content":"ok"}],"api_key":"sk-body-secret","nested":{"Authorization":"Bearer nested-secret"}}`)
	got := formatStoredPayload(payload)
	if strings.Contains(got, "sk-body-secret") || strings.Contains(got, "nested-secret") {
		t.Fatalf("stored payload leaked secret values: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redacted payload, got %s", got)
	}
}

func TestEstimateTokensFromPayloadUsesTextSegments(t *testing.T) {
	body := []byte("\u4f60\u597d\u4e16\u754c")

	got := estimateTokensFromPayload(body, int64(len(body)))
	if got != 4 {
		t.Fatalf("expected CJK response body to count runes, got %d", got)
	}
}

func TestEstimateTokensFromPayloadFallsBackForPartialBody(t *testing.T) {
	got := estimateTokensFromPayload([]byte("partial"), 13)
	if got != 3 {
		t.Fatalf("expected partial body to use byte fallback, got %d", got)
	}
}

func TestOversizedProxyRequestReturns413(t *testing.T) {
	e := echo.New()
	server := NewServer(nil, nil, nil, body.NewStore(t.TempDir(), 4, 4).WithMaxBytes(8), &transport.UpstreamTransport{}, nil)
	server.Register(e.Group("/v1"))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+strings.Repeat("x", 32)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected oversized proxy request to return 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequestContextBypassSkipsHooks(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var called bool
	server := NewServer(nil, nil, nil, body.NewStore(t.TempDir(), 4, 4), &transport.UpstreamTransport{}, nil).
		WithRequestContextHooks([]RequestContextHook{
			func(context.Context, RequestContextInput) (TrustedRequestContext, error) {
				called = true
				return TrustedRequestContext{}, errors.New("context hook should not be called")
			},
		}).
		WithRequestContextBypass(func(*echo.Context) bool { return true })

	trusted, err := server.requestContext(context.Background(), c, "req-1", models.RouteKindChat)
	if err != nil {
		t.Fatalf("request context: %v", err)
	}
	if called {
		t.Fatal("expected request context hook to be skipped")
	}
	if len(trusted.Metadata) != 0 {
		t.Fatalf("expected empty trusted context, got %#v", trusted)
	}
}

func TestRequestContextRunsWhenBypassDoesNotAuthorize(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Extension-Context", "encoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var called bool
	server := NewServer(nil, nil, nil, body.NewStore(t.TempDir(), 4, 4), &transport.UpstreamTransport{}, nil).
		WithRequestContextHooks([]RequestContextHook{
			func(context.Context, RequestContextInput) (TrustedRequestContext, error) {
				called = true
				return TrustedRequestContext{
					Metadata: map[string]string{"actor": "Test User"},
				}, nil
			},
		}).
		WithRequestContextBypass(func(*echo.Context) bool { return false })

	trusted, err := server.requestContext(context.Background(), c, "req-1", models.RouteKindChat)
	if err != nil {
		t.Fatalf("request context: %v", err)
	}
	if !called {
		t.Fatal("expected request context hook to run when platform headers are present")
	}
	if trusted.Metadata["actor"] != "Test User" {
		t.Fatalf("unexpected trusted context: %#v", trusted)
	}
}

func TestPayloadCapturePoliciesCanOnlyRestrictCapture(t *testing.T) {
	server := (&Server{}).WithPayloadCapturePolicyHooks([]PayloadCapturePolicyHook{
		func(_ context.Context, input PayloadCapturePolicyInput) (bool, error) {
			return input.TrustedContext.Metadata["capture"] == "allowed", nil
		},
	})
	if server.payloadCaptureAllowed(context.Background(), "req-1", TrustedRequestContext{Metadata: map[string]string{"capture": "denied"}}) {
		t.Fatal("expected extension policy to disable payload capture")
	}
	if !server.payloadCaptureAllowed(context.Background(), "req-2", TrustedRequestContext{Metadata: map[string]string{"capture": "allowed"}}) {
		t.Fatal("expected extension policy to allow the administrator setting to remain effective")
	}
}

func TestPayloadCapturePolicyFailureDisablesCapture(t *testing.T) {
	server := (&Server{}).WithPayloadCapturePolicyHooks([]PayloadCapturePolicyHook{
		func(context.Context, PayloadCapturePolicyInput) (bool, error) {
			return true, errors.New("policy unavailable")
		},
	})
	if server.payloadCaptureAllowed(context.Background(), "req-1", TrustedRequestContext{}) {
		t.Fatal("expected fail-closed payload capture policy")
	}
}

type failingRoundTripper struct {
	t *testing.T
}

func (rt failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.t.Fatal("self-referential dispatch should be rejected before HTTP transport")
	return nil, nil
}
