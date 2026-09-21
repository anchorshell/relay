package ws

import (
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/telemetry"
)

func TestSnapshotCacheBoundsAndTenantEviction(t *testing.T) {
	cache := NewSnapshotCache()
	cache.maxEntries = 2
	cache.maxBytes = 1 << 20
	producer := func() ([]telemetry.Event, error) {
		return []telemetry.Event{{Type: "snapshot"}}, nil
	}
	for _, key := range []string{"org-a:user-1", "org-a:user-2", "org-b:user-1"} {
		if _, err := cache.Load(key, 1, nil, producer); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	if len(cache.entries) != 2 || cache.bytes > cache.maxBytes {
		t.Fatalf("snapshot cache bounds not enforced: entries=%d bytes=%d", len(cache.entries), cache.bytes)
	}
	cache.mu.Unlock()
	cache.EvictTenant("org-a")
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key := range cache.entries {
		if key == "org-a" || strings.HasPrefix(key, "org-a:") {
			t.Fatalf("tenant snapshot remained cached: %q", key)
		}
	}
}
