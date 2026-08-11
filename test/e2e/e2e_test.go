//go:build e2e

// Package e2e_test builds the real binary, runs it as a separate process, and
// drives it only over HTTP.
//
// This tier answers "does the artifact we ship actually work", including the
// parts no in-process test can reach: environment parsing, the wiring in
// main(), the listen address, and shutdown on SIGTERM.
package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const payload = `{
	"name": "Orlando",
	"weather": [{"main": "Rain", "description": "light rain"}],
	"main": {"temp": 75.20, "humidity": 88},
	"dt": 1723400500,
	"cod": 200
}`

type service struct {
	baseURL string
	cmd     *exec.Cmd
	logs    *strings.Builder
}

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// buildBinary compiles the service the way CI does, rather than using `go run`
// — which differs in signal handling and exit codes, and those are two of the
// things this tier exists to check.
func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "weather-microservice")

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	cmd.Dir = repoRoot(t)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	return binary
}

// freePort asks the kernel for an unused port. Hardcoding one makes the suite
// fail when it runs twice at once or on a busy machine.
func freePort(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	return listener.Addr().(*net.TCPAddr).Port
}

func startService(t *testing.T, binary string, env map[string]string) *service {
	t.Helper()

	port := freePort(t)
	logs := &strings.Builder{}

	// context.Background(), not t.Context(): the test context is cancelled when
	// the test ends, which would kill the process before Cleanup can stop it
	// gracefully — and graceful shutdown is one of the things under test.
	cmd := exec.CommandContext(context.Background(), binary)
	cmd.Env = append(os.Environ(), fmt.Sprintf("ADDR=127.0.0.1:%d", port))
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Stdout = logs
	cmd.Stderr = logs

	// The binary falls back to a key file in its working directory, so run it
	// somewhere empty to prove the environment variable is what is being used.
	cmd.Dir = t.TempDir()

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the service: %v", err)
	}

	svc := &service{baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd, logs: logs}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
		// Dump the log only on failure. Printing it always buries the one run
		// you actually need to read.
		if t.Failed() {
			t.Logf("service output:\n%s", logs.String())
		}
	})

	waitForHTTP(t, svc.baseURL+"/health", 15*time.Second, logs)
	return svc
}

// waitForHTTP polls instead of sleeping. A fixed sleep is either too short
// (flaky) or too long (slow), and wrong on another machine either way.
func waitForHTTP(t *testing.T, url string, timeout time.Duration, logs *strings.Builder) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	var lastErr error

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("%s never became ready within %s (last: %v)\nservice output:\n%s",
		url, timeout, lastErr, logs.String())
}

func stubUpstream(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("q") == "Atlantis" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"cod":"404","message":"city not found"}`)
			return
		}
		fmt.Fprint(w, payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestServiceServesEveryEndpoint(t *testing.T) {
	binary := buildBinary(t)
	upstream := stubUpstream(t)

	svc := startService(t, binary, map[string]string{
		"OPENWEATHER_API_KEY":  "e2e-key",
		"WEATHER_UPSTREAM_URL": upstream.URL,
	})

	tests := []struct {
		path string
		want string
	}{
		{path: "/weather_description?city=Orlando", want: "Rain"},
		{path: "/weather_temp?city=Orlando", want: "75.20"},
		{path: "/weather_all?city=Orlando", want: `"name":"Orlando"`},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			status, body := get(t, svc.baseURL+tc.path)

			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", status, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("body = %q, want it to contain %q", body, tc.want)
			}
		})
	}
}

func TestServiceReturns404ForUnknownCity(t *testing.T) {
	binary := buildBinary(t)
	upstream := stubUpstream(t)

	svc := startService(t, binary, map[string]string{
		"OPENWEATHER_API_KEY":  "e2e-key",
		"WEATHER_UPSTREAM_URL": upstream.URL,
	})

	if status, body := get(t, svc.baseURL+"/weather_temp?city=Atlantis"); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", status, body)
	}
}

// The Kubernetes probes, verified against the real running binary. This is the
// tier that would have caught them 404-ing.
func TestKubernetesProbesAnswer(t *testing.T) {
	binary := buildBinary(t)

	svc := startService(t, binary, map[string]string{
		"OPENWEATHER_API_KEY":  "e2e-key",
		"WEATHER_UPSTREAM_URL": "http://127.0.0.1:1", // deliberately unreachable
	})

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			// The upstream is down and the probes must still answer, or an
			// OpenWeatherMap blip takes every pod out of rotation.
			if status, body := get(t, svc.baseURL+path); status != http.StatusOK {
				t.Errorf("status = %d, want 200 (body: %s)", status, body)
			}
		})
	}
}

// Configuration errors are a main() concern, so this is the only tier that can
// check them. A service that starts happily without its key and fails on the
// first request is strictly worse than one that refuses to start.
func TestServiceRefusesToStartWithoutAKey(t *testing.T) {
	binary := buildBinary(t)

	cmd := exec.CommandContext(t.Context(), binary)
	cmd.Env = append(os.Environ(), "ADDR=127.0.0.1:0", "OPENWEATHER_API_KEY=")
	cmd.Dir = t.TempDir() // empty, so the legacy key file is absent too

	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("the service started with no API key; it should exit non-zero")
	}
	if !strings.Contains(string(out), "OPENWEATHER_API_KEY") {
		t.Errorf("output did not explain the problem:\n%s", out)
	}
}

// Graceful shutdown is invisible to every other tier. If this breaks, rolling
// deploys drop in-flight requests and nobody notices until a customer does.
func TestServiceShutsDownGracefullyOnSIGTERM(t *testing.T) {
	binary := buildBinary(t)
	upstream := stubUpstream(t)

	svc := startService(t, binary, map[string]string{
		"OPENWEATHER_API_KEY":  "e2e-key",
		"WEATHER_UPSTREAM_URL": upstream.URL,
	})

	if err := svc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- svc.cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exited with %v, want a clean exit\n%s", err, svc.logs)
		}
		if !strings.Contains(svc.logs.String(), "stopped cleanly") {
			t.Errorf("no clean-stop log:\n%s", svc.logs)
		}
	case <-time.After(10 * time.Second):
		_ = svc.cmd.Process.Kill()
		t.Fatal("the service ignored SIGTERM for 10s")
	}
}
