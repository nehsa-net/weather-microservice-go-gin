//go:build integration

// Package integration_test exercises the seams: real router, real HTTP client,
// real TCP listener, real JSON decoding — with only OpenWeatherMap replaced,
// and replaced by a real server rather than a stub function.
//
// The build tag keeps this tier out of the default `go test ./...`, so the
// fast suite stays fast enough that people actually run it.
package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/nehsa-net/weather-microservice-go-gin/internal/httpapi"
	"github.com/nehsa-net/weather-microservice-go-gin/internal/weather"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

const orlandoPayload = `{
	"name": "Orlando",
	"weather": [{"main": "Rain", "description": "light rain"}],
	"main": {"temp": 75.20, "humidity": 88},
	"dt": 1723400500,
	"cod": 200
}`

// stubUpstream stands in for OpenWeatherMap.
//
// Why not call the real API? Because a test that depends on somebody else's
// uptime, rate limit, and today's actual weather is not a test, it is a
// monitor — and it cannot assert a specific temperature, because the real one
// changes hourly.
func stubUpstream(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Query().Get("appid") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"cod":401,"message":"Invalid API key"}`)
			return
		}
		if r.URL.Query().Get("q") == "Atlantis" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"cod":"404","message":"city not found"}`)
			return
		}
		fmt.Fprint(w, orlandoPayload)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// newStack wires the real client, service and router together and serves them
// on a real port — the assembly main() performs, which is exactly what unit
// tests cannot check.
func newStack(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()

	upstream := stubUpstream(t)
	client := weather.NewClient(upstream.URL, apiKey, &http.Client{Timeout: 5 * time.Second})
	app := httptest.NewServer(httpapi.New(weather.NewService(client)))
	t.Cleanup(app.Close)

	return app
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

func TestEndpointsThroughTheWholeStack(t *testing.T) {
	app := newStack(t, "integration-key")

	tests := []struct {
		name   string
		path   string
		verify func(t *testing.T, body string)
	}{
		{
			name: "description returns one word",
			path: "/weather_description?city=Orlando",
			verify: func(t *testing.T, body string) {
				t.Helper()
				if body != "Rain" {
					t.Errorf("body = %q, want Rain", body)
				}
			},
		},
		{
			name: "temp returns two decimal places",
			path: "/weather_temp?city=Orlando",
			verify: func(t *testing.T, body string) {
				t.Helper()
				if body != "75.20" {
					t.Errorf("body = %q, want 75.20", body)
				}
			},
		},
		{
			name: "all returns the re-serialised payload",
			path: "/weather_all?city=Orlando",
			verify: func(t *testing.T, body string) {
				t.Helper()
				// The field names below are this service's public contract:
				// anything consuming /weather_all depends on them.
				var decoded weather.Response
				if err := json.Unmarshal([]byte(body), &decoded); err != nil {
					t.Fatalf("body is not valid JSON: %v\n%s", err, body)
				}
				if decoded.Name != "Orlando" {
					t.Errorf("name = %q, want Orlando", decoded.Name)
				}
				if decoded.Main.Temp != 75.20 {
					t.Errorf("temp = %v, want 75.20", decoded.Main.Temp)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := get(t, app.URL+tc.path)

			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", status, body)
			}
			tc.verify(t, body)
		})
	}
}

func TestUnknownCityIsA404ThroughTheStack(t *testing.T) {
	app := newStack(t, "integration-key")

	status, body := get(t, app.URL+"/weather_temp?city=Atlantis")

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if !strings.Contains(body, "no weather for that city") {
		t.Errorf("body = %q, want the flat public sentence", body)
	}
}

// A missing key is caught before the request goes out, so this is a 500 — the
// service is misconfigured, which is a different problem from the upstream
// being unavailable, and an operator needs to be able to tell them apart.
func TestMissingAPIKeyBecomes500(t *testing.T) {
	app := newStack(t, "") // no key reaches the upstream

	status, body := get(t, app.URL+"/weather_temp?city=Orlando")

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
	for _, forbidden := range []string{"appid", "API key", "401"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body leaked %q: %s", forbidden, body)
		}
	}
}

func TestUnreachableUpstreamBecomes502(t *testing.T) {
	// Nothing listens on port 1, so this is a real transport failure.
	client := weather.NewClient("http://127.0.0.1:1", "key", &http.Client{Timeout: 2 * time.Second})
	app := httptest.NewServer(httpapi.New(weather.NewService(client)))
	t.Cleanup(app.Close)

	status, body := get(t, app.URL+"/weather_temp?city=Orlando")

	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	// The wrapped error carries the full request URL including the key.
	// This asserts it never reaches the caller.
	for _, forbidden := range []string{"appid", "dial tcp", "127.0.0.1:1"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body leaked %q: %s", forbidden, body)
		}
	}
}

func TestProbesAnswerWithoutTheUpstream(t *testing.T) {
	// No stub upstream at all — the probes must not depend on it.
	client := weather.NewClient("http://127.0.0.1:1", "key", &http.Client{Timeout: time.Second})
	app := httptest.NewServer(httpapi.New(weather.NewService(client)))
	t.Cleanup(app.Close)

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			if status, body := get(t, app.URL+path); status != http.StatusOK {
				t.Errorf("status = %d, want 200 (body: %s)", status, body)
			}
		})
	}
}
