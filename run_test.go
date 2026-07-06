package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestMain lets this test binary double as a fake "plaklet" subprocess, the
// standard Go re-exec trick for testing code that shells out. When invoked
// with PLAKAR_EDGE_FAKE_PLAKLET=1 it skips the normal test run and instead
// behaves like plaklet: it reads an ExecPayload from stdin and writes
// ExecReply lines to stdout, scripted by PLAKAR_EDGE_FAKE_PLAKLET_SCRIPT.
func TestMain(m *testing.M) {
	if os.Getenv("PLAKAR_EDGE_FAKE_PLAKLET") == "1" {
		fakePlakletMain()
		return
	}
	os.Exit(m.Run())
}

// fakePlakletMain emulates plaklet's stdin/stdout contract for tests.
// Scripts:
//   - "success": consumes stdin, emits one ReplySuccess.
//   - "failure": consumes stdin, emits one ReplyFailure.
//   - "multi": emits ReplyInfo then ReplySuccess.
//   - "silent": exits 0 without emitting anything (no terminal reply).
//   - "crash": exits nonzero without emitting anything.
func fakePlakletMain() {
	var payload ExecPayload
	_ = json.NewDecoder(os.Stdin).Decode(&payload)

	enc := json.NewEncoder(os.Stdout)
	switch os.Getenv("PLAKAR_EDGE_FAKE_PLAKLET_SCRIPT") {
	case "success":
		_ = enc.Encode(ExecReply{Type: ReplySuccess, Message: "ok"})
	case "failure":
		_ = enc.Encode(ExecReply{Type: ReplyFailure, Message: "boom"})
	case "multi":
		_ = enc.Encode(ExecReply{Type: ReplyInfo, Message: "working"})
		_ = enc.Encode(ExecReply{Type: ReplySuccess, Message: "done"})
	case "silent":
		// no output, clean exit
	case "crash":
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "unknown script")
		os.Exit(2)
	}
}

// fakePlakletBin returns the path to this same test binary along with the
// env vars needed to make it behave as the given fake-plaklet script.
func fakePlakletBin(t *testing.T, script string) (bin string, env []string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	env = append(os.Environ(),
		"PLAKAR_EDGE_FAKE_PLAKLET=1",
		"PLAKAR_EDGE_FAKE_PLAKLET_SCRIPT="+script,
	)
	return self, env
}

// runWithFakePlaklet runs spawnPlaklet with cfg.PlakletBin wired to this test
// binary acting as the given fake-plaklet script, via a thin wrapper since
// exec.CommandContext doesn't take an env override directly in spawnPlaklet.
// We achieve the override by launching a tiny shell wrapper script instead
// when PATH-based env injection isn't available; simplest is to write a
// wrapper script that re-execs the test binary with the right environment.
func writePlakletWrapper(t *testing.T, script string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	wrapperPath := dir + "/plaklet"
	contents := "#!/bin/sh\n" +
		"export PLAKAR_EDGE_FAKE_PLAKLET=1\n" +
		"export PLAKAR_EDGE_FAKE_PLAKLET_SCRIPT=" + script + "\n" +
		"exec " + shellQuote(self) + " \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(contents), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	return wrapperPath
}

func shellQuote(s string) string {
	return "'" + s + "'"
}

func testConfig(t *testing.T, plakletBin string) *Config {
	t.Helper()
	pkgDir := t.TempDir()
	return &Config{
		PlakletBin: plakletBin,
		PkgDir:     pkgDir,
	}
}

func TestSpawnPlakletSuccess(t *testing.T) {
	bin := writePlakletWrapper(t, "success")
	cfg := testConfig(t, bin)

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	err := spawnPlaklet(context.Background(), clt, cfg, item)
	if err != nil {
		t.Fatalf("spawnPlaklet: %v", err)
	}
	if len(replies) != 1 || replies[0].Type != ReplySuccess {
		t.Fatalf("replies = %+v, want one ReplySuccess", replies)
	}
}

func TestSpawnPlakletFailureReplyIsForwardedNotErrored(t *testing.T) {
	bin := writePlakletWrapper(t, "failure")
	cfg := testConfig(t, bin)

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	// plaklet itself reporting ReplyFailure is a terminal reply, so
	// spawnPlaklet should return nil (it forwarded the failure already).
	err := spawnPlaklet(context.Background(), clt, cfg, item)
	if err != nil {
		t.Fatalf("spawnPlaklet: %v", err)
	}
	if len(replies) != 1 || replies[0].Type != ReplyFailure || replies[0].Message != "boom" {
		t.Fatalf("replies = %+v, want one ReplyFailure(boom)", replies)
	}
}

func TestSpawnPlakletMultipleReplies(t *testing.T) {
	bin := writePlakletWrapper(t, "multi")
	cfg := testConfig(t, bin)

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	if err := spawnPlaklet(context.Background(), clt, cfg, item); err != nil {
		t.Fatalf("spawnPlaklet: %v", err)
	}
	if len(replies) != 2 {
		t.Fatalf("replies = %+v, want 2", replies)
	}
	if replies[0].Type != ReplyInfo || replies[1].Type != ReplySuccess {
		t.Fatalf("replies = %+v, want [info, success]", replies)
	}
}

func TestSpawnPlakletSilentExitSynthesizesFailure(t *testing.T) {
	bin := writePlakletWrapper(t, "silent")
	cfg := testConfig(t, bin)

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	err := spawnPlaklet(context.Background(), clt, cfg, item)
	if err == nil {
		t.Fatal("expected error when plaklet exits without a terminal reply")
	}
}

func TestSpawnPlakletCrashSynthesizesFailure(t *testing.T) {
	bin := writePlakletWrapper(t, "crash")
	cfg := testConfig(t, bin)

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	err := spawnPlaklet(context.Background(), clt, cfg, item)
	if err == nil {
		t.Fatal("expected error when plaklet crashes without a terminal reply")
	}
	if len(replies) != 0 {
		t.Fatalf("replies = %+v, want none (nothing was emitted before the crash)", replies)
	}
}

func TestSpawnPlakletMissingBinary(t *testing.T) {
	cfg := testConfig(t, "/nonexistent/plaklet-binary-xyz")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	err := spawnPlaklet(context.Background(), clt, cfg, item)
	if err == nil {
		t.Fatal("expected error for missing plaklet binary")
	}
}

func TestSpawnPlakletSendsCorrectArgs(t *testing.T) {
	cfg := &Config{
		PlakletBin: "/bin/echo", // not JSON output, but we only check args wiring via PkgDir below
		PkgDir:     "/base/pkg",
	}
	if got, want := cfg.plakletPkgDir(), "/base/pkg/integrations"; got != want {
		t.Fatalf("plakletPkgDir() = %q, want %q", got, want)
	}
	if got, want := cfg.plakletCacheDir(), "/base/pkg/cache"; got != want {
		t.Fatalf("plakletCacheDir() = %q, want %q", got, want)
	}
}

func TestRunWorkReportsFailureOnSpawnError(t *testing.T) {
	cfg := testConfig(t, "/nonexistent/plaklet-binary-xyz")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	runWork(context.Background(), clt, cfg, item)

	if len(replies) != 1 || replies[0].Type != ReplyFailure {
		t.Fatalf("replies = %+v, want one ReplyFailure", replies)
	}
}

func TestRunWorkNoDoubleReplyOnPlakletSuccess(t *testing.T) {
	bin := writePlakletWrapper(t, "success")
	cfg := testConfig(t, bin)

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}

	runWork(context.Background(), clt, cfg, item)

	if len(replies) != 1 {
		t.Fatalf("replies = %+v, want exactly 1 (no synthesized failure on top of plaklet's own reply)", replies)
	}
}

func TestForwardReplyPassesFieldsThrough(t *testing.T) {
	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	clt := NewClient(srv.URL)
	workID := uuid.New()
	r := ExecReply{
		Type:    ReplyReport,
		Message: "progress",
		Report:  json.RawMessage(`{"bytes":42}`),
		State:   json.RawMessage(`{"cursor":"x"}`),
	}
	forwardReply(context.Background(), clt, workID, r)

	if len(replies) != 1 {
		t.Fatalf("replies = %+v, want 1", replies)
	}
	got := replies[0]
	if got.Type != ReplyReport || got.Message != "progress" {
		t.Errorf("got = %+v", got)
	}
	if string(got.Report) != `{"bytes":42}` {
		t.Errorf("Report = %s", got.Report)
	}
	if string(got.State) != `{"cursor":"x"}` {
		t.Errorf("State = %s", got.State)
	}
}

func TestForwardReplyDoesNotPanicOnTransportError(t *testing.T) {
	// Client pointed at a closed server: Reply() will error, and
	// forwardReply must swallow it (log only), not panic or block.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	clt := NewClient(srv.URL)
	done := make(chan struct{})
	go func() {
		forwardReply(context.Background(), clt, uuid.New(), ExecReply{Type: ReplySuccess})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forwardReply hung on transport error")
	}
}

// newReplyCapturingServer starts an httptest.Server that accepts enroll/poll
// silently and appends every posted Reply body into *replies.
func newReplyCapturingServer(replies *[]Reply) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/edge/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			var rep Reply
			dec := json.NewDecoder(bufio.NewReader(r.Body))
			if err := dec.Decode(&rep); err == nil {
				*replies = append(*replies, rep)
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}
