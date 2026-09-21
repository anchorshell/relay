package relay

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anchorshell/relay/pkg/characterization"
	"gorm.io/gorm"
)

var ErrAdminCapacityContextUnavailable = errors.New("admin capacity context unavailable")

type Options struct {
	EnvFile         string
	AdminCookieName string
	DBOpener        DBOpener
	Extensions      []Extension
	UIFS            fs.FS
	UIBasePath      string
	// GracefulTimeout bounds HTTP/SSE/WebSocket draining after SIGTERM. Zero
	// preserves the public server's conservative default.
	GracefulTimeout time.Duration
}

func DefaultOptions() Options {
	return Options{}
}

type Extension interface {
	Name() string
	Apply(*Hooks) error
}

type Hooks struct {
	StartupHooks               []StartupHook
	ShutdownHooks              []ShutdownHook
	CredentialRuntimes         []CredentialRuntimeHook
	CredentialResolvers        []CredentialSecretResolver
	UpstreamRequestAdapters    []UpstreamRequestAdapter
	UpstreamDispatchers        []UpstreamDispatcher
	RoutingMiddleware          []RoutingMiddleware
	RequestContexts            []RequestContextHook
	PayloadCapturePolicies     []PayloadCapturePolicyHook
	RequestAccepted            []RequestAcceptedHook
	RequestInFlight            []RequestInFlightHook
	RequestCompleted           []RequestCompletedHook
	RequestRejected            []RequestRejectedHook
	AdminAuthorizers           []AdminAuthorizer
	AdminTenantScopes          []AdminTenantScopeHook
	AdminQueryScopes           []AdminQueryScopeHook
	AdminQueueScopes           []AdminQueueScopeHook
	AdminCapacityRows          []AdminCapacityRowsHook
	AdminRuntimes              []AdminRuntimeHook
	AdminRealtime              []AdminRealtimeConnectionHook
	LimitScopes                []LimitScopeHook
	ExternalLimits             []ExternalLimitHook
	DispatchAdmissions         []DispatchAdmissionHook
	RequestFinalized           []RequestFinalizedHook
	FinalizeAndAdmitNext       []FinalizeAndAdmitNextHook
	CharacterizationPolicies   []CharacterizationPolicyHook
	RoutingCharacterization    []RoutingCharacterizationPolicyHook
	CharacterizationClassifier characterization.CandidateClassifier
	CharacterizationEngine     func(context.Context, CharacterizationPolicyInput) (characterization.EngineID, error)
	AdminHTTPRegistrars        []HTTPRegistrar
	HTTPRegistrars             []HTTPRegistrar
	PartitionedRuntime         bool
	SoftDeleteProviderCatalog  bool
	// AuthoritativeFinalization means RequestFinalized hooks durably commit the
	// terminal request, usage, reservation, and concurrency state needed before
	// another request in the same serial group may be admitted. Derived public
	// request-log enrichment may then run after the group handoff.
	AuthoritativeFinalization bool
	QueuedBodyStore           QueuedBodyStore
	DisableLocalBodySpool     bool
}

type CharacterizationPolicyInput struct {
	RequestID string
	Metadata  map[string]string
}

// CharacterizationPolicyHook is a non-authoritative, observe-only feature
// control. Returning false disables characterization for the request; it may
// never reject or otherwise change dispatch.
type CharacterizationPolicyHook func(ctx context.Context, input CharacterizationPolicyInput) bool

// RoutingCharacterizationPolicyInput contains only bounded request metadata.
// It never exposes request text or provider credentials to an extension.
type RoutingCharacterizationPolicyInput struct {
	RequestID     string
	RouteKind     RouteKind
	IncomingModel string
	Lane          string
	Metadata      map[string]string
}

// RoutingCharacterizationPolicyHook opts a request into bounded, pre-routing
// characterization. The default remains disabled so OSS routing behavior is
// unchanged unless a compiled extension explicitly requests the result.
type RoutingCharacterizationPolicyHook func(ctx context.Context, input RoutingCharacterizationPolicyInput) bool

// CredentialRuntime exposes the canonical encrypted credential store to
// trusted compiled extensions without exposing the concrete secret-manager or
// database implementations. Storage identifiers are internal runtime values;
// browser and product APIs must continue to use public UUIDs.
//
// Load returns a caller-owned plaintext copy. Callers must clear it as soon as
// practical and must never log or return it.
type CredentialRuntime struct {
	Store func(ctx context.Context, credentialStorageID, providerStorageID uint64, plaintext []byte) error
	// Seal returns the canonical encrypted-at-rest representation without
	// writing it. Compiled extensions use this only when their own sidecar
	// version and the existing credentials row must commit in one DB
	// transaction.
	Seal func(ctx context.Context, credentialStorageID, providerStorageID uint64, plaintext []byte) (string, error)
	// Remember publishes plaintext to the bounded decrypted cache only after a
	// caller has committed the sealed value. Callers still own and clear their
	// input copy.
	Remember func(ctx context.Context, credentialStorageID uint64, plaintext []byte)
	Load     func(ctx context.Context, credentialStorageID uint64) ([]byte, error)
	Forget   func(ctx context.Context, credentialStorageID uint64)
}

type CredentialRuntimeHook func(CredentialRuntime)

// CredentialSecretInput contains a caller-owned copy of the plaintext stored
// by the canonical encrypted credential manager. A compiled extension may
// resolve a versioned or provider-specific envelope into the exact secret the
// existing transport should apply. Public-core credentials remain unchanged
// when no resolver handles the value.
type CredentialSecretInput struct {
	CredentialStorageID uint64
	ProviderStorageID   uint64
	Plaintext           []byte
}

// CredentialSecretResolver returns handled=false for credentials it does not
// own. A handled result must be a newly allocated caller-owned byte slice.
type CredentialSecretResolver func(ctx context.Context, input CredentialSecretInput) (secret []byte, handled bool, err error)

// UpstreamRequestInput is the neutral, request-scoped view passed to compiled
// provider adapters immediately before the existing proxy serializes an
// upstream JSON body. The map is owned by the request and may be replaced by
// the adapter. Implementations must never retain or log it.
type UpstreamRequestInput struct {
	RouteKind           RouteKind
	ProviderStorageID   uint
	ProviderID          string
	ProviderKey         string
	CredentialStorageID uint
	CredentialID        string
	EndpointStorageID   uint
	EndpointID          string
	ReasoningEffort     string
	Body                map[string]any
}

// UpstreamRequestAdapter translates neutral request controls into a
// provider-native body. Public-core behavior is unchanged when no adapter is
// installed.
type UpstreamRequestAdapter func(ctx context.Context, input UpstreamRequestInput) (map[string]any, error)

// UpstreamDispatchInput is the neutral request view offered to compiled
// extensions after the ordinary request adapters have run. It lets an
// extension dispatch through an official SDK or a local provider runtime
// while leaving HTTP-compatible providers on the public-core transport.
// Body is request-scoped and must never be retained or logged.
type UpstreamDispatchInput struct {
	RouteKind           RouteKind
	ProviderStorageID   uint
	ProviderID          string
	ProviderKey         string
	CredentialStorageID uint
	CredentialID        string
	EndpointStorageID   uint
	EndpointID          string
	UpstreamModel       string
	Method              string
	Path                string
	Streaming           bool
	Body                []byte
}

// UpstreamDispatcher returns handled=false when the normal HTTP transport
// should be used. A handled response must have a non-nil Body; the existing
// proxy continues to own streaming, retry, quota, health, and logging logic.
type UpstreamDispatcher func(ctx context.Context, input UpstreamDispatchInput) (response *http.Response, handled bool, err error)

type QueuedBodyInput struct {
	RequestID             string
	OrganizationUUID      string
	ContentType           string
	Body                  []byte
	OriginalModel         string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	OwnerGeneration       int64
}

// PayloadCapturePolicyInput contains only trusted request identity. Extensions
// may use it to further restrict the administrator's store_requests setting.
// A policy can disable capture; it can never enable capture by itself.
type PayloadCapturePolicyInput struct {
	RequestID      string
	TrustedContext TrustedRequestContext
}

type PayloadCapturePolicyHook func(ctx context.Context, input PayloadCapturePolicyInput) (bool, error)

// QueuedBodyStore is optional. Pro supplies a PostgreSQL implementation;
// standalone OSS deliberately leaves it nil and retains its existing body
// handling without any persistent request-body schema.
type QueuedBodyStore interface {
	PersistQueuedBody(ctx context.Context, input QueuedBodyInput) (string, error)
	OpenQueuedBody(ctx context.Context, reference, selectedModel string) (io.ReadCloser, int64, error)
	DeleteQueuedBody(ctx context.Context, reference string) error
}

type StartupHook func(ctx context.Context, info AppInfo) error
type ShutdownHook func(ctx context.Context) error

type AdminRuntimeHook func(AdminRuntime)

type AdminRealtimeConnectionEvent struct {
	Opened    bool
	Path      string
	RequestID string
	Headers   http.Header
}

type AdminRealtimeConnectionHook func(ctx context.Context, event AdminRealtimeConnectionEvent)

type AdminRuntime struct {
	ReevaluateExternalLimits func(ctx context.Context) error
	InvalidateRoutingCatalog func(ctx context.Context, organizationUUID string)
	PublishCapacityChanges   func(ctx context.Context, changes []CapacityChange) error
	AbortAll                 func(reason error) int
	QueueDepth               func() int
	StreamDelta              func(partitionKey string, delta int64)
}

type AppInfo struct {
	Service    string
	HTTPAddr   string
	Extensions []string
}

type DBConfig struct {
	Path string
}

type DBOpener interface {
	OpenRelayDB(ctx context.Context, cfg DBConfig) (*gorm.DB, error)
}

type DBPostMigrator interface {
	AfterRelayMigrate(ctx context.Context, db *gorm.DB) error
}

// DBMigrationPolicy lets a production database opener require an explicit
// one-shot migration task. Custom openers that do not implement it retain the
// existing migrate-on-open behavior.
type DBMigrationPolicy interface {
	MigrateRelaySchemaOnOpen() bool
}

type DBRuntimeValidator interface {
	ValidateRelayRuntimeSchema(ctx context.Context, db *gorm.DB) error
}

type DBOpenFunc func(ctx context.Context, cfg DBConfig) (*gorm.DB, error)

func (f DBOpenFunc) OpenRelayDB(ctx context.Context, cfg DBConfig) (*gorm.DB, error) {
	return f(ctx, cfg)
}

type RouteKind string

const (
	RouteKindChat       RouteKind = "chat"
	RouteKindResponses  RouteKind = "responses"
	RouteKindEmbeddings RouteKind = "embeddings"
	RouteKindMulti      RouteKind = "multi"
)

type RoutingInput struct {
	RequestID             string
	RouteKind             RouteKind
	IncomingModel         string
	Lane                  string
	Priority              int
	MaxWaitMS             int64
	AllowFallback         bool
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	MaxCostMicros         int64
	OverrideEndpointID    string
	Streaming             bool
	ReasoningEffort       string
	Characterization      characterization.Characterization
	// TrustedMetadata is produced by compiled request-context hooks. It is not
	// populated from client headers directly and must remain free of secrets.
	TrustedMetadata map[string]string
}

type RoutingDecision struct {
	Candidates []RoutingCandidate
	Metadata   map[string]string
}

type RoutingCandidate struct {
	EndpointID              string
	EndpointName            string
	ProviderID              string
	UpstreamModel           string
	Lane                    string
	Rank                    int
	FallbackCount           int
	SupportsStreaming       bool
	ReasoningControlKind    string
	AllowedReasoningEfforts []string
	DefaultReasoningEffort  string
	MaximumReasoningEffort  string
	Metadata                map[string]string
}

type RoutingPolicy interface {
	SelectCandidates(ctx context.Context, input RoutingInput) (RoutingDecision, error)
}

type RoutingMiddleware func(next RoutingPolicy) RoutingPolicy

type RequestContextInput struct {
	RequestID string
	RouteKind RouteKind
	Headers   http.Header
}

type AdminAuthInput struct {
	Method  string
	Path    string
	Headers http.Header
}

type AdminAuthorizer func(ctx context.Context, input AdminAuthInput) (bool, error)

type AdminTenantScopeInput struct {
	Method  string
	Path    string
	Headers http.Header
}

type AdminTenantScopeHook func(ctx context.Context, input AdminTenantScopeInput) (TenantScope, error)

type AdminTenantScopeRejection struct {
	Status  int
	Message string
}

func (r AdminTenantScopeRejection) Error() string {
	if strings.TrimSpace(r.Message) != "" {
		return r.Message
	}
	return http.StatusText(r.Status)
}

type AdminQueryScopeInput struct {
	Resource string
	Method   string
	Path     string
	Headers  http.Header
	Query    url.Values
}

type AdminQueryScopeHook func(ctx context.Context, input AdminQueryScopeInput) ([]func(*gorm.DB) *gorm.DB, error)

type AdminQueueScopeInput struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
}

type AdminQueueScope struct {
	OrganizationUUID string
	ActorID          string
	Deny             bool
}

type AdminQueueScopeHook func(ctx context.Context, input AdminQueueScopeInput) (AdminQueueScope, error)

type AdminQueryScopeRejection struct {
	Status  int
	Message string
}

func (r AdminQueryScopeRejection) Error() string {
	if strings.TrimSpace(r.Message) != "" {
		return r.Message
	}
	return http.StatusText(r.Status)
}

type CapacityRuntimeUsedFunc func(scope LimitScope, metric string, period string) int64

type AdminCapacityRowsInput struct {
	Headers     http.Header
	Now         time.Time
	RuntimeUsed CapacityRuntimeUsedFunc
	Preview     bool
}

type CapacityRow struct {
	Key           string    `json:"key"`
	Label         string    `json:"label"`
	ScopeType     string    `json:"scope_type"`
	ScopeID       string    `json:"scope_id"`
	TargetType    string    `json:"target_type,omitempty"`
	TargetKey     string    `json:"target_key,omitempty"`
	ActorID       string    `json:"actor_id,omitempty"`
	Metric        string    `json:"metric"`
	Period        string    `json:"period"`
	Configured    int64     `json:"configured"`
	Effective     int64     `json:"effective"`
	Used          int64     `json:"used"`
	Reserved      int64     `json:"reserved"`
	Remaining     int64     `json:"remaining"`
	Percent       float64   `json:"percent"`
	WindowStart   time.Time `json:"window_start"`
	ResetAt       time.Time `json:"reset_at,omitempty"`
	Source        string    `json:"source"`
	UserScoped    bool      `json:"user_scoped,omitempty"`
	Blocked       bool      `json:"blocked,omitempty"`
	BlockedUntil  time.Time `json:"blocked_until,omitempty"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
}

type CapacityChange struct {
	Row     CapacityRow
	Removed bool
}

type AdminCapacityRowsHook func(ctx context.Context, input AdminCapacityRowsInput) ([]CapacityRow, error)

type LimitScope struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type LimitScopeInput struct {
	RequestID     string
	RouteKind     RouteKind
	IncomingModel string
	Lane          string
	Metadata      map[string]string
	Scopes        []LimitScope
}

type LimitScopeHook func(ctx context.Context, input LimitScopeInput) ([]LimitScope, error)

type ExternalLimit struct {
	Scope                LimitScope `json:"scope"`
	Metric               string     `json:"metric"`
	Period               string     `json:"period"`
	LimitValue           int64      `json:"limit_value"`
	Used                 int64      `json:"used"`
	Reserved             int64      `json:"reserved"`
	ResetAt              time.Time  `json:"reset_at"`
	DeferScope           string     `json:"defer_scope,omitempty"`
	DeferReason          string     `json:"defer_reason,omitempty"`
	ActorScoped          bool       `json:"actor_scoped,omitempty"`
	CapacityKeyNamespace string     `json:"capacity_key_namespace,omitempty"`
	CapacityLabel        string     `json:"capacity_label,omitempty"`
}

type ExternalLimitInput struct {
	RequestID       string
	RouteKind       RouteKind
	IncomingModel   string
	Lane            string
	EndpointID      string
	EndpointName    string
	ProviderID      string
	UpstreamModel   string
	LaneID          string
	Metadata        map[string]string
	EstimatedTokens int64
	EstimatedSpend  int64
	Preview         bool
}

type ExternalLimitHook func(ctx context.Context, input ExternalLimitInput) ([]ExternalLimit, error)

type TrustedRequestContext struct {
	LogicalRequestID string
	PartitionKey     string
	Metadata         map[string]string
	Limits           TrustedRequestLimits
}

type TrustedRequestLimits struct {
	DailySpendLimitCents     *int64
	RemainingDailySpendCents *int64
	DailyRequestLimit        *int64
	RemainingDailyRequests   *int64
	DailyTokenLimit          *int64
	RemainingDailyTokens     *int64
}

type RequestContextHook func(ctx context.Context, input RequestContextInput) (TrustedRequestContext, error)

type RequestContextRejection struct {
	Status     int
	Message    string
	Code       string
	RetryAfter time.Duration
}

type RequestAcceptedEvent struct {
	RequestID     string
	RouteKind     RouteKind
	IncomingModel string
	Lane          string
	Streaming     bool
	Metadata      map[string]string
}

type RequestAcceptedHook func(ctx context.Context, event RequestAcceptedEvent) (map[string]string, error)

type DispatchAdmissionInput struct {
	RequestID             string
	TaskID                string
	RouteKind             RouteKind
	IncomingModel         string
	Lane                  string
	LaneID                string
	EndpointID            string
	EndpointName          string
	ProviderID            string
	UpstreamModel         string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	EstimatedCostMicros   int64
	Metadata              map[string]string
}

type DispatchAdmissionDecision struct {
	Allowed    bool
	EligibleAt time.Time
	Reason     string
}

type DispatchAdmissionHook func(ctx context.Context, input DispatchAdmissionInput) (DispatchAdmissionDecision, error)

type RequestFinalizedEvent struct {
	RequestID             string
	TaskID                string
	RouteKind             RouteKind
	IncomingModel         string
	Lane                  string
	LaneID                string
	LaneStorageID         uint
	EndpointID            string
	EndpointStorageID     uint
	ProviderID            string
	ProviderStorageID     uint
	UpstreamModel         string
	ReasoningEffort       string
	Streaming             bool
	Priority              int
	FallbackCount         int
	State                 string
	StatusCode            int
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	EstimatedCostMicros   int64
	ActualInputTokens     int64
	ActualOutputTokens    int64
	ActualCostMicros      int64
	QueuedAt              time.Time
	StartedAt             time.Time
	FinishedAt            time.Time
	WaitMS                int64
	LatencyMS             int64
	CandidateTraceJSON    string
	LimitImpactJSON       string
	AppliedOverridesJSON  string
	Metadata              map[string]string
	Characterization      characterization.Characterization
}

type RequestFinalizedHook func(ctx context.Context, event RequestFinalizedEvent) error

type FinalizeAndAdmitNextInput struct {
	Current RequestFinalizedEvent
	Next    DispatchAdmissionInput
}

type FinalizeAndAdmitNextDecision struct {
	Handled    bool
	Allowed    bool
	EligibleAt time.Time
	Reason     string
}

type FinalizeAndAdmitNextHook func(ctx context.Context, input FinalizeAndAdmitNextInput) (FinalizeAndAdmitNextDecision, error)

func (r RequestContextRejection) Error() string {
	if strings.TrimSpace(r.Message) != "" {
		return r.Message
	}
	return http.StatusText(r.Status)
}

type RequestInFlightEvent struct {
	RequestID       string
	RouteKind       RouteKind
	IncomingModel   string
	Lane            string
	EndpointID      string
	EndpointName    string
	ProviderID      string
	UpstreamModel   string
	ReasoningEffort string
	Streaming       bool
	Waited          time.Duration
	WaitMS          int64
	SelectedAt      time.Time
	Metadata        map[string]string
	Limits          TrustedRequestLimits
}

type RequestInFlightHook func(ctx context.Context, event RequestInFlightEvent)

type RequestCompletedEvent struct {
	RequestID             string
	RouteKind             RouteKind
	IncomingModel         string
	Lane                  string
	EndpointID            string
	EndpointName          string
	ProviderID            string
	UpstreamModel         string
	ReasoningEffort       string
	Streaming             bool
	StatusCode            int
	State                 string
	WaitMS                int64
	LatencyMS             int64
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	ActualInputTokens     int64
	ActualOutputTokens    int64
	ActualTotalTokens     int64
	EstimatedCostMicros   int64
	ActualCostMicros      int64
	QueuedAt              time.Time
	StartedAt             time.Time
	FinishedAt            time.Time
	Metadata              map[string]string
	Limits                TrustedRequestLimits
	Characterization      characterization.Characterization
}

type RequestCompletedHook func(ctx context.Context, event RequestCompletedEvent)

type RequestRejectedEvent struct {
	RequestID             string
	RouteKind             RouteKind
	IncomingModel         string
	Lane                  string
	StatusCode            int
	State                 string
	Reason                string
	EstimatedInputTokens  int64
	EstimatedOutputTokens int64
	QueuedAt              time.Time
	FinishedAt            time.Time
	WaitMS                int64
	Streaming             bool
	Priority              int
	FallbackCount         int
	Metadata              map[string]string
	Characterization      characterization.Characterization
}

type RequestRejectedHook func(ctx context.Context, event RequestRejectedEvent)

type HTTPRegistrar func(ctx context.Context, routes HTTPRoutes) error

type HTTPRoutes interface {
	Handle(method string, path string, handler HTTPHandler)
}

type HTTPRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Params  map[string]string
	Body    []byte
}

type HTTPResponse struct {
	Status int
	Body   any
}

type HTTPHandler func(ctx context.Context, req HTTPRequest) (HTTPResponse, error)

func JSON(status int, body any) HTTPResponse {
	return HTTPResponse{Status: status, Body: body}
}

func OK(body any) HTTPResponse {
	return JSON(http.StatusOK, body)
}
