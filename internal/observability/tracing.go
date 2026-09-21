package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "anchorshell-relay"

type Provider struct {
	tracerProvider *sdktrace.TracerProvider
	enabled        bool
}

func Setup(ctx context.Context, defaultServiceName string) (*Provider, error) {
	// Propagate only W3C trace context. Arbitrary client-provided baggage must not
	// cross Relay and provider trust boundaries.
	propagator := propagation.TraceContext{}
	otel.SetTextMapPropagator(propagator)
	if !exportConfigured() {
		return &Provider{}, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize OTLP trace exporter: %w", err)
	}
	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attribute.String("service.name", serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize OpenTelemetry resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(time.Second),
			sdktrace.WithExportTimeout(10*time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(configuredSampler()),
	)
	otel.SetTracerProvider(tp)
	return &Provider{tracerProvider: tp, enabled: true}, nil
}

func (p *Provider) Enabled() bool { return p != nil && p.enabled }

func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.tracerProvider == nil {
		return nil
	}
	return p.tracerProvider.Shutdown(ctx)
}

func Tracer() trace.Tracer { return otel.Tracer(instrumentationName) }

func EchoMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			ctx := otel.GetTextMapPropagator().Extract(req.Context(), propagation.HeaderCarrier(req.Header))
			route := strings.TrimSpace(c.Path())
			if route == "" {
				route = "unmatched"
			}
			spanRoute := route
			if strings.ContainsAny(spanRoute, "*:") && strings.TrimSpace(req.URL.Path) != "" {
				spanRoute = req.URL.Path
			}
			spanName := req.Method + " " + spanRoute
			isRoot := !trace.SpanContextFromContext(ctx).IsValid()
			if isRoot {
				spanName = "ROOT " + spanName
			}
			ctx, span := Tracer().Start(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.Bool("anchorshell.trace.root", isRoot),
					attribute.String("http.request.method", req.Method),
					attribute.String("http.route", route),
					attribute.String("url.path", req.URL.Path),
				),
			)
			c.SetRequest(req.WithContext(ctx))
			err := next(c)
			status := 0
			if response, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil {
				status = response.Status
			}
			if err != nil || status == 0 {
				status = statusFromError(err)
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if requestID := strings.TrimSpace(c.Response().Header().Get("X-Request-Id")); requestID != "" {
				span.SetAttributes(attribute.String("anchorshell.request.id", requestID))
			}
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
			span.End()
			return err
		}
	}
}

func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &tracingTransport{base: base}
}

type tracingTransport struct{ base http.RoundTripper }

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", req.Method),
		attribute.String("url.scheme", req.URL.Scheme),
		attribute.String("url.path", req.URL.EscapedPath()),
	}
	if host := req.URL.Hostname(); host != "" {
		attrs = append(attrs, attribute.String("server.address", host))
	}
	if rawPort := req.URL.Port(); rawPort != "" {
		if port, err := strconv.Atoi(rawPort); err == nil {
			attrs = append(attrs, attribute.Int("server.port", port))
		}
	}
	ctx, span := Tracer().Start(req.Context(), "relay.provider.round_trip",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	var timingMu sync.Mutex
	var getConnAt, gotConnAt, wroteRequestAt, firstByteAt time.Time
	clientTrace := &httptrace.ClientTrace{
		GetConn: func(string) {
			timingMu.Lock()
			getConnAt = time.Now()
			timingMu.Unlock()
			span.AddEvent("http.connection.requested")
		},
		GotConn: func(info httptrace.GotConnInfo) {
			timingMu.Lock()
			gotConnAt = time.Now()
			timingMu.Unlock()
			span.AddEvent("http.connection.acquired", trace.WithAttributes(
				attribute.Bool("network.connection.reused", info.Reused),
				attribute.Bool("network.connection.was_idle", info.WasIdle),
			))
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			timingMu.Lock()
			wroteRequestAt = time.Now()
			timingMu.Unlock()
			if info.Err != nil {
				span.SetStatus(codes.Error, "HTTP request write failed")
			}
			span.AddEvent("http.request.written")
		},
		GotFirstResponseByte: func() {
			timingMu.Lock()
			firstByteAt = time.Now()
			timingMu.Unlock()
			span.AddEvent("http.response.first_byte")
		},
	}
	ctx = httptrace.WithClientTrace(ctx, clientTrace)
	request := req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	response, err := t.base.RoundTrip(request)
	timingMu.Lock()
	connectionRequestedAt := getConnAt
	connectionAcquiredAt := gotConnAt
	requestWrittenAt := wroteRequestAt
	firstResponseByteAt := firstByteAt
	if !connectionRequestedAt.IsZero() && !connectionAcquiredAt.IsZero() {
		span.SetAttributes(attribute.Int64("anchorshell.http.transport_wait_ms", connectionAcquiredAt.Sub(connectionRequestedAt).Milliseconds()))
	}
	if !requestWrittenAt.IsZero() && !firstResponseByteAt.IsZero() {
		span.SetAttributes(attribute.Int64("anchorshell.http.time_to_first_byte_ms", firstResponseByteAt.Sub(requestWrittenAt).Milliseconds()))
	}
	timingMu.Unlock()
	if !connectionRequestedAt.IsZero() && !connectionAcquiredAt.IsZero() && !connectionAcquiredAt.Before(connectionRequestedAt) {
		_, waitSpan := Tracer().Start(ctx, "relay.provider.outbound_connection_wait", trace.WithTimestamp(connectionRequestedAt))
		waitSpan.End(trace.WithTimestamp(connectionAcquiredAt))
	}
	if err != nil {
		span.SetAttributes(attribute.String("error.type", "transport"))
		span.SetStatus(codes.Error, "HTTP transport failed")
		span.End()
		return response, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode >= http.StatusBadRequest {
		span.SetStatus(codes.Error, http.StatusText(response.StatusCode))
	}
	if response.Body == nil || response.Body == http.NoBody {
		span.End()
		return response, nil
	}
	_, bodySpan := Tracer().Start(ctx, "relay.proxy.response_body_read")
	body := &tracingBody{body: response.Body, span: span, bodySpan: bodySpan}
	if readWriter, ok := response.Body.(io.ReadWriteCloser); ok {
		response.Body = &tracingReadWriteBody{tracingBody: body, writer: readWriter}
	} else {
		response.Body = body
	}
	return response, nil
}

type tracingBody struct {
	body     io.ReadCloser
	span     trace.Span
	bodySpan trace.Span
	once     sync.Once
}

func (b *tracingBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		if err != io.EOF {
			b.span.SetAttributes(attribute.String("error.type", "response_body"))
			b.span.SetStatus(codes.Error, "HTTP response body failed")
		}
		b.finish()
	}
	return n, err
}

func (b *tracingBody) Close() error {
	err := b.body.Close()
	if err != nil {
		b.span.SetAttributes(attribute.String("error.type", "response_body"))
		b.span.SetStatus(codes.Error, "HTTP response body close failed")
	}
	b.finish()
	return err
}

func (b *tracingBody) finish() {
	b.once.Do(func() {
		b.bodySpan.End()
		b.span.End()
	})
}

type tracingReadWriteBody struct {
	*tracingBody
	writer io.Writer
}

func (b *tracingReadWriteBody) Write(p []byte) (int, error) {
	return b.writer.Write(p)
}

func SetRequestAttributes(ctx context.Context, requestID string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		span.SetAttributes(attribute.String("anchorshell.request.id", requestID))
	}
	span.SetAttributes(attrs...)
}

func exportConfigured() bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != ""
}

func configuredSampler() sdktrace.Sampler {
	ratio := 0.05
	if raw := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			ratio = parsed
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER"))) {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
}

func statusFromError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code >= 100 && httpErr.Code <= 599 {
		return httpErr.Code
	}
	return http.StatusInternalServerError
}
