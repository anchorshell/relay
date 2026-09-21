package guardrails

import (
	"context"
	"encoding/json"
	"time"

	"github.com/anchorshell/relay/internal/models"
)

type Stage string
type Decision string
type FailMode string

const (
	StagePreDispatch  Stage = "pre_dispatch"
	StagePostResponse Stage = "post_response"

	DecisionAllow           Decision = "allow"
	DecisionLogOnly         Decision = "log_only"
	DecisionBlock           Decision = "block"
	DecisionReplaceResponse Decision = "replace_response"
	DecisionError           Decision = "error"

	FailOpen    FailMode = "fail_open"
	FailClosed  FailMode = "fail_closed"
	ReturnError FailMode = "return_error"
)

type NormalizedMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type Request struct {
	RawJSON           json.RawMessage
	Messages          []NormalizedMessage
	Text              string
	LatestUserMessage string
	Model             string
	Stream            bool
	ContentType       string
}

type Response struct {
	RawJSON     json.RawMessage
	Text        string
	StatusCode  int
	ContentType string
}

type Route struct {
	LaneUUID      string
	LaneName      string
	ProviderUUID  string
	ProviderName  string
	EndpointUUID  string
	EndpointName  string
	UpstreamModel string
}

type CharacterizationSummary struct {
	PrimaryAction string
	Domains       []string
	Flags         []string
	Confidence    float64
}

type Input struct {
	Stage            Stage
	RequestID        string
	Request          Request
	Response         *Response
	Route            Route
	Characterization *CharacterizationSummary
}

type RuleResult struct {
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	Source   string `json:"source,omitempty"`
	Path     string `json:"path,omitempty"`
	Operator string `json:"operator,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	Expected any    `json:"expected,omitempty"`
	Matched  bool   `json:"matched"`
}

type Result struct {
	Decision                    Decision             `json:"decision"`
	GuardrailUUID               string               `json:"guardrail_uuid"`
	GuardrailName               string               `json:"guardrail_name"`
	Stage                       Stage                `json:"stage"`
	Preset                      string               `json:"preset"`
	BindingSources              []string             `json:"binding_sources,omitempty"`
	MatchedRules                []RuleResult         `json:"matched_rules,omitempty"`
	ReplacementText             string               `json:"replacement_text,omitempty"`
	HTTPStatus                  int                  `json:"http_status"`
	ProviderStatus              int                  `json:"provider_status"`
	Duration                    time.Duration        `json:"-"`
	ErrorCode                   string               `json:"error_code,omitempty"`
	ErrorType                   string               `json:"error_type,omitempty"`
	Message                     string               `json:"message,omitempty"`
	FailMode                    FailMode             `json:"fail_mode,omitempty"`
	ProviderResponseBody        json.RawMessage      `json:"-"`
	ProviderResponseContentType string               `json:"-"`
	TrueResponseFields          []ResponseFieldMatch `json:"-"`
}

type Engine interface {
	Evaluate(context.Context, Input, Config) (Result, error)
}

type Config struct {
	UUID                string
	Name                string
	Preset              string
	Method              string
	URL                 string
	AuthMode            string
	SecretHeaderName    string
	Headers             map[string]string
	CredentialID        uint
	Timeout             time.Duration
	MaxRequestBytes     int64
	MaxResponseBytes    int64
	FollowRedirects     bool
	NetworkAccessMode   string
	RequestTemplateJSON string
	Rules               RuleSet
	FailurePolicy       FailurePolicy
	BindingSources      []string
}

type RuleSet struct {
	MatchMode   string     `json:"match_mode"`
	Rules       []Rule     `json:"rules"`
	OnMatch     RuleAction `json:"on_match"`
	OnNoMatch   RuleAction `json:"on_no_match"`
	MissingPath string     `json:"missing_path"`
}

type Rule struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Header   string `json:"header,omitempty"`
	Operator string `json:"operator"`
	Value    any    `json:"value,omitempty"`
}

type RuleAction struct {
	Action          Decision `json:"action"`
	HTTPStatus      int      `json:"http_status,omitempty"`
	ErrorType       string   `json:"error_type,omitempty"`
	ErrorCode       string   `json:"error_code,omitempty"`
	Message         string   `json:"message,omitempty"`
	ReplacementText string   `json:"replacement_text,omitempty"`
	ReplacementPath string   `json:"replacement_path,omitempty"`
}

type FailurePolicy struct {
	Mode       FailMode `json:"mode"`
	HTTPStatus int      `json:"http_status,omitempty"`
	ErrorType  string   `json:"error_type,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
	Message    string   `json:"message,omitempty"`
}

type EffectiveGuardrail struct {
	Config
	Model               models.Guardrail
	Priority            int
	PreDispatchEnabled  bool
	PostResponseEnabled bool
}

type AuditResult struct {
	GuardrailUUID  string       `json:"guardrail_uuid"`
	GuardrailName  string       `json:"guardrail_name"`
	Preset         string       `json:"preset"`
	Stage          Stage        `json:"stage"`
	Decision       Decision     `json:"decision"`
	DurationMS     int64        `json:"duration_ms"`
	HTTPStatus     int          `json:"http_status"`
	MatchedRuleIDs []string     `json:"matched_rule_ids"`
	MatchedLabels  []string     `json:"matched_rule_labels,omitempty"`
	ErrorCode      string       `json:"error_code"`
	FailMode       FailMode     `json:"fail_mode"`
	BindingSources []string     `json:"binding_sources,omitempty"`
	MatchedRules   []RuleResult `json:"matched_rules,omitempty"`
}

type ResponseFieldMatch struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type AuditResponse struct {
	GuardrailUUID  string               `json:"guardrail_uuid"`
	GuardrailName  string               `json:"guardrail_name"`
	Preset         string               `json:"preset"`
	Stage          Stage                `json:"stage"`
	ProviderStatus int                  `json:"provider_status"`
	ContentType    string               `json:"content_type,omitempty"`
	TrueFields     []ResponseFieldMatch `json:"true_fields,omitempty"`
	Body           json.RawMessage      `json:"body"`
}

type Summary struct {
	Status         string          `json:"status"`
	DurationMS     int64           `json:"duration_ms"`
	PreDurationMS  int64           `json:"pre_duration_ms"`
	PostDurationMS int64           `json:"post_duration_ms"`
	Results        []AuditResult   `json:"results"`
	Responses      []AuditResponse `json:"-"`
}

// HTTPInspection is available only to the explicit admin test path. Runtime
// callers do not install an inspection sink and never retain hook bodies.
type HTTPInspection struct {
	ResponseStatus int `json:"response_status"`
	ResponseBody   any `json:"response_body"`
}

func DefaultFailurePolicy() FailurePolicy {
	return FailurePolicy{Mode: FailClosed, HTTPStatus: 503, ErrorType: "guardrail_error", ErrorCode: "guardrail_unavailable", Message: "The configured guardrail service is unavailable."}
}
