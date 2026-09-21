package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
	"time"
)

func TestParseDummyBehaviorWaitDecimal(t *testing.T) {
	behavior, err := parseDummyBehavior([]struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{
		{Role: "user", Content: "wait_5.1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if behavior.kind != "wait" {
		t.Fatalf("expected wait behavior, got %q", behavior.kind)
	}
	if behavior.wait != 5100*time.Millisecond {
		t.Fatalf("expected 5.1s wait, got %s", behavior.wait)
	}
	if behavior.waitLabel != "5.1 seconds" {
		t.Fatalf("unexpected wait label: %q", behavior.waitLabel)
	}
}

func TestParseDummyBehaviorAirforceGzip(t *testing.T) {
	behavior, err := parseDummyBehavior([]struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{
		{Role: "user", Content: "ratelimit_af_gzip"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !behavior.gzipBody {
		t.Fatalf("expected gzip body")
	}

	reader, err := gzip.NewReader(bytes.NewReader(behavior.responseBody))
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(body) != string(airforceRateLimitBody) {
		t.Fatalf("unexpected decompressed body: %s", string(body))
	}
}
