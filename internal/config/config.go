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

type MountConfig struct {
	DefaultPath string `yaml:"default_path"`
	AllowOther  bool   `yaml:"allow_other"`
}

type LogConfig struct {
	Level    string `yaml:"level"`
	File     string `yaml:"file"`
	APIStats bool   `yaml:"api_stats"`
}

// TelemetryConfig configures the OTEL metrics pipeline (internal/telemetry).
// The in-memory meter and the journald summary line are always on; only the
// JSONL file export is configurable here.
type TelemetryConfig struct {
	File TelemetryFileConfig `yaml:"file"`
}

// TelemetryFileConfig gates the JSONL metrics file export (off by default).
type TelemetryFileConfig struct {
	Enabled   bool          `yaml:"enabled"`
	Path      string        `yaml:"path"`
	Interval  time.Duration `yaml:"interval"`
	MaxSizeMB int           `yaml:"max_size_mb"`
}

func DefaultConfig() *Config {
	return &Config{
		Cache: CacheConfig{
			TTL:        60 * time.Second,
			MaxEntries: 10000,
		},
		Mount: MountConfig{
			DefaultPath: "",
			AllowOther:  false,
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

// Load loads configuration using the real environment.
func Load() (*Config, error) {
	return LoadWithEnv(os.Getenv)
}

// LoadWithEnv loads configuration using the provided environment lookup function.
// This allows tests to provide isolated environment values.
func LoadWithEnv(getenv func(string) string) (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from config file
	configPath := getConfigPathWithEnv(getenv)
	if data, err := os.ReadFile(configPath); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Environment variables override config file
	if apiKey := getenv("LINEAR_API_KEY"); apiKey != "" {
		cfg.APIKey = apiKey
	}

	return cfg, nil
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
