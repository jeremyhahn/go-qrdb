# Volume Pools — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first. Decisions
> D1-D10 are ruled; the Phase 1 build is in progress.
>
> This folder is the requirements contract for named volume pools. Every
> decision below is ruled; see [Decisions](#decisions-ruled).
> Tenant moves (decision D5) are **Phase 2** -- everything else in this
> folder is Phase 1. An identifier marked *(new)* did not exist in the tree
> when this was last verified; check the code before assuming it still
> doesn't.

## The problem

A `volume` or `raft` tenant needs a directory of its own on every node that
serves it. That directory holds the tenant's barrier store and its data:

- a `volume` tenant's per-tenant raft host;
- a `raft` tenant's per-database state machines under `databases/{db}`.

Until recently the directory was recorded as an absolute path, chosen by
whichever node allocated it, and every other node trusted that path. On hosts
that share a filesystem, one node opened another node's live store, and the
placement reconciler's purge deleted another node's data. `tenant create
--data-root` let an operator pin such a path by hand.

The interim fix closed the data-loss path:

- tenant locations are recorded as a pool name and a relative slot;
- each node resolves the slot against its own pool;
- records that name a path are refused;
- `--data-root` is refused.

That fix keeps the old pool model, which is a list of paths addressed by
**index**. It is correct only while every node configures the same paths in the
same order. It also leaves four gaps:

- a tenant cannot be confined to the disks it pays for;
- nothing measures a pool or reports its health;
- an operator cannot see which tenant is where;
- there is no way to put a tenant on an encrypted block device.

## The shape

**A volume pool is a named, cluster-wide place a tenant can live.** Each node
maps the name to a mount of its own in its node configuration. A tenant's
location is a **pool name plus a tenant-relative path**, recorded once in the
replicated store and resolved to a directory independently by every node.

```
replicated store (every node reads the same records)
  __sys:volumepool/pools/fast          { provider: "dir", capacity_bytes: 2 TiB }
  __sys:volumepool/assign/acme         { pool: "fast", path: "acme", reserved_bytes: 200 GiB }
  __sys:volumepool/nodes/{node}        lease-backed health report, one per node

node configuration (each node's own)
  node-1: storage.volume_pools: [{ name: fast, path: /mnt/nvme-a }]
  node-2: storage.volume_pools: [{ name: fast, path: /data/fast }]

resolution on each node
  node-1: /mnt/nvme-a/acme
  node-2: /data/fast/acme
```

The pieces:

- **The pool catalog** *(new)* is replicated. It records which pools exist and
  the cluster-wide facts about each one: the volume provider that realizes it,
  its allocatable capacity, and an optional description. Pools are created and
  deleted through the management API by the platform operator.
- **The per-node mount mapping** stays in node configuration. It is the only
  per-node fact about a pool: which local directory realizes it on this node,
  plus provider options such as a block-device path. It is never replicated and
  never settable over the API, because a path names one node's disk.
- **The tenant assignment** is replicated. It records which pool a tenant lives
  on, the tenant-relative path (the tenant id), and the capacity reserved for
  it. This record is the only tenant-location state there is: tenant records,
  placement replicas and tombstones carry none.
- **The node health report** *(new)* is replicated and lease-backed, following
  the same pattern as `cluster.Member` and the feature-gate advertisement. Each
  node publishes the state of every pool it maps: mounted, writable, size,
  free, provider state, and the last error. When the lease expires, a dead node
  reports nothing rather than stale health.
  A standalone node on the `file` or `objstore` backend keeps no leases, so it
  publishes no report.
- **The volume provider** *(new)* realizes a pool's mount on one node. qrdb
  ships the plain-directory provider, `dir`. go-xkms registers a LUKS2 provider
  through a `With*` option. A pool names its provider in the catalog, so a pool
  is the same kind of volume on every node.

## Quotas and entitlements

There is no parallel plane. Pools join the existing tenant quota and entitlement
plane (`pkg/quota`, `TenantService.CheckFeature`,
`TenantService.UpdateQuotas` / `UpdateEntitlements`):

- **Entitlement.** `quota.Entitlements` gains `AllowedVolumePools []string`
  *(new)*. A `volume` or `raft` tenant may be placed only on a pool in that
  list. The list is fail-closed: an empty list entitles no pool.
  `AllowVolumeStorage` and `AllowDedicatedRaft` keep gating the isolation tier
  itself. A tenant needs both the tier and the pool.
- **Capacity.** A tenant lives on exactly one pool. Its existing
  `Quotas.MaxStorageBytes` is the capacity it reserves on that pool. The
  reservation is taken when the tenant is placed (at create, or at a move). It
  is refused when the pool's unreserved capacity cannot cover it. This is the
  "per-pool capacity quota enforced at allocation".
- **Usage.** The existing storage-quota reconciler (`quota.NewReconciler`)
  already walks a volume tenant's directory and reports
  `ResourceStorageBytes`. It walks the pool-resolved directory instead of a
  recorded path, and it also attributes the usage to the pool for the
  management view.

## Management

The management surface is Tier 1, on all six transports, both SDKs, the CLI
and the GUI:

- list pools, with capacity, reserved, used, tenant count and a per-node health
  summary;
- inspect a pool: per node, the mount, provider state, size, free, report age
  and last error; plus the tenants assigned to it;
- create, update (description, capacity) and delete a pool. Deleting is refused
  while any tenant is assigned;
- list a pool's tenant assignments;
- move a tenant to another pool. This is offline and journaled; see
  [Moving a tenant between pools](#moving-a-tenant-between-pools). **Phase 2
  (decision D5): not part of this build.**

`tenant create` takes the pool by name. `tenant get` and `tenant list` report
the tenant's pool and, when the answering node refuses to serve the tenant, a
typed refusal.

## The VolumeProvider seam

The seam follows the pattern of `seal.StrategyRegistry` and `cakey.Provider`. A
small interface lives in qrdb, qrdb ships the dependency-free implementation,
and a higher layer injects its own implementation by name at composition time.
qrdb never imports the higher layer.

```
qrdb  pkg/volume            Provider, Volume, Registry, the "dir" provider   (new)
      pkg/volume            Lockable, VolumeKey, DeriveVolumeKey            (new)
      pkg/seal               VolumeLifecycle: unlock/lock ordering around the
                            system barrier's unseal/seal (decision D6)      (new)
      pkg/server            WithVolumeProvider(volume.Provider) ServerOption  (new)
      sdk/go/node           WithVolumeProvider(volume.Provider) NodeOption    (new)

go-xkms                     a "luks2" Provider over go-luks2, registered through
                            WithVolumeProvider. Its Volume implements
                            volume.Lockable: Unlock maps and decrypts the
                            device given the VolumeKey qrdb derived and
                            passed in; Lock closes it again. go-xkms owns no
                            key of its own for routine unlock -- see
                            "Encrypted volumes and the cluster seal" below.
```

The provider opens a pool's volume on its node and returns the directory
tenants are placed under. It reports capacity and health, and closes the volume
again. The `dir` provider checks that the configured directory exists, is a
directory and is writable, then reports `statfs` numbers. A LUKS2 provider's
`Open` maps the configured block device (without decrypting it: see decision
D6) and reports the same numbers once unlocked. **qrdb holds no LUKS
implementation and contains no LUKS code**; it does hold the one key every
Lockable volume on this node is unlocked with, derived fresh at every unseal
and never persisted.

## Encrypted volumes and the cluster seal (decision D6)

**LUKS (and any encrypted volume) is a VolumeProvider concern, not a
SealStrategy.** A `SealStrategy` releases the barrier's root key; a volume
protects the data at rest underneath the store. The two are related -- the
volume key below is derived FROM the barrier -- but they are not the same
seam, and a LUKS2 provider is never registered as a `SealStrategy`.

The `VolumeProvider` seam gains an optional interface, `volume.Lockable`,
that a `Volume` implements when its underlying device can be locked and
unlocked: `Unlock(ctx, VolumeKey) error` and `Lock(ctx) error`. The `dir`
provider does not implement it, because a plain directory has nothing to
lock. qrdb never imports LUKS code; go-xkms implements `Lockable` for its
`luks2` provider (over go-luks2), composed through the same
`WithVolumeProvider` option a software-only deployment uses for `dir`. The
composition direction stays one-way.

**qrdb derives the key, go-xkms uses it.** qrdb derives a per-volume
`VolumeKey` from the node's own unsealed SYSTEM barrier with HKDF-SHA256 --
the same primitive go-quicraft's `EpochBarrier` already uses to derive its
own per-epoch data keys, so this is no new cryptographic primitive, only a
new, domain-separated info string (`pkg/volume.DeriveVolumeKey`). The info
string binds the node's identity and the pool's name; under decision D1's
one-volume-per-pool-per-node granularity that pair already identifies the
volume. **Nothing derived is ever stored**: it is re-derived fresh at every
unseal and wiped immediately after the `Unlock` call returns. A sealed
node's system barrier has no root key to derive from, so **a sealed node
cannot unlock its volumes** -- this is the property the design exists to
get, not an incidental side effect. go-xkms may register further key slots
(TPM 2.0, PKCS#11) on top of its `luks2` provider for break-glass recovery;
routine unlock always comes through qrdb's own unseal.

**Ordering, enforced by qrdb:**

- **UNSEAL** = the system barrier unseals -> qrdb unlocks every tracked
  `Lockable` volume -> the composer opens tenant/database stores on them.
- **SEAL** (a whole-node seal and the cluster cut-off both reach this) =
  stores on the volume are closed -> qrdb locks every tracked `Lockable`
  volume -> the system barrier's root key is wiped.

The ordering is a property of composition order, not a runtime
negotiation: `pkg/seal.VolumeLifecycle` registers its own unlock/lock
observers on the system barrier at construction, so building it BEFORE the
node's `BarrierRegistry` (which registers its own tenant-restore observer
when IT is built) is what makes "unlock before stores open" true, with no
reaching into the other package's internals.
Locking before the key wipe falls out the same way: `StorageBarrier.Seal`
runs its sealing observers (which includes the lock step) before it zeroes
the root key, and `SealAll` has always closed tenant stores before calling
the system barrier's own `Seal`.

**Failure is per-pool, never per-node:**

- A failed `Unlock` leaves that pool's volume unavailable with a typed,
  audited error (`*seal.VolumeUnlockError`). The node still boots and keeps
  serving every other pool; only this pool's tenants are refused on this
  node, exactly like a `dir` mount that failed to open.
- A failed `Lock` is reported -- typed and audited
  (`*seal.VolumeLockError`) -- but the system barrier's key wipe still
  completes. Sealing a node must not be blockable by a stuck device.

**Scope.** Per node, per pool, tied to the system barrier -- matching D1's
granularity. A per-TENANT volume key, derived from a tenant's own barrier
instead of the system barrier, is a documented future extension of the same
`DeriveVolumeKey` hook (a third, tenant-id component in the info string),
not built now.

## Data flow

**Create.** `tenant create --isolation-type raft --pool fast` reaches
`TenantService.CreateTenant` on any transport. The service then:

1. checks the tier entitlement and the `fast` entitlement;
2. reserves `MaxStorageBytes` against the `fast` catalog capacity with a
   compare-and-swap on the catalog record, so concurrent creates cannot
   over-reserve;
3. writes the assignment `{pool: fast, path: <id>}` with put-if-absent;
4. registers the barrier.

Every node that later serves the tenant joins the assignment's `path` onto its
own `fast` mount, after checking that the result is strictly inside the mount.

**Boot (decision D10).** Each node opens every pool its
configuration maps, through that pool's provider, before it restores
tenants. Opening a `Lockable` volume maps the device without unlocking it --
that only happens on unseal, see "Encrypted volumes and the cluster seal"
above.

A pool whose volume does not open (a missing mount, an absent device, an
unregistered provider) fails `qrdbd start` outright by **default**: the
daemon never serves with some of its tenants' data unopened and silently
unreachable. The error is typed (`*volume.OpenAtBootError`), names every
inaccessible pool and the reason, and the process exits non-zero before any
database or tenant store opens.

The operator opts into the OLD behavior with `--allow-degraded-volume-boot`
(or its config-file equivalent, `storage.allow_degraded_volume_boot`): with
it set, a pool that fails to open is reported as unhealthy, the node still
boots, and every tenant assigned to that pool is **refused on this node**
with a typed error (`*volume.PoolUnavailableError`, classified `Unavailable`
on every transport -- see [engineering.md](engineering.md#error-semantics)).
Its data is not opened, and the rest are served. This matches the rule that
one refused tenant never takes the others down. Each skipped pool is logged
at ERROR, reported in this node's health (`qrdb volume-pool get`) and
exported as `qrdb_volume_pool_ready{pool}` (0), so the degradation stays
visible and is never silent. A pool mapped to a `Lockable` provider whose
volume opened but has not yet been unlocked (the node is still sealed)
reports `StateLocked` and refuses its tenants the same way, under either
boot mode -- `StateLocked` is a normal, transient part of the unseal
sequence, not an open failure the default's fail-fast check reacts to.

**Raft placement.** The placement scheduler places a `raft` tenant's replicas
only on nodes whose latest health report shows the tenant's pool healthy. A
node without the pool is never chosen, so it never has to refuse a replica it
was given. A node whose first report fails -- the system shard is electing a
leader while it boots -- keeps retrying on the report interval, so it joins
placement as soon as that report lands rather than staying out for its whole
life.

**Removal.** Every removal of a tenant or shard directory stays inside the
pool's resolved root. The check is the existing `RequireInsideLocalPool`
guard, with symbolic links resolved on both sides, now applied per pool.

## Moving a tenant between pools

> **Phase 2 (decision D5).** Approved as designed below, but not part of
> this build: Phase 1 ships the catalog, entitlements, health reporting and
> every wire/CLI change except tenant moves. The `TenantMove` /
> `TenantMoveStatus` surface in [api.md](api.md) and the `qrdb tenant move` /
> `move-status` verbs in [cli.md](cli.md) land with this phase, once the
> catalog, entitlements and wire changes below have shipped and proven
> themselves.

**Feasible, offline only.** A move is a stop, copy, flip and restart, recorded
in a replicated move journal so that a crash at any point resumes or rolls
back:

1. Reserve capacity on the target pool. Mark the move `preparing`.
2. Fence the tenant cluster-wide, as the delete fence does. Its runtimes stop
   on every node, so writes are refused with a retryable error rather than
   lost.
3. Each node serving the tenant copies the tenant directory from source to
   target and reports `copied` in the journal. Within one filesystem the copy
   is a rename; across filesystems it is a copy, an fsync of every file and
   directory, then a size and hash verification.
4. When every serving node has reported, one compare-and-swap flips the
   assignment to the target pool and marks the move `committed`.
5. The fence lifts and the runtimes restart on the target.
6. Each node removes the source copy (guarded as above), and the source
   reservation is released.

A node that is down during the move finds a committed journal entry when it
returns. It copies from its own source directory, then removes it.

**Online move is not offered.** A `volume` tenant's raft host and a `raft`
tenant's state machines keep open Pebble stores and WALs under the tenant
directory. Moving them while live would need either:

- dual-writing every store, which nothing in the engine supports; or
- a per-database replica dance (add a replica on the target, transfer
  leadership, remove the source). That works for `raft` tenants only, and only
  across nodes, not within one node.

The cost is out of proportion to the need. The documented downtime is the copy
time of the largest serving node.

## Trust boundaries

- **Paths never cross a node boundary.** Nothing replicated names a filesystem
  path. A mount is configuration on the node that owns it. A record or request
  that carries an absolute path is refused, exactly as today.
- **The tenant-relative path is validated at both ends.** It must be one clean
  path component when written, and the joined path must be strictly inside the
  pool root, with links resolved, when it is used.
- **The routine unlock key comes from qrdb, derived, never stored (decision
  D6).** qrdb derives each `Lockable` volume's key from the node's own
  unsealed system barrier with HKDF and passes it to `Unlock`; the provider
  wipes or discards it after the call, and qrdb wipes its own copy
  immediately. Each node's volume has its own key, bound to that node's
  identity and the pool's name, so no key is transported between nodes --
  consistent with the per-node seal-key ruling -- and a sealed node derives
  nothing, so it cannot unlock. go-xkms may layer its own hardware-backed key
  slots (TPM 2.0, PKCS#11) onto its `luks2` provider for break-glass
  recovery outside the routine unseal path; that key custody, if any, is
  go-xkms's own and never qrdb's.
- **Pool management is a platform-operator surface.** Every verb that changes
  the catalog, an assignment or a move is a cross-scope permission held only
  in the `system` tenant. Listing and inspecting pools is also operator-only,
  because the health report reveals node mounts. A tenant sees its own pool's
  name through `tenant get` and nothing else.
- **Per-tenant encryption is unchanged.** The tenant barrier and DEK encrypt the
  tenant's data whatever the volume is. A LUKS2 pool adds encryption below the
  tenant, it does not replace it.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Pool catalog, assignments, health reports, move journal | `pkg/volume` *(new)* | go-qrdb |
| `Provider` / `Volume` / `Registry` seam and the `dir` provider | `pkg/volume` *(new)* | go-qrdb |
| Tenant location resolution, the inside-pool removal guard | `pkg/seal` (`BarrierRegistry`) | go-qrdb |
| `Lockable`, `VolumeKey`, `DeriveVolumeKey` | `pkg/volume` *(new)* | go-qrdb |
| Unlock/lock ordering around the system barrier's unseal/seal | `pkg/seal` (`VolumeLifecycle`) *(new)* | go-qrdb |
| Entitlement and reservation checks at create and move | `pkg/tenant`, `pkg/quota` | go-qrdb |
| Placement filter on pool health | `pkg/placement` | go-qrdb |
| Management handlers | `pkg/api/{rest,grpc,quic,mcp,unix}`, `sdk/go/transport/*` | go-qrdb |
| `qrdb volume-pool` verbs | `cmd/qrdb` | go-qrdb |
| GUI Volume Pools view | `cmd/qrdb/frontend` | go-qrdb |
| LUKS2 provider implementing `volume.Lockable`, over go-luks2 | — | **go-xkms** |
| Any hardware-backed recovery key slot layered onto the LUKS2 provider | — | **go-xkms** |
| The objstore pool primitive | — | **go-objstore**: no longer used by this feature, see engineering.md |

## Alternatives rejected

- **Keep a pool as an ordered list of paths addressed by slot index.** This is
  the current model. Rejected: the slot means the same disk only while every
  node lists the same paths in the same order. Nothing enforces that, and a
  reordered config silently moves every tenant onto another disk's empty
  directory. A pool with one mount per node has no ordering to get wrong.
  Several disks on one node become several pools, or one pool on a volume
  manager the operator runs beneath it.
- **Named volumes inside a pool**, a second level of name under the pool.
  Rejected: it adds a level that a second pool already provides, and every
  verb, quota and health row would need to address it.
- **Derive the catalog from node configurations**, so a pool exists if any node
  maps it. Rejected: a typo in one node's configuration would create a pool
  tenants could be entitled to. Entitlements and quotas would then name
  something no record vouches for, and a pool missing on a node would be
  indistinguishable from a pool that does not exist.
- **Replicate the mount paths**, a cluster-wide pool-to-path map. Rejected: a
  path names one node's disk, which is the defect this feature exists to end.
- **A separate pool quota plane.** Rejected by the owner: reuse the tenant
  quota and entitlement plane.
- **A round-robin cursor that spreads tenants across pools.** Rejected: the
  pool is the operator's placement decision, and an entitlement or capacity
  rule a cursor cannot see would be bypassed. A create without `--pool` uses
  the tenant's default pool: the first entry of `AllowedVolumePools`.
- **Ship a LUKS implementation in qrdb.** Rejected by the owner: qrdb is the
  software-only layer, and LUKS mounting/unmounting must come from go-xkms
  (over go-luks2).
- **Hand the device key to the LUKS2 provider from a go-xkms key backend,
  independent of qrdb's own seal state.** Rejected under decision D6: that
  would let a sealed node's volume unlock without qrdb ever unsealing,
  breaking the property the design exists to get -- a sealed node cannot
  unlock its volumes. The routine key comes from qrdb's own barrier
  derivation; go-xkms's hardware key slots are for break-glass recovery
  only, not routine unlock.
- **Online moves.** Rejected for cost; see above.

## What this feature deliberately does not do

- **No remote volumes.** A pool's mount is a local directory on its node. A
  provider may sit on a block device; it may not sit on an object store.
- **No per-database pools.** A tenant and all of its databases live on one pool.
- **No automatic rebalancing between pools.** A move is an operator action.
- **No key custody beyond the per-node, HKDF-derived volume key (decision
  D6).** qrdb derives and passes the key for its own `Lockable` volumes, and
  nothing else: no hardware key backend, no persisted key, no key transport
  between nodes. go-xkms may layer its own hardware-backed recovery key
  slots onto the `luks2` provider, but routine unlock always goes through
  qrdb's unseal.
- **No migration of old location records.** A tenant whose record carries a
  path, or a slot index from the interim fix, is refused and re-created, under
  the standing no-backward-compatibility ruling. See decision D9.

## Decisions (ruled)

Every decision below is ruled. D1-D5, D7, D8 and
D9 are the recommendation each item already gave, approved as written. D6 is
replaced by the design in
["Encrypted volumes and the cluster seal"](#encrypted-volumes-and-the-cluster-seal-decision-d6)
above, which this entry only summarizes.

- **D1. Pool granularity: (a) one mount per pool per node.** Approved as
  written. It matches "pool name plus tenant-relative path" exactly and has
  no ordering hazard.
- **D2. Where pools are declared: (a) a replicated catalog the operator
  creates through the API,** with node configuration mapping only names to
  mounts. Approved as written.
- **D3. The capacity quota: (a) the tenant's existing `MaxStorageBytes`
  is its reservation on its single pool,** with the catalog carrying the
  pool's `capacity_bytes` and reservations never exceeding capacity times an
  overcommit ratio (default 1.0). Approved as written.
- **D4. A pool's capacity when the operator does not set one: (a) the
  smallest size any healthy node reports for it,** recomputed from health
  reports, with an explicit value overriding it. Approved as written.
- **D5. Tenant moves: (a) offline journaled move,** approved as designed,
  but **as Phase 2** -- a second implementation phase that starts only
  after the catalog, entitlements and wire changes of Phase 1 have shipped.
  See the note at the top of
  ["Moving a tenant between pools"](#moving-a-tenant-between-pools).
- **D6. Encrypted volumes and the cluster seal: REPLACED.** The two options
  this item originally posed (lock on seal vs. rely on the tenant barrier
  alone) are superseded by the "seal-lifecycle volumes" design ruled in
  full under
  ["Encrypted volumes and the cluster seal"](#encrypted-volumes-and-the-cluster-seal-decision-d6):
  LUKS is a `VolumeProvider` concern, not a `SealStrategy`; the optional
  interface is named `volume.Lockable`, not `volume.Locker`; qrdb derives
  the per-volume key from the unsealed system barrier with HKDF and passes
  it to `Unlock`, nothing is ever stored; ordering (unlock after barrier
  unseal and before stores open; lock after stores close and before the key
  wipe) is enforced by qrdb through `pkg/seal.VolumeLifecycle`; and failure
  is per-pool for a failed unlock, and reported-but-non-blocking for a
  failed lock.
- **D7. The CLI group's place in the locked surface: add `qrdb volume-pool`
  as a qrdb-owned control-plane group,** like `cluster` and `rebalance`.
  **Ruled**, but the edit itself -- adding the group to the locked surface
  -- is a handbook change and is **outside this repository**; it is not
  made as part of this build. This decision only records that the owner
  wants it added; see the company handbook for the actual surface change.
- **D8. The in-process `Client.VolumePools()` client is deleted and
  replaced by the Tier 1 management client.** Approved as written. This is
  BREAKING for SDK consumers, go-xkms included.
- **D9. Records written by the interim fix (`{pool, slot}`): (a) refuse
  them like path records** -- the tenant is deleted and re-created --
  **per the no-backward-compatibility ruling.** Approved as written.
- **D10. Boot behavior when a volume or volume pool cannot be opened
  (ruled): fail loudly by default, with an explicit opt-in to
  the old degraded-continue behavior.** Verbatim: "volumes that are
  inaccessible at boot should fail loudly. we should offer a flag that
  allows the admin to ignore the error and continue starting the
  databases and volumes it can, however." This supersedes this folder's
  earlier framing of invariant 6 ("boot never fails because of a pool")
  as the *only* behavior -- it is now the behavior under
  `--allow-degraded-volume-boot` /
  `storage.allow_degraded_volume_boot`, not the unconditional default.
  See ["Boot"](#data-flow) above and
  [engineering.md](engineering.md#invariants) invariant 6.
