package admin

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/security"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/labstack/echo/v5"
)

const (
	defaultAdminCookieName = "relay_admin_token"
	adminSessionTTL        = 24 * time.Hour
)

func (a *API) WithAdminCookieName(name string) *API {
	if a == nil {
		return nil
	}
	a.adminCookieName = normalizeAdminCookieName(name)
	return a
}

func (a *API) cookieName() string {
	if a == nil {
		return defaultAdminCookieName
	}
	return normalizeAdminCookieName(a.adminCookieName)
}

func normalizeAdminCookieName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || strings.ContainsAny(trimmed, " \t\r\n;=,") {
		return defaultAdminCookieName
	}
	return trimmed
}

func (a *API) createSession(c *echo.Context) error {
	if a.hasQueryToken(c) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid admin token")
	}
	token, ok := security.BearerToken(c.Request().Header.Get("Authorization"))
	if !ok || !a.tokenMatches(token) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid admin token")
	}
	sessionID, err := a.createAdminSession()
	if err != nil {
		return err
	}
	cookieName := a.cookieName()
	c.SetCookie(&http.Cookie{
		Name:     cookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(adminSessionTTL),
	})
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func (a *API) sessionStatus(c *echo.Context) error {
	if a.hasQueryToken(c) {
		return echo.NewHTTPError(http.StatusUnauthorized, "admin token required")
	}
	authorized := a.authorized(c)
	return c.JSON(http.StatusOK, map[string]any{"authenticated": authorized, "system": a.systemInfo})
}

func (a *API) logout(c *echo.Context) error {
	cookieName := a.cookieName()
	if cookie, err := c.Cookie(cookieName); err == nil {
		a.deleteAdminSession(cookie.Value)
	}
	c.SetCookie(&http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	return c.NoContent(http.StatusNoContent)
}

func (a *API) Middleware() echo.MiddlewareFunc {
	return a.authMiddleware
}

func (a *API) AuthorizedRequest(c *echo.Context) bool {
	if a.hasQueryToken(c) {
		return false
	}
	return a.authorizedBuiltIn(c)
}

func (a *API) authMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if a.hasQueryToken(c) {
			return echo.NewHTTPError(http.StatusUnauthorized, "admin token required")
		}
		if a.authorized(c) {
			if err := a.applyTenantScope(c); err != nil {
				return err
			}
			return next(c)
		}
		return echo.NewHTTPError(http.StatusUnauthorized, "admin token required")
	}
}

func (a *API) applyTenantScope(c *echo.Context) error {
	if a == nil || c == nil || c.Request() == nil {
		return nil
	}
	for _, hook := range a.externalTenantScopeHooks {
		if hook == nil {
			continue
		}
		scope, err := hook(c)
		if err != nil {
			return err
		}
		if strings.TrimSpace(scope.OrganizationUUID) == "" {
			continue
		}
		ctx := tenancy.ContextWithScope(c.Request().Context(), tenancy.Scope{
			OrganizationUUID: scope.OrganizationUUID,
			UserUUID:         scope.UserUUID,
		})
		c.SetRequest(c.Request().WithContext(ctx))
	}
	return nil
}

func (a *API) hasQueryToken(c *echo.Context) bool {
	return hasQueryToken(c, a.cookieName())
}

func hasQueryToken(c *echo.Context, cookieName string) bool {
	if c == nil || c.Request() == nil || c.Request().URL == nil {
		return false
	}
	query := c.Request().URL.Query()
	keys := []string{"token", "admin_token", defaultAdminCookieName, "RELAY_ADMIN_TOKEN"}
	if cookieName != "" && cookieName != defaultAdminCookieName {
		keys = append(keys, cookieName)
	}
	for _, key := range keys {
		if _, ok := query[key]; ok {
			return true
		}
	}
	return false
}

func (a *API) authorized(c *echo.Context) bool {
	if a.authorizedBuiltIn(c) {
		return true
	}
	for _, authorizer := range a.externalAuthorizers {
		if authorizer != nil && authorizer(c) {
			return true
		}
	}
	return false
}

func (a *API) authorizedBuiltIn(c *echo.Context) bool {
	if token, ok := security.BearerToken(c.Request().Header.Get("Authorization")); ok && a.tokenMatches(token) {
		return true
	}
	if c != nil {
		cookie, err := c.Cookie(a.cookieName())
		if err == nil && a.validAdminSession(cookie.Value) {
			return true
		}
	}
	return false
}

func (a *API) tokenMatches(candidate string) bool {
	return security.TokensEqual(a.adminToken, candidate)
}

func (a *API) createAdminSession() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	sessionID := base64.RawURLEncoding.EncodeToString(value)
	a.adminSessionsMu.Lock()
	if a.adminSessions == nil {
		a.adminSessions = make(map[string]time.Time)
	}
	a.adminSessions[sessionID] = time.Now().Add(adminSessionTTL)
	a.adminSessionsMu.Unlock()
	return sessionID, nil
}

func (a *API) validAdminSession(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	now := time.Now()
	a.adminSessionsMu.Lock()
	expiresAt, ok := a.adminSessions[sessionID]
	if !ok {
		a.adminSessionsMu.Unlock()
		return false
	}
	if now.After(expiresAt) {
		delete(a.adminSessions, sessionID)
		a.adminSessionsMu.Unlock()
		return false
	}
	a.adminSessionsMu.Unlock()
	return true
}

func (a *API) deleteAdminSession(sessionID string) {
	if sessionID == "" {
		return
	}
	a.adminSessionsMu.Lock()
	delete(a.adminSessions, sessionID)
	a.adminSessionsMu.Unlock()
}
