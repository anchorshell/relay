package transport

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/store"
	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
)

type ObservedLearner struct {
	store     *store.Store
	telemetry *telemetry.Hub
}

func NewObservedLearner(st *store.Store, hub *telemetry.Hub) *ObservedLearner {
	return &ObservedLearner{store: st, telemetry: hub}
}

func (l *ObservedLearner) Learn(ctx context.Context, endpoint models.Endpoint, headers http.Header, statusCode int) error {
	now := time.Now().UTC()
	for _, mapping := range []struct {
		header string
		metric models.Metric
		period models.Period
	}{
		{"x-ratelimit-limit-requests", models.MetricRequests, models.PeriodMinute},
		{"x-ratelimit-limit-tokens", models.MetricTokens, models.PeriodMinute},
	} {
		raw := headers.Get(mapping.header)
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		limit := models.ObservedLimit{
			ScopeType:     models.ScopeEndpoint,
			ScopeID:       endpoint.ID,
			ScopeUUID:     endpoint.UUID,
			Metric:        mapping.metric,
			Period:        mapping.period,
			ObservedValue: value,
			SourceHeader:  mapping.header,
			ObservedAt:    now,
			Enabled:       true,
		}
		if err := l.store.LearnObservedLimit(ctx, limit); err != nil {
			return err
		}
		l.publish(ctx, limit)
	}

	retryAfter := headers.Get("retry-after")
	if retryAfter != "" || statusCode == http.StatusTooManyRequests {
		if until := parseRetryAfter(retryAfter, now); !until.IsZero() {
			endpoint.CooldownUntil = &until
			endpoint.HealthStatus = models.HealthCoolingDown
			endpoint.CooldownReason = upstreamCooldownReason(statusCode)
			if statusCode >= http.StatusBadRequest {
				endpoint.CooldownStatusCode = statusCode
			} else {
				endpoint.CooldownStatusCode = 0
			}
			if err := l.store.SaveEndpointState(ctx, endpoint); err != nil {
				return err
			}
			if l.telemetry != nil {
				l.telemetry.Publish(telemetry.Event{
					Type: "endpoint_health_change",
					Payload: map[string]any{
						"organization_uuid":    tenancy.OrganizationUUID(ctx),
						"endpoint_id":          endpoint.UUID,
						"provider_id":          endpoint.ProviderUUID,
						"health_status":        endpoint.HealthStatus,
						"cooldown_until":       endpoint.CooldownUntil,
						"cooldown_reason":      endpoint.CooldownReason,
						"cooldown_status_code": endpoint.CooldownStatusCode,
						"updated_at":           now,
					},
				})
			}
		}
	}

	return nil
}

func upstreamCooldownReason(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "upstream_rate_limited"
	case statusCode >= http.StatusInternalServerError:
		return "upstream_server_error"
	default:
		return "upstream_retry_after"
	}
}

func (l *ObservedLearner) publish(ctx context.Context, limit models.ObservedLimit) {
	if l.telemetry == nil {
		return
	}
	l.telemetry.Publish(telemetry.Event{
		Type: "observed_limit_learned",
		Payload: map[string]any{
			"organization_uuid": tenancy.OrganizationUUID(ctx),
			"scope_type":        limit.ScopeType,
			"scope_id":          limit.ScopeUUID,
			"metric":            limit.Metric,
			"period":            limit.Period,
			"observed_value":    limit.ObservedValue,
			"source_header":     limit.SourceHeader,
			"observed_at":       limit.ObservedAt,
			"expires_at":        limit.ExpiresAt,
		},
	})
}

func parseRetryAfter(raw string, now time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if when, err := http.ParseTime(raw); err == nil {
		return when.UTC()
	}
	return time.Time{}
}

type UpstreamTransport struct {
	Base       http.RoundTripper
	Store      *store.Store
	Learner    *ObservedLearner
	SecretFunc func(context.Context, models.Credential) (string, error)
}

func (t *UpstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Base == nil {
		t.Base = http.DefaultTransport
	}
	meta, _ := req.Context().Value(contextKey{}).(RequestContext)
	if meta.Provider.AuthMode == "none" {
		req.Header.Del("Authorization")
		req.Header.Del("X-Stainless-Helper")
	}
	if meta.Provider.AuthMode == "bearer_static" && meta.Credential.ID != 0 && t.SecretFunc != nil {
		secret, err := t.SecretFunc(req.Context(), meta.Credential)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if meta.Provider.AuthMode == "custom_header" && meta.Credential.ID != 0 && meta.Provider.AuthHeaderName != "" && t.SecretFunc != nil {
		secret, err := t.SecretFunc(req.Context(), meta.Credential)
		if err != nil {
			return nil, err
		}
		req.Header.Set(meta.Provider.AuthHeaderName, secret)
	}
	resp, err := t.Base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.Learner != nil {
		_ = t.Learner.Learn(req.Context(), meta.Endpoint, resp.Header, resp.StatusCode)
	}
	return resp, nil
}

type RequestContext struct {
	Provider   models.Provider
	Credential models.Credential
	Endpoint   models.Endpoint
}

type contextKey struct{}

func WithRequestContext(ctx context.Context, value RequestContext) context.Context {
	return context.WithValue(ctx, contextKey{}, value)
}
