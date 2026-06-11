<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# GUI System

The `qrdb` binary serves as both a CLI and a desktop GUI application. When
launched without arguments on a system with a graphical display, it opens
the desktop GUI powered by Wails v2 and Svelte 5. When launched with
arguments (or without a display), it behaves as the standard CLI.

## Architecture

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

**Desktop flow:** Svelte -> TS SDK (WailsClient) -> Wails IPC -> Go bindings -> Go SDK -> qrdbd embedded server

**Browser flow:** Svelte -> TS SDK (RestClient) -> fetch() + JSON -> qrdbd REST API

The frontend is a single Svelte 5 codebase that runs in both environments.
The TypeScript SDK (`@qrdb/sdk`) provides a `getClient()` factory that
auto-detects the runtime environment and returns either a `WailsClient`
(desktop IPC) or `RestClient` (HTTP fetch).

## Build Tags

| Tag        | File                         | Behavior                                  |
|------------|------------------------------|-------------------------------------------|
| `desktop`  | `cmd/qrdb/gui_desktop.go`   | Full Wails v2 GUI with embedded assets    |
| `!desktop` | `cmd/qrdb/gui_stub.go`      | Returns `GUIUnavailableError`, falls through to CLI |

The `desktop` build tag requires CGO for native window integration.

## Display Detection

Platform-specific display detection determines whether the GUI should
launch when no CLI arguments are provided.

Source: `cmd/qrdb/display_{linux,darwin,windows}.go`

| Platform | Detection Logic                                             |
|----------|-------------------------------------------------------------|
| Linux    | `DISPLAY` or `WAYLAND_DISPLAY` environment variable is set  |
| Darwin   | Always true unless `SSH_TTY` is set (SSH session)           |
| Windows  | Always true                                                 |

## Entry Point

Source: `cmd/qrdb/main.go`

```go
func main() {
    if len(os.Args) == 1 && hasDisplay() {
        if err := launchGUI(); err != nil {
            // falls through to CLI on GUIUnavailableError
        } else { return }
    }
    rootCmd := newRootCmd()
    rootCmd.Execute()
}
```

When the binary is compiled without `desktop`, `launchGUI()` returns a
`GUIUnavailableError` and the CLI takes over transparently.

## Wails Integration

Source: `cmd/qrdb/gui_desktop.go`

The desktop build embeds the compiled frontend assets:

```go
//go:embed all:frontend/dist
var assets embed.FS
```

The Wails application registers 25 Go binding objects, each wrapping a
domain of the Go SDK (the authoritative list is `guiBindings.bindList()`
in `cmd/qrdb/gui_desktop_bindings.go`, pinned by
`TestGUIBindings_AllBindingsPresent`):

| Binding                    | Domain                  | Service Interface Used       |
|----------------------------|-------------------------|------------------------------|
| `ConnectionBinding`        | Transport lifecycle     | `sdk.Client`                 |
| `ConnectionManagerBinding` | Connection profiles     | `sdk.Client` (multi-profile) |
| `EmbeddedServerBinding`    | In-process server       | `server.Server`              |
| `KVBinding`                | Key-value CRUD          | `sdk.KVClient`               |
| `AdminBinding`             | Cluster admin           | `sdk.AdminClient`            |
| `MVCCBinding`              | MVCC operations         | `sdk.MVCCClient`             |
| `TransactionBinding`       | Transactions            | `sdk.TransactionClient`      |
| `BatchBinding`             | Batch operations        | `sdk.BatchClient`            |
| `WatchBinding`             | Real-time events        | `sdk.WatchClient`            |
| `LeaseBinding`             | Distributed leases      | `sdk.LeaseClient`            |
| `IndexBinding`             | Secondary indexes       | `sdk.IndexClient`            |
| `TimeSeriesBinding`        | Time-series data        | `sdk.TimeSeriesClient`       |
| `VectorBinding`            | Vector database         | `sdk.VectorClient`           |
| `UserBinding`              | User management         | `sdk.UserClient`             |
| `RoleBinding`              | Role management         | `sdk.RoleClient`             |
| `CertBinding`              | Certificates            | `sdk.CertClient`             |
| `DatabaseBinding`          | Database management     | `sdk` Database client        |
| `CollectionBinding`        | Collection management   | `sdk` Collections client     |
| `SQLBinding`               | SQL queries             | SQL engine                   |
| `SealBinding`              | Seal/unseal             | `sdk.SealClient`             |
| `ConfigBinding`            | App config state        | Local file I/O               |
| `TenantBinding`            | Tenant management       | `sdk` Tenants client         |
| `BackupBinding`            | Backup/restore          | `sdk` Backup client          |
| `OfficerBinding`           | Security Officer ceremony | `sdk` Barrier + Tenants clients |
| `DaemonBinding`            | Local qrdbd lifecycle   | `os/exec` process control    |

`OfficerBinding` exposes the Security Officer share-distribution
ceremony to the desktop GUI: system and tenant officer
enrollment (CSR-based), claim status, the two-step
challenge/signature share claim, quorum seal votes, and the
recovery-officer mirror. PEM material crosses the Wails IPC boundary as
plain strings (local DTOs convert to/from the transport's `[]byte`
fields) so certificates and CSRs are not base64-mangled by
`encoding/json`. See [../seal/shamir.md](../seal/shamir.md) for the
ceremony contract.

Watch events are delivered to the frontend in real time via Wails
`runtime.EventsEmit`, which the Svelte watch store subscribes to using
the Wails runtime event listener.

## Frontend Structure

Source: `cmd/qrdb/frontend/`

| Technology   | Version | Purpose                                    |
|--------------|---------|--------------------------------------------|
| Svelte       | 5.x     | Reactive UI framework with runes           |
| Vite         | 6.x     | Build toolchain and dev server             |
| TypeScript   | 5.7+    | Type safety across all frontend code       |
| TailwindCSS  | 4.1+    | Utility-first CSS framework                |
| DaisyUI      | 5.x     | Component library for Tailwind             |

```
cmd/qrdb/frontend/
  package.json               - qrdb-frontend (private)
  vite.config.ts             - Vite + Svelte + Tailwind plugins
  tsconfig.json              - Strict TypeScript configuration
  src/
    main.ts                  - Application entry point
    App.svelte               - Root component, app shell state machine
    stores/
      connection.svelte.ts   - Transport connection state
      capabilities.svelte.ts - Server feature flags
      identity.svelte.ts     - Current user identity from mTLS
      wizard.svelte.ts       - Setup wizard flow state
      seal.svelte.ts         - Seal/unseal lifecycle state
      toast.svelte.ts        - Notification toast queue
      theme.svelte.ts        - Light/dark theme preference
      watch.svelte.ts        - Real-time watch event store
```

Stores use Svelte 5 runes (`$state`, `$derived`, `$effect`) for reactive
state management.

The frontend depends on the TypeScript SDK as a local npm package:

```json
{ "dependencies": { "@qrdb/sdk": "file:../../../sdk/typescript" } }
```

## App Shell State Machine

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

## Setup Wizard

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

## Views

### User Area

| View           | Description                              | Capability Gate       |
|----------------|------------------------------------------|-----------------------|
| KV Explorer    | Browse, search, create, edit key-values  | Always available      |
| MVCC Explorer  | Revision history, historical reads       | `sm_type == "mvcc"`   |
| Transactions   | Build and execute CAS transactions       | `sm_type == "mvcc"`   |
| Batch          | Compose and submit batch operations      | Always available      |
| Watch          | Real-time key change event stream        | `sm_type == "mvcc"`   |
| Leases         | Grant, revoke, keep-alive, TTL monitor   | `sm_type == "mvcc"`   |
| Indexes        | Create, drop, query secondary indexes    | `sm_type == "mvcc"`   |
| TimeSeries     | Write, query, aggregate time-series data | `capabilities.timeSeries` |
| Vectors        | Namespace CRUD, search, index management | `capabilities.vector` |

### Admin Area

| View             | Description                                    |
|------------------|------------------------------------------------|
| Dashboard        | Cluster health, node status, shard overview    |
| Cluster          | Peer membership, add/remove members            |
| Databases        | Create, drop, configure databases and protocols |
| Seal Manager     | Initialize, seal, unseal, rotate, rekey; for Shamir barriers a Security Officers panel handles CSR enrollment, claim status, and quorum seal votes (single-admin seal is blocked) |
| Tenants          | Tenant lifecycle, per-tenant barrier strategy and officer management |
| Users            | Create, edit, enable/disable user accounts     |
| Roles            | Create, edit, assign/unassign roles            |
| Certificates     | Generate, export, revoke client certificates   |
| Audit Log        | Browse and search audit trail entries          |
| Settings         | Server configuration and preferences           |
| Daemon           | Start/stop the local `qrdbd` process; shown only for **external** profiles on desktop builds — embedded ("Local Server") profiles run in-process and hide this panel |

#### Database Protocol Configuration

The Databases admin view includes protocol toggles for each database.
Protocols can be enabled or disabled per database:

| Protocol   | Toggle                  | Additional Fields                    | Capability Gate        |
|------------|-------------------------|--------------------------------------|------------------------|
| MySQL      | Enable/disable switch   | Port                                 | `capabilities.mysql`   |
| PostgreSQL | Enable/disable switch   | Port                                 | `capabilities.postgresql` |
| etcd       | Enable/disable switch   | Listen address, advertise URLs       | `capabilities.etcd`    |

Protocol toggles are only visible when the corresponding build-tag
capability is reported by the server. For example, the etcd toggle
appears only when the server binary was compiled with
`WITH_QRDB_ETCD=1`.

## Authentication

No login screen exists. Authentication is handled entirely via mTLS
client certificates at the TLS handshake level. The identity displayed
in the GUI header is extracted from the client certificate Subject CN.

## Makefile Targets

```
make install-frontend    # npm install for TypeScript SDK + frontend
make build-frontend      # Build TypeScript SDK + Vite frontend
make build-client        # Build client binary in devcontainer (CLI + GUI)
make dev-gui             # Wails dev mode with hot reload
```

The `build-client` target produces a single binary with all frontend assets
embedded via `//go:embed`. No external files are needed at runtime.

## Cross-References

- [TypeScript SDK](../typescript-sdk/README.md) -- `@qrdb/sdk` package documentation
- [CLI Reference](../cli/README.md) -- CLI command tree (same binary, different mode)
- [Go SDK](../sdk/README.md) -- Go SDK used by Wails bindings
- [Configuration](../12-configuration.md) -- Server configuration reference
- [Seal Package](../seal/README.md) -- Seal/unseal lifecycle
- [Authentication](../auth/README.md) -- mTLS identity extraction
