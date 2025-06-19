package metrics

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	healthyStatus   = "healthy"
	unhealthyStatus = "unhealthy"
	unknownStatus   = "unknown"

	// error_code is required although healthy and unknown checkers do not use it.
	// We set a default value for healthy and unknown result.
	healthyCode = healthyStatus
	unknownCode = unknownStatus
)

// Server holds Prometheus collectors and exposes them via HTTP.
type Server struct {
	registry *prometheus.Registry
	port     int
	server   *http.Server
}

// NewServer creates a new Metrics instance with a custom registry and listen address.
func NewServer(port int) (*Server, error) {
	reg := prometheus.NewRegistry()
	if err := reg.Register(checkerResultCounter); err != nil {
		log.Printf("Failed to register checker counter: %v.", err)
		return nil, err
	}
	return &Server{
		registry: reg,
		port:     port,
	}, nil
}

// Run starts the HTTP server to expose Prometheus metrics.
func (m *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))
	addr := fmt.Sprintf("0.0.0.0:%d", m.port)
	m.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Starting Prometheus metrics server at %s/metrics.", addr)
		errCh <- m.server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		// Context canceled, initiate graceful shutdown.
		log.Println("Shutting down metrics server due to context cancel...")
		shutdownErr := m.server.Shutdown(ctx)
		if shutdownErr != nil {
			return shutdownErr
		}
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
