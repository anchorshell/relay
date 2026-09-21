package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v5"
)

func TestWebSocketRejectsDisallowedOriginBeforeUpgrade(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "http://relay.test/ws", nil)
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-Websocket-Version", "13")
	req.Header.Set("Sec-Websocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	err := Handler(telemetry.NewHub())(e.NewContext(req, rec))
	if err == nil {
		t.Fatal("expected disallowed origin to fail before websocket upgrade")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed origin, got %d", rec.Code)
	}
}

func TestSameOriginOrNoOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://relay.test/ws", nil)
	if !sameOriginOrNoOrigin(req) {
		t.Fatal("expected missing Origin to be allowed for non-browser clients")
	}
	req.Header.Set("Origin", "http://relay.test")
	if !sameOriginOrNoOrigin(req) {
		t.Fatal("expected same-origin websocket request to be allowed")
	}
	req.Header.Set("Origin", "https://evil.example")
	if sameOriginOrNoOrigin(req) {
		t.Fatal("expected cross-origin websocket request to be rejected")
	}
}

func TestWebSocketSendsInitialSnapshot(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	e.GET("/ws", HandlerWithSnapshots(hub, func(_ *echo.Context) ([]telemetry.Event, error) {
		return []telemetry.Event{{
			Type:    "capacity_snapshot",
			Payload: map[string]any{"sequence": 1},
		}}, nil
	}, nil))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var event telemetry.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "capacity_snapshot" || event.Payload["sequence"].(float64) != 1 {
		t.Fatalf("expected initial capacity snapshot, got %#v", event)
	}
}

func TestWebSocketNegotiatesPerMessageCompression(t *testing.T) {
	e := echo.New()
	e.GET("/ws", Handler(telemetry.NewHub()))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	dialer := *websocket.DefaultDialer
	dialer.EnableCompression = true
	conn, response, err := dialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if extension := response.Header.Get("Sec-Websocket-Extensions"); !strings.Contains(extension, "permessage-deflate") {
		t.Fatalf("expected permessage-deflate negotiation, got %q", extension)
	}
}

func TestSemanticSnapshotFingerprintIgnoresEnvelopeFreshness(t *testing.T) {
	first, err := semanticSnapshotFingerprint([]byte(`{"type":"realtime_snapshot","timestamp":"2026-01-01T00:00:00Z","payload":{"generated_at":"2026-01-01T00:00:00Z","sequence":1,"models":[{"endpoint_id":"endpoint-a","capacity_state":"healthy"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := semanticSnapshotFingerprint([]byte(`{"type":"realtime_snapshot","timestamp":"2026-01-01T00:00:02Z","payload":{"generated_at":"2026-01-01T00:00:02Z","sequence":2,"models":[{"endpoint_id":"endpoint-a","capacity_state":"healthy"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("expected timestamps and sequence alone not to change the snapshot fingerprint")
	}

	changed, err := semanticSnapshotFingerprint([]byte(`{"type":"realtime_snapshot","timestamp":"2026-01-01T00:00:02Z","payload":{"generated_at":"2026-01-01T00:00:02Z","sequence":2,"models":[{"endpoint_id":"endpoint-a","capacity_state":"rate-limited"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("expected a capacity state change to change the snapshot fingerprint")
	}
}

func TestBrowserEventSanitizerAllowsConnectionHeartbeat(t *testing.T) {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{Type: "connection_heartbeat"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected connection heartbeat to be allowed")
	}
	if len(payload) > 128 {
		t.Fatalf("expected compact heartbeat under 128 bytes, got %d", len(payload))
	}
}

func TestWebSocketSendsCapacitySnapshotErrorOnProducerError(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	e.GET("/ws", HandlerWithSnapshots(hub, func(_ *echo.Context) ([]telemetry.Event, error) {
		return nil, errors.New("temporary snapshot failure")
	}, nil))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	event := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	if event.Type != "capacity_snapshot_error" {
		t.Fatalf("expected capacity snapshot error, got %#v", event)
	}
	if event.Payload["reason"] != "capacity_snapshot_unavailable" {
		t.Fatalf("unexpected snapshot error payload: %#v", event.Payload)
	}
	if reconnect, _ := event.Payload["reconnect"].(bool); reconnect {
		t.Fatalf("expected first transient snapshot error to keep websocket open: %#v", event.Payload)
	}
}

func TestWebSocketClosesForUnavailableSnapshotContext(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	e.GET("/ws", HandlerWithSnapshots(hub, func(_ *echo.Context) ([]telemetry.Event, error) {
		return nil, ErrSnapshotContextUnavailable
	}, nil))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	event := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	if event.Type != "capacity_snapshot_error" {
		t.Fatalf("expected capacity snapshot error, got %#v", event)
	}
	if reconnect, _ := event.Payload["reconnect"].(bool); !reconnect {
		t.Fatalf("expected unavailable snapshot context to request reconnect: %#v", event.Payload)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected websocket to close after unavailable snapshot context")
	}
}

func TestWebSocketClosesAfterRepeatedSnapshotProducerErrors(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	e.GET("/ws", HandlerWithSnapshots(hub, func(_ *echo.Context) ([]telemetry.Event, error) {
		return nil, errors.New("temporary snapshot failure")
	}, nil))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for errorsSeen := 0; errorsSeen < snapshotErrorThreshold; errorsSeen++ {
		if errorsSeen > 0 {
			hub.Publish(telemetry.Event{
				Type:    "request_completed",
				Payload: map[string]any{"request_id": "req"},
			})
		}
		event := readTelemetryEventType(t, conn, "capacity_snapshot_error", time.Now().Add(2*time.Second))
		reconnect, _ := event.Payload["reconnect"].(bool)
		if errorsSeen+1 < snapshotErrorThreshold && reconnect {
			t.Fatalf("unexpected reconnect before threshold on error %d: %#v", errorsSeen+1, event.Payload)
		}
		if errorsSeen+1 == snapshotErrorThreshold && !reconnect {
			t.Fatalf("expected reconnect at snapshot error threshold: %#v", event.Payload)
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected websocket to close after repeated snapshot producer errors")
	}
}

func TestWebSocketSnapshotSuccessResetsProducerErrorCount(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	var calls atomic.Int64
	e.GET("/ws", HandlerWithSnapshots(hub, func(_ *echo.Context) ([]telemetry.Event, error) {
		call := calls.Add(1)
		if call == 3 {
			return []telemetry.Event{{
				Type:    "capacity_snapshot",
				Payload: map[string]any{"sequence": 1},
			}}, nil
		}
		return nil, errors.New("temporary snapshot failure")
	}, nil))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	readTelemetryEventType(t, conn, "capacity_snapshot_error", time.Now().Add(2*time.Second))
	for i := 0; i < 2; i++ {
		hub.Publish(telemetry.Event{
			Type:    "request_completed",
			Payload: map[string]any{"request_id": "req"},
		})
		expected := "capacity_snapshot_error"
		if i == 1 {
			expected = "capacity_snapshot"
		}
		readTelemetryEventType(t, conn, expected, time.Now().Add(2*time.Second))
	}

	hub.Publish(telemetry.Event{
		Type:    "request_completed",
		Payload: map[string]any{"request_id": "req"},
	})
	event := readTelemetryEventType(t, conn, "capacity_snapshot_error", time.Now().Add(2*time.Second))
	if reconnect, _ := event.Payload["reconnect"].(bool); reconnect {
		t.Fatalf("expected producer error count to reset after successful snapshot: %#v", event.Payload)
	}

	hub.Publish(telemetry.Event{
		Type:    "endpoint_health_change",
		Payload: map[string]any{"endpoint_id": "endpoint-a", "provider_id": "provider-a", "health_status": "healthy"},
	})
	readTelemetryEventType(t, conn, "endpoint_health_change", time.Now().Add(2*time.Second))
}

func TestWebSocketDirtyEventSchedulesDebouncedSnapshot(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	var sequence atomic.Int64
	e.GET("/ws", HandlerWithSnapshots(hub, func(_ *echo.Context) ([]telemetry.Event, error) {
		next := sequence.Add(1)
		return []telemetry.Event{{
			Type: "capacity_snapshot",
			Payload: map[string]any{
				"sequence": next,
				"queue":    map[string]any{"states": map[string]any{"ready": next - 1}},
			},
		}}, nil
	}, nil))
	server := httptest.NewServer(e)
	defer server.Close()

	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}

	hub.Publish(telemetry.Event{
		Type:    "request_completed",
		Payload: map[string]any{"request_id": "req-1"},
	})

	var sawDirty bool
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("expected debounced capacity snapshot before ticker, sawDirty=%v err=%v", sawDirty, err)
		}
		var event telemetry.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "request_completed" {
			sawDirty = true
			continue
		}
		if event.Type == "capacity_snapshot" && event.Payload["sequence"].(float64) == 2 {
			if !sawDirty {
				t.Fatal("expected dirty event before debounced snapshot")
			}
			return
		}
	}
	t.Fatal("timed out waiting for debounced capacity snapshot")
}

func TestBrowserEventSanitizerRemovesEffectiveLimits(t *testing.T) {
	event := telemetry.Event{
		Type: "request_in_flight",
		Payload: map[string]any{
			"request_id": "req-1",
			"task_id":    "task-1",
			"state":      "in_flight",
			"candidate_trace": []any{map[string]any{
				"endpoint_id":       "endpoint-a",
				"endpoint_name":     "dummy",
				"provider_id":       "provider-a",
				"upstream_model":    "dummy",
				"rank":              1,
				"fallback_count":    0,
				"eligible_at":       time.Now().UTC(),
				"predicted_wait_ms": 0,
				"decision":          "selected",
				"reason":            "ready",
				"effective_limits": []any{
					map[string]any{"metric": "requests", "period": "minute", "effective": 5},
				},
			}},
		},
	}
	payload, ok, err := marshalBrowserEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected request event to be allowed")
	}
	if strings.Contains(string(payload), "effective_limits") {
		t.Fatalf("expected effective_limits to be removed, got %s", payload)
	}
	if strings.Contains(string(payload), "endpoint_name") || strings.Contains(string(payload), "provider_id") || strings.Contains(string(payload), "predicted_wait_ms") {
		t.Fatalf("expected browser candidate trace to contain only rendering fields, got %s", payload)
	}
	if len(payload) > 512 {
		t.Fatalf("expected compact request event under 512 bytes, got %d bytes", len(payload))
	}
}

func TestBrowserEventSanitizerPreservesAPIKeyAttribution(t *testing.T) {
	const apiKeyUUID = "53124747-8b57-4f3c-9488-936b30361d0a"
	for _, eventType := range []string{"request_in_flight", "request_completed", "request_log"} {
		t.Run(eventType, func(t *testing.T) {
			payload, ok, err := marshalBrowserEvent(telemetry.Event{
				Type: eventType,
				Payload: map[string]any{
					"request_id":   "request-a",
					"api_key_uuid": apiKeyUUID,
					"state":        "completed",
				},
			})
			if err != nil || !ok {
				t.Fatalf("expected browser event, ok=%v err=%v", ok, err)
			}
			if !strings.Contains(string(payload), `"api_key_uuid":"`+apiKeyUUID+`"`) {
				t.Fatalf("expected API-key attribution in %s payload: %s", eventType, payload)
			}
		})
	}

	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "realtime_snapshot",
		Payload: map[string]any{
			"queue_items": []any{map[string]any{
				"request_id":   "request-a",
				"api_key_uuid": apiKeyUUID,
			}},
		},
	})
	if err != nil || !ok {
		t.Fatalf("expected browser snapshot, ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(payload), `"api_key_uuid":"`+apiKeyUUID+`"`) {
		t.Fatalf("expected API-key attribution in queue snapshot: %s", payload)
	}
}

func TestBrowserRequestLogPreservesPayloadAvailabilityWithoutBodies(t *testing.T) {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "request_log",
		Payload: map[string]any{
			"organization_uuid":     "must-not-reach-browser",
			"request_id":            "request-a",
			"request_bodies_stored": true,
			"request_body_json":     `{"secret":"request"}`,
			"upstream_request_json": `{"secret":"upstream"}`,
			"response_body_json":    `{"secret":"response"}`,
		},
	})
	if err != nil || !ok {
		t.Fatalf("expected browser request-log event, ok=%v err=%v", ok, err)
	}
	body := string(payload)
	if !strings.Contains(body, `"request_bodies_stored":true`) {
		t.Fatalf("expected payload availability flag: %s", body)
	}
	for _, forbidden := range []string{"organization_uuid", "request_body_json", "upstream_request_json", "response_body_json", "secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("browser event exposed %q: %s", forbidden, body)
		}
	}
}

func TestBrowserRequestLogPreservesCompactCharacterizationWithoutInspectorPayloads(t *testing.T) {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "request_log",
		Payload: map[string]any{
			"request_id":                 "request-a",
			"primary_action":             "explain",
			"wait_ms":                    23,
			"guardrail_pre_duration_ms":  447,
			"provider_latency_ms":        10800,
			"guardrail_post_duration_ms": 434,
			"total_time_ms":              11200,
			"characterization": map[string]any{
				"primary_action":             "explain",
				"target_objects":             []string{"document", "source_code"},
				"output_objects":             []string{"answer", "document"},
				"domains":                    []string{"software", "general"},
				"required_capabilities":      []string{"needs_coder", "other"},
				"context_burden":             map[string]any{"tier": "large", "score": 0.9},
				"classifier_status":          "complete",
				"action_scores":              []any{map[string]any{"label": "explain", "score": 0.91}},
				"classification_duration_ms": 0.8,
			},
			"characterization_json":  `{"large":"inspector-only"}`,
			"guardrail_results_json": `[{"large":"inspector-only"}]`,
			"candidate_text":         "must-not-reach-browser",
		},
	})
	if err != nil || !ok {
		t.Fatalf("expected browser request-log event, ok=%v err=%v", ok, err)
	}
	body := string(payload)
	for _, expected := range []string{`"primary_action":"explain"`, `"target_objects":["document"]`, `"output_objects":["answer"]`, `"domains":["software"]`, `"required_capabilities":["needs_coder"]`, `"context_burden":{"tier":"large"}`, `"wait_ms":23`, `"guardrail_pre_duration_ms":447`, `"provider_latency_ms":10800`, `"guardrail_post_duration_ms":434`, `"total_time_ms":11200`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected %s in request-log event: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"candidate_text", "must-not-reach-browser", "characterization_json", "guardrail_results_json", "action_scores", `"score":0.9`, "source_code", `"general"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("browser request-log event exposed %q: %s", forbidden, body)
		}
	}
}

func TestBrowserRequestReadyPayloadDropsInternalRoutingBulk(t *testing.T) {
	var event telemetry.Event
	if err := json.Unmarshal([]byte(`{"type":"request_ready","timestamp":"2026-07-10T13:21:18.84949Z","payload":{"actor_id":"08fbdd3f-35b7-45c1-bb78-6a6d4dfab667","candidate_trace":[{"decision":"queued","eligible_at":"2026-07-10T21:33:25.629599Z","endpoint_id":"7880508e-cffd-4ac8-865a-60824d188695","endpoint_name":"dummyorg1","fallback_count":0,"predicted_wait_ms":29526790,"provider_id":"b55003e4-e39d-4285-8380-93b8c4e22992","rank":1,"reason":"endpoint cooldown (queued beyond wait budget)","upstream_model":"dummyorg1"},{"decision":"queued","eligible_at":"2026-07-10T22:06:00.550327Z","endpoint_id":"ded3ac61-2b67-4b4f-ac4d-2789bca621b0","endpoint_name":"dummyorg2","fallback_count":1,"predicted_wait_ms":31481710,"provider_id":"b55003e4-e39d-4285-8380-93b8c4e22992","rank":2,"reason":"endpoint cooldown (queued beyond wait budget)","upstream_model":"dummyorg2"},{"decision":"selected","eligible_at":"2026-07-10T13:21:18.839538Z","endpoint_id":"85b3f5b4-28b9-4e3e-b754-80747efece67","endpoint_name":"dummyorg3","fallback_count":2,"predicted_wait_ms":0,"provider_id":"b55003e4-e39d-4285-8380-93b8c4e22992","rank":3,"reason":"","upstream_model":"dummyorg2"}],"defer_reason":"","defer_scope":"","delay_reason":"","eligible_at":"2026-07-10T13:21:18.839538Z","endpoint_id":"85b3f5b4-28b9-4e3e-b754-80747efece67","endpoint_name":"dummyorg3","estimated_cost_micros":0,"estimated_ms":0,"fallback":2,"incoming_model":"agentic","lane":"agentic","organization_uuid":"2374509b-42bd-427a-92ae-16d7400794bc","predicted_eligible_at":"2026-07-10T13:21:18.839538Z","priority":0,"provider_id":"b55003e4-e39d-4285-8380-93b8c4e22992","queue_depth":1,"queued_at":"2026-07-10T13:21:18.839538Z","request_id":"6124a600-8c00-4149-95a7-9fc90cb6d94e","resource_eligible_at":"2026-07-10T13:21:18.839538Z","selected_upstream_model":"dummyorg2","state":"ready","task_id":"c06f1ec9-f25b-4929-9b93-f6e7f0b5c4dd","user_eligible_at":"0001-01-01T00:00:00Z","user_limit_reason":"","wait_ms":9}}`), &event); err != nil {
		t.Fatal(err)
	}
	payload, ok, err := marshalBrowserEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected request-ready event to be allowed")
	}
	t.Logf("compact request-ready payload: %d bytes", len(payload))
	if len(payload) >= 1000 {
		t.Fatalf("expected sample request-ready event below 1000 bytes, got %d: %s", len(payload), payload)
	}
	for _, internalField := range []string{"organization_uuid", "eligible_at", "estimated_ms", "queue_depth", "priority", "predicted_wait_ms", "upstream_model", "rank"} {
		if strings.Contains(string(payload), `"`+internalField+`"`) {
			t.Fatalf("expected %q to be absent from browser event: %s", internalField, payload)
		}
	}
	event.Type = "request_completed"
	event.Payload["state"] = "completed"
	terminalPayload, ok, err := marshalBrowserEvent(event)
	if err != nil || !ok {
		t.Fatalf("expected compact terminal event, ok=%v err=%v", ok, err)
	}
	t.Logf("compact terminal payload: %d bytes", len(terminalPayload))
	if len(terminalPayload) >= 500 {
		t.Fatalf("expected terminal event below 500 bytes, got %d: %s", len(terminalPayload), terminalPayload)
	}
}

func TestBrowserProgressPayloadIsIncremental(t *testing.T) {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "request_progress",
		Payload: map[string]any{
			"request_id":              "request-a",
			"task_id":                 "task-a",
			"actor_id":                "actor-a",
			"lane":                    "agentic",
			"incoming_model":          "agentic",
			"endpoint_id":             "endpoint-a",
			"endpoint_name":           "Endpoint A",
			"provider_id":             "provider-a",
			"selected_upstream_model": "model-a",
			"state":                   "in_flight",
			"substatus":               "streaming response",
			"queued_at":               "2026-07-10T13:21:18Z",
			"started_at":              "2026-07-10T13:21:19Z",
			"wait_ms":                 1000,
			"uploaded_tokens":         12,
			"downloaded_tokens":       40,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected request-progress event to be allowed")
	}
	t.Logf("compact request-progress delta: %d bytes", len(payload))
	for _, repeatedField := range []string{"actor_id", "lane", "incoming_model", "endpoint_id", "endpoint_name", "provider_id", "selected_upstream_model", "queued_at", "started_at", "wait_ms"} {
		if strings.Contains(string(payload), `"`+repeatedField+`"`) {
			t.Fatalf("expected %q to come from the lifecycle event or snapshot, not every progress delta: %s", repeatedField, payload)
		}
	}
	if len(payload) >= 300 {
		t.Fatalf("expected progress delta below 300 bytes, got %d: %s", len(payload), payload)
	}
}

func TestBrowserCancelledPayloadKeepsCompletedFeedFields(t *testing.T) {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "request_cancelled",
		Payload: map[string]any{
			"organization_uuid":       "org-a",
			"request_id":              "request-a",
			"task_id":                 "task-a",
			"actor_id":                "actor-a",
			"lane":                    "agentic",
			"lane_id":                 "lane-a",
			"incoming_model":          "agentic",
			"endpoint_id":             "endpoint-a",
			"provider_id":             "provider-a",
			"selected_upstream_model": "model-a",
			"state":                   "cancelled",
			"task_state":              "cancelled",
			"queued_at":               "2026-07-24T17:00:00Z",
			"started_at":              "2026-07-24T17:00:01Z",
			"finished_at":             "2026-07-24T17:00:02Z",
			"wait_ms":                 1000,
			"latency_ms":              1000,
			"fallback_count":          1,
			"status_code":             499,
			"error_text":              "client canceled request",
		},
	})
	if err != nil || !ok {
		t.Fatalf("cancelled browser event: ok=%v err=%v", ok, err)
	}
	body := string(payload)
	for _, expected := range []string{
		`"request_cancelled"`,
		`"lane_id":"lane-a"`,
		`"provider_id":"provider-a"`,
		`"queued_at":"2026-07-24T17:00:00Z"`,
		`"started_at":"2026-07-24T17:00:01Z"`,
		`"finished_at":"2026-07-24T17:00:02Z"`,
		`"status_code":499`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("cancelled browser event missing %s: %s", expected, body)
		}
	}
	if strings.Contains(body, "organization_uuid") {
		t.Fatalf("cancelled browser event exposed organization UUID: %s", body)
	}
}

func TestBrowserSnapshotKeepsOnlyRenderedCapacityFields(t *testing.T) {
	event := telemetry.Event{
		Type: "realtime_snapshot",
		Payload: map[string]any{
			"generated_at":         "2026-07-10T13:21:18Z",
			"sequence":             9,
			"external_rows_status": "ok",
			"visible_queue_limit":  12,
			"queue": map[string]any{
				"queue_depth_global":      0,
				"queue_depth_by_endpoint": map[string]any{},
				"in_flight_by_endpoint":   map[string]any{},
				"states":                  map[string]any{},
			},
			"queue_items": []any{},
			"models": []any{map[string]any{
				"endpoint_id":    "endpoint-a",
				"provider_id":    "provider-a",
				"capacity_state": "healthy",
				"health_status":  "healthy",
				"limit_rows": []any{map[string]any{
					"key":          "endpoint:endpoint-a:requests:minute:",
					"label":        "RPM",
					"scope_type":   "endpoint",
					"scope_id":     "endpoint-a",
					"target_type":  "model",
					"target_key":   "endpoint-a",
					"metric":       "requests",
					"period":       "minute",
					"configured":   60,
					"effective":    60,
					"used":         4,
					"remaining":    56,
					"percent":      6.67,
					"window_start": "2026-07-10T13:21:00Z",
					"reset_at":     "2026-07-10T13:22:00Z",
					"source":       "configured",
				}},
			}},
		},
	}
	payload, ok, err := marshalBrowserEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected snapshot to be allowed")
	}
	t.Logf("compact one-model snapshot payload: %d bytes", len(payload))
	for _, internalField := range []string{"visible_queue_limit", "queue_depth_by_endpoint", "provider_id"} {
		if strings.Contains(string(payload), `"`+internalField+`"`) {
			t.Fatalf("expected %q to be absent from compact snapshot: %s", internalField, payload)
		}
	}
	for _, stateField := range []string{"scope_id", "used", "remaining", "reset_at", "source"} {
		if !strings.Contains(string(payload), `"`+stateField+`"`) {
			t.Fatalf("expected authoritative capacity field %q in snapshot: %s", stateField, payload)
		}
	}
	if len(payload) >= 700 {
		t.Fatalf("expected one-model snapshot below 700 bytes, got %d: %s", len(payload), payload)
	}
}

func TestBrowserCapacityLimitStateKeepsAuthoritativeRow(t *testing.T) {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "capacity_limit_state",
		Payload: map[string]any{
			"organization_uuid": "must-not-reach-browser",
			"actor_id":          "actor-a",
			"endpoint_id":       "endpoint-a",
			"health_status":     "rate_limited",
			"cooldown_until":    "2026-07-17T19:00:00Z",
			"cooldown_reason":   "configured_limit",
			"removed_keys":      []string{"endpoint:old:requests:minute:"},
			"rows": []map[string]any{{
				"key":         "endpoint:endpoint-a:requests:minute:",
				"scope_type":  "endpoint",
				"scope_id":    "endpoint-a",
				"metric":      "requests",
				"period":      "minute",
				"effective":   5,
				"used":        0,
				"remaining":   5,
				"percent":     0.0,
				"reset_at":    "2026-07-17T19:00:00Z",
				"user_scoped": false,
				"blocked":     false,
				"private":     "drop-me",
			}},
		},
	})
	if err != nil || !ok {
		t.Fatalf("capacity limit event was not browser-safe: ok=%v err=%v", ok, err)
	}
	text := string(payload)
	for _, expected := range []string{`"capacity_limit_state"`, `"health_status":"rate_limited"`, `"cooldown_until":"2026-07-17T19:00:00Z"`, `"cooldown_reason":"configured_limit"`, `"used":0`, `"remaining":5`, `"percent":0`, `"reset_at"`, `"user_scoped":false`, `"blocked":false`, `"removed_keys":["endpoint:old:requests:minute:"]`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %s in %s", expected, text)
		}
	}
	for _, forbidden := range []string{"organization_uuid", "private", "drop-me"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("did not expect %q in %s", forbidden, text)
		}
	}
}

func TestBrowserEventSanitizerDropsUnknownEvents(t *testing.T) {
	_, ok, err := marshalBrowserEvent(telemetry.Event{
		Type:    "unknown_internal_event",
		Payload: map[string]any{"debug": strings.Repeat("x", 1024)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected unknown browser websocket event to be dropped")
	}
}

func readTelemetryEvent(t *testing.T, conn *websocket.Conn, deadline time.Time) telemetry.Event {
	t.Helper()
	_ = conn.SetReadDeadline(deadline)
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var event telemetry.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func readTelemetryEventType(t *testing.T, conn *websocket.Conn, eventType string, deadline time.Time) telemetry.Event {
	t.Helper()
	for time.Now().Before(deadline) {
		event := readTelemetryEvent(t, conn, deadline)
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("timed out waiting for telemetry event %q", eventType)
	return telemetry.Event{}
}

func TestSameOriginOrNoOriginAllowsLoopbackDevPorts(t *testing.T) {
	tests := []struct {
		name   string
		target string
		origin string
	}{
		{
			name:   "127 loopback different ports",
			target: "http://127.0.0.1:11730/ws",
			origin: "http://127.0.0.1:3030",
		},
		{
			name:   "localhost loopback different ports",
			target: "http://localhost:11730/ws",
			origin: "http://localhost:3030",
		},
		{
			name:   "localhost to 127 loopback",
			target: "http://127.0.0.1:11730/ws",
			origin: "http://localhost:3030",
		},
		{
			name:   "ipv6 loopback different ports",
			target: "http://[::1]:11730/ws",
			origin: "http://[::1]:3030",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.Header.Set("Origin", tt.origin)
			if !sameOriginOrNoOrigin(req) {
				t.Fatalf("expected origin %q to be allowed for target %q", tt.origin, tt.target)
			}
		})
	}
}

func TestSameOriginOrNoOriginRejectsNonLoopbackPortMismatch(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.example:8080/ws", nil)
	req.Header.Set("Origin", "http://app.example:3030")
	if sameOriginOrNoOrigin(req) {
		t.Fatal("expected non-loopback port mismatch to be rejected")
	}
}

func TestWebSocketLimitsAreBounded(t *testing.T) {
	if maxMessageBytes <= 0 || maxMessageBytes > 64*1024 {
		t.Fatalf("unexpected websocket read limit %d", maxMessageBytes)
	}
	if pongWait <= 0 || pingPeriod <= 0 || writeWait <= 0 {
		t.Fatalf("expected positive websocket deadlines: pong=%s ping=%s write=%s", pongWait, pingPeriod, writeWait)
	}
	if pingPeriod >= pongWait {
		t.Fatalf("expected ping period %s to be shorter than pong wait %s", pingPeriod, pongWait)
	}
	if heartbeatPeriod <= 0 || heartbeatPeriod >= pongWait {
		t.Fatalf("expected heartbeat period %s to fit inside pong wait %s", heartbeatPeriod, pongWait)
	}
	if snapshotPeriod < time.Minute {
		t.Fatalf("expected recovery snapshots no more than once per minute, got %s", snapshotPeriod)
	}
}
