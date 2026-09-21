package httpsec

import (
	"net/http"

	"github.com/anchorshell/relay/internal/security"
	"github.com/labstack/echo/v5"
)

// InferenceAuth is deliberately independent of management sessions, cookies,
// provider credentials, and request-context hooks. An empty configured token
// is the operator's explicit opt-out, resolved during credential bootstrap.
func InferenceAuth(expected string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if expected == "" {
				return next(c)
			}
			values := c.Request().Header.Values("Authorization")
			if len(values) == 1 {
				if token, ok := security.BearerToken(values[0]); ok && security.TokensEqual(expected, token) {
					return next(c)
				}
			}
			SetNoStore(c.Response().Header())
			c.Response().Header().Set("WWW-Authenticate", `Bearer realm="Relay inference API"`)
			return echo.NewHTTPError(http.StatusUnauthorized, "valid Relay API token required")
		}
	}
}
