package admin

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/ws"
)

const (
	defaultLiveFlowPreviewServiceMS = 2_000
	maxLiveFlowPreviewServiceMS     = 30_000
)

type liveFlowPreviewEngineSession struct {
	id             string
	hub            *telemetry.Hub
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	scheduler      *scheduler.Scheduler
	enqueueCancel  context.CancelFunc
	enqueueRunning bool
	sequence       int64
	generation     int64
	createdAt      time.Time
	updatedAt      time.Time
}

type liveFlowPreviewSessionResponse struct {
	PreviewSessionID  string `json:"preview_session_id"`
	PreviewGeneration int64  `json:"preview_generation"`
}

type liveFlowPreviewEnqueuePayload struct {
	Lane                  string           `json:"lane"`
	RouteKind             models.RouteKind `json:"route_kind"`
	RequestCount          int              `json:"request_count"`
	ArrivalIntervalMS     int64            `json:"arrival_interval_ms"`
	MaxWaitMS             int64            `json:"max_wait_ms"`
	AllowFallback         bool             `json:"allow_fallback"`
	Priority              int              `json:"priority"`
	EstimatedInputTokens  int64            `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64            `json:"estimated_output_tokens"`
	MaxCostMicros         int64            `json:"max_cost_micros"`
	SyntheticServiceMS    int64            `json:"synthetic_service_ms"`
	Scenario              string           `json:"scenario"`
}

func (a *API) createLiveFlowPreviewSession(c *echo.Context) error {
	session, err := a.newLiveFlowPreviewEngineSession(c.Request().Context())
	if err != nil {
		return err
	}
	a.previewEngineMu.Lock()
	if a.previewEngineSessions == nil {
		a.previewEngineSessions = make(map[string]*liveFlowPreviewEngineSession)
	}
	a.pruneLiveFlowPreviewEngineSessionsLocked(time.Now().UTC())
	a.previewEngineSessions[session.id] = session
	a.previewEngineMu.Unlock()
	return c.JSON(http.StatusOK, session.response())
}

func (a *API) liveFlowPreviewSessionWS(c *echo.Context) error {
	id := c.Param("id")
	session := a.liveFlowPreviewEngineSession(id)
	if session == nil {
		return echo.NewHTTPError(http.StatusNotFound, "preview session not found")
	}
	session.touch()
	return ws.HandlerWithSnapshots(session.hub, a.liveFlowPreviewCapacitySnapshotProducer(session), func() {
		a.closeLiveFlowPreviewEngineSession(id)
	})(c)
}

func (a *API) enqueueLiveFlowPreviewSession(c *echo.Context) error {
	session := a.liveFlowPreviewEngineSession(c.Param("id"))
	if session == nil {
		return echo.NewHTTPError(http.StatusNotFound, "preview session not found")
	}
	var payload liveFlowPreviewEnqueuePayload
	if err := c.Bind(&payload); err != nil {
		return err
	}
	normalizeLiveFlowPreviewEnqueuePayload(&payload)
	if payload.Lane == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "lane is required")
	}
	trusted, err := a.requestContext(c.Request().Context(), c, payload.RouteKind)
	if err != nil {
		return err
	}
	ctx := tenancy.ContextWithMetadataScope(c.Request().Context(), trusted.Metadata)
	if ctx != c.Request().Context() {
		c.SetRequest(c.Request().WithContext(ctx))
	}
	if err := session.enqueue(c.Request().Context(), a, payload, trusted.Metadata); err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, map[string]any{"ok": true, "preview_session_id": session.id, "preview_generation": session.currentGeneration()})
}

func (a *API) stopLiveFlowPreviewSession(c *echo.Context) error {
	session := a.liveFlowPreviewEngineSession(c.Param("id"))
	if session == nil {
		return echo.NewHTTPError(http.StatusNotFound, "preview session not found")
	}
	session.stopEnqueue()
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "preview_session_id": session.id, "preview_generation": session.currentGeneration()})
}

func (a *API) resetLiveFlowPreviewSession(c *echo.Context) error {
	session := a.liveFlowPreviewEngineSession(c.Param("id"))
	if session == nil {
		return echo.NewHTTPError(http.StatusNotFound, "preview session not found")
	}
	a.forgetLiveFlowPreviewPlannerSession(session.id)
	if err := session.reset(a); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, session.response())
}

func (a *API) liveFlowPreviewCapacitySnapshotProducer(session *liveFlowPreviewEngineSession) func(c *echo.Context) ([]telemetry.Event, error) {
	return func(c *echo.Context) ([]telemetry.Event, error) {
		sch, generation := session.currentSchedulerAndGeneration()
		queueItems := []scheduler.TaskSnapshot{}
		if sch != nil {
			queueItems = sch.Items()
		}
		snapshot, err := a.buildCapacitySnapshotWithOptions(c.Request().Context(), capacityRequestHeaders(c), sch, queueItems, capacitySnapshotOptions{Preview: true})
		if err != nil {
			return nil, err
		}
		payload := realtimeSnapshotPayload(snapshot)
		payload["preview_session_id"] = session.id
		payload["preview_generation"] = generation
		return []telemetry.Event{{
			Type:      "realtime_snapshot",
			Timestamp: snapshot.GeneratedAt,
			Payload:   payload,
		}}, nil
	}
}

func (a *API) deleteLiveFlowPreviewSession(c *echo.Context) error {
	a.closeLiveFlowPreviewEngineSession(c.Param("id"))
	return c.NoContent(http.StatusNoContent)
}

func (a *API) closeLiveFlowPreviewEngineSession(id string) {
	a.forgetLiveFlowPreviewPlannerSession(id)
	a.previewEngineMu.Lock()
	session := a.previewEngineSessions[id]
	delete(a.previewEngineSessions, id)
	a.previewEngineMu.Unlock()
	if session != nil {
		session.close()
	}
}

func (a *API) forgetLiveFlowPreviewPlannerSession(id string) {
	if id == "" {
		return
	}
	a.liveFlowPreviewMu.Lock()
	delete(a.liveFlowPreviewSessions, id)
	a.liveFlowPreviewMu.Unlock()
}

func (a *API) liveFlowPreviewEngineSession(id string) *liveFlowPreviewEngineSession {
	a.previewEngineMu.Lock()
	defer a.previewEngineMu.Unlock()
	session := a.previewEngineSessions[id]
	if session != nil {
		session.touch()
	}
	return session
}

func (a *API) newLiveFlowPreviewEngineSession(_ context.Context) (*liveFlowPreviewEngineSession, error) {
	session := &liveFlowPreviewEngineSession{
		id:        uuid.NewString(),
		hub:       telemetry.NewHub(),
		createdAt: time.Now().UTC(),
		updatedAt: time.Now().UTC(),
	}
	if err := session.reset(a); err != nil {
		return nil, err
	}
	return session, nil
}

func (a *API) newLiveFlowPreviewScheduler(hub *telemetry.Hub) (*scheduler.Scheduler, context.Context, context.CancelFunc, error) {
	tracker := limits.NewTracker(a.store)
	resolver := limits.NewResolver(a.store, tracker)
	resolver.LoadSettings(context.Background())
	resolver.SetObservedLimitsEnabled(false)
	resolver.SetPersistedEndpointStateEnabled(false)
	sch := scheduler.New(a.store, resolver, tracker, hub, scheduler.HeuristicEstimator{}, 60_000, 10_000, 0).
		WithLimitScopeHooks(a.limitHooks...).
		WithExternalLimitHooks(a.externalLimitHooks...)
	sch.SetPersistence(false)
	ctx, cancel := context.WithCancel(context.Background())
	sch.Start()
	return sch, ctx, cancel, nil
}

func (a *API) pruneLiveFlowPreviewEngineSessionsLocked(now time.Time) {
	for id, session := range a.previewEngineSessions {
		if now.Sub(session.lastUpdated()) > liveFlowPreviewSessionTTL {
			delete(a.previewEngineSessions, id)
			session.close()
		}
	}
	if len(a.previewEngineSessions) <= maxLiveFlowPreviewSessions {
		return
	}
	type sessionAge struct {
		id        string
		updatedAt time.Time
	}
	items := make([]sessionAge, 0, len(a.previewEngineSessions))
	for id, session := range a.previewEngineSessions {
		items = append(items, sessionAge{id: id, updatedAt: session.lastUpdated()})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].updatedAt.Before(items[j].updatedAt)
	})
	for len(a.previewEngineSessions) > maxLiveFlowPreviewSessions && len(items) > 0 {
		session := a.previewEngineSessions[items[0].id]
		delete(a.previewEngineSessions, items[0].id)
		if session != nil {
			session.close()
		}
		items = items[1:]
	}
}

func normalizeLiveFlowPreviewEnqueuePayload(payload *liveFlowPreviewEnqueuePayload) {
	payload.Lane = strings.TrimSpace(payload.Lane)
	payload.Scenario = strings.TrimSpace(payload.Scenario)
	if payload.RouteKind == "" {
		payload.RouteKind = models.RouteKindChat
	}
	if payload.RequestCount <= 0 {
		payload.RequestCount = defaultLiveFlowSimulationRequestCount
	}
	if payload.RequestCount > maxLiveFlowSimulationRequestCount {
		payload.RequestCount = maxLiveFlowSimulationRequestCount
	}
	if payload.ArrivalIntervalMS <= 0 {
		payload.ArrivalIntervalMS = liveFlowSimulationArrivalInterval
	}
	if payload.ArrivalIntervalMS > 10_000 {
		payload.ArrivalIntervalMS = 10_000
	}
	if payload.Priority == 0 {
		payload.Priority = 50
	}
	if payload.EstimatedInputTokens <= 0 {
		payload.EstimatedInputTokens = 1500
	}
	if payload.EstimatedOutputTokens <= 0 {
		payload.EstimatedOutputTokens = 600
	}
	if payload.SyntheticServiceMS <= 0 {
		payload.SyntheticServiceMS = defaultLiveFlowPreviewServiceMS
	}
	if payload.SyntheticServiceMS > maxLiveFlowPreviewServiceMS {
		payload.SyntheticServiceMS = maxLiveFlowPreviewServiceMS
	}
}

func (s *liveFlowPreviewEngineSession) reset(a *API) error {
	s.mu.Lock()
	if s.enqueueCancel != nil {
		s.enqueueCancel()
		s.enqueueCancel = nil
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.scheduler != nil {
		s.scheduler.Stop()
		s.scheduler = nil
	}
	s.sequence = 0
	s.generation++
	generation := s.generation
	s.enqueueRunning = false
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()

	scheduler, ctx, cancel, err := a.newLiveFlowPreviewScheduler(s.hub)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.scheduler = scheduler
	s.ctx = ctx
	s.cancel = cancel
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()

	s.hub.Publish(telemetry.Event{
		Type: "preview_reset",
		Payload: map[string]any{
			"preview_session_id": s.id,
			"preview_generation": generation,
		},
	})
	return nil
}

func (s *liveFlowPreviewEngineSession) enqueue(ctx context.Context, api *API, payload liveFlowPreviewEnqueuePayload, trustedMetadata map[string]string) error {
	s.mu.Lock()
	if s.scheduler == nil || s.ctx == nil {
		s.mu.Unlock()
		return errors.New("preview session is not initialized")
	}
	if s.enqueueCancel != nil {
		s.enqueueCancel()
	}
	sessionCtx := tenancy.ContextWithMetadataScope(s.ctx, trustedMetadata)
	if scoped, ok := tenancy.ScopeFromContext(ctx); ok {
		sessionCtx = tenancy.ContextWithScope(sessionCtx, scoped)
	}
	enqueueCtx, enqueueCancel := context.WithCancel(sessionCtx)
	generation := s.generation
	s.enqueueCancel = enqueueCancel
	s.enqueueRunning = true
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()

	s.hub.Publish(telemetry.Event{
		Type: "preview_enqueue_started",
		Payload: map[string]any{
			"preview_session_id": s.id,
			"preview_generation": generation,
			"request_count":      payload.RequestCount,
		},
	})

	go s.runEnqueue(enqueueCtx, sessionCtx, api, payload, trustedMetadata, generation)
	return nil
}

func (s *liveFlowPreviewEngineSession) runEnqueue(enqueueCtx context.Context, sessionCtx context.Context, api *API, payload liveFlowPreviewEnqueuePayload, trustedMetadata map[string]string, generation int64) {
	completed := false
	defer func() {
		s.mu.Lock()
		current := s.generation == generation
		if current && s.enqueueCancel != nil {
			s.enqueueCancel = nil
		}
		if current {
			s.enqueueRunning = false
			s.updatedAt = time.Now().UTC()
		}
		s.mu.Unlock()
		if !current {
			return
		}
		if !completed {
			return
		}
		s.hub.Publish(telemetry.Event{
			Type: "preview_enqueue_finished",
			Payload: map[string]any{
				"preview_session_id": s.id,
				"preview_generation": generation,
			},
		})
	}()

	interval := time.Duration(payload.ArrivalIntervalMS) * time.Millisecond
	for i := 0; i < payload.RequestCount; i++ {
		if i > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-enqueueCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		select {
		case <-enqueueCtx.Done():
			return
		default:
		}
		sequence := s.nextSequence()
		queuedAt := time.Now().UTC()
		go s.runSyntheticRequest(sessionCtx, api, payload, sequence, generation, queuedAt, cloneStringMap(trustedMetadata))
	}
	completed = true
}

func cloneLiveFlowPreviewCandidates(candidates []router.Candidate) []router.Candidate {
	cloned := make([]router.Candidate, len(candidates))
	for i, candidate := range candidates {
		candidate.Endpoint.HealthStatus = models.HealthHealthy
		candidate.Endpoint.CooldownUntil = nil
		cloned[i] = candidate
	}
	return cloned
}

func (s *liveFlowPreviewEngineSession) runSyntheticRequest(ctx context.Context, api *API, payload liveFlowPreviewEnqueuePayload, sequence int64, generation int64, queuedAt time.Time, trustedMetadata map[string]string) {
	schedulerInstance := s.currentSchedulerForGeneration(generation)
	if schedulerInstance == nil {
		return
	}

	requestID := liveFlowPreviewRequestID(s.id, generation, sequence)
	req := router.RequestMeta{
		RequestID:             requestID,
		RouteKind:             payload.RouteKind,
		IncomingModel:         payload.Lane,
		Lane:                  payload.Lane,
		MaxWaitMS:             payload.MaxWaitMS,
		AllowFallback:         payload.AllowFallback,
		Priority:              payload.Priority,
		EstimatedInputTokens:  payload.EstimatedInputTokens,
		EstimatedOutputTokens: payload.EstimatedOutputTokens,
		MaxCostMicros:         payload.MaxCostMicros,
	}
	strategy := router.NewWaterfallStrategy(api.store)
	candidates, err := strategy.SelectCandidates(ctx, req)
	if err != nil {
		s.publishPreviewRequestFailure(req, err)
		return
	}
	candidates = cloneLiveFlowPreviewCandidates(candidates)
	if req.MaxWaitMS <= 0 && len(candidates) > 0 && candidates[0].Lane != nil {
		req.MaxWaitMS = candidates[0].Lane.DefaultMaxWaitMS
	}
	if req.MaxWaitMS <= 0 {
		req.MaxWaitMS = 60_000
	}

	permit, err := schedulerInstance.Submit(ctx, scheduler.SubmitRequest{
		Meta:            req,
		Candidates:      candidates,
		EnqueuedAt:      queuedAt,
		TrustedMetadata: cloneStringMap(trustedMetadata),
		Preview:         true,
	})
	if err != nil {
		if ctx.Err() == nil {
			s.publishPreviewRequestFailure(req, err)
		}
		return
	}

	if strings.EqualFold(payload.Scenario, "ratelimit_429") {
		nextPermit, err := schedulerInstance.Requeue(ctx, permit, 12*time.Second, "upstream throttle")
		if err != nil {
			if ctx.Err() == nil {
				s.publishPreviewRequestFailure(req, err)
			}
			return
		}
		permit = nextPermit
	}

	permit = adjustPreviewPermitStart(permit, time.Now().UTC())

	if err := s.simulateSyntheticService(ctx, schedulerInstance, req, permit, time.Duration(payload.SyntheticServiceMS)*time.Millisecond); err != nil {
		return
	}
	_ = schedulerInstance.CompleteSynthetic(ctx, permit, http.StatusOK, payload.EstimatedInputTokens, payload.EstimatedOutputTokens)
}

func adjustPreviewPermitStart(permit scheduler.Permit, startedAt time.Time) scheduler.Permit {
	if startedAt.IsZero() {
		return permit
	}
	queuedAt := permit.SelectedAt.Add(-permit.Waited)
	if queuedAt.IsZero() || queuedAt.After(startedAt) {
		queuedAt = startedAt
	}
	permit.SelectedAt = startedAt.UTC()
	permit.Waited = startedAt.Sub(queuedAt)
	if permit.Waited < 0 {
		permit.Waited = 0
	}
	return permit
}

func (s *liveFlowPreviewEngineSession) simulateSyntheticService(ctx context.Context, sch *scheduler.Scheduler, req router.RequestMeta, permit scheduler.Permit, duration time.Duration) error {
	startedAt := time.Now().UTC()
	tick := 250 * time.Millisecond
	timer := time.NewTimer(duration)
	ticker := time.NewTicker(tick)
	defer timer.Stop()
	defer ticker.Stop()
	s.publishPreviewRequestProgress(sch, req, permit, "synthetic upstream", startedAt, permit.EstimatedInput, 0)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			elapsed := time.Since(startedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			progress := float64(elapsed) / float64(duration)
			if progress > 1 {
				progress = 1
			}
			downloaded := int64(progress * float64(permit.EstimatedOutput))
			s.publishPreviewRequestProgress(sch, req, permit, "synthetic upstream", startedAt, permit.EstimatedInput, downloaded)
		case <-timer.C:
			s.publishPreviewRequestProgress(sch, req, permit, "synthetic complete", startedAt, permit.EstimatedInput, permit.EstimatedOutput)
			return nil
		}
	}
}

func (s *liveFlowPreviewEngineSession) publishPreviewRequestProgress(sch *scheduler.Scheduler, req router.RequestMeta, permit scheduler.Permit, substatus string, startedAt time.Time, uploadedTokens, downloadedTokens int64) {
	if sch != nil {
		sch.UpdateProgress(permit.TaskID, startedAt, substatus, uploadedTokens, downloadedTokens)
	}
	queuedAt := startedAt.Add(-permit.Waited).UTC()
	s.hub.Publish(telemetry.Event{
		Type: "request_progress",
		Payload: map[string]any{
			"preview_session_id":      s.id,
			"preview_generation":      liveFlowPreviewGenerationFromRequestID(req.RequestID),
			"request_id":              req.RequestID,
			"task_id":                 permit.TaskID,
			"endpoint_id":             permit.Endpoint.UUID,
			"endpoint_name":           permit.Endpoint.Name,
			"provider_id":             permit.Endpoint.ProviderUUID,
			"lane":                    req.Lane,
			"incoming_model":          req.IncomingModel,
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

func (s *liveFlowPreviewEngineSession) publishPreviewRequestFailure(req router.RequestMeta, err error) {
	now := time.Now().UTC()
	s.hub.Publish(telemetry.Event{
		Type: "request_failed",
		Payload: map[string]any{
			"preview_session_id": s.id,
			"preview_generation": liveFlowPreviewGenerationFromRequestID(req.RequestID),
			"request_id":         req.RequestID,
			"lane":               req.Lane,
			"incoming_model":     req.IncomingModel,
			"state":              "failed",
			"queued_at":          now,
			"finished_at":        now,
			"error_text":         security.RedactString(err.Error()),
		},
	})
}

func (s *liveFlowPreviewEngineSession) stopEnqueue() {
	s.mu.Lock()
	if s.enqueueCancel != nil {
		s.enqueueCancel()
		s.enqueueCancel = nil
	}
	s.enqueueRunning = false
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
	s.hub.Publish(telemetry.Event{
		Type: "preview_enqueue_stopped",
		Payload: map[string]any{
			"preview_session_id": s.id,
			"preview_generation": s.currentGeneration(),
		},
	})
}

func (s *liveFlowPreviewEngineSession) close() {
	s.mu.Lock()
	if s.enqueueCancel != nil {
		s.enqueueCancel()
		s.enqueueCancel = nil
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.scheduler != nil {
		s.scheduler.Stop()
		s.scheduler = nil
	}
	s.enqueueRunning = false
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
}

func (s *liveFlowPreviewEngineSession) currentScheduler() *scheduler.Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduler
}

func (s *liveFlowPreviewEngineSession) currentSchedulerForGeneration(generation int64) *scheduler.Scheduler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return nil
	}
	return s.scheduler
}

func (s *liveFlowPreviewEngineSession) currentSchedulerAndGeneration() (*scheduler.Scheduler, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduler, s.generation
}

func (s *liveFlowPreviewEngineSession) currentGeneration() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

func (s *liveFlowPreviewEngineSession) currentContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

func (s *liveFlowPreviewEngineSession) nextSequence() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	s.updatedAt = time.Now().UTC()
	return s.sequence
}

func (s *liveFlowPreviewEngineSession) touch() {
	s.mu.Lock()
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
}

func (s *liveFlowPreviewEngineSession) lastUpdated() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updatedAt
}

func (s *liveFlowPreviewEngineSession) response() liveFlowPreviewSessionResponse {
	return liveFlowPreviewSessionResponse{
		PreviewSessionID:  s.id,
		PreviewGeneration: s.currentGeneration(),
	}
}

func liveFlowPreviewRequestID(sessionID string, generation int64, sequence int64) string {
	return strings.Join([]string{
		"live-flow-preview",
		sessionID,
		strconv.FormatInt(generation, 10),
		strconv.FormatInt(sequence, 10),
	}, "-")
}

func liveFlowPreviewGenerationFromRequestID(requestID string) int64 {
	parts := strings.Split(requestID, "-")
	if len(parts) < 2 {
		return 0
	}
	generation, _ := strconv.ParseInt(parts[len(parts)-2], 10, 64)
	return generation
}
