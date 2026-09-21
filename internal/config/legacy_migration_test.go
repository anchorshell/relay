package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyEnvFileMigrationPreservesValuesAndFormatting(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# keep this comment\r\nexport BOUNCER_ADMIN_TOKEN = 'existing-long-management-secret' # keep inline\r\nBOUNCER_API_TOKEN=\r\nBOUNCER_MASTER_KEY=\"" + validMasterKey() + "\"\r\nBOUNCER_HTTP_ADDR=:9000\r\nOTHER='value#literal'\r\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyEnvFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.ReplaceAll(body, "BOUNCER_", "RELAY_") {
		t.Fatal("migration changed values or formatting")
	}
	file, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.values()["RELAY_ADMIN_TOKEN"] != "existing-long-management-secret" {
		t.Fatal("quoted credential did not retain its value")
	}
	if value, ok := file.values()["RELAY_API_TOKEN"]; !ok || value != "" {
		t.Fatal("explicit authentication opt-out was lost")
	}
	if err := MigrateLegacyEnvFile(path); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(got, again) {
		t.Fatal("migration is not idempotent")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("unsafe configuration permissions")
	}
}

func TestLegacyEnvConflictUsesValidatedNewValue(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"new wins", "BOUNCER_API_TOKEN=old-secret\nRELAY_API_TOKEN=new-secret\n", false},
		{"intentional empty", "BOUNCER_API_TOKEN=old-secret\nRELAY_API_TOKEN=\n", false},
		{"invalid admin", "BOUNCER_ADMIN_TOKEN=existing-long-management-secret\nRELAY_ADMIN_TOKEN=short\n", true},
		{"invalid master", "BOUNCER_MASTER_KEY=old-key\nRELAY_MASTER_KEY=invalid\n", true},
		{"invalid API", "BOUNCER_API_TOKEN=old-secret\nRELAY_API_TOKEN='invalid value'\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := MigrateLegacyEnvFile(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected migration outcome: %v", err)
			}
			got, _ := os.ReadFile(path)
			if tc.wantError && string(got) != tc.body {
				t.Fatal("failed migration changed the file")
			}
			if !tc.wantError && strings.Contains(string(got), "BOUNCER_") {
				t.Fatal("legacy key retained")
			}
		})
	}
}

func TestLegacyProcessEnvironmentRejectedWithoutSecrets(t *testing.T) {
	for _, key := range []string{"BOUNCER_API_TOKEN", "BOUNCER_ADMIN_TOKEN", "BOUNCER_MASTER_KEY", "BOUNCER_HTTP_ADDR"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "must-never-appear-in-errors")
			err := MigrateLegacyEnvFile(filepath.Join(t.TempDir(), ".env"))
			if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "must-never-appear-in-errors") {
				t.Fatal("legacy process setting was accepted or error exposed a value")
			}
		})
	}
}

func TestLegacyEnvMigrationRefusesSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	target := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(target, []byte("BOUNCER_API_TOKEN=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyEnvFile(path); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestLegacyEnvBootstrapPreservesCredentialsWithoutPrinting(t *testing.T) {
	clearRelayEnv(t)
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("RELAY_TEMP_DIR", t.TempDir())
	body := "BOUNCER_ADMIN_TOKEN=" + validAdminToken() + "\nBOUNCER_API_TOKEN=existing-inference-test-token\nBOUNCER_MASTER_KEY=" + validMasterKey() + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, report, err := BootstrapSecrets(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminToken != validAdminToken() || cfg.APIToken != "existing-inference-test-token" || cfg.MasterKey != validMasterKey() {
		t.Fatal("credential values changed")
	}
	var output bytes.Buffer
	PrintBootstrapBanner(&output, report)
	if output.Len() != 0 {
		t.Fatal("migration printed credential material")
	}
	_, second, err := BootstrapSecrets(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	PrintBootstrapBanner(&output, second)
	if output.Len() != 0 {
		t.Fatal("repeat startup regenerated credentials")
	}
}

func TestLegacyGeneratedDefaultsRenameWithoutOpeningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "BOUNCER_DB_PATH=./bouncer.db\nBOUNCER_ADMIN_COOKIE_NAME=bouncer_admin_token\nBOUNCER_PRO_DB_MAX_OPEN_CONNS=12\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyEnvFile(path); err != nil {
		t.Fatal(err)
	}
	file, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := file.values()
	if values["RELAY_DB_PATH"] != "./relay.db" || values["RELAY_ADMIN_COOKIE_NAME"] != "relay_admin_token" || values["RELAY_PRO_DB_MAX_OPEN_CONNS"] != "12" {
		t.Fatal("generated defaults were not renamed")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "relay.db")); !os.IsNotExist(err) {
		t.Fatal("configuration preparation touched a database")
	}
}
