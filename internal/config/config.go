// Package config resolves runtime settings.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	Addr        string
	UpstreamURL string
	APIKey      string
}

// KeyFile is the legacy location the service used to read its key from.
const KeyFile = "openweatherapi.key"

// DefaultUpstream is the public OpenWeatherMap host.
const DefaultUpstream = "https://api.openweathermap.org"

// Load resolves configuration, preferring the environment and falling back to
// the legacy key file so existing deployments keep working.
//
// It takes a lookup function rather than calling os.Getenv, so tests can pass a
// map and run in parallel — t.Setenv panics in a parallel test, because the
// process environment is shared mutable state.
func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}

	cfg := Config{
		Addr:        get("ADDR", ":8080"),
		UpstreamURL: get("WEATHER_UPSTREAM_URL", DefaultUpstream),
		APIKey:      get("OPENWEATHER_API_KEY", ""),
	}

	if cfg.APIKey == "" {
		// Legacy path. A missing file is not fatal here — the error below
		// reports the real problem ("no key from anywhere") rather than a
		// confusing "no such file".
		if key, err := readKeyFile(KeyFile); err == nil {
			cfg.APIKey = key
		}
	}

	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf(
			"no API key: set OPENWEATHER_API_KEY, or put the key in %s", KeyFile)
	}
	return cfg, nil
}

// readKeyFile reads the first line of path.
func readKeyFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading %s: %w", path, err)
		}
		return "", fmt.Errorf("%s is empty", path)
	}
	return strings.TrimSpace(scanner.Text()), nil
}
