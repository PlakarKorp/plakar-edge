package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestResolveHookScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name       string
		scriptsDir string
		script     string
		wantErr    string
	}{
		{"resolves a plain name", dir, "ok.sh", ""},
		{"no scripts dir configured", "", "ok.sh", "no -scripts-dir"},
		{"empty name", dir, "", "invalid script name"},
		{"dot", dir, ".", "invalid script name"},
		{"dotdot", dir, "..", "invalid script name"},
		{"relative escape", dir, "../ok.sh", "invalid script name"},
		{"nested path", dir, "subdir/ok.sh", "invalid script name"},
		{"backslash path", dir, `subdir\ok.sh`, "invalid script name"},
		{"absolute path", dir, filepath.Join(dir, "ok.sh"), "invalid script name"},
		{"missing script", dir, "ghost.sh", "ghost.sh"},
		{"not a regular file", dir, "subdir", "not a regular file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, err := resolveHookScript(tc.scriptsDir, tc.script)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("resolveHookScript: %v", err)
				}
				if want := filepath.Join(dir, tc.script); path != want {
					t.Fatalf("path = %q, want %q", path, want)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveHookScript(%q, %q) = %q, want error containing %q", tc.scriptsDir, tc.script, path, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// writeHookScript drops an executable script into dir that appends its
// PLAKAR_HOOK value to markerFile and then runs body.
func writeHookScript(t *testing.T, dir, name, markerFile, body string) {
	t.Helper()
	contents := "#!/bin/sh\n" +
		"echo \"$PLAKAR_HOOK\" >> " + shellQuote(markerFile) + "\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
}

// readMarker answers the hooks that ran, in order.
func readMarker(t *testing.T, markerFile string) []string {
	t.Helper()
	buf, err := os.ReadFile(markerFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return strings.Fields(string(buf))
}

func hooksTestSetup(t *testing.T, plakletScript string) (*Config, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts are shell scripts")
	}
	cfg := testConfig(t, writePlakletWrapper(t, plakletScript))
	cfg.ScriptsDir = t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	return cfg, cfg.ScriptsDir, marker
}

func TestRunWorkRunsHooksAroundPlaklet(t *testing.T) {
	cfg, scripts, marker := hooksTestSetup(t, "success")
	writeHookScript(t, scripts, "pre.sh", marker, "")
	writeHookScript(t, scripts, "post.sh", marker, "")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup", TaskConfig: map[string]string{
		"hooks.pre_job":  "pre.sh",
		"hooks.post_job": "post.sh",
	}}
	runWork(context.Background(), NewClient(srv.URL), cfg, item)

	if got := readMarker(t, marker); len(got) != 2 || got[0] != "pre_job" || got[1] != "post_job" {
		t.Fatalf("hooks ran = %v, want [pre_job post_job]", got)
	}
	if len(replies) != 1 || replies[0].Type != ReplySuccess {
		t.Fatalf("replies = %+v, want one ReplySuccess", replies)
	}
}

func TestRunWorkPreHookFailureAbortsTheWork(t *testing.T) {
	cfg, scripts, marker := hooksTestSetup(t, "success")
	writeHookScript(t, scripts, "pre.sh", marker, "echo cannot quiesce >&2\nexit 1")
	writeHookScript(t, scripts, "post.sh", marker, "")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup", TaskConfig: map[string]string{
		"hooks.pre_job":  "pre.sh",
		"hooks.post_job": "post.sh",
	}}
	runWork(context.Background(), NewClient(srv.URL), cfg, item)

	// The pre-job hook failed: plaklet never ran, the post-job hook (its
	// undo) never ran, and the one terminal reply is a failure naming the
	// hook and carrying its output.
	if got := readMarker(t, marker); len(got) != 1 || got[0] != "pre_job" {
		t.Fatalf("hooks ran = %v, want [pre_job]", got)
	}
	if len(replies) != 1 || replies[0].Type != ReplyFailure {
		t.Fatalf("replies = %+v, want one ReplyFailure", replies)
	}
	for _, want := range []string{"pre-job hook", "pre.sh", "cannot quiesce"} {
		if !strings.Contains(replies[0].Message, want) {
			t.Fatalf("failure message %q does not name %q", replies[0].Message, want)
		}
	}
}

func TestRunWorkMissingScriptsDirFailsAHookedWork(t *testing.T) {
	cfg, scripts, marker := hooksTestSetup(t, "success")
	writeHookScript(t, scripts, "pre.sh", marker, "")
	cfg.ScriptsDir = "" // the operator never opted into scripts

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup", TaskConfig: map[string]string{
		"hooks.pre_job": "pre.sh",
	}}
	runWork(context.Background(), NewClient(srv.URL), cfg, item)

	if got := readMarker(t, marker); got != nil {
		t.Fatalf("hooks ran = %v, want none", got)
	}
	if len(replies) != 1 || replies[0].Type != ReplyFailure || !strings.Contains(replies[0].Message, "no -scripts-dir") {
		t.Fatalf("replies = %+v, want one ReplyFailure naming the missing -scripts-dir", replies)
	}
}

func TestRunWorkPostHookFailureFailsASuccessfulWork(t *testing.T) {
	cfg, scripts, marker := hooksTestSetup(t, "success")
	writeHookScript(t, scripts, "post.sh", marker, "echo cannot resume >&2\nexit 1")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup", TaskConfig: map[string]string{
		"hooks.post_job": "post.sh",
	}}
	runWork(context.Background(), NewClient(srv.URL), cfg, item)

	// Plaklet succeeded but its cleanup did not: exactly one terminal reply,
	// and it is a failure naming the post-job hook.
	if len(replies) != 1 || replies[0].Type != ReplyFailure {
		t.Fatalf("replies = %+v, want one ReplyFailure", replies)
	}
	for _, want := range []string{"post-job hook", "post.sh", "cannot resume"} {
		if !strings.Contains(replies[0].Message, want) {
			t.Fatalf("failure message %q does not name %q", replies[0].Message, want)
		}
	}
}

func TestRunWorkPostHookRunsAndReportsAfterPlakletFailure(t *testing.T) {
	cfg, scripts, marker := hooksTestSetup(t, "failure")
	writeHookScript(t, scripts, "post.sh", marker, "exit 1")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup", TaskConfig: map[string]string{
		"hooks.post_job": "post.sh",
	}}
	runWork(context.Background(), NewClient(srv.URL), cfg, item)

	// The post-job hook still ran (cleanup happens on failure too), its own
	// failure rides as a non-terminal error, and plaklet's failure stays the
	// one terminal reply.
	if got := readMarker(t, marker); len(got) != 1 || got[0] != "post_job" {
		t.Fatalf("hooks ran = %v, want [post_job]", got)
	}
	if len(replies) != 2 || replies[0].Type != ReplyError || replies[1].Type != ReplyFailure {
		t.Fatalf("replies = %+v, want [error, failure]", replies)
	}
	if replies[1].Message != "boom" {
		t.Fatalf("terminal message = %q, want plaklet's own %q", replies[1].Message, "boom")
	}
}

func TestRunWorkWithoutHooksRunsNoScript(t *testing.T) {
	cfg, _, marker := hooksTestSetup(t, "success")

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}
	runWork(context.Background(), NewClient(srv.URL), cfg, item)

	if got := readMarker(t, marker); got != nil {
		t.Fatalf("hooks ran = %v, want none", got)
	}
	if len(replies) != 1 || replies[0].Type != ReplySuccess {
		t.Fatalf("replies = %+v, want one ReplySuccess", replies)
	}
}
