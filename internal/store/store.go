package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchorshell/relay/internal/models"
	"github.com/anchorshell/relay/internal/tenancy"
	"github.com/anchorshell/relay/pkg/characterization"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DefaultRequestLogListLimit       = 50
	MaxRequestLogListLimit           = 100
	DefaultRecentRequestLogListLimit = 24
	DefaultRecentFlowActivityLimit   = 24
	MaxRecentFlowActivityLimit       = 50
	DefaultRecentModelUsageLimit     = 50
	MaxRecentModelUsageLimit         = 200

	SettingReserveEstimatedTokensForLimits = "reserve_estimated_tokens_for_limits"
	SettingReserveEstimatedSpendForLimits  = "reserve_estimated_spend_for_limits"
)

var (
	ErrInvalidRequestLogSort       = errors.New("invalid request log sort")
	ErrInvalidRequestLogDirection  = errors.New("invalid request log sort direction")
	ErrInvalidRequestLogOffset     = errors.New("invalid request log offset")
	ErrInvalidRequestLogAction     = errors.New("invalid request log action")
	ErrInvalidRequestLogDomain     = errors.New("invalid request log domain")
	ErrInvalidGuardrailStatus      = errors.New("invalid guardrail status")
	ErrInvalidGuardrailPreset      = errors.New("invalid guardrail preset")
	ErrInvalidRecentFlowLaneID     = errors.New("invalid recent flow lane id")
	ErrInvalidRecentFlowEndpointID = errors.New("invalid recent flow endpoint id")
	ErrProviderNameConflict        = errors.New("a provider with this name already exists")
	ErrEndpointNameConflict        = errors.New("a model with this name already exists for this provider")
	ErrRoutingLaneNameConflict     = errors.New("a group with this name already exists")
	ErrGuardrailNameConflict       = errors.New("a guardrail with this name already exists")
)

type LimitPolicyScope struct {
	ScopeType models.ScopeType
	ScopeID   uint
}

type Store struct {
	engineMu     sync.Mutex
	engineLoaded bool
	engine       characterization.EngineID
	db           *gorm.DB
	catalog      *catalogCache
}

type tenantProviderRow struct {
	models.Provider
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantProviderRow) TableName() string { return "providers" }

type tenantCredentialRow struct {
	models.Credential
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantCredentialRow) TableName() string { return "credentials" }

type tenantEndpointRow struct {
	models.Endpoint
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantEndpointRow) TableName() string { return "endpoints" }

type tenantRoutingLaneRow struct {
	models.RoutingLane
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantRoutingLaneRow) TableName() string { return "routing_lanes" }

type tenantLaneMembershipRow struct {
	models.LaneMembership
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantLaneMembershipRow) TableName() string { return "lane_memberships" }

type tenantLimitPolicyRow struct {
	models.LimitPolicy
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantLimitPolicyRow) TableName() string { return "limit_policies" }

type tenantLimitPolicyStateRow struct {
	models.LimitPolicyState
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantLimitPolicyStateRow) TableName() string { return "limit_policy_states" }

type tenantLimitPolicyStateSegmentRow struct {
	models.LimitPolicyStateSegment
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantLimitPolicyStateSegmentRow) TableName() string { return "limit_policy_state_segments" }

type tenantObservedLimitRow struct {
	models.ObservedLimit
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantObservedLimitRow) TableName() string { return "observed_limits" }

type tenantObservedLimitStateSegmentRow struct {
	models.ObservedLimitStateSegment
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantObservedLimitStateSegmentRow) TableName() string {
	return "observed_limit_state_segments"
}

type tenantPricingPolicyRow struct {
	models.PricingPolicy
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantPricingPolicyRow) TableName() string { return "pricing_policies" }

type tenantGuardrailRow struct {
	models.Guardrail
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantGuardrailRow) TableName() string { return "guardrails" }

type tenantGuardrailCredentialRow struct {
	models.GuardrailCredential
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantGuardrailCredentialRow) TableName() string { return "guardrail_credentials" }

type tenantGuardrailBindingRow struct {
	models.GuardrailBinding
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantGuardrailBindingRow) TableName() string { return "guardrail_bindings" }

type tenantRequestLogRow struct {
	models.RequestLog
	OrganizationUUID string `gorm:"column:organization_uuid"`
	UserUUID         string `gorm:"column:user_uuid"`
}

func (tenantRequestLogRow) TableName() string { return "request_logs" }

type tenantRequestLogDiagnosticsRow struct {
	models.RequestLogDiagnostics
	OrganizationUUID string `gorm:"column:organization_uuid"`
}

func (tenantRequestLogDiagnosticsRow) TableName() string { return "request_log_diagnostics" }

func New(db *gorm.DB) *Store {
	tenancy.InstallCallbacks(db)
	return &Store{db: db, catalog: newCatalogCache()}
}

// EvictTenantCaches removes reloadable configuration state for an inactive
// runtime. It never deletes authoritative database rows.
func (s *Store) EvictTenantCaches(organizationUUID string) {
	if s == nil || s.catalog == nil {
		return
	}
	s.catalog.evictTenant(organizationUUID)
}

// CatalogGeneration changes after routing mutations. Pricing and policy
// mutations invalidate their warm entries but do not force queued requests to
// rebuild an otherwise unchanged route.
func (s *Store) CatalogGeneration(ctx context.Context) uint64 {
	if s == nil {
		return 0
	}
	return s.catalog.generation(ctx)
}

// InvalidateRoutingCatalog lets an extension announce an atomic mutation to
// the ordinary routing tables. It clears only reloadable state; the database
// remains authoritative.
func (s *Store) InvalidateRoutingCatalog(ctx context.Context) {
	if s == nil || s.catalog == nil {
		return
	}
	s.catalog.invalidate(ctx, true)
}

func (s *Store) DB() *gorm.DB {
	return s.db
}

func (s *Store) SeedDefaults(ctx context.Context, defaultMaxWaitMS, defaultMaxLatencyMS int64, maxQueueLen int, maxQueueAgeMS int64, memBodyBytes, fileBodyBytes int64, insecureDev bool) error {
	if s.tenantSchemaActive() {
		if _, ok := tenancy.ScopeFromContext(ctx); !ok {
			return nil
		}
	}
	settings := map[string]any{
		"default_max_wait_ms":                  defaultMaxWaitMS,
		"default_max_latency_ms":               defaultMaxLatencyMS,
		"max_queue_length":                     maxQueueLen,
		"max_queue_age_ms":                     maxQueueAgeMS,
		"body_memory_threshold_bytes":          memBodyBytes,
		"body_spool_threshold_bytes":           fileBodyBytes,
		"insecure_dev_mode":                    insecureDev,
		"max_queued_estimated_spend_micros":    int64(0),
		SettingReserveEstimatedTokensForLimits: true,
		SettingReserveEstimatedSpendForLimits:  true,
		"store_requests":                       false,
	}
	for key, value := range settings {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.AppSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := s.createSetting(ctx, key, string(body), time.Now().UTC()); err != nil {
			return err
		}
	}

	defaultLanes := []models.RoutingLane{
		{Name: "reasoning", Description: "Reasoning-focused models for deeper thought and higher-quality answers.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
		{Name: "agentic", Description: "Models suited for longer, tool-driven, multi-step tasks.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
		{Name: "coding", Description: "Models best suited for code generation, editing, and technical reasoning.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
		{Name: "vision", Description: "Models that should accept multimodal or image-heavy work.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
		{Name: "structured", Description: "Models optimized for reliable JSON and structured extraction flows.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
		{Name: "embeddings", Description: "Embedding models for retrieval, indexing, and semantic search.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
		{Name: "fast", Description: "Low-latency models for quick turnarounds and cheap throughput work.", Hidden: false, Enabled: true, DefaultMaxWaitMS: defaultMaxWaitMS, AllowFallback: true, DefaultPriority: 50},
	}

	for _, lane := range defaultLanes {
		var existing models.RoutingLane
		err := s.db.WithContext(ctx).Where("name = ?", lane.Name).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.Create(ctx, &lane); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		existing.Description = lane.Description
		if err := s.Save(ctx, &existing); err != nil {
			return err
		}
	}

	return s.cleanupBuiltinDummy(ctx)
}

func (s *Store) tenantSchemaActive() bool {
	if s == nil || s.db == nil {
		return false
	}
	return s.db.Migrator().HasColumn("app_settings", "organization_uuid") ||
		s.db.Migrator().HasColumn("routing_lanes", "organization_uuid")
}

func (s *Store) cleanupBuiltinDummy(ctx context.Context) error {
	var provider models.Provider
	err := s.db.WithContext(ctx).Where("slug = ?", "builtin-dummy").First(&provider).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		var endpoints []models.Endpoint
		if err := s.db.WithContext(ctx).Where("provider_id = ?", provider.ID).Find(&endpoints).Error; err != nil {
			return err
		}
		endpointIDs := make([]uint, 0, len(endpoints))
		for _, endpoint := range endpoints {
			endpointIDs = append(endpointIDs, endpoint.ID)
		}
		tx := s.db.WithContext(ctx)
		if len(endpointIDs) > 0 {
			if err := tx.Where("endpoint_id IN ?", endpointIDs).Delete(&models.LaneMembership{}).Error; err != nil {
				return err
			}
			if err := tx.Where("endpoint_id IN ?", endpointIDs).Delete(&models.PricingPolicy{}).Error; err != nil {
				return err
			}
			if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeEndpoint, endpointIDs); err != nil {
				return err
			}
			if err := tx.Where("scope_type = ? AND scope_id IN ?", models.ScopeEndpoint, endpointIDs).Delete(&models.LimitPolicy{}).Error; err != nil {
				return err
			}
			if err := tx.Where("scope_type = ? AND scope_id IN ?", models.ScopeEndpoint, endpointIDs).Delete(&models.ObservedLimit{}).Error; err != nil {
				return err
			}
			if err := tx.Where("provider_id = ?", provider.ID).Delete(&models.Endpoint{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("provider_id = ?", provider.ID).Delete(&models.Credential{}).Error; err != nil {
			return err
		}
		if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeProvider, []uint{provider.ID}); err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeProvider, provider.ID).Delete(&models.LimitPolicy{}).Error; err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeProvider, provider.ID).Delete(&models.ObservedLimit{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&provider).Error; err != nil {
			return err
		}
	}

	var lane models.RoutingLane
	err = s.db.WithContext(ctx).Where("name = ? AND hidden = ?", "dummy", true).First(&lane).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	tx := s.db.WithContext(ctx)
	if err := tx.Where("lane_id = ?", lane.ID).Delete(&models.LaneMembership{}).Error; err != nil {
		return err
	}
	if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeLane, []uint{lane.ID}); err != nil {
		return err
	}
	if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeLane, lane.ID).Delete(&models.LimitPolicy{}).Error; err != nil {
		return err
	}
	if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeLane, lane.ID).Delete(&models.ObservedLimit{}).Error; err != nil {
		return err
	}
	return tx.Delete(&lane).Error
}

func deleteLimitPolicyStatesForScopes(tx *gorm.DB, scopeType models.ScopeType, scopeIDs []uint) error {
	if tx == nil || len(scopeIDs) == 0 {
		return nil
	}
	policyIDs := tx.Model(&models.LimitPolicy{}).
		Select("id").
		Where("scope_type = ? AND scope_id IN ?", scopeType, scopeIDs)
	return tx.Where("policy_id IN (?)", policyIDs).Delete(&models.LimitPolicyState{}).Error
}

func (s *Store) GetSettingInt64(ctx context.Context, key string, fallback int64) int64 {
	var setting models.AppSetting
	if err := s.db.WithContext(ctx).First(&setting, "key = ?", key).Error; err != nil {
		return fallback
	}
	var value any
	if err := json.Unmarshal([]byte(setting.ValueJSON), &value); err != nil {
		return fallback
	}
	switch v := value.(type) {
	case float64:
		return int64(v)
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fallback
		}
		return parsed
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

func (s *Store) GetSettingBool(ctx context.Context, key string, fallback bool) bool {
	var setting models.AppSetting
	if err := s.db.WithContext(ctx).First(&setting, "key = ?", key).Error; err != nil {
		return fallback
	}
	var value any
	if err := json.Unmarshal([]byte(setting.ValueJSON), &value); err != nil {
		return fallback
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

func (s *Store) GetSettings(ctx context.Context) ([]models.AppSetting, error) {
	var settings []models.AppSetting
	return settings, s.db.WithContext(ctx).Order("key asc").Find(&settings).Error
}

func (s *Store) UpsertSetting(ctx context.Context, key string, value any) error {
	return s.UpsertSettings(ctx, map[string]any{key: value})
}

func (s *Store) UpsertSettings(ctx context.Context, values map[string]any) error {
	if value, ok := values["characterization_engine"]; ok {
		if _, scoped := s.tenantScopeForTable(ctx, "app_settings"); scoped {
			return errors.New("use organization characterization settings")
		}
		id, ok := value.(string)
		if !ok || !characterization.ValidEngine(characterization.EngineID(id)) {
			return errors.New("invalid characterization engine")
		}
		s.engineMu.Lock()
		defer s.engineMu.Unlock()
		defer func() { s.engineLoaded = false }()
	}
	if len(values) == 0 {
		return nil
	}
	type encodedSetting struct {
		key       string
		valueJSON string
	}
	encoded := make([]encodedSetting, 0, len(values))
	for key, value := range values {
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		encoded = append(encoded, encodedSetting{key: key, valueJSON: string(body)})
	}
	sort.Slice(encoded, func(i, j int) bool { return encoded[i].key < encoded[j].key })

	scope, tenantScoped := s.tenantScopeForTable(ctx, "app_settings")
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range encoded {
			query := tx.Table("app_settings").Where("key = ?", item.key)
			if tenantScoped {
				query = query.Where("organization_uuid = ?", scope.OrganizationUUID)
			}
			result := query.Updates(map[string]any{
				"value_json": item.valueJSON,
				"updated_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				continue
			}
			row := map[string]any{
				"key":        item.key,
				"value_json": item.valueJSON,
				"updated_at": now,
			}
			if tenantScoped {
				row["organization_uuid"] = scope.OrganizationUUID
			}
			if err := tx.Table("app_settings").Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) createSetting(ctx context.Context, key, valueJSON string, updatedAt time.Time) error {
	if scope, ok := s.tenantScopeForTable(ctx, "app_settings"); ok {
		return s.db.WithContext(ctx).Table("app_settings").Create(map[string]any{
			"organization_uuid": scope.OrganizationUUID,
			"key":               key,
			"value_json":        valueJSON,
			"updated_at":        updatedAt,
		}).Error
	}
	return s.db.WithContext(ctx).Create(&models.AppSetting{Key: key, ValueJSON: valueJSON, UpdatedAt: updatedAt}).Error
}

func (s *Store) tenantScopeForTable(ctx context.Context, table string) (tenancy.Scope, bool) {
	if s == nil || s.db == nil {
		return tenancy.Scope{}, false
	}
	scope, ok := tenancy.ScopeFromContext(ctx)
	if !ok {
		return tenancy.Scope{}, false
	}
	// A tenant-scoped request must fail closed when its schema is stale. The
	// tenant row wrappers intentionally include organization_uuid so a missing
	// column produces an error instead of falling back to an unscoped write.
	return scope, true
}

func (s *Store) sameTenantJoin(ctx context.Context, baseTable string, joinedTable string) string {
	if _, ok := s.tenantScopeForTable(ctx, baseTable); !ok {
		return ""
	}
	if s == nil || s.db == nil || !s.db.Migrator().HasColumn(joinedTable, "organization_uuid") {
		return ""
	}
	return fmt.Sprintf(" AND %s.organization_uuid = %s.organization_uuid", joinedTable, baseTable)
}

func (s *Store) ListProviders(ctx context.Context) ([]models.Provider, error) {
	return s.ListProvidersWithScopes(ctx)
}

func (s *Store) ListProvidersWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.Provider, error) {
	var items []models.Provider
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("id asc").Find(&items).Error
}

func (s *Store) ListCredentials(ctx context.Context) ([]models.Credential, error) {
	return s.ListCredentialsWithScopes(ctx)
}

func (s *Store) ListCredentialsWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.Credential, error) {
	var items []models.Credential
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("id asc").Find(&items).Error
}

func (s *Store) ListCredentialCacheEntries(ctx context.Context) ([]models.CredentialCacheEntry, error) {
	items := make([]models.CredentialCacheEntry, 0)
	if err := s.StreamCredentialCacheEntries(ctx, func(entry models.CredentialCacheEntry) error {
		items = append(items, entry)
		return nil
	}); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) StreamCredentialCacheEntries(ctx context.Context, consume func(models.CredentialCacheEntry) error) error {
	type credentialCacheRow struct {
		ID               uint
		UUID             string
		ProviderID       uint
		ProviderUUID     string
		Name             string
		EncryptedSecret  string
		Enabled          bool
		CreatedAt        time.Time
		UpdatedAt        time.Time
		OrganizationUUID string
	}
	columns := []string{"id", "uuid", "provider_id", "provider_uuid", "name", "encrypted_secret", "enabled", "created_at", "updated_at"}
	if s.db.Migrator().HasColumn("credentials", "organization_uuid") {
		columns = append(columns, "organization_uuid")
	}
	query := s.db.WithContext(ctx).Table("credentials").Select(columns).Order("id asc")
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row credentialCacheRow
		if err := query.ScanRows(rows, &row); err != nil {
			return err
		}
		entry := models.CredentialCacheEntry{
			Credential: models.Credential{
				ID: row.ID, UUID: row.UUID, ProviderID: row.ProviderID, ProviderUUID: row.ProviderUUID,
				Name: row.Name, EncryptedSecret: row.EncryptedSecret, Enabled: row.Enabled,
				CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			},
			OrganizationUUID: strings.TrimSpace(row.OrganizationUUID),
		}
		if err := consume(entry); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) GetCredentialCacheEntry(ctx context.Context, credentialID uint) (models.CredentialCacheEntry, error) {
	if credentialID == 0 {
		return models.CredentialCacheEntry{}, gorm.ErrRecordNotFound
	}
	var credential models.Credential
	if err := s.db.WithContext(ctx).Where("id = ?", credentialID).First(&credential).Error; err != nil {
		return models.CredentialCacheEntry{}, err
	}
	scope, _ := tenancy.ScopeFromContext(ctx)
	return models.CredentialCacheEntry{Credential: credential, OrganizationUUID: strings.TrimSpace(scope.OrganizationUUID)}, nil
}

func (s *Store) ListCredentialsByProvider(ctx context.Context, providerID uint) ([]models.Credential, error) {
	var items []models.Credential
	return items, s.db.WithContext(ctx).Where("provider_id = ?", providerID).Order("id asc").Find(&items).Error
}

func (s *Store) HasEncryptedCredentials(ctx context.Context) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).
		Model(&models.Credential{}).
		Where("encrypted_secret LIKE ?", "relay:%").
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) UpdateCredentialEncryptedSecret(ctx context.Context, credentialID uint, envelope string) error {
	return s.db.WithContext(ctx).
		Model(&models.Credential{}).
		Where("id = ?", credentialID).
		Updates(map[string]any{
			"encrypted_secret": envelope,
			"updated_at":       time.Now().UTC(),
		}).Error
}

func (s *Store) ListEndpoints(ctx context.Context) ([]models.Endpoint, error) {
	return s.ListEndpointsWithScopes(ctx)
}

func (s *Store) ListEndpointsWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.Endpoint, error) {
	var items []models.Endpoint
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("provider_id asc, name asc, id asc").Find(&items).Error
}

func (s *Store) ListLanes(ctx context.Context) ([]models.RoutingLane, error) {
	var items []models.RoutingLane
	return items, s.db.WithContext(ctx).Order("name asc").Find(&items).Error
}

func (s *Store) ListLaneMemberships(ctx context.Context) ([]models.LaneMembership, error) {
	return s.ListLaneMembershipsWithScopes(ctx)
}

func (s *Store) ListLaneMembershipsWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.LaneMembership, error) {
	var items []models.LaneMembership
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("lane_id asc, manual_rank asc, id asc").Find(&items).Error
}

func (s *Store) ListLimitPolicies(ctx context.Context) ([]models.LimitPolicy, error) {
	return s.ListLimitPoliciesWithScopes(ctx)
}

func (s *Store) ListLimitPoliciesWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.LimitPolicy, error) {
	var items []models.LimitPolicy
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("scope_type asc, scope_id asc").Find(&items).Error
}

func (s *Store) ListObservedLimits(ctx context.Context) ([]models.ObservedLimit, error) {
	return s.ListObservedLimitsWithScopes(ctx)
}

func (s *Store) ListObservedLimitsWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.ObservedLimit, error) {
	var items []models.ObservedLimit
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("observed_at desc").Find(&items).Error
}

func (s *Store) ListPricingPolicies(ctx context.Context) ([]models.PricingPolicy, error) {
	return s.ListPricingPoliciesWithScopes(ctx)
}

func (s *Store) ListPricingPoliciesWithScopes(ctx context.Context, scopes ...func(*gorm.DB) *gorm.DB) ([]models.PricingPolicy, error) {
	var items []models.PricingPolicy
	return items, s.db.WithContext(ctx).Scopes(scopes...).Order("endpoint_id asc").Find(&items).Error
}

type RequestLogListOptions struct {
	Limit            int
	Offset           int
	Sort             string
	Direction        string
	PrimaryAction    string
	Domain           string
	GuardrailApplied *bool
	GuardrailStatus  string
	GuardrailPreset  string
	QueryScopes      []func(*gorm.DB) *gorm.DB
}

type RecentFlowRequestOptions struct {
	Limit        int
	LaneUUID     string
	EndpointUUID string
	QueryScopes  []func(*gorm.DB) *gorm.DB
}

type RequestLogListPage struct {
	Items      []models.RequestLog `json:"items"`
	Total      int64               `json:"total"`
	Limit      int                 `json:"limit"`
	Offset     int                 `json:"offset"`
	NextOffset int                 `json:"next_offset"`
	HasMore    bool                `json:"has_more"`
}

type RecentFlowRequest struct {
	RequestID             string     `json:"request_id"`
	ActorID               string     `json:"actor_id,omitempty" gorm:"-"`
	APIKeyUUID            string     `json:"api_key_uuid,omitempty" gorm:"-"`
	LaneUUID              *string    `json:"lane_id,omitempty" gorm:"column:lane_uuid"`
	EndpointUUID          *string    `json:"endpoint_id,omitempty" gorm:"column:endpoint_uuid"`
	ProviderUUID          *string    `json:"provider_id,omitempty" gorm:"column:provider_uuid"`
	IncomingModel         string     `json:"incoming_model"`
	SelectedUpstreamModel string     `json:"selected_upstream_model"`
	StatusCode            int        `json:"status_code"`
	TaskState             string     `json:"task_state"`
	QueuedAt              *time.Time `json:"queued_at"`
	StartedAt             *time.Time `json:"started_at"`
	FinishedAt            *time.Time `json:"finished_at"`
	WaitMS                int64      `json:"wait_ms"`
	LatencyMS             int64      `json:"latency_ms"`
	CharacterizationJSON  *string    `json:"-" gorm:"column:characterization_json"`
	CharacterizationMS    float64    `json:"characterization_duration_ms" gorm:"-"`
	GuardrailStatus       string     `json:"guardrail_status,omitempty" gorm:"column:guardrail_status"`
	GuardrailPreMS        int64      `json:"guardrail_pre_duration_ms" gorm:"column:guardrail_pre_duration_ms"`
	ProviderLatencyMS     int64      `json:"provider_latency_ms" gorm:"-"`
	GuardrailPostMS       int64      `json:"guardrail_post_duration_ms" gorm:"column:guardrail_post_duration_ms"`
	TotalTimeMS           float64    `json:"total_time_ms" gorm:"-"`
	FallbackCount         int        `json:"fallback_count"`
	EstimatedInputTokens  int64      `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64      `json:"estimated_output_tokens"`
	ActualInputTokens     int64      `json:"actual_input_tokens"`
	ActualOutputTokens    int64      `json:"actual_output_tokens"`
	ActualTotalTokens     int64      `json:"actual_total_tokens"`
	EstimatedCostMicros   int64      `json:"estimated_cost_micros"`
	ActualCostMicros      int64      `json:"actual_cost_micros"`
	ErrorText             string     `json:"error_text,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type RecentModelUsage struct {
	Key          string `json:"key" gorm:"-"`
	LaneUUID     string `json:"lane_id" gorm:"column:lane_id"`
	LaneName     string `json:"lane_name" gorm:"column:lane_name"`
	EndpointUUID string `json:"endpoint_id" gorm:"column:endpoint_id"`
	ProviderUUID string `json:"provider_id" gorm:"column:provider_id"`
	ProviderName string `json:"provider_name" gorm:"column:provider_name"`
	ModelName    string `json:"model_name" gorm:"column:model_name"`
	LastUsedAt   string `json:"last_used_at" gorm:"column:last_used_at"`
	RequestCount int64  `json:"request_count" gorm:"column:request_count"`
}

func (s *Store) ListRequestLogs(ctx context.Context, limit int) ([]models.RequestLog, error) {
	return s.ListRequestLogsWithOptions(ctx, RequestLogListOptions{Limit: limit})
}

func (s *Store) ListRequestLogsWithOptions(ctx context.Context, opts RequestLogListOptions) ([]models.RequestLog, error) {
	var items []models.RequestLog
	limit, offset, order, err := NormalizeRequestLogListOptions(opts)
	if err != nil {
		return nil, err
	}
	query := applyRequestLogFilters(s.db.WithContext(ctx).Scopes(opts.QueryScopes...), opts)
	if err := query.
		Select([]string{
			"id",
			"uuid",
			"request_id",
			"lane_id",
			"lane_uuid",
			"endpoint_id",
			"endpoint_uuid",
			"provider_id",
			"provider_uuid",
			"route_kind",
			"incoming_model",
			"selected_upstream_model",
			"status_code",
			"task_state",
			"queued_at",
			"started_at",
			"finished_at",
			"wait_ms",
			"latency_ms",
			"streaming",
			"priority",
			"fallback_count",
			"estimated_input_tokens",
			"estimated_output_tokens",
			"actual_input_tokens",
			"actual_output_tokens",
			"actual_total_tokens",
			"estimated_cost_micros",
			"actual_cost_micros",
			"error_text",
			"primary_action",
			"action_confidence",
			"characterization_version",
			"characterization_json",
			"guardrail_status",
			"guardrail_duration_ms",
			"guardrail_pre_duration_ms",
			"guardrail_post_duration_ms",
			"guardrail_results_json",
			"created_at",
			"updated_at",
		}).
		Order(order).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: order.Desc}).
		Limit(limit).
		Offset(offset).
		Find(&items).Error; err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}
	requestIDs := make([]string, 0, len(items))
	for _, item := range items {
		requestIDs = append(requestIDs, item.RequestID)
	}
	var storedIDs []string
	if err := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Where("request_id IN ? AND (COALESCE(request_body_json, '') <> '' OR COALESCE(upstream_request_json, '') <> '' OR COALESCE(response_body_json, '') <> '')", requestIDs).
		Pluck("request_id", &storedIDs).Error; err != nil {
		return nil, err
	}
	stored := make(map[string]bool, len(storedIDs))
	for _, requestID := range storedIDs {
		stored[requestID] = true
	}
	for index := range items {
		items[index].RequestBodiesStored = stored[items[index].RequestID]
	}
	actors, err := s.requestLogActorIDs(ctx, requestIDs, opts.QueryScopes)
	if err != nil {
		return nil, err
	}
	apiKeyUUIDs, err := s.requestLogAPIKeyUUIDs(ctx, requestIDs, opts.QueryScopes)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].ActorID = actors[items[index].RequestID]
		items[index].APIKeyUUID = apiKeyUUIDs[items[index].RequestID]
	}
	return items, nil
}

func populateRecentFlowTimings(item *RecentFlowRequest) {
	if item == nil {
		return
	}
	background := false
	if item.CharacterizationJSON != nil && strings.TrimSpace(*item.CharacterizationJSON) != "" {
		var result characterization.Characterization
		if json.Unmarshal([]byte(*item.CharacterizationJSON), &result) == nil && result.ClassificationDurationMS > 0 {
			item.CharacterizationMS = result.ClassificationDurationMS
			background = result.ClassificationBackground
		}
	}
	if !strings.EqualFold(strings.TrimSpace(item.GuardrailStatus), "blocked_pre") {
		item.ProviderLatencyMS = max(item.LatencyMS-item.GuardrailPreMS-item.GuardrailPostMS, 0)
	}
	item.TotalTimeMS = float64(max(item.WaitMS, 0)+max(item.GuardrailPreMS, 0)+item.ProviderLatencyMS+max(item.GuardrailPostMS, 0)) + item.CharacterizationMS
	if background {
		item.TotalTimeMS -= item.CharacterizationMS
	}
}

func (s *Store) CountRequestLogs(ctx context.Context) (int64, error) {
	return s.CountRequestLogsWithScopes(ctx, nil)
}

func (s *Store) CountRequestLogsWithScopes(ctx context.Context, scopes []func(*gorm.DB) *gorm.DB) (int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&models.RequestLog{}).Scopes(scopes...).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) countRequestLogsWithOptions(ctx context.Context, opts RequestLogListOptions) (int64, error) {
	var total int64
	query := s.db.WithContext(ctx).Model(&models.RequestLog{}).Scopes(opts.QueryScopes...)
	if err := applyRequestLogFilters(query, opts).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) ListRequestLogPage(ctx context.Context, opts RequestLogListOptions) (RequestLogListPage, error) {
	limit, offset, _, err := NormalizeRequestLogListOptions(opts)
	if err != nil {
		return RequestLogListPage{}, err
	}
	total, err := s.countRequestLogsWithOptions(ctx, opts)
	if err != nil {
		return RequestLogListPage{}, err
	}
	items, err := s.ListRequestLogsWithOptions(ctx, opts)
	if err != nil {
		return RequestLogListPage{}, err
	}
	nextOffset := offset + len(items)
	return RequestLogListPage{
		Items:      items,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
		NextOffset: nextOffset,
		HasMore:    int64(nextOffset) < total,
	}, nil
}

func (s *Store) ListRecentFlowRequests(ctx context.Context, limit int) ([]RecentFlowRequest, error) {
	return s.ListRecentFlowRequestsWithOptions(ctx, RecentFlowRequestOptions{Limit: limit})
}

func (s *Store) ListRecentFlowRequestsWithOptions(ctx context.Context, opts RecentFlowRequestOptions) ([]RecentFlowRequest, error) {
	limit, laneUUID, endpointUUID, err := NormalizeRecentFlowRequestOptions(opts)
	if err != nil {
		return nil, err
	}
	var items []RecentFlowRequest
	query := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Scopes(opts.QueryScopes...).
		Select([]string{
			"request_id",
			"lane_uuid",
			"endpoint_uuid",
			"provider_uuid",
			"incoming_model",
			"selected_upstream_model",
			"status_code",
			"task_state",
			"queued_at",
			"started_at",
			"finished_at",
			"wait_ms",
			"latency_ms",
			"characterization_json",
			"guardrail_status",
			"guardrail_pre_duration_ms",
			"guardrail_post_duration_ms",
			"fallback_count",
			"estimated_input_tokens",
			"estimated_output_tokens",
			"actual_input_tokens",
			"actual_output_tokens",
			"actual_total_tokens",
			"estimated_cost_micros",
			"actual_cost_micros",
			"error_text",
			"created_at",
			"updated_at",
		}).
		Where("task_state IN ?", []string{"completed", "failed", "cancelled"})
	if laneUUID != "" {
		query = query.Where("lane_uuid = ?", laneUUID)
	}
	if endpointUUID != "" {
		query = query.Where("endpoint_uuid = ?", endpointUUID)
	}
	if err := query.
		Order(clause.OrderByColumn{Column: clause.Column{Name: "created_at"}, Desc: true}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: true}).
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	requestIDs := make([]string, 0, len(items))
	for _, item := range items {
		requestIDs = append(requestIDs, item.RequestID)
	}
	actors, err := s.requestLogActorIDs(ctx, requestIDs, opts.QueryScopes)
	if err != nil {
		return nil, err
	}
	apiKeyUUIDs, err := s.requestLogAPIKeyUUIDs(ctx, requestIDs, opts.QueryScopes)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].ActorID = actors[items[index].RequestID]
		items[index].APIKeyUUID = apiKeyUUIDs[items[index].RequestID]
		populateRecentFlowTimings(&items[index])
	}
	return items, nil
}

func (s *Store) ListRecentModelUsage(ctx context.Context, limit int) ([]RecentModelUsage, error) {
	return s.ListRecentModelUsageWithScopes(ctx, limit, nil)
}

func (s *Store) ListRecentModelUsageWithScopes(ctx context.Context, limit int, scopes []func(*gorm.DB) *gorm.DB) ([]RecentModelUsage, error) {
	limit = NormalizeRecentModelUsageLimit(limit)
	modelNameExpr := "COALESCE(NULLIF(endpoints.name, ''), NULLIF(endpoints.upstream_model, ''), NULLIF(request_logs.selected_upstream_model, ''), NULLIF(request_logs.incoming_model, ''), 'Unknown model')"
	lastUsedExpr := "MAX(COALESCE(request_logs.finished_at, request_logs.updated_at, request_logs.started_at, request_logs.queued_at, request_logs.created_at))"

	var items []RecentModelUsage
	if err := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Scopes(scopes...).
		Select(
			"COALESCE(request_logs.lane_uuid, routing_lanes.uuid, '') AS lane_id, "+
				"COALESCE(routing_lanes.name, '') AS lane_name, "+
				"COALESCE(request_logs.endpoint_uuid, endpoints.uuid, '') AS endpoint_id, "+
				"COALESCE(providers.uuid, request_logs.provider_uuid, '') AS provider_id, "+
				"COALESCE(providers.name, '') AS provider_name, "+
				modelNameExpr+" AS model_name, "+
				lastUsedExpr+" AS last_used_at, "+
				"COUNT(*) AS request_count",
		).
		Joins("LEFT JOIN routing_lanes ON routing_lanes.id = request_logs.lane_id"+s.sameTenantJoin(ctx, "request_logs", "routing_lanes")).
		Joins("LEFT JOIN endpoints ON endpoints.id = request_logs.endpoint_id"+s.sameTenantJoin(ctx, "request_logs", "endpoints")).
		Joins("LEFT JOIN providers ON providers.id = COALESCE(endpoints.provider_id, request_logs.provider_id)"+s.sameTenantJoin(ctx, "request_logs", "providers")).
		Where("request_logs.task_state IN ?", []string{"completed", "failed", "cancelled"}).
		Where("(request_logs.endpoint_id IS NOT NULL OR request_logs.endpoint_uuid IS NOT NULL OR request_logs.selected_upstream_model <> '' OR request_logs.incoming_model <> '')").
		Group(
			"COALESCE(request_logs.lane_uuid, routing_lanes.uuid, ''), " +
				"COALESCE(routing_lanes.name, ''), " +
				"COALESCE(request_logs.endpoint_uuid, endpoints.uuid, ''), " +
				"COALESCE(providers.uuid, request_logs.provider_uuid, ''), " +
				"COALESCE(providers.name, ''), " +
				modelNameExpr,
		).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "last_used_at"}, Desc: true}).
		Limit(limit).
		Scan(&items).Error; err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Key = recentModelUsageKey(items[index])
	}
	return items, nil
}

func recentModelUsageKey(item RecentModelUsage) string {
	modelKey := item.EndpointUUID
	if modelKey == "" {
		modelKey = item.ModelName
	}
	return item.LaneUUID + ":" + modelKey
}

func NormalizeRecentFlowActivityLimit(limit int) int {
	if limit <= 0 {
		return DefaultRecentFlowActivityLimit
	}
	if limit > MaxRecentFlowActivityLimit {
		return MaxRecentFlowActivityLimit
	}
	return limit
}

func NormalizeRecentFlowRequestOptions(opts RecentFlowRequestOptions) (int, string, string, error) {
	limit := NormalizeRecentFlowActivityLimit(opts.Limit)
	laneUUID := strings.TrimSpace(opts.LaneUUID)
	endpointUUID := strings.TrimSpace(opts.EndpointUUID)
	if laneUUID != "" {
		parsed, err := parsePublicUUID(laneUUID)
		if err != nil {
			return 0, "", "", ErrInvalidRecentFlowLaneID
		}
		laneUUID = parsed
	}
	if endpointUUID != "" {
		parsed, err := parsePublicUUID(endpointUUID)
		if err != nil {
			return 0, "", "", ErrInvalidRecentFlowEndpointID
		}
		endpointUUID = parsed
	}
	return limit, laneUUID, endpointUUID, nil
}

func NormalizeRecentModelUsageLimit(limit int) int {
	if limit <= 0 {
		return DefaultRecentModelUsageLimit
	}
	if limit > MaxRecentModelUsageLimit {
		return MaxRecentModelUsageLimit
	}
	return limit
}

func NormalizeRequestLogListOptions(opts RequestLogListOptions) (int, int, clause.OrderByColumn, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultRequestLogListLimit
	}
	if limit > MaxRequestLogListLimit {
		limit = MaxRequestLogListLimit
	}
	offset := opts.Offset
	if offset < 0 {
		return 0, 0, clause.OrderByColumn{}, ErrInvalidRequestLogOffset
	}
	if opts.PrimaryAction != "" && opts.PrimaryAction != string(characterization.ActionUnknown) && !characterization.ValidAction(opts.PrimaryAction) {
		return 0, 0, clause.OrderByColumn{}, ErrInvalidRequestLogAction
	}
	if opts.Domain != "" && !characterization.ValidDomain(opts.Domain) {
		return 0, 0, clause.OrderByColumn{}, ErrInvalidRequestLogDomain
	}
	if status := strings.TrimSpace(opts.GuardrailStatus); status != "" {
		valid := map[string]bool{"none": true, "passed": true, "blocked_pre": true, "blocked_post": true, "replaced_post": true, "fail_open": true, "error": true}
		if !valid[status] {
			return 0, 0, clause.OrderByColumn{}, ErrInvalidGuardrailStatus
		}
	}
	if preset := strings.TrimSpace(opts.GuardrailPreset); preset != "" {
		if len(preset) > 64 || models.SlugifyName(preset) != preset {
			return 0, 0, clause.OrderByColumn{}, ErrInvalidGuardrailPreset
		}
	}

	sortKey := opts.Sort
	if sortKey == "" {
		sortKey = "created_at"
	}
	column, ok := allowedRequestLogSortColumns[sortKey]
	if !ok {
		return 0, 0, clause.OrderByColumn{}, ErrInvalidRequestLogSort
	}

	direction := opts.Direction
	if direction == "" {
		direction = "desc"
	}
	var desc bool
	switch direction {
	case "asc":
		desc = false
	case "desc":
		desc = true
	default:
		return 0, 0, clause.OrderByColumn{}, ErrInvalidRequestLogDirection
	}

	return limit, offset, clause.OrderByColumn{
		Column: clause.Column{Name: column},
		Desc:   desc,
	}, nil
}

func applyRequestLogFilters(query *gorm.DB, opts RequestLogListOptions) *gorm.DB {
	if action := strings.TrimSpace(opts.PrimaryAction); action != "" {
		query = query.Where("primary_action = ?", action)
	}
	if domain := strings.TrimSpace(opts.Domain); domain != "" {
		// Characterization JSON is compact and its domains are a validated enum.
		// This parameterized pattern matches a quoted member only within the
		// domains array and works on both SQLite and PostgreSQL.
		query = query.Where("characterization_json LIKE ?", `%"domains":[%"`+domain+`"%]%`)
	}
	if opts.GuardrailApplied != nil {
		if *opts.GuardrailApplied {
			query = query.Where("guardrail_status <> '' AND guardrail_status <> ?", "none")
		} else {
			query = query.Where("guardrail_status = '' OR guardrail_status = ?", "none")
		}
	}
	if status := strings.TrimSpace(opts.GuardrailStatus); status != "" {
		query = query.Where("guardrail_status = ?", status)
	}
	if preset := strings.TrimSpace(opts.GuardrailPreset); preset != "" {
		query = query.Where("guardrail_results_json LIKE ?", `%"preset":"`+preset+`"%`)
	}
	return query
}

var allowedRequestLogSortColumns = map[string]string{
	"created_at":  "created_at",
	"updated_at":  "updated_at",
	"queued_at":   "queued_at",
	"started_at":  "started_at",
	"finished_at": "finished_at",
	"status_code": "status_code",
	"wait_ms":     "wait_ms",
	"latency_ms":  "latency_ms",
}

func (s *Store) GetRequestLog(ctx context.Context, requestID string) (models.RequestLog, error) {
	return s.GetRequestLogWithScopes(ctx, requestID, nil)
}

func (s *Store) GetRequestLogWithScopes(ctx context.Context, requestID string, scopes []func(*gorm.DB) *gorm.DB) (models.RequestLog, error) {
	var item models.RequestLog
	err := s.db.WithContext(ctx).Scopes(scopes...).Where("request_id = ?", requestID).First(&item).Error
	item.RequestBodiesStored = item.RequestBodyJSON != "" || item.UpstreamRequestJSON != "" || item.ResponseBodyJSON != ""
	if err == nil {
		actors, actorErr := s.requestLogActorIDs(ctx, []string{requestID}, scopes)
		if actorErr != nil {
			return item, actorErr
		}
		item.ActorID = actors[requestID]
		apiKeyUUIDs, apiKeyErr := s.requestLogAPIKeyUUIDs(ctx, []string{requestID}, scopes)
		if apiKeyErr != nil {
			return item, apiKeyErr
		}
		item.APIKeyUUID = apiKeyUUIDs[requestID]
		if diagnosticErr := s.loadRequestLogDiagnostics(ctx, &item); diagnosticErr != nil {
			return item, diagnosticErr
		}
	}
	return item, err
}

func (s *Store) loadRequestLogDiagnostics(ctx context.Context, item *models.RequestLog) error {
	if item == nil || item.ID == 0 {
		return nil
	}
	var diagnostic models.RequestLogDiagnostics
	query := s.db.WithContext(ctx).Where("request_log_id = ?", item.ID)
	if scope, ok := s.tenantScopeForTable(ctx, "request_log_diagnostics"); ok {
		var row tenantRequestLogDiagnosticsRow
		err := query.Where("organization_uuid = ?", scope.OrganizationUUID).First(&row).Error
		if err == nil {
			diagnostic = row.RequestLogDiagnostics
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	} else {
		err := query.First(&diagnostic).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if diagnostic.ID != 0 {
		item.CandidateTraceJSON = diagnostic.CandidateTraceJSON
		if jsonEmpty(item.CandidateTraceJSON) {
			item.CandidateTraceJSON = synthesizeSelectedRouteDiagnostics(*item)
		}
		item.LimitImpactJSON = diagnostic.LimitImpactJSON
		item.AppliedOverridesJSON = diagnostic.AppliedOverridesJSON
		return nil
	}
	item.CandidateTraceJSON = synthesizeSelectedRouteDiagnostics(*item)
	item.LimitImpactJSON = "[]"
	item.AppliedOverridesJSON = "{}"
	return nil
}

func synthesizeSelectedRouteDiagnostics(item models.RequestLog) string {
	if item.EndpointUUID == nil && item.ProviderUUID == nil && strings.TrimSpace(item.SelectedUpstreamModel) == "" {
		return "[]"
	}
	payload := []map[string]any{{
		"endpoint_id":    pointerString(item.EndpointUUID),
		"provider_id":    pointerString(item.ProviderUUID),
		"upstream_model": item.SelectedUpstreamModel,
		"decision":       "selected",
	}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type requestLogActorRow struct {
	RequestID string `gorm:"column:request_id"`
	ActorID   string `gorm:"column:actor_id"`
}

func (s *Store) requestLogActorIDs(ctx context.Context, requestIDs []string, scopes []func(*gorm.DB) *gorm.DB) (map[string]string, error) {
	out := make(map[string]string, len(requestIDs))
	if len(requestIDs) == 0 || !s.db.Migrator().HasColumn("request_logs", "user_uuid") {
		return out, nil
	}
	var rows []requestLogActorRow
	if err := s.db.WithContext(ctx).Model(&models.RequestLog{}).Scopes(scopes...).Select("request_id, user_uuid AS actor_id").Where("request_id IN ?", requestIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.RequestID] = row.ActorID
	}
	return out, nil
}

type requestLogAPIKeyRow struct {
	RequestID  string `gorm:"column:request_id"`
	APIKeyUUID string `gorm:"column:api_key_uuid"`
}

func (s *Store) requestLogAPIKeyUUIDs(ctx context.Context, requestIDs []string, scopes []func(*gorm.DB) *gorm.DB) (map[string]string, error) {
	out := make(map[string]string, len(requestIDs))
	if len(requestIDs) == 0 || !s.db.Migrator().HasColumn("request_logs", "api_key_uuid") {
		return out, nil
	}
	var rows []requestLogAPIKeyRow
	if err := s.db.WithContext(ctx).Model(&models.RequestLog{}).Scopes(scopes...).Select("request_id, api_key_uuid").Where("request_id IN ?", requestIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.RequestID] = row.APIKeyUUID
	}
	return out, nil
}

func (s *Store) Create(ctx context.Context, target any) error {
	var restoreLog *models.RequestLog
	var originalTrace, originalLimits, originalOverrides string
	if log, ok := target.(*models.RequestLog); ok && log != nil {
		restoreLog = log
		originalTrace, originalLimits, originalOverrides = log.CandidateTraceJSON, log.LimitImpactJSON, log.AppliedOverridesJSON
		defer func() {
			restoreLog.CandidateTraceJSON = originalTrace
			restoreLog.LimitImpactJSON = originalLimits
			restoreLog.AppliedOverridesJSON = originalOverrides
		}()
	}
	diagnostic := detachRequestLogDiagnostics(target)
	if err := s.prepareCatalogIdentity(ctx, target); err != nil {
		return err
	}
	if err := s.preparePublicUUIDs(ctx, target); err != nil {
		return err
	}
	if handled, err := s.createWithTenantScope(ctx, target); handled {
		if err == nil && isCatalogConfiguration(target) {
			s.catalog.invalidate(ctx, isRoutingConfiguration(target))
		}
		if err != nil {
			return err
		}
		return s.persistRequestLogDiagnostics(ctx, target, diagnostic)
	}
	if err := s.db.WithContext(ctx).Create(target).Error; err != nil {
		return err
	}
	err := tenancy.StampCreated(ctx, s.db, target)
	if err == nil && isCatalogConfiguration(target) {
		s.catalog.invalidate(ctx, isRoutingConfiguration(target))
	}
	if err != nil {
		return err
	}
	return s.persistRequestLogDiagnostics(ctx, target, diagnostic)
}

func detachRequestLogDiagnostics(target any) *models.RequestLogDiagnostics {
	log, ok := target.(*models.RequestLog)
	if !ok || log == nil {
		return nil
	}
	diagnostic := requestLogDiagnosticsFor(*log)
	log.CandidateTraceJSON = ""
	log.LimitImpactJSON = ""
	log.AppliedOverridesJSON = ""
	return diagnostic
}

func requestLogDiagnosticsFor(log models.RequestLog) *models.RequestLogDiagnostics {
	trace := compactCandidateTrace(log.CandidateTraceJSON, log.LaneUUID != nil && strings.TrimSpace(*log.LaneUUID) != "")
	limitsJSON := compactLimitImpact(log.LimitImpactJSON)
	overrides := strings.TrimSpace(log.AppliedOverridesJSON)
	// compactCandidateTrace already removes an ordinary direct selection. Any
	// trace left here is intentional, including a grouped route's selected rank.
	if jsonEmpty(trace) && jsonEmpty(limitsJSON) && jsonEmpty(overrides) {
		return nil
	}
	return &models.RequestLogDiagnostics{
		RequestUUID:          log.UUID,
		CandidateTraceJSON:   trace,
		LimitImpactJSON:      limitsJSON,
		AppliedOverridesJSON: overrides,
	}
}

// compactCandidateTrace keeps only decision diagnostics. Effective limits are
// persisted in limit_impact_json, and an ordinary selected route is already
// represented by the request log's canonical lane/provider/endpoint columns.
func compactCandidateTrace(value string, keepSelected bool) string {
	value = strings.TrimSpace(value)
	if jsonEmpty(value) {
		return ""
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(value), &rows); err != nil {
		return value
	}
	compacted := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		decision, _ := row["decision"].(string)
		if decision == "not_needed" {
			continue
		}
		compacted = append(compacted, selectDiagnosticFields(row,
			"rank", "decision", "reason", "eligible_at", "predicted_wait_ms",
			"endpoint_id", "endpoint_name", "provider_id", "provider_name", "upstream_model",
		))
	}
	rows = compacted
	if len(rows) == 1 {
		decision, _ := rows[0]["decision"].(string)
		rank, _ := rows[0]["rank"].(float64)
		if decision == "selected" && !keepSelected && rank <= 1 {
			return ""
		}
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return value
	}
	return string(encoded)
}

// compactLimitImpact stores only limits that actually applied to the selected
// model. Older payloads contained the complete scope x metric x period matrix,
// including dozens of null rows that conveyed no scheduling information.
func compactLimitImpact(value string) string {
	value = strings.TrimSpace(value)
	if jsonEmpty(value) {
		return ""
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(value), &rows); err != nil {
		return value
	}
	compacted := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row["configured"] == nil && row["observed"] == nil && row["effective"] == nil {
			continue
		}
		compacted = append(compacted, selectDiagnosticFields(row,
			"metric", "period", "scope_type", "scope_id",
			"configured", "observed", "effective", "used", "reserved", "next_available_at",
		))
	}
	if len(compacted) == 0 {
		return ""
	}
	encoded, err := json.Marshal(compacted)
	if err != nil {
		return value
	}
	return string(encoded)
}

func selectDiagnosticFields(row map[string]any, fields ...string) map[string]any {
	selected := make(map[string]any, len(fields))
	for _, field := range fields {
		if value, ok := row[field]; ok && value != nil {
			selected[field] = value
		}
	}
	return selected
}

func jsonEmpty(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "null" || value == "[]" || value == "{}"
}

func (s *Store) persistRequestLogDiagnostics(ctx context.Context, target any, diagnostic *models.RequestLogDiagnostics) error {
	if diagnostic == nil {
		return nil
	}
	log, ok := target.(*models.RequestLog)
	if !ok || log == nil || log.ID == 0 {
		return nil
	}
	diagnostic.RequestLogID = log.ID
	diagnostic.RequestUUID = log.UUID
	conflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "request_log_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"request_uuid", "candidate_trace_json", "limit_impact_json", "applied_overrides_json", "updated_at",
		}),
	}
	if scope, ok := s.tenantScopeForTable(ctx, "request_log_diagnostics"); ok {
		row := tenantRequestLogDiagnosticsRow{RequestLogDiagnostics: *diagnostic, OrganizationUUID: scope.OrganizationUUID}
		return s.db.WithContext(ctx).Omit("RequestLog").Clauses(conflict).Create(&row).Error
	}
	return s.db.WithContext(ctx).Omit("RequestLog").Clauses(conflict).Create(diagnostic).Error
}

func (s *Store) Save(ctx context.Context, target any) error {
	if err := s.prepareCatalogIdentity(ctx, target); err != nil {
		return err
	}
	if err := s.preparePublicUUIDs(ctx, target); err != nil {
		return err
	}
	if s.tenantScopedTarget(ctx, target) {
		result := s.db.WithContext(ctx).Model(target).Select("*").Updates(target)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if isCatalogConfiguration(target) {
			s.catalog.invalidate(ctx, isRoutingConfiguration(target))
		}
		return nil
	}
	if err := s.db.WithContext(ctx).Save(target).Error; err != nil {
		return err
	}
	err := tenancy.StampCreated(ctx, s.db, target)
	if err == nil && isCatalogConfiguration(target) {
		s.catalog.invalidate(ctx, isRoutingConfiguration(target))
	}
	return err
}

func (s *Store) prepareCatalogIdentity(ctx context.Context, target any) error {
	switch item := target.(type) {
	case *models.Provider:
		item.Name = strings.TrimSpace(item.Name)
		item.Slug = models.SlugifyName(item.Name)
		var count int64
		query := s.db.WithContext(ctx).Model(&models.Provider{}).
			Where("(lower(name) = lower(?) OR slug = ?)", item.Name, item.Slug)
		if item.ID != 0 {
			query = query.Where("id <> ?", item.ID)
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrProviderNameConflict
		}
	case *models.Endpoint:
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			item.Name = strings.TrimSpace(item.UpstreamModel)
		}
		item.Slug = models.SlugifyName(item.Name)
		var count int64
		query := s.db.WithContext(ctx).Model(&models.Endpoint{}).
			Where("provider_id = ? AND (lower(name) = lower(?) OR slug = ?)", item.ProviderID, item.Name, item.Slug)
		if item.ID != 0 {
			query = query.Where("id <> ?", item.ID)
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrEndpointNameConflict
		}
	case *models.RoutingLane:
		item.Name = strings.TrimSpace(item.Name)
		item.Slug = models.SlugifyName(item.Name)
		var count int64
		query := s.db.WithContext(ctx).Model(&models.RoutingLane{}).
			Where("(lower(name) = lower(?) OR slug = ?)", item.Name, item.Slug)
		if item.ID != 0 {
			query = query.Where("id <> ?", item.ID)
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrRoutingLaneNameConflict
		}
	case *models.Guardrail:
		item.Name = strings.TrimSpace(item.Name)
		item.Slug = models.SlugifyName(item.Name)
		var count int64
		query := s.db.WithContext(ctx).Model(&models.Guardrail{}).Where("(lower(name) = lower(?) OR slug = ?)", item.Name, item.Slug)
		if item.ID != 0 {
			query = query.Where("id <> ?", item.ID)
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrGuardrailNameConflict
		}
	}
	return nil
}

// SaveEndpointState persists only mutable scheduler health. Routing identity,
// provider binding, ranks, pricing, and policy configuration remain in the
// immutable catalog and are invalidated only by an explicit configuration
// mutation through Save/Create/Delete.
func (s *Store) SaveEndpointState(ctx context.Context, endpoint models.Endpoint) error {
	if s == nil || s.db == nil || endpoint.ID == 0 {
		return gorm.ErrRecordNotFound
	}
	result := s.db.WithContext(ctx).Model(&models.Endpoint{}).
		Where("id = ?", endpoint.ID).
		Updates(map[string]any{
			"health_status":        endpoint.HealthStatus,
			"cooldown_until":       endpoint.CooldownUntil,
			"cooldown_reason":      endpoint.CooldownReason,
			"cooldown_status_code": endpoint.CooldownStatusCode,
			"updated_at":           time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	s.catalog.rememberEndpointState(ctx, endpoint)
	return nil
}

func (s *Store) tenantScopedTarget(ctx context.Context, target any) bool {
	if s == nil || s.db == nil || target == nil {
		return false
	}
	if _, ok := tenancy.ScopeFromContext(ctx); !ok {
		return false
	}
	stmt := &gorm.Statement{DB: s.db}
	if err := stmt.Parse(target); err != nil || stmt.Schema == nil {
		return false
	}
	switch stmt.Schema.Table {
	case "app_settings", "providers", "credentials", "endpoints", "routing_lanes", "lane_memberships", "limit_policies", "limit_policy_states", "observed_limits", "pricing_policies", "guardrails", "guardrail_credentials", "guardrail_bindings", "request_logs":
		return true
	default:
		return false
	}
}

func (s *Store) DeleteByID(ctx context.Context, target any, id uint) error {
	err := s.db.WithContext(ctx).Delete(target, id).Error
	if err == nil && isCatalogConfiguration(target) {
		s.catalog.invalidate(ctx, isRoutingConfiguration(target))
	}
	return err
}

func (s *Store) FindByUUID(ctx context.Context, target any, publicUUID string) error {
	parsed, err := parsePublicUUID(publicUUID)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Where("uuid = ?", parsed).First(target).Error
}

func (s *Store) DeleteByUUID(ctx context.Context, target any, publicUUID string) error {
	if err := s.FindByUUID(ctx, target, publicUUID); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Delete(target).Error
	if err == nil && isCatalogConfiguration(target) {
		s.catalog.invalidate(ctx, isRoutingConfiguration(target))
	}
	return err
}

func (s *Store) createWithTenantScope(ctx context.Context, target any) (bool, error) {
	switch item := target.(type) {
	case *models.Provider:
		scope, ok := s.tenantScopeForTable(ctx, "providers")
		if !ok {
			return false, nil
		}
		row := tenantProviderRow{Provider: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.Provider
		return true, nil
	case *models.Credential:
		scope, ok := s.tenantScopeForTable(ctx, "credentials")
		if !ok {
			return false, nil
		}
		row := tenantCredentialRow{Credential: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.Credential
		return true, nil
	case *models.Endpoint:
		scope, ok := s.tenantScopeForTable(ctx, "endpoints")
		if !ok {
			return false, nil
		}
		row := tenantEndpointRow{Endpoint: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.Endpoint
		return true, nil
	case *models.RoutingLane:
		scope, ok := s.tenantScopeForTable(ctx, "routing_lanes")
		if !ok {
			return false, nil
		}
		row := tenantRoutingLaneRow{RoutingLane: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.RoutingLane
		return true, nil
	case *models.LaneMembership:
		scope, ok := s.tenantScopeForTable(ctx, "lane_memberships")
		if !ok {
			return false, nil
		}
		row := tenantLaneMembershipRow{LaneMembership: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.LaneMembership
		return true, nil
	case *models.LimitPolicy:
		scope, ok := s.tenantScopeForTable(ctx, "limit_policies")
		if !ok {
			return false, nil
		}
		row := tenantLimitPolicyRow{LimitPolicy: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.LimitPolicy
		return true, nil
	case *models.LimitPolicyState:
		scope, ok := s.tenantScopeForTable(ctx, "limit_policy_states")
		if !ok {
			return false, nil
		}
		row := tenantLimitPolicyStateRow{LimitPolicyState: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Omit("Policy").Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.LimitPolicyState
		return true, nil
	case *models.ObservedLimit:
		scope, ok := s.tenantScopeForTable(ctx, "observed_limits")
		if !ok {
			return false, nil
		}
		row := tenantObservedLimitRow{ObservedLimit: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.ObservedLimit
		return true, nil
	case *models.PricingPolicy:
		scope, ok := s.tenantScopeForTable(ctx, "pricing_policies")
		if !ok {
			return false, nil
		}
		row := tenantPricingPolicyRow{PricingPolicy: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.PricingPolicy
		return true, nil
	case *models.Guardrail:
		scope, ok := s.tenantScopeForTable(ctx, "guardrails")
		if !ok {
			return false, nil
		}
		row := tenantGuardrailRow{Guardrail: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.Guardrail
		return true, nil
	case *models.GuardrailCredential:
		scope, ok := s.tenantScopeForTable(ctx, "guardrail_credentials")
		if !ok {
			return false, nil
		}
		row := tenantGuardrailCredentialRow{GuardrailCredential: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.GuardrailCredential
		return true, nil
	case *models.GuardrailBinding:
		scope, ok := s.tenantScopeForTable(ctx, "guardrail_bindings")
		if !ok {
			return false, nil
		}
		row := tenantGuardrailBindingRow{GuardrailBinding: *item, OrganizationUUID: scope.OrganizationUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.GuardrailBinding
		return true, nil
	case *models.RequestLog:
		scope, ok := s.tenantScopeForTable(ctx, "request_logs")
		if !ok {
			return false, nil
		}
		row := tenantRequestLogRow{RequestLog: *item, OrganizationUUID: scope.OrganizationUUID, UserUUID: scope.UserUUID}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return true, err
		}
		*item = row.RequestLog
		return true, nil
	default:
		return false, nil
	}
}

func (s *Store) DeleteProviderCascade(ctx context.Context, providerID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var provider models.Provider
		if err := tx.First(&provider, providerID).Error; err != nil {
			return err
		}

		var endpoints []models.Endpoint
		if err := tx.Where("provider_id = ?", providerID).Find(&endpoints).Error; err != nil {
			return err
		}

		endpointIDs := make([]uint, 0, len(endpoints))
		for _, endpoint := range endpoints {
			endpointIDs = append(endpointIDs, endpoint.ID)
		}

		if len(endpointIDs) > 0 {
			if err := tx.Where("endpoint_id IN ?", endpointIDs).Delete(&models.LaneMembership{}).Error; err != nil {
				return err
			}
			if err := tx.Where("endpoint_id IN ?", endpointIDs).Delete(&models.PricingPolicy{}).Error; err != nil {
				return err
			}
			if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeEndpoint, endpointIDs); err != nil {
				return err
			}
			if err := tx.Where("scope_type = ? AND scope_id IN ?", models.ScopeEndpoint, endpointIDs).Delete(&models.LimitPolicy{}).Error; err != nil {
				return err
			}
			if err := tx.Where("scope_type = ? AND scope_id IN ?", models.ScopeEndpoint, endpointIDs).Delete(&models.ObservedLimit{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("provider_id = ?", providerID).Delete(&models.Endpoint{}).Error; err != nil {
				return err
			}
		}

		if err := tx.Where("provider_id = ?", providerID).Delete(&models.Credential{}).Error; err != nil {
			return err
		}
		if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeProvider, []uint{providerID}); err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeProvider, providerID).Delete(&models.LimitPolicy{}).Error; err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeProvider, providerID).Delete(&models.ObservedLimit{}).Error; err != nil {
			return err
		}

		return tx.Unscoped().Delete(&provider).Error
	})
	if err == nil {
		s.catalog.invalidate(ctx, true)
	}
	return err
}

func (s *Store) DeleteEndpointCascade(ctx context.Context, endpointID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var endpoint models.Endpoint
		if err := tx.First(&endpoint, endpointID).Error; err != nil {
			return err
		}

		if err := tx.Where("endpoint_id = ?", endpointID).Delete(&models.LaneMembership{}).Error; err != nil {
			return err
		}
		if err := tx.Where("endpoint_id = ?", endpointID).Delete(&models.PricingPolicy{}).Error; err != nil {
			return err
		}
		if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeEndpoint, []uint{endpointID}); err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeEndpoint, endpointID).Delete(&models.LimitPolicy{}).Error; err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeEndpoint, endpointID).Delete(&models.ObservedLimit{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&endpoint).Error
	})
	if err == nil {
		s.catalog.invalidate(ctx, true)
	}
	return err
}

// SoftDeleteProviderCascade removes a provider and its endpoints from the
// active catalog without destroying their records or dependent configuration.
// Pro uses this path so provider history and relationships remain recoverable.
func (s *Store) SoftDeleteProviderCascade(ctx context.Context, providerID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var provider models.Provider
		if err := tx.First(&provider, providerID).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Model(&models.Endpoint{}).
			Where("provider_id = ?", providerID).
			Updates(map[string]any{"enabled": false, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("provider_id = ?", providerID).Delete(&models.Endpoint{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&provider).Updates(map[string]any{"enabled": false, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Delete(&provider).Error
	})
	if err == nil {
		s.catalog.invalidate(ctx, true)
	}
	return err
}

// SoftDeleteEndpoint removes an endpoint from the active catalog while
// preserving its memberships, policies, pricing, and operational history.
func (s *Store) SoftDeleteEndpoint(ctx context.Context, endpointID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var endpoint models.Endpoint
		if err := tx.First(&endpoint, endpointID).Error; err != nil {
			return err
		}
		if err := tx.Model(&endpoint).Updates(map[string]any{
			"enabled":    false,
			"updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		return tx.Delete(&endpoint).Error
	})
	if err == nil {
		s.catalog.invalidate(ctx, true)
	}
	return err
}

func (s *Store) DeleteRoutingLaneCascade(ctx context.Context, laneID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lane models.RoutingLane
		if err := tx.First(&lane, laneID).Error; err != nil {
			return err
		}
		if err := tx.Where("lane_id = ?", laneID).Delete(&models.LaneMembership{}).Error; err != nil {
			return err
		}
		if err := deleteLimitPolicyStatesForScopes(tx, models.ScopeLane, []uint{laneID}); err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeLane, laneID).Delete(&models.LimitPolicy{}).Error; err != nil {
			return err
		}
		if err := tx.Where("scope_type = ? AND scope_id = ?", models.ScopeLane, laneID).Delete(&models.ObservedLimit{}).Error; err != nil {
			return err
		}
		return tx.Delete(&lane).Error
	})
	if err == nil {
		s.catalog.invalidate(ctx, true)
	}
	return err
}

func (s *Store) FindByID(ctx context.Context, target any, id uint) error {
	return s.db.WithContext(ctx).First(target, id).Error
}

func (s *Store) EndpointIDByUUID(ctx context.Context, publicUUID string) (uint, error) {
	return s.idByUUID(ctx, &models.Endpoint{}, publicUUID)
}

func (s *Store) ScopeUUIDByID(ctx context.Context, scopeType models.ScopeType, id uint) (string, error) {
	identity := string(scopeType) + ":" + strconv.FormatUint(uint64(id), 10)
	value, err := s.catalog.load(ctx, catalogKey(ctx, "scope-uuid", identity), func() (any, error) {
		publicUUID, loadErr := s.scopeUUIDByID(ctx, scopeType, id)
		if errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return scopeUUIDCatalogEntry{Found: false}, nil
		}
		return scopeUUIDCatalogEntry{UUID: publicUUID, Found: loadErr == nil}, loadErr
	})
	if err != nil {
		return "", err
	}
	entry := value.(scopeUUIDCatalogEntry)
	if !entry.Found {
		return "", gorm.ErrRecordNotFound
	}
	return entry.UUID, nil
}

type scopeUUIDCatalogEntry struct {
	UUID  string
	Found bool
}

func (s *Store) ResolveProviderCredential(ctx context.Context, endpointID uint) (models.Provider, models.Credential, models.Endpoint, error) {
	value, err := s.catalog.load(ctx, catalogKey(ctx, "dispatch-route", strconv.FormatUint(uint64(endpointID), 10)), func() (any, error) {
		provider, credential, endpoint, loadErr := s.resolveProviderCredentialUncached(ctx, endpointID)
		if loadErr != nil {
			return nil, loadErr
		}
		return dispatchRouteCatalogEntry{Provider: provider, Credential: credential, Endpoint: endpoint}, nil
	})
	if err != nil {
		return models.Provider{}, models.Credential{}, models.Endpoint{}, err
	}
	entry := value.(dispatchRouteCatalogEntry)
	endpoints := s.catalog.overlayEndpointStates(ctx, []models.Endpoint{entry.Endpoint})
	return entry.Provider, entry.Credential, endpoints[0], nil
}

type dispatchRouteCatalogEntry struct {
	Provider   models.Provider
	Credential models.Credential
	Endpoint   models.Endpoint
}

func (s *Store) resolveProviderCredentialUncached(ctx context.Context, endpointID uint) (models.Provider, models.Credential, models.Endpoint, error) {
	var endpoint models.Endpoint
	if err := s.FindByID(ctx, &endpoint, endpointID); err != nil {
		return models.Provider{}, models.Credential{}, models.Endpoint{}, err
	}
	var provider models.Provider
	if err := s.FindByID(ctx, &provider, endpoint.ProviderID); err != nil {
		return models.Provider{}, models.Credential{}, models.Endpoint{}, err
	}
	var credential models.Credential
	if endpoint.CredentialID != 0 {
		err := s.db.WithContext(ctx).
			Select("id", "uuid", "provider_id", "provider_uuid", "name", "enabled", "created_at", "updated_at").
			First(&credential, endpoint.CredentialID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Provider{}, models.Credential{}, models.Endpoint{}, err
		}
	}
	return provider, credential, endpoint, nil
}

func (s *Store) preparePublicUUIDs(ctx context.Context, target any) error {
	switch item := target.(type) {
	case *models.Provider:
		return ensureStorePublicUUID(&item.UUID)
	case *models.Credential:
		return s.prepareCredentialUUIDs(ctx, item)
	case *models.Endpoint:
		return s.prepareEndpointUUIDs(ctx, item)
	case *models.RoutingLane:
		return ensureStorePublicUUID(&item.UUID)
	case *models.LaneMembership:
		return s.prepareLaneMembershipUUIDs(ctx, item)
	case *models.LimitPolicy:
		return s.prepareLimitPolicyUUIDs(ctx, item)
	case *models.ObservedLimit:
		return s.prepareObservedLimitUUIDs(ctx, item)
	case *models.PricingPolicy:
		return s.preparePricingPolicyUUIDs(ctx, item)
	case *models.Guardrail:
		return s.prepareGuardrailUUIDs(ctx, item)
	case *models.GuardrailCredential:
		return ensureStorePublicUUID(&item.UUID)
	case *models.GuardrailBinding:
		return s.prepareGuardrailBindingUUIDs(ctx, item)
	case *models.RequestLog:
		return s.prepareRequestLogUUIDs(ctx, item)
	default:
		return nil
	}
}

func ensureStorePublicUUID(value *string) error {
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		*value = uuid.NewString()
		return nil
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return err
	}
	*value = parsed.String()
	return nil
}

func (s *Store) PopulatePublicUUIDs(ctx context.Context, target any) error {
	return s.preparePublicUUIDs(ctx, target)
}

func (s *Store) prepareCredentialUUIDs(ctx context.Context, item *models.Credential) error {
	if item.ProviderID == 0 && item.ProviderUUID != "" {
		id, err := s.idByUUID(ctx, &models.Provider{}, item.ProviderUUID)
		if err != nil {
			return err
		}
		item.ProviderID = id
	}
	if item.ProviderID != 0 && strings.TrimSpace(item.ProviderUUID) == "" {
		if publicUUID, err := s.uuidByID(ctx, &models.Provider{}, item.ProviderID); err == nil {
			item.ProviderUUID = publicUUID
		}
	}
	return nil
}

func (s *Store) prepareEndpointUUIDs(ctx context.Context, item *models.Endpoint) error {
	if item.ProviderID == 0 && item.ProviderUUID != "" {
		id, err := s.idByUUID(ctx, &models.Provider{}, item.ProviderUUID)
		if err != nil {
			return err
		}
		item.ProviderID = id
	}
	if item.ProviderID != 0 && strings.TrimSpace(item.ProviderUUID) == "" {
		if publicUUID, err := s.uuidByID(ctx, &models.Provider{}, item.ProviderID); err == nil {
			item.ProviderUUID = publicUUID
		}
	}
	if item.CredentialID == 0 && item.CredentialUUID != "" {
		id, err := s.idByUUID(ctx, &models.Credential{}, item.CredentialUUID)
		if err != nil {
			return err
		}
		item.CredentialID = id
	}
	if item.CredentialID != 0 && strings.TrimSpace(item.CredentialUUID) == "" {
		if publicUUID, err := s.uuidByID(ctx, &models.Credential{}, item.CredentialID); err == nil {
			item.CredentialUUID = publicUUID
		}
	}
	return nil
}

func (s *Store) prepareLaneMembershipUUIDs(ctx context.Context, item *models.LaneMembership) error {
	if item.LaneID == 0 && item.LaneUUID != "" {
		id, err := s.idByUUID(ctx, &models.RoutingLane{}, item.LaneUUID)
		if err != nil {
			return err
		}
		item.LaneID = id
	}
	if item.EndpointID == 0 && item.EndpointUUID != "" {
		id, err := s.idByUUID(ctx, &models.Endpoint{}, item.EndpointUUID)
		if err != nil {
			return err
		}
		item.EndpointID = id
	}
	if item.LaneID != 0 {
		if publicUUID, err := s.uuidByID(ctx, &models.RoutingLane{}, item.LaneID); err == nil {
			item.LaneUUID = publicUUID
		}
	}
	if item.EndpointID != 0 {
		if publicUUID, err := s.uuidByID(ctx, &models.Endpoint{}, item.EndpointID); err == nil {
			item.EndpointUUID = publicUUID
		}
	}
	return nil
}

func (s *Store) prepareLimitPolicyUUIDs(ctx context.Context, item *models.LimitPolicy) error {
	return s.prepareScopeUUIDs(ctx, item.ScopeType, &item.ScopeID, &item.ScopeUUID)
}

func (s *Store) prepareObservedLimitUUIDs(ctx context.Context, item *models.ObservedLimit) error {
	return s.prepareScopeUUIDs(ctx, item.ScopeType, &item.ScopeID, &item.ScopeUUID)
}

func (s *Store) preparePricingPolicyUUIDs(ctx context.Context, item *models.PricingPolicy) error {
	if item.EndpointID == 0 && item.EndpointUUID != "" {
		id, err := s.idByUUID(ctx, &models.Endpoint{}, item.EndpointUUID)
		if err != nil {
			return err
		}
		item.EndpointID = id
	}
	if item.EndpointID != 0 {
		if publicUUID, err := s.uuidByID(ctx, &models.Endpoint{}, item.EndpointID); err == nil {
			item.EndpointUUID = publicUUID
		}
	}
	return nil
}

func (s *Store) prepareGuardrailUUIDs(ctx context.Context, item *models.Guardrail) error {
	if item.CredentialID == nil || *item.CredentialID == 0 {
		if item.CredentialUUID == nil || strings.TrimSpace(*item.CredentialUUID) == "" {
			item.CredentialID, item.CredentialUUID = nil, nil
			return nil
		}
		id, err := s.idByUUID(ctx, &models.GuardrailCredential{}, *item.CredentialUUID)
		if err != nil {
			return err
		}
		item.CredentialID = &id
	}
	if item.CredentialUUID == nil || strings.TrimSpace(*item.CredentialUUID) == "" {
		uuid, err := s.uuidByID(ctx, &models.GuardrailCredential{}, *item.CredentialID)
		if err != nil {
			return err
		}
		item.CredentialUUID = stringPointer(uuid)
	}
	return nil
}

func (s *Store) prepareGuardrailBindingUUIDs(ctx context.Context, item *models.GuardrailBinding) error {
	if item.GuardrailID == 0 && item.GuardrailUUID != "" {
		id, err := s.idByUUID(ctx, &models.Guardrail{}, item.GuardrailUUID)
		if err != nil {
			return err
		}
		item.GuardrailID = id
	}
	if item.GuardrailID != 0 && item.GuardrailUUID == "" {
		uuid, err := s.uuidByID(ctx, &models.Guardrail{}, item.GuardrailID)
		if err != nil {
			return err
		}
		item.GuardrailUUID = uuid
	}
	targets := 0
	if item.RoutingLaneID != nil || item.RoutingLaneUUID != nil {
		targets++
		if err := s.prepareOptionalTarget(ctx, &models.RoutingLane{}, &item.RoutingLaneID, &item.RoutingLaneUUID); err != nil {
			return err
		}
	}
	if item.ProviderID != nil || item.ProviderUUID != nil {
		targets++
		if err := s.prepareOptionalTarget(ctx, &models.Provider{}, &item.ProviderID, &item.ProviderUUID); err != nil {
			return err
		}
	}
	if item.EndpointID != nil || item.EndpointUUID != nil {
		targets++
		if err := s.prepareOptionalTarget(ctx, &models.Endpoint{}, &item.EndpointID, &item.EndpointUUID); err != nil {
			return err
		}
	}
	if targets != 1 {
		return errors.New("guardrail binding must have exactly one target")
	}
	return nil
}

func (s *Store) prepareOptionalTarget(ctx context.Context, model any, id **uint, publicUUID **string) error {
	if *id == nil || **id == 0 {
		if *publicUUID == nil || strings.TrimSpace(**publicUUID) == "" {
			return errors.New("guardrail binding target is incomplete")
		}
		resolved, err := s.idByUUID(ctx, model, **publicUUID)
		if err != nil {
			return err
		}
		*id = &resolved
	}
	if *publicUUID == nil || strings.TrimSpace(**publicUUID) == "" {
		resolved, err := s.uuidByID(ctx, model, **id)
		if err != nil {
			return err
		}
		*publicUUID = stringPointer(resolved)
	}
	return nil
}

func (s *Store) prepareRequestLogUUIDs(ctx context.Context, item *models.RequestLog) error {
	if item.ProviderID == nil || *item.ProviderID == 0 {
		item.ProviderUUID = nil
	} else if item.ProviderUUID == nil || strings.TrimSpace(*item.ProviderUUID) == "" {
		if publicUUID, err := s.uuidByID(ctx, &models.Provider{}, *item.ProviderID); err == nil {
			item.ProviderUUID = stringPointer(publicUUID)
		}
	}
	if item.EndpointID == nil || *item.EndpointID == 0 {
		item.EndpointUUID = nil
	} else if item.EndpointUUID == nil || strings.TrimSpace(*item.EndpointUUID) == "" {
		if publicUUID, err := s.uuidByID(ctx, &models.Endpoint{}, *item.EndpointID); err == nil {
			item.EndpointUUID = stringPointer(publicUUID)
		}
	}
	if item.LaneID == nil || *item.LaneID == 0 {
		item.LaneUUID = nil
	} else if item.LaneUUID == nil || strings.TrimSpace(*item.LaneUUID) == "" {
		if publicUUID, err := s.uuidByID(ctx, &models.RoutingLane{}, *item.LaneID); err == nil {
			item.LaneUUID = stringPointer(publicUUID)
		}
	}
	return nil
}

func (s *Store) prepareScopeUUIDs(ctx context.Context, scopeType models.ScopeType, scopeID *uint, scopeUUID *string) error {
	if scopeType == models.ScopeGlobal {
		*scopeID = 0
		*scopeUUID = "global"
		return nil
	}
	if *scopeID == 0 && *scopeUUID != "" {
		id, err := s.scopeIDByUUID(ctx, scopeType, *scopeUUID)
		if err != nil {
			return err
		}
		*scopeID = id
	}
	if *scopeID != 0 {
		if publicUUID, err := s.scopeUUIDByID(ctx, scopeType, *scopeID); err == nil {
			*scopeUUID = publicUUID
		}
	}
	return nil
}

func (s *Store) scopeIDByUUID(ctx context.Context, scopeType models.ScopeType, publicUUID string) (uint, error) {
	switch scopeType {
	case models.ScopeProvider:
		return s.idByUUID(ctx, &models.Provider{}, publicUUID)
	case models.ScopeEndpoint:
		return s.idByUUID(ctx, &models.Endpoint{}, publicUUID)
	case models.ScopeLane:
		return s.idByUUID(ctx, &models.RoutingLane{}, publicUUID)
	default:
		return 0, fmt.Errorf("unsupported scope type %q", scopeType)
	}
}

func (s *Store) scopeUUIDByID(ctx context.Context, scopeType models.ScopeType, id uint) (string, error) {
	switch scopeType {
	case models.ScopeProvider:
		return s.uuidByID(ctx, &models.Provider{}, id)
	case models.ScopeEndpoint:
		return s.uuidByID(ctx, &models.Endpoint{}, id)
	case models.ScopeLane:
		return s.uuidByID(ctx, &models.RoutingLane{}, id)
	default:
		return "", fmt.Errorf("unsupported scope type %q", scopeType)
	}
}

func (s *Store) idByUUID(ctx context.Context, model any, publicUUID string) (uint, error) {
	parsed, err := parsePublicUUID(publicUUID)
	if err != nil {
		return 0, err
	}
	var row struct {
		ID uint
	}
	if err := s.db.WithContext(ctx).Model(model).Select("id").Where("uuid = ?", parsed).First(&row).Error; err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s *Store) uuidByID(ctx context.Context, model any, id uint) (string, error) {
	if id == 0 {
		return "", nil
	}
	var row struct {
		UUID string
	}
	if err := s.db.WithContext(ctx).Model(model).Select("uuid").Where("id = ?", id).First(&row).Error; err != nil {
		return "", err
	}
	return row.UUID, nil
}

func parsePublicUUID(value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *Store) ActiveEndpointsForLane(ctx context.Context, laneName string, routeKind models.RouteKind) ([]models.Endpoint, *models.RoutingLane, error) {
	key := catalogKey(ctx, "lane-route", strings.TrimSpace(laneName)+"|"+string(routeKind))
	value, err := s.catalog.load(ctx, key, func() (any, error) {
		endpoints, lane, loadErr := s.activeEndpointsForLaneUncached(ctx, laneName, routeKind)
		if loadErr != nil {
			return nil, loadErr
		}
		return laneRouteCatalogEntry{Endpoints: append([]models.Endpoint(nil), endpoints...), Lane: *lane}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	entry := value.(laneRouteCatalogEntry)
	lane := entry.Lane
	return s.catalog.overlayEndpointStates(ctx, entry.Endpoints), &lane, nil
}

type laneRouteCatalogEntry struct {
	Endpoints []models.Endpoint
	Lane      models.RoutingLane
}

func (s *Store) activeEndpointsForLaneUncached(ctx context.Context, laneName string, routeKind models.RouteKind) ([]models.Endpoint, *models.RoutingLane, error) {
	var lane models.RoutingLane
	laneTarget := strings.TrimSpace(laneName)
	if err := s.db.WithContext(ctx).
		Where("(lower(name) = lower(?) OR slug = ?) AND enabled = ?", laneTarget, models.SlugifyName(laneTarget), true).
		First(&lane).Error; err != nil {
		return nil, nil, err
	}
	var memberships []models.LaneMembership
	if err := s.db.WithContext(ctx).Where("lane_id = ? and enabled = ?", lane.ID, true).Order("manual_rank asc, id asc").Find(&memberships).Error; err != nil {
		return nil, nil, err
	}
	ids := make([]uint, 0, len(memberships))
	for _, membership := range memberships {
		ids = append(ids, membership.EndpointID)
	}
	if len(ids) == 0 {
		return nil, &lane, nil
	}
	var fetched []models.Endpoint
	providerJoin := "JOIN providers ON providers.id = endpoints.provider_id AND providers.enabled = ?" + s.sameTenantJoin(ctx, "endpoints", "providers")
	if err := s.db.WithContext(ctx).
		Joins(providerJoin, true).
		Where("endpoints.id IN ? and endpoints.enabled = ? and (endpoints.route_kind = ? or endpoints.route_kind = ?)", ids, true, routeKind, models.RouteKindMulti).
		Find(&fetched).Error; err != nil {
		return nil, nil, err
	}
	endpointMap := make(map[uint]models.Endpoint, len(fetched))
	for _, endpoint := range fetched {
		endpointMap[endpoint.ID] = endpoint
	}
	endpoints := make([]models.Endpoint, 0, len(memberships))
	for _, membership := range memberships {
		endpoint, ok := endpointMap[membership.EndpointID]
		if !ok {
			continue
		}
		// Lane membership order is the routing order. Endpoint-level rank should not override it.
		endpoint.ManualRank = membership.ManualRank
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, &lane, nil
}

func (s *Store) ActiveEndpointsForModel(ctx context.Context, upstreamModel string, routeKind models.RouteKind) ([]models.Endpoint, error) {
	identity := strings.TrimSpace(upstreamModel) + "|" + string(routeKind)
	value, err := s.catalog.load(ctx, catalogKey(ctx, "model-route", identity), func() (any, error) {
		endpoints, loadErr := s.activeEndpointsForModelUncached(ctx, upstreamModel, routeKind)
		return append([]models.Endpoint(nil), endpoints...), loadErr
	})
	if err != nil {
		return nil, err
	}
	return s.catalog.overlayEndpointStates(ctx, value.([]models.Endpoint)), nil
}

func (s *Store) activeEndpointsForModelUncached(ctx context.Context, upstreamModel string, routeKind models.RouteKind) ([]models.Endpoint, error) {
	var endpoints []models.Endpoint
	providerJoin := "JOIN providers ON providers.id = endpoints.provider_id AND providers.enabled = ?" + s.sameTenantJoin(ctx, "endpoints", "providers")
	upstreamModel = strings.TrimSpace(upstreamModel)
	err := s.db.WithContext(ctx).
		Joins(providerJoin, true).
		Where("(endpoints.upstream_model = ? OR lower(endpoints.name) = lower(?) OR endpoints.slug = ?) AND endpoints.enabled = ? AND (endpoints.route_kind = ? OR endpoints.route_kind = ?)",
			upstreamModel, upstreamModel, models.SlugifyName(upstreamModel), true, routeKind, models.RouteKindMulti).
		Order("endpoints.manual_rank asc, endpoints.id asc").
		Find(&endpoints).Error
	if err != nil || len(endpoints) > 0 {
		return endpoints, err
	}

	providerTarget, modelTarget, qualified := splitQualifiedModelTarget(upstreamModel)
	if !qualified {
		return endpoints, nil
	}
	if err := s.db.WithContext(ctx).
		Joins(providerJoin, true).
		Where("endpoints.enabled = ? and (endpoints.route_kind = ? or endpoints.route_kind = ?)", true, routeKind, models.RouteKindMulti).
		Where("(lower(providers.name) = lower(?) OR providers.slug = ?)", providerTarget, models.SlugifyName(providerTarget)).
		Where("(lower(endpoints.name) = lower(?) OR endpoints.slug = ? OR endpoints.upstream_model = ?)", modelTarget, models.SlugifyName(modelTarget), modelTarget).
		Order("endpoints.manual_rank asc, endpoints.id asc").
		Find(&endpoints).Error; err != nil {
		return nil, err
	}
	return endpoints, nil
}

func splitQualifiedModelTarget(value string) (provider string, model string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(value), "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	provider = strings.TrimSpace(parts[0])
	model = strings.TrimSpace(parts[1])
	return provider, model, provider != "" && model != ""
}

func (s *Store) GetPricingByEndpoint(ctx context.Context, endpointID uint) (models.PricingPolicy, error) {
	value, err := s.catalog.load(ctx, catalogKey(ctx, "pricing", strconv.FormatUint(uint64(endpointID), 10)), func() (any, error) {
		var pricing models.PricingPolicy
		loadErr := s.db.WithContext(ctx).Where("endpoint_id = ?", endpointID).First(&pricing).Error
		if errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return pricingCatalogEntry{Found: false}, nil
		}
		return pricingCatalogEntry{Pricing: pricing, Found: loadErr == nil}, loadErr
	})
	if err != nil {
		return models.PricingPolicy{}, err
	}
	entry := value.(pricingCatalogEntry)
	if !entry.Found {
		return models.PricingPolicy{}, gorm.ErrRecordNotFound
	}
	return entry.Pricing, nil
}

type pricingCatalogEntry struct {
	Pricing models.PricingPolicy
	Found   bool
}

func (s *Store) FindLimits(ctx context.Context, scopeType models.ScopeType, scopeID uint) ([]models.LimitPolicy, []models.ObservedLimit, error) {
	var configured []models.LimitPolicy
	var observed []models.ObservedLimit
	if err := s.db.WithContext(ctx).Where("scope_type = ? and scope_id = ? and enabled = ?", scopeType, scopeID, true).Find(&configured).Error; err != nil {
		return nil, nil, err
	}
	if err := s.db.WithContext(ctx).Where("scope_type = ? and scope_id = ? and enabled = ?", scopeType, scopeID, true).Find(&observed).Error; err != nil {
		return nil, nil, err
	}
	return configured, observed, nil
}

// FindLimitsForScopes resolves all configured and observed limits for one
// candidate in two bounded reads. Callers carry the returned policy identity
// through admission and completion instead of re-querying each scope.
func (s *Store) FindLimitsForScopes(ctx context.Context, scopes []LimitPolicyScope) ([]models.LimitPolicy, []models.ObservedLimit, error) {
	if len(scopes) == 0 {
		return nil, nil, nil
	}
	value, err := s.catalog.load(ctx, catalogKey(ctx, "limits", limitScopesCacheIdentity(scopes)), func() (any, error) {
		configured, observed, loadErr := s.findLimitsForScopesUncached(ctx, scopes)
		if loadErr != nil {
			return nil, loadErr
		}
		return limitCatalogEntry{
			Configured: append([]models.LimitPolicy(nil), configured...),
			Observed:   append([]models.ObservedLimit(nil), observed...),
		}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	entry := value.(limitCatalogEntry)
	return append([]models.LimitPolicy(nil), entry.Configured...), append([]models.ObservedLimit(nil), entry.Observed...), nil
}

type limitCatalogEntry struct {
	Configured []models.LimitPolicy
	Observed   []models.ObservedLimit
}

func (s *Store) findLimitsForScopesUncached(ctx context.Context, scopes []LimitPolicyScope) ([]models.LimitPolicy, []models.ObservedLimit, error) {
	query := s.db.Session(&gorm.Session{NewDB: true}).
		Where("scope_type = ? AND scope_id = ?", scopes[0].ScopeType, scopes[0].ScopeID)
	for _, scope := range scopes[1:] {
		query = query.Or("scope_type = ? AND scope_id = ?", scope.ScopeType, scope.ScopeID)
	}
	var configured []models.LimitPolicy
	if err := s.db.WithContext(ctx).Where(query).Where("enabled = ?", true).Find(&configured).Error; err != nil {
		return nil, nil, err
	}
	var observed []models.ObservedLimit
	if err := s.db.WithContext(ctx).Where(query).Where("enabled = ?", true).Find(&observed).Error; err != nil {
		return nil, nil, err
	}
	return configured, observed, nil
}

func (s *Store) LearnObservedLimit(ctx context.Context, limit models.ObservedLimit) error {
	if err := s.preparePublicUUIDs(ctx, &limit); err != nil {
		return err
	}
	var existing models.ObservedLimit
	err := s.db.WithContext(ctx).Where("scope_type = ? and scope_id = ? and metric = ? and period = ? and source_header = ?",
		limit.ScopeType, limit.ScopeID, limit.Metric, limit.Period, limit.SourceHeader).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.Create(ctx, &limit)
	}
	if err != nil {
		return err
	}
	existing.ObservedValue = limit.ObservedValue
	existing.ObservedAt = limit.ObservedAt
	existing.ExpiresAt = limit.ExpiresAt
	existing.Enabled = true
	existing.ScopeUUID = limit.ScopeUUID
	return s.Save(ctx, &existing)
}

// UpsertLimitPolicyStates persists one operational snapshot per configured
// policy in a single statement. The state is a rebuildable cache backed by
// request_logs; it is not a historical usage ledger.
func (s *Store) UpsertLimitPolicyStates(ctx context.Context, states []models.LimitPolicyState) error {
	if s == nil || s.db == nil || len(states) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range states {
		if states[i].PolicyID == 0 || states[i].PolicyUUID == "" {
			return errors.New("limit policy state requires policy identity")
		}
		states[i].Policy = models.LimitPolicy{}
		if states[i].StateVersion <= 0 {
			states[i].StateVersion = 1
		}
		if states[i].CreatedAt.IsZero() {
			states[i].CreatedAt = now
		}
		states[i].UpdatedAt = now
	}
	conflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "policy_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"policy_uuid":      gorm.Expr("excluded.policy_uuid"),
			"window_start":     gorm.Expr("excluded.window_start"),
			"window_end":       gorm.Expr("excluded.window_end"),
			"used_value":       gorm.Expr("excluded.used_value"),
			"reserved_value":   gorm.Expr("excluded.reserved_value"),
			"next_eligible_at": gorm.Expr("excluded.next_eligible_at"),
			"policy_version":   gorm.Expr("excluded.policy_version"),
			"state_version":    gorm.Expr("limit_policy_states.state_version + 1"),
			"updated_at":       gorm.Expr("excluded.updated_at"),
		}),
	}
	if scope, ok := s.tenantScopeForTable(ctx, "limit_policy_states"); ok {
		rows := make([]tenantLimitPolicyStateRow, 0, len(states))
		for _, state := range states {
			rows = append(rows, tenantLimitPolicyStateRow{LimitPolicyState: state, OrganizationUUID: scope.OrganizationUUID})
		}
		return s.db.WithContext(ctx).Omit("Policy").Clauses(conflict).Create(&rows).Error
	}
	return s.db.WithContext(ctx).Omit("Policy").Clauses(conflict).Create(&states).Error
}

func (s *Store) ListLimitPolicyStates(ctx context.Context, policyIDs []uint) ([]models.LimitPolicyState, error) {
	if s == nil || s.db == nil || len(policyIDs) == 0 {
		return nil, nil
	}
	var states []models.LimitPolicyState
	if err := s.db.WithContext(ctx).Where("policy_id IN ?", policyIDs).Find(&states).Error; err != nil {
		return nil, err
	}
	return states, nil
}

// DeleteLimitPolicyState removes derived operational state when a policy is
// disabled or deleted. Historical usage remains in request_logs and can be
// rebuilt if the policy is enabled again.
func (s *Store) DeleteLimitPolicyState(ctx context.Context, policyID uint) error {
	if s == nil || s.db == nil || policyID == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("policy_id = ?", policyID).Delete(&models.LimitPolicyState{}).Error
}

// ListLimitPolicyStateSegments loads only bounded operational checkpoints.
// Request history is deliberately not part of the normal hydration path.
func (s *Store) ListLimitPolicyStateSegments(ctx context.Context, policyIDs []uint) ([]models.LimitPolicyStateSegment, error) {
	if s == nil || s.db == nil || len(policyIDs) == 0 {
		return nil, nil
	}
	var rows []models.LimitPolicyStateSegment
	err := s.db.WithContext(ctx).Where("policy_id IN ?", policyIDs).Find(&rows).Error
	return rows, err
}

// LatestUsageEventAt returns the latest canonical event time which can
// contribute to a configured rolling limit. Checkpoint hydration uses this
// inexpensive watermark to detect a valid-but-stale release segment without
// replaying request history during an ordinary restart.
func (s *Store) LatestUsageEventAt(ctx context.Context) (time.Time, error) {
	if s == nil || s.db == nil {
		return time.Time{}, nil
	}
	var raw any
	err := s.db.WithContext(ctx).
		Model(&models.RequestLog{}).
		Select("MAX(COALESCE(finished_at, started_at, queued_at, created_at))").
		Where(
			"task_state = ? OR (task_state IN ? AND (actual_total_tokens > 0 OR actual_input_tokens > 0 OR actual_output_tokens > 0 OR actual_cost_micros > 0))",
			"completed",
			[]string{"failed", "cancelled"},
		).
		Row().
		Scan(&raw)
	if err != nil || raw == nil {
		return time.Time{}, err
	}
	switch value := raw.(type) {
	case time.Time:
		return value.UTC(), nil
	case string:
		return parseDatabaseTime(value)
	case []byte:
		return parseDatabaseTime(string(value))
	default:
		return time.Time{}, fmt.Errorf("unsupported request-log watermark type %T", raw)
	}
}

func parseDatabaseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
	} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid request-log watermark timestamp")
}

func (s *Store) ListObservedLimitStateSegments(ctx context.Context, observedIDs []uint) ([]models.ObservedLimitStateSegment, error) {
	if s == nil || s.db == nil || len(observedIDs) == 0 {
		return nil, nil
	}
	var rows []models.ObservedLimitStateSegment
	err := s.db.WithContext(ctx).Where("observed_limit_id IN ?", observedIDs).Find(&rows).Error
	return rows, err
}

// UpsertLimitStateBundle writes the public operational snapshot and its
// bounded release schedule atomically. A failed transaction leaves both at
// their prior version.
func (s *Store) UpsertLimitStateBundle(ctx context.Context, states []models.LimitPolicyState, policySegments []models.LimitPolicyStateSegment, observedSegments []models.ObservedLimitStateSegment) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txStore := &Store{db: tx, catalog: s.catalog}
		if err := txStore.UpsertLimitPolicyStates(ctx, states); err != nil {
			return err
		}
		if err := txStore.upsertLimitPolicyStateSegments(ctx, policySegments); err != nil {
			return err
		}
		return txStore.upsertObservedLimitStateSegments(ctx, observedSegments)
	})
}

func (s *Store) upsertLimitPolicyStateSegments(ctx context.Context, segments []models.LimitPolicyStateSegment) error {
	if len(segments) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range segments {
		segments[i].Policy = models.LimitPolicy{}
		segments[i].UpdatedAt = now
		if segments[i].CreatedAt.IsZero() {
			segments[i].CreatedAt = now
		}
	}
	conflict := clause.OnConflict{Columns: []clause.Column{{Name: "policy_id"}}, DoUpdates: clause.AssignmentColumns([]string{
		"policy_uuid", "format_version", "policy_version", "state_version", "window_period", "segment_start_ms", "segment_end_ms", "next_expiry_ms", "payload", "checksum", "updated_at",
	})}
	if scope, ok := s.tenantScopeForTable(ctx, "limit_policy_state_segments"); ok {
		rows := make([]tenantLimitPolicyStateSegmentRow, 0, len(segments))
		for _, segment := range segments {
			rows = append(rows, tenantLimitPolicyStateSegmentRow{LimitPolicyStateSegment: segment, OrganizationUUID: scope.OrganizationUUID})
		}
		return s.db.WithContext(ctx).Omit("Policy").Clauses(conflict).Create(&rows).Error
	}
	return s.db.WithContext(ctx).Omit("Policy").Clauses(conflict).Create(&segments).Error
}

func (s *Store) upsertObservedLimitStateSegments(ctx context.Context, segments []models.ObservedLimitStateSegment) error {
	if len(segments) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range segments {
		segments[i].ObservedLimit = models.ObservedLimit{}
		segments[i].UpdatedAt = now
		if segments[i].CreatedAt.IsZero() {
			segments[i].CreatedAt = now
		}
	}
	conflict := clause.OnConflict{Columns: []clause.Column{{Name: "observed_limit_id"}}, DoUpdates: clause.AssignmentColumns([]string{
		"observed_limit_uuid", "format_version", "policy_version", "state_version", "window_period", "segment_start_ms", "segment_end_ms", "next_expiry_ms", "payload", "checksum", "updated_at",
	})}
	if scope, ok := s.tenantScopeForTable(ctx, "observed_limit_state_segments"); ok {
		rows := make([]tenantObservedLimitStateSegmentRow, 0, len(segments))
		for _, segment := range segments {
			rows = append(rows, tenantObservedLimitStateSegmentRow{ObservedLimitStateSegment: segment, OrganizationUUID: scope.OrganizationUUID})
		}
		return s.db.WithContext(ctx).Omit("ObservedLimit").Clauses(conflict).Create(&rows).Error
	}
	return s.db.WithContext(ctx).Omit("ObservedLimit").Clauses(conflict).Create(&segments).Error
}

func (s *Store) DeleteLimitPolicyStateBundle(ctx context.Context, policyID uint) error {
	if s == nil || s.db == nil || policyID == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("policy_id = ?", policyID).Delete(&models.LimitPolicyStateSegment{}).Error; err != nil {
			return err
		}
		return tx.Where("policy_id = ?", policyID).Delete(&models.LimitPolicyState{}).Error
	})
}

func (s *Store) UpdateRequestLog(ctx context.Context, requestID string, mutate func(*models.RequestLog) error) error {
	var log models.RequestLog
	query := s.db.WithContext(ctx).Where("request_id = ?", requestID)
	if scope, ok := tenancy.ScopeFromContext(ctx); ok && scope.UserUUID != "" && s.db.Migrator().HasColumn("request_logs", "user_uuid") {
		query = query.Where("user_uuid = ?", scope.UserUUID)
	}
	err := query.First(&log).Error
	isNew := errors.Is(err, gorm.ErrRecordNotFound)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.RequestID = requestID
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := mutate(&log); err != nil {
		return err
	}
	diagnostic := detachRequestLogDiagnostics(&log)
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}
	log.UpdatedAt = time.Now().UTC()
	if err := s.preparePublicUUIDs(ctx, &log); err != nil {
		return err
	}
	if isNew {
		if diagnostic != nil {
			log.CandidateTraceJSON = diagnostic.CandidateTraceJSON
			log.LimitImpactJSON = diagnostic.LimitImpactJSON
			log.AppliedOverridesJSON = diagnostic.AppliedOverridesJSON
		}
		return s.Create(ctx, &log)
	}
	if err := s.Save(ctx, &log); err != nil {
		return err
	}
	return s.persistRequestLogDiagnostics(ctx, &log, diagnostic)
}

// UpdateRequestLogCapturedBodies patches only the optional captured payload
// columns. It deliberately avoids the read/modify/Save path because payload
// capture runs after terminal finalization and a stale whole-row save could
// erase newer candidate identity and limit-attribution fields.
func (s *Store) UpdateRequestLogCapturedBodies(ctx context.Context, requestID, requestBodyJSON, upstreamRequestJSON, responseBodyJSON string) error {
	if s == nil || s.db == nil {
		return errors.New("request log store is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("request_id is required")
	}
	query := s.db.WithContext(ctx).Model(&models.RequestLog{}).Where("request_id = ?", requestID)
	if scope, ok := tenancy.ScopeFromContext(ctx); ok && scope.UserUUID != "" && s.db.Migrator().HasColumn("request_logs", "user_uuid") {
		query = query.Where("user_uuid = ?", scope.UserUUID)
	}
	result := query.Updates(map[string]any{
		"request_body_json":     requestBodyJSON,
		"upstream_request_json": upstreamRequestJSON,
		"response_body_json":    responseBodyJSON,
		"updated_at":            time.Now().UTC(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) Summary(ctx context.Context) (map[string]any, error) {
	count := func(model any) (int64, error) {
		var total int64
		return total, s.db.WithContext(ctx).Model(model).Count(&total).Error
	}
	countRequestState := func(state string) (int64, error) {
		var total int64
		return total, s.db.WithContext(ctx).Model(&models.RequestLog{}).Where("task_state = ?", state).Count(&total).Error
	}
	providers, err := count(&models.Provider{})
	if err != nil {
		return nil, err
	}
	endpoints, err := count(&models.Endpoint{})
	if err != nil {
		return nil, err
	}
	lanes, err := count(&models.RoutingLane{})
	if err != nil {
		return nil, err
	}
	logs, err := count(&models.RequestLog{})
	if err != nil {
		return nil, err
	}
	completed, err := countRequestState("completed")
	if err != nil {
		return nil, err
	}
	failed, err := countRequestState("failed")
	if err != nil {
		return nil, err
	}
	cancelled, err := countRequestState("cancelled")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"providers": providers,
		"endpoints": endpoints,
		"lanes":     lanes,
		"requests":  logs,
		"request_states": map[string]int64{
			"completed": completed,
			"failed":    failed,
			"cancelled": cancelled,
		},
	}, nil
}

func (s *Store) RecomputeSuggestedRank(ctx context.Context, endpointID uint) (models.Endpoint, error) {
	var endpoint models.Endpoint
	if err := s.FindByID(ctx, &endpoint, endpointID); err != nil {
		return models.Endpoint{}, err
	}
	score := endpoint.QualityScore*1000000 + endpoint.ContextWindow*100 - endpoint.ParamSizeB
	endpoint.SuggestedScore = score
	endpoint.SuggestedRank = 1
	if err := s.Save(ctx, &endpoint); err != nil {
		return models.Endpoint{}, err
	}
	return endpoint, nil
}

func (s *Store) RecomputeAllSuggestions(ctx context.Context) error {
	items, err := s.ListEndpoints(ctx)
	if err != nil {
		return err
	}
	for i := range items {
		score := items[i].QualityScore*1000000 + items[i].ContextWindow*100 - items[i].ParamSizeB
		items[i].SuggestedScore = score
	}
	for i := range items {
		rank := 1
		for j := range items {
			if items[j].SuggestedScore > items[i].SuggestedScore {
				rank++
			}
		}
		items[i].SuggestedRank = rank
		if err := s.Save(ctx, &items[i]); err != nil {
			return fmt.Errorf("save suggestion for endpoint %d: %w", items[i].ID, err)
		}
	}
	return nil
}
