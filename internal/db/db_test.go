package db

import (
	"bytes"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestDatabaseLoggerSuppressesExpectedMissesButKeepsRealErrors(t *testing.T) {
	var output bytes.Buffer
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "logging.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.RoutingLane{}); err != nil {
		t.Fatal(err)
	}
	database = database.Session(&gorm.Session{
		Logger: newDatabaseLogger(log.New(&output, "", 0)),
	})

	var lane models.RoutingLane
	err = database.Where("name = ?", "expected-miss").First(&lane).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing row error = %v, want record not found", err)
	}
	if strings.Contains(strings.ToLower(output.String()), "record not found") {
		t.Fatalf("expected record-not-found error to be suppressed, got %q", output.String())
	}
	output.Reset()

	err = database.Exec("SELECT * FROM table_that_does_not_exist").Error
	if err == nil {
		t.Fatal("expected invalid SQL to fail")
	}
	if !strings.Contains(output.String(), "no such table") {
		t.Fatalf("real database error was not logged: %q", output.String())
	}
}

func TestOpenEnforcesStandaloneConfigurationUniqueness(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}

	assertSecondCreateFails(t, database,
		&models.Provider{Name: "Provider A", Slug: "shared"},
		&models.Provider{Name: "Provider B", Slug: "shared"},
	)
	assertSecondCreateFails(t, database,
		&models.RoutingLane{Name: "shared"},
		&models.RoutingLane{Name: "shared"},
	)
	assertSecondCreateFails(t, database,
		&models.PricingPolicy{EndpointID: 42},
		&models.PricingPolicy{EndpointID: 42},
	)
}

func TestSharedMigrationLeavesUniquenessToCustomDatabaseOwner(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "custom.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	if err := database.Create(&models.RoutingLane{Name: "organization-local"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.RoutingLane{Name: "organization-local"}).Error; err != nil {
		t.Fatalf("shared migration unexpectedly enforced global lane-name uniqueness: %v", err)
	}
	if err := Migrate(database); err != nil {
		t.Fatalf("repeat shared migration rejected downstream-scoped duplicates: %v", err)
	}
}

func TestOpenRepairsExistingCaseCollidingCatalogNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	existing, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(existing); err != nil {
		t.Fatal(err)
	}
	for _, lane := range []models.RoutingLane{
		{Name: "Coding", Enabled: true},
		{Name: "codinG", Enabled: true},
	} {
		if err := existing.Create(&lane).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, err := existing.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var lanes []models.RoutingLane
	if err := reopened.Order("id ASC").Find(&lanes).Error; err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 2 || lanes[0].Slug != "coding" || lanes[1].Slug == "coding" ||
		strings.EqualFold(lanes[0].Name, lanes[1].Name) {
		t.Fatalf("case-colliding group migration result = %#v", lanes)
	}
}

func assertSecondCreateFails(t *testing.T, database *gorm.DB, first any, second any) {
	t.Helper()
	if err := database.Create(first).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(second).Error; err == nil {
		t.Fatal("expected standalone uniqueness constraint to reject duplicate")
	}
}
