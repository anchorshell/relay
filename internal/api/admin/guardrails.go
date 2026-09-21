package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/models"
)

type guardrailMutation struct {
	models.Guardrail
	Secret string `json:"secret"`
}

type guardrailResponse struct {
	models.Guardrail
	HasCredential bool `json:"has_credential"`
	BindingCount  int  `json:"binding_count"`
}

func (a *API) guardrailPresets(c *echo.Context) error {
	return c.JSON(http.StatusOK, guardrails.Presets())
}

func (a *API) listGuardrails(c *echo.Context) error {
	items, err := a.store.ListGuardrails(c.Request().Context())
	if err != nil {
		return err
	}
	bindings, err := a.store.ListGuardrailBindings(c.Request().Context(), 0)
	if err != nil {
		return err
	}
	counts := map[uint]int{}
	for _, binding := range bindings {
		counts[binding.GuardrailID]++
	}
	response := make([]guardrailResponse, 0, len(items))
	for _, item := range items {
		response = append(response, guardrailResponse{Guardrail: item, HasCredential: item.CredentialID != nil, BindingCount: counts[item.ID]})
	}
	return c.JSON(http.StatusOK, response)
}

func (a *API) listAllGuardrailBindings(c *echo.Context) error {
	bindings, err := a.store.ListGuardrailBindings(c.Request().Context(), 0)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bindings)
}

func (a *API) getGuardrail(c *echo.Context) error {
	var item models.Guardrail
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	bindings, err := a.store.ListGuardrailBindings(c.Request().Context(), item.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"guardrail": guardrailResponse{Guardrail: item, HasCredential: item.CredentialID != nil, BindingCount: len(bindings)}, "bindings": bindings})
}

func (a *API) createGuardrail(c *echo.Context) error {
	var payload guardrailMutation
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid guardrail configuration")
	}
	payload.ID, payload.UUID = 0, ""
	payload.Enabled = false
	applyGuardrailDefaults(&payload.Guardrail)
	if err := validateGuardrailModel(payload.Guardrail, false); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	credential, err := a.createGuardrailCredential(c, payload.Name, payload.Secret)
	zeroBytes([]byte(payload.Secret))
	payload.Secret = ""
	if err != nil {
		return err
	}
	if credential != nil {
		payload.CredentialID = &credential.ID
		payload.CredentialUUID = &credential.UUID
	}
	if err := a.store.Create(c.Request().Context(), &payload.Guardrail); err != nil {
		if credential != nil {
			_ = a.store.DeleteByID(c.Request().Context(), &models.GuardrailCredential{}, credential.ID)
		}
		return catalogMutationError(&payload.Guardrail, err)
	}
	return c.JSON(http.StatusCreated, guardrailResponse{Guardrail: payload.Guardrail, HasCredential: credential != nil})
}

func (a *API) updateGuardrail(c *echo.Context) error {
	var previous models.Guardrail
	if err := a.store.FindByUUID(c.Request().Context(), &previous, c.Param("id")); err != nil {
		return err
	}
	payload := guardrailMutation{Guardrail: previous}
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid guardrail configuration")
	}
	payload.ID, payload.UUID, payload.CreatedAt = previous.ID, previous.UUID, previous.CreatedAt
	applyGuardrailDefaults(&payload.Guardrail)
	if err := validateGuardrailModel(payload.Guardrail, payload.Enabled); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(payload.Secret) != "" {
		credentialID := uint(0)
		if previous.CredentialID != nil {
			credentialID = *previous.CredentialID
		}
		credential, err := a.upsertGuardrailCredential(c, credentialID, payload.Name, payload.Secret)
		zeroBytes([]byte(payload.Secret))
		payload.Secret = ""
		if err != nil {
			return err
		}
		payload.CredentialID, payload.CredentialUUID = &credential.ID, &credential.UUID
	}
	if payload.AuthMode != "none" && payload.CredentialID == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "an encrypted guardrail credential is required for this auth mode")
	}
	if err := a.store.Save(c.Request().Context(), &payload.Guardrail); err != nil {
		return catalogMutationError(&payload.Guardrail, err)
	}
	return c.JSON(http.StatusOK, guardrailResponse{Guardrail: payload.Guardrail, HasCredential: payload.CredentialID != nil})
}

func (a *API) deleteGuardrail(c *echo.Context) error {
	var item models.Guardrail
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	if err := a.store.DeleteGuardrailCascade(c.Request().Context(), item.ID); err != nil {
		return err
	}
	if item.CredentialID != nil {
		_ = a.store.DeleteGuardrailCredentialIfUnused(c.Request().Context(), *item.CredentialID)
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) listGuardrailBindings(c *echo.Context) error {
	var item models.Guardrail
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	bindings, err := a.store.ListGuardrailBindings(c.Request().Context(), item.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bindings)
}

func (a *API) replaceGuardrailBindings(c *echo.Context) error {
	var item models.Guardrail
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	var payload struct {
		Bindings []models.GuardrailBinding `json:"bindings"`
	}
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid guardrail bindings")
	}
	if len(payload.Bindings) > 500 {
		return echo.NewHTTPError(http.StatusBadRequest, "too many guardrail bindings")
	}
	if err := a.store.ReplaceGuardrailBindings(c.Request().Context(), item, payload.Bindings); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	bindings, err := a.store.ListGuardrailBindings(c.Request().Context(), item.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bindings)
}

func (a *API) effectiveGuardrails(c *echo.Context) error {
	resolve := func(name string, model any) (uint, error) {
		raw := strings.TrimSpace(c.QueryParam(name))
		if raw == "" {
			return 0, nil
		}
		if err := a.store.FindByUUID(c.Request().Context(), model, raw); err != nil {
			return 0, err
		}
		switch typed := model.(type) {
		case *models.RoutingLane:
			return typed.ID, nil
		case *models.Provider:
			return typed.ID, nil
		case *models.Endpoint:
			return typed.ID, nil
		}
		return 0, nil
	}
	laneID, err := resolve("routing_lane_id", &models.RoutingLane{})
	if err != nil {
		return err
	}
	providerID, err := resolve("provider_id", &models.Provider{})
	if err != nil {
		return err
	}
	endpointID, err := resolve("endpoint_id", &models.Endpoint{})
	if err != nil {
		return err
	}
	items, err := a.store.EffectiveGuardrails(c.Request().Context(), laneID, providerID, endpointID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

func applyGuardrailDefaults(item *models.Guardrail) {
	item.Name = strings.TrimSpace(item.Name)
	item.HTTPMethod = strings.ToUpper(strings.TrimSpace(item.HTTPMethod))
	if item.HTTPMethod == "" {
		item.HTTPMethod = "POST"
	}
	item.AuthMode = strings.ToLower(strings.TrimSpace(item.AuthMode))
	if item.AuthMode == "" {
		item.AuthMode = "none"
	}
	item.NetworkAccessMode = strings.ToLower(strings.TrimSpace(item.NetworkAccessMode))
	if item.NetworkAccessMode == "" {
		item.NetworkAccessMode = guardrails.NetworkPublicHTTPS
	}
	if item.TimeoutMS == 0 {
		item.TimeoutMS = guardrails.DefaultTimeout.Milliseconds()
	}
	if item.MaxRequestBytes == 0 {
		item.MaxRequestBytes = guardrails.DefaultMaxRequestBytes
	}
	if item.MaxResponseBytes == 0 {
		item.MaxResponseBytes = guardrails.DefaultMaxResponseBytes
	}
	if strings.TrimSpace(item.PreFailurePolicyJSON) == "" {
		encoded, _ := json.Marshal(guardrails.DefaultFailurePolicy())
		item.PreFailurePolicyJSON = string(encoded)
	}
	if strings.TrimSpace(item.PostFailurePolicyJSON) == "" {
		encoded, _ := json.Marshal(guardrails.DefaultFailurePolicy())
		item.PostFailurePolicyJSON = string(encoded)
	}
	if strings.TrimSpace(item.SampleResponseJSON) == "" {
		item.SampleResponseJSON = `{"allowed":true}`
		if preset, ok := guardrails.PresetBySlug(item.PresetSlug); ok && strings.TrimSpace(preset.SampleResponse) != "" {
			item.SampleResponseJSON = preset.SampleResponse
		}
	}
}

func validateGuardrailModel(item models.Guardrail, enabling bool) error {
	if item.Name == "" {
		return errors.New("guardrail name is required")
	}
	if item.PreDispatchEnabled == item.PostResponseEnabled && !item.PreDispatchEnabled && enabling {
		return errors.New("at least one guardrail stage is required")
	}
	if strings.TrimSpace(item.BaseURL) != "" {
		if _, err := url.ParseRequestURI(strings.TrimSpace(item.BaseURL)); err != nil {
			return errors.New("a valid guardrail URL is required")
		}
		if _, _, err := guardrails.ValidateDestinationSyntax(item.BaseURL, item.NetworkAccessMode); err != nil {
			return err
		}
	} else if enabling {
		return errors.New("a valid guardrail URL is required")
	}
	var headers map[string]string
	if strings.TrimSpace(item.RequestHeadersJSON) != "" {
		if err := json.Unmarshal([]byte(item.RequestHeadersJSON), &headers); err != nil {
			return errors.New("request headers must be a JSON string map")
		}
	}
	for name := range headers {
		if guardrails.IsSecretHeaderName(name) {
			return errors.New("secret headers must use an encrypted guardrail credential")
		}
	}
	var sampleResponse any
	if err := json.Unmarshal([]byte(item.SampleResponseJSON), &sampleResponse); err != nil {
		return errors.New("sample response must be valid JSON")
	}
	// Disabled drafts intentionally permit incomplete templates and rules. They
	// are validated in full before the enabled bit can be persisted.
	if !enabling {
		return nil
	}
	validateStage := func(stage guardrails.Stage, template, rulesRaw, failureRaw string) error {
		stageName := "pre-dispatch"
		if stage == guardrails.StagePostResponse {
			stageName = "post-response"
		}
		if err := guardrails.ValidateTemplate(template); err != nil {
			return fmt.Errorf("%s request template: %w", stageName, err)
		}
		rules, err := guardrails.ParseRuleSet(rulesRaw)
		if err != nil {
			return fmt.Errorf("%s response rules: %w", stageName, err)
		}
		policy := guardrails.DefaultFailurePolicy()
		if err := json.Unmarshal([]byte(failureRaw), &policy); err != nil {
			return fmt.Errorf("%s failure policy is invalid JSON", stageName)
		}
		credentialID := uint(0)
		if item.CredentialID != nil {
			credentialID = *item.CredentialID
		}
		if err := guardrails.ValidateConfig(guardrails.Config{Method: item.HTTPMethod, URL: item.BaseURL, AuthMode: item.AuthMode, SecretHeaderName: item.SecretHeaderName, CredentialID: credentialID, Timeout: time.Duration(item.TimeoutMS) * time.Millisecond, MaxRequestBytes: item.MaxRequestBytes, MaxResponseBytes: item.MaxResponseBytes, NetworkAccessMode: item.NetworkAccessMode, RequestTemplateJSON: template, Rules: rules, FailurePolicy: policy}, stage); err != nil {
			return fmt.Errorf("%s configuration: %w", stageName, err)
		}
		return nil
	}
	if item.PreDispatchEnabled {
		if err := validateStage(guardrails.StagePreDispatch, item.PreRequestTemplateJSON, item.PreResponseRulesJSON, item.PreFailurePolicyJSON); err != nil {
			return err
		}
	}
	if item.PostResponseEnabled {
		if err := validateStage(guardrails.StagePostResponse, item.PostRequestTemplateJSON, item.PostResponseRulesJSON, item.PostFailurePolicyJSON); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) createGuardrailCredential(c *echo.Context, name, secret string) (*models.GuardrailCredential, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, nil
	}
	return a.upsertGuardrailCredential(c, 0, name, secret)
}

func (a *API) upsertGuardrailCredential(c *echo.Context, id uint, name, secret string) (*models.GuardrailCredential, error) {
	if a.guardrailSecrets == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "guardrail credential encryption is unavailable")
	}
	item := models.GuardrailCredential{ID: id, Name: strings.TrimSpace(name) + " credential", Enabled: true}
	created := id == 0
	if id == 0 {
		if err := a.store.Create(c.Request().Context(), &item); err != nil {
			return nil, err
		}
	} else {
		if err := a.store.FindByID(c.Request().Context(), &item, id); err != nil {
			return nil, err
		}
		item.Name = strings.TrimSpace(name) + " credential"
	}
	plaintext := []byte(secret)
	defer zeroBytes(plaintext)
	envelope, err := a.guardrailSecrets.EncryptGuardrailCredential(c.Request().Context(), item.ID, plaintext)
	if err != nil {
		if created {
			_ = a.store.DeleteByID(c.Request().Context(), &models.GuardrailCredential{}, item.ID)
		}
		return nil, err
	}
	if err := a.store.UpdateGuardrailCredentialEnvelope(c.Request().Context(), item.ID, envelope); err != nil {
		if created {
			_ = a.store.DeleteByID(c.Request().Context(), &models.GuardrailCredential{}, item.ID)
		}
		return nil, err
	}
	return &item, nil
}

type guardrailTestPayload struct {
	Stage          guardrails.Stage `json:"stage"`
	RequestText    string           `json:"request_text"`
	ResponseText   string           `json:"response_text"`
	SampleResponse json.RawMessage  `json:"sample_response"`
}

func (a *API) testGuardrail(c *echo.Context) error {
	if a.guardrailEngine == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "guardrail runtime is unavailable")
	}
	var item models.Guardrail
	if err := a.store.FindByUUID(c.Request().Context(), &item, c.Param("id")); err != nil {
		return err
	}
	var payload guardrailTestPayload
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid test input")
	}
	if payload.Stage != guardrails.StagePreDispatch && payload.Stage != guardrails.StagePostResponse {
		return echo.NewHTTPError(http.StatusBadRequest, "stage must be pre_dispatch or post_response")
	}
	config, err := guardrails.ConfigFromModel(item, []string{"admin_test"}, payload.Stage)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	input := representativeGuardrailInput(payload.Stage, "admin-test", payload.RequestText, payload.ResponseText)
	rendered, err := guardrails.RenderTemplate(config.RequestTemplateJSON, input, config.MaxRequestBytes)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var inspection guardrails.HTTPInspection
	testCtx := guardrails.WithHTTPInspection(c.Request().Context(), func(value guardrails.HTTPInspection) { inspection = value })
	result, err := a.guardrailEngine.Evaluate(testCtx, input, config)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	item.LastTestedAt = &now
	item.LastTestLatencyMS = result.Duration.Milliseconds()
	item.LastTestStatus = string(result.Decision)
	item.LastTestErrorCode = result.ErrorCode
	_ = a.store.Save(c.Request().Context(), &item)
	return c.JSON(http.StatusOK, map[string]any{"method": config.Method, "url": config.URL, "headers": redactedHeaderNames(config), "rendered_request_body": json.RawMessage(rendered), "response_status": inspection.ResponseStatus, "response_body": inspection.ResponseBody, "latency_ms": result.Duration.Milliseconds(), "result": result})
}

func redactedHeaderNames(config guardrails.Config) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for key, value := range config.Headers {
		out[key] = value
	}
	if config.AuthMode != "none" {
		name := config.SecretHeaderName
		if name == "" {
			name = "Authorization"
		}
		out[name] = "[REDACTED]"
	}
	return out
}

func (a *API) previewGuardrail(c *echo.Context) error {
	var payload struct {
		Template       string           `json:"template_json"`
		Rules          string           `json:"rules_json"`
		Stage          guardrails.Stage `json:"stage"`
		RequestText    string           `json:"request_text"`
		ResponseText   string           `json:"response_text"`
		SampleResponse json.RawMessage  `json:"sample_response"`
		SampleStatus   int              `json:"sample_status"`
	}
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid preview")
	}
	rules, err := guardrails.ParseRuleSet(payload.Rules)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	input := representativeGuardrailInput(payload.Stage, "preview", payload.RequestText, payload.ResponseText)
	rendered, err := guardrails.RenderTemplate(payload.Template, input, guardrails.DefaultMaxRequestBytes)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	status := payload.SampleStatus
	if status == 0 {
		status = 200
	}
	var sample any
	decoder := json.NewDecoder(bytes.NewReader(payload.SampleResponse))
	decoder.UseNumber()
	if err := decoder.Decode(&sample); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "sample_response must be valid JSON")
	}
	action, matches, err := guardrails.EvaluateRules(rules, status, http.Header{}, sample)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	matchedCount := 0
	for _, result := range matches {
		if result.Matched {
			matchedCount++
		}
	}
	matched := matchedCount > 0
	if rules.MatchMode == "all" {
		matched = matchedCount == len(rules.Rules)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"rendered_request_body": json.RawMessage(rendered),
		"matched":               matched,
		"next_action":           action.Action,
		"action":                action,
		"rules":                 matches,
	})
}

func representativeGuardrailInput(stage guardrails.Stage, requestID, requestText, responseText string) guardrails.Input {
	requestMessages := []guardrails.NormalizedMessage{
		{Role: "system", Text: "You are a helpful assistant."},
		{Role: "user", Text: requestText},
	}
	rawRequest, _ := json.Marshal(map[string]any{
		"model": "preview-model",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": requestText},
		},
		"stream": false,
	})
	input := guardrails.Input{
		Stage:     stage,
		RequestID: requestID,
		Request: guardrails.Request{
			RawJSON:           rawRequest,
			Text:              requestText,
			LatestUserMessage: requestText,
			Messages:          requestMessages,
			Model:             "preview-model",
			ContentType:       "application/json",
		},
		Route: guardrails.Route{LaneName: requestID},
	}
	if stage == guardrails.StagePostResponse {
		rawResponse, _ := json.Marshal(map[string]any{
			"id":    "chatcmpl-preview",
			"model": "preview-model",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": responseText},
				"finish_reason": "stop",
			}},
		})
		input.Response = &guardrails.Response{RawJSON: rawResponse, Text: responseText, StatusCode: 200, ContentType: "application/json"}
	}
	return input
}

func (a *API) importGuardrailCurl(c *echo.Context) error {
	var payload struct {
		Curl string `json:"curl"`
	}
	if err := c.Bind(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cURL import")
	}
	imported, err := parseGuardrailCurl(payload.Curl)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, imported)
}

func parseGuardrailCurl(raw string) (map[string]any, error) {
	if strings.Contains(raw, "$(") || strings.Contains(raw, "`") || strings.ContainsAny(raw, "|;><") || strings.Contains(raw, "&&") || strings.Contains(raw, "||") {
		return nil, errors.New("unsupported shell syntax in cURL input")
	}
	raw = strings.ReplaceAll(raw, "\\\r\n", " ")
	raw = strings.ReplaceAll(raw, "\\\n", " ")
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("unsupported newline in cURL input")
	}
	tokens, err := shellWords(raw)
	if err != nil || len(tokens) == 0 || tokens[0] != "curl" {
		return nil, errors.New("input must be a supported curl command")
	}
	method, target, body := "POST", "", ""
	headers := map[string]string{}
	secretHeaders := []string{}
	for i := 1; i < len(tokens); i++ {
		token := tokens[i]
		next := func() (string, error) {
			i++
			if i >= len(tokens) {
				return "", errors.New("curl option is missing a value")
			}
			return tokens[i], nil
		}
		switch token {
		case "-X", "--request":
			value, err := next()
			if err != nil {
				return nil, err
			}
			method = strings.ToUpper(value)
		case "-H", "--header":
			value, err := next()
			if err != nil {
				return nil, err
			}
			parts := strings.SplitN(value, ":", 2)
			if len(parts) != 2 {
				return nil, errors.New("invalid curl header")
			}
			name, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if guardrails.IsSecretHeaderName(name) {
				headers[name] = "[REDACTED]"
				secretHeaders = append(secretHeaders, name)
			} else {
				headers[name] = value
			}
		case "-d", "--data", "--data-raw", "--data-binary":
			value, err := next()
			if err != nil {
				return nil, err
			}
			body = value
		default:
			if strings.HasPrefix(token, "-") {
				return nil, errors.New("unsupported curl option " + token)
			}
			if target != "" {
				return nil, errors.New("multiple curl URLs are unsupported")
			}
			target = token
		}
	}
	if target == "" {
		return nil, errors.New("curl URL is required")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("invalid curl URL")
	}
	var jsonBody any
	if strings.TrimSpace(body) != "" {
		if err := json.Unmarshal([]byte(body), &jsonBody); err != nil {
			return nil, errors.New("curl body must be JSON")
		}
	}
	sort.Strings(secretHeaders)
	return map[string]any{"http_method": method, "base_url": target, "request_headers": headers, "request_template": jsonBody, "detected_secret_headers": secretHeaders}, nil
}

func shellWords(raw string) ([]string, error) {
	var words []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, char := range raw {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ' ' || char == '\t' {
			flush()
			continue
		}
		current.WriteRune(char)
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated quote or escape in curl input")
	}
	flush()
	return words, nil
}
