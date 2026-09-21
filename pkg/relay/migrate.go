package relay

import (
	"github.com/anchorshell/relay/internal/db"
	"gorm.io/gorm"
)

// MigrateRelayDB applies the public Model Relay base schema to an
// already-opened GORM database. Product extensions that provide their own
// database opener can use this to initialize the shared tables, while owning
// any deployment-specific uniqueness constraints themselves.
func MigrateRelayDB(gormDB *gorm.DB) error {
	return db.Migrate(gormDB)
}
