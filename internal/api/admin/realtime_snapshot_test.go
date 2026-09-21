package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anchorshell/relay/internal/limits"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/scheduler"
)

func TestRealtimeSnapshotPayloadIsCompact(t *testing.T) {
	now := time.Now().UTC()
	effective := int64(5)
	items := make([]scheduler.TaskSnapshot, 0, 100)
	for i := 0; i < 100; i++ {
		items = append(items, scheduler.TaskSnapshot{
			TaskID:            fmt.Sprintf("task-%03d", i),
			RequestID:         fmt.Sprintf("request-%03d", i),
			Lane:              "agentic",
			IncomingModel:     "agentic",
			EndpointUUID:      "endpoint-a",
			EndpointName:      "dummy",
			UpstreamModel:     "dummy",
			ProviderUUID:      "provider-a",
			State:             "waiting",
			QueuedAt:          now.Add(time.Duration(i) * time.Millisecond),
			PredictedEligible: now.Add(time.Minute),
			CandidateTrace: []scheduler.CandidateTrace{{
				EndpointUUID:    "endpoint-a",
				EndpointName:    "dummy",
				ProviderUUID:    "provider-a",
				UpstreamModel:   "dummy",
				Rank:            1,
				PredictedWaitMS: 60_000,
				Decision:        "deferred",
				Reason:          "cooldown",
				EffectiveLimits: []limits.EffectiveLimit{{
					Metric:    models.MetricRequests,
					Period:    models.PeriodMinute,
					Effective: &effective,
					ScopeType: models.ScopeEndpoint,
					ScopeUUID: "endpoint-a",
				}},
			}},
		})
	}
	payload := realtimeSnapshotPayload(CapacitySnapshot{
		GeneratedAt: now,
		Sequence:    1,
		Queue: scheduler.Snapshot{
			QueueDepthGlobal: 100,
			States:           map[string]int{"waiting": 100},
		},
		QueueItems: items,
		Models: []ModelCapacitySnapshot{{
			EndpointID:    "endpoint-a",
			ProviderID:    "provider-a",
			CapacityState: "cooling-down",
			HealthStatus:  models.HealthCoolingDown,
		}},
	})
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 50*1024 {
		t.Fatalf("expected compact realtime snapshot under 50KB, got %d bytes", len(body))
	}
	if strings.Contains(string(body), "effective_limits") {
		t.Fatalf("expected realtime snapshot to omit effective_limits, got %s", body)
	}
	queueItems, ok := payload["queue_items"].([]any)
	if !ok || len(queueItems) != realtimeSnapshotVisibleQueueLimit {
		t.Fatalf("expected capped visible queue items, got %#v", payload["queue_items"])
	}
}
