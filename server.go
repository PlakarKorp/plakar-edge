package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/node_exporter/collector"
)

// health tracks the daemon's liveness/readiness for the supervision endpoint.
// The process is "live" as soon as the HTTP server is up; it becomes "ready"
// only once it has a valid identity and is polling the control plane. A
// supervisor can restart the edge on a failed liveness probe and hold traffic
// (or alert) on a not-ready readiness probe.
type health struct {
	ready     atomic.Bool
	startedAt time.Time
}

// healthResponse is the JSON body returned by /health and /ready.
type healthResponse struct {
	Status        string  `json:"status"` // "ok" or "not_ready"
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

// serverConfig configures the supervision/metrics HTTP server.
type serverConfig struct {
	// Addr is the listen address (host:port). Empty disables the server.
	Addr string
	// Metrics enables the node-exporter /metrics endpoint.
	Metrics bool
}

// startServer brings up the supervision + metrics HTTP server on cfg.Addr and
// returns the health handle (so the caller can flip readiness) and a shutdown
// func to call on exit. If cfg.Addr is empty the server is disabled and both
// returned values are safe no-ops.
func startServer(ctx context.Context, cfg serverConfig) (*health, func(context.Context) error) {
	h := &health{startedAt: time.Now()}
	if cfg.Addr == "" {
		return h, func(context.Context) error { return nil }
	}

	mux := http.NewServeMux()

	// /health is the liveness probe: it answers 200 as long as the process is
	// serving. Use it to decide whether to restart the daemon.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		h.writeJSON(w, true)
	})

	// /ready is the readiness probe: 200 only once the edge is enrolled and
	// polling, 503 otherwise. Use it to gate whether the edge is expected to
	// pick up work.
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		h.writeJSON(w, h.ready.Load())
	})

	if cfg.Metrics {
		reg, err := newMetricsRegistry()
		if err != nil {
			// A failed collector build shouldn't take down the daemon: log it
			// and serve everything but /metrics.
			log.Printf("metrics disabled: %v", err)
		} else {
			mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		}
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("supervision server listening on %s (/health, /ready%s)", cfg.Addr, metricsSuffix(cfg.Metrics))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("supervision server error: %v", err)
		}
	}()

	shutdown := func(ctx context.Context) error {
		return srv.Shutdown(ctx)
	}
	return h, shutdown
}

func metricsSuffix(metrics bool) string {
	if metrics {
		return ", /metrics"
	}
	return ""
}

// setReady marks the edge ready (or not) for the /ready probe.
func (h *health) setReady(ready bool) { h.ready.Store(ready) }

func (h *health) writeJSON(w http.ResponseWriter, ok bool) {
	status := "ok"
	code := http.StatusOK
	if !ok {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}
	body, _ := json.Marshal(healthResponse{
		Status:        status,
		Version:       Version,
		UptimeSeconds: time.Since(h.startedAt).Seconds(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// kingpinOnce ensures the node_exporter collector flags (declared as package
// level kingpin.Flag(...) values) are resolved exactly once. The collectors
// dereference those flag pointers at scrape time, so their defaults must be
// applied before any collector is built — otherwise they panic on a nil flag.
// We parse an empty argument list so only the built-in defaults take effect;
// the edge is not a CLI for node_exporter and exposes no tuning flags of its
// own for it.
var kingpinOnce sync.Once

func applyNodeExporterDefaults() {
	kingpinOnce.Do(func() {
		// Parsing with no args resolves every registered flag to its default.
		// Errors here would mean a malformed default in the upstream library;
		// there is nothing the operator can do, so we ignore and let a later
		// scrape surface any real problem.
		_, _ = kingpin.CommandLine.Parse([]string{})
	})
}

// newMetricsRegistry builds a Prometheus registry exposing node-exporter host
// metrics plus the standard Go runtime and process collectors, so /metrics is a
// drop-in scrape target for a Prometheus-based supervisor.
func newMetricsRegistry() (*prometheus.Registry, error) {
	applyNodeExporterDefaults()

	// node_exporter logs collector errors through slog; route them to the same
	// place as the rest of the daemon at warn level so a broken collector is
	// visible without drowning the log.
	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelWarn}))

	nodeCollector, err := collector.NewNodeCollector(logger)
	if err != nil {
		return nil, err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		nodeCollector,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg, nil
}
