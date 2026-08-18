# Desktop GUI and TypeScript SDK — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Build tags

| Tag | File | Behaviour |
|---|---|---|
| `desktop` | `cmd/qrdb/gui_desktop.go` | full Wails v2 GUI with embedded assets |
| `!desktop` | `cmd/qrdb/gui_stub.go` | returns `GUIUnavailableError`; the binary falls through to the CLI |

**The `desktop` tag requires CGO** for native window integration. The Makefile's
`DESKTOP_TAGS` is `desktop production webkit2_41` plus the ordinary
`BUILD_TAGS`.

The `embedded` transport value on the CLI's `--transport` flag likewise
requires the desktop/GUI build.

## Exported surface

**Go side** — Wails bindings in `cmd/qrdb/bindings_*.go`, one file per subject
area (kv, mvcc, sql, vector, timeseries, seal, tenant, database, collection,
index, lease, lock, watch, batch, transaction, users, roles, certs, credential,
officer, backup, rebalance, daemon, config, server). Several have `_stub.go`
counterparts for builds without the corresponding feature tag, so the binding
surface exists whether or not the feature is compiled in.

**TypeScript side** — `@qrdb/sdk` (`sdk/typescript`):

```ts
getClient()   // auto-detects the runtime, returns WailsClient or RestClient
```

| Client | Environment | Mechanism |
|---|---|---|
| `WailsClient` | desktop | Wails IPC to the Go bindings |
| `RestClient` | browser | `fetch()` against the qrdbd REST API |

The service interfaces, error type hierarchy (transport, service, vector,
management, feature errors), watch streaming and npm packaging are documented in
[typescript-sdk/README.md](../typescript-sdk/README.md).

## Invariants

- **One frontend codebase runs in both environments.** A view that only works on
  one is a defect, not a variant.
- **`getClient()` auto-detects.** Frontend code does not branch on environment.
- **There is no login screen and no password path.** Authentication is mTLS at
  the TLS handshake; the header identity is the client certificate's Subject CN.
- **The browser path uses the ordinary REST API plus CORS.** No GUI-only
  endpoint exists, so the GUI is exactly as privileged as the certificate it
  presents.
- **Views are capability-gated** against the server's reported capabilities
  rather than a hardcoded list, so a server without a feature shows no view for
  it.
- **Assets are embedded** with `//go:embed` by `make build-client`; the binary
  needs no external files at runtime.
- **Display detection decides whether the GUI launches with no arguments** —
  Linux on `DISPLAY`/`WAYLAND_DISPLAY`, Darwin always **unless `SSH_TTY` is
  set**, Windows always.
- **A non-`desktop` build returns `GUIUnavailableError`** and falls through to
  the CLI rather than failing.

## App shell states

`loading` → (`wizard` on first run) → `unseal` if sealed → `main`.

| State | Condition |
|---|---|
| `loading` | initial load, fetching capabilities |
| `wizard` | first run (`isFirstRun()` is true) |
| `unseal` | the server is sealed |
| `main` | unsealed and connected |

## Setup wizard branches

`ServerMode` → **Embedded**: `StorageBackend` → (`RaftConfig` when Raft) →
`EncryptionBarrier` → `Summary` → `Initializing` → `Complete`.

`ServerMode` → **Remote**: `ConnectionConfig` → `Summary` → `Complete`.

## Error semantics

| Condition | Behaviour | Fails closed? |
|---|---|---|
| Non-`desktop` build asked for the GUI | `GUIUnavailableError`, CLI runs instead | **yes** |
| No display detected | GUI does not launch; CLI runs | n/a |
| Server sealed | the shell shows the unseal state; no data views | **yes** |
| Capability absent on the server | its view is not shown | **yes** |
| No client certificate | the TLS handshake fails — there is no fallback | **yes** |

The TypeScript SDK's error hierarchy separates transport, service, vector,
management and feature errors so a frontend can distinguish "the server said
no" from "the connection failed".

## Makefile targets

```bash
make install-frontend    # npm install for the TypeScript SDK + frontend
make build-frontend      # build the TypeScript SDK + Vite frontend
make build-client        # client binary in the devcontainer (CLI + GUI)
make dev-gui             # Wails dev mode with hot reload
```

`build-client` produces a **single binary with every frontend asset embedded**.

## Known test gap

`sdk/typescript/test/` holds **seven vitest contract suites** —
`rest-contract`, `seal-contract`, `officer-contract`,
`credential-audit-contract`, `wire-format`, `transform`, `vector-enum-wire`.
The Makefile has `npm install` and `npm run build` targets but **no test
target**, and **no CI job runs them**.

**The TypeScript SDK is therefore entirely untested in CI**, despite the suites
existing. That is a tracked gap, recorded here rather than left to be
discovered.

## Test strategy

- **Go side**: the bindings have unit tests per subject area, including stub
  builds, so the binding surface compiles and behaves with and without each
  feature tag.
- **Display detection** is tested per platform against the environment
  variables above.
- **TypeScript side**: the seven contract suites exist and are **not run** — see
  the gap above. Running them is the first thing to fix here.

## Observability

The GUI's own state machine is the operator-visible signal: `unseal` means the
server is sealed, and a missing view means the capability is absent. Neither is
an error state to debug — both are the shell reflecting the server.
