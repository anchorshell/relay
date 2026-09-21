package relay

import (
	"context"

	"github.com/anchorshell/relay/internal/scheduler"
)

func schedulerRequestRejectedHooks(hooks []RequestRejectedHook) []scheduler.RequestRejectedHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]scheduler.RequestRejectedHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		items = append(items, func(ctx context.Context, event scheduler.RequestRejectedEvent) {
			hook(ctx, RequestRejectedEvent{
				RequestID:             event.RequestID,
				RouteKind:             RouteKind(event.RouteKind),
				IncomingModel:         event.IncomingModel,
				Lane:                  event.Lane,
				StatusCode:            event.StatusCode,
				State:                 event.State,
				Reason:                event.Reason,
				EstimatedInputTokens:  event.EstimatedInputTokens,
				EstimatedOutputTokens: event.EstimatedOutputTokens,
				QueuedAt:              event.QueuedAt,
				FinishedAt:            event.FinishedAt,
				WaitMS:                event.WaitMS,
				Streaming:             event.Streaming,
				Priority:              event.Priority,
				FallbackCount:         event.FallbackCount,
				Metadata:              cloneStringMap(event.Metadata),
				Characterization:      event.Characterization,
			})
		})
	}
	return items
}
