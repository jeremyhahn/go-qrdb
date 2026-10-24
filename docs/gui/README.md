# GUI System

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion to `docs/desktop-gui/`, which is the contract.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

The `qrdb` binary serves as both a CLI and a desktop GUI application. When
launched with no subcommand (log flags only) on a system with a graphical
display, it opens the desktop GUI powered by Wails v2 and Svelte 5.
Otherwise it behaves as the standard CLI.

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
launch when no subcommand is given.

Source: `cmd/qrdb/display_{linux,darwin,windows}.go`

| Platform | Detection Logic                                             |
|----------|-------------------------------------------------------------|
| Linux    | `DISPLAY` or `WAYLAND_DISPLAY` environment variable is set  |
| Darwin   | Always true unless `SSH_TTY` is set (SSH session)           |
| Windows  | Always true                                                 |

## Entry Point

Source: `run()` in `cmd/qrdb/main.go`

```go
if isGUIOnly() && hasDisplay() {
    if err := launchGUI(log, file); err != nil {
        // a *GUIUnavailableError falls through to the CLI; any other error exits 1
    } else {
        return 0
    }
}
rootCmd := newRootCmd()
rootCmd.ExecuteContext(ctx)
```

`isGUIOnly()` is true when the arguments are log flags only. When the binary
is compiled without `desktop`, `launchGUI()` returns a `GUIUnavailableError`
and the CLI takes over transparently.

## Wails Integration

Source: `cmd/qrdb/gui_desktop.go`

The desktop build embeds the compiled frontend assets:

```go
//go:embed all:frontend/dist
var assets embed.FS
```

The Wails application registers 31 Go binding objects, each wrapping a
domain of the Go SDK (the authoritative list is `guiBindings.bindList()`
in `cmd/qrdb/gui_desktop_bindings.go`, pinned by
`TestGUIBindings_AllBindingsPresent`):

| Binding                    | Domain                  |
|----------------------------|-------------------------|
| `ConnectionBinding`        | Transport lifecycle     |
| `ConnectionManagerBinding` | Connection profiles     |
| `EmbeddedServerBinding`    | In-process server       |
| `KVBinding`                | Key-value CRUD          |
| `AdminBinding`             | Cluster admin           |
| `MVCCBinding`              | MVCC operations         |
| `TransactionBinding`       | Transactions            |
| `BatchBinding`             | Batch operations        |
| `WatchBinding`             | Real-time events        |
| `LeaseBinding`             | Distributed leases      |
| `IndexBinding`             | Secondary indexes       |
| `TimeSeriesBinding`        | Time-series data        |
| `VectorBinding`            | Vector database         |
| `UserBinding`              | User management         |
| `InvitationBinding`        | User invitations        |
| `RoleBinding`              | Role management         |
| `CertBinding`              | Certificates            |
| `DatabaseBinding`          | Database management     |
| `CollectionBinding`        | Collection management   |
| `SQLBinding`               | SQL queries             |
| `SealBinding`              | Seal/unseal             |
| `ConfigBinding`            | App config state        |
| `TenantBinding`            | Tenant management       |
| `BackupBinding`            | Backup/restore          |
| `OfficerBinding`           | Share holders           |
| `CeremonyBinding`          | Initialization ceremony |
| `RebalanceBinding`         | Shard rebalancing       |
| `LockBinding`              | Row locks               |
| `CredentialBinding`        | Named credentials       |
| `AuditBinding`             | Audit log               |
| `DaemonBinding`            | Local qrdbd lifecycle   |

`OfficerBinding` exposes share-holder operations to the desktop GUI for
the system barrier (unseal and recovery domains), tenant barriers and
database barriers: `RegisterHolder`, `ListHolders`, `ClaimEnvelope` (signs
the claim nonce with the holder's key in the application process),
`SubmitShare` (answers the `mtls` factor, opens the envelope and submits the
share), `StartRekey`, `RevokeHolder`, `InitializeDatabaseShamir` and
`UnenrolDatabaseHolder`. Keys passed in are wiped after each call; only
factor responses and the opened share leave the process. See
[../shamir-custody/](../shamir-custody/) for the contract.

Watch events are delivered to the frontend in real time via Wails
`runtime.EventsEmit` on the event `qrdb:watch:<id>`, which the TypeScript
SDK's `WailsClient` subscribes to with `EventsOnMultiple`.

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
  package.json                   - qrdb-frontend (private)
  vite.config.ts                 - Vite + Svelte + Tailwind plugins
  tsconfig.json                  - TypeScript configuration
  src/
    main.ts                      - Application entry point
    App.svelte                   - Root component, app shell state machine
    stores/
      connection.svelte.ts         - Transport connection state
      connection-manager.svelte.ts - Saved connection profiles
      capabilities.svelte.ts       - Server feature flags
      identity.svelte.ts           - Current user identity from mTLS
      setup-wizard.svelte.ts       - Server setup wizard flow state
      database-wizard.svelte.ts    - Database creation wizard flow state
      database.svelte.ts           - Database selection state
      navigation.svelte.ts         - Navigation state
      seal.svelte.ts               - Seal/unseal lifecycle state
      daemon.svelte.ts             - Local qrdbd daemon state
      toast.svelte.ts              - Notification toast queue
      theme.svelte.ts              - Light/dark theme preference
      watch.svelte.ts              - Real-time watch event store
```

Stores use Svelte 5 runes (`$state`, `$derived`, `$effect`) for reactive
state management.

The frontend depends on the TypeScript SDK as a local npm package:

```json
{ "dependencies": { "@qrdb/sdk": "file:../../../sdk/typescript" } }
```

## App Shell State Machine

The root `App.svelte` component manages application lifecycle as a state
machine (`AppState`):

| State     | Condition                                          | View                     |
|-----------|----------------------------------------------------|--------------------------|
| `loading` | Initial load: creating the client                  | Loading spinner          |
| `init`    | Browser mode, server sealed and not initialized    | Setup wizard (`ServerInitWizard`) |
| `unseal`  | Browser mode, server sealed and initialized        | Unseal prompt            |
| `app`     | Profiles loaded                                    | Full application (`MainApp`) |
| `error`   | Initialization failed                              | Error message            |

On desktop the shell goes from `loading` to `app`; `MainApp` handles empty
profiles, connection errors and seal flows.

## Setup Wizard

The setup wizard (`setup-wizard.svelte.ts`) guides first-time server
configuration. The flow branches on the selected server mode:

```
welcome --> serverMode --> [standalone] --> globalBarrier --> summary --> applying --> complete
                       --> [clustered]  --> clusterConfig --> globalBarrier --> ...
```

| Step            | Description                                              |
|-----------------|----------------------------------------------------------|
| `welcome`       | Introduction                                             |
| `serverMode`    | Choose `standalone` or `clustered`                       |
| `clusterConfig` | Clustered only: cluster type (`local` or `distributed`), peer discovery, replica ID and Raft address |
| `globalBarrier` | Barrier mode: `none`, `passphrase` or `shamir`           |
| `summary`       | Review configuration before applying                     |
| `applying`      | Apply the configuration (shows progress)                 |
| `complete`      | Wizard finished                                          |

## Views

### Database Workspace

An open database shows one tab per tool (`DatabaseWorkspace.svelte`). Every
database exposes every engine, so the tabs are not gated by engine:

| Tab          | Description                              |
|--------------|------------------------------------------|
| Data         | Browse, search, create, edit key-values  |
| Collections  | Collection management                    |
| Indexes      | Create, drop, query secondary indexes    |
| Transactions | Build and execute transactions           |
| Batch        | Compose and submit batch operations      |
| Watch        | Real-time key change event stream        |
| Leases       | Grant, revoke, keep-alive, TTL monitor   |
| Locks        | Row locks                                |
| Time Series  | Write, query, aggregate time-series data |
| Vectors      | Namespace CRUD, search, index management |
| SQL Browser  | Shown only when MySQL or PostgreSQL is enabled for the database |
| Settings     | Per-database settings, always last       |

### Admin Area

The admin navigation (`components/sidebar/AdminNav.svelte`):

| View             | Description                                    |
|------------------|------------------------------------------------|
| Dashboard        | Cluster health, node status, shard overview    |
| Cluster          | Peer membership, add/remove members; Raft mode only |
| Topology         | Shard topology; Raft mode only                 |
| Rebalance        | Shard rebalancing; Raft mode only              |
| Seal Manager     | Initialize, seal, unseal, rotate, rekey; for Shamir barriers a Security Officers panel handles CSR enrollment, claim status, and quorum seal votes (single-admin seal is blocked). When no encryption barrier is configured (strategy `none`) it shows an "Encryption Not Configured" panel — no Seal/Unseal controls, since there is nothing to seal — with [migration guidance](../seal/enabling-encryption.md) for enabling encryption |
| Tenants          | Tenant lifecycle, per-tenant barrier strategy and officer management |
| Users            | Create, edit, enable/disable user accounts     |
| Roles            | Create, edit, assign/unassign roles            |
| Certificates     | Generate, export, revoke client certificates   |
| Credentials      | Named credential management                    |
| Databases        | Create, drop, configure databases and protocols |
| Backups          | Backup and restore                             |
| Daemon           | Start/stop the local `qrdbd` process; shown only for **external** profiles on desktop builds — embedded ("Local Server") profiles run in-process and hide this panel |
| Audit Log        | Browse and search audit trail entries          |
| Settings         | Server configuration and preferences           |

#### Database Protocol Configuration

A database's detail view in the Databases admin view, and the workspace's
Settings tab, toggle the MySQL and PostgreSQL wire protocols for that
database. The etcd-compatible API is chosen when the database is created
(`StepDatabaseSQL.svelte` in the database wizard), for Raft databases only.

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

- [TypeScript SDK](../typescript-sdk/) -- `@qrdb/sdk` package documentation
- [CLI Reference](../api-server/cli.md) -- CLI command tree (same binary, different mode)
- [Go SDK](../api-server/engineering.md) -- Go SDK used by Wails bindings
- [Configuration](../12-configuration.md) -- Server configuration reference
- [Seal Package](../seal/README.md) -- Seal/unseal lifecycle
- [Authentication](../auth/README.md) -- mTLS identity extraction
