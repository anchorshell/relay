package guardrails

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/store"
)

type Runtime struct {
	store  *store.Store
	engine Engine
}

func NewRuntime(st *store.Store, engine Engine) *Runtime { return &Runtime{store: st, engine: engine} }

func (r *Runtime) Resolve(ctx context.Context, laneID, providerID, endpointID uint) ([]EffectiveGuardrail, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	rows, err := r.store.EffectiveGuardrails(ctx, laneID, providerID, endpointID)
	if err != nil {
		return nil, err
	}
	result := make([]EffectiveGuardrail, 0, len(rows))
	for _, row := range rows {
		configStage := StagePreDispatch
		if !row.Guardrail.PreDispatchEnabled {
			configStage = StagePostResponse
		}
		config, err := ConfigFromModel(row.Guardrail, row.Sources, configStage)
		if err != nil {
			return nil, fmt.Errorf("guardrail %s: %w", row.Guardrail.UUID, err)
		}
		result = append(result, EffectiveGuardrail{Config: config, Model: row.Guardrail, Priority: row.Guardrail.Priority, PreDispatchEnabled: row.Guardrail.PreDispatchEnabled, PostResponseEnabled: row.Guardrail.PostResponseEnabled})
	}
	// EffectiveGuardrails already supplies the contract order: priority,
	// narrowest matching scope (endpoint, provider, lane), then UUID. Preserve
	// it here; re-sorting by UUID would silently discard scope specificity.
	return result, nil
}

func (r *Runtime) EvaluateStage(ctx context.Context, stage Stage, input Input, effective []EffectiveGuardrail) ([]Result, *Result) {
	results := make([]Result, 0, len(effective))
	for _, item := range effective {
		if stage == StagePreDispatch && !itemStageEnabled(item, stage) || stage == StagePostResponse && !itemStageEnabled(item, stage) {
			continue
		}
		cfg := item.Config
		cfg, err := ConfigFromModel(item.Model, item.BindingSources, stage)
		if err != nil {
			result := Result{Decision: DecisionError, GuardrailUUID: item.UUID, GuardrailName: item.Name, Stage: stage, ErrorCode: "invalid_configuration", HTTPStatus: 503, BindingSources: item.BindingSources}
			recordResult(ctx, result)
			results = append(results, result)
			return results, &results[len(results)-1]
		}
		input.Stage = stage
		result, err := r.engine.Evaluate(ctx, input, cfg)
		if err != nil {
			result = Result{Decision: DecisionError, GuardrailUUID: cfg.UUID, GuardrailName: cfg.Name, Stage: stage, ErrorCode: "internal_error", HTTPStatus: 503, BindingSources: cfg.BindingSources}
		}
		recordResult(ctx, result)
		results = append(results, result)
		if result.Decision == DecisionBlock || result.Decision == DecisionReplaceResponse || result.Decision == DecisionError {
			return results, &results[len(results)-1]
		}
	}
	return results, nil
}

func itemStageEnabled(item EffectiveGuardrail, stage Stage) bool {
	if stage == StagePreDispatch {
		return item.PreDispatchEnabled
	}
	return item.PostResponseEnabled
}

func ConfigFromModel(model models.Guardrail, sources []string, stage Stage) (Config, error) {
	var headers map[string]string
	if raw := strings.TrimSpace(model.RequestHeadersJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return Config{}, errors.New("request headers must be a JSON string map")
		}
	}
	if headers == nil {
		headers = map[string]string{}
	}
	templateJSON, rulesJSON, failureJSON := model.PreRequestTemplateJSON, model.PreResponseRulesJSON, model.PreFailurePolicyJSON
	if stage == StagePostResponse {
		templateJSON, rulesJSON, failureJSON = model.PostRequestTemplateJSON, model.PostResponseRulesJSON, model.PostFailurePolicyJSON
	}
	rules, err := ParseRuleSet(rulesJSON)
	if err != nil {
		return Config{}, err
	}
	failure := DefaultFailurePolicy()
	if strings.TrimSpace(failureJSON) != "" {
		if err := json.Unmarshal([]byte(failureJSON), &failure); err != nil {
			return Config{}, errors.New("invalid failure policy")
		}
	}
	credentialID := uint(0)
	if model.CredentialID != nil {
		credentialID = *model.CredentialID
	}
	timeout := time.Duration(model.TimeoutMS) * time.Millisecond
	return Config{UUID: model.UUID, Name: model.Name, Preset: model.PresetSlug, Method: model.HTTPMethod, URL: model.BaseURL, AuthMode: model.AuthMode, SecretHeaderName: model.SecretHeaderName, Headers: headers, CredentialID: credentialID, Timeout: timeout, MaxRequestBytes: model.MaxRequestBytes, MaxResponseBytes: model.MaxResponseBytes, FollowRedirects: model.FollowRedirects, NetworkAccessMode: model.NetworkAccessMode, RequestTemplateJSON: templateJSON, Rules: rules, FailurePolicy: failure, BindingSources: append([]string(nil), sources...)}, nil
}

func HasEnabledStage(effective []EffectiveGuardrail) bool {
	for _, item := range effective {
		if itemStageEnabled(item, StagePreDispatch) || itemStageEnabled(item, StagePostResponse) {
			return true
		}
	}
	return false
}

func Summarize(results []Result, includeResponses ...bool) Summary {
	summary := Summary{}
	retainResponses := len(includeResponses) > 0 && includeResponses[0]
	if len(results) == 0 {
		return summary
	}
	summary.Status = "passed"
	for _, result := range results {
		duration := result.Duration.Milliseconds()
		summary.DurationMS += duration
		if result.Stage == StagePreDispatch {
			summary.PreDurationMS += duration
		} else {
			summary.PostDurationMS += duration
		}
		audit := AuditResult{GuardrailUUID: result.GuardrailUUID, GuardrailName: result.GuardrailName, Preset: result.Preset, Stage: result.Stage, Decision: result.Decision, DurationMS: duration, HTTPStatus: result.ProviderStatus, ErrorCode: result.ErrorCode, FailMode: result.FailMode, BindingSources: result.BindingSources}
		for _, rule := range result.MatchedRules {
			if rule.Matched {
				audit.MatchedRuleIDs = append(audit.MatchedRuleIDs, rule.ID)
				audit.MatchedRules = append(audit.MatchedRules, rule)
				if rule.Label != "" {
					audit.MatchedLabels = append(audit.MatchedLabels, rule.Label)
				}
			}
		}
		summary.Results = append(summary.Results, audit)
		if retainResponses && len(result.ProviderResponseBody) > 0 {
			summary.Responses = append(summary.Responses, AuditResponse{
				GuardrailUUID: result.GuardrailUUID, GuardrailName: result.GuardrailName, Preset: result.Preset,
				Stage: result.Stage, ProviderStatus: result.ProviderStatus, ContentType: result.ProviderResponseContentType,
				TrueFields: append([]ResponseFieldMatch(nil), result.TrueResponseFields...), Body: append(json.RawMessage(nil), result.ProviderResponseBody...),
			})
		}
		if result.FailMode == FailOpen {
			summary.Status = "fail_open"
		}
		switch result.Decision {
		case DecisionBlock:
			if result.Stage == StagePreDispatch {
				summary.Status = "blocked_pre"
			} else {
				summary.Status = "blocked_post"
			}
		case DecisionReplaceResponse:
			summary.Status = "replaced_post"
		case DecisionError:
			summary.Status = "error"
		}
	}
	return summary
}

func MarshalBoundedResponses(summary Summary, maxBytes int) string {
	if len(summary.Responses) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = 64 * 1024 * 1024
	}
	for len(summary.Responses) > 0 {
		encoded, err := json.Marshal(summary.Responses)
		if err == nil && len(encoded) <= maxBytes {
			return string(encoded)
		}
		summary.Responses = summary.Responses[:len(summary.Responses)-1]
	}
	return ""
}

func MarshalBoundedSummary(summary Summary, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 16 * 1024
	}
	for len(summary.Results) > 0 {
		encoded, err := json.Marshal(summary.Results)
		if err == nil && len(encoded) <= maxBytes {
			return string(encoded)
		}
		summary.Results = summary.Results[:len(summary.Results)-1]
	}
	return "[]"
}

func ClientError(result Result, policy FailurePolicy) (int, map[string]any) {
	status := result.HTTPStatus
	if status < 400 || status > 599 {
		status = http.StatusForbidden
	}
	message, errorType, code := "This request was blocked by the configured safety policy.", "content_policy_violation", result.ErrorCode
	if result.Message != "" {
		message = result.Message
	}
	if result.ErrorType != "" {
		errorType = result.ErrorType
	}
	if result.Decision == DecisionError || result.FailMode == FailClosed || result.FailMode == ReturnError {
		if result.Message == "" && policy.Message != "" {
			message = policy.Message
		}
		if result.ErrorType == "" && policy.ErrorType != "" {
			errorType = policy.ErrorType
		}
		if result.ErrorCode == "" && policy.ErrorCode != "" {
			code = policy.ErrorCode
		}
	}
	if code == "" {
		code = "content_policy_violation"
	}
	return status, map[string]any{"error": map[string]any{"message": message, "type": errorType, "param": nil, "code": code}}
}
