package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nehsa-net/weather-microservice-go-gin/internal/config"
)

// The tests below that call t.Chdir deliberately do NOT call t.Parallel().
//
// t.Chdir panics in a parallel test, for the same reason t.Setenv does: the
// working directory is process-global, so one test changing it would change it
// under every test running at the same time. The panic is the framework
// refusing to let you write a race.
//
// envMap turns a map into the lookup function Load expects. This is why Load
// takes a function instead of calling os.Getenv: these tests run in parallel,
// and t.Setenv panics in a parallel test because the process environment is
// shared mutable state.
func envMap(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envMap(map[string]string{"OPENWEATHER_API_KEY": "abc123"}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.APIKey != "abc123" {
		t.Errorf("APIKey = %q, want abc123", cfg.APIKey)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.UpstreamURL != config.DefaultUpstream {
		t.Errorf("UpstreamURL = %q, want %q", cfg.UpstreamURL, config.DefaultUpstream)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envMap(map[string]string{
		"OPENWEATHER_API_KEY":  "abc123",
		"ADDR":                 ":9999",
		"WEATHER_UPSTREAM_URL": "http://127.0.0.1:1234",
	}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Addr != ":9999" {
		t.Errorf("Addr = %q, want :9999", cfg.Addr)
	}
	if cfg.UpstreamURL != "http://127.0.0.1:1234" {
		t.Errorf("UpstreamURL = %q, want the override", cfg.UpstreamURL)
	}
}

func TestLoadTrimsAndTreatsBlankAsUnset(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(envMap(map[string]string{
		"OPENWEATHER_API_KEY": "  abc123  ",
		"ADDR":                "   ",
	}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	// A key with a trailing newline — the usual result of `echo key > file` or
	// a copy-paste into a secret — must still work.
	if cfg.APIKey != "abc123" {
		t.Errorf("APIKey = %q, want it trimmed to abc123", cfg.APIKey)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want the default when the value is blank", cfg.Addr)
	}
}

func TestLoadWithoutAnyKey(t *testing.T) {

	// chdir into an empty directory so the legacy key file cannot be found.
	// t.Chdir (Go 1.24+) restores the working directory automatically.
	t.Chdir(t.TempDir())

	_, err := config.Load(envMap(map[string]string{}))

	if err == nil {
		t.Fatal("Load() succeeded with no key anywhere, want an error")
	}
	// The message must name both ways of supplying it, or an operator has to
	// read the source to find out.
	for _, want := range []string{"OPENWEATHER_API_KEY", config.KeyFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The legacy path still works, so an existing deployment that mounts the file
// keeps running after this change.
func TestLoadFallsBackToTheKeyFile(t *testing.T) {

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.KeyFile), []byte("key-from-file\n"), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	t.Chdir(dir)

	cfg, err := config.Load(envMap(map[string]string{}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.APIKey != "key-from-file" {
		t.Errorf("APIKey = %q, want key-from-file (newline trimmed)", cfg.APIKey)
	}
}

func TestEnvironmentWinsOverTheKeyFile(t *testing.T) {

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.KeyFile), []byte("key-from-file"), 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	t.Chdir(dir)

	cfg, err := config.Load(envMap(map[string]string{"OPENWEATHER_API_KEY": "key-from-env"}))
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	// Precedence matters during a migration: an operator setting the variable
	// expects it to take effect even if a stale file is still on the image.
	if cfg.APIKey != "key-from-env" {
		t.Errorf("APIKey = %q, want the environment to win", cfg.APIKey)
	}
}

func TestLoadErrorDoesNotEchoTheKey(t *testing.T) {

	t.Chdir(t.TempDir())

	// An empty key is treated as absent, and the resulting error must not
	// contain whatever was in the variable.
	const secret = "super-secret-key-value"
	_, err := config.Load(envMap(map[string]string{"OPENWEATHER_API_KEY": "   "}))
	if err == nil {
		t.Fatal("Load() succeeded, want an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked the key: %v", err)
	}
}
