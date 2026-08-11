package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Doer is the seam that makes this package testable.
//
// Depending on *http.Client directly would mean no test could run without real
// network access. Depending on the one method actually used lets a test pass a
// stub, while production passes *http.Client unchanged.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client talks to the OpenWeatherMap API.
//
// BaseURL is a field rather than the hardcoded constant it used to be, for one
// reason: a test can point it at an httptest.Server. That single change is what
// makes the integration and e2e tiers possible.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    Doer
}

// NewClient builds a Client. A nil Doer yields a real *http.Client with a
// timeout, so production callers stay terse.
//
// The original code had no timeout at all: a hung upstream held the request
// open indefinitely and eventually exhausted the connection pool.
func NewClient(baseURL, apiKey string, doer Doer) *Client {
	if doer == nil {
		doer = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    doer,
	}
}

// Fetch returns the current conditions for a city in the requested units.
func (c *Client) Fetch(ctx context.Context, city string, units Units) (Response, error) {
	city, err := NormaliseCity(city)
	if err != nil {
		return Response{}, err
	}
	if c.APIKey == "" {
		return Response{}, ErrNoAPIKey
	}

	endpoint, err := url.Parse(c.BaseURL + "/data/2.5/weather")
	if err != nil {
		return Response{}, fmt.Errorf("%w: bad base url: %w", ErrUpstream, err)
	}
	q := endpoint.Query()
	q.Set("q", city)
	q.Set("appid", c.APIKey)
	q.Set("units", string(units))
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	// The error from closing a body is not actionable, but it must be read:
	// an unclosed body leaks a connection per request.
	defer func() { _ = resp.Body.Close() }()

	// Cap the read. An upstream that streams forever should fail the request,
	// not exhaust the machine.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Response{}, fmt.Errorf("%w: reading body: %w", ErrUpstream, err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Response{}, fmt.Errorf("%w: %q", ErrCityNotFound, city)
	case resp.StatusCode != http.StatusOK:
		return Response{}, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	var payload Response
	if err := json.Unmarshal(body, &payload); err != nil {
		return Response{}, fmt.Errorf("%w: decoding body: %w", ErrUpstream, err)
	}
	return payload, nil
}
