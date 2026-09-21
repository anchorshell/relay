package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v5"
	"github.com/anchorshell/relay/internal/telemetry"
)

func TestScopedDeltaWebSocketSharesPreparedFramesAndSnapshotCache(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	hub.SetAudienceResolver(func(event telemetry.Event) telemetry.Audience {
		partition, _ := event.Payload["partition"].(string)
		return telemetry.Audience{Scoped: true, Partition: partition}
	})
	cache := NewSnapshotCache()
	var snapshots atomic.Int64
	e.GET("/ws", HandlerWithInitialSnapshotScopedLifecycle(
		hub,
		func(_ *echo.Context) ([]telemetry.Event, error) {
			snapshots.Add(1)
			return []telemetry.Event{{Type: "realtime_snapshot", Payload: map[string]any{"models": []any{}}}}, nil
		},
		func(_ *echo.Context) (ScopedConnection, error) {
			return ScopedConnection{
				Scope:            telemetry.SubscriptionScope{Partition: "org-a"},
				SnapshotCacheKey: "org-a:user-a",
			}, nil
		},
		cache,
		nil,
		nil,
	))
	server := httptest.NewServer(e)
	defer server.Close()

	first := dialTestWebSocket(t, server.URL)
	defer first.Close()
	firstBaseline := readTelemetryEvent(t, first, time.Now().Add(2*time.Second))
	second := dialTestWebSocket(t, server.URL)
	defer second.Close()
	secondBaseline := readTelemetryEvent(t, second, time.Now().Add(2*time.Second))
	if snapshots.Load() != 1 {
		t.Fatalf("expected identical initial snapshot to be built once, got %d", snapshots.Load())
	}
	if firstBaseline.StreamID != secondBaseline.StreamID || firstBaseline.Sequence != secondBaseline.Sequence {
		t.Fatalf("expected shared visibility cursor, got %#v and %#v", firstBaseline, secondBaseline)
	}

	hub.Publish(telemetry.Event{Type: "request_queued", Payload: map[string]any{
		"partition":  "org-a",
		"request_id": "req-1",
		"state":      "queued",
	}})
	hub.Flush()
	firstDelta := readTelemetryEvent(t, first, time.Now().Add(2*time.Second))
	secondDelta := readTelemetryEvent(t, second, time.Now().Add(2*time.Second))
	if firstDelta.StreamID != secondDelta.StreamID || firstDelta.Sequence != secondDelta.Sequence {
		t.Fatalf("expected one shared prepared delta cursor, got %#v and %#v", firstDelta, secondDelta)
	}

	third := dialTestWebSocket(t, server.URL)
	defer third.Close()
	thirdBaseline := readTelemetryEvent(t, third, time.Now().Add(2*time.Second))
	if snapshots.Load() != 2 {
		t.Fatalf("expected publish version to invalidate cached snapshot, got %d builds", snapshots.Load())
	}
	if thirdBaseline.Sequence != firstDelta.Sequence {
		t.Fatalf("expected new baseline at current shared cursor %d, got %d", firstDelta.Sequence, thirdBaseline.Sequence)
	}
}

func TestScopedDeltaWebSocketDoesNotNegotiateCompression(t *testing.T) {
	e := echo.New()
	e.GET("/ws", HandlerWithInitialSnapshotScopedLifecycle(
		telemetry.NewHub(),
		func(_ *echo.Context) ([]telemetry.Event, error) {
			return []telemetry.Event{{Type: "realtime_snapshot", Payload: map[string]any{"models": []any{}}}}, nil
		},
		nil,
		NewSnapshotCache(),
		nil,
		nil,
	))
	server := httptest.NewServer(e)
	defer server.Close()
	target := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	dialer := *websocket.DefaultDialer
	dialer.EnableCompression = true
	conn, response, err := dialer.Dial(target, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if extension := response.Header.Get("Sec-Websocket-Extensions"); extension != "" {
		t.Fatalf("expected small-delta stream without compression negotiation, got %q", extension)
	}
}

func TestScopedDeltaWebSocketIsolatesPartitions(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	hub.SetAudienceResolver(func(event telemetry.Event) telemetry.Audience {
		partition, _ := event.Payload["partition"].(string)
		return telemetry.Audience{Scoped: true, Partition: partition}
	})
	producer := func(_ *echo.Context) ([]telemetry.Event, error) {
		return []telemetry.Event{{Type: "realtime_snapshot", Payload: map[string]any{"models": []any{}}}}, nil
	}
	for _, partition := range []string{"org-a", "org-b"} {
		partition := partition
		e.GET("/ws/"+partition, HandlerWithInitialSnapshotScopedLifecycle(
			hub,
			producer,
			func(_ *echo.Context) (ScopedConnection, error) {
				return ScopedConnection{Scope: telemetry.SubscriptionScope{Partition: partition}, SnapshotCacheKey: partition}, nil
			},
			NewSnapshotCache(),
			nil,
			nil,
		))
	}
	server := httptest.NewServer(e)
	defer server.Close()
	dial := func(path string) *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	a := dial("/ws/org-a")
	defer a.Close()
	b := dial("/ws/org-b")
	defer b.Close()
	readTelemetryEvent(t, a, time.Now().Add(2*time.Second))
	readTelemetryEvent(t, b, time.Now().Add(2*time.Second))

	hub.Publish(telemetry.Event{Type: "request_queued", Payload: map[string]any{
		"partition": "org-a", "request_id": "req-a", "state": "queued",
	}})
	hub.Flush()
	if event := readTelemetryEvent(t, a, time.Now().Add(2*time.Second)); event.Payload["request_id"] != "req-a" {
		t.Fatalf("unexpected org-a event %#v", event)
	}
	_ = b.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := b.ReadMessage(); err == nil {
		t.Fatal("expected no cross-partition event")
	}
}

func TestDeltaWebSocketSendsOneBaselineThenSequencedDeltas(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	var snapshots atomic.Int64
	e.GET("/ws", HandlerWithInitialSnapshotFilteredLifecycle(
		hub,
		func(_ *echo.Context) ([]telemetry.Event, error) {
			snapshots.Add(1)
			return []telemetry.Event{{Type: "realtime_snapshot", Payload: map[string]any{
				"sequence": 1,
				"models":   []any{},
			}}}, nil
		},
		nil,
		nil,
		nil,
	))
	server := httptest.NewServer(e)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	baseline := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	if baseline.Type != "realtime_snapshot" || baseline.StreamID == "" || baseline.Sequence != 1 {
		t.Fatalf("expected sequenced initial snapshot, got %#v", baseline)
	}

	hub.Publish(telemetry.Event{Type: "request_queued", Payload: map[string]any{
		"request_id": "req-1",
		"state":      "queued",
	}})
	delta := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	if delta.Type != "request_queued" || delta.StreamID != baseline.StreamID || delta.Sequence != 2 {
		t.Fatalf("expected sequence 2 delta on the baseline stream, got %#v", delta)
	}
	time.Sleep(snapshotDebounce + 100*time.Millisecond)
	if snapshots.Load() != 1 {
		t.Fatalf("expected ordinary deltas not to rebuild snapshots, got %d producer calls", snapshots.Load())
	}
}

func TestDeltaWebSocketFiltersBeforeAssigningSequence(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	e.GET("/ws", HandlerWithInitialSnapshotFilteredLifecycle(
		hub,
		func(_ *echo.Context) ([]telemetry.Event, error) {
			return []telemetry.Event{{Type: "realtime_snapshot", Payload: map[string]any{"models": []any{}}}}, nil
		},
		func(_ *echo.Context, event telemetry.Event) bool {
			return event.Payload["visible"] == true
		},
		nil,
		nil,
	))
	server := httptest.NewServer(e)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	baseline := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	hub.Publish(telemetry.Event{Type: "request_queued", Payload: map[string]any{
		"request_id": "hidden",
		"state":      "queued",
		"visible":    false,
	}})
	hub.Publish(telemetry.Event{Type: "request_queued", Payload: map[string]any{
		"request_id": "visible",
		"state":      "queued",
		"visible":    true,
	}})
	delta := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	if delta.Sequence != baseline.Sequence+1 || delta.Payload["request_id"] != "visible" {
		t.Fatalf("expected filtered events not to consume browser sequence numbers, got %#v", delta)
	}
}

func TestDeltaWebSocketRequiresResyncAfterSubscriberOverflow(t *testing.T) {
	e := echo.New()
	hub := telemetry.NewHub()
	e.GET("/ws", HandlerWithInitialSnapshotFilteredLifecycle(
		hub,
		func(_ *echo.Context) ([]telemetry.Event, error) {
			for i := 0; i < 256; i++ {
				hub.Publish(telemetry.Event{Type: "request_queued", Payload: map[string]any{
					"request_id": "overflow",
					"state":      "queued",
				}})
			}
			return []telemetry.Event{{Type: "realtime_snapshot", Payload: map[string]any{"models": []any{}}}}, nil
		},
		nil,
		nil,
		nil,
	))
	server := httptest.NewServer(e)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	baseline := readTelemetryEvent(t, conn, time.Now().Add(2*time.Second))
	resync := readTelemetryEventType(t, conn, "resync_required", time.Now().Add(2*time.Second))
	if resync.StreamID != baseline.StreamID || resync.Sequence <= baseline.Sequence {
		t.Fatalf("expected sequenced resync control event, got %#v", resync)
	}
	if resync.Payload["reason"] != "subscriber_overflow" || resync.Payload["reconnect"] != true {
		t.Fatalf("unexpected resync payload %#v", resync.Payload)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected server to close an out-of-sync stream")
	}
}

func TestDeltaWebSocketClosesWhenInitialSnapshotFails(t *testing.T) {
	e := echo.New()
	e.GET("/ws", HandlerWithInitialSnapshotFilteredLifecycle(
		telemetry.NewHub(),
		func(_ *echo.Context) ([]telemetry.Event, error) { return nil, ErrSnapshotContextUnavailable },
		nil,
		nil,
		nil,
	))
	server := httptest.NewServer(e)
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL)
	defer conn.Close()
	event := readTelemetryEventType(t, conn, "capacity_snapshot_error", time.Now().Add(2*time.Second))
	if event.StreamID == "" || event.Sequence != 1 || event.Payload["reconnect"] != true {
		t.Fatalf("expected a sequenced reconnect error, got %#v", event)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected failed initial baseline to close the stream")
	}
}

func dialTestWebSocket(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	target := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
