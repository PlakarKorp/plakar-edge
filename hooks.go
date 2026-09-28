package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Pre/post-job hook scripts.
//
// A task authored on the control plane may name two scripts in its config,
// under the flat keys "hooks.pre_job" and "hooks.post_job" (flattened from
// plakman's contract.HooksConfig, the same additive-key convention as
// exec.cpu/exec.concurrency). The names are bare file names; this edge only
// ever runs them from its -scripts-dir, which is the trust boundary: the
// operator who populates that directory decides what a task may run, the task
// author only picks by name. With no -scripts-dir configured every hook is
// refused, and a refused pre-job hook fails the work rather than running the
// job without it.

// hookScript answers the hook script the work item names under
// "hooks.<hook>", or "" when the task carries no such hook.
func hookScript(item *WorkItem, hook string) string {
	return item.TaskConfig["hooks."+hook]
}

// resolveHookScript answers the path of a named hook script inside scriptsDir,
// refusing anything that could name a file outside it.
func resolveHookScript(scriptsDir, name string) (string, error) {
	if scriptsDir == "" {
		return "", errors.New("this edge has no -scripts-dir configured")
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid script name %q: a hook is a file name inside the scripts directory, not a path", name)
	}
	path := filepath.Join(scriptsDir, name)
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("script %q: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("script %q is not a regular file", name)
	}
	return path, nil
}

// runHook runs the hook script the work item names under "hooks.<hook>", if
// any. The script inherits the daemon's environment plus PLAKAR_WORK_ID,
// PLAKAR_OP and PLAKAR_HOOK so one script can serve several tasks and both
// ends of one. Its combined output goes to logs, the job's output log; on
// failure its tail also rides in the error, which the job's status shows.
func runHook(ctx context.Context, cfg *Config, item *WorkItem, hook string, logs io.Writer) error {
	name := hookScript(item, hook)
	if name == "" {
		return nil
	}
	path, err := resolveHookScript(cfg.ScriptsDir, name)
	if err != nil {
		return err
	}

	log.Printf("work %s: running %s hook %q", item.WorkId, hook, name)
	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = cfg.ScriptsDir
	cmd.Env = append(os.Environ(),
		"PLAKAR_WORK_ID="+item.WorkId.String(),
		"PLAKAR_OP="+item.Op,
		"PLAKAR_HOOK="+hook,
	)
	fmt.Fprintf(logs, "running %s hook %q\n", hook, name)
	tail := &tailBuffer{max: 4096}
	// One writer for both streams: exec then copies them from a single pipe,
	// so tailBuffer sees no concurrent writes.
	out := io.MultiWriter(logs, tail)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("script %q: %w%s", name, err, outputTail(tail.bytes()))
	}
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte {
	if len(t.buf) > t.max {
		return t.buf[len(t.buf)-t.max:]
	}
	return t.buf
}

// outputTail renders the end of a failed script's output for an error message,
// bounded so a chatty script cannot bloat the reply it rides in.
func outputTail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	const max = 512
	if len(s) > max {
		s = "..." + s[len(s)-max:]
	}
	return ": " + s
}
