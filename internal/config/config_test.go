package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mockEnv creates an environment lookup function from a map.
func mockEnv(env map[string]string) func(string) string {
	return func(key string) string {
		return env[key]
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
  default_path: ~/linear
  allow_other: true
log:
  level: debug
  file: /var/log/linearfs.log
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Use isolated environment
	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
		// LINEAR_API_KEY not set - should use file value
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	if cfg.APIKey != "test_api_key_from_file" {
		t.Errorf("LoadWithEnv() APIKey = %q, want %q", cfg.APIKey, "test_api_key_from_file")
	}
	if cfg.Cache.TTL != 120*time.Second {
		t.Errorf("LoadWithEnv() Cache.TTL = %v, want %v", cfg.Cache.TTL, 120*time.Second)
	}
	if cfg.Cache.MaxEntries != 5000 {
		t.Errorf("LoadWithEnv() Cache.MaxEntries = %d, want 5000", cfg.Cache.MaxEntries)
	}
	if cfg.Mount.DefaultPath != "~/linear" {
		t.Errorf("LoadWithEnv() Mount.DefaultPath = %q, want %q", cfg.Mount.DefaultPath, "~/linear")
	}
	if cfg.Mount.AllowOther != true {
		t.Error("LoadWithEnv() Mount.AllowOther should be true")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("LoadWithEnv() Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if cfg.Log.File != "/var/log/linearfs.log" {
		t.Errorf("LoadWithEnv() Log.File = %q, want %q", cfg.Log.File, "/var/log/linearfs.log")
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	t.Parallel()
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

	// Use isolated environment with both XDG_CONFIG_HOME and LINEAR_API_KEY
	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
		"LINEAR_API_KEY":  "env_api_key",
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	// Environment variable should override file
	if cfg.APIKey != "env_api_key" {
		t.Errorf("LoadWithEnv() APIKey = %q, want %q (env override)", cfg.APIKey, "env_api_key")
	}
}

func TestLoadNoConfigFile(t *testing.T) {
	t.Parallel()
	// Create a temp directory with no config file
	tmpDir := t.TempDir()

	// Use isolated environment pointing to empty dir
	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
		// LINEAR_API_KEY not set
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	// Should get defaults
	if cfg.Cache.TTL != 60*time.Second {
		t.Errorf("LoadWithEnv() without file should use default Cache.TTL, got %v", cfg.Cache.TTL)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("LoadWithEnv() without file should use default Log.Level, got %q", cfg.Log.Level)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	t.Parallel()
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

	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
	})

	_, err := LoadWithEnv(env)
	if err == nil {
		t.Error("LoadWithEnv() with invalid YAML should return error")
	}
}

func TestGetConfigPathXDG(t *testing.T) {
	t.Parallel()
	tmpDir := "/custom/config/path"

	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
	})

	path := getConfigPathWithEnv(env)
	expected := filepath.Join(tmpDir, "linearfs", "config.yaml")
	if path != expected {
		t.Errorf("getConfigPathWithEnv() = %q, want %q", path, expected)
	}
}

func TestGetConfigPathFallback(t *testing.T) {
	t.Parallel()
	// Empty environment - no XDG_CONFIG_HOME set
	env := mockEnv(map[string]string{})

	path := getConfigPathWithEnv(env)
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "linearfs", "config.yaml")
	if path != expected {
		t.Errorf("getConfigPathWithEnv() = %q, want %q", path, expected)
	}
}

func TestLoadPartialConfig(t *testing.T) {
	t.Parallel()
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

	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
		// LINEAR_API_KEY not set
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	// Explicitly set value
	if cfg.Cache.TTL != 5*time.Minute {
		t.Errorf("LoadWithEnv() Cache.TTL = %v, want %v", cfg.Cache.TTL, 5*time.Minute)
	}

	// Default value preserved (this is how YAML unmarshaling works with pre-initialized structs)
	if cfg.Cache.MaxEntries != 10000 {
		t.Errorf("LoadWithEnv() Cache.MaxEntries = %d, want 10000 (default)", cfg.Cache.MaxEntries)
	}

	// Log level should still be default
	if cfg.Log.Level != "info" {
		t.Errorf("LoadWithEnv() Log.Level = %q, want %q (default)", cfg.Log.Level, "info")
	}
}
