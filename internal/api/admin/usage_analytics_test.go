package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

func TestUsageAnalyticsSelectedRangeUsesLocalCalendarDays(t *testing.T) {
	now := time.Date(2026, 4, 11, 18, 0, 0, 0, time.UTC)

	startDateValue, endDateValue, start, end, err := usageAnalyticsSelectedRange("2026-04-10", "2026-04-12", 360, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if startDateValue != "2026-04-10" || endDateValue != "2026-04-12" {
		t.Fatalf("expected echoed range, got %q -> %q", startDateValue, endDateValue)
	}

	expectedStart := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 4, 13, 6, 0, 0, 0, time.UTC)
	if !start.Equal(expectedStart) {
		t.Fatalf("expected UTC start %s, got %s", expectedStart, start)
	}
	if !end.Equal(expectedEnd) {
		t.Fatalf("expected UTC end %s, got %s", expectedEnd, end)
	}
}

func TestUsageAnalyticsSelectedRangeDefaultsToLocalToday(t *testing.T) {
	now := time.Date(2026, 4, 11, 3, 15, 0, 0, time.UTC)

	startDateValue, endDateValue, start, end, err := usageAnalyticsSelectedRange("", "", 360, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if startDateValue != "2026-04-10" || endDateValue != "2026-04-10" {
		t.Fatalf("expected local range 2026-04-10 -> 2026-04-10, got %q -> %q", startDateValue, endDateValue)
	}

	expectedStart := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 4, 11, 6, 0, 0, 0, time.UTC)
	if !start.Equal(expectedStart) || !end.Equal(expectedEnd) {
		t.Fatalf("expected [%s, %s), got [%s, %s)", expectedStart, expectedEnd, start, end)
	}
}

func TestUsageAnalyticsSelectedRangeSwapsReversedInputs(t *testing.T) {
	now := time.Date(2026, 4, 11, 18, 0, 0, 0, time.UTC)

	startDateValue, endDateValue, _, _, err := usageAnalyticsSelectedRange("2026-04-12", "2026-04-10", 360, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if startDateValue != "2026-04-10" || endDateValue != "2026-04-12" {
		t.Fatalf("expected range to be normalized, got %q -> %q", startDateValue, endDateValue)
	}
}

func TestUsageAnalyticsPointsForHourBucket(t *testing.T) {
	rangeStart := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 4, 11, 6, 0, 0, 0, time.UTC)

	points, err := usageAnalyticsPoints(rangeStart, rangeEnd, 360, usageAnalyticsBucketHour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 24 {
		t.Fatalf("expected 24 hourly buckets, got %d", len(points))
	}
	if points[0].Label != "2026-04-10 00:00" {
		t.Fatalf("expected first hourly label to be local midnight, got %q", points[0].Label)
	}
	if points[len(points)-1].Label != "2026-04-10 23:00" {
		t.Fatalf("expected final hourly label to be local 23:00, got %q", points[len(points)-1].Label)
	}
}

func TestUsageAnalyticsPointsForDayBucket(t *testing.T) {
	rangeStart := time.Date(2026, 4, 10, 6, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 4, 13, 6, 0, 0, 0, time.UTC)

	points, err := usageAnalyticsPoints(rangeStart, rangeEnd, 360, usageAnalyticsBucketDay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("expected 3 daily buckets, got %d", len(points))
	}
	if points[0].Label != "2026-04-10" || points[2].Label != "2026-04-12" {
		t.Fatalf("unexpected daily labels: %#v", points)
	}
}

func TestCharacterizationIntentAnalyticsGroupsScopedRequests(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{}).
		WithExternalQueryScopeHooks(func(_ *echo.Context, input QueryScopeInput) ([]func(*gorm.DB) *gorm.DB, error) {
			if input.Resource != "usage_analytics" {
				return nil, nil
			}
			return []func(*gorm.DB) *gorm.DB{
				func(db *gorm.DB) *gorm.DB {
					return db.Where("request_logs.request_id <> ?", "hidden-conversation")
				},
			}, nil
		})
	e := echo.New()
	api.Register(e)

	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	createLog := func(requestID, action string, tokens int64) {
		actionValue := action
		log := models.RequestLog{
			RequestID:         requestID,
			TaskState:         "completed",
			StatusCode:        http.StatusOK,
			PrimaryAction:     &actionValue,
			ActualInputTokens: tokens,
			QueuedAt:          &at,
			StartedAt:         &at,
			FinishedAt:        &at,
			CreatedAt:         at,
			UpdatedAt:         at,
		}
		if err := st.Create(ctx, &log); err != nil {
			t.Fatal(err)
		}
	}
	createLog("explain-1", "explain", 10)
	createLog("explain-2", "explain", 10)
	createLog("create-1", "create", 5)
	createLog("hidden-conversation", "conversation", 100)

	resp := adminRequest(t, e, http.MethodGet, "/api/stats/characterization-intents?start_date=2026-08-05&end_date=2026-08-05&tz_offset_minutes=0", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var body CharacterizationIntentAnalyticsResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 3 {
		t.Fatalf("expected 3 scoped characterized requests, got %d", body.Total)
	}
	if len(body.Items) != 2 {
		t.Fatalf("expected 2 intent groups, got %#v", body.Items)
	}
	if body.Metric != "requests" {
		t.Fatalf("expected requests metric, got %q", body.Metric)
	}
	if body.Items[0].PrimaryAction != "explain" || body.Items[0].Value != 2 {
		t.Fatalf("expected explain to be the largest group, got %#v", body.Items[0])
	}
	if body.Items[1].PrimaryAction != "create" || body.Items[1].Value != 1 {
		t.Fatalf("expected create group, got %#v", body.Items[1])
	}

	tokenResp := adminRequest(t, e, http.MethodGet, "/api/stats/characterization-intents?start_date=2026-08-05&end_date=2026-08-05&tz_offset_minutes=0&metric=tokens", "")
	if tokenResp.Code != http.StatusOK {
		t.Fatalf("expected token status 200, got %d: %s", tokenResp.Code, tokenResp.Body.String())
	}
	var tokenBody CharacterizationIntentAnalyticsResponse
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &tokenBody); err != nil {
		t.Fatal(err)
	}
	if tokenBody.Metric != "tokens" || tokenBody.Total != 25 {
		t.Fatalf("expected 25 scoped tokens, got metric=%q total=%d", tokenBody.Metric, tokenBody.Total)
	}
	if len(tokenBody.Items) != 2 || tokenBody.Items[0].PrimaryAction != "explain" || tokenBody.Items[0].Value != 20 {
		t.Fatalf("expected explain token group, got %#v", tokenBody.Items)
	}
}
