package admin

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/labstack/echo/v5"
)

type UsageSeriesPoint struct {
	Label       string    `json:"label"`
	BucketStart time.Time `json:"bucket_start"`
	BucketEnd   time.Time `json:"bucket_end"`
	Value       int64     `json:"value"`
	Requests    int64     `json:"requests"`
	TokensUp    int64     `json:"tokens_up"`
	TokensDown  int64     `json:"tokens_down"`
	CostMicros  int64     `json:"cost_micros"`
}

type UsageSeriesResponse struct {
	Metric string             `json:"metric"`
	Window string             `json:"window"`
	Points []UsageSeriesPoint `json:"points"`
}

type UsageAnalyticsTotals struct {
	Requests   int64 `json:"requests"`
	TokensIn   int64 `json:"tokens_in"`
	TokensDown int64 `json:"tokens_down"`
	CostMicros int64 `json:"cost_micros"`
}

type UsageAnalyticsRow struct {
	ProviderID   string `json:"provider_id"`
	EndpointID   string `json:"endpoint_id"`
	ProviderName string `json:"provider_name"`
	ModelName    string `json:"model_name"`
	Requests     int64  `json:"requests"`
	TokensIn     int64  `json:"tokens_in"`
	TokensDown   int64  `json:"tokens_down"`
	CostMicros   int64  `json:"cost_micros"`
}

type UsageAnalyticsResponse struct {
	StartDate             string               `json:"start_date"`
	EndDate               string               `json:"end_date"`
	Metric                string               `json:"metric"`
	Bucket                string               `json:"bucket"`
	TimezoneOffsetMinutes int                  `json:"timezone_offset_minutes"`
	Series                []UsageSeriesPoint   `json:"series"`
	Totals                UsageAnalyticsTotals `json:"totals"`
	Rows                  []UsageAnalyticsRow  `json:"rows"`
}

type CharacterizationIntentAnalyticsItem struct {
	PrimaryAction string  `json:"primary_action"`
	Value         int64   `json:"value"`
	Percentage    float64 `json:"percentage"`
}

type CharacterizationIntentAnalyticsResponse struct {
	StartDate             string                                `json:"start_date"`
	EndDate               string                                `json:"end_date"`
	Metric                string                                `json:"metric"`
	TimezoneOffsetMinutes int                                   `json:"timezone_offset_minutes"`
	Total                 int64                                 `json:"total"`
	Items                 []CharacterizationIntentAnalyticsItem `json:"items"`
}

type usageAnalyticsBucket string

const (
	usageAnalyticsBucketMinute usageAnalyticsBucket = "minute"
	usageAnalyticsBucketHour   usageAnalyticsBucket = "hour"
	usageAnalyticsBucketDay    usageAnalyticsBucket = "day"
	usageAnalyticsBucketWeek   usageAnalyticsBucket = "week"
	usageAnalyticsBucketMonth  usageAnalyticsBucket = "month"
)

const maxUsageAnalyticsBuckets = 2000

func (a *API) usageSeries(c *echo.Context) error {
	metric := models.Metric(c.QueryParam("metric"))
	switch metric {
	case models.MetricRequests, models.MetricTokens, models.MetricSpend:
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "metric must be requests, tokens, or spend")
	}

	window := c.QueryParam("window")
	if window == "" {
		window = "day"
	}

	now := time.Now().UTC()
	var bucketSize time.Duration
	var bucketCount int
	var labelFor func(time.Time) string

	switch window {
	case "hour":
		bucketSize = time.Minute
		bucketCount = 60
		labelFor = func(t time.Time) string { return t.Format("15:04") }
	case "day":
		bucketSize = time.Hour
		bucketCount = 24
		labelFor = func(t time.Time) string { return t.Format("15:00") }
	case "week":
		bucketSize = 24 * time.Hour
		bucketCount = 7
		labelFor = func(t time.Time) string { return t.Format("Mon") }
	case "month":
		bucketSize = 24 * time.Hour
		bucketCount = 30
		labelFor = func(t time.Time) string { return t.Format("Jan 02") }
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "window must be hour, day, week, or month")
	}

	bucketStart := now.Truncate(bucketSize).Add(-time.Duration(bucketCount-1) * bucketSize)
	bucketEnd := bucketStart.Add(time.Duration(bucketCount) * bucketSize)

	points := make([]UsageSeriesPoint, bucketCount)
	for i := 0; i < bucketCount; i++ {
		start := bucketStart.Add(time.Duration(i) * bucketSize)
		end := start.Add(bucketSize)
		points[i] = UsageSeriesPoint{
			Label:       labelFor(start),
			BucketStart: start,
			BucketEnd:   end,
			Value:       0,
		}
	}

	states := usageSeriesStates(metric)
	queryScopes, err := a.queryScopes(c, "usage_series")
	if err != nil {
		return err
	}

	query := a.store.DB().WithContext(c.Request().Context()).
		Model(&models.RequestLog{}).
		Select(usageRequestLogColumns()).
		Scopes(queryScopes...).
		Where("task_state IN ?", states).
		Where(
			"((started_at >= ? AND started_at < ?) OR (queued_at >= ? AND queued_at < ?) OR (finished_at >= ? AND finished_at < ?) OR (created_at >= ? AND created_at < ?))",
			bucketStart, bucketEnd,
			bucketStart, bucketEnd,
			bucketStart, bucketEnd,
			bucketStart, bucketEnd,
		).
		Order("coalesce(started_at, queued_at, finished_at, created_at) asc, id asc")
	if err := streamRequestLogs(query, func(log models.RequestLog) error {
		if !usageSeriesIncludesLog(log, metric) {
			return nil
		}
		at := usageSeriesTime(log)
		if at.IsZero() {
			return nil
		}
		index := int(at.Sub(bucketStart) / bucketSize)
		if index < 0 || index >= len(points) {
			return nil
		}
		points[index].Value += usageSeriesMetricValue(log, metric)
		return nil
	}); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, UsageSeriesResponse{
		Metric: string(metric),
		Window: window,
		Points: points,
	})
}

func (a *API) usageAnalytics(c *echo.Context) error {
	metric := models.Metric(c.QueryParam("metric"))
	switch metric {
	case models.MetricRequests, models.MetricTokens, models.MetricSpend:
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "metric must be requests, tokens, or spend")
	}

	bucket, err := parseUsageAnalyticsBucket(c.QueryParam("bucket"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bucket must be minute, hour, day, week, or month")
	}

	timezoneOffsetMinutes, err := parseTimezoneOffsetMinutes(c.QueryParam("tz_offset_minutes"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "tz_offset_minutes must be a valid integer between -1440 and 1440")
	}
	startDateValue := c.QueryParam("start_date")
	endDateValue := c.QueryParam("end_date")
	if startDateValue == "" && endDateValue == "" {
		if legacyDate := c.QueryParam("date"); legacyDate != "" {
			startDateValue = legacyDate
			endDateValue = legacyDate
		}
	}
	startDateValue, endDateValue, rangeStart, rangeEnd, err := usageAnalyticsSelectedRange(startDateValue, endDateValue, timezoneOffsetMinutes, time.Now().UTC())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "start_date and end_date must be in YYYY-MM-DD format")
	}
	queryRangeStart := rangeStart
	queryRangeEnd := rangeEnd
	zoomStartValue := c.QueryParam("range_start")
	zoomEndValue := c.QueryParam("range_end")
	if zoomStartValue != "" || zoomEndValue != "" {
		if zoomStartValue == "" || zoomEndValue == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "range_start and range_end must be provided together")
		}
		zoomStart, startErr := time.Parse(time.RFC3339Nano, zoomStartValue)
		zoomEnd, endErr := time.Parse(time.RFC3339Nano, zoomEndValue)
		if startErr != nil || endErr != nil || !zoomStart.Before(zoomEnd) {
			return echo.NewHTTPError(http.StatusBadRequest, "range_start and range_end must be valid RFC3339 timestamps")
		}
		zoomStart = zoomStart.UTC()
		zoomEnd = zoomEnd.UTC()
		if zoomStart.Before(rangeStart) || zoomEnd.After(rangeEnd) {
			return echo.NewHTTPError(http.StatusBadRequest, "zoom range must be within the selected date range")
		}
		queryRangeStart = zoomStart
		queryRangeEnd = zoomEnd
	}

	points, err := usageAnalyticsPoints(rangeStart, rangeEnd, timezoneOffsetMinutes, bucket)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	providers, err := a.store.ListProviders(c.Request().Context())
	if err != nil {
		return err
	}
	endpoints, err := a.store.ListEndpoints(c.Request().Context())
	if err != nil {
		return err
	}

	providerByID := make(map[uint]models.Provider, len(providers))
	for _, provider := range providers {
		providerByID[provider.ID] = provider
	}
	endpointByID := make(map[uint]models.Endpoint, len(endpoints))
	for _, endpoint := range endpoints {
		endpointByID[endpoint.ID] = endpoint
	}

	queryScopes, err := a.queryScopes(c, "usage_analytics")
	if err != nil {
		return err
	}

	query := a.store.DB().WithContext(c.Request().Context()).
		Model(&models.RequestLog{}).
		Select(usageRequestLogColumns()).
		Scopes(queryScopes...).
		Where("task_state IN ?", usageSeriesStates(models.MetricRequests)).
		Where(
			"((started_at >= ? AND started_at < ?) OR (queued_at >= ? AND queued_at < ?) OR (finished_at >= ? AND finished_at < ?) OR (created_at >= ? AND created_at < ?))",
			queryRangeStart, queryRangeEnd,
			queryRangeStart, queryRangeEnd,
			queryRangeStart, queryRangeEnd,
			queryRangeStart, queryRangeEnd,
		).
		Order("coalesce(started_at, queued_at, finished_at, created_at) asc, id asc")

	rowsByKey := make(map[string]*UsageAnalyticsRow)
	totals := UsageAnalyticsTotals{}

	if err := streamRequestLogs(query, func(log models.RequestLog) error {
		at := usageSeriesTime(log)
		if at.IsZero() || at.Before(queryRangeStart) || !at.Before(queryRangeEnd) {
			return nil
		}

		if index := usageAnalyticsPointIndex(at, points); index >= 0 {
			point := &points[index]
			if usageSeriesIncludesLog(log, models.MetricRequests) {
				point.Requests++
			}
			if usageSeriesIncludesLog(log, models.MetricTokens) {
				inputTokens, outputTokens := usageAnalyticsTokenValues(log)
				point.TokensUp += inputTokens
				point.TokensDown += outputTokens
			}
			if usageSeriesIncludesLog(log, models.MetricSpend) {
				point.CostMicros += usageAnalyticsCostMicros(log)
			}
			if usageSeriesIncludesLog(log, metric) {
				point.Value += usageSeriesMetricValue(log, metric)
			}
		}

		rowKey := ""
		rowProviderID := uint(0)
		rowProviderUUID := ""
		rowEndpointUUID := ""
		providerName := ""
		modelName := log.SelectedUpstreamModel

		if log.EndpointID != nil {
			if endpoint, ok := endpointByID[*log.EndpointID]; ok {
				rowKey = "endpoint:" + endpoint.UUID
				rowProviderID = endpoint.ProviderID
				rowEndpointUUID = endpoint.UUID
				modelName = usageAnalyticsModelName(endpoint)
				if provider, exists := providerByID[endpoint.ProviderID]; exists && provider.Name != "" {
					providerName = provider.Name
					rowProviderUUID = provider.UUID
				}
			}
		}
		if rowKey == "" {
			if log.ProviderID != nil {
				rowProviderID = *log.ProviderID
				if provider, exists := providerByID[rowProviderID]; exists && provider.Name != "" {
					providerName = provider.Name
					rowProviderUUID = provider.UUID
				}
			}
			if modelName == "" {
				modelName = log.IncomingModel
			}
			if modelName == "" {
				modelName = "Unknown model"
			}
			rowKey = "fallback:" + rowProviderUUID + ":" + modelName
		}

		row, ok := rowsByKey[rowKey]
		if !ok {
			row = &UsageAnalyticsRow{
				ProviderID:   rowProviderUUID,
				EndpointID:   rowEndpointUUID,
				ProviderName: providerName,
				ModelName:    modelName,
			}
			rowsByKey[rowKey] = row
		}

		if usageSeriesIncludesLog(log, models.MetricRequests) {
			row.Requests++
			totals.Requests++
		}
		if usageSeriesIncludesLog(log, models.MetricTokens) {
			inputTokens, outputTokens := usageAnalyticsTokenValues(log)
			row.TokensIn += inputTokens
			row.TokensDown += outputTokens
			totals.TokensIn += inputTokens
			totals.TokensDown += outputTokens
		}
		if usageSeriesIncludesLog(log, models.MetricSpend) {
			costMicros := usageAnalyticsCostMicros(log)
			row.CostMicros += costMicros
			totals.CostMicros += costMicros
		}
		return nil
	}); err != nil {
		return err
	}

	rows := make([]UsageAnalyticsRow, 0, len(rowsByKey))
	for _, row := range rowsByKey {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CostMicros != rows[j].CostMicros {
			return rows[i].CostMicros > rows[j].CostMicros
		}
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		if rows[i].TokensIn != rows[j].TokensIn {
			return rows[i].TokensIn > rows[j].TokensIn
		}
		if rows[i].ProviderName != rows[j].ProviderName {
			return rows[i].ProviderName < rows[j].ProviderName
		}
		return rows[i].ModelName < rows[j].ModelName
	})

	return c.JSON(http.StatusOK, UsageAnalyticsResponse{
		StartDate:             startDateValue,
		EndDate:               endDateValue,
		Metric:                string(metric),
		Bucket:                string(bucket),
		TimezoneOffsetMinutes: timezoneOffsetMinutes,
		Series:                points,
		Totals:                totals,
		Rows:                  rows,
	})
}

func (a *API) characterizationIntentAnalytics(c *echo.Context) error {
	metric := models.Metric(c.QueryParam("metric"))
	if metric == "" {
		metric = models.MetricRequests
	}
	switch metric {
	case models.MetricRequests, models.MetricTokens, models.MetricSpend:
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "metric must be requests, tokens, or spend")
	}

	timezoneOffsetMinutes, err := parseTimezoneOffsetMinutes(c.QueryParam("tz_offset_minutes"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "tz_offset_minutes must be a valid integer between -1440 and 1440")
	}

	startDateValue, endDateValue, rangeStart, rangeEnd, err := usageAnalyticsSelectedRange(
		c.QueryParam("start_date"),
		c.QueryParam("end_date"),
		timezoneOffsetMinutes,
		time.Now().UTC(),
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "start_date and end_date must be in YYYY-MM-DD format")
	}

	queryRangeStart := rangeStart
	queryRangeEnd := rangeEnd
	zoomStartValue := c.QueryParam("range_start")
	zoomEndValue := c.QueryParam("range_end")
	if zoomStartValue != "" || zoomEndValue != "" {
		if zoomStartValue == "" || zoomEndValue == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "range_start and range_end must be provided together")
		}
		zoomStart, startErr := time.Parse(time.RFC3339Nano, zoomStartValue)
		zoomEnd, endErr := time.Parse(time.RFC3339Nano, zoomEndValue)
		if startErr != nil || endErr != nil || !zoomStart.Before(zoomEnd) {
			return echo.NewHTTPError(http.StatusBadRequest, "range_start and range_end must be valid RFC3339 timestamps")
		}
		zoomStart = zoomStart.UTC()
		zoomEnd = zoomEnd.UTC()
		if zoomStart.Before(rangeStart) || zoomEnd.After(rangeEnd) {
			return echo.NewHTTPError(http.StatusBadRequest, "zoom range must be within the selected date range")
		}
		queryRangeStart = zoomStart
		queryRangeEnd = zoomEnd
	}

	queryScopes, err := a.queryScopes(c, "usage_analytics")
	if err != nil {
		return err
	}

	columns := append(usageRequestLogColumns(), "primary_action")
	query := a.store.DB().WithContext(c.Request().Context()).
		Model(&models.RequestLog{}).
		Select(columns).
		Scopes(queryScopes...).
		Where("task_state IN ?", usageSeriesStates(models.MetricRequests)).
		Where("COALESCE(started_at, queued_at, finished_at, created_at) >= ?", queryRangeStart).
		Where("COALESCE(started_at, queued_at, finished_at, created_at) < ?", queryRangeEnd).
		Where("primary_action IS NOT NULL AND TRIM(primary_action) <> ''").
		Order("COALESCE(started_at, queued_at, finished_at, created_at) ASC, id ASC")

	valuesByAction := make(map[string]int64)
	if err := streamRequestLogs(query, func(log models.RequestLog) error {
		if !usageSeriesIncludesLog(log, metric) || log.PrimaryAction == nil {
			return nil
		}
		action := strings.ToLower(strings.TrimSpace(*log.PrimaryAction))
		if action == "" {
			return nil
		}
		value := usageSeriesMetricValue(log, metric)
		if value > 0 {
			valuesByAction[action] += value
		}
		return nil
	}); err != nil {
		return err
	}

	var total int64
	for _, value := range valuesByAction {
		total += value
	}
	items := make([]CharacterizationIntentAnalyticsItem, 0, len(valuesByAction))
	for action, value := range valuesByAction {
		percentage := float64(0)
		if total > 0 {
			percentage = float64(value) * 100 / float64(total)
		}
		items = append(items, CharacterizationIntentAnalyticsItem{
			PrimaryAction: action,
			Value:         value,
			Percentage:    percentage,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Value != items[j].Value {
			return items[i].Value > items[j].Value
		}
		return items[i].PrimaryAction < items[j].PrimaryAction
	})

	return c.JSON(http.StatusOK, CharacterizationIntentAnalyticsResponse{
		StartDate:             startDateValue,
		EndDate:               endDateValue,
		Metric:                string(metric),
		TimezoneOffsetMinutes: timezoneOffsetMinutes,
		Total:                 total,
		Items:                 items,
	})
}

func parseTimezoneOffsetMinutes(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if value < -1440 || value > 1440 {
		return 0, errors.New("timezone offset out of range")
	}
	return value, nil
}

func parseUsageAnalyticsBucket(raw string) (usageAnalyticsBucket, error) {
	switch usageAnalyticsBucket(raw) {
	case "":
		return usageAnalyticsBucketHour, nil
	case usageAnalyticsBucketMinute, usageAnalyticsBucketHour, usageAnalyticsBucketDay, usageAnalyticsBucketWeek, usageAnalyticsBucketMonth:
		return usageAnalyticsBucket(raw), nil
	default:
		return "", errors.New("invalid analytics bucket")
	}
}

func usageAnalyticsSelectedRange(startDateValue, endDateValue string, timezoneOffsetMinutes int, now time.Time) (string, string, time.Time, time.Time, error) {
	localToday := now.UTC().Add(-time.Duration(timezoneOffsetMinutes) * time.Minute).Format("2006-01-02")
	if startDateValue == "" && endDateValue == "" {
		startDateValue = localToday
		endDateValue = localToday
	} else if startDateValue == "" {
		startDateValue = endDateValue
	} else if endDateValue == "" {
		endDateValue = startDateValue
	}

	startLocal, err := time.Parse("2006-01-02", startDateValue)
	if err != nil {
		return "", "", time.Time{}, time.Time{}, err
	}
	endLocal, err := time.Parse("2006-01-02", endDateValue)
	if err != nil {
		return "", "", time.Time{}, time.Time{}, err
	}
	if endLocal.Before(startLocal) {
		startLocal, endLocal = endLocal, startLocal
		startDateValue = startLocal.Format("2006-01-02")
		endDateValue = endLocal.Format("2006-01-02")
	}

	start := time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), 0, 0, 0, 0, time.UTC).
		Add(time.Duration(timezoneOffsetMinutes) * time.Minute)
	end := time.Date(endLocal.Year(), endLocal.Month(), endLocal.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, 1).
		Add(time.Duration(timezoneOffsetMinutes) * time.Minute)
	return startDateValue, endDateValue, start, end, nil
}

func usageAnalyticsPoints(rangeStart, rangeEnd time.Time, timezoneOffsetMinutes int, bucket usageAnalyticsBucket) ([]UsageSeriesPoint, error) {
	offset := time.Duration(timezoneOffsetMinutes) * time.Minute
	localCursor := rangeStart.Add(-offset)
	localEnd := rangeEnd.Add(-offset)
	points := make([]UsageSeriesPoint, 0, 96)

	for localCursor.Before(localEnd) {
		nextLocal := usageAnalyticsNextBucket(localCursor, bucket)
		if !nextLocal.After(localCursor) {
			return nil, errors.New("invalid analytics bucket")
		}
		if nextLocal.After(localEnd) {
			nextLocal = localEnd
		}
		if len(points) >= maxUsageAnalyticsBuckets {
			return nil, errors.New("selected range creates too many buckets; choose a shorter range or coarser bucket")
		}
		points = append(points, UsageSeriesPoint{
			Label:       usageAnalyticsBucketLabel(localCursor, nextLocal, bucket),
			BucketStart: localCursor.Add(offset),
			BucketEnd:   nextLocal.Add(offset),
			Value:       0,
		})
		localCursor = nextLocal
	}

	return points, nil
}

func usageAnalyticsNextBucket(localStart time.Time, bucket usageAnalyticsBucket) time.Time {
	switch bucket {
	case usageAnalyticsBucketMinute:
		return localStart.Add(time.Minute)
	case usageAnalyticsBucketHour:
		return localStart.Add(time.Hour)
	case usageAnalyticsBucketDay:
		return localStart.AddDate(0, 0, 1)
	case usageAnalyticsBucketWeek:
		return localStart.AddDate(0, 0, 7)
	case usageAnalyticsBucketMonth:
		return localStart.AddDate(0, 1, 0)
	default:
		return localStart
	}
}

func usageAnalyticsBucketLabel(startLocal, endLocal time.Time, bucket usageAnalyticsBucket) string {
	switch bucket {
	case usageAnalyticsBucketMinute:
		return startLocal.Format("2006-01-02 15:04")
	case usageAnalyticsBucketHour:
		return startLocal.Format("2006-01-02 15:00")
	case usageAnalyticsBucketDay:
		return startLocal.Format("2006-01-02")
	case usageAnalyticsBucketWeek, usageAnalyticsBucketMonth:
		inclusiveEnd := endLocal.Add(-time.Nanosecond)
		startLabel := startLocal.Format("2006-01-02")
		endLabel := inclusiveEnd.Format("2006-01-02")
		if startLabel == endLabel {
			return startLabel
		}
		return startLabel + " - " + endLabel
	default:
		return startLocal.Format("2006-01-02")
	}
}

func usageAnalyticsPointIndex(at time.Time, points []UsageSeriesPoint) int {
	for i, point := range points {
		if (at.Equal(point.BucketStart) || at.After(point.BucketStart)) && at.Before(point.BucketEnd) {
			return i
		}
	}
	return -1
}

func usageAnalyticsModelName(endpoint models.Endpoint) string {
	if endpoint.UpstreamModel != "" {
		return endpoint.UpstreamModel
	}
	if endpoint.Name != "" {
		return endpoint.Name
	}
	return "Unknown model"
}
