package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/testutil"
	"github.com/labstack/echo/v5"
)

func TestTelemetryEventFilterRequiresMatchingOrganizationScope(t *testing.T) {
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{}).WithExternalQueueScopeHooks(func(*echo.Context, QueueScopeInput) (ExternalQueueScope, error) {
		return ExternalQueueScope{OrganizationUUID: "org-a"}, nil
	})

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/ws", nil), httptest.NewRecorder())

	if !api.telemetryEventFilter(c, telemetry.Event{
		Type:    "request_in_flight",
		Payload: map[string]any{"organization_uuid": "org-a", "request_id": "req-a"},
	}) {
		t.Fatal("expected same-organization request event to pass")
	}
	if api.telemetryEventFilter(c, telemetry.Event{
		Type:    "request_in_flight",
		Payload: map[string]any{"organization_uuid": "org-b", "request_id": "req-b"},
	}) {
		t.Fatal("expected other-organization request event to be filtered")
	}
	if api.telemetryEventFilter(c, telemetry.Event{
		Type:    "request_in_flight",
		Payload: map[string]any{"request_id": "req-missing-org"},
	}) {
		t.Fatal("expected tenant request event without organization_uuid to be filtered")
	}
	if api.telemetryEventFilter(c, telemetry.Event{
		Type:    "endpoint_health_change",
		Payload: map[string]any{"endpoint_id": "endpoint-missing-org"},
	}) {
		t.Fatal("expected tenant endpoint event without organization_uuid to be filtered")
	}
	if !api.telemetryEventFilter(c, telemetry.Event{
		Type:    "capacity_snapshot_error",
		Payload: map[string]any{"error": "temporary"},
	}) {
		t.Fatal("expected non-tenant operational event to pass")
	}
}

func TestTelemetryEventFilterRequiresMatchingActorForSelfScope(t *testing.T) {
	st := testutil.NewStore(t)
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{}).WithExternalQueueScopeHooks(func(*echo.Context, QueueScopeInput) (ExternalQueueScope, error) {
		return ExternalQueueScope{OrganizationUUID: "org-a", ActorID: "user-a"}, nil
	})

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/ws", nil), httptest.NewRecorder())

	if !api.telemetryEventFilter(c, telemetry.Event{
		Type:    "request_completed",
		Payload: map[string]any{"organization_uuid": "org-a", "actor_id": "user-a", "request_id": "req-a"},
	}) {
		t.Fatal("expected same-organization self request event to pass")
	}
	if api.telemetryEventFilter(c, telemetry.Event{
		Type:    "request_completed",
		Payload: map[string]any{"organization_uuid": "org-a", "actor_id": "user-b", "request_id": "req-b"},
	}) {
		t.Fatal("expected same-organization other-user request event to be filtered")
	}
	if api.telemetryEventFilter(c, telemetry.Event{
		Type:    "request_completed",
		Payload: map[string]any{"organization_uuid": "org-b", "actor_id": "user-a", "request_id": "req-c"},
	}) {
		t.Fatal("expected same-user other-organization request event to be filtered")
	}
	if !api.telemetryEventFilter(c, telemetry.Event{
		Type:    "endpoint_health_change",
		Payload: map[string]any{"organization_uuid": "org-a", "endpoint_id": "endpoint-a"},
	}) {
		t.Fatal("expected organization-wide endpoint state to reach a self-scoped stream")
	}
	if !api.telemetryEventFilter(c, telemetry.Event{
		Type: "capacity_limit_state",
		Payload: map[string]any{
			"organization_uuid": "org-a",
			"row":               map[string]any{"user_scoped": false},
		},
	}) {
		t.Fatal("expected organization-wide capacity state to reach a self-scoped stream")
	}
	if api.telemetryEventFilter(c, telemetry.Event{
		Type: "capacity_limit_state",
		Payload: map[string]any{
			"organization_uuid": "org-a",
			"actor_id":          "user-b",
			"row":               map[string]any{"user_scoped": true},
		},
	}) {
		t.Fatal("expected another user's capacity state to be filtered")
	}
}

func TestTelemetryEventFilterResolvesScopeOncePerConnection(t *testing.T) {
	st := testutil.NewStore(t)
	var calls int
	api := New(st, nil, nil, nil, adminTestToken(), SystemInfo{}).WithExternalQueueScopeHooks(func(*echo.Context, QueueScopeInput) (ExternalQueueScope, error) {
		calls++
		return ExternalQueueScope{OrganizationUUID: "org-a"}, nil
	})

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/ws", nil), httptest.NewRecorder())
	for _, requestID := range []string{"req-a", "req-b"} {
		if !api.telemetryEventFilter(c, telemetry.Event{
			Type:    "request_in_flight",
			Payload: map[string]any{"organization_uuid": "org-a", "request_id": requestID},
		}) {
			t.Fatalf("expected %s to pass", requestID)
		}
	}
	if calls != 1 {
		t.Fatalf("expected one scope resolution for the websocket request, got %d", calls)
	}
}
