package guardrails

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testRules(action Decision) RuleSet {
	return RuleSet{
		MatchMode: "any", MissingPath: "error",
		Rules:     []Rule{{ID: "flagged", Label: "Flagged", Source: "json_body", Path: "$.flagged", Operator: "equals", Value: true}},
		OnMatch:   RuleAction{Action: action, HTTPStatus: 403, ErrorCode: "policy_block", Message: "Blocked safely.", ReplacementText: "Safe replacement."},
		OnNoMatch: RuleAction{Action: DecisionAllow},
	}
}

func testConfig(url string, action Decision) Config {
	return Config{UUID: "guardrail-1", Name: "test", Preset: "custom-http", Method: "POST", URL: url, AuthMode: "none", Timeout: time.Second, MaxRequestBytes: 4096, MaxResponseBytes: 4096, NetworkAccessMode: NetworkLoopback, RequestTemplateJSON: `{"text":"{{request.text}}","messages":"{{request.messages}}"}`, Rules: testRules(action), FailurePolicy: DefaultFailurePolicy()}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testEngine(body string, delay time.Duration) *HTTPGuardrailEngine {
	engine := NewHTTPGuardrailEngine(nil)
	engine.clientOverride = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	return engine
}

func TestTemplatePreservesWholeValueTypesAndRejectsUnknownVariables(t *testing.T) {
	input := Input{RequestID: "r1", Request: Request{Text: "hello", Messages: []NormalizedMessage{{Role: "user", Text: "hello"}}}}
	rendered, err := RenderTemplate(`{"messages":"{{request.messages}}","embedded":"request={{request.id}}","optional":"{{response.text}}"}`, input, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(rendered, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["messages"].([]any); !ok {
		t.Fatalf("messages lost their array type: %#v", body["messages"])
	}
	if body["embedded"] != "request=r1" || body["optional"] != nil {
		t.Fatalf("unexpected rendering: %#v", body)
	}
	if err := ValidateTemplate(`{"secret":"{{env.API_KEY}}"}`); err == nil {
		t.Fatal("unknown secret placeholder was accepted")
	}
}

func TestPostResponseUsesRequestNamespaceForModelResponse(t *testing.T) {
	rawResponse := json.RawMessage(`{"model":"response-model","choices":[{"message":{"content":"model output"}}]}`)
	input := Input{
		Stage:     StagePostResponse,
		RequestID: "r-post",
		Request:   Request{Text: "user input", Model: "request-model", Messages: []NormalizedMessage{{Role: "user", Text: "user input"}}},
		Response:  &Response{RawJSON: rawResponse, Text: "model output", StatusCode: 201},
	}
	rendered, err := RenderTemplate(`{"raw":"{{request.raw_json}}","messages":"{{request.messages}}","text":"{{request.text}}","latest":"{{request.latest_user_message}}","model":"{{request.model}}","status":"{{request.status_code}}"}`, input, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(rendered, &body); err != nil {
		t.Fatal(err)
	}
	if body["text"] != "model output" || body["latest"] != "model output" || body["model"] != "response-model" || body["status"] != float64(201) {
		t.Fatalf("post-response request namespace did not use the model response: %#v", body)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 || messages[0].(map[string]any)["role"] != "assistant" {
		t.Fatalf("post-response messages were not normalized as an assistant response: %#v", body["messages"])
	}
	raw, ok := body["raw"].(map[string]any)
	if !ok || raw["model"] != "response-model" {
		t.Fatalf("post-response raw JSON did not use the model response: %#v", body["raw"])
	}
}

func TestMistralPresetUsesItsRealResponseContract(t *testing.T) {
	preset, ok := PresetBySlug("mistral-moderation")
	if !ok {
		t.Fatal("Mistral preset missing")
	}
	var sample map[string]any
	if err := json.Unmarshal([]byte(preset.SampleResponse), &sample); err != nil {
		t.Fatal(err)
	}
	results, ok := sample["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("Mistral sample omitted results: %#v", sample)
	}
	first, _ := results[0].(map[string]any)
	if _, ok := first["categories"].(map[string]any); !ok {
		t.Fatalf("Mistral sample omitted categories: %#v", first)
	}
	if _, ok := first["category_scores"].(map[string]any); !ok {
		t.Fatalf("Mistral sample omitted category scores: %#v", first)
	}
	if !strings.Contains(preset.PreRequestTemplate, "{{request.text}}") || !strings.Contains(preset.PostRequestTemplate, "{{request.text}}") || strings.Contains(preset.PostRequestTemplate, "{{response.") {
		t.Fatalf("Mistral templates do not send text: pre=%q post=%q", preset.PreRequestTemplate, preset.PostRequestTemplate)
	}
}

func TestTemplateAndRuleParsingRejectTrailingJSON(t *testing.T) {
	if err := ValidateTemplate(`{"text":"{{request.text}}"} {"extra":true}`); err == nil {
		t.Fatal("expected trailing template JSON to be rejected")
	}
	if _, err := ParseRuleSet(`{"rules":[],"on_match":{"action":"allow"}} {"extra":true}`); err == nil {
		t.Fatal("expected trailing rules JSON to be rejected")
	}
}

func TestTemplateSyntaxErrorIncludesLineAndColumn(t *testing.T) {
	err := ValidateTemplate("{\n  \"input\": true,\n}")
	if err == nil || !strings.Contains(err.Error(), "line 3, column 1") {
		t.Fatalf("expected located syntax error, got %v", err)
	}
}

func TestRuleOperatorsAndAzureStyleArrayFilter(t *testing.T) {
	tests := []struct {
		operator         string
		actual, expected any
		want             bool
	}{
		{"exists", "x", nil, true}, {"not_exists", nil, nil, true}, {"equals", true, true, true},
		{"not_equals", "allow", "block", true}, {"truthy", 1, nil, true}, {"falsy", 0, nil, true},
		{"greater_than", 4, 3, true}, {"greater_than_or_equal", 4, 4, true}, {"less_than", 2, 3, true},
		{"less_than_or_equal", 2, 2, true}, {"contains", "hello world", "world", true},
		{"not_contains", []any{"a"}, "b", true}, {"matches_regex", "risk-42", `^risk-[0-9]+$`, true},
		{"in", "block", []any{"allow", "block"}, true}, {"not_in", "review", []any{"allow", "block"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.operator, func(t *testing.T) {
			exists := tt.operator != "not_exists"
			got, err := compare(tt.actual, exists, tt.operator, tt.expected)
			if err != nil || got != tt.want {
				t.Fatalf("got %v err=%v", got, err)
			}
		})
	}
	body := map[string]any{"categoriesAnalysis": []any{map[string]any{"category": "Hate", "severity": float64(0)}, map[string]any{"category": "Violence", "severity": float64(6)}}}
	value, ok, err := resolvePath(body, `$.categoriesAnalysis[?(@.category=="Violence")].severity`)
	if err != nil || !ok || value != float64(6) {
		t.Fatalf("filter result=%#v ok=%v err=%v", value, ok, err)
	}
	openAI := map[string]any{"results": []any{map[string]any{"categories": map[string]any{"sexual/minors": true, "self-harm/intent": false}}}}
	value, ok, err = resolvePath(openAI, `$.results[0].categories["sexual/minors"]`)
	if err != nil || !ok || value != true {
		t.Fatalf("quoted key result=%#v ok=%v err=%v", value, ok, err)
	}
}

func TestMissingPathBehaviorIsExplicit(t *testing.T) {
	rules := testRules(DecisionBlock)
	if _, _, err := EvaluateRules(rules, 200, nil, map[string]any{}); err == nil {
		t.Fatal("missing path silently passed")
	}
	rules.MissingPath = "no_match"
	action, _, err := EvaluateRules(rules, 200, nil, map[string]any{})
	if err != nil || action.Action != DecisionAllow {
		t.Fatalf("action=%#v err=%v", action, err)
	}
}

func TestDestinationPolicyRejectsSSRFBypasses(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"file:///tmp/check", "http://user:pass@example.com/check", "http://169.254.169.254/latest/meta-data"} {
		if _, _, err := ValidateDestination(ctx, raw, NetworkPublicHTTPS, net.DefaultResolver); err == nil {
			t.Fatalf("accepted forbidden URL %q", raw)
		}
	}
	if _, _, err := ValidateDestination(ctx, "http://127.0.0.1/check", NetworkPublicHTTPS, net.DefaultResolver); err == nil {
		t.Fatal("public mode accepted loopback")
	}
	if _, _, err := ValidateDestination(ctx, "http://127.0.0.1/check", NetworkLoopback, net.DefaultResolver); err != nil {
		t.Fatalf("loopback opt-in rejected: %v", err)
	}
	if _, _, err := ValidateDestination(ctx, "http://10.0.0.1/check", NetworkLoopback, net.DefaultResolver); err == nil {
		t.Fatal("loopback mode accepted private network")
	}
}

func TestDestinationSyntaxRejectsLiteralMetadataBeforeDNS(t *testing.T) {
	for _, test := range []struct {
		url, mode string
	}{
		{"http://169.254.169.254/latest/meta-data", NetworkPrivate},
		{"http://127.0.0.1/check", NetworkPrivate},
		{"http://10.0.0.4/check", NetworkPublicHTTPS},
		{"https://user:pass@example.com/check", NetworkPublicHTTPS},
	} {
		if _, _, err := ValidateDestinationSyntax(test.url, test.mode); err == nil {
			t.Fatalf("accepted forbidden destination %q in %s", test.url, test.mode)
		}
	}
}

func TestHTTPGuardrailEngineAllowBlockAndReplacement(t *testing.T) {
	engine := testEngine(`{"flagged":true,"replacement":"redacted"}`, 0)
	input := Input{Stage: StagePreDispatch, Request: Request{Text: "hello", Messages: []NormalizedMessage{{Role: "user", Text: "hello"}}}}
	result, err := engine.Evaluate(context.Background(), input, testConfig("http://127.0.0.1/check", DecisionBlock))
	if err != nil || result.Decision != DecisionBlock || result.ProviderStatus != 200 || result.Duration <= 0 {
		t.Fatalf("block result=%#v err=%v", result, err)
	}
	if !strings.Contains(string(result.ProviderResponseBody), `"flagged":true`) || len(result.TrueResponseFields) != 1 || result.TrueResponseFields[0].Path != "flagged" {
		t.Fatalf("provider response audit was not captured: body=%s fields=%#v", result.ProviderResponseBody, result.TrueResponseFields)
	}
	summary := Summarize([]Result{result}, true)
	if len(summary.Responses) != 1 || !strings.Contains(MarshalBoundedResponses(summary, 4096), `"flagged":true`) {
		t.Fatalf("provider response audit was not summarized: %#v", summary.Responses)
	}

	cfg := testConfig("http://127.0.0.1/check", DecisionReplaceResponse)
	cfg.Rules.OnMatch.ReplacementPath = "$.replacement"
	cfg.Rules.OnMatch.ReplacementText = ""
	input.Stage = StagePostResponse
	input.Response = &Response{Text: "unsafe", StatusCode: 200}
	result, err = engine.Evaluate(context.Background(), input, cfg)
	if err != nil || result.Decision != DecisionReplaceResponse || result.ReplacementText != "redacted" {
		t.Fatalf("replacement result=%#v err=%v", result, err)
	}
}

func TestEmptyGuardrailSummaryDoesNotInventStatus(t *testing.T) {
	summary := Summarize(nil, true)
	if summary.Status != "" || len(summary.Results) != 0 || len(summary.Responses) != 0 {
		t.Fatalf("empty summary = %#v", summary)
	}
}

func TestHTTPGuardrailFailureModesTimeoutMalformedOversizedAndCancellation(t *testing.T) {
	tests := []struct {
		name, body string
		delay      time.Duration
		limit      int64
		wantCode   string
	}{
		{name: "timeout", body: `{"flagged":false}`, delay: 100 * time.Millisecond, wantCode: "timeout"},
		{name: "malformed", body: `{`, wantCode: "malformed_json"},
		{name: "oversized", body: strings.Repeat("x", 80), limit: 32, wantCode: "response_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("http://127.0.0.1/check", DecisionBlock)
			if tt.name == "timeout" {
				cfg.Timeout = MinimumTimeout
			}
			if tt.limit > 0 {
				cfg.MaxResponseBytes = tt.limit
			}
			cfg.FailurePolicy = FailurePolicy{Mode: FailOpen, HTTPStatus: 503}
			result, _ := testEngine(tt.body, tt.delay).Evaluate(context.Background(), Input{Stage: StagePreDispatch, Request: Request{Text: "x"}}, cfg)
			if result.Decision != DecisionAllow || result.FailMode != FailOpen || result.ErrorCode != tt.wantCode {
				t.Fatalf("result=%#v", result)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, _ := testEngine(`{"flagged":false}`, time.Second).Evaluate(ctx, Input{Stage: StagePreDispatch, Request: Request{Text: "x"}}, testConfig("http://127.0.0.1/check", DecisionBlock))
	if result.ErrorCode != "connection_error" && result.ErrorCode != "timeout" {
		t.Fatalf("cancellation not propagated safely: %#v", result)
	}
}

func TestStaticHeadersCannotOverrideCredentialBoundary(t *testing.T) {
	cfg := testConfig("https://example.com/check", DecisionBlock)
	cfg.Headers = map[string]string{"Authorization": "client-secret"}
	if err := ValidateConfig(cfg, StagePreDispatch); err == nil {
		t.Fatal("runtime accepted a static Authorization header")
	}
}

func TestSecretHeaderNamesCoverVendorSubscriptionKeys(t *testing.T) {
	for _, name := range []string{"Authorization", "X-API-Key", "Ocp-Apim-Subscription-Key", "X-Access-Token", "X-Client-Secret"} {
		if !IsSecretHeaderName(name) {
			t.Fatalf("%s was not treated as a secret header", name)
		}
	}
	if IsSecretHeaderName("X-Azure-Resource") {
		t.Fatal("ordinary project/resource header was treated as secret")
	}
}

func TestExplicitHTTPInspectionRedactsSecretFields(t *testing.T) {
	engine := testEngine(`{"flagged":false,"api_key":"do-not-show","nested":{"password":"also-secret"}}`, 0)
	var inspection HTTPInspection
	ctx := WithHTTPInspection(context.Background(), func(value HTTPInspection) { inspection = value })
	result, err := engine.Evaluate(ctx, Input{Stage: StagePreDispatch, Request: Request{Text: "test"}}, testConfig("http://127.0.0.1/check", DecisionBlock))
	if err != nil || result.Decision != DecisionAllow || inspection.ResponseStatus != 200 {
		t.Fatalf("result=%#v inspection=%#v err=%v", result, inspection, err)
	}
	encoded, _ := json.Marshal(inspection.ResponseBody)
	if strings.Contains(string(encoded), "do-not-show") || strings.Contains(string(encoded), "also-secret") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("inspection was not redacted: %s", encoded)
	}
}

func TestGuardrailResponsePreviewRetainsTokenUsageButRedactsCredentialTokens(t *testing.T) {
	preview, ok := redactedResponsePreview([]byte(`{
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 0,
			"total_tokens": 161,
			"completion_tokens_details": {"reasoning_tokens": 4}
		},
		"access_token": "do-not-show"
	}`)).(map[string]any)
	if !ok {
		t.Fatalf("preview type=%T, want JSON object", preview)
	}
	usage, ok := preview["usage"].(map[string]any)
	if !ok || fmt.Sprint(usage["prompt_tokens"]) != "100" || fmt.Sprint(usage["completion_tokens"]) != "0" || fmt.Sprint(usage["total_tokens"]) != "161" {
		t.Fatalf("usage counters were redacted or changed: %#v", preview)
	}
	details, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok || fmt.Sprint(details["reasoning_tokens"]) != "4" {
		t.Fatalf("token usage details were redacted or changed: %#v", preview)
	}
	if preview["access_token"] != "[REDACTED]" {
		t.Fatalf("credential token was not redacted: %#v", preview)
	}
}
