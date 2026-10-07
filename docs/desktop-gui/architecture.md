# Desktop GUI — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

> **Build-tag gated.** The GUI exists only in builds carrying `desktop`
> (which requires CGO for native window integration). Without it,
> `cmd/qrdb/gui_stub.go` returns `GUIUnavailableError` and the binary falls
> through to the CLI.

## Problem

Two audiences want a graphical view of the same server: an operator on a
desktop, and a browser reaching a remote daemon. Building two frontends means
two implementations of every view drifting apart.

## Shape chosen

**One Svelte 5 frontend, one TypeScript SDK, two transports underneath it.**

```
+--------------------------------------------------------------+
|                 Svelte 5 Frontend (universal)                 |
|                                                               |
|  Views/Components --> Stores --> TypeScript SDK               |
|                                  (@qrdb/sdk)                  |
|                          +-----------+-----------+            |
|                          |                       |            |
|                   WailsClient            RestClient           |
|                          |                       |            |
+--------------------------+-----------+-----------+------------+
                           |                       |
  Desktop (Wails):         |           Browser:    |
  Wails IPC --> Go bindings|           fetch() --> qrdbd REST API
        |                  |                       |
        v                  |                       v
    Go sdk/go              |               pkg/api/rest/ (+ CORS)
        |                  |                       |
        v                  |                       v
    qrdbd server <---------+                   qrdbd server
```


**Desktop flow:** Svelte → TS SDK (`WailsClient`) → Wails IPC → Go bindings →
Go SDK → embedded `qrdbd` server.

**Browser flow:** Svelte → TS SDK (`RestClient`) → `fetch()` + JSON → the
`qrdbd` REST API.

The frontend is a **single Svelte 5 codebase that runs in both environments**.
`@qrdb/sdk`'s `getClient()` factory auto-detects the runtime and returns the
right client.

### Alternatives rejected

- **Two frontends.** Rejected on the problem above.
- **A GUI-specific server API.** Rejected: the browser path uses the same REST
  API every other client uses, with CORS. The desktop path uses the same Go SDK.
  There is no privileged GUI surface.
- **A login screen.** Rejected outright — **authentication is mTLS at the TLS
  handshake**. The identity in the GUI header is read from the client
  certificate's Subject CN. A password box would be a second, weaker
  authentication path.
- **Shipping frontend assets as files.** Rejected: `make build-client` embeds
  everything with `//go:embed`, so the binary needs nothing external at runtime.

## Display detection

Whether the GUI launches when the binary is invoked with no CLI arguments:

| Platform | Logic |
|---|---|
| Linux | `DISPLAY` or `WAYLAND_DISPLAY` is set |
| Darwin | always, **unless `SSH_TTY` is set** (an SSH session) |
| Windows | always |

Source: `cmd/qrdb/display_{linux,darwin,windows}.go`. The Darwin SSH check is
the one that matters in practice: it stops a remote session trying to open a
window.

## App shell state machine

The root `App.svelte` component manages application lifecycle as a state
machine:

```
loading --> wizard --> unseal --> main app
  |           |
  |           +--> (first run: setup wizard)
  |
  +--> unseal --> main app  (returning user)
```

| State     | Condition                                | View                     |
|-----------|------------------------------------------|--------------------------|
| `loading` | Initial load, fetching capabilities      | Splash/loading spinner   |
| `wizard`  | First run (`isFirstRun()` returns true)  | Setup wizard             |
| `unseal`  | Server is sealed                         | Unseal prompt            |
| `main`    | Server is unsealed and connected         | Full application         |


## Setup wizard

The setup wizard guides first-time users through server configuration.
The flow branches based on the selected server mode:

```
ServerMode --> [Embedded] --> StorageBackend --> [Raft] --> RaftConfig
                                                       --> EncryptionBarrier
                                                       --> Summary
                                                       --> Initializing
                                                       --> Complete

ServerMode --> [Remote] --> ConnectionConfig --> Summary --> Complete
```

| Step                | Description                                          |
|---------------------|------------------------------------------------------|
| `ServerMode`        | Choose embedded (in-process) or remote server        |
| `StorageBackend`    | Select PebbleDB or memory storage                   |
| `RaftConfig`        | Configure shard ID, replica ID, peers                |
| `EncryptionBarrier` | Configure Shamir shares and threshold                |
| `ConnectionConfig`  | Enter remote server address and TLS settings         |
| `Summary`           | Review configuration before applying                 |
| `Initializing`      | Start embedded server (shows progress)               |
| `Complete`          | Wizard finished, transition to unseal or main        |


## Component boundaries

| Piece | Location | Owner |
|---|---|---|
| Svelte 5 frontend | `cmd/qrdb/frontend` | go-qrdb |
| TypeScript SDK `@qrdb/sdk` | `sdk/typescript` | go-qrdb |
| Wails desktop shell and Go bindings | `cmd/qrdb/gui_desktop.go`, `bindings_*.go` | go-qrdb |
| Non-desktop stub | `cmd/qrdb/gui_stub.go` | go-qrdb |
| Display detection | `cmd/qrdb/display_*.go` | go-qrdb |
| The server both paths reach | `pkg/api/rest` (+ CORS), `sdk/go` | go-qrdb |
| Wails v2 | — | upstream |

## Trust boundaries

- **No login screen; authentication is mTLS.** The header identity comes from
  the certificate Subject CN.
- **The browser path is ordinary REST with CORS** — no privileged GUI-only
  endpoints exist, so the GUI cannot do anything a CLI user with the same
  certificate could not.
- **The desktop path runs an embedded server in process**, which means the
  operator's machine holds the data directory and the barrier. Treat a desktop
  deployment as a single-node server, because it is one.
- Views are **capability-gated**: what the frontend shows follows
  `admin capabilities`, not a hardcoded list.

## What this feature deliberately does not do

- **No password authentication.**
- **No GUI-only API.**
- **No GUI in a non-`desktop` build** — the stub returns `GUIUnavailableError`
  and the CLI runs instead.

## Companion documents

- [typescript-sdk/README.md](../typescript-sdk/README.md) — `@qrdb/sdk` package
  reference: client interface, service interfaces, factory, transports, watch
  streaming, error types, npm packaging.
- [gui/README.md](../gui/README.md) — the fuller GUI reference including the
  view inventory and Wails integration detail.
