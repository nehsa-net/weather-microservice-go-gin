// Package weather holds the domain types, the OpenWeatherMap client, and the
// service logic.
//
// It is split so each tier has an obvious target: model.go is pure and needs no
// setup, client.go is the only file that touches the network, and service.go
// orchestrates over an interface.
package weather

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Callers compare with errors.Is rather than by string, so
// adding context with %w stays safe.
var (
	ErrInvalidCity  = errors.New("weather: city must not be empty")
	ErrCityNotFound = errors.New("weather: city not found")
	ErrUpstream     = errors.New("weather: upstream request failed")
	ErrInvalidUnits = errors.New("weather: unknown units")
	ErrNoAPIKey     = errors.New("weather: no API key configured")
)

// Units is the measurement system OpenWeatherMap should use.
type Units string

const (
	Imperial Units = "imperial" // °F
	Metric   Units = "metric"   // °C
	Standard Units = "standard" // K
)

// DefaultCity is used when the caller supplies no city, preserving the
// behaviour the service has always had.
const DefaultCity = "Cape Canaveral, FL"

// ParseUnits maps a query-string value onto Units. An empty value is not an
// error: it means the caller did not care, so the default applies.
func ParseUnits(s string) (Units, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "imperial":
		return Imperial, nil
	case "metric":
		return Metric, nil
	case "standard":
		return Standard, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidUnits, s)
	}
}

// NormaliseCity trims and collapses whitespace so "  Cape   Canaveral " and
// "Cape Canaveral" are the same query.
func NormaliseCity(city string) (string, error) {
	fields := strings.Fields(city)
	if len(fields) == 0 {
		return "", ErrInvalidCity
	}
	return strings.Join(fields, " "), nil
}

// Response is the OpenWeatherMap 2.5 payload.
//
// It stays exported because /weather_all re-serialises it verbatim, and that
// is part of this service's public contract.
type Response struct {
	Coord struct {
		Lon float64 `json:"lon"`
		Lat float64 `json:"lat"`
	} `json:"coord"`
	Weather []struct {
		Id          int    `json:"id"`
		Main        string `json:"main"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
	} `json:"weather"`
	Base string `json:"base"`
	Main struct {
		Temp      float64 `json:"temp"`
		FeelsLike float64 `json:"feels_like"`
		TempMin   float64 `json:"temp_min"`
		TempMax   float64 `json:"temp_max"`
		Pressure  int     `json:"pressure"`
		Humidity  int     `json:"humidity"`
		SeaLevel  int     `json:"sea_level"`
		GrndLevel int     `json:"grnd_level"`
	} `json:"main"`
	Visibility int `json:"visibility"`
	Wind       struct {
		Speed float64 `json:"speed"`
		Deg   int     `json:"deg"`
	} `json:"wind"`
	Clouds struct {
		All int `json:"all"`
	} `json:"clouds"`
	Dt  int `json:"dt"`
	Sys struct {
		Type    int    `json:"type"`
		Id      int    `json:"id"`
		Country string `json:"country"`
		Sunrise int    `json:"sunrise"`
		Sunset  int    `json:"sunset"`
	} `json:"sys"`
	Timezone int    `json:"timezone"`
	Id       int    `json:"id"`
	Name     string `json:"name"`
	Cod      int    `json:"cod"`
}

// Description returns the one-word summary, or an error if the payload carried
// no weather entries.
//
// The original code indexed Weather[0] unchecked, which panics on any payload
// without one — including every OpenWeatherMap error body. That panic took the
// whole process down; this returns an error instead.
func (r Response) Description() (string, error) {
	if len(r.Weather) == 0 {
		return "", fmt.Errorf("%w: payload carried no weather entries", ErrUpstream)
	}
	return r.Weather[0].Main, nil
}

// Temperature formats the temperature to two decimal places, as the
// /weather_temp endpoint has always returned it.
func (r Response) Temperature() string {
	return fmt.Sprintf("%.2f", r.Main.Temp)
}
