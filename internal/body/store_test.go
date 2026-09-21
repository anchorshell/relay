package body

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestSpoolLargeBodyAndCleanup(t *testing.T) {
	store := NewStore(t.TempDir(), 8, 16)
	handle, err := store.Save(context.Background(), bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz")))
	if err != nil {
		t.Fatalf("save body: %v", err)
	}
	if handle.path == "" {
		t.Fatal("expected temp file backing for large body")
	}
	reader, err := handle.Open()
	if err != nil {
		t.Fatalf("open handle: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read handle: %v", err)
	}
	if string(body) != "abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("unexpected body %q", string(body))
	}
	if err := handle.Cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestStoreMaxBytesRejectsOversizedBody(t *testing.T) {
	store := NewStore(t.TempDir(), 8, 16).WithMaxBytes(12)
	if _, err := store.Save(context.Background(), bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz"))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestStoreCanceledContextBeforeTempFileDoesNotPanic(t *testing.T) {
	store := NewStore(t.TempDir(), 8, 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Save(ctx, bytes.NewReader([]byte("abc"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}
