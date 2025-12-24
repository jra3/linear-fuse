package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	APIKey string      `yaml:"api_key"`
	Cache  CacheConfig `yaml:"cache"`
	Mount  MountConfig `yaml:"mount"`
	Log    LogConfig   `yaml:"log"`
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
	}
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

func getConfigPath() string {
	return getConfigPathWithEnv(os.Getenv)
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
