package admin

import (
	"context"
	"encoding/json"
	"github.com/anchorshell/relay/pkg/characterization"
	"net/http"

	"github.com/anchorshell/relay/internal/store"
	"github.com/labstack/echo/v5"
)

type SystemInfo struct {
	Laya                 characterization.WorkerStatus `json:"laya"`
	HTTPAddr             string                        `json:"http_addr"`
	DBPath               string                        `json:"db_path"`
	TempDir              string                        `json:"temp_dir"`
	LogLevel             string                        `json:"log_level"`
	InsecureDev          bool                          `json:"insecure_dev"`
	AdminTokenConfigured bool                          `json:"admin_token_configured"`
	MasterKeyConfigured  bool                          `json:"master_key_configured"`
}

var writableSettingKeys = map[string]struct{}{
	"characterization_engine":                    {},
	"default_max_wait_ms":                        {},
	"default_max_latency_ms":                     {},
	"max_queue_length":                           {},
	"max_queue_age_ms":                           {},
	"body_memory_threshold_bytes":                {},
	"body_spool_threshold_bytes":                 {},
	"insecure_dev_mode":                          {},
	"max_queued_estimated_spend_micros":          {},
	store.SettingReserveEstimatedTokensForLimits: {},
	store.SettingReserveEstimatedSpendForLimits:  {},
	"store_requests":                             {},
}

func (a *API) settings(c *echo.Context) error {
	items, err := a.store.GetSettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

func (a *API) system(c *echo.Context) error {
	info := a.systemInfo
	info.Laya = characterization.ConfiguredWorkerStatus(c.Request().Context())
	return c.JSON(http.StatusOK, info)
}

func (a *API) upsertSetting(c *echo.Context) error {
	key := c.Param("key")
	if _, ok := writableSettingKeys[key]; !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "unknown setting key")
	}
	var payload map[string]any
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return err
	}
	if key == "characterization_engine" {
		value, ok := payload["value"].(string)
		if !ok || !characterization.ValidEngine(characterization.EngineID(value)) {
			return echo.NewHTTPError(http.StatusBadRequest, "engine must be anchorshell or laya")
		}
	}
	if err := a.store.UpsertSetting(c.Request().Context(), key, payload["value"]); err != nil {
		return err
	}
	if isLimitReservationSetting(key) {
		if err := a.refreshLimitSettingsRuntime(c.Request().Context()); err != nil {
			return err
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) upsertSettings(c *echo.Context) error {
	var payload map[string]any
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return err
	}
	if len(payload) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no settings supplied")
	}
	refreshLimitRuntime := false
	for key := range payload {
		if key == "characterization_engine" {
			value, ok := payload[key].(string)
			if !ok || !characterization.ValidEngine(characterization.EngineID(value)) {
				return echo.NewHTTPError(http.StatusBadRequest, "engine must be anchorshell or laya")
			}
		}
		if _, ok := writableSettingKeys[key]; !ok {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown setting key")
		}
		refreshLimitRuntime = refreshLimitRuntime || isLimitReservationSetting(key)
	}
	if err := a.store.UpsertSettings(c.Request().Context(), payload); err != nil {
		return err
	}
	if refreshLimitRuntime {
		if err := a.refreshLimitSettingsRuntime(c.Request().Context()); err != nil {
			return err
		}
	}
	items, err := a.store.GetSettings(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, items)
}

func isLimitReservationSetting(key string) bool {
	return key == store.SettingReserveEstimatedTokensForLimits || key == store.SettingReserveEstimatedSpendForLimits
}

func (a *API) refreshLimitSettingsRuntime(ctx context.Context) error {
	if a.scheduler != nil {
		if err := a.scheduler.ReloadLimitSettingsAndReevaluate(ctx); err != nil {
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
			if err := schedulerInstance.ReloadLimitSettingsAndReevaluate(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
