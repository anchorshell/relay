package relay

import (
	"errors"
	"os"

	"github.com/anchorshell/relay/internal/config"
)

// PrepareEnvironment migrates an existing local .env before loading it. Pro
// calls this before reading its database configuration. It generates no secrets
// and never accepts legacy process-environment aliases.
func PrepareEnvironment(path string) error {
	if err := config.MigrateLegacyEnvFile(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return config.LoadDotEnv(path)
}
