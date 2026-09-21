package ws

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/anchorshell/relay/internal/telemetry"
)

const maxBrowserEventBytes = 64 * 1024

var requestLifecycleEventTypes = map[string]bool{
	"request_queued":    true,
	"request_waiting":   true,
	"request_ready":     true,
	"request_started":   true,
	"request_in_flight": true,
	"request_finished":  true,
	"request_completed": true,
	"request_failed":    true,
	"request_cancelled": true,
}

var requestLifecyclePayloadFields = []string{
	"task_id",
	"request_id",
	"lane",
	"incoming_model",
	"endpoint_id",
	"endpoint_name",
	"provider_id",
	"actor_id",
	"api_key_uuid",
	"selected_upstream_model",
	"state",
	"task_state",
	"queued_at",
	"started_at",
	"finished_at",
	"wait_ms",
	"latency_ms",
	"fallback",
	"fallback_count",
	"predicted_eligible_at",
	"resource_eligible_at",
	"user_eligible_at",
	"user_limit_reason",
	"delay_reason",
	"defer_scope",
	"defer_reason",
	"substatus",
	"uploaded_tokens",
	"downloaded_tokens",
	"status_code",
	"error_text",
	"preview_session_id",
	"preview_generation",
}

var requestProgressPayloadFields = []string{
	"request_id",
	"task_id",
	"state",
	"substatus",
	"uploaded_tokens",
	"downloaded_tokens",
	"preview_session_id",
	"preview_generation",
}

var requestTerminalPayloadFields = []string{
	"request_id",
	"task_id",
	"actor_id",
	"api_key_uuid",
	"lane",
	"lane_id",
	"incoming_model",
	"endpoint_id",
	"provider_id",
	"selected_upstream_model",
	"state",
	"task_state",
	"queued_at",
	"started_at",
	"finished_at",
	"wait_ms",
	"latency_ms",
	"fallback_count",
	"status_code",
	"error_text",
	"uploaded_tokens",
	"downloaded_tokens",
	"preview_session_id",
	"preview_generation",
}

var requestLogPayloadFields = []string{
	"request_id",
	"actor_id",
	"api_key_uuid",
	"lane_id",
	"endpoint_id",
	"provider_id",
	"incoming_model",
	"selected_upstream_model",
	"status_code",
	"task_state",
	"queued_at",
	"started_at",
	"finished_at",
	"wait_ms",
	"latency_ms",
	"guardrail_pre_duration_ms",
	"provider_latency_ms",
	"guardrail_post_duration_ms",
	"total_time_ms",
	"fallback_count",
	"estimated_input_tokens",
	"estimated_output_tokens",
	"actual_input_tokens",
	"actual_output_tokens",
	"actual_total_tokens",
	"estimated_cost_micros",
	"actual_cost_micros",
	"error_text",
	"request_bodies_stored",
	"primary_action",
	"characterization",
	"guardrail_status",
	"created_at",
	"updated_at",
	"preview_session_id",
	"preview_generation",
}

var snapshotPayloadFields = []string{
	"generated_at",
	"sequence",
	"external_rows_status",
	"preview_session_id",
	"preview_generation",
}

var snapshotQueueItemFields = []string{
	"task_id",
	"request_id",
	"lane",
	"incoming_model",
	"endpoint_id",
	"endpoint_name",
	"provider_id",
	"actor_id",
	"api_key_uuid",
	"selected_upstream_model",
	"state",
	"queued_at",
	"started_at",
	"wait_ms",
	"predicted_eligible_at",
	"resource_eligible_at",
	"user_eligible_at",
	"user_limit_reason",
	"delay_reason",
	"defer_scope",
	"defer_reason",
	"substatus",
	"uploaded_tokens",
	"downloaded_tokens",
	"estimated_cost_micros",
	"fallback_count",
}

var candidateTraceFields = []string{
	"endpoint_id",
	"fallback_count",
	"decision",
}

var modelPayloadFields = []string{
	"endpoint_id",
	"capacity_state",
	"health_status",
	"cooldown_until",
	"cooldown_reason",
	"cooldown_status_code",
	"limit_rows",
}

var limitRowFields = []string{
	"key",
	"label",
	"scope_type",
	"scope_id",
	"target_type",
	"target_key",
	"actor_id",
	"metric",
	"period",
	"configured",
	"effective",
	"used",
	"reserved",
	"remaining",
	"percent",
	"window_start",
	"reset_at",
	"source",
	"user_scoped",
	"blocked",
	"blocked_until",
	"blocked_reason",
}

var endpointHealthFields = []string{
	"endpoint_id",
	"provider_id",
	"health_status",
	"cooldown_until",
	"cooldown_reason",
	"cooldown_status_code",
	"updated_at",
}

var observedLimitFields = []string{
	"id",
	"scope_type",
	"scope_id",
	"metric",
	"period",
	"observed_value",
	"source_header",
	"observed_at",
	"expires_at",
	"enabled",
}

var limitPolicyFields = []string{
	"id",
	"action",
	"scope_type",
	"scope_id",
	"metric",
	"period",
	"limit_value",
	"enabled",
	"source",
	"created_at",
	"updated_at",
}

var previewControlFields = []string{
	"ok",
	"preview_session_id",
	"preview_generation",
	"request_count",
	"arrival_interval_ms",
	"generated",
	"queued",
	"completed",
	"active",
	"fallbacks",
	"reason",
}

func marshalBrowserEvent(event telemetry.Event) ([]byte, bool, error) {
	sanitized, ok := sanitizeBrowserEvent(event)
	if !ok {
		return nil, false, nil
	}
	if sanitized.Timestamp.IsZero() {
		sanitized.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(sanitized)
	if err != nil {
		return nil, false, err
	}
	if len(payload) <= maxBrowserEventBytes {
		return payload, true, nil
	}
	if isSnapshotType(sanitized.Type) {
		if trimmed := trimOversizedSnapshot(sanitized); trimmed != nil {
			payload, err = json.Marshal(*trimmed)
			if err != nil {
				return nil, false, err
			}
			if len(payload) <= maxBrowserEventBytes {
				return payload, true, nil
			}
		}
	}
	return nil, false, nil
}

func sanitizeBrowserEvent(event telemetry.Event) (telemetry.Event, bool) {
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}

	switch {
	case isSnapshotType(event.Type):
		event.Payload = sanitizeSnapshotPayload(payload)
		return event, true
	case event.Type == "capacity_snapshot_error":
		event.Payload = copyAllowed(payload, []string{"reason", "reconnect"})
		return event, true
	case event.Type == "resync_required":
		event.Payload = copyAllowed(payload, []string{"reason", "reconnect"})
		return event, true
	case event.Type == "connection_heartbeat":
		event.Payload = map[string]any{}
		return event, true
	case event.Type == "request_log":
		event.Payload = sanitizeRequestLogPayload(payload)
		return event, true
	case event.Type == "request_progress":
		event.Payload = copyMeaningful(payload, requestProgressPayloadFields)
		return event, true
	case requestLifecycleEventTypes[event.Type]:
		event.Payload = sanitizeRequestLifecyclePayload(event.Type, payload)
		return event, true
	case event.Type == "endpoint_health_change":
		event.Payload = copyAllowed(payload, endpointHealthFields)
		return event, true
	case event.Type == "capacity_limit_state":
		event.Payload = copyAllowed(payload, []string{
			"request_id", "endpoint_id", "provider_id", "user_scoped", "removed_keys",
			"health_status", "cooldown_until", "cooldown_reason", "cooldown_status_code",
		})
		if raw, exists := payload["row"]; exists {
			event.Payload["row"] = copyAllowed(mapFromAny(raw), limitRowFields)
		}
		if raw, exists := payload["rows"]; exists {
			event.Payload["rows"] = sanitizeLimitStateRows(raw)
		}
		return event, true
	case event.Type == "observed_limit_learned":
		event.Payload = copyAllowed(payload, observedLimitFields)
		return event, true
	case event.Type == "limit_policy_changed" || event.Type == "limit_policy_deleted":
		event.Payload = copyAllowed(payload, limitPolicyFields)
		if event.Type == "limit_policy_deleted" {
			event.Payload["action"] = "deleted"
		}
		return event, true
	case strings.HasPrefix(event.Type, "guardrail_check_"):
		event.Payload = copyAllowed(payload, []string{"request_id", "guardrail_uuid", "preset", "stage", "decision", "duration_ms", "http_status", "error_code"})
		return event, true
	case strings.HasPrefix(event.Type, "preview_"):
		event.Payload = copyAllowed(payload, previewControlFields)
		return event, true
	default:
		return telemetry.Event{}, false
	}
}

func sanitizeRequestLogPayload(payload map[string]any) map[string]any {
	out := copyMeaningful(payload, requestLogPayloadFields)
	raw, exists := payload["characterization"]
	if !exists {
		return out
	}
	characterizationPayload := mapFromAny(raw)
	compact := copyMeaningful(characterizationPayload, []string{
		"primary_action",
		"classification_duration_ms",
		"classifier_status",
		"classification_background",
	})
	for _, field := range []string{"target_objects", "output_objects", "domains", "required_capabilities"} {
		if values := firstBrowserString(characterizationPayload[field]); len(values) > 0 {
			compact[field] = values
		}
	}
	burden := mapFromAny(characterizationPayload["context_burden"])
	if tier := stringValue(burden["tier"]); tier != "" {
		compact["context_burden"] = map[string]any{"tier": tier}
	}
	if len(compact) > 0 {
		out["characterization"] = compact
	} else {
		delete(out, "characterization")
	}
	return out
}

func firstBrowserString(raw any) []string {
	if raw == nil {
		return nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var values []string
	if err := json.Unmarshal(body, &values); err != nil || len(values) == 0 {
		return nil
	}
	return values[:1]
}

func sanitizeSnapshotPayload(payload map[string]any) map[string]any {
	out := copyMeaningful(payload, snapshotPayloadFields)
	if raw, ok := payload["queue"]; ok {
		queue := mapFromAny(raw)
		out["queue"] = map[string]any{"states": queue["states"]}
	}
	if raw, ok := payload["queue_items"]; ok {
		out["queue_items"] = sanitizeSnapshotRequestList(raw)
	}
	if raw, ok := payload["models"]; ok {
		out["models"] = sanitizeModelList(raw)
	}
	return out
}

func sanitizeRequestLifecyclePayload(eventType string, payload map[string]any) map[string]any {
	fields := requestLifecyclePayloadFields
	if isTerminalRequestEventType(eventType) {
		fields = requestTerminalPayloadFields
	}
	out := copyMeaningful(payload, fields)
	compactRequestRedundancies(out)
	if raw, ok := payload["candidate_trace"]; ok && !isTerminalRequestEventType(eventType) {
		out["candidate_trace"] = sanitizeCandidateTraceList(raw)
	}
	return out
}

func sanitizeSnapshotRequestList(raw any) []map[string]any {
	items := listOfMaps(raw)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		next := copyMeaningful(item, snapshotQueueItemFields)
		compactRequestRedundancies(next)
		if rawTrace, ok := item["candidate_trace"]; ok {
			next["candidate_trace"] = sanitizeCandidateTraceList(rawTrace)
		}
		out = append(out, next)
	}
	return out
}

func compactRequestRedundancies(payload map[string]any) {
	dropEqualString(payload, "incoming_model", "lane")
	dropEqualString(payload, "task_state", "state")
	dropEqualString(payload, "predicted_eligible_at", "queued_at")
	if !dropEqualString(payload, "resource_eligible_at", "predicted_eligible_at") {
		dropEqualString(payload, "resource_eligible_at", "queued_at")
	}
}

func dropEqualString(payload map[string]any, key string, otherKey string) bool {
	value := strings.TrimSpace(stringValue(payload[key]))
	other := strings.TrimSpace(stringValue(payload[otherKey]))
	if value == "" || other == "" || value != other {
		return false
	}
	delete(payload, key)
	return true
}

func sanitizeCandidateTraceList(raw any) []map[string]any {
	items := listOfMaps(raw)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		next := copyMeaningful(item, candidateTraceFields)
		if reason := compactCandidateReason(item); reason != "" {
			next["reason"] = reason
		}
		out = append(out, next)
	}
	return out
}

func sanitizeModelList(raw any) []map[string]any {
	items := listOfMaps(raw)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		next := copyMeaningful(item, modelPayloadFields)
		if rawRows, ok := item["limit_rows"]; ok {
			next["limit_rows"] = sanitizeLimitRows(rawRows)
		}
		out = append(out, next)
	}
	return out
}

func sanitizeLimitRows(raw any) []map[string]any {
	items := listOfMaps(raw)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		next := copyMeaningful(item, limitRowFields)
		if key := strings.TrimSpace(stringValue(item["key"])); key != "" {
			next["key"] = key
		} else {
			next["key"] = compactLimitRowKey(item)
		}
		if sameNumber(item["configured"], item["effective"]) {
			delete(next, "configured")
		}
		out = append(out, next)
	}
	return out
}

func sanitizeLimitStateRows(raw any) []map[string]any {
	items := listOfMaps(raw)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, copyAllowed(item, limitRowFields))
	}
	return out
}

func trimOversizedSnapshot(event telemetry.Event) *telemetry.Event {
	next := event
	next.Payload = make(map[string]any, len(event.Payload))
	for key, value := range event.Payload {
		next.Payload[key] = value
	}
	next.Payload["queue_items"] = []map[string]any{}
	return &next
}

func isTerminalRequestEventType(eventType string) bool {
	switch eventType {
	case "request_finished", "request_completed", "request_failed", "request_cancelled":
		return true
	default:
		return false
	}
}

func compactCandidateReason(item map[string]any) string {
	decision := strings.ToLower(strings.TrimSpace(stringValue(item["decision"])))
	reason := strings.ToLower(strings.TrimSpace(stringValue(item["reason"])))
	if decision != "queued" {
		return ""
	}
	switch {
	case strings.Contains(reason, "cooldown"):
		return "cooldown"
	case strings.Contains(reason, "rate") || strings.Contains(reason, "limit"):
		return "rate_limit"
	case strings.Contains(reason, "unhealthy"):
		return "unhealthy"
	default:
		return "queued"
	}
}

func compactLimitRowKey(item map[string]any) string {
	parts := []string{
		strings.TrimSpace(stringValue(item["scope_type"])),
		strings.TrimSpace(stringValue(item["label"])),
		strings.TrimSpace(stringValue(item["metric"])),
		strings.TrimSpace(stringValue(item["period"])),
		strings.TrimSpace(stringValue(item["actor_id"])),
	}
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ":")
}

func copyMeaningful(payload map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := payload[key]; ok && meaningfulBrowserValue(value) {
			out[key] = value
		}
	}
	return out
}

func meaningfulBrowserValue(value any) bool {
	if value == nil {
		return false
	}
	if timestamp, ok := value.(time.Time); ok {
		return !timestamp.IsZero()
	}
	if timestamp, ok := value.(*time.Time); ok {
		return timestamp != nil && !timestamp.IsZero()
	}
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		return trimmed != "" && !strings.HasPrefix(trimmed, "0001-01-01T00:00:00")
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	case reflect.Array, reflect.Slice, reflect.Map, reflect.String:
		return reflected.Len() > 0
	case reflect.Pointer, reflect.Interface:
		return !reflected.IsNil()
	default:
		return true
	}
}

func mapFromAny(raw any) map[string]any {
	if mapped, ok := raw.(map[string]any); ok {
		return mapped
	}
	var mapped map[string]any
	body, err := json.Marshal(raw)
	if err != nil {
		return map[string]any{}
	}
	if err := json.Unmarshal(body, &mapped); err != nil {
		return map[string]any{}
	}
	return mapped
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func sameNumber(left any, right any) bool {
	leftValue, leftOK := numericValue(left)
	rightValue, rightOK := numericValue(right)
	return leftOK && rightOK && leftValue == rightValue
}

func numericValue(value any) (float64, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(reflected.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(reflected.Uint()), true
	case reflect.Float32, reflect.Float64:
		return reflected.Float(), true
	default:
		return 0, false
	}
}

func isSnapshotType(eventType string) bool {
	return eventType == "realtime_snapshot" || eventType == "capacity_snapshot"
}

func copyAllowed(payload map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			out[key] = value
		}
	}
	return out
}

func listOfMaps(raw any) []map[string]any {
	switch items := raw.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		var out []map[string]any
		body, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil
		}
		return out
	}
}
