package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorshell/relay/internal/security"
)

func TestInvalidMasterKeyAutoGeneratesOnlyWhenNoEncryptedCredentialsExist(t *testing.T) {
	clearRelayEnv(t)
	envPath := writeTestEnv(t, "RELAY_ADMIN_TOKEN="+validAdminToken()+"\nRELAY_MASTER_KEY=change-me\n")
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := BootstrapSecrets(Config{EnvPath: envPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedMasterKey == "" {
		t.Fatal("expected generated master key")
	}
	if !security.MasterKeyValid(cfg.MasterKey) {
		t.Fatal("expected resolved master key to be valid")
	}
	body := readFile(t, envPath)
	if !strings.Contains(body, "RELAY_MASTER_KEY="+report.GeneratedMasterKey) {
		t.Fatal("expected generated master key to be written to .env")
	}
	if strings.Contains(body, "change-me") {
		t.Fatal("expected placeholder to be replaced")
	}
}

func TestInvalidMasterKeyFailsWhenEncryptedCredentialsExist(t *testing.T) {
	clearRelayEnv(t)
	original := "RELAY_ADMIN_TOKEN=" + validAdminToken() + "\nRELAY_MASTER_KEY=change-me\n"
	envPath := writeTestEnv(t, original)
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}

	_, report, err := BootstrapSecrets(Config{EnvPath: envPath}, true)
	if err == nil {
		t.Fatal("expected invalid master key to fail when encrypted credentials exist")
	}
	if report.GeneratedMasterKey != "" || report.GeneratedAdminToken != "" {
		t.Fatal("did not expect generated secrets on failed bootstrap")
	}
	if got := readFile(t, envPath); got != original {
		t.Fatalf("expected .env to remain unchanged, got %q", got)
	}
}

func TestInvalidAdminTokenAutoGenerates(t *testing.T) {
	clearRelayEnv(t)
	envPath := writeTestEnv(t, "RELAY_ADMIN_TOKEN=change-me\nRELAY_MASTER_KEY="+validMasterKey()+"\n")
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := BootstrapSecrets(Config{EnvPath: envPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAdminToken == "" {
		t.Fatal("expected generated admin token")
	}
	if !AdminTokenValid(cfg.AdminToken) {
		t.Fatal("expected resolved admin token to be valid")
	}
	if !strings.Contains(readFile(t, envPath), "RELAY_ADMIN_TOKEN="+report.GeneratedAdminToken) {
		t.Fatal("expected generated admin token to be written to .env")
	}
}

func TestMissingEnvCreatedOnFirstRun(t *testing.T) {
	clearRelayEnv(t)
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previous)
	})

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	expectedPath, err := filepath.EvalSymlinks(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	gotPath, err := filepath.EvalSymlinks(cfg.EnvPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != expectedPath {
		t.Fatalf("expected .env under temp dir, got %q", cfg.EnvPath)
	}
	if _, err := os.Stat(cfg.EnvPath); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := BootstrapSecrets(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAdminToken == "" || report.GeneratedMasterKey == "" {
		t.Fatal("expected both startup secrets to be generated")
	}
	body := readFile(t, cfg.EnvPath)
	if !strings.Contains(body, "RELAY_ADMIN_TOKEN="+cfg.AdminToken) {
		t.Fatal("expected admin token in .env")
	}
	if !strings.Contains(body, "RELAY_MASTER_KEY="+cfg.MasterKey) {
		t.Fatal("expected master key in .env")
	}
}

func TestExistingEnvUpdatedWithoutDroppingUnrelatedLines(t *testing.T) {
	clearRelayEnv(t)
	envPath := writeTestEnv(t, "# local operator notes\nFOO=bar\n\nRELAY_ADMIN_TOKEN=change-me\nRELAY_MASTER_KEY=change-me\n")
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}

	if _, _, err := BootstrapSecrets(Config{EnvPath: envPath}, false); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, envPath)
	if !strings.Contains(body, "# local operator notes") || !strings.Contains(body, "FOO=bar") {
		t.Fatalf("expected unrelated .env lines to be preserved, got %q", body)
	}
	if strings.Contains(body, "change-me") {
		t.Fatal("expected placeholders to be replaced")
	}
}

func TestValidExistingEnvValuesAreNotRegenerated(t *testing.T) {
	clearRelayEnv(t)
	original := "# keep me\nRELAY_API_TOKEN=existing-inference-token\nRELAY_ADMIN_TOKEN=" + validAdminToken() + "\nRELAY_MASTER_KEY=" + validMasterKey() + "\n"
	envPath := writeTestEnv(t, original)
	if err := LoadDotEnv(envPath); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := BootstrapSecrets(Config{EnvPath: envPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAdminToken != "" || report.GeneratedMasterKey != "" {
		t.Fatal("did not expect valid existing values to be regenerated")
	}
	if cfg.AdminToken != validAdminToken() || cfg.MasterKey != validMasterKey() {
		t.Fatal("expected valid existing values to be used")
	}
	if got := readFile(t, envPath); got != original {
		t.Fatalf("expected .env content to remain unchanged, got %q", got)
	}
}

func TestValidEnvFileValuesWinOverInvalidProcessEnv(t *testing.T) {
	clearRelayEnv(t)
	original := "RELAY_API_TOKEN=existing-inference-token\nRELAY_ADMIN_TOKEN=" + validAdminToken() + "\nRELAY_MASTER_KEY=" + validMasterKey() + "\n"
	envPath := writeTestEnv(t, original)
	if err := os.Setenv("RELAY_ADMIN_TOKEN", "short"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("RELAY_MASTER_KEY", "change-me"); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := BootstrapSecrets(Config{EnvPath: envPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAdminToken != "" || report.GeneratedMasterKey != "" {
		t.Fatal("did not expect generation when .env contains valid values")
	}
	if cfg.AdminToken != validAdminToken() || cfg.MasterKey != validMasterKey() {
		t.Fatal("expected valid .env values to be used")
	}
	if got := readFile(t, envPath); got != original {
		t.Fatalf("expected .env content to remain unchanged, got %q", got)
	}
}

func TestValidProcessEnvValuesArePersistedWhenEnvMissingKeys(t *testing.T) {
	clearRelayEnv(t)
	envPath := writeTestEnv(t, "# empty local file\n")
	if err := os.Setenv("RELAY_ADMIN_TOKEN", validAdminToken()); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("RELAY_MASTER_KEY", validMasterKey()); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := BootstrapSecrets(Config{EnvPath: envPath}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAdminToken != "" || report.GeneratedMasterKey != "" {
		t.Fatal("did not expect generation when process env values are valid")
	}
	body := readFile(t, envPath)
	if !strings.Contains(body, "RELAY_ADMIN_TOKEN="+cfg.AdminToken) {
		t.Fatal("expected process admin token to be persisted")
	}
	if !strings.Contains(body, "RELAY_MASTER_KEY="+cfg.MasterKey) {
		t.Fatal("expected process master key to be persisted")
	}
}

func writeTestEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func clearRelayEnv(t *testing.T) {
	t.Helper()
	keys := []string{"RELAY_ADMIN_TOKEN", "RELAY_API_TOKEN", "RELAY_MASTER_KEY"}
	previous := make(map[string]string, len(keys))
	present := make(map[string]bool, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		previous[key] = value
		present[key] = ok
		_ = os.Unsetenv(key)
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if present[key] {
				_ = os.Setenv(key, previous[key])
			} else {
				_ = os.Unsetenv(key)
			}
		}
	})
}

func validAdminToken() string {
	return "valid-admin-token-1234567890"
}

func validMasterKey() string {
	return "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
}
