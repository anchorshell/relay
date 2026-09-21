package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/tenancy"
)

func TestAdminHTTPRegistrarUsesRealAdminMiddleware(t *testing.T) {
	ctx := context.Background()
	plain := newTestApp(t, ctx, nil)
	for _, path := range []string{"/api/extension-check/item", "/api/extension-public"} {
		rec := httptest.NewRecorder()
		plain.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unregistered extension route %s: status = %d", path, rec.Code)
		}
	}

	type response struct {
		ID     string `json:"id"`
		Query  string `json:"query"`
		Body   string `json:"body"`
		Tenant string `json:"tenant"`
	}
	var calls int
	handler := func(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
		calls++
		return OK(response{
			ID: req.Params["id"], Query: req.Query.Get("q"), Body: string(req.Body),
			Tenant: tenancy.OrganizationUUID(ctx),
		}), nil
	}
	app := newTestApp(t, ctx, []Extension{testExtension{
		name: "admin-http-contract",
		apply: func(hooks *Hooks) error {
			hooks.AdminAuthorizers = append(hooks.AdminAuthorizers, func(_ context.Context, input AdminAuthInput) (bool, error) {
				return input.Headers.Get("X-Test-Admin") == "allowed", nil
			})
			hooks.AdminTenantScopes = append(hooks.AdminTenantScopes, func(_ context.Context, input AdminTenantScopeInput) (TenantScope, error) {
				if input.Headers.Get("X-Test-Deny-Scope") != "" {
					return TenantScope{}, AdminTenantScopeRejection{Status: http.StatusForbidden, Message: "scope denied"}
				}
				return TenantScope{OrganizationUUID: "tenant-a", UserUUID: "actor-a"}, nil
			})
			hooks.AdminHTTPRegistrars = append(hooks.AdminHTTPRegistrars, func(_ context.Context, routes HTTPRoutes) error {
				// Both accepted path forms must retain the authenticated API group.
				routes.Handle(http.MethodGet, "extension-check/:id", handler)
				routes.Handle(http.MethodPost, "/api/extension-check/:id", handler)
				return nil
			})
			hooks.HTTPRegistrars = append(hooks.HTTPRegistrars, func(_ context.Context, routes HTTPRoutes) error {
				routes.Handle(http.MethodGet, "/api/extension-public", handler)
				return nil
			})
			return nil
		},
	}})

	for _, tc := range []struct {
		name, method, path, body string
		headers                  map[string]string
		status                   int
		want                     response
		admin                    bool
	}{
		{name: "admin authentication required", method: http.MethodGet, path: "/api/extension-check/item", status: http.StatusUnauthorized, admin: true},
		{name: "relative path and built-in bearer", method: http.MethodGet, path: "/api/extension-check/item?q=value", headers: map[string]string{"Authorization": "Bearer test-admin-token-with-enough-length"}, status: http.StatusOK, want: response{ID: "item", Query: "value", Tenant: "tenant-a"}, admin: true},
		{name: "absolute path and external authorizer", method: http.MethodPost, path: "/api/extension-check/item?q=value", body: `{"value":1}`, headers: map[string]string{"X-Test-Admin": "allowed"}, status: http.StatusOK, want: response{ID: "item", Query: "value", Body: `{"value":1}`, Tenant: "tenant-a"}, admin: true},
		{name: "tenant rejection", method: http.MethodGet, path: "/api/extension-check/item", headers: map[string]string{"X-Test-Admin": "allowed", "X-Test-Deny-Scope": "yes"}, status: http.StatusForbidden, admin: true},
		{name: "query token rejection", method: http.MethodGet, path: "/api/extension-check/item?token=fixture", headers: map[string]string{"X-Test-Admin": "allowed"}, status: http.StatusUnauthorized, admin: true},
		{name: "bounded extension body", method: http.MethodPost, path: "/api/extension-check/item", body: strings.Repeat("x", (1<<20)+1), headers: map[string]string{"X-Test-Admin": "allowed"}, status: http.StatusRequestEntityTooLarge, admin: true},
		{name: "ordinary registrar stays outside admin group", method: http.MethodGet, path: "/api/extension-public", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if tc.admin && rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("extension admin route did not inherit no-store")
			}
			if !tc.admin && rec.Header().Get("Cache-Control") != "" {
				t.Fatal("ordinary registrar unexpectedly inherited admin no-store")
			}
			if tc.status != http.StatusOK {
				if calls != before {
					t.Fatal("handler ran despite middleware rejection")
				}
				return
			}
			if calls != before+1 {
				t.Fatal("handler was not invoked exactly once")
			}
			var got response
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("response = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestAuthenticatedRoutePathUsesAnExactAPISegment(t *testing.T) {
	for input, want := range map[string]string{
		"/api/pro/example": "/pro/example",
		"pro/example":      "/pro/example",
		"/pro/example":     "/pro/example",
		"/api":             "/",
		"/apiary/example":  "/apiary/example",
	} {
		if got := authenticatedRoutePath(input); got != want {
			t.Errorf("authenticatedRoutePath(%q) = %q, want %q", input, got, want)
		}
	}
}
