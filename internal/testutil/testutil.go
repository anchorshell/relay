package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/anchorshell/relay/internal/db"
	"github.com/anchorshell/relay/internal/store"
)

func NewStore(t *testing.T) *store.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(sqlDB)
	if err := st.SeedDefaults(context.Background(), 30000, 600000, 1000, 300000, 1024, 4096, true); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	return st
}
