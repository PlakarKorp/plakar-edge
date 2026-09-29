package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStartSSHAgent(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent not installed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	sock, err := startSSHAgent(ctx)
	if err != nil {
		cancel()
		t.Fatalf("startSSHAgent: %v", err)
	}

	// ssh-add -l exits 1 on an empty agent and 2 when it cannot reach one.
	cmd := exec.Command("ssh-add", "-l")
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	err = cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
		cancel()
		t.Fatalf("ssh-add -l against %s: %v", sock, err)
	}

	cancel()
	dir := filepath.Dir(sock)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s left behind after shutdown", dir)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSpawnPlakletPassesSSHAuthSock(t *testing.T) {
	bin := writePlakletWrapper(t, "authsock")
	cfg := testConfig(t, bin)
	cfg.SSHAuthSock = "/nonexistent/agent.sock"

	var replies []Reply
	srv := newReplyCapturingServer(&replies)
	defer srv.Close()

	item := &WorkItem{WorkId: uuid.New(), Op: "backup"}
	terminal, err := spawnPlaklet(context.Background(), NewClient(srv.URL), cfg, item, io.Discard)
	if err != nil {
		t.Fatalf("spawnPlaklet: %v", err)
	}
	if terminal == nil || terminal.Message != cfg.SSHAuthSock {
		t.Fatalf("terminal = %+v, want SSH_AUTH_SOCK %q", terminal, cfg.SSHAuthSock)
	}
}
