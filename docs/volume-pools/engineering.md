# Volume Pools — Engineering

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.
>
> The design choices it assumes are in [architecture.md](architecture.md).

## Exported surface

### `pkg/volume`

```go
// Provider realizes a pool's volume on one node. Implementations are safe for
// concurrent use.
type Provider interface {
	// Name is the id a pool's catalog record names ("dir", "luks2").
	Name() string
	// Open makes the volume available on this node and returns it. It is
	// idempotent: opening an open volume returns it again.
	Open(ctx context.Context, spec Spec) (Volume, error)
}

// Spec is everything a provider is told about one pool on one node. It never
// carries key material.
type Spec struct {
	Pool    string            // the pool name
	NodeID  string            // this node's identity, for per-node key lookup
	Path    string            // node configuration: the mount, or the directory
	Options map[string]string // node configuration: provider-specific options
}

// Volume is an opened pool volume on this node.
type Volume interface {
	// Root is the absolute, clean directory tenants are placed under.
	Root() string
	// Usage reports the volume's size and free space.
	Usage(ctx context.Context) (Usage, error)
	// Health reports the provider's view of the volume.
	Health(ctx context.Context) Health
	// Close releases the volume. Close of a closed volume returns nil.
	Close(ctx context.Context) error
}

// Lockable is implemented by a Volume whose provider can lock and unlock the
// underlying device in step with the node's system barrier (see
// architecture.md's "Encrypted volumes and the cluster seal"). The "dir"
// provider does not implement it.
type Lockable interface {
	// Unlock makes the volume's data readable, given the key the caller
	// derived from the now-unsealed system barrier with DeriveVolumeKey.
	Unlock(ctx context.Context, key VolumeKey) error
	// Lock makes the volume's data unreadable again. Takes no key: it is
	// the inverse of mapping the device available, not of decrypting it.
	Lock(ctx context.Context) error
}

// VolumeKey is a per-volume, per-node symmetric key. Never persisted:
// derived fresh at every unseal and wiped immediately after use.
type VolumeKey [32]byte

func (k *VolumeKey) Wipe()

// DeriveVolumeKey derives the key for the volume identified by nodeID and
// pool from the system barrier's unwrapped root key, with HKDF-SHA256 --
// the same primitive go-quicraft's EpochBarrier already uses for its own
// per-epoch data keys. masterKey is never retained by this function.
func DeriveVolumeKey(masterKey []byte, nodeID, pool string) (VolumeKey, error)

// Reporter publishes this node's own NodeReport on a lease, the SAME
// mechanism cluster.Advertiser uses to publish a cluster.Member: Grant,
// then KeepAlive on a DefaultReportInterval ticker, with the record
// attached to the lease via PutWithLease so the engine deletes it
// automatically if the node stops refreshing it -- "a dead node reports
// nothing" is the engine's own lease expiry, not reader-side staleness.
// Start's first publish is no different from a refresh: when it fails (no
// system-shard leader during boot) it is logged and retried on the next
// tick, never abandoned -- a node with no report is never placed on.
func NewReporter(mvcc MVCCServicer, leases lease.LeaseServicer, nodeID string, opts ...ReporterOption) (*Reporter, error)
func (r *Reporter) Start(ctx context.Context, build func() NodeReport) error
func (r *Reporter) Report(ctx context.Context, report NodeReport) error
func (r *Reporter) Close() error

// WithReporterSealProbe makes the Reporter propose nothing while the node
// barrier is sealed: a refresh is skipped, Report returns *NodeSealedError,
// and Close leaves the report to its lease, returning *NodeSealedError
// rather than proposing the revoke. Both composers pass the node barrier.
func WithReporterSealProbe(p SealProbe) ReporterOption

type Usage struct{ SizeBytes, FreeBytes int64 }

type Health struct {
	State     State  // StateReady, StateLocked, StateUnavailable
	Detail    string // provider text, e.g. "device not present"; never a path
	CheckedAt time.Time
}

// Registry maps provider names to providers, like seal.StrategyRegistry.
type Registry interface {
	Register(p Provider) error // *ProviderAlreadyRegisteredError on a duplicate
	Lookup(name string) (Provider, error) // *ProviderNotRegisteredError
	ListAvailable() []string // sorted
}

func NewRegistry() Registry  // pre-populated with the "dir" provider
func NewDirProvider() Provider
```

Records and the management service:

```go
// Pool is a catalog record: __sys:volumepool/pools/{name}.
type Pool struct {
	Name          string
	Provider      string
	Description   string
	CapacityBytes int64   // 0 = derived from health reports
	Overcommit    float64 // reservations may reach CapacityBytes*Overcommit; default 1.0
	ReservedBytes int64   // sum of assignment reservations; maintained by CAS
	CreatedAt, UpdatedAt time.Time
}

// Assignment is a tenant's location: __sys:volumepool/assign/{tenant}.
type Assignment struct {
	TenantID      string
	Pool          string
	Path          string // tenant-relative; one clean path component
	ReservedBytes int64
}

// NodeReport is one node's lease-backed health report:
// __sys:volumepool/nodes/{node}.
type NodeReport struct {
	NodeID     string
	Pools      map[string]PoolOnNode
	ReportedAt time.Time
}

type PoolOnNode struct {
	Mapped bool // node configuration maps the pool
	Usage  Usage
	Health Health
}

// Service is the management surface every transport delegates to.
type Service interface {
	ListPools(ctx context.Context) ([]PoolView, error)
	GetPool(ctx context.Context, name string) (*PoolDetail, error)
	CreatePool(ctx context.Context, req CreatePoolRequest) (*Pool, error)
	UpdatePool(ctx context.Context, name string, req UpdatePoolRequest) (*Pool, error)
	DeletePool(ctx context.Context, name string) error
	ListAssignments(ctx context.Context, pool, pageToken string, pageSize int) (*AssignmentPage, error)
	// MoveTenant admits a move through the wired MoveAdmission and
	// Catalog.AdmitMove (*MoveNotImplementedError with none wired); GetMove
	// reads the journal. No transport exposes them.
	MoveTenant(ctx context.Context, req MoveTenantRequest) (*Move, error)
	GetMove(ctx context.Context, tenantID string) (*Move, error)
}
```

`PoolView` is a `Pool` plus `UsedBytes`, `TenantCount` and per-node health
counts. `PoolDetail` is a `PoolView` plus one `NodeReportRow` per node.

The node-local step of the tenant move
([../decisions/volume-pool-tenant-moves.md](../decisions/volume-pool-tenant-moves.md))
is built in `move_copy.go`; these functions are called by
`seal.BarrierRegistry.RunMoveCopyStep`, which `seal.MoveStepRunner` calls on
each node:

- `CopyTree(ctx, src, dst) (*Manifest, error)`: the node-step copy of a
  closed directory tree, fsyncing files and directories, source kept;
- `BuildManifest(ctx, root) (*Manifest, error)`: the node-step manifest of a
  tree, recomputed from disk;
- `VerifyCopy(ctx, dst, want) error`: the node-step check that the
  destination's manifest equals the one built while copying;
- `Manifest{Entries}`: the node-step manifest, sorted by path;
- `ManifestEntry{Path, Dir, Mode, Size, SHA256}`: one node-step manifest
  entry, root-relative.

The catalog half of a move's admit step, called by `MoveTenant`
(`server.WireTenantMoves` wires it on every node):

- `Catalog.AdmitMove(ctx, MoveAdmitRequest) (*Move, error)`: one transaction
  reserving the tenant's `Assignment.ReservedBytes` on `ToPool` and writing
  the journal in state `admitted`, after the Q3 and Q10 checks and the Q9
  refusal (`*MoveToUnencryptedPoolError` for a Lockable-to-non-Lockable
  move, interim under ruling 1115);
- `MoveAdmitRequest{TenantID, ToPool, ReplicaNodes}`:
  the admit input; the caller supplies every replica node;
- `MoveKey(tenant)` / `MoveKeyPrefix`: the journal key
  `__sys:volumepool/moves/{tenant}`;
- `MoveStateAdmitted`: the state `AdmitMove` writes, no step granted;
- `MoveStateCopying` / `MoveStateCopied` / `MoveStateAborting` /
  `MoveStateCutover`: a node step granted or between steps; no node pending
  (ready for cut over); a node's step failed its attempts or the move was
  cancelled; cut over, past the commit point (the move only goes forward);
- `DefaultMoveStepAttempts` (3): attempts of one node's step before the move
  aborts (Q6 A);
- `Move.ReservedBytes`, `Move.AdmittedAt`: the bytes reserved on the
  destination, the admit time;
- `Move.StepNode` / `Move.StepAttempts` / `Move.UpdatedAt`: the node holding
  the step (at most one, Q1 B), its failed attempts, the last transition;
- `Move.NodesRetired` / `Move.SourceReservedBytes` / `Move.TenantRecorded`:
  the nodes whose source copy is retired, the source bytes the last retire
  releases, whether the tenant record names the destination;
- `Catalog.GrantMoveStep` / `CompleteMoveStep` / `FailMoveStep`: the
  journal's step transitions, each one compare-and-swap on the journal
  (`*MoveJournalConflictError` after the bounded retries);
- `Catalog.CutOverMove`: one transaction pointing the assignment at the
  destination pool and setting the journal `cutover` (the commit point);
- `Catalog.MarkMoveTenantRecorded`: records on the journal that the tenant
  record names the destination, which retire waits for;
- `Catalog.GrantRetireStep` / `CompleteRetireStep` / `FailRetireStep`: the
  retire step on each copied node (Q7 A); the last retire releases the
  source reservation and deletes the journal;
- `Catalog.CancelMove`: sends a move not yet cut over to `aborting` (Q8 A);
  refused past cut over;
- `Catalog.GrantAbortStep` / `CompleteAbortStep` / `FailAbortStep`: the
  abort step, copied nodes first, then pending nodes; the last abort
  releases the destination reservation and deletes the journal;
- `MoveStep{TenantID, NodeID, FromPool, ToPool, Attempt, Phase, Copied}` /
  `MoveStepPhaseCopy` / `MoveStepPhaseAbort` / `MoveStepPhaseRetire` /
  `MoveStepRunner{RunMoveStep}`: one node's step, its phase, whether that
  node holds a copy, and the seam that runs it on that node
  (`seal.MoveStepRunner`);
- `MoveReconciler` / `NewMoveReconciler(cat, runner, opts...)` /
  `WithMoveStepAttempts` / `WithMoveReconcilerLogger` /
  `WithMoveTransitionRecorder` / `ReconcileOnce(ctx)` /
  `RunLocalSteps(ctx, node)`: the leader pass (grants and cuts over) and
  the node pass (runs and settles this node's own step), scheduled by
  `server.TenantMoveWorker`: `RunLocalSteps` every tick on every node,
  `ReconcileOnce` on the system shard's leader
  (`server.DefaultTenantMoveInterval`, 2 s);
- `LockableProvider{OpensLockable() bool}`: optional provider interface; a
  provider without it counts as Lockable as a source and not Lockable as a
  destination (`dir` returns false);
- `MoveTenantRequest{TenantID, ToPool}`: the operator's move request;
- `MoveAdmission{AdmitTenantMove}`: the tenant layer's half of admit (kind,
  entitlement), returning the replica node IDs;
- `MoveStartAuditor{BeginMoveStart}`: audits a move's start before admit (an
  error refuses it) and settles once with `AdmitMove`'s answer;
- `Catalog.SetMoveAdmission` / `Catalog.SetMoveStartAuditor`: the
  post-construction seams `MoveTenant` uses; nil unwires;
- `MoveCutoverRecorder{RecordTenantPoolMoved}`: makes the tenant record name
  the destination pool after cut over;
- `Catalog.SetMoveCutoverRecorder`: the post-construction seam the
  reconciler uses after cut over; nil unwires (a copied move then waits).

Both composers (cmd/qrdbd and `sdk/go/node`) wire the move through
`server.WireTenantMoves(TenantMoveConfig)`:

- `TenantInfraConfig.VolumeNodeID`: this node's move node ID (its system
  replica ID, the `NodeReport` node ID), set on the barrier registry before
  `RestoreVolumeTenants` so a moving tenant resolves from the journal;
- `server.MoveStoreCloser`: closes and reopens a tenant's raft replicas, or
  its IsolationVolume Host, and its barrier around a step; a reopened raft
  replica counts as caught up once a ReadIndex on its shard completes;
- the replica-node source: an IsolationRaft tenant's placements, each
  replica's raft address mapped to its system-shard replica ID; a tenant
  with no placement moves on this node only;
- `service.NewVolumeMoveTransitionAudit`: one best-effort
  `volume_pool.move_<transition>` event per journal transition, with
  `node_id` and `phase` attributes.

### Composition options

| Option | Package | Effect |
|---|---|---|
| `WithVolumeProvider(p volume.Provider) NodeOption` | `sdk/go/node` | registers `p` in the node's volume registry |
| `NewVolumeRegistry(extra ...volume.Provider)` | `pkg/server/lifecycle` | builds the node's registry: `dir` plus the composed providers |
| `WithVolumeService(svc volume.Service) ServerOption` | `pkg/api/{rest,grpc,quic,mcp,unix}` | wires the management handlers |
| `WithVolumeService(svc volume.Service) Option` | `sdk/go/transport/embedded` | the same, for the embedded transport |

go-xkms registers its `luks2` provider with `WithVolumeProvider` at
composition. The provider takes no qrdb key material: its `Volume` implements
`volume.Lockable`, and the key for routine `Unlock` arrives as the `VolumeKey`
argument on each call, derived and passed by `pkg/seal.VolumeLifecycle`.
Any hardware-backed recovery key go-xkms wants to layer on top (TPM 2.0,
PKCS#11) is its own construction-time concern, orthogonal to this option.
Registering a name already registered fails startup with
`*volume.ProviderAlreadyRegisteredError`. A provider cannot be replaced
silently.

`pkg/seal.VolumeLifecycle` (not a `With*` option: it is built directly over
a node's system barrier, before that node's `BarrierRegistry`) is the
ordering seam:

```go
// NewVolumeLifecycle registers its own unlock/lock observers on system
// immediately. Build it BEFORE the node's BarrierRegistry so "unlock before
// stores open" holds by construction order alone.
func NewVolumeLifecycle(system *StorageBarrier, nodeID string, logger *slog.Logger, opts ...VolumeLifecycleOption) *VolumeLifecycle

// Track registers the Lockable volume opened for pool on this node.
// *VolumeAlreadyTrackedError on a duplicate pool.
func (vl *VolumeLifecycle) Track(pool string, l volume.Lockable) error
func (vl *VolumeLifecycle) Untrack(pool string)
func (vl *VolumeLifecycle) Tracked() []string // sorted
```

`StorageBarrier` carries the sealing-side half of the hook pair, beside
`OnUnsealed`:

```go
// OnSealing registers fn to run once, before the EpochBarrier zeroes the
// root key, each time Seal runs. A failing fn is folded into Seal's
// returned error (and audited) but never blocks the wipe.
func (b *StorageBarrier) OnSealing(fn func(context.Context) error)
```

### Volume pool surface in other packages

| Identifier | Role |
|---|---|
| `quota.Entitlements.AllowedVolumePools []string` (`json:"allowed_volume_pools,omitempty"`) | the pools a tenant may be placed on; the first is its default |
| `quota.Entitlements.AllowsVolumePool(name string) bool` | true only for a listed pool; fail-closed on an empty list |
| `tenant.TenantInfo.VolumePool`, `tenant.TenantInfo.Refusal` | the tenant's pool, and the answering node's refusal, carried on the wire as `TenantRefusal` |
| `tenant.WithVolumePool(name string) CreateTenantOption` | the pool a `volume` or `raft` tenant is placed on |
| `tenant.Tombstone.VolumePool` | the deleted tenant's pool |
| `config.VolumePoolConfig` | `Name`, `Path`, `Provider` (default `dir`) and `Options map[string]string` |
| `seal.BarrierRegistry.LocalTenantDataRoot` | resolves through the assignment and the opened `volume.Volume` |
| `seal.BarrierRegistry.RequireInsideLocalPool` | checks against the pool's opened `Volume.Root()` |
| `seal.BarrierRegistry.RunMoveCopyStep(ctx, nodeID, tenantID, toPool) (*volume.Manifest, error)` | the move's node step on this node's pools: resolve both directories, copy, verify, remove a partial destination through `RemoveDataRoot`; nothing calls it in this build |
| `tenant.TenantService.AdmitTenantMove(ctx, volume.MoveTenantRequest) ([]string, error)` | implements `volume.MoveAdmission`: the tenant exists, is `volume` or `raft` (not the system tenant), is entitled to `ToPool`; returns its replica nodes |
| `tenant.TenantService.RecordTenantPoolMoved(ctx, tenantID, fromPool, toPool) error` | implements `volume.MoveCutoverRecorder`: makes the tenant record name `toPool`; idempotent; a tenant naming another pool is `*volume.MoveConflictError`, a missing tenant `ErrTenantNotFound`; nothing wires it in this build |
| `tenant.MoveReplicaNodeSource` / `tenant.MoveReplicaNodeWiring` | the optional source of a tenant's replica node IDs and its setter; with none set `AdmitTenantMove` returns `*volume.MoveNotImplementedError` |
| `service.NewVolumeMoveAudit(logger, policy) volume.MoveStartAuditor` | audits a move's start as `volume_pool.move_start` (`to_pool`, `from_pool` on the outcome); gated by the failure policy before admit, best-effort outcome after |
| `seal.ErrMoveStepInvalid` | `RunMoveCopyStep` called without a node ID or destination pool |
| `seal.ErrMoveSamePool` | the destination pool is the tenant's current pool |
| `seal.ErrMoveTenantOpen` | the tenant still has a barrier on this node; the step copies a closed directory |
| `placement.ReplicaFilter` | `func(tenantID string, shardID uint64, raftAddr string) error`; `WithReplicaFilter` composes several |

## Invariants

0. **Unlock precedes store-open; lock precedes key-wipe.**
   `pkg/seal.VolumeLifecycle`'s `OnUnsealed` observer is registered on a
   node's system barrier before that node's `BarrierRegistry` (which
   restores tenant stores) is built, and its `OnSealing` observer runs
   inside `StorageBarrier.Seal` before the root key is zeroed, after
   `BarrierRegistry.SealAll` has already closed tenant stores. A failed
   `Unlock` leaves only that pool's volume unavailable; a failed `Lock` is
   reported (`*seal.VolumeLockError`, audited) but the key wipe still
   completes.

1. **No replicated record names a filesystem path.** The catalog, assignments,
   health reports, tenant records, tombstones and placement replicas carry
   none. A health report carries a pool's per-node `Usage` and `Health`, never
   its mount, and a provider's `Health.Detail` never names a path or device. A decoded record that carries a path is refused with the existing
   typed refusals.
2. **An assignment's `Path` is one clean path component.** It is validated on
   write (`*volume.InvalidAssignmentPathError`), and the joined path must be
   strictly inside `Volume.Root()`, with links resolved, on every use.
3. **A tenant has at most one assignment.** It is written with put-if-absent. A
   create that finds an assignment for its id resolves that assignment and
   never picks again, which keeps key delivery and retried creates idempotent.
4. **Reservations never exceed `CapacityBytes * Overcommit`.** The pool
   record's `ReservedBytes` is updated by compare-and-swap in the same Raft
   transaction that writes or deletes an assignment, through the replicated
   system KV's `service.TransactionServicer`. This is the same transaction
   `lifecycle.NewReplicatedSealBackend` already uses for put-if-absent. Two
   creates racing for the last capacity cannot both succeed.
5. **Entitlement precedes reservation.** A create first checks the
   isolation tier (`AllowVolumeStorage` / `AllowDedicatedRaft`), then
   `AllowsVolumePool`, then reserves. A denial changes nothing.
6. **A pool that does not open on a node refuses its tenants on that node
   only -- under `--allow-degraded-volume-boot`.** Without that flag (or its config-file equivalent,
   `storage.allow_degraded_volume_boot`), which is the default, a pool that
   fails to open fails `qrdbd start` outright with `*volume.OpenAtBootError`,
   naming every inaccessible pool and the reason, before any tenant
   restores: boot never *partially* serves. With the flag set, every tenant
   assigned to an unavailable pool is listed with a typed refusal
   (`*volume.PoolUnavailableError`, classified `Unavailable` on every
   transport), its data is never opened, and the rest of the node's pools
   and tenants are served. Each skipped pool is logged at
   ERROR (not the ordinary open-failure WARN), reported in this node's
   health (`qrdb volume-pool get`) and exported as
   `qrdb_volume_pool_ready{pool}` = 0, so the degradation is never silent.
7. **Removal stays inside the pool root.** Every directory removal — tenant
   delete, deleted-tenant reconcile, shard purge — passes
   `RequireInsideLocalPool` against the pool's `Volume.Root()`, with links
   resolved on both sides.
8. **The catalog is the authority on pool existence.** A name in node
   configuration with no catalog record is not a pool: no tenant can be
   placed on it, and the node still boots.
9. **A health report needs an engine that keeps leases.** The report is
   lease-bound, so a composer starts `volume.Reporter` only over an engine
   for which `lifecycle.LocalEngineKeepsLeases` holds. A standalone node on
   the `file` or `objstore` backend composes the plain KV engine, which keeps
   none: it publishes no report, and its pools show `no report` for it. A
   clustered node always reports.
10. **A pool with assignments cannot be deleted**
    (`*volume.PoolInUseError` naming the tenants, first page), nor can a
    pool a move journal names as source or destination (`*volume.PoolInUseError`
    naming the moving tenants; a journal that does not decode refuses with
    `*volume.CorruptRecordError`).

## Error semantics

Every error is typed. Each classification below is the row added to
`pkg/api/errclass/table.go`; every transport maps it through `errclass`.

| Error | When | Class (HTTP / gRPC) |
|---|---|---|
| `*volume.PoolNotFoundError{Pool}` | a request names a pool the catalog does not have | `NotFound` (404) |
| `*volume.PoolAlreadyExistsError{Pool}` | create of an existing name | `AlreadyExists` (409) |
| `*volume.PoolInUseError{Pool, Tenants}` | delete while assigned | `FailedPrecondition` (409) |
| `*volume.ProviderNotRegisteredError{Provider, Available}` | a pool names a provider this node lacks | `FailedPrecondition` (409) at create; a refusal on the node at boot |
| `*volume.ProviderAlreadyRegisteredError{Provider}` | composition registers a name twice | startup failure; never on the wire |
| `*volume.PoolCapacityExceededError{Pool, Requested, Available}` | a reservation would exceed capacity | `ResourceExhausted` (429) |
| `*volume.PoolUnavailableError{Pool, NodeID, Err}` | this node cannot open or resolve the pool | `Unavailable` (503); listed as the tenant's refusal |
| `*volume.InvalidAssignmentPathError{TenantID, Path}` | an assignment path is not one clean component | `Internal` (500): a corrupt record, never caller input |
| `*quota.EntitlementError{Feature: "volume_pool:<name>"}` | the tenant is not entitled to the pool | `PermissionDenied` (403), the existing row |
| `*tenant.PersistedDataRootError` | a record carries a path | the existing `FailedPrecondition` row |
| `*seal.VolumeUnlockError{Pool, Err}` | a tracked `Lockable` volume's `Unlock` failed after unseal | not on the wire directly: surfaces as `PoolUnavailableError`/`pool_unavailable` through the pool's resolution |
| `*seal.VolumeLockError{Pool, Err}` | a tracked `Lockable` volume's `Lock` failed while sealing | not on the wire: `Internal`-class, logged and audited; the barrier still seals |
| `*volume.MoveStepError{NodeID, Phase, Path, Err}` | the node-local move copy (`Phase` `copy`) or verify (`Phase` `verify`) failed; `Path` is root-relative | `Unavailable` (503) when the cause is `PoolUnavailableError` or `NodeSealedError`, else `Internal` (500); no transport carries it in this build |
| `*volume.ManifestMismatchError{Path, Reason}` | the destination's manifest differs (`missing`, `extra`, `type`, `mode`, `size`, `digest`); wrapped in a `MoveStepError` | `Internal` (500); no transport carries it in this build |
| `*volume.MoveConflictError{TenantID, NodeID, Reason}` | a move admit is refused (a move already runs, the destination is the current pool, the destination is not ready on `NodeID`, the tenant is not a `volume` or `raft` tenant, or it has no replica node), a step transition is refused (no step to grant, or `NodeID` does not hold the step), cut over is refused (not copied, a node pending or holding a step, the assignment no longer naming the source pool), a retire or abort step is refused, `CancelMove` is refused past cut over, `RecordTenantPoolMoved` finds the tenant on neither pool, or tenant delete / a quota change is refused while a move runs | `FailedPrecondition` (409); no transport carries it in this build |
| `*volume.MoveToUnencryptedPoolError{TenantID, FromPool, ToPool}` | a move from a Lockable pool to one that is not, refused outright while Q9 is with the owner (ruling 1115) | `FailedPrecondition` (409); no transport carries it in this build |
| `*volume.MoveJournalConflictError{TenantID}` | a move-journal step transition lost every compare-and-swap attempt; nothing written, retrying is safe | `Aborted` (409); no transport carries it in this build |
| `*volume.InvalidMoveRequestError{Reason}` | `AdmitMove` got an empty tenant or pool, no replica nodes, or an empty or duplicate node ID; `MoveTenant` an empty tenant or pool; `GetMove` an empty tenant | `InvalidArgument` (400); no transport carries it in this build |
| `*volume.MoveNotFoundError{TenantID}` | `GetMove` or `CancelMove` found no journal for the tenant | `NotFound` (404); no transport carries it in this build |
| `*volume.NodeSealedError{Op}` | the node barrier is sealed and a write the node would propose on its own behalf is refused | `Unavailable` (503) |
| `*volume.MoveNotImplementedError` | `MoveTenant` in a composition that wired no `MoveAdmission`, or a tenant service with no replica-node source (every daemon in this build) | `Unimplemented`, the existing row |

**Fail-closed paths.** An unreadable catalog or assignment fails the create, the
move and the resolution. Nothing falls back to a default path or a fresh pick.
A provider whose `Open` fails yields `PoolUnavailableError`, never an empty
directory. A move that cannot read its journal does nothing.

**Refusals on the wire.** `TenantResponse` carries a `refusal` with a `code` and
a `message`. The code is stable and the message is not:

| Code | From |
|---|---|
| `persisted_path` | `*tenant.PersistedDataRootError` |
| `pool_unavailable` | `*volume.PoolUnavailableError` |
| `provider_missing` | `*volume.ProviderNotRegisteredError` |

The refusal is the **answering node's** view: another node may serve the same
tenant. A client that needs the cluster view reads the pool's health.

## Configuration

Node configuration (`storage.volume_pools`), per node, never replicated:

```yaml
storage:
  volume_pools:
    - name: fast              # must exist in the catalog to be served
      path: /mnt/nvme-a       # dir: the directory; luks2: the mount point
      provider: dir           # default dir; must equal the catalog's provider
      options: {}             # provider-specific, e.g. luks2: { device: /dev/nvme1n1 }
  allow_degraded_volume_boot: false  # see below
```

`storage.allow_degraded_volume_boot` (default `false`) and its CLI
equivalent, `qrdbd start --allow-degraded-volume-boot`, are the opt-in to a
degraded boot (invariant 6): an explicit `--allow-degraded-volume-boot` on
the command line wins over the config file, the same precedence as
`--replica-forwarding`. With neither set, any configured pool that fails to
open fails `qrdbd start` outright.

Validation (`config.Load`, strict) rejects:

- an empty or duplicate name;
- an empty or relative path;
- two pools whose paths overlap (`validation.PathsOverlap`);
- a pool path inside the node's data directory or containing it.

A provider mismatch with the catalog is a runtime refusal, not a load error,
because the catalog is not readable at load time.

Default entitlements (`tenant.default_entitlements.allowed_volume_pools`) and
default quotas keep their existing keys.

## Persistence

All in the replicated system namespace, hidden from user list and scan:

| Key | Value | Written by |
|---|---|---|
| `__sys:volumepool/pools/{pool}` | `Pool` (JSON) | `CreatePool`, `UpdatePool`, reservation CAS |
| `__sys:volumepool/assign/{tenant}` | `Assignment` (JSON) | create (put-if-absent), delete |
| `__sys:volumepool/nodes/{node}` | `NodeReport` (JSON), lease-bound | `volume.Reporter`, refreshed every `DefaultReportInterval` *(5 s)*; lease TTL `DefaultLeaseTTL` *(15 s)* |
| `__sys:volumepool/moves/{tenant}` | `Move` (JSON) | `Catalog.AdmitMove`, the step transitions, `CutOverMove`, `CancelMove`, and the final retire or abort (delete) |

A tenant whose records predate this layout is refused, listed with its
refusal, and deleted and re-created.

## Concurrency

- Reservation and assignment changes are one Raft transaction: a compare on
  the pool record, and a put-if-absent (or delete) on the assignment. A lost CAS is
  re-read and retried a bounded number of times, then reported as
  `*volume.ReservationConflictError` (`Aborted`).
- A move admit is one transaction: a compare on the destination pool
  record, the tenant's assignment and an absent journal, then a put of the
  destination pool record and the journal. `Release` and
  `AdjustReservation` also compare the journal absent, so neither races an
  admit.
- Cut over is one transaction: a compare on the tenant's assignment and the
  journal, then a put of both.
- The last retire (on the source pool) and the last abort (on the
  destination pool) are each one transaction: a compare on that pool record
  and the journal, then a put of the pool record and a delete of the
  journal.
- Health reports are owned by their node. No other writer touches them.

## Test strategy

**Unit, per package, in memory:**

- `pkg/volume`: registry, `dir` provider (including statfs, writability and
  symlink/relative-path rejection), `DeriveVolumeKey` (determinism, and that
  node/pool each change the derived key), `VolumeKey.Wipe`;
- `pkg/volume` (`move_copy.go`): `CopyTree` round trip against
  `BuildManifest` of both trees, its refusals (path rules, overlap, symlink,
  named pipe, cancelled context), every `VerifyCopy` mismatch reason, and no
  absolute path in any message;
- `pkg/volume` (`catalog_move_test.go`): `AdmitMove` success (journal equal
  to the result, nodes pending sorted, destination reserved, source and
  assignment unchanged); each invalid request; no assignment; unknown
  destination; same pool; a second admit (journal and reservation
  unchanged); a replica node with no report, a stale report, the pool
  unmapped or locked (`NodeID` set); capacity; bounded retries; Q9 refused outright
  with a Lockable test provider (`*MoveToUnencryptedPoolError` for luks2 to
  dir with no journal, reservation or assignment change, for a provider
  without `OpensLockable` as source and as destination, and through
  `MoveTenant` with the settle; plain to Lockable admitted); Q10 after admit (`Release`, `AdjustReservation` refused with
  reservations unchanged, `DeletePool` of either pool names the tenant); a
  corrupt journal fails `Release`, `AdjustReservation`, `DeletePool` and
  `AdmitMove` closed; `MoveTenant` with no admission wired (not
  implemented), invalid requests (admission never called), an admission
  refusal and an auditor refusal (no journal, no reservation), success
  (journal from the admission's nodes, settle with the move, `GetMove`
  equal), a second move settled with `*MoveConflictError`, no auditor
  wired; `GetMove` invalid, not found and corrupt; `SetMoveAdmission(nil)`
  unwires;
- `pkg/volume` (`move_journal_test.go`): a grant picks the first pending node
  and a second grant writes nothing; completing n1 keeps copying, n2 sets
  copied; complete or fail by a node without the step refused; failures
  below the limit keep the step and count, the third sets aborting with the
  node still pending; success clears the count; grant on copied or aborting
  refused; no journal, a corrupt journal, empty IDs; CAS exhaustion is
  `*MoveJournalConflictError`;
- `pkg/volume` (`move_reconciler_test.go`): two passes take a two-node move
  to copied (n1 then n2, attempt 1 each) and a copied move is not run again;
  a node failing every attempt aborts after 3 runs, 2 with
  `WithMoveStepAttempts(2)`; a failure then a success completes on attempt
  2; a corrupt journal does not stop another tenant's step; a catalog error
  is returned; nil dependencies refused; a cancelled context returns
  `context.Canceled`;
- `pkg/volume` (`move_journal_test.go`, cut over to delete):
  `TestCatalog_CutOverMove` and `_Refusals`, `_MarkMoveTenantRecorded`,
  `_RetireSteps`, `_RetireStep_SourceFloorZero`, `_NeedsTenantRecorded` and
  `_FailureStaysCutover`, `_CancelMove` and `_CancelMove_InFlightStepRefused`,
  `_AbortSteps`, `_AbortStep_Refusals` and `_FailureKeepsTheNode`,
  `_PhaseSteps_StoreFailures`;
- `pkg/volume` (`move_reconciler_test.go`, the dispatch):
  `TestMoveReconciler_FullMoveCutsOverAndRetires`, `_CopiedWaitsForRecorder`,
  `_RecorderErrorRetried`, `_RetireFailureRetried`, `_CancelThenAbort`,
  `_AbortFailureRetried`, `_StepPhases`, `_PhaseCatalogErrorReturned`;
- `pkg/tenant` (`volume_move_test.go`): `AdmitTenantMove` not found, a
  prefix tenant refused, a destination not entitled, no source, a source
  error, no replica node, a volume and a raft tenant admitted;
  `TestRecordTenantPoolMoved`: the record names the destination and a
  repeat is a no-op, another pool refused, a missing tenant;
- `pkg/api/errclass` (`volume_move_test.go`): `TestClassify_VolumeMoveErrors`,
  each move error's class, and `MoveStepError` by its cause (a wrapped
  `PoolUnavailableError` or `NodeSealedError` `Unavailable`, anything else
  `Internal`, also under `fmt.Errorf("%w")`);
- `pkg/service` (`volume_move_audit_test.go`): the start recorded with
  `to_pool` only, the outcome with `from_pool`, a failure outcome, the gate
  following the policy, a nil logger giving no auditor;
- `pkg/seal`: `VolumeLifecycle` ordering, proven with a fake `Lockable` --
  unlock after unseal and before a later-registered `OnUnsealed` observer
  (standing in for `BarrierRegistry`'s tenant restore); lock before the root
  key is wiped (the fake reads `GetMasterKey` from inside its own `Lock` and
  asserts it still succeeds); a failed `Unlock` leaves the barrier's own
  `Unseal` call successful and the other tracked pools still attempted; a
  failed `Lock` is folded into `Seal`'s returned error while `IsSealed()`
  still ends up true and the other tracked pools are still attempted;
  `Track`/`Untrack`/`Tracked` and the duplicate-registration refusal;
- `pkg/volume`, `pkg/tenant`, `pkg/quota`: records, reservation CAS under
  concurrent creates, entitlement and
  reservation ordering;
- `pkg/placement`: the pool filter;
- `pkg/seal` (`move_step_test.go`): `RunMoveCopyStep` over two real `dir`
  pools copies a nested tree (subdirectory, empty file, file over 1 MiB, a
  0600 file) to `<destination root>/<Assignment.Path>` with a manifest equal
  to `BuildManifest` of both trees, the source and the assignment unchanged;
  a leftover partial destination is removed and the copy redone; refusals
  (empty tenant, empty node or pool, no pools configured, no assignment, same
  pool, a pool not open on this node, a tenant with a registered barrier, a
  symbolic link in the source); a verify failure (a file truncated between
  copy and verify) removes the destination and keeps the source, and with a
  failing remover the error still carries the `*volume.MoveStepError` and the
  remover's error; a cancelled context; no message names a pool path.

Fault seams:

- a `volume.Provider` double whose `Open`, `Usage` and `Close` fail on demand;
- a `volume.Lockable` double whose `Unlock` and `Lock` fail independently on
  demand, used by the `VolumeLifecycle` ordering tests above;
- a `failingBackend` on the replicated store, for CAS loss and write failure;
- the existing `seal.DirectoryRemover`, for removal failures.

Every double asserts the contract the real provider keeps. In particular, a
double never returns a `Root()` that is not absolute and clean.

**Integration (`test/integration/`), real nodes:**

- three nodes mapping one pool at three different paths, a `raft` tenant
  placed on it: each node's directory is under its own mount, and nothing
  replicated names a path;
- a pool missing on one node: that node never receives a replica and lists the
  tenant as refused, while the others serve it;
- capacity exhaustion: the second create fails with 429 and leaves no
  assignment;
- an entitlement denial;
- delete of a pool in use;
- a `Lockable` pool: the tenant is refused while the node is sealed, served
  immediately after unseal, and the volume reports locked again (and the
  tenant refused again) after a re-seal;
- a symlinked tenant directory pointing outside the pool is refused on delete.

**Transport parity.** Every management verb on all six transports through the
CLI's `CommandDefinition` sweep, with the status mapping of every error row
above.

**LUKS2.** Tested in go-xkms against this contract. In qrdb the seam is proven
with a `volume.Lockable` double that requires `Spec.Options["device"]`, used
both for `pkg/volume`'s own provider-contract tests and for
`pkg/seal.VolumeLifecycle`'s ordering tests.

## Observability

Log events (structured, `slog`):

| Event | Level |
|---|---|
| `volume: pool opened` (`pool`, `provider`, `root`) | INFO |
| `volume pool: failed to open volume; pool will not be served on this node` (`pool`, `error`) -- `lifecycle.OpenConfiguredVolumes`, both boot modes; the default turns this into a fatal `*volume.OpenAtBootError` immediately after, the degraded-boot opt-in does not | ERROR |
| `volume: tenant refused, pool unavailable` (`tenant`, `pool`) | ERROR |
| `volume: reservation` (`tenant`, `pool`, `bytes`) | INFO |
| `volume: unlocked` / `volume: unlock failed` (`pool`[, `error`]) | INFO / ERROR |
| `volume: locked` / `volume: lock failed` (`pool`[, `error`]) | INFO / ERROR |

Metrics:

- `qrdb_volume_pool_size_bytes{pool}` *(not yet implemented)*
- `qrdb_volume_pool_free_bytes{pool}` *(not yet implemented)*
- `qrdb_volume_pool_reserved_bytes{pool}` *(not yet implemented)*
- `qrdb_volume_pool_used_bytes{pool}` *(not yet implemented)*
- `qrdb_volume_pool_tenants{pool}` *(not yet implemented)*
- `qrdb_volume_pool_ready{pool}` (0/1, per node; `pkg/volume.RecordPoolHealth`) --
  **shipped**: 1 only for `StateReady`; set at boot (once per configured
  pool, by `OpenConfiguredVolumes`) and refreshed on every later health
  check (`BuildNodeReport`), so a degraded boot's skipped pools, and any
  pool that later becomes unavailable or locks on re-seal, read 0 for as
  long as the condition lasts. Local to this node -- it needs no lease,
  unlike `NodeReport` (invariant 9).
- `qrdb_volume_pool_locked{pool}` (0/1, per node; always 0 for a non-`Lockable` volume) *(not yet implemented)*

Audit: every catalog change, assignment change and move transition is an
audit record naming the operator identity (a move's start is
`volume_pool.move_start` through `service.NewVolumeMoveAudit`; later transitions arrive with the reconciler); every
`Lockable` unlock and lock attempt is an audit record naming the pool and,
on failure, the error, under actions `volume_unlock` / `volume_lock`
(`pkg/seal.VolumeLifecycle`, subject `seal`, since these run inside the
system barrier before any operator request exists to attribute them to).
