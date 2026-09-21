package config

import (
	"os"
	"testing"
)

func TestHTTPAddrDefaultAndOverrides(t *testing.T) {
	for _, tt := range []struct {
		name    string
		file    string
		process *string
		want    string
	}{
		{name: "default", want: ":11730"},
		{name: "empty uses default", process: stringPointer(""), want: ":11730"},
		{name: "dotenv override", file: "RELAY_HTTP_ADDR=127.0.0.1:9000\n", want: "127.0.0.1:9000"},
		{name: "process overrides dotenv", file: "RELAY_HTTP_ADDR=:9000\n", process: stringPointer("127.0.0.1:9001"), want: "127.0.0.1:9001"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// LoadDotEnv fills only unset variables. Restore the caller's
			// environment even when this case loads the address from a file.
			t.Setenv("RELAY_HTTP_ADDR", "")
			if tt.process == nil {
				if err := os.Unsetenv("RELAY_HTTP_ADDR"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("RELAY_HTTP_ADDR", *tt.process)
			}
			t.Setenv("RELAY_TEMP_DIR", t.TempDir())
			cfg, err := LoadFromEnvFile(writeTestEnv(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HTTPAddr != tt.want {
				t.Fatalf("HTTPAddr = %q, want %q", cfg.HTTPAddr, tt.want)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
