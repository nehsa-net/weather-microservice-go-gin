// Package httpapi wires the HTTP surface. It knows about status codes and
// query strings; it knows nothing about how weather is fetched.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/nehsa-net/weather-microservice-go-gin/internal/weather"
)

// Service is the narrow interface the handlers need. Being an interface is
// what lets a router test drive every branch — including the 502 path —
// without a network or a real client.
type Service interface {
	JSON(ctx context.Context, city string, units weather.Units) (string, error)
	Temperature(ctx context.Context, city string, units weather.Units) (string, error)
	Description(ctx context.Context, city string, units weather.Units) (string, error)
}

// New builds the router.
//
// Returning *gin.Engine rather than starting a server is what lets tests call
// router.ServeHTTP against an httptest recorder — no port, no goroutine, no
// cleanup. The original defined every handler as a closure inside main(),
// where no test could reach them.
func New(svc Service) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())

	// The Kubernetes manifest has always probed these two paths. Until now
	// neither existed, so both probes 404'd — liveness failing that way
	// restarts the pod in a loop.
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/ready", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	router.GET("/weather_all", serve(svc.JSON))
	router.GET("/weather_temp", serve(svc.Temperature))
	router.GET("/weather_description", serve(svc.Description))

	return router
}

type handlerFunc func(ctx context.Context, city string, units weather.Units) (string, error)

// serve holds the parsing and error mapping the three endpoints share.
//
// The original repeated this block three times, so a fix to one endpoint's
// defaulting silently left the other two behind — which is how /weather_all
// ended up with a different default city from its siblings.
func serve(fn handlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		city := c.Query("city")
		if city == "" {
			city = weather.DefaultCity
		}

		units, err := weather.ParseUnits(c.Query("units"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "units must be imperial, metric or standard"})
			return
		}

		result, err := fn(c.Request.Context(), city, units)
		if err != nil {
			writeError(c, err)
			return
		}
		c.String(http.StatusOK, result)
	}
}

// writeError maps domain errors onto status codes in one place.
//
// Note what the caller never sees: the upstream detail. It goes to the log; the
// response gets a status code and a flat sentence. The original returned
// err.Error() straight to the client, which echoed the full request URL —
// including the API key — on any transport failure.
func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, weather.ErrInvalidCity):
		c.JSON(http.StatusBadRequest, gin.H{"error": "city is required"})
	case errors.Is(err, weather.ErrCityNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no weather for that city"})
	case errors.Is(err, weather.ErrNoAPIKey):
		_ = c.Error(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the service is misconfigured"})
	default:
		_ = c.Error(err) // recorded for the log, not rendered to the caller
		c.JSON(http.StatusBadGateway, gin.H{"error": "weather is unavailable right now"})
	}
}
