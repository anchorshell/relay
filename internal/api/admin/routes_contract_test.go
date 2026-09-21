package admin

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

// This is an independent inventory of the public contract, not generated at test
// time from Register. Changes require an intentional API compatibility review.
const adminRouteContract = `
POST /api/session
GET /api/session
DELETE /api/session
GET /api/ws
GET /api/system
GET /api/page-data/:page
GET /api/providers
POST /api/providers
PUT /api/providers/:id
DELETE /api/providers/:id
GET /api/credentials
POST /api/credentials
PUT /api/credentials/:id
DELETE /api/credentials/:id
GET /api/endpoints
POST /api/endpoints
PUT /api/endpoints/:id
DELETE /api/endpoints/:id
GET /api/routing-lanes
POST /api/routing-lanes
PUT /api/routing-lanes/:id
DELETE /api/routing-lanes/:id
GET /api/lane-memberships
POST /api/lane-memberships
PUT /api/lane-memberships/:id
DELETE /api/lane-memberships/:id
GET /api/limit-policies
POST /api/limit-policies
PUT /api/limit-policies/:id
DELETE /api/limit-policies/:id
GET /api/observed-limits
POST /api/observed-limits
PUT /api/observed-limits/:id
DELETE /api/observed-limits/:id
GET /api/pricing-policies
POST /api/pricing-policies
PUT /api/pricing-policies/:id
DELETE /api/pricing-policies/:id
GET /api/guardrail-presets
GET /api/guardrails
GET /api/guardrail-bindings
POST /api/guardrails
GET /api/guardrails/effective
POST /api/guardrails/import-curl
POST /api/guardrails/preview
GET /api/guardrails/:id
PUT /api/guardrails/:id
DELETE /api/guardrails/:id
GET /api/guardrails/:id/bindings
PUT /api/guardrails/:id/bindings
POST /api/guardrails/:id/test
GET /api/settings
PUT /api/settings
PUT /api/settings/:key
GET /api/stats/summary
GET /api/stats/queue
GET /api/capacity-snapshot
GET /api/queue/items
DELETE /api/queue/items/:taskID
GET /api/stats/usage
GET /api/stats/usage-series
GET /api/stats/usage-analytics
GET /api/stats/characterization-intents
GET /api/stats/recent-flow-activity
GET /api/stats/recent-model-usage
GET /api/stats/spend
GET /api/logs/requests
GET /api/logs/requests/:requestID
GET /api/logs/events
GET /api/health
GET /api/suggestions/ranks
POST /api/simulate/lane
POST /api/simulate/live-flow
POST /api/preview/live-flow/sessions
GET /api/preview/live-flow/sessions/:id/ws
POST /api/preview/live-flow/sessions/:id/enqueue
POST /api/preview/live-flow/sessions/:id/stop
POST /api/preview/live-flow/sessions/:id/reset
DELETE /api/preview/live-flow/sessions/:id
POST /api/queue/cancel/:taskID
POST /api/endpoints/:id/recompute-suggested-rank
POST /api/suggestions/recompute-all
`

func TestAdminRouteContract(t *testing.T) {
	e := echo.New()
	New(nil, nil, nil, nil, adminTestToken(), SystemInfo{}).Register(e)
	var got []string
	for _, route := range e.Router().Routes() {
		got = append(got, route.Method+" "+route.Path)
		var wantParams []string
		for _, part := range strings.Split(route.Path, "/") {
			if strings.HasPrefix(part, ":") {
				wantParams = append(wantParams, strings.TrimPrefix(part, ":"))
			}
		}
		if !slices.Equal(route.Parameters, wantParams) {
			t.Errorf("%s %s: parameters = %v, want %v", route.Method, route.Path, route.Parameters, wantParams)
		}
	}
	want := strings.Split(strings.TrimSpace(adminRouteContract), "\n")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("admin route contract changed:\ngot: %v\nwant: %v", got, want)
	}
}

func TestLegacyManagementRoutesAreNotRegistered(t *testing.T) {
	e := echo.New()
	New(nil, nil, nil, nil, adminTestToken(), SystemInfo{}).Register(e)
	for _, route := range strings.Split(strings.TrimSpace(adminRouteContract), "\n") {
		method, path, _ := strings.Cut(route, " ")
		// This historical prefix exists only to prove that no aliases survive.
		legacy := "/api/admin" + strings.TrimPrefix(path, "/api")
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest(method, legacy, nil)
			if authenticated {
				req.Header.Set("Authorization", "Bearer "+adminTestToken())
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s (authenticated=%t): got %d, want 404", method, legacy, authenticated, rec.Code)
			}
		}
	}
}

func TestAdminRouteAuthenticationContract(t *testing.T) {
	e := echo.New()
	New(nil, nil, nil, nil, adminTestToken(), SystemInfo{}).Register(e)
	for _, route := range strings.Split(strings.TrimSpace(adminRouteContract), "\n") {
		method, pattern, ok := strings.Cut(route, " ")
		if !ok {
			t.Fatalf("invalid route fixture %q", route)
		}
		parts := strings.Split(pattern, "/")
		for i, part := range parts {
			if strings.HasPrefix(part, ":") {
				parts[i] = "fixture"
			}
		}
		path := strings.Join(parts, "/")
		t.Run(route, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			want := http.StatusUnauthorized
			if method == http.MethodGet && pattern == "/api/session" {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("unauthenticated status = %d, want %d", rec.Code, want)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store header")
			}

			// Even a valid bearer token must not make query-token URLs acceptable.
			req := httptest.NewRequest(method, path+"?token=fixture", nil)
			req.Header.Set("Authorization", "Bearer "+adminTestToken())
			rec = httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("query-token status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestAdminSessionRoutesPreserveAuthenticationBoundaries(t *testing.T) {
	e := echo.New()
	api := New(nil, nil, nil, nil, adminTestToken(), SystemInfo{})
	api.Register(e)
	status := func(cookie *http.Cookie, want bool) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		expected := `"authenticated":false`
		if want {
			expected = `"authenticated":true`
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), expected) {
			t.Fatalf("session status did not report authenticated=%t", want)
		}
	}
	status(nil, false)
	login := adminRequest(t, e, http.MethodPost, "/api/session", "")
	if login.Code != http.StatusOK || strings.TrimSpace(login.Body.String()) != `{"ok":true}` {
		t.Fatal("session creation response changed")
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != "/" {
		t.Fatal("session cookie contract changed")
	}
	status(cookies[0], true)
	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatal("logout response changed")
	}
	status(cookies[0], false)

	// Extension authorization may report a session, but cannot mint an OSS
	// session cookie without the built-in bearer token.
	api.WithExternalAuthorizers(func(*echo.Context) bool { return true })
	status(nil, true)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/session", nil))
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatal("external authorizer was allowed to create a built-in session")
	}
}
