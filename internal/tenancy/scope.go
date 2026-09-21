package tenancy

import (
	"context"
	"strings"
)

type scopeContextKey struct{}
type skipCallbacksContextKey struct{}

type Scope struct {
	OrganizationUUID string
	UserUUID         string
	Metadata         map[string]string
}

func ContextWithScope(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	scope.OrganizationUUID = strings.TrimSpace(scope.OrganizationUUID)
	scope.UserUUID = strings.TrimSpace(scope.UserUUID)
	scope.Metadata = cloneMetadata(scope.Metadata)
	if scope.OrganizationUUID == "" {
		return ctx
	}
	return context.WithValue(ctx, scopeContextKey{}, scope)
}

func ScopeFromContext(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	scope, ok := ctx.Value(scopeContextKey{}).(Scope)
	if !ok || strings.TrimSpace(scope.OrganizationUUID) == "" {
		return Scope{}, false
	}
	scope.OrganizationUUID = strings.TrimSpace(scope.OrganizationUUID)
	scope.UserUUID = strings.TrimSpace(scope.UserUUID)
	scope.Metadata = cloneMetadata(scope.Metadata)
	return scope, true
}

func OrganizationUUID(ctx context.Context) string {
	scope, ok := ScopeFromContext(ctx)
	if !ok {
		return ""
	}
	return scope.OrganizationUUID
}

func ScopeFromMetadata(metadata map[string]string) (Scope, bool) {
	if len(metadata) == 0 {
		return Scope{}, false
	}
	return normalizeScope(Scope{
		OrganizationUUID: metadata["organization_uuid"],
		UserUUID:         metadata["user_uuid"],
		Metadata:         metadata,
	})
}

func ContextWithMetadataScope(ctx context.Context, metadata map[string]string) context.Context {
	scope, ok := ScopeFromMetadata(metadata)
	if !ok {
		return ctx
	}
	return ContextWithScope(ctx, scope)
}

func normalizeScope(scope Scope) (Scope, bool) {
	scope.OrganizationUUID = strings.TrimSpace(scope.OrganizationUUID)
	scope.UserUUID = strings.TrimSpace(scope.UserUUID)
	scope.Metadata = cloneMetadata(scope.Metadata)
	return scope, scope.OrganizationUUID != ""
}

func cloneMetadata(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func contextSkippingCallbacks(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skipCallbacksContextKey{}, true)
}

func callbacksSkipped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(skipCallbacksContextKey{}).(bool)
	return value
}
