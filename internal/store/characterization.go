package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/pkg/characterization"
	"gorm.io/gorm"
)

// DeploymentCharacterizationEngine is only for standalone, unpartitioned Relay.
// Downstream tenants resolve their preference through the neutral engine hook.
func (s *Store) DeploymentCharacterizationEngine(ctx context.Context) (characterization.EngineID, error) {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	if s.engineLoaded {
		return s.engine, nil
	}
	var row models.AppSetting
	err := s.db.WithContext(ctx).Where("key = ?", "characterization_engine").Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	id := characterization.EngineAnchorShell
	if err == nil {
		if json.Unmarshal([]byte(row.ValueJSON), &id) != nil || !characterization.ValidEngine(id) {
			id = characterization.EngineAnchorShell
		}
	}
	s.engine, s.engineLoaded = id, true
	return id, nil
}

// Patch only enrichment columns, never whole-row Save or accounting fields.
// The inherited tenancy callbacks enforce organization scope in Pro.
func (s *Store) UpdateRequestLogCharacterization(ctx context.Context, requestID string, result characterization.Characterization) error {
	if s == nil || s.db == nil || requestID == "" {
		return errors.New("invalid characterization update")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	query := s.db.WithContext(ctx).Model(&models.RequestLog{}).Where("request_id = ? AND finished_at IS NOT NULL", requestID)
	if scope, ok := tenancy.ScopeFromContext(ctx); ok {
		query = query.Where("organization_uuid = ?", scope.OrganizationUUID)
		if scope.UserUUID != "" {
			query = query.Where("user_uuid = ?", scope.UserUUID)
		}
	}
	updated := query.Updates(map[string]any{"primary_action": string(result.PrimaryAction), "action_confidence": result.Confidence, "characterization_version": result.Version, "characterization_json": string(body)})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
