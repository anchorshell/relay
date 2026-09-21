package ws

import (
	"compress/flate"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v5"
	"github.com/anchorshell/relay/internal/telemetry"
)

const (
	maxMessageBytes = 16 * 1024
	pongWait        = 60 * time.Second
	pingPeriod      = 30 * time.Second
	writeWait       = 10 * time.Second
	// The snapshot timers below belong to the snapshot-oriented preview handler.
	// The production admin stream uses delta.go and has no periodic snapshot.
	heartbeatPeriod  = 10 * time.Second
	snapshotPeriod   = time.Minute
	snapshotDebounce = time.Second

	snapshotErrorThreshold = 3
)

var upgrader = websocket.Upgrader{
	CheckOrigin:       sameOriginOrNoOrigin,
	EnableCompression: true,
}

var ErrSnapshotContextUnavailable = errors.New("capacity snapshot context unavailable")

func Handler(hub *telemetry.Hub) echo.HandlerFunc {
	return HandlerWithClose(hub, nil)
}

func HandlerWithClose(hub *telemetry.Hub, onClose func()) echo.HandlerFunc {
	return HandlerWithSnapshots(hub, nil, onClose)
}

type SnapshotProducer func(c *echo.Context) ([]telemetry.Event, error)
type EventFilter func(c *echo.Context, event telemetry.Event) bool
type SnapshotTrigger func(event telemetry.Event) bool

func HandlerWithSnapshots(hub *telemetry.Hub, producer SnapshotProducer, onClose func()) echo.HandlerFunc {
	return HandlerWithSnapshotsFiltered(hub, producer, nil, onClose)
}

func HandlerWithSnapshotsFiltered(hub *telemetry.Hub, producer SnapshotProducer, filter EventFilter, onClose func()) echo.HandlerFunc {
	return HandlerWithSnapshotsFilteredAndTrigger(hub, producer, filter, nil, onClose)
}

func HandlerWithSnapshotsFilteredAndTrigger(hub *telemetry.Hub, producer SnapshotProducer, filter EventFilter, trigger SnapshotTrigger, onClose func()) echo.HandlerFunc {
	var closeWithContext func(*echo.Context)
	if onClose != nil {
		closeWithContext = func(*echo.Context) { onClose() }
	}
	return HandlerWithSnapshotsFilteredAndTriggerLifecycle(hub, producer, filter, trigger, nil, closeWithContext)
}

func HandlerWithSnapshotsFilteredAndTriggerLifecycle(hub *telemetry.Hub, producer SnapshotProducer, filter EventFilter, trigger SnapshotTrigger, onOpen func(*echo.Context), onClose func(*echo.Context)) echo.HandlerFunc {
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

		ch, cancel := hub.Subscribe()
		defer cancel()
		pingTicker := time.NewTicker(pingPeriod)
		defer pingTicker.Stop()
		heartbeatTicker := time.NewTicker(heartbeatPeriod)
		defer heartbeatTicker.Stop()
		snapshotTicker := time.NewTicker(snapshotPeriod)
		defer snapshotTicker.Stop()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		lastSnapshot := time.Time{}
		lastBrowserWrite := time.Now()
		lastSnapshotFingerprints := make(map[string][sha256.Size]byte)
		snapshotErrorCount := 0
		var snapshotDebounceTimer *time.Timer
		var snapshotDebounceC <-chan time.Time
		clearScheduledSnapshot := func() {
			if snapshotDebounceTimer == nil {
				return
			}
			if !snapshotDebounceTimer.Stop() {
				select {
				case <-snapshotDebounceTimer.C:
				default:
				}
			}
			snapshotDebounceTimer = nil
			snapshotDebounceC = nil
		}
		scheduleSnapshot := func(delay time.Duration) {
			if delay < 0 {
				delay = 0
			}
			if snapshotDebounceTimer != nil {
				return
			}
			snapshotDebounceTimer = time.NewTimer(delay)
			snapshotDebounceC = snapshotDebounceTimer.C
		}
		writeSnapshots := func() bool {
			if producer == nil {
				return true
			}
			events, err := producer(c)
			if err != nil {
				snapshotErrorCount++
				reconnect := errors.Is(err, ErrSnapshotContextUnavailable) || snapshotErrorCount >= snapshotErrorThreshold
				if !writeSnapshotError(conn, reconnect) {
					return false
				}
				lastBrowserWrite = time.Now()
				if reconnect {
					return false
				}
				scheduleSnapshot(snapshotDebounce)
				return true
			}
			snapshotErrorCount = 0
			for _, event := range events {
				payload, ok, err := marshalBrowserEvent(event)
				if err != nil {
					continue
				}
				if !ok {
					continue
				}
				if isSnapshotType(event.Type) {
					fingerprint, err := semanticSnapshotFingerprint(payload)
					if err != nil {
						continue
					}
					if previous, exists := lastSnapshotFingerprints[event.Type]; exists && previous == fingerprint {
						continue
					}
					lastSnapshotFingerprints[event.Type] = fingerprint
				}
				if err := writeMessage(conn, websocket.TextMessage, payload); err != nil {
					return false
				}
				lastBrowserWrite = time.Now()
			}
			lastSnapshot = time.Now()
			return true
		}
		if !writeSnapshots() {
			return nil
		}

		for {
			select {
			case payload, ok := <-ch:
				if !ok {
					return nil
				}
				event, decoded := decodeEvent(payload)
				if !decoded {
					continue
				}
				if decoded && filter != nil && !filter(c, event) {
					if shouldProduceSnapshot(trigger, event) {
						elapsed := time.Since(lastSnapshot)
						if elapsed >= snapshotDebounce {
							clearScheduledSnapshot()
							if !writeSnapshots() {
								return nil
							}
						} else {
							scheduleSnapshot(snapshotDebounce - elapsed)
						}
					}
					continue
				}
				sanitizedPayload, allowed, err := marshalBrowserEvent(event)
				if err != nil {
					continue
				}
				if allowed {
					if err := writeMessage(conn, websocket.TextMessage, sanitizedPayload); err != nil {
						return nil
					}
					lastBrowserWrite = time.Now()
				}
				if shouldProduceSnapshot(trigger, event) {
					elapsed := time.Since(lastSnapshot)
					if elapsed >= snapshotDebounce {
						clearScheduledSnapshot()
						if !writeSnapshots() {
							return nil
						}
					} else {
						scheduleSnapshot(snapshotDebounce - elapsed)
					}
				}
			case <-snapshotTicker.C:
				clearScheduledSnapshot()
				if !writeSnapshots() {
					return nil
				}
			case <-snapshotDebounceC:
				snapshotDebounceTimer = nil
				snapshotDebounceC = nil
				if !writeSnapshots() {
					return nil
				}
			case <-pingTicker.C:
				if err := writeMessage(conn, websocket.PingMessage, []byte("ping")); err != nil {
					return nil
				}
			case <-heartbeatTicker.C:
				if time.Since(lastBrowserWrite) < heartbeatPeriod {
					continue
				}
				payload, ok, err := marshalBrowserEvent(telemetry.Event{Type: "connection_heartbeat"})
				if err != nil || !ok {
					continue
				}
				if err := writeMessage(conn, websocket.TextMessage, payload); err != nil {
					return nil
				}
				lastBrowserWrite = time.Now()
			case <-done:
				return nil
			}
		}
	}
}

func shouldProduceSnapshot(trigger SnapshotTrigger, event telemetry.Event) bool {
	if trigger != nil {
		return trigger(event)
	}
	return isCapacityDirtyEventType(event.Type)
}

func semanticSnapshotFingerprint(payload []byte) ([sha256.Size]byte, error) {
	var event struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return [sha256.Size]byte{}, err
	}
	// A freshly-built snapshot always advances these fields even when the state
	// shown by the UI is identical. Exclude them from change detection.
	delete(event.Payload, "generated_at")
	delete(event.Payload, "sequence")
	canonical, err := json.Marshal(event)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

func writeSnapshotError(conn *websocket.Conn, reconnect bool) bool {
	payload, ok, err := marshalBrowserEvent(telemetry.Event{
		Type: "capacity_snapshot_error",
		Payload: map[string]any{
			"reason":    "capacity_snapshot_unavailable",
			"reconnect": reconnect,
		},
	})
	if err != nil || !ok {
		return true
	}
	return writeMessage(conn, websocket.TextMessage, payload) == nil
}

func decodeEvent(payload []byte) (telemetry.Event, bool) {
	var event struct {
		Type      string         `json:"type"`
		Timestamp time.Time      `json:"timestamp"`
		Payload   map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return telemetry.Event{}, false
	}
	return telemetry.Event{Type: event.Type, Timestamp: event.Timestamp, Payload: event.Payload}, true
}

func isCapacityDirtyEventType(eventType string) bool {
	switch eventType {
	case "preview_reset", "request_queued", "request_waiting", "request_ready", "request_started", "request_in_flight", "request_finished", "request_completed", "request_failed", "request_cancelled", "request_log", "endpoint_health_change", "observed_limit_learned", "limit_policy_changed", "limit_policy_deleted", "guardrail_check_started", "guardrail_check_passed", "guardrail_check_blocked", "guardrail_check_failed_open", "guardrail_check_error":
		return true
	default:
		return false
	}
}

func writeMessage(conn *websocket.Conn, messageType int, payload []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(messageType, payload)
}

func sameOriginOrNoOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	return isLoopbackHost(parsed.Hostname()) && isLoopbackHost(hostnameFromHost(r.Host))
}

func hostnameFromHost(host string) string {
	hostname, _, err := net.SplitHostPort(host)
	if err == nil {
		return hostname
	}
	return strings.Trim(strings.TrimSpace(host), "[]")
}

func isLoopbackHost(host string) bool {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if normalized == "localhost" {
		return true
	}
	ip := net.ParseIP(normalized)
	return ip != nil && ip.IsLoopback()
}
