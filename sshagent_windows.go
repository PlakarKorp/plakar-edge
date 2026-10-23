//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Well-known pipe used by the ssh-agent service of win32_OpenSSH
const WIN32_OPENSSH_AGENT_PIPE = `\\.\pipe\openssh-ssh-agent`

// startSSHAgent determines whether the system-wide ssh-agent service of
// win32_OpenSSH is running.
func startSSHAgent(ctx context.Context) (string, error) {
	var args []string

	args = append(args, "-NoProfile", "-NonInteractive", "-Command")
	args = append(args, "Get-Service ssh-agent | " +
	    "Select Status | Format-List")

	cmd := exec.CommandContext(ctx, "PowerShell", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	// reap process
	go func() { _ = cmd.Wait(); }()

	buf, err := io.ReadAll(stdout)
	if err != nil {
		return "", err
	}

	if len(buf) > 0 {
		for line := range strings.Lines(string(buf)) {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}

			s := strings.Split(trimmed, " : ");
			if len(s) != 2 {
				continue
			}
			if s[1] == "Running" {
				return WIN32_OPENSSH_AGENT_PIPE, nil
			}
		}
	}

	return "", fmt.Errorf("ssh-agent is not running.")
}
