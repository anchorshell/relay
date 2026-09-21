package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	DefaultHTTPAddr                     = ":11730"
	DefaultDBPath                       = "relay.db"
	DefaultAdminCookieName              = "relay_admin_token"
	DefaultLogLevel                     = "info"
	DefaultTempDir                      = "tmp"
	DefaultMaxWaitMS                    = 60000
	DefaultMaxLatencyMS                 = 600000
	DefaultMaxQueueLength               = 1000
	DefaultMaxQueueAgeMS                = 300000
	DefaultMemBodyBytes                 = 1 << 20
	DefaultFileBodyBytes                = 8 << 20
	DefaultUpstreamMaxIdleConns         = 1024
	DefaultUpstreamMaxIdleConnsPerHost  = 512
	DefaultUpstreamIdleConnTimeout      = 90 * time.Second
	DefaultCharacterizationWorkers      = 1
	DefaultCharacterizationQueueSize    = 128
	DefaultCharacterizationJobTimeout   = 25 * time.Millisecond
	DefaultCharacterizationTerminalWait = 5 * time.Millisecond
)

type Config struct {
	EnvPath                      string
	HTTPAddr                     string
	DBPath                       string
	AdminToken                   string
	APIToken                     string
	AdminCookieName              string
	MasterKey                    string
	InsecureDev                  bool
	DevUITarget                  string
	DevReloadFile                string
	LogLevel                     string
	TempDir                      string
	DefaultWaitMS                int64
	DefaultMaxLatencyMS          int64
	MaxQueueLen                  int
	MaxQueueAgeMS                int64
	MemBodyBytes                 int64
	FileBodyBytes                int64
	UpstreamMaxIdleConns         int
	UpstreamMaxIdleConnsPerHost  int
	UpstreamIdleConnTimeout      time.Duration
	CharacterizationEnabled      bool
	CharacterizationWorkers      int
	CharacterizationQueueSize    int
	CharacterizationJobTimeout   time.Duration
	CharacterizationTerminalWait time.Duration
	CharacterizationStrictModel  bool
}

func Load() (Config, error) {
	envPath, err := LocateEnvFile()
	if err != nil {
		return Config{}, err
	}
	return LoadFromEnvFile(envPath)
}

func LoadFromEnvFile(envPath string) (Config, error) {
	if err := EnsureEnvFile(envPath); err != nil {
		return Config{}, err
	}
	if err := LoadDotEnv(envPath); err != nil {
		return Config{}, err
	}

	cfg := Config{
		EnvPath:                      envPath,
		HTTPAddr:                     envOr("RELAY_HTTP_ADDR", DefaultHTTPAddr),
		DBPath:                       envOr("RELAY_DB_PATH", DefaultDBPath),
		AdminToken:                   os.Getenv("RELAY_ADMIN_TOKEN"),
		APIToken:                     os.Getenv("RELAY_API_TOKEN"),
		AdminCookieName:              envOr("RELAY_ADMIN_COOKIE_NAME", DefaultAdminCookieName),
		MasterKey:                    os.Getenv("RELAY_MASTER_KEY"),
		InsecureDev:                  envBool("RELAY_INSECURE_DEV", false),
		DevUITarget:                  os.Getenv("RELAY_DEV_UI_TARGET"),
		DevReloadFile:                os.Getenv("RELAY_DEV_RELOAD_FILE"),
		LogLevel:                     envOr("RELAY_LOG_LEVEL", DefaultLogLevel),
		TempDir:                      envOr("RELAY_TEMP_DIR", DefaultTempDir),
		DefaultWaitMS:                envInt64("RELAY_DEFAULT_MAX_WAIT_MS", DefaultMaxWaitMS),
		DefaultMaxLatencyMS:          envInt64("RELAY_DEFAULT_MAX_LATENCY_MS", DefaultMaxLatencyMS),
		MaxQueueLen:                  envInt("RELAY_MAX_QUEUE_LENGTH", DefaultMaxQueueLength),
		MaxQueueAgeMS:                envInt64("RELAY_MAX_QUEUE_AGE_MS", DefaultMaxQueueAgeMS),
		MemBodyBytes:                 envInt64("RELAY_BODY_MEMORY_THRESHOLD_BYTES", DefaultMemBodyBytes),
		FileBodyBytes:                envInt64("RELAY_BODY_SPOOL_THRESHOLD_BYTES", DefaultFileBodyBytes),
		UpstreamMaxIdleConns:         envInt("RELAY_UPSTREAM_MAX_IDLE_CONNS", DefaultUpstreamMaxIdleConns),
		UpstreamMaxIdleConnsPerHost:  envInt("RELAY_UPSTREAM_MAX_IDLE_CONNS_PER_HOST", DefaultUpstreamMaxIdleConnsPerHost),
		UpstreamIdleConnTimeout:      envDuration("RELAY_UPSTREAM_IDLE_CONN_TIMEOUT", DefaultUpstreamIdleConnTimeout),
		CharacterizationEnabled:      envBool("CHARACTERIZATION_ENABLED", true),
		CharacterizationWorkers:      envInt("CHARACTERIZATION_WORKERS", DefaultCharacterizationWorkers),
		CharacterizationQueueSize:    envInt("CHARACTERIZATION_QUEUE_SIZE", DefaultCharacterizationQueueSize),
		CharacterizationJobTimeout:   envDuration("CHARACTERIZATION_JOB_TIMEOUT", DefaultCharacterizationJobTimeout),
		CharacterizationTerminalWait: envDuration("CHARACTERIZATION_TERMINAL_WAIT", DefaultCharacterizationTerminalWait),
		CharacterizationStrictModel:  envBool("CHARACTERIZATION_STRICT_MODEL", false),
	}

	if err := os.MkdirAll(filepath.Clean(cfg.TempDir), 0o755); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.Atoi(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		parsed, err := time.ParseDuration(v)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
