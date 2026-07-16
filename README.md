# plakar-edge

`plakar-edge` is a small, daemonized executor that runs on a remote network and
executes tasks handed to it by a [plakman](https://github.com/PlakarKorp/plakman)
control plane. It lets backups, checks, syncs and the like run *where the data
lives* — inside a customer network, behind NAT — while scheduling, state and
reporting stay centralized in the control plane.

The daemon is intentionally tiny: it speaks HTTP to the control plane and spawns
the existing `plaklet` binary to do the actual work. It depends only on the Go
standard library and `github.com/google/uuid`.

## How it works

```
plakman control plane                          remote network
┌────────────────────┐                        ┌────────────────────────┐
│ scheduler          │                        │ plakar-edge            │
│   └─ forward task ──┼──(edge polls)──────────┤   1. POST /edge/enroll │
│ edge API           │◄───────────────────────┤   2. POST /edge/poll   │
│   /edge/enroll     │                        │   3. spawn plaklet ───┐│
│   /edge/poll       │◄──(stream replies)──────┤   4. POST .../reply   ││
│   /edge/{id}/reply │                        │            plaklet ◄──┘│
└────────────────────┘                        └────────────────────────┘
```

1. **Enroll** — on first boot the edge presents the control plane's enrollment
   key and receives its own bearer token, which it persists under `-state-dir`.
2. **Poll** — it long-polls `/edge/poll` for work. All traffic is
   edge-initiated, so the control plane never needs to reach into the remote
   network.
3. **Run** — for each work item it spawns `plaklet`, feeding it the task on
   stdin.
4. **Report** — it streams `plaklet`'s replies (progress, state, success or
   failure) back to `/edge/{work_id}/reply`.

Connector secrets are resolved centrally by the control plane and travel with
the work item, so the edge does not need secret-manager plugins.

## Build

```sh
make            # or: go build -o plakar-edge .
```

`plakar-edge` is a **single self-contained binary**. It embeds the
[plaklet](https://github.com/PlakarKorp/plaklet) executor (pinned in `go.mod`)
and runs it as a subcommand — `plakar-edge plaklet <args>` — so there is no
separate `plaklet` binary to build or ship. Building the edge builds plaklet.

## Run

```sh
plakar-edge \
  -control-plane  https://plakman.example.com \
  -enroll         <key from the control plane> \
  -name           edge-paris-1 \
  -state-dir      /var/lib/plakar-edge \
  -pkg            /var/lib/plakar-edge/pkgs
```

To execute a task the daemon re-execs itself as `plakar-edge plaklet …`; no
external binary is needed.

### Connector packages

The embedded plaklet needs a connector package (s3, sftp, …) for each source and
target it backs up. The edge is assumed to have no network access beyond the
control plane, so when a task names a connector the edge doesn't have, it fetches
that package **for its own GOOS/GOARCH through the control plane** — plakman
proxies it from the plugin feed. Packages are cached under `<pkg>/integrations`
and reused, so each is downloaded only once.

After the first successful enrollment the token is stored under `-state-dir`;
subsequent restarts resume with it and `-enroll` is no longer required. If the
control plane is unreachable at first boot, enrollment retries with backoff until
it succeeds (a rejected enrollment key is fatal). Once enrolled, the poll loop
likewise retries through control-plane outages.

| Flag | Default | Meaning |
|------|---------|---------|
| `-control-plane` | *(required)* | Control plane API base URL |
| `-enroll` | | Enrollment key; required only on first boot |
| `-name` | hostname | Edge name registered with the control plane |
| `-state-dir` | `/var/lib/plakar-edge` | Where the edge identity/token is persisted |
| `-pkg` | | Plaklet package base dir (`<pkg>/integrations`, `<pkg>/cache`) |
| `-poll-hold` | `30s` | Expected server-side long-poll hold |
| `-listen` | `127.0.0.1:9877` | Address for the supervision HTTP server (`/health`, `/ready`, `/metrics`); empty disables it |
| `-metrics` | `true` | Expose node-exporter metrics at `/metrics` on the `-listen` address |

## Supervision & metrics

The edge runs a small HTTP server on `-listen` for supervision and monitoring.
It binds to loopback by default; set `-listen` to `0.0.0.0:9877` (or empty to
disable) as your deployment requires.

| Endpoint | Purpose |
|----------|---------|
| `/health` | **Liveness** — `200` as long as the process is serving. Fail → restart the daemon. |
| `/ready` | **Readiness** — `200` only once the edge is enrolled and polling, `503` otherwise. Gates whether the edge is expected to pick up work. |
| `/metrics` | **Node exporter** — host metrics (CPU, memory, filesystem, network, load, …) via the upstream [`node_exporter`](https://github.com/prometheus/node_exporter) collectors, plus the standard Go runtime/process collectors. Scrape it with Prometheus. |

`/health` and `/ready` return a small JSON body:

```json
{ "status": "ok", "version": "v1.0.0", "uptime_seconds": 42.7 }
```

A typical systemd/process supervisor uses `/health` to decide whether to
restart the unit; a Prometheus deployment scrapes `/metrics`; and an
orchestrator that fans work out to edges can consult `/ready`.

> Note: the node-exporter collectors are the upstream ones. Most are richest on
> Linux; on other platforms unsupported collectors simply report a per-collector
> `node_scrape_collector_success 0` and are skipped, without failing the scrape.

## Wire protocol

`protocol.go` holds the JSON contracts the edge exchanges with the control
plane and with `plaklet`. These are **duplicated** from plakman on purpose, to
keep this repository free of any plakman-internal dependency; the file documents
the plakman source of truth to keep in sync with.

### Protocol version

The edge sends `EdgeProtocolVersion` (an integer) at enrollment. It is bumped
only when the wire structs (`WorkItem` / `Reply` / `Configuration` / the plaklet
`ExecPayload`/`ExecReply`) change, so the edge and the control plane can release
independently. The control plane records it; if it does not support that
version, the edge still enrolls (so it stays visible) but is **not dispatched
work** until upgraded — the enroll response's `supported` flag signals this, and
the daemon logs a warning. A separate `edge_version` build string is reported
for observability only and is not used for gating.
