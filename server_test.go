package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// dialServer waits for the supervision server to accept connections on addr,
// so tests don't race the goroutine that calls ListenAndServe.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestServerHealthAndReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:0"
	// Bind an ephemeral port ourselves so we know the address up front.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = ln.Addr().String()
	ln.Close()

	hlth, shutdown := startServer(ctx, serverConfig{Addr: addr, Metrics: true})
	defer func() {
		sc, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = shutdown(sc)
	}()
	waitListening(t, addr)

	base := "http://" + addr

	// /health is live immediately, regardless of readiness.
	if code, body := get(t, base+"/health"); code != http.StatusOK {
		t.Errorf("/health before ready: got %d, body %q", code, body)
	}

	// /ready is 503 until we flip readiness.
	if code, _ := get(t, base+"/ready"); code != http.StatusServiceUnavailable {
		t.Errorf("/ready before ready: got %d, want 503", code)
	}

	hlth.setReady(true)

	code, body := get(t, base+"/ready")
	if code != http.StatusOK {
		t.Fatalf("/ready after ready: got %d, body %q", code, body)
	}
	var hr healthResponse
	if err := json.Unmarshal([]byte(body), &hr); err != nil {
		t.Fatalf("decode /ready body %q: %v", body, err)
	}
	if hr.Status != "ok" {
		t.Errorf("status = %q, want ok", hr.Status)
	}
	if hr.Version != Version {
		t.Errorf("version = %q, want %q", hr.Version, Version)
	}
	if hr.UptimeSeconds < 0 {
		t.Errorf("uptime = %v, want >= 0", hr.UptimeSeconds)
	}
}

func TestServerMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	_, shutdown := startServer(ctx, serverConfig{Addr: addr, Metrics: true})
	defer func() {
		sc, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = shutdown(sc)
	}()
	waitListening(t, addr)

	code, body := get(t, "http://"+addr+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("/metrics: got %d", code)
	}
	// node_exporter always exports a scrape-success gauge per collector; the Go
	// runtime collector exports go_goroutines. Their presence proves both the
	// node collectors and the standard collectors are wired in.
	for _, want := range []string{"node_scrape_collector_success", "go_goroutines"} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

func TestServerDisabled(t *testing.T) {
	ctx := context.Background()
	hlth, shutdown := startServer(ctx, serverConfig{Addr: "", Metrics: true})
	if hlth == nil {
		t.Fatal("expected non-nil health handle even when disabled")
	}
	// Shutdown must be a safe no-op.
	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown of disabled server: %v", err)
	}
	// setReady must not panic on the returned handle.
	hlth.setReady(true)
}
