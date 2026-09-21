package admin

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/anchorshell/relay/internal/scheduler"
)

const realtimeSnapshotVisibleQueueLimit = 12

type RealtimeSnapshot struct {
	GeneratedAt        time.Time               `json:"generated_at"`
	Sequence           uint64                  `json:"sequence"`
	ExternalRowsStatus string                  `json:"external_rows_status,omitempty"`
	Queue              scheduler.Snapshot      `json:"queue"`
	QueueItems         []RealtimeQueueItem     `json:"queue_items"`
	Models             []ModelCapacitySnapshot `json:"models"`
	VisibleQueueLimit  int                     `json:"visible_queue_limit"`
}

type RealtimeQueueItem struct {
	TaskID            string                   `json:"task_id"`
	RequestID         string                   `json:"request_id"`
	Lane              string                   `json:"lane"`
	IncomingModel     string                   `json:"incoming_model"`
	EndpointUUID      string                   `json:"endpoint_id"`
	EndpointName      string                   `json:"endpoint_name"`
	UpstreamModel     string                   `json:"upstream_model,omitempty"`
	SelectedModel     string                   `json:"selected_upstream_model,omitempty"`
	ProviderUUID      string                   `json:"provider_id"`
	ActorID           string                   `json:"actor_id,omitempty"`
	State             string                   `json:"state"`
	Priority          int                      `json:"priority"`
	QueuedAt          time.Time                `json:"queued_at"`
	WaitMS            int64                    `json:"wait_ms"`
	PredictedEligible time.Time                `json:"predicted_eligible_at"`
	ResourceEligible  time.Time                `json:"resource_eligible_at,omitempty"`
	UserEligible      time.Time                `json:"user_eligible_at,omitempty"`
	UserLimitReason   string                   `json:"user_limit_reason,omitempty"`
	DelayReason       string                   `json:"delay_reason,omitempty"`
	DeferScope        string                   `json:"defer_scope,omitempty"`
	DeferReason       string                   `json:"defer_reason,omitempty"`
	EstimatedCost     int64                    `json:"estimated_cost_micros"`
	FallbackCount     int                      `json:"fallback_count"`
	CandidateTrace    []RealtimeCandidateTrace `json:"candidate_trace,omitempty"`
	StartedAt         *time.Time               `json:"started_at,omitempty"`
	Substatus         string                   `json:"substatus,omitempty"`
	UploadedTokens    int64                    `json:"uploaded_tokens,omitempty"`
	DownloadedTokens  int64                    `json:"downloaded_tokens,omitempty"`
}

type RealtimeCandidateTrace struct {
	EndpointUUID    string    `json:"endpoint_id"`
	EndpointName    string    `json:"endpoint_name"`
	ProviderUUID    string    `json:"provider_id"`
	UpstreamModel   string    `json:"upstream_model"`
	Rank            int       `json:"rank"`
	FallbackCount   int       `json:"fallback_count"`
	EligibleAt      time.Time `json:"eligible_at,omitempty"`
	PredictedWaitMS int64     `json:"predicted_wait_ms"`
	Decision        string    `json:"decision"`
	Reason          string    `json:"reason"`
}

func realtimeSnapshotPayload(snapshot CapacitySnapshot) map[string]any {
	realtime := RealtimeSnapshot{
		GeneratedAt:        snapshot.GeneratedAt,
		Sequence:           snapshot.Sequence,
		ExternalRowsStatus: snapshot.ExternalRowsStatus,
		Queue:              snapshot.Queue,
		QueueItems:         compactRealtimeQueueItems(snapshot.QueueItems, realtimeSnapshotVisibleQueueLimit),
		Models:             snapshot.Models,
		VisibleQueueLimit:  realtimeSnapshotVisibleQueueLimit,
	}
	var payload map[string]any
	body, err := json.Marshal(realtime)
	if err != nil {
		return map[string]any{}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]any{}
	}
	return payload
}

func compactRealtimeQueueItems(items []scheduler.TaskSnapshot, visibleLimit int) []RealtimeQueueItem {
	if visibleLimit < 0 {
		visibleLimit = 0
	}
	active := make([]scheduler.TaskSnapshot, 0)
	queued := make([]scheduler.TaskSnapshot, 0)
	for _, item := range items {
		switch item.State {
		case "completed", "failed", "cancelled":
			continue
		case "in_flight", "started":
			active = append(active, item)
		default:
			queued = append(queued, item)
		}
	}
	sortTaskSnapshots(active)
	sortTaskSnapshots(queued)

	out := make([]RealtimeQueueItem, 0, len(active)+min(visibleLimit, len(queued)))
	seen := make(map[string]bool, len(active)+visibleLimit)
	for _, item := range active {
		compact := compactRealtimeQueueItem(item)
		key := compact.RequestID
		if key == "" {
			key = compact.TaskID
		}
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		out = append(out, compact)
	}
	for _, item := range queued {
		if visibleLimit <= 0 {
			break
		}
		compact := compactRealtimeQueueItem(item)
		key := compact.RequestID
		if key == "" {
			key = compact.TaskID
		}
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		out = append(out, compact)
		visibleLimit--
	}
	return out
}

func compactRealtimeQueueItem(item scheduler.TaskSnapshot) RealtimeQueueItem {
	return RealtimeQueueItem{
		TaskID:            item.TaskID,
		RequestID:         item.RequestID,
		Lane:              item.Lane,
		IncomingModel:     item.IncomingModel,
		EndpointUUID:      item.EndpointUUID,
		EndpointName:      item.EndpointName,
		UpstreamModel:     item.UpstreamModel,
		SelectedModel:     item.UpstreamModel,
		ProviderUUID:      item.ProviderUUID,
		ActorID:           item.ActorID,
		State:             item.State,
		Priority:          item.Priority,
		QueuedAt:          item.QueuedAt,
		WaitMS:            item.WaitMS,
		PredictedEligible: item.PredictedEligible,
		ResourceEligible:  item.ResourceEligible,
		UserEligible:      item.UserEligible,
		UserLimitReason:   item.UserLimitReason,
		DelayReason:       item.DelayReason,
		DeferScope:        item.DeferScope,
		DeferReason:       item.DeferReason,
		EstimatedCost:     item.EstimatedCost,
		FallbackCount:     item.FallbackCount,
		CandidateTrace:    compactRealtimeCandidateTrace(item.CandidateTrace),
		StartedAt:         item.StartedAt,
		Substatus:         item.Substatus,
		UploadedTokens:    item.UploadedTokens,
		DownloadedTokens:  item.DownloadedTokens,
	}
}

func compactRealtimeCandidateTrace(trace []scheduler.CandidateTrace) []RealtimeCandidateTrace {
	if len(trace) == 0 {
		return nil
	}
	out := make([]RealtimeCandidateTrace, 0, len(trace))
	for _, item := range trace {
		out = append(out, RealtimeCandidateTrace{
			EndpointUUID:    item.EndpointUUID,
			EndpointName:    item.EndpointName,
			ProviderUUID:    item.ProviderUUID,
			UpstreamModel:   item.UpstreamModel,
			Rank:            item.Rank,
			FallbackCount:   item.FallbackCount,
			EligibleAt:      item.EligibleAt,
			PredictedWaitMS: item.PredictedWaitMS,
			Decision:        item.Decision,
			Reason:          item.Reason,
		})
	}
	return out
}

func sortTaskSnapshots(items []scheduler.TaskSnapshot) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].QueuedAt.Equal(items[j].QueuedAt) {
			return items[i].QueuedAt.Before(items[j].QueuedAt)
		}
		if items[i].RequestID != items[j].RequestID {
			return items[i].RequestID < items[j].RequestID
		}
		return items[i].TaskID < items[j].TaskID
	})
}
