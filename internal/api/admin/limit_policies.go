package admin

import (
	"context"
	"net/http"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/labstack/echo/v5"
)

func (a *API) createLimitPolicy(c *echo.Context) error {
	var item models.LimitPolicy
	if err := c.Bind(&item); err != nil {
		return err
	}
	item.ID = 0
	if err := a.store.Create(c.Request().Context(), &item); err != nil {
		return err
	}
	if err := a.refreshLimitPolicyRuntime(c.Request().Context(), item); err != nil {
		return err
	}
	if a.scheduler != nil {
		if err := a.scheduler.PublishLimitPolicyState(c.Request().Context(), item, false); err != nil {
			return err
		}
	}
	a.publishLimitPolicyChange("created", item)
	return c.JSON(http.StatusCreated, item)
}

func (a *API) updateLimitPolicy(c *echo.Context) error {
	var previous models.LimitPolicy
	if err := a.store.FindByUUID(c.Request().Context(), &previous, c.Param("id")); err != nil {
		return err
	}
	item := previous
	if err := c.Bind(&item); err != nil {
		return err
	}
	item.ID = previous.ID
	if err := a.store.Save(c.Request().Context(), &item); err != nil {
		return err
	}
	stateIncompatible := previous.ScopeType != item.ScopeType || previous.ScopeID != item.ScopeID || previous.Metric != item.Metric || previous.Period != item.Period
	if !item.Enabled || stateIncompatible {
		if err := a.store.DeleteLimitPolicyState(c.Request().Context(), item.ID); err != nil {
			return err
		}
	}
	if err := a.refreshLimitPolicyRuntime(c.Request().Context(), previous, item); err != nil {
		return err
	}
	if a.scheduler != nil {
		if stateIncompatible {
			if err := a.scheduler.PublishLimitPolicyState(c.Request().Context(), previous, true); err != nil {
				return err
			}
		}
		if err := a.scheduler.PublishLimitPolicyState(c.Request().Context(), item, !item.Enabled); err != nil {
			return err
		}
	}
	a.publishLimitPolicyChange("updated", item)
	return c.JSON(http.StatusOK, item)
}

func (a *API) deleteLimitPolicy(c *echo.Context) error {
	var previous models.LimitPolicy
	if err := a.store.FindByUUID(c.Request().Context(), &previous, c.Param("id")); err != nil {
		return err
	}
	if err := a.store.DeleteLimitPolicyState(c.Request().Context(), previous.ID); err != nil {
		return err
	}
	if err := a.store.DeleteByID(c.Request().Context(), &models.LimitPolicy{}, previous.ID); err != nil {
		return err
	}
	if err := a.refreshLimitPolicyRuntime(c.Request().Context(), previous); err != nil {
		return err
	}
	if a.scheduler != nil {
		if err := a.scheduler.PublishLimitPolicyState(c.Request().Context(), previous, true); err != nil {
			return err
		}
	}
	a.publishLimitPolicyChange("deleted", previous)
	return c.NoContent(http.StatusNoContent)
}

func (a *API) publishLimitPolicyChange(action string, policy models.LimitPolicy) {
	if a.telemetry == nil {
		return
	}
	eventType := "limit_policy_changed"
	if action == "deleted" {
		eventType = "limit_policy_deleted"
	}
	a.telemetry.Publish(telemetry.Event{
		Type:    eventType,
		Payload: limitPolicyPayload(action, policy),
	})
}

func limitPolicyPayload(action string, policy models.LimitPolicy) map[string]any {
	return map[string]any{
		"action":      action,
		"id":          policy.UUID,
		"scope_type":  policy.ScopeType,
		"scope_id":    policy.ScopeUUID,
		"metric":      policy.Metric,
		"period":      policy.Period,
		"limit_value": policy.LimitValue,
		"enabled":     policy.Enabled,
		"source":      policy.Source,
		"created_at":  policy.CreatedAt,
		"updated_at":  policy.UpdatedAt,
	}
}

func (a *API) refreshLimitPolicyRuntime(ctx context.Context, policies ...models.LimitPolicy) error {
	refs := make([]limits.ScopeRef, 0, len(policies))
	seen := make(map[limits.ScopeRef]bool)
	for _, policy := range policies {
		ref := limits.ScopeRef{Type: policy.ScopeType, ID: policy.ScopeID}
		if ref.Type == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil
	}
	if a.scheduler != nil {
		if err := a.scheduler.ReevaluateLimitScopes(ctx, refs...); err != nil {
			return err
		}
	}

	a.previewEngineMu.Lock()
	sessions := make([]*liveFlowPreviewEngineSession, 0, len(a.previewEngineSessions))
	for _, session := range a.previewEngineSessions {
		sessions = append(sessions, session)
	}
	a.previewEngineMu.Unlock()

	for _, session := range sessions {
		if schedulerInstance := session.currentScheduler(); schedulerInstance != nil {
			if err := schedulerInstance.ReevaluateLimitScopes(ctx, refs...); err != nil {
				return err
			}
		}
	}
	return nil
}
