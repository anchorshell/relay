package models

import (
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func SlugifyName(name string) string {
	var result strings.Builder
	pendingSeparator := false
	for _, value := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(value) || unicode.IsDigit(value):
			if pendingSeparator && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(unicode.ToLower(value))
			pendingSeparator = false
		default:
			pendingSeparator = result.Len() > 0
		}
	}
	return result.String()
}

type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthRateLimited HealthStatus = "rate_limited"
	HealthCoolingDown HealthStatus = "cooling_down"
	HealthUnhealthy   HealthStatus = "unhealthy"
)

type ScopeType string
type Metric string
type Period string
type RouteKind string
type GuardrailStage string

const (
	ScopeGlobal     ScopeType = "global"
	ScopeProvider   ScopeType = "provider"
	ScopeCredential ScopeType = "credential"
	ScopeEndpoint   ScopeType = "endpoint"
	ScopeLane       ScopeType = "lane"
)

const (
	GuardrailStagePreDispatch  GuardrailStage = "pre_dispatch"
	GuardrailStagePostResponse GuardrailStage = "post_response"
)

const (
	MetricRequests    Metric = "requests"
	MetricTokens      Metric = "tokens"
	MetricSpend       Metric = "spend"
	MetricConcurrency Metric = "concurrency"
)

const (
	PeriodSecond Period = "second"
	PeriodMinute Period = "minute"
	PeriodHour   Period = "hour"
	PeriodDay    Period = "day"
	PeriodMonth  Period = "month"
)

const (
	RouteKindChat       RouteKind = "chat"
	RouteKindResponses  RouteKind = "responses"
	RouteKindEmbeddings RouteKind = "embeddings"
	RouteKindMulti      RouteKind = "multi"
)

type Provider struct {
	ID             uint           `json:"-" gorm:"primaryKey"`
	UUID           string         `json:"id" gorm:"column:uuid;index;size:36"`
	Name           string         `json:"name"`
	Slug           string         `json:"slug"`
	BaseURL        string         `json:"base_url"`
	MaxLatencyMS   int64          `json:"max_latency_ms"`
	AuthMode       string         `json:"auth_mode"`
	AuthHeaderName string         `json:"auth_header_name"`
	Enabled        bool           `json:"enabled"`
	Notes          string         `json:"notes"`
	HealthStatus   HealthStatus   `json:"health_status"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
}

type Credential struct {
	ID              uint      `json:"-" gorm:"primaryKey"`
	UUID            string    `json:"id" gorm:"column:uuid;index;size:36"`
	ProviderID      uint      `json:"-" gorm:"index"`
	ProviderUUID    string    `json:"provider_id" gorm:"column:provider_uuid;index;size:36"`
	Name            string    `json:"name"`
	EncryptedSecret string    `json:"-"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CredentialCacheEntry struct {
	Credential       Credential
	OrganizationUUID string
}

type Endpoint struct {
	ID                      uint           `json:"-" gorm:"primaryKey"`
	UUID                    string         `json:"id" gorm:"column:uuid;index;size:36"`
	ProviderID              uint           `json:"-" gorm:"index"`
	ProviderUUID            string         `json:"provider_id" gorm:"column:provider_uuid;index;size:36"`
	CredentialID            uint           `json:"-" gorm:"index"`
	CredentialUUID          string         `json:"credential_id" gorm:"column:credential_uuid;index;size:36"`
	Name                    string         `json:"name"`
	Slug                    string         `json:"slug"`
	UpstreamModel           string         `json:"upstream_model" gorm:"index"`
	RouteKind               RouteKind      `json:"route_kind" gorm:"index"`
	Enabled                 bool           `json:"enabled" gorm:"index"`
	ManualRank              int            `json:"manual_rank" gorm:"index"`
	SuggestedRank           int            `json:"suggested_rank"`
	SuggestedScore          int64          `json:"suggested_score"`
	QualityScore            int64          `json:"quality_score"`
	ContextWindow           int64          `json:"context_window"`
	ParamSizeB              int64          `json:"param_size_b"`
	Modalities              []string       `json:"modalities" gorm:"serializer:json"`
	DescriptorTags          []string       `json:"descriptor_tags" gorm:"serializer:json"`
	SupportsStreaming       bool           `json:"supports_streaming"`
	SupportsTools           bool           `json:"supports_tools"`
	SupportsVision          bool           `json:"supports_vision"`
	ReasoningControlKind    string         `json:"reasoning_control_kind"`
	AllowedReasoningEfforts []string       `json:"allowed_reasoning_efforts" gorm:"serializer:json"`
	DefaultReasoningEffort  string         `json:"default_reasoning_effort"`
	MaximumReasoningEffort  string         `json:"maximum_reasoning_effort"`
	Pacing                  *bool          `json:"pacing" gorm:"default:true"`
	HealthStatus            HealthStatus   `json:"health_status" gorm:"index"`
	CooldownUntil           *time.Time     `json:"cooldown_until"`
	CooldownReason          string         `json:"cooldown_reason,omitempty"`
	CooldownStatusCode      int            `json:"cooldown_status_code,omitempty"`
	MaxLatencyMS            int64          `json:"max_latency_ms"`
	Notes                   string         `json:"notes"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
	DeletedAt               gorm.DeletedAt `json:"-" gorm:"index"`
}

func (e Endpoint) PacingEnabled() bool {
	return e.Pacing == nil || *e.Pacing
}

type RoutingLane struct {
	ID               uint      `json:"-" gorm:"primaryKey"`
	UUID             string    `json:"id" gorm:"column:uuid;index;size:36"`
	Name             string    `json:"name"`
	Slug             string    `json:"slug"`
	Description      string    `json:"description"`
	Hidden           bool      `json:"hidden"`
	Enabled          bool      `json:"enabled"`
	DefaultMaxWaitMS int64     `json:"default_max_wait_ms"`
	AllowFallback    bool      `json:"allow_fallback"`
	DefaultPriority  int       `json:"default_priority"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type LaneMembership struct {
	ID           uint      `json:"-" gorm:"primaryKey"`
	UUID         string    `json:"id" gorm:"column:uuid;index;size:36"`
	LaneID       uint      `json:"-" gorm:"index"`
	LaneUUID     string    `json:"lane_id" gorm:"column:lane_uuid;index;size:36"`
	EndpointID   uint      `json:"-" gorm:"index"`
	EndpointUUID string    `json:"endpoint_id" gorm:"column:endpoint_uuid;index;size:36"`
	ManualRank   int       `json:"manual_rank"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type LimitPolicy struct {
	ID         uint      `json:"-" gorm:"primaryKey"`
	UUID       string    `json:"id" gorm:"column:uuid;index;size:36"`
	ScopeType  ScopeType `json:"scope_type" gorm:"index"`
	ScopeID    uint      `json:"-" gorm:"index"`
	ScopeUUID  string    `json:"scope_id" gorm:"column:scope_uuid;index;size:36"`
	Metric     Metric    `json:"metric" gorm:"index"`
	Period     Period    `json:"period" gorm:"index"`
	LimitValue int64     `json:"limit_value"`
	Enabled    bool      `json:"enabled"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// LimitPolicyState is the current operational snapshot for one configured
// limit policy. It is deliberately policy-linked: historical usage remains in
// request_logs, without a second generic usage ledger. Sliding-window
// exactness remains in the runtime tracker and is rebuilt from request_logs on
// startup.
type LimitPolicyState struct {
	ID             uint        `json:"-" gorm:"primaryKey"`
	PolicyID       uint        `json:"-" gorm:"not null;uniqueIndex"`
	PolicyUUID     string      `json:"policy_id" gorm:"column:policy_uuid;index;size:36"`
	WindowStart    time.Time   `json:"window_start"`
	WindowEnd      time.Time   `json:"window_end"`
	UsedValue      int64       `json:"used_value"`
	ReservedValue  int64       `json:"reserved_value"`
	NextEligibleAt *time.Time  `json:"next_eligible_at,omitempty"`
	PolicyVersion  int64       `json:"policy_version"`
	StateVersion   int64       `json:"state_version" gorm:"not null;default:1"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Policy         LimitPolicy `json:"-" gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;foreignKey:PolicyID"`
}

// LimitPolicyStateSegment stores the bounded release schedule required to
// restore an exact rolling window without replaying request_logs. It is
// policy-linked operational state, not a second usage-history ledger.
type LimitPolicyStateSegment struct {
	ID             uint        `json:"-" gorm:"primaryKey"`
	PolicyID       uint        `json:"-" gorm:"not null;uniqueIndex"`
	PolicyUUID     string      `json:"policy_id" gorm:"column:policy_uuid;index;size:36"`
	FormatVersion  int         `json:"format_version" gorm:"not null"`
	PolicyVersion  int64       `json:"policy_version" gorm:"not null"`
	StateVersion   int64       `json:"state_version" gorm:"not null;default:1"`
	WindowPeriod   Period      `json:"window_period" gorm:"not null"`
	SegmentStartMS int64       `json:"segment_start_ms" gorm:"not null"`
	SegmentEndMS   int64       `json:"segment_end_ms" gorm:"not null"`
	NextExpiryMS   int64       `json:"next_expiry_ms" gorm:"not null"`
	Payload        []byte      `json:"-" gorm:"column:payload;not null"`
	Checksum       string      `json:"-" gorm:"size:64;not null"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Policy         LimitPolicy `json:"-" gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;foreignKey:PolicyID"`
}

// ObservedLimitStateSegment is the equivalent bounded checkpoint for a
// learned provider limit that is not backed by a configured LimitPolicy row.
type ObservedLimitStateSegment struct {
	ID              uint          `json:"-" gorm:"primaryKey"`
	ObservedLimitID uint          `json:"-" gorm:"column:observed_limit_id;not null;uniqueIndex"`
	ObservedUUID    string        `json:"observed_limit_id" gorm:"column:observed_limit_uuid;index;size:36"`
	FormatVersion   int           `json:"format_version" gorm:"not null"`
	PolicyVersion   int64         `json:"policy_version" gorm:"not null"`
	StateVersion    int64         `json:"state_version" gorm:"not null;default:1"`
	WindowPeriod    Period        `json:"window_period" gorm:"not null"`
	SegmentStartMS  int64         `json:"segment_start_ms" gorm:"not null"`
	SegmentEndMS    int64         `json:"segment_end_ms" gorm:"not null"`
	NextExpiryMS    int64         `json:"next_expiry_ms" gorm:"not null"`
	Payload         []byte        `json:"-" gorm:"column:payload;not null"`
	Checksum        string        `json:"-" gorm:"size:64;not null"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	ObservedLimit   ObservedLimit `json:"-" gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;foreignKey:ObservedLimitID"`
}

// RequestLogDiagnostics contains optional, potentially bulky explanation
// data. Canonical route and usage attribution remains on request_logs.
type RequestLogDiagnostics struct {
	ID                   uint       `json:"-" gorm:"primaryKey"`
	RequestLogID         uint       `json:"-" gorm:"column:request_log_id;not null;uniqueIndex"`
	RequestUUID          string     `json:"request_id" gorm:"column:request_uuid;index;size:36"`
	CandidateTraceJSON   string     `json:"candidate_trace_json"`
	LimitImpactJSON      string     `json:"limit_impact_json"`
	AppliedOverridesJSON string     `json:"applied_overrides_json"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	RequestLog           RequestLog `json:"-" gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;foreignKey:RequestLogID"`
}

type ObservedLimit struct {
	ID            uint       `json:"-" gorm:"primaryKey"`
	UUID          string     `json:"id" gorm:"column:uuid;index;size:36"`
	ScopeType     ScopeType  `json:"scope_type" gorm:"index"`
	ScopeID       uint       `json:"-" gorm:"index"`
	ScopeUUID     string     `json:"scope_id" gorm:"column:scope_uuid;index;size:36"`
	Metric        Metric     `json:"metric" gorm:"index"`
	Period        Period     `json:"period" gorm:"index"`
	ObservedValue int64      `json:"observed_value"`
	SourceHeader  string     `json:"source_header"`
	ObservedAt    time.Time  `json:"observed_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type PricingPolicy struct {
	ID                               uint      `json:"-" gorm:"primaryKey"`
	UUID                             string    `json:"id" gorm:"column:uuid;index;size:36"`
	EndpointID                       uint      `json:"-"`
	EndpointUUID                     string    `json:"endpoint_id" gorm:"column:endpoint_uuid;index;size:36"`
	Currency                         string    `json:"currency"`
	InputCostMicrosPer1MTokens       int64     `json:"input_cost_micros_per_1m_tokens"`
	OutputCostMicrosPer1MTokens      int64     `json:"output_cost_micros_per_1m_tokens"`
	CachedInputCostMicrosPer1MTokens int64     `json:"cached_input_cost_micros_per_1m_tokens"`
	FlatRequestCostMicros            int64     `json:"flat_request_cost_micros"`
	CreatedAt                        time.Time `json:"created_at"`
	UpdatedAt                        time.Time `json:"updated_at"`
}

// Guardrail is a neutral HTTP policy hook. Vendor presets only populate these
// generic fields; runtime execution never branches on PresetSlug.
type Guardrail struct {
	ID                      uint       `json:"-" gorm:"primaryKey"`
	UUID                    string     `json:"id" gorm:"column:uuid;index;size:36"`
	Name                    string     `json:"name"`
	Slug                    string     `json:"slug"`
	Description             string     `json:"description"`
	Enabled                 bool       `json:"enabled" gorm:"index"`
	PresetSlug              string     `json:"preset_slug" gorm:"index;size:64"`
	Priority                int        `json:"priority" gorm:"index"`
	HTTPMethod              string     `json:"http_method" gorm:"size:8"`
	BaseURL                 string     `json:"base_url"`
	AuthMode                string     `json:"auth_mode" gorm:"size:32"`
	CredentialID            *uint      `json:"-" gorm:"index"`
	CredentialUUID          *string    `json:"credential_id" gorm:"column:credential_uuid;index;size:36"`
	SecretHeaderName        string     `json:"secret_header_name"`
	RequestHeadersJSON      string     `json:"request_headers_json" gorm:"type:text"`
	TimeoutMS               int64      `json:"timeout_ms"`
	MaxRequestBytes         int64      `json:"max_request_bytes"`
	MaxResponseBytes        int64      `json:"max_response_bytes"`
	FollowRedirects         bool       `json:"follow_redirects"`
	NetworkAccessMode       string     `json:"network_access_mode" gorm:"size:32"`
	PreDispatchEnabled      bool       `json:"pre_dispatch_enabled"`
	PostResponseEnabled     bool       `json:"post_response_enabled"`
	PreRequestTemplateJSON  string     `json:"pre_request_template_json" gorm:"type:text"`
	PreResponseRulesJSON    string     `json:"pre_response_rules_json" gorm:"type:text"`
	PreFailurePolicyJSON    string     `json:"pre_failure_policy_json" gorm:"type:text"`
	PostRequestTemplateJSON string     `json:"post_request_template_json" gorm:"type:text"`
	PostResponseRulesJSON   string     `json:"post_response_rules_json" gorm:"type:text"`
	PostFailurePolicyJSON   string     `json:"post_failure_policy_json" gorm:"type:text"`
	SampleResponseJSON      string     `json:"sample_response_json" gorm:"type:text"`
	LastTestStatus          string     `json:"last_test_status" gorm:"size:32"`
	LastTestedAt            *time.Time `json:"last_tested_at"`
	LastTestLatencyMS       int64      `json:"last_test_latency_ms"`
	LastTestErrorCode       string     `json:"last_test_error_code" gorm:"size:64"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

// GuardrailCredential keeps guardrail secrets separate from model-provider
// credentials while using the same encrypted envelope implementation.
type GuardrailCredential struct {
	ID              uint      `json:"-" gorm:"primaryKey"`
	UUID            string    `json:"id" gorm:"column:uuid;index;size:36"`
	Name            string    `json:"name"`
	EncryptedSecret string    `json:"-" gorm:"type:text"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// GuardrailBinding attaches one guardrail to exactly one routing target. The
// API and database constraints enforce the one-target invariant.
type GuardrailBinding struct {
	ID              uint      `json:"-" gorm:"primaryKey"`
	UUID            string    `json:"id" gorm:"column:uuid;index;size:36"`
	GuardrailID     uint      `json:"-" gorm:"index"`
	GuardrailUUID   string    `json:"guardrail_id" gorm:"column:guardrail_uuid;index;size:36"`
	RoutingLaneID   *uint     `json:"-" gorm:"index"`
	RoutingLaneUUID *string   `json:"routing_lane_id" gorm:"column:routing_lane_uuid;index;size:36"`
	ProviderID      *uint     `json:"-" gorm:"index"`
	ProviderUUID    *string   `json:"provider_id" gorm:"column:provider_uuid;index;size:36"`
	EndpointID      *uint     `json:"-" gorm:"index"`
	EndpointUUID    *string   `json:"endpoint_id" gorm:"column:endpoint_uuid;index;size:36"`
	Enabled         bool      `json:"enabled" gorm:"index"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RequestLog struct {
	ID                          uint       `json:"-" gorm:"primaryKey"`
	UUID                        string     `json:"id" gorm:"column:uuid;index;size:36"`
	RequestID                   string     `json:"request_id" gorm:"index"`
	ParentRequestID             string     `json:"parent_request_id"`
	LaneID                      *uint      `json:"-" gorm:"index"`
	LaneUUID                    *string    `json:"lane_id" gorm:"column:lane_uuid;index;size:36"`
	EndpointID                  *uint      `json:"-" gorm:"index"`
	EndpointUUID                *string    `json:"endpoint_id" gorm:"column:endpoint_uuid;index;size:36"`
	ProviderID                  *uint      `json:"-" gorm:"index"`
	ProviderUUID                *string    `json:"provider_id" gorm:"column:provider_uuid;index;size:36"`
	ActorID                     string     `json:"actor_id,omitempty" gorm:"-"`
	APIKeyUUID                  string     `json:"api_key_uuid,omitempty" gorm:"-"`
	RouteKind                   RouteKind  `json:"route_kind"`
	IncomingModel               string     `json:"incoming_model"`
	SelectedUpstreamModel       string     `json:"selected_upstream_model"`
	StatusCode                  int        `json:"status_code"`
	TaskState                   string     `json:"task_state" gorm:"index"`
	QueuedAt                    *time.Time `json:"queued_at"`
	StartedAt                   *time.Time `json:"started_at"`
	FinishedAt                  *time.Time `json:"finished_at"`
	WaitMS                      int64      `json:"wait_ms"`
	LatencyMS                   int64      `json:"latency_ms"`
	Streaming                   bool       `json:"streaming"`
	Priority                    int        `json:"priority"`
	FallbackCount               int        `json:"fallback_count"`
	EstimatedInputTokens        int64      `json:"estimated_input_tokens"`
	EstimatedOutputTokens       int64      `json:"estimated_output_tokens"`
	ActualInputTokens           int64      `json:"actual_input_tokens"`
	ActualOutputTokens          int64      `json:"actual_output_tokens"`
	ActualTotalTokens           int64      `json:"actual_total_tokens"`
	EstimatedCostMicros         int64      `json:"estimated_cost_micros"`
	ActualCostMicros            int64      `json:"actual_cost_micros"`
	ErrorText                   string     `json:"error_text"`
	CandidateTraceJSON          string     `json:"candidate_trace_json"`
	LimitImpactJSON             string     `json:"limit_impact_json"`
	AppliedOverridesJSON        string     `json:"applied_overrides_json"`
	UpstreamHeadersJSON         string     `json:"upstream_headers_json"`
	RequestBodyJSON             string     `json:"request_body_json"`
	UpstreamRequestJSON         string     `json:"upstream_request_json"`
	ResponseBodyJSON            string     `json:"response_body_json"`
	PrimaryAction               *string    `json:"primary_action" gorm:"column:primary_action;size:32"`
	ActionConfidence            *float64   `json:"action_confidence" gorm:"column:action_confidence"`
	CharacterizationVersion     *string    `json:"characterization_version" gorm:"column:characterization_version;size:32"`
	CharacterizationJSON        *string    `json:"characterization_json" gorm:"column:characterization_json;type:text"`
	GuardrailStatus             string     `json:"guardrail_status" gorm:"column:guardrail_status;index;size:32"`
	GuardrailDurationMS         int64      `json:"guardrail_duration_ms"`
	GuardrailPreDurationMS      int64      `json:"guardrail_pre_duration_ms"`
	GuardrailPostDurationMS     int64      `json:"guardrail_post_duration_ms"`
	GuardrailResultsJSON        string     `json:"guardrail_results_json" gorm:"type:text"`
	GuardrailResponseBodiesJSON string     `json:"guardrail_response_bodies_json" gorm:"type:text"`
	RequestBodiesStored         bool       `json:"request_bodies_stored" gorm:"->;column:request_bodies_stored;-:migration"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
}

// UsageSummary is a request-log-derived API response. It is not persisted and
// must never become an operational accounting ledger.
type UsageSummary struct {
	ScopeType   ScopeType `json:"scope_type"`
	ScopeUUID   string    `json:"scope_id"`
	Metric      Metric    `json:"metric"`
	Period      Period    `json:"period"`
	WindowStart time.Time `json:"window_start"`
	UsedValue   int64     `json:"used_value"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AppSetting struct {
	Key       string    `json:"key" gorm:"primaryKey"`
	ValueJSON string    `json:"value_json"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (p *Provider) BeforeCreate(_ *gorm.DB) error            { return ensurePublicUUID(&p.UUID) }
func (c *Credential) BeforeCreate(_ *gorm.DB) error          { return ensurePublicUUID(&c.UUID) }
func (e *Endpoint) BeforeCreate(_ *gorm.DB) error            { return ensurePublicUUID(&e.UUID) }
func (l *RoutingLane) BeforeCreate(_ *gorm.DB) error         { return ensurePublicUUID(&l.UUID) }
func (m *LaneMembership) BeforeCreate(_ *gorm.DB) error      { return ensurePublicUUID(&m.UUID) }
func (p *LimitPolicy) BeforeCreate(_ *gorm.DB) error         { return ensurePublicUUID(&p.UUID) }
func (l *ObservedLimit) BeforeCreate(_ *gorm.DB) error       { return ensurePublicUUID(&l.UUID) }
func (p *PricingPolicy) BeforeCreate(_ *gorm.DB) error       { return ensurePublicUUID(&p.UUID) }
func (g *Guardrail) BeforeCreate(_ *gorm.DB) error           { return ensurePublicUUID(&g.UUID) }
func (c *GuardrailCredential) BeforeCreate(_ *gorm.DB) error { return ensurePublicUUID(&c.UUID) }
func (b *GuardrailBinding) BeforeCreate(_ *gorm.DB) error    { return ensurePublicUUID(&b.UUID) }
func (l *RequestLog) BeforeCreate(_ *gorm.DB) error          { return ensurePublicUUID(&l.UUID) }

func ensurePublicUUID(value *string) error {
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		*value = uuid.NewString()
		return nil
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return err
	}
	*value = parsed.String()
	return nil
}
