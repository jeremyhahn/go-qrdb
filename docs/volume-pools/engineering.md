# Volume Pools — Engineering

> **Status: APPROVED — ruled 2026-10-04 (D1-D9), 2026-10-06 (D10); Phase 1
> build in progress.** · Scope: go-qrdb · Last verified: 2026-10-06
>
> Identifiers marked *(new)* did not exist in the tree when this was last
> verified; this document is the contract they are built to. The design
> choices it assumes are the decisions ruled in
> [architecture.md](architecture.md#decisions-ruled-2026-10-04). Tenant moves
> (decision D5, the `MoveTenant` / `GetMove` surface and the move journal) are
> **Phase 2** -- everything else here is Phase 1.

## Exported surface

### `pkg/volume` *(new)*

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
// underlying device in step with the node's system barrier (decision D6,
// ruled 2026-10-04: see architecture.md's "Encrypted volumes and the cluster
// seal"). The "dir" provider does not implement it.
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
// barrier is sealed: a refresh is skipped and Report returns
// *NodeSealedError. Both composers pass the node barrier.
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
	CapacityBytes int64   // 0 = derived from health reports (decision D4)
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
	// MoveTenant and GetMove are Phase 2 (decision D5); their journal and
	// wire surface are not part of this build.
	MoveTenant(ctx context.Context, tenantID, toPool string) (*Move, error)
	GetMove(ctx context.Context, tenantID string) (*Move, error)
}
```

`PoolView` is a `Pool` plus `UsedBytes`, `TenantCount` and per-node health
counts. `PoolDetail` is a `PoolView` plus every `NodeReport` row for the pool.

### Composition options *(new)*

| Option | Package | Effect |
|---|---|---|
| `WithVolumeProvider(p volume.Provider) ServerOption` | `pkg/server` | registers `p` in the node's volume registry |
| `WithVolumeProvider(p volume.Provider) NodeOption` | `sdk/go/node` | the same, for embedded nodes |
| `WithVolumeService(svc volume.Service)` | `pkg/api/{rest,grpc,quic,mcp,unix}` | wires the management handlers |

go-xkms calls `WithVolumeProvider(luks2.NewProvider())` at composition, where
`luks2.NewProvider` takes no qrdb key material -- its `Volume` implements
`volume.Lockable`, and the key for routine `Unlock` arrives as the `VolumeKey`
argument on each call, derived and passed by `pkg/seal.VolumeLifecycle`.
Any hardware-backed recovery key go-xkms wants to layer on top (TPM 2.0,
PKCS#11) is its own construction-time concern, orthogonal to this option.
Registering a name already registered fails startup with
`*volume.ProviderAlreadyRegisteredError`. A provider cannot be replaced
silently.

`pkg/seal.VolumeLifecycle` (not a `With*` option: it is built directly over
a node's system barrier, before that node's `BarrierRegistry`) is the
ordering seam decision D6 requires:

```go
// NewVolumeLifecycle registers its own unlock/lock observers on system
// immediately. Build it BEFORE the node's BarrierRegistry so "unlock before
// stores open" holds by construction order alone.
func NewVolumeLifecycle(system *StorageBarrier, nodeID string, logger *slog.Logger) *VolumeLifecycle

// Track registers the Lockable volume opened for pool on this node.
// *VolumeAlreadyTrackedError on a duplicate pool.
func (vl *VolumeLifecycle) Track(pool string, l volume.Lockable) error
func (vl *VolumeLifecycle) Untrack(pool string)
func (vl *VolumeLifecycle) Tracked() []string // sorted
```

`StorageBarrier` gains the sealing-side half of the hook pair `OnUnsealed`
already had:

```go
// OnSealing registers fn to run once, before the EpochBarrier zeroes the
// root key, each time Seal runs. A failing fn is folded into Seal's
// returned error (and audited) but never blocks the wipe.
func (b *StorageBarrier) OnSealing(fn func(context.Context) error)
```

### Changes to existing surface

| Identifier | Change |
|---|---|
| `quota.Entitlements` | gains `AllowedVolumePools []string` (`json:"allowed_volume_pools,omitempty"`) |
| `quota.Entitlements.AllowsVolumePool(name string) bool` *(new)* | true only for a listed pool; fail-closed on an empty list |
| `tenant.TenantInfo` | `DataRoot` is removed; gains `VolumePool string`; `Refusal error` (in-process, from the recovery fix) gains a wire form |
| `tenant.WithDataRoot`, `tenant.DataRootRetiredError` | removed: the wire field they refuse no longer exists |
| `tenant.WithVolumePool(name string) CreateTenantOption` *(new)* | the pool a `volume` or `raft` tenant is placed on |
| `tenant.Tombstone.DataRoot` | replaced by `VolumePool` |
| `config.VolumePoolConfig` | `Paths []string` and `Strategy` replaced by `Path string`, `Provider string` (default `dir`) and `Options map[string]string` |
| `pool.VolumePoolSubsystem`, `pool.BuildVolumePoolSubsystem`, the round-robin state stores | removed; replaced by `pkg/volume` |
| `seal.BarrierRegistry.LocalTenantDataRoot` (from the recovery fix) | resolves through the assignment and the opened `volume.Volume` |
| `seal.BarrierRegistry.RequireInsideLocalPool` (from the recovery fix) | checks against the pool's opened `Volume.Root()` |
| `placement.ReplicaFilter` | gains the tenant id: `func(tenantID string, shardID uint64, raftAddr string) error`; `WithReplicaFilter` composes several |
| `transport.VolumePoolClient` (`List`, `Pick`, `RegisterPool`) and `Client.VolumePools()` | replaced by the Tier 1 management client (decision D8) |

## Invariants

0. **Unlock precedes store-open; lock precedes key-wipe (decision D6).**
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
5. **Entitlement precedes reservation.** A create or move first checks the
   isolation tier (`AllowVolumeStorage` / `AllowDedicatedRaft`), then
   `AllowsVolumePool`, then reserves. A denial changes nothing.
6. **A pool that does not open on a node refuses its tenants on that node
   only -- under `--allow-degraded-volume-boot` (decision D10, ruled
   2026-10-06).** Without that flag (or its config-file equivalent,
   `storage.allow_degraded_volume_boot`), which is the default, a pool that
   fails to open fails `qrdbd start` outright with `*volume.OpenAtBootError`,
   naming every inaccessible pool and the reason, before any tenant
   restores: boot never *partially* serves. With the flag set, every tenant
   assigned to an unavailable pool is listed with a typed refusal
   (`*volume.PoolUnavailableError`, classified `Unavailable` on every
   transport), its data is never opened, and the rest of the node's pools
   and tenants are served -- the degraded-boot behavior this invariant
   originally described unconditionally. Each skipped pool is logged at
   ERROR (not the ordinary open-failure WARN), reported in this node's
   health (`qrdb volume-pool get`) and exported as
   `qrdb_volume_pool_ready{pool}` = 0, so the degradation is never silent.
7. **Removal stays inside the pool root.** Every directory removal — tenant
   delete, deleted-tenant reconcile, shard purge, move cleanup — passes
   `RequireInsideLocalPool` against the pool's `Volume.Root()`, with links
   resolved on both sides.
8. **The catalog is the authority on pool existence.** Node configuration that
   maps an unknown pool name is reported in the node's health report as
   `unknown pool`. It is not served, and the node still boots.
9. **A health report needs an engine that keeps leases.** The report is
   lease-bound, so a composer starts `volume.Reporter` only over an engine
   for which `lifecycle.LocalEngineKeepsLeases` holds. A standalone node on
   the `file` or `objstore` backend composes the plain KV engine, which keeps
   none: it publishes no report, and its pools show `no report` for it. A
   clustered node always reports.
10. **A pool with assignments cannot be deleted**
    (`*volume.PoolInUseError` naming the tenants, first page).
11. **A move is resumable and never loses the source** before the assignment
    flips. The journal's states are `preparing`, `fenced`, `copying`,
    `committed` and `cleaned`. Before `committed`, an abort releases the target
    reservation, deletes the target copies and lifts the fence. After
    `committed`, only cleanup remains.

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
| `*volume.MoveInProgressError{TenantID, State}` | a second move, or a delete, during a move | `FailedPrecondition` (409); **Phase 2** |
| `*volume.MoveError{TenantID, State, NodeID, Err}` | a move step failed; the journal says where | `Internal` (500); **Phase 2** |
| `*quota.EntitlementError{Feature: "volume_pool:<name>"}` | the tenant is not entitled to the pool | `PermissionDenied` (403), the existing row |
| `*tenant.PersistedDataRootError` | a record carries a path | the existing `FailedPrecondition` row |
| `*seal.VolumeUnlockError{Pool, Err}` | a tracked `Lockable` volume's `Unlock` failed after unseal (decision D6) | not on the wire directly: surfaces as `PoolUnavailableError`/`pool_unavailable` through the pool's resolution |
| `*seal.VolumeLockError{Pool, Err}` | a tracked `Lockable` volume's `Lock` failed while sealing | not on the wire: `Internal`-class, logged and audited; the barrier still seals |

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
  allow_degraded_volume_boot: false  # decision D10 (2026-10-06); see below
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
| `__sys:volumepool/assign/{tenant}` | `Assignment` (JSON) | create (put-if-absent), move commit, delete |
| `__sys:volumepool/nodes/{node}` | `NodeReport` (JSON), lease-bound | `volume.Reporter`, refreshed every `DefaultReportInterval` *(5 s)*; lease TTL `DefaultLeaseTTL` *(15 s)* |
| `__sys:volumepool/move/{tenant}` | move journal (JSON) | `MoveTenant` and each node's copy step; **Phase 2** |

Removed:

- the round-robin cursor (`__sys:volumepool/` cursor keys and the node-local
  `PebbleStateStore`);
- the `slot` field of the interim assignment;
- pool and slot in the node-local tenant-root record, which keeps only its
  auto-unseal flag.

**Migration: none** (decision D9). A tenant whose records predate this layout is
refused, listed with its refusal, and deleted and re-created.

## Concurrency

- Reservation and assignment changes are one Raft transaction: a compare on
  the pool record, and a put-if-absent (or delete) on the assignment. A lost CAS is
  re-read and retried a bounded number of times, then reported as
  `*volume.ReservationConflictError` *(new, `Aborted`)*.
- Health reports are owned by their node. No other writer touches them.
- A move holds the tenant's delete fence, so delete, create-with-same-id and a
  second move are refused for its duration.

## Test strategy

**Unit, per package, in memory:**

- `pkg/volume`: registry, `dir` provider (including statfs, writability and
  symlink/relative-path rejection), `DeriveVolumeKey` (determinism, and that
  node/pool each change the derived key), `VolumeKey.Wipe`;
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
  concurrent creates (Phase 1, once the catalog lands), entitlement and
  reservation ordering;
- `pkg/placement`: the pool filter;
- `pkg/seal` (`BarrierRegistry`): resolution and the inside-root guard, once
  the catalog/assignment resolution replaces the objstore-pool path.

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
- a move: kill a node mid-copy, restart it, and the move completes;
  **Phase 2**;
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
| `volume pool: failed to open volume; pool will not be served on this node` (`pool`, `error`) -- `lifecycle.OpenConfiguredVolumes`, both boot modes; the default (D10) turns this into a fatal `*volume.OpenAtBootError` immediately after, the degraded-boot opt-in does not | ERROR |
| `volume: tenant refused, pool unavailable` (`tenant`, `pool`) | ERROR |
| `volume: reservation` (`tenant`, `pool`, `bytes`) | INFO |
| `volume: move state` (`tenant`, `from`, `to`, `state`) | INFO; failures at ERROR; **Phase 2** |
| `volume: unlocked` / `volume: unlock failed` (`pool`[, `error`]) | INFO / ERROR |
| `volume: locked` / `volume: lock failed` (`pool`[, `error`]) | INFO / ERROR |

Metrics:

- `qrdb_volume_pool_size_bytes{pool}` *(new, not yet implemented)*
- `qrdb_volume_pool_free_bytes{pool}` *(new, not yet implemented)*
- `qrdb_volume_pool_reserved_bytes{pool}` *(new, not yet implemented)*
- `qrdb_volume_pool_used_bytes{pool}` *(new, not yet implemented)*
- `qrdb_volume_pool_tenants{pool}` *(new, not yet implemented)*
- `qrdb_volume_pool_ready{pool}` (0/1, per node; `pkg/volume.RecordPoolHealth`) --
  **shipped**: 1 only for `StateReady`; set at boot (once per configured
  pool, by `OpenConfiguredVolumes`) and refreshed on every later health
  check (`BuildNodeReport`), so a degraded boot's skipped pools, and any
  pool that later becomes unavailable or locks on re-seal, read 0 for as
  long as the condition lasts. Local to this node -- it needs no lease,
  unlike `NodeReport` (invariant 9).
- `qrdb_volume_pool_locked{pool}` (0/1, per node; always 0 for a non-`Lockable` volume) *(new, not yet implemented)*

Audit: every catalog change, assignment change and move transition (**Phase
2** for moves) is an audit record naming the operator identity; every
`Lockable` unlock and lock attempt is an audit record naming the pool and,
on failure, the error, under actions `volume_unlock` / `volume_lock`
(`pkg/seal.VolumeLifecycle`, subject `seal`, since these run inside the
system barrier before any operator request exists to attribute them to).
