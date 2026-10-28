# Leases — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go`:

```go
func (c *Client) Lease() transport.LeaseClient
```

`sdk/go/transport`:

```go
type LeaseClient interface {
	LeaseGrant(ctx context.Context, ttl int64) (*LeaseInfo, error)
	LeaseRevoke(ctx context.Context, leaseID int64) error
	LeaseKeepAlive(ctx context.Context, leaseID int64) (*LeaseInfo, error)
	LeaseTimeToLive(ctx context.Context, leaseID int64) (*LeaseInfo, error)
	LeaseList(ctx context.Context) ([]*LeaseInfo, error)
}

type LeaseInfo struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         [][]byte `json:"keys,omitempty"`
}
```

Response envelopes: `LeaseGrantResponse`, `LeaseKeepAliveResponse`,
`LeaseTimeToLiveResponse`, `LeaseListResponse` — each wrapping `Lease` or
`Leases`.

Attachment is on the write side, not here:

```go
// transport.MVCCClient
PutWithLease(ctx, key, value, leaseID, opts...) (*MVCCKeyValue, error)
```

The resulting `MVCCKeyValue` carries `LeaseID` (`lease_id`), so a reader can see
which lease a key hangs off. On an MVCC state machine the write is read back
at its revision, so the result carries its full revision metadata. The default
`pebble` state machine attaches the lease just the same but keeps no revision
history: there the result is the key, value and lease written, with zero
revisions, rather than an error after the write applied. This is what lets
lease-backed registrations -- dynamic membership, feature advertisements --
run on the default state machine.

`pkg/lease` is the state machines' side of the contract. Every mutation that
changes durable lease state takes a `Writer`, which is the apply batch:

```go
type Writer interface {
	Set(key, value []byte) error
	Delete(key []byte) error
}

const NoLease int64 = 0
const TablePrefix = "\x00qrdb.lease/"

func (lr *Lessor) Grant(w Writer, ttl int64) (*Lease, error)
func (lr *Lessor) Revoke(w Writer, id int64) ([][]byte, error) // keys still bound, ascending
func (lr *Lessor) Bind(w Writer, key []byte, leaseID int64) error // NoLease releases
func (lr *Lessor) Unbind(w Writer, key []byte) error
func (lr *Lessor) Has(id int64) bool
func (lr *Lessor) LeaseOf(key []byte) int64
func (lr *Lessor) Restore(r storage.Reader) error
func IsTableKey(k []byte) bool

// Checkpoints (the etcd lessor model; see Invariants).
type Checkpoint struct {
	ID           int64
	RemainingTTL int64 // seconds; 0 clears the lease's checkpoint
}

type Checkpointer interface {
	CheckpointEnabled() bool
	Checkpoint(ctx context.Context, token, index uint64, cps []Checkpoint) error
	AwaitApplied(ctx context.Context) error
}

func (lr *Lessor) Applying(index uint64) // the Raft index of the entry about to apply
func (lr *Lessor) ApplyCheckpoints(w Writer, token, index uint64, cps []Checkpoint) error
func (lr *Lessor) RequestCheckpoint() (<-chan error, error)
func WithCheckpoint(interval time.Duration, cp Checkpointer) ExpiryOption
func WithFailoverGrace(grace time.Duration) ExpiryOption
```

`pkg/service` implements `lease.Checkpointer` over a shard's Raft group, and
builds a replicated shard's sweeper options from config:

```go
func NewLeaseCheckpointer(host Host, shardID uint64) lease.Checkpointer
func LeaseExpiryOptions(host Host, shardID uint64, cfg config.LeaseConfig) ([]lease.ExpiryOption, error)
func (s *LeaseService) CheckpointAll(ctx context.Context) error // checkpoint every lease now
```

`statemachine.LeaseRefusal` and `statemachine.OpsLeaseRefusal` answer, before a
write stages anything, whether it names a lease the state machine cannot bind.

`pkg/statemachine`'s `KVStateMachine` -- every `sm_type` it builds: `memory`,
`standard`, `concurrent`, `highperf`, `fast` and `disk` -- keeps durable leases
too, in the same table under the same prefix, and serves the same opcodes:

```go
func WithKVLessor(lr *lease.Lessor) KVOption // the composition root's lessor

// ResolveTypedWithStorage passes KV options through to a KVStateMachine type
// and refuses them, with *KVOptionsNotApplicableError, for any other type.
func ResolveTypedWithStorage(smType, smBackend string, sc StorageConfig, opts ...KVOption) (sm.CreateFunc, error)
```

A `KVStateMachine` always has a lessor: its own, or the one `WithKVLessor`
injects -- the one the composition root's expiry sweeper watches. Every lease
mutation is staged into the same `Backend.ApplyAtomic` set as the write that
caused it, on every backend. With no MVCC store, `OpMVCCPut` and
`OpMVCCDelete` are the native lease-bearing put and the delete, as on the
`pebble` state machine. The lessor is rebuilt from the table when an
in-memory state machine is created (over its empty keyspace), when the disk
state machine opens its storage, and after any snapshot recovery. The
`regular` state machine keeps no leases; `qrdbd` and the SDK's clustered node
refuse it at start (`SMLeasesUnsupportedError` / the registry's
`KVOptionsNotApplicableError`), because cluster membership and the feature gate
are lease-backed.

## Invariants

- **The sweeper is started by the composer, and every clustered composition
  must start it.** `Lessor.RunExpiry` is a loop, not a background service that
  begins on its own; a composition that builds a `Lessor` and hands it to the
  state machine has done half the wiring. Every clustered composition runs it —
  `cmd/qrdbd`, `sdk/go/node`, and in `pkg/server` the tenant host and each
  database's Raft shard — and a new one must too; a standalone node runs it
  through `lifecycle.StartLocalLeaseExpiry`. It takes a NODE-LIFETIME context, never a boot request's ctx,
  which is cancelled as soon as `New`/`Unseal` returns.
- **`lease.mu` guards `expiry` as well as `keys`, and every reader takes it.**
  `KeepAlive` writes `expiry` from the Raft apply goroutine, and `RenewLocal`
  from the reader that served the renewal, while the sweeper reads it from its
  own; an unlocked read there is a data race. The `*Locked` helpers
  (`effectiveTTLLocked`, `remainingTTLLocked`, `isExpiredLocked`,
  `extendLocked`) require the caller to hold `l.mu`, and a caller that already
  holds it, such as `toLease`, calls them rather than a locking variant like
  `isExpired`: `sync.RWMutex` is not reentrant, so a nested `RLock` deadlocks
  the moment a writer queues between the two acquisitions. Lock ordering is
  `Lessor.mu` then `lease.mu`, never the reverse.
- **The sweep holds while the node barrier is sealed.** A revoke is a Raft
  proposal, and a sealed node barrier's log cannot take one. `cmd/qrdbd` and
  `sdk/go/node` pass `lease.WithExpiryHold` the node barrier's sealed state;
  a held sweep revokes nothing, and when the hold is released every lease is
  promoted to a full TTL from that moment (the etcd lessor's `Promote`,
  extend-only) before the sweep resumes. Holders could not renew while the
  node was sealed, so they are not revoked for it; a lease still unrenewed a
  full TTL after the unseal is revoked as usual.
- **Assert the reap by reading the STORE, not `Lessor.Leases()`.** That getter
  filters expired leases, so a test asserting on it passes whether or not
  anything was reaped.
- **Expiry is a committed Raft operation, not a local timer.** Every replica
  observes the cascade delete at the same log position. No node expires a key on
  its own clock.
- **`GrantedTTL` is immutable for the life of the lease**; `LeaseKeepAlive`
  resets `RemainingTTL` to it, it does not change the grant. A checkpoint
  records a remaining TTL beside it, never above it.
- **Attachment is many-to-one**: many keys, one lease. Deleting the lease
  deletes all of them.
- **A key is bound to the lease of its latest write, and to at most one.** Every
  write path calls `Lessor.Bind` with the lease the write named: `PutWithLease`,
  a plain `Put` (which binds `NoLease`, releasing the key), the conditional
  puts, and every put inside a batch or transaction. Every delete path,
  including compare-and-delete and transaction deletes, calls `Unbind`.
  Compare-and-swap does neither: it edits the value, and the key keeps its
  lease on both state machines. `Revoke` therefore returns only keys whose
  latest write named the revoked lease. The binding goes into the same apply
  batch as the value, so the two commit together or not at all.
- **The lease table is replicated state, not a node-local cache.** It lives in
  each state machine's raw keyspace under `lease.TablePrefix` (`\x00qrdb.lease/`):
  one record per lease (ID -> granted TTL), one checkpoint record per
  checkpointed lease (ID -> remaining TTL in seconds), one renewal record per
  lease renewed through the log since recording started (ID -> applied index),
  the renewal marker (the index recording started at), one binding record per
  bound key (key -> lease ID), and the highest ID ever granted. The 0x00 prefix hides it
  from user key enumeration. Unlike the last-applied index, which is also
  0x00-prefixed but local to a node, the lease table is carried in snapshots and
  covered by the replica hash (`store.isNodeLocalKey` draws the line).
- **The lessor is rebuilt from the table whenever storage changes underneath
  it.** `PebbleStateMachine.Open` and `RecoverFromSnapshot` call
  `Lessor.Restore`. The MVCC engine restores its lessor whenever it is
  constructed, which covers `MVCCStateMachine.Open`, its snapshot recovery, and
  the standalone engine. A table that fails to decode or contradicts itself
  (a binding to a lease it does not hold, an ID above the sequence) is a
  `lease.CorruptTableError`, and the state machine refuses to open rather than
  guess which keys a lease owns.
- **Restored leases count from their effective TTL.** The deadline is
  wall-clock state that was never replicated, so after a gap of unknown length
  the only safe value is one no renewal can have exceeded: the checkpointed
  remaining TTL when the lease has one, otherwise the granted TTL. Every renewal
  since a checkpoint went through the log and deleted it (below), so the
  checkpoint is such a value. Expiring early deletes keys whose owner may still
  be renewing; expiring late costs that much staleness.
- **A keep-alive is renewed in the leader's memory, never one log entry per
  renewal.** `Lessor.RenewLocal` extends the deadline; it returns
  `*lease.NotPrimaryError` unless the leader gate `RunExpiry` installed reports
  this replica as the leader, so a replica that is not leading, or a `Lessor`
  nobody sweeps, never renews on its own. The service layer calls it inside
  `host.SyncRead(LeaseKeepAliveQuery)`, whose heartbeat quorum is what confirms
  leadership at answer time, and falls back to the replicated
  `OpLeaseKeepAlive` proposal on `*lease.NotPrimaryError` (a follower, and the
  state machines whose `Lookup` always answers it, such as `fast`). While the
  shard's `featuregate.LeaseCheckpoint` gate is closed — a mixed-version group
  — `LeaseService.KeepAlive` does not renew in memory at all and proposes
  every renewal, the leader's included: an older replica learns of a renewal
  only from the log, and would revoke the lease on winning an election.
- **A lease with a checkpoint is renewed through the log.** `RenewLocal` also
  refuses, with `*lease.CheckpointedError`, a lease that has a checkpoint
  recorded or one proposed and not yet applied, and the service layer falls
  back to `OpLeaseKeepAlive` in the same way. `Lessor.KeepAlive`'s apply deletes
  the lease's checkpoint record on every replica, in the same batch. This is
  the on-demand checkpoint etcd proposes on a renewal: without it a renewal in
  the leader's memory would leave the checkpoint understating the lease, and a
  new leader would expire it early.
- **Checkpoints are periodic, batched and carry the remaining TTL.** `RunExpiry`
  with `lease.WithCheckpoint` runs a round every `lease.checkpoint_interval` on
  the leader. A round takes each live lease whose effective TTL is longer than
  the interval — etcd's rule — and each lease whose earlier checkpoint has not
  applied, records its remaining TTL rounded up to a whole second and capped at
  the granted TTL, and proposes them through `service.NewLeaseCheckpointer` as
  `OpLeaseCheckpoint` commands of at most `protocol.MaxLeaseCheckpointBatch`
  (1000) leases. `Lessor.ApplyCheckpoints` writes or, for zero, deletes each
  record; it never moves a deadline, and it skips a lease already revoked.
- **A checkpoint carries the applied index it was sampled at.** The
  `OpLeaseCheckpoint` payload is `[token:8][index:8][count:4][entries]`, and
  `index` is the last entry this replica had applied before the round sampled
  (`Lessor.sampledIndex`). Every apply site calls `Lessor.Applying(entry.Index)`
  before it dispatches a lease command — the kv state machine, the pebble
  store and the MVCC engine alike — so the lessor knows the index each command
  applies at.
- **A renewal records the index it applied at.** Once recording has started,
  `Lessor.KeepAlive` writes `r/<id>` (the applying index, 8 bytes big-endian)
  in the same batch as the renewal; `Revoke` deletes it with the lease.
  Recording starts at the group's first applied checkpoint, which writes the
  marker `rfrom` (its own entry index). Until then nothing records renewals,
  so a group that never checkpoints keeps byte-identical replicated state.
- **The apply rejects a checkpoint a renewal superseded.**
  `Lessor.ApplyCheckpoints` skips a lease's entry when
  `index < max(rfrom, renewIndex)`: a renewal of that lease applied after the
  sample, or the sample predates the marker and so predates the renewals being
  recorded at all. The renewal's full TTL stands. The rule reads only
  replicated state, so every replica reaches the same verdict, and so does a
  restart, a log replay or a snapshot restore (`Restore` reads `r/` and
  `rfrom` back). A renewal without the marker is a `*lease.CorruptTableError`.
  A renewal's index is never compared with the marker's: that order belongs to
  the host's log, and a host whose entry index restarts with the process must
  not make its table unreadable.
- **The first checkpoint is sampled twice.** The checkpoint that writes the
  marker is itself rejected. When a round's proposal is the one that started
  recording, `checkpointRound` samples again and proposes once more, so the
  checkpoint before a leadership transfer is not lost. A group pays for this
  with one extra entry over its lifetime.
- **The leader's pending marks are leader-local.** A lease proposed in round
  *n* carries `pending = n` until that round applies, and `RenewLocal` refuses
  it meanwhile. A failed proposal leaves the lease pending, so the next round
  retries it.
- **A leader checkpoints every lease before it hands over.** Before a
  scale-down or a replica move transfers leadership away from it
  (`transferLeadershipAwayFromRemoved`), the leader calls
  `LeaseService.CheckpointAll`, which asks the lessor's loop
  (`Lessor.RequestCheckpoint`, through a `LeaseCheckpointNowQuery` read) for a
  round over every live lease and waits until it has applied. It is bounded and
  best-effort: a failure is logged and the transfer proceeds.
- **A new leader promotes only after applying its predecessor's log.** On the
  tick where the leader gate goes false to true, `RunExpiry` first calls
  `Checkpointer.AwaitApplied` — a linearizable read, which Raft serves only once
  the new leader has committed an entry of its own term — so every checkpoint
  the previous leader committed has applied here. Until it succeeds the replica
  neither sweeps nor checkpoints and retries on the next tick, as etcd promotes
  its lessor only once the first entry of the new term has applied. Then
  `Lessor.Promote(grace)` floors every deadline at *now + effective TTL +
  `failover_grace`*, drops the leader-local marks, and the sweep runs. Promotion
  never shortens a deadline.
- **`Lessor.KeepAlive` never shortens a deadline.** A replicated renewal takes
  the later of the current deadline and *now + TTL*.
- **A follower's deadline does not follow the leader's in-memory renewals.**
  Only a replicated renewal, a restore and its own promotion move it. Expiry is
  the leader's call, so `Lessor.Leases` on a replica whose leader gate is false
  lists every lease it holds — each stays until the leader's revoke applies —
  rather than hide one its stale deadline says has passed.
- **The remaining TTL is the leader's answer.** For the same reason
  `Lessor.TimeToLive` on such a replica refuses a lease it holds with
  `*lease.StaleTTLError`, carrying the lease, rather than report its stale
  deadline; an unknown ID is `*lease.NotFoundError` on every replica. Both
  engines' TimeToLive queries go through it. `LeaseService.TimeToLive` turns
  the refusal into `*service.LeaseTTLNotLeaderError`, which unwraps to
  `*service.NotLeaderError` naming the shard leader's replica ID and Raft
  address (the address is empty on a host that implements `service.Host`
  without `raftadmin.Host`, which cannot report the membership), so the
  gRPC, unix, QUIC and MCP listeners forward it as any not-leader refusal; REST
  forwards it in `handleLeaseTimeToLive`, the embedded `forwardingScope`
  through `ReplicaForwarder.ForwardToLeader`, and the etcd listener answers
  `Unavailable`. Callers that need only existence — `rowlock.Table.leaseAlive`
  and the namespaced view's ownership check — take the follower's lease through
  `service.LeaseExists`, so a database never learns of another's lease from the
  refusal.
- **Checkpoints need every replica to understand them.** `OpLeaseCheckpoint`
  is an opcode behind the `lease-checkpoint` cluster feature
  (`featuregate.LeaseCheckpoint`). `LeaseService.CheckpointEnabled` reports it,
  and until it is enabled a round proposes nothing and a new leader counts every
  lease from its granted TTL. The applied index and the renewal records share
  this gate rather than taking one of their own: they change only the
  `OpLeaseCheckpoint` payload and the records its apply starts, and with the
  gate closed neither exists, so there is no older checkpoint format for a new
  gate to protect.
- **`pkg/lease` has a clock seam.** `lease.WithClock` replaces the time source
  of a `Lessor`; the renewal, promotion and checkpoint tests run on a fake clock
  and never sleep.
- **Lease IDs come from a replicated sequence.** `Grant` takes the next ID after
  the persisted highest, so a replica that restarts or recovers allocates the
  same ID as its peers for the next grant, and a revoked lease's ID is never
  reused.
- **A `Lessor` serves exactly one state machine.** `Restore` replaces its whole
  table, so sharing one across shards would let one shard's recovery erase
  another's leases. Every composition builds one per shard.
- **The table is written before memory changes.** A `Grant`, `Revoke` or `Bind`
  whose table write fails returns `lease.TableWriteError` and leaves the
  in-memory state as it was. The state machine treats that error as an apply
  fault, never as a refusal: a fault local to one replica, answered as a
  refusal, would diverge it from peers whose write succeeded.
- **Reading or writing a leased key does not renew the lease.** Only
  `LeaseKeepAlive` does.
- **Revoke is immediate and idempotent from the caller's view**: revoking an
  already-expired lease is not an error worth distinguishing, and both end with
  the lease gone.
- **A lease ID is meaningful only within its (tenant, database) scope.** Lease
  IDs are global integers, so the scope is recorded rather than implied: at
  Grant the namespaced lease view attaches an ownership record
  (`__sys:lease-owner/<tenant>/<database>/<id>`, outside every database's
  keyspace) to the new lease. `Leases` lists only leases carrying the caller's
  record; `KeepAlive` and `TimeToLive` answer any other ID with the same
  `service.LeaseNotFoundError` a missing lease gets, and `Revoke` with the same
  idempotent success -- without revoking it. The no-op is answered by the view
  rather than passed to the lessor, since IDs are allocated in sequence and a
  not-yet-granted ID passed through could revoke a lease another scope is
  granted in between. The record is
  attached, so it is replicated with the lease and deleted with it. A lease
  whose record cannot be written is revoked and the grant fails -- except a
  lease the expiry sweep revoked between the grant and the record: the grant
  and the record are two proposals, so a short lease can expire in between.
  That lease was granted, and IDs are never reused, so the grant answers with
  it and the scope then finds it expired, as it finds any lease whose TTL ran
  out. A write may
  attach only a lease its own namespace owns: `PutWithLease` and
  `PutWithLeaseAndMode` (and the etcd `Put` that rides on them) answer any
  other lease ID with `service.LeaseNotFoundError` and write nothing. This
  holds on every transport, etcd included.
- **TTL granularity is seconds.**

## Error semantics

Typed errors; `service.LeaseExistsError` and its siblings are matched by type at
the transport boundary, never by string.

| Condition | Behavior | Fails closed? |
|---|---|---|
| Lease ID not found (expired, revoked, or never existed) | not-found | yes |
| Attaching a write to an unknown lease | the write is **refused**: by the namespaced view before proposal (`service.LeaseNotFoundError`), and again at apply if the lease was revoked or expired in between. The apply-time refusal carries the lease ID (`protocol.EncodeLeaseNotFoundError`), and the service layer answers it with the same `service.LeaseNotFoundError`, so the caller sees one error type, `NOT_FOUND` on every transport, whichever side caught it. A batch or transaction naming one stages none of its operations | **yes** — a key silently written without its lease would never expire |
| A write naming a lease on a state machine built without a lessor | refused as unimplemented (`service.CapabilityUnsupportedError`) | yes |
| `ttl <= 0` on grant | invalid-argument (`service.CommandRejectedError`, encoded by the state machine as an `AppErrorInvalid` refusal) | yes |
| Grant, revoke or keep-alive on a state machine built without a lessor | unimplemented (`service.CapabilityUnsupportedError`) | yes |
| Missing `lease:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |
| Keep-alive against an already-expired lease | not-found; the keys are already gone. The etcd API answers it as etcd does: the lease ID with TTL 0 on a stream that stays open | **yes** |

The refusal on attaching to an unknown lease is the important one: accepting the
write and dropping the association would produce a key that outlives its owner
with no way to notice.

**The typed error originates where the lease table lives.** `raft.sm_type`
selects the state machine, and each one that holds a `*lease.Lessor` answers
`LeaseTimeToLiveQuery` itself. The two MVCC-capable backends, `pkg/engine`'s
`MVCCEngine` (`sm_type: mvcc`) and `pkg/store`'s `PebbleStateMachine`
(`sm_type: pebble`, the default), both call `Lessor.TimeToLive`, whose absence
test is `Lessor.LookupOrNotFound`, and translate its `*lease.NotFoundError` into
`*service.LeaseNotFoundError` at their own layer, so the two cannot drift apart
on what "not found" means and the match on `service.LeaseNotFoundError` at
every transport boundary fires. An expired lease and an absent one are
indistinguishable from the read path up: the expiry sweeper deletes an expired
lease's entry outright rather than tombstoning it.

`sdk/go/transport.LeaseNotFoundError` / `LeaseExpiredError` cross `embedded`,
`grpc`, `unix`, `quic` and `mcp` as the same two types. All four remote
transports gate on a **not-found status** — `codes.NotFound` on `grpc` and
`unix`, `wire.StatusNotFound` on `quic`, the `NOT_FOUND` (−32004) error code on
`mcp` — and then split the two lease conditions apart by message text at the
lease call sites, because no wire here carries an expired-specific sibling of
not-found.

Two things about that split are worth stating, because they look alike and are
not. Deciding **which subject** is missing from a status shared across subjects
by reading prose is fail-open classification and is forbidden. Deciding **which
of a lease's two conditions** it is, at a call site that already knows the
subject is a lease and already holds the lease id, is not: the id on the typed
error comes from the caller's own argument, never from the message.

`quic` gates on `wire.StatusNotFound`, not on the status a transport fault
carries, so a gone lease and a broken node never share a status; see
[the API server's status vocabulary](../api-server/api.md#the-quic-status-vocabulary).
`mcp` gates on the not-found code before it reads the message, so a tool failure
of any other kind is never reported as a lease condition; see
[the MCP error vocabulary](../api-server/api.md#the-mcp-error-vocabulary).

**REST is the exception.** Only `LeaseTimeToLive` returns a typed
`LeaseNotFoundError` (from a 404); `LeaseRevoke` and `LeaseKeepAlive` return a
generic `RequestError` for a 404, and REST never returns `LeaseExpiredError`,
because the server answers 404 for both not-found and expired.

**Non-goal for now:** telling not-found from expired on REST needs a wire
change (a distinct status or body field); until then `sdk/go`'s lease monitor
sees an external revoke over REST only at the lease's own expiry.

## Configuration

The `lease:` section of the YAML config has two keys that govern a lease across
a leader change. Both are durations in Go syntax, parsed and validated at load
(`pkg/config.Validate`), and a bad value refuses to start the node with a
`ValidationError` naming the key.

| Key | Default | Valid range | Meaning |
|---|---|---|---|
| `lease.checkpoint_interval` | `5m` | `> 0`, at most `1h` | How often the leader checkpoints the remaining TTL of every lease whose TTL is longer than this. A new leader or a restarted replica counts a checkpointed lease from what it had left. Shorter intervals checkpoint more leases more often, and each checkpointed lease's next renewal goes through the log. |
| `lease.failover_grace` | `5s` | `>= 0`, at most `1h` | Extra life a new leader gives every lease beyond its effective TTL — the checkpointed remaining TTL, or the granted TTL — when it takes over. `0` gives none. |

An empty value takes the default. The defaults live once, in
`pkg/lease` (`DefaultCheckpointInterval`, `DefaultFailoverGrace`), and a test
pins `pkg/config`'s string defaults to them. Choose the grace at least as long
as the time a holder needs to notice a leader change and renew: leader election
plus one renewal cadence. A lease whose TTL is not longer than the checkpoint
interval is never checkpointed, so a new leader gives it its full TTL plus the
grace. A larger grace only delays reaping of an abandoned
lease after a failover.

A standalone node has no other replica, so it ignores both checkpoint keys.

The expiry sweep runs on the leader, once every
`lease.DefaultExpiryInterval` (one second), in every composition that starts it
(see Invariants); there is no setting for it. An unrefreshed lease's keys are
therefore reaped within its TTL plus one interval, plus the time the revoke
itself takes to apply.

The lessor's time source is a composition seam: `server.WithClock` /
`node.WithClock` hand the node's `clock.Clock` to the lessor, so lease time and
its expiry sweeper follow it. Nil keeps the real clock. A composer anchors lease
expiry to a trusted time source with it; a test shares one fake clock between a
node and its client so a lease's expiry follows the fake. The same clock reaches
the key-delivery receiver and the invitation-redeem and holder-act token
buckets. See [release/extension-seams.md](../release/extension-seams.md#the-clock).

## Persistence

Lease objects and key bindings live in the lease table described under
Invariants, written in the apply batch and replicated like any other
state-machine record. They are **not** visible through user-facing KV
enumeration — see the internal-key guard in
[../key-value/engineering.md](../key-value/engineering.md). A snapshot carries
the table, so a recovering replica restores keys and their bindings together.
The MVCC record's `LeaseID` is reported to readers; the binding that revoke and
expiry act on is the lease table's.

**Upgrading.** Leases granted by a release without the lease table are not in
any table, so they do not survive the upgrade's restart. Keys still bound to such a lease stay as they
are and never expire. Re-grant and re-write them, which is what a client whose
keep-alive starts returning not-found does anyway.

## Test strategy

Each layer proves the part of the contract it owns:

- **`pkg/lease` unit tests**: the lessor alone — grant, keep-alive, revoke,
  list, expiry, promotion and checkpointing, plus the lease table's encoding
  and every corruption its reader refuses.
- **State machine tests** (`pkg/statemachine`, `pkg/store`): the lease table
  and key bindings through committed commands on every lease-capable state
  machine — re-binding and release on later writes, revoke deleting exactly
  the bound keys, snapshot recovery, restart and log-replay determinism.
- **Service tests** (`pkg/service`): the keep-alive's local-renewal and
  replicated fallback paths, the checkpointer, and failover on a real
  three-replica Raft group (`sdk/go/testutil/raftharness`).
- **Integration tests** (`test/integration`): real `qrdbd` daemons driven
  through the CLI on every daemon transport and through a stock etcd client:
  keep-alive and expiry, binding across a cluster restart, and a renewal that
  survives a leader kill.

**No `time.Sleep` in the unit tests of `pkg/lease`**: time is driven through
the clock seam (`lease.WithClock`). Tests on a real Raft group or real daemons
run on the wall clock and poll for the outcome against a bound
(`raftharness.Bound`, a generous deadline), never a fixed sleep standing in for
a condition. `make test-pkg PKG=./pkg/lease/...` runs the unit tests, and
`make integration-test-leases` the integration tests (below). Contention
behaviour of the keep-alive latency tail is in
[../../test/performance/lease-keepalive.md](../../test/performance/lease-keepalive.md).

## Observability

- Lease grant, keep-alive, revoke and expiry are audited through
  [../audit/](../audit/).
- Cascade deletes appear as ordinary delete events on
  [../watch/](../watch/) streams, which is how a consumer learns a session ended.
- Log events, all from `pkg/lease` at `Debug` unless noted:
  `lease expiry: promotion awaits the previous term` (a new leader's
  `AwaitApplied` has not succeeded yet; retried next tick),
  `lease expiry: revoke failed` (retried next sweep),
  `lease checkpoint: propose failed` (retried next round), and, at `Warn`
  from `pkg/service`, `lease checkpoint before leader transfer failed;
  transferring anyway`.
- There are no lease metrics.

## Integration testing

`make integration-test-leases` runs this feature's self-spawned integration tests: `lease_binding_test.go`, `lease_cli_test.go` and `lease_keepalive_test.go`. The etcd keep-alive case runs with the etcd suite, `make integration-test-etcd-compatibility`. The per-transport accounting, including every gap, is the `leases` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
