package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
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

func TestCheckStateWritableCreatesDirectoryAndCleansUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	c := &Config{StateDir: dir}

	if err := checkStateWritable(c); err != nil {
		t.Fatalf("checkStateWritable: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("state dir contains probe files after check: %v", entries)
	}
}

func TestCheckStateWritableRejectsUnusablePath(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create regular file: %v", err)
	}

	err := checkStateWritable(&Config{StateDir: filepath.Join(path, "state")})
	if err == nil {
		t.Fatal("checkStateWritable succeeded with a regular file as parent")
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
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (backoff should prevent a second attempt within the timeout)", got)
	}
}

func TestPollLoopContinuesOnNilItem(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pollLoop(ctx, c, &Config{PollHold: time.Millisecond})

	if got := calls.Load(); got < 2 {
		t.Errorf("calls = %d, want at least 2 (loop should keep polling on nil item)", got)
	}
}

func TestPollLoopSendsConfiguredTags(t *testing.T) {
	var mu sync.Mutex
	var got PollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	pollLoop(ctx, c, &Config{PollHold: time.Millisecond, Tags: []string{"role=ingest", "env=prod"}})

	want := []string{"role=ingest", "env=prod"}
	mu.Lock()
	defer mu.Unlock()
	if len(got.Tags) != len(want) || got.Tags[0] != want[0] || got.Tags[1] != want[1] {
		t.Errorf("poll body Tags = %+v, want %+v", got.Tags, want)
	}
}

// fakeSleepingPlaklet writes a fake plaklet binary that blocks until killed,
// standing in for a long-running task.
func fakeSleepingPlaklet(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake plaklet is a shell script")
	}
	script := filepath.Join(t.TempDir(), "fake-plaklet")
	// exec, so the kill on context cancel hits sleep itself rather than
	// leaving an orphan holding the stdout pipe open.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake plaklet: %v", err)
	}
	return script
}

// pollOnceThenCount serves one work item on the first poll and 204 afterwards,
// returning a pointer to the poll counter.
func pollOnceThenCount(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var pollCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/edge/poll", func(w http.ResponseWriter, r *http.Request) {
		if pollCount.Add(1) == 1 {
			item := WorkItem{WorkId: uuid.New(), Op: "noop"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(item)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/edge/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &pollCount
}

func TestPollLoopPollsWhileTaskRuns(t *testing.T) {
	script := fakeSleepingPlaklet(t)
	srv, pollCount := pollOnceThenCount(t)

	c := NewClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		pollLoop(ctx, c, &Config{
			PollHold:    time.Millisecond,
			PlakletBin:  script,
			PkgDir:      t.TempDir(),
			MaxParallel: 2,
		})
		close(done)
	}()

	// With two slots, the loop must issue a second poll while the first task
	// is still sleeping.
	deadline := time.After(2 * time.Second)
	for pollCount.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("no second poll while a task was running: work is not dispatched in parallel")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pollLoop did not return after cancel; in-flight task was not reaped")
	}
}

// fakeControlPlane behaves like a v2 server: it always delivers a queued
// cancel, delivers queued work only to a poll advertising a free slot, and
// records the slots of every poll and the type of every reply.
type fakeControlPlane struct {
	work    chan WorkItem
	cancels chan WorkItem

	mu      sync.Mutex
	slots   []int
	replies []ReplyType
}

func newFakeControlPlane(t *testing.T) (*fakeControlPlane, *httptest.Server) {
	t.Helper()
	cp := &fakeControlPlane{work: make(chan WorkItem, 4), cancels: make(chan WorkItem, 4)}
	serve := func(w http.ResponseWriter, item WorkItem) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/edge/poll", func(w http.ResponseWriter, r *http.Request) {
		var req PollRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		cp.mu.Lock()
		cp.slots = append(cp.slots, req.Slots)
		cp.mu.Unlock()
		select {
		case item := <-cp.cancels:
			serve(w, item)
			return
		default:
		}
		if req.Slots > 0 {
			select {
			case item := <-cp.work:
				serve(w, item)
				return
			default:
			}
		}
		time.Sleep(5 * time.Millisecond) // stands in for the long-poll hold
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/edge/", func(w http.ResponseWriter, r *http.Request) {
		var rep Reply
		_ = json.NewDecoder(r.Body).Decode(&rep)
		cp.mu.Lock()
		cp.replies = append(cp.replies, rep.Type)
		cp.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return cp, srv
}

func (cp *fakeControlPlane) lastSlots() int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.slots) == 0 {
		return -1
	}
	return cp.slots[len(cp.slots)-1]
}

func (cp *fakeControlPlane) replied(typ ReplyType) bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return slices.Contains(cp.replies, typ)
}

// startPollLoop runs pollLoop in the background; the returned func stops it
// and fails the test if in-flight work is not reaped.
func startPollLoop(t *testing.T, srv *httptest.Server, cfg *Config) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pollLoop(ctx, NewClient(srv.URL), cfg)
		close(done)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("pollLoop did not return after cancel; in-flight work was not reaped")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPollLoopReportsFreeSlots(t *testing.T) {
	cp, srv := newFakeControlPlane(t)
	cp.work <- WorkItem{WorkId: uuid.New(), Op: "noop"}

	// MaxParallel unset clamps to 1.
	stop := startPollLoop(t, srv, &Config{
		PollHold:   time.Millisecond,
		PlakletBin: fakeSleepingPlaklet(t),
		PkgDir:     t.TempDir(),
	})
	defer stop()

	waitFor(t, "a poll advertising 0 slots while the task runs", func() bool { return cp.lastSlots() == 0 })
	cp.mu.Lock()
	first := cp.slots[0]
	cp.mu.Unlock()
	if first != 1 {
		t.Errorf("first poll slots = %d, want 1 (MaxParallel below 1 is treated as 1)", first)
	}
}

func TestPollLoopCancelsRunningWork(t *testing.T) {
	cp, srv := newFakeControlPlane(t)
	id := uuid.New()
	cp.work <- WorkItem{WorkId: id, Op: "noop"}

	stop := startPollLoop(t, srv, &Config{
		PollHold:   time.Millisecond,
		PlakletBin: fakeSleepingPlaklet(t), // sleeps 30s unless killed
		PkgDir:     t.TempDir(),
	})
	defer stop()

	waitFor(t, "the work to take the slot", func() bool { return cp.lastSlots() == 0 })
	cp.cancels <- WorkItem{WorkId: id, Op: "cancel"}
	// The slot comes back only once runWork has returned, i.e. plaklet was killed.
	waitFor(t, "the slot to be freed by the cancel", func() bool { return cp.lastSlots() == 1 })

	if cp.replied(ReplyFailure) || cp.replied(ReplySuccess) {
		t.Error("a canceled work must not send a terminal reply")
	}
}

func TestPollLoopIgnoresCancelForUnknownWork(t *testing.T) {
	cp, srv := newFakeControlPlane(t)
	cp.cancels <- WorkItem{WorkId: uuid.New(), Op: "cancel"}

	stop := startPollLoop(t, srv, &Config{PollHold: time.Millisecond, MaxParallel: 2})
	defer stop()

	waitFor(t, "polling to continue after the cancel", func() bool {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return len(cp.slots) >= 3
	})
	if got := cp.lastSlots(); got != 2 {
		t.Errorf("slots = %d, want 2 (an unknown cancel must not take a slot)", got)
	}
}

func TestPollLoopDispatchesWorkAndReplies(t *testing.T) {
	var pollCount atomic.Int32
	var mu sync.Mutex
	var gotReply Reply

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/edge/poll", func(w http.ResponseWriter, r *http.Request) {
		if pollCount.Add(1) == 1 {
			item := WorkItem{WorkId: uuid.New(), Op: "noop"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(item)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/edge/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&gotReply)
			mu.Unlock()
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

	mu.Lock()
	defer mu.Unlock()
	if gotReply.Type != ReplyFailure {
		t.Errorf("gotReply.Type = %q, want %q", gotReply.Type, ReplyFailure)
	}
}
