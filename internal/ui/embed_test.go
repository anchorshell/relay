package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v5"
)

func TestDashboardFallsBackToSPAIndex(t *testing.T) {
	e := echo.New()
	RegisterFS(e, "", fstest.MapFS{
		"index.html": {
			Data: []byte(`<html><title>Model Relay | AnchorShell</title><body>SPA fixture</body></html>`),
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard to serve SPA index, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Model Relay | AnchorShell") {
		t.Fatalf("expected SPA index fixture, got %q", body)
	}
}

func TestEmbeddedUIBasePathStripsPrefixForAssetsAndSPA(t *testing.T) {
	e := echo.New()
	RegisterFSAt(e, "", fstest.MapFS{
		"index.html": {
			Data: []byte(`<html>base path app</html>`),
		},
		"_nuxt/app.js": {
			Data: []byte(`console.log("ok")`),
		},
	}, "/relay")

	indexRec := httptest.NewRecorder()
	e.ServeHTTP(indexRec, httptest.NewRequest(http.MethodGet, "/relay/usage", nil))
	if indexRec.Code != http.StatusOK || !strings.Contains(indexRec.Body.String(), "base path app") {
		t.Fatalf("expected base path SPA index, got %d: %s", indexRec.Code, indexRec.Body.String())
	}

	assetRec := httptest.NewRecorder()
	e.ServeHTTP(assetRec, httptest.NewRequest(http.MethodGet, "/relay/_nuxt/app.js", nil))
	if assetRec.Code != http.StatusOK || !strings.Contains(assetRec.Body.String(), "ok") {
		t.Fatalf("expected base path asset, got %d: %s", assetRec.Code, assetRec.Body.String())
	}

	rootRec := httptest.NewRecorder()
	e.ServeHTTP(rootRec, httptest.NewRequest(http.MethodGet, "/usage", nil))
	if rootRec.Code != http.StatusNotFound {
		t.Fatalf("expected non-base path to be ignored, got %d: %s", rootRec.Code, rootRec.Body.String())
	}
}

func TestReservedDiagnosticPathsDoNotUseSPAFallback(t *testing.T) {
	for _, path := range []string{"/debug", "/debug/pprof", "/metrics", "/vars"} {
		if !isReservedDiagnosticPath(path) {
			t.Fatalf("expected %s to be reserved", path)
		}
	}
	if isReservedDiagnosticPath("/settings") {
		t.Fatal("did not expect normal dashboard path to be reserved")
	}
}
