package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestConfigDerivedPaths(t *testing.T) {
	c := &Config{PkgDir: "/var/lib/plakar-edge/pkgs"}
	if got, want := c.plakletPkgDir(), filepath.Join(c.PkgDir, "integrations"); got != want {
		t.Errorf("plakletPkgDir() = %q, want %q", got, want)
	}
	if got, want := c.plakletCacheDir(), filepath.Join(c.PkgDir, "cache"); got != want {
		t.Errorf("plakletCacheDir() = %q, want %q", got, want)
	}
}

func TestStatePath(t *testing.T) {
	c := &Config{StateDir: "/tmp/edge-state"}
	want := filepath.Join("/tmp/edge-state", "edge.json")
	if got := c.statePath(); got != want {
		t.Errorf("statePath() = %q, want %q", got, want)
	}
}

func TestSaveAndLoadStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := &Config{StateDir: dir}
	st := &state{EdgeId: "edge-1", Token: "tok-1"}

	if err := saveState(c, st); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	got, err := loadState(c)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got.EdgeId != st.EdgeId || got.Token != st.Token {
		t.Errorf("loadState = %+v, want %+v", got, st)
	}
}

func TestSaveStateCreatesStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	c := &Config{StateDir: dir}
	if err := saveState(c, &state{EdgeId: "x", Token: "y"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("state dir was not created: %v", err)
	}
}

func TestSaveStateFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file permissions not applicable on windows")
	}
	dir := t.TempDir()
	c := &Config{StateDir: dir}
	if err := saveState(c, &state{EdgeId: "x", Token: "y"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	info, err := os.Stat(c.statePath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file perm = %o, want %o", perm, 0o600)
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	c := &Config{StateDir: dir}
	_, err := loadState(c)
	if err == nil {
		t.Fatal("expected error for missing state file, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist error, got: %v", err)
	}
}

func TestLoadStateCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	c := &Config{StateDir: dir}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(c.statePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadState(c)
	if err == nil {
		t.Fatal("expected error for corrupt state file, got nil")
	}
}

func TestPollLoopStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		pollLoop(ctx, c, &Config{PollHold: time.Millisecond})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollLoop did not return after context cancellation")
	}
}

func TestPollLoopBacksOffOnError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("fail"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	pollLoop(ctx, c, &Config{PollHold: time.Millisecond})

	// With a 5s backoff and a 200ms context timeout, the loop should only get
	// through its very first poll attempt before the context expires.
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (backoff should prevent a second attempt within the timeout)", calls)
	}
}

func TestPollLoopContinuesOnNilItem(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pollLoop(ctx, c, &Config{PollHold: time.Millisecond})

	if calls < 2 {
		t.Errorf("calls = %d, want at least 2 (loop should keep polling on nil item)", calls)
	}
}

func TestPollLoopDispatchesWorkAndReplies(t *testing.T) {
	var pollCount int32
	var gotReply Reply

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/edge/poll", func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		if pollCount == 1 {
			item := WorkItem{WorkId: uuid.New(), Op: "noop"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(item)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/edge/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&gotReply)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// PlakletBin points at a nonexistent binary, so runWork will fail fast and
	// report a failure reply — enough to prove pollLoop dispatches the item.
	pollLoop(ctx, c, &Config{PollHold: time.Millisecond, PlakletBin: "/nonexistent/plaklet-binary"})

	if gotReply.Type != ReplyFailure {
		t.Errorf("gotReply.Type = %q, want %q", gotReply.Type, ReplyFailure)
	}
}
