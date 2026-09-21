package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/labstack/echo/v5"
)

func (a *API) health(c *echo.Context) error {
	scopes, err := a.queryScopes(c, "endpoints")
	if err != nil {
		return err
	}
	endpoints, err := a.store.ListEndpointsWithScopes(c.Request().Context(), scopes...)
	if err != nil {
		return err
	}
	endpoints, err = a.decorateEndpointHealth(c.Request().Context(), endpoints)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, endpoints)
}

func (a *API) decorateEndpointHealth(ctx context.Context, endpoints []models.Endpoint) ([]models.Endpoint, error) {
	memberships, err := a.store.ListLaneMemberships(ctx)
	if err != nil {
		return nil, err
	}
	lanes, err := a.store.ListLanes(ctx)
	if err != nil {
		return nil, err
	}
	laneByID := make(map[uint]models.RoutingLane, len(lanes))
	for _, lane := range lanes {
		laneByID[lane.ID] = lane
	}
	laneOptionsByEndpoint := make(map[uint][]models.RoutingLane)
	for _, membership := range memberships {
		if !membership.Enabled {
			continue
		}
		lane, ok := laneByID[membership.LaneID]
		if !ok || !lane.Enabled {
			continue
		}
		laneOptionsByEndpoint[membership.EndpointID] = append(laneOptionsByEndpoint[membership.EndpointID], lane)
	}
	now := time.Now().UTC()
	for i := range endpoints {
		previousStatus := endpoints[i].HealthStatus
		previousCooldown := endpoints[i].CooldownUntil
		previousCooldownReason := endpoints[i].CooldownReason
		previousCooldownStatusCode := endpoints[i].CooldownStatusCode
		status, until, err := a.scheduler.DeriveEndpointHealth(ctx, endpoints[i], laneOptionsByEndpoint[endpoints[i].ID], now)
		if err != nil {
			return nil, err
		}
		endpoints[i].HealthStatus = status
		endpoints[i].CooldownUntil = until
		preserveCooldownSource := previousCooldownReason != "" &&
			(status == models.HealthCoolingDown || status == models.HealthRateLimited || status == models.HealthUnhealthy) &&
			sameOptionalTime(previousCooldown, until)
		if !preserveCooldownSource {
			endpoints[i].CooldownReason = ""
			endpoints[i].CooldownStatusCode = 0
		}
		if previousStatus != status || !sameOptionalTime(previousCooldown, until) ||
			previousCooldownReason != endpoints[i].CooldownReason || previousCooldownStatusCode != endpoints[i].CooldownStatusCode {
			if err := a.store.SaveEndpointState(ctx, endpoints[i]); err != nil {
				return nil, err
			}
			if a.telemetry != nil {
				a.telemetry.Publish(telemetry.Event{
					Type: "endpoint_health_change",
					Payload: map[string]any{
						"organization_uuid":    tenancy.OrganizationUUID(ctx),
						"endpoint_id":          endpoints[i].UUID,
						"provider_id":          endpoints[i].ProviderUUID,
						"health_status":        endpoints[i].HealthStatus,
						"cooldown_until":       endpoints[i].CooldownUntil,
						"cooldown_reason":      endpoints[i].CooldownReason,
						"cooldown_status_code": endpoints[i].CooldownStatusCode,
						"updated_at":           time.Now().UTC(),
					},
				})
			}
		}
	}
	return endpoints, nil
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func healthSeverity(status models.HealthStatus) int {
	switch status {
	case models.HealthUnhealthy:
		return 3
	case models.HealthRateLimited:
		return 2
	case models.HealthCoolingDown:
		return 1
	default:
		return 0
	}
}

func deriveProviderHealth(providers []models.Provider, endpoints []models.Endpoint) {
	byProvider := make(map[uint]models.HealthStatus)
	for _, endpoint := range endpoints {
		current := byProvider[endpoint.ProviderID]
		if healthSeverity(endpoint.HealthStatus) > healthSeverity(current) {
			byProvider[endpoint.ProviderID] = endpoint.HealthStatus
		}
	}
	for i := range providers {
		if status, ok := byProvider[providers[i].ID]; ok {
			providers[i].HealthStatus = status
			continue
		}
		if providers[i].HealthStatus != models.HealthUnhealthy {
			providers[i].HealthStatus = models.HealthHealthy
		}
	}
}
