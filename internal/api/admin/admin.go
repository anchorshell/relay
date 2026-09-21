package admin

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchorshell/relay/internal/guardrails"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/ws"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type API struct {
	store                     *store.Store
	scheduler                 *scheduler.Scheduler
	telemetry                 *telemetry.Hub
	secrets                   security.SecretManager
	guardrailSecrets          security.GuardrailSecretManager
	guardrailEngine           *guardrails.HTTPGuardrailEngine
	adminToken                string
	adminCookieName           string
	externalAuthorizers       []ExternalAuthorizer
	externalTenantScopeHooks  []ExternalTenantScopeHook
	externalQueryScopeHooks   []ExternalQueryScopeHook
	externalQueueScopeHooks   []ExternalQueueScopeHook
	externalCapacityHooks     []ExternalCapacityRowsHook
	requestContextHooks       []RequestContextHook
	limitHooks                []scheduler.LimitScopeHook
	externalLimitHooks        []scheduler.ExternalLimitHook
	realtimeConnectionHooks   []RealtimeConnectionHook
	softDeleteProviderCatalog bool
	realtimeSnapshotCache     *ws.SnapshotCache
	capacitySequence          atomic.Uint64
	adminSessionsMu           sync.Mutex
	adminSessions             map[string]time.Time
	systemInfo                SystemInfo
	liveFlowPreviewMu         sync.Mutex
	liveFlowPreviewSessions   map[string]*liveFlowPreviewSession
	previewEngineMu           sync.Mutex
	previewEngineSessions     map[string]*liveFlowPreviewEngineSession
}

type ExternalAuthorizer func(*echo.Context) bool

type RealtimeConnectionHook func(*echo.Context, bool)

type TenantScope struct {
	OrganizationUUID string
	UserUUID         string
}

type ExternalTenantScopeHook func(*echo.Context) (TenantScope, error)

type QueryScopeInput struct {
	Resource string
}

type ExternalQueryScopeHook func(*echo.Context, QueryScopeInput) ([]func(*gorm.DB) *gorm.DB, error)

type QueueScopeInput struct{}

type ExternalQueueScope struct {
	OrganizationUUID string
	ActorID          string
	Deny             bool
}

type ExternalQueueScopeHook func(*echo.Context, QueueScopeInput) (ExternalQueueScope, error)

type CapacityScope struct {
	Type string
	ID   string
}

type CapacityRuntimeUsedFunc func(scope CapacityScope, metric string, period string) int64

type ExternalCapacityRowsInput struct {
	Headers     http.Header
	Now         time.Time
	RuntimeUsed CapacityRuntimeUsedFunc
	Preview     bool
}

type ExternalCapacityRow struct {
	Key           string    `json:"key"`
	Label         string    `json:"label"`
	ScopeType     string    `json:"scope_type"`
	ScopeID       string    `json:"scope_id"`
	TargetType    string    `json:"target_type,omitempty"`
	TargetKey     string    `json:"target_key,omitempty"`
	ActorID       string    `json:"actor_id,omitempty"`
	Metric        string    `json:"metric"`
	Period        string    `json:"period"`
	Configured    int64     `json:"configured"`
	Effective     int64     `json:"effective"`
	Used          int64     `json:"used"`
	Reserved      int64     `json:"reserved"`
	Remaining     int64     `json:"remaining"`
	Percent       float64   `json:"percent"`
	WindowStart   time.Time `json:"window_start"`
	ResetAt       time.Time `json:"reset_at,omitempty"`
	Source        string    `json:"source"`
	UserScoped    bool      `json:"user_scoped,omitempty"`
	Blocked       bool      `json:"blocked,omitempty"`
	BlockedUntil  time.Time `json:"blocked_until,omitempty"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
}

type ExternalCapacityChange struct {
	Row     ExternalCapacityRow
	Removed bool
}

type ExternalCapacityRowsHook func(context.Context, ExternalCapacityRowsInput) ([]ExternalCapacityRow, error)

type RequestContextInput struct {
	RequestID string
	RouteKind models.RouteKind
	Headers   http.Header
}

type TrustedRequestContext struct {
	Metadata map[string]string
}

type RequestContextHook func(context.Context, RequestContextInput) (TrustedRequestContext, error)

func New(st *store.Store, sch *scheduler.Scheduler, hub *telemetry.Hub, secrets security.SecretManager, adminToken string, systemInfo SystemInfo) *API {
	api := &API{
		store:                   st,
		scheduler:               sch,
		telemetry:               hub,
		secrets:                 secrets,
		adminToken:              adminToken,
		adminCookieName:         defaultAdminCookieName,
		realtimeSnapshotCache:   ws.NewSnapshotCache(),
		adminSessions:           make(map[string]time.Time),
		systemInfo:              systemInfo,
		liveFlowPreviewSessions: make(map[string]*liveFlowPreviewSession),
		previewEngineSessions:   make(map[string]*liveFlowPreviewEngineSession),
	}
	if guardrailSecrets, ok := any(secrets).(security.GuardrailSecretManager); ok {
		api.guardrailSecrets = guardrailSecrets
		api.guardrailEngine = guardrails.NewHTTPGuardrailEngine(guardrailSecrets.GuardrailSecret)
	}
	return api
}

func (a *API) WithExternalAuthorizers(authorizers ...ExternalAuthorizer) *API {
	if a == nil {
		return nil
	}
	a.externalAuthorizers = append([]ExternalAuthorizer(nil), authorizers...)
	return a
}

func (a *API) WithExternalTenantScopeHooks(hooks ...ExternalTenantScopeHook) *API {
	if a == nil {
		return nil
	}
	a.externalTenantScopeHooks = append([]ExternalTenantScopeHook(nil), hooks...)
	return a
}

func (a *API) WithExternalQueryScopeHooks(hooks ...ExternalQueryScopeHook) *API {
	if a == nil {
		return nil
	}
	a.externalQueryScopeHooks = append([]ExternalQueryScopeHook(nil), hooks...)
	return a
}

func (a *API) WithExternalQueueScopeHooks(hooks ...ExternalQueueScopeHook) *API {
	if a == nil {
		return nil
	}
	a.externalQueueScopeHooks = append([]ExternalQueueScopeHook(nil), hooks...)
	return a
}

func (a *API) WithExternalCapacityRowHooks(hooks ...ExternalCapacityRowsHook) *API {
	if a == nil {
		return nil
	}
	a.externalCapacityHooks = append([]ExternalCapacityRowsHook(nil), hooks...)
	return a
}

func (a *API) WithRequestContextHooks(hooks ...RequestContextHook) *API {
	if a == nil {
		return nil
	}
	a.requestContextHooks = append([]RequestContextHook(nil), hooks...)
	return a
}

func (a *API) WithLimitScopeHooks(hooks ...scheduler.LimitScopeHook) *API {
	if a == nil {
		return nil
	}
	a.limitHooks = append([]scheduler.LimitScopeHook(nil), hooks...)
	return a
}

func (a *API) WithExternalLimitHooks(hooks ...scheduler.ExternalLimitHook) *API {
	if a == nil {
		return nil
	}
	a.externalLimitHooks = append([]scheduler.ExternalLimitHook(nil), hooks...)
	return a
}

func (a *API) WithRealtimeConnectionHooks(hooks ...RealtimeConnectionHook) *API {
	if a == nil {
		return nil
	}
	a.realtimeConnectionHooks = append([]RealtimeConnectionHook(nil), hooks...)
	return a
}

func (a *API) WithSoftDeleteProviderCatalog(enabled bool) *API {
	if a == nil {
		return nil
	}
	a.softDeleteProviderCatalog = enabled
	return a
}

func (a *API) requestContext(ctx context.Context, c *echo.Context, routeKind models.RouteKind) (TrustedRequestContext, error) {
	if a == nil || len(a.requestContextHooks) == 0 {
		return TrustedRequestContext{}, nil
	}
	requestID := c.Request().Header.Get("X-Anchorshell-Request-ID")
	input := RequestContextInput{
		RequestID: requestID,
		RouteKind: routeKind,
		Headers:   c.Request().Header.Clone(),
	}
	var trusted TrustedRequestContext
	for _, hook := range a.requestContextHooks {
		if hook == nil {
			continue
		}
		next, err := hook(ctx, input)
		if err != nil {
			return TrustedRequestContext{}, err
		}
		if len(next.Metadata) > 0 {
			trusted = next
		}
	}
	return trusted, nil
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

func actorIDFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ""
	}
	if value := strings.TrimSpace(metadata["actor_id"]); value != "" {
		return value
	}
	return strings.TrimSpace(metadata["user_uuid"])
}

func (a *API) queryScopes(c *echo.Context, resource string) ([]func(*gorm.DB) *gorm.DB, error) {
	scopes := make([]func(*gorm.DB) *gorm.DB, 0)
	for _, hook := range a.externalQueryScopeHooks {
		if hook == nil {
			continue
		}
		items, err := hook(c, QueryScopeInput{Resource: resource})
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, items...)
	}
	return scopes, nil
}
