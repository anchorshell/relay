package store

import (
	"strings"
	"testing"
)

func TestCatalogCacheBoundsAndTenantEviction(t *testing.T) {
	cache := newCatalogCache()
	cache.maxEntries = 2
	cache.maxBytes = 1 << 20
	cache.mu.Lock()
	cache.putEntryLocked("org-a\x00providers", []string{"a"})
	cache.putEntryLocked("org-a\x00endpoints", []string{"b"})
	cache.putEntryLocked("org-b\x00providers", []string{"c"})
	if len(cache.entries) != 2 || cache.entryBytes > cache.maxBytes {
		cache.mu.Unlock()
		t.Fatalf("catalog cache bounds not enforced: entries=%d bytes=%d", len(cache.entries), cache.entryBytes)
	}
	cache.mu.Unlock()
	cache.evictTenant("org-a")
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key := range cache.entries {
		if strings.HasPrefix(key, "org-a\x00") {
			t.Fatalf("tenant catalog entry remained cached: %q", key)
		}
	}
}
