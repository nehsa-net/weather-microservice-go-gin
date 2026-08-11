package weather

import (
	"context"
	"encoding/json"
	"fmt"
)

// Provider is what Service needs from the outside world. Client satisfies it,
// and so does a small fake in a test file.
//
// Declaring the interface next to the consumer — rather than next to Client —
// is the Go convention, and it keeps the interface as narrow as the consumer
// actually requires.
type Provider interface {
	Fetch(ctx context.Context, city string, units Units) (Response, error)
}

// Service turns raw upstream responses into what each endpoint serves.
//
// The three original functions (getWeatherJson, getWeatherTemp,
// getWeatherWords) each called getWeatherv25 separately and each re-read the
// API key from disk. They are one type with one dependency now.
type Service struct {
	provider Provider
}

// NewService wires a Service over a Provider.
func NewService(p Provider) *Service {
	return &Service{provider: p}
}

// JSON returns the full upstream payload, re-serialised — what /weather_all
// has always returned.
func (s *Service) JSON(ctx context.Context, city string, units Units) (string, error) {
	response, err := s.provider.Fetch(ctx, city, units)
	if err != nil {
		return "", err
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		// The original logged this and returned the value anyway, so a
		// marshalling failure produced an empty 200. Now it is an error.
		return "", fmt.Errorf("%w: encoding response: %w", ErrUpstream, err)
	}
	return string(encoded), nil
}

// Temperature returns the temperature to two decimal places — /weather_temp.
func (s *Service) Temperature(ctx context.Context, city string, units Units) (string, error) {
	response, err := s.provider.Fetch(ctx, city, units)
	if err != nil {
		return "", err
	}
	return response.Temperature(), nil
}

// Description returns the one-word summary — /weather_description.
func (s *Service) Description(ctx context.Context, city string, units Units) (string, error) {
	response, err := s.provider.Fetch(ctx, city, units)
	if err != nil {
		return "", err
	}
	return response.Description()
}
