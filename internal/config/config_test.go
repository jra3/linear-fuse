package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Check default log level
	if cfg.Log.Level != "info" {
		t.Errorf("DefaultConfig() Log.Level = %q, want %q", cfg.Log.Level, "info")
	}

	// API key should be empty by default
	if cfg.APIKey != "" {
		t.Errorf("DefaultConfig() APIKey should be empty, got %q", cfg.APIKey)
	}

	// Telemetry file export is off by default with sane knob defaults
	if cfg.Telemetry.File.Enabled {
		t.Error("DefaultConfig() Telemetry.File.Enabled should be false")
	}
	if cfg.Telemetry.File.Interval != 60*time.Second {
		t.Errorf("DefaultConfig() Telemetry.File.Interval = %v, want %v", cfg.Telemetry.File.Interval, 60*time.Second)
	}
	if cfg.Telemetry.File.MaxSizeMB != 50 {
		t.Errorf("DefaultConfig() Telemetry.File.MaxSizeMB = %d, want 50", cfg.Telemetry.File.MaxSizeMB)
	}
	// Default path sits next to the other linearfs state files
	if filepath.Base(cfg.Telemetry.File.Path) != "metrics.jsonl" {
		t.Errorf("DefaultConfig() Telemetry.File.Path = %q, want a .../linearfs/metrics.jsonl path", cfg.Telemetry.File.Path)
	}
	if filepath.Base(filepath.Dir(cfg.Telemetry.File.Path)) != "linearfs" {
		t.Errorf("DefaultConfig() Telemetry.File.Path = %q, want it under a linearfs dir", cfg.Telemetry.File.Path)
	}
}

func TestLoadTelemetryConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
telemetry:
  file:
    enabled: true
    path: /tmp/custom-metrics.jsonl
    interval: 30s
    max_size_mb: 10
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	if !cfg.Telemetry.File.Enabled {
		t.Error("LoadWithEnv() Telemetry.File.Enabled should be true")
	}
	if cfg.Telemetry.File.Path != "/tmp/custom-metrics.jsonl" {
		t.Errorf("LoadWithEnv() Telemetry.File.Path = %q, want %q", cfg.Telemetry.File.Path, "/tmp/custom-metrics.jsonl")
	}
	if cfg.Telemetry.File.Interval != 30*time.Second {
		t.Errorf("LoadWithEnv() Telemetry.File.Interval = %v, want %v", cfg.Telemetry.File.Interval, 30*time.Second)
	}
	if cfg.Telemetry.File.MaxSizeMB != 10 {
		t.Errorf("LoadWithEnv() Telemetry.File.MaxSizeMB = %d, want 10", cfg.Telemetry.File.MaxSizeMB)
	}
}

func TestLoadTelemetryPartialKeepsDefaults(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	// Only flip the gate — path/interval/cap should keep their defaults.
	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
telemetry:
  file:
    enabled: true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	if !cfg.Telemetry.File.Enabled {
		t.Error("LoadWithEnv() Telemetry.File.Enabled should be true")
	}
	if cfg.Telemetry.File.Interval != 60*time.Second {
		t.Errorf("LoadWithEnv() Telemetry.File.Interval = %v, want default %v", cfg.Telemetry.File.Interval, 60*time.Second)
	}
	if cfg.Telemetry.File.MaxSizeMB != 50 {
		t.Errorf("LoadWithEnv() Telemetry.File.MaxSizeMB = %d, want default 50", cfg.Telemetry.File.MaxSizeMB)
	}
	if filepath.Base(cfg.Telemetry.File.Path) != "metrics.jsonl" {
		t.Errorf("LoadWithEnv() Telemetry.File.Path = %q, want default metrics.jsonl path", cfg.Telemetry.File.Path)
	}
}

// TestLoadTelemetryRequestsConfig covers the per-request debug log gate
// (telemetry.requests.*): parsing an explicit config, and the off-by-default
// with default path when the keys are absent.
func TestLoadTelemetryRequestsConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
telemetry:
  requests:
    enabled: true
    path: /tmp/custom-requests.jsonl
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	env := mockEnv(map[string]string{
		"XDG_CONFIG_HOME": tmpDir,
	})

	cfg, err := LoadWithEnv(env)
	if err != nil {
		t.Fatalf("LoadWithEnv() error: %v", err)
	}

	if !cfg.Telemetry.Requests.Enabled {
		t.Error("LoadWithEnv() Telemetry.Requests.Enabled should be true")
	}
	if cfg.Telemetry.Requests.Path != "/tmp/custom-requests.jsonl" {
		t.Errorf("LoadWithEnv() Telemetry.Requests.Path = %q, want /tmp/custom-requests.jsonl", cfg.Telemetry.Requests.Path)
	}
	// The sibling file export keeps its defaults untouched.
	if cfg.Telemetry.File.Enabled {
		t.Error("LoadWithEnv() Telemetry.File.Enabled flipped by a requests-only config")
	}
}

func TestTelemetryRequestsDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if cfg.Telemetry.Requests.Enabled {
		t.Error("DefaultConfig() Telemetry.Requests.Enabled should be false (the request log is opt-in)")
	}
	if filepath.Base(cfg.Telemetry.Requests.Path) != "requests.jsonl" {
		t.Errorf("DefaultConfig() Telemetry.Requests.Path = %q, want default requests.jsonl path", cfg.Telemetry.Requests.Path)
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
log:
  level: debug
  file: /var/log/linearfs.log
`
	// 0600: a config.yaml carrying an api_key must be owner-only, else Load
	// refuses it (see TestLoadRefusesLooseKeyFile).
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
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
	if cfg.Log.Level != "debug" {
		t.Errorf("LoadWithEnv() Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if cfg.Log.File != "/var/log/linearfs.log" {
		t.Errorf("LoadWithEnv() Log.File = %q, want %q", cfg.Log.File, "/var/log/linearfs.log")
	}
}

// TestLoadToleratesRemovedKeys: existing user config files may still carry
// keys whose fields have been removed — log.api_stats (died with APIStats)
// and mount.allow_other (dead knob removed in #355; the mount was always
// owner-only). yaml.v3 ignores unknown keys, so such a file must keep parsing.
func TestLoadToleratesRemovedKeys(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "linearfs")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}
	configContent := `
api_key: "key_with_stale_field"
mount:
  allow_other: true
log:
  level: debug
  api_stats: true
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadWithEnv(mockEnv(map[string]string{"XDG_CONFIG_HOME": tmpDir}))
	if err != nil {
		t.Fatalf("LoadWithEnv() error with stale api_stats key: %v", err)
	}
	if cfg.APIKey != "key_with_stale_field" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "key_with_stale_field")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
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

func TestLoadFrom(t *testing.T) {
	t.Run("explicit file loads", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "custom.yaml")
		// 0600: a key-bearing config file must be owner-only or Load refuses it.
		if err := os.WriteFile(path, []byte("api_key: from-file\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("LINEAR_API_KEY", "")
		cfg, err := LoadFrom(path)
		if err != nil {
			t.Fatalf("LoadFrom() error: %v", err)
		}
		if cfg.APIKey != "from-file" {
			t.Errorf("LoadFrom() APIKey = %q, want %q", cfg.APIKey, "from-file")
		}
	})

	t.Run("missing explicit file is an error", func(t *testing.T) {
		// Unlike Load's optional default path, a user-named --config file
		// that can't be read must fail loudly, not mount with defaults.
		_, err := LoadFrom(filepath.Join(t.TempDir(), "nope.yaml"))
		if err == nil {
			t.Fatal("LoadFrom() with missing file: want error, got nil")
		}
	})

	t.Run("env overrides explicit file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "custom.yaml")
		if err := os.WriteFile(path, []byte("api_key: from-file\n"), 0644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("LINEAR_API_KEY", "from-env")
		cfg, err := LoadFrom(path)
		if err != nil {
			t.Fatalf("LoadFrom() error: %v", err)
		}
		if cfg.APIKey != "from-env" {
			t.Errorf("LoadFrom() APIKey = %q, want %q", cfg.APIKey, "from-env")
		}
	})
}

// TestLoadRefusesLooseKeyFile guards #338: a config.yaml that carries an
// api_key and is group- or world-accessible (mode & 0o077 != 0) is refused at
// load with a message that names the fix (chmod 600). An owner-only (0600)
// file is accepted, and — the escape hatch — a loose file whose key is
// overridden by LINEAR_API_KEY is unaffected (the env is the key source, so
// the file's mode is irrelevant).
func TestLoadRefusesLooseKeyFile(t *testing.T) {
	// group/world-readable modes that must all be refused when a key is present.
	looseModes := []os.FileMode{0640, 0604, 0644, 0660, 0666, 0700 | 0044}
	for _, mode := range looseModes {
		mode := mode
		t.Run(fmt.Sprintf("refuse mode %04o", mode), func(t *testing.T) {
			tmpDir := t.TempDir()
			configDir := filepath.Join(tmpDir, "linearfs")
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(configDir, "config.yaml")
			if err := os.WriteFile(path, []byte("api_key: secret\n"), mode); err != nil {
				t.Fatal(err)
			}
			// os.WriteFile is subject to umask; force the intended loose bits.
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			_, err := LoadWithEnv(mockEnv(map[string]string{"XDG_CONFIG_HOME": tmpDir}))
			if err == nil {
				t.Fatalf("LoadWithEnv() with %04o key file: want refusal, got nil", mode)
			}
			if !strings.Contains(err.Error(), "chmod 600") {
				t.Errorf("refusal error %q: want it to name the fix (chmod 600)", err.Error())
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("refusal error %q: want it to name the file %q", err.Error(), path)
			}
		})
	}

	t.Run("accepts 0600 key file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configDir := filepath.Join(tmpDir, "linearfs")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(path, []byte("api_key: secret\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWithEnv(mockEnv(map[string]string{"XDG_CONFIG_HOME": tmpDir}))
		if err != nil {
			t.Fatalf("LoadWithEnv() with 0600 key file: %v", err)
		}
		if cfg.APIKey != "secret" {
			t.Errorf("APIKey = %q, want %q", cfg.APIKey, "secret")
		}
	})

	t.Run("env override makes loose file irrelevant", func(t *testing.T) {
		tmpDir := t.TempDir()
		configDir := filepath.Join(tmpDir, "linearfs")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(path, []byte("api_key: file-secret\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWithEnv(mockEnv(map[string]string{
			"XDG_CONFIG_HOME": tmpDir,
			"LINEAR_API_KEY":  "env-secret",
		}))
		if err != nil {
			t.Fatalf("LoadWithEnv() with env override of loose file: %v", err)
		}
		if cfg.APIKey != "env-secret" {
			t.Errorf("APIKey = %q, want env-secret (env is the source)", cfg.APIKey)
		}
	})

	t.Run("loose file with no api_key is fine", func(t *testing.T) {
		// A config.yaml that carries only non-secret settings never triggers
		// the refusal — the check keys on a present api_key, not the file.
		tmpDir := t.TempDir()
		configDir := filepath.Join(tmpDir, "linearfs")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(configDir, "config.yaml")
		if err := os.WriteFile(path, []byte("log:\n  level: debug\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWithEnv(mockEnv(map[string]string{"XDG_CONFIG_HOME": tmpDir}))
		if err != nil {
			t.Fatalf("LoadWithEnv() with keyless loose file: %v", err)
		}
		if cfg.Log.Level != "debug" {
			t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
		}
	})
}
