package admin

import (
	"net/http"
	"strings"

	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/labstack/echo/v5"
)

func (a *API) queueStats(c *echo.Context) error {
	snap, err := a.scopedQueueSnapshot(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, snap)
}

func (a *API) queueItems(c *echo.Context) error {
	items, err := a.scopedQueueItems(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

func (a *API) cancelTask(c *echo.Context) error {
	if ok := a.scheduler.CancelQueued(c.Param("taskID")); !ok {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) deleteTask(c *echo.Context) error {
	if ok := a.scheduler.Delete(c.Param("taskID")); !ok {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	return c.NoContent(http.StatusNoContent)
}

const queueScopeContextKey = "anchorshell.admin.queue_scope"

type queueScopeResolution struct {
	scope ExternalQueueScope
	err   error
}

func (a *API) scopedQueueItems(c *echo.Context) ([]scheduler.TaskSnapshot, error) {
	return a.scopedQueueItemsForScheduler(c, a.scheduler)
}

func (a *API) scopedQueueItemsForScheduler(c *echo.Context, sch *scheduler.Scheduler) ([]scheduler.TaskSnapshot, error) {
	if sch == nil {
		return []scheduler.TaskSnapshot{}, nil
	}
	items := sch.Items()
	scope, err := a.queueScope(c)
	if err != nil {
		return nil, err
	}
	if scope.Deny {
		return []scheduler.TaskSnapshot{}, nil
	}
	if scope.OrganizationUUID == "" && scope.ActorID == "" {
		return items, nil
	}
	filtered := make([]scheduler.TaskSnapshot, 0, len(items))
	for _, item := range items {
		if queueItemVisibleToScope(scope, item) {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (a *API) scopedQueueSnapshot(c *echo.Context) (scheduler.Snapshot, error) {
	scope, err := a.queueScope(c)
	if err != nil {
		return scheduler.Snapshot{}, err
	}
	if scope.Deny {
		return snapshotFromQueueItems(nil), nil
	}
	if scope.OrganizationUUID == "" && scope.ActorID == "" {
		return a.scheduler.Snapshot(), nil
	}
	items := a.scheduler.Items()
	filtered := make([]scheduler.TaskSnapshot, 0, len(items))
	for _, item := range items {
		if queueItemVisibleToScope(scope, item) {
			filtered = append(filtered, item)
		}
	}
	return snapshotFromQueueItems(filtered), nil
}

func (a *API) queueScope(c *echo.Context) (ExternalQueueScope, error) {
	if cached, ok := c.Get(queueScopeContextKey).(queueScopeResolution); ok {
		return cached.scope, cached.err
	}
	scope, err := a.resolveQueueScope(c)
	c.Set(queueScopeContextKey, queueScopeResolution{scope: scope, err: err})
	return scope, err
}

func (a *API) resolveQueueScope(c *echo.Context) (ExternalQueueScope, error) {
	if len(a.externalQueueScopeHooks) == 0 {
		return ExternalQueueScope{}, nil
	}
	for _, hook := range a.externalQueueScopeHooks {
		if hook == nil {
			continue
		}
		scope, err := hook(c, QueueScopeInput{})
		if err != nil {
			return ExternalQueueScope{}, err
		}
		if scope.Deny {
			return ExternalQueueScope{Deny: true}, nil
		}
		scope.OrganizationUUID = strings.TrimSpace(scope.OrganizationUUID)
		scope.ActorID = strings.TrimSpace(scope.ActorID)
		if scope.OrganizationUUID != "" || scope.ActorID != "" {
			return scope, nil
		}
	}
	return ExternalQueueScope{}, nil
}

func queueItemVisibleToScope(scope ExternalQueueScope, item scheduler.TaskSnapshot) bool {
	if scope.OrganizationUUID != "" && !strings.EqualFold(strings.TrimSpace(item.OrganizationUUID), scope.OrganizationUUID) {
		return false
	}
	if scope.ActorID != "" && !strings.EqualFold(strings.TrimSpace(item.ActorID), scope.ActorID) {
		return false
	}
	return true
}

func snapshotFromQueueItems(items []scheduler.TaskSnapshot) scheduler.Snapshot {
	snap := scheduler.Snapshot{
		QueueDepthGlobal: len(items),
		QueueDepthByEP:   map[string]int{},
		InFlightByEP:     map[string]int64{},
		States:           map[string]int{},
	}
	for _, item := range items {
		if item.EndpointUUID != "" {
			snap.QueueDepthByEP[item.EndpointUUID]++
			if item.StartedAt != nil {
				snap.InFlightByEP[item.EndpointUUID]++
			}
		}
		snap.States[item.State]++
	}
	return snap
}
