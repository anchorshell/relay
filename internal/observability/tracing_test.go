package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEchoMiddlewareMarksOnlyRootServerSpan(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	e := echo.New()
	e.Use(EchoMiddleware())
	e.POST("/v1/chat/completions", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "ROOT POST /v1/chat/completions" {
		t.Fatalf("unexpected root spans: %#v", spans)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	e.ServeHTTP(httptest.NewRecorder(), req)
	spans = recorder.Ended()
	if len(spans) != 2 || spans[1].Name() != "POST /v1/chat/completions" {
		t.Fatalf("unexpected continued spans: %#v", spans)
	}
}

func TestEchoMiddlewareUsesConcretePathForWildcardSpanName(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})

	e := echo.New()
	e.Use(EchoMiddleware())
	e.POST("/v1/*", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "ROOT POST /v1/chat/completions" {
		t.Fatalf("wildcard root span did not use concrete request path: %#v", spans)
	}
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "http.route" && attr.Value.AsString() != "/v1/*" {
			t.Fatalf("normalized http.route changed unexpectedly: %q", attr.Value.AsString())
		}
	}
}
