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
go build -o plakar-edge .
```

The edge spawns `plaklet`, so that binary and its integration plugins must be
available on the edge host.

## Run

```sh
plakar-edge \
  -api-url        https://plakman.example.com \
  -enrollment-key <key from the control plane> \
  -name           edge-paris-1 \
  -state-dir      /var/lib/plakar-edge \
  -plaklet-bin    /usr/local/bin/plaklet \
  -pkg            /var/lib/plakar-edge/pkgs
```

After the first successful enrollment the token is stored under `-state-dir`;
subsequent restarts resume with it and `-enrollment-key` is no longer required.

| Flag | Default | Meaning |
|------|---------|---------|
| `-api-url` | *(required)* | Control plane API base URL |
| `-enrollment-key` | | Enrollment key; required only on first boot |
| `-name` | hostname | Edge name registered with the control plane |
| `-state-dir` | `/var/lib/plakar-edge` | Where the edge identity/token is persisted |
| `-plaklet-bin` | `plaklet` | Path to the `plaklet` binary |
| `-pkg` | | Plaklet package base dir (`<pkg>/integrations`, `<pkg>/cache`) |
| `-poll-hold` | `30s` | Expected server-side long-poll hold |

## Wire protocol

`protocol.go` holds the JSON contracts the edge exchanges with the control
plane and with `plaklet`. These are **duplicated** from plakman on purpose, to
keep this repository free of any plakman-internal dependency; the file documents
the plakman source of truth to keep in sync with.
