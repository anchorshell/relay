package store

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestCatalogLoadRetriesWhenInvalidatedDuringDatabaseRead(t *testing.T) {
	cache := newCatalogCache()
	ctx := context.Background()
	key := catalogKey(ctx, "routing", "agentic")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64

	result := make(chan any, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := cache.load(ctx, key, func() (any, error) {
			call := calls.Add(1)
			if call == 1 {
				close(started)
				<-release
				return "stale", nil
			}
			return "fresh", nil
		})
		if err != nil {
			errs <- err
			return
		}
		result <- value
	}()

	<-started
	cache.invalidate(ctx, true)
	close(release)

	select {
	case err := <-errs:
		t.Fatal(err)
	case value := <-result:
		if value != "fresh" {
			t.Fatalf("load returned invalidated value %#v", value)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("database loads=%d, want stale read plus one retry", calls.Load())
	}
}
