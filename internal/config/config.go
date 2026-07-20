package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	APIKey    string          `yaml:"api_key"`
	Cache     CacheConfig     `yaml:"cache"`
	Mount     MountConfig     `yaml:"mount"`
	Log       LogConfig       `yaml:"log"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

type CacheConfig struct {
	TTL        time.Duration `yaml:"ttl"`
	MaxEntries int           `yaml:"max_entries"`
}

// MountConfig configures the mount. The allow_other key that used to live
// here was a dead knob (never wired to fuse.MountOptions — the mount is
// always owner-only) and is gone (#355); yaml.v3 ignores unknown keys, so
// old config files carrying it still parse.
type MountConfig struct {
	DefaultPath string `yaml:"default_path"`
}

// LogConfig configures logging. The api_stats key that used to live here is
// gone with APIStats (the OTEL telemetry summary is always on); yaml.v3
// ignores unknown keys, so old config files carrying it still parse.
type LogConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

// TelemetryConfig configures the OTEL metrics pipeline (internal/telemetry)
// plus the per-request debug log. The in-memory meter and the journald
// summary line are always on; only the JSONL file export and the request log
// are configurable here.
type TelemetryConfig struct {
	File     TelemetryFileConfig     `yaml:"file"`
	Requests TelemetryRequestsConfig `yaml:"requests"`
}

// TelemetryFileConfig gates the JSONL metrics file export (off by default).
type TelemetryFileConfig struct {
	Enabled   bool          `yaml:"enabled"`
	Path      string        `yaml:"path"`
	Interval  time.Duration `yaml:"interval"`
	MaxSizeMB int           `yaml:"max_size_mb"`
}

// TelemetryRequestsConfig gates the per-request JSONL debug log (off by
// default): one JSON line per completed GraphQL request, written by the api
// client. This is an application debug log, NOT an OTEL signal — the
// metrics-only/traces-never policy is untouched. It exists for offline
// analysis runs (duplicate-fetch detection, complexity attribution; see
// docs/plans/2026-07-09-coldstart-observation-plan.md), which is why the
// full variables map is logged.
type TelemetryRequestsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

func DefaultConfig() *Config {
	return &Config{
		Cache: CacheConfig{
			TTL:        60 * time.Second,
			MaxEntries: 10000,
		},
		Mount: MountConfig{
			DefaultPath: "",
		},
		Log: LogConfig{
			Level: "info",
		},
		Telemetry: TelemetryConfig{
			File: TelemetryFileConfig{
				Enabled:   false,
				Path:      DefaultTelemetryPath(),
				Interval:  60 * time.Second,
				MaxSizeMB: 50,
			},
			Requests: TelemetryRequestsConfig{
				Enabled: false,
				Path:    DefaultRequestLogPath(),
			},
		},
	}
}

// DefaultTelemetryPath returns the default JSONL metrics file path, next to
// the other linearfs state files (same convention as db.DefaultDBPath's
// cache.db).
func DefaultTelemetryPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "linearfs", "metrics.jsonl")
}

// DefaultRequestLogPath returns the default per-request JSONL debug log
// path, next to the other linearfs state files (same convention as
// DefaultTelemetryPath's metrics.jsonl).
func DefaultRequestLogPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.Getenv("HOME")
	}
	return filepath.Join(configDir, "linearfs", "requests.jsonl")
}

// Load loads configuration using the real environment and the default config
// path. A missing default file is fine: defaults + env apply.
func Load() (*Config, error) {
	return LoadWithEnv(os.Getenv)
}

// LoadFrom loads configuration from an explicitly named config file (the
// --config flag). Unlike Load, an unreadable file is an error — the user
// asked for that exact file, so silently falling back to defaults would mount
// with the wrong config. Environment variables still override.
func LoadFrom(path string) (*Config, error) {
	return loadPath(os.Getenv, path, true)
}

// LoadWithEnv loads configuration using the provided environment lookup function.
// This allows tests to provide isolated environment values.
func LoadWithEnv(getenv func(string) string) (*Config, error) {
	return loadPath(getenv, getConfigPathWithEnv(getenv), false)
}

// loadPath reads path into DefaultConfig, then applies env overrides.
// explicit governs the missing-file contract: the default path is optional,
// a user-named path is not.
func loadPath(getenv func(string) string, path string, explicit bool) (*Config, error) {
	cfg := DefaultConfig()

	fileRead := false
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		fileRead = true
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
		}
	case explicit:
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// The api_key came from the file unless the env var overrides it below.
	keyFromFile := fileRead && cfg.APIKey != ""

	// Environment variables override config file
	if apiKey := getenv("LINEAR_API_KEY"); apiKey != "" {
		cfg.APIKey = apiKey
		keyFromFile = false
	}

	// #338: when the API key's source is the config file (not the env-var
	// escape hatch), the file must be owner-only — group or other access to a
	// file holding a secret is refused, ssh StrictModes style. The env path is
	// deliberately untouched: the systemd EnvironmentFile is systemd's to
	// protect, and an operator exporting LINEAR_API_KEY has opted out of the
	// on-disk key entirely.
	if keyFromFile {
		if err := requireOwnerOnly(path); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// requireOwnerOnly refuses a config file that holds the API key and is
// accessible to group or other (mode & 0o077 != 0). The error names the fix so
// an operator can act on it directly.
func requireOwnerOnly(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat config file %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf(
			"config file %s is group/other-accessible (mode %04o) but holds an api_key; "+
				"refusing to load — run: chmod 600 %s",
			path, perm, path)
	}
	return nil
}

func getConfigPathWithEnv(getenv func(string) string) string {
	// Check XDG_CONFIG_HOME first
	if xdgConfig := getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "linearfs", "config.yaml")
	}

	// Fall back to ~/.config
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "linearfs", "config.yaml")
}
