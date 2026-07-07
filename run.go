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
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
)

// runWork executes one work item by spawning the local plaklet, feeding it the
// ExecPayload on stdin and forwarding every ExecReply it emits back to the
// control plane. It always sends a terminal reply (success or failure) so the
// forwarder on the control-plane side unblocks.
func runWork(ctx context.Context, clt *Client, cfg *Config, item *WorkItem) {
	start := time.Now()
	log.Printf("work %s (%s) started", item.WorkId, item.Op)

	// Make sure the connector packages this work item needs are present, fetching
	// any that are missing through the control-plane proxy.
	if err := ensurePackages(ctx, clt, cfg, item); err != nil {
		log.Printf("work %s (%s) failed after %s: %v", item.WorkId, item.Op, time.Since(start), err)
		_ = clt.Reply(ctx, item.WorkId, Reply{Type: ReplyFailure, Message: err.Error()})
		return
	}

	if err := spawnPlaklet(ctx, clt, cfg, item); err != nil {
		log.Printf("work %s (%s) failed after %s: %v", item.WorkId, item.Op, time.Since(start), err)
		_ = clt.Reply(ctx, item.WorkId, Reply{
			Type:    ReplyFailure,
			Message: err.Error(),
		})
		return
	}
	// spawnPlaklet forwarded plaklet's own terminal (success) reply.
	log.Printf("work %s (%s) succeeded in %s", item.WorkId, item.Op, time.Since(start))
}

// ensurePackages fetches, through the control-plane proxy, any connector package
// named by the work item's source/target that is not already present in the
// edge's integrations dir. Packages are cached there and reused across runs.
func ensurePackages(ctx context.Context, clt *Client, cfg *Config, item *WorkItem) error {
	intdir := cfg.plakletPkgDir()
	if err := os.MkdirAll(intdir, 0o755); err != nil {
		return fmt.Errorf("cannot create integrations dir: %w", err)
	}

	seen := map[string]bool{}
	for _, conf := range []*Configuration{item.Source, item.Target} {
		if conf == nil || conf.Integration.Name == "" {
			continue
		}
		name, version := conf.Integration.Name, conf.Integration.Version
		key := name + "@" + version
		if seen[key] {
			continue
		}
		seen[key] = true

		filename := fmt.Sprintf("%s_%s_%s_%s.ptar", name, version, runtime.GOOS, runtime.GOARCH)
		dst := filepath.Join(intdir, filename)
		if _, err := os.Stat(dst); err == nil {
			continue // already present
		}

		log.Printf("fetching connector package %s (%s/%s) via control plane", key, runtime.GOOS, runtime.GOARCH)
		if err := clt.FetchPackage(ctx, name, version, runtime.GOOS, runtime.GOARCH, dst); err != nil {
			return fmt.Errorf("failed to fetch package %s: %w", key, err)
		}
		log.Printf("installed %s", filename)
	}
	return nil
}

func spawnPlaklet(ctx context.Context, clt *Client, cfg *Config, item *WorkItem) error {
	plakletArgs := []string{"-pkg", cfg.plakletPkgDir(), "-cache", cfg.plakletCacheDir(), "-quiet"}

	// Honor the task's execution limits (set on the scheduler task as
	// exec.cpu / exec.concurrency and flowed through in TaskConfig). Without
	// this the edge's plaklet always ran at its default (NumCPU) concurrency,
	// ignoring the task setting — which can OOM on large sources.
	if v := item.TaskConfig["exec.cpu"]; v != "" {
		plakletArgs = append(plakletArgs, "-cpu", v)
	}
	if v := item.TaskConfig["exec.concurrency"]; v != "" {
		plakletArgs = append(plakletArgs, "-concurrency", v)
	}

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
