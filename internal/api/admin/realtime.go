package admin

import (
	"net/http"
	"strings"

	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/internal/ws"
	"github.com/labstack/echo/v5"
)

func (a *API) EvictRuntimeCaches(organizationUUID string) {
	if a == nil {
		return
	}
	a.realtimeSnapshotCache.EvictTenant(organizationUUID)
}

func (a *API) notifyRealtimeConnection(c *echo.Context, opened bool) {
	if a == nil || c == nil {
		return
	}
	for _, hook := range a.realtimeConnectionHooks {
		if hook != nil {
			hook(c, opened)
		}
	}
}

func (a *API) eventLogs(c *echo.Context) error {
	events := a.telemetry.Events()
	if len(events) == 0 {
		return c.JSON(http.StatusOK, events)
	}
	filtered := make([]telemetry.Event, 0, len(events))
	for _, event := range events {
		if a.telemetryEventFilter(c, event) {
			filtered = append(filtered, event)
		}
	}
	return c.JSON(http.StatusOK, filtered)
}

func (a *API) telemetryEventFilter(c *echo.Context, event telemetry.Event) bool {
	scope, err := a.queueScope(c)
	if err != nil || scope.Deny {
		return false
	}
	if scope.OrganizationUUID == "" && scope.ActorID == "" {
		return true
	}
	if !tenantTelemetryEvent(event.Type) {
		return true
	}
	eventOrg := organizationUUIDFromEventPayload(event.Payload)
	if scope.OrganizationUUID != "" && (eventOrg == "" || !strings.EqualFold(eventOrg, scope.OrganizationUUID)) {
		return false
	}
	if scope.ActorID != "" && telemetryEventRequiresActor(event) {
		return strings.EqualFold(strings.TrimSpace(actorIDFromEventPayload(event.Payload)), scope.ActorID)
	}
	return true
}

func (a *API) telemetryEventAudience(event telemetry.Event) telemetry.Audience {
	if !tenantTelemetryEvent(event.Type) {
		return telemetry.Audience{}
	}
	return telemetry.Audience{
		Scoped:      true,
		Partition:   organizationUUIDFromEventPayload(event.Payload),
		Subject:     actorIDFromEventPayload(event.Payload),
		SubjectOnly: telemetryEventRequiresActor(event),
	}
}

func (a *API) realtimeScopedConnection(c *echo.Context) (ws.ScopedConnection, error) {
	scope, err := a.queueScope(c)
	if err != nil {
		return ws.ScopedConnection{}, err
	}
	if scope.Deny {
		return ws.ScopedConnection{}, echo.NewHTTPError(http.StatusForbidden, "realtime scope denied")
	}
	partition := strings.TrimSpace(scope.OrganizationUUID)
	subject := strings.TrimSpace(scope.ActorID)
	user := ""
	if tenant, ok := tenancy.ScopeFromContext(c.Request().Context()); ok {
		if partition == "" {
			partition = tenant.OrganizationUUID
		}
		user = tenant.UserUUID
	}
	cacheKey := strings.Join([]string{"realtime-v1", partition, user, subject}, "\x1f")
	return ws.ScopedConnection{
		Scope: telemetry.SubscriptionScope{
			Partition: partition,
			Subject:   subject,
		},
		SnapshotCacheKey: cacheKey,
	}, nil
}

func telemetryEventRequiresActor(event telemetry.Event) bool {
	if strings.HasPrefix(event.Type, "request_") {
		return true
	}
	if event.Type != "capacity_limit_state" || event.Payload == nil {
		return false
	}
	if userScoped, ok := event.Payload["user_scoped"].(bool); ok {
		return userScoped
	}
	row, ok := event.Payload["row"].(map[string]any)
	if !ok {
		return false
	}
	userScoped, _ := row["user_scoped"].(bool)
	return userScoped
}

func tenantTelemetryEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "request_") ||
		eventType == "capacity_limit_state" ||
		eventType == "endpoint_health_change" ||
		eventType == "observed_limit_learned" ||
		eventType == "limit_policy_changed" ||
		eventType == "limit_policy_deleted"
}

func organizationUUIDFromEventPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	switch value := payload["organization_uuid"].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func actorIDFromEventPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	switch value := payload["actor_id"].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}
