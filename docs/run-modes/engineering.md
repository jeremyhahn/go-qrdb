# Run Modes — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go/node`:

```go
func NewStandaloneNode(...) (*Node, error)   // standalone composition
func NewClusteredNode(...)  (*Node, error)   // raft composition

func (n *Node) Server()      *server.Server   // the started multi-protocol server
func (n *Node) Client()      *sdk.Client      // in-process SDK client
func (n *Node) Services()    *lifecycle.Node  // aggregated service instances
func (n *Node) KVService()    service.KVServicer
func (n *Node) AdminService() service.AdminServicer
func (n *Node) Close()       error            // tears down in reverse order
```

`pkg/capabilities`:

```go
func Register(feature string)        // called from tag-gated init()
func Seal(profile ...)               // composition root seals the effective profile
func Get(feature string) bool        // pre-seal, falls back to compile-time
```

The daemon selects the mode with `mode: standalone | raft` in YAML or
`--mode standalone`; **absent means raft**, for full backward compatibility.

## Invariants

- **One canonical composition per mode.** SDK `node://`
  (`node.NewStandaloneNode`), the flat `embedded://` driver, and the daemon all
  share it (`lifecycle.NewLocalStorage` / `NewLocalEngine` →
  `backend.LocalBackend`). There is no second standalone assembly.
- **Precedence is build tag > run mode > config.** Config narrows only. Asking
  config to enable what the mode forbids or the binary lacks is a **validation
  error**, not a silent no-op.
- **`pkg/capabilities` is the single runtime truth.** At boot the composition
  root seals the effective profile (build tags ∩ mode ∩ config) via
  `capabilities.Seal(cfg.EffectiveCapabilities())`. `admin capabilities` reports
  it faithfully, including a `mode` field.
- **A `false` from the capability registry is three-way ambiguous** by design:
  not compiled, mode-gated, or config-disabled. The `mode` field plus the build
  is what disambiguates.
- **Before sealing, `Get` keeps the compile-time fallback** so a library
  embedding that never seals still gets correct answers.
- **There is no `qrdb_raft` build tag.** Raft is always compiled; the mode
  selects it.
- **Raft-only refusals are typed and name the remedy** — `requires mode: raft` —
  on every transport, not a generic error.
- **Standalone skips the raft identity requirements**; only `raft.data_dir`
  stays mandatory.

## Capability gates

errors, not silent no-ops).

| Gate | Question | Mechanism |
|---|---|---|
| Build tag | Compiled in? | Tag-gated `init()` → `capabilities.Register` (existing: `qrdb_sql`, `qrdb_mysql`, `qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`, `codec_*`) |
| Run mode | Structurally possible? | `mode: standalone \| raft` (implemented in config, daemon, and SDK); raft-only features are not composed in standalone |
| Config | Operator-enabled? | Existing sections plus `enabled` on `mvcc`/`watch`/`lease` to disable even in raft mode (implemented) |

Build tags currently registering capabilities: `qrdb_sql`, `qrdb_mysql`,
`qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`, plus the
`codec_*` family.

## Standalone: what is and is not served

**Served** — standard database operations:

- KV (get/put/delete/range/scan), batch, secondary indexes
- Collections and entity CRUD
- Database management (create/list/delete; engines kv, timeseries, vector)
- SQL (build-tag gated, `qrdb_sql`)
- Timeseries and vector (build-tag gated; require the MVCC-capable engine, so
  unavailable over the file/objstore backends — the engine boundary is in
  `pkg/engine/kv_engine.go`)
- Plus the always-compiled core below

**Refused with `requires mode: raft`** — everything consensus-shaped: MVCC
revisions, watch, lease, lock, transactions, etcd compatibility, and cluster
operations.

## The always-compiled core

security posture or breaks a dependency of everything else:

| Core component | Why it cannot be optional |
|---|---|
| `pkg/capabilities` | The registry every feature reports through |
| KV + batch + collections (`pkg/kv`, `pkg/kvstore`, engine layer) | Baseline data plane; every feature and the IAM stores depend on it |
| Database manager (`pkg/database`, `pkg/dao`) | Nothing opens without it |
| `pkg/config`, logger, `pkg/errors`, `pkg/firstboot` | Process cannot boot or bootstrap without them |
| Storage backends memory/file/pebble (`pkg/storage`) | No storage, no database; seal + IAM ride them |
| Auth: mTLS, RBAC, IAM stores (`pkg/auth`, `pkg/user`, `pkg/role`, `pkg/permission`) | An unauthenticated binary violates the security posture |
| Seal barrier + credentials (`pkg/seal`, `pkg/credentials`) | At-rest protection is non-negotiable; IAM depends on the barrier |
| Audit (`pkg/audit`) | Compliance posture; services emit unconditionally |
| REST + gRPC transports | A server with no transport is unusable; enabled per `protocols.*` |


## Storage selection in raft mode

`raft.sm_storage` selects the backend the storage-generic state machines
(`sm_type: disk` or `fast`) compose: `pebble` (default), `file`, or `memory`.

- **The file backend earned the hot-apply role with real durability**: atomic
  temp-file + rename + fsync writes and an all-or-nothing commit journal
  replayed at open (`pkg/storage/file/journal.go`), plus the `storage.Syncer`
  seam.
- **Objstore is a snapshot/backup tier** and is **refused as a hot apply path**
  with a pointed validation error.
- **The pebble-native state machines (`pebble`, `mvcc`) carry their own
  dedicated Pebble store** and reject a non-pebble `sm_storage`.

## Configuration

```yaml
mode: raft   # "standalone" | "raft"; absent = raft (full back-compat)
```

- `mode: standalone` + explicit `mvcc.enabled`/`watch.enabled`/
  `lease.enabled: true`, `raft.join`, or a multi-member `raft.peers` list →
  validation error: `requires mode: raft (standalone serves standard
  database operations only)`. Standalone skips the raft identity
  requirements (only `raft.data_dir` stays mandatory). Etcd is enabled
  per-database at runtime, so its mode gate lives at service composition.
- Enabling a wire protocol whose build tag is absent → boot-time error
  naming the tag (`Config.ValidateCapabilities`, called by composition
  roots — a deliberate separate gate so pure-config validation stays
  build-independent).
- The daemon serves `mode: standalone` natively: raft-only endpoint
  families answer 501/Unimplemented with the `requires mode: raft` hint
  on every transport (nil singletons plus the gated per-database service
  registry, `pkg/server/standalone_gate.go`), while the SQL catalog keeps
  its internal MVCC/transaction plumbing.
- Refusals at runtime surface through the existing per-transport typed
  errors (REST 501 / gRPC & QUIC Unimplemented) with the
  `requires mode: raft` hint.


## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Raft-only call in standalone | typed error, `requires mode: raft`; REST 501, gRPC/QUIC `Unimplemented` | **yes** |
| `mode: standalone` with `mvcc.enabled`/`watch.enabled`/`lease.enabled: true`, `raft.join`, or multi-member `raft.peers` | validation error at boot | **yes** |
| Wire protocol enabled whose build tag is absent | boot-time error **naming the tag** (`Config.ValidateCapabilities`) | **yes** |
| Objstore configured as `raft.sm_storage` | validation error | **yes** |
| Non-pebble `sm_storage` with a pebble-native state machine | rejected | **yes** |

`Config.ValidateCapabilities` is a deliberately separate gate from ordinary
config validation, so pure-config validation stays build-independent.

## Behavior change notice

SDK standalone (`NewStandaloneNode`) historically composed MVCC, lease,
and transaction services over `LocalBackend`. Under this requirement those
become raft-mode-only everywhere; standalone compositions stop building
them. Downstream consumers are notified via Gitea issues on their repos.

## Test strategy

- **Unit**: mode validation for each rejected combination, capability sealing
  (build ∩ mode ∩ config), the pre-seal compile-time fallback, and the
  standalone gate's refusal on every raft-only endpoint family.
- **The refusal tests are the important ones**: they are what stops a
  raft-only feature silently degrading rather than erroring.
- **Integration**: the standalone stage runs the standalone composition; the
  cluster stage runs raft, and is **not currently wired into CI**.

## Observability

`qrdb admin capabilities` is the authoritative runtime report: the effective
profile plus a `mode` field. It is the first thing to check when a feature is
"missing".
