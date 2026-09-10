# Run Modes — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

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
- **Every no-argument `*Node` accessor performs a race-free read** from any
  goroutine at any point — a fully constructed value or nil, never a torn one.
  **`Close` itself is not synchronised against `Unseal` or `EnableEncryption`**;
  callers must not overlap them. See *Node accessor concurrency* below.

## Node accessor concurrency

Two things are true, and only the first is a guarantee. Keeping them apart is
the point of this section.

### Guaranteed: accessor reads are race-free

Every **no-argument exported accessor** on `*Node` — `Client`, `Server`,
`Services`, `KVService`, `AdminService`, `RebalanceService`, `UserService`,
`AuthFactorService`, `UserAuthFactorService`, `ABACPolicyReloader`,
`BarrierRegistry`, `IsSealed` — performs a race-free read from any goroutine at
any point. It returns a fully constructed value or nil, never a torn or
unsynchronised one.

This matters because a node built with a barrier that needs operator input is
returned to the embedder **before its data plane exists**: `IsSealed()` is true
and the data-plane accessors return nil. `Services()` is the near-exception —
it returns the seal-only `lifecycle.Node` the constructor built, with `Barrier`,
`Ceremony` and `Credential` populated and the data-plane services nil — but the
same boot republishes it with the full graph, so it is inside this contract.
Those documented nils are an invitation to hold the node in one goroutine and
poll it while a second drives `Unseal` — the sealed-window shape every embedder
uses (`ServeSealedWindow` on one goroutine, the operator's unseal on another).

### NOT guaranteed: `Close` is not synchronised against boot or migration

`Node` has no lifecycle state. Two interleavings are therefore **undefined**,
and callers must not create them:

- **`Close` concurrent with `Unseal`.** `Close` retracts the handles, then the
  in-flight boot's `Store` calls republish them, and the node looks open again
  after `Close` returned.
- **`Close` concurrent with `EnableEncryption`.** The migration reloads
  `barrier` and `barrierRegistry` after `Close` has nilled them, and `Reopen`
  can rebuild the data plane after shutdown.

Neither is a torn read — the reads are still atomic — so this is an **ordering**
gap, not the publication defect the accessors fixed. Closing it needs an
explicit lifecycle state plus a ruling on what `Close` means for an embedded
`Node` (block on an in-flight `Unseal`, or start failing it and with which
typed error). That ruling is the owner's, and the gap is deliberately left
open until it is made.

### Mechanism

Every such handle lives in an `atomic.Pointer` on `Node`, written by
`wireClientAndServer` (or, for the service graph, by `completeStandaloneBoot` /
`completeClusteredBoot`; the seal handles by the constructor) and retracted by
`Close` / `teardownDataPlane`. Interface-typed handles are boxed, so the
pointer holds `*Iface`.

`atomic.Pointer` rather than a mutex because each handle is an independent
single-word publication with no invariant spanning two of them, and reads
outnumber the single write by orders of magnitude — the same reasoning
`pkg/database`, `pkg/secure` and `pkg/storage/pool` apply to their swappable
handles. A mutex is what the **lifecycle** gap above will need; it is not what a
per-handle publication needs.

### Retraction, within those limits

For a caller that does not overlap `Close` with a boot, `Close` retracts every
handle it published before stopping what backs them, so every accessor reads
nil afterwards rather than handing out a live-looking handle onto a stopped
service graph.

The in-process encryption-migration recycle (`EnableEncryption` →
`teardownDataPlane`) retracts the handles owned by the service graph it stops —
the custodian factor service, the login factor service, the ABAC reload seam —
and `wireClientAndServer` republishes them on the rebuild. The
Shamir-committed branch of `Reopen` never reaches that rebuild (it returns
sealed, pending a Security Officer quorum), and leaving handles published
through *that* window is precisely the stale-handle hazard this closes.

The scoped encryption router (`Node.encMigrator`, reached through
`EnableEncryption`) is the deliberate exception: it is **not** retracted by
`teardownDataPlane`. Its tenant leg reaches `TenantHostRegistry` and
`RaftBackendFactory`, which survive a system-scope recycle by design — tenant
migrations use a per-tenant guard and do not contend on `migrateMu`. Clearing
it mid-recycle would turn a concurrent tenant-scope migration, which today
proceeds independently, into a typed "not yet fully booted" refusal. `Close`
clears it directly instead, where no rebuild follows.

The router's tenant leg is wired with a `nodeTenantResolver`
(`sdk/go/node/migrate_encryption.go`), not a captured `svcNode.Tenant`: it
resolves `n.svcNode.Load().Tenant` on every call, so a concurrent tenant-scope
migration during the window always reaches whichever tenant service is
currently live — including the seal-window-only service `Reopen`'s
Shamir-committed branch builds while waiting on the Security Officer quorum —
rather than the one that existed when the router was constructed.

A DIFFERENT, still-open instance of the same capture-once shape lives one
layer over: `TenantHostRegistry` (`n.tenantHosts`), built once by
`BuildTenantInfra` inside `completeStandaloneBoot` and guarded by
`n.tenantHosts == nil` so it is never rebuilt on a later system-scope
recycle, also captures a `*tenant.Service` at that first-boot call — and that
one is NOT resolved lazily. A tenant-scope migration issued after ANY
system-scope recycle has completed (not only the Shamir-sealed window above)
therefore fails `TenantHostRegistry.MigrateEncryption`'s `dataRootOf` lookup
against the pre-recycle, stopped instance — the identical "storage is
closed" symptom the router's lazy resolution fixes. This was found, not
theorized, while writing that fix's regression test
(`sdk/go/node/migrate_tenant_concurrent_test.go`), and is out of scope
there: it is a `BuildTenantInfra`/`TenantHostRegistry` defect, not a
`server.NewScopedEncryptionMigrator` one, and is tracked separately.

### What none of this promises

It governs **acquiring** a handle, never **using** one. Calling through a handle
you already hold, concurrently with `Close`, is a use-after-close; the node has
never promised to survive that and still does not. The nil-while-sealed
contract is likewise unchanged — this makes the transition safe, not different.

### Verification

The regression tests enumerate the accessors by **reflection over the type**,
not by hand. A hand-list stops covering the next accessor someone adds, which
is exactly how `IsSealed` and `BarrierRegistry` were missed on the first pass.
`Node.encMigrator` has no no-argument accessor and so cannot be reached
reflectively; it is asserted directly.

Measured: two independent `-race` sightings from a consumer
(`Read at Client()` vs `Previous write at wireClientAndServer()`), the second
reproduced at a clean base, which is what attributed the defect here rather
than to the consumer.

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
- Transactions / compare-and-swap
- Plus the always-compiled core below

**Refused with `requires mode: raft`** — everything consensus-shaped: MVCC
revisions, watch, lease, lock, etcd compatibility, and cluster operations.

**Transactions are the exception and are NOT refused** (owner ruling
2026-08-23). This list carried them among the refusals; corrected to match
`raftOnlyGatedRegistry` in `pkg/server/standalone_gate.go`, which refuses
`MVCCService`, `WatchService` and `LeaseService` and passes
`TransactionService` through. A standalone node is engine-direct and
single-process, so its compare-and-swap is already linearizable — consensus is
what makes a swap linearizable across *replicas*. The etcd-style `Txn` endpoint
is still refused, under etcd compatibility.

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
and transaction services over `LocalBackend`. Under this requirement **MVCC and
lease** become raft-mode-only everywhere; standalone compositions stop building
them. Downstream consumers were notified on their own repositories.

**Transactions did not become raft-only** (owner ruling 2026-08-23), and this
notice said they did — corrected to match the shipped composition. Standalone
still serves the transaction / compare-and-swap family on every transport,
in-process and on the wire; `pkg/server/standalone_gate.go` passes
`TransactionService` through by name and says why.

## Test strategy

- **Unit**: mode validation for each rejected combination, capability sealing
  (build ∩ mode ∩ config), the pre-seal compile-time fallback, and the
  standalone gate's refusal on every raft-only endpoint family.
- **The refusal tests are the important ones**: they are what stops a
  raft-only feature silently degrading rather than erroring.
- **Accessor publication is verified under `-race` only.** An unsynchronised
  word read almost never observes a stale value on amd64/arm64, so
  `sdk/go/node`'s `TestNode_AccessorsRaceFreeDuringUnseal` and
  `TestNode_AccessorsRaceFreeDuringClose` are evidence only when the detector
  is on; `make test-sdk` runs them that way (`GOTEST` carries `-race`).
- **Retraction is verified with the optional handles populated.**
  `TestNode_CloseRetractsEveryHandle_Configured` boots with ABAC live reload so
  `ABACPolicyReloader` and the login factor service are non-nil *before*
  `Close`; asserting nil afterwards on a node that never populated them would
  be vacuous.
- **Integration**: the standalone stage runs the standalone composition; the
  cluster stage runs raft, and is **not currently wired into CI**.

## Observability

`qrdb admin capabilities` is the authoritative runtime report: the effective
profile plus a `mode` field. It is the first thing to check when a feature is
"missing".
