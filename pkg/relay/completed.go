package relay

import (
	"context"

	"github.com/anchorshell/relay/internal/proxy"
)

func proxyRequestCompletedHooks(hooks []RequestCompletedHook) []proxy.RequestCompletedHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]proxy.RequestCompletedHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		items = append(items, func(ctx context.Context, event proxy.RequestCompletedEvent) {
			hook(ctx, RequestCompletedEvent{
				RequestID:             event.RequestID,
				RouteKind:             RouteKind(event.RouteKind),
				IncomingModel:         event.IncomingModel,
				Lane:                  event.Lane,
				EndpointID:            event.EndpointID,
				EndpointName:          event.EndpointName,
				ProviderID:            event.ProviderID,
				UpstreamModel:         event.UpstreamModel,
				ReasoningEffort:       event.ReasoningEffort,
				Streaming:             event.Streaming,
				StatusCode:            event.StatusCode,
				State:                 event.State,
				WaitMS:                event.WaitMS,
				LatencyMS:             event.LatencyMS,
				EstimatedInputTokens:  event.EstimatedInputTokens,
				EstimatedOutputTokens: event.EstimatedOutputTokens,
				ActualInputTokens:     event.ActualInputTokens,
				ActualOutputTokens:    event.ActualOutputTokens,
				ActualTotalTokens:     event.ActualTotalTokens,
				EstimatedCostMicros:   event.EstimatedCostMicros,
				ActualCostMicros:      event.ActualCostMicros,
				QueuedAt:              event.QueuedAt,
				StartedAt:             event.StartedAt,
				FinishedAt:            event.FinishedAt,
				Metadata:              cloneStringMap(event.TrustedContext.Metadata),
				Limits: TrustedRequestLimits{
					DailySpendLimitCents:     event.TrustedContext.Limits.DailySpendLimitCents,
					RemainingDailySpendCents: event.TrustedContext.Limits.RemainingDailySpendCents,
					DailyRequestLimit:        event.TrustedContext.Limits.DailyRequestLimit,
					RemainingDailyRequests:   event.TrustedContext.Limits.RemainingDailyRequests,
					DailyTokenLimit:          event.TrustedContext.Limits.DailyTokenLimit,
					RemainingDailyTokens:     event.TrustedContext.Limits.RemainingDailyTokens,
				},
				Characterization: event.Characterization,
			})
		})
	}
	return items
}
