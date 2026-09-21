package relay

import (
	"context"
	"strings"

	"github.com/anchorshell/relay/internal/tenancy"
)

type TenantScope struct {
	OrganizationUUID string
	UserUUID         string
}

func ContextWithTenantScope(ctx context.Context, scope TenantScope) context.Context {
	return tenancy.ContextWithScope(ctx, tenancy.Scope{
		OrganizationUUID: strings.TrimSpace(scope.OrganizationUUID),
		UserUUID:         strings.TrimSpace(scope.UserUUID),
	})
}

func TenantScopeFromContext(ctx context.Context) (TenantScope, bool) {
	scope, ok := tenancy.ScopeFromContext(ctx)
	if !ok {
		return TenantScope{}, false
	}
	return TenantScope{
		OrganizationUUID: scope.OrganizationUUID,
		UserUUID:         scope.UserUUID,
	}, true
}
