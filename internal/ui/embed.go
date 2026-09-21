package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v5"
)

//go:embed all:dist
var assets embed.FS

func Register(e *echo.Echo, devTarget string) {
	RegisterFS(e, devTarget, nil)
}

func RegisterAt(e *echo.Echo, devTarget string, basePath string) {
	RegisterFSAt(e, devTarget, nil, basePath)
}

func RegisterFS(e *echo.Echo, devTarget string, customAssets fs.FS) {
	RegisterFSAt(e, devTarget, customAssets, "")
}

func RegisterFSAt(e *echo.Echo, devTarget string, customAssets fs.FS, basePath string) {
	if devTarget != "" {
		registerDevRedirect(e, devTarget, basePath)
		return
	}
	registerEmbedded(e, customAssets, basePath)
}

func registerEmbedded(e *echo.Echo, customAssets fs.FS, basePath string) {
	sub := customAssets
	if sub == nil {
		sub, _ = fs.Sub(assets, "dist")
	}
	basePath = normalizeBasePath(basePath)
	files := http.FileServer(http.FS(sub))
	serveIndex := func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "no-store")
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			return err
		}
		return c.HTMLBlob(http.StatusOK, b)
	}
	handler := func(c *echo.Context) error {
		p := c.Request().URL.Path
		if isAPIPath(p) || isReservedDiagnosticPath(p) {
			return echo.ErrNotFound
		}
		cleanPath, ok := pathForBase(p, basePath)
		if !ok {
			return echo.ErrNotFound
		}
		if cleanPath == "/" || cleanPath == "" {
			return serveIndex(c)
		}

		clean := strings.TrimPrefix(path.Clean(cleanPath), "/")
		if _, err := fs.Stat(sub, clean); err == nil {
			if strings.HasPrefix(clean, "_nuxt/") {
				c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				c.Response().Header().Set("Cache-Control", "no-store")
			}
			req := c.Request().Clone(c.Request().Context())
			urlCopy := *req.URL
			urlCopy.Path = "/" + clean
			req.URL = &urlCopy
			files.ServeHTTP(c.Response(), req)
			return nil
		}

		// Never fall back to SPA HTML for missing static assets.
		// Return a plain 404 here so module/script requests do not get a JSON error body.
		if strings.HasPrefix(clean, "_nuxt/") || filepath.Ext(clean) != "" {
			return c.NoContent(http.StatusNotFound)
		}

		return serveIndex(c)
	}
	e.GET("/*", handler)
}

func registerDevRedirect(e *echo.Echo, devTarget string, basePath string) {
	target, err := url.Parse(devTarget)
	if err != nil || target.Scheme == "" || target.Host == "" {
		panic("invalid RELAY_DEV_UI_TARGET: " + devTarget)
	}
	basePath = normalizeBasePath(basePath)

	e.GET("/*", func(c *echo.Context) error {
		p := c.Request().URL.Path
		if isAPIPath(p) || isReservedDiagnosticPath(p) {
			return echo.ErrNotFound
		}
		if _, ok := pathForBase(p, basePath); !ok {
			return echo.ErrNotFound
		}

		redirectURL := *target
		redirectURL.Path = singleJoiningSlash(target.Path, c.Request().URL.Path)
		redirectURL.RawQuery = c.Request().URL.RawQuery
		return c.Redirect(http.StatusTemporaryRedirect, redirectURL.String())
	})
}

func normalizeBasePath(value string) string {
	value = "/" + strings.Trim(strings.TrimSpace(value), "/")
	if value == "/" {
		return ""
	}
	return path.Clean(value)
}

func pathForBase(requestPath string, basePath string) (string, bool) {
	if basePath == "" {
		return requestPath, true
	}
	if requestPath == basePath {
		return "/", true
	}
	if strings.HasPrefix(requestPath, basePath+"/") {
		return strings.TrimPrefix(requestPath, basePath), true
	}
	return "", false
}

func isAPIPath(p string) bool {
	return p == "/api" || p == "/v1" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/v1/")
}

func isReservedDiagnosticPath(p string) bool {
	return p == "/debug" ||
		p == "/metrics" ||
		p == "/vars" ||
		strings.HasPrefix(p, "/debug/") ||
		strings.HasPrefix(p, "/metrics/") ||
		strings.HasPrefix(p, "/vars/")
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}
