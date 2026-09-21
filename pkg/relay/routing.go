package relay

import (
	"context"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/router"
	"github.com/anchorshell/relay/internal/tenancy"
)

type routingStrategyAdapter struct {
	base        router.Strategy
	middlewares []RoutingMiddleware
}

type routingPolicyFunc func(context.Context, RoutingInput) (RoutingDecision, error)

func (f routingPolicyFunc) SelectCandidates(ctx context.Context, input RoutingInput) (RoutingDecision, error) {
	return f(ctx, input)
}

func newRoutingStrategyAdapter(base router.Strategy, middlewares []RoutingMiddleware) router.Strategy {
	return routingStrategyAdapter{base: base, middlewares: append([]RoutingMiddleware(nil), middlewares...)}
}

func (a routingStrategyAdapter) SelectCandidates(ctx context.Context, req router.RequestMeta) ([]router.Candidate, error) {
	var baseCandidates []router.Candidate
	policy := RoutingPolicy(routingPolicyFunc(func(ctx context.Context, input RoutingInput) (RoutingDecision, error) {
		candidates, err := a.base.SelectCandidates(ctx, internalRequestMeta(input))
		if err != nil {
			return RoutingDecision{}, err
		}
		baseCandidates = append([]router.Candidate(nil), candidates...)
		return RoutingDecision{Candidates: publicRoutingCandidates(candidates)}, nil
	}))
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		policy = a.middlewares[i](policy)
	}
	decision, err := policy.SelectCandidates(ctx, publicRoutingInput(ctx, req))
	if err != nil {
		return nil, err
	}
	if len(decision.Candidates) == 0 {
		return nil, nil
	}

	byEndpointID := make(map[string]router.Candidate, len(baseCandidates))
	for _, candidate := range baseCandidates {
		if candidate.Endpoint.UUID != "" {
			byEndpointID[candidate.Endpoint.UUID] = candidate
		}
	}

	selected := make([]router.Candidate, 0, len(decision.Candidates))
	for _, item := range decision.Candidates {
		candidate, ok := byEndpointID[item.EndpointID]
		if !ok {
			continue
		}
		candidate.Rank = item.Rank
		candidate.FallbackCount = item.FallbackCount
		candidate.RoutingMetadata = cloneStringMap(item.Metadata)
		for key, value := range decision.Metadata {
			if candidate.RoutingMetadata == nil {
				candidate.RoutingMetadata = map[string]string{}
			}
			candidate.RoutingMetadata[key] = value
		}
		selected = append(selected, candidate)
	}
	return selected, nil
}

func publicRoutingInput(ctx context.Context, req router.RequestMeta) RoutingInput {
	input := RoutingInput{
		RequestID:             req.RequestID,
		RouteKind:             RouteKind(req.RouteKind),
		IncomingModel:         req.IncomingModel,
		Lane:                  req.Lane,
		Priority:              req.Priority,
		MaxWaitMS:             req.MaxWaitMS,
		AllowFallback:         req.AllowFallback,
		EstimatedInputTokens:  req.EstimatedInputTokens,
		EstimatedOutputTokens: req.EstimatedOutputTokens,
		MaxCostMicros:         req.MaxCostMicros,
		OverrideEndpointID:    req.OverrideEndpointUUID,
		Streaming:             req.Streaming,
		ReasoningEffort:       req.ReasoningEffort,
		Characterization:      req.Characterization,
	}
	if scope, ok := tenancy.ScopeFromContext(ctx); ok {
		input.TrustedMetadata = cloneStringMap(scope.Metadata)
	}
	return input
}

func internalRequestMeta(input RoutingInput) router.RequestMeta {
	return router.RequestMeta{
		RequestID:             input.RequestID,
		RouteKind:             models.RouteKind(input.RouteKind),
		IncomingModel:         input.IncomingModel,
		Lane:                  input.Lane,
		Priority:              input.Priority,
		MaxWaitMS:             input.MaxWaitMS,
		AllowFallback:         input.AllowFallback,
		EstimatedInputTokens:  input.EstimatedInputTokens,
		EstimatedOutputTokens: input.EstimatedOutputTokens,
		MaxCostMicros:         input.MaxCostMicros,
		OverrideEndpointUUID:  input.OverrideEndpointID,
		Streaming:             input.Streaming,
		ReasoningEffort:       input.ReasoningEffort,
		Characterization:      input.Characterization,
	}
}

func publicRoutingCandidates(candidates []router.Candidate) []RoutingCandidate {
	items := make([]RoutingCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		lane := ""
		if candidate.Lane != nil {
			lane = candidate.Lane.Name
		}
		items = append(items, RoutingCandidate{
			EndpointID:              candidate.Endpoint.UUID,
			EndpointName:            candidate.Endpoint.Name,
			ProviderID:              candidate.Endpoint.ProviderUUID,
			UpstreamModel:           candidate.Endpoint.UpstreamModel,
			Lane:                    lane,
			Rank:                    candidate.Rank,
			FallbackCount:           candidate.FallbackCount,
			SupportsStreaming:       candidate.Endpoint.SupportsStreaming,
			ReasoningControlKind:    candidate.Endpoint.ReasoningControlKind,
			AllowedReasoningEfforts: append([]string(nil), candidate.Endpoint.AllowedReasoningEfforts...),
			DefaultReasoningEffort:  candidate.Endpoint.DefaultReasoningEffort,
			MaximumReasoningEffort:  candidate.Endpoint.MaximumReasoningEffort,
			Metadata:                cloneStringMap(candidate.RoutingMetadata),
		})
	}
	return items
}
