package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	// Check default cache TTL
	if cfg.Cache.TTL != 60*time.Second {
		t.Errorf("DefaultConfig() Cache.TTL = %v, want %v", cfg.Cache.TTL, 60*time.Second)
	}

	// Check default cache max entries
	if cfg.Cache.MaxEntries != 10000 {
		t.Errorf("DefaultConfig() Cache.MaxEntries = %d, want 10000", cfg.Cache.MaxEntries)
	}

	// Check default mount settings
	if cfg.Mount.DefaultPath != "" {
		t.Errorf("DefaultConfig() Mount.DefaultPath = %q, want empty", cfg.Mount.DefaultPath)
	}
	if cfg.Mount.AllowOther != false {
		t.Error("DefaultConfig() Mount.AllowOther should be false")
	}

	// Check default log level
	if cfg.Log.Level != "info" {
		t.Errorf("DefaultConfig() Log.Level = %q, want %q", cfg.Log.Level, "info")
	}

	// API key should be empty by default
	if cfg.APIKey != "" {
		t.Errorf("DefaultConfig() APIKey should be empty, got %q", cfg.APIKey)
	}
}

func TestLoadWithConfigFile(t *testing.T) {
	// Create a temporary directory for config
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
api_key: "test_api_key_from_file"
cache:
  ttl: 120s
  max_entries: 5000
mount:
  default_path: /mnt/linear
  allow_other: true
log:
  level: debug
  file: /var/log/linearfs.log
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Set XDG_CONFIG_HOME to our temp directory
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	// Clear LINEAR_API_KEY to test file-based config
	oldAPIKey := os.Getenv("LINEAR_API_KEY")
	os.Unsetenv("LINEAR_API_KEY")
	defer func() {
		if oldAPIKey != "" {
			os.Setenv("LINEAR_API_KEY", oldAPIKey)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.APIKey != "test_api_key_from_file" {
		t.Errorf("Load() APIKey = %q, want %q", cfg.APIKey, "test_api_key_from_file")
	}
	if cfg.Cache.TTL != 120*time.Second {
		t.Errorf("Load() Cache.TTL = %v, want %v", cfg.Cache.TTL, 120*time.Second)
	}
	if cfg.Cache.MaxEntries != 5000 {
		t.Errorf("Load() Cache.MaxEntries = %d, want 5000", cfg.Cache.MaxEntries)
	}
	if cfg.Mount.DefaultPath != "/mnt/linear" {
		t.Errorf("Load() Mount.DefaultPath = %q, want %q", cfg.Mount.DefaultPath, "/mnt/linear")
	}
	if cfg.Mount.AllowOther != true {
		t.Error("Load() Mount.AllowOther should be true")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Load() Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if cfg.Log.File != "/var/log/linearfs.log" {
		t.Errorf("Load() Log.File = %q, want %q", cfg.Log.File, "/var/log/linearfs.log")
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	// Create a config file with an API key
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `api_key: "file_api_key"`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Set XDG_CONFIG_HOME
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	// Set environment variable to override
	oldAPIKey := os.Getenv("LINEAR_API_KEY")
	os.Setenv("LINEAR_API_KEY", "env_api_key")
	defer func() {
		if oldAPIKey != "" {
			os.Setenv("LINEAR_API_KEY", oldAPIKey)
		} else {
			os.Unsetenv("LINEAR_API_KEY")
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Environment variable should override file
	if cfg.APIKey != "env_api_key" {
		t.Errorf("Load() APIKey = %q, want %q (env override)", cfg.APIKey, "env_api_key")
	}
}

func TestLoadNoConfigFile(t *testing.T) {
	// Create a temp directory with no config file
	tmpDir := t.TempDir()

	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	// Clear LINEAR_API_KEY
	oldAPIKey := os.Getenv("LINEAR_API_KEY")
	os.Unsetenv("LINEAR_API_KEY")
	defer func() {
		if oldAPIKey != "" {
			os.Setenv("LINEAR_API_KEY", oldAPIKey)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should get defaults
	if cfg.Cache.TTL != 60*time.Second {
		t.Errorf("Load() without file should use default Cache.TTL, got %v", cfg.Cache.TTL)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Load() without file should use default Log.Level, got %q", cfg.Log.Level)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	invalidContent := `
api_key: [this is invalid yaml
cache:
  ttl: not a duration
`
	if err := os.WriteFile(configPath, []byte(invalidContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	_, err := Load()
	if err == nil {
		t.Error("Load() with invalid YAML should return error")
	}
}

func TestGetConfigPathXDG(t *testing.T) {
	tmpDir := t.TempDir()

	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	path := getConfigPath()
	expected := filepath.Join(tmpDir, "linearfs", "config.yaml")
	if path != expected {
		t.Errorf("getConfigPath() = %q, want %q", path, expected)
	}
}

func TestGetConfigPathFallback(t *testing.T) {
	// Clear XDG_CONFIG_HOME to test fallback
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Unsetenv("XDG_CONFIG_HOME")
	defer func() {
		if oldXDG != "" {
			os.Setenv("XDG_CONFIG_HOME", oldXDG)
		}
	}()

	path := getConfigPath()
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "linearfs", "config.yaml")
	if path != expected {
		t.Errorf("getConfigPath() = %q, want %q", path, expected)
	}
}

func TestLoadPartialConfig(t *testing.T) {
	// Test that partial config merges with defaults
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	// Only set cache TTL, leave everything else to defaults
	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
cache:
  ttl: 5m
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	oldAPIKey := os.Getenv("LINEAR_API_KEY")
	os.Unsetenv("LINEAR_API_KEY")
	defer func() {
		if oldAPIKey != "" {
			os.Setenv("LINEAR_API_KEY", oldAPIKey)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Explicitly set value
	if cfg.Cache.TTL != 5*time.Minute {
		t.Errorf("Load() Cache.TTL = %v, want %v", cfg.Cache.TTL, 5*time.Minute)
	}

	// Default value preserved (this is how YAML unmarshaling works with pre-initialized structs)
	if cfg.Cache.MaxEntries != 10000 {
		t.Errorf("Load() Cache.MaxEntries = %d, want 10000 (default)", cfg.Cache.MaxEntries)
	}

	// Log level should still be default
	if cfg.Log.Level != "info" {
		t.Errorf("Load() Log.Level = %q, want %q (default)", cfg.Log.Level, "info")
	}
}
