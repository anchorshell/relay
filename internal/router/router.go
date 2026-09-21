package router

import (
	"context"
	"errors"
	"strings"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/pkg/characterization"
	"gorm.io/gorm"
)

type RequestMeta struct {
	RequestID             string                            `json:"request_id"`
	RouteKind             models.RouteKind                  `json:"route_kind"`
	IncomingModel         string                            `json:"incoming_model"`
	Lane                  string                            `json:"lane"`
	ExplicitLane          bool                              `json:"-"`
	Priority              int                               `json:"priority"`
	MaxWaitMS             int64                             `json:"max_wait_ms"`
	AllowFallback         bool                              `json:"allow_fallback"`
	EstimatedInputTokens  int64                             `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64                             `json:"estimated_output_tokens"`
	MaxCostMicros         int64                             `json:"max_cost_micros"`
	OverrideEndpointID    uint                              `json:"-"`
	OverrideEndpointUUID  string                            `json:"override_endpoint_id,omitempty"`
	Streaming             bool                              `json:"streaming"`
	ReasoningEffort       string                            `json:"reasoning_effort,omitempty"`
	Overrides             map[string]any                    `json:"overrides"`
	Characterization      characterization.Characterization `json:"-"`
}

type Candidate struct {
	Endpoint        models.Endpoint     `json:"endpoint"`
	Lane            *models.RoutingLane `json:"lane,omitempty"`
	FallbackCount   int                 `json:"fallback_count"`
	Rank            int                 `json:"rank"`
	RoutingMetadata map[string]string   `json:"-"`
}

type Strategy interface {
	SelectCandidates(ctx context.Context, req RequestMeta) ([]Candidate, error)
}

type WaterfallStrategy struct {
	store *store.Store
}

func NewWaterfallStrategy(st *store.Store) *WaterfallStrategy {
	return &WaterfallStrategy{store: st}
}

func (w *WaterfallStrategy) SelectCandidates(ctx context.Context, req RequestMeta) ([]Candidate, error) {
	if req.OverrideEndpointID == 0 && req.OverrideEndpointUUID != "" {
		id, err := w.store.EndpointIDByUUID(ctx, req.OverrideEndpointUUID)
		if err != nil {
			return nil, err
		}
		req.OverrideEndpointID = id
	}
	if req.OverrideEndpointID != 0 {
		var endpoint models.Endpoint
		if err := w.store.FindByID(ctx, &endpoint, req.OverrideEndpointID); err != nil {
			return nil, err
		}
		if !endpoint.Enabled {
			return nil, errors.New("override endpoint is disabled")
		}
		return []Candidate{{Endpoint: endpoint, Rank: endpoint.ManualRank}}, nil
	}

	if req.Lane == "" {
		req.Lane = req.IncomingModel
	}
	endpoints, lane, err := w.store.ActiveEndpointsForLane(ctx, req.Lane, req.RouteKind)
	if err != nil {
		if req.ExplicitLane || !errors.Is(err, gorm.ErrRecordNotFound) || strings.TrimSpace(req.IncomingModel) == "" {
			return nil, err
		}
		modelEndpoints, modelErr := w.store.ActiveEndpointsForModel(ctx, req.IncomingModel, req.RouteKind)
		if modelErr != nil {
			return nil, modelErr
		}
		if len(modelEndpoints) == 0 {
			return nil, err
		}
		candidates := make([]Candidate, 0, len(modelEndpoints))
		for i, endpoint := range modelEndpoints {
			candidates = append(candidates, Candidate{
				Endpoint:      endpoint,
				FallbackCount: i,
				Rank:          endpoint.ManualRank,
			})
		}
		return candidates, nil
	}
	candidates := make([]Candidate, 0, len(endpoints))
	for i, endpoint := range endpoints {
		candidates = append(candidates, Candidate{
			Endpoint:      endpoint,
			Lane:          lane,
			FallbackCount: i,
			Rank:          endpoint.ManualRank,
		})
	}
	return candidates, nil
}
