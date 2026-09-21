package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func TestAdminSessionDoesNotReturnAdminToken(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	login := adminRequest(t, e, http.MethodPost, "/api/session", "")
	if login.Code != http.StatusOK {
		t.Fatalf("expected login status 200, got %d: %s", login.Code, login.Body.String())
	}
	if strings.Contains(login.Body.String(), adminTestToken()) {
		t.Fatal("session response body leaked admin token")
	}
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}
	var sessionCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == "relay_admin_token" {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected relay_admin_token cookie")
	}
	if sessionCookie.Value == "" || sessionCookie.Value == adminTestToken() {
		t.Fatal("expected random session cookie value, not admin token")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected session status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatalf("expected session cookie to authenticate, got %s", rec.Body.String())
	}
}

func TestAdminSessionCookieNameCanBeCustomized(t *testing.T) {
	st := testutil.NewStore(t)
	secrets, err := security.NewLocalSecretManager(adminTestMasterKey(), st)
	if err != nil {
		t.Fatal(err)
	}
	api := New(st, nil, nil, secrets, adminTestToken(), SystemInfo{}).WithAdminCookieName("relay_pro_admin_token")
	e := echo.New()
	api.Register(e)

	login := adminRequest(t, e, http.MethodPost, "/api/session", "")
	if login.Code != http.StatusOK {
		t.Fatalf("expected login status 200, got %d: %s", login.Code, login.Body.String())
	}
	var customCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == "relay_admin_token" {
			t.Fatal("customized admin session emitted the default public cookie")
		}
		if cookie.Name == "relay_pro_admin_token" {
			customCookie = cookie
		}
	}
	if customCookie == nil {
		t.Fatal("expected customized admin session cookie")
	}

	defaultReq := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	defaultReq.AddCookie(&http.Cookie{Name: "relay_admin_token", Value: customCookie.Value})
	defaultRec := httptest.NewRecorder()
	e.ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("expected default-cookie status check to return 200, got %d: %s", defaultRec.Code, defaultRec.Body.String())
	}
	if strings.Contains(defaultRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("default cookie name authenticated against custom cookie API: %s", defaultRec.Body.String())
	}

	customReq := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	customReq.AddCookie(customCookie)
	customRec := httptest.NewRecorder()
	e.ServeHTTP(customRec, customReq)
	if customRec.Code != http.StatusOK {
		t.Fatalf("expected custom-cookie status check to return 200, got %d: %s", customRec.Code, customRec.Body.String())
	}
	if !strings.Contains(customRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("expected custom cookie to authenticate, got %s", customRec.Body.String())
	}
}

func TestAdminQueryStringTokenIsRejected(t *testing.T) {
	e, _, _ := credentialTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/providers?token="+adminTestToken(), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected query-string admin token to be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
}
