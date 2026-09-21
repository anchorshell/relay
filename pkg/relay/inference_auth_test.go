package relay

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const inferenceTestToken = "test-inference-token-with-enough-length"
const managementTestToken = "test-admin-token-with-enough-length"

func TestInferenceRouteAuthentication(t *testing.T) {
	app := newTestApp(t, context.Background(), nil)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1/embeddings"},
		{http.MethodGet, "/v1/models"},
		{http.MethodPost, "/v1/dummy/chat/completions"},
	} {
		t.Run(route.path, func(t *testing.T) {
			for _, header := range []string{"", "Bearer wrong-token", "Basic " + inferenceTestToken, "Bearer " + managementTestToken, "Bearer", "Bearer " + inferenceTestToken + " extra"} {
				req := httptest.NewRequest(route.method, route.path+"?token="+inferenceTestToken, nil)
				req.Header.Set("Authorization", header)
				req.AddCookie(&http.Cookie{Name: "relay_api_token", Value: inferenceTestToken})
				body := &unreadInferenceBody{t: t}
				req.Body = body
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("expected private 401 challenge, got %d", rec.Code)
				}
				if strings.Contains(rec.Body.String(), inferenceTestToken) || strings.Contains(rec.Body.String(), managementTestToken) {
					t.Fatal("authentication error disclosed credentials")
				}
			}
			// Authenticated malformed bodies reach normal validation; model
			// listing succeeds. No upstream provider request can be made.
			req := httptest.NewRequest(route.method, route.path, strings.NewReader("{"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "bEaReR "+inferenceTestToken)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			want := http.StatusBadRequest
			if route.method == http.MethodGet {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("authenticated request: got %d, want %d", rec.Code, want)
			}
		})
	}
	// Multiple Authorization headers are ambiguous and must fail closed.
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Add("Authorization", "Bearer "+inferenceTestToken)
	req.Header.Add("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("duplicate credentials were accepted")
	}
}

type unreadInferenceBody struct{ t *testing.T }

func (b *unreadInferenceBody) Read([]byte) (int, error) {
	b.t.Fatal("unauthenticated request body was read")
	return 0, nil
}
func (*unreadInferenceBody) Close() error { return nil }

func TestInferenceAndManagementCredentialsAreSeparate(t *testing.T) {
	app := newTestApp(t, context.Background(), nil)
	for _, path := range []string{"/api/providers", "/api/settings", "/api/guardrails", "/api/ws"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+inferenceTestToken)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("inference token granted management access to %s", path)
		}
	}
	for _, token := range []string{inferenceTestToken, managementTestToken} {
		req := httptest.NewRequest(http.MethodPost, "/api/session", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if token == inferenceTestToken {
			if rec.Code != http.StatusUnauthorized {
				t.Fatal("inference token created an admin session")
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatal("management authentication changed")
		}
		cookie := rec.Result().Cookies()[0]
		for _, path := range []string{"/api/providers", "/v1/models"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			want := http.StatusOK
			if path == "/v1/models" {
				want = http.StatusUnauthorized
			}
			if rec.Code != want {
				t.Fatalf("session access at %s: got %d, want %d", path, rec.Code, want)
			}
		}
	}
	// Safe status endpoints must never disclose the configured API credential.
	for _, path := range []string{"/api/session", "/api/system", "/api/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+managementTestToken)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), inferenceTestToken) {
			t.Fatalf("unsafe configuration response at %s", path)
		}
	}
}

func TestInferenceExplicitOptOut(t *testing.T) {
	app := newTestAppWithAPIToken(t, context.Background(), nil, "")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit opt-out: got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatal("inference opt-out disabled management authentication")
	}
}

func TestAuthenticatedDummyResponseAndStreaming(t *testing.T) {
	app := newTestApp(t, context.Background(), nil)
	for _, stream := range []string{"false", "true"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/dummy/chat/completions", strings.NewReader(`{"model":"dummy","messages":[{"role":"user","content":"wait_0"}],"stream":`+stream+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+inferenceTestToken)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("dummy request: got %d", rec.Code)
		}
		if stream == "true" && (!strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), "[DONE]")) {
			t.Fatal("authenticated streaming response changed")
		}
	}
}

func TestInferenceCredentialsNotLogged(t *testing.T) {
	app := newTestApp(t, context.Background(), nil)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, token := range []string{inferenceTestToken, "wrong-supplied-secret"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models?token="+token, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		app.Handler().ServeHTTP(httptest.NewRecorder(), req)
		if strings.Contains(logs.String(), token) {
			t.Fatal("inference credential leaked into request logs")
		}
	}
}
