package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/body"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/anchorshell/relay/internal/transport"
	"github.com/labstack/echo/v5"
)

// Keep retired spellings in negative tests only: neither prefix is an API alias.
var removedControlValues = map[string]string{
	"Lane": "not-the-requested-group", "Endpoint": "not-an-endpoint-uuid",
	"Max-Wait-Ms": "not-an-integer", "Allow-Fallback": "true", "Priority": "999999",
	"Estimated-Input-Tokens": "1", "Estimated-Output-Tokens": "1", "Max-Cost-Micros": "1",
}

func TestInferenceIgnoresRemovedClientControls(t *testing.T) {
	e, st, endpoint, _ := newControlTestGateway(t)
	for _, route := range []string{"chat/completions", "responses", "embeddings"} {
		t.Run(route, func(t *testing.T) {
			payload := map[string]any{"model": "agentic", "messages": []any{map[string]any{"role": "user", "content": strings.Repeat("actual input ", 40)}}, "input": strings.Repeat("actual input ", 40)}
			baseline := sendControlTestRequest(t, e, st, route, payload, nil, "", http.StatusOK)
			if controlTestEndpoint(baseline) != endpoint.UUID || baseline.Priority != 0 || baseline.EstimatedInputTokens <= 1 || baseline.EstimatedOutputTokens <= 1 {
				t.Fatalf("unexpected baseline: %+v", baseline)
			}
			assertUnchanged := func(t *testing.T, payload map[string]any, headers http.Header, query string) {
				t.Helper()
				got := sendControlTestRequest(t, e, st, route, payload, headers, query, http.StatusOK)
				if controlTestEndpoint(got) != controlTestEndpoint(baseline) || got.Priority != baseline.Priority || got.EstimatedInputTokens != baseline.EstimatedInputTokens || got.EstimatedOutputTokens != baseline.EstimatedOutputTokens || got.EstimatedCostMicros != baseline.EstimatedCostMicros || got.AppliedOverridesJSON != baseline.AppliedOverridesJSON {
					t.Fatalf("client controls changed routing/policy/accounting: baseline=%+v got=%+v", baseline, got)
				}
			}
			for _, prefix := range []string{"X-Relay-", "X-Bouncer-"} {
				for name, value := range removedControlValues {
					t.Run(prefix+name, func(t *testing.T) {
						headers := http.Header{}
						headers.Set(prefix+name, value)
						assertUnchanged(t, payload, headers, "")
					})
				}
			}
			for name, value := range map[string]any{
				"max_wait_ms": 999999, "allow_fallback": true, "priority": 999999,
				"estimated_input_tokens": 1, "estimated_output_tokens": 1, "max_cost_micros": 1,
				"lane": "other", "route": "other", "endpoint": "missing", "endpoint_id": "missing",
				"override_endpoint": "missing", "override_endpoint_id": "missing",
			} {
				t.Run(name, func(t *testing.T) {
					for _, nested := range []bool{false, true} {
						withControl := cloneJSONMap(payload)
						if nested {
							withControl["relay"] = map[string]any{name: value}
						} else {
							withControl[name] = value
						}
						assertUnchanged(t, withControl, nil, "")
					}
					encoded, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					query := url.Values{name: {string(encoded)}, "relay." + name: {string(encoded)}}
					assertUnchanged(t, payload, nil, "?"+query.Encode())
				})
			}
		})
	}
}

func TestInferenceBodyModelSelectsGroupAndProviderModel(t *testing.T) {
	e, st, endpoint, _ := newControlTestGateway(t)
	for _, model := range []string{"agentic", "control-provider/control-model"} {
		t.Run(model, func(t *testing.T) {
			got := sendControlTestRequest(t, e, st, "chat/completions", map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}, nil, "", http.StatusOK)
			if controlTestEndpoint(got) != endpoint.UUID {
				t.Fatalf("body model selected %q", controlTestEndpoint(got))
			}
		})
	}
}

func TestInferenceRemovedControlsCannotOverrideStoredPolicies(t *testing.T) {
	for _, policy := range []string{"no-fallback", "fallback", "spend-budget", "token-budget"} {
		t.Run(policy, func(t *testing.T) {
			e, st, endpoint, group := newControlTestGateway(t)
			ctx := context.Background()
			wantStatus := http.StatusTooManyRequests
			if policy == "no-fallback" || policy == "fallback" {
				until := time.Now().Add(time.Hour)
				endpoint.CooldownUntil = &until
				if err := st.SaveEndpointState(ctx, endpoint); err != nil {
					t.Fatal(err)
				}
				second := models.Endpoint{ProviderID: endpoint.ProviderID, Name: "second", UpstreamModel: "second", RouteKind: models.RouteKindMulti, Enabled: true, HealthStatus: models.HealthHealthy}
				if err := st.Create(ctx, &second); err != nil {
					t.Fatal(err)
				}
				if err := st.Create(ctx, &models.LaneMembership{LaneID: group.ID, EndpointID: second.ID, ManualRank: 2, Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if policy == "fallback" {
					group.AllowFallback = true
					if err := st.Save(ctx, &group); err != nil {
						t.Fatal(err)
					}
					wantStatus = http.StatusOK
				}
			} else {
				metric := models.MetricSpend
				if policy == "token-budget" {
					metric = models.MetricTokens
				}
				if err := st.Create(ctx, &models.LimitPolicy{ScopeType: models.ScopeGlobal, Metric: metric, Period: models.PeriodDay, LimitValue: 1, Enabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			headers := http.Header{}
			for _, prefix := range []string{"X-Relay-", "X-Bouncer-"} {
				for name, value := range removedControlValues {
					headers.Set(prefix+name, value)
				}
				headers.Set(prefix+"Max-Wait-Ms", "9999999")
				headers.Set(prefix+"Max-Cost-Micros", "9999999")
				if policy == "fallback" {
					headers.Set(prefix+"Allow-Fallback", "false")
				}
			}
			got := sendControlTestRequest(t, e, st, "chat/completions", map[string]any{"model": "agentic", "messages": []any{map[string]any{"role": "user", "content": strings.Repeat("real input ", 40)}}}, headers, "", wantStatus)
			if policy == "fallback" && controlTestEndpoint(got) == endpoint.UUID {
				t.Fatal("configured fallback did not select the available model")
			}
		})
	}
}

func TestInferenceStandardOutputLimitsStillInformEstimates(t *testing.T) {
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		t.Run(key, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"content": strings.Repeat("input ", 100)}}, key: 3})
			in, out := (scheduler.HeuristicEstimator{}).Estimate(router.RequestMeta{}, raw, int64(len(raw)))
			if in <= 3 || out != 3 {
				t.Fatalf("standard output cap ignored: %d/%d", in, out)
			}
		})
	}
}

func newControlTestGateway(t *testing.T) (*echo.Echo, *store.Store, models.Endpoint, models.RoutingLane) {
	t.Helper()
	ctx := context.Background()
	st := testutil.NewStore(t)
	provider := models.Provider{Name: "control-provider", BaseURL: "https://provider.example/v1", AuthMode: "none", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{ProviderID: provider.ID, Name: "control-model", UpstreamModel: "control-model", RouteKind: models.RouteKindMulti, Enabled: true, HealthStatus: models.HealthHealthy}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}
	var group models.RoutingLane
	if err := st.DB().Where("name = ?", "agentic").First(&group).Error; err != nil {
		t.Fatal(err)
	}
	group.DefaultMaxWaitMS = 20
	group.AllowFallback = false
	if err := st.Save(ctx, &group); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.LaneMembership{LaneID: group.ID, EndpointID: endpoint.ID, ManualRank: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, &models.PricingPolicy{EndpointID: endpoint.ID, FlatRequestCostMicros: 100}); err != nil {
		t.Fatal(err)
	}
	hub := telemetry.NewHub()
	t.Cleanup(hub.Close)
	tracker := limits.NewTracker(st)
	sch := scheduler.New(st, limits.NewResolver(st, tracker), tracker, hub, nil, 20, 1000, 300000)
	sch.Start()
	t.Cleanup(sch.Stop)
	server := NewServer(st, router.NewWaterfallStrategy(st), sch, body.NewStore(t.TempDir(), 1<<20, 1<<20), &transport.UpstreamTransport{Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		for name := range req.Header {
			if strings.HasPrefix(name, "X-Relay-") || strings.HasPrefix(name, "X-Bouncer-") {
				t.Errorf("request control reached provider: %s", name)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))}, nil
	})}, hub)
	e := echo.New()
	server.Register(e.Group("/v1"))
	return e, st, endpoint, group
}

func sendControlTestRequest(t *testing.T, e *echo.Echo, st *store.Store, route string, payload map[string]any, headers http.Header, query string, wantStatus int) models.RequestLog {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/"+route+query, strings.NewReader(string(raw))).WithContext(ctx)
	if headers != nil {
		req.Header = headers.Clone()
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var log models.RequestLog
	if err := st.DB().Order("id DESC").First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if wantStatus == http.StatusOK && rec.Header().Get("X-Relay-Selected-Endpoint-Id") != controlTestEndpoint(log) {
		t.Fatal("selection response metadata changed")
	}
	return log
}

func controlTestEndpoint(log models.RequestLog) string {
	if log.EndpointUUID == nil {
		return ""
	}
	return *log.EndpointUUID
}
