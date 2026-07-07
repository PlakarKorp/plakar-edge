package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// enrollRetryDelayForTest shortens the enroll backoff for the duration of a test
// and returns a restore func.
func enrollRetryDelayForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	orig := enrollRetryDelay
	enrollRetryDelay = d
	return func() { enrollRetryDelay = orig }
}

// The client surfaces a 4xx/5xx as an *HTTPError carrying the status, so the
// enroll loop can tell a definitive rejection from a transient failure.
func TestClientHTTPErrorCarriesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).Enroll(context.Background(), EnrollRequest{})
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("want *HTTPError, got %T (%v)", err, err)
	}
	if he.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", he.Status)
	}
}

// enroll retries on transient failures (here: 503) and succeeds once the control
// plane recovers, without fatal-exiting.
func TestEnrollRetriesThenSucceeds(t *testing.T) {
	origDelay := enrollRetryDelayForTest(t, 10*time.Millisecond)
	defer origDelay()

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // transient
			return
		}
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			EdgeId:    uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"),
			Token:     "tok",
			Supported: true,
		})
	}))
	defer srv.Close()

	cfg := &Config{APIURL: srv.URL, StateDir: t.TempDir()}
	st := enroll(context.Background(), NewClient(srv.URL), cfg, "key", "edge", "host")
	if st == nil {
		t.Fatal("enroll returned nil (context not canceled, should have succeeded)")
	}
	if st.Token != "tok" {
		t.Errorf("token = %q, want tok", st.Token)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (two transient failures then success)", got)
	}
}

// enroll returns nil (not fatal, not hang) when the context is canceled while it
// is still retrying a transiently-failing control plane.
func TestEnrollReturnsNilOnContextCancel(t *testing.T) {
	origDelay := enrollRetryDelayForTest(t, 50*time.Millisecond)
	defer origDelay()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // never recovers
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	cfg := &Config{APIURL: srv.URL, StateDir: t.TempDir()}
	if st := enroll(ctx, NewClient(srv.URL), cfg, "key", "edge", "host"); st != nil {
		t.Errorf("enroll = %+v, want nil on context cancel", st)
	}
}
