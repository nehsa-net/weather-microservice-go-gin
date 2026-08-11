// Command weather-microservice serves current conditions from OpenWeatherMap.
//
// main() reads config, wires dependencies, and serves — nothing else.
// Everything worth testing lives in a package the tests can import, which is
// what makes the unit tier possible at all: you cannot call func main() from a
// test.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nehsa-net/weather-microservice-go-gin/internal/config"
	"github.com/nehsa-net/weather-microservice-go-gin/internal/httpapi"
	"github.com/nehsa-net/weather-microservice-go-gin/internal/weather"
)

func main() {
	if err := run(); err != nil {
		log.Printf("service exited: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	client := weather.NewClient(cfg.UpstreamURL, cfg.APIKey, nil)
	svc := weather.NewService(client)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(svc),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown. Without it, a rolling deploy kills the process
	// mid-request and drops whatever was in flight.
	idle := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		close(idle)
	}()

	log.Printf("listening on %s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}

	<-idle
	log.Print("stopped cleanly")
	return nil
}
