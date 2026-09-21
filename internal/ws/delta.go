package ws

import (
	"compress/flate"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v5"
	"github.com/anchorshell/relay/internal/telemetry"
)

var deltaUpgrader = websocket.Upgrader{
	CheckOrigin:       sameOriginOrNoOrigin,
	EnableCompression: false,
}

type ScopedConnection struct {
	Scope            telemetry.SubscriptionScope
	SnapshotCacheKey string
}

type ScopedConnectionProducer func(c *echo.Context) (ScopedConnection, error)

// HandlerWithInitialSnapshotScopedLifecycle is the production realtime path.
// It resolves visibility once, joins an organization/actor group, and receives
// already-sanitized bytes prepared once for that group. Small delta frames are
// intentionally left uncompressed to avoid per-connection DEFLATE work.
func HandlerWithInitialSnapshotScopedLifecycle(
	hub *telemetry.Hub,
	producer SnapshotProducer,
	scopeProducer ScopedConnectionProducer,
	cache *SnapshotCache,
	onOpen func(*echo.Context),
	onClose func(*echo.Context),
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if hub == nil {
			hub = telemetry.NewHub()
		}
		scoped := ScopedConnection{Scope: telemetry.SubscriptionScope{Format: "browser-v1"}, SnapshotCacheKey: "public"}
		if scopeProducer != nil {
			resolved, err := scopeProducer(c)
			if err != nil {
				return err
			}
			scoped = resolved
		}
		scoped.Scope.Format = "browser-v1"

		conn, err := deltaUpgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			return err
		}
		if onOpen != nil {
			onOpen(c)
		}
		conn.SetReadLimit(maxMessageBytes)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		defer func() {
			_ = conn.Close()
			if onClose != nil {
				onClose(c)
			}
		}()

		events, overflowed, cancel, cursor := hub.SubscribePrepared(scoped.Scope, marshalBrowserEvent)
		defer cancel()
		pingTicker := time.NewTicker(pingPeriod)
		defer pingTicker.Stop()
		heartbeatTicker := time.NewTicker(heartbeatPeriod)
		defer heartbeatTicker.Stop()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		lastBrowserWrite := time.Now()
		writeControl := func(event telemetry.Event) bool {
			payload, allowed, err := marshalBrowserEvent(event)
			if err != nil || !allowed {
				return false
			}
			if err := writeMessage(conn, websocket.TextMessage, payload); err != nil {
				return false
			}
			lastBrowserWrite = time.Now()
			return true
		}
		writeResyncRequired := func(reason string) bool {
			return writeControl(telemetry.Event{
				Type: "resync_required",
				Payload: map[string]any{
					"reason":    reason,
					"reconnect": true,
				},
			})
		}

		if producer == nil {
			writeResyncRequired("initial_snapshot_unavailable")
			return nil
		}
		version := hub.Version()
		initial, err := cache.Load(scoped.SnapshotCacheKey, version, hub.Version, func() ([]telemetry.Event, error) {
			return producer(c)
		})
		if err != nil {
			_ = writeControl(telemetry.Event{
				Type: "capacity_snapshot_error",
				Payload: map[string]any{
					"reason":    "capacity_snapshot_unavailable",
					"reconnect": true,
				},
			})
			return nil
		}
		wroteBaseline := false
		for _, event := range initial {
			if wroteBaseline {
				writeResyncRequired("multiple_initial_snapshots")
				return nil
			}
			event.StreamID = cursor.StreamID
			event.Sequence = cursor.Sequence
			payload, allowed, marshalErr := marshalBrowserEvent(event)
			if marshalErr != nil {
				return nil
			}
			if !allowed {
				continue
			}
			if err := writeMessage(conn, websocket.TextMessage, payload); err != nil {
				return nil
			}
			lastBrowserWrite = time.Now()
			wroteBaseline = true
		}
		if !wroteBaseline {
			writeResyncRequired("initial_snapshot_unavailable")
			return nil
		}

		for {
			select {
			case payload, ok := <-events:
				if !ok {
					return nil
				}
				if overflowed() {
					writeResyncRequired("subscriber_overflow")
					return nil
				}
				if err := writeMessage(conn, websocket.TextMessage, payload); err != nil {
					return nil
				}
				lastBrowserWrite = time.Now()
			case <-pingTicker.C:
				if err := writeMessage(conn, websocket.PingMessage, []byte("ping")); err != nil {
					return nil
				}
			case <-heartbeatTicker.C:
				if overflowed() {
					writeResyncRequired("subscriber_overflow")
					return nil
				}
				if time.Since(lastBrowserWrite) < heartbeatPeriod {
					continue
				}
				if !writeControl(telemetry.Event{Type: "connection_heartbeat"}) {
					return nil
				}
			case <-done:
				return nil
			}
		}
	}
}

// HandlerWithInitialSnapshotFilteredLifecycle establishes an authoritative
// baseline once and then streams only filtered deltas. Unlike the preview
// handler, ordinary lifecycle events never rebuild the snapshot.
func HandlerWithInitialSnapshotFilteredLifecycle(
	hub *telemetry.Hub,
	producer SnapshotProducer,
	filter EventFilter,
	onOpen func(*echo.Context),
	onClose func(*echo.Context),
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if hub == nil {
			hub = telemetry.NewHub()
		}
		conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			return err
		}
		if onOpen != nil {
			onOpen(c)
		}
		conn.SetReadLimit(maxMessageBytes)
		conn.EnableWriteCompression(true)
		_ = conn.SetCompressionLevel(flate.BestSpeed)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		defer func() {
			_ = conn.Close()
			if onClose != nil {
				onClose(c)
			}
		}()

		events, overflowed, cancel := hub.SubscribeWithOverflow()
		defer cancel()
		pingTicker := time.NewTicker(pingPeriod)
		defer pingTicker.Stop()
		heartbeatTicker := time.NewTicker(heartbeatPeriod)
		defer heartbeatTicker.Stop()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		streamID := newStreamID()
		var sequence uint64
		lastBrowserWrite := time.Now()
		writeEvent := func(event telemetry.Event) bool {
			nextSequence := sequence + 1
			event.StreamID = streamID
			event.Sequence = nextSequence
			payload, allowed, err := marshalBrowserEvent(event)
			if err != nil {
				return false
			}
			if !allowed {
				return true
			}
			if err := writeMessage(conn, websocket.TextMessage, payload); err != nil {
				return false
			}
			sequence = nextSequence
			lastBrowserWrite = time.Now()
			return true
		}
		writeResyncRequired := func(reason string) bool {
			return writeEvent(telemetry.Event{
				Type: "resync_required",
				Payload: map[string]any{
					"reason":    reason,
					"reconnect": true,
				},
			})
		}

		if producer == nil {
			writeResyncRequired("initial_snapshot_unavailable")
			return nil
		}
		initial, err := producer(c)
		if err != nil {
			_ = writeEvent(telemetry.Event{
				Type: "capacity_snapshot_error",
				Payload: map[string]any{
					"reason":    "capacity_snapshot_unavailable",
					"reconnect": true,
				},
			})
			return nil
		}
		wroteBaseline := false
		for _, event := range initial {
			before := sequence
			if !writeEvent(event) {
				return nil
			}
			wroteBaseline = wroteBaseline || sequence > before
		}
		if !wroteBaseline {
			writeResyncRequired("initial_snapshot_unavailable")
			return nil
		}

		for {
			select {
			case payload, ok := <-events:
				if !ok {
					return nil
				}
				if overflowed() {
					writeResyncRequired("subscriber_overflow")
					return nil
				}
				event, decoded := decodeEvent(payload)
				if !decoded {
					writeResyncRequired("invalid_event")
					return nil
				}
				if filter != nil && !filter(c, event) {
					continue
				}
				if !writeEvent(event) {
					return nil
				}
			case <-pingTicker.C:
				if err := writeMessage(conn, websocket.PingMessage, []byte("ping")); err != nil {
					return nil
				}
			case <-heartbeatTicker.C:
				if overflowed() {
					writeResyncRequired("subscriber_overflow")
					return nil
				}
				if time.Since(lastBrowserWrite) < heartbeatPeriod {
					continue
				}
				if !writeEvent(telemetry.Event{Type: "connection_heartbeat"}) {
					return nil
				}
			case <-done:
				return nil
			}
		}
	}
}

func newStreamID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
