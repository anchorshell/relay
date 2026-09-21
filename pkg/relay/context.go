package relay

import (
	"context"
	"errors"

	"github.com/anchorshell/relay/internal/api/admin"
	"github.com/anchorshell/relay/internal/proxy"
	"github.com/labstack/echo/v5"
)

func proxyRequestContextHooks(hooks []RequestContextHook) []proxy.RequestContextHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]proxy.RequestContextHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		items = append(items, func(ctx context.Context, input proxy.RequestContextInput) (proxy.TrustedRequestContext, error) {
			trusted, err := hook(ctx, RequestContextInput{
				RequestID: input.RequestID,
				RouteKind: RouteKind(input.RouteKind),
				Headers:   input.Headers.Clone(),
			})
			if err != nil {
				var rejection RequestContextRejection
				if errors.As(err, &rejection) {
					return proxy.TrustedRequestContext{}, proxy.RequestContextRejection{
						Status:     rejection.Status,
						Message:    rejection.Message,
						Code:       rejection.Code,
						RetryAfter: rejection.RetryAfter,
					}
				}
				return proxy.TrustedRequestContext{}, err
			}
			return proxy.TrustedRequestContext{
				LogicalRequestID: trusted.LogicalRequestID,
				PartitionKey:     trusted.PartitionKey,
				Metadata:         cloneStringMap(trusted.Metadata),
				Limits: proxy.TrustedRequestLimits{
					DailySpendLimitCents:     trusted.Limits.DailySpendLimitCents,
					RemainingDailySpendCents: trusted.Limits.RemainingDailySpendCents,
					DailyRequestLimit:        trusted.Limits.DailyRequestLimit,
					RemainingDailyRequests:   trusted.Limits.RemainingDailyRequests,
					DailyTokenLimit:          trusted.Limits.DailyTokenLimit,
					RemainingDailyTokens:     trusted.Limits.RemainingDailyTokens,
				},
			}, nil
		})
	}
	return items
}

func proxyPayloadCapturePolicyHooks(hooks []PayloadCapturePolicyHook) []proxy.PayloadCapturePolicyHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]proxy.PayloadCapturePolicyHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		items = append(items, func(ctx context.Context, input proxy.PayloadCapturePolicyInput) (bool, error) {
			return hook(ctx, PayloadCapturePolicyInput{
				RequestID: input.RequestID,
				TrustedContext: TrustedRequestContext{
					LogicalRequestID: input.TrustedContext.LogicalRequestID,
					PartitionKey:     input.TrustedContext.PartitionKey,
					Metadata:         cloneStringMap(input.TrustedContext.Metadata),
				},
			})
		})
	}
	return items
}

func proxyRequestAcceptedHooks(hooks []RequestAcceptedHook) []proxy.RequestAcceptedHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]proxy.RequestAcceptedHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		hook := hook
		items = append(items, func(ctx context.Context, event proxy.RequestAcceptedEvent) (map[string]string, error) {
			metadata, err := hook(ctx, RequestAcceptedEvent{
				RequestID:     event.RequestID,
				RouteKind:     RouteKind(event.RouteKind),
				IncomingModel: event.IncomingModel,
				Lane:          event.Lane,
				Streaming:     event.Streaming,
				Metadata:      cloneStringMap(event.Metadata),
			})
			if err != nil {
				var rejection RequestContextRejection
				if errors.As(err, &rejection) {
					return nil, proxy.RequestContextRejection{
						Status:     rejection.Status,
						Message:    rejection.Message,
						Code:       rejection.Code,
						RetryAfter: rejection.RetryAfter,
					}
				}
				return nil, err
			}
			return metadata, nil
		})
	}
	return items
}

func adminRequestContextHooks(hooks []RequestContextHook) []admin.RequestContextHook {
	if len(hooks) == 0 {
		return nil
	}
	items := make([]admin.RequestContextHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		items = append(items, func(ctx context.Context, input admin.RequestContextInput) (admin.TrustedRequestContext, error) {
			trusted, err := hook(ctx, RequestContextInput{
				RequestID: input.RequestID,
				RouteKind: RouteKind(input.RouteKind),
				Headers:   input.Headers.Clone(),
			})
			if err != nil {
				var rejection RequestContextRejection
				if errors.As(err, &rejection) {
					return admin.TrustedRequestContext{}, echo.NewHTTPError(rejection.Status, rejection.Message)
				}
				return admin.TrustedRequestContext{}, err
			}
			return admin.TrustedRequestContext{
				Metadata: cloneStringMap(trusted.Metadata),
			}, nil
		})
	}
	return items
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
