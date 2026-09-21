package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/scheduler"
	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func taskStateCounts(items []scheduler.TaskSnapshot) map[string]int {
	counts := make(map[string]int)
	for _, item := range items {
		counts[item.State]++
	}
	return counts
}

func receiveAdminPermit(t *testing.T, permits <-chan scheduler.Permit, errs <-chan error, timeout time.Duration) scheduler.Permit {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case permit := <-permits:
		return permit
	case err := <-errs:
		t.Fatal(err)
	case <-timer.C:
		t.Fatalf("timed out waiting for permit after %s", timeout)
	}
	return scheduler.Permit{}
}

func assertNoAdminPermit(t *testing.T, permits <-chan scheduler.Permit, errs <-chan error, wait time.Duration) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case permit := <-permits:
		t.Fatalf("unexpected permit before serial group released: %#v", permit)
	case err := <-errs:
		t.Fatal(err)
	case <-timer.C:
	}
}

func previewGenerationFromPayload(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	switch value := payload["preview_generation"].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		t.Fatalf("expected numeric preview_generation, got %#v", payload["preview_generation"])
		return 0
	}
}

func credentialTestAPI(t *testing.T) (*echo.Echo, *store.Store, *security.LocalSecretManager) {
	t.Helper()
	st := testutil.NewStore(t)
	secrets, err := security.NewLocalSecretManager(adminTestMasterKey(), st)
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, nil, nil, secrets, adminTestToken(), SystemInfo{})
	e := echo.New()
	api.Register(e)
	return e, st, secrets
}

func usageStatsTestAPI(t *testing.T) (*echo.Echo, *store.Store, *limits.Tracker, models.Endpoint) {
	t.Helper()
	ctx := context.Background()
	st := testutil.NewStore(t)
	tracker := limits.NewTracker(nil)
	resolver := limits.NewResolver(st, tracker)
	sch := scheduler.New(st, resolver, tracker, telemetry.NewHub(), nil, 30000, 1000, 300000)
	api := New(st, sch, nil, nil, adminTestToken(), SystemInfo{})

	provider := models.Provider{Name: "Dummy Provider", Slug: "dummy-usage-stats", BaseURL: "https://example.com", Enabled: true}
	if err := st.Create(ctx, &provider); err != nil {
		t.Fatal(err)
	}
	endpoint := models.Endpoint{
		ProviderID:    provider.ID,
		Name:          "dummy2",
		UpstreamModel: "dummy2",
		Enabled:       true,
		HealthStatus:  models.HealthHealthy,
	}
	if err := st.Create(ctx, &endpoint); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	api.Register(e)
	return e, st, tracker, endpoint
}

func usageStatsSummary(t *testing.T, e *echo.Echo, scopeUUID string, metric models.Metric, period models.Period) models.UsageSummary {
	t.Helper()
	resp := adminRequest(t, e, http.MethodGet, "/api/stats/usage", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected usage stats status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var summaries []models.UsageSummary
	if err := json.Unmarshal(resp.Body.Bytes(), &summaries); err != nil {
		t.Fatal(err)
	}
	for _, summary := range summaries {
		if summary.ScopeType == models.ScopeEndpoint && summary.ScopeUUID == scopeUUID && summary.Metric == metric && summary.Period == period {
			return summary
		}
	}
	t.Fatalf("expected endpoint usage summary scope=%s metric=%s period=%s in %s", scopeUUID, metric, period, resp.Body.String())
	return models.UsageSummary{}
}

func capacitySnapshotResponse(t *testing.T, e *echo.Echo) CapacitySnapshot {
	t.Helper()
	resp := adminRequest(t, e, http.MethodGet, "/api/capacity-snapshot", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected capacity snapshot status 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var snapshot CapacitySnapshot
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func waitForSchedulerTask(t *testing.T, sch *scheduler.Scheduler, requestID string, endpointUUID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, item := range sch.Items() {
			if item.RequestID == requestID && item.EndpointUUID == endpointUUID && (item.State == "waiting" || item.State == "ready") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for scheduler task request_id=%s endpoint=%s", requestID, endpointUUID)
}

func capacityModel(t *testing.T, snapshot CapacitySnapshot, endpointUUID string) ModelCapacitySnapshot {
	t.Helper()
	for _, model := range snapshot.Models {
		if model.EndpointID == endpointUUID {
			return model
		}
	}
	t.Fatalf("expected endpoint %s in capacity snapshot %#v", endpointUUID, snapshot)
	return ModelCapacitySnapshot{}
}

func capacityLimitRow(t *testing.T, model ModelCapacitySnapshot, metric models.Metric, period models.Period) ExternalCapacityRow {
	t.Helper()
	for _, row := range model.LimitRows {
		if row.Metric == string(metric) && row.Period == string(period) && !row.UserScoped {
			return row
		}
	}
	t.Fatalf("expected metric=%s period=%s in capacity rows %#v", metric, period, model.LimitRows)
	return ExternalCapacityRow{}
}

func adminRequest(t *testing.T, e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminTestToken())
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func assertNoCredentialLeak(t *testing.T, body string, sentinel string) {
	t.Helper()
	if strings.Contains(body, sentinel) {
		t.Fatalf("response leaked plaintext credential secret: %s", body)
	}
	if strings.Contains(body, "encrypted_secret") {
		t.Fatalf("response exposed encrypted_secret field: %s", body)
	}
	if strings.Contains(body, "relay:v1:aes-256-gcm:local:") {
		t.Fatalf("response leaked credential ciphertext envelope: %s", body)
	}
}

func adminTestToken() string {
	return "valid-admin-token-1234567890"
}

func adminTestMasterKey() string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(key)
}
