package httpsec

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"
)

const RequestBodyTooLargeMessage = "request body too large"

func SecurityHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			header := c.Response().Header()
			setHeaderIfEmpty(header, "X-Content-Type-Options", "nosniff")
			setHeaderIfEmpty(header, "Referrer-Policy", "no-referrer")
			setHeaderIfEmpty(header, "X-Frame-Options", "DENY")
			return next(c)
		}
	}
}

func NoStore() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			SetNoStore(c.Response().Header())
			return next(c)
		}
	}
}

func SetNoStore(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
}

func BodyLimit(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if limit > 0 && c.Request().Body != nil {
				c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, limit)
			}
			if err := next(c); err != nil {
				if IsBodyTooLarge(err) {
					return echo.NewHTTPError(http.StatusRequestEntityTooLarge, RequestBodyTooLargeMessage)
				}
				return err
			}
			return nil
		}
	}
}

func IsBodyTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func setHeaderIfEmpty(header http.Header, key, value string) {
	if header.Get(key) == "" {
		header.Set(key, value)
	}
}
