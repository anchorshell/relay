package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/store"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type RecentModelUsageResponse struct {
	Items []store.RecentModelUsage `json:"items"`
}

func (a *API) summary(c *echo.Context) error {
	stats, err := a.store.Summary(c.Request().Context())
	if err != nil {
		return err
	}
	queue, err := a.scopedQueueSnapshot(c)
	if err != nil {
		return err
	}
	stats["queue"] = queue
	return c.JSON(http.StatusOK, stats)
}

func (a *API) usageStats(c *echo.Context) error {
	ctx := c.Request().Context()
	providers, err := a.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	endpoints, err := a.store.ListEndpoints(ctx)
	if err != nil {
		return err
	}
	lanes, err := a.store.ListLanes(ctx)
	if err != nil {
		return err
	}

	scopes := make([]limits.ScopeRef, 0, 1+len(providers)+len(endpoints)+len(lanes))
	scopes = append(scopes, limits.ScopeRef{Type: models.ScopeGlobal, ID: 0, Key: "global"})
	for _, provider := range providers {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeProvider, ID: provider.ID, Key: provider.UUID})
	}
	for _, endpoint := range endpoints {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID, Key: endpoint.UUID})
	}
	for _, lane := range lanes {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeLane, ID: lane.ID, Key: lane.UUID})
	}

	metrics := []models.Metric{
		models.MetricRequests,
		models.MetricTokens,
	}
	summaries, err := a.currentRequestLogUsage(ctx, scopes, metrics, time.Now().UTC())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, summaries)
}

type requestLogUsageKey struct {
	scopeType models.ScopeType
	scopeID   uint
	metric    models.Metric
	period    models.Period
}

func (a *API) currentRequestLogUsage(ctx context.Context, scopes []limits.ScopeRef, metrics []models.Metric, now time.Time) ([]models.UsageSummary, error) {
	periods := []models.Period{
		models.PeriodSecond,
		models.PeriodMinute,
		models.PeriodHour,
		models.PeriodDay,
		models.PeriodMonth,
	}
	durations := map[models.Period]time.Duration{
		models.PeriodSecond: time.Second,
		models.PeriodMinute: time.Minute,
		models.PeriodHour:   time.Hour,
		models.PeriodDay:    24 * time.Hour,
		models.PeriodMonth:  30 * 24 * time.Hour,
	}
	now = now.UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	query := a.store.DB().WithContext(ctx).
		Model(&models.RequestLog{}).
		Select([]string{
			"lane_id", "endpoint_id", "provider_id", "status_code", "task_state",
			"started_at", "queued_at", "created_at",
			"estimated_input_tokens", "estimated_output_tokens",
			"actual_input_tokens", "actual_output_tokens", "actual_total_tokens",
			"estimated_cost_micros", "actual_cost_micros",
		}).
		Where("task_state IN ?", usageSeriesStates(models.MetricRequests)).
		Where("(started_at >= ? OR (started_at IS NULL AND queued_at >= ?) OR (started_at IS NULL AND queued_at IS NULL AND created_at >= ?))", cutoff, cutoff, cutoff).
		Order("id ASC")

	values := make(map[requestLogUsageKey]int64, len(scopes)*len(metrics)*len(periods))
	err := streamRequestLogs(query, func(log models.RequestLog) error {
		if !models.RequestLogCountsAsUsage(log) {
			return nil
		}
		at := usageSeriesTime(log)
		if at.IsZero() || at.After(now) {
			return nil
		}
		logScopes := []limits.ScopeRef{{Type: models.ScopeGlobal, ID: 0}}
		if log.ProviderID != nil {
			logScopes = append(logScopes, limits.ScopeRef{Type: models.ScopeProvider, ID: *log.ProviderID})
		}
		if log.EndpointID != nil {
			logScopes = append(logScopes, limits.ScopeRef{Type: models.ScopeEndpoint, ID: *log.EndpointID})
		}
		if log.LaneID != nil {
			logScopes = append(logScopes, limits.ScopeRef{Type: models.ScopeLane, ID: *log.LaneID})
		}
		for _, metric := range metrics {
			value := usageSeriesMetricValue(log, metric)
			if value <= 0 {
				continue
			}
			for _, period := range periods {
				if at.Before(now.Add(-durations[period])) {
					continue
				}
				for _, scope := range logScopes {
					values[requestLogUsageKey{scopeType: scope.Type, scopeID: scope.ID, metric: metric, period: period}] += value
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]models.UsageSummary, 0, len(scopes)*len(metrics)*len(periods))
	for _, scope := range scopes {
		for _, metric := range metrics {
			for _, period := range periods {
				duration := durations[period]
				out = append(out, models.UsageSummary{
					ScopeType:   scope.Type,
					ScopeUUID:   scope.Key,
					Metric:      metric,
					Period:      period,
					WindowStart: now.Truncate(duration),
					UsedValue:   values[requestLogUsageKey{scopeType: scope.Type, scopeID: scope.ID, metric: metric, period: period}],
					UpdatedAt:   now,
				})
			}
		}
	}
	return out, nil
}

func usageRequestLogColumns() []string {
	return []string{
		"id", "lane_id", "endpoint_id", "provider_id", "route_kind", "incoming_model", "selected_upstream_model",
		"status_code", "task_state", "queued_at", "started_at", "finished_at", "created_at",
		"estimated_input_tokens", "estimated_output_tokens", "actual_input_tokens", "actual_output_tokens", "actual_total_tokens",
		"estimated_cost_micros", "actual_cost_micros",
	}
}

func streamRequestLogs(query *gorm.DB, consume func(models.RequestLog) error) error {
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var log models.RequestLog
		if err := query.ScanRows(rows, &log); err != nil {
			return err
		}
		if err := consume(log); err != nil {
			return err
		}
	}
	return rows.Err()
}

func usageSeriesStates(metric models.Metric) []string {
	return []string{"completed", "failed", "cancelled"}
}

func usageSeriesIncludesLog(log models.RequestLog, metric models.Metric) bool {
	return models.RequestLogCountsAsUsage(log)
}

func usageAnalyticsTokenValues(log models.RequestLog) (int64, int64) {
	if log.ActualTotalTokens > 0 && log.ActualInputTokens+log.ActualOutputTokens <= 0 {
		return 0, log.ActualTotalTokens
	}
	if log.ActualTotalTokens > 0 || log.ActualInputTokens > 0 || log.ActualOutputTokens > 0 {
		return log.ActualInputTokens, log.ActualOutputTokens
	}
	if !models.RequestLogSuccessfulUsage(log) {
		return 0, 0
	}
	return log.EstimatedInputTokens, log.EstimatedOutputTokens
}

func usageAnalyticsCostMicros(log models.RequestLog) int64 {
	if log.ActualCostMicros > 0 {
		return log.ActualCostMicros
	}
	if !models.RequestLogSuccessfulUsage(log) {
		return 0
	}
	return log.EstimatedCostMicros
}

func usageSeriesTime(log models.RequestLog) time.Time {
	switch {
	case log.StartedAt != nil && !log.StartedAt.IsZero():
		return log.StartedAt.UTC()
	case log.QueuedAt != nil && !log.QueuedAt.IsZero():
		return log.QueuedAt.UTC()
	case log.FinishedAt != nil && !log.FinishedAt.IsZero():
		return log.FinishedAt.UTC()
	case !log.CreatedAt.IsZero():
		return log.CreatedAt.UTC()
	default:
		return time.Time{}
	}
}

func usageSeriesMetricValue(log models.RequestLog, metric models.Metric) int64 {
	switch metric {
	case models.MetricRequests:
		return 1
	case models.MetricTokens:
		if log.ActualTotalTokens > 0 {
			return log.ActualTotalTokens
		}
		if log.ActualInputTokens > 0 || log.ActualOutputTokens > 0 {
			return log.ActualInputTokens + log.ActualOutputTokens
		}
		if !models.RequestLogSuccessfulUsage(log) {
			return 0
		}
		return log.EstimatedInputTokens + log.EstimatedOutputTokens
	case models.MetricSpend:
		if log.ActualCostMicros > 0 {
			return log.ActualCostMicros
		}
		if !models.RequestLogSuccessfulUsage(log) {
			return 0
		}
		return log.EstimatedCostMicros
	default:
		return 0
	}
}

func (a *API) spendStats(c *echo.Context) error {
	ctx := c.Request().Context()
	providers, err := a.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	endpoints, err := a.store.ListEndpoints(ctx)
	if err != nil {
		return err
	}
	lanes, err := a.store.ListLanes(ctx)
	if err != nil {
		return err
	}
	scopes := make([]limits.ScopeRef, 0, 1+len(providers)+len(endpoints)+len(lanes))
	scopes = append(scopes, limits.ScopeRef{Type: models.ScopeGlobal, Key: "global"})
	for _, provider := range providers {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeProvider, ID: provider.ID, Key: provider.UUID})
	}
	for _, endpoint := range endpoints {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeEndpoint, ID: endpoint.ID, Key: endpoint.UUID})
	}
	for _, lane := range lanes {
		scopes = append(scopes, limits.ScopeRef{Type: models.ScopeLane, ID: lane.ID, Key: lane.UUID})
	}
	summaries, err := a.currentRequestLogUsage(ctx, scopes, []models.Metric{models.MetricSpend}, time.Now().UTC())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, summaries)
}

func (a *API) recentModelUsage(c *echo.Context) error {
	queryScopes, err := a.queryScopes(c, "recent_model_usage")
	if err != nil {
		return err
	}
	limit := store.DefaultRecentModelUsageLimit
	if rawLimit := strings.TrimSpace(c.QueryParam("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be an integer")
		}
		limit = parsed
	}
	items, err := a.store.ListRecentModelUsageWithScopes(c.Request().Context(), limit, queryScopes)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, RecentModelUsageResponse{Items: items})
}
