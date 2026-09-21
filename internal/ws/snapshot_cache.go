package ws

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/telemetry"
)

type snapshotCacheEntry struct {
	version    uint64
	events     []telemetry.Event
	size       int64
	lastAccess time.Time
}

type snapshotCacheCall struct {
	version uint64
	done    chan struct{}
	events  []telemetry.Event
	err     error
}

// SnapshotCache deduplicates identical initial snapshot work. Entries are
// valid only at an exact Hub version; Publish increments that version before
// asynchronous delivery, so model, limit, cooldown, and request changes make
// prior entries unusable immediately.
type SnapshotCache struct {
	mu         sync.Mutex
	entries    map[string]snapshotCacheEntry
	inflight   map[string]*snapshotCacheCall
	bytes      int64
	maxEntries int
	maxBytes   int64
	ttl        time.Duration
}

const (
	snapshotCacheMaxEntries = 512
	snapshotCacheMaxBytes   = int64(32 << 20)
	snapshotCacheTTL        = 5 * time.Minute
)

func NewSnapshotCache() *SnapshotCache {
	return &SnapshotCache{
		entries:    make(map[string]snapshotCacheEntry),
		inflight:   make(map[string]*snapshotCacheCall),
		maxEntries: snapshotPositiveIntEnv("ANCHORSHELL_WS_SNAPSHOT_CACHE_MAX_ENTRIES", snapshotCacheMaxEntries),
		maxBytes:   int64(snapshotPositiveIntEnv("ANCHORSHELL_WS_SNAPSHOT_CACHE_MAX_BYTES", int(snapshotCacheMaxBytes))),
		ttl:        snapshotPositiveDurationEnv("ANCHORSHELL_WS_SNAPSHOT_CACHE_TTL", snapshotCacheTTL),
	}
}

func snapshotPositiveIntEnv(name string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func snapshotPositiveDurationEnv(name string, fallback time.Duration) time.Duration {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func (c *SnapshotCache) Load(
	key string,
	version uint64,
	currentVersion func() uint64,
	producer func() ([]telemetry.Event, error),
) ([]telemetry.Event, error) {
	if c == nil || key == "" || producer == nil {
		return producer()
	}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && entry.version == version && time.Since(entry.lastAccess) <= c.ttl {
		entry.lastAccess = time.Now().UTC()
		c.entries[key] = entry
		events := cloneEvents(entry.events)
		c.mu.Unlock()
		return events, nil
	}
	if call := c.inflight[key]; call != nil && call.version == version {
		c.mu.Unlock()
		<-call.done
		return cloneEvents(call.events), call.err
	}
	call := &snapshotCacheCall{version: version, done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	events, err := producer()
	call.events = cloneEvents(events)
	call.err = err

	c.mu.Lock()
	if err == nil && (currentVersion == nil || currentVersion() == version) {
		c.putLocked(key, version, events)
	}
	if c.inflight[key] == call {
		delete(c.inflight, key)
	}
	close(call.done)
	c.mu.Unlock()
	return events, err
}

func snapshotEventsSize(events []telemetry.Event) int64 {
	encoded, err := json.Marshal(events)
	if err != nil {
		return int64(len(events)) * 256
	}
	return int64(len(encoded)) + 128
}

func (c *SnapshotCache) deleteLocked(key string) {
	entry, ok := c.entries[key]
	if !ok {
		return
	}
	delete(c.entries, key)
	c.bytes -= entry.size
	if c.bytes < 0 {
		c.bytes = 0
	}
}

func (c *SnapshotCache) putLocked(key string, version uint64, events []telemetry.Event) {
	c.deleteLocked(key)
	cloned := cloneEvents(events)
	entry := snapshotCacheEntry{version: version, events: cloned, size: snapshotEventsSize(cloned), lastAccess: time.Now().UTC()}
	c.entries[key] = entry
	c.bytes += entry.size
	for len(c.entries) > c.maxEntries || c.bytes > c.maxBytes {
		oldestKey := ""
		var oldest time.Time
		for candidate, item := range c.entries {
			if oldestKey == "" || item.lastAccess.Before(oldest) {
				oldestKey, oldest = candidate, item.lastAccess
			}
		}
		if oldestKey == "" {
			break
		}
		c.deleteLocked(oldestKey)
	}
}

// EvictTenant removes cached snapshots for a runtime. Snapshot keys use the
// organization UUID as their leading visibility component.
func (c *SnapshotCache) EvictTenant(organizationUUID string) {
	if c == nil || strings.TrimSpace(organizationUUID) == "" {
		return
	}
	prefix := strings.TrimSpace(organizationUUID)
	c.mu.Lock()
	for key := range c.entries {
		if key == prefix || strings.HasPrefix(key, prefix+"\x00") || strings.HasPrefix(key, prefix+":") {
			c.deleteLocked(key)
		}
	}
	c.mu.Unlock()
}

func cloneEvents(events []telemetry.Event) []telemetry.Event {
	if events == nil {
		return nil
	}
	out := make([]telemetry.Event, len(events))
	copy(out, events)
	return out
}
