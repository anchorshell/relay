package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/api/admin"
	"github.com/anchorshell/relay/internal/httpsec"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

type httpRoutes struct {
	echo  *echo.Echo
	group *echo.Group
}

func (r httpRoutes) Handle(method string, path string, handler HTTPHandler) {
	echoHandler := func(c *echo.Context) error {
		req, err := newHTTPRequest(c)
		if err != nil {
			return err
		}
		status, body, err := handlePublicHTTP(c.Request().Context(), req, handler)
		if err != nil {
			return err
		}
		if body == nil {
			return c.NoContent(status)
		}
		return c.JSON(status, body)
	}
	if r.group != nil {
		r.group.Add(method, authenticatedRoutePath(path), echoHandler)
		return
	}
	r.echo.Add(method, path, echoHandler)
}

func newHTTPRequest(c *echo.Context) (HTTPRequest, error) {
	var body []byte
	if c.Request().Body != nil {
		var err error
		body, err = io.ReadAll(c.Request().Body)
		if err != nil {
			return HTTPRequest{}, err
		}
	}
	pathValues := c.PathValues()
	params := make(map[string]string, len(pathValues))
	for _, item := range pathValues {
		params[item.Name] = item.Value
	}
	return HTTPRequest{
		Method:  c.Request().Method,
		Path:    c.Request().URL.Path,
		Headers: c.Request().Header.Clone(),
		Query:   cloneURLValues(c.Request().URL.Query()),
		Params:  params,
		Body:    body,
	}, nil
}

func authenticatedRoutePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "/api" {
		return "/"
	}
	trimmed = strings.TrimPrefix(trimmed, "/api/")
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}

func handlePublicHTTP(ctx context.Context, req HTTPRequest, handler HTTPHandler) (int, any, error) {
	resp, err := handler(ctx, req)
	if err != nil {
		return 0, nil, err
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	return status, resp.Body, nil
}

func adminAuthorizers(hooks []AdminAuthorizer) []admin.ExternalAuthorizer {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]admin.ExternalAuthorizer, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(c *echo.Context) bool {
			if c == nil || c.Request() == nil {
				return false
			}
			ok, err := hook(c.Request().Context(), AdminAuthInput{
				Method:  c.Request().Method,
				Path:    c.Request().URL.Path,
				Headers: c.Request().Header.Clone(),
			})
			return err == nil && ok
		})
	}
	return out
}

func adminTenantScopeHooks(hooks []AdminTenantScopeHook) []admin.ExternalTenantScopeHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]admin.ExternalTenantScopeHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(c *echo.Context) (admin.TenantScope, error) {
			if c == nil || c.Request() == nil {
				return admin.TenantScope{}, nil
			}
			scope, err := hook(c.Request().Context(), AdminTenantScopeInput{
				Method:  c.Request().Method,
				Path:    c.Request().URL.Path,
				Headers: c.Request().Header.Clone(),
			})
			if err != nil {
				var rejection AdminTenantScopeRejection
				if errors.As(err, &rejection) {
					status := rejection.Status
					if status == 0 {
						status = http.StatusForbidden
					}
					return admin.TenantScope{}, echo.NewHTTPError(status, rejection.Error())
				}
				return admin.TenantScope{}, err
			}
			return admin.TenantScope{
				OrganizationUUID: strings.TrimSpace(scope.OrganizationUUID),
				UserUUID:         strings.TrimSpace(scope.UserUUID),
			}, nil
		})
	}
	return out
}

func adminQueryScopeHooks(hooks []AdminQueryScopeHook) []admin.ExternalQueryScopeHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]admin.ExternalQueryScopeHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(c *echo.Context, input admin.QueryScopeInput) ([]func(*gorm.DB) *gorm.DB, error) {
			if c == nil || c.Request() == nil {
				return nil, nil
			}
			query := c.Request().URL.Query()
			scopes, err := hook(c.Request().Context(), AdminQueryScopeInput{
				Resource: input.Resource,
				Method:   c.Request().Method,
				Path:     c.Request().URL.Path,
				Headers:  c.Request().Header.Clone(),
				Query:    cloneURLValues(query),
			})
			if err == nil {
				return scopes, nil
			}
			var rejection AdminQueryScopeRejection
			if errors.As(err, &rejection) {
				status := rejection.Status
				if status == 0 {
					status = http.StatusForbidden
				}
				return nil, echo.NewHTTPError(status, rejection.Error())
			}
			return nil, err
		})
	}
	return out
}

func adminQueueScopeHooks(hooks []AdminQueueScopeHook) []admin.ExternalQueueScopeHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]admin.ExternalQueueScopeHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(c *echo.Context, _ admin.QueueScopeInput) (admin.ExternalQueueScope, error) {
			if c == nil || c.Request() == nil {
				return admin.ExternalQueueScope{}, nil
			}
			scope, err := hook(c.Request().Context(), AdminQueueScopeInput{
				Method:  c.Request().Method,
				Path:    c.Request().URL.Path,
				Headers: c.Request().Header.Clone(),
				Query:   cloneURLValues(c.Request().URL.Query()),
			})
			if err != nil {
				var rejection AdminQueryScopeRejection
				if errors.As(err, &rejection) {
					status := rejection.Status
					if status == 0 {
						status = http.StatusForbidden
					}
					return admin.ExternalQueueScope{}, echo.NewHTTPError(status, rejection.Error())
				}
				return admin.ExternalQueueScope{}, err
			}
			return admin.ExternalQueueScope{
				OrganizationUUID: strings.TrimSpace(scope.OrganizationUUID),
				ActorID:          strings.TrimSpace(scope.ActorID),
				Deny:             scope.Deny,
			}, nil
		})
	}
	return out
}

func adminCapacityRowHooks(hooks []AdminCapacityRowsHook) []admin.ExternalCapacityRowsHook {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]admin.ExternalCapacityRowsHook, 0, len(hooks))
	for _, hook := range hooks {
		hook := hook
		if hook == nil {
			continue
		}
		out = append(out, func(ctx context.Context, input admin.ExternalCapacityRowsInput) ([]admin.ExternalCapacityRow, error) {
			rows, err := hook(ctx, AdminCapacityRowsInput{
				Headers: input.Headers.Clone(),
				Now:     input.Now,
				Preview: input.Preview,
				RuntimeUsed: func(scope LimitScope, metric string, period string) int64 {
					if input.RuntimeUsed == nil {
						return 0
					}
					return input.RuntimeUsed(admin.CapacityScope{Type: scope.Type, ID: scope.ID}, metric, period)
				},
			})
			if errors.Is(err, ErrAdminCapacityContextUnavailable) {
				return nil, admin.ErrCapacityContextUnavailable
			}
			if err != nil || rows == nil {
				return nil, err
			}
			out := make([]admin.ExternalCapacityRow, 0, len(rows))
			for _, row := range rows {
				out = append(out, adminCapacityRow(row))
			}
			return out, nil
		})
	}
	return out
}

func adminCapacityRow(row CapacityRow) admin.ExternalCapacityRow {
	return admin.ExternalCapacityRow{
		Key:           row.Key,
		Label:         row.Label,
		ScopeType:     row.ScopeType,
		ScopeID:       row.ScopeID,
		TargetType:    row.TargetType,
		TargetKey:     row.TargetKey,
		ActorID:       row.ActorID,
		Metric:        row.Metric,
		Period:        row.Period,
		Configured:    row.Configured,
		Effective:     row.Effective,
		Used:          row.Used,
		Reserved:      row.Reserved,
		Remaining:     row.Remaining,
		Percent:       row.Percent,
		WindowStart:   row.WindowStart,
		ResetAt:       row.ResetAt,
		Source:        row.Source,
		UserScoped:    row.UserScoped,
		Blocked:       row.Blocked,
		BlockedUntil:  row.BlockedUntil,
		BlockedReason: row.BlockedReason,
	}
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, item := range values {
		cloned[key] = append([]string(nil), item...)
	}
	return cloned
}

func requestLoggerMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()
			err := next(c)
			slog.Info("http request",
				"method", c.Request().Method,
				"path", c.Path(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
			return err
		}
	}
}

func recoverMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic recovered",
						"panic_type", fmt.Sprintf("%T", r),
						"method", c.Request().Method,
						"path", c.Path(),
					)
					err = echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
				}
			}()
			return next(c)
		}
	}
}

func sanitizedHTTPErrorHandler(c *echo.Context, err error) {
	code, msg := sanitizedHTTPError(err)
	if resp, err := echo.UnwrapResponse(c.Response()); err == nil && resp != nil && resp.Committed {
		return
	}
	_ = c.JSON(code, map[string]any{"error": msg})
}

func sanitizedHTTPError(err error) (int, string) {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		code := he.Code
		if code == 0 {
			code = http.StatusInternalServerError
		}
		if code >= http.StatusInternalServerError {
			return code, "internal server error"
		}
		return code, fmt.Sprint(he.Message)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return http.StatusNotFound, "not found"
	}
	if errors.Is(err, echo.ErrNotFound) {
		return http.StatusNotFound, "not found"
	}
	if httpsec.IsBodyTooLarge(err) {
		return http.StatusRequestEntityTooLarge, httpsec.RequestBodyTooLargeMessage
	}
	return http.StatusInternalServerError, "internal server error"
}
