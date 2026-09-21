package relay

import (
	"context"
	"net/http"

	"github.com/anchorshell/relay/internal/proxy"
)

func proxyUpstreamRequestAdapters(adapters []UpstreamRequestAdapter) []proxy.UpstreamRequestAdapter {
	if len(adapters) == 0 {
		return nil
	}
	items := make([]proxy.UpstreamRequestAdapter, 0, len(adapters))
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		adapter := adapter
		items = append(items, func(ctx context.Context, input proxy.UpstreamRequestInput) (map[string]any, error) {
			return adapter(ctx, UpstreamRequestInput{
				RouteKind: RouteKind(input.RouteKind), ProviderStorageID: input.ProviderStorageID,
				ProviderID: input.ProviderID, ProviderKey: input.ProviderKey,
				CredentialStorageID: input.CredentialStorageID, CredentialID: input.CredentialID,
				EndpointStorageID: input.EndpointStorageID, EndpointID: input.EndpointID,
				ReasoningEffort: input.ReasoningEffort, Body: input.Body,
			})
		})
	}
	return items
}

func proxyUpstreamDispatchers(dispatchers []UpstreamDispatcher) []proxy.UpstreamDispatcher {
	if len(dispatchers) == 0 {
		return nil
	}
	items := make([]proxy.UpstreamDispatcher, 0, len(dispatchers))
	for _, dispatcher := range dispatchers {
		if dispatcher == nil {
			continue
		}
		dispatcher := dispatcher
		items = append(items, func(ctx context.Context, input proxy.UpstreamDispatchInput) (*http.Response, bool, error) {
			return dispatcher(ctx, UpstreamDispatchInput{
				RouteKind: RouteKind(input.RouteKind), ProviderStorageID: input.ProviderStorageID,
				ProviderID: input.ProviderID, ProviderKey: input.ProviderKey,
				CredentialStorageID: input.CredentialStorageID, CredentialID: input.CredentialID,
				EndpointStorageID: input.EndpointStorageID, EndpointID: input.EndpointID,
				UpstreamModel: input.UpstreamModel, Method: input.Method, Path: input.Path,
				Streaming: input.Streaming, Body: input.Body,
			})
		})
	}
	return items
}
