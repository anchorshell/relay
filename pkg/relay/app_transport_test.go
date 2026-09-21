package relay

import (
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/config"
)

func TestUpstreamHTTPTransportPoolConfiguration(t *testing.T) {
	transport := newUpstreamHTTPTransport(config.Config{
		UpstreamMaxIdleConns:        1024,
		UpstreamMaxIdleConnsPerHost: 512,
		UpstreamIdleConnTimeout:     90 * time.Second,
	})
	if transport.MaxIdleConns != 1024 || transport.MaxIdleConnsPerHost != 512 {
		t.Fatalf("unexpected idle pool: total=%d per_host=%d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != 0 {
		t.Fatalf("active provider connections unexpectedly capped at %d", transport.MaxConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("idle timeout=%s, want 90s", transport.IdleConnTimeout)
	}
}
