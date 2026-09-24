package metrics

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
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
	if err := reg.Register(CheckerResultCounter); err != nil {
		klog.ErrorS(err, "Failed to register checker result counter")
		return nil, err
	}
	if err := reg.Register(PodHealthResultCounter); err != nil {
		klog.ErrorS(err, "Failed to register CoreDNS pod result counter")
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
		errCh <- m.server.ListenAndServe()
	}()
	klog.InfoS("Started Prometheus metrics server",
		"address", addr)
	select {
	case <-ctx.Done():
		// Context canceled, initiate graceful shutdown.
		shutdownErr := m.server.Shutdown(ctx)
		if shutdownErr != nil {
			return shutdownErr
		}
		klog.InfoS("Shut down metrics server due to context cancel")
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
