package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	"github.com/google/uuid"
)

// runWork executes one work item by spawning the local plaklet, feeding it the
// ExecPayload on stdin and forwarding every ExecReply it emits back to the
// control plane. It always sends a terminal reply (success or failure) so the
// forwarder on the control-plane side unblocks.
func runWork(ctx context.Context, clt *Client, cfg *Config, item *WorkItem) {
	log.Printf("running work %s (%s)", item.WorkId, item.Op)

	err := spawnPlaklet(ctx, clt, cfg, item)
	if err != nil {
		log.Printf("work %s failed: %v", item.WorkId, err)
		_ = clt.Reply(ctx, item.WorkId, Reply{
			Type:    ReplyFailure,
			Message: err.Error(),
		})
		return
	}
	// spawnPlaklet forwards plaklet's own terminal reply; nothing else to do.
}

func spawnPlaklet(ctx context.Context, clt *Client, cfg *Config, item *WorkItem) error {
	plakletArgs := []string{"-pkg", cfg.plakletPkgDir(), "-cache", cfg.plakletCacheDir(), "-quiet"}

	// Default path: re-exec this same binary with the "plaklet" subcommand, so
	// the embedded executor runs. Only use an external binary if -plaklet-bin was
	// set explicitly.
	name := cfg.PlakletBin
	var args []string
	if name == "" {
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot locate own executable to run plaklet: %w", err)
		}
		name = self
		args = append([]string{"plaklet"}, plakletArgs...)
	} else {
		args = plakletArgs
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("failed to start plaklet: %w", err)
	}

	payload := ExecPayload{
		Op:         item.Op,
		TaskConfig: item.TaskConfig,
		Source:     item.Source,
		Target:     item.Target,
	}
	if err := json.NewEncoder(stdin).Encode(&payload); err != nil {
		stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("failed to send payload to plaklet: %w", err)
	}
	stdin.Close()

	// Pump plaklet's reply stream to the control plane. A terminal reply
	// (success/failure) ends the loop; sawTerminal tracks whether we saw one.
	sawTerminal := false
	dec := json.NewDecoder(stdout)
	for {
		var r ExecReply
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			log.Printf("work %s: decode plaklet reply: %v", item.WorkId, err)
			break
		}
		if r.Type == ReplySuccess || r.Type == ReplyFailure {
			sawTerminal = true
		}
		forwardReply(ctx, clt, item.WorkId, r)
	}

	waitErr := cmd.Wait()

	// If plaklet died without a terminal reply, synthesize a failure so the
	// control plane doesn't hang waiting on the work.
	if !sawTerminal {
		msg := "plaklet exited without a result"
		if waitErr != nil {
			msg = fmt.Sprintf("plaklet exited abnormally: %v", waitErr)
		}
		return errors.New(msg)
	}
	return nil
}

// forwardReply relays a single plaklet ExecReply to the control plane as an edge
// Reply. The report/state raw JSON is passed through untouched.
func forwardReply(ctx context.Context, clt *Client, workID uuid.UUID, r ExecReply) {
	if err := clt.Reply(ctx, workID, Reply{
		Type:    r.Type,
		Message: r.Message,
		Report:  r.Report,
		State:   r.State,
	}); err != nil {
		log.Printf("work %s: failed to forward reply %q: %v", workID, r.Type, err)
	}
}
