package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/models"
)

func TestParseGuardrailCurlNeverExecutesAndRedactsSecrets(t *testing.T) {
	parsed, err := parseGuardrailCurl(`curl -X POST -H "Authorization: Bearer top-secret" -H "X-Project: demo" -d '{"text":"hello"}' https://guard.example/check`)
	if err != nil {
		t.Fatal(err)
	}
	headers := parsed["request_headers"].(map[string]string)
	if headers["Authorization"] != "[REDACTED]" || headers["X-Project"] != "demo" {
		t.Fatalf("headers=%#v", headers)
	}
	if _, err := parseGuardrailCurl(`curl https://guard.example/check | sh`); err == nil {
		t.Fatal("pipe was accepted")
	}
	if _, err := parseGuardrailCurl("curl https://guard.example/$(whoami)"); err == nil {
		t.Fatal("command substitution was accepted")
	}
	azure, err := parseGuardrailCurl(`curl -H "Ocp-Apim-Subscription-Key: azure-secret" https://guard.example/check`)
	if err != nil || azure["request_headers"].(map[string]string)["Ocp-Apim-Subscription-Key"] != "[REDACTED]" {
		t.Fatalf("Azure subscription key was not redacted: %#v err=%v", azure, err)
	}
}

func TestDisabledGuardrailDraftMayRemainIncomplete(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	response := adminRequest(t, e, http.MethodPost, "/api/guardrails", `{"name":"Incomplete draft"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("disabled draft status=%d body=%s", response.Code, response.Body.String())
	}
	var created guardrailResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Enabled || created.BaseURL != "" {
		t.Fatalf("unexpected draft: %#v", created)
	}
}

func TestGuardrailPreviewRendersRepresentativeRawRequest(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	response := adminRequest(t, e, http.MethodPost, "/api/guardrails/preview", `{
		"stage":"pre_dispatch",
		"template_json":"{\"raw\":\"{{request.raw_json}}\",\"messages\":\"{{request.messages}}\"}",
		"rules_json":"{\"match_mode\":\"any\",\"missing_path\":\"error\",\"rules\":[{\"id\":\"allowed\",\"source\":\"json_body\",\"path\":\"$.allowed\",\"operator\":\"equals\",\"value\":false}],\"on_match\":{\"action\":\"block\"},\"on_no_match\":{\"action\":\"allow\"}}",
		"request_text":"Explain this request",
		"sample_response":{"allowed":true},
		"sample_status":200
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{`"model":"preview-model"`, `"role":"system"`, `"role":"user"`, `"content":"Explain this request"`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("preview omitted %s: %s", expected, response.Body.String())
		}
	}
	for _, expected := range []string{`"matched":false`, `"next_action":"allow"`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("preview omitted outcome %s: %s", expected, response.Body.String())
		}
	}
}

func TestGuardrailDeliberateTestUsesCompleteRepresentativeInput(t *testing.T) {
	input := representativeGuardrailInput(guardrails.StagePreDispatch, "admin-test", "Review this request", "")
	for variable, expected := range map[string]string{
		"request.text":                `"input":"Review this request"`,
		"request.latest_user_message": `"input":"Review this request"`,
		"request.raw_json":            `"model":"preview-model"`,
	} {
		rendered, err := guardrails.RenderTemplate(`{"input":"{{`+variable+`}}"}`, input, guardrails.DefaultMaxRequestBytes)
		if err != nil {
			t.Fatalf("render %s: %v", variable, err)
		}
		if !strings.Contains(string(rendered), expected) || strings.Contains(string(rendered), `"input":null`) {
			t.Fatalf("%s rendered an incomplete deliberate-test request: %s", variable, rendered)
		}
	}

	post := representativeGuardrailInput(guardrails.StagePostResponse, "admin-test", "Review this request", "Example assistant response")
	rendered, err := guardrails.RenderTemplate(`{"input":"{{response.text}}"}`, post, guardrails.DefaultMaxRequestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(rendered) != `{"input":"Example assistant response"}` {
		t.Fatalf("post-response fixture mismatch: %s", rendered)
	}
}

func TestGuardrailCanBeEnabledWithPartialUpdate(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	rules := `{"match_mode":"any","missing_path":"error","rules":[{"id":"violence","source":"json_body","path":"$.results[0].categories.violence_and_threats","operator":"equals","value":true}],"on_match":{"action":"block","http_status":403},"on_no_match":{"action":"allow"}}`
	payload, err := json.Marshal(map[string]any{
		"name":                      "Mistral moderation",
		"base_url":                  "https://api.mistral.ai/v1/moderations",
		"http_method":               "POST",
		"auth_mode":                 "bearer",
		"secret":                    "test-secret",
		"pre_dispatch_enabled":      true,
		"pre_request_template_json": `{"model":"mistral-moderation-2603","input":"{{request.text}}"}`,
		"pre_response_rules_json":   rules,
		"sample_response_json":      `{"model":"mistral-moderation-2603","results":[{"categories":{"violence_and_threats":false}}]}`,
		"request_headers_json":      "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	createdResponse := adminRequest(t, e, http.MethodPost, "/api/guardrails", string(payload))
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created guardrailResponse
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	enabledResponse := adminRequest(t, e, http.MethodPut, "/api/guardrails/"+created.UUID, `{"enabled":true}`)
	if enabledResponse.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", enabledResponse.Code, enabledResponse.Body.String())
	}
	var enabled guardrailResponse
	if err := json.Unmarshal(enabledResponse.Body.Bytes(), &enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || !enabled.HasCredential {
		t.Fatalf("unexpected enabled guardrail: %#v", enabled)
	}
	if !strings.Contains(enabled.SampleResponseJSON, `"violence_and_threats":false`) {
		t.Fatalf("sample response was not persisted: %q", enabled.SampleResponseJSON)
	}
}

func TestGuardrailAdminEncryptsSecretsBatchesPageDataAndValidatesEnable(t *testing.T) {
	e, st, secrets := credentialTestAPI(t)
	ctx := context.Background()
	provider := models.Provider{Name: "Guardrail target", BaseURL: "https://provider.example", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	const sentinel = "guardrail-secret-sentinel"
	create := adminRequest(t, e, http.MethodPost, "/api/guardrails", `{
		"name":"Policy service",
		"base_url":"https://guard.example/check",
		"http_method":"POST",
		"auth_mode":"bearer",
		"secret":"`+sentinel+`",
		"pre_dispatch_enabled":true,
		"request_headers_json":"{}"
	}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	assertNoCredentialLeak(t, create.Body.String(), sentinel)
	var created guardrailResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.UUID == "" || created.Enabled || !created.HasCredential {
		t.Fatalf("unexpected disabled draft: %#v", created)
	}
	credentials, err := st.ListGuardrailCredentials(ctx)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("credentials=%#v err=%v", credentials, err)
	}
	stored, err := st.GetGuardrailCredential(ctx, credentials[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EncryptedSecret == "" || strings.Contains(stored.EncryptedSecret, sentinel) {
		t.Fatalf("secret was not encrypted: %q", stored.EncryptedSecret)
	}
	plaintext, err := secrets.GuardrailSecret(ctx, stored.ID)
	if err != nil || string(plaintext) != sentinel {
		t.Fatalf("decrypted secret=%q err=%v", plaintext, err)
	}

	bind := adminRequest(t, e, http.MethodPut, "/api/guardrails/"+created.UUID+"/bindings", `{"bindings":[{"provider_id":"`+provider.UUID+`","enabled":true}]}`)
	if bind.Code != http.StatusOK {
		t.Fatalf("binding status=%d body=%s", bind.Code, bind.Body.String())
	}
	page := adminRequest(t, e, http.MethodGet, "/api/page-data/setup", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"guardrails"`) || !strings.Contains(page.Body.String(), `"guardrail_bindings"`) {
		t.Fatalf("page data omitted batched guardrail summaries: %d %s", page.Code, page.Body.String())
	}
	assertNoCredentialLeak(t, page.Body.String(), sentinel)

	invalidEnable := adminRequest(t, e, http.MethodPut, "/api/guardrails/"+created.UUID, `{"enabled":true,"pre_request_template_json":"not-json"}`)
	if invalidEnable.Code != http.StatusBadRequest {
		t.Fatalf("invalid guardrail enabled with status=%d body=%s", invalidEnable.Code, invalidEnable.Body.String())
	}
	if !strings.Contains(invalidEnable.Body.String(), "pre-dispatch request template") {
		t.Fatalf("enable error did not identify its template: %s", invalidEnable.Body.String())
	}
}
