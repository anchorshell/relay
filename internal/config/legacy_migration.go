package config

// Legacy names are accepted only as on-disk migration input, never as runtime
// environment aliases. Keep this file independent of credential generation.
import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anchorshell/relay/internal/security"
)

func RejectLegacyEnvironment() error {
	var names []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "BOUNCER_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) != 0 {
		name := names[0]
		return fmt.Errorf("%s was renamed to %s. Update your environment (values have not been logged)", name, "RELAY_"+strings.TrimPrefix(name, "BOUNCER_"))
	}
	return nil
}

// MigrateLegacyEnvFile atomically renames keys while retaining the original
// quoting, comments, whitespace, line endings, and intentionally empty values.
func MigrateLegacyEnvFile(path string) error {
	if err := RejectLegacyEnvironment(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("configuration migration requires a regular .env file, not a symlink")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(body), "\n")
	values := map[string]string{}
	for _, line := range lines {
		if key, ok := parseEnvKey(line); ok {
			_, raw, _ := strings.Cut(line, "=")
			values[key] = parseEnvValue(raw)
		}
	}
	changed := false
	var output strings.Builder
	for _, line := range lines {
		key, ok := parseEnvKey(line)
		if !ok || !strings.HasPrefix(key, "BOUNCER_") {
			output.WriteString(line)
			continue
		}
		next := "RELAY_" + strings.TrimPrefix(key, "BOUNCER_")
		if value, exists := values[next]; exists {
			if err := validateMigrationWinner(next, value, values); err != nil {
				return err
			}
			// Preserve an inline comment even when removing a duplicate assignment.
			_, raw, _ := strings.Cut(line, "=")
			if i := inlineCommentIndex(raw); i >= 0 {
				output.WriteString(strings.TrimLeft(raw[i:], " \t"))
			}
		} else {
			before, after, _ := strings.Cut(line, "=")
			// These were generated defaults, not user data. Databases are reset
			// manually for this pre-launch cutover; never open the old default.
			if next == "RELAY_DB_PATH" && filepath.Base(values[key]) == "bouncer.db" {
				after = strings.Replace(after, "bouncer.db", "relay.db", 1)
			}
			if next == "RELAY_ADMIN_COOKIE_NAME" {
				for _, old := range []string{"bouncer_admin_token", "bouncer_pro_admin_token"} {
					if values[key] == old {
						after = strings.Replace(after, old, strings.Replace(old, "bouncer", "relay", 1), 1)
					}
				}
			}
			output.WriteString(strings.Replace(before, key, next, 1) + "=" + after)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return replaceEnvFile(path, body, []byte(output.String()))
}

func validateMigrationWinner(key, value string, values map[string]string) error {
	valid := true
	switch key {
	case "RELAY_ADMIN_TOKEN":
		valid = AdminTokenValid(value)
	case "RELAY_MASTER_KEY":
		valid = security.MasterKeyValid(value)
	case "RELAY_API_TOKEN":
		valid = !strings.ContainsAny(value, " \t\r\n")
		for _, other := range []string{"ADMIN_TOKEN", "MASTER_KEY"} {
			candidate, exists := values["RELAY_"+other]
			if !exists {
				candidate = values["BOUNCER_"+other]
			}
			if value != "" && security.TokensEqual(value, candidate) {
				valid = false
			}
		}
	}
	if !valid {
		return fmt.Errorf("cannot migrate .env: conflicting %s is invalid; correct it before removing the legacy setting", key)
	}
	return nil
}

// replaceEnvFile never truncates the original. Sync the replacement before
// publishing it, and refuse to overwrite edits made since it was read.
func replaceEnvFile(path string, original, replacement []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".relay-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(replacement); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, current) {
		return errors.New(".env changed during migration; retry without concurrent configuration writers")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
