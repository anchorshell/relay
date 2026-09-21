package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/proxy"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/tenancy"
)

type testExtension struct {
	name  string
	apply func(*Hooks) error
}

func (e testExtension) Name() string {
	return e.name
}

func (e testExtension) Apply(hooks *Hooks) error {
	if e.apply != nil {
		return e.apply(hooks)
	}
	return nil
}

func TestExtensionRegistrationOrderIsDeterministic(t *testing.T) {
	var order []string
	_, names, err := applyExtensions([]Extension{
		testExtension{name: "first", apply: func(*Hooks) error {
			order = append(order, "first")
			return nil
		}},
		testExtension{name: "second", apply: func(*Hooks) error {
			order = append(order, "second")
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("apply extensions: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("unexpected apply order: %#v", order)
	}
	if !reflect.DeepEqual(names, []string{"first", "second"}) {
		t.Fatalf("unexpected extension names: %#v", names)
	}
}

func TestRoutingMiddlewareWrapsDefaultPolicy(t *testing.T) {
	base := staticRouter{
		candidates: []router.Candidate{
			{Endpoint: models.Endpoint{UUID: "endpoint-a", ProviderUUID: "provider", Name: "A", UpstreamModel: "a"}, Rank: 1},
			{Endpoint: models.Endpoint{UUID: "endpoint-b", ProviderUUID: "provider", Name: "B", UpstreamModel: "b"}, Rank: 2, FallbackCount: 1},
		},
	}
	var invoked bool
	strategy := newRoutingStrategyAdapter(base, []RoutingMiddleware{
		func(next RoutingPolicy) RoutingPolicy {
			return routingPolicyFunc(func(ctx context.Context, input RoutingInput) (RoutingDecision, error) {
				invoked = true
				decision, err := next.SelectCandidates(ctx, input)
				if err != nil {
					return RoutingDecision{}, err
				}
				decision.Candidates[0], decision.Candidates[1] = decision.Candidates[1], decision.Candidates[0]
				decision.Candidates[0].Rank = 1
				decision.Candidates[0].FallbackCount = 0
				decision.Candidates[1].Rank = 2
				decision.Candidates[1].FallbackCount = 1
				return decision, nil
			})
		},
	})

	candidates, err := strategy.SelectCandidates(context.Background(), router.RequestMeta{IncomingModel: "coding"})
	if err != nil {
		t.Fatalf("select candidates: %v", err)
	}
	if !invoked {
		t.Fatal("expected middleware to be invoked")
	}
	if len(candidates) != 2 || candidates[0].Endpoint.UUID != "endpoint-b" || candidates[1].Endpoint.UUID != "endpoint-a" {
		t.Fatalf("expected middleware reorder to affect internal candidates, got %#v", candidates)
	}
}

func TestRoutingMiddlewareReceivesClonedTrustedTenantMetadata(t *testing.T) {
	metadata := map[string]string{"organization_uuid": "org-1", "user_uuid": "user-1", "api_key_uuid": "key-1"}
	ctx := tenancy.ContextWithMetadataScope(context.Background(), metadata)
	var received map[string]string
	strategy := newRoutingStrategyAdapter(staticRouter{}, []RoutingMiddleware{
		func(next RoutingPolicy) RoutingPolicy {
			return routingPolicyFunc(func(ctx context.Context, input RoutingInput) (RoutingDecision, error) {
				received = input.TrustedMetadata
				input.TrustedMetadata["api_key_uuid"] = "mutated"
				return next.SelectCandidates(ctx, input)
			})
		},
	})
	if _, err := strategy.SelectCandidates(ctx, router.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if received["organization_uuid"] != "org-1" || metadata["api_key_uuid"] != "key-1" {
		t.Fatalf("trusted metadata was missing or aliased: received=%#v source=%#v", received, metadata)
	}
}

func TestStartupHookAndHTTPRegistrarRunOnlyWhenExtensionRegistered(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t, ctx, nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/pro/status", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected pro route to be absent without extension, got %d", rec.Code)
	}

	var startupCalled bool
	app = newTestApp(t, ctx, []Extension{
		testExtension{name: "pro-test", apply: func(hooks *Hooks) error {
			hooks.StartupHooks = append(hooks.StartupHooks, func(_ context.Context, info AppInfo) error {
				startupCalled = true
				if !reflect.DeepEqual(info.Extensions, []string{"pro-test"}) {
					t.Fatalf("unexpected extensions in app info: %#v", info.Extensions)
				}
				return nil
			})
			hooks.HTTPRegistrars = append(hooks.HTTPRegistrars, func(_ context.Context, routes HTTPRoutes) error {
				routes.Handle(http.MethodGet, "/api/pro/status", func(context.Context, HTTPRequest) (HTTPResponse, error) {
					return OK(map[string]any{
						"extension":      "anchorshell-relay-pro",
						"loaded":         true,
						"algorithm_hook": "registered",
					}), nil
				})
				return nil
			})
			return nil
		}},
	})
	if !startupCalled {
		t.Fatal("expected startup hook to run")
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/pro/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected pro route status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["loaded"] != true || body["extension"] != "anchorshell-relay-pro" {
		t.Fatalf("unexpected pro status body: %#v", body)
	}
}

func TestShutdownHooksRunExactlyOnce(t *testing.T) {
	var calls int
	app := newTestApp(t, context.Background(), []Extension{
		testExtension{name: "shutdown-test", apply: func(hooks *Hooks) error {
			hooks.ShutdownHooks = append(hooks.ShutdownHooks, func(context.Context) error {
				calls++
				return nil
			})
			return nil
		}},
	})

	app.Stop()
	app.Stop()
	if calls != 1 {
		t.Fatalf("expected shutdown hook to run once, got %d", calls)
	}
}

func TestRequestInFlightHookAdapterUsesSafeDTO(t *testing.T) {
	var got RequestInFlightEvent
	hooks := proxyRequestInFlightHooks([]RequestInFlightHook{
		func(_ context.Context, event RequestInFlightEvent) {
			got = event
		},
	})
	if len(hooks) != 1 {
		t.Fatalf("expected one proxy hook, got %d", len(hooks))
	}
	selectedAt := time.Date(2026, 6, 28, 18, 0, 0, 0, time.UTC)
	hooks[0](context.Background(), proxy.RequestInFlightEvent{
		RequestID:     "req-1",
		RouteKind:     models.RouteKindChat,
		IncomingModel: "dummy",
		Lane:          "agentic",
		EndpointID:    "endpoint-id",
		EndpointName:  "Dummy",
		ProviderID:    "provider-id",
		UpstreamModel: "dummy",
		Waited:        9 * time.Second,
		SelectedAt:    selectedAt,
		TrustedContext: proxy.TrustedRequestContext{
			Metadata: map[string]string{
				"actor":       "Test User",
				"product_key": "model_relay",
			},
			Limits: proxy.TrustedRequestLimits{
				RemainingDailyRequests: ptrInt64(7),
			},
		},
	})

	if got.RequestID != "req-1" || got.RouteKind != RouteKindChat || got.UpstreamModel != "dummy" {
		t.Fatalf("unexpected in-flight event: %#v", got)
	}
	if got.WaitMS != 9000 || got.Waited != 9*time.Second || !got.SelectedAt.Equal(selectedAt) {
		t.Fatalf("unexpected wait fields: %#v", got)
	}
	if got.Metadata["actor"] != "Test User" || got.Metadata["product_key"] != "model_relay" {
		t.Fatalf("unexpected trusted metadata: %#v", got.Metadata)
	}
	if got.Limits.RemainingDailyRequests == nil || *got.Limits.RemainingDailyRequests != 7 {
		t.Fatalf("unexpected trusted limit fields: %#v", got.Limits)
	}
}

func TestRequestCompletedHookAdapterUsesSafeDTO(t *testing.T) {
	var got RequestCompletedEvent
	hooks := proxyRequestCompletedHooks([]RequestCompletedHook{
		func(_ context.Context, event RequestCompletedEvent) {
			got = event
		},
	})
	if len(hooks) != 1 {
		t.Fatalf("expected one proxy hook, got %d", len(hooks))
	}
	startedAt := time.Date(2026, 6, 28, 18, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(2 * time.Second)
	hooks[0](context.Background(), proxy.RequestCompletedEvent{
		RequestID:          "req-1",
		RouteKind:          models.RouteKindChat,
		IncomingModel:      "dummy",
		Lane:               "agentic",
		EndpointID:         "endpoint-id",
		EndpointName:       "Dummy",
		ProviderID:         "provider-id",
		UpstreamModel:      "dummy",
		StatusCode:         http.StatusOK,
		State:              "completed",
		WaitMS:             100,
		LatencyMS:          2000,
		ActualInputTokens:  12,
		ActualOutputTokens: 34,
		ActualTotalTokens:  46,
		ActualCostMicros:   560000,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		TrustedContext: proxy.TrustedRequestContext{
			Metadata: map[string]string{
				"actor":       "Test User",
				"product_key": "model_relay",
			},
			Limits: proxy.TrustedRequestLimits{
				RemainingDailyRequests: ptrInt64(7),
			},
		},
	})

	if got.RequestID != "req-1" || got.RouteKind != RouteKindChat || got.StatusCode != http.StatusOK {
		t.Fatalf("unexpected completion event: %#v", got)
	}
	if got.Metadata["actor"] != "Test User" || got.Metadata["product_key"] != "model_relay" {
		t.Fatalf("unexpected trusted metadata: %#v", got.Metadata)
	}
	if got.ActualTotalTokens != 46 || got.ActualCostMicros != 560000 || !got.FinishedAt.Equal(finishedAt) {
		t.Fatalf("unexpected usage fields: %#v", got)
	}
}

func TestRequestRejectedHookAdapterUsesSafeDTO(t *testing.T) {
	var got RequestRejectedEvent
	hooks := schedulerRequestRejectedHooks([]RequestRejectedHook{
		func(_ context.Context, event RequestRejectedEvent) {
			got = event
		},
	})
	if len(hooks) != 1 {
		t.Fatalf("expected one rejected hook adapter, got %d", len(hooks))
	}
	hooks[0](context.Background(), scheduler.RequestRejectedEvent{
		RequestID:             "rejected-request",
		RouteKind:             models.RouteKindChat,
		IncomingModel:         "agentic",
		Lane:                  "agentic",
		StatusCode:            http.StatusTooManyRequests,
		State:                 "failed",
		Reason:                "all candidates exceeded wait budget",
		EstimatedInputTokens:  12,
		EstimatedOutputTokens: 8,
		Metadata:              map[string]string{"organization_uuid": "org", "user_uuid": "user"},
	})
	if got.RequestID != "rejected-request" || got.StatusCode != http.StatusTooManyRequests || got.Reason == "" {
		t.Fatalf("unexpected adapted rejected event: %#v", got)
	}
	if got.Metadata["organization_uuid"] != "org" {
		t.Fatalf("expected metadata to be copied, got %#v", got.Metadata)
	}
}

func TestRequestContextHookAdapterUsesSafeHeadersAndDTO(t *testing.T) {
	var got RequestContextInput
	hooks := proxyRequestContextHooks([]RequestContextHook{
		func(_ context.Context, input RequestContextInput) (TrustedRequestContext, error) {
			got = input
			return TrustedRequestContext{
				Metadata: map[string]string{
					"actor":       "Test User",
					"product_key": "model_relay",
				},
				Limits: TrustedRequestLimits{
					RemainingDailyRequests: ptrInt64(5),
				},
			}, nil
		},
	})
	if len(hooks) != 1 {
		t.Fatalf("expected one context hook, got %d", len(hooks))
	}
	trusted, err := hooks[0](context.Background(), proxy.RequestContextInput{
		RequestID: "internal-request-id",
		RouteKind: models.RouteKindChat,
		Headers:   http.Header{"X-Extension-Signature": []string{"v1=signature"}},
	})
	if err != nil {
		t.Fatalf("context hook: %v", err)
	}
	if got.RequestID != "internal-request-id" || got.Headers.Get("X-Extension-Signature") != "v1=signature" {
		t.Fatalf("unexpected context input: %#v", got)
	}
	if trusted.Metadata["actor"] != "Test User" || trusted.Metadata["product_key"] != "model_relay" {
		t.Fatalf("unexpected trusted metadata: %#v", trusted.Metadata)
	}
	if trusted.Limits.RemainingDailyRequests == nil || *trusted.Limits.RemainingDailyRequests != 5 {
		t.Fatalf("unexpected trusted limits: %#v", trusted.Limits)
	}
}

func TestRequestContextHookAdapterPreservesRejectionStatus(t *testing.T) {
	hooks := proxyRequestContextHooks([]RequestContextHook{
		func(context.Context, RequestContextInput) (TrustedRequestContext, error) {
			return TrustedRequestContext{}, RequestContextRejection{
				Status:  http.StatusTooManyRequests,
				Message: "team member limit exhausted",
			}
		},
	})
	if len(hooks) != 1 {
		t.Fatalf("expected one context hook, got %d", len(hooks))
	}
	_, err := hooks[0](context.Background(), proxy.RequestContextInput{})
	if err == nil {
		t.Fatal("expected rejection")
	}
	var rejection proxy.RequestContextRejection
	if !errors.As(err, &rejection) || rejection.Status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 proxy rejection, got %T %[1]v", err)
	}
}

func ptrInt64(value int64) *int64 {
	return &value
}

type staticRouter struct {
	candidates []router.Candidate
}

func (r staticRouter) SelectCandidates(context.Context, router.RequestMeta) ([]router.Candidate, error) {
	return append([]router.Candidate(nil), r.candidates...), nil
}

func newTestApp(t *testing.T, ctx context.Context, extensions []Extension) *App {
	t.Helper()
	return newTestAppWithAPIToken(t, ctx, extensions, "test-inference-token-with-enough-length")
}

func newTestAppWithAPIToken(t *testing.T, ctx context.Context, extensions []Extension, apiToken string) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RELAY_DB_PATH", filepath.Join(dir, "relay.db"))
	t.Setenv("RELAY_TEMP_DIR", filepath.Join(dir, "tmp"))
	t.Setenv("RELAY_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("RELAY_ADMIN_TOKEN", "test-admin-token-with-enough-length")
	t.Setenv("RELAY_API_TOKEN", apiToken)
	t.Setenv("RELAY_MASTER_KEY", base64.StdEncoding.EncodeToString([]byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24,
		25, 26, 27, 28, 29, 30, 31, 32,
	}))
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	app, err := New(ctx, Options{EnvFile: envPath, Extensions: extensions})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(app.Stop)
	return app
}
