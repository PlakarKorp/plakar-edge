package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// startSSHAgent runs an ssh-agent owned by the edge for as long as ctx lives,
// restarting it if it dies, and returns the socket path to hand to plaklet as
// SSH_AUTH_SOCK. The sftp integration loads a task's ssh_private_key into the
// agent with ssh-add, which fails outright when no agent is reachable — and a
// daemonized edge (systemd, container) inherits none. The control-plane
// executor runs its own agent for the same reason.
//
// The socket lives in a private temp dir rather than under -state-dir, so a
// long state path cannot exceed the unix socket path limit and a stale socket
// from a crashed run cannot make ssh-agent refuse to start.
func startSSHAgent(ctx context.Context) (string, error) {
	bin, err := exec.LookPath("ssh-agent")
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "plakar-edge-agent-")
	if err != nil {
		return "", err
	}
	sock := filepath.Join(dir, "agent.sock")

	mkcmd := func() *exec.Cmd {
		// ssh-agent refuses to bind over an existing socket; one is left
		// behind when the previous agent was killed rather than stopped.
		if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("ssh-agent: rm %s: %v", sock, err)
		}
		// -D keeps it in the foreground so it can be supervised, without
		// the per-request debug output -d adds.
		cmd := exec.CommandContext(ctx, bin, "-D", "-a", sock)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		// SIGTERM rather than the default SIGKILL, so the agent removes its
		// socket on the way out.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		return cmd
	}

	cmd := mkcmd()
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	// exited is closed when the first agent dies, so the wait below can
	// give up early instead of sitting out its deadline.
	exited := make(chan struct{})
	var once sync.Once
	go func() {
		defer os.RemoveAll(dir)
		for {
			err := cmd.Wait()
			once.Do(func() { close(exited) })
			if ctx.Err() != nil {
				return
			}
			log.Printf("ssh-agent stopped: %v, restarting", err)
			time.Sleep(time.Second)
			if ctx.Err() != nil {
				return
			}
			cmd = mkcmd()
			if err := cmd.Start(); err != nil {
				log.Printf("failed to restart ssh-agent: %v", err)
				return
			}
		}
	}()

	// The socket appears a moment after the process starts; a task arriving
	// in that window would see ssh-add fail to connect.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return sock, nil
		}
		select {
		case <-exited:
			return "", fmt.Errorf("ssh-agent exited before creating %s", sock)
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("ssh-agent did not create %s", sock)
		}
	}
}
