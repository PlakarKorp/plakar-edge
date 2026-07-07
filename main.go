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
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/PlakarKorp/plaklet"
)

// Version is the plakar-edge build version, reported to the control plane at
// enrollment for observability (not used for compatibility gating — that is the
// protocol version). Override at build time with -ldflags "-X main.Version=...".
var Version = "v0.1.0-devel"

// Config holds everything the daemon needs at runtime.
type Config struct {
	APIURL   string
	StateDir string
	// PlakletBin overrides the executor binary. In production it is always empty
	// — the daemon re-execs itself with the "plaklet" subcommand. It exists only
	// so tests can inject a fake plaklet; there is no CLI flag for it.
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
	// plakar-edge is a multi-call binary: `plakar-edge plaklet <args>` runs the
	// embedded plaklet executor (the same code the standalone plaklet binary
	// runs). This is how the edge executes tasks — it re-execs itself with this
	// subcommand rather than shelling out to a separate binary.
	if len(os.Args) > 1 && os.Args[1] == "plaklet" {
		os.Exit(plaklet.Main(os.Args[2:]))
	}

	var cfg Config
	var enrollmentKey, name string

	flag.StringVar(&cfg.APIURL, "api-url", "", "plakman control plane API base URL (required)")
	flag.StringVar(&enrollmentKey, "enrollment-key", "", "enrollment key (required on first run)")
	flag.StringVar(&name, "name", "", "edge name to register (defaults to hostname)")
	flag.StringVar(&cfg.StateDir, "state-dir", "/var/lib/plakar-edge", "directory for persisted edge identity")
	flag.StringVar(&cfg.PkgDir, "pkg", "", "plaklet package base dir (default: <state-dir>/pkg)")
	flag.DurationVar(&cfg.PollHold, "poll-hold", 30*time.Second, "how long the server holds a poll open")
	flag.Parse()

	if cfg.APIURL == "" {
		fatal("-api-url is required")
	}

	// Default the package dir under the state dir, and make it absolute so the
	// dir the daemon fetches into is the same one the spawned plaklet loads from
	// regardless of working directory.
	if cfg.PkgDir == "" {
		cfg.PkgDir = filepath.Join(cfg.StateDir, "pkg")
	}
	if abs, err := filepath.Abs(cfg.PkgDir); err == nil {
		cfg.PkgDir = abs
	}

	hostname, _ := os.Hostname()
	if name == "" {
		name = hostname
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		st = enroll(rootCtx, clt, &cfg, enrollmentKey, name, hostname)
		if st == nil {
			return // context canceled during enrollment (SIGTERM)
		}
	default:
		fatal("failed to read state: %v", err)
	}

	clt.SetToken(st.Token)

	log.Printf("edge %s online, polling %s", st.EdgeId, cfg.APIURL)
	pollLoop(rootCtx, clt, &cfg)
	log.Printf("edge %s shutting down", st.EdgeId)
}

// enrollRetryDelay is how long enrollment waits between attempts when the
// control plane is unreachable or erroring transiently. A var (not const) so
// tests can shorten it.
var enrollRetryDelay = 5 * time.Second

// enroll registers this edge, retrying on transient failures (network errors,
// 5xx) until it succeeds or the context is canceled. A definitive rejection
// (4xx — e.g. a wrong/absent enrollment key) is fatal: retrying it is pointless.
// Returns nil only if the context is canceled before enrollment succeeds.
func enroll(ctx context.Context, clt *Client, cfg *Config, key, name, hostname string) *state {
	for {
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("enrolling as %q against %s (protocol v%d)", name, cfg.APIURL, EdgeProtocolVersion)
		resp, err := clt.Enroll(ctx, EnrollRequest{
			EnrollmentKey:   key,
			Name:            name,
			Hostname:        hostname,
			ProtocolVersion: EdgeProtocolVersion,
			EdgeVersion:     Version,
			SystemInfo:      gatherSystemInfo(),
		})
		if err != nil {
			// A 4xx is a definitive rejection (bad key, name taken, bad
			// request): retrying it will never succeed, so exit fatally.
			var he *HTTPError
			if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
				if he.Status == http.StatusConflict {
					fatal("enrollment rejected: %v; choose a different -name or remove the existing edge", err)
				}
				fatal("enrollment rejected: %v", err)
			}
			// Otherwise transient (control plane down/starting, 5xx, network):
			// back off and retry.
			log.Printf("enrollment failed: %v (retrying in %s)", err, enrollRetryDelay)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(enrollRetryDelay):
			}
			continue
		}

		if !resp.Supported {
			log.Printf("WARNING: control plane protocol is v%d, this edge speaks v%d; "+
				"the control plane will not dispatch work until this edge is upgraded",
				resp.ProtocolVersion, EdgeProtocolVersion)
		}
		st := &state{EdgeId: resp.EdgeId.String(), Token: resp.Token}
		if err := saveState(cfg, st); err != nil {
			fatal("failed to persist identity: %v", err)
		}
		log.Printf("enrolled as edge %s", st.EdgeId)
		return st
	}
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
