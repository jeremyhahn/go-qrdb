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
- **The database wizard's barrier passphrase is write-only.** Choosing the
  passphrase barrier asks for the passphrase and a confirmation (both
  `type="password"`); the create request carries `barrier: { enabled: true,
  mode: "passphrase", secret }`. The passphrase lives in the barrier step, then
  in the wizard store only until the create request is built:
  `takeBarrierPassphrase` hands it to the request and clears it before the
  request is sent, so neither a successful nor a failed create leaves it
  behind, and cancelling the wizard (`resetDatabaseWizard`) clears it. It is
  never logged -- the create step logs a summary of the request, not the
  request -- and the summary step shows only that it is set. Returning to the
  barrier step asks for it again, and tearing the wizard down by any route
  (`onDestroy` → `clearBarrierPassphrase`) clears it. The barrier defaults to
  none, so the barrier is opt-in. The Shamir card is shown disabled, with a
  tooltip, until per-database Shamir share holders ship: the server refuses a
  per-database Shamir barrier until then. *(Changed: the wizard sent the barrier block without
  `enabled` and with no secret, so the server never created a barrier and the
  barrier step had no effect.)*

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

## TypeScript in CI

CI's `test-typescript` job, required by the CI gate, runs the TypeScript SDK's
`tsc`, `npm test` (which typechecks the tests, then runs the vitest suites) and
build, then `svelte-check` on this frontend against the built SDK. *(Corrected:
this section recorded that no CI job ran the TypeScript suites; that gap is
closed.)* The Playwright suites under `cmd/qrdb/frontend/e2e/` are not in CI.

## Test strategy

- **Go side**: the bindings have unit tests per subject area, including stub
  builds, so the binding surface compiles and behaves with and without each
  feature tag.
- **Display detection** is tested per platform against the environment
  variables above.
- **TypeScript side**: the SDK's vitest suites and `svelte-check` run in CI
  (above). The wizard's barrier passphrase is covered by
  `e2e/database-wizard-barrier-passphrase.spec.ts` (mocked tier): validation,
  the exact create request, and that the passphrase appears on no page and in
  no console message after a successful or a failed create. The Shamir card's
  disabled state is covered in `wizard-encryption.spec.ts` and
  `database-wizard-full-flow.spec.ts`. On the Go side,
  `TestDatabaseBinding_CreateWithBarrierInitialisesIt` drives a barrier create
  through the binding into a real barrier manager and proves the passphrase it
  sent is the one that unseals the database.

## Observability

The GUI's own state machine is the operator-visible signal: `unseal` means the
server is sealed, and a missing view means the capability is absent. Neither is
an error state to debug — both are the shell reflecting the server.
