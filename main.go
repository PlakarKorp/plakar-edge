// Command plakar-edge is a standalone, daemonized executor that enrolls against a
// plakman control plane and runs tasks forwarded to it. It depends only on the
// standard library (plus google/uuid); the wire contracts it speaks are
// duplicated in protocol.go rather than imported from plakman.
//
// Lifecycle:
//   - On first boot it enrolls with the control-plane API URL + enrollment key,
//     receiving a per-edge token which it persists under -state-dir.
//   - Thereafter it long-polls /edge/poll for work, spawns the local plaklet to
//     run each task, and streams plaklet's replies back to /edge/{work}/reply.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Version is the plakar-edge build version, reported to the control plane at
// enrollment for observability (not used for compatibility gating — that is the
// protocol version). Override at build time with -ldflags "-X main.Version=...".
var Version = "v0.1.0-devel"

// Config holds everything the daemon needs at runtime.
type Config struct {
	APIURL     string
	StateDir   string
	PlakletBin string
	// PkgDir is the plaklet package base directory. Plaklet is invoked with
	// its integrations under <PkgDir>/integrations and its cache under
	// <PkgDir>/cache, matching how the control-plane executor drives plaklet.
	PkgDir   string
	PollHold time.Duration
}

// plakletPkgDir and plakletCacheDir derive the paths plaklet expects from the
// single package base dir, mirroring executor.execArgsFromConfig.
func (c *Config) plakletPkgDir() string   { return filepath.Join(c.PkgDir, "integrations") }
func (c *Config) plakletCacheDir() string { return filepath.Join(c.PkgDir, "cache") }

// state is the persisted identity written after a successful enrollment.
type state struct {
	EdgeId string `json:"edge_id"`
	Token  string `json:"token"`
}

func (c *Config) statePath() string { return filepath.Join(c.StateDir, "edge.json") }

func loadState(c *Config) (*state, error) {
	buf, err := os.ReadFile(c.statePath())
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(buf, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(c *Config, s *state) error {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the token is a credential.
	return os.WriteFile(c.statePath(), buf, 0o600)
}

func main() {
	var cfg Config
	var enrollmentKey, name string

	flag.StringVar(&cfg.APIURL, "api-url", "", "plakman control plane API base URL (required)")
	flag.StringVar(&enrollmentKey, "enrollment-key", "", "enrollment key (required on first run)")
	flag.StringVar(&name, "name", "", "edge name to register (defaults to hostname)")
	flag.StringVar(&cfg.StateDir, "state-dir", "/var/lib/plakar-edge", "directory for persisted edge identity")
	flag.StringVar(&cfg.PlakletBin, "plaklet-bin", "plaklet", "path to the plaklet binary")
	flag.StringVar(&cfg.PkgDir, "pkg", "", "plaklet package base dir (expects <pkg>/integrations and <pkg>/cache)")
	flag.DurationVar(&cfg.PollHold, "poll-hold", 30*time.Second, "how long the server holds a poll open")
	flag.Parse()

	if cfg.APIURL == "" {
		fatal("-api-url is required")
	}

	hostname, _ := os.Hostname()
	if name == "" {
		name = hostname
	}

	clt := NewClient(cfg.APIURL)

	// Load a persisted identity, or enroll if this is the first boot.
	st, err := loadState(&cfg)
	switch {
	case err == nil:
		log.Printf("resuming as edge %s", st.EdgeId)
	case os.IsNotExist(err):
		if enrollmentKey == "" {
			fatal("no persisted identity and -enrollment-key not provided")
		}
		log.Printf("enrolling as %q against %s (protocol v%d)", name, cfg.APIURL, EdgeProtocolVersion)
		resp, err := clt.Enroll(context.Background(), EnrollRequest{
			EnrollmentKey:   enrollmentKey,
			Name:            name,
			Hostname:        hostname,
			ProtocolVersion: EdgeProtocolVersion,
			EdgeVersion:     Version,
		})
		if err != nil {
			fatal("enrollment failed: %v", err)
		}
		if !resp.Supported {
			log.Printf("WARNING: control plane protocol is v%d, this edge speaks v%d; "+
				"the control plane will not dispatch work until this edge is upgraded",
				resp.ProtocolVersion, EdgeProtocolVersion)
		}
		st = &state{EdgeId: resp.EdgeId.String(), Token: resp.Token}
		if err := saveState(&cfg, st); err != nil {
			fatal("failed to persist identity: %v", err)
		}
		log.Printf("enrolled as edge %s", st.EdgeId)
	default:
		fatal("failed to read state: %v", err)
	}

	clt.SetToken(st.Token)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("edge %s online, polling %s", st.EdgeId, cfg.APIURL)
	pollLoop(ctx, clt, &cfg)
	log.Printf("edge %s shutting down", st.EdgeId)
}

// pollLoop is the daemon's heart: long-poll, run, repeat, until the context is
// canceled. Transient poll errors back off briefly rather than spinning.
func pollLoop(ctx context.Context, clt *Client, cfg *Config) {
	const backoff = 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		item, err := clt.Poll(ctx, cfg.PollHold)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("poll error: %v (retrying in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		if item == nil {
			continue // no work; poll again
		}

		// Run synchronously: one edge handles one task at a time for the PoC.
		runWork(ctx, clt, cfg, item)
	}
}

func fatal(format string, args ...any) {
	log.Printf(format, args...)
	os.Exit(1)
}
