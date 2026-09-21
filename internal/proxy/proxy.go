package proxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/body"
	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/observability"
	"github.com/anchorshell/relay/internal/pricing"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/tokenestimate"
	"github.com/anchorshell/relay/internal/transport"
	"github.com/anchorshell/relay/pkg/characterization"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type Server struct {
	store                           *store.Store
	router                          router.Strategy
	scheduler                       *scheduler.Scheduler
	bodyStore                       *body.Store
	transport                       *transport.UpstreamTransport
	httpClient                      *http.Client
	telemetry                       *telemetry.Hub
	requestContextHooks             []RequestContextHook
	payloadCaptureHooks             []PayloadCapturePolicyHook
	requestAcceptedHooks            []RequestAcceptedHook
	requestContextBypass            func(*echo.Context) bool
	inFlightHooks                   []RequestInFlightHook
	completedHooks                  []RequestCompletedHook
	healthMu                        sync.Mutex
	failures                        map[uint]int
	queuedBodies                    QueuedBodyStore
	upstreamAdapters                []UpstreamRequestAdapter
	upstreamDispatchers             []UpstreamDispatcher
	characterizer                   *characterization.Manager
	characterizationEngine          func(context.Context, string, TrustedRequestContext) (characterization.EngineID, error)
	characterizationPolicies        []CharacterizationPolicyHook
	routingCharacterizationPolicies []RoutingCharacterizationPolicyHook
	guardrails                      *guardrails.Runtime
}

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

type QueuedBodyStore interface {
	PersistQueuedBody(context.Context, QueuedBodyInput) (string, error)
	OpenQueuedBody(context.Context, string, string) (io.ReadCloser, int64, error)
	DeleteQueuedBody(context.Context, string) error
}

type UpstreamRequestInput struct {
	RouteKind           models.RouteKind
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

type UpstreamRequestAdapter func(context.Context, UpstreamRequestInput) (map[string]any, error)

type UpstreamDispatchInput struct {
	RouteKind           models.RouteKind
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

type UpstreamDispatcher func(context.Context, UpstreamDispatchInput) (*http.Response, bool, error)

type RequestInFlightEvent struct {
	RequestID       string
	RouteKind       models.RouteKind
	IncomingModel   string
	Lane            string
	EndpointID      string
	EndpointName    string
	ProviderID      string
	UpstreamModel   string
	ReasoningEffort string
	Streaming       bool
	Waited          time.Duration
	SelectedAt      time.Time
	TrustedContext  TrustedRequestContext
}

type RequestInFlightHook func(ctx context.Context, event RequestInFlightEvent)

type RequestCompletedEvent struct {
	RequestID             string
	RouteKind             models.RouteKind
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
	TrustedContext        TrustedRequestContext
	Characterization      characterization.Characterization
}

type RequestCompletedHook func(ctx context.Context, event RequestCompletedEvent)

type RequestContextInput struct {
	RequestID string
	RouteKind models.RouteKind
	Headers   http.Header
}

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

type PayloadCapturePolicyInput struct {
	RequestID      string
	TrustedContext TrustedRequestContext
}

type PayloadCapturePolicyHook func(ctx context.Context, input PayloadCapturePolicyInput) (bool, error)

type RequestContextRejection struct {
	Status     int
	Message    string
	Code       string
	RetryAfter time.Duration
}

type RequestAcceptedEvent struct {
	RequestID     string
	RouteKind     models.RouteKind
	IncomingModel string
	Lane          string
	Streaming     bool
	Metadata      map[string]string
}

type RequestAcceptedHook func(ctx context.Context, event RequestAcceptedEvent) (map[string]string, error)

type CharacterizationPolicyHook func(ctx context.Context, requestID string, trusted TrustedRequestContext) bool

type RoutingCharacterizationPolicyInput struct {
	RequestID     string
	RouteKind     models.RouteKind
	IncomingModel string
	Lane          string
	Trusted       TrustedRequestContext
}

type RoutingCharacterizationPolicyHook func(ctx context.Context, input RoutingCharacterizationPolicyInput) bool

func (r RequestContextRejection) Error() string {
	if strings.TrimSpace(r.Message) != "" {
		return r.Message
	}
	return http.StatusText(r.Status)
}

const progressPublishInterval = 250 * time.Millisecond
const routingCharacterizationWait = 30 * time.Millisecond
const defaultMaxLatencyMS int64 = 10 * 60 * 1000
const maxStoredPayloadBytes = 1024 * 1024
const maxBufferedResponseBytes int64 = 16 * 1024 * 1024
const upstreamThrottleInitialBackoff = 5 * time.Second
const upstreamThrottleSecondBackoff = 20 * time.Second
const upstreamThrottleMaxBackoff = 5 * time.Minute

var (
	errUpstreamMaxLatencyExceeded = errors.New("upstream max latency exceeded")
	errSelfReferentialProvider    = errors.New("provider base URL points back to Relay's routed endpoint; use a provider base URL ending in /v1/dummy for the built-in dummy provider")
)

func cloneMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (s *Server) payloadCaptureAllowed(ctx context.Context, requestID string, trusted TrustedRequestContext) bool {
	for _, hook := range s.payloadCaptureHooks {
		if hook == nil {
			continue
		}
		allowed, err := hook(ctx, PayloadCapturePolicyInput{RequestID: requestID, TrustedContext: trusted})
		if err != nil {
			slog.Warn("payload capture policy failed; capture disabled", "request_id", requestID, "error", err)
			return false
		}
		if !allowed {
			return false
		}
	}
	return true
}

type upstreamThrottleError struct {
	reason     string
	retryAfter time.Duration
	statusCode int
}

func (e upstreamThrottleError) Error() string {
	if e.reason == "" {
		return "upstream rate limited"
	}
	return e.reason
}

func NewServer(st *store.Store, strategy router.Strategy, sch *scheduler.Scheduler, bodyStore *body.Store, rt *transport.UpstreamTransport, hub *telemetry.Hub) *Server {
	if rt.Base == nil {
		rt.Base = http.DefaultTransport
	}
	return &Server{
		store:     st,
		router:    strategy,
		scheduler: sch,
		bodyStore: bodyStore,
		transport: rt,
		httpClient: &http.Client{
			Transport:     observability.HTTPTransport(rt),
			CheckRedirect: sameOriginRedirectOnly,
		},
		telemetry: hub,
		failures:  map[uint]int{},
	}
}

func (s *Server) WithRequestInFlightHooks(hooks []RequestInFlightHook) *Server {
	if s == nil {
		return s
	}
	s.inFlightHooks = append([]RequestInFlightHook(nil), hooks...)
	return s
}

func (s *Server) WithRequestCompletedHooks(hooks []RequestCompletedHook) *Server {
	if s == nil {
		return s
	}
	s.completedHooks = append([]RequestCompletedHook(nil), hooks...)
	return s
}

func (s *Server) WithRequestContextHooks(hooks []RequestContextHook) *Server {
	if s == nil {
		return s
	}
	s.requestContextHooks = append([]RequestContextHook(nil), hooks...)
	return s
}

func (s *Server) WithRequestAcceptedHooks(hooks []RequestAcceptedHook) *Server {
	if s == nil {
		return s
	}
	s.requestAcceptedHooks = append([]RequestAcceptedHook(nil), hooks...)
	return s
}

func (s *Server) WithPayloadCapturePolicyHooks(hooks []PayloadCapturePolicyHook) *Server {
	if s == nil {
		return s
	}
	s.payloadCaptureHooks = append([]PayloadCapturePolicyHook(nil), hooks...)
	return s
}

func (s *Server) WithUpstreamRequestAdapters(adapters []UpstreamRequestAdapter) *Server {
	if s == nil {
		return s
	}
	s.upstreamAdapters = append([]UpstreamRequestAdapter(nil), adapters...)
	return s
}

func (s *Server) WithUpstreamDispatchers(dispatchers []UpstreamDispatcher) *Server {
	if s == nil {
		return s
	}
	s.upstreamDispatchers = append([]UpstreamDispatcher(nil), dispatchers...)
	return s
}

func (s *Server) WithRequestContextBypass(fn func(*echo.Context) bool) *Server {
	if s == nil {
		return s
	}
	s.requestContextBypass = fn
	return s
}

func (s *Server) WithQueuedBodyStore(store QueuedBodyStore) *Server {
	if s != nil {
		s.queuedBodies = store
	}
	return s
}

func (s *Server) WithCharacterization(manager *characterization.Manager, policies []CharacterizationPolicyHook) *Server {
	if s != nil {
		s.characterizer = manager
		s.characterizationPolicies = append([]CharacterizationPolicyHook(nil), policies...)
	}
	return s
}

func (s *Server) WithRoutingCharacterizationPolicies(policies []RoutingCharacterizationPolicyHook) *Server {
	if s != nil {
		s.routingCharacterizationPolicies = append([]RoutingCharacterizationPolicyHook(nil), policies...)
	}
	return s
}

func (s *Server) WithGuardrails(runtime *guardrails.Runtime) *Server {
	if s != nil {
		s.guardrails = runtime
	}
	return s
}

func (s *Server) Register(g *echo.Group) {
	g.POST("/dummy/chat/completions", s.dummyChatCompletions)
	g.POST("/chat/completions", s.handle("chat/completions", models.RouteKindChat))
	g.POST("/responses", s.handle("responses", models.RouteKindResponses))
	g.POST("/embeddings", s.handle("embeddings", models.RouteKindEmbeddings))
	g.GET("/models", s.models)
}

func (s *Server) handle(path string, routeKind models.RouteKind) echo.HandlerFunc {
	return func(c *echo.Context) error {
		requestID := uuid.NewString()
		handle, err := s.bodyStore.Save(c.Request().Context(), c.Request().Body)
		if err != nil {
			if errors.Is(err, body.ErrTooLarge) {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
			}
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		defer handle.Cleanup()

		meta, bodyMap, requestBodyBytes, err := s.requestMeta(c, requestID, routeKind, handle)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		verifyCtx, verifySpan := observability.Tracer().Start(c.Request().Context(), "relay.context.verify")
		trustedContext, err := s.requestContext(verifyCtx, c, requestID, routeKind)
		if err != nil {
			verifySpan.SetStatus(codes.Error, "request context rejected")
		}
		verifySpan.End()
		if err != nil {
			var rejection RequestContextRejection
			if errors.As(err, &rejection) {
				status := rejection.Status
				if status < 400 || status > 599 {
					status = http.StatusUnauthorized
				}
				message := rejection.Message
				if message == "" {
					message = http.StatusText(status)
				}
				if rejection.Code != "" {
					c.Response().Header().Set("X-Anchorshell-Relay-Error", rejection.Code)
				}
				if rejection.RetryAfter > 0 {
					c.Response().Header().Set("Retry-After", strconv.FormatInt(max(1, int64(rejection.RetryAfter.Seconds())), 10))
				}
				return echo.NewHTTPError(status, message)
			}
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid request context")
		}
		if logicalID := strings.TrimSpace(trustedContext.LogicalRequestID); logicalID != "" {
			if len(logicalID) > 200 || strings.ContainsAny(logicalID, "\r\n\x00") {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid request context")
			}
			requestID = logicalID
			meta.RequestID = logicalID
		}
		observability.SetRequestAttributes(c.Request().Context(), requestID,
			attribute.String("anchorshell.relay.route_kind", string(routeKind)),
			attribute.Bool("anchorshell.relay.streaming", meta.Streaming),
		)
		scopedCtx := tenancy.ContextWithMetadataScope(c.Request().Context(), trustedContext.Metadata)
		if scopedCtx != c.Request().Context() {
			c.SetRequest(c.Request().WithContext(scopedCtx))
		}
		storeRequests := s.store != nil && s.store.GetSettingBool(c.Request().Context(), "store_requests", false)
		if storeRequests && !s.payloadCaptureAllowed(c.Request().Context(), requestID, trustedContext) {
			storeRequests = false
		}
		queuedBodyReference := ""
		defer func() {
			if queuedBodyReference != "" && s.queuedBodies != nil {
				_ = s.queuedBodies.DeleteQueuedBody(context.WithoutCancel(c.Request().Context()), queuedBodyReference)
			}
		}()

		characterizationHandle := characterization.DisabledHandle()
		routingCharacterization := s.routingCharacterizationRequired(c.Request().Context(), RoutingCharacterizationPolicyInput{
			RequestID: requestID, RouteKind: routeKind, IncomingModel: meta.IncomingModel, Lane: meta.Lane, Trusted: trustedContext,
		})
		if routingCharacterization {
			characterizationHandle = s.submitCharacterization(c.Request().Context(), requestID, routeKind, bodyMap, meta, requestBodyBytes, c.Request().UserAgent(), trustedContext, false)
			result := characterizationHandle.Finalize(characterizationHandle.RoutingWait(routingCharacterizationWait))
			meta.Characterization = result
		}

		routeCtx, routeSpan := observability.Tracer().Start(c.Request().Context(), "relay.route.select")
		candidates, err := s.router.SelectCandidates(routeCtx, meta)
		routeSpan.SetAttributes(attribute.Int("anchorshell.relay.candidate_count", len(candidates)))
		if err != nil {
			routeSpan.SetStatus(codes.Error, "route selection failed")
		}
		routeSpan.End()
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		meta = applyRoutingPolicy(meta, candidates)
		acceptCtx, acceptSpan := observability.Tracer().Start(c.Request().Context(), "relay.request.accept")
		for _, hook := range s.requestAcceptedHooks {
			if hook == nil {
				continue
			}
			additional, hookErr := hook(acceptCtx, RequestAcceptedEvent{
				RequestID:     requestID,
				RouteKind:     routeKind,
				IncomingModel: meta.IncomingModel,
				Lane:          meta.Lane,
				Streaming:     meta.Streaming,
				Metadata:      cloneMetadata(trustedContext.Metadata),
			})
			if hookErr != nil {
				var rejection RequestContextRejection
				if errors.As(hookErr, &rejection) {
					acceptSpan.SetStatus(codes.Error, "request rejected")
					acceptSpan.End()
					status := rejection.Status
					if status < 400 || status > 599 {
						status = http.StatusServiceUnavailable
					}
					if rejection.Code != "" {
						c.Response().Header().Set("X-Anchorshell-Relay-Error", rejection.Code)
					}
					if rejection.RetryAfter > 0 {
						c.Response().Header().Set("Retry-After", strconv.FormatInt(max(1, int64(rejection.RetryAfter.Seconds())), 10))
					}
					message := rejection.Message
					if message == "" {
						message = http.StatusText(status)
					}
					return echo.NewHTTPError(status, message)
				}
				acceptSpan.SetStatus(codes.Error, "request acceptance failed")
				acceptSpan.End()
				return hookErr
			}
			if trustedContext.Metadata == nil {
				trustedContext.Metadata = map[string]string{}
			}
			for key, value := range additional {
				trustedContext.Metadata[key] = value
			}
		}
		acceptSpan.End()

		if !routingCharacterization && s.characterizationAllowed(c.Request().Context(), requestID, trustedContext) {
			characterizationHandle = s.submitCharacterization(c.Request().Context(), requestID, routeKind, bodyMap, meta, requestBodyBytes, c.Request().UserAgent(), trustedContext, true)
			characterizationHandle.AllowBackground()
		}

		submitReq := scheduler.SubmitRequest{
			Meta:             meta,
			Candidates:       candidates,
			Body:             requestBodyBytes,
			BodyBytes:        handle.Size(),
			EnqueuedAt:       time.Now().UTC(),
			TrustedMetadata:  trustedContext.Metadata,
			PartitionKey:     trustedContext.PartitionKey,
			Characterization: characterizationHandle,
		}
		if s.queuedBodies != nil {
			submitReq.OnQueued = func(ctx context.Context, estimatedInput, estimatedOutput int64) error {
				if queuedBodyReference != "" {
					return nil
				}
				ownerGeneration, _ := strconv.ParseInt(strings.TrimSpace(trustedContext.Metadata["owner_generation"]), 10, 64)
				reference, persistErr := s.queuedBodies.PersistQueuedBody(ctx, QueuedBodyInput{
					RequestID: requestID, OrganizationUUID: tenancy.OrganizationUUID(ctx), ContentType: c.Request().Header.Get("Content-Type"),
					Body: requestBodyBytes, OriginalModel: meta.IncomingModel,
					EstimatedInputTokens: estimatedInput, EstimatedOutputTokens: estimatedOutput, OwnerGeneration: ownerGeneration,
				})
				if persistErr != nil {
					return persistErr
				}
				queuedBodyReference = reference
				requestBodyBytes = nil
				bodyMap = nil
				handle.ReleaseMemory()
				submitReq.Body = nil
				submitReq.Meta.EstimatedInputTokens = estimatedInput
				submitReq.Meta.EstimatedOutputTokens = estimatedOutput
				return nil
			}
		}
		submitCtx, submitSpan := observability.Tracer().Start(c.Request().Context(), "relay.scheduler.wait")
		permit, err := s.scheduler.Submit(submitCtx, submitReq)
		submitSpan.SetAttributes(attribute.Int64("anchorshell.relay.queue_wait_ms", permit.Waited.Milliseconds()))
		if permit.Endpoint.UUID != "" {
			submitSpan.SetAttributes(
				attribute.String("anchorshell.relay.endpoint.id", permit.Endpoint.UUID),
				attribute.String("anchorshell.relay.provider.id", permit.Endpoint.ProviderUUID),
			)
		}
		if err != nil {
			submitSpan.SetStatus(codes.Error, "scheduler admission failed")
		}
		submitSpan.End()
		if err != nil {
			var rejection scheduler.Rejection
			if errors.As(err, &rejection) {
				c.Response().Header().Set("Retry-After", strconv.FormatInt(int64(rejection.RetryAfter.Seconds()), 10))
				responsePayload := waitBudgetRejectionPayload(rejection)
				if storeRequests {
					if responseBody, marshalErr := json.Marshal(responsePayload); marshalErr == nil {
						s.storeCapturedRequestBodies(c.Request().Context(), true, requestID, requestBodyBytes, nil, responseBody)
					}
				}
				return c.JSON(http.StatusTooManyRequests, responsePayload)
			}
			return err
		}

		var activeTaskMu sync.RWMutex
		activeTaskID := permit.TaskID
		setActiveTaskID := func(taskID string) {
			activeTaskMu.Lock()
			activeTaskID = taskID
			activeTaskMu.Unlock()
		}
		stopCancelWatch := context.AfterFunc(c.Request().Context(), func() {
			activeTaskMu.RLock()
			taskID := activeTaskID
			activeTaskMu.RUnlock()
			if taskID == "" {
				return
			}
			_ = s.scheduler.Cancel(taskID)
		})
		defer stopCancelWatch()
		var guardrailResults []guardrails.Result

		for {
			activeCtx, detachPermitContext := s.attachPermitContext(c.Request().Context(), permit)
			if isPermitContextCancelled(c.Request().Context(), activeCtx) {
				detachPermitContext()
				_ = s.scheduler.Cancel(permit.TaskID)
				return requestCancelledError(c.Request().Context())
			}

			prepareStarted := permit.SelectedAt.UTC()
			if prepareStarted.IsZero() {
				prepareStarted = time.Now().UTC()
			}
			prepareCtx, prepareSpan := observability.Tracer().Start(activeCtx, "relay.scheduler.selected_to_proxy_start",
				trace.WithTimestamp(prepareStarted),
				trace.WithAttributes(
					attribute.String("anchorshell.request.id", requestID),
					attribute.String("anchorshell.relay.endpoint.id", permit.Endpoint.UUID),
				),
			)
			provider, credential, endpoint, err := s.store.ResolveProviderCredential(prepareCtx, permit.Endpoint.ID)
			if err != nil {
				prepareSpan.SetStatus(codes.Error, "provider route preparation failed")
				prepareSpan.End()
				if isPermitContextCancelled(c.Request().Context(), activeCtx) {
					detachPermitContext()
					_ = s.scheduler.Cancel(permit.TaskID)
					return requestCancelledError(c.Request().Context())
				}
				detachPermitContext()
				_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusBadGateway, err.Error())
				return echo.NewHTTPError(http.StatusBadGateway, "upstream route unavailable")
			}
			permit.Endpoint = endpoint
			observability.SetRequestAttributes(c.Request().Context(), requestID,
				attribute.String("anchorshell.relay.endpoint.id", endpoint.UUID),
				attribute.String("anchorshell.relay.provider.id", endpoint.ProviderUUID),
			)
			var effectiveGuardrails []guardrails.EffectiveGuardrail
			if s.guardrails != nil {
				laneID := uint(0)
				if permit.Lane != nil {
					laneID = permit.Lane.ID
				}
				effectiveGuardrails, err = s.guardrails.Resolve(prepareCtx, laneID, provider.ID, endpoint.ID)
				if err != nil {
					prepareSpan.SetStatus(codes.Error, "guardrail resolution failed")
					prepareSpan.End()
					detachPermitContext()
					_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusServiceUnavailable, "guardrail configuration unavailable")
					return echo.NewHTTPError(http.StatusServiceUnavailable, "guardrail configuration unavailable")
				}
			}
			if meta.Streaming && guardrails.HasEnabledStage(effectiveGuardrails) {
				prepareSpan.End()
				streamResult := guardrails.Result{Decision: guardrails.DecisionError, Stage: guardrails.StagePreDispatch, HTTPStatus: http.StatusBadRequest, ErrorCode: "guardrails_require_non_streaming"}
				guardrailResults = append(guardrailResults, streamResult)
				summary := guardrails.Summarize(guardrailResults, storeRequests)
				s.scheduler.SetGuardrailSummary(permit.TaskID, summary)
				detachPermitContext()
				_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusBadRequest, "streaming is unavailable while guardrails are enabled")
				return c.JSON(http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "Streaming is unavailable while guardrails are enabled for this route.", "type": "guardrail_streaming_unsupported", "param": "stream", "code": "guardrails_require_non_streaming"}})
			}
			if routeKind == models.RouteKindEmbeddings && guardrails.HasEnabledStage(effectiveGuardrails) {
				prepareSpan.End()
				unsupported := guardrails.Result{Decision: guardrails.DecisionError, Stage: guardrails.StagePreDispatch, HTTPStatus: http.StatusBadRequest, ErrorCode: "guardrails_route_kind_unsupported"}
				guardrailResults = append(guardrailResults, unsupported)
				s.scheduler.SetGuardrailSummary(permit.TaskID, guardrails.Summarize(guardrailResults, storeRequests))
				detachPermitContext()
				_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusBadRequest, "guardrails are unavailable for embeddings")
				return c.JSON(http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "Guardrails are unavailable for embeddings requests.", "type": "guardrail_route_kind_unsupported", "param": "model", "code": "guardrails_route_kind_unsupported"}})
			}
			guardrailInput, inputErr := s.guardrailInput(c.Request().Context(), requestID, meta, requestBodyBytes, bodyMap, queuedBodyReference, routeKind, permit, provider, endpoint, characterizationHandle.Snapshot())
			if inputErr != nil && guardrails.HasEnabledStage(effectiveGuardrails) {
				prepareSpan.SetStatus(codes.Error, "guardrail input unavailable")
				prepareSpan.End()
				result := guardrails.Result{Decision: guardrails.DecisionError, Stage: guardrails.StagePreDispatch, HTTPStatus: http.StatusServiceUnavailable, ErrorCode: "guardrail_input_unavailable"}
				guardrailResults = append(guardrailResults, result)
				s.scheduler.SetGuardrailSummary(permit.TaskID, guardrails.Summarize(guardrailResults, storeRequests))
				detachPermitContext()
				_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusServiceUnavailable, "guardrail input unavailable")
				return echo.NewHTTPError(http.StatusServiceUnavailable, "guardrail input unavailable")
			}
			if s.guardrails != nil {
				preCtx, preSpan := observability.Tracer().Start(prepareCtx, "guardrail.pre_dispatch")
				s.publishGuardrailStarts(requestID, guardrails.StagePreDispatch, effectiveGuardrails)
				preResults, terminal := s.guardrails.EvaluateStage(preCtx, guardrails.StagePreDispatch, guardrailInput, effectiveGuardrails)
				guardrailResults = append(guardrailResults, preResults...)
				s.publishGuardrailResults(requestID, preResults)
				if terminal != nil {
					preSpan.SetAttributes(guardrailTraceAttributes(*terminal)...)
				}
				preSpan.End()
				if terminal != nil {
					summary := guardrails.Summarize(guardrailResults, storeRequests)
					s.scheduler.SetGuardrailSummary(permit.TaskID, summary)
					status, payload := guardrails.ClientError(*terminal, guardrails.DefaultFailurePolicy())
					prepareSpan.End()
					detachPermitContext()
					_ = s.scheduler.Fail(c.Request().Context(), permit, status, "blocked by guardrail")
					capturedRequestBody := requestBodyBytes
					if len(capturedRequestBody) == 0 {
						capturedRequestBody = guardrailInput.Request.RawJSON
					}
					capturedResponseBody, _ := json.Marshal(payload)
					s.storeCapturedRequestBodies(context.WithoutCancel(c.Request().Context()), storeRequests, requestID, capturedRequestBody, nil, capturedResponseBody)
					return c.JSON(status, payload)
				}
			}
			latencyCtx, cancelLatency, maxLatencyMS := s.withMaxLatencyContext(activeCtx, provider, endpoint)
			prepareSpan.End()

			start := time.Now().UTC()
			startedAt := permit.SelectedAt.UTC()
			if startedAt.IsZero() {
				startedAt = start
			}
			s.publishRequestInFlight(c.Request().Context(), meta, requestID, permit, startedAt, trustedContext)
			s.publishRequestProgress(meta, requestID, permit, waitingSubstatus(routeKind), startedAt, permit.EstimatedInput, 0)
			resp, actualIn, actualOut, upstreamRequestBytes, err := s.dispatch(latencyCtx, c, path, provider, credential, endpoint, bodyMap, queuedBodyReference, permit.EstimatedInput, permit.EstimatedOutput)
			if err != nil {
				if errors.Is(err, errSelfReferentialProvider) {
					cancelLatency()
					detachPermitContext()
					_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusBadRequest, err.Error())
					return echo.NewHTTPError(http.StatusBadRequest, err.Error())
				}
				timedOut := isMaxLatencyExceeded(latencyCtx)
				cancelLatency()
				if isPermitContextCancelled(c.Request().Context(), activeCtx) {
					detachPermitContext()
					_ = s.scheduler.Cancel(permit.TaskID)
					return requestCancelledError(c.Request().Context())
				}
				if timedOut {
					nextPermit, handled, nextErr := s.handleUpstreamFailure(c.Request().Context(), &submitReq, permit, http.StatusGatewayTimeout, maxLatencyReason(maxLatencyMS))
					if handled {
						detachPermitContext()
						if nextErr != nil {
							return nextErr
						}
						permit = nextPermit
						setActiveTaskID(permit.TaskID)
						continue
					}
				}
				nextPermit, handled, nextErr := s.handleUpstreamFailure(c.Request().Context(), &submitReq, permit, http.StatusBadGateway, err.Error())
				if handled {
					detachPermitContext()
					if nextErr != nil {
						return nextErr
					}
					permit = nextPermit
					setActiveTaskID(permit.TaskID)
					continue
				}
				detachPermitContext()
				_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusBadGateway, err.Error())
				return echo.NewHTTPError(http.StatusBadGateway, "upstream request failed")
			}

			if meta.Streaming {
				s.publishRequestProgress(meta, requestID, permit, streamingSubstatus(routeKind), startedAt, permit.EstimatedInput, 0)
				if reason, failed := detectUpstreamServerFailure(resp.StatusCode, nil); failed {
					nextPermit, handled, nextErr := s.handleUpstreamFailure(c.Request().Context(), &submitReq, permit, resp.StatusCode, reason)
					_ = resp.Body.Close()
					cancelLatency()
					if handled {
						detachPermitContext()
						if nextErr != nil {
							return nextErr
						}
						permit = nextPermit
						setActiveTaskID(permit.TaskID)
						continue
					}
				}
				if retryAfter, reason, shouldRetry := detectUpstreamThrottle(resp, nil, permit.AppliedLimitState); shouldRetry {
					_ = resp.Body.Close()
					cancelLatency()
					permit, err = s.requeueUpstreamThrottle(c.Request().Context(), &submitReq, permit, retryAfter, reason, resp.StatusCode)
					if err != nil {
						detachPermitContext()
						return err
					}
					detachPermitContext()
					setActiveTaskID(permit.TaskID)
					continue
				}
				streamedAny := false
				var responseBodyBytes []byte
				if actualOut, streamedAny, responseBodyBytes, err = s.copyStreamingResponse(c, resp.Body, resp.Header, resp.StatusCode, meta, requestID, permit, startedAt, routeKind, storeRequests); err != nil {
					_ = resp.Body.Close()
					timedOut := isMaxLatencyExceeded(latencyCtx)
					cancelLatency()
					var throttleErr upstreamThrottleError
					if errors.As(err, &throttleErr) && !streamedAny {
						permit, err = s.requeueUpstreamThrottle(c.Request().Context(), &submitReq, permit, throttleErr.retryAfter, throttleErr.reason, throttleErr.statusCode)
						if err != nil {
							detachPermitContext()
							return err
						}
						detachPermitContext()
						setActiveTaskID(permit.TaskID)
						continue
					}
					if isPermitContextCancelled(c.Request().Context(), activeCtx) {
						detachPermitContext()
						_ = s.scheduler.Cancel(permit.TaskID)
						return requestCancelledError(c.Request().Context())
					}
					if timedOut && !streamedAny {
						nextPermit, handled, nextErr := s.handleUpstreamFailure(c.Request().Context(), &submitReq, permit, http.StatusGatewayTimeout, maxLatencyReason(maxLatencyMS))
						if handled {
							detachPermitContext()
							if nextErr != nil {
								return nextErr
							}
							permit = nextPermit
							setActiveTaskID(permit.TaskID)
							continue
						}
					}
					detachPermitContext()
					_ = s.scheduler.Fail(c.Request().Context(), permit, resp.StatusCode, err.Error())
					return err
				}
				_ = resp.Body.Close()
				cancelLatency()
				if isPermitContextCancelled(c.Request().Context(), activeCtx) || !s.scheduler.IsActive(permit.TaskID) {
					detachPermitContext()
					_ = s.scheduler.Cancel(permit.TaskID)
					return requestCancelledError(c.Request().Context())
				}
				if !streamedAny {
					applyProxyResponseHeaders(c, resp.Header, permit)
					c.Response().WriteHeader(resp.StatusCode)
				}
				if flusher, ok := c.Response().(http.Flusher); ok {
					flusher.Flush()
				}

				s.clearEndpointFailure(c.Request().Context(), permit.Endpoint)
				if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
					actualIn = 0
					actualOut = 0
				}
				actualCost, actualCostResolved := s.actualCostMicros(c.Request().Context(), permit, actualIn, actualOut)
				finishedAt := time.Now().UTC()
				latencyMS := finishedAt.Sub(start).Milliseconds()
				finalizeCtx, finalizeSpan := observability.Tracer().Start(c.Request().Context(), "relay.request.finalize")
				finalizeSpan.SetAttributes(
					attribute.Int("http.response.status_code", resp.StatusCode),
					attribute.String("anchorshell.relay.terminal_state", "completed"),
				)
				characterizationResult := characterizationHandle.FinalizeTerminal()
				s.publishRequestCompleted(finalizeCtx, meta, requestID, permit, trustedContext, characterizationResult, resp.StatusCode, "completed", startedAt, finishedAt, latencyMS, actualIn, actualOut, actualCost)
				if completeErr := s.completeSchedulerRequest(finalizeCtx, permit, resp.StatusCode, actualIn, actualOut, actualCost, actualCostResolved, finishedAt); completeErr != nil {
					finalizeSpan.SetStatus(codes.Error, "request finalization failed")
					slog.Error("Relay request finalization failed", "request_id", requestID, "status_code", resp.StatusCode, "error", completeErr)
				}
				finalizeSpan.End()
				s.storeCapturedRequestBodies(c.Request().Context(), storeRequests, requestID, requestBodyBytes, upstreamRequestBytes, responseBodyBytes)
				s.telemetry.Publish(telemetry.Event{
					Type: "request_finished",
					Payload: map[string]any{
						"request_id":              requestID,
						"task_id":                 permit.TaskID,
						"endpoint_id":             permit.Endpoint.UUID,
						"endpoint_name":           permit.Endpoint.Name,
						"provider_id":             permit.Endpoint.ProviderUUID,
						"organization_uuid":       permit.OrganizationUUID,
						"actor_id":                permit.ActorID,
						"selected_upstream_model": permit.Endpoint.UpstreamModel,
						"lane":                    meta.Lane,
						"incoming_model":          meta.IncomingModel,
						"state":                   "completed",
						"queued_at":               startedAt.Add(-permit.Waited).UTC(),
						"started_at":              startedAt.UTC(),
						"finished_at":             finishedAt,
						"wait_ms":                 permit.Waited.Milliseconds(),
						"latency_ms":              latencyMS,
						"status_code":             resp.StatusCode,
						"uploaded_tokens":         actualIn,
						"downloaded_tokens":       actualOut,
					},
				})
				detachPermitContext()
				setActiveTaskID("")
				return nil
			}

			bodyBytes, downloadedTokens, err := s.readResponseBody(resp.Body, meta, requestID, permit, startedAt, responseDownloadSubstatus(routeKind))
			_ = resp.Body.Close()
			if err != nil {
				timedOut := isMaxLatencyExceeded(latencyCtx)
				cancelLatency()
				if isPermitContextCancelled(c.Request().Context(), activeCtx) {
					detachPermitContext()
					_ = s.scheduler.Cancel(permit.TaskID)
					return requestCancelledError(c.Request().Context())
				}
				if timedOut {
					nextPermit, handled, nextErr := s.handleUpstreamFailure(c.Request().Context(), &submitReq, permit, http.StatusGatewayTimeout, maxLatencyReason(maxLatencyMS))
					if handled {
						detachPermitContext()
						if nextErr != nil {
							return nextErr
						}
						permit = nextPermit
						setActiveTaskID(permit.TaskID)
						continue
					}
				}
				detachPermitContext()
				_ = s.scheduler.Fail(c.Request().Context(), permit, http.StatusBadGateway, err.Error())
				return err
			}
			if reason, shouldHandle := detectUpstreamServerFailure(resp.StatusCode, bodyBytes); shouldHandle {
				cancelLatency()
				nextPermit, handled, nextErr := s.handleUpstreamFailure(c.Request().Context(), &submitReq, permit, resp.StatusCode, reason)
				if handled {
					detachPermitContext()
					if nextErr != nil {
						return nextErr
					}
					permit = nextPermit
					setActiveTaskID(permit.TaskID)
					continue
				}
			}
			if retryAfter, reason, shouldRetry := detectUpstreamThrottle(resp, bodyBytes, permit.AppliedLimitState); shouldRetry {
				cancelLatency()
				permit, err = s.requeueUpstreamThrottle(c.Request().Context(), &submitReq, permit, retryAfter, reason, resp.StatusCode)
				if err != nil {
					detachPermitContext()
					return err
				}
				detachPermitContext()
				setActiveTaskID(permit.TaskID)
				continue
			}
			s.publishRequestProgress(meta, requestID, permit, "downloading response", startedAt, permit.EstimatedInput, downloadedTokens)

			actualIn, actualOut = usageFromResponseForStatus(resp.StatusCode, bodyBytes, permit.EstimatedInput, permit.EstimatedOutput)
			actualCost, actualCostResolved := s.actualCostMicros(c.Request().Context(), permit, actualIn, actualOut)
			if isPermitContextCancelled(c.Request().Context(), activeCtx) || !s.scheduler.IsActive(permit.TaskID) {
				cancelLatency()
				detachPermitContext()
				_ = s.scheduler.Cancel(permit.TaskID)
				return requestCancelledError(c.Request().Context())
			}
			if s.guardrails != nil && len(effectiveGuardrails) > 0 {
				guardrailInput.Response = func() *guardrails.Response {
					value := guardrails.NormalizeResponse(bodyBytes, resp.StatusCode, resp.Header.Get("Content-Type"))
					return &value
				}()
				postCtx, postSpan := observability.Tracer().Start(activeCtx, "guardrail.post_response")
				s.publishGuardrailStarts(requestID, guardrails.StagePostResponse, effectiveGuardrails)
				postResults, terminal := s.guardrails.EvaluateStage(postCtx, guardrails.StagePostResponse, guardrailInput, effectiveGuardrails)
				guardrailResults = append(guardrailResults, postResults...)
				s.publishGuardrailResults(requestID, postResults)
				if terminal != nil {
					postSpan.SetAttributes(guardrailTraceAttributes(*terminal)...)
				}
				postSpan.End()
				if terminal != nil {
					if terminal.Decision == guardrails.DecisionReplaceResponse {
						bodyBytes = replacementResponseBody(routeKind, bodyBytes, terminal.ReplacementText, endpoint.UpstreamModel)
						resp.StatusCode = http.StatusOK
					} else {
						status, payload := guardrails.ClientError(*terminal, guardrails.DefaultFailurePolicy())
						bodyBytes, _ = json.Marshal(payload)
						resp.StatusCode = status
					}
					resp.Header.Del("Content-Length")
					resp.Header.Del("Content-Encoding")
					resp.Header.Set("Content-Type", "application/json")
				}
			}
			s.scheduler.SetGuardrailSummary(permit.TaskID, guardrails.Summarize(guardrailResults, storeRequests))
			applyProxyResponseHeaders(c, resp.Header, permit)
			applyProxyUsageHeaders(c, actualIn, actualOut, actualCost)
			c.Response().WriteHeader(resp.StatusCode)
			cancelLatency()
			_, clientWriteErr := c.Response().Write(bodyBytes)

			// Provider work and usage are authoritative once the bounded upstream
			// body has been received. A downstream disconnect must not turn that
			// completed provider attempt into a failed/no-usage scheduler record.
			s.clearEndpointFailure(c.Request().Context(), permit.Endpoint)
			finishedAt := time.Now().UTC()
			latencyMS := finishedAt.Sub(start).Milliseconds()
			finalizeCtx, finalizeSpan := observability.Tracer().Start(c.Request().Context(), "relay.request.finalize")
			finalizeSpan.SetAttributes(
				attribute.Int("http.response.status_code", resp.StatusCode),
				attribute.String("anchorshell.relay.terminal_state", "completed"),
			)
			characterizationResult := characterizationHandle.FinalizeTerminal()
			s.publishRequestCompleted(finalizeCtx, meta, requestID, permit, trustedContext, characterizationResult, resp.StatusCode, "completed", startedAt, finishedAt, latencyMS, actualIn, actualOut, actualCost)
			if completeErr := s.completeSchedulerRequest(finalizeCtx, permit, resp.StatusCode, actualIn, actualOut, actualCost, actualCostResolved, finishedAt); completeErr != nil {
				finalizeSpan.SetStatus(codes.Error, "request finalization failed")
				slog.Error("Relay request finalization failed", "request_id", requestID, "status_code", resp.StatusCode, "error", completeErr)
			}
			finalizeSpan.End()
			s.storeCapturedRequestBodies(c.Request().Context(), storeRequests, requestID, requestBodyBytes, upstreamRequestBytes, bodyBytes)
			s.telemetry.Publish(telemetry.Event{
				Type: "request_finished",
				Payload: map[string]any{
					"request_id":              requestID,
					"task_id":                 permit.TaskID,
					"endpoint_id":             permit.Endpoint.UUID,
					"endpoint_name":           permit.Endpoint.Name,
					"provider_id":             permit.Endpoint.ProviderUUID,
					"organization_uuid":       permit.OrganizationUUID,
					"actor_id":                permit.ActorID,
					"selected_upstream_model": permit.Endpoint.UpstreamModel,
					"lane":                    meta.Lane,
					"incoming_model":          meta.IncomingModel,
					"state":                   "completed",
					"queued_at":               startedAt.Add(-permit.Waited).UTC(),
					"started_at":              startedAt.UTC(),
					"finished_at":             finishedAt,
					"wait_ms":                 permit.Waited.Milliseconds(),
					"latency_ms":              latencyMS,
					"status_code":             resp.StatusCode,
					"uploaded_tokens":         actualIn,
					"downloaded_tokens":       actualOut,
				},
			})
			detachPermitContext()
			setActiveTaskID("")
			if clientWriteErr != nil {
				return clientWriteErr
			}
			return nil
		}
	}
}

func (s *Server) handleUpstreamFailure(ctx context.Context, submitReq *scheduler.SubmitRequest, permit scheduler.Permit, statusCode int, reason string) (scheduler.Permit, bool, error) {
	if isDownstreamCancelled(ctx) {
		_ = s.scheduler.Cancel(permit.TaskID)
		return scheduler.Permit{}, true, ctx.Err()
	}
	backoff, unhealthy, err := s.markEndpointFailure(ctx, permit.Endpoint, statusCode, reason)
	if err != nil {
		return scheduler.Permit{}, true, err
	}
	s.scheduler.ScheduleEndpointHealthSync(permit.TaskID, permit.Endpoint.ID, time.Now().UTC().Add(backoff))
	if unhealthy && submitReq.Meta.AllowFallback {
		remaining := filterCandidates(submitReq.Candidates, permit.Endpoint.ID)
		if len(remaining) > 0 {
			s.scheduler.ReleasePermit(permit)
			submitReq.Candidates = remaining
			nextPermit, err := s.scheduler.Submit(ctx, *submitReq)
			return nextPermit, true, err
		}
	}
	if err := prepareQueuedRequestBody(ctx, submitReq, permit); err != nil {
		return scheduler.Permit{}, true, err
	}
	nextPermit, err := s.scheduler.Requeue(ctx, permit, backoff, reason)
	return nextPermit, true, err
}

func (s *Server) requeueUpstreamThrottle(ctx context.Context, submitReq *scheduler.SubmitRequest, permit scheduler.Permit, detectedWait time.Duration, reason string, statusCode int) (scheduler.Permit, error) {
	wait, err := s.markEndpointThrottle(ctx, permit.Endpoint, detectedWait, reason, statusCode)
	if err != nil {
		return scheduler.Permit{}, err
	}
	s.scheduler.ScheduleEndpointHealthSync(permit.TaskID, permit.Endpoint.ID, time.Now().UTC().Add(wait))
	if err := prepareQueuedRequestBody(ctx, submitReq, permit); err != nil {
		return scheduler.Permit{}, err
	}
	return s.scheduler.Requeue(ctx, permit, wait, reason)
}

func prepareQueuedRequestBody(ctx context.Context, submitReq *scheduler.SubmitRequest, permit scheduler.Permit) error {
	if submitReq == nil || submitReq.OnQueued == nil || len(submitReq.Body) == 0 {
		return nil
	}
	estimatedInput := permit.EstimatedInput
	estimatedOutput := permit.EstimatedOutput
	if estimatedInput <= 0 {
		estimatedInput = submitReq.Meta.EstimatedInputTokens
	}
	if estimatedOutput <= 0 {
		estimatedOutput = submitReq.Meta.EstimatedOutputTokens
	}
	return submitReq.OnQueued(ctx, estimatedInput, estimatedOutput)
}

func isDownstreamCancelled(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

func (s *Server) attachPermitContext(parent context.Context, permit scheduler.Permit) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	s.scheduler.AttachCancel(permit.TaskID, cancel)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			s.scheduler.DetachCancel(permit.TaskID)
			cancel()
		})
	}
}

func isPermitContextCancelled(downstreamCtx, permitCtx context.Context) bool {
	if downstreamCtx != nil && downstreamCtx.Err() != nil {
		return true
	}
	return permitCtx != nil && permitCtx.Err() != nil
}

func requestCancelledError(downstreamCtx context.Context) error {
	if downstreamCtx != nil && downstreamCtx.Err() != nil {
		return downstreamCtx.Err()
	}
	return echo.NewHTTPError(499, "request cancelled")
}

func (s *Server) withMaxLatencyContext(ctx context.Context, provider models.Provider, endpoint models.Endpoint) (context.Context, context.CancelFunc, int64) {
	maxLatencyMS := s.resolveMaxLatencyMS(ctx, provider, endpoint)
	timeout := time.Duration(maxLatencyMS) * time.Millisecond
	timeoutCtx, cancel := context.WithTimeoutCause(ctx, timeout, errUpstreamMaxLatencyExceeded)
	return timeoutCtx, cancel, maxLatencyMS
}

func (s *Server) resolveMaxLatencyMS(ctx context.Context, provider models.Provider, endpoint models.Endpoint) int64 {
	switch {
	case endpoint.MaxLatencyMS > 0:
		return endpoint.MaxLatencyMS
	case provider.MaxLatencyMS > 0:
		return provider.MaxLatencyMS
	case s.store != nil:
		if value := s.store.GetSettingInt64(ctx, "default_max_latency_ms", defaultMaxLatencyMS); value > 0 {
			return value
		}
	}
	return defaultMaxLatencyMS
}

func isMaxLatencyExceeded(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errUpstreamMaxLatencyExceeded)
}

func maxLatencyReason(maxLatencyMS int64) string {
	if maxLatencyMS <= 0 {
		return "upstream max latency exceeded"
	}
	return "upstream max latency exceeded"
}

func filterCandidates(candidates []router.Candidate, excludeEndpointID uint) []router.Candidate {
	filtered := make([]router.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Endpoint.ID == excludeEndpointID {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func detectUpstreamServerFailure(statusCode int, body []byte) (string, bool) {
	if statusCode >= http.StatusInternalServerError {
		return http.StatusText(statusCode), true
	}
	message := strings.ToLower(extractUpstreamErrorMessage(body))
	switch {
	case strings.Contains(message, "internal server error"),
		strings.Contains(message, "service unavailable"),
		strings.Contains(message, "bad gateway"),
		strings.Contains(message, "gateway timeout"),
		strings.Contains(message, "temporarily unavailable"),
		strings.Contains(message, "server overloaded"):
		return "upstream server error", true
	default:
		return "", false
	}
}

func (s *Server) markEndpointFailure(ctx context.Context, endpoint models.Endpoint, statusCode int, reason string) (time.Duration, bool, error) {
	now := time.Now().UTC()

	s.healthMu.Lock()
	s.failures[endpoint.ID]++
	streak := s.failures[endpoint.ID]
	s.healthMu.Unlock()

	backoff := 30 * time.Second
	status := models.HealthCoolingDown
	switch {
	case streak == 1:
		backoff = 30 * time.Second
	case streak == 2:
		backoff = 60 * time.Second
	default:
		backoff = 5 * time.Minute
		status = models.HealthUnhealthy
	}

	until := now.Add(backoff)
	endpoint.CooldownUntil = &until
	endpoint.HealthStatus = status
	endpoint.CooldownReason = upstreamCooldownReason(statusCode)
	endpoint.CooldownStatusCode = statusCode
	if err := s.store.SaveEndpointState(ctx, endpoint); err != nil {
		return 0, false, err
	}
	if s.telemetry != nil {
		s.telemetry.Publish(telemetry.Event{
			Type: "endpoint_health_change",
			Payload: map[string]any{
				"organization_uuid":    tenancy.OrganizationUUID(ctx),
				"endpoint_id":          endpoint.UUID,
				"provider_id":          endpoint.ProviderUUID,
				"health_status":        endpoint.HealthStatus,
				"cooldown_until":       endpoint.CooldownUntil,
				"cooldown_reason":      endpoint.CooldownReason,
				"cooldown_status_code": endpoint.CooldownStatusCode,
				"reason":               reason,
				"status_code":          statusCode,
				"failure_streak":       streak,
				"updated_at":           now,
			},
		})
	}
	return backoff, status == models.HealthUnhealthy, nil
}

func (s *Server) markEndpointThrottle(ctx context.Context, endpoint models.Endpoint, detectedWait time.Duration, reason string, statusCode int) (time.Duration, error) {
	now := time.Now().UTC()

	s.healthMu.Lock()
	s.failures[endpoint.ID]++
	streak := s.failures[endpoint.ID]
	s.healthMu.Unlock()

	backoff := upstreamThrottleBackoff(streak)
	if detectedWait > backoff {
		backoff = detectedWait
	}
	if backoff <= 0 {
		backoff = upstreamThrottleInitialBackoff
	}

	until := now.Add(backoff)
	endpoint.CooldownUntil = &until
	endpoint.HealthStatus = models.HealthCoolingDown
	endpoint.CooldownReason = upstreamThrottleCooldownReason(statusCode, reason)
	if statusCode >= http.StatusBadRequest && statusCode <= 599 {
		endpoint.CooldownStatusCode = statusCode
	} else {
		endpoint.CooldownStatusCode = 0
	}
	if s.store != nil {
		if err := s.store.SaveEndpointState(ctx, endpoint); err != nil {
			return 0, err
		}
	}
	if s.telemetry != nil {
		s.telemetry.Publish(telemetry.Event{
			Type: "endpoint_health_change",
			Payload: map[string]any{
				"organization_uuid":    tenancy.OrganizationUUID(ctx),
				"endpoint_id":          endpoint.UUID,
				"provider_id":          endpoint.ProviderUUID,
				"health_status":        endpoint.HealthStatus,
				"cooldown_until":       endpoint.CooldownUntil,
				"cooldown_reason":      endpoint.CooldownReason,
				"cooldown_status_code": endpoint.CooldownStatusCode,
				"reason":               reason,
				"status_code":          endpoint.CooldownStatusCode,
				"throttle_streak":      streak,
				"updated_at":           now,
			},
		})
	}
	return backoff, nil
}

func upstreamCooldownReason(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "upstream_rate_limited"
	case statusCode >= http.StatusInternalServerError:
		return "upstream_server_error"
	default:
		return "upstream_unavailable"
	}
}

func upstreamThrottleCooldownReason(statusCode int, reason string) string {
	if statusCode >= http.StatusBadRequest && statusCode <= 599 {
		return upstreamCooldownReason(statusCode)
	}
	switch {
	case strings.Contains(strings.ToLower(reason), "rate limit"), strings.Contains(strings.ToLower(reason), "concurrency limit"):
		return "upstream_rate_limited"
	default:
		return "upstream_retry_after"
	}
}

func upstreamThrottleBackoff(streak int) time.Duration {
	switch {
	case streak <= 1:
		return upstreamThrottleInitialBackoff
	case streak == 2:
		return upstreamThrottleSecondBackoff
	}
	backoff := upstreamThrottleSecondBackoff
	for i := 3; i <= streak; i++ {
		backoff *= 2
		if backoff >= upstreamThrottleMaxBackoff {
			return upstreamThrottleMaxBackoff
		}
	}
	return backoff
}

func (s *Server) clearEndpointFailure(ctx context.Context, endpoint models.Endpoint) {
	s.healthMu.Lock()
	_, tracked := s.failures[endpoint.ID]
	if tracked {
		delete(s.failures, endpoint.ID)
	}
	s.healthMu.Unlock()
	if !tracked && endpoint.HealthStatus == models.HealthHealthy && endpoint.CooldownUntil == nil && endpoint.CooldownReason == "" && endpoint.CooldownStatusCode == 0 {
		return
	}

	endpoint.HealthStatus = models.HealthHealthy
	endpoint.CooldownUntil = nil
	endpoint.CooldownReason = ""
	endpoint.CooldownStatusCode = 0
	if s.store != nil {
		_ = s.store.SaveEndpointState(ctx, endpoint)
	}
	if s.telemetry != nil {
		s.telemetry.Publish(telemetry.Event{
			Type: "endpoint_health_change",
			Payload: map[string]any{
				"organization_uuid":    tenancy.OrganizationUUID(ctx),
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

func (s *Server) requestMeta(c *echo.Context, requestID string, routeKind models.RouteKind, handle *body.Handle) (router.RequestMeta, map[string]any, []byte, error) {
	reader, err := handle.Open()
	if err != nil {
		return router.RequestMeta{}, nil, nil, err
	}
	defer reader.Close()
	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		return router.RequestMeta{}, nil, nil, err
	}
	var payload map[string]any
	if len(bodyBytes) > 0 && c.Request().Method != http.MethodGet {
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			return router.RequestMeta{}, nil, nil, err
		}
	}
	incomingModel, _ := payload["model"].(string)
	streaming, _ := payload["stream"].(bool)
	reasoningEffort, err := requestReasoningEffort(payload)
	if err != nil {
		return router.RequestMeta{}, nil, nil, err
	}
	// Model is the only public routing selector. Scheduling policy and token
	// estimates are derived internally, never bound from headers, query values,
	// or proprietary body fields. Simulation controls have a separate parser.
	overrides := map[string]any{
		"lane":           incomingModel,
		"allow_fallback": true,
	}
	if reasoningEffort != "" {
		overrides["reasoning_effort"] = reasoningEffort
	}
	return router.RequestMeta{
		RequestID:       requestID,
		RouteKind:       routeKind,
		IncomingModel:   incomingModel,
		Lane:            incomingModel,
		AllowFallback:   true,
		Streaming:       streaming,
		ReasoningEffort: reasoningEffort,
		Overrides:       overrides,
	}, payload, bodyBytes, nil
}

func (s *Server) dispatch(ctx context.Context, c *echo.Context, path string, provider models.Provider, credential models.Credential, endpoint models.Endpoint, bodyMap map[string]any, queuedBodyReference string, estimatedIn, estimatedOut int64) (*http.Response, int64, int64, []byte, error) {
	_, buildSpan := observability.Tracer().Start(ctx, "relay.proxy.request_build")
	var bodyBytes []byte
	var dispatchedResponse *http.Response
	req, err := func() (*http.Request, error) {
		var requestBody io.ReadCloser
		var contentLength int64
		if queuedBodyReference != "" && s.queuedBodies != nil {
			opened, size, openErr := s.queuedBodies.OpenQueuedBody(ctx, queuedBodyReference, endpoint.UpstreamModel)
			if openErr != nil {
				return nil, openErr
			}
			queuedBytes, readErr := io.ReadAll(opened)
			closeErr := opened.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if int64(len(queuedBytes)) != size {
				return nil, errors.New("queued request body size mismatch")
			}
			if err := json.Unmarshal(queuedBytes, &bodyMap); err != nil {
				return nil, errors.New("queued request body is invalid")
			}
		} else {
			bodyMap = cloneJSONMap(bodyMap)
		}
		if bodyMap == nil {
			bodyMap = make(map[string]any)
		}
		bodyMap["model"] = endpoint.UpstreamModel
		for _, adapter := range s.upstreamAdapters {
			if adapter == nil {
				continue
			}
			adapted, adaptErr := adapter(ctx, UpstreamRequestInput{
				RouteKind: endpoint.RouteKind, ProviderStorageID: provider.ID, ProviderID: provider.UUID, ProviderKey: provider.Slug,
				CredentialStorageID: credential.ID, CredentialID: credential.UUID,
				EndpointStorageID: endpoint.ID, EndpointID: endpoint.UUID,
				ReasoningEffort: requestReasoningEffortUnchecked(bodyMap), Body: bodyMap,
			})
			if adaptErr != nil {
				return nil, adaptErr
			}
			if adapted != nil {
				bodyMap = adapted
			}
		}
		encoded, marshalErr := json.Marshal(bodyMap)
		if marshalErr != nil {
			return nil, marshalErr
		}
		bodyBytes = encoded
		streaming, _ := bodyMap["stream"].(bool)
		for _, dispatcher := range s.upstreamDispatchers {
			if dispatcher == nil {
				continue
			}
			response, handled, dispatchErr := dispatcher(ctx, UpstreamDispatchInput{
				RouteKind: endpoint.RouteKind, ProviderStorageID: provider.ID, ProviderID: provider.UUID, ProviderKey: provider.Slug,
				CredentialStorageID: credential.ID, CredentialID: credential.UUID,
				EndpointStorageID: endpoint.ID, EndpointID: endpoint.UUID, UpstreamModel: endpoint.UpstreamModel,
				Method: c.Request().Method, Path: path, Streaming: streaming, Body: append([]byte(nil), encoded...),
			})
			if dispatchErr != nil {
				return nil, dispatchErr
			}
			if !handled {
				continue
			}
			if response == nil || response.Body == nil {
				return nil, errors.New("upstream dispatcher returned an invalid response")
			}
			dispatchedResponse = response
			return nil, nil
		}
		contentLength = int64(len(encoded))
		requestBody = io.NopCloser(bytes.NewReader(encoded))
		baseURL, parseErr := url.Parse(provider.BaseURL)
		if parseErr != nil {
			return nil, parseErr
		}
		baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/" + path
		if isSelfReferentialProxyTarget(c.Request(), baseURL) {
			return nil, errSelfReferentialProvider
		}
		request, requestErr := http.NewRequestWithContext(ctx, c.Request().Method, baseURL.String(), requestBody)
		if requestErr != nil {
			_ = requestBody.Close()
			return nil, requestErr
		}
		request.ContentLength = contentLength
		request.Header = safeForwardRequestHeaders(c.Request().Header)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "AnchorShell-Model-Relay/1.0")
		tctx := transport.WithRequestContext(ctx, transport.RequestContext{
			Provider:   provider,
			Credential: credential,
			Endpoint:   endpoint,
		})
		return request.WithContext(tctx), nil
	}()
	if err != nil {
		buildSpan.SetStatus(codes.Error, "provider request build failed")
	}
	buildSpan.End()
	if err != nil {
		return nil, 0, 0, bodyBytes, err
	}
	if dispatchedResponse != nil {
		return dispatchedResponse, estimatedIn, estimatedOut, bodyBytes, nil
	}
	resp, err := s.httpClient.Do(req)
	return resp, estimatedIn, estimatedOut, bodyBytes, err
}

func (s *Server) guardrailInput(ctx context.Context, requestID string, meta router.RequestMeta, requestBytes []byte, payload map[string]any, queuedReference string, routeKind models.RouteKind, permit scheduler.Permit, provider models.Provider, endpoint models.Endpoint, characterized characterization.Characterization) (guardrails.Input, error) {
	raw := append([]byte(nil), requestBytes...)
	if len(raw) == 0 && queuedReference != "" && s.queuedBodies != nil {
		reader, _, err := s.queuedBodies.OpenQueuedBody(ctx, queuedReference, endpoint.UpstreamModel)
		if err != nil {
			return guardrails.Input{}, err
		}
		raw, err = io.ReadAll(io.LimitReader(reader, maxBufferedResponseBytes+1))
		closeErr := reader.Close()
		if err != nil {
			return guardrails.Input{}, err
		}
		if closeErr != nil {
			return guardrails.Input{}, closeErr
		}
		if int64(len(raw)) > maxBufferedResponseBytes {
			return guardrails.Input{}, errors.New("queued request exceeds guardrail normalization limit")
		}
	}
	if payload == nil {
		if len(raw) > 0 && json.Unmarshal(raw, &payload) != nil {
			return guardrails.Input{}, errors.New("guardrail request body is invalid")
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}
	request := guardrails.NormalizeRequest(raw, payload, "application/json")
	laneUUID := ""
	if permit.Lane != nil {
		laneUUID = permit.Lane.UUID
	}
	domains := make([]string, 0, len(characterized.Domains))
	for _, value := range characterized.Domains {
		domains = append(domains, string(value))
	}
	flags := make([]string, 0, len(characterized.ObservedFlags)+len(characterized.RequiredCapabilities))
	for _, value := range characterized.ObservedFlags {
		flags = append(flags, string(value))
	}
	for _, value := range characterized.RequiredCapabilities {
		flags = append(flags, string(value))
	}
	return guardrails.Input{Stage: guardrails.StagePreDispatch, RequestID: requestID, Request: request, Route: guardrails.Route{LaneUUID: laneUUID, LaneName: meta.Lane, ProviderUUID: provider.UUID, ProviderName: provider.Name, EndpointUUID: endpoint.UUID, EndpointName: endpoint.Name, UpstreamModel: endpoint.UpstreamModel}, Characterization: &guardrails.CharacterizationSummary{PrimaryAction: string(characterized.PrimaryAction), Domains: domains, Flags: flags, Confidence: characterized.Confidence}}, nil
}

func replacementResponseBody(routeKind models.RouteKind, original []byte, text, model string) []byte {
	var payload map[string]any
	_ = json.Unmarshal(original, &payload)
	if payload == nil {
		payload = map[string]any{}
	}
	if routeKind == models.RouteKindResponses {
		payload["object"] = "response"
		payload["model"] = model
		payload["status"] = "completed"
		payload["output_text"] = text
		payload["output"] = []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}
	} else {
		payload["object"] = "chat.completion"
		payload["model"] = model
		payload["choices"] = []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"error":{"message":"The configured guardrail replacement could not be returned.","type":"guardrail_error","code":"guardrail_replacement_error"}}`)
	}
	return encoded
}

func (s *Server) publishGuardrailResults(requestID string, results []guardrails.Result) {
	if s == nil || s.telemetry == nil {
		return
	}
	for _, result := range results {
		eventType := "guardrail_check_passed"
		switch {
		case result.FailMode == guardrails.FailOpen:
			eventType = "guardrail_check_failed_open"
		case result.Decision == guardrails.DecisionBlock || result.Decision == guardrails.DecisionReplaceResponse:
			eventType = "guardrail_check_blocked"
		case result.Decision == guardrails.DecisionError:
			eventType = "guardrail_check_error"
		}
		s.telemetry.Publish(telemetry.Event{Type: eventType, Payload: map[string]any{"request_id": requestID, "guardrail_uuid": result.GuardrailUUID, "preset": result.Preset, "stage": result.Stage, "decision": result.Decision, "duration_ms": result.Duration.Milliseconds(), "http_status": result.ProviderStatus, "error_code": result.ErrorCode}})
	}
}

func (s *Server) publishGuardrailStarts(requestID string, stage guardrails.Stage, effective []guardrails.EffectiveGuardrail) {
	if s.telemetry == nil {
		return
	}
	for _, item := range effective {
		if stage == guardrails.StagePreDispatch && !item.PreDispatchEnabled || stage == guardrails.StagePostResponse && !item.PostResponseEnabled {
			continue
		}
		s.telemetry.Publish(telemetry.Event{Type: "guardrail_check_started", Payload: map[string]any{"request_id": requestID, "guardrail_uuid": item.UUID, "preset": item.Preset, "stage": stage}})
	}
}

func guardrailTraceAttributes(result guardrails.Result) []attribute.KeyValue {
	bindingType := ""
	if len(result.BindingSources) > 0 {
		bindingType = result.BindingSources[0]
	}
	return []attribute.KeyValue{
		attribute.String("guardrail.uuid", result.GuardrailUUID),
		attribute.String("guardrail.preset", result.Preset),
		attribute.String("guardrail.stage", string(result.Stage)),
		attribute.String("guardrail.decision", string(result.Decision)),
		attribute.Int("guardrail.http_status", result.ProviderStatus),
		attribute.Int64("guardrail.duration_ms", result.Duration.Milliseconds()),
		attribute.String("guardrail.error_code", result.ErrorCode),
		attribute.String("guardrail.binding_type", bindingType),
	}
}

func requestReasoningEffort(payload map[string]any) (string, error) {
	top, topPresent := payload["reasoning_effort"]
	topValue := ""
	if topPresent {
		value, ok := top.(string)
		if !ok {
			return "", errors.New("reasoning_effort must be a string")
		}
		topValue = strings.ToLower(strings.TrimSpace(value))
	}
	nestedValue := ""
	if raw, present := payload["reasoning"]; present {
		reasoning, ok := raw.(map[string]any)
		if !ok {
			return "", errors.New("reasoning must be an object")
		}
		if rawEffort, present := reasoning["effort"]; present {
			value, ok := rawEffort.(string)
			if !ok {
				return "", errors.New("reasoning effort must be a string")
			}
			nestedValue = strings.ToLower(strings.TrimSpace(value))
		}
	}
	if topValue != "" && nestedValue != "" && topValue != nestedValue {
		return "", errors.New("conflicting reasoning effort values")
	}
	if topValue != "" {
		return topValue, nil
	}
	return nestedValue, nil
}

func requestReasoningEffortUnchecked(payload map[string]any) string {
	value, _ := requestReasoningEffort(payload)
	return value
}

func cloneJSONMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneJSONValue(value)
	}
	return output
}

func cloneJSONValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneJSONMap(item)
	case []any:
		output := make([]any, len(item))
		for index := range item {
			output[index] = cloneJSONValue(item[index])
		}
		return output
	default:
		return value
	}
}

func isSelfReferentialProxyTarget(req *http.Request, target *url.URL) bool {
	if req == nil || req.URL == nil || target == nil {
		return false
	}
	if cleanURLPath(req.URL.Path) != cleanURLPath(target.Path) {
		return false
	}

	requestHost := req.Host
	if requestHost == "" {
		requestHost = req.URL.Host
	}
	if requestHost == "" || target.Host == "" {
		return false
	}

	requestHost, requestPort := normalizedHostPort(requestHost, requestScheme(req))
	targetHost, targetPort := normalizedHostPort(target.Host, target.Scheme)
	if requestHost == "" || targetHost == "" || requestPort != targetPort {
		return false
	}
	if strings.EqualFold(requestHost, targetHost) {
		return true
	}
	return isLoopbackHost(requestHost) && isLoopbackHost(targetHost)
}

func cleanURLPath(value string) string {
	if value == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimLeft(value, "/"))
}

func requestScheme(req *http.Request) string {
	if req.URL != nil && req.URL.Scheme != "" {
		return req.URL.Scheme
	}
	if req.TLS != nil {
		return "https"
	}
	return "http"
}

func normalizedHostPort(rawHost, scheme string) (string, string) {
	host, port, err := net.SplitHostPort(rawHost)
	if err != nil {
		host = rawHost
		port = defaultPortForScheme(scheme)
	}
	if port == "" {
		port = defaultPortForScheme(scheme)
	}
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	return host, port
}

func defaultPortForScheme(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func applyRoutingPolicy(meta router.RequestMeta, candidates []router.Candidate) router.RequestMeta {
	if len(candidates) == 0 || candidates[0].Lane == nil {
		return meta
	}
	lane := candidates[0].Lane
	if meta.MaxWaitMS <= 0 {
		meta.MaxWaitMS = lane.DefaultMaxWaitMS
		if meta.Overrides != nil {
			meta.Overrides["max_wait_ms"] = meta.MaxWaitMS
		}
	}
	meta.AllowFallback = lane.AllowFallback
	if meta.Overrides != nil {
		meta.Overrides["allow_fallback"] = meta.AllowFallback
	}
	return meta
}

func applyProxyResponseHeaders(c *echo.Context, upstream http.Header, permit scheduler.Permit) {
	for key, values := range safeForwardResponseHeaders(upstream) {
		for _, value := range values {
			c.Response().Header().Add(key, value)
		}
	}
	c.Response().Header().Set("X-Relay-Selected-Endpoint", permit.Endpoint.Name)
	c.Response().Header().Set("X-Relay-Selected-Endpoint-Id", permit.Endpoint.UUID)
	c.Response().Header().Set("X-Relay-Selected-Provider-Id", permit.Endpoint.ProviderUUID)
	c.Response().Header().Set("X-Relay-Selected-Upstream-Model", permit.Endpoint.UpstreamModel)
	c.Response().Header().Set("X-Relay-Wait-Ms", strconv.FormatInt(permit.Waited.Milliseconds(), 10))
	c.Response().Header().Set("X-Relay-Fallback-Count", strconv.Itoa(permit.FallbackCount))
	c.Response().Header().Set("X-Relay-Estimated-Input-Tokens", strconv.FormatInt(permit.EstimatedInput, 10))
	c.Response().Header().Set("X-Relay-Estimated-Output-Tokens", strconv.FormatInt(permit.EstimatedOutput, 10))
	c.Response().Header().Set("X-Relay-Estimated-Cost-Micros", strconv.FormatInt(permit.EstimatedCost, 10))
}

func applyProxyUsageHeaders(c *echo.Context, actualIn, actualOut, actualCostMicros int64) {
	if actualIn < 0 {
		actualIn = 0
	}
	if actualOut < 0 {
		actualOut = 0
	}
	if actualCostMicros < 0 {
		actualCostMicros = 0
	}
	c.Response().Header().Set("X-Relay-Actual-Input-Tokens", strconv.FormatInt(actualIn, 10))
	c.Response().Header().Set("X-Relay-Actual-Output-Tokens", strconv.FormatInt(actualOut, 10))
	c.Response().Header().Set("X-Relay-Actual-Total-Tokens", strconv.FormatInt(actualIn+actualOut, 10))
	c.Response().Header().Set("X-Relay-Actual-Cost-Micros", strconv.FormatInt(actualCostMicros, 10))
}

func (s *Server) actualCostMicros(ctx context.Context, permit scheduler.Permit, actualIn, actualOut int64) (int64, bool) {
	if s == nil || s.store == nil {
		return 0, false
	}
	policy, err := s.store.GetPricingByEndpoint(ctx, permit.Endpoint.ID)
	if err != nil {
		return 0, false
	}
	return pricing.ReconcileCost(policy, actualIn, actualOut), true
}

func (s *Server) completeSchedulerRequest(ctx context.Context, permit scheduler.Permit, statusCode int, actualIn, actualOut, actualCost int64, actualCostResolved bool, finishedAt time.Time) error {
	if actualCostResolved {
		return s.scheduler.CompleteAtWithCost(ctx, permit, statusCode, actualIn, actualOut, actualCost, finishedAt)
	}
	return s.scheduler.CompleteAt(ctx, permit, statusCode, actualIn, actualOut, finishedAt)
}

func safeForwardRequestHeaders(in http.Header) http.Header {
	out := make(http.Header)
	for key, values := range in {
		canonical := http.CanonicalHeaderKey(key)
		if !allowedForwardRequestHeader(canonical) {
			continue
		}
		for _, value := range values {
			out.Add(canonical, value)
		}
	}
	out.Del("Accept-Encoding")
	return out
}

func allowedForwardRequestHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Accept",
		"Accept-Language",
		"Content-Type",
		"Openai-Beta",
		"Openai-Organization",
		"Openai-Project",
		"Anthropic-Version",
		"Anthropic-Beta":
		return true
	default:
		return false
	}
}

func safeForwardResponseHeaders(in http.Header) http.Header {
	out := make(http.Header)
	for key, values := range in {
		canonical := http.CanonicalHeaderKey(key)
		if unsafeProxyHeader(canonical) || sensitiveHeader(canonical) {
			continue
		}
		for _, value := range values {
			out.Add(canonical, value)
		}
	}
	return out
}

func unsafeProxyHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade":
		return true
	default:
		return false
	}
}

func sensitiveHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key":
		return true
	default:
		return false
	}
}

func sameOriginRedirectOnly(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if req.URL.Scheme == first.Scheme && strings.EqualFold(req.URL.Host, first.Host) {
		return nil
	}
	return http.ErrUseLastResponse
}

func (s *Server) models(c *echo.Context) error {
	endpoints, err := s.store.ListEndpoints(c.Request().Context())
	if err != nil {
		return err
	}
	data := make([]map[string]any, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if !endpoint.Enabled {
			continue
		}
		data = append(data, map[string]any{
			"id":       endpoint.UpstreamModel,
			"object":   "model",
			"owned_by": "anchorshell-relay",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"object": "list", "data": data})
}

func usageFromResponse(body []byte, fallbackIn, fallbackOut int64) (int64, int64) {
	in, out, ok := actualUsageFromResponse(body)
	if !ok {
		return fallbackIn, fallbackOut
	}
	if in == 0 {
		in = fallbackIn
	}
	if out == 0 {
		out = fallbackOut
	}
	return in, out
}

func usageFromResponseForStatus(statusCode int, body []byte, fallbackIn, fallbackOut int64) (int64, int64) {
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		return usageFromResponse(body, fallbackIn, fallbackOut)
	}
	in, out, ok := actualUsageFromResponse(body)
	if !ok {
		return 0, 0
	}
	return in, out
}

func actualUsageFromResponse(body []byte) (int64, int64, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, 0, false
	}
	rawUsage, ok := payload["usage"].(map[string]any)
	if !ok {
		return 0, 0, false
	}
	in := parseInt64Any(rawUsage["prompt_tokens"])
	if in == 0 {
		in = parseInt64Any(rawUsage["input_tokens"])
	}
	out := parseInt64Any(rawUsage["completion_tokens"])
	if out == 0 {
		out = parseInt64Any(rawUsage["output_tokens"])
	}
	total := parseInt64Any(rawUsage["total_tokens"])
	if total == 0 {
		total = parseInt64Any(rawUsage["total"])
	}
	if total > 0 && in+out == 0 {
		out = total
	}
	return in, out, true
}

func (s *Server) requestContext(ctx context.Context, c *echo.Context, requestID string, routeKind models.RouteKind) (TrustedRequestContext, error) {
	if len(s.requestContextHooks) == 0 {
		return TrustedRequestContext{}, nil
	}
	input := RequestContextInput{
		RequestID: requestID,
		RouteKind: routeKind,
		Headers:   c.Request().Header.Clone(),
	}
	if s.requestContextBypass != nil && s.requestContextBypass(c) {
		return TrustedRequestContext{}, nil
	}
	var trusted TrustedRequestContext
	for _, hook := range s.requestContextHooks {
		if hook == nil {
			continue
		}
		next, err := hook(ctx, input)
		if err != nil {
			return TrustedRequestContext{}, err
		}
		if next.LogicalRequestID != "" || next.PartitionKey != "" || len(next.Metadata) != 0 || !trustedRequestLimitsEmpty(next.Limits) {
			trusted = next
		}
	}
	return trusted, nil
}

func trustedRequestLimitsEmpty(limits TrustedRequestLimits) bool {
	return limits.DailySpendLimitCents == nil &&
		limits.RemainingDailySpendCents == nil &&
		limits.DailyRequestLimit == nil &&
		limits.RemainingDailyRequests == nil &&
		limits.DailyTokenLimit == nil &&
		limits.RemainingDailyTokens == nil
}

func (s *Server) publishRequestInFlight(ctx context.Context, meta router.RequestMeta, requestID string, permit scheduler.Permit, startedAt time.Time, trustedContext TrustedRequestContext) {
	if len(s.inFlightHooks) == 0 {
		return
	}
	event := RequestInFlightEvent{
		RequestID:       requestID,
		RouteKind:       meta.RouteKind,
		IncomingModel:   meta.IncomingModel,
		Lane:            meta.Lane,
		EndpointID:      permit.Endpoint.UUID,
		EndpointName:    permit.Endpoint.Name,
		ProviderID:      permit.Endpoint.ProviderUUID,
		UpstreamModel:   permit.Endpoint.UpstreamModel,
		ReasoningEffort: meta.ReasoningEffort,
		Streaming:       meta.Streaming,
		Waited:          permit.Waited,
		SelectedAt:      startedAt.UTC(),
		TrustedContext:  trustedContext,
	}
	for _, hook := range s.inFlightHooks {
		if hook != nil {
			hook(ctx, event)
		}
	}
}

func (s *Server) publishRequestCompleted(ctx context.Context, meta router.RequestMeta, requestID string, permit scheduler.Permit, trustedContext TrustedRequestContext, characterizationResult characterization.Characterization, statusCode int, state string, startedAt time.Time, finishedAt time.Time, latencyMS int64, actualIn int64, actualOut int64, actualCostMicros int64) {
	if len(s.completedHooks) == 0 {
		return
	}
	event := RequestCompletedEvent{
		RequestID:             requestID,
		RouteKind:             meta.RouteKind,
		IncomingModel:         meta.IncomingModel,
		Lane:                  meta.Lane,
		EndpointID:            permit.Endpoint.UUID,
		EndpointName:          permit.Endpoint.Name,
		ProviderID:            permit.Endpoint.ProviderUUID,
		UpstreamModel:         permit.Endpoint.UpstreamModel,
		ReasoningEffort:       meta.ReasoningEffort,
		Streaming:             meta.Streaming,
		StatusCode:            statusCode,
		State:                 state,
		WaitMS:                permit.Waited.Milliseconds(),
		LatencyMS:             latencyMS,
		EstimatedInputTokens:  permit.EstimatedInput,
		EstimatedOutputTokens: permit.EstimatedOutput,
		ActualInputTokens:     actualIn,
		ActualOutputTokens:    actualOut,
		ActualTotalTokens:     actualIn + actualOut,
		EstimatedCostMicros:   permit.EstimatedCost,
		ActualCostMicros:      actualCostMicros,
		QueuedAt:              startedAt.Add(-permit.Waited).UTC(),
		StartedAt:             startedAt.UTC(),
		FinishedAt:            finishedAt.UTC(),
		TrustedContext:        trustedContext,
		Characterization:      characterizationResult,
	}
	for _, hook := range s.completedHooks {
		if hook != nil {
			hook(ctx, event)
		}
	}
}

func (s *Server) characterizationAllowed(ctx context.Context, requestID string, trusted TrustedRequestContext) bool {
	if s == nil || s.characterizer == nil || !s.characterizer.Enabled() {
		return false
	}
	for _, policy := range s.characterizationPolicies {
		if policy != nil && !policy(ctx, requestID, trusted) {
			return false
		}
	}
	return true
}

func (s *Server) routingCharacterizationRequired(ctx context.Context, input RoutingCharacterizationPolicyInput) bool {
	if s == nil || s.characterizer == nil || !s.characterizer.Enabled() {
		return false
	}
	for _, policy := range s.routingCharacterizationPolicies {
		if policy != nil && policy(ctx, input) {
			return true
		}
	}
	return false
}

func (s *Server) submitCharacterization(ctx context.Context, requestID string, routeKind models.RouteKind, bodyMap map[string]any, meta router.RequestMeta, requestBodyBytes []byte, userAgent string, trusted TrustedRequestContext, background bool) *characterization.Handle {
	if s == nil || s.characterizer == nil || !s.characterizer.Enabled() {
		return characterization.DisabledHandle()
	}
	estimatedInputTokens := meta.EstimatedInputTokens
	if estimatedInputTokens <= 0 {
		estimatedInputTokens = max(1, int64(len(requestBodyBytes))/4)
	}
	normalized := characterization.Normalize(string(routeKind), bodyMap, estimatedInputTokens, meta.Streaming)
	prepared := characterization.Prepare(normalized, string(routeKind), characterization.HarnessHints{
		UserAgent:      boundedHeaderValue(userAgent, 512),
		ClientMetadata: characterizationClientMetadata(trusted.Metadata),
	}, characterization.DefaultThresholds())
	id := characterization.EngineAnchorShell
	var err error
	if s.characterizationEngine != nil {
		id, err = s.characterizationEngine(ctx, requestID, trusted)
	} else if s.store != nil {
		id, err = s.store.DeploymentCharacterizationEngine(ctx)
	}
	if err != nil {
		return s.characterizer.SubmitSelectionFallback(requestID, prepared)
	}
	// Ordinary classification has its own deadline, independent of the HTTP
	// response. Keep Smart Group cancellation and pre-routing wait unchanged.
	if background {
		ctx = context.Background()
	}
	// No automatic full-schema jobs on the shared single-threaded worker: they
	// would hold subsequent Smart Group routing behind rich enrichment.
	return s.characterizer.SubmitRoutingEngine(ctx, id, requestID, prepared)
}

func (s *Server) WithCharacterizationEngine(resolver func(context.Context, string, TrustedRequestContext) (characterization.EngineID, error)) *Server {
	s.characterizationEngine = resolver
	return s
}

func characterizationClientMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"client": true, "client_name": true, "client_type": true,
		"harness": true, "source": true, "sdk": true,
	}
	out := make(map[string]string, len(allowed))
	for key, value := range metadata {
		key = strings.ToLower(strings.TrimSpace(key))
		if allowed[key] {
			out[key] = boundedHeaderValue(value, 256)
		}
	}
	return out
}

func boundedHeaderValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func (s *Server) publishRequestProgress(meta router.RequestMeta, requestID string, permit scheduler.Permit, substatus string, startedAt time.Time, uploadedTokens, downloadedTokens int64) {
	if s.scheduler != nil {
		s.scheduler.UpdateProgress(permit.TaskID, startedAt, substatus, uploadedTokens, downloadedTokens)
	}
	if s.telemetry == nil {
		return
	}
	queuedAt := startedAt.Add(-permit.Waited).UTC()
	s.telemetry.Publish(telemetry.Event{
		Type: "request_progress",
		Payload: map[string]any{
			"request_id":              requestID,
			"task_id":                 permit.TaskID,
			"endpoint_id":             permit.Endpoint.UUID,
			"endpoint_name":           permit.Endpoint.Name,
			"provider_id":             permit.Endpoint.ProviderUUID,
			"organization_uuid":       permit.OrganizationUUID,
			"actor_id":                permit.ActorID,
			"lane":                    meta.Lane,
			"incoming_model":          meta.IncomingModel,
			"selected_upstream_model": permit.Endpoint.UpstreamModel,
			"state":                   "in_flight",
			"substatus":               substatus,
			"queued_at":               queuedAt,
			"wait_ms":                 permit.Waited.Milliseconds(),
			"started_at":              startedAt.UTC(),
			"uploaded_tokens":         uploadedTokens,
			"downloaded_tokens":       downloadedTokens,
		},
	})
}

func (s *Server) readResponseBody(body io.Reader, meta router.RequestMeta, requestID string, permit scheduler.Permit, startedAt time.Time, substatus string) ([]byte, int64, error) {
	var buffer bytes.Buffer
	chunk := make([]byte, 16*1024)
	var totalBytes int64
	publishedAt := time.Now().UTC()

	for {
		n, err := body.Read(chunk)
		if n > 0 {
			totalBytes += int64(n)
			if totalBytes > maxBufferedResponseBytes {
				return nil, estimateTokensFromBytes(totalBytes), errors.New("upstream response too large")
			}
			if _, writeErr := buffer.Write(chunk[:n]); writeErr != nil {
				return nil, estimateTokensFromBytes(totalBytes), writeErr
			}
			now := time.Now().UTC()
			if publishedAt.IsZero() || now.Sub(publishedAt) >= progressPublishInterval {
				s.publishRequestProgress(meta, requestID, permit, substatus, startedAt, permit.EstimatedInput, estimateTokensFromBytes(totalBytes))
				publishedAt = now
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, estimateTokensFromBytes(totalBytes), err
		}
	}

	return buffer.Bytes(), estimateTokensFromPayload(buffer.Bytes(), totalBytes), nil
}

func (s *Server) copyStreamingResponse(c *echo.Context, body io.Reader, responseHeaders http.Header, statusCode int, meta router.RequestMeta, requestID string, permit scheduler.Permit, startedAt time.Time, routeKind models.RouteKind, captureBody bool) (int64, bool, []byte, error) {
	chunk := make([]byte, 16*1024)
	var totalBytes int64
	publishedAt := time.Now().UTC()
	lastPublishedTokens := int64(0)
	var preview bytes.Buffer
	var captured bytes.Buffer
	committed := false
	flusher, _ := c.Response().(http.Flusher)

	for {
		n, err := body.Read(chunk)
		if n > 0 {
			totalBytes += int64(n)
			if captureBody {
				appendStoredPayload(&captured, chunk[:n])
			}
			if !committed {
				if _, writeErr := preview.Write(chunk[:n]); writeErr != nil {
					return estimateTokensFromBytes(totalBytes), false, captured.Bytes(), writeErr
				}
				previewBytes := preview.Bytes()
				if retryAfter, reason, throttled := detectThrottleStatusAndDelay(statusCode, responseHeaders, previewBytes, permit.AppliedLimitState); throttled {
					return estimateTokensFromBytes(totalBytes), false, captured.Bytes(), upstreamThrottleError{reason: reason, retryAfter: retryAfter, statusCode: statusCode}
				}
				if looksLikeSSE(previewBytes) {
					applyProxyResponseHeaders(c, responseHeaders, permit)
					c.Response().WriteHeader(statusCode)
					if _, writeErr := c.Response().Write(previewBytes); writeErr != nil {
						return estimateTokensFromBytes(totalBytes), false, captured.Bytes(), writeErr
					}
					if flusher != nil {
						flusher.Flush()
					}
					committed = true
					preview.Reset()
				} else if preview.Len() >= 8*1024 || errors.Is(err, io.EOF) {
					applyProxyResponseHeaders(c, responseHeaders, permit)
					c.Response().WriteHeader(statusCode)
					if _, writeErr := c.Response().Write(previewBytes); writeErr != nil {
						return estimateTokensFromBytes(totalBytes), false, captured.Bytes(), writeErr
					}
					if flusher != nil {
						flusher.Flush()
					}
					committed = true
					preview.Reset()
				}
			} else {
				if _, writeErr := c.Response().Write(chunk[:n]); writeErr != nil {
					return estimateTokensFromBytes(totalBytes), true, captured.Bytes(), writeErr
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			now := time.Now().UTC()
			if publishedAt.IsZero() || now.Sub(publishedAt) >= progressPublishInterval {
				downloadedTokens := estimateTokensFromBytes(totalBytes)
				s.publishRequestProgress(meta, requestID, permit, streamingSubstatus(routeKind), startedAt, permit.EstimatedInput, downloadedTokens)
				lastPublishedTokens = downloadedTokens
				publishedAt = now
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return estimateTokensFromBytes(totalBytes), committed, captured.Bytes(), err
		}
	}

	if !committed && preview.Len() > 0 {
		previewBytes := preview.Bytes()
		if retryAfter, reason, throttled := detectThrottleStatusAndDelay(statusCode, responseHeaders, previewBytes, permit.AppliedLimitState); throttled {
			return estimateTokensFromBytes(totalBytes), false, captured.Bytes(), upstreamThrottleError{reason: reason, retryAfter: retryAfter, statusCode: statusCode}
		}
		applyProxyResponseHeaders(c, responseHeaders, permit)
		c.Response().WriteHeader(statusCode)
		if _, err := c.Response().Write(previewBytes); err != nil {
			return estimateTokensFromBytes(totalBytes), false, captured.Bytes(), err
		}
		if flusher != nil {
			flusher.Flush()
		}
		committed = true
	}

	downloadedTokens := estimateTokensFromPayload(captured.Bytes(), totalBytes)
	if downloadedTokens != lastPublishedTokens {
		s.publishRequestProgress(meta, requestID, permit, streamingSubstatus(routeKind), startedAt, permit.EstimatedInput, downloadedTokens)
	}
	return downloadedTokens, committed, captured.Bytes(), nil
}

func appendStoredPayload(buffer *bytes.Buffer, chunk []byte) {
	if buffer.Len() >= maxStoredPayloadBytes {
		return
	}
	remaining := maxStoredPayloadBytes - buffer.Len()
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	_, _ = buffer.Write(chunk)
}

func (s *Server) storeCapturedRequestBodies(ctx context.Context, enabled bool, requestID string, requestBody, upstreamRequestBody, responseBody []byte) {
	if !enabled || s.store == nil {
		return
	}
	// Capture is authorized before inference. Persist the completed payload even
	// if the client disconnected after receiving its response; keep tenant values.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	requestText := formatStoredPayload(requestBody)
	upstreamText := formatStoredPayload(upstreamRequestBody)
	responseText := formatStoredPayload(responseBody)
	payloadStored := requestText != "" || upstreamText != "" || responseText != ""
	if err := s.store.UpdateRequestLogCapturedBodies(ctx, requestID, requestText, upstreamText, responseText); err != nil {
		slog.Warn("request payload capture persistence failed", "request_id", requestID)
		return
	}
	if !payloadStored || s.telemetry == nil {
		return
	}
	payload := map[string]any{
		"request_id":            requestID,
		"request_bodies_stored": true,
	}
	if scope, ok := tenancy.ScopeFromContext(ctx); ok {
		payload["organization_uuid"] = scope.OrganizationUUID
		payload["actor_id"] = scope.UserUUID
	}
	s.telemetry.Publish(telemetry.Event{Type: "request_log", Payload: payload})
}

func waitBudgetRejectionPayload(rejection scheduler.Rejection) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"type":        "rate_limit_wait_budget_exceeded",
			"message":     rejection.Reason,
			"retry_after": rejection.RetryAfter.Seconds(),
			"nearest_at":  rejection.NearestAt,
		},
	}
}

func formatStoredPayload(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	truncated := len(body) > maxStoredPayloadBytes
	if truncated {
		body = body[:maxStoredPayloadBytes]
	}
	if redacted, ok := security.RedactJSONBytes(body); ok {
		body = redacted
	}
	text := security.RedactString(string(body))
	if truncated {
		text += "\n\n[truncated at 1MiB]"
	}
	return text
}

func looksLikeSSE(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, ":")
}

func detectThrottleStatusAndDelay(statusCode int, headers http.Header, body []byte, applied []limits.EffectiveLimit) (time.Duration, string, bool) {
	reason, throttled := throttleReason(statusCode, inspectableBody(headers, body))
	if !throttled {
		return 0, "", false
	}
	wait := retryAfterDelay(headers.Get("Retry-After"), time.Now().UTC())
	if wait <= 0 {
		wait = derivedPacingDelay(applied)
	}
	if wait <= 0 {
		wait = 5 * time.Second
	}
	return wait, reason, true
}

func waitingSubstatus(routeKind models.RouteKind) string {
	switch routeKind {
	case models.RouteKindEmbeddings:
		return "processing embeddings"
	default:
		return "thinking"
	}
}

func responseDownloadSubstatus(routeKind models.RouteKind) string {
	switch routeKind {
	case models.RouteKindEmbeddings:
		return "receiving embeddings"
	default:
		return "downloading response"
	}
}

func streamingSubstatus(routeKind models.RouteKind) string {
	switch routeKind {
	case models.RouteKindEmbeddings:
		return "streaming embeddings"
	default:
		return "streaming response"
	}
}

func estimateTokensFromBytes(totalBytes int64) int64 {
	return tokenestimate.CountByteLength(totalBytes)
}

func estimateTokensFromPayload(body []byte, fallbackBytes int64) int64 {
	if len(body) > 0 && int64(len(body)) == fallbackBytes {
		if tokens := tokenestimate.CountBytes(body); tokens > 0 {
			return tokens
		}
	}
	return estimateTokensFromBytes(fallbackBytes)
}

func detectUpstreamThrottle(resp *http.Response, body []byte, applied []limits.EffectiveLimit) (time.Duration, string, bool) {
	reason, throttled := throttleReason(resp.StatusCode, inspectableBody(resp.Header, body))
	if !throttled {
		return 0, "", false
	}
	wait := retryAfterDelay(resp.Header.Get("Retry-After"), time.Now().UTC())
	if wait <= 0 {
		wait = derivedPacingDelay(applied)
	}
	if wait <= 0 {
		wait = 5 * time.Second
	}
	return wait, reason, true
}

func throttleReason(statusCode int, body []byte) (string, bool) {
	if statusCode == http.StatusTooManyRequests {
		return "upstream rate limited", true
	}

	normalizedBody := strings.ToLower(string(body))
	if strings.Contains(normalizedBody, "discord.gg/airforce") &&
		(strings.Contains(normalizedBody, "ratelimit exceeded") ||
			strings.Contains(normalizedBody, "rate limit exceeded") ||
			strings.Contains(normalizedBody, "too many requests")) {
		return "upstream rate limited", true
	}
	if strings.Contains(normalizedBody, "discord.gg/airforce") &&
		strings.Contains(normalizedBody, "concurrency limit exceeded") {
		return "upstream concurrency limited", true
	}

	message := extractUpstreamErrorMessage(body)
	if message == "" {
		return "", false
	}
	normalized := strings.ToLower(message)
	switch {
	case strings.Contains(normalized, "concurrency limit exceeded"):
		return "upstream concurrency limited", true
	case strings.Contains(normalized, "rate limit"),
		strings.Contains(normalized, "rate_limit"),
		strings.Contains(normalized, "too many requests"),
		strings.Contains(normalized, "try again later"),
		strings.Contains(normalized, "limit exceeded"):
		return "upstream rate limited", true
	default:
		return "", false
	}
}

func inspectableBody(headers http.Header, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	encoding := strings.ToLower(strings.TrimSpace(headers.Get("Content-Encoding")))
	switch {
	case strings.Contains(encoding, "gzip"):
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		defer reader.Close()
		decoded, err := io.ReadAll(io.LimitReader(reader, maxStoredPayloadBytes+1))
		if err != nil {
			return body
		}
		if len(decoded) > maxStoredPayloadBytes {
			return decoded[:maxStoredPayloadBytes]
		}
		return decoded
	case strings.Contains(encoding, "deflate"):
		reader, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		defer reader.Close()
		decoded, err := io.ReadAll(io.LimitReader(reader, maxStoredPayloadBytes+1))
		if err != nil {
			return body
		}
		if len(decoded) > maxStoredPayloadBytes {
			return decoded[:maxStoredPayloadBytes]
		}
		return decoded
	default:
		return body
	}
}

func extractUpstreamErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if rawError, ok := payload["error"]; ok {
		switch value := rawError.(type) {
		case string:
			return value
		case map[string]any:
			if message, _ := value["message"].(string); message != "" {
				return message
			}
		}
	}
	if message, ok := payload["message"].(map[string]any); ok {
		if content := extractMessageContent(message); content != "" {
			return content
		}
	}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			message, ok := choice["message"].(map[string]any)
			if !ok {
				continue
			}
			if content := extractMessageContent(message); content != "" {
				return content
			}
		}
	}
	return ""
}

func extractMessageContent(message map[string]any) string {
	if content, _ := message["content"].(string); content != "" {
		return strings.TrimSpace(content)
	}
	if parts, ok := message["content"].([]any); ok {
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := part["text"].(string); text != "" {
				return strings.TrimSpace(text)
			}
			if text, _ := part["content"].(string); text != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func retryAfterDelay(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		delay := when.UTC().Sub(now.UTC())
		if delay > 0 {
			return delay
		}
	}
	return 0
}

func derivedPacingDelay(applied []limits.EffectiveLimit) time.Duration {
	var wait time.Duration
	for _, item := range applied {
		if item.Metric != models.MetricRequests || item.Effective == nil || *item.Effective <= 0 {
			continue
		}
		var duration time.Duration
		switch item.Period {
		case models.PeriodSecond:
			duration = time.Second
		case models.PeriodMinute:
			duration = time.Minute
		case models.PeriodHour:
			duration = time.Hour
		default:
			continue
		}
		spacing := time.Duration(int64(duration) / *item.Effective)
		if spacing > wait {
			wait = spacing
		}
	}
	return wait
}

func parseInt64Any(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}
