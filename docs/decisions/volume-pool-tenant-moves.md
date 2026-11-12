# Moving a tenant between volume pools

> **Status: PROPOSED — awaiting owner ruling** · Scope: go-qrdb
> Last verified: 2026-11-12 at main e8b579229d
> SPEC row `volume-pool-tenant-moves` (future, phase 2). Nothing here is
> built. Each open question lists its options, a recommendation and the
> lower-project dependency each option needs. The owner rules; the Ruling
> section is empty until then.

## Problem

A `volume` or `raft` tenant lives on exactly one volume pool, named by its
assignment record `__sys:volumepool/assign/{tenant}`
([../volume-pools/architecture.md](../volume-pools/architecture.md), "The
shape"). Today the tenant can never leave that pool: no transport exposes a
move, and `volume.Catalog.MoveTenant` and `GetMove` return
`*volume.MoveNotImplementedError`, classified `Unimplemented`. An operator who
needs to retire a disk, move a tenant onto an encrypted (`Lockable`) pool, or
move it to a larger pool has to delete and recreate the tenant.

`volume.Service` already carries the shape of the API:
`MoveTenant(ctx, tenantID, toPool) (*Move, error)` and
`GetMove(ctx, tenantID) (*Move, error)`, with `volume.Move` holding
`TenantID`, `FromPool`, `ToPool`, `State`, `NodesCopied`, `NodesPending` and
`Error`. This record decides what sits behind it. No other per-node field
is added: `NodesCopied` is the durable record of which directory is
authoritative for a node until cut over. A node in `NodesCopied` opens the
tenant from the destination pool; a node not in it (still in `NodesPending`,
or removed from `NodesCopied` by an abort) opens it from the source pool, as
the assignment says.

The move has to keep every rule the feature already has:

- **Paths never cross a node boundary.** Each node copies only its own
  directory, from its own source mount to its own destination mount. Nothing
  replicated names a path; the move journal names pools and nodes only.
- **Unlock keys are derived per node and pool, never transported.** No step
  of a move carries a volume key between nodes or persists one.
- **One location record.** The assignment stays the only tenant-location
  state; a move is a journal beside it, not a second location.
- **Every removal stays inside the pool's resolved root**
  (`seal.BarrierRegistry.RequireInsideLocalPool`), on both pools.
- **Pool management is a platform-operator surface**, cross-scope, held only
  in the `system` tenant
  ([../volume-pools/api.md](../volume-pools/api.md)).

## The move, phase by phase

The phases below are the recommended shape; the questions after them name the
alternatives at each choice.

1. **Admit.** The operator calls `MoveTenant(tenant, toPool)`. The service
   checks, in one compare-and-swap sequence:
   the tenant exists and is `volume` or `raft`; `toPool` exists, differs from
   the current pool and is in the tenant's `AllowedVolumePools`; no move is
   already journalled for the tenant; `toPool` reports `StateReady` on every
   node that holds a replica of the tenant; and the tenant's
   `MaxStorageBytes` fits `toPool`'s unreserved capacity. It then reserves the
   capacity on `toPool` (CAS on the catalog record, as create does) and writes
   the journal `__sys:volumepool/moves/{tenant}` with state `admitted`, every
   replica node in `NodesPending`. The source reservation stays until retire,
   so the tenant is counted on both pools while it moves.
2. **Copy, one node at a time.** For each pending node in turn, the node
   itself closes the tenant's stores on the source pool (its replica leaves
   the raft group's live set for the step; the others keep quorum), copies its
   own resolved directory into the destination pool's resolved directory,
   fsyncs it, and records the step. The data copied is already ciphertext
   under the tenant barrier, so plaintext never enters the copy path.
3. **Verify, on the same node.** The node compares the destination copy with
   the source (Q4), reopens the tenant's stores from the destination
   directory, waits until its replica has caught up with the group, and moves
   itself from `NodesPending` to `NodesCopied` with a CAS on the journal. The
   node serves the tenant from the destination from here on; its source
   directory is kept, closed, until retire.
4. **Cut over.** When `NodesPending` is empty, one CAS writes the assignment
   `{pool: toPool, path: <id>}` and sets the journal to `cutover`. This is the
   commit point: before it the move rolls back, after it the move only rolls
   forward.
5. **Retire the source.** Each node removes its source directory through the
   inside-pool guard on the source pool and acknowledges. When every node has
   acknowledged, the source reservation is released and the journal is
   deleted. `GetMove` then reports no move; `tenant get` reports `toPool`.

A node added to the tenant's replica set while a move runs is placed on a
node whose health shows **both** pools ready and is added to `NodesPending`.

**Key re-derivation for the destination pool.** Nothing new is derived for
the tenant. A destination pool provided by a `Lockable` volume was unlocked
at that node's unseal with `DeriveVolumeKey(node, toPool)`, exactly as for any
other pool, and the tenant's data lands on an already-unlocked volume. The
tenant barrier and DEK are untouched, because the bytes copied are ciphertext
under them. A node that is sealed, or whose destination volume is
`StateLocked` or unavailable, cannot run its step: the step waits for that
node's unseal (Q3) and the journal reports the node in `NodesPending` with
the typed reason in `Error`.

## Failure and rollback

- **Before cut over**, a failed step (copy error, verify mismatch, the node
  cannot reopen from the destination, the destination pool becomes
  unavailable) is retried on that node (Q6). If it still fails, or the
  operator cancels (Q8), the move aborts. The journal goes to `aborting`;
  nodes still in `NodesPending` remove any partial destination directory.
  A node in `NodesCopied` has served the tenant from the destination since
  step 3, and Q10 serves writes during the move, so its kept source
  directory is stale: it lacks every write the node acknowledged after it
  resumed. The abort therefore never simply reopens the source. One
  `NodesCopied` node at a time (as in step 2), the node quiesces its replica
  (closes its destination stores; the others keep quorum) and, while it stays
  quiesced, makes the source authoritative:
  - **(a) Multi-replica tenant.** The source directory needs no copy: every
    write the node acknowledged is held by a quorum of the other replicas,
    none of which is rolled back at the same time, and the node catches up
    from the group after it reopens, the same restart catch-up step 3 relies
    on. Because its log goes back, the replica is first demoted to a
    non-voting member (a membership change committed in the group), so it
    never votes or counts toward a commit while it lacks entries it once
    acknowledged; it is promoted only once caught up. If go-quicraft cannot
    demote a replica in place, the multi-replica case uses (b) as well.
  - **(b) Single-replica tenant.** No other replica holds its writes, so the
    node copies the destination directory back over the source directory and
    verifies the copy as in Q4.

  Only then, still quiesced, the node leaves `NodesCopied` with a CAS on the
  journal: that CAS durably records that the source is authoritative for the
  node. After it, the node reopens the tenant from the source directory (for
  (a), catching up as a non-voting member and promoted once caught up), and
  last removes the destination directory through the inside-pool guard on
  the destination pool. The destination is never deleted while the journal
  still sends the node to it. A node that crashes before the CAS redoes its
  abort step from the start at boot: it is still in `NodesCopied`, its
  destination directory is intact, and a partial copy-back is simply
  overwritten. A node that crashes after the CAS opens from the source per
  the assignment and removes any leftover destination directory at boot.
  When no node is left in `NodesCopied`, the destination reservation is
  released and the journal is deleted. Every write acknowledged before or
  during the move is on the source when the abort ends, so the abort loses
  nothing. The alternative, making a node's resume on the destination the
  point after which the move only rolls forward, is not picked: it would
  leave cancel open only until the first replica resumes (for a
  single-replica tenant, never after its one step), and a later node whose destination pool turns unready would leave
  the tenant split across two pools with no way back.
- **A node that crashes mid-step** reads the journal at boot. A step that had
  not recorded `NodesCopied` is redone from the start: the partial
  destination directory is removed and the copy restarts. A node in
  `NodesCopied` opens the tenant from the destination pool, because the
  journal, not the assignment, says where that node's copy lives until cut
  over. During `aborting`, a node no longer in `NodesCopied` opens the tenant
  from the source pool and removes any destination directory left behind,
  through the inside-pool guard; it never selects the destination. This is the only time a node resolves a tenant from anything but the
  assignment, and it is bounded by the journal's life.
- **After cut over**, a node that fails to remove its source directory
  retries on the report interval; the move stays in `cutover` and `GetMove`
  reports the node. The tenant is served throughout; only the source pool's
  space and reservation are held.
- **Every failure is typed and audited**: a new `*volume.MoveStepError`
  (node, phase, cause), classified `Unavailable` when the cause is a pool or
  node that is not ready and `Internal` otherwise, and
  `*volume.MoveConflictError` (`FailedPrecondition`) for an admit refusal.

## Transports and permission

Two verbs plus a cancel (Q8), Tier 1 on all six transports, both SDKs and
the CLI, following the pool verbs in
[../volume-pools/api.md](../volume-pools/api.md):

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | CLI | `scp` permission |
|---|---|---|---|---|---|---|---|
| Start a move | `POST /v1/volume-pools/{pool}/tenants/{tenant}/move` | `VolumePoolMoveTenant` | `volumepool.MoveTenant` | `qrdb_volume_pool_move_tenant` | `MoveTenant` | `qrdb volume-pool move-tenant <tenant> --to <pool>` | `volume_pools:move` |
| Inspect a move | `GET /v1/tenants/{tenant}/volume-move` | `VolumePoolGetMove` | `volumepool.GetMove` | `qrdb_volume_pool_get_move` | `GetMove` | `qrdb volume-pool get-move <tenant>` | `volume_pools:read` |
| Cancel a move | `DELETE /v1/tenants/{tenant}/volume-move` | `VolumePoolCancelMove` | `volumepool.CancelMove` | `qrdb_volume_pool_cancel_move` | `CancelMove` | `qrdb volume-pool cancel-move <tenant>` | `volume_pools:move` |

`volume_pools:move` is cross-scope like every `volume_pools:*` permission and
is carried by the built-in Admin role. A tenant sees its own pool through
`tenant get` and, while a move runs, the move's state and nothing else.

## Open questions

### Q1. Quiesce the tenant, or move it online?

- **A. Quiesce.** The whole tenant is refused (`Unavailable`) from admit to
  cut over; every node copies in parallel. Simplest and fastest in wall time,
  but the tenant is down for the length of the largest copy. Dependency:
  none.
- **B (recommended). Rolling, one replica at a time.** One node's replica is
  offline for its own copy step while the others keep quorum, as described
  above. The tenant stays served throughout unless it has a single replica,
  in which case its step is a quiesce of that replica alone. Dependency:
  none; it uses the stop/reopen and catch-up a node restart already uses.
- **C. Raft replica replacement.** Add a new replica of the tenant's shards
  on the destination pool of the same node, let it catch up from a snapshot,
  then remove the source replica; no file copy at all. Dependency:
  go-quicraft would have to host two replicas of one shard on one node host,
  each with its own data directory, and change membership between them.
  This lane did not find that in go-quicraft v0.24.3-alpha; if the owner
  prefers C, go-quicraft needs that API first, and the move waits for its
  tag.

**Reason for B:** it keeps the tenant available with no new lower-project
API, and it reuses the restart path the cluster already proves on every
rolling upgrade. C is the purer raft answer but blocks on go-quicraft; A
trades availability for simplicity the operator did not ask for.

### Q2. How is a node's copy made?

- **A (recommended). File copy of the closed replica directory.** With the
  node's stores for the tenant closed, copy the directory tree from the
  source mount to the destination mount, fsync files and directories, and
  keep the source until retire. Dependency: none.
- **B. Snapshot export and import.** Ask the replica for a snapshot, write it
  into the destination directory, and open a fresh replica from it.
  Dependency: a go-quicraft API to export a shard snapshot to, and restore
  it from, a chosen directory; not verified in v0.24.3-alpha.
- **C. Re-replicate from peers.** Remove the local copy, open an empty
  replica on the destination and let the leader send a snapshot. Dependency:
  none, but the node holds no local copy of the tenant between the removal
  and the snapshot, so a second failure in the group in that window can lose
  quorum's data.

**Reason for A:** the source stays intact until the move commits, which is
what makes rollback free, and it needs nothing from go-quicraft. The copy is
of ciphertext, so it adds no exposure.

### Q3. What does admit require of the destination pool?

- **A (recommended). Ready on every replica node at admit, and again at each
  node's step.** A node whose destination pool is locked, unhealthy or not
  mapped refuses its step; the move waits and reports it. Dependency: none.
- **B. Ready on a quorum of the replica nodes.** The move starts sooner, but
  a node that never maps the pool leaves the move stuck before cut over.
  Dependency: none.

**Reason for A:** it mirrors raft placement, which places a replica only
where the pool is healthy, so a move never starts that cannot finish.

### Q4. How is a copy verified?

- **A (recommended). Manifest plus reopen.** The node builds a manifest of
  the source tree (relative path, size, SHA-256) while copying, recomputes it
  from the destination after fsync, and requires equality; it then reopens
  the stores from the destination and requires its replica to reach the
  group's commit index before it records the step. Dependency: none.
- **B. Reopen only.** Trust the copy and rely on the store's own checksums
  when it reopens and catches up. Faster, but a silently truncated file that
  the store does not read at open passes. Dependency: none.
- **C. State-machine hash.** Compare a hash of the replica's state machine
  across replicas. Dependency: a go-quicraft API to hash a replica's applied
  state at an index; not in v0.24.3-alpha as far as this lane found.

**Reason for A:** it proves the bytes and that the store opens and serves,
with no lower-project API.

### Q5. Where does the move's state live?

- **A (recommended). A move journal beside the assignment.**
  `__sys:volumepool/moves/{tenant}` holds `volume.Move` (pools, state, nodes
  copied and pending, error); every transition is a CAS on it; the assignment
  changes once, at cut over. Dependency: none.
- **B. Per-node location in the assignment.** The assignment gains a node to
  pool map while a move runs. Dependency: none, but it gives the tenant a
  second kind of location record, which the architecture rules out.

**Reason for A:** the assignment stays the one location record, and
`volume.Move` already has the journal's fields.

### Q6. Who drives the steps?

- **A (recommended). A move reconciler on the system shard's leader.** Like
  the placement reconciler, it reads the journal, grants the step to one
  pending node at a time (so at most one replica is offline), retries a
  failed step up to a bounded count (default 3) before it aborts, and writes
  the cut over. The node does its own copy and verify, because only it knows
  its paths. Dependency: none.
- **B. Nodes self-schedule.** Each node takes the next step by a CAS lease
  on the journal and the last node writes the cut over. No leader component,
  but serialising "one replica offline at a time" needs a lease per tenant
  and a stale lease after a crash stalls the move until it expires.
  Dependency: none.

**Reason for A:** one place decides the order and enforces the one-offline
rule; the existing reconcilers show the pattern, and leader change only
moves the reconciler, the journal carries the state.

### Q7. When is the source removed?

- **A (recommended). At retire, right after cut over.** Each node removes its
  source directory once the assignment has flipped; the source reservation
  is released when all have. Dependency: none.
- **B. After a retention window.** Keep the source copy for an operator-set
  time so a move can be undone after cut over. Dependency: none, but the
  tenant is reserved on two pools for the window, a hidden second copy of
  tenant data outlives the move, and undoing after cut over is a second
  move anyway.

**Reason for A:** verification has already proved the destination; holding
the source only double-counts capacity and keeps a copy the operator did not
ask for.

### Q8. Can an operator cancel a move?

- **A (recommended). Yes, until cut over.** `CancelMove` aborts as in
  "Failure and rollback": each node that already serves from the destination
  is quiesced and brought back to the source with every write it
  acknowledged (group catch-up, or a verified copy back for a single
  replica) before the destination is removed. After cut over it is refused
  with `FailedPrecondition`. Dependency: none.
- **B. No cancel.** A move ends only by finishing or failing. Dependency:
  none; one verb fewer, but an operator who started a move onto the wrong
  pool waits for it to finish and moves back.

**Reason for A:** the abort path exists for failures anyway; exposing it
costs one verb per transport.

### Q9. May a tenant move from an encrypted pool to an unencrypted one?

- **A (recommended). Only with an explicit flag.** A move from a pool whose
  provider is `Lockable` to one whose provider is not is refused unless the
  request sets `allow_unencrypted_destination`; the start is audited with the
  flag. Dependency: none.
- **B. Allow whenever the tenant is entitled to the destination.**
  Dependency: none; entitlement already says the tenant may live there.
- **C. Refuse.** Dependency: none; the operator has to recreate the tenant
  to leave a LUKS pool.

**Reason for A:** entitlement says where a tenant may live, not that the
operator meant to drop the at-rest layer below it; the flag makes the
downgrade deliberate and visible, without forbidding it.

### Q10. Which tenant operations are refused while a move runs?

- **A (recommended). Refuse delete, quota change and a second move; serve
  reads and writes.** `tenant delete`, `UpdateQuotas` (it changes the
  reservation) and `MoveTenant` return `*volume.MoveConflictError`;
  deleting either pool is already refused while a tenant or a journal names
  it. Dependency: none.
- **B. Let delete cancel the move first.** Dependency: none; fewer refusals,
  but a delete then implies an abort the operator did not request.

**Reason for A:** every refusal is a fixed rule the operator can read, and
none of them takes the tenant's data path down.

## Tests that would prove it

Per the project's test standard (CLI end to end on every transport, no
sleeps: every wait is on an observable state):

- **Unit, `pkg/volume`.** The journal state machine: every legal transition
  and the refusal of every illegal one; the CAS on admit under concurrent
  moves and creates (no over-reservation); reservation on both pools during
  the move and release at retire and at abort; the admit refusals of Q3, Q9
  and Q10 with their typed errors and classifications.
- **Unit, the node step.** Copy and manifest on a `dir` pool in a temporary
  tree; a corrupted or truncated destination file fails verify; removal of
  both directories goes through `RequireInsideLocalPool`, including a
  symbolic link pointing out of the pool; a crash injected after copy and
  before the journal CAS redoes the step at reopen. The abort step of a
  single-replica `NodesCopied` node with writes made after it resumed on the
  destination: the destination is copied back over the source, a corrupted
  copy-back fails verify and the source is not reopened, and after reopen
  every write acknowledged on the destination is readable from the source.
  Crash injection in that abort step: after the copy-back verifies and before
  the journal CAS, the node is still in `NodesCopied` at boot, redoes the
  abort step and finds the destination intact; after the CAS and before the
  destination is deleted, the node opens from the source and deletes the
  destination at boot; after the deletion, the node opens from the source and
  never selects the missing destination.
- **Unit, the reconciler.** One replica offline at a time; bounded retry,
  then abort; leader change mid-move resumes from the journal; an abort
  returns `NodesCopied` nodes one at a time, and a multi-replica node
  rejoins non-voting and is promoted only once caught up. The same three
  crash points on the multi-replica (a) path: after the demotion and before
  the CAS, the node redoes the abort step with its destination intact; after
  the CAS and before deletion, it opens from the source as a non-voting
  member, catches up, and deletes the destination at boot; after deletion, it
  opens from the source and never selects the missing destination.
- **Unit, keys.** A `Lockable` destination double that records `Unlock`
  calls: the move derives no key and calls `Unlock` on none; a sealed node's
  step waits and reports `StateLocked`.
- **Integration (the volume-pools feature target), every transport.** A
  three-node cluster with two `dir` pools: create a `raft` tenant on one,
  write through the CLI, `volume-pool move-tenant`, keep writing and reading
  during the move, `get-move` until the move is gone, then `tenant get`
  reports the new pool, every value is readable, and the source directories
  are gone. The same with `cancel-move` before cut over (data intact on the
  source, destination directories gone); `cancel-move` after at least one
  replica has resumed on the destination and the test has written through
  the CLI since, for a single-replica tenant and for a three-replica tenant,
  then every write the CLI acknowledged is readable after `cancel-move`
  returns and the move is gone; with a destination pool that is
  unready on one node (admit refused), and without the permission
  (`PERMISSION_DENIED`).

## Ruling

None yet. The owner rules on Q1 to Q10; the Status line and this section
record the ruling, and `docs/volume-pools/` absorbs the ruled design when the
row is built.
