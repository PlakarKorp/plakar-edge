//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

// Well-known pipe used by the ssh-agent service of win32_OpenSSH
const WIN32_OPENSSH_AGENT_PIPE = `\\.\pipe\openssh-ssh-agent`

// startSSHAgent determines whether the system-wide ssh-agent service of
// win32_OpenSSH is running.
func startSSHAgent(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "PowerShell", "-NoProfile", "-NonInteractive",
		"-Command", "Get-Service ssh-agent | Select Status | ConvertTo-Json")

	output, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return "", fmt.Errorf("Get-Service failed: %w: %s", err,
				bytes.TrimSpace(ee.Stderr))
		}
		return "", fmt.Errorf("Get-Service failed: %w", err)
	}

	type Status struct {
		Status int `json:"status"`
	}

	var ret Status
	if err := json.Unmarshal(output, &ret); err != nil {
		return "", fmt.Errorf("failed to decode Get-Service output: %w", err)
	}

	if ret.Status != windows.SERVICE_RUNNING {
		return "", errors.New("ssh-agent is not running")
	}

	return WIN32_OPENSSH_AGENT_PIPE, nil
}
