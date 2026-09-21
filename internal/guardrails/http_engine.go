package guardrails

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTimeout          = 3 * time.Second
	MinimumTimeout          = 50 * time.Millisecond
	MaximumTimeout          = 30 * time.Second
	DefaultMaxRequestBytes  = int64(1 << 20)
	DefaultMaxResponseBytes = int64(1 << 20)
	HardMaxBodyBytes        = int64(8 << 20)
)

type SecretLoader func(context.Context, uint) ([]byte, error)

type HTTPGuardrailEngine struct {
	secrets        SecretLoader
	resolver       *net.Resolver
	mu             sync.Mutex
	clients        map[string]*http.Client
	clientOverride *http.Client
}

type inspectionContextKey struct{}

func WithHTTPInspection(ctx context.Context, sink func(HTTPInspection)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, inspectionContextKey{}, sink)
}

func NewHTTPGuardrailEngine(secrets SecretLoader) *HTTPGuardrailEngine {
	return &HTTPGuardrailEngine{secrets: secrets, resolver: net.DefaultResolver, clients: map[string]*http.Client{}}
}

func (e *HTTPGuardrailEngine) Evaluate(ctx context.Context, input Input, cfg Config) (result Result, returnedErr error) {
	started := time.Now()
	result = Result{GuardrailUUID: cfg.UUID, GuardrailName: cfg.Name, Stage: input.Stage, Preset: cfg.Preset, BindingSources: append([]string(nil), cfg.BindingSources...)}
	defer func() { result.Duration = time.Since(started) }()
	if err := ValidateConfig(cfg, input.Stage); err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "invalid_configuration"), nil
	}
	body, err := RenderTemplate(cfg.RequestTemplateJSON, input, normalizedRequestLimit(cfg.MaxRequestBytes))
	if err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "template_error"), nil
	}
	if _, _, err := ValidateDestination(ctx, cfg.URL, cfg.NetworkAccessMode, e.resolver); err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "network_policy_error"), nil
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, strings.ToUpper(cfg.Method), cfg.URL, bytes.NewReader(body))
	if err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "request_build_error"), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AnchorShell-Guardrails/1.0")
	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}
	if err := e.applyAuth(callCtx, req, cfg); err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "credential_error"), nil
	}
	client := e.clientOverride
	if client == nil {
		client, err = e.client(callCtx, cfg.NetworkAccessMode, cfg.FollowRedirects)
		if err != nil {
			return e.failureResult(result, cfg.FailurePolicy, "network_policy_error"), nil
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		code := classifyHTTPError(callCtx, err)
		return e.failureResult(result, cfg.FailurePolicy, code), nil
	}
	defer resp.Body.Close()
	result.ProviderStatus = resp.StatusCode
	limit := normalizedResponseLimit(cfg.MaxResponseBytes)
	reader := &io.LimitedReader{R: resp.Body, N: limit + 1}
	responseBody, err := io.ReadAll(reader)
	if err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "response_read_error"), nil
	}
	if int64(len(responseBody)) > limit {
		publishHTTPInspection(callCtx, resp.StatusCode, nil)
		return e.failureResult(result, cfg.FailurePolicy, "response_too_large"), nil
	}
	responsePreview := redactedResponsePreview(responseBody)
	publishHTTPInspection(callCtx, resp.StatusCode, responsePreview)
	result.ProviderResponseContentType = strings.ToLower(resp.Header.Get("Content-Type"))
	if encoded, encodeErr := json.Marshal(responsePreview); encodeErr == nil {
		result.ProviderResponseBody = append(json.RawMessage(nil), encoded...)
		result.TrueResponseFields = trueResponseFields(responsePreview)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := "http_4xx"
		if resp.StatusCode >= 500 {
			code = "http_5xx"
		}
		return e.failureResult(result, cfg.FailurePolicy, code), nil
	}
	contentType := result.ProviderResponseContentType
	if contentType != "" && !strings.Contains(contentType, "json") {
		return e.failureResult(result, cfg.FailurePolicy, "invalid_content_type"), nil
	}
	var parsed any
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "malformed_json"), nil
	}
	action, matches, err := EvaluateRules(cfg.Rules, resp.StatusCode, resp.Header, parsed)
	if err != nil {
		return e.failureResult(result, cfg.FailurePolicy, "rule_evaluation_error"), nil
	}
	result.Decision = action.Action
	result.HTTPStatus = action.HTTPStatus
	result.ErrorCode = action.ErrorCode
	result.ErrorType = action.ErrorType
	result.Message = action.Message
	result.MatchedRules = matches
	if result.Decision == "" {
		result.Decision = DecisionAllow
	}
	if result.Decision == DecisionReplaceResponse {
		result.ReplacementText = action.ReplacementText
		if action.ReplacementPath != "" {
			value, ok, pathErr := resolvePath(parsed, action.ReplacementPath)
			if pathErr != nil || !ok {
				return e.failureResult(result, cfg.FailurePolicy, "replacement_path_missing"), nil
			}
			replacement, ok := value.(string)
			if !ok {
				return e.failureResult(result, cfg.FailurePolicy, "replacement_not_string"), nil
			}
			result.ReplacementText = replacement
		}
		if len(result.ReplacementText) > 64*1024 {
			return e.failureResult(result, cfg.FailurePolicy, "replacement_too_large"), nil
		}
	}
	return result, nil
}

func ValidateConfig(cfg Config, stage Stage) error {
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return errors.New("guardrail method must be POST, PUT, or PATCH")
	}
	if cfg.Timeout != 0 && (cfg.Timeout < MinimumTimeout || cfg.Timeout > MaximumTimeout) {
		return errors.New("guardrail timeout must be between 50ms and 30s")
	}
	if cfg.MaxRequestBytes < 0 || cfg.MaxRequestBytes > HardMaxBodyBytes || cfg.MaxResponseBytes < 0 || cfg.MaxResponseBytes > HardMaxBodyBytes {
		return errors.New("guardrail body limit is invalid")
	}
	if stage != StagePreDispatch && stage != StagePostResponse {
		return errors.New("unsupported guardrail stage")
	}
	if _, _, err := ValidateDestinationSyntax(cfg.URL, cfg.NetworkAccessMode); err != nil {
		return err
	}
	if err := ValidateTemplate(cfg.RequestTemplateJSON); err != nil {
		return err
	}
	if err := ValidateRuleSet(cfg.Rules); err != nil {
		return err
	}
	if cfg.Rules.OnMatch.Action == DecisionReplaceResponse && stage != StagePostResponse {
		return errors.New("replace_response is supported only after a provider response")
	}
	if err := validateFailurePolicy(cfg.FailurePolicy); err != nil {
		return err
	}
	if cfg.AuthMode != "" && cfg.AuthMode != "none" && cfg.AuthMode != "bearer" && cfg.AuthMode != "api_key_header" && cfg.AuthMode != "basic" && cfg.AuthMode != "custom_secret_header" {
		return errors.New("unsupported guardrail auth mode")
	}
	for name := range cfg.Headers {
		if IsSecretHeaderName(name) {
			return errors.New("secret headers must use an encrypted guardrail credential")
		}
	}
	return nil
}

// IsSecretHeaderName identifies headers whose values must be supplied only by
// the encrypted credential path, never static configuration or cURL import.
func IsSecretHeaderName(name string) bool {
	canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
	lower := strings.ToLower(canonical)
	if canonical == "Authorization" || canonical == "Proxy-Authorization" || canonical == "Cookie" || canonical == "Set-Cookie" {
		return true
	}
	for _, marker := range []string{"api-key", "apikey", "token", "secret", "password", "subscription-key", "access-key", "credential", "signature"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.HasSuffix(lower, "-key")
}

func isSecretFieldName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, marker := range []string{"authorization", "cookie", "api_key", "api-key", "apikey", "token", "secret", "password", "credential", "subscription-key", "access-key", "signature", "signed"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return lower == "key" || lower == "sig"
}

func publishHTTPInspection(ctx context.Context, status int, body any) {
	sink, _ := ctx.Value(inspectionContextKey{}).(func(HTTPInspection))
	if sink != nil {
		sink(HTTPInspection{ResponseStatus: status, ResponseBody: body})
	}
}

func redactedResponsePreview(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "[non-JSON response omitted]"
	}
	return redactResponseValue(value)
}

func trueResponseFields(value any) []ResponseFieldMatch {
	fields := make([]ResponseFieldMatch, 0)
	collectTrueResponseFields(value, "", &fields)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	return fields
}

func collectTrueResponseFields(value any, path string, fields *[]ResponseFieldMatch) {
	if len(*fields) >= 256 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			next := key
			if path != "" {
				next = path + "." + key
			}
			collectTrueResponseFields(child, next, fields)
		}
	case []any:
		for index, child := range typed {
			collectTrueResponseFields(child, fmt.Sprintf("%s[%d]", path, index), fields)
		}
	case bool:
		if typed {
			*fields = append(*fields, ResponseFieldMatch{Path: path, Value: true})
		}
	}
}

func redactResponseValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if isSecretFieldName(key) && !isSafeTokenUsageField(key, child) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactResponseValue(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index := range typed {
			out[index] = redactResponseValue(typed[index])
		}
		return out
	default:
		return value
	}
}

func isSafeTokenUsageField(name string, value any) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"), " ", "_"))
	switch normalized {
	case "prompt_tokens", "completion_tokens", "input_tokens", "output_tokens", "total_tokens",
		"cached_tokens", "reasoning_tokens", "audio_tokens", "accepted_prediction_tokens", "rejected_prediction_tokens":
		switch value.(type) {
		case nil, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		default:
			return false
		}
	case "prompt_tokens_details", "prompt_token_details", "completion_tokens_details", "completion_token_details",
		"input_tokens_details", "input_token_details", "output_tokens_details", "output_token_details":
		_, isObject := value.(map[string]any)
		return value == nil || isObject
	default:
		return false
	}
}

func validateFailurePolicy(policy FailurePolicy) error {
	mode := policy.Mode
	if mode == "" {
		mode = FailClosed
	}
	if mode != FailOpen && mode != FailClosed && mode != ReturnError {
		return errors.New("invalid guardrail failure mode")
	}
	if policy.HTTPStatus != 0 && (policy.HTTPStatus < 400 || policy.HTTPStatus > 599) {
		return errors.New("failure HTTP status must be between 400 and 599")
	}
	return nil
}

func (e *HTTPGuardrailEngine) failureResult(result Result, policy FailurePolicy, code string) Result {
	if policy.Mode == "" {
		policy = DefaultFailurePolicy()
	}
	result.FailMode = policy.Mode
	result.ErrorCode = code
	result.HTTPStatus = policy.HTTPStatus
	result.ErrorType = policy.ErrorType
	result.Message = policy.Message
	if result.HTTPStatus == 0 {
		result.HTTPStatus = 503
	}
	switch policy.Mode {
	case FailOpen:
		result.Decision = DecisionAllow
	case FailClosed:
		result.Decision = DecisionBlock
	default:
		result.Decision = DecisionError
	}
	return result
}

func (e *HTTPGuardrailEngine) applyAuth(ctx context.Context, req *http.Request, cfg Config) error {
	mode := cfg.AuthMode
	if mode == "" {
		mode = "none"
	}
	if mode == "none" {
		return nil
	}
	if cfg.CredentialID == 0 || e.secrets == nil {
		return errors.New("guardrail credential is unavailable")
	}
	secret, err := e.secrets(ctx, cfg.CredentialID)
	if err != nil {
		return err
	}
	defer zero(secret)
	switch mode {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+string(secret))
	case "basic":
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(secret))
	case "api_key_header", "custom_secret_header":
		name := strings.TrimSpace(cfg.SecretHeaderName)
		if name == "" {
			name = "X-API-Key"
		}
		if !validHeaderName(name) {
			return errors.New("invalid secret header name")
		}
		req.Header.Set(name, string(secret))
	default:
		return errors.New("unsupported guardrail auth mode")
	}
	return nil
}

func (e *HTTPGuardrailEngine) client(ctx context.Context, mode string, redirects bool) (*http.Client, error) {
	if mode == "" {
		mode = NetworkPublicHTTPS
	}
	if mode != NetworkPublicHTTPS && mode != NetworkPrivate && mode != NetworkLoopback {
		return nil, errors.New("invalid network access mode")
	}
	key := mode
	if redirects {
		key += ":redirects"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if client := e.clients[key]; client != nil {
		return client, nil
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         secureDialContext(mode, e.resolver, nil),
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !redirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= 3 {
			return errors.New("too many guardrail redirects")
		}
		if _, _, err := ValidateDestination(req.Context(), req.URL.String(), mode, e.resolver); err != nil {
			return err
		}
		if len(via) > 0 && !sameAuthority(via[0].URL, req.URL) {
			for key := range req.Header {
				switch http.CanonicalHeaderKey(key) {
				case "Accept", "Content-Type", "User-Agent":
				default:
					req.Header.Del(key)
				}
			}
		}
		return nil
	}
	e.clients[key] = client
	return client, nil
}

func sameAuthority(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}
func normalizedRequestLimit(value int64) int64 {
	if value <= 0 {
		return DefaultMaxRequestBytes
	}
	if value > HardMaxBodyBytes {
		return HardMaxBodyBytes
	}
	return value
}
func normalizedResponseLimit(value int64) int64 {
	if value <= 0 {
		return DefaultMaxResponseBytes
	}
	if value > HardMaxBodyBytes {
		return HardMaxBodyBytes
	}
	return value
}
func classifyHTTPError(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && strings.Contains(strings.ToLower(urlErr.Error()), "tls") {
		return "tls_error"
	}
	return "connection_error"
}
func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
