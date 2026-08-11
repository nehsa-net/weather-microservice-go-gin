package weather_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nehsa-net/weather-microservice-go-gin/internal/weather"
)

// The package is weather_test, not weather: an external test package can only
// reach exported identifiers, so the tests exercise the public contract rather
// than the implementation.

const validPayload = `{
	"name": "Cape Canaveral",
	"weather": [{"main": "Clouds", "description": "broken clouds"}],
	"main": {"temp": 81.53, "humidity": 74},
	"dt": 1723400000,
	"cod": 200
}`

// ---- model ------------------------------------------------------------------

func TestParseUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    weather.Units
		wantErr error
	}{
		{name: "empty defaults to imperial", input: "", want: weather.Imperial},
		{name: "imperial", input: "imperial", want: weather.Imperial},
		{name: "metric", input: "metric", want: weather.Metric},
		{name: "standard", input: "standard", want: weather.Standard},
		{name: "mixed case is accepted", input: "MeTrIc", want: weather.Metric},
		{name: "surrounding space is trimmed", input: "  metric  ", want: weather.Metric},
		{name: "kelvin is rejected", input: "kelvin", wantErr: weather.ErrInvalidUnits},
		{name: "the singular unit is rejected", input: "unit", wantErr: weather.ErrInvalidUnits},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := weather.ParseUnits(tc.input)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ParseUnits(%q) error = %v, want %v", tc.input, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("ParseUnits(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormaliseCity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "already clean", input: "Orlando", want: "Orlando"},
		{name: "trims", input: "  Orlando  ", want: "Orlando"},
		{name: "collapses inner whitespace", input: "Cape   Canaveral", want: "Cape Canaveral"},
		{name: "keeps the comma form", input: "Cape Canaveral, FL", want: "Cape Canaveral, FL"},
		{name: "empty is an error", input: "", wantErr: weather.ErrInvalidCity},
		{name: "whitespace only is an error", input: "  \t ", wantErr: weather.ErrInvalidCity},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := weather.NormaliseCity(tc.input)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NormaliseCity(%q) error = %v, want %v", tc.input, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("NormaliseCity(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// The regression test for the panic this refactor removed. The original
// indexed Weather[0] unchecked, so any payload without an entry — including
// every OpenWeatherMap error body — took the whole process down.
func TestResponseDescriptionOnEmptyWeather(t *testing.T) {
	t.Parallel()

	var empty weather.Response

	got, err := empty.Description()

	if !errors.Is(err, weather.ErrUpstream) {
		t.Fatalf("Description() error = %v, want ErrUpstream", err)
	}
	if got != "" {
		t.Errorf("Description() = %q, want empty", got)
	}
}

func TestResponseTemperatureFormatting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		temp float64
		want string
	}{
		{name: "two decimals are kept", temp: 81.53, want: "81.53"},
		{name: "a whole number is padded", temp: 81, want: "81.00"},
		{name: "rounds to two places", temp: 81.536, want: "81.54"},
		{name: "negatives keep their sign", temp: -3.5, want: "-3.50"},
		{name: "zero", temp: 0, want: "0.00"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var response weather.Response
			response.Main.Temp = tc.temp

			if got := response.Temperature(); got != tc.want {
				t.Errorf("Temperature() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---- client -----------------------------------------------------------------

func TestClientFetch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		city       string
		units      weather.Units
		wantErr    error
		wantTemp   float64
		wantQuery  string
		wantUnits  string
		skipServer bool
	}{
		{
			name:      "success",
			status:    http.StatusOK,
			body:      validPayload,
			city:      "Cape Canaveral",
			units:     weather.Imperial,
			wantTemp:  81.53,
			wantQuery: "Cape Canaveral",
			wantUnits: "imperial",
		},
		{
			name:      "the city is normalised before the request goes out",
			status:    http.StatusOK,
			body:      validPayload,
			city:      "  Cape   Canaveral ",
			units:     weather.Metric,
			wantTemp:  81.53,
			wantQuery: "Cape Canaveral",
			wantUnits: "metric",
		},
		{
			name:    "404 becomes ErrCityNotFound",
			status:  http.StatusNotFound,
			body:    `{"cod":"404","message":"city not found"}`,
			city:    "Atlantis",
			units:   weather.Imperial,
			wantErr: weather.ErrCityNotFound,
		},
		{
			name:    "401 becomes ErrUpstream",
			status:  http.StatusUnauthorized,
			body:    `{"cod":401,"message":"Invalid API key"}`,
			city:    "Orlando",
			units:   weather.Imperial,
			wantErr: weather.ErrUpstream,
		},
		{
			name:    "500 becomes ErrUpstream",
			status:  http.StatusInternalServerError,
			body:    `upstream exploded`,
			city:    "Orlando",
			units:   weather.Imperial,
			wantErr: weather.ErrUpstream,
		},
		{
			name:    "malformed json becomes ErrUpstream",
			status:  http.StatusOK,
			body:    `{"name": "Orlando", "main": {`,
			city:    "Orlando",
			units:   weather.Imperial,
			wantErr: weather.ErrUpstream,
		},
		{
			name:       "an empty city never reaches the network",
			city:       "   ",
			units:      weather.Imperial,
			wantErr:    weather.ErrInvalidCity,
			skipServer: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotQuery, gotUnits, gotPath, gotKey string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.Query().Get("q")
				gotUnits = r.URL.Query().Get("units")
				gotKey = r.URL.Query().Get("appid")
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			client := weather.NewClient(srv.URL, "test-key", srv.Client())

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			got, err := client.Fetch(ctx, tc.city, tc.units)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Fetch() error = %v, want %v", err, tc.wantErr)
				}
				if tc.skipServer && gotPath != "" {
					t.Error("Fetch() hit the network for an invalid city; it should fail first")
				}
				return
			}
			if err != nil {
				t.Fatalf("Fetch() unexpected error: %v", err)
			}

			if got.Main.Temp != tc.wantTemp {
				t.Errorf("Temp = %v, want %v", got.Main.Temp, tc.wantTemp)
			}

			// Assert on the request that was SENT, not only the response
			// parsed. A client that drops the units parameter passes every
			// response-shaped assertion above.
			if gotQuery != tc.wantQuery {
				t.Errorf("upstream received q=%q, want %q", gotQuery, tc.wantQuery)
			}
			if gotUnits != tc.wantUnits {
				t.Errorf("upstream received units=%q, want %q", gotUnits, tc.wantUnits)
			}
			if gotKey != "test-key" {
				t.Errorf("upstream received appid=%q, want test-key", gotKey)
			}
			if gotPath != "/data/2.5/weather" {
				t.Errorf("upstream path = %q, want /data/2.5/weather", gotPath)
			}
		})
	}
}

func TestClientFetchWithoutAPIKey(t *testing.T) {
	t.Parallel()

	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	t.Cleanup(srv.Close)

	client := weather.NewClient(srv.URL, "", srv.Client())

	_, err := client.Fetch(context.Background(), "Orlando", weather.Imperial)

	if !errors.Is(err, weather.ErrNoAPIKey) {
		t.Fatalf("Fetch() error = %v, want ErrNoAPIKey", err)
	}
	if reached {
		t.Error("Fetch() called the upstream with no key; it should fail first")
	}
}

type stubDoer struct{ err error }

func (s stubDoer) Do(*http.Request) (*http.Response, error) { return nil, s.err }

func TestClientFetchTransportError(t *testing.T) {
	t.Parallel()

	client := weather.NewClient("https://example.invalid", "key", stubDoer{
		err: errors.New("dial tcp: connection refused"),
	})

	_, err := client.Fetch(context.Background(), "Orlando", weather.Imperial)

	if !errors.Is(err, weather.ErrUpstream) {
		t.Fatalf("error = %v, want ErrUpstream", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error %q lost the underlying cause", err)
	}
}

func TestClientFetchHonoursContextDeadline(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() {
		close(blocked)
		srv.Close()
	})

	client := weather.NewClient(srv.URL, "key", srv.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Fetch(ctx, "Orlando", weather.Imperial)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v; the deadline was not honoured", elapsed)
	}
}

// ---- service ----------------------------------------------------------------

type fakeProvider struct {
	response weather.Response
	err      error

	calls     int
	lastCity  string
	lastUnits weather.Units
}

func (f *fakeProvider) Fetch(_ context.Context, city string, units weather.Units) (weather.Response, error) {
	f.calls++
	f.lastCity = city
	f.lastUnits = units
	return f.response, f.err
}

func sampleResponse() weather.Response {
	var r weather.Response
	r.Name = "Cape Canaveral"
	r.Main.Temp = 81.53
	r.Main.Humidity = 74
	r.Weather = append(r.Weather, struct {
		Id          int    `json:"id"`
		Main        string `json:"main"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
	}{Id: 803, Main: "Clouds", Description: "broken clouds", Icon: "04d"})
	return r
}

func TestServiceDescription(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{response: sampleResponse()}
	svc := weather.NewService(provider)

	got, err := svc.Description(context.Background(), "Orlando", weather.Metric)
	if err != nil {
		t.Fatalf("Description() unexpected error: %v", err)
	}

	if got != "Clouds" {
		t.Errorf("Description() = %q, want Clouds", got)
	}
	if provider.lastCity != "Orlando" {
		t.Errorf("provider received city %q, want Orlando", provider.lastCity)
	}
	if provider.lastUnits != weather.Metric {
		t.Errorf("provider received units %q, want metric", provider.lastUnits)
	}
	// One upstream call per request. The original fetched separately for each
	// piece of the response.
	if provider.calls != 1 {
		t.Errorf("provider called %d times, want 1", provider.calls)
	}
}

func TestServiceTemperature(t *testing.T) {
	t.Parallel()

	svc := weather.NewService(&fakeProvider{response: sampleResponse()})

	got, err := svc.Temperature(context.Background(), "Orlando", weather.Imperial)
	if err != nil {
		t.Fatalf("Temperature() unexpected error: %v", err)
	}
	if got != "81.53" {
		t.Errorf("Temperature() = %q, want 81.53", got)
	}
}

func TestServiceJSON(t *testing.T) {
	t.Parallel()

	svc := weather.NewService(&fakeProvider{response: sampleResponse()})

	got, err := svc.JSON(context.Background(), "Orlando", weather.Imperial)
	if err != nil {
		t.Fatalf("JSON() unexpected error: %v", err)
	}

	// /weather_all re-serialises the upstream payload, so these field names are
	// part of this service's public contract.
	for _, want := range []string{`"name":"Cape Canaveral"`, `"temp":81.53`, `"main":"Clouds"`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON() missing %s\ngot: %s", want, got)
		}
	}
}

func TestServicePropagatesProviderErrors(t *testing.T) {
	t.Parallel()

	for _, wantErr := range []error{
		weather.ErrCityNotFound,
		weather.ErrUpstream,
		weather.ErrInvalidCity,
		weather.ErrNoAPIKey,
	} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			t.Parallel()

			svc := weather.NewService(&fakeProvider{err: wantErr})

			if _, err := svc.Description(context.Background(), "Orlando", weather.Imperial); !errors.Is(err, wantErr) {
				t.Errorf("Description() error = %v, want %v", err, wantErr)
			}
			if _, err := svc.Temperature(context.Background(), "Orlando", weather.Imperial); !errors.Is(err, wantErr) {
				t.Errorf("Temperature() error = %v, want %v", err, wantErr)
			}
			if _, err := svc.JSON(context.Background(), "Orlando", weather.Imperial); !errors.Is(err, wantErr) {
				t.Errorf("JSON() error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestServiceDescriptionOnEmptyPayload(t *testing.T) {
	t.Parallel()

	// An upstream 200 carrying no weather entries. This is the exact input
	// that used to panic.
	svc := weather.NewService(&fakeProvider{response: weather.Response{}})

	_, err := svc.Description(context.Background(), "Orlando", weather.Imperial)

	if !errors.Is(err, weather.ErrUpstream) {
		t.Fatalf("Description() error = %v, want ErrUpstream", err)
	}
}
