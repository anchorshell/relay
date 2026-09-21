package relay

import (
	"context"

	"github.com/anchorshell/relay/internal/proxy"
)

func proxyRequestInFlightHooks(hooks []RequestInFlightHook) []proxy.RequestInFlightHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]proxy.RequestInFlightHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		items = append(items, func(ctx context.Context, event proxy.RequestInFlightEvent) {
			hook(ctx, RequestInFlightEvent{
				RequestID:       event.RequestID,
				RouteKind:       RouteKind(event.RouteKind),
				IncomingModel:   event.IncomingModel,
				Lane:            event.Lane,
				EndpointID:      event.EndpointID,
				EndpointName:    event.EndpointName,
				ProviderID:      event.ProviderID,
				UpstreamModel:   event.UpstreamModel,
				ReasoningEffort: event.ReasoningEffort,
				Streaming:       event.Streaming,
				Waited:          event.Waited,
				WaitMS:          event.Waited.Milliseconds(),
				SelectedAt:      event.SelectedAt,
				Metadata:        cloneStringMap(event.TrustedContext.Metadata),
				Limits: TrustedRequestLimits{
					DailySpendLimitCents:     event.TrustedContext.Limits.DailySpendLimitCents,
					RemainingDailySpendCents: event.TrustedContext.Limits.RemainingDailySpendCents,
					DailyRequestLimit:        event.TrustedContext.Limits.DailyRequestLimit,
					RemainingDailyRequests:   event.TrustedContext.Limits.RemainingDailyRequests,
					DailyTokenLimit:          event.TrustedContext.Limits.DailyTokenLimit,
					RemainingDailyTokens:     event.TrustedContext.Limits.RemainingDailyTokens,
				},
			})
		})
	}
	return items
}
