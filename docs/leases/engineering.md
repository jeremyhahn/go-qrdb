# Leases — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13
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
// KV
Put(ctx, key, value, opts...)                        // lease via WriteOption
// MVCC
PutWithLease(ctx, key, value, leaseID, opts...) (*MVCCKeyValue, error)
```

The resulting `MVCCKeyValue` carries the `lease` field, so a reader can see
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
state machine opens its storage, and after any snapshot recovery. The legacy
`regular` state machine keeps no leases; `qrdbd` and the SDK's clustered node
refuse it at start (`SMLeasesUnsupportedError` / the registry's
`KVOptionsNotApplicableError`), because cluster membership and the feature gate
are lease-backed.

## Invariants

- **The sweeper is started by the composer, and every clustered composition
  must start it.** `Lessor.RunExpiry` is a loop, not a background service that
  begins on its own; a composition that builds a `Lessor` and hands it to the
  state machine has done half the wiring. All three clustered compositions run
  it — `cmd/qrdbd`, `pkg/server`'s tenant host, and `sdk/go/node` — and a new
  one must too. It takes a NODE-LIFETIME context, never a boot request's ctx,
  which is cancelled as soon as `New`/`Unseal` returns.
- **`lease.mu` guards `expiry` as well as `keys`, and every reader takes it.**
  `KeepAlive` writes `expiry` from the Raft apply goroutine, and `RenewLocal`
  from the reader that served the renewal, while the sweeper reads it from its
  own; an unlocked read there is a real data race, measured
  under `-race`. The `*Locked` helpers carry the
  caller-holds-the-lock contract and are deliberately NOT wrapped by the
  locking variants: `toLease` already holds `l.mu` when it needs the remaining
  TTL, and `sync.RWMutex` is not reentrant, so a nested `RLock` deadlocks the
  moment a writer queues between the two acquisitions. Lock ordering is
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
  anything was reaped — which is how the embedded gap stayed invisible.
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
  `*service.NotLeaderError` naming the shard leader's Raft address, so the
  gRPC, unix, QUIC and MCP listeners forward it as any not-leader refusal; REST
  forwards it in `handleLeaseTimeToLive`, the embedded `forwardingScope`
  through `ReplicaForwarder.ForwardToLeader`, and the etcd listener answers
  `Unavailable`. Callers that need only existence — `rowlock.Table.leaseAlive`
  and the namespaced view's ownership check — take the follower's lease through
  `service.LeaseExists`, so a database never learns of another's lease from the
  refusal.
- **Checkpoints need every replica to understand them.** `OpLeaseCheckpoint`
  is a new opcode behind the `lease-checkpoint` cluster feature
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

**The typed error has to originate where the lease table actually lives, and
there are two of those.** `raft.sm_type` selects between two clustered state
machine backends that each hold their own `*lease.Lessor` and each answer
`LeaseTimeToLiveQuery` independently: `pkg/engine`'s `MVCCEngine` (`sm_type:
mvcc`) and `pkg/store`'s `PebbleStateMachine` (`sm_type: pebble`, the
default). Every transport already matched on `service.LeaseNotFoundError`,
but both `Lookup` implementations answered a missing lease with a generic
query error instead of the typed one, so the match at every transport
boundary never fired for a real deployment -- only for tests that called the
service layer directly with a hand-built typed error. The "lease is absent"
decision now lives in one place, `Lessor.LookupOrNotFound`, which both
`Lookup` implementations call and translate into `*service.LeaseNotFoundError`
at their own layer, so the two backends cannot drift apart on what "not
found" means. `Lessor.Lookup` still does not distinguish an expired lease
from an absent one -- the expiry sweeper deletes an expired lease's entry
outright rather than tombstoning it -- so the two conditions remain
indistinguishable from the read path up, exactly as this document already
required.

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

The `quic` gate used to be `wire.StatusError` — the same status a transport
fault carries — so the words "lease not found" were the only thing separating a
gone lease from a broken node. That changed when the QUIC wire gained a status
vocabulary; see
[the API server's status vocabulary](../api-server/api.md#the-quic-status-vocabulary).

`mcp` had no gate at all: it tested the message for "lease expired", then for
"not found", on **any** tool failure, so a broken node whose prose carried
either phrase was reported as a lease condition. It now gates on the same
not-found code as its siblings before the message is consulted, which is the
difference between a split made at a call site that knows its subject and a
guess made about an unclassified failure. See
[the MCP error vocabulary](../api-server/api.md#the-mcp-error-vocabulary).

**REST is the exception, and it is a partial one.** Only `LeaseTimeToLive` maps
a typed lease error (`transport/rest/transport_lease.go` turns a 404 into
`LeaseNotFoundError`). `LeaseRevoke` and `LeaseKeepAlive` go through
`postJSON`/`postJSONDecode`, whose 404 lands in `decodeErrorResponse` and comes
back as a generic `RequestError`. And REST emits **no `LeaseExpiredError` at
all**: `mapHTTPError` sends both the not-found and the expired case to
`http.StatusNotFound`, so the wire cannot tell them apart — a client would have
to parse the body text, which is what #141 exists to eliminate.

Closing that gap means giving REST a status or body field that distinguishes
the two, which is a wire-format change and not a client-side mapping; it is not
done here. `sdk/go`'s
lease monitor (`coordination.go`) is the reason this matters: it detects
external revocation at the revoke rather than at the lease's own expiry only
when the absence arrives typed.

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
(`pkg/server/lifecycle.StartLocalLeaseExpiry`, the raft backend factory, the
tenant host); there is no setting for it. An unrefreshed lease's keys are
therefore reaped within its TTL plus one interval, plus the time the revoke
itself takes to apply. *(Corrected: this said the cadence follows the Raft
tick, which no composition does.)*

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

*(Corrected: this section used to say the binding lived on the key's MVCC
record, so a snapshot restored keys and bindings together. It did not. The
lessor's table was held only in memory on both state machines. A restart or
snapshot recovery came back with no leases at all, so leased keys never
expired, and the next grant reused ID 1. The MVCC record's `lease` field is
still written and reported to readers, but the binding is the lease table's.)*

**Upgrading.** Leases granted by a release without the lease table are not in
any table, so they do not survive the upgrade's restart. That matches what a
restart has always done to them. Keys still bound to such a lease stay as they
are and never expire. Re-grant and re-write them, which is what a client whose
keep-alive starts returning not-found does anyway.

## Test strategy

- **Unit**: `pkg/lease` — grant, keep-alive resetting `RemainingTTL` to
  `GrantedTTL`, revoke cascade, list, and expiry sweep. Each function has a
  happy path and an error path. `bind_test.go` covers re-binding, release,
  idempotence, unknown leases and write faults; `table_test.go` covers the
  table round trip, replacement on restore, every corruption the reader
  refuses, and read faults.
- **KV state machines**: `pkg/statemachine/kv_lease_test.go` runs grant,
  keep-alive, revoke deleting exactly the bound keys, re-binding on a later
  write (plain put, delete, a move to another lease; compare-and-swap keeping
  it), batch and transaction binding with refusal of an unknown lease,
  snapshot recovery onto a fresh replica with its next lease id and replica
  hash, and log-replay determinism on every backend: standard, concurrent,
  highperf, fast and disk. The disk state machine is also restarted over its
  own storage, and a corrupt table fails its open. Removing any one of the
  table restore on snapshot recovery, the restore on open, the release on a
  plain write, the reset on create, the revoke's deletes, or the client-write
  refusal turns a case red. `kv_lease_bytes_lookup_test.go` holds the
  non-allocating point reads to the same rule: go-quicraft routes a `[]byte`
  query to `BytesLookup`, not `Lookup`, so the disk and highperf adapters'
  `BytesLookup` (and highperf's `BytesLookupInto`) return nothing for the
  `seq`, `l/<id>` and `b/<key>` records.
- **State machine**: `pkg/store/lease_binding_test.go` runs every case against
  both lease-capable state machines through committed commands only. It covers
  re-put under another lease, re-put without a lease, base KV put,
  compare-and-swap keeping the lease, delete then recreate, transaction and
  batch paths, refusal of a write naming a revoked lease, restart, snapshot
  recovery onto a replica that held other leases (with a hash comparison),
  expiry through the real sweeper, and a corrupt table refusing both open and
  recovery. Run its behavioural cases against a tree without the binding and
  ten of the eleven fail. Compare-and-swap passes because that behaviour was
  already correct.
- **Integration** (`test/integration/lease_binding_test.go`): a three-node
  qrdbd cluster re-puts and revokes, then repeats the check after a full
  cluster restart over the same data directories, on every replica, on
  `sm_type: mvcc`. The standalone multi-protocol suite repeats the re-put case
  over every transport. A three-node cluster on `sm_type: memory`
  (`TestDEK_Cluster_EveryNodeDecryptsEveryVersion`) and on `sm_type: disk`
  over file storage (`TestRaftSMOverFileStorage_ThreeNodeCluster`) forms only
  because its lease-backed membership registrations now apply.
- **No `time.Sleep` in the unit tests of `pkg/lease`.** Expiry, renewal,
  promotion and checkpointing are exercised by driving the clock seam
  (`lease.WithClock`), not by waiting — the only way these tests stay
  deterministic on a loaded runner. `pkg/lease/renew_test.go` covers
  `RenewLocal` (refused without a leader gate, `NotFound`), `KeepAlive` never
  shortening a deadline, `Promote` flooring at *now + effective TTL + grace*,
  counting from a checkpoint and dropping the pending marks, and `RunExpiry`
  promoting on the leadership edge only, only once `AwaitApplied` succeeds, and
  checkpointing long leases while it leads. `pkg/lease/checkpoint_test.go`
  covers a round proposing only leases that outlive the interval, stopping once
  the checkpoint is inside it, covering every lease on demand, proposing nothing
  while the feature is disabled, retrying a failed proposal, and clearing a
  checkpoint a renewal overtook; `RenewLocal` refusing a checkpointed lease;
  `KeepAlive` and `Revoke` deleting the record; `ApplyCheckpoints` clamping,
  skipping, clearing and leaving memory unchanged on a write fault; `Restore`
  counting from the checkpoint; `RequestCheckpoint`; and a follower listing
  leases past its own deadline.
- **Service and state machines**: `pkg/service/lease_service_test.go` pins the
  keep-alive's outcomes — local renewal with no proposal, `NotPrimaryError` and
  `CheckpointedError` falling back to the replicated proposal, and an unknown
  lease typed as `LeaseNotFoundError` — and the checkpointer.
  `pkg/service/lease_expiry_options_test.go` pins the options built from config.
  Each lease-capable `Lookup` (`pkg/statemachine`, `pkg/engine`, `pkg/store`)
  has a test for the keep-alive and on-demand checkpoint queries, with and
  without a lessor, and for applying `OpLeaseCheckpoint`, refusing a malformed
  one as an apply fault. `TestKVLease_Checkpoint` proves on every KV state
  machine that a checkpoint is replicated state, that a keep-alive deletes it,
  and that one naming an unknown lease writes nothing; and
  `TestDurableLease_RestartCountsFromTheCheckpoint` restarts a durable node over
  its own data and proves it counts the lease from the checkpoint.
- **Multi-node failover** (`pkg/service/lease_failover_test.go`): a real
  three-replica Raft group (`sdk/go/testutil/raftharness`) with one `Lessor` and
  one production-wired sweeper per replica.
  `TestLeaseKeepAlive_CheckpointedLeaseRenewsOnEveryReplica` proves a renewal
  of a checkpointed lease goes through the log and clears the checkpoint on
  every replica. `TestLeaseFailover_NewLeaderFloorsTheDeadlineAtTTLPlusGrace`
  moves leadership and proves the new leader floors the deadline at TTL plus
  grace, keeps the lease past its own TTL, and revokes it on every replica only
  after the grace. `TestLeaseFailover_NewLeaderCountsFromTheOnDemandCheckpoint`
  checkpoints every lease on demand before the transfer and proves the new
  leader counts the lease from what it had left, not from a full TTL. These run
  in real time, because an election is real; the waits are
  polls bounded by `raftharness.Bound`, not fixed sleeps standing in for a
  condition, except the two intervals the assertions are about.
- **Latency tail** (`sdk/go`'s `TestRaft_LeaseKeepAliveLatencyTail`): 64
  renewals against a real single-replica group on real disk, asserting p90 stays
  under 3x the median. Run it under contention as
  [../../test/performance/lease-keepalive.md](../../test/performance/lease-keepalive.md)
  describes.
- **Integration, keep-alive and expiry**: the integration tests run real
  daemons on the wall clock, so they renew and wait in whole seconds against
  short TTLs and poll for the outcome with a generous deadline.
  `lease_cli_test.go`'s `TestLease_CLI_KeepAliveAndExpiry_PerProtocol` renews
  a 3-second lease through `qrdb lease keep-alive` for twice its TTL on every
  daemon transport (REST, gRPC, QUIC, MCP, Unix; an embedded node lives for
  one CLI invocation, shorter than any sweep), checks the bound key survives, then stops and checks the
  sweeper deletes the key and that `lease ttl` and `lease keep-alive` both
  report the lease not found. `etcd_test.go`'s
  `TestEtcd_LeaseKeepAliveAndExpiry` does the same through a stock etcd
  client, ending in `ErrLeaseNotFound` from the TTL-0 answer.
- **Integration, leader failover** (`lease_keepalive_test.go`): a three-node
  qrdbd cluster renews a lease on the leader six seconds into its ten-second
  TTL, so every follower's copy would lapse four seconds later, then the
  leader is killed. The test checks the new leader reports the lease at no
  less than its full TTL, that no survivor deletes the bound key before the
  deadline the keep-alive reported, that a keep-alive sent to a follower
  renews it, and that the new leader's sweeper revokes it once keep-alives
  stop.
- **Fault injection**: the leader seam allows a leadership change to be forced
  between grant and expiry.

### Reading a failure from the sub-TTL renewal test

`sdk/go`'s `TestRaft_LeaseRenewedOnSubTTLCadenceKeepsDoneOpen` can fail for two
different reasons and **says which**. Read the message before assuming a lease
defect.

| Message says | Meaning | What to do |
|---|---|---|
| `... within the 2s TTL, so the lease should still have been held` | The deadline had **not** passed and renewal failed anyway. A **real defect**. | Investigate the lease path. |
| `THE ENVIRONMENT COULD NOT VALIDATE the sub-TTL renewal property` | Every attempt exceeded the TTL before a renewal landed. The assertion **never ran**. | Re-run on a quiet host. See below — this is **not** automatically the host's fault. |

**The second message is not a clean bill of health.** Two causes produce it and
this test cannot tell them apart:

1. **host contention** — the machine could not schedule a 1.5 s cadence inside a
   2 s deadline, five times running; or
2. **a renewal-path regression** in which keepalive acknowledgements consistently
   miss the local deadline.

Both present as "every window overran". If a quiet host still cannot get a clean
window, suspect the second. This is the sharpest reason the path must **fail**
rather than skip: under a skip, cause 2 — a genuine regression — would fire the
skip on every run and CI would report success. **This test is the only end-to-end
coverage of the property**, so a non-failing silence there is the whole signal
lost.

**And skipping is forbidden regardless.** `CLAUDE.md` and `AGENTS.md` both say
*"NEVER use skip guards or -short flags"*, and a measured precondition is not an
exemption from a rule written as *never*.

**Which interval decides it.** Not the sleep. The lease deadline is dated from
the instant the **previous** confirmation was *sent* (`heldUntil = sent.Add(ttl)`
in `sdk/go/coordination.go`), so the quantity that decides expiry is
`previous renew's round trip + the sleep + this renew's round trip`. A
discriminator built on the sleep omits the previous round trip — which is exactly
what consumes the 500 ms of slack between a 1.5 s cadence and a 2 s TTL — and
will report a slow pair of raft proposals as a lease-renewal defect.

**Why the timing is not widened.** A 2 s TTL renewed at 1.5 s is the exact
cadence that produces the ~0.999 s remainder the whole-second `RemainingTTL`
truncation rounds to zero, which is the defect the test exists to catch. A longer
TTL would make it pass more often and stop testing anything. The durable fix is
the injectable clock seam, which this test is a named consumer of.

## Observability

- Lease grant, keep-alive, revoke and expiry are audited through
  [../audit/](../audit/).
- Cascade deletes appear as ordinary delete events on
  [../watch/](../watch/) streams, which is how a consumer learns a session ended.

## Integration testing

`make integration-test-leases` runs this feature's self-spawned integration tests: `lease_binding_test.go`, `lease_cli_test.go` and `lease_keepalive_test.go`. The etcd keep-alive case runs with the etcd suite, `make integration-test-etcd-compatibility`. The per-transport accounting, including every gap, is the `leases` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
