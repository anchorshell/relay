package store

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
	"gorm.io/gorm"
)

type EffectiveGuardrailRow struct {
	Guardrail models.Guardrail
	Sources   []string
}

type effectiveGuardrailQueryRow struct {
	models.Guardrail
	BindingID     uint
	RoutingLaneID *uint
	ProviderID    *uint
	EndpointID    *uint
}

func (effectiveGuardrailQueryRow) TableName() string { return "guardrails" }

func (s *Store) ListGuardrails(ctx context.Context) ([]models.Guardrail, error) {
	var rows []models.Guardrail
	return rows, s.db.WithContext(ctx).Order("priority ASC, uuid ASC").Find(&rows).Error
}

func (s *Store) ListGuardrailCredentials(ctx context.Context) ([]models.GuardrailCredential, error) {
	var rows []models.GuardrailCredential
	err := s.db.WithContext(ctx).Select("id", "uuid", "name", "enabled", "created_at", "updated_at").Order("name ASC").Find(&rows).Error
	return rows, err
}

func (s *Store) GetGuardrailCredential(ctx context.Context, id uint) (models.GuardrailCredential, error) {
	var row models.GuardrailCredential
	if id == 0 {
		return row, gorm.ErrRecordNotFound
	}
	err := s.db.WithContext(ctx).First(&row, id).Error
	return row, err
}

func (s *Store) UpdateGuardrailCredentialEnvelope(ctx context.Context, id uint, envelope string) error {
	result := s.db.WithContext(ctx).Model(&models.GuardrailCredential{}).Where("id = ?", id).Updates(map[string]any{"encrypted_secret": envelope})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) ListGuardrailBindings(ctx context.Context, guardrailID uint) ([]models.GuardrailBinding, error) {
	var rows []models.GuardrailBinding
	query := s.db.WithContext(ctx).Order("created_at ASC")
	if guardrailID != 0 {
		query = query.Where("guardrail_id = ?", guardrailID)
	}
	return rows, query.Find(&rows).Error
}

// EffectiveGuardrails resolves lane, provider, and endpoint inheritance in one
// bounded query. Duplicate bindings are collapsed in memory and retain every
// source for audit/UI display.
func (s *Store) EffectiveGuardrails(ctx context.Context, laneID, providerID, endpointID uint) ([]EffectiveGuardrailRow, error) {
	var rows []effectiveGuardrailQueryRow
	query := s.db.WithContext(ctx).
		Table("guardrails AS g").
		Select("g.*, b.id AS binding_id, b.routing_lane_id, b.provider_id, b.endpoint_id").
		Joins("JOIN guardrail_bindings AS b ON b.guardrail_id = g.id").
		Where("g.enabled = ? AND b.enabled = ?", true, true).
		Where("(b.routing_lane_id = ? OR b.provider_id = ? OR b.endpoint_id = ?)", laneID, providerID, endpointID)
	if scope, ok := tenancy.ScopeFromContext(ctx); ok && s.db.Migrator().HasColumn("guardrails", "organization_uuid") {
		query = query.Where("g.organization_uuid = ? AND b.organization_uuid = ?", scope.OrganizationUUID, scope.OrganizationUUID)
	}
	err := query.Order("g.priority ASC, g.uuid ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]*EffectiveGuardrailRow, len(rows))
	ordered := make([]*EffectiveGuardrailRow, 0, len(rows))
	for _, row := range rows {
		item := byID[row.Guardrail.ID]
		if item == nil {
			item = &EffectiveGuardrailRow{Guardrail: row.Guardrail}
			byID[row.Guardrail.ID] = item
			ordered = append(ordered, item)
		}
		source := "routing_lane"
		if row.EndpointID != nil {
			source = "endpoint"
		} else if row.ProviderID != nil {
			source = "provider"
		}
		if !containsString(item.Sources, source) {
			item.Sources = append(item.Sources, source)
		}
	}
	result := make([]EffectiveGuardrailRow, 0, len(ordered))
	for _, row := range ordered {
		sort.SliceStable(row.Sources, func(i, j int) bool { return bindingSpecificity(row.Sources[i]) < bindingSpecificity(row.Sources[j]) })
		result = append(result, *row)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Guardrail.Priority != result[j].Guardrail.Priority {
			return result[i].Guardrail.Priority < result[j].Guardrail.Priority
		}
		left, right := 3, 3
		if len(result[i].Sources) > 0 {
			left = bindingSpecificity(result[i].Sources[0])
		}
		if len(result[j].Sources) > 0 {
			right = bindingSpecificity(result[j].Sources[0])
		}
		if left != right {
			return left < right
		}
		return result[i].Guardrail.UUID < result[j].Guardrail.UUID
	})
	return result, nil
}

func bindingSpecificity(source string) int {
	switch source {
	case "endpoint":
		return 0
	case "provider":
		return 1
	default:
		return 2
	}
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Store) ReplaceGuardrailBindings(ctx context.Context, guardrail models.Guardrail, bindings []models.GuardrailBinding) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		transactionStore := New(tx)
		if err := tx.Where("guardrail_id = ?", guardrail.ID).Delete(&models.GuardrailBinding{}).Error; err != nil {
			return err
		}
		seen := map[string]struct{}{}
		for i := range bindings {
			bindings[i].ID = 0
			bindings[i].UUID = ""
			bindings[i].GuardrailID = guardrail.ID
			bindings[i].GuardrailUUID = guardrail.UUID
			key := bindingIdentity(bindings[i])
			if key == "" {
				return errors.New("guardrail binding must have exactly one target")
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if err := transactionStore.Create(ctx, &bindings[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func bindingIdentity(binding models.GuardrailBinding) string {
	if binding.EndpointUUID != nil && strings.TrimSpace(*binding.EndpointUUID) != "" {
		return "endpoint:" + *binding.EndpointUUID
	}
	if binding.ProviderUUID != nil && strings.TrimSpace(*binding.ProviderUUID) != "" {
		return "provider:" + *binding.ProviderUUID
	}
	if binding.RoutingLaneUUID != nil && strings.TrimSpace(*binding.RoutingLaneUUID) != "" {
		return "routing_lane:" + *binding.RoutingLaneUUID
	}
	return ""
}

func (s *Store) DeleteGuardrailCascade(ctx context.Context, guardrailID uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("guardrail_id = ?", guardrailID).Delete(&models.GuardrailBinding{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&models.Guardrail{}, guardrailID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// DeleteGuardrailCredentialIfUnused removes a credential only after the
// owning guardrail has been deleted and no other scoped guardrail references
// it. Historical request-log metadata never references credential rows.
func (s *Store) DeleteGuardrailCredentialIfUnused(ctx context.Context, credentialID uint) error {
	if credentialID == 0 {
		return nil
	}
	var references int64
	if err := s.db.WithContext(ctx).Model(&models.Guardrail{}).Where("credential_id = ?", credentialID).Count(&references).Error; err != nil {
		return err
	}
	if references > 0 {
		return nil
	}
	return s.db.WithContext(ctx).Delete(&models.GuardrailCredential{}, credentialID).Error
}
