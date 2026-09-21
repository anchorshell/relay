package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPITokenBootstrapAndRestart(t *testing.T) {
	for _, tt := range []struct {
		name, setting string
		newFile       bool
		wantGenerate  bool
		wantToken     string
	}{
		{name: "new install", newFile: true, wantGenerate: true},
		{name: "existing missing setting", wantGenerate: true},
		{name: "explicit empty", setting: "RELAY_API_TOKEN=\n"},
		{name: "quoted empty", setting: "RELAY_API_TOKEN=\"\"\n"},
		{name: "configured", setting: "RELAY_API_TOKEN=existing-api-token\n", wantToken: "existing-api-token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearRelayEnv(t)
			t.Setenv("RELAY_TEMP_DIR", t.TempDir())
			original := "# preserve operator notes\nRELAY_ADMIN_TOKEN=" + validAdminToken() + "\nRELAY_MASTER_KEY=" + validMasterKey() + "\n" + tt.setting
			path := filepath.Join(t.TempDir(), ".env")
			if !tt.newFile {
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := LoadFromEnvFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg, report, err := BootstrapSecrets(cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			if (report.GeneratedAPIToken != "") != tt.wantGenerate {
				t.Fatal("unexpected API-token generation state")
			}
			if tt.wantGenerate {
				raw, err := base64.RawURLEncoding.DecodeString(cfg.APIToken)
				if err != nil || len(raw) != 32 || cfg.APIToken == cfg.AdminToken || cfg.APIToken == cfg.MasterKey {
					t.Fatal("expected an independent 256-bit API token")
				}
			} else if cfg.APIToken != tt.wantToken {
				t.Fatal("configured API token changed")
			}
			body := readFile(t, path)
			persisted, err := readEnvFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if value, exists := persisted.values()["RELAY_API_TOKEN"]; !exists || value != cfg.APIToken {
				t.Fatal("API-token setting was not persisted")
			}
			if !tt.newFile && !strings.HasPrefix(body, original) {
				t.Fatal("migration changed unrelated existing configuration")
			}
			stat, err := os.Stat(path)
			if err != nil || stat.Mode().Perm() != 0o600 {
				t.Fatal("environment file is not owner-only")
			}
			var banner bytes.Buffer
			PrintBootstrapBanner(&banner, report)
			if strings.Contains(banner.String(), "RELAY_API_TOKEN=") != tt.wantGenerate {
				t.Fatal("API token must be printed only when generated")
			}
			// Simulate a fresh process, not just another call with populated env.
			for _, key := range []string{"RELAY_ADMIN_TOKEN", "RELAY_API_TOKEN", "RELAY_MASTER_KEY"} {
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := LoadFromEnvFile(path)
			if err != nil {
				t.Fatal(err)
			}
			restarted, report, err = BootstrapSecrets(restarted, false)
			if err != nil {
				t.Fatal(err)
			}
			banner.Reset()
			PrintBootstrapBanner(&banner, report)
			if restarted.APIToken != cfg.APIToken || banner.Len() != 0 || readFile(t, path) != body {
				t.Fatal("restart changed credentials or printed an existing token")
			}
		})
	}
}

func TestAPITokenProcessOverride(t *testing.T) {
	for _, token := range []string{"", "process-api-token"} {
		t.Run("token="+token, func(t *testing.T) {
			clearRelayEnv(t)
			t.Setenv("RELAY_API_TOKEN", token)
			path := writeTestEnv(t, "RELAY_ADMIN_TOKEN="+validAdminToken()+"\nRELAY_MASTER_KEY="+validMasterKey()+"\nRELAY_API_TOKEN=file-api-token\n")
			original := readFile(t, path)
			cfg, report, err := BootstrapSecrets(Config{EnvPath: path}, false)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.APIToken != token || report.GeneratedAPIToken != "" || readFile(t, path) != original {
				t.Fatal("process override must win without overwriting an existing file setting")
			}
		})
	}
}

func TestAPITokenRejectsCredentialReuseAndWhitespace(t *testing.T) {
	for _, token := range []string{validAdminToken(), validMasterKey(), " ", "token\nvalue"} {
		t.Run("invalid configured token", func(t *testing.T) {
			clearRelayEnv(t)
			t.Setenv("RELAY_API_TOKEN", token)
			path := writeTestEnv(t, "RELAY_ADMIN_TOKEN="+validAdminToken()+"\nRELAY_MASTER_KEY="+validMasterKey()+"\n")
			original := readFile(t, path)
			_, _, err := BootstrapSecrets(Config{EnvPath: path}, false)
			if err == nil || readFile(t, path) != original {
				t.Fatal("invalid credentials must fail without changing the environment file")
			}
			if token != " " && strings.Contains(err.Error(), token) {
				t.Fatal("error disclosed credential")
			}
		})
	}
}
