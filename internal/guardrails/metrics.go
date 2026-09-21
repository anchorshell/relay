package guardrails

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var guardrailMetrics struct {
	once     sync.Once
	checks   metric.Int64Counter
	duration metric.Float64Histogram
	blocks   metric.Int64Counter
	errors   metric.Int64Counter
	failOpen metric.Int64Counter
}

func initializeMetrics() {
	guardrailMetrics.once.Do(func() {
		meter := otel.Meter("anchorshell-relay/guardrails")
		guardrailMetrics.checks, _ = meter.Int64Counter("relay_guardrail_checks_total")
		guardrailMetrics.duration, _ = meter.Float64Histogram("relay_guardrail_duration_seconds")
		guardrailMetrics.blocks, _ = meter.Int64Counter("relay_guardrail_blocks_total")
		guardrailMetrics.errors, _ = meter.Int64Counter("relay_guardrail_errors_total")
		guardrailMetrics.failOpen, _ = meter.Int64Counter("relay_guardrail_fail_open_total")
	})
}

func recordResult(ctx context.Context, result Result) {
	initializeMetrics()
	preset := result.Preset
	if preset == "" {
		preset = "custom"
	}
	base := metric.WithAttributes(
		attribute.String("stage", string(result.Stage)),
		attribute.String("preset", preset),
	)
	guardrailMetrics.checks.Add(ctx, 1, metric.WithAttributes(
		attribute.String("stage", string(result.Stage)),
		attribute.String("decision", string(result.Decision)),
		attribute.String("preset", preset),
	))
	guardrailMetrics.duration.Record(ctx, result.Duration.Seconds(), base)
	if result.Decision == DecisionBlock || result.Decision == DecisionReplaceResponse {
		guardrailMetrics.blocks.Add(ctx, 1, base)
	}
	if result.ErrorCode != "" && result.FailMode != "" {
		guardrailMetrics.errors.Add(ctx, 1, metric.WithAttributes(
			attribute.String("stage", string(result.Stage)),
			attribute.String("error_type", result.ErrorCode),
			attribute.String("fail_mode", string(result.FailMode)),
		))
	}
	if result.FailMode == FailOpen {
		guardrailMetrics.failOpen.Add(ctx, 1, base)
	}
}
