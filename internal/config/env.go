package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anchorshell/relay/internal/security"
)

const minAdminTokenLength = 24

type BootstrapReport struct {
	EnvPath             string
	GeneratedAdminToken string
	GeneratedAPIToken   string
	GeneratedMasterKey  string
}

type envFile struct {
	path  string
	lines []envLine
}

type envLine struct {
	raw    string
	key    string
	hasKey bool
}

func LocateEnvFile() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, ".env"), nil
}

func EnsureEnvFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return chmodOwnerOnly(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return chmodOwnerOnly(path)
}

func LoadDotEnv(path string) error {
	if err := MigrateLegacyEnvFile(path); err != nil {
		return err
	}
	file, err := readEnvFile(path)
	if err != nil {
		return err
	}
	values := file.values()
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func BootstrapSecrets(cfg Config, encryptedCredentialsExist bool) (Config, BootstrapReport, error) {
	report := BootstrapReport{EnvPath: cfg.EnvPath}
	if err := MigrateLegacyEnvFile(cfg.EnvPath); err != nil {
		return cfg, report, err
	}
	file, err := readEnvFile(cfg.EnvPath)
	if err != nil {
		return cfg, report, err
	}
	fileValues := file.values()

	adminToken := strings.TrimSpace(os.Getenv("RELAY_ADMIN_TOKEN"))
	masterKey := strings.TrimSpace(os.Getenv("RELAY_MASTER_KEY"))
	if !AdminTokenValid(adminToken) {
		if fileAdminToken, ok := fileValues["RELAY_ADMIN_TOKEN"]; ok && AdminTokenValid(fileAdminToken) {
			adminToken = strings.TrimSpace(fileAdminToken)
		}
	}
	if !security.MasterKeyValid(masterKey) {
		if fileMasterKey, ok := fileValues["RELAY_MASTER_KEY"]; ok && security.MasterKeyValid(fileMasterKey) {
			masterKey = strings.TrimSpace(fileMasterKey)
		}
	}

	masterValid := security.MasterKeyValid(masterKey)
	if !masterValid && encryptedCredentialsExist {
		return cfg, report, errors.New("RELAY_MASTER_KEY is missing or invalid, and encrypted provider credentials already exist; set the original RELAY_MASTER_KEY to decrypt existing credentials")
	}
	if !masterValid {
		generated, err := security.GenerateMasterKey()
		if err != nil {
			return cfg, report, err
		}
		masterKey = generated
		report.GeneratedMasterKey = generated
	}

	adminValid := AdminTokenValid(adminToken)
	if !adminValid {
		generated, err := generateToken()
		if err != nil {
			return cfg, report, err
		}
		adminToken = generated
		report.GeneratedAdminToken = generated
	}

	// LookupEnv, not Getenv: an explicitly empty process/file value is an
	// intentional opt-out. Process environment retains its existing precedence.
	apiToken, apiTokenSet := os.LookupEnv("RELAY_API_TOKEN")
	if !apiTokenSet {
		apiToken, apiTokenSet = fileValues["RELAY_API_TOKEN"]
	}
	if !apiTokenSet {
		apiToken, err = generateToken()
		if err != nil {
			return cfg, report, err
		}
		report.GeneratedAPIToken = apiToken
	}
	if apiToken != "" {
		if strings.ContainsAny(apiToken, " \t\r\n") {
			return cfg, report, errors.New("RELAY_API_TOKEN must not contain whitespace; use an explicitly empty value to disable inference authentication")
		}
		if security.TokensEqual(apiToken, adminToken) || security.TokensEqual(apiToken, masterKey) {
			return cfg, report, errors.New("RELAY_API_TOKEN must be different from RELAY_ADMIN_TOKEN and RELAY_MASTER_KEY")
		}
	}

	if err := os.Setenv("RELAY_ADMIN_TOKEN", adminToken); err != nil {
		return cfg, report, err
	}
	if err := os.Setenv("RELAY_MASTER_KEY", masterKey); err != nil {
		return cfg, report, err
	}
	cfg.AdminToken = adminToken
	cfg.APIToken = apiToken
	cfg.MasterKey = masterKey

	updates := make(map[string]string)
	if _, exists := fileValues["RELAY_API_TOKEN"]; !exists {
		updates["RELAY_API_TOKEN"] = apiToken
	}
	if shouldPersistAdminToken(fileValues, report.GeneratedAdminToken != "") {
		updates["RELAY_ADMIN_TOKEN"] = adminToken
	}
	if shouldPersistMasterKey(fileValues, report.GeneratedMasterKey != "") {
		updates["RELAY_MASTER_KEY"] = masterKey
	}
	if len(updates) > 0 {
		if err := file.write(updates); err != nil {
			return cfg, report, err
		}
	} else if err := chmodOwnerOnly(cfg.EnvPath); err != nil {
		return cfg, report, err
	}
	if err := os.Setenv("RELAY_API_TOKEN", apiToken); err != nil {
		return cfg, report, err
	}

	return cfg, report, nil
}

func PrintBootstrapBanner(w io.Writer, report BootstrapReport) {
	if report.GeneratedAdminToken == "" && report.GeneratedAPIToken == "" && report.GeneratedMasterKey == "" {
		return
	}
	fmt.Fprintf(w, "\nAnchorShell Relay generated local startup credentials and wrote them to %s.\n", report.EnvPath)
	fmt.Fprintln(w, "Save these values now; generated secrets are printed only once.")
	if report.GeneratedAdminToken != "" {
		fmt.Fprintf(w, "RELAY_ADMIN_TOKEN=%s\n", report.GeneratedAdminToken)
	}
	if report.GeneratedAPIToken != "" {
		fmt.Fprintf(w, "RELAY_API_TOKEN=%s\n", report.GeneratedAPIToken)
	}
	if report.GeneratedMasterKey != "" {
		fmt.Fprintf(w, "RELAY_MASTER_KEY=%s\n", report.GeneratedMasterKey)
		fmt.Fprintln(w, "Losing RELAY_MASTER_KEY means encrypted provider credentials cannot be recovered.")
	}
	fmt.Fprintln(w)
}

func AdminTokenValid(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < minAdminTokenLength {
		return false
	}
	return !weakSecretValue(trimmed)
}

func readEnvFile(path string) (envFile, error) {
	if err := EnsureEnvFile(path); err != nil {
		return envFile{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return envFile{}, err
	}
	rawLines := splitEnvLines(string(body))
	lines := make([]envLine, 0, len(rawLines))
	for _, raw := range rawLines {
		key, ok := parseEnvKey(raw)
		lines = append(lines, envLine{raw: raw, key: key, hasKey: ok})
	}
	return envFile{path: path, lines: lines}, nil
}

func (f envFile) values() map[string]string {
	values := make(map[string]string)
	for _, line := range f.lines {
		if !line.hasKey {
			continue
		}
		if _, rawValue, ok := strings.Cut(line.raw, "="); ok {
			values[line.key] = parseEnvValue(rawValue)
		}
	}
	return values
}

func (f envFile) write(updates map[string]string) error {
	remaining := make(map[string]string, len(updates))
	for key, value := range updates {
		remaining[key] = value
	}

	lines := make([]string, 0, len(f.lines)+len(updates)+1)
	for _, line := range f.lines {
		if line.hasKey {
			if value, ok := remaining[line.key]; ok {
				lines = append(lines, line.key+"="+value)
				delete(remaining, line.key)
				continue
			}
		}
		lines = append(lines, line.raw)
	}
	if len(remaining) > 0 && len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	for _, key := range []string{"RELAY_ADMIN_TOKEN", "RELAY_API_TOKEN", "RELAY_MASTER_KEY"} {
		if value, ok := remaining[key]; ok {
			lines = append(lines, key+"="+value)
			delete(remaining, key)
		}
	}
	for key, value := range remaining {
		lines = append(lines, key+"="+value)
	}

	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	original, err := os.ReadFile(f.path)
	if err != nil {
		return err
	}
	return replaceEnvFile(f.path, original, []byte(body))
}

func shouldPersistAdminToken(values map[string]string, generated bool) bool {
	value, exists := values["RELAY_ADMIN_TOKEN"]
	if generated {
		return true
	}
	return !exists || !AdminTokenValid(value)
}

func shouldPersistMasterKey(values map[string]string, generated bool) bool {
	value, exists := values["RELAY_MASTER_KEY"]
	if generated {
		return true
	}
	return !exists || !security.MasterKeyValid(value)
}

func generateToken() (string, error) {
	token := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func splitEnvLines(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimSuffix(body, "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

func parseEnvKey(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	before, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", false
	}
	key := strings.TrimSpace(before)
	if !validEnvKey(key) {
		return "", false
	}
	return key, true
}

func parseEnvValue(raw string) string {
	value := strings.TrimSpace(raw)
	if index := inlineCommentIndex(value); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '\'' || quote == '"') && value[len(value)-1] == quote {
			if quote == '"' {
				if unquoted, err := strconv.Unquote(value); err == nil {
					return unquoted
				}
			}
			return value[1 : len(value)-1]
		}
	}
	return value
}

func inlineCommentIndex(value string) int {
	inSingle := false
	inDouble := false
	escaped := false
	for i, r := range value {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inDouble {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
				return i
			}
		}
	}
	return -1
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	first := key[0]
	return first == '_' || (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')
}

func weakSecretValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "change-me", "changeme", "placeholder", "secret", "password":
		return true
	default:
		return false
	}
}

func chmodOwnerOnly(path string) error {
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return nil
}
