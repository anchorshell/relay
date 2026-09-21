package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/anchorshell/relay/internal/telemetry"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/pkg/characterization"
)

func (s *Scheduler) completeCharacterizationLog(requestID string, metadata map[string]string, handle *characterization.Handle) {
	if s.store == nil {
		return
	}
	scope := tenancy.ContextWithMetadataScope(context.Background(), metadata)
	handle.OnLateCompletion(func(result characterization.Characterization) {
		ctx, cancel := context.WithTimeout(scope, 5*time.Second)
		defer cancel()
		if err := s.store.UpdateRequestLogCharacterization(ctx, requestID, result); err != nil {
			slog.Warn("request characterization update failed", "request_id", requestID)
			return
		}
		if s.telemetry != nil {
			payload := map[string]any{"request_id": requestID, "primary_action": result.PrimaryAction, "characterization": result, "characterization_duration_ms": result.ClassificationDurationMS}
			if identity, ok := tenancy.ScopeFromContext(scope); ok {
				payload["organization_uuid"] = identity.OrganizationUUID
				payload["actor_id"] = identity.UserUUID
				payload["api_key_uuid"] = identity.Metadata["api_key_uuid"]
			}
			s.telemetry.Publish(telemetry.Event{Type: "request_log", Payload: payload})
		}
	})
}
