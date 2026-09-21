package db

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/anchorshell/relay/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: newDatabaseLogger(log.New(os.Stdout, "\r\n", log.LstdFlags)),
	})
	if err != nil {
		return nil, err
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
	}
	for _, stmt := range pragmas {
		if err := db.Exec(stmt).Error; err != nil {
			return nil, fmt.Errorf("sqlite pragma failed: %w", err)
		}
	}
	if err := Migrate(db); err != nil {
		return nil, err
	}
	if err := migrateStandaloneCatalogIdentities(db); err != nil {
		return nil, fmt.Errorf("catalog identity migration failed: %w", err)
	}
	if err := createStandaloneUniqueIndexes(db); err != nil {
		return nil, fmt.Errorf("standalone unique index migration failed: %w", err)
	}
	return db, nil
}

func newDatabaseLogger(writer logger.Writer) logger.Interface {
	return logger.New(writer, logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
	})
}

func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&models.Provider{},
		&models.Credential{},
		&models.Endpoint{},
		&models.RoutingLane{},
		&models.LaneMembership{},
		&models.LimitPolicy{},
		&models.LimitPolicyState{},
		&models.LimitPolicyStateSegment{},
		&models.ObservedLimit{},
		&models.ObservedLimitStateSegment{},
		&models.PricingPolicy{},
		&models.GuardrailCredential{},
		&models.Guardrail{},
		&models.GuardrailBinding{},
		&models.RequestLog{},
		&models.RequestLogDiagnostics{},
		&models.AppSetting{},
	); err != nil {
		return err
	}
	if err := db.Model(&models.Endpoint{}).Where("pacing IS NULL").Update("pacing", true).Error; err != nil {
		return fmt.Errorf("endpoint pacing migration failed: %w", err)
	}
	if err := createPublicUUIDIndexes(db); err != nil {
		return fmt.Errorf("public uuid index migration failed: %w", err)
	}
	if db.Dialector.Name() == "sqlite" {
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_request_logs_primary_action ON request_logs(primary_action);").Error; err != nil {
			return fmt.Errorf("request characterization index migration failed: %w", err)
		}
	}
	return nil
}

func createPublicUUIDIndexes(db *gorm.DB) error {
	statements := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_providers_public_uuid ON providers(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_public_uuid ON credentials(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_public_uuid ON endpoints(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_lanes_public_uuid ON routing_lanes(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_lane_memberships_public_uuid ON lane_memberships(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_limit_policies_public_uuid ON limit_policies(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_observed_limits_public_uuid ON observed_limits(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_pricing_policies_public_uuid ON pricing_policies(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrails_public_uuid ON guardrails(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrail_credentials_public_uuid ON guardrail_credentials(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrail_bindings_public_uuid ON guardrail_bindings(uuid) WHERE uuid <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_public_uuid ON request_logs(uuid) WHERE uuid <> '';",
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// Standalone Relay is single-tenant, so these configuration keys are global.
// Custom database openers (including Pro) own their own scoped uniqueness and
// intentionally do not run this SQLite-only startup step.
func createStandaloneUniqueIndexes(db *gorm.DB) error {
	statements := []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_providers_slug ON providers(slug);",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_providers_name_ci ON providers(lower(name)) WHERE name <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_lanes_slug ON routing_lanes(slug);",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_lanes_name_ci ON routing_lanes(lower(name)) WHERE name <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_provider_slug ON endpoints(provider_id, slug) WHERE slug <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_provider_name_ci ON endpoints(provider_id, lower(name)) WHERE name <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_pricing_policies_endpoint_id ON pricing_policies(endpoint_id);",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrails_slug ON guardrails(slug) WHERE slug <> '';",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrail_bindings_guardrail_lane ON guardrail_bindings(guardrail_id, routing_lane_id) WHERE routing_lane_id IS NOT NULL;",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrail_bindings_guardrail_provider ON guardrail_bindings(guardrail_id, provider_id) WHERE provider_id IS NOT NULL;",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_guardrail_bindings_guardrail_endpoint ON guardrail_bindings(guardrail_id, endpoint_id) WHERE endpoint_id IS NOT NULL;",
		"DROP TRIGGER IF EXISTS guardrail_bindings_one_target_insert;",
		"DROP TRIGGER IF EXISTS guardrail_bindings_one_target_update;",
		"CREATE TRIGGER guardrail_bindings_one_target_insert BEFORE INSERT ON guardrail_bindings WHEN NOT ((NEW.routing_lane_id IS NOT NULL AND NEW.routing_lane_uuid IS NOT NULL AND NEW.provider_id IS NULL AND NEW.provider_uuid IS NULL AND NEW.endpoint_id IS NULL AND NEW.endpoint_uuid IS NULL) OR (NEW.provider_id IS NOT NULL AND NEW.provider_uuid IS NOT NULL AND NEW.routing_lane_id IS NULL AND NEW.routing_lane_uuid IS NULL AND NEW.endpoint_id IS NULL AND NEW.endpoint_uuid IS NULL) OR (NEW.endpoint_id IS NOT NULL AND NEW.endpoint_uuid IS NOT NULL AND NEW.routing_lane_id IS NULL AND NEW.routing_lane_uuid IS NULL AND NEW.provider_id IS NULL AND NEW.provider_uuid IS NULL)) BEGIN SELECT RAISE(ABORT, 'guardrail binding must have exactly one target'); END;",
		"CREATE TRIGGER guardrail_bindings_one_target_update BEFORE UPDATE ON guardrail_bindings WHEN NOT ((NEW.routing_lane_id IS NOT NULL AND NEW.routing_lane_uuid IS NOT NULL AND NEW.provider_id IS NULL AND NEW.provider_uuid IS NULL AND NEW.endpoint_id IS NULL AND NEW.endpoint_uuid IS NULL) OR (NEW.provider_id IS NOT NULL AND NEW.provider_uuid IS NOT NULL AND NEW.routing_lane_id IS NULL AND NEW.routing_lane_uuid IS NULL AND NEW.endpoint_id IS NULL AND NEW.endpoint_uuid IS NULL) OR (NEW.endpoint_id IS NOT NULL AND NEW.endpoint_uuid IS NOT NULL AND NEW.routing_lane_id IS NULL AND NEW.routing_lane_uuid IS NULL AND NEW.provider_id IS NULL AND NEW.provider_uuid IS NULL)) BEGIN SELECT RAISE(ABORT, 'guardrail binding must have exactly one target'); END;",
	}
	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateStandaloneCatalogIdentities(db *gorm.DB) error {
	for _, statement := range []string{
		"DROP INDEX IF EXISTS idx_providers_slug",
		"DROP INDEX IF EXISTS idx_providers_name_ci",
		"DROP INDEX IF EXISTS idx_routing_lanes_name",
		"DROP INDEX IF EXISTS idx_routing_lanes_slug",
		"DROP INDEX IF EXISTS idx_routing_lanes_name_ci",
		"DROP INDEX IF EXISTS idx_endpoints_provider_slug",
		"DROP INDEX IF EXISTS idx_endpoints_provider_name_ci",
	} {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}

	var providers []models.Provider
	if err := db.Order("id ASC").Find(&providers).Error; err != nil {
		return err
	}
	providerSlugs := map[string]struct{}{}
	for _, provider := range providers {
		name, slug := uniqueCatalogIdentity(provider.Name, "provider", providerSlugs)
		if err := db.Model(&models.Provider{}).Where("id = ?", provider.ID).Updates(map[string]any{"name": name, "slug": slug}).Error; err != nil {
			return err
		}
	}

	var lanes []models.RoutingLane
	if err := db.Order("id ASC").Find(&lanes).Error; err != nil {
		return err
	}
	laneSlugs := map[string]struct{}{}
	for _, lane := range lanes {
		name, slug := uniqueCatalogIdentity(lane.Name, "group", laneSlugs)
		if err := db.Model(&models.RoutingLane{}).Where("id = ?", lane.ID).Updates(map[string]any{"name": name, "slug": slug}).Error; err != nil {
			return err
		}
	}

	var endpoints []models.Endpoint
	if err := db.Order("provider_id ASC, id ASC").Find(&endpoints).Error; err != nil {
		return err
	}
	endpointSlugsByProvider := map[uint]map[string]struct{}{}
	for _, endpoint := range endpoints {
		used := endpointSlugsByProvider[endpoint.ProviderID]
		if used == nil {
			used = map[string]struct{}{}
			endpointSlugsByProvider[endpoint.ProviderID] = used
		}
		fallback := endpoint.UpstreamModel
		if fallback == "" {
			fallback = "model"
		}
		name, slug := uniqueCatalogIdentity(endpoint.Name, fallback, used)
		if err := db.Model(&models.Endpoint{}).Where("id = ?", endpoint.ID).Updates(map[string]any{"name": name, "slug": slug}).Error; err != nil {
			return err
		}
	}
	return nil
}

func uniqueCatalogIdentity(name, fallback string, used map[string]struct{}) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(fallback)
	}
	if name == "" {
		name = "item"
	}
	baseName := name
	slug := models.SlugifyName(baseName)
	if slug == "" {
		baseName = strings.TrimSpace(fallback)
		slug = models.SlugifyName(baseName)
	}
	if slug == "" {
		baseName = "item"
		slug = "item"
	}
	if _, exists := used[slug]; !exists {
		used[slug] = struct{}{}
		return baseName, slug
	}
	for suffix := 2; ; suffix++ {
		candidateName := fmt.Sprintf("%s copy %d", baseName, suffix)
		candidateSlug := models.SlugifyName(candidateName)
		if _, exists := used[candidateSlug]; exists {
			continue
		}
		used[candidateSlug] = struct{}{}
		return candidateName, candidateSlug
	}
}
