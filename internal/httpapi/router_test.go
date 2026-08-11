package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/nehsa-net/weather-microservice-go-gin/internal/httpapi"
	"github.com/nehsa-net/weather-microservice-go-gin/internal/weather"
)

func TestMain(m *testing.M) {
	// Silence gin's request log. Test output should be the test's, not the
	// framework's.
	gin.SetMode(gin.TestMode)
	m.Run()
}

// fakeService drives every status-code branch without a network, a client, or
// a real weather provider.
type fakeService struct {
	result string
	err    error

	calls     int
	lastCity  string
	lastUnits weather.Units
}

func (f *fakeService) record(city string, units weather.Units) (string, error) {
	f.calls++
	f.lastCity = city
	f.lastUnits = units
	return f.result, f.err
}

func (f *fakeService) JSON(_ context.Context, city string, u weather.Units) (string, error) {
	return f.record(city, u)
}

func (f *fakeService) Temperature(_ context.Context, city string, u weather.Units) (string, error) {
	return f.record(city, u)
}

func (f *fakeService) Description(_ context.Context, city string, u weather.Units) (string, error) {
	return f.record(city, u)
}

// do drives the router in-process: no port bound, nothing to clean up.
func do(t *testing.T, svc httpapi.Service, target string) *httptest.ResponseRecorder {
	t.Helper()

	router := httpapi.New(svc)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// The Kubernetes manifest has probed these two paths since it was written, and
// until this change neither existed — so liveness 404'd and restarted the pod
// in a loop. These two tests are the reason that cannot happen again.
func TestHealthAndReady(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{}
			rec := do(t, svc, path)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			// A probe must not depend on the upstream, or an OpenWeatherMap
			// blip takes every pod out of rotation.
			if svc.calls != 0 {
				t.Errorf("probe called the weather service %d times, want 0", svc.calls)
			}
		})
	}
}

func TestEndpointsReturnTheServiceResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		result string
	}{
		{name: "description", target: "/weather_description?city=Orlando", result: "Clouds"},
		{name: "temp", target: "/weather_temp?city=Orlando", result: "81.53"},
		{name: "all", target: "/weather_all?city=Orlando", result: `{"name":"Orlando"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, &fakeService{result: tc.result}, tc.target)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.result {
				t.Errorf("body = %q, want %q", got, tc.result)
			}
		})
	}
}

func TestStatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     string
		svcErr     error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "unknown city is 404",
			target:     "/weather_temp?city=Atlantis",
			svcErr:     weather.ErrCityNotFound,
			wantStatus: http.StatusNotFound,
			wantBody:   "no weather for that city",
		},
		{
			name:       "invalid city is 400",
			target:     "/weather_temp?city=+",
			svcErr:     weather.ErrInvalidCity,
			wantStatus: http.StatusBadRequest,
			wantBody:   "city is required",
		},
		{
			name:       "upstream failure is 502",
			target:     "/weather_temp?city=Orlando",
			svcErr:     weather.ErrUpstream,
			wantStatus: http.StatusBadGateway,
			wantBody:   "weather is unavailable right now",
		},
		{
			name:       "a missing key is 500, not 502",
			target:     "/weather_temp?city=Orlando",
			svcErr:     weather.ErrNoAPIKey,
			wantStatus: http.StatusInternalServerError,
			wantBody:   "the service is misconfigured",
		},
		{
			name:       "bad units is 400 before the service is called",
			target:     "/weather_temp?city=Orlando&units=kelvin",
			wantStatus: http.StatusBadRequest,
			wantBody:   "units must be imperial, metric or standard",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, &fakeService{result: "ignored", err: tc.svcErr}, tc.target)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body)
			}

			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding %q: %v", rec.Body, err)
			}
			if body.Error != tc.wantBody {
				t.Errorf("error = %q, want %q", body.Error, tc.wantBody)
			}
		})
	}
}

// The three endpoints used to duplicate this parsing, and drifted: /weather_all
// defaulted to a different city from its siblings. One shared helper means one
// test covers all three.
func TestQueryParsingIsConsistentAcrossEndpoints(t *testing.T) {
	t.Parallel()

	endpoints := []string{"/weather_description", "/weather_temp", "/weather_all"}

	tests := []struct {
		name      string
		query     string
		wantCity  string
		wantUnits weather.Units
	}{
		{name: "explicit city and units", query: "?city=Orlando&units=metric", wantCity: "Orlando", wantUnits: weather.Metric},
		{name: "missing city uses the default", query: "", wantCity: weather.DefaultCity, wantUnits: weather.Imperial},
		{name: "missing units defaults to imperial", query: "?city=Orlando", wantCity: "Orlando", wantUnits: weather.Imperial},
		{name: "url-encoded city is decoded", query: "?city=Cape%20Canaveral%2C%20FL", wantCity: "Cape Canaveral, FL", wantUnits: weather.Imperial},
		{name: "standard units", query: "?city=Orlando&units=standard", wantCity: "Orlando", wantUnits: weather.Standard},
	}

	for _, endpoint := range endpoints {
		for _, tc := range tests {
			t.Run(endpoint+" "+tc.name, func(t *testing.T) {
				t.Parallel()

				svc := &fakeService{result: "ok"}
				do(t, svc, endpoint+tc.query)

				if svc.lastCity != tc.wantCity {
					t.Errorf("service received city %q, want %q", svc.lastCity, tc.wantCity)
				}
				if svc.lastUnits != tc.wantUnits {
					t.Errorf("service received units %q, want %q", svc.lastUnits, tc.wantUnits)
				}
			})
		}
	}
}

// A regression test for a real information leak. The original handlers returned
// err.Error() straight to the caller, and the wrapped error carried the full
// request URL — including the API key — on any transport failure.
func TestErrorsDoNotLeakTheAPIKey(t *testing.T) {
	t.Parallel()

	leaky := errorWithDetail{
		"Get \"https://api.openweathermap.org/data/2.5/weather?appid=SECRETKEY123&q=Orlando\": dial tcp: refused",
	}

	for _, endpoint := range []string{"/weather_description", "/weather_temp", "/weather_all"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()

			rec := do(t, &fakeService{err: leaky}, endpoint+"?city=Orlando")

			body := rec.Body.String()
			for _, forbidden := range []string{"SECRETKEY123", "appid", "api.openweathermap.org", "dial tcp"} {
				if strings.Contains(body, forbidden) {
					t.Errorf("response leaked %q to the caller: %s", forbidden, body)
				}
			}
		})
	}
}

type errorWithDetail struct{ msg string }

func (e errorWithDetail) Error() string { return e.msg }
