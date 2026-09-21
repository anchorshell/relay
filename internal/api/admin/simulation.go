package admin

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/labstack/echo/v5"
)

const (
	defaultLiveFlowSimulationRequestCount = 72
	maxLiveFlowSimulationRequestCount     = 180
	liveFlowSimulationArrivalInterval     = 250
	liveFlowPreviewSessionTTL             = 2 * time.Hour
	maxLiveFlowPreviewSessions            = 128
)

func (a *API) simulateLane(c *echo.Context) error {
	var payload struct {
		Lane                  string           `json:"lane"`
		IncomingModel         string           `json:"incoming_model"`
		RouteKind             models.RouteKind `json:"route_kind"`
		MaxWaitMS             int64            `json:"max_wait_ms"`
		AllowFallback         bool             `json:"allow_fallback"`
		Priority              int              `json:"priority"`
		EstimatedInputTokens  int64            `json:"estimated_input_tokens"`
		EstimatedOutputTokens int64            `json:"estimated_output_tokens"`
		MaxCostMicros         int64            `json:"max_cost_micros"`
		OverrideEndpointID    string           `json:"override_endpoint_id"`
	}
	if err := c.Bind(&payload); err != nil {
		return err
	}
	overrideEndpointID := uint(0)
	if payload.OverrideEndpointID != "" {
		id, err := a.store.EndpointIDByUUID(c.Request().Context(), payload.OverrideEndpointID)
		if err != nil {
			return err
		}
		overrideEndpointID = id
	}
	req := router.RequestMeta{
		RequestID:             "simulation",
		RouteKind:             payload.RouteKind,
		IncomingModel:         payload.IncomingModel,
		Lane:                  payload.Lane,
		MaxWaitMS:             payload.MaxWaitMS,
		AllowFallback:         payload.AllowFallback,
		Priority:              payload.Priority,
		EstimatedInputTokens:  payload.EstimatedInputTokens,
		EstimatedOutputTokens: payload.EstimatedOutputTokens,
		MaxCostMicros:         payload.MaxCostMicros,
		OverrideEndpointID:    overrideEndpointID,
		OverrideEndpointUUID:  payload.OverrideEndpointID,
	}
	strategy := router.NewWaterfallStrategy(a.store)
	candidates, err := strategy.SelectCandidates(c.Request().Context(), req)
	if err != nil {
		return err
	}
	if req.MaxWaitMS <= 0 && len(candidates) > 0 && candidates[0].Lane != nil {
		req.MaxWaitMS = candidates[0].Lane.DefaultMaxWaitMS
	}
	if req.MaxWaitMS <= 0 {
		req.MaxWaitMS = 60_000
	}
	planner, err := a.newDryRunScheduler(c.Request().Context(), true)
	if err != nil {
		return err
	}
	permit, err := planner.DryRunSubmit(c.Request().Context(), scheduler.SubmitRequest{
		Meta:       req,
		Candidates: candidates,
		EnqueuedAt: time.Now().UTC(),
		Preview:    true,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"selected":   selectedTrace(permit.CandidateTrace),
		"candidates": permit.CandidateTrace,
	})
}

type liveFlowSimulationRequest struct {
	RequestID             string                     `json:"request_id"`
	Sequence              int                        `json:"sequence"`
	IncomingModel         string                     `json:"incoming_model"`
	QueuedAt              time.Time                  `json:"queued_at"`
	SelectedAt            *time.Time                 `json:"selected_at,omitempty"`
	WaitMS                int64                      `json:"wait_ms"`
	Status                string                     `json:"status"`
	State                 string                     `json:"state"`
	EndpointID            string                     `json:"endpoint_id"`
	EndpointName          string                     `json:"endpoint_name"`
	ProviderID            string                     `json:"provider_id"`
	ActorID               string                     `json:"actor_id,omitempty"`
	PredictedEligibleAt   time.Time                  `json:"predicted_eligible_at"`
	DelayReason           string                     `json:"delay_reason"`
	Selected              *scheduler.CandidateTrace  `json:"selected,omitempty"`
	Candidates            []scheduler.CandidateTrace `json:"candidates"`
	EstimatedInputTokens  int64                      `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64                      `json:"estimated_output_tokens"`
	EstimatedCostMicros   int64                      `json:"estimated_cost_micros"`
	FallbackCount         int                        `json:"fallback_count"`
}

type liveFlowPreviewSession struct {
	planner   *scheduler.Scheduler
	lane      string
	routeKind models.RouteKind
	updatedAt time.Time
}

type liveFlowSimulationResponse struct {
	StartedAt         time.Time                   `json:"started_at"`
	RequestCount      int                         `json:"request_count"`
	ArrivalIntervalMS int64                       `json:"arrival_interval_ms"`
	PreviewSessionID  string                      `json:"preview_session_id,omitempty"`
	Requests          []liveFlowSimulationRequest `json:"requests"`
}

type liveFlowSimulationPayload struct {
	PreviewSessionID      string           `json:"preview_session_id"`
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
}

func normalizeLiveFlowSimulationPayload(payload *liveFlowSimulationPayload) {
	payload.PreviewSessionID = strings.TrimSpace(payload.PreviewSessionID)
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
}

func (a *API) simulateLiveFlow(c *echo.Context) error {
	var payload liveFlowSimulationPayload
	if err := c.Bind(&payload); err != nil {
		return err
	}
	normalizeLiveFlowSimulationPayload(&payload)
	if len(payload.PreviewSessionID) > 128 {
		return echo.NewHTTPError(http.StatusBadRequest, "preview session id is too long")
	}

	planner, previewSessionID, err := a.liveFlowPreviewPlanner(c.Request().Context(), payload)
	if err != nil {
		return err
	}
	trusted, err := a.requestContext(c.Request().Context(), c, payload.RouteKind)
	if err != nil {
		return err
	}
	strategy := router.NewWaterfallStrategy(a.store)
	startedAt := time.Now().UTC()
	requestIDPrefix := "live-flow-preview-" + strconv.FormatInt(startedAt.UnixNano(), 10)
	if previewSessionID != "" {
		requestIDPrefix = "live-flow-preview-" + previewSessionID + "-" + strconv.FormatInt(startedAt.UnixNano(), 10)
	}
	requests := make([]liveFlowSimulationRequest, 0, payload.RequestCount)

	for i := 0; i < payload.RequestCount; i++ {
		queuedAt := startedAt.Add(time.Duration(i*int(payload.ArrivalIntervalMS)) * time.Millisecond)
		req := router.RequestMeta{
			RequestID:             requestIDPrefix + "-" + strconv.Itoa(i+1),
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
		candidates, err := strategy.SelectCandidates(c.Request().Context(), req)
		if err != nil {
			return err
		}
		if req.MaxWaitMS <= 0 && len(candidates) > 0 && candidates[0].Lane != nil {
			req.MaxWaitMS = candidates[0].Lane.DefaultMaxWaitMS
		}
		if req.MaxWaitMS <= 0 {
			req.MaxWaitMS = 60_000
		}

		permit, err := planner.DryRunSubmit(c.Request().Context(), scheduler.SubmitRequest{
			Meta:            req,
			Candidates:      candidates,
			EnqueuedAt:      queuedAt,
			TrustedMetadata: cloneStringMap(trusted.Metadata),
			Preview:         true,
		})
		if err != nil {
			return err
		}
		requests = append(requests, liveFlowRequestFromPermit(req, i+1, queuedAt, permit, trusted.Metadata))
	}

	return c.JSON(http.StatusOK, liveFlowSimulationResponse{
		StartedAt:         startedAt,
		RequestCount:      len(requests),
		ArrivalIntervalMS: payload.ArrivalIntervalMS,
		PreviewSessionID:  previewSessionID,
		Requests:          requests,
	})
}

func (a *API) liveFlowPreviewPlanner(ctx context.Context, payload liveFlowSimulationPayload) (*scheduler.Scheduler, string, error) {
	if payload.PreviewSessionID == "" {
		planner, err := a.newDryRunScheduler(ctx, false)
		return planner, "", err
	}

	now := time.Now().UTC()
	a.liveFlowPreviewMu.Lock()
	if a.liveFlowPreviewSessions == nil {
		a.liveFlowPreviewSessions = make(map[string]*liveFlowPreviewSession)
	}
	a.pruneLiveFlowPreviewSessionsLocked(now)
	if session := a.liveFlowPreviewSessions[payload.PreviewSessionID]; session != nil && session.lane == payload.Lane && session.routeKind == payload.RouteKind {
		session.updatedAt = now
		planner := session.planner
		a.liveFlowPreviewMu.Unlock()
		return planner, payload.PreviewSessionID, nil
	}
	a.liveFlowPreviewMu.Unlock()

	planner, err := a.newDryRunScheduler(ctx, false)
	if err != nil {
		return nil, "", err
	}

	a.liveFlowPreviewMu.Lock()
	defer a.liveFlowPreviewMu.Unlock()
	if a.liveFlowPreviewSessions == nil {
		a.liveFlowPreviewSessions = make(map[string]*liveFlowPreviewSession)
	}
	if session := a.liveFlowPreviewSessions[payload.PreviewSessionID]; session != nil && session.lane == payload.Lane && session.routeKind == payload.RouteKind {
		session.updatedAt = now
		return session.planner, payload.PreviewSessionID, nil
	}
	a.liveFlowPreviewSessions[payload.PreviewSessionID] = &liveFlowPreviewSession{
		planner:   planner,
		lane:      payload.Lane,
		routeKind: payload.RouteKind,
		updatedAt: now,
	}
	a.pruneLiveFlowPreviewSessionsLocked(now)
	return planner, payload.PreviewSessionID, nil
}

func (a *API) pruneLiveFlowPreviewSessionsLocked(now time.Time) {
	for id, session := range a.liveFlowPreviewSessions {
		if now.Sub(session.updatedAt) > liveFlowPreviewSessionTTL {
			delete(a.liveFlowPreviewSessions, id)
		}
	}
	if len(a.liveFlowPreviewSessions) <= maxLiveFlowPreviewSessions {
		return
	}
	type sessionAge struct {
		id        string
		updatedAt time.Time
	}
	items := make([]sessionAge, 0, len(a.liveFlowPreviewSessions))
	for id, session := range a.liveFlowPreviewSessions {
		items = append(items, sessionAge{id: id, updatedAt: session.updatedAt})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].updatedAt.Before(items[j].updatedAt)
	})
	for len(a.liveFlowPreviewSessions) > maxLiveFlowPreviewSessions && len(items) > 0 {
		delete(a.liveFlowPreviewSessions, items[0].id)
		items = items[1:]
	}
}

func (a *API) newDryRunScheduler(ctx context.Context, hydrateUsage bool) (*scheduler.Scheduler, error) {
	tracker := limits.NewTracker(a.store)
	if hydrateUsage {
		if err := tracker.HydrateRecentUsage(ctx, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	resolver := limits.NewResolver(a.store, tracker)
	resolver.LoadSettings(ctx)
	sch := scheduler.New(a.store, resolver, tracker, nil, scheduler.HeuristicEstimator{}, 60_000, 10_000, 0).
		WithLimitScopeHooks(a.limitHooks...).
		WithExternalLimitHooks(a.externalLimitHooks...)
	return sch, nil
}

func liveFlowRequestFromPermit(req router.RequestMeta, sequence int, queuedAt time.Time, permit scheduler.Permit, trustedMetadata map[string]string) liveFlowSimulationRequest {
	selected := selectedTrace(permit.CandidateTrace)
	selectedAt := permit.SelectedAt.UTC()
	waitMS := selectedAt.Sub(queuedAt).Milliseconds()
	if waitMS < 0 {
		waitMS = 0
	}
	state := "ready"
	if waitMS > 0 {
		state = "waiting"
	}
	delayReason := ""
	if selected != nil {
		delayReason = selected.Reason
	}
	return liveFlowSimulationRequest{
		RequestID:             req.RequestID,
		Sequence:              sequence,
		IncomingModel:         req.IncomingModel,
		QueuedAt:              queuedAt,
		SelectedAt:            &selectedAt,
		WaitMS:                waitMS,
		Status:                "selected",
		State:                 state,
		EndpointID:            permit.Endpoint.UUID,
		EndpointName:          permit.Endpoint.Name,
		ProviderID:            permit.Endpoint.ProviderUUID,
		ActorID:               actorIDFromMetadata(trustedMetadata),
		PredictedEligibleAt:   selectedAt,
		DelayReason:           delayReason,
		Selected:              selected,
		Candidates:            permit.CandidateTrace,
		EstimatedInputTokens:  permit.EstimatedInput,
		EstimatedOutputTokens: permit.EstimatedOutput,
		EstimatedCostMicros:   permit.EstimatedCost,
		FallbackCount:         permit.FallbackCount,
	}
}

func selectedTrace(trace []scheduler.CandidateTrace) *scheduler.CandidateTrace {
	for i := range trace {
		if trace[i].Decision == "selected" {
			selected := trace[i]
			return &selected
		}
	}
	return nil
}
