package store

import (
	"encoding/json"
	"testing"

	"github.com/anchorshell/relay/internal/models"
)

func TestRequestLogDiagnosticsRetainGroupedModelPositionAndOnlyActiveLimits(t *testing.T) {
	laneUUID := "lane-1"
	diagnostic := requestLogDiagnosticsFor(models.RequestLog{
		LaneUUID: &laneUUID,
		CandidateTraceJSON: `[
			{"endpoint_id":"model-a","rank":1,"decision":"queued","reason":"requests/minute","effective_limits":[{"metric":"requests"}],"fallback_count":0},
			{"endpoint_id":"model-b","provider_id":"provider-b","upstream_model":"b","rank":2,"decision":"selected","reason":"","effective_limits":[{"metric":"requests"}],"fallback_count":1},
			{"endpoint_id":"model-c","rank":3,"decision":"not_needed"}
		]`,
		LimitImpactJSON: `[
			{"metric":"requests","period":"second","configured":null,"observed":null,"effective":null,"scope_type":"endpoint","scope_id":"model-b"},
			{"metric":"requests","period":"minute","configured":10,"observed":null,"effective":10,"scope_type":"endpoint","scope_id":"model-b","used":7,"reserved":1,"next_available_at":"2026-07-20T13:00:01Z","policy_id":"verbose-policy","source_header":"x-ratelimit"}
		]`,
	})
	if diagnostic == nil {
		t.Fatal("expected compact grouped-route diagnostics")
	}
	var candidates []map[string]any
	if err := json.Unmarshal([]byte(diagnostic.CandidateTraceJSON), &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[1]["rank"] != float64(2) || candidates[1]["decision"] != "selected" {
		t.Fatalf("group position was not retained: %#v", candidates)
	}
	if _, exists := candidates[1]["effective_limits"]; exists {
		t.Fatalf("candidate duplicated limit matrix: %#v", candidates[1])
	}
	if _, exists := candidates[1]["fallback_count"]; exists {
		t.Fatalf("candidate retained redundant fallback count: %#v", candidates[1])
	}

	var limits []map[string]any
	if err := json.Unmarshal([]byte(diagnostic.LimitImpactJSON), &limits); err != nil {
		t.Fatal(err)
	}
	if len(limits) != 1 || limits[0]["used"] != float64(7) || limits[0]["next_available_at"] == nil {
		t.Fatalf("active limit snapshot was not retained: %#v", limits)
	}
	if _, exists := limits[0]["policy_id"]; exists {
		t.Fatalf("limit retained policy bookkeeping: %#v", limits[0])
	}
	if _, exists := limits[0]["source_header"]; exists {
		t.Fatalf("limit retained source header bookkeeping: %#v", limits[0])
	}
}

func TestRequestLogDiagnosticsOmitOrdinaryDirectSelectionAndNullLimitMatrix(t *testing.T) {
	diagnostic := requestLogDiagnosticsFor(models.RequestLog{
		CandidateTraceJSON: `[{"endpoint_id":"model-a","rank":1,"decision":"selected","effective_limits":[]}]`,
		LimitImpactJSON:    `[{"metric":"requests","period":"second","configured":null,"observed":null,"effective":null}]`,
	})
	if diagnostic != nil {
		t.Fatalf("ordinary direct route should use canonical columns only: %#v", diagnostic)
	}
}
