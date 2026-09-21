package characterization

import (
	"context"
	"os"
	"time"
)

// WorkerStatus exposes no internal addresses or credentials.
type WorkerStatus struct {
	Configured bool `json:"configured"`
	Ready      bool `json:"ready"`
}

func ConfiguredWorkerStatus(ctx context.Context) WorkerStatus {
	endpoint := os.Getenv("RELAY_LAYA_URL")
	if endpoint == "" {
		return WorkerStatus{}
	}
	status := WorkerStatus{Configured: true}
	e, err := NewHTTPEngine(endpoint, os.Getenv("RELAY_LAYA_TOKEN"))
	if err != nil {
		return status
	}
	defer e.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	status.Ready = e.Ready(ctx)
	return status
}
