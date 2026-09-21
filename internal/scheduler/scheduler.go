package scheduler

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/observability"
	"github.com/anchorshell/relay/internal/pricing"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/tokenestimate"
	"github.com/anchorshell/relay/pkg/characterization"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var ErrRejected = errors.New("request rejected")

const (
	defaultEstimatedOutputTokens int64 = 8
	completionPostBuffer               = 256
)

const (
	deferScopeUser               = "user"
	deferScopeUserModel          = "user_model"
	deferScopeUserProvider       = "user_provider"
	deferScopeAPIKey             = "api_key"
	deferScopeAPIKeyModel        = "api_key_model"
	deferScopeAPIKeyProvider     = "api_key_provider"
	deferReasonUserLimitExceeded = "user_limit_exceeded"
	deferReasonUserModelExceeded = "user_model_limit_exceeded"
	deferReasonUserProviderLimit = "user_provider_limit_exceeded"
)

type Estimator interface {
	Estimate(meta router.RequestMeta, body []byte, bodyBytes int64) (int64, int64)
}

type HeuristicEstimator struct{}

func (HeuristicEstimator) Estimate(meta router.RequestMeta, body []byte, bodyBytes int64) (int64, int64) {
	estimatedIn := meta.EstimatedInputTokens
	if estimatedIn <= 0 {
		estimatedIn = requestInputTokenEstimate(body)
	}
	if estimatedIn <= 0 {
		estimatedIn = tokenestimate.CountByteLength(bodyBytes)
	}
	if estimatedIn <= 0 {
		estimatedIn = 1
	}

	estimatedOut := meta.EstimatedOutputTokens
	if estimatedOut <= 0 {
		estimatedOut = max(defaultEstimatedOutputTokens, estimatedIn/2)
		if limit := requestOutputTokenLimit(body); limit > 0 && limit < estimatedOut {
			estimatedOut = limit
		}
	}
	return estimatedIn, estimatedOut
}

func requestInputTokenEstimate(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return tokenestimate.CountBytes(body)
	}
	var tokens int64
	for _, key := range []string{"messages", "input", "prompt", "instructions", "tools", "functions", "response_format"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if key == "messages" {
			tokens += messageTokens(value)
			continue
		}
		tokens += textishTokens(value)
	}
	if tokens > 0 {
		return tokens
	}
	return tokenestimate.CountBytes(body)
}

func messageTokens(value any) int64 {
	switch v := value.(type) {
	case []any:
		var tokens int64
		for _, item := range v {
			tokens += messageTokens(item)
		}
		return tokens
	case map[string]any:
		var tokens int64
		for _, key := range []string{"content", "tool_calls", "function_call"} {
			tokens += textishTokens(v[key])
		}
		return tokens
	case string:
		return tokenestimate.Count(v)
	default:
		return 0
	}
}

func textishTokens(value any) int64 {
	switch v := value.(type) {
	case string:
		return tokenestimate.Count(v)
	case []any:
		var tokens int64
		for _, item := range v {
			tokens += textishTokens(item)
		}
		return tokens
	case map[string]any:
		var tokens int64
		preferredKeys := []string{"text", "input_text", "output_text", "content", "arguments", "name", "description", "parameters", "schema"}
		for _, key := range preferredKeys {
			if _, ok := v[key]; ok {
				tokens += textishTokens(v[key])
			}
		}
		if tokens > 0 {
			return tokens
		}
		for _, item := range v {
			tokens += textishTokens(item)
		}
		return tokens
	default:
		return 0
	}
}

func requestOutputTokenLimit(body []byte) int64 {
	if len(body) == 0 {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	for _, key := range []string{"max_completion_tokens", "max_output_tokens", "max_tokens"} {
		if value := positiveInt64(payload[key]); value > 0 {
			return value
		}
	}
	return 0
}

func positiveInt64(value any) int64 {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return int64(v)
		}
	case int64:
		if v > 0 {
			return v
		}
	case int:
		if v > 0 {
			return int64(v)
		}
	case json.Number:
		n, err := v.Int64()
		if err == nil && n > 0 {
			return n
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func candidateTokenEstimate(meta router.RequestMeta, endpoint models.Endpoint, body []byte, fallbackIn, fallbackOut int64) (int64, int64) {
	if !isDummyEndpoint(endpoint) {
		return fallbackIn, fallbackOut
	}
	dummyIn, dummyOut, ok := dummyChatTokenEstimate(body)
	if !ok {
		return fallbackIn, fallbackOut
	}
	if meta.EstimatedInputTokens <= 0 {
		fallbackIn = dummyIn
	}
	if meta.EstimatedOutputTokens <= 0 {
		fallbackOut = dummyOut
	}
	return fallbackIn, fallbackOut
}

func isDummyEndpoint(endpoint models.Endpoint) bool {
	for _, value := range []string{endpoint.UpstreamModel, endpoint.Name} {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "dummy" || strings.HasPrefix(normalized, "dummy") {
			return true
		}
	}
	return false
}

func dummyChatTokenEstimate(body []byte) (int64, int64, bool) {
	if len(body) == 0 {
		return 0, 0, false
	}
	var payload struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, 0, false
	}
	raw := strings.TrimSpace(dummyMessageContent(payload.Messages))
	if !strings.HasPrefix(raw, "wait_") {
		return 0, 0, false
	}
	wait, err := time.ParseDuration(strings.TrimPrefix(raw, "wait_") + "s")
	if err != nil || wait < 0 || wait > 300*time.Second {
		return 0, 0, false
	}
	content := fmt.Sprintf("I waited %s.", dummyWaitLabel(wait))
	return max(1, int64(len(strings.Fields(raw)))), max(1, int64(len(strings.Fields(content)))), true
}

func dummyMessageContent(messages []struct {
	Content any `json:"content"`
}) string {
	if len(messages) == 0 {
		return ""
	}
	content := messages[len(messages)-1].Content
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(v)
	}
}

func dummyWaitLabel(wait time.Duration) string {
	seconds := wait.Seconds()
	if seconds == float64(int64(seconds)) {
		if int64(seconds) == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%.0f seconds", seconds)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", seconds), "0"), ".") + " seconds"
}

type SubmitRequest struct {
	Meta             router.RequestMeta
	Candidates       []router.Candidate
	Body             []byte
	BodyBytes        int64
	EnqueuedAt       time.Time
	TrustedMetadata  map[string]string
	Preview          bool
	PartitionKey     string
	OnQueued         func(context.Context, int64, int64) error
	Characterization *characterization.Handle
}

type RequestRejectedEvent struct {
	RequestID             string
	RouteKind             models.RouteKind
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

type DispatchAdmissionInput struct {
	RequestID             string
	TaskID                string
	RouteKind             string
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
	RouteKind             string
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

// FinalizeAndAdmitNextHook is an optional downstream authority seam. It lets a
// clustered implementation make the current terminal write and the next
// strictly-serial group admission one fenced database transaction. Public OSS
// behavior remains unchanged when no hook is registered.
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

type LimitScope struct {
	Type string
	ID   string
}

type LimitScopeInput struct {
	RequestID     string
	RouteKind     string
	IncomingModel string
	Lane          string
	Metadata      map[string]string
	Scopes        []LimitScope
}

type LimitScopeHook func(ctx context.Context, input LimitScopeInput) ([]LimitScope, error)

type ExternalLimit struct {
	Scope                LimitScope
	Metric               string
	Period               string
	LimitValue           int64
	Used                 int64
	Reserved             int64
	ResetAt              time.Time
	DeferScope           string
	DeferReason          string
	ActorScoped          bool
	CapacityKeyNamespace string
	CapacityLabel        string
}

type ExternalLimitInput struct {
	RequestID       string
	RouteKind       string
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

type scopeBinding struct {
	ref   limits.ScopeRef
	scope LimitScope
}

type Permit struct {
	TaskID            string
	Endpoint          models.Endpoint
	Lane              *models.RoutingLane
	OrganizationUUID  string
	ActorID           string
	EstimatedInput    int64
	EstimatedOutput   int64
	EstimatedCost     int64
	FallbackCount     int
	Waited            time.Duration
	Overrides         map[string]any
	SelectedAt        time.Time
	AppliedLimitState []limits.EffectiveLimit
	CandidateTrace    []CandidateTrace
}

type Rejection struct {
	Reason         string           `json:"reason"`
	RetryAfter     time.Duration    `json:"retry_after"`
	NearestAt      time.Time        `json:"nearest_at"`
	CandidateIDs   []string         `json:"candidate_ids"`
	CandidateTrace []CandidateTrace `json:"-"`
}

func (r Rejection) Error() string {
	return fmt.Sprintf("%s (retry after %s)", r.Reason, r.RetryAfter)
}

type task struct {
	id                 string
	meta               router.RequestMeta
	trustedMetadata    map[string]string
	candidate          router.Candidate
	candidates         []router.Candidate
	evaluation         limits.CandidateEvaluation
	resourceEligibleAt time.Time
	userEligibleAt     time.Time
	userLimitReason    string
	deferScope         string
	deferScopeID       string
	deferReason        string
	limitScopes        []limits.ScopeRef
	reservationScopes  []limits.ScopeRef
	estimatedInput     int64
	estimatedOutput    int64
	estimatedCost      int64
	enqueuedAt         time.Time
	seq                int64
	state              string
	candidateTrace     []CandidateTrace
	readyCh            chan Permit
	errCh              chan error
	activeCancel       context.CancelFunc
	cancelled          bool
	startedAt          time.Time
	substatus          string
	uploadedTokens     int64
	downloadedTokens   int64
	preview            bool
	partitionKey       string
	catalogGeneration  uint64
	preAdmitted        bool
	spanContext        trace.SpanContext
	characterization   *characterization.Handle
	guardrailSummary   guardrails.Summary
}

// SetGuardrailSummary attaches bounded audit metadata to the in-memory task so
// the existing terminal request-log write persists it exactly once.
func (s *Scheduler) SetGuardrailSummary(taskID string, summary guardrails.Summary) {
	if s == nil || taskID == "" {
		return
	}
	if s.isRegistry() {
		if runtime := s.runtimeForTask(taskID); runtime != nil {
			runtime.SetGuardrailSummary(taskID, summary)
		}
		return
	}
	s.mu.Lock()
	if item := s.tasks[taskID]; item != nil {
		item.guardrailSummary = summary
		item.guardrailSummary.Results = append([]guardrails.AuditResult(nil), summary.Results...)
		item.guardrailSummary.Responses = make([]guardrails.AuditResponse, len(summary.Responses))
		for index, response := range summary.Responses {
			item.guardrailSummary.Responses[index] = response
			item.guardrailSummary.Responses[index].Body = append(json.RawMessage(nil), response.Body...)
			item.guardrailSummary.Responses[index].TrueFields = append([]guardrails.ResponseFieldMatch(nil), response.TrueFields...)
		}
	}
	s.mu.Unlock()
}

type groupTaskNode struct {
	previous string
	next     string
}

// groupTaskQueue keeps the strict routing-group order without shifting a slice
// on every completion. Enqueue, head lookup, next lookup, and removal are all
// O(1), including cancellation of a non-head request.
type groupTaskQueue struct {
	head  string
	tail  string
	nodes map[string]groupTaskNode
}

func newGroupTaskQueue() *groupTaskQueue {
	return &groupTaskQueue{nodes: make(map[string]groupTaskNode)}
}

func (q *groupTaskQueue) append(taskID string) {
	if q == nil || taskID == "" {
		return
	}
	node := groupTaskNode{previous: q.tail}
	if q.tail == "" {
		q.head = taskID
	} else {
		tail := q.nodes[q.tail]
		tail.next = taskID
		q.nodes[q.tail] = tail
	}
	q.nodes[taskID] = node
	q.tail = taskID
}

func (q *groupTaskQueue) next(taskID string) string {
	if q == nil {
		return ""
	}
	return q.nodes[taskID].next
}

func (q *groupTaskQueue) remove(taskID string) {
	if q == nil || taskID == "" {
		return
	}
	node, ok := q.nodes[taskID]
	if !ok {
		return
	}
	if node.previous == "" {
		q.head = node.next
	} else {
		previous := q.nodes[node.previous]
		previous.next = node.next
		q.nodes[node.previous] = previous
	}
	if node.next == "" {
		q.tail = node.previous
	} else {
		next := q.nodes[node.next]
		next.previous = node.previous
		q.nodes[node.next] = next
	}
	delete(q.nodes, taskID)
}

func (q *groupTaskQueue) empty() bool {
	return q == nil || q.head == ""
}

type queuedSelection struct {
	candidate          router.Candidate
	evaluation         limits.CandidateEvaluation
	resourceEligibleAt time.Time
	userEligibleAt     time.Time
	userLimitReason    string
	deferScope         string
	deferScopeID       string
	deferReason        string
	limitScopes        []limits.ScopeRef
	reservationScopes  []limits.ScopeRef
	estimatedCost      int64
	estimatedInput     int64
	estimatedOutput    int64
	retry              time.Duration
	tracePrefix        []CandidateTrace
	candidateIndex     int
}

type CandidateTrace struct {
	EndpointID      uint                    `json:"-"`
	EndpointUUID    string                  `json:"endpoint_id"`
	EndpointName    string                  `json:"endpoint_name"`
	ProviderID      uint                    `json:"-"`
	ProviderUUID    string                  `json:"provider_id"`
	UpstreamModel   string                  `json:"upstream_model"`
	Rank            int                     `json:"rank"`
	FallbackCount   int                     `json:"fallback_count"`
	EligibleAt      time.Time               `json:"eligible_at"`
	PredictedWaitMS int64                   `json:"predicted_wait_ms"`
	Decision        string                  `json:"decision"`
	Reason          string                  `json:"reason"`
	EffectiveLimits []limits.EffectiveLimit `json:"effective_limits,omitempty"`
}

type TaskSnapshot struct {
	TaskID            string           `json:"task_id"`
	RequestID         string           `json:"request_id"`
	Lane              string           `json:"lane"`
	IncomingModel     string           `json:"incoming_model"`
	EndpointID        uint             `json:"-"`
	EndpointUUID      string           `json:"endpoint_id"`
	EndpointName      string           `json:"endpoint_name"`
	UpstreamModel     string           `json:"upstream_model"`
	ProviderID        uint             `json:"-"`
	ProviderUUID      string           `json:"provider_id"`
	OrganizationUUID  string           `json:"organization_uuid,omitempty"`
	ActorID           string           `json:"actor_id,omitempty"`
	APIKeyUUID        string           `json:"api_key_uuid,omitempty"`
	State             string           `json:"state"`
	Priority          int              `json:"priority"`
	QueuedAt          time.Time        `json:"queued_at"`
	WaitMS            int64            `json:"wait_ms"`
	PredictedEligible time.Time        `json:"predicted_eligible_at"`
	ResourceEligible  time.Time        `json:"resource_eligible_at,omitempty"`
	UserEligible      time.Time        `json:"user_eligible_at,omitempty"`
	UserLimitReason   string           `json:"user_limit_reason,omitempty"`
	DelayReason       string           `json:"delay_reason"`
	DeferScope        string           `json:"defer_scope,omitempty"`
	DeferScopeID      string           `json:"defer_scope_id,omitempty"`
	DeferReason       string           `json:"defer_reason,omitempty"`
	EstimatedCost     int64            `json:"estimated_cost_micros"`
	FallbackCount     int              `json:"fallback_count"`
	CandidateTrace    []CandidateTrace `json:"candidate_trace"`
	StartedAt         *time.Time       `json:"started_at,omitempty"`
	Substatus         string           `json:"substatus,omitempty"`
	UploadedTokens    int64            `json:"uploaded_tokens"`
	DownloadedTokens  int64            `json:"downloaded_tokens"`
}

type readyHeap []*task
type delayedHeap []*task

func (h readyHeap) Len() int { return len(h) }
func (h readyHeap) Less(i, j int) bool {
	if h[i].meta.Priority != h[j].meta.Priority {
		return h[i].meta.Priority > h[j].meta.Priority
	}
	if !h[i].enqueuedAt.Equal(h[j].enqueuedAt) {
		return h[i].enqueuedAt.Before(h[j].enqueuedAt)
	}
	if h[i].candidate.Rank != h[j].candidate.Rank {
		return h[i].candidate.Rank < h[j].candidate.Rank
	}
	return h[i].seq < h[j].seq
}
func (h readyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x any)   { *h = append(*h, x.(*task)) }
func (h *readyHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}

func (h delayedHeap) Len() int { return len(h) }
func (h delayedHeap) Less(i, j int) bool {
	if !h[i].evaluation.EligibleAt.Equal(h[j].evaluation.EligibleAt) {
		return h[i].evaluation.EligibleAt.Before(h[j].evaluation.EligibleAt)
	}
	if !h[i].enqueuedAt.Equal(h[j].enqueuedAt) {
		return h[i].enqueuedAt.Before(h[j].enqueuedAt)
	}
	return h[i].seq < h[j].seq
}
func (h delayedHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *delayedHeap) Push(x any)   { *h = append(*h, x.(*task)) }
func (h *delayedHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}

type Snapshot struct {
	QueueDepthGlobal int              `json:"queue_depth_global"`
	QueueDepthByEP   map[string]int   `json:"queue_depth_by_endpoint"`
	InFlightByEP     map[string]int64 `json:"in_flight_by_endpoint"`
	States           map[string]int   `json:"states"`
}

func (s *Scheduler) DeriveEndpointHealth(ctx context.Context, endpoint models.Endpoint, lanes []models.RoutingLane, now time.Time) (models.HealthStatus, *time.Time, error) {
	if s.isRegistry() {
		runtime, err := s.runtimeForContext(ctx, true)
		if err != nil {
			return models.HealthHealthy, nil, err
		}
		return runtime.DeriveEndpointHealth(ctx, endpoint, lanes, now)
	}
	return s.deriveEndpointHealth(ctx, endpoint, lanes, now, false)
}

func (s *Scheduler) deriveEndpointHealth(ctx context.Context, endpoint models.Endpoint, lanes []models.RoutingLane, now time.Time, ignoreEndpointState bool) (models.HealthStatus, *time.Time, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if endpoint.HealthStatus == models.HealthUnhealthy && endpoint.CooldownUntil != nil && endpoint.CooldownUntil.After(now) {
		return models.HealthUnhealthy, endpoint.CooldownUntil, nil
	}
	if endpoint.HealthStatus == models.HealthRateLimited {
		ignoreEndpointState = true
	}

	waitBudgetByLane := make(map[string]int64, len(lanes))
	minWaitBudgetMS := s.defaultWaitMS
	for _, lane := range lanes {
		if !lane.Enabled {
			continue
		}
		waitBudget := lane.DefaultMaxWaitMS
		if waitBudget <= 0 {
			waitBudget = s.defaultWaitMS
		}
		waitBudgetByLane[lane.Name] = waitBudget
		if minWaitBudgetMS <= 0 || waitBudget < minWaitBudgetMS {
			minWaitBudgetMS = waitBudget
		}
	}
	if minWaitBudgetMS <= 0 {
		minWaitBudgetMS = s.defaultWaitMS
	}

	bestStatus := models.HealthHealthy
	var bestUntil *time.Time
	for _, item := range s.Items() {
		if item.EndpointID != endpoint.ID {
			continue
		}
		if item.State != "waiting" && item.State != "ready" {
			continue
		}
		if isRequestScopedDefer(item.DeferScope) {
			continue
		}

		waitBudgetMS := waitBudgetByLane[item.Lane]
		if waitBudgetMS <= 0 {
			waitBudgetMS = minWaitBudgetMS
		}
		until := item.ResourceEligible.UTC()
		if until.IsZero() || !until.After(now) {
			continue
		}
		delay := until.Sub(now)
		if delay < 0 {
			delay = 0
		}
		status := models.HealthCoolingDown
		if delay > time.Duration(waitBudgetMS)*time.Millisecond {
			status = models.HealthRateLimited
		}
		if bestUntil == nil || until.Before(*bestUntil) {
			bestStatus = status
			bestUntil = &until
		}
	}
	if bestStatus != models.HealthHealthy {
		return bestStatus, bestUntil, nil
	}

	evaluateStatus := func(laneID uint, waitBudgetMS int64) (models.HealthStatus, *time.Time, error) {
		var eval limits.CandidateEvaluation
		var err error
		if ignoreEndpointState {
			eval, err = s.resolver.EvaluateIgnoringEndpointState(ctx, nil, endpoint, laneID, 0, 0, now)
		} else {
			eval, err = s.resolver.Evaluate(ctx, nil, endpoint, laneID, 0, 0, now)
		}
		if err != nil {
			return models.HealthHealthy, nil, err
		}
		if !eval.EligibleAt.After(now) {
			return models.HealthHealthy, nil, nil
		}
		until := eval.EligibleAt.UTC()
		if until.Sub(now) <= time.Duration(waitBudgetMS)*time.Millisecond {
			return models.HealthCoolingDown, &until, nil
		}
		return models.HealthRateLimited, &until, nil
	}

	if len(waitBudgetByLane) == 0 {
		status, until, err := evaluateStatus(0, minWaitBudgetMS)
		if err != nil {
			return models.HealthHealthy, nil, err
		}
		return status, until, nil
	}

	for _, lane := range lanes {
		if !lane.Enabled {
			continue
		}
		waitBudgetMS := lane.DefaultMaxWaitMS
		if waitBudgetMS <= 0 {
			waitBudgetMS = minWaitBudgetMS
		}
		status, until, err := evaluateStatus(lane.ID, waitBudgetMS)
		if err != nil {
			return models.HealthHealthy, nil, err
		}
		if healthSeverity(status) > healthSeverity(bestStatus) || (healthSeverity(status) == healthSeverity(bestStatus) && afterPtr(until, bestUntil)) {
			bestStatus = status
			bestUntil = until
		}
	}
	return bestStatus, bestUntil, nil
}

func healthSeverity(status models.HealthStatus) int {
	switch status {
	case models.HealthUnhealthy:
		return 3
	case models.HealthRateLimited:
		return 2
	case models.HealthCoolingDown:
		return 1
	default:
		return 0
	}
}

func afterPtr(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return a.After(*b)
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UTC().Equal(b.UTC())
}

type Scheduler struct {
	store                     *store.Store
	resolver                  *limits.Resolver
	tracker                   *limits.Tracker
	telemetry                 *telemetry.Hub
	estimator                 Estimator
	routingStrategy           router.Strategy
	defaultWaitMS             int64
	maxQueueLen               int
	maxQueueAgeMS             int64
	limitHooks                []LimitScopeHook
	externalLimitHooks        []ExternalLimitHook
	rejectedHooks             []RequestRejectedHook
	admissionHooks            []DispatchAdmissionHook
	finalizedHooks            []RequestFinalizedHook
	handoffHooks              []FinalizeAndAdmitNextHook
	serialGroupDispatch       bool
	authoritativeFinalization bool
	capacityTelemetry         *capacityTelemetryEmitter

	mu               sync.Mutex
	admissionMu      sync.Mutex
	coordinator      *keyedCoordinator
	dispatchSlots    chan struct{}
	seq              int64
	ready            readyHeap
	delayed          delayedHeap
	tasks            map[string]*task
	totals           map[string]int
	dryRunLaneTails  map[string]time.Time
	groupReleasedAt  map[string]time.Time
	groupActive      map[string]string
	groupQueued      map[string]int
	groupTasks       map[string]*groupTaskQueue
	groupFIFOTails   map[string]time.Time
	completionPosts  chan *completionPost
	healthRefreshes  chan []uint
	completionPostWG sync.WaitGroup
	pendingPosts     atomic.Int64
	activeStreams    atomic.Int64
	completionAsync  bool
	wakeup           chan struct{}
	stopCh           chan struct{}
	stopOnce         sync.Once
	persistState     bool
	backgroundCtx    context.Context

	registryMode             bool
	runtimeFactory           func(context.Context, string) (*Scheduler, error)
	runtimeMu                sync.RWMutex
	runtimes                 map[string]*Scheduler
	taskRuntimes             map[string]*Scheduler
	runtimeMeta              map[string]*runtimeLifecycle
	runtimeIdleTTL           time.Duration
	runtimeSweep             time.Duration
	runtimeCheckpointTimeout time.Duration
	registryStop             chan struct{}
	registryOnce             sync.Once
	registryWG               sync.WaitGroup
	runtimeEvicted           func(context.Context, string)
}

func New(st *store.Store, resolver *limits.Resolver, tracker *limits.Tracker, hub *telemetry.Hub, estimator Estimator, defaultWaitMS int64, maxQueueLen int, maxQueueAgeMS int64) *Scheduler {
	if estimator == nil {
		estimator = HeuristicEstimator{}
	}
	s := &Scheduler{
		store:               st,
		resolver:            resolver,
		tracker:             tracker,
		telemetry:           hub,
		estimator:           estimator,
		defaultWaitMS:       defaultWaitMS,
		maxQueueLen:         maxQueueLen,
		maxQueueAgeMS:       maxQueueAgeMS,
		tasks:               make(map[string]*task),
		totals:              make(map[string]int),
		dryRunLaneTails:     make(map[string]time.Time),
		groupReleasedAt:     make(map[string]time.Time),
		groupActive:         make(map[string]string),
		groupQueued:         make(map[string]int),
		groupTasks:          make(map[string]*groupTaskQueue),
		groupFIFOTails:      make(map[string]time.Time),
		coordinator:         newKeyedCoordinator(),
		dispatchSlots:       make(chan struct{}, 64),
		completionPosts:     make(chan *completionPost, completionPostBuffer),
		healthRefreshes:     make(chan []uint, 1024),
		wakeup:              make(chan struct{}, 1),
		stopCh:              make(chan struct{}),
		persistState:        true,
		serialGroupDispatch: true,
		backgroundCtx:       context.Background(),
	}
	heap.Init(&s.ready)
	heap.Init(&s.delayed)
	s.capacityTelemetry = newCapacityTelemetryEmitter(s)
	return s
}

func (s *Scheduler) WithBackgroundContext(ctx context.Context) *Scheduler {
	if s == nil {
		return s
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.backgroundCtx = context.WithoutCancel(ctx)
	return s
}

// WithRoutingStrategy keeps queued catalog refreshes on the same decorated
// candidate path used at initial submission. Without it, an extension-owned
// authorization filter could be bypassed when catalog state changes while a
// request is waiting.
func (s *Scheduler) WithRoutingStrategy(strategy router.Strategy) *Scheduler {
	if s != nil {
		s.routingStrategy = strategy
	}
	return s
}

func (s *Scheduler) backgroundContext() context.Context {
	if s == nil || s.backgroundCtx == nil {
		return context.Background()
	}
	return s.backgroundCtx
}

func (s *Scheduler) taskContext(task *task) context.Context {
	ctx := s.backgroundContext()
	if task == nil {
		return ctx
	}
	if task.spanContext.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, task.spanContext)
	}
	return tenancy.ContextWithMetadataScope(ctx, task.trustedMetadata)
}

func (s *Scheduler) WithLimitScopeHooks(hooks ...LimitScopeHook) *Scheduler {
	if s == nil {
		return nil
	}
	s.limitHooks = append([]LimitScopeHook(nil), hooks...)
	return s
}

func (s *Scheduler) WithExternalLimitHooks(hooks ...ExternalLimitHook) *Scheduler {
	if s == nil {
		return nil
	}
	s.externalLimitHooks = append([]ExternalLimitHook(nil), hooks...)
	return s
}

func (s *Scheduler) WithRequestRejectedHooks(hooks ...RequestRejectedHook) *Scheduler {
	if s == nil {
		return nil
	}
	s.rejectedHooks = append([]RequestRejectedHook(nil), hooks...)
	return s
}

func (s *Scheduler) WithDispatchAdmissionHooks(hooks ...DispatchAdmissionHook) *Scheduler {
	if s == nil {
		return nil
	}
	s.admissionHooks = append([]DispatchAdmissionHook(nil), hooks...)
	return s
}

func (s *Scheduler) WithRequestFinalizedHooks(hooks ...RequestFinalizedHook) *Scheduler {
	if s == nil {
		return nil
	}
	s.finalizedHooks = append([]RequestFinalizedHook(nil), hooks...)
	return s
}

func (s *Scheduler) WithFinalizeAndAdmitNextHooks(hooks ...FinalizeAndAdmitNextHook) *Scheduler {
	if s == nil {
		return nil
	}
	s.handoffHooks = append([]FinalizeAndAdmitNextHook(nil), hooks...)
	return s
}

func (s *Scheduler) WithAuthoritativeFinalization(enabled bool) *Scheduler {
	if s == nil {
		return nil
	}
	s.authoritativeFinalization = enabled
	return s
}

func (s *Scheduler) Start() {
	if s.isRegistry() {
		return
	}
	if s.authoritativeFinalization {
		s.completionAsync = true
		s.completionPostWG.Add(1)
		go s.completionPostLoop()
	}
	s.completionPostWG.Add(1)
	go s.endpointHealthRefreshLoop()
	go s.loop()
}

func (s *Scheduler) Stop() {
	if s.isRegistry() {
		s.registryOnce.Do(func() { close(s.registryStop) })
		s.registryWG.Wait()
		for _, runtime := range s.runtimeList() {
			runtime.Stop()
		}
		return
	}
	s.stopOnce.Do(func() {
		if s.capacityTelemetry != nil {
			s.capacityTelemetry.stop()
		}
		close(s.stopCh)
		s.completionPostWG.Wait()
	})
}

// AbortAll fails closed without attempting persistence. It is intended for a
// downstream coordinator that has lost the authority required to dispatch or
// finalize work. Durable downstream reconciliation remains responsible for
// recording abandoned attempts after the authority store becomes available.
func (s *Scheduler) AbortAll(reason error) int {
	if s == nil {
		return 0
	}
	if reason == nil {
		reason = errors.New("scheduler authority unavailable")
	}
	if s.isRegistry() {
		total := 0
		for _, runtime := range s.runtimeList() {
			total += runtime.AbortAll(reason)
		}
		s.runtimeMu.Lock()
		s.taskRuntimes = make(map[string]*Scheduler)
		s.runtimeMu.Unlock()
		return total
	}

	type abortedTask struct {
		item     *task
		inFlight bool
		cancel   context.CancelFunc
	}
	s.admissionMu.Lock()
	s.mu.Lock()
	aborted := make([]abortedTask, 0, len(s.tasks))
	for id, item := range s.tasks {
		if item == nil {
			delete(s.tasks, id)
			continue
		}
		aborted = append(aborted, abortedTask{item: item, inFlight: item.state == "in_flight", cancel: item.activeCancel})
		item.activeCancel = nil
		item.cancelled = true
		item.state = "failed"
		delete(s.tasks, id)
		s.totals["failed"]++
	}
	s.ready = nil
	s.delayed = nil
	s.groupActive = make(map[string]string)
	s.groupQueued = make(map[string]int)
	s.groupTasks = make(map[string]*groupTaskQueue)
	s.groupFIFOTails = make(map[string]time.Time)
	s.groupReleasedAt = make(map[string]time.Time)
	heap.Init(&s.ready)
	heap.Init(&s.delayed)
	s.mu.Unlock()

	for _, abortedItem := range aborted {
		if abortedItem.cancel != nil {
			abortedItem.cancel()
		}
		s.tracker.Release(abortedItem.item.id)
		if abortedItem.inFlight {
			s.tracker.AddConcurrency(taskReservationScopes(abortedItem.item), -1)
		}
		select {
		case abortedItem.item.errCh <- reason:
		default:
		}
		s.emitState(abortedItem.item)
	}
	s.admissionMu.Unlock()
	s.signal()
	return len(aborted)
}

func (s *Scheduler) SetPersistence(enabled bool) {
	if s.isRegistry() {
		s.runtimeMu.Lock()
		s.persistState = enabled
		runtimes := make([]*Scheduler, 0, len(s.runtimes))
		for _, runtime := range s.runtimes {
			runtimes = append(runtimes, runtime)
		}
		s.runtimeMu.Unlock()
		for _, runtime := range runtimes {
			runtime.SetPersistence(enabled)
		}
		return
	}
	s.persistState = enabled
}

func (s *Scheduler) SetSerialGroupDispatch(enabled bool) {
	if s == nil {
		return
	}
	if s.isRegistry() {
		for _, runtime := range s.runtimeList() {
			runtime.SetSerialGroupDispatch(enabled)
		}
		return
	}
	s.mu.Lock()
	s.serialGroupDispatch = enabled
	s.mu.Unlock()
	s.signal()
}

func (s *Scheduler) ReloadLimitSettings(ctx context.Context) {
	if s.isRegistry() {
		for _, runtime := range s.runtimeList() {
			runtime.ReloadLimitSettings(ctx)
		}
		return
	}
	if s == nil || s.resolver == nil {
		return
	}
	s.resolver.LoadSettings(ctx)
}

func (s *Scheduler) ReloadLimitSettingsAndReevaluate(ctx context.Context) error {
	if s.isRegistry() {
		for _, runtime := range s.runtimeList() {
			if err := runtime.ReloadLimitSettingsAndReevaluate(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if s == nil {
		return nil
	}
	s.ReloadLimitSettings(ctx)
	return s.ReevaluateLimitScopes(ctx, limits.ScopeRef{Type: models.ScopeGlobal, ID: 0})
}

func (s *Scheduler) Submit(ctx context.Context, req SubmitRequest) (Permit, error) {
	if s.isRegistry() {
		runtime, err := s.runtime(ctx, req.PartitionKey, true)
		if err != nil {
			return Permit{}, err
		}
		permit, err := runtime.Submit(ctx, req)
		if err == nil {
			s.rememberTask(permit.TaskID, runtime)
		}
		return permit, err
	}
	now := req.EnqueuedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	waitBudget := req.Meta.MaxWaitMS
	if waitBudget <= 0 {
		waitBudget = s.defaultWaitMS
	}
	if len(req.Candidates) == 0 {
		rejection := Rejection{Reason: "no candidates", RetryAfter: time.Second}
		s.recordSubmitRejection(ctx, req, now, rejection)
		return Permit{}, rejection
	}

	selected, submitErr := func() (*task, error) {
		queuedPrepared := false
		// Selection is repeated under the exact group/policy keys it discovers.
		// A concurrent catalog or observed-policy mutation may change those keys;
		// retry instead of admitting under a stale guard. In the normal warm-cache
		// path the first retry succeeds.
		for attempts := 0; attempts < 4; attempts++ {
			selection, err := s.selectCandidate(ctx, req, now, waitBudget)
			if err != nil {
				return nil, err
			}
			if !queuedPrepared && selection.retry > 0 && req.OnQueued != nil {
				if err := req.OnQueued(ctx, selection.estimatedInput, selection.estimatedOutput); err != nil {
					return nil, err
				}
				req.Meta.EstimatedInputTokens = selection.estimatedInput
				req.Meta.EstimatedOutputTokens = selection.estimatedOutput
				req.Body = nil
				queuedPrepared = true
				selection, err = s.selectCandidate(ctx, req, now, waitBudget)
				if err != nil {
					return nil, err
				}
			}
			keys := selectionCoordinatorKeys(candidateGroupKey(req.Meta, selection.candidate), selection.evaluation)
			unlock := s.coordinator.lock(keys...)

			selection, err = s.selectCandidate(ctx, req, now, waitBudget)
			if err != nil {
				unlock()
				return nil, err
			}
			guardedKeys := selectionCoordinatorKeys(candidateGroupKey(req.Meta, selection.candidate), selection.evaluation)
			if !equalCoordinatorKeys(keys, guardedKeys) {
				unlock()
				continue
			}

			s.mu.Lock()
			if len(s.tasks) >= s.maxQueueLen {
				s.mu.Unlock()
				unlock()
				return nil, Rejection{Reason: "queue full", RetryAfter: time.Second}
			}
			s.mu.Unlock()

			s.applyGroupFIFOSelection(req, selection, now, false)
			item := s.enqueueTask(ctx, now, req.Meta, req.TrustedMetadata, req.PartitionKey, req.Preview, selection, req.Candidates, req.Characterization)
			unlock()
			return item, nil
		}
		return nil, errors.New("routing catalog changed repeatedly during admission")
	}()
	if submitErr != nil {
		s.recordSubmitRejection(ctx, req, now, submitErr)
		return Permit{}, submitErr
	}

	select {
	case permit := <-selected.readyCh:
		return permit, nil
	case err := <-selected.errCh:
		s.recordSubmitRejection(ctx, req, now, err)
		return Permit{}, err
	case <-ctx.Done():
		_ = s.Cancel(selected.id)
		return Permit{}, ctx.Err()
	}
}

func (s *Scheduler) DryRunSubmit(ctx context.Context, req SubmitRequest) (Permit, error) {
	if s.isRegistry() {
		runtime, err := s.runtime(ctx, req.PartitionKey, true)
		if err != nil {
			return Permit{}, err
		}
		return runtime.DryRunSubmit(ctx, req)
	}
	now := req.EnqueuedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	waitBudget := req.Meta.MaxWaitMS
	if waitBudget <= 0 {
		waitBudget = s.defaultWaitMS
	}
	if len(req.Candidates) == 0 {
		return Permit{}, Rejection{Reason: "no candidates", RetryAfter: time.Second}
	}

	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	selection, err := s.selectCandidate(ctx, req, now, waitBudget)
	if err != nil {
		return Permit{}, err
	}
	s.applyGroupFIFOSelection(req, selection, now, true)

	taskID := req.Meta.RequestID
	if taskID == "" {
		taskID = uuid.NewString()
	}
	trace := candidateTrace(selection, req.Candidates)
	scopes := selection.reservationScopes
	if len(scopes) == 0 {
		scopes = selection.limitScopes
	}
	if len(scopes) == 0 {
		scopes = candidateScopes(selection.candidate)
	}
	if !isRequestScopedDefer(selection.deferScope) {
		s.reserveUsageEstimates(taskID, scopes, selection.estimatedInput+selection.estimatedOutput, selection.estimatedCost, selection.evaluation.EligibleAt)
	}

	return Permit{
		TaskID:            taskID,
		Endpoint:          selection.candidate.Endpoint,
		Lane:              selection.candidate.Lane,
		OrganizationUUID:  organizationUUIDFromMetadata(req.TrustedMetadata),
		ActorID:           actorIDFromMetadata(req.TrustedMetadata),
		EstimatedInput:    selection.estimatedInput,
		EstimatedOutput:   selection.estimatedOutput,
		EstimatedCost:     selection.estimatedCost,
		FallbackCount:     selection.candidate.FallbackCount,
		Waited:            selection.retry,
		Overrides:         req.Meta.Overrides,
		SelectedAt:        selection.evaluation.EligibleAt.UTC(),
		AppliedLimitState: selection.evaluation.EffectiveLimits,
		CandidateTrace:    trace,
	}, nil
}

func (s *Scheduler) selectCandidate(ctx context.Context, req SubmitRequest, now time.Time, waitBudget int64) (*queuedSelection, error) {
	return s.selectCandidateWithOptions(ctx, req, now, waitBudget, false)
}

func (s *Scheduler) selectCandidateWithOptions(ctx context.Context, req SubmitRequest, now time.Time, waitBudget int64, ignoreEndpointState bool) (*queuedSelection, error) {
	estimatedIn, estimatedOut := s.estimator.Estimate(req.Meta, req.Body, req.BodyBytes)
	var bestRejection Rejection
	var selected *queuedSelection
	var deferred *queuedSelection
	trace := make([]CandidateTrace, 0, len(req.Candidates))
	for candidateIndex, candidate := range req.Candidates {
		laneID := uint(0)
		if candidate.Lane != nil {
			laneID = candidate.Lane.ID
		}
		candidateEstimatedIn, candidateEstimatedOut := candidateTokenEstimate(req.Meta, candidate.Endpoint, req.Body, estimatedIn, estimatedOut)
		pricingPolicy, err := s.store.GetPricingByEndpoint(ctx, candidate.Endpoint.ID)
		if err != nil {
			pricingPolicy = models.PricingPolicy{EndpointID: candidate.Endpoint.ID, Currency: "USD"}
		}
		estimate := pricing.EstimateCost(pricingPolicy, candidateEstimatedIn, candidateEstimatedOut)
		if req.Meta.MaxCostMicros > 0 && estimate.CostMicros > req.Meta.MaxCostMicros {
			trace = append(trace, CandidateTrace{
				EndpointID:      candidate.Endpoint.ID,
				EndpointUUID:    candidate.Endpoint.UUID,
				EndpointName:    candidate.Endpoint.Name,
				ProviderID:      candidate.Endpoint.ProviderID,
				ProviderUUID:    candidate.Endpoint.ProviderUUID,
				UpstreamModel:   candidate.Endpoint.UpstreamModel,
				Rank:            candidate.Rank,
				FallbackCount:   candidate.FallbackCount,
				EligibleAt:      now,
				PredictedWaitMS: 0,
				Decision:        "rejected",
				Reason:          "max cost exceeded",
			})
			bestRejection = chooseSooner(bestRejection, Rejection{
				Reason:       "max cost exceeded",
				RetryAfter:   0,
				NearestAt:    now,
				CandidateIDs: candidateUUIDs(candidate.Endpoint),
			})
			continue
		}
		limitScopes, err := s.limitScopesForCandidate(ctx, req, candidate)
		if err != nil {
			return nil, err
		}
		var evaluation limits.CandidateEvaluation
		if ignoreEndpointState {
			evaluation, err = s.resolver.EvaluateIgnoringEndpointState(ctx, limitScopes, candidate.Endpoint, laneID, candidateEstimatedIn+candidateEstimatedOut, estimate.CostMicros, now)
		} else {
			evaluation, err = s.resolver.Evaluate(ctx, limitScopes, candidate.Endpoint, laneID, candidateEstimatedIn+candidateEstimatedOut, estimate.CostMicros, now)
		}
		if err != nil {
			return nil, err
		}
		resourceEligibleAt := evaluation.EligibleAt
		externalEvaluation, externalScopes, externalDeferScope, externalDeferScopeID, externalDeferReason, userEligibleAt, userLimitReason, err := s.evaluateExternalLimits(ctx, req, candidate, candidateEstimatedIn+candidateEstimatedOut, estimate.CostMicros, now)
		if err != nil {
			return nil, err
		}
		deferScope := ""
		deferScopeID := ""
		deferReason := ""
		if isRequestScopedDefer(externalDeferScope) {
			deferScope = externalDeferScope
			deferScopeID = externalDeferScopeID
			deferReason = externalDeferReason
		}
		evaluation = mergeEvaluations(evaluation, externalEvaluation, now)
		reservationScopes := reservationScopesForDecision(limitScopes, externalScopes, deferScope, resourceEligibleAt, now)
		retry := evaluation.EligibleAt.Sub(now)
		if retry < 0 {
			retry = 0
		}
		waitBudgetDuration := time.Duration(waitBudget) * time.Millisecond
		if retry > waitBudgetDuration {
			trace = append(trace, CandidateTrace{
				EndpointID:      candidate.Endpoint.ID,
				EndpointUUID:    candidate.Endpoint.UUID,
				EndpointName:    candidate.Endpoint.Name,
				ProviderID:      candidate.Endpoint.ProviderID,
				ProviderUUID:    candidate.Endpoint.ProviderUUID,
				UpstreamModel:   candidate.Endpoint.UpstreamModel,
				Rank:            candidate.Rank,
				FallbackCount:   candidate.FallbackCount,
				EligibleAt:      evaluation.EligibleAt,
				PredictedWaitMS: retry.Milliseconds(),
				Decision:        "queued",
				Reason:          evaluation.Reason + " (queued beyond wait budget)",
				EffectiveLimits: evaluation.EffectiveLimits,
			})
			bestRejection = chooseSooner(bestRejection, Rejection{
				Reason:       "all candidates exceed wait budget",
				RetryAfter:   retry,
				NearestAt:    evaluation.EligibleAt,
				CandidateIDs: appendCandidateUUID(bestRejection.CandidateIDs, candidate.Endpoint),
			})
			if !req.Meta.AllowFallback {
				break
			}
			continue
		}

		if isCandidateScopedDefer(deferScope) && retry > 0 && req.Meta.AllowFallback {
			trace = append(trace, CandidateTrace{
				EndpointID:      candidate.Endpoint.ID,
				EndpointUUID:    candidate.Endpoint.UUID,
				EndpointName:    candidate.Endpoint.Name,
				ProviderID:      candidate.Endpoint.ProviderID,
				ProviderUUID:    candidate.Endpoint.ProviderUUID,
				UpstreamModel:   candidate.Endpoint.UpstreamModel,
				Rank:            candidate.Rank,
				FallbackCount:   candidate.FallbackCount,
				EligibleAt:      evaluation.EligibleAt,
				PredictedWaitMS: retry.Milliseconds(),
				Decision:        "queued",
				Reason:          evaluation.Reason,
				EffectiveLimits: evaluation.EffectiveLimits,
			})
			if deferred == nil ||
				evaluation.EligibleAt.Before(deferred.evaluation.EligibleAt) ||
				(evaluation.EligibleAt.Equal(deferred.evaluation.EligibleAt) && candidate.Rank < deferred.candidate.Rank) {
				deferred = &queuedSelection{
					candidate:          candidate,
					evaluation:         evaluation,
					resourceEligibleAt: resourceEligibleAt,
					userEligibleAt:     userEligibleAt,
					userLimitReason:    userLimitReason,
					deferScope:         deferScope,
					deferScopeID:       deferScopeID,
					deferReason:        deferReason,
					limitScopes:        append([]limits.ScopeRef(nil), limitScopes...),
					reservationScopes:  append([]limits.ScopeRef(nil), reservationScopes...),
					estimatedCost:      estimate.CostMicros,
					estimatedInput:     candidateEstimatedIn,
					estimatedOutput:    candidateEstimatedOut,
					retry:              retry,
					tracePrefix:        append([]CandidateTrace(nil), trace[:len(trace)-1]...),
					candidateIndex:     candidateIndex,
				}
			}
			continue
		}

		selected = &queuedSelection{
			candidate:          candidate,
			evaluation:         evaluation,
			resourceEligibleAt: resourceEligibleAt,
			userEligibleAt:     userEligibleAt,
			userLimitReason:    userLimitReason,
			deferScope:         deferScope,
			deferScopeID:       deferScopeID,
			deferReason:        deferReason,
			limitScopes:        append([]limits.ScopeRef(nil), limitScopes...),
			reservationScopes:  append([]limits.ScopeRef(nil), reservationScopes...),
			estimatedCost:      estimate.CostMicros,
			estimatedInput:     candidateEstimatedIn,
			estimatedOutput:    candidateEstimatedOut,
			retry:              retry,
			tracePrefix:        append([]CandidateTrace(nil), trace...),
			candidateIndex:     candidateIndex,
		}
		break
	}
	if selected == nil && deferred != nil {
		selected = deferred
	}
	if selected == nil {
		if bestRejection.Reason == "" {
			bestRejection = Rejection{Reason: "no candidate accepted", RetryAfter: time.Second}
		}
		bestRejection.CandidateTrace = append([]CandidateTrace(nil), trace...)
		return nil, bestRejection
	}
	return selected, nil
}

func (s *Scheduler) ReevaluateLimitScopes(ctx context.Context, refs ...limits.ScopeRef) error {
	if s.isRegistry() {
		// Limit-policy mutations arrive with the signed organization scope that
		// owns both the policy and its in-memory scheduler. Target that runtime
		// directly. Passing one tenant's context through every runtime can query
		// the wrong tenant-scoped catalog and delays the runtime that actually has
		// the queued work.
		if runtime, err := s.runtimeForContext(ctx, false); err == nil {
			scope, _ := tenancy.ScopeFromContext(ctx)
			slog.Info("Relay limit policy runtime refresh",
				"organization_uuid", scope.OrganizationUUID,
				"runtime_found", runtime != nil,
				"runtime_count", s.RuntimeCount(),
				"scope_count", len(refs),
			)
			if runtime == nil {
				return nil
			}
			return runtime.ReevaluateLimitScopes(ctx, refs...)
		} else if !errors.Is(err, ErrPartitionKeyRequired) {
			return err
		}
		// Internal callers without a tenant scope intentionally retain the old
		// all-runtime behavior (for example a process-wide settings refresh).
		for _, runtime := range s.runtimeList() {
			if err := runtime.ReevaluateLimitScopes(ctx, refs...); err != nil {
				return err
			}
		}
		return nil
	}
	if s == nil || s.store == nil || len(refs) == 0 {
		return nil
	}
	endpointIDs, err := s.endpointIDsForLimitScopes(ctx, refs)
	if err != nil {
		return err
	}
	if len(endpointIDs) == 0 {
		return nil
	}

	s.admissionMu.Lock()

	now := time.Now().UTC()
	affected := make([]*task, 0)
	skip := make(map[*task]bool)
	touched := make(map[uint]bool)
	var reevaluationErr error

	s.mu.Lock()
	for _, item := range s.tasks {
		if item.cancelled || (item.state != "waiting" && item.state != "ready") {
			continue
		}
		// A policy edit can make any route candidate preferable immediately,
		// not only the endpoint selected when the request first entered the
		// queue. Re-evaluate the whole request when any candidate belongs to an
		// affected scope so an alternate model that just regained capacity can
		// release queued work without waiting for the old selection's timer.
		if !taskHasCandidateEndpoint(item, endpointIDs) {
			continue
		}
		affected = append(affected, item)
		skip[item] = true
		touched[item.candidate.Endpoint.ID] = true
		item.state = "recomputing"
	}
	if len(affected) > 0 {
		s.rebuildQueuesLocked(skip)
		s.rebuildGroupFIFOTailsLocked(skip)
	}
	s.mu.Unlock()

	sort.Slice(affected, func(i, j int) bool {
		if !affected[i].enqueuedAt.Equal(affected[j].enqueuedAt) {
			return affected[i].enqueuedAt.Before(affected[j].enqueuedAt)
		}
		return affected[i].seq < affected[j].seq
	})

	for _, item := range affected {
		s.tracker.Release(item.id)
	}

	for _, item := range affected {
		candidates := item.candidates
		if len(candidates) == 0 {
			candidates = []router.Candidate{item.candidate}
		}
		meta := item.meta
		meta.EstimatedInputTokens = item.estimatedInput
		meta.EstimatedOutputTokens = item.estimatedOutput
		req := SubmitRequest{
			Meta:            meta,
			Candidates:      candidates,
			EnqueuedAt:      item.enqueuedAt,
			TrustedMetadata: cloneMetadata(item.trustedMetadata),
			Preview:         item.preview,
		}
		waitBudget := meta.MaxWaitMS
		if waitBudget <= 0 {
			waitBudget = s.defaultWaitMS
		}
		selection, err := s.selectCandidateWithOptions(ctx, req, now, waitBudget, true)
		if err != nil {
			// Do not pretend the policy refresh succeeded while retaining the old
			// eligibility deadline. Requeue the task safely, but surface the error
			// to the mutating API so the operator can retry instead of watching a
			// stale cooldown until its original timer expires.
			reevaluationErr = errors.Join(reevaluationErr, fmt.Errorf("re-evaluate request %s after limit change: %w", item.meta.RequestID, err))
			selection = &queuedSelection{
				candidate:          item.candidate,
				evaluation:         item.evaluation,
				resourceEligibleAt: item.resourceEligibleAt,
				userEligibleAt:     item.userEligibleAt,
				userLimitReason:    item.userLimitReason,
				deferScope:         item.deferScope,
				deferScopeID:       item.deferScopeID,
				deferReason:        item.deferReason,
				limitScopes:        append([]limits.ScopeRef(nil), item.limitScopes...),
				reservationScopes:  append([]limits.ScopeRef(nil), item.reservationScopes...),
				estimatedCost:      item.estimatedCost,
				estimatedInput:     item.estimatedInput,
				estimatedOutput:    item.estimatedOutput,
				retry:              time.Until(item.evaluation.EligibleAt),
				candidateIndex:     0,
			}
		} else {
			s.applyGroupFIFOSelection(req, selection, now, false)
		}
		if selection.retry < 0 {
			selection.retry = 0
		}

		touched[item.candidate.Endpoint.ID] = true
		touched[selection.candidate.Endpoint.ID] = true
		s.mu.Lock()
		item.candidate = selection.candidate
		item.evaluation = selection.evaluation
		item.resourceEligibleAt = selection.resourceEligibleAt
		item.userEligibleAt = selection.userEligibleAt
		item.userLimitReason = selection.userLimitReason
		item.deferScope = selection.deferScope
		item.deferScopeID = selection.deferScopeID
		item.deferReason = selection.deferReason
		item.limitScopes = append([]limits.ScopeRef(nil), selection.limitScopes...)
		item.reservationScopes = append([]limits.ScopeRef(nil), selection.reservationScopes...)
		item.estimatedCost = selection.estimatedCost
		item.estimatedInput = selection.estimatedInput
		item.estimatedOutput = selection.estimatedOutput
		item.candidateTrace = candidateTrace(selection, candidates)
		if groupKey := candidateGroupKey(item.meta, item.candidate); groupKey != "" {
			eligibleAt := selection.evaluation.EligibleAt.UTC()
			if eligibleAt.IsZero() || eligibleAt.Before(now) {
				eligibleAt = now
			}
			if eligibleAt.After(s.groupFIFOTails[groupKey]) {
				s.groupFIFOTails[groupKey] = eligibleAt
			}
		}
		s.mu.Unlock()

		if s.shouldReserveOnEnqueue(item) {
			s.reserveUsageEstimates(item.id, taskScopes(item), selection.estimatedInput+selection.estimatedOutput, selection.estimatedCost, selection.evaluation.EligibleAt)
		}

		s.mu.Lock()
		if selection.evaluation.EligibleAt.After(now) {
			item.state = "waiting"
			heap.Push(&s.delayed, item)
		} else {
			item.state = "ready"
			heap.Push(&s.ready, item)
		}
		s.mu.Unlock()
		s.emitState(item)
	}

	for endpointID := range endpointIDs {
		touched[endpointID] = true
	}
	for endpointID := range touched {
		s.syncEndpointHealthWithOptions(endpointID, true)
	}
	// syncEndpointHealthWithOptions persists the newly-authoritative endpoint
	// state, but queued tasks keep endpoint values captured when they entered the
	// scheduler. Refresh only the candidates touched by this policy mutation so
	// normal dispatch cannot put a newly-ready task back behind its old cached
	// cooldown. This is deliberately local to control-plane policy changes;
	// ordinary proxy requeues must continue preserving upstream 429/5xx cooldown
	// metadata until that cooldown actually expires.
	if err := s.refreshQueuedEndpointSnapshots(ctx, affected, touched); err != nil {
		reevaluationErr = errors.Join(reevaluationErr, err)
	}
	readyCount := 0
	waitingCount := 0
	s.mu.Lock()
	for _, item := range affected {
		switch item.state {
		case "ready":
			readyCount++
		case "waiting":
			waitingCount++
		}
	}
	s.mu.Unlock()
	slog.Info("Relay queued work refreshed after limit policy change",
		"affected_tasks", len(affected),
		"affected_endpoints", len(endpointIDs),
		"ready_tasks", readyCount,
		"waiting_tasks", waitingCount,
		"error", reevaluationErr,
	)
	s.admissionMu.Unlock()

	// A policy edit is an explicit control-plane wake-up. Promote and dispatch
	// synchronously so a newly eligible request leaves the delayed heap before
	// the update request returns; the regular wake-up remains for concurrent
	// work and future timers.
	s.signal()
	s.promoteReady()
	s.dispatchReady()
	return reevaluationErr
}

func (s *Scheduler) refreshQueuedEndpointSnapshots(ctx context.Context, items []*task, endpointIDs map[uint]bool) error {
	if s == nil || s.store == nil || len(items) == 0 || len(endpointIDs) == 0 {
		return nil
	}
	snapshots := make(map[uint]models.Endpoint, len(endpointIDs))
	var refreshErr error
	for endpointID := range endpointIDs {
		if endpointID == 0 {
			continue
		}
		var endpoint models.Endpoint
		if err := s.store.FindByID(ctx, &endpoint, endpointID); err != nil {
			refreshErr = errors.Join(refreshErr, fmt.Errorf("refresh endpoint %d after limit change: %w", endpointID, err))
			continue
		}
		snapshots[endpointID] = endpoint
	}
	if len(snapshots) == 0 {
		return refreshErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		if item == nil || item.cancelled {
			continue
		}
		if endpoint, ok := snapshots[item.candidate.Endpoint.ID]; ok {
			item.candidate.Endpoint = endpoint
		}
		for i := range item.candidates {
			if endpoint, ok := snapshots[item.candidates[i].Endpoint.ID]; ok {
				item.candidates[i].Endpoint = endpoint
			}
		}
	}
	return refreshErr
}

func taskHasCandidateEndpoint(item *task, endpointIDs map[uint]bool) bool {
	if item == nil || len(endpointIDs) == 0 {
		return false
	}
	if endpointIDs[item.candidate.Endpoint.ID] {
		return true
	}
	for _, candidate := range item.candidates {
		if endpointIDs[candidate.Endpoint.ID] {
			return true
		}
	}
	return false
}

// ReevaluateExternalLimits forces queued work through the current external limit
// hooks. Extensions use this after mutating their own limit policies so tasks
// deferred by request-scoped limits do not sleep until a stale retry time.
func (s *Scheduler) ReevaluateExternalLimits(ctx context.Context) error {
	if s.isRegistry() {
		for _, runtime := range s.runtimeList() {
			if err := runtime.ReevaluateExternalLimits(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	now := time.Now().UTC()
	affected := make([]*task, 0)
	skip := make(map[*task]bool)
	touched := make(map[uint]bool)

	s.mu.Lock()
	for _, item := range s.tasks {
		if item.cancelled || (item.state != "waiting" && item.state != "ready") {
			continue
		}
		affected = append(affected, item)
		skip[item] = true
		touched[item.candidate.Endpoint.ID] = true
		item.state = "recomputing"
	}
	if len(affected) > 0 {
		s.rebuildQueuesLocked(skip)
	}
	s.mu.Unlock()

	if len(affected) == 0 {
		return nil
	}

	sort.Slice(affected, func(i, j int) bool {
		if !affected[i].enqueuedAt.Equal(affected[j].enqueuedAt) {
			return affected[i].enqueuedAt.Before(affected[j].enqueuedAt)
		}
		return affected[i].seq < affected[j].seq
	})

	for _, item := range affected {
		s.tracker.Release(item.id)
	}

	for _, item := range affected {
		candidates := item.candidates
		if len(candidates) == 0 {
			candidates = []router.Candidate{item.candidate}
		}
		meta := item.meta
		meta.EstimatedInputTokens = item.estimatedInput
		meta.EstimatedOutputTokens = item.estimatedOutput
		req := SubmitRequest{
			Meta:            meta,
			Candidates:      candidates,
			EnqueuedAt:      item.enqueuedAt,
			TrustedMetadata: cloneMetadata(item.trustedMetadata),
			Preview:         item.preview,
		}
		waitBudget := meta.MaxWaitMS
		if waitBudget <= 0 {
			waitBudget = s.defaultWaitMS
		}
		selection, err := s.selectCandidateWithOptions(ctx, req, now, waitBudget, true)
		if err != nil {
			selection = &queuedSelection{
				candidate:          item.candidate,
				evaluation:         item.evaluation,
				resourceEligibleAt: item.resourceEligibleAt,
				userEligibleAt:     item.userEligibleAt,
				userLimitReason:    item.userLimitReason,
				deferScope:         item.deferScope,
				deferScopeID:       item.deferScopeID,
				deferReason:        item.deferReason,
				limitScopes:        append([]limits.ScopeRef(nil), item.limitScopes...),
				reservationScopes:  append([]limits.ScopeRef(nil), item.reservationScopes...),
				estimatedCost:      item.estimatedCost,
				estimatedInput:     item.estimatedInput,
				estimatedOutput:    item.estimatedOutput,
				retry:              time.Until(item.evaluation.EligibleAt),
				candidateIndex:     0,
			}
		} else {
			s.applyGroupFIFOSelection(req, selection, now, false)
		}
		if selection.retry < 0 {
			selection.retry = 0
		}

		touched[item.candidate.Endpoint.ID] = true
		touched[selection.candidate.Endpoint.ID] = true
		s.mu.Lock()
		item.candidate = selection.candidate
		item.evaluation = selection.evaluation
		item.resourceEligibleAt = selection.resourceEligibleAt
		item.userEligibleAt = selection.userEligibleAt
		item.userLimitReason = selection.userLimitReason
		item.deferScope = selection.deferScope
		item.deferScopeID = selection.deferScopeID
		item.deferReason = selection.deferReason
		item.limitScopes = append([]limits.ScopeRef(nil), selection.limitScopes...)
		item.reservationScopes = append([]limits.ScopeRef(nil), selection.reservationScopes...)
		item.estimatedCost = selection.estimatedCost
		item.estimatedInput = selection.estimatedInput
		item.estimatedOutput = selection.estimatedOutput
		item.candidateTrace = candidateTrace(selection, candidates)
		if groupKey := candidateGroupKey(item.meta, item.candidate); groupKey != "" {
			eligibleAt := selection.evaluation.EligibleAt.UTC()
			if eligibleAt.IsZero() || eligibleAt.Before(now) {
				eligibleAt = now
			}
			if eligibleAt.After(s.groupFIFOTails[groupKey]) {
				s.groupFIFOTails[groupKey] = eligibleAt
			}
		}
		s.mu.Unlock()

		if !isRequestScopedDefer(item.deferScope) {
			s.reserveUsageEstimates(item.id, taskScopes(item), selection.estimatedInput+selection.estimatedOutput, selection.estimatedCost, selection.evaluation.EligibleAt)
		}

		s.mu.Lock()
		if selection.evaluation.EligibleAt.After(now) {
			item.state = "waiting"
			heap.Push(&s.delayed, item)
		} else {
			item.state = "ready"
			heap.Push(&s.ready, item)
		}
		s.mu.Unlock()
		s.emitState(item)
	}

	for endpointID := range touched {
		s.syncEndpointHealthWithOptions(endpointID, true)
	}
	s.signal()
	return nil
}

func (s *Scheduler) endpointIDsForLimitScopes(ctx context.Context, refs []limits.ScopeRef) (map[uint]bool, error) {
	endpoints, err := s.store.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	memberships, err := s.store.ListLaneMemberships(ctx)
	if err != nil {
		return nil, err
	}
	laneEndpointIDs := make(map[uint][]uint)
	for _, membership := range memberships {
		if !membership.Enabled {
			continue
		}
		laneEndpointIDs[membership.LaneID] = append(laneEndpointIDs[membership.LaneID], membership.EndpointID)
	}

	ids := make(map[uint]bool)
	for _, ref := range refs {
		switch ref.Type {
		case models.ScopeGlobal:
			for _, endpoint := range endpoints {
				ids[endpoint.ID] = true
			}
		case models.ScopeProvider:
			for _, endpoint := range endpoints {
				if endpoint.ProviderID == ref.ID {
					ids[endpoint.ID] = true
				}
			}
		case models.ScopeEndpoint:
			if ref.ID != 0 {
				ids[ref.ID] = true
			}
		case models.ScopeLane:
			for _, endpointID := range laneEndpointIDs[ref.ID] {
				ids[endpointID] = true
			}
		}
	}
	return ids, nil
}

func (s *Scheduler) applyGroupFIFOSelection(req SubmitRequest, selection *queuedSelection, now time.Time, dryRun bool) {
	if isRequestScopedDefer(selection.deferScope) {
		return
	}
	key := candidateGroupKey(req.Meta, selection.candidate)
	if key == "" {
		return
	}

	tail := s.groupFIFOTail(key, now, dryRun)
	if tail.After(selection.evaluation.EligibleAt) {
		selection.evaluation.EligibleAt = tail.UTC()
		selection.retry = selection.evaluation.EligibleAt.Sub(now)
		if selection.retry < 0 {
			selection.retry = 0
		}
		if selection.evaluation.Reason == "" {
			selection.evaluation.Reason = "group queue"
		}
	}

	if dryRun {
		tailEligibleAt := selection.evaluation.EligibleAt
		if tailEligibleAt.IsZero() || !tailEligibleAt.After(now) {
			return
		}
		s.mu.Lock()
		if current := s.dryRunLaneTails[key]; current.IsZero() || tailEligibleAt.After(current) {
			s.dryRunLaneTails[key] = tailEligibleAt.UTC()
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) groupFIFOTail(key string, now time.Time, dryRun bool) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	tail := s.groupFIFOTails[key]
	// Restored/test-injected tasks may predate the incremental index. Bootstrap
	// that one group once; normal enqueues keep groupQueued and the tail current.
	if tail.IsZero() && s.groupQueued[key] == 0 {
		for _, item := range s.tasks {
			if item == nil || item.cancelled || isRequestScopedDefer(item.deferScope) || candidateGroupKey(item.meta, item.candidate) != key {
				continue
			}
			if item.state != "waiting" && item.state != "ready" {
				continue
			}
			eligibleAt := item.evaluation.EligibleAt.UTC()
			if eligibleAt.After(tail) {
				tail = eligibleAt
			}
		}
	}
	if dryRun {
		if dryRunTail := s.dryRunLaneTails[key]; dryRunTail.After(tail) {
			tail = dryRunTail
		}
	}
	if tail.Before(now) {
		return now
	}
	return tail
}

func (s *Scheduler) enqueueTask(ctx context.Context, now time.Time, meta router.RequestMeta, trustedMetadata map[string]string, partitionKey string, preview bool, selection *queuedSelection, candidates []router.Candidate, characterizationHandle *characterization.Handle) *task {
	meta.Overrides = routingOverrides(meta.Overrides, selection.candidate.RoutingMetadata)
	s.mu.Lock()
	s.seq++
	task := &task{
		id:                 uuid.NewString(),
		meta:               meta,
		trustedMetadata:    cloneMetadata(trustedMetadata),
		candidate:          selection.candidate,
		candidates:         append([]router.Candidate(nil), candidates...),
		evaluation:         selection.evaluation,
		resourceEligibleAt: selection.resourceEligibleAt,
		userEligibleAt:     selection.userEligibleAt,
		userLimitReason:    selection.userLimitReason,
		deferScope:         selection.deferScope,
		deferScopeID:       selection.deferScopeID,
		deferReason:        selection.deferReason,
		limitScopes:        append([]limits.ScopeRef(nil), selection.limitScopes...),
		reservationScopes:  append([]limits.ScopeRef(nil), selection.reservationScopes...),
		estimatedInput:     selection.estimatedInput,
		estimatedOutput:    selection.estimatedOutput,
		estimatedCost:      selection.estimatedCost,
		enqueuedAt:         now,
		seq:                s.seq,
		state:              "queued",
		candidateTrace:     candidateTrace(selection, candidates),
		readyCh:            make(chan Permit, 1),
		errCh:              make(chan error, 1),
		preview:            preview,
		partitionKey:       partitionKey,
		catalogGeneration:  s.store.CatalogGeneration(ctx),
		spanContext:        trace.SpanContextFromContext(ctx),
		characterization:   characterizationHandle,
	}
	s.tasks[task.id] = task
	if groupKey := candidateGroupKey(task.meta, task.candidate); groupKey != "" {
		s.groupQueued[groupKey]++
		queue := s.groupTasks[groupKey]
		if queue == nil {
			queue = newGroupTaskQueue()
			s.groupTasks[groupKey] = queue
		}
		queue.append(task.id)
		eligibleAt := selection.evaluation.EligibleAt.UTC()
		if eligibleAt.IsZero() || eligibleAt.Before(now) {
			eligibleAt = now
		}
		if eligibleAt.After(s.groupFIFOTails[groupKey]) {
			s.groupFIFOTails[groupKey] = eligibleAt
		}
	}
	if selection.retry == 0 {
		task.state = "ready"
		heap.Push(&s.ready, task)
	} else {
		task.state = "waiting"
		heap.Push(&s.delayed, task)
	}
	// Once the scheduler mutex is released, a ready task may be claimed by a
	// dispatcher immediately (the dispatcher also polls independently of the
	// wakeup signal below). Capture every value needed by enqueue-side work
	// while the task is still protected instead of reading the live task after
	// publication.
	reserveOnEnqueue := s.shouldReserveOnEnqueueLocked(task)
	reservationScopes := taskScopes(task)
	waiting := task.state == "waiting"
	endpointID := task.candidate.Endpoint.ID
	s.mu.Unlock()

	if reserveOnEnqueue {
		s.reserveUsageEstimates(task.id, reservationScopes, selection.estimatedInput+selection.estimatedOutput, selection.estimatedCost, selection.evaluation.EligibleAt)
	}

	s.emitState(task)
	s.signal()
	if waiting {
		s.syncEndpointHealth(endpointID)
	}
	return task
}

func (s *Scheduler) Complete(ctx context.Context, permit Permit, statusCode int, actualIn, actualOut int64) error {
	return s.completeAt(ctx, permit, statusCode, actualIn, actualOut, nil, time.Now().UTC(), s.persistState)
}

func (s *Scheduler) CompleteAt(ctx context.Context, permit Permit, statusCode int, actualIn, actualOut int64, finishedAt time.Time) error {
	return s.completeAt(ctx, permit, statusCode, actualIn, actualOut, nil, finishedAt, s.persistState)
}

// CompleteAtWithCost avoids resolving the same endpoint pricing policy twice
// when the proxy already calculated the authoritative response cost.
func (s *Scheduler) CompleteAtWithCost(ctx context.Context, permit Permit, statusCode int, actualIn, actualOut, actualCost int64, finishedAt time.Time) error {
	return s.completeAt(ctx, permit, statusCode, actualIn, actualOut, &actualCost, finishedAt, s.persistState)
}

func (s *Scheduler) completeAt(ctx context.Context, permit Permit, statusCode int, actualIn, actualOut int64, actualCost *int64, finishedAt time.Time, persist bool) error {
	if s.isRegistry() {
		runtime := s.runtimeForTask(permit.TaskID)
		if runtime == nil {
			return errors.New("task not found")
		}
		err := runtime.completeAt(ctx, permit, statusCode, actualIn, actualOut, actualCost, finishedAt, persist)
		if err == nil {
			s.forgetTask(permit.TaskID)
		}
		return err
	}
	s.mu.Lock()
	item := s.tasks[permit.TaskID]
	coordinatorKeys := taskCoordinatorKeys(item)
	s.mu.Unlock()
	if item == nil {
		return nil
	}
	lockCtx, lockSpan := observability.Tracer().Start(ctx, "relay.scheduler.complete_lock_wait")
	lockStarted := time.Now()
	unlock := s.coordinator.lock(coordinatorKeys...)
	lockSpan.SetAttributes(attribute.Int64("anchorshell.relay.lock_wait_ms", time.Since(lockStarted).Milliseconds()))
	lockSpan.End()
	criticalCtx, criticalSpan := observability.Tracer().Start(lockCtx, "relay.scheduler.complete_critical_section")
	post, err := s.complete(criticalCtx, permit, statusCode, actualIn, actualOut, actualCost, persist, finishedAt)
	criticalSpan.End()
	unlock()
	if err != nil {
		return err
	}
	if post == nil {
		return nil
	}
	s.publishCompletion(post)
	post.releaseTaskGraph()
	s.dispatchSerialReady()
	if post.persistDeferred && s.completionAsync {
		if err := s.enqueueCompletionPost(ctx, post); err != nil {
			return err
		}
		return nil
	}
	if err := s.persistCompletionPost(ctx, post); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) CompleteSynthetic(ctx context.Context, permit Permit, statusCode int, actualIn, actualOut int64) error {
	if s.isRegistry() {
		runtime := s.runtimeForTask(permit.TaskID)
		if runtime == nil {
			return errors.New("task not found")
		}
		err := runtime.CompleteSynthetic(ctx, permit, statusCode, actualIn, actualOut)
		if err == nil {
			s.forgetTask(permit.TaskID)
		}
		return err
	}
	return s.completeAt(ctx, permit, statusCode, actualIn, actualOut, nil, time.Now().UTC(), false)
}

func terminalUsageCommitValues(taskState string, statusCode int, totalTokens, actualCostMicros int64) (requests, tokens, spend int64) {
	if !models.CountsAsUsage(taskState, statusCode, totalTokens, 0, 0, actualCostMicros) {
		return 0, 0, 0
	}
	requests = 1
	if totalTokens > 0 {
		tokens = totalTokens
	}
	if actualCostMicros > 0 {
		spend = actualCostMicros
	}
	return requests, tokens, spend
}

func (s *Scheduler) AttachCancel(taskID string, cancel context.CancelFunc) bool {
	if s.isRegistry() {
		runtime := s.runtimeForTask(taskID)
		return runtime != nil && runtime.AttachCancel(taskID, cancel)
	}
	if cancel == nil {
		return false
	}
	s.mu.Lock()
	task, ok := s.tasks[taskID]
	if ok && !task.cancelled && task.state == "in_flight" {
		task.activeCancel = cancel
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	cancel()
	return false
}

func (s *Scheduler) DetachCancel(taskID string) {
	if s.isRegistry() {
		if runtime := s.runtimeForTask(taskID); runtime != nil {
			runtime.DetachCancel(taskID)
		}
		return
	}
	s.mu.Lock()
	if task, ok := s.tasks[taskID]; ok {
		task.activeCancel = nil
	}
	s.mu.Unlock()
}

func (s *Scheduler) IsActive(taskID string) bool {
	if s.isRegistry() {
		runtime := s.runtimeForTask(taskID)
		return runtime != nil && runtime.IsActive(taskID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	return ok && !task.cancelled
}

type completionPost struct {
	characterization *characterization.Handle
	task             *task
	log              models.RequestLog
	trustedMetadata  map[string]string
	spanContext      trace.SpanContext
	candidateTrace   string
	limitImpact      string
	appliedOverrides string
	policies         []models.LimitPolicy
	commitScopes     []limits.ScopeRef
	persist          bool
	persistDeferred  bool
	commitRequests   int64
	commitTokens     int64
	commitSpend      int64
	started          time.Time
	finished         time.Time
}

func (post *completionPost) releaseTaskGraph() {
	if post == nil {
		return
	}
	post.task = nil
}

func (s *Scheduler) complete(ctx context.Context, permit Permit, statusCode int, actualIn, actualOut int64, actualCostOverride *int64, persist bool, finishedAt time.Time) (*completionPost, error) {
	s.mu.Lock()
	task, ok := s.tasks[permit.TaskID]
	s.mu.Unlock()
	if !ok {
		return nil, nil
	}
	now := finishedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	started := permit.SelectedAt.UTC()
	if started.IsZero() {
		started = task.evaluation.EligibleAt
		if started.IsZero() {
			started = now.Add(-permit.Waited)
		}
	}
	successfulStatus := statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices
	actualTokensProvided := actualIn > 0 || actualOut > 0
	if successfulStatus && !actualTokensProvided {
		actualIn = task.estimatedInput
		actualOut = task.estimatedOutput
	}
	totalTokens := actualIn + actualOut
	actualCost := int64(0)
	if actualCostOverride != nil {
		actualCost = max(*actualCostOverride, 0)
	} else {
		if successfulStatus {
			actualCost = task.estimatedCost
		}
		if (successfulStatus || actualTokensProvided) && s.store != nil {
			pricingCtx, pricingSpan := observability.Tracer().Start(ctx, "relay.pricing.resolve")
			if policy, err := s.store.GetPricingByEndpoint(pricingCtx, task.candidate.Endpoint.ID); err == nil {
				actualCost = pricing.ReconcileCost(policy, actualIn, actualOut)
			}
			pricingSpan.End()
		}
	}
	terminalBaseCtx, cancelTerminal := terminalPersistenceContext(ctx)
	defer cancelTerminal()
	terminalCtx, terminalSpan := observability.Tracer().Start(terminalBaseCtx, "relay.request.terminal_persist")
	err := s.finalizeCompletedWithOptionalHandoff(terminalCtx, task, statusCode, actualIn, actualOut, actualCost, started, now)
	if err != nil {
		terminalSpan.SetStatus(codes.Error, "terminal state persistence failed")
	}
	terminalSpan.End()
	if err != nil {
		s.tracker.Release(task.id)
		s.mu.Lock()
		task.state = "failed"
		s.mu.Unlock()
		s.emitState(task)
		return nil, err
	}
	reservationScopes := taskReservationScopes(task)
	s.tracker.AddConcurrency(reservationScopes, -1)
	commitScopes := completionLimitScopes(task, permit.AppliedLimitState)
	if !persist {
		commitScopes = taskReservationScopes(task)
	}
	commitRequests, commitTokens, commitSpend := terminalUsageCommitValues("completed", statusCode, totalTokens, actualCost)
	usageCtx, usageSpan := observability.Tracer().Start(ctx, "relay.usage.commit")
	if commitRequests > 0 || commitTokens > 0 || commitSpend > 0 {
		if err := s.tracker.Commit(usageCtx, task.id, commitScopes, commitRequests, commitTokens, commitSpend, started); err != nil {
			usageSpan.SetStatus(codes.Error, "usage commit failed")
			usageSpan.End()
			return nil, err
		}
	} else {
		s.tracker.Release(task.id)
	}
	usageSpan.End()

	finished := now
	waitMS := permit.Waited.Milliseconds()
	latencyMS := finished.Sub(started).Milliseconds()
	log := models.RequestLog{
		RequestID:             task.meta.RequestID,
		ActorID:               actorIDFromMetadata(task.trustedMetadata),
		RouteKind:             task.meta.RouteKind,
		IncomingModel:         task.meta.IncomingModel,
		SelectedUpstreamModel: task.candidate.Endpoint.UpstreamModel,
		StatusCode:            statusCode,
		TaskState:             "completed",
		QueuedAt:              &task.enqueuedAt,
		StartedAt:             &started,
		FinishedAt:            &finished,
		WaitMS:                waitMS,
		LatencyMS:             latencyMS,
		Streaming:             task.meta.Streaming,
		Priority:              task.meta.Priority,
		FallbackCount:         task.candidate.FallbackCount,
		EstimatedInputTokens:  task.estimatedInput,
		EstimatedOutputTokens: task.estimatedOutput,
		ActualInputTokens:     actualIn,
		ActualOutputTokens:    actualOut,
		ActualTotalTokens:     totalTokens,
		EstimatedCostMicros:   task.estimatedCost,
		ActualCostMicros:      actualCost,
		CreatedAt:             task.enqueuedAt,
		UpdatedAt:             finished,
	}
	applyCharacterizationToLog(&log, task.characterization)
	applyGuardrailSummaryToLog(&log, task.guardrailSummary)
	setRequestLogCandidate(&log, task.candidate)
	post := &completionPost{
		characterization: task.characterization,
		task:             task,
		log:              log,
		trustedMetadata:  cloneMetadata(task.trustedMetadata),
		spanContext:      task.spanContext,
		candidateTrace:   marshalJSON(task.candidateTrace),
		limitImpact:      marshalJSON(permit.AppliedLimitState),
		appliedOverrides: marshalJSON(task.meta.Overrides),
		policies:         completionConfiguredPolicies(task, permit.AppliedLimitState),
		commitScopes:     append([]limits.ScopeRef(nil), commitScopes...),
		persist:          persist && s.store != nil,
		persistDeferred:  persist && s.store != nil && s.authoritativeFinalization,
		commitRequests:   commitRequests,
		commitTokens:     commitTokens,
		commitSpend:      commitSpend,
		started:          started,
		finished:         finished,
	}
	if post.persist && !post.persistDeferred {
		// Durable completion work must not depend on the browser/client request
		// remaining open. Use the runtime-owned tenant context so a completed
		// provider call cannot update only in-memory usage and lose it on restart.
		persistCtx, cancel := context.WithTimeout(s.completionPostContext(post), 5*time.Second)
		err := s.persistCompletionState(persistCtx, post)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	releasedAt := time.Now().UTC()
	groupKey := candidateGroupKey(task.meta, task.candidate)
	s.mu.Lock()
	s.removeTaskLocked(task)
	task.state = "completed"
	if groupKey != "" && s.hasPendingGroupTaskLocked(groupKey) {
		s.groupReleasedAt[groupKey] = releasedAt
	} else if groupKey != "" {
		delete(s.groupReleasedAt, groupKey)
	}
	s.mu.Unlock()
	return post, nil
}

func (s *Scheduler) hasPendingGroupTaskLocked(groupKey string) bool {
	return s.groupQueued[groupKey] > 0
}

func (s *Scheduler) nextReadyGroupTaskLocked(current *task, now time.Time) *task {
	if current == nil {
		return nil
	}
	key := candidateGroupKey(current.meta, current.candidate)
	if key == "" {
		return nil
	}
	queue := s.groupTasks[key]
	if queue == nil || queue.head != current.id {
		return nil
	}
	next := s.tasks[queue.next(current.id)]
	if next == nil || next.cancelled || next.state != "ready" || next.evaluation.EligibleAt.After(now) {
		return nil
	}
	return next
}

func (s *Scheduler) finalizeCompletedWithOptionalHandoff(ctx context.Context, current *task, statusCode int, actualInput, actualOutput, actualCost int64, startedAt, finishedAt time.Time) error {
	if current == nil || len(s.handoffHooks) == 0 {
		return s.finalizeRequest(ctx, current, "completed", statusCode, actualInput, actualOutput, actualCost, startedAt, finishedAt)
	}
	s.mu.Lock()
	next := s.nextReadyGroupTaskLocked(current, finishedAt)
	if next != nil && s.store != nil && next.catalogGeneration != s.store.CatalogGeneration(ctx) {
		// Never pre-admit a queued selection built from an older route catalog.
		// Normal dispatch will refresh and coordinate the new candidate instead.
		next = nil
	}
	s.mu.Unlock()
	if next == nil {
		return s.finalizeRequest(ctx, current, "completed", statusCode, actualInput, actualOutput, actualCost, startedAt, finishedAt)
	}

	input := FinalizeAndAdmitNextInput{
		Current: requestFinalizedEvent(current, "completed", statusCode, actualInput, actualOutput, actualCost, startedAt, finishedAt),
		Next:    dispatchAdmissionInputForTask(next),
	}
	for _, hook := range s.handoffHooks {
		if hook == nil {
			continue
		}
		decision, err := hook(ctx, input)
		if err != nil {
			return err
		}
		if !decision.Handled {
			continue
		}
		s.mu.Lock()
		if decision.Allowed {
			next.preAdmitted = true
		} else {
			eligibleAt := decision.EligibleAt.UTC()
			if eligibleAt.IsZero() || !eligibleAt.After(finishedAt) {
				eligibleAt = finishedAt.Add(time.Second)
			}
			next.evaluation.EligibleAt = eligibleAt
			next.evaluation.RetryAfter = eligibleAt.Sub(finishedAt)
			if decision.Reason != "" {
				next.evaluation.Reason = decision.Reason
			}
			next.state = "waiting"
			s.extendGroupFIFOTailLocked(next, eligibleAt)
			s.rebuildQueuesLocked(nil)
		}
		s.mu.Unlock()
		return nil
	}
	return s.finalizeRequest(ctx, current, "completed", statusCode, actualInput, actualOutput, actualCost, startedAt, finishedAt)
}

func (s *Scheduler) persistCompletionPost(ctx context.Context, post *completionPost) error {
	if post == nil || !post.persistDeferred {
		return nil
	}
	persistCtx, cancel := persistenceContext(ctx, s.completionPostContext(post))
	defer cancel()
	return s.persistCompletionState(persistCtx, post)
}

func (s *Scheduler) completionPostContext(post *completionPost) context.Context {
	ctx := s.backgroundContext()
	if post == nil {
		return ctx
	}
	if post.spanContext.IsValid() {
		ctx = trace.ContextWithSpanContext(ctx, post.spanContext)
	}
	return tenancy.ContextWithMetadataScope(ctx, post.trustedMetadata)
}

func (s *Scheduler) enqueueCompletionPost(ctx context.Context, post *completionPost) error {
	if post == nil {
		return nil
	}
	select {
	case <-s.stopCh:
		if err := s.persistCompletionPost(ctx, post); err != nil {
			return err
		}
		return nil
	default:
	}
	select {
	case s.completionPosts <- post:
		s.pendingPosts.Add(1)
		return nil
	default:
		// Bounded backpressure is intentional. A saturated post-processing
		// pipeline may slow the caller, but it must never discard derived state.
		if err := s.persistCompletionPost(ctx, post); err != nil {
			return err
		}
		return nil
	}
}

func (s *Scheduler) completionPostLoop() {
	defer s.completionPostWG.Done()
	process := func(post *completionPost) {
		s.pendingPosts.Add(-1)
		if post == nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.completionPostContext(post), 5*time.Second)
		defer cancel()
		ctx, span := observability.Tracer().Start(ctx, "relay.completion.post_process",
			trace.WithAttributes(attribute.Int("anchorshell.relay.post_queue_depth", len(s.completionPosts))),
		)
		if err := s.persistCompletionPost(ctx, post); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "deferred completion persistence failed")
			slog.Warn("Relay deferred completion enrichment failed", "request_id", post.log.RequestID, "error", err)
		}
		span.End()
	}
	for {
		select {
		case post := <-s.completionPosts:
			process(post)
		case <-s.stopCh:
			for {
				select {
				case post := <-s.completionPosts:
					process(post)
				default:
					return
				}
			}
		}
	}
}

func (s *Scheduler) persistCompletionState(ctx context.Context, post *completionPost) error {
	if post == nil || !post.persist || s.store == nil {
		return nil
	}
	var checkpointErr error
	if !post.persistDeferred {
		stateCtx, stateSpan := observability.Tracer().Start(ctx, "relay.limit_state.reconcile",
			trace.WithAttributes(
				attribute.Int("anchorshell.relay.limit_state.policy_count", len(post.policies)),
				attribute.Int("anchorshell.relay.limit_state.scope_count", len(post.commitScopes)),
				attribute.Int("anchorshell.relay.limit_state.database_round_trips", 1),
			),
		)
		checkpointErr = s.tracker.FlushCommittedUsageCheckpoints(
			stateCtx,
			post.commitScopes,
			post.policies,
			post.commitRequests,
			post.commitTokens,
			post.commitSpend,
			post.finished,
		)
		if checkpointErr != nil {
			stateSpan.RecordError(checkpointErr)
			stateSpan.SetStatus(codes.Error, "limit state reconciliation failed")
		}
		stateSpan.End()
	}

	log := post.log
	spanName := "relay.request_log.update"
	if post.persistDeferred {
		spanName = "relay.request_log.enrich"
	}
	requestCtx, requestSpan := observability.Tracer().Start(ctx, spanName)
	err := s.store.UpdateRequestLog(requestCtx, log.RequestID, func(stored *models.RequestLog) error {
		stored.LaneID = log.LaneID
		stored.LaneUUID = log.LaneUUID
		stored.EndpointID = log.EndpointID
		stored.EndpointUUID = log.EndpointUUID
		stored.ProviderID = log.ProviderID
		stored.ProviderUUID = log.ProviderUUID
		stored.RouteKind = log.RouteKind
		stored.IncomingModel = log.IncomingModel
		stored.SelectedUpstreamModel = log.SelectedUpstreamModel
		stored.StatusCode = log.StatusCode
		stored.TaskState = log.TaskState
		stored.QueuedAt = log.QueuedAt
		stored.StartedAt = log.StartedAt
		stored.FinishedAt = log.FinishedAt
		stored.WaitMS = log.WaitMS
		stored.LatencyMS = log.LatencyMS
		stored.Streaming = log.Streaming
		stored.Priority = log.Priority
		stored.FallbackCount = log.FallbackCount
		stored.EstimatedInputTokens = log.EstimatedInputTokens
		stored.EstimatedOutputTokens = log.EstimatedOutputTokens
		stored.ActualInputTokens = log.ActualInputTokens
		stored.ActualOutputTokens = log.ActualOutputTokens
		stored.ActualTotalTokens = log.ActualTotalTokens
		stored.EstimatedCostMicros = log.EstimatedCostMicros
		stored.ActualCostMicros = log.ActualCostMicros
		stored.PrimaryAction = log.PrimaryAction
		stored.ActionConfidence = log.ActionConfidence
		stored.CharacterizationVersion = log.CharacterizationVersion
		stored.CharacterizationJSON = log.CharacterizationJSON
		stored.GuardrailStatus = log.GuardrailStatus
		stored.GuardrailDurationMS = log.GuardrailDurationMS
		stored.GuardrailPreDurationMS = log.GuardrailPreDurationMS
		stored.GuardrailPostDurationMS = log.GuardrailPostDurationMS
		stored.GuardrailResultsJSON = log.GuardrailResultsJSON
		stored.GuardrailResponseBodiesJSON = log.GuardrailResponseBodiesJSON
		stored.CandidateTraceJSON = post.candidateTrace
		stored.LimitImpactJSON = post.limitImpact
		stored.AppliedOverridesJSON = post.appliedOverrides
		return nil
	})
	if err != nil {
		requestSpan.RecordError(err)
		requestSpan.SetStatus(codes.Error, "request log update failed")
	}
	requestSpan.End()
	if err == nil {
		s.completeCharacterizationLog(log.RequestID, post.trustedMetadata, post.characterization)
	}
	// An authoritative downstream finalizer has already reconciled the durable
	// policy state and released reservations atomically. Replaying the public
	// tracker snapshot after the next admission could overwrite newer state.
	if post.persistDeferred {
		return err
	}
	// Request-log enrichment and limit checkpoints are independent durable
	// outcomes. Always attempt both and report every failure to the caller.
	return errors.Join(checkpointErr, err)
}

func (s *Scheduler) publishCompletion(post *completionPost) {
	if post == nil || post.task == nil {
		return
	}
	s.publishCapacityUsage(post.task, post.commitRequests, post.commitTokens, post.commitSpend, post.started, post.finished)
	s.publishRequestLog(post.log, post.task.trustedMetadata)
	s.emitState(post.task)
	s.signal()
	// Committing usage can move a paced endpoint into cooldown even when there
	// is no queued request to trigger another scheduler evaluation. Publish the
	// derived health transition immediately so realtime clients do not remain
	// stale until the next request arrives. Carry the verified request
	// partition explicitly: relying only on the runtime background context can
	// produce an unscoped event that a tenant WebSocket correctly discards.
	s.syncEndpointHealthForOrganization(
		post.task.candidate.Endpoint.ID,
		organizationUUIDFromMetadata(post.task.trustedMetadata),
	)
}

func (s *Scheduler) Fail(ctx context.Context, permit Permit, statusCode int, errText string) error {
	if s.isRegistry() {
		runtime := s.runtimeForTask(permit.TaskID)
		if runtime == nil {
			return errors.New("task not found")
		}
		err := runtime.Fail(ctx, permit, statusCode, errText)
		if err == nil {
			s.forgetTask(permit.TaskID)
		}
		return err
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	s.mu.Lock()
	task, ok := s.tasks[permit.TaskID]
	if ok {
		s.removeTaskLocked(task)
		s.totals["failed"]++
	}
	s.mu.Unlock()
	if !ok {
		return nil
	}
	persistCtx, cancelPersist := persistenceContext(ctx, s.taskContext(task))
	defer cancelPersist()
	s.tracker.AddConcurrency(taskReservationScopes(task), -1)
	started := permit.SelectedAt.UTC()
	if started.IsZero() {
		started = task.evaluation.EligibleAt
	}
	s.tracker.Release(task.id)
	now := time.Now().UTC()
	if err := s.finalizeRequest(persistCtx, task, "failed", statusCode, 0, 0, 0, started, now); err != nil {
		task.state = "failed"
		s.emitState(task)
		return err
	}
	if err := s.store.UpdateRequestLog(persistCtx, task.meta.RequestID, func(log *models.RequestLog) error {
		setRequestLogCandidate(log, task.candidate)
		log.RouteKind = task.meta.RouteKind
		log.IncomingModel = task.meta.IncomingModel
		log.SelectedUpstreamModel = task.candidate.Endpoint.UpstreamModel
		log.StatusCode = statusCode
		log.TaskState = "failed"
		log.ErrorText = errText
		log.QueuedAt = &task.enqueuedAt
		log.StartedAt = &started
		log.FinishedAt = &now
		log.WaitMS = permit.Waited.Milliseconds()
		log.LatencyMS = now.Sub(started).Milliseconds()
		log.Streaming = task.meta.Streaming
		log.Priority = task.meta.Priority
		log.FallbackCount = task.candidate.FallbackCount
		log.EstimatedInputTokens = task.estimatedInput
		log.EstimatedOutputTokens = task.estimatedOutput
		log.EstimatedCostMicros = task.estimatedCost
		log.CandidateTraceJSON = marshalJSON(task.candidateTrace)
		log.LimitImpactJSON = marshalJSON(permit.AppliedLimitState)
		log.AppliedOverridesJSON = marshalJSON(task.meta.Overrides)
		applyCharacterizationToLog(log, task.characterization)
		applyGuardrailSummaryToLog(log, task.guardrailSummary)
		return nil
	}); err != nil {
		return err
	}
	eventLog := models.RequestLog{
		RequestID:             task.meta.RequestID,
		ActorID:               actorIDFromMetadata(task.trustedMetadata),
		RouteKind:             task.meta.RouteKind,
		IncomingModel:         task.meta.IncomingModel,
		SelectedUpstreamModel: task.candidate.Endpoint.UpstreamModel,
		StatusCode:            statusCode,
		TaskState:             "failed",
		QueuedAt:              &task.enqueuedAt,
		StartedAt:             &started,
		FinishedAt:            &now,
		WaitMS:                permit.Waited.Milliseconds(),
		LatencyMS:             now.Sub(started).Milliseconds(),
		Streaming:             task.meta.Streaming,
		Priority:              task.meta.Priority,
		FallbackCount:         task.candidate.FallbackCount,
		EstimatedInputTokens:  task.estimatedInput,
		EstimatedOutputTokens: task.estimatedOutput,
		EstimatedCostMicros:   task.estimatedCost,
		ErrorText:             errText,
		CreatedAt:             task.enqueuedAt,
		UpdatedAt:             now,
	}
	applyGuardrailSummaryToLog(&eventLog, task.guardrailSummary)
	applyCharacterizationToLog(&eventLog, task.characterization)
	setRequestLogCandidate(&eventLog, task.candidate)
	s.publishRequestLog(eventLog, task.trustedMetadata)
	s.completeCharacterizationLog(task.meta.RequestID, task.trustedMetadata, task.characterization)
	task.state = "failed"
	s.emitState(task)
	s.signal()
	s.syncEndpointHealth(task.candidate.Endpoint.ID)
	return nil
}

func persistenceContext(ctx context.Context, fallback context.Context) (context.Context, context.CancelFunc) {
	if ctx != nil && ctx.Err() == nil {
		return ctx, func() {}
	}
	if fallback == nil {
		fallback = context.Background()
	}
	return context.WithTimeout(fallback, 5*time.Second)
}

// terminalPersistenceContext keeps authoritative terminal state independent
// of the client connection lifetime. A streaming client may close the response
// immediately after receiving its final event, which cancels the HTTP request
// context while the scheduler is still committing completion. Preserve the
// request values and trace lineage, but replace cancellation and deadlines with
// a short, bounded persistence deadline.
func terminalPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *Scheduler) Requeue(ctx context.Context, permit Permit, wait time.Duration, reason string) (Permit, error) {
	if s.isRegistry() {
		runtime := s.runtimeForTask(permit.TaskID)
		if runtime == nil {
			return Permit{}, errors.New("task not found")
		}
		return runtime.Requeue(ctx, permit, wait, reason)
	}
	if wait <= 0 {
		wait = time.Second
	}

	now := time.Now().UTC()
	s.admissionMu.Lock()
	s.mu.Lock()
	task, ok := s.tasks[permit.TaskID]
	if !ok {
		s.mu.Unlock()
		s.admissionMu.Unlock()
		return Permit{}, errors.New("task not found")
	}
	if task.cancelled {
		s.mu.Unlock()
		s.admissionMu.Unlock()
		return Permit{}, context.Canceled
	}
	// Requeue performs database-backed limit evaluation without holding s.mu.
	// Copy every value used during that work while the scheduler owns the task;
	// all scheduler-visible mutations are committed together under s.mu below.
	meta := task.meta
	trustedMetadata := cloneMetadata(task.trustedMetadata)
	preview := task.preview
	candidate := task.candidate
	estimatedInput := task.estimatedInput
	estimatedOutput := task.estimatedOutput
	estimatedCost := task.estimatedCost
	previousEvaluation := task.evaluation
	deferScope := task.deferScope
	deferScopeID := task.deferScopeID
	deferReason := task.deferReason
	limitScopes := append([]limits.ScopeRef(nil), taskLimitScopes(task)...)
	reservationScopes := append([]limits.ScopeRef(nil), taskReservationScopes(task)...)
	laneID := taskLaneID(task)
	s.mu.Unlock()

	s.tracker.AddConcurrency(reservationScopes, -1)
	s.tracker.Release(permit.TaskID)

	retryAt := now.Add(wait).UTC()
	if candidate.Endpoint.CooldownUntil == nil || candidate.Endpoint.CooldownUntil.Before(retryAt) {
		candidate.Endpoint.CooldownUntil = &retryAt
	}
	candidate.Endpoint.HealthStatus = models.HealthCoolingDown
	s.preservePersistedCooldownSource(ctx, &candidate.Endpoint, now)

	evaluation, err := s.resolver.Evaluate(ctx, limitScopes, candidate.Endpoint, laneID, estimatedInput+estimatedOutput, estimatedCost, now)
	if err != nil {
		evaluation = limits.CandidateEvaluation{
			EligibleAt:      retryAt,
			RetryAfter:      wait,
			Reason:          reason,
			EffectiveLimits: previousEvaluation.EffectiveLimits,
		}
	}
	resourceEligibleAt := evaluation.EligibleAt
	externalEvaluation, externalScopes, externalDeferScope, externalDeferScopeID, externalDeferReason, userEligibleAt, userLimitReason, externalErr := s.evaluateExternalLimits(ctx, SubmitRequest{
		Meta:            meta,
		TrustedMetadata: trustedMetadata,
		Preview:         preview,
	}, candidate, estimatedInput+estimatedOutput, estimatedCost, now)
	nextReservationScopes := reservationScopes
	if externalErr == nil {
		if isRequestScopedDefer(externalDeferScope) {
			deferScope = externalDeferScope
			deferScopeID = externalDeferScopeID
			deferReason = externalDeferReason
		} else {
			deferScope = ""
			deferScopeID = ""
			deferReason = ""
		}
		evaluation = mergeEvaluations(evaluation, externalEvaluation, now)
		nextReservationScopes = reservationScopesForDecision(limitScopes, externalScopes, deferScope, resourceEligibleAt, now)
	}
	if evaluation.EligibleAt.Before(retryAt) {
		evaluation.EligibleAt = retryAt
	}
	evaluation.RetryAfter = evaluation.EligibleAt.Sub(now)
	if evaluation.RetryAfter < 0 {
		evaluation.RetryAfter = 0
	}
	if reason != "" {
		evaluation.Reason = reason
	}

	s.mu.Lock()
	if task.cancelled {
		s.mu.Unlock()
		s.admissionMu.Unlock()
		return Permit{}, context.Canceled
	}
	task.candidate = candidate
	task.evaluation = evaluation
	task.resourceEligibleAt = resourceEligibleAt
	if externalErr == nil {
		task.deferScope = deferScope
		task.deferScopeID = deferScopeID
		task.deferReason = deferReason
		task.userEligibleAt = userEligibleAt
		task.userLimitReason = userLimitReason
		task.reservationScopes = nextReservationScopes
	}
	task.state = "waiting"
	task.startedAt = time.Time{}
	task.substatus = ""
	task.uploadedTokens = 0
	task.downloadedTokens = 0
	s.unclaimTaskGroupLocked(task)
	s.extendGroupFIFOTailLocked(task, evaluation.EligibleAt)
	s.mu.Unlock()

	if !isRequestScopedDefer(deferScope) {
		s.reserveUsageEstimates(permit.TaskID, nextReservationScopes, estimatedInput+estimatedOutput, estimatedCost, evaluation.EligibleAt)
	}

	if s.persistState && s.store != nil {
		_ = s.store.SaveEndpointState(ctx, candidate.Endpoint)
	}

	s.mu.Lock()
	heap.Push(&s.delayed, task)
	s.mu.Unlock()
	s.emitState(task)
	s.signal()
	// Requeue persists the proxy's richer upstream reason before the task waits.
	// Force the subsequent derived-state event even when the stored cooldown
	// already matches, otherwise direct/short requeues can become invisible to
	// open realtime clients.
	s.syncEndpointHealthWithOptionsForOrganization(
		task.candidate.Endpoint.ID,
		false,
		organizationUUIDFromMetadata(trustedMetadata),
		true,
	)
	s.admissionMu.Unlock()

	select {
	case nextPermit := <-task.readyCh:
		return nextPermit, nil
	case err := <-task.errCh:
		return Permit{}, err
	case <-ctx.Done():
		_ = s.Cancel(task.id)
		return Permit{}, ctx.Err()
	}
}

// preservePersistedCooldownSource keeps the upstream failure metadata written by
// the proxy while requeue updates the scheduler's older endpoint snapshot. The
// scheduler owns the retry deadline, but it must not turn an upstream 429/5xx
// cooldown into an unlabelled generic cooldown when it persists that deadline.
func (s *Scheduler) preservePersistedCooldownSource(ctx context.Context, endpoint *models.Endpoint, now time.Time) {
	if s == nil || s.store == nil || endpoint == nil || endpoint.ID == 0 {
		return
	}
	var persisted models.Endpoint
	if err := s.store.FindByID(ctx, &persisted, endpoint.ID); err != nil || persisted.CooldownUntil == nil || !persisted.CooldownUntil.After(now) {
		return
	}
	if persisted.CooldownReason == "" && persisted.CooldownStatusCode == 0 {
		return
	}
	endpoint.CooldownReason = persisted.CooldownReason
	endpoint.CooldownStatusCode = persisted.CooldownStatusCode
}

func (s *Scheduler) Cancel(taskID string) error {
	if s.isRegistry() {
		runtime := s.runtimeForTask(taskID)
		if runtime == nil {
			return nil
		}
		err := runtime.Cancel(taskID)
		if err == nil {
			s.forgetTask(taskID)
		}
		return err
	}
	s.mu.Lock()
	pending := s.tasks[taskID]
	coordinatorKeys := taskCoordinatorKeys(pending)
	s.mu.Unlock()
	if pending == nil {
		return nil
	}
	// Completion no longer takes the organization-wide admission mutex. Use the
	// same exact group/policy guard as completion so a downstream cancellation
	// cannot race the authoritative terminal transition or double-release usage.
	unlockCoordinator := s.coordinator.lock(coordinatorKeys...)
	defer unlockCoordinator()
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	s.mu.Lock()
	task, ok := s.tasks[taskID]
	var activeCancel context.CancelFunc
	if ok {
		activeCancel = task.activeCancel
		task.activeCancel = nil
		s.removeTaskLocked(task)
		task.cancelled = true
		s.totals["cancelled"]++
	}
	s.mu.Unlock()
	if !ok {
		return nil
	}
	if activeCancel != nil {
		activeCancel()
	}
	if task.state == "in_flight" {
		s.tracker.Release(task.id)
	} else {
		s.tracker.Release(task.id)
	}
	if task.state == "in_flight" {
		s.tracker.AddConcurrency(taskReservationScopes(task), -1)
	}
	now := time.Now().UTC()
	var startedAt *time.Time
	var waitMS int64
	var latencyMS int64
	if task.state == "in_flight" && !task.startedAt.IsZero() {
		started := task.startedAt.UTC()
		startedAt = &started
		waitMS = started.Sub(task.enqueuedAt).Milliseconds()
		latencyMS = now.Sub(started).Milliseconds()
	} else {
		waitMS = now.Sub(task.enqueuedAt).Milliseconds()
	}
	cancelCtx, cancel := context.WithTimeout(s.taskContext(task), 5*time.Second)
	defer cancel()
	finalizeErr := s.finalizeRequest(cancelCtx, task, "cancelled", 499, 0, 0, 0, task.startedAtOr(now), now)
	if finalizeErr == nil && s.persistState && s.store != nil {
		_ = s.store.UpdateRequestLog(cancelCtx, task.meta.RequestID, func(log *models.RequestLog) error {
			setRequestLogCandidate(log, task.candidate)
			log.RouteKind = task.meta.RouteKind
			log.IncomingModel = task.meta.IncomingModel
			log.SelectedUpstreamModel = task.candidate.Endpoint.UpstreamModel
			log.StatusCode = 499
			log.TaskState = "cancelled"
			log.ErrorText = "client canceled request"
			log.QueuedAt = &task.enqueuedAt
			log.StartedAt = startedAt
			log.FinishedAt = &now
			log.WaitMS = waitMS
			log.LatencyMS = latencyMS
			log.Streaming = task.meta.Streaming
			log.Priority = task.meta.Priority
			log.FallbackCount = task.candidate.FallbackCount
			log.EstimatedInputTokens = task.estimatedInput
			log.EstimatedOutputTokens = task.estimatedOutput
			log.EstimatedCostMicros = task.estimatedCost
			log.CandidateTraceJSON = marshalJSON(task.candidateTrace)
			log.AppliedOverridesJSON = marshalJSON(task.meta.Overrides)
			applyCharacterizationToLog(log, task.characterization)
			return nil
		})
	}
	eventLog := models.RequestLog{
		RequestID:             task.meta.RequestID,
		ActorID:               actorIDFromMetadata(task.trustedMetadata),
		RouteKind:             task.meta.RouteKind,
		IncomingModel:         task.meta.IncomingModel,
		SelectedUpstreamModel: task.candidate.Endpoint.UpstreamModel,
		StatusCode:            499,
		TaskState:             "cancelled",
		QueuedAt:              &task.enqueuedAt,
		StartedAt:             startedAt,
		FinishedAt:            &now,
		WaitMS:                waitMS,
		LatencyMS:             latencyMS,
		Streaming:             task.meta.Streaming,
		Priority:              task.meta.Priority,
		FallbackCount:         task.candidate.FallbackCount,
		EstimatedInputTokens:  task.estimatedInput,
		EstimatedOutputTokens: task.estimatedOutput,
		EstimatedCostMicros:   task.estimatedCost,
		ErrorText:             "client canceled request",
		CreatedAt:             task.enqueuedAt,
		UpdatedAt:             now,
	}
	applyCharacterizationToLog(&eventLog, task.characterization)
	setRequestLogCandidate(&eventLog, task.candidate)
	task.state = "cancelled"
	s.publishRequestLog(eventLog, task.trustedMetadata)
	s.completeCharacterizationLog(task.meta.RequestID, task.trustedMetadata, task.characterization)
	s.emitCancelledState(task, eventLog)
	select {
	case task.errCh <- context.Canceled:
	default:
	}
	s.signal()
	s.syncEndpointHealth(task.candidate.Endpoint.ID)
	return finalizeErr
}

func (t *task) startedAtOr(fallback time.Time) time.Time {
	if !t.startedAt.IsZero() {
		return t.startedAt.UTC()
	}
	return fallback.UTC()
}

func (s *Scheduler) CancelQueued(taskID string) bool {
	if s.isRegistry() {
		runtime := s.runtimeForTask(taskID)
		ok := runtime != nil && runtime.CancelQueued(taskID)
		if ok {
			s.forgetTask(taskID)
		}
		return ok
	}
	s.mu.Lock()
	_, ok := s.tasks[taskID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	return s.Cancel(taskID) == nil
}

func (s *Scheduler) ReleasePermit(permit Permit) bool {
	if s.isRegistry() {
		runtime := s.runtimeForTask(permit.TaskID)
		ok := runtime != nil && runtime.ReleasePermit(permit)
		if ok {
			s.forgetTask(permit.TaskID)
		}
		return ok
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	s.mu.Lock()
	task, ok := s.tasks[permit.TaskID]
	if ok {
		s.removeTaskLocked(task)
		task.cancelled = true
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	s.tracker.Release(task.id)
	if task.state == "in_flight" {
		s.tracker.AddConcurrency(taskReservationScopes(task), -1)
	}
	s.signal()
	return true
}

func (s *Scheduler) Delete(taskID string) bool {
	if s.isRegistry() {
		runtime := s.runtimeForTask(taskID)
		ok := runtime != nil && runtime.Delete(taskID)
		if ok {
			s.forgetTask(taskID)
		}
		return ok
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	s.mu.Lock()
	task, ok := s.tasks[taskID]
	var activeCancel context.CancelFunc
	if ok {
		activeCancel = task.activeCancel
		task.activeCancel = nil
		s.removeTaskLocked(task)
		task.cancelled = true
		s.totals["cancelled"]++
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	if activeCancel != nil {
		activeCancel()
	}
	if task.state == "in_flight" {
		s.tracker.Release(task.id)
	} else {
		s.tracker.Release(task.id)
	}
	if task.state == "in_flight" {
		s.tracker.AddConcurrency(taskReservationScopes(task), -1)
	}
	now := time.Now().UTC()
	deleteCtx, cancel := context.WithTimeout(s.taskContext(task), 5*time.Second)
	defer cancel()
	finalizeErr := s.finalizeRequest(deleteCtx, task, "cancelled", 499, 0, 0, 0, task.startedAtOr(now), now)
	if finalizeErr == nil && s.persistState && s.store != nil {
		_ = s.store.UpdateRequestLog(deleteCtx, task.meta.RequestID, func(log *models.RequestLog) error {
			setRequestLogCandidate(log, task.candidate)
			log.RouteKind = task.meta.RouteKind
			log.IncomingModel = task.meta.IncomingModel
			log.SelectedUpstreamModel = task.candidate.Endpoint.UpstreamModel
			log.StatusCode = 499
			log.TaskState = "cancelled"
			log.ErrorText = "queue item cancelled"
			log.QueuedAt = &task.enqueuedAt
			log.FinishedAt = &now
			log.WaitMS = now.Sub(task.enqueuedAt).Milliseconds()
			log.Streaming = task.meta.Streaming
			log.Priority = task.meta.Priority
			log.FallbackCount = task.candidate.FallbackCount
			log.EstimatedInputTokens = task.estimatedInput
			log.EstimatedOutputTokens = task.estimatedOutput
			log.EstimatedCostMicros = task.estimatedCost
			log.CandidateTraceJSON = marshalJSON(task.candidateTrace)
			log.AppliedOverridesJSON = marshalJSON(task.meta.Overrides)
			applyCharacterizationToLog(log, task.characterization)
			return nil
		})
	}
	eventLog := models.RequestLog{
		RequestID:             task.meta.RequestID,
		ActorID:               actorIDFromMetadata(task.trustedMetadata),
		RouteKind:             task.meta.RouteKind,
		IncomingModel:         task.meta.IncomingModel,
		SelectedUpstreamModel: task.candidate.Endpoint.UpstreamModel,
		StatusCode:            499,
		TaskState:             "cancelled",
		QueuedAt:              &task.enqueuedAt,
		FinishedAt:            &now,
		WaitMS:                now.Sub(task.enqueuedAt).Milliseconds(),
		Streaming:             task.meta.Streaming,
		Priority:              task.meta.Priority,
		FallbackCount:         task.candidate.FallbackCount,
		EstimatedInputTokens:  task.estimatedInput,
		EstimatedOutputTokens: task.estimatedOutput,
		EstimatedCostMicros:   task.estimatedCost,
		ErrorText:             "queue item cancelled",
		CreatedAt:             task.enqueuedAt,
		UpdatedAt:             now,
	}
	applyCharacterizationToLog(&eventLog, task.characterization)
	setRequestLogCandidate(&eventLog, task.candidate)
	task.state = "cancelled"
	s.publishRequestLog(eventLog, task.trustedMetadata)
	s.completeCharacterizationLog(task.meta.RequestID, task.trustedMetadata, task.characterization)
	s.emitCancelledState(task, eventLog)
	select {
	case task.errCh <- context.Canceled:
	default:
	}
	s.signal()
	s.syncEndpointHealth(task.candidate.Endpoint.ID)
	return finalizeErr == nil
}

func (s *Scheduler) finalizeRequest(ctx context.Context, item *task, state string, statusCode int, actualInput, actualOutput, actualCost int64, startedAt, finishedAt time.Time) error {
	if item == nil || len(s.finalizedHooks) == 0 {
		return nil
	}
	ctx, span := observability.Tracer().Start(ctx, "relay.request.finalized_hooks",
		trace.WithAttributes(
			attribute.String("anchorshell.request.id", item.meta.RequestID),
			attribute.String("anchorshell.relay.terminal_state", state),
			attribute.Int("http.response.status_code", statusCode),
		),
	)
	defer span.End()
	event := requestFinalizedEvent(item, state, statusCode, actualInput, actualOutput, actualCost, startedAt, finishedAt)
	for _, hook := range s.finalizedHooks {
		if hook == nil {
			continue
		}
		if err := hook(ctx, event); err != nil {
			span.SetStatus(codes.Error, "terminal state persistence failed")
			return err
		}
	}
	return nil
}

func requestFinalizedEvent(item *task, state string, statusCode int, actualInput, actualOutput, actualCost int64, startedAt, finishedAt time.Time) RequestFinalizedEvent {
	if item == nil {
		return RequestFinalizedEvent{}
	}
	laneID := ""
	if item.candidate.Lane != nil {
		laneID = item.candidate.Lane.UUID
	}
	return RequestFinalizedEvent{
		RequestID:             item.meta.RequestID,
		TaskID:                item.id,
		RouteKind:             string(item.meta.RouteKind),
		IncomingModel:         item.meta.IncomingModel,
		Lane:                  item.meta.Lane,
		LaneID:                laneID,
		LaneStorageID:         taskLaneID(item),
		EndpointID:            item.candidate.Endpoint.UUID,
		EndpointStorageID:     item.candidate.Endpoint.ID,
		ProviderID:            item.candidate.Endpoint.ProviderUUID,
		ProviderStorageID:     item.candidate.Endpoint.ProviderID,
		UpstreamModel:         item.candidate.Endpoint.UpstreamModel,
		ReasoningEffort:       item.meta.ReasoningEffort,
		Streaming:             item.meta.Streaming,
		Priority:              item.meta.Priority,
		FallbackCount:         item.candidate.FallbackCount,
		State:                 state,
		StatusCode:            statusCode,
		EstimatedInputTokens:  item.estimatedInput,
		EstimatedOutputTokens: item.estimatedOutput,
		EstimatedCostMicros:   item.estimatedCost,
		ActualInputTokens:     actualInput,
		ActualOutputTokens:    actualOutput,
		ActualCostMicros:      actualCost,
		QueuedAt:              item.enqueuedAt.UTC(),
		StartedAt:             startedAt.UTC(),
		FinishedAt:            finishedAt.UTC(),
		WaitMS:                max(startedAt.Sub(item.enqueuedAt).Milliseconds(), 0),
		LatencyMS:             max(finishedAt.Sub(startedAt).Milliseconds(), 0),
		CandidateTraceJSON:    marshalJSON(item.candidateTrace),
		LimitImpactJSON:       marshalJSON(item.evaluation.EffectiveLimits),
		AppliedOverridesJSON:  marshalJSON(item.meta.Overrides),
		Metadata:              cloneMetadata(item.trustedMetadata),
		Characterization:      characterizationFromHandle(item.characterization),
	}
}

func (s *Scheduler) Snapshot() Snapshot {
	if s.isRegistry() {
		out := Snapshot{QueueDepthByEP: map[string]int{}, InFlightByEP: map[string]int64{}, States: map[string]int{}}
		for _, runtime := range s.runtimeList() {
			next := runtime.Snapshot()
			out.QueueDepthGlobal += next.QueueDepthGlobal
			for key, value := range next.QueueDepthByEP {
				out.QueueDepthByEP[key] += value
			}
			for key, value := range next.InFlightByEP {
				out.InFlightByEP[key] += value
			}
			for key, value := range next.States {
				out.States[key] += value
			}
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		QueueDepthGlobal: len(s.tasks),
		QueueDepthByEP:   map[string]int{},
		InFlightByEP:     map[string]int64{},
		States:           map[string]int{},
	}
	for state, count := range s.totals {
		snap.States[state] = count
	}
	for _, task := range s.tasks {
		endpointUUID := task.candidate.Endpoint.UUID
		if endpointUUID == "" {
			continue
		}
		snap.QueueDepthByEP[endpointUUID]++
		snap.States[task.state]++
		snap.InFlightByEP[endpointUUID] = s.tracker.Concurrency(limits.ScopeRef{Type: models.ScopeEndpoint, ID: task.candidate.Endpoint.ID})
	}
	return snap
}

func candidateUUIDs(endpoint models.Endpoint) []string {
	if endpoint.UUID == "" {
		return nil
	}
	return []string{endpoint.UUID}
}

func appendCandidateUUID(ids []string, endpoint models.Endpoint) []string {
	if endpoint.UUID == "" {
		return ids
	}
	return append(ids, endpoint.UUID)
}

func (s *Scheduler) CurrentRuntimeUsedForContext(ctx context.Context, scope limits.ScopeRef, metric models.Metric, period models.Period, now time.Time) int64 {
	if !s.isRegistry() {
		if s.tracker == nil {
			return 0
		}
		return s.tracker.CurrentRuntimeUsed(scope, metric, period, now)
	}
	runtime, err := s.runtimeForContext(ctx, false)
	if err != nil || runtime == nil || runtime.tracker == nil {
		return 0
	}
	return runtime.tracker.CurrentRuntimeUsed(scope, metric, period, now)
}

func (s *Scheduler) CurrentLimitPolicyStatesForContext(ctx context.Context, policies []models.LimitPolicy, now time.Time) []models.LimitPolicyState {
	if !s.isRegistry() {
		if s.tracker == nil {
			return nil
		}
		return s.tracker.CurrentPolicyStates(policies, now)
	}
	// Capacity is authoritative operational state. On the first page load after
	// a process restart, create the tenant runtime so its tracker hydrates from
	// persisted checkpoints (or performs a bounded stale-checkpoint repair)
	// instead of returning an empty runtime overlay.
	runtime, err := s.runtimeForContext(ctx, true)
	if err != nil || runtime == nil || runtime.tracker == nil {
		return nil
	}
	return runtime.tracker.CurrentPolicyStates(policies, now)
}

func (s *Scheduler) Items() []TaskSnapshot {
	if s.isRegistry() {
		var out []TaskSnapshot
		for _, runtime := range s.runtimeList() {
			out = append(out, runtime.Items()...)
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]TaskSnapshot, 0, len(s.tasks))
	now := time.Now().UTC()
	for _, task := range s.tasks {
		var startedAt *time.Time
		if !task.startedAt.IsZero() {
			ts := task.startedAt
			startedAt = &ts
		}
		items = append(items, TaskSnapshot{
			TaskID:            task.id,
			RequestID:         task.meta.RequestID,
			Lane:              task.meta.Lane,
			IncomingModel:     task.meta.IncomingModel,
			EndpointID:        task.candidate.Endpoint.ID,
			EndpointUUID:      task.candidate.Endpoint.UUID,
			EndpointName:      task.candidate.Endpoint.Name,
			UpstreamModel:     task.candidate.Endpoint.UpstreamModel,
			ProviderID:        task.candidate.Endpoint.ProviderID,
			ProviderUUID:      task.candidate.Endpoint.ProviderUUID,
			OrganizationUUID:  organizationUUIDFromMetadata(task.trustedMetadata),
			ActorID:           actorIDFromMetadata(task.trustedMetadata),
			APIKeyUUID:        apiKeyUUIDFromMetadata(task.trustedMetadata),
			State:             task.state,
			Priority:          task.meta.Priority,
			QueuedAt:          task.enqueuedAt,
			WaitMS:            now.Sub(task.enqueuedAt).Milliseconds(),
			PredictedEligible: task.evaluation.EligibleAt,
			ResourceEligible:  task.resourceEligibleAt,
			UserEligible:      task.userEligibleAt,
			UserLimitReason:   task.userLimitReason,
			DelayReason:       task.evaluation.Reason,
			DeferScope:        task.deferScope,
			DeferScopeID:      task.deferScopeID,
			DeferReason:       task.deferReason,
			EstimatedCost:     task.estimatedCost,
			FallbackCount:     task.candidate.FallbackCount,
			CandidateTrace:    append([]CandidateTrace(nil), task.candidateTrace...),
			StartedAt:         startedAt,
			Substatus:         task.substatus,
			UploadedTokens:    task.uploadedTokens,
			DownloadedTokens:  task.downloadedTokens,
		})
	}
	return items
}

func (s *Scheduler) UpdateProgress(taskID string, startedAt time.Time, substatus string, uploadedTokens, downloadedTokens int64) {
	if s.isRegistry() {
		if runtime := s.runtimeForTask(taskID); runtime != nil {
			runtime.UpdateProgress(taskID, startedAt, substatus, uploadedTokens, downloadedTokens)
		}
		return
	}
	if taskID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return
	}
	if !startedAt.IsZero() {
		task.startedAt = startedAt.UTC()
	}
	if substatus != "" {
		task.substatus = substatus
	}
	task.uploadedTokens = uploadedTokens
	task.downloadedTokens = downloadedTokens
}

func (s *Scheduler) loop() {
	for {
		timer := s.nextTimer()
		select {
		case <-s.stopCh:
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.wakeup:
		case <-timerChan(timer):
		}
		if timer != nil {
			timer.Stop()
		}
		s.promoteReady()
		s.dispatchReady()
	}
}

func (s *Scheduler) nextTimer() *time.Timer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.delayed) == 0 {
		return time.NewTimer(time.Hour)
	}
	wait := time.Until(s.delayed[0].evaluation.EligibleAt)
	if wait < 0 {
		wait = 0
	}
	return time.NewTimer(wait)
}

func (s *Scheduler) promoteReady() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.delayed) > 0 {
		item := s.delayed[0]
		if item.cancelled {
			heap.Pop(&s.delayed)
			continue
		}
		if item.evaluation.EligibleAt.After(now) {
			break
		}
		heap.Pop(&s.delayed)
		item.state = "ready"
		heap.Push(&s.ready, item)
	}
}

func (s *Scheduler) rebuildQueuesLocked(skip map[*task]bool) {
	s.ready = readyHeap{}
	s.delayed = delayedHeap{}
	for _, item := range s.tasks {
		if item.cancelled || skip[item] {
			continue
		}
		switch item.state {
		case "ready":
			heap.Push(&s.ready, item)
		case "waiting":
			heap.Push(&s.delayed, item)
		}
	}
	heap.Init(&s.ready)
	heap.Init(&s.delayed)
}

// rebuildGroupFIFOTailsLocked is used only by explicit control-plane
// re-evaluation. Normal request deltas maintain the map incrementally, keeping
// the dispatch path O(1); a policy edit may move many deadlines backwards and
// is allowed to rebuild this derived index once.
func (s *Scheduler) rebuildGroupFIFOTailsLocked(skip map[*task]bool) {
	s.groupFIFOTails = make(map[string]time.Time)
	for _, item := range s.tasks {
		if item == nil || item.cancelled || skip[item] || isRequestScopedDefer(item.deferScope) {
			continue
		}
		if item.state != "ready" && item.state != "waiting" {
			continue
		}
		key := candidateGroupKey(item.meta, item.candidate)
		if key == "" {
			continue
		}
		eligibleAt := item.evaluation.EligibleAt.UTC()
		if eligibleAt.After(s.groupFIFOTails[key]) {
			s.groupFIFOTails[key] = eligibleAt
		}
	}
}

func (s *Scheduler) dispatchReady() {
	for {
		s.mu.Lock()
		readyCount := len(s.ready)
		s.mu.Unlock()
		if readyCount == 0 {
			return
		}
		select {
		case s.dispatchSlots <- struct{}{}:
			go func() {
				dispatched := s.dispatchOneReady()
				<-s.dispatchSlots
				if dispatched {
					s.signal()
				}
			}()
		default:
			return
		}
	}
}

func (s *Scheduler) dispatchSerialReady() {
	if !s.serialGroupDispatchEnabled() {
		return
	}
	s.dispatchReady()
}

func (s *Scheduler) serialGroupDispatchEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serialGroupDispatch
}

func (s *Scheduler) dispatchOneReady() bool {
	now := time.Now().UTC()
	var skipped []*task
	defer func() {
		s.restoreReadyTasks(skipped)
	}()

	for {
		s.mu.Lock()
		if len(s.ready) == 0 {
			s.mu.Unlock()
			return false
		}
		item := heap.Pop(&s.ready).(*task)
		s.mu.Unlock()

		s.mu.Lock()
		eligibleState := !item.cancelled && item.state == "ready"
		s.mu.Unlock()
		if !eligibleState {
			continue
		}

		s.mu.Lock()
		claimed := s.claimTaskGroupLocked(item)
		s.mu.Unlock()
		if !claimed {
			skipped = append(skipped, item)
			continue
		}
		s.tracker.Release(item.id)
		if err := s.refreshTaskCatalogIfChanged(s.taskContext(item), item); err != nil {
			s.mu.Lock()
			s.removeTaskLocked(item)
			item.state = "failed"
			s.mu.Unlock()
			s.emitState(item)
			select {
			case item.errCh <- err:
			default:
			}
			continue
		}
		unlockCoordinator, err := s.lockAndRefreshTaskEligibility(s.taskContext(item), item, now)
		if err != nil {
			s.mu.Lock()
			s.removeTaskLocked(item)
			item.state = "failed"
			s.mu.Unlock()
			s.emitState(item)
			select {
			case item.errCh <- err:
			default:
			}
			continue
		}
		s.mu.Lock()
		eligibleAt := item.evaluation.EligibleAt
		deferScope := item.deferScope
		s.mu.Unlock()
		if eligibleAt.After(now) {
			s.tracker.Release(item.id)
			if s.shouldReserveOnEnqueue(item) {
				s.reserveUsageEstimates(item.id, taskReservationScopes(item), item.estimatedInput+item.estimatedOutput, item.estimatedCost, eligibleAt)
			}
			s.mu.Lock()
			item.state = "waiting"
			s.unclaimTaskGroupLocked(item)
			s.extendGroupFIFOTailLocked(item, eligibleAt)
			heap.Push(&s.delayed, item)
			s.mu.Unlock()
			unlockCoordinator()
			s.emitState(item)
			return true
		}
		if isRequestScopedDefer(deferScope) {
			s.tracker.Release(item.id)
			s.mu.Lock()
			item.evaluation.EligibleAt = now.Add(time.Second)
			item.state = "waiting"
			s.unclaimTaskGroupLocked(item)
			s.extendGroupFIFOTailLocked(item, item.evaluation.EligibleAt)
			heap.Push(&s.delayed, item)
			s.mu.Unlock()
			unlockCoordinator()
			s.emitState(item)
			return true
		}
		s.mu.Lock()
		preAdmitted := item.preAdmitted
		item.preAdmitted = false
		s.mu.Unlock()
		admission := DispatchAdmissionDecision{Allowed: true}
		err = nil
		if !preAdmitted {
			admission, err = s.dispatchAdmission(s.taskContext(item), item)
		}
		if err != nil {
			s.tracker.Release(item.id)
			finishedAt := time.Now().UTC()
			persistCtx, cancelPersist := persistenceContext(s.taskContext(item), s.backgroundContext())
			finalizeErr := s.finalizeRequest(persistCtx, item, "failed", http.StatusServiceUnavailable, 0, 0, 0, item.startedAt, finishedAt)
			if s.persistState && s.store != nil {
				_ = s.store.UpdateRequestLog(persistCtx, item.meta.RequestID, func(log *models.RequestLog) error {
					setRequestLogCandidate(log, item.candidate)
					log.TaskState = "failed"
					log.StatusCode = http.StatusServiceUnavailable
					log.ErrorText = "dispatch admission failed"
					log.FinishedAt = &finishedAt
					applyCharacterizationToLog(log, item.characterization)
					return nil
				})
			}
			cancelPersist()
			s.mu.Lock()
			s.removeTaskLocked(item)
			item.state = "failed"
			s.totals["failed"]++
			s.mu.Unlock()
			unlockCoordinator()
			s.emitState(item)
			if finalizeErr != nil {
				err = errors.Join(err, finalizeErr)
			}
			s.completeCharacterizationLog(item.meta.RequestID, item.trustedMetadata, item.characterization)
			select {
			case item.errCh <- err:
			default:
			}
			return true
		}
		if !admission.Allowed {
			s.tracker.Release(item.id)
			eligibleAt := admission.EligibleAt.UTC()
			if eligibleAt.IsZero() || !eligibleAt.After(now) {
				eligibleAt = now.Add(time.Second)
			}
			s.mu.Lock()
			item.evaluation.EligibleAt = eligibleAt
			item.evaluation.RetryAfter = eligibleAt.Sub(now)
			if admission.Reason != "" {
				item.evaluation.Reason = admission.Reason
			}
			item.state = "waiting"
			s.unclaimTaskGroupLocked(item)
			s.extendGroupFIFOTailLocked(item, eligibleAt)
			heap.Push(&s.delayed, item)
			s.mu.Unlock()
			unlockCoordinator()
			s.emitState(item)
			s.signal()
			return true
		}
		s.reserveRuntimeUsageEstimates(item.id, taskReservationScopes(item), item.estimatedInput+item.estimatedOutput, item.estimatedCost, now)
		s.publishCapacityReservation(item, now)
		scopes := taskScopes(item)
		allow := true
		for _, scope := range scopes {
			if s.tracker.Concurrency(scope) > 0 && scope.Type == models.ScopeEndpoint {
				// Endpoint concurrency limits are enforced via resolver. Once a slot opens, wakeups retry dispatch.
			}
		}
		if !allow {
			s.mu.Lock()
			item.evaluation.EligibleAt = now.Add(time.Second)
			item.state = "waiting"
			s.unclaimTaskGroupLocked(item)
			s.extendGroupFIFOTailLocked(item, item.evaluation.EligibleAt)
			heap.Push(&s.delayed, item)
			s.mu.Unlock()
			unlockCoordinator()
			continue
		}
		selectedAt := time.Now().UTC()
		s.tracker.AddConcurrency(scopes, 1)
		s.mu.Lock()
		item.state = "in_flight"
		item.startedAt = selectedAt
		item.substatus = "Dispatching upstream"
		item.uploadedTokens = 0
		item.downloadedTokens = 0
		s.mu.Unlock()
		s.recordGroupHandoff(item, selectedAt)
		unlockCoordinator()
		s.emitState(item)
		s.enqueueEndpointHealthRefresh(item)
		select {
		case item.readyCh <- Permit{
			TaskID:            item.id,
			Endpoint:          item.candidate.Endpoint,
			Lane:              item.candidate.Lane,
			OrganizationUUID:  organizationUUIDFromMetadata(item.trustedMetadata),
			ActorID:           actorIDFromMetadata(item.trustedMetadata),
			EstimatedInput:    item.estimatedInput,
			EstimatedOutput:   item.estimatedOutput,
			EstimatedCost:     item.estimatedCost,
			FallbackCount:     item.candidate.FallbackCount,
			Waited:            selectedAt.Sub(item.enqueuedAt),
			Overrides:         item.meta.Overrides,
			SelectedAt:        selectedAt,
			AppliedLimitState: item.evaluation.EffectiveLimits,
			CandidateTrace:    item.candidateTrace,
		}:
		default:
			s.tracker.AddConcurrency(scopes, -1)
			s.tracker.Release(item.id)
			s.mu.Lock()
			s.removeTaskLocked(item)
			item.state = "failed"
			s.mu.Unlock()
			select {
			case item.errCh <- errors.New("ready channel blocked"):
			default:
			}
		}
		return true
	}
}

func (s *Scheduler) lockAndRefreshTaskEligibility(ctx context.Context, item *task, now time.Time) (func(), error) {
	for attempts := 0; attempts < 4; attempts++ {
		s.mu.Lock()
		keys := taskCoordinatorKeys(item)
		originalGroup := candidateGroupKey(item.meta, item.candidate)
		s.mu.Unlock()
		unlock := s.coordinator.lock(keys...)
		if err := s.refreshTaskEligibility(ctx, item, now); err != nil {
			unlock()
			return nil, err
		}
		s.mu.Lock()
		guardedKeys := taskCoordinatorKeys(item)
		guardedGroup := candidateGroupKey(item.meta, item.candidate)
		s.mu.Unlock()
		if originalGroup != guardedGroup {
			unlock()
			return nil, errors.New("routing group changed while request was queued")
		}
		if equalCoordinatorKeys(keys, guardedKeys) {
			return unlock, nil
		}
		unlock()
	}
	return nil, errors.New("limit policy set changed repeatedly during dispatch admission")
}

func (s *Scheduler) recordGroupHandoff(item *task, selectedAt time.Time) {
	if item == nil {
		return
	}
	key := candidateGroupKey(item.meta, item.candidate)
	if key == "" {
		return
	}
	s.mu.Lock()
	releasedAt, ok := s.groupReleasedAt[key]
	if ok {
		delete(s.groupReleasedAt, key)
	}
	s.mu.Unlock()
	if !ok || releasedAt.IsZero() || selectedAt.Before(releasedAt) {
		return
	}
	ctx := s.taskContext(item)
	_, span := observability.Tracer().Start(ctx, "relay.scheduler.group_handoff",
		trace.WithTimestamp(releasedAt),
		trace.WithAttributes(
			attribute.Int64("anchorshell.relay.group_handoff_ms", selectedAt.Sub(releasedAt).Milliseconds()),
			attribute.String("anchorshell.relay.group_kind", groupKeyKind(key)),
		),
	)
	span.End(trace.WithTimestamp(selectedAt))
}

func groupKeyKind(key string) string {
	if before, _, ok := strings.Cut(key, ":"); ok {
		return before
	}
	return "unknown"
}

func (s *Scheduler) dispatchAdmission(ctx context.Context, item *task) (DispatchAdmissionDecision, error) {
	decision := DispatchAdmissionDecision{Allowed: true}
	if item == nil || len(s.admissionHooks) == 0 {
		return decision, nil
	}
	ctx, span := observability.Tracer().Start(ctx, "relay.scheduler.dispatch_admission",
		trace.WithAttributes(
			attribute.String("anchorshell.request.id", item.meta.RequestID),
			attribute.String("anchorshell.relay.task.id", item.id),
			attribute.String("anchorshell.relay.endpoint.id", item.candidate.Endpoint.UUID),
			attribute.String("anchorshell.relay.provider.id", item.candidate.Endpoint.ProviderUUID),
		),
	)
	defer func() {
		span.SetAttributes(attribute.Bool("anchorshell.relay.admission.allowed", decision.Allowed))
		span.End()
	}()
	input := dispatchAdmissionInputForTask(item)
	for _, hook := range s.admissionHooks {
		if hook == nil {
			continue
		}
		next, err := hook(ctx, input)
		if err != nil {
			decision.Allowed = false
			span.SetStatus(codes.Error, "dispatch admission failed")
			return DispatchAdmissionDecision{}, err
		}
		if next.Allowed {
			continue
		}
		decision.Allowed = false
		if next.EligibleAt.After(decision.EligibleAt) {
			decision.EligibleAt = next.EligibleAt.UTC()
		}
		if next.Reason != "" {
			decision.Reason = next.Reason
		}
	}
	return decision, nil
}

func dispatchAdmissionInputForTask(item *task) DispatchAdmissionInput {
	if item == nil {
		return DispatchAdmissionInput{}
	}
	laneID := ""
	if item.candidate.Lane != nil {
		laneID = item.candidate.Lane.UUID
	}
	return DispatchAdmissionInput{
		RequestID:             item.meta.RequestID,
		TaskID:                item.id,
		RouteKind:             string(item.meta.RouteKind),
		IncomingModel:         item.meta.IncomingModel,
		Lane:                  item.meta.Lane,
		LaneID:                laneID,
		EndpointID:            item.candidate.Endpoint.UUID,
		EndpointName:          item.candidate.Endpoint.Name,
		ProviderID:            item.candidate.Endpoint.ProviderUUID,
		UpstreamModel:         item.candidate.Endpoint.UpstreamModel,
		EstimatedInputTokens:  item.estimatedInput,
		EstimatedOutputTokens: item.estimatedOutput,
		EstimatedCostMicros:   item.estimatedCost,
		Metadata:              cloneMetadata(item.trustedMetadata),
	}
}

func (s *Scheduler) restoreReadyTasks(items []*task) {
	if len(items) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		if item == nil || item.cancelled || item.state != "ready" {
			continue
		}
		heap.Push(&s.ready, item)
	}
}

func (s *Scheduler) claimTaskGroupLocked(item *task) bool {
	if item == nil || !s.serialGroupDispatch {
		return true
	}
	key := candidateGroupKey(item.meta, item.candidate)
	if key == "" {
		return true
	}
	queue := s.groupTasks[key]
	if queue != nil && queue.head != "" && queue.head != item.id {
		return false
	}
	if activeTaskID := s.groupActive[key]; activeTaskID != "" && activeTaskID != item.id {
		return false
	}
	s.groupActive[key] = item.id
	return true
}

func (s *Scheduler) unclaimTaskGroupLocked(item *task) {
	if item == nil {
		return
	}
	key := candidateGroupKey(item.meta, item.candidate)
	if key == "" {
		return
	}
	if s.groupActive[key] == item.id {
		delete(s.groupActive, key)
	}
}

func (s *Scheduler) removeTaskFromGroupQueueLocked(item *task) {
	if item == nil {
		return
	}
	key := candidateGroupKey(item.meta, item.candidate)
	if key == "" {
		return
	}
	queue := s.groupTasks[key]
	if queue != nil {
		queue.remove(item.id)
	}
	if queue.empty() {
		delete(s.groupTasks, key)
	}
	s.unclaimTaskGroupLocked(item)
}

func (s *Scheduler) extendGroupFIFOTailLocked(item *task, eligibleAt time.Time) {
	if item == nil || isRequestScopedDefer(item.deferScope) {
		return
	}
	key := candidateGroupKey(item.meta, item.candidate)
	if key == "" {
		return
	}
	eligibleAt = eligibleAt.UTC()
	if eligibleAt.After(s.groupFIFOTails[key]) {
		s.groupFIFOTails[key] = eligibleAt
	}
}

func (s *Scheduler) rebuildGroupFIFOTailLocked(key string) {
	queue := s.groupTasks[key]
	if queue == nil {
		delete(s.groupFIFOTails, key)
		return
	}
	var tail time.Time
	for taskID := queue.head; taskID != ""; taskID = queue.next(taskID) {
		item := s.tasks[taskID]
		if item == nil || item.cancelled || isRequestScopedDefer(item.deferScope) || (item.state != "ready" && item.state != "waiting") {
			continue
		}
		if eligibleAt := item.evaluation.EligibleAt.UTC(); eligibleAt.After(tail) {
			tail = eligibleAt
		}
	}
	if tail.IsZero() {
		delete(s.groupFIFOTails, key)
		return
	}
	s.groupFIFOTails[key] = tail
}

func (s *Scheduler) removeTaskLocked(item *task) {
	if item == nil {
		return
	}
	removedEligibleAt := item.evaluation.EligibleAt.UTC()
	delete(s.tasks, item.id)
	s.removeTaskFromGroupQueueLocked(item)
	key := candidateGroupKey(item.meta, item.candidate)
	if key == "" {
		return
	}
	if s.groupQueued[key] <= 1 {
		delete(s.groupQueued, key)
		delete(s.groupTasks, key)
		delete(s.groupFIFOTails, key)
		delete(s.groupReleasedAt, key)
		return
	}
	s.groupQueued[key]--
	if tail := s.groupFIFOTails[key]; !tail.After(removedEligibleAt) {
		// Cancellation of a deferred tail must not leave later submissions
		// sleeping behind a request that no longer exists. This scan is only on
		// that cold removal edge; normal head completion remains O(1).
		s.rebuildGroupFIFOTailLocked(key)
	}
}

func (s *Scheduler) signal() {
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

func (s *Scheduler) emitState(task *task) {
	if s.telemetry == nil {
		return
	}
	now := time.Now().UTC()
	// Deltas are emitted on every lifecycle transition. Building a full
	// Snapshot here made one event O(total queued requests) and repeated tracker
	// reads for every connected client. The task map already owns the exact
	// global depth, so delta generation remains O(1).
	s.mu.Lock()
	queueDepth := len(s.tasks)
	snapshot := *task
	snapshot.trustedMetadata = cloneMetadata(task.trustedMetadata)
	snapshot.candidateTrace = append([]CandidateTrace(nil), task.candidateTrace...)
	s.mu.Unlock()
	task = &snapshot
	payload := map[string]any{
		"task_id":                 task.id,
		"request_id":              task.meta.RequestID,
		"lane":                    task.meta.Lane,
		"incoming_model":          task.meta.IncomingModel,
		"endpoint_id":             task.candidate.Endpoint.UUID,
		"endpoint_name":           task.candidate.Endpoint.Name,
		"provider_id":             task.candidate.Endpoint.ProviderUUID,
		"organization_uuid":       organizationUUIDFromMetadata(task.trustedMetadata),
		"actor_id":                actorIDFromMetadata(task.trustedMetadata),
		"api_key_uuid":            apiKeyUUIDFromMetadata(task.trustedMetadata),
		"selected_upstream_model": task.candidate.Endpoint.UpstreamModel,
		"state":                   task.state,
		"priority":                task.meta.Priority,
		"queued_at":               task.enqueuedAt,
		"wait_ms":                 now.Sub(task.enqueuedAt).Milliseconds(),
		"queue_depth":             queueDepth,
		"fallback":                task.candidate.FallbackCount,
		"eligible_at":             task.evaluation.EligibleAt,
		"predicted_eligible_at":   task.evaluation.EligibleAt,
		"resource_eligible_at":    task.resourceEligibleAt,
		"user_eligible_at":        task.userEligibleAt,
		"user_limit_reason":       task.userLimitReason,
		"delay_reason":            task.evaluation.Reason,
		"defer_scope":             task.deferScope,
		"defer_reason":            task.deferReason,
		"estimated_ms":            task.evaluation.RetryAfter.Milliseconds(),
		"estimated_cost_micros":   task.estimatedCost,
		"candidate_trace":         compactCandidateTraceForTelemetry(task.candidateTrace),
	}
	if !task.startedAt.IsZero() {
		payload["started_at"] = task.startedAt.UTC()
	}
	if task.substatus != "" {
		payload["substatus"] = task.substatus
	}
	if task.uploadedTokens > 0 {
		payload["uploaded_tokens"] = task.uploadedTokens
	}
	if task.downloadedTokens > 0 {
		payload["downloaded_tokens"] = task.downloadedTokens
	}
	s.telemetry.Publish(telemetry.Event{
		Type:    "request_" + task.state,
		Payload: payload,
	})
}

func (s *Scheduler) emitCancelledState(task *task, log models.RequestLog) {
	if s == nil || s.telemetry == nil || task == nil {
		return
	}
	payload := map[string]any{
		"task_id":                 task.id,
		"request_id":              log.RequestID,
		"organization_uuid":       organizationUUIDFromMetadata(task.trustedMetadata),
		"actor_id":                log.ActorID,
		"api_key_uuid":            apiKeyUUIDFromMetadata(task.trustedMetadata),
		"lane":                    task.meta.Lane,
		"lane_id":                 log.LaneUUID,
		"incoming_model":          log.IncomingModel,
		"endpoint_id":             log.EndpointUUID,
		"provider_id":             log.ProviderUUID,
		"selected_upstream_model": log.SelectedUpstreamModel,
		"state":                   "cancelled",
		"task_state":              "cancelled",
		"queued_at":               log.QueuedAt,
		"started_at":              log.StartedAt,
		"finished_at":             log.FinishedAt,
		"wait_ms":                 log.WaitMS,
		"latency_ms":              log.LatencyMS,
		"fallback_count":          log.FallbackCount,
		"status_code":             499,
		"error_text":              log.ErrorText,
	}
	s.telemetry.Publish(telemetry.Event{Type: "request_cancelled", Payload: payload})
}

func compactCandidateTraceForTelemetry(trace []CandidateTrace) []CandidateTrace {
	if len(trace) == 0 {
		return nil
	}
	out := make([]CandidateTrace, len(trace))
	for i, item := range trace {
		item.EffectiveLimits = nil
		out[i] = item
	}
	return out
}

func (s *Scheduler) enqueueEndpointHealthRefresh(task *task) {
	if task == nil || s.healthRefreshes == nil {
		return
	}
	seen := make(map[uint]bool)
	endpointIDs := make([]uint, 0, len(task.candidateTrace))
	for _, item := range task.candidateTrace {
		if item.EndpointID == 0 || item.EndpointID == task.candidate.Endpoint.ID || seen[item.EndpointID] {
			continue
		}
		if item.Decision != "queued" || !strings.Contains(item.Reason, "queued beyond wait budget") {
			continue
		}
		seen[item.EndpointID] = true
		endpointIDs = append(endpointIDs, item.EndpointID)
	}
	if len(endpointIDs) == 0 {
		return
	}
	select {
	case s.healthRefreshes <- endpointIDs:
	default:
		// Health is derived state. A later limit/cooldown transition or timer will
		// publish the newest value; dispatch must never block behind telemetry.
	}
}

func (s *Scheduler) endpointHealthRefreshLoop() {
	defer s.completionPostWG.Done()
	for {
		select {
		case endpointIDs := <-s.healthRefreshes:
			for _, endpointID := range endpointIDs {
				s.syncEndpointHealth(endpointID)
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *Scheduler) syncEndpointHealth(endpointID uint) {
	s.syncEndpointHealthForOrganization(endpointID, "")
}

func (s *Scheduler) syncEndpointHealthForOrganization(endpointID uint, organizationUUID string) {
	s.syncEndpointHealthWithOptionsForOrganization(endpointID, false, organizationUUID, false)
}

func (s *Scheduler) syncEndpointHealthWithOptions(endpointID uint, ignoreEndpointState bool) {
	s.syncEndpointHealthWithOptionsForOrganization(endpointID, ignoreEndpointState, "", false)
}

func (s *Scheduler) syncEndpointHealthWithOptionsForOrganization(endpointID uint, ignoreEndpointState bool, organizationUUID string, forcePublish bool) {
	if endpointID == 0 || s.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(s.backgroundContext(), 5*time.Second)
	defer cancel()

	var endpoint models.Endpoint
	if err := s.store.DB().WithContext(ctx).First(&endpoint, endpointID).Error; err != nil {
		return
	}
	previousCooldownReason := endpoint.CooldownReason
	previousCooldownStatusCode := endpoint.CooldownStatusCode
	if !s.persistState {
		endpoint.HealthStatus = models.HealthHealthy
		endpoint.CooldownUntil = nil
		endpoint.CooldownReason = ""
		endpoint.CooldownStatusCode = 0
	}

	lanes, err := s.endpointLanes(ctx, endpointID)
	if err != nil {
		return
	}

	now := time.Now().UTC()
	status, until, err := s.deriveEndpointHealth(ctx, endpoint, lanes, now, ignoreEndpointState)
	if err != nil {
		return
	}
	if until != nil && until.After(now) {
		// Health is derived from exact pacing/limit windows. Arrange the inverse
		// transition as soon as this boundary passes even when the relay is
		// otherwise idle and no new queue item causes a reevaluation.
		s.ScheduleEndpointHealthSync("", endpointID, until.UTC().Add(time.Millisecond))
	}
	preserveCooldownSource := endpoint.CooldownReason != "" &&
		(status == models.HealthCoolingDown || status == models.HealthRateLimited || status == models.HealthUnhealthy) &&
		sameTimePtr(endpoint.CooldownUntil, until)
	if !preserveCooldownSource {
		endpoint.CooldownReason = ""
		endpoint.CooldownStatusCode = 0
	}
	stateUnchanged := s.persistState && endpoint.HealthStatus == status && sameTimePtr(endpoint.CooldownUntil, until) &&
		previousCooldownReason == endpoint.CooldownReason && previousCooldownStatusCode == endpoint.CooldownStatusCode
	if stateUnchanged && !forcePublish {
		return
	}

	endpoint.HealthStatus = status
	endpoint.CooldownUntil = until
	if s.persistState && !stateUnchanged {
		if err := s.store.SaveEndpointState(ctx, endpoint); err != nil {
			return
		}
	}

	if s.telemetry != nil {
		organizationUUID = strings.TrimSpace(organizationUUID)
		if organizationUUID == "" {
			organizationUUID = tenancy.OrganizationUUID(ctx)
		}
		s.telemetry.Publish(telemetry.Event{
			Type: "endpoint_health_change",
			Payload: map[string]any{
				"organization_uuid":    organizationUUID,
				"endpoint_id":          endpoint.UUID,
				"provider_id":          endpoint.ProviderUUID,
				"health_status":        endpoint.HealthStatus,
				"cooldown_until":       endpoint.CooldownUntil,
				"cooldown_reason":      endpoint.CooldownReason,
				"cooldown_status_code": endpoint.CooldownStatusCode,
				"updated_at":           time.Now().UTC(),
			},
		})
	}
}

func (s *Scheduler) endpointLanes(ctx context.Context, endpointID uint) ([]models.RoutingLane, error) {
	memberships, err := s.store.ListLaneMemberships(ctx)
	if err != nil {
		return nil, err
	}
	lanes, err := s.store.ListLanes(ctx)
	if err != nil {
		return nil, err
	}

	laneByID := make(map[uint]models.RoutingLane, len(lanes))
	for _, lane := range lanes {
		laneByID[lane.ID] = lane
	}

	options := make([]models.RoutingLane, 0, 4)
	for _, membership := range memberships {
		if membership.EndpointID != endpointID || !membership.Enabled {
			continue
		}
		lane, ok := laneByID[membership.LaneID]
		if !ok || !lane.Enabled {
			continue
		}
		options = append(options, lane)
	}
	return options, nil
}

func (s *Scheduler) publishRequestLog(log models.RequestLog, metadata map[string]string) {
	if s.telemetry == nil {
		return
	}
	// Request telemetry is emitted on the scheduler handoff path. Candidate
	// UUIDs are already copied into the log by setRequestLogCandidate; resolving
	// them from the database here would turn non-authoritative telemetry into a
	// synchronous scheduling dependency.
	publicID := log.UUID
	if publicID == "" {
		publicID = log.RequestID
	}
	s.telemetry.Publish(telemetry.Event{
		Type: "request_log",
		Payload: map[string]any{
			"id":                           publicID,
			"request_id":                   log.RequestID,
			"parent_request_id":            log.ParentRequestID,
			"lane_id":                      log.LaneUUID,
			"endpoint_id":                  log.EndpointUUID,
			"provider_id":                  log.ProviderUUID,
			"organization_uuid":            organizationUUIDFromMetadata(metadata),
			"actor_id":                     log.ActorID,
			"api_key_uuid":                 apiKeyUUIDFromMetadata(metadata),
			"route_kind":                   log.RouteKind,
			"incoming_model":               log.IncomingModel,
			"selected_upstream_model":      log.SelectedUpstreamModel,
			"status_code":                  log.StatusCode,
			"task_state":                   log.TaskState,
			"queued_at":                    log.QueuedAt,
			"started_at":                   log.StartedAt,
			"finished_at":                  log.FinishedAt,
			"wait_ms":                      log.WaitMS,
			"latency_ms":                   log.LatencyMS,
			"characterization_duration_ms": requestLogCharacterizationDurationMS(log),
			"guardrail_pre_duration_ms":    log.GuardrailPreDurationMS,
			"provider_latency_ms":          requestLogProviderLatencyMS(log),
			"guardrail_post_duration_ms":   log.GuardrailPostDurationMS,
			"total_time_ms":                requestLogTotalTimeMS(log),
			"streaming":                    log.Streaming,
			"priority":                     log.Priority,
			"fallback_count":               log.FallbackCount,
			"estimated_input_tokens":       log.EstimatedInputTokens,
			"estimated_output_tokens":      log.EstimatedOutputTokens,
			"actual_input_tokens":          log.ActualInputTokens,
			"actual_output_tokens":         log.ActualOutputTokens,
			"actual_total_tokens":          log.ActualTotalTokens,
			"estimated_cost_micros":        log.EstimatedCostMicros,
			"actual_cost_micros":           log.ActualCostMicros,
			"error_text":                   log.ErrorText,
			"primary_action":               log.PrimaryAction,
			"characterization":             compactCharacterizationTelemetry(log.CharacterizationJSON),
			"guardrail_status":             log.GuardrailStatus,
			"created_at":                   log.CreatedAt,
			"updated_at":                   log.UpdatedAt,
		},
	})
}

func requestLogProviderLatencyMS(log models.RequestLog) int64 {
	if strings.EqualFold(strings.TrimSpace(log.GuardrailStatus), "blocked_pre") {
		return 0
	}
	return max(log.LatencyMS-log.GuardrailPreDurationMS-log.GuardrailPostDurationMS, 0)
}

func requestLogTotalTimeMS(log models.RequestLog) float64 {
	classification := requestLogCharacterizationDurationMS(log)
	if log.CharacterizationJSON != nil {
		var value struct {
			Background bool `json:"classification_background"`
		}
		if json.Unmarshal([]byte(*log.CharacterizationJSON), &value) == nil && value.Background {
			classification = 0
		}
	}
	return float64(max(log.WaitMS, 0)+max(log.GuardrailPreDurationMS, 0)+requestLogProviderLatencyMS(log)+max(log.GuardrailPostDurationMS, 0)) + classification
}

func requestLogCharacterizationDurationMS(log models.RequestLog) float64 {
	if log.CharacterizationJSON == nil || strings.TrimSpace(*log.CharacterizationJSON) == "" {
		return 0
	}
	var value struct {
		ClassificationDurationMS float64 `json:"classification_duration_ms"`
	}
	if err := json.Unmarshal([]byte(*log.CharacterizationJSON), &value); err != nil || value.ClassificationDurationMS < 0 {
		return 0
	}
	return value.ClassificationDurationMS
}

func compactCharacterizationTelemetry(raw *string) map[string]any {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var value characterization.Characterization
	if err := json.Unmarshal([]byte(*raw), &value); err != nil || value.ClassifierStatus == characterization.StatusDisabled {
		return nil
	}
	return map[string]any{
		"primary_action":             value.PrimaryAction,
		"target_objects":             firstTelemetryValue(value.TargetObjects),
		"output_objects":             firstTelemetryValue(value.OutputObjects),
		"domains":                    firstTelemetryValue(value.Domains),
		"required_capabilities":      firstTelemetryValue(value.RequiredCapabilities),
		"context_burden":             map[string]any{"tier": value.ContextBurden.Tier},
		"classification_duration_ms": value.ClassificationDurationMS,
		"classification_background":  value.ClassificationBackground,
		"classifier_status":          value.ClassifierStatus,
	}
}

func firstTelemetryValue[T any](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	return values[:1]
}

func (s *Scheduler) recordSubmitRejection(ctx context.Context, req SubmitRequest, enqueuedAt time.Time, submitErr error) {
	var rejection Rejection
	isRejection := errors.As(submitErr, &rejection)
	if req.Preview {
		return
	}
	statusCode := http.StatusServiceUnavailable
	taskState := "failed"
	reason := "scheduler admission unavailable"
	var trace []CandidateTrace
	switch {
	case isRejection:
		statusCode = http.StatusTooManyRequests
		reason = rejection.Reason
		trace = rejection.CandidateTrace
	case errors.Is(submitErr, context.Canceled), errors.Is(submitErr, context.DeadlineExceeded):
		statusCode = 499
		taskState = "cancelled"
		reason = "client canceled request"
	}
	if enqueuedAt.IsZero() {
		enqueuedAt = time.Now().UTC()
	}
	finishedAt := time.Now().UTC()
	requestID := req.Meta.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}
	estimatedInput, estimatedOutput := s.estimator.Estimate(req.Meta, req.Body, req.BodyBytes)
	laneID := uint(0)
	laneUUID := ""
	fallbackCount := 0
	for _, candidate := range req.Candidates {
		if laneID == 0 && candidate.Lane != nil {
			laneID = candidate.Lane.ID
			laneUUID = candidate.Lane.UUID
		}
		if candidate.FallbackCount > fallbackCount {
			fallbackCount = candidate.FallbackCount
		}
	}
	log := models.RequestLog{
		RequestID:             requestID,
		LaneID:                nilIfZero(laneID),
		LaneUUID:              optionalString(laneUUID),
		ActorID:               actorIDFromMetadata(req.TrustedMetadata),
		RouteKind:             req.Meta.RouteKind,
		IncomingModel:         req.Meta.IncomingModel,
		StatusCode:            statusCode,
		TaskState:             taskState,
		QueuedAt:              &enqueuedAt,
		FinishedAt:            &finishedAt,
		WaitMS:                max(finishedAt.Sub(enqueuedAt).Milliseconds(), 0),
		Streaming:             req.Meta.Streaming,
		Priority:              req.Meta.Priority,
		FallbackCount:         fallbackCount,
		EstimatedInputTokens:  estimatedInput,
		EstimatedOutputTokens: estimatedOutput,
		ErrorText:             reason,
		CandidateTraceJSON:    marshalJSON(trace),
		AppliedOverridesJSON:  marshalJSON(req.Meta.Overrides),
		CreatedAt:             enqueuedAt,
		UpdatedAt:             finishedAt,
	}
	applyCharacterizationToLog(&log, req.Characterization)
	if s.persistState && s.store != nil {
		fallback := tenancy.ContextWithMetadataScope(s.backgroundContext(), req.TrustedMetadata)
		persistCtx, cancelPersist := persistenceContext(ctx, fallback)
		defer cancelPersist()
		_ = s.store.UpdateRequestLog(persistCtx, requestID, func(stored *models.RequestLog) error {
			stored.LaneID = log.LaneID
			stored.LaneUUID = log.LaneUUID
			stored.EndpointID = nil
			stored.ProviderID = nil
			stored.RouteKind = log.RouteKind
			stored.IncomingModel = log.IncomingModel
			stored.SelectedUpstreamModel = ""
			stored.StatusCode = log.StatusCode
			stored.TaskState = log.TaskState
			stored.QueuedAt = log.QueuedAt
			stored.StartedAt = nil
			stored.FinishedAt = log.FinishedAt
			stored.WaitMS = log.WaitMS
			stored.LatencyMS = 0
			stored.Streaming = log.Streaming
			stored.Priority = log.Priority
			stored.FallbackCount = log.FallbackCount
			stored.EstimatedInputTokens = log.EstimatedInputTokens
			stored.EstimatedOutputTokens = log.EstimatedOutputTokens
			stored.ActualInputTokens = 0
			stored.ActualOutputTokens = 0
			stored.ActualTotalTokens = 0
			stored.EstimatedCostMicros = 0
			stored.ActualCostMicros = 0
			stored.ErrorText = log.ErrorText
			stored.CandidateTraceJSON = log.CandidateTraceJSON
			stored.LimitImpactJSON = ""
			stored.AppliedOverridesJSON = log.AppliedOverridesJSON
			stored.PrimaryAction = log.PrimaryAction
			stored.ActionConfidence = log.ActionConfidence
			stored.CharacterizationVersion = log.CharacterizationVersion
			stored.CharacterizationJSON = log.CharacterizationJSON
			return nil
		})
	}
	s.publishRequestLog(log, req.TrustedMetadata)
	s.completeCharacterizationLog(requestID, req.TrustedMetadata, req.Characterization)
	finished := finishedAt.UTC()
	event := RequestRejectedEvent{
		RequestID:             requestID,
		RouteKind:             req.Meta.RouteKind,
		IncomingModel:         req.Meta.IncomingModel,
		Lane:                  req.Meta.Lane,
		StatusCode:            statusCode,
		State:                 taskState,
		Reason:                reason,
		EstimatedInputTokens:  estimatedInput,
		EstimatedOutputTokens: estimatedOutput,
		QueuedAt:              enqueuedAt.UTC(),
		FinishedAt:            finished,
		WaitMS:                max(finished.Sub(enqueuedAt).Milliseconds(), 0),
		Streaming:             req.Meta.Streaming,
		Priority:              req.Meta.Priority,
		FallbackCount:         fallbackCount,
		Metadata:              cloneMetadata(req.TrustedMetadata),
		Characterization:      characterizationFromHandle(req.Characterization),
	}
	fallback := tenancy.ContextWithMetadataScope(s.backgroundContext(), req.TrustedMetadata)
	hookCtx, cancelHook := persistenceContext(ctx, fallback)
	defer cancelHook()
	for _, hook := range s.rejectedHooks {
		if hook != nil {
			hook(hookCtx, event)
		}
	}
}

func chooseSooner(current, candidate Rejection) Rejection {
	if current.Reason == "" {
		return candidate
	}
	if candidate.NearestAt.IsZero() {
		return current
	}
	if current.NearestAt.IsZero() || candidate.NearestAt.Before(current.NearestAt) {
		return candidate
	}
	return current
}

func candidateTrace(selection *queuedSelection, candidates []router.Candidate) []CandidateTrace {
	trace := append([]CandidateTrace(nil), selection.tracePrefix...)
	trace = append(trace, CandidateTrace{
		EndpointID:      selection.candidate.Endpoint.ID,
		EndpointUUID:    selection.candidate.Endpoint.UUID,
		EndpointName:    selection.candidate.Endpoint.Name,
		ProviderID:      selection.candidate.Endpoint.ProviderID,
		ProviderUUID:    selection.candidate.Endpoint.ProviderUUID,
		UpstreamModel:   selection.candidate.Endpoint.UpstreamModel,
		Rank:            selection.candidate.Rank,
		FallbackCount:   selection.candidate.FallbackCount,
		EligibleAt:      selection.evaluation.EligibleAt,
		PredictedWaitMS: selection.retry.Milliseconds(),
		Decision:        "selected",
		Reason:          selection.evaluation.Reason,
		EffectiveLimits: selection.evaluation.EffectiveLimits,
	})
	return trace
}

func candidateGroupKey(meta router.RequestMeta, candidate router.Candidate) string {
	if candidate.Lane != nil {
		if candidate.Lane.UUID != "" {
			return "lane:" + candidate.Lane.UUID
		}
		if candidate.Lane.ID != 0 {
			return fmt.Sprintf("lane:%d", candidate.Lane.ID)
		}
		if name := strings.TrimSpace(candidate.Lane.Name); name != "" {
			// A non-nil Lane is already a resolved routing-group selection. Some
			// embedded/test callers construct that selection before persistence,
			// so its stable name is the final identity fallback.
			return "lane-name:" + name
		}
	}
	// A routing group is represented by a resolved lane. Incoming model names
	// and request metadata are deliberately not fallback lock identities: direct
	// model/endpoint calls have no candidate Lane and must remain concurrent
	// unless a real shared policy or cooldown coordinates them.
	return ""
}

func (s *Scheduler) reserveUsageEstimates(taskID string, scopes []limits.ScopeRef, estimatedTokens, estimatedSpend int64, at time.Time) {
	s.reserveUsageEstimatesWithVisibility(taskID, scopes, estimatedTokens, estimatedSpend, at, false)
}

func (s *Scheduler) reserveRuntimeUsageEstimates(taskID string, scopes []limits.ScopeRef, estimatedTokens, estimatedSpend int64, at time.Time) {
	s.reserveUsageEstimatesWithVisibility(taskID, scopes, estimatedTokens, estimatedSpend, at, true)
}

func (s *Scheduler) reserveUsageEstimatesWithVisibility(taskID string, scopes []limits.ScopeRef, estimatedTokens, estimatedSpend int64, at time.Time, visible bool) {
	reserve := s.tracker.Reserve
	if visible {
		reserve = s.tracker.ReserveRuntime
	}
	reserve(taskID, scopes, models.MetricRequests, 1, at)
	if s.resolver == nil || s.resolver.ReserveEstimatedTokensForLimits() {
		reserve(taskID, scopes, models.MetricTokens, estimatedTokens, at)
	}
	if s.resolver == nil || s.resolver.ReserveEstimatedSpendForLimits() {
		reserve(taskID, scopes, models.MetricSpend, estimatedSpend, at)
	}
}

func (s *Scheduler) shouldReserveOnEnqueue(task *task) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shouldReserveOnEnqueueLocked(task)
}

func (s *Scheduler) shouldReserveOnEnqueueLocked(task *task) bool {
	if task == nil || isRequestScopedDefer(task.deferScope) {
		return false
	}
	if !s.serialGroupDispatch {
		return true
	}
	return candidateGroupKey(task.meta, task.candidate) == ""
}

func candidateScopes(candidate router.Candidate) []limits.ScopeRef {
	scopes := []limits.ScopeRef{
		{Type: models.ScopeGlobal, ID: 0, Key: "global"},
		{Type: models.ScopeProvider, ID: candidate.Endpoint.ProviderID, Key: candidate.Endpoint.ProviderUUID},
		{Type: models.ScopeEndpoint, ID: candidate.Endpoint.ID, Key: candidate.Endpoint.UUID},
	}
	if candidate.Lane != nil {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeLane, ID: candidate.Lane.ID, Key: candidate.Lane.UUID})
	}
	return scopes
}

func (s *Scheduler) limitScopesForCandidate(ctx context.Context, req SubmitRequest, candidate router.Candidate) ([]limits.ScopeRef, error) {
	bindings := defaultScopeBindings(candidate)
	current := scopeDescriptors(bindings)
	if len(s.limitHooks) == 0 {
		return scopeRefsForDescriptors(current, bindings)
	}
	for _, hook := range s.limitHooks {
		if hook == nil {
			continue
		}
		next, err := hook(ctx, LimitScopeInput{
			RequestID:     req.Meta.RequestID,
			RouteKind:     string(req.Meta.RouteKind),
			IncomingModel: req.Meta.IncomingModel,
			Lane:          req.Meta.Lane,
			Metadata:      cloneMetadata(req.TrustedMetadata),
			Scopes:        cloneLimitScopes(current),
		})
		if err != nil {
			return nil, err
		}
		if next != nil {
			current = cloneLimitScopes(next)
		}
	}
	return scopeRefsForDescriptors(current, bindings)
}

func (s *Scheduler) evaluateExternalLimits(ctx context.Context, req SubmitRequest, candidate router.Candidate, estimatedTokens, estimatedSpend int64, now time.Time) (limits.CandidateEvaluation, []limits.ScopeRef, string, string, string, time.Time, string, error) {
	return s.evaluateExternalLimitsExcludingTask(ctx, req, candidate, estimatedTokens, estimatedSpend, now, "")
}

func (s *Scheduler) evaluateExternalLimitsExcludingTask(ctx context.Context, req SubmitRequest, candidate router.Candidate, estimatedTokens, estimatedSpend int64, now time.Time, excludedTaskID string) (limits.CandidateEvaluation, []limits.ScopeRef, string, string, string, time.Time, string, error) {
	evaluation := limits.CandidateEvaluation{EligibleAt: now}
	if len(s.externalLimitHooks) == 0 {
		return evaluation, nil, "", "", "", time.Time{}, "", nil
	}
	refs := make([]limits.ScopeRef, 0)
	seenRefs := make(map[limits.ScopeRef]bool)
	deferScope := ""
	deferScopeID := ""
	deferReason := ""
	userEligibleAt := time.Time{}
	userLimitReason := ""
	for _, hook := range s.externalLimitHooks {
		if hook == nil {
			continue
		}
		items, err := hook(ctx, ExternalLimitInput{
			RequestID:       req.Meta.RequestID,
			RouteKind:       string(req.Meta.RouteKind),
			IncomingModel:   req.Meta.IncomingModel,
			Lane:            req.Meta.Lane,
			EndpointID:      candidate.Endpoint.UUID,
			EndpointName:    candidate.Endpoint.Name,
			ProviderID:      candidate.Endpoint.ProviderUUID,
			UpstreamModel:   candidate.Endpoint.UpstreamModel,
			LaneID:          candidateLaneUUID(candidate),
			Metadata:        cloneMetadata(req.TrustedMetadata),
			EstimatedTokens: estimatedTokens,
			EstimatedSpend:  estimatedSpend,
			Preview:         req.Preview,
		})
		if err != nil {
			return limits.CandidateEvaluation{}, nil, "", "", "", time.Time{}, "", err
		}
		for _, item := range items {
			metric, ok := externalMetric(item.Metric)
			if !ok {
				continue
			}
			period, ok := externalPeriod(item.Period)
			if !ok || item.LimitValue <= 0 {
				continue
			}
			ref := externalScopeRef(item.Scope)
			if !seenRefs[ref] {
				seenRefs[ref] = true
				refs = append(refs, ref)
			}
			needed := s.externalMetricNeed(metric, estimatedTokens, estimatedSpend)
			effective := item.LimitValue
			configured := item.LimitValue
			used := item.Used + s.tracker.UsedExcludingTask(ref, metric, period, now, excludedTaskID)
			entry := limits.EffectiveLimit{
				Metric:               metric,
				Period:               period,
				Configured:           &configured,
				Effective:            &effective,
				ScopeType:            models.ScopeType(item.Scope.Type),
				ScopeUUID:            item.Scope.ID,
				Used:                 used,
				Reserved:             max(item.Reserved, 0),
				ResetAt:              item.ResetAt.UTC(),
				ActorScoped:          item.ActorScoped,
				CapacityKeyNamespace: strings.TrimSpace(item.CapacityKeyNamespace),
				CapacityLabel:        strings.TrimSpace(item.CapacityLabel),
			}
			evaluation.EffectiveLimits = append(evaluation.EffectiveLimits, entry)
			if used+needed <= item.LimitValue {
				continue
			}
			next := item.ResetAt.UTC()
			if next.IsZero() || !next.After(now) {
				next = s.tracker.NextEligibleAtExcludingTask(ref, metric, period, max(item.LimitValue-item.Used, 0), needed, now, excludedTaskID)
			}
			itemDeferScope := externalLimitDeferScope(item)
			if isRequestScopedDefer(itemDeferScope) && next.After(now) {
				if requestScopedDeferPriority(itemDeferScope) > requestScopedDeferPriority(deferScope) {
					deferScope = itemDeferScope
					deferScopeID = strings.TrimSpace(item.Scope.ID)
					deferReason = externalLimitDeferReason(item)
				}
				if next.After(userEligibleAt) {
					userEligibleAt = next
					userLimitReason = string(metric) + "/" + string(period)
				}
			}
			if next.After(evaluation.EligibleAt) {
				evaluation.EligibleAt = next
				evaluation.Reason = string(metric) + "/" + string(period)
			}
		}
	}
	evaluation.RetryAfter = evaluation.EligibleAt.Sub(now)
	return evaluation, refs, deferScope, deferScopeID, deferReason, userEligibleAt, userLimitReason, nil
}

func mergeEvaluations(base limits.CandidateEvaluation, external limits.CandidateEvaluation, now time.Time) limits.CandidateEvaluation {
	if base.EligibleAt.IsZero() {
		base.EligibleAt = now
	}
	if external.EligibleAt.After(base.EligibleAt) {
		base.EligibleAt = external.EligibleAt
		base.Reason = external.Reason
	}
	base.EffectiveLimits = append(base.EffectiveLimits, external.EffectiveLimits...)
	base.RetryAfter = base.EligibleAt.Sub(now)
	return base
}

func (s *Scheduler) taskWaitBudgetDuration(task *task) time.Duration {
	if task == nil {
		return 0
	}
	waitBudget := task.meta.MaxWaitMS
	if waitBudget <= 0 {
		waitBudget = s.defaultWaitMS
	}
	if waitBudget <= 0 {
		return 0
	}
	return time.Duration(waitBudget) * time.Millisecond
}

func (s *Scheduler) refreshTaskEligibility(ctx context.Context, task *task, now time.Time) error {
	if task == nil {
		return nil
	}
	if len(task.candidates) > 1 {
		return s.reselectTaskCandidate(ctx, task, now)
	}
	limitScopes := taskLimitScopes(task)
	evaluation, err := s.resolver.EvaluateIgnoringEndpointStateExcludingTaskReservations(ctx, limitScopes, task.candidate.Endpoint, taskLaneID(task), task.estimatedInput+task.estimatedOutput, task.estimatedCost, now, task.id)
	if err != nil {
		return err
	}
	resourceEligibleAt := evaluation.EligibleAt
	externalEvaluation, externalScopes, externalDeferScope, externalDeferScopeID, externalDeferReason, userEligibleAt, userLimitReason, err := s.evaluateExternalLimitsExcludingTask(ctx, SubmitRequest{
		Meta:            task.meta,
		TrustedMetadata: task.trustedMetadata,
		Preview:         task.preview,
	}, task.candidate, task.estimatedInput+task.estimatedOutput, task.estimatedCost, now, task.id)
	if err != nil {
		return err
	}
	deferScope := ""
	deferScopeID := ""
	deferReason := ""
	if isRequestScopedDefer(externalDeferScope) {
		deferScope = externalDeferScope
		deferScopeID = externalDeferScopeID
		deferReason = externalDeferReason
	}
	evaluation = mergeEvaluations(evaluation, externalEvaluation, now)
	s.mu.Lock()
	task.evaluation = evaluation
	task.resourceEligibleAt = resourceEligibleAt
	task.userEligibleAt = userEligibleAt
	task.userLimitReason = userLimitReason
	task.deferScope = deferScope
	task.deferScopeID = deferScopeID
	task.deferReason = deferReason
	task.limitScopes = append([]limits.ScopeRef(nil), limitScopes...)
	task.reservationScopes = reservationScopesForDecision(limitScopes, externalScopes, deferScope, resourceEligibleAt, now)
	s.mu.Unlock()
	if waitBudget := s.taskWaitBudgetDuration(task); waitBudget > 0 {
		retry := evaluation.EligibleAt.Sub(now)
		if retry > waitBudget {
			return Rejection{
				Reason:         "all candidates exceed wait budget",
				RetryAfter:     retry,
				NearestAt:      evaluation.EligibleAt,
				CandidateIDs:   candidateUUIDs(task.candidate.Endpoint),
				CandidateTrace: append([]CandidateTrace(nil), task.candidateTrace...),
			}
		}
	}
	return nil
}

func (s *Scheduler) refreshTaskCatalogIfChanged(ctx context.Context, task *task) error {
	if s == nil || s.store == nil || task == nil {
		return nil
	}
	generation := s.store.CatalogGeneration(ctx)
	s.mu.Lock()
	currentGeneration := task.catalogGeneration
	s.mu.Unlock()
	if generation == currentGeneration {
		return nil
	}
	strategy := s.routingStrategy
	if strategy == nil {
		strategy = router.NewWaterfallStrategy(s.store)
	}
	candidates, err := strategy.SelectCandidates(ctx, task.meta)
	if err != nil {
		return fmt.Errorf("refresh routing catalog for request %s: %w", task.meta.RequestID, err)
	}
	if len(candidates) == 0 {
		return fmt.Errorf("refresh routing catalog for request %s: no candidates", task.meta.RequestID)
	}
	s.mu.Lock()
	task.candidates = append([]router.Candidate(nil), candidates...)
	task.catalogGeneration = generation
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) reselectTaskCandidate(ctx context.Context, task *task, now time.Time) error {
	candidates := task.candidates
	if len(candidates) == 0 {
		candidates = []router.Candidate{task.candidate}
	}
	meta := task.meta
	meta.EstimatedInputTokens = task.estimatedInput
	meta.EstimatedOutputTokens = task.estimatedOutput
	req := SubmitRequest{
		Meta:            meta,
		Candidates:      candidates,
		EnqueuedAt:      task.enqueuedAt,
		TrustedMetadata: cloneMetadata(task.trustedMetadata),
		Preview:         task.preview,
	}
	waitBudget := meta.MaxWaitMS
	if waitBudget <= 0 {
		waitBudget = s.defaultWaitMS
	}
	selection, err := s.selectCandidateWithOptions(ctx, req, now, waitBudget, false)
	if err != nil {
		return err
	}
	if selection.retry < 0 {
		selection.retry = 0
	}
	s.mu.Lock()
	applySelectionToTask(task, selection, candidates)
	s.mu.Unlock()
	return nil
}

func applySelectionToTask(task *task, selection *queuedSelection, candidates []router.Candidate) {
	task.candidate = selection.candidate
	task.meta.Overrides = routingOverrides(task.meta.Overrides, selection.candidate.RoutingMetadata)
	task.evaluation = selection.evaluation
	task.resourceEligibleAt = selection.resourceEligibleAt
	task.userEligibleAt = selection.userEligibleAt
	task.userLimitReason = selection.userLimitReason
	task.deferScope = selection.deferScope
	task.deferScopeID = selection.deferScopeID
	task.deferReason = selection.deferReason
	task.limitScopes = append([]limits.ScopeRef(nil), selection.limitScopes...)
	task.reservationScopes = append([]limits.ScopeRef(nil), selection.reservationScopes...)
	task.estimatedCost = selection.estimatedCost
	task.estimatedInput = selection.estimatedInput
	task.estimatedOutput = selection.estimatedOutput
	task.candidateTrace = candidateTrace(selection, candidates)
}

func routingOverrides(existing map[string]any, metadata map[string]string) map[string]any {
	if len(metadata) == 0 {
		return existing
	}
	out := make(map[string]any, len(existing)+1)
	for key, value := range existing {
		out[key] = value
	}
	routing := make(map[string]string, len(metadata))
	for key, value := range metadata {
		routing[key] = value
	}
	out["routing_decision"] = routing
	return out
}

func (s *Scheduler) externalMetricNeed(metric models.Metric, estimatedTokens, estimatedSpend int64) int64 {
	switch metric {
	case models.MetricTokens:
		if s.resolver == nil || s.resolver.ReserveEstimatedTokensForLimits() {
			return estimatedTokens
		}
		return 1
	case models.MetricSpend:
		if s.resolver == nil || s.resolver.ReserveEstimatedSpendForLimits() {
			return estimatedSpend
		}
		return 1
	default:
		return 1
	}
}

func externalMetric(value string) (models.Metric, bool) {
	switch models.Metric(strings.TrimSpace(value)) {
	case models.MetricRequests:
		return models.MetricRequests, true
	case models.MetricTokens:
		return models.MetricTokens, true
	case models.MetricSpend:
		return models.MetricSpend, true
	case models.MetricConcurrency:
		return models.MetricConcurrency, true
	default:
		return "", false
	}
}

func externalPeriod(value string) (models.Period, bool) {
	switch models.Period(strings.TrimSpace(value)) {
	case models.PeriodSecond:
		return models.PeriodSecond, true
	case models.PeriodMinute:
		return models.PeriodMinute, true
	case models.PeriodHour:
		return models.PeriodHour, true
	case models.PeriodDay:
		return models.PeriodDay, true
	case models.PeriodMonth:
		return models.PeriodMonth, true
	default:
		return "", false
	}
}

func externalScopeRef(scope LimitScope) limits.ScopeRef {
	scopeType := strings.TrimSpace(scope.Type)
	if scopeType == "" {
		scopeType = "external"
	}
	scopeID := strings.TrimSpace(scope.ID)
	if scopeID == "" {
		scopeID = "default"
	}
	return limits.ScopeRef{Type: models.ScopeType(scopeType), Key: scopeID}
}

func defaultScopeBindings(candidate router.Candidate) []scopeBinding {
	bindings := []scopeBinding{
		{ref: limits.ScopeRef{Type: models.ScopeGlobal, ID: 0, Key: "global"}, scope: LimitScope{Type: string(models.ScopeGlobal), ID: "global"}},
		{ref: limits.ScopeRef{Type: models.ScopeProvider, ID: candidate.Endpoint.ProviderID, Key: candidate.Endpoint.ProviderUUID}, scope: LimitScope{Type: string(models.ScopeProvider), ID: candidate.Endpoint.ProviderUUID}},
		{ref: limits.ScopeRef{Type: models.ScopeEndpoint, ID: candidate.Endpoint.ID, Key: candidate.Endpoint.UUID}, scope: LimitScope{Type: string(models.ScopeEndpoint), ID: candidate.Endpoint.UUID}},
	}
	if candidate.Lane != nil {
		bindings = append(bindings, scopeBinding{
			ref:   limits.ScopeRef{Type: models.ScopeLane, ID: candidate.Lane.ID, Key: candidate.Lane.UUID},
			scope: LimitScope{Type: string(models.ScopeLane), ID: candidate.Lane.UUID},
		})
	}
	return bindings
}

func scopeDescriptors(bindings []scopeBinding) []LimitScope {
	scopes := make([]LimitScope, 0, len(bindings))
	for _, binding := range bindings {
		scopes = append(scopes, binding.scope)
	}
	return scopes
}

func scopeRefsForDescriptors(scopes []LimitScope, bindings []scopeBinding) ([]limits.ScopeRef, error) {
	lookup := make(map[string]limits.ScopeRef, len(bindings))
	for _, binding := range bindings {
		lookup[scopeKey(binding.scope)] = binding.ref
	}
	refs := make([]limits.ScopeRef, 0, len(scopes))
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		key := scopeKey(scope)
		if seen[key] {
			continue
		}
		ref, ok := lookup[key]
		if !ok {
			return nil, fmt.Errorf("unknown limit scope %s", key)
		}
		seen[key] = true
		refs = append(refs, ref)
	}
	return refs, nil
}

func scopeKey(scope LimitScope) string {
	return strings.TrimSpace(scope.Type) + "\x00" + strings.TrimSpace(scope.ID)
}

func cloneLimitScopes(scopes []LimitScope) []LimitScope {
	if scopes == nil {
		return nil
	}
	return append([]LimitScope(nil), scopes...)
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func actorIDFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ""
	}
	if value := strings.TrimSpace(metadata["actor_id"]); value != "" {
		return value
	}
	return strings.TrimSpace(metadata["user_uuid"])
}

func apiKeyUUIDFromMetadata(metadata map[string]string) string {
	return strings.TrimSpace(metadata["api_key_uuid"])
}

func organizationUUIDFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ""
	}
	return strings.TrimSpace(metadata["organization_uuid"])
}

func configuredPoliciesFromEvaluation(items []limits.EffectiveLimit) []models.LimitPolicy {
	seen := make(map[uint]bool, len(items))
	policies := make([]models.LimitPolicy, 0, len(items))
	for _, item := range items {
		if item.PolicyID == 0 || item.PolicyUUID == "" || seen[item.PolicyID] {
			continue
		}
		seen[item.PolicyID] = true
		updatedAt := time.Time{}
		if item.PolicyVersion > 0 {
			updatedAt = time.Unix(0, item.PolicyVersion).UTC()
		}
		limitValue := int64(0)
		if item.Configured != nil {
			limitValue = *item.Configured
		}
		policies = append(policies, models.LimitPolicy{
			ID:         item.PolicyID,
			UUID:       item.PolicyUUID,
			ScopeType:  item.ScopeType,
			ScopeID:    item.ScopeID,
			ScopeUUID:  item.ScopeUUID,
			Metric:     item.Metric,
			Period:     item.Period,
			LimitValue: limitValue,
			Enabled:    true,
			UpdatedAt:  updatedAt,
		})
	}
	return policies
}

// completionConfiguredPolicies uses the immutable limit decision copied into
// the issued permit. A task may be re-evaluated while it waits or requeues, so
// its mutable evaluation is not authoritative once dispatch has begun.
func completionConfiguredPolicies(task *task, applied []limits.EffectiveLimit) []models.LimitPolicy {
	policies := configuredPoliciesFromEvaluation(applied)
	if len(policies) > 0 || task == nil {
		return policies
	}
	return configuredPoliciesFromEvaluation(task.evaluation.EffectiveLimits)
}

// completionLimitScopes guarantees that every configured policy in the
// dispatch permit receives the terminal usage event. The cached task scopes
// remain useful for scopes without a configured limit, but they cannot be the
// sole source of attribution because candidate/task state can change between
// selection and completion.
func completionLimitScopes(task *task, applied []limits.EffectiveLimit) []limits.ScopeRef {
	if task == nil {
		return nil
	}
	scopes := taskLimitScopes(task)
	seen := make(map[string]struct{}, len(scopes)+len(applied))
	scopeIdentity := func(scope limits.ScopeRef) string {
		if scope.ID != 0 || scope.Type == models.ScopeGlobal {
			return string(scope.Type) + "\x00" + strconv.FormatUint(uint64(scope.ID), 10)
		}
		return string(scope.Type) + "\x00" + strings.TrimSpace(scope.Key)
	}
	for _, scope := range scopes {
		seen[scopeIdentity(scope)] = struct{}{}
	}
	for _, item := range applied {
		if item.PolicyID == 0 || item.ScopeType == "" {
			continue
		}
		scope := limits.ScopeRef{Type: item.ScopeType, ID: item.ScopeID, Key: item.ScopeUUID}
		key := scopeIdentity(scope)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		scopes = append(scopes, scope)
	}
	return scopes
}

func taskScopes(task *task) []limits.ScopeRef {
	return taskReservationScopes(task)
}

func taskLimitScopes(task *task) []limits.ScopeRef {
	if len(task.limitScopes) > 0 {
		return append([]limits.ScopeRef(nil), task.limitScopes...)
	}
	return candidateScopes(task.candidate)
}

func taskReservationScopes(task *task) []limits.ScopeRef {
	if len(task.reservationScopes) > 0 {
		return append([]limits.ScopeRef(nil), task.reservationScopes...)
	}
	return taskLimitScopes(task)
}

func reservationScopesForDecision(limitScopes []limits.ScopeRef, externalScopes []limits.ScopeRef, deferScope string, resourceEligibleAt time.Time, now time.Time) []limits.ScopeRef {
	if isRequestScopedDefer(deferScope) && len(externalScopes) > 0 {
		return append([]limits.ScopeRef(nil), externalScopes...)
	}
	scopes := append([]limits.ScopeRef(nil), limitScopes...)
	scopes = append(scopes, externalScopes...)
	return scopes
}

func isRequestScopedDefer(scope string) bool {
	switch strings.TrimSpace(scope) {
	case deferScopeUser, deferScopeUserModel, deferScopeUserProvider,
		deferScopeAPIKey, deferScopeAPIKeyModel, deferScopeAPIKeyProvider:
		return true
	default:
		return false
	}
}

func isCandidateScopedDefer(scope string) bool {
	switch strings.TrimSpace(scope) {
	case deferScopeUserProvider, deferScopeAPIKeyProvider:
		return true
	default:
		return false
	}
}

func requestScopedDeferPriority(scope string) int {
	switch strings.TrimSpace(scope) {
	case deferScopeUser, deferScopeAPIKey:
		return 3
	case deferScopeUserModel, deferScopeAPIKeyModel:
		return 2
	case deferScopeUserProvider, deferScopeAPIKeyProvider:
		return 1
	default:
		return 0
	}
}

func externalLimitDeferScope(item ExternalLimit) string {
	scope := strings.TrimSpace(item.DeferScope)
	if scope == "" && strings.TrimSpace(item.Scope.Type) == deferScopeUser {
		return deferScopeUser
	}
	if isRequestScopedDefer(scope) {
		return scope
	}
	return ""
}

func externalLimitDeferReason(item ExternalLimit) string {
	if reason := strings.TrimSpace(item.DeferReason); reason != "" {
		return reason
	}
	switch externalLimitDeferScope(item) {
	case deferScopeAPIKeyModel:
		return "api_key_model_limit_exceeded"
	case deferScopeAPIKeyProvider:
		return "api_key_provider_limit_exceeded"
	case deferScopeAPIKey:
		return "api_key_limit_exceeded"
	case deferScopeUserModel:
		return deferReasonUserModelExceeded
	case deferScopeUserProvider:
		return deferReasonUserProviderLimit
	case deferScopeUser:
		return deferReasonUserLimitExceeded
	default:
		return ""
	}
}

func taskLaneID(task *task) uint {
	if task.candidate.Lane != nil {
		return task.candidate.Lane.ID
	}
	return 0
}

func candidateLaneUUID(candidate router.Candidate) string {
	if candidate.Lane == nil {
		return ""
	}
	return candidate.Lane.UUID
}

func timerChan(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func nilIfZero(v uint) *uint {
	if v == 0 {
		return nil
	}
	return &v
}

func setRequestLogCandidate(log *models.RequestLog, candidate router.Candidate) {
	if log == nil {
		return
	}
	log.EndpointID = nilIfZero(candidate.Endpoint.ID)
	log.EndpointUUID = optionalString(candidate.Endpoint.UUID)
	log.ProviderID = nilIfZero(candidate.Endpoint.ProviderID)
	log.ProviderUUID = optionalString(candidate.Endpoint.ProviderUUID)
	if candidate.Lane == nil {
		log.LaneID = nil
		log.LaneUUID = nil
		return
	}
	log.LaneID = nilIfZero(candidate.Lane.ID)
	log.LaneUUID = optionalString(candidate.Lane.UUID)
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func marshalJSON(v any) string {
	body, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(body)
}

func characterizationFromHandle(handle *characterization.Handle) characterization.Characterization {
	if handle == nil {
		return characterization.Characterization{Version: characterization.Version, TaxonomyVersion: characterization.TaxonomyVersion, PrimaryAction: characterization.ActionUnknown, ClassifierStatus: characterization.StatusDisabled}
	}
	return handle.FinalizeTerminal()
}

func applyCharacterizationToLog(log *models.RequestLog, handle *characterization.Handle) {
	if log == nil {
		return
	}
	result := characterizationFromHandle(handle)
	if result.ClassifierStatus == characterization.StatusDisabled {
		log.PrimaryAction = nil
		log.ActionConfidence = nil
		log.CharacterizationVersion = nil
		log.CharacterizationJSON = nil
		return
	}
	primary := string(result.PrimaryAction)
	version := result.Version
	confidence := result.Confidence
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	text := string(encoded)
	log.PrimaryAction = &primary
	log.ActionConfidence = &confidence
	log.CharacterizationVersion = &version
	log.CharacterizationJSON = &text
}

func applyGuardrailSummaryToLog(log *models.RequestLog, summary guardrails.Summary) {
	if log == nil {
		return
	}
	log.GuardrailStatus = summary.Status
	log.GuardrailDurationMS = summary.DurationMS
	log.GuardrailPreDurationMS = summary.PreDurationMS
	log.GuardrailPostDurationMS = summary.PostDurationMS
	log.GuardrailResultsJSON = guardrails.MarshalBoundedSummary(summary, 16*1024)
	log.GuardrailResponseBodiesJSON = guardrails.MarshalBoundedResponses(summary, 64*1024*1024)
}
