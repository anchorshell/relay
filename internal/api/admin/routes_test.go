package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminOversizedJSONRequestReturns413(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	body := `{"name":"` + strings.Repeat("x", int(adminJSONBodyLimitBytes)+1) + `"}`
	resp := adminRequest(t, e, http.MethodPost, "/api/providers", body)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected oversized admin request to return 413, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestAdminResponsesAreNoStoreAndDoNotAllowCredentialedWildcardCORS(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/credentials", nil)
	req.Header.Set("Authorization", "Bearer "+adminTestToken())
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected admin request status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", got)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") == "*" && strings.EqualFold(rec.Header().Get("Access-Control-Allow-Credentials"), "true") {
		t.Fatal("admin CORS allowed wildcard origin with credentials")
	}
}
