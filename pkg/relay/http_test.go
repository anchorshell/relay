package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

func TestSanitizedHTTPErrorHidesDatabaseDetails(t *testing.T) {
	code, message := sanitizedHTTPError(errors.New("near SELECT * FROM credentials: syntax error"))
	if code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", code)
	}
	if message != "internal server error" {
		t.Fatalf("expected sanitized message, got %q", message)
	}
	if strings.Contains(message, "credentials") || strings.Contains(message, "SELECT") {
		t.Fatalf("message leaked SQL details: %q", message)
	}
}

func TestSanitizedHTTPErrorMapsRecordNotFound(t *testing.T) {
	code, message := sanitizedHTTPError(gorm.ErrRecordNotFound)
	if code != http.StatusNotFound || message != "not found" {
		t.Fatalf("expected sanitized not found, got %d %q", code, message)
	}
}

func TestSanitizedHTTPErrorPreservesExplicitHTTPErrors(t *testing.T) {
	code, message := sanitizedHTTPError(echo.NewHTTPError(http.StatusBadRequest, "invalid request log sort"))
	if code != http.StatusBadRequest || message != "invalid request log sort" {
		t.Fatalf("expected explicit HTTP error, got %d %q", code, message)
	}
}

func TestSanitizedHTTPErrorHidesExplicitInternalErrors(t *testing.T) {
	code, message := sanitizedHTTPError(echo.NewHTTPError(http.StatusBadGateway, "Authorization: Bearer sk-provider-secret"))
	if code != http.StatusBadGateway || message != "internal server error" {
		t.Fatalf("expected sanitized 5xx error, got %d %q", code, message)
	}
}

func TestRecoverMiddlewareReturnsGeneric500AndRedactsPanicLog(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e := echo.New()
	e.HTTPErrorHandler = sanitizedHTTPErrorHandler
	e.Use(recoverMiddleware())
	e.GET("/panic", func(c *echo.Context) error {
		panic("Authorization: Bearer sk-provider-secret RELAY_MASTER_KEY=master-secret")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, leaked := range []string{"sk-provider-secret", "master-secret", "admin-secret", "Authorization"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("panic response leaked %q: %s", leaked, rec.Body.String())
		}
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("panic log leaked %q: %s", leaked, logs.String())
		}
	}
}

func TestRequestLoggerDoesNotLogHeadersOrBodies(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	e := echo.New()
	e.Use(requestLoggerMiddleware())
	e.POST("/ok", func(c *echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/ok", strings.NewReader(`{"secret":"sk-body-secret"}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	for _, leaked := range []string{"admin-secret", "sk-body-secret", "Authorization", "secret"} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("request log leaked %q: %s", leaked, logs.String())
		}
	}
}

func TestExtensionHTTPRequestIncludesPathParameters(t *testing.T) {
	e := echo.New()
	routes := httpRoutes{echo: e}
	routes.Handle(http.MethodGet, "/resource/:resource_id", func(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
		return OK(map[string]string{"resource_id": req.Params["resource_id"]}), nil
	})
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/resource/public-uuid", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "public-uuid") {
		t.Fatalf("extension path parameters were not forwarded: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
