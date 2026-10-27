# Key-Value Store — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Exported surface

### SDK — `sdk/go`

```go
func (c *Client) KV() transport.KVClient
func (c *Client) MVCC() transport.MVCCClient
func (c *Client) KVStore() KVStore          // kvstore adapter for DAOs
func (c *Client) DatabaseScoped(name string) (*Client, error)
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error)
```

`sdk/go/transport`:

```go
type KVClient interface {
	Put(ctx context.Context, key string, value []byte, opts ...WriteOption) error
	Get(ctx context.Context, key string, opts ...ReadOption) ([]byte, error)
	Delete(ctx context.Context, key string, opts ...WriteOption) error
	List(ctx context.Context, prefix string, opts ...ReadOption) ([]string, error)
	Scan(ctx context.Context, prefix string, opts ...ReadOption) (map[string][]byte, error)
	Exists(ctx context.Context, key string, opts ...ReadOption) (bool, error)
}

type MVCCClient interface {
	PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...WriteOption) (*MVCCKeyValue, error)
	GetRevision(ctx context.Context, key string, opts ...ReadOption) (*MVCCKeyValue, error)
	GetAtRevision(ctx context.Context, key string, revision int64, opts ...ReadOption) (*MVCCKeyValue, error)
	Range(ctx context.Context, start, end []byte, rangeOpts RangeOptions, opts ...ReadOption) (*RangeResponse, error)
	Compact(ctx context.Context, revision int64) error
	CurrentRevision(ctx context.Context) (int64, error)
	RestoreToRevision(ctx context.Context, req *RestoreRevisionRequest) (*RestoreRevisionResult, error)
}
```

```go
// GuardedKVClient is a CAPABILITY sub-interface, detected by type assertion.
// It is deliberately NOT part of KVClient: a transport may only offer these
// once its wire contract carries them.
type GuardedKVClient interface {
	KVClient
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
}
```

Every transport carries the compile-time proof that it implements both:
`var _ transport.KVClient = (*Transport)(nil)`.

`GuardedKVClient` carries the same proof on every transport the SDK ships —
embedded, gRPC, unix, QUIC, REST and `mcp`. **The assertion is the contract**:
adding the line makes a consumer's type assertion start succeeding, so it may
only be added alongside methods that carry the verb's atomicity. A single call
per verb, no preceding read, no read-then-write fallback; the server handler
calls `service.KVServicer`'s atomic method directly.

The guarded verbs take **no `WriteOption`**. A conditional write must be
linearizable to mean anything, so it is always synchronous.

#### The `kvstore.KVStore` surface — `sdk/go`

```go
func (c *Client) KVStore() KVStore   // routes index ops to IndexClient

// Store capability, for consumers that must know before they depend on it.
// PutIfAbsent (take), CompareAndSwap (edit) and CompareAndDelete (release).
// Discovered by RUNTIME type assertion, so an implementation missing any one
// of them fails at run time rather than at build time — widening this
// interface means implementing it everywhere in the same change.
type AtomicKVStore = kvstore.AtomicKVStore
var  ErrKVIndexUnsupported       = kvstore.ErrIndexUnsupported
var  ErrKVAtomicWriteUnsupported = kvstore.ErrAtomicWriteUnsupported

// The store Client.KVStore() returns satisfies AtomicKVStore: it forwards
// PutIfAbsent/CompareAndSwap/CompareAndDelete to the transport's
// GuardedKVClient capability, which every production transport (REST, gRPC,
// QUIC, Unix, MCP, embedded) carries.
//
// CompareAndSwapWithCurrent composes CompareAndSwap with a follow-up Get on
// a failed compare, so a contended counter (a PIN-lockout attempt count)
// learns the value it should have compared against without a second,
// separately-written round trip. The read is NOT part of the same atomic
// step -- see the function's own doc for the window that leaves open.
var CompareAndSwapWithCurrent = kvstore.CompareAndSwapWithCurrent
// func(ctx context.Context, store AtomicKVStore, key string, expected, next []byte) (swapped bool, current []byte, err error)

// FencedCreator is kvstore.FencedCreator's alias, for an embedder holding a
// kvstore.KVStore it did not get from Client.KVStore() (a custom
// seal.StorageBackend composition, for one) and needs to assert for the
// same capability AtomicKVStore above is exported for. It has no
// wire-level implementation on any sdk/go/transport client, so the
// type alias does not make the capability newly reachable through
// Client.KVStore() -- only through a store the caller built or received
// some other way.
//
// kvstore.MultiGuardedBatchKVStore (the real atomic multi-key write
// primitive pkg/dao.UnitOfWork REQUIRES, with no fallback) is different:
// the store Client.KVStore() returns satisfies it too, by composing
// the transport's TransactionClient -- see
// ../transactions/engineering.md#kvstoremultiguardedbatchkvstore-over-clientkvstore.
type FencedCreator = kvstore.FencedCreator

// An in-process store with real index maintenance and real atomicity, for
// unit-testing DAO code without booting a node.
var NewMemoryKVStore = kvstore.NewMemoryKVStore
type MemoryKVStore = kvstore.MemoryKVStore

// Ask before caching anything keyed by a resolved store. KVStore is an
// interface, so its dynamic type need not be comparable, and using a
// non-comparable one as a map or sync.Map key panics on the first operation.
// A store that cannot be a key is not an error: skip the cache for it.
//
// A func, not a var alias: a safety predicate an importer could reassign is
// not one canonical judgement.
func UsableAsMapKey(store KVStore) bool
```

### Consistency levels — `pkg/consistency`

`consistency.Level` is an `iota` enum, in this order:

| Constant | `String()` | Mechanism | Typical latency |
|---|---|---|---|
| `Strong` | `strong` | full Raft consensus (SyncRead) | 10–50 ms |
| `Linearizable` | `linearizable` | Raft ReadIndex — **the default** | 1–5 ms |
| `LeaseRead` | `lease_read` | leader serves locally under a valid leadership lease | sub-ms |
| `Eventual` | `eventual` | local state machine, no coordination | <1 ms |
| `FollowerRead` | `follower_read` | follower confirms applied index via ReadIndex, then reads locally | between `Eventual` and `Strong` |

`LeaseRead` has no staleness window. The raft adapter
(`pkg/backend/raft/adapter.go`) maps `LeaseRead` and `Linearizable` to the same
`SyncRead` (ReadIndex) path; with `lease_read` on (the default,
`pkg/config/config.go`) the leader answers it from its lease without a
heartbeat round. go-quicraft runs the lease on the monotonic clock, anchored at
heartbeat send time, for `ElectionRTT - 2` ticks less 1 %
(`leaseDriftDivisor = 100`); when the lease is out the read falls back to the
heartbeat quorum round. Reads are linearizable while the clock rate drift
between replicas stays under 1 % and every replica runs the same `ElectionRTT`
and `RTTMillisecond`. A wall-clock step does not affect the lease. `Eventual`
reads local state with no bound on a partitioned node. The deployment
requirement is in
[../deployment/user.md](../deployment/user.md#keep-replica-clocks-disciplined),
the mechanism in [../05-consistency.md](../05-consistency.md#3-leaseread).

### Write durability — `pkg/writemode`

Re-exported from go-quicraft; qrdb does not define its own:

| Constant | Meaning |
|---|---|
| `writemode.Sync` | fsync before acknowledging |
| `writemode.WALDurable` | WAL-durable, deferred data fsync |
| `writemode.Batched` | group-committed by the batch aggregator |

### Internal key guard — `pkg/service`

```go
const InternalKeyPrefixSystem = "__sys:"
func InternalKeyPrefixes() []string
func IsInternalKey(key string) bool
func SkipInternalKeyFilter(prefix string) bool
```

Reserved prefixes: `__sys:`, `__tbl:`, `__idx:`, `__seq:`, `__tblgen:`,
`__reclaim:job/`, `__reclaim:index/`, `ts:`, `idx:metric:`, `idx:tag:`,
`retention:`, `__vec:`.

`InternalKeyPrefixes` returns a **copy** so no caller can widen the user
keyspace by mutating the backing array.

`SkipInternalKeyFilter` grants an enumeration escape only when the requested
prefix lies *inside* a reserved namespace whose owner reads its own records
back through the shared `KVServicer`. A prefix that merely *spans* one — `_`,
which covers `__sys:` and `__tbl:`, or `t`, which covers `ts:` — is an ordinary
user prefix and is filtered. That distinction is what stops a one-character
sweep becoming an enumeration of the internal keyspace.

## Invariants

- **Reads and writes are scoped to exactly one (tenant, database) pair**,
  resolved server side. The tenant comes from the authenticated identity; a
  client-supplied tenant field is never trusted.
- **`DatabaseScoped` returns a new client**, sharing the connection but carrying
  its own immutable scope. Scopes never leak between clients and no global
  mutable state is involved. A caller that names a database is never handed the default one: a transport
  that cannot confine its data plane to the database refuses with
  `transport.DatabaseScopeUnsupportedError`. The embedded transport scopes
  through the node's per-(tenant, database) service registry
  (`embedded.WithServiceRegistry`, wired by `sdk/go/node`), resolving each
  data-plane call for the context's tenant as the network transports' server
  does per request; without a registry it refuses.
- **Revision monotonicity**: the store's current revision only increases.
  `Compact(r)` discards revisions strictly older than `r`; reads below the
  compaction floor fail closed rather than returning an approximation.
- **Compaction is per database and never cross-tenant.** `Compact(r)`
  compacts the history of the database the request resolved to -- its
  `<tenant>/<database>/` namespace on a shared shard, its own shard under raft
  isolation -- and nothing else. `mvcc:compact` is `ScopeSelfStrict`: own
  tenant only, for every identity, the system tenant included. The raw
  (un-namespaced) service refuses with `service.CompactScopeRequiredError`, so
  no path compacts a store globally. See architecture.md "MVCC compaction is
  per database".
- **The compaction floor is per keyspace prefix.** `mvcc.Store` records one
  floor per compacted prefix (`[0x04][prefix]`). A point read at revision `r`
  is refused when `r` is at or below the floor of any prefix of its key; a
  range read, a watch replay and a change-log scan when it is at or below a
  floor whose keyspace the range overlaps.
- **Compaction keeps the value live at the floor (etcd's rule).** Compacting
  to `r` keeps, for each key, its newest entry at or below `r` -- its state
  at `r` -- and every entry above `r`, and deletes only the entries older
  than the kept one. So every read at a revision above the floor answers
  exactly as it did before: a key written at 1, 3 and 5 and compacted to 3
  still reads its revision-3 value at 4, and a `Range` at 4 still lists it,
  which a client listing at a revision just above the compaction point
  (`kube-apiserver`) relies on. A kept entry that is a tombstone goes too
  when the key has a newer entry, since it records only that the key did not
  exist.
- **Compaction validates its revision.** `r < 1` is
  `service.CompactRevisionInvalidError`; `r` above the store's current
  revision is `service.CompactRevisionFutureError`; `r` at or below the floor
  already covering the database is `service.RevisionCompactedError` and
  changes nothing. The state machine re-checks all three when it applies the
  command, so two concurrent compactions of one database cannot regress its
  floor.
- **Nothing compacts implicitly.** A database is compacted only when its
  owning tenant asks: an explicit `Compact`, or the scheduled-compaction policy
  that tenant configured on the database (below). There is no node-wide
  retention setting.
- **Restore is a logical undo, never a Raft rewind.** Every change is proposed
  as an ordinary forward write. Committed history is never discarded.
- **Restore is not atomic across keys.** Each key is an independent Raft
  proposal; partial application is possible and retry is safe.
- **Restore blast radius is capped at `service.MaxRestoreKeys` (10000).** A
  scope matching more is refused whole, never silently truncated.
- **Restore scope is exactly one of `--prefix` or `--keys`.** Neither (unscoped)
  and both (ambiguous) are refused.
- **`kv restore` and as-of reads require `raft.sm_type: mvcc`.** The default
  `pebble` state machine keeps no revision history and refuses them.
- **An absent key and a key holding an empty value are different answers on
  every `raft.sm_type`.** `Get` returns not-found for the first and a
  zero-length value for the second; `Exists` is false and true respectively.
  This is a promise about the read plane, not about any one state machine —
  see [Point reads and absence](#point-reads-and-absence).
- **A `Get` issued after `Delete` has returned reports not-found.** On every
  storage engine (memory, file, pebble, objstore) and every transport, the
  answer is the typed `*transport.KeyNotFoundError` and `Exists` is false,
  never a read failure: `Delete` returns only once applied, `Get` is a
  linearizable read, and every engine maps its backend's own not-found to an
  absent key. `TestDeleteThenGetIsTypedNotFound` in `sdk/go/conformance`
  holds it over 50 rounds per cell, for short keys and for keys long enough
  that objstore stores them under a hashed name (#1163).
- **A refusal never costs the shard its reads.** Raft treats any non-nil error
  out of `Update` as a fatal apply failure: `lastApplied` stops advancing, and
  every subsequent linearizable read on that shard — on any engine sharing it,
  on keys written and read successfully microseconds earlier — waits out the
  caller's deadline and reports a consensus timeout. So an outcome that is a
  deterministic consequence of valid committed input travels back in
  `Result.Data`, never as a Go error. See
  [Refusal and fault at the apply seam](#refusal-and-fault-at-the-apply-seam).
- **`GetHash` never hashes a state between two halves of the same batch or
  recovery.** `KVStateMachine` and `DiskAdapter`
  each hold one `sync.RWMutex` ("batch") shared by every `Update` and
  `RecoverSnapshot`/`RecoverFromSnapshot` call and exclusive to `GetHash`.
  `Update` and recovery hold it for their WHOLE span — one call for a
  multi-entry batch, not once per entry — so a replica comparison never sees
  a state no replica settles in. `DiskAdapter.Update` applies one entry per
  call for its own index-stamping reasons; it takes the lock itself, once,
  around that whole per-entry loop, and calls `KVStateMachine`'s unlocked
  `applyEntries` rather than its locking `Update` for each entry, so the
  lock is not released and reacquired between entries of what its own
  caller hands it as one batch. The trade-off this accepts: `GetHash` is a
  diagnostic, replica-divergence-detection path, not a hot path, so
  blocking every concurrent apply for however long one hash takes is an
  accepted cost here, in exchange for never answering from a half-applied
  or half-recovered state — a stall this package is willing to risk on this
  one diagnostic path specifically because it is not the kind of apply-path
  stall this project otherwise designs against. `Lookup` takes no part in
  this lock at all, so reads are unaffected either way. The one exception is
  the replica state digest (`statedigest.Query`, see
  [../clustering/engineering.md](../clustering/engineering.md#replica-state-hashes)):
  it is answered through `Lookup` but takes the batch lock exclusively, as
  `GetHash` does, for the same reason and at the same cost.
- **Every KV state machine records its applied index.** The reserved
  applied-index record (`diskMetaKeyLastApplied`) is stamped at the end of
  every applied batch: by `DiskAdapter` directly on its storage, and by `KVStateMachine.Update` through the backend for every
  other KV state machine. It is replicated state like any record, so it
  travels in snapshots and is restored with them. A client can neither read
  nor write it (`isStateMachineKey`), and the state digest reports it as the
  applied index rather than hashing it.

### Refusal and fault at the apply seam

Two things can go wrong while a committed entry is being applied, and they need
opposite answers.

A **refusal** is "this state machine was not built to do that": an opcode it has
no handler for, a subsystem not wired into this instance, a subsystem compiled
out of this binary. It says nothing about the health of the storage underneath,
so it must be answered — the proposer hears it, the apply loop keeps advancing,
the shard keeps serving.

A **fault** is divergence: a committed entry that will not decode, a storage
write that failed. A replica that cannot apply what its peers applied must stop
rather than serve state its peers do not have.

**The error declares which it is.** An error implementing
`protocol.CapabilityError` — an `error` with a `Capability() string` method
naming the absent verb or subsystem — is a refusal, and
`statemachine.ResolveApplyError` encodes it into `Result.Data` with the
capability marker, which the service layer turns into a typed
`CapabilityUnsupportedError` for the caller. **Everything else is a fault and
halts Apply.** Silence means fatal; only a declaration means benign.

That direction is deliberate and is the safe one. Inverting it — halting only on
errors explicitly marked fatal — would make a forgotten marker into a swallowed
storage fault and a diverged replica serving wrong data. Under the rule as
written, a forgotten declaration costs availability on one shard and is loud;
the alternative costs correctness and is silent.

Two consequences:

- **The classification is a property of the error, not a list kept elsewhere.**
  A new compiled-out subsystem declares its own refusal type beside the code
  that returns it. Nothing central is edited, so nothing central can be
  forgotten. `test/buildgates` fails the build if a stub declares a
  not-compiled refusal as a bare `errors.New` sentinel.
- **Every apply seam classifies, and that is enforced rather than reviewed.**
  `test/buildgates` finds every method in `pkg/` that takes `[]sm.Entry` and
  requires it to route handler errors through `ResolveApplyError` or to be
  listed, with a reason, as a delegator that forwards to a seam which does. A
  new state machine is red until it does one or the other: the gate is
  default-deny, because the absence of a call is not something a reviewer
  reading one diff can see.

**A refused batch or transaction stages nothing.** Opcodes are validated before
any operation in the set is applied. Applying until the unrecognised one is
reached would commit half the batch while telling the caller it was refused.

### Point reads and absence

A point read (`Get`, `GetStale`, `GetWithConsistency`, `ExistsWithConsistency`)
passes the bare key as the Raft query and gets back an `interface{}`. Absence is
decided on that value being nil, not on its length: every storage engine behind
every state machine returns `(nil, nil)` for a key it does not hold and a
defensively copied `make([]byte, len(v))` for one it does, which is non-nil even
at length zero. Length is therefore never a proxy for absence, and a key written
with an empty value keeps reading back as present.

The nil test has two arms, and the second is load-bearing rather than
defensive. go-quicraft's `rsm.StateMachine.Lookup` has a fast path for `[]byte`
queries that forwards to `sm.NALookup` and returns its result directly, which
boxes a `[]byte(nil)` into a NON-NIL `interface{}`. The two state machines that
implement `sm.NALookup` for their zero-allocation read path are `DiskAdapter`
and `HighPerfAdapter`, so on `raft.sm_type: disk` and `highperf` a missing key
reaches the service layer as a non-nil interface holding a nil slice; tested
on the interface alone it would be served as a successful read of an empty
value, and `Exists` would answer true for every key. The state machines without
`sm.NALookup` take the generic `Lookup` path, which returns an untyped nil.

`service.keyIsAbsent` (`pkg/service/service.go`) therefore treats a nil `[]byte`
as not-found as well as a nil interface. That compensates for the upstream
defect at this repo's four point-read call sites only; it does not fix any other
consumer of `Host.SyncRead`. The defect is tracked against the consensus
library, and when a release carrying its fix is pinned here the second arm
becomes redundant and can be removed. The code comment on `keyIsAbsent` carries
the tracker reference.

### Absence across the `kvstore.KVStore` adapter seam

The user and role stores do not talk to the KV service directly. They talk to
`kvstore.KVStore`, and something adapts one to the other — `kvStoreAdapter` in
`pkg/server/lifecycle` for the server, `serverKVAdapter` in
`sdk/go/standalone` for the in-process server.

**The two interfaces disagree about how absence is spelled, which is what makes
the adapter a seam rather than a pass-through.** The service reports a missing
key as a `*service.KeyNotFoundError`; `kvstore.KVStore` reports it as a
*value* — `(nil, nil)` from `Get`, `(false, nil)` from `Exists`. An adapter that
lets the error through hands a caller asking "is this key here?" a failure for
the commonest answer.

**Absence is recognised by type, never by message.** `errors.As` against
`*service.KeyNotFoundError`, which also unwraps the `*service.ReadError` a
linearizable read wraps it in. The alternative — matching `"not found"` in the
rendered string — works, right up until someone rewords an error: the wording
is then load-bearing without saying so, and the failure is that every absent key
becomes a hard error at a caller that was testing `if data == nil`. Both
adapters use the type, and a test drives each with an absence whose message
deliberately contains no recognisable wording, so the message cannot become
load-bearing without that test going red.

**An error that is not absence still propagates.** The opposite mistake — a too
eager adapter reporting a failed read as "the key is not there" — is the same
lie in the other direction, and is asserted separately.

### `kvstore.KVStore` comparability is a contract, not an enforced property

Routing layers that resolve a tenant-scoped store cache one per-tenant service
per *resolved* `kvstore.KVStore`, keyed by the store value itself, because
store identity is the only thing that distinguishes one tenant's backing from
another's. That caching pattern only works if the store's dynamic type is
comparable, and `KVStore` is an interface: nothing in Go requires every type
that satisfies it to also be comparable. A named slice or map type with value
receivers implements `KVStore` perfectly well and panics with `"hash of
unhashable type"` the first time it is used as a map key — before any tenant
work runs.

**This is documented as a contract on `KVStore` and deliberately not
enforced.** Go cannot add a compile-time comparability constraint to an
existing interface without breaking every implementation, and doing so would
also foreclose an implementation that has a legitimate reason to be
non-comparable. Instead, `kvstore.UsableAsMapKey(s KVStore) bool` is the
predicate every caller that caches per-store state must consult before using a
`KVStore` value as a cache key; a `false` result means "skip the cache," not
"refuse the store."

### The single-key guarded verbs — available on every state machine

`PutIfAbsent`, `CompareAndSwap` and `CompareAndDelete` are one primitive:
acquire a claim, edit it under the claim, release it under the claim. They are
useful only together. A guarded create paired with an unguarded delete is half
a primitive — the delete can un-decide what the create decided — and a create
and a release with no guarded edit between them leaves the transition in the
middle decided nowhere.

- **They are not scoped to `mvcc`/`pebble`.** They are native opcodes
  (`kv.OpPutIfAbsent`, `protocol.OpCompareAndDelete` 66,
  `protocol.OpCompareAndSwap` 67) dispatched against the state machine's own
  store, so they need no MVCC store behind them.

  Availability by `raft.sm_type`, which is the row to read before choosing one:

  | `sm_type` | `PutIfAbsent` | `CompareAndDelete` / `CompareAndSwap` | Transactions, guarded batches, batches | Revision compares |
  |---|---|---|---|---|
  | `mvcc` | yes | yes | yes | yes |
  | `pebble` (default) | yes | yes | yes | refused |
  | `memory`, `standard`, `concurrent`, `highperf`, `fast`, `disk`, `regular` | yes | yes | yes | refused |

  There is no row that declines a guarded verb.
- **Every state machine runs transactions against its OWN keyspace.** The
  `pkg/statemachine` state machines have no MVCC store; `OpTransaction` and
  `OpBatch` are decided against, and committed to, the same keyspace `Put`,
  `Get` and the guarded verbs use (`pkg/statemachine/native_txn.go`), with the
  default pebble state machine's compare semantics: value compares (an absent
  key compares as the empty value), version compares that degrade to 0 when
  absent and 1 when present, and create/mod-revision compares refused with the
  same application error, because answering "revision 0" for every key would
  let two read-then-CAS writers both win. A separate MVCC keyspace was the
  alternative and was rejected: a compare there cannot see a plain `Put`, so a
  guarded batch fenced on a claim would fence on nothing. A lease-bearing
  write binds its key to the lease in the same commit, except on the legacy
  `regular` state machine, which keeps no lease table and refuses the whole
  set outright; on every other state machine an unknown lease ID refuses the
  set with `lease-not-found`, the same answer a plain lease-bound `Put`
  naming it would give.
- **The commit is all or nothing.** Transactions and batches commit through
  `Backend.ApplyAtomic`, a required method. `disk` commits the set as one
  `storage.Batch` carrying the entry's applied index; every other backend
  applies it under every touched shard's lock at once, and `List`/`Scan` on the
  sharded ones hold every shard's read lock for their whole sweep, so no
  concurrent read sees part of a set. `fast` then writes through to storage
  asynchronously, in apply order, as `Put` does: its durability is the Raft
  log's, because every replica starts from an empty keyspace (reset in one
  storage batch) and is rebuilt from the latest snapshot or a full replay, so
  a synchronous batch would protect nothing a restart reads. Neither `disk`
  nor `fast` can replay an entry over its own result, where a compare could
  take the other branch and diverge from every other replica.
- **Every write on `disk` carries its entry's applied index**, single-key
  verbs included — `Put`, `Delete` and the guarded pair commit through the same
  `ApplyAtomic`. A guarded single-key verb does not converge on replay either:
  from an absent key, a `CompareAndSwap` expecting `a` declines and a
  `PutIfAbsent` of `a` creates; replayed over that result, the swap succeeds and
  the create declines. The disk state machine's applied-index key is reserved,
  and a client write naming it is refused.
- **An empty key is refused, never stored.** The transaction and batch services
  refuse it before proposing; a state machine that receives one anyway refuses
  it as an application result (`statemachine.InvalidKeyError`, declared a
  `protocol.RefusalError`) and keeps applying, instead of passing it to storage
  that refuses it with an error that would halt Apply on every replica.
- **The comparison runs inside apply, never on the proposer.** Every replica
  evaluates the same entry against the state it has applied up to that entry's
  log position, so every replica reaches the same decision. Reading on the
  proposer and then proposing a plain `Delete` or `Put` is two operations with
  a window between them, and that window is the defect: two releasers both read
  holder A, the first deletes, a third party legitimately claims the now-absent
  key, and the second delete removes THEIR value.
- **A declined guard is a successful application, not an error.** The state
  machine reports `ResultNotStored` / `ResultNotDeleted` / `ResultNotSwapped`
  through `Result.Value`. Returning an error would stop Raft advancing
  `lastApplied` and stall every subsequent linearizable read on the shard.
- **An absent key matches no expectation, including an empty one.** Neither
  `CompareAndSwap` nor `CompareAndDelete` creates: a swap moves a record
  between two states and a record that does not exist has no first state.
  Creation is `PutIfAbsent`'s job. A key holding an empty value is a different
  thing from an absent key and the guard distinguishes them.
- **A storage read fault is propagated, never read as a mismatch.** A fault is
  local to one replica, so folding it into "the value differs" would have that
  replica decline an entry its peers applied — divergence from an identical
  log.
- **They are durable before they acknowledge, and ordered.** "Your write is
  queued" and "your claim succeeded" are different promises, and only the
  second is worth a disk wait, so a guarded verb persists synchronously on the
  apply path and RETURNS a storage failure rather than logging it. A write that
  does not reach storage is withdrawn from the cache, restoring exactly what
  was there — an absence, a delete tombstone, or the previous value, which are
  three distinct states and not interchangeable. No earlier write to the same
  key can land after it.

- **`KVStateMachine` gets those promises from the `Backend` interface, which
  carries a durable write pair.** `Backend` has four write methods, in two
  contracts: `Put` and `Delete` are asynchronous — "your write is queued", a
  failure may be logged — while `PutDurable` and `DeleteDurable` return only
  once the write has taken effect, return the failure instead of logging it,
  and land after every earlier write to the same key. The three guarded verbs
  use the durable pair; `Put`, `Delete` and the batch verbs use the
  asynchronous one.

  **Every implementation must answer, and that is the design.** The methods are
  on `Backend` itself rather than in an optional interface a backend may
  implement. An optional interface is the idiomatic Go shape and it is wrong
  here: a backend that skipped it would fall back to the asynchronous path
  SILENTLY, which is the degradation the guarded plane exists to refuse. An
  opt-in guarantee is not a guarantee. The cost this accepts is that `Backend`
  is SDK-exported, so every implementation outside this repository must change
  too.

  `DiskBackend` and the in-memory backends (`standard`, `concurrent`,
  `highperf`) satisfy the pair by delegating, because their writes already land
  before the call returns. `FastBackend` implements it as a synchronous ordered
  write on the calling goroutine that returns its storage error and
  **withdraws** the cache entry when the write does not land — the same
  discipline `FastStateMachine` applies through `persistClaimLocked`. Its ordinary `Put` and `Delete` are untouched and stay
  asynchronous: that contract is what the backend exists to provide, and making
  them synchronous was rejected for that reason.

  `sm_type: fast` is a `KVStateMachine` over `FastBackend`, which reaches
  storage only through this seam: without the durable pair, an acknowledged
  `PutIfAbsent` that never landed would leave the key absent after a restart
  and let a second caller win the same claim.

  `FastBackend` orders its background writes: it draws a `persistOrder` ticket
  under the same lock acquisition as its cache write, so an older pending write
  never lands after a newer one, guarded or not.

  The durability battery in `pkg/statemachine/guarded_persist_test.go` runs over
  `FastStateMachine` and over `KVStateMachine` + `FastBackend`, unchanged
  between them: the promises are the same, not a weaker set for a weaker
  backend.

- **They cannot appear inside a batch.** The batch encoding carries no expected
  value, so a batched operation cannot express a guard. Multi-key atomicity is
  [../transactions/](../transactions/), which every `sm_type` serves.

- **A database with its own store answers them too, without Raft.**
  `BackendKVService` — the servicer a database with a dedicated store or a
  per-database barrier is served by — asserts its backend to
  `database.AtomicBackend` and delegates. `database.StandaloneBackend`, what
  every `memory`, `file` and `pebble` database is built on, satisfies it by
  making the read and the write one critical section; `database.BarrierBackend`
  satisfies it whenever its inner backend does, which is the composition the
  dedicated-store bundle actually builds.

  **The mutex is shared with the unguarded writes, and that is the whole
  correctness argument.** `StandaloneBackend.Put`, `Delete` and `Batch` enter
  the same critical section, so no write on that backend can land between a
  guarded operation's read and its write. Were they outside it, a plain `Put`
  could overwrite what a `PutIfAbsent` had just decided and the verb would be
  advisory — which is worse than the refusal below, because a refusal is
  honest and an advisory guarantee is not. `Batch` keeps its own all-or-nothing
  property, and it cannot commit inside a guarded operation's window.

  **One write mutex, not per-key lock striping.** Striping is no faster on
  `pebble` and slower on `memory`, which serializes internally and gains
  nothing from the extra lock. It is the construction `seal.StorageAdapter` and
  `database.BackendStorageAdapter` use. A lock-free form is not
  available at all: `storage.Storage` exposes no compare-and-set, so a
  check-then-write over an arbitrary engine has nothing to CAS on.

  What the shipped path costs is reproducible — `make bench-database`, run with
  `-cpu` at or above the host's core count, since a write mutex is invisible
  single-threaded. Parallel `Put` through the backend against an unlocked `Set`
  on the same engine measures within a few per cent on `memory` and under ten
  per cent on `pebble`.

  **The remote proxy refuses**, with
  `kvstore.ErrAtomicWriteUnsupported`, because its wire protocol carries no
  guarded operation. That refusal is why the capability is discovered by type
  assertion instead of living on `database.Backend`: widening the interface
  would force the proxy to ship a method that either lies or always fails, and
  a caller cannot tell those apart from a real refusal.

### Wire-field bounds — the owning contract for every plane

This section owns the length-field contract for the whole extended wire
protocol (`pkg/protocol`). The transaction, time-series and vector engineering
contracts point here rather than restating it, because it is one rule and it
must not drift into four.

- **Every field that IS length-framed is bounded by the field that frames it.**
  Two-byte fields — MVCC and base-KV **keys**, index and metric names, tag keys
  and values, tag and point counts, vector namespaces and ids — hold at most
  65,535 bytes. Four-byte fields — transaction and batch elements, vector
  dimensions and metadata — hold at most 4,294,967,295.
- **An MVCC Put VALUE is not length-framed and is not bounded by 65,535.**
  `EncodeMVCCPut` frames only the key; the value is the remainder of the
  payload, so it needs no length field and has none. Its practical limit is the
  Raft entry / proposal size, not a protocol field. Do not infer a 64 KiB value
  cap from the key bound — that would reject valid writes.
- **A longer value is refused, never truncated.** Narrowing it is not a
  truncation of that field but a corruption of the NEXT one: a 65,537-byte key
  written into a two-byte field stores a length of 1, so the decoder reads a
  one-byte key and folds the remaining 65,536 bytes into whatever follows. On a
  write that stores under the wrong key; on a delete it removes a different
  record than the caller named — from input a caller can craft, inside a
  replicated state machine.
- **Every encoder returns `([]byte, error)`.**
- **Validation completes before allocation.** An encoder validates the whole
  request, including every nested field of a batch, before sizing its buffer,
  so an unencodable request costs no allocation proportional to itself.
- **Refusals are typed.** `*protocol.FieldTooLongError` names the offending
  field; `*protocol.EncodedSizeOverflowError` reports a total an `int` cannot
  describe.
- **A refusal is a CLIENT error.** No proposal is made, so the services return
  `*service.EncodeRequestError`, which maps to gRPC `InvalidArgument` and HTTP
  **400** — deliberately not `service.ProposeError`, which maps to
  `Internal`/500 and would report a healthy cluster as broken while inviting a
  retry of an input that can never succeed.

Source: `pkg/protocol/lengths.go`, `pkg/service/errors.go`. Portfolio-level
summary: [../01-architecture.md](../01-architecture.md).

## Error semantics

Errors are typed values in `pkg/errors` and the per-transport error packages
(`pkg/api/errors*.go`); `fmt.Errorf` is not used. Every error crosses the SDK
boundary as a typed value and is mapped per transport by
[../api-server/engineering.md](../api-server/engineering.md).

| Condition | Behavior | Fails closed? |
|---|---|---|
| Key absent | not-found, distinct from an empty value | n/a |
| Requested revision above current | invalid-argument | yes |
| Requested revision below the compaction floor | error, never an approximate value | **yes** |
| `Compact` through the raw, un-namespaced service | `service.CompactScopeRequiredError`; nothing compacted | **yes** |
| `MVCCKVService.CompactPrefix` with a prefix that is not exactly one database's keyspace (a `<tenant>/<database>/` namespace, or `""` for a dedicated raft group) | `service.CompactPrefixInvalidError` (`INVALID_ARGUMENT`); nothing proposed | **yes** |
| `Compact` to a revision below 1 | `service.CompactRevisionInvalidError` | **yes** |
| `Compact` to a revision above the current one | `service.CompactRevisionFutureError` | **yes** |
| `Compact` to a revision the database is already compacted to | `service.RevisionCompactedError`; the floor is unchanged | **yes** |
| `Compact` before every replica of the group supports per-database compaction | `featuregate.NotActiveError` naming `prefix-compaction` (503 / `UNAVAILABLE`); nothing proposed | **yes** |
| As-of read against a non-MVCC state machine | `service.MVCCBackendRequiredError` (501 / `UNIMPLEMENTED`) | **yes** |
| Restore naming a reserved prefix or key | refused, HTTP 403 equivalent, **no override** | **yes** |
| Restore with no scope or both scopes | refused | **yes** |
| Restore matching more than `MaxRestoreKeys` | refused whole | **yes** |
| Restore CAS conflict without `--force` | key left untouched, reported as `CONFLICT` | **yes** |
| Barrier sealed | request refused; no plaintext and no ciphertext served | **yes** |
| Node is not the leader and consistency requires it | routed to the leader, or refused when no resolver is configured | yes |
| Node closed its store under an in-flight operation | typed error matching `sdk.ErrStorageClosed`; never a panic | **yes** |
| Verb the state machine does not implement, or subsystem not compiled in | `CapabilityUnsupportedError`; the shard keeps serving reads and writes | **yes** |
| Committed entry that will not decode, or a storage write fault | Apply halts on that replica rather than diverging | **yes** |

The restore refusal on `__sys:` has no override by design: restoring
`__sys:roles/…` or `__sys:users/…` to an earlier revision would undo a
permission revocation.

### A KV operation that meets a closed store

A daemon that closes its storage while a goroutine is still writing is an
ordinary shutdown race, not a defect, and the KV surface reports it as an
error rather than a crash:

```go
if err := kv.Put(ctx, key, value); err != nil {
	if sdk.IsStorageClosed(err) {          // or errors.Is(err, sdk.ErrStorageClosed)
		return nil                     // the node is going down; conclude cleanly
	}
	return err
}
```

- **The sentinel is `sdk.ErrStorageClosed`.** Every producer unwraps to it —
  the storage engine's `StorageClosedError`, the local backend's closed
  refusal, and each of them wrapped in an operation, batch or snapshot error.
  One `errors.Is` covers all of them; `sdk.IsStorageClosed` is the same test
  spelled as a predicate. **Never match the error's text.**
- **`embedded` preserves the value.** The service and transport wrappers both
  unwrap, so an in-process caller matches the sentinel and, if it prefers,
  the `*sdk.StorageClosedError` type.
- **The network transports carry the CLASSIFICATION, not the value** — gRPC
  `Unavailable`, HTTP `503`. A status code is all that crosses a wire, so a
  remote caller reads the code. The classification is per-condition and not
  per-verb: a `GET` that meets a dying node and a `PUT` that meets one are the
  same fact and get the same status. See
  [../storage-engines/engineering.md](../storage-engines/engineering.md).
- **A closed store is distinct from a closed client.** `ClientClosedError`
  means the caller closed its own handle, which is its own bug to fix;
  `sdk.IsStorageClosed` deliberately does not match it.

## Configuration

| Key | Values | Default | Effect |
|---|---|---|---|
| `raft.sm_type` | `pebble`, `memory`, `mvcc`, `standard`, `concurrent`, `fast`, `disk`, `highperf`, `regular` | `pebble` | State-machine wrapper. Every value supports the single-key guarded verbs, batches and transactions. Only `mvcc` keeps revisions, so only `mvcc` supports as-of reads, watch replay, selective restore and create/mod-revision compares. |
| `storage.backend` | `pebble`, `file`, `memory`, `objstore` | `pebble` | Underlying storage engine. |

```yaml
raft:
  sm_type: mvcc
```

There is no node-wide compaction setting. A database's scheduled compaction is
a per-database policy, owned by the database's tenant -- see "Scheduled
compaction" below and [../databases/](../databases/).

### Node-keyspace compaction

The node keyspace is every key outside all `<tenant>/<db>/` namespaces: the
node's own internal records and the system tenant's raw keyspace. It belongs
to no tenant database, so no tenant's policy reaches it; the platform compacts
it, and only when it has set a policy. Off by default.

- **Set by a platform-only verb.** `SetNodeCompaction` / `NodeCompaction` on
  the admin surface of every transport (`PUT`/`GET /admin/node-compaction`,
  gRPC and the Unix socket `SetNodeCompaction`/`GetNodeCompaction`, QUIC
  `admin.SetNodeCompaction`/`admin.GetNodeCompaction`, MCP
  `qrdb_update_node_compaction`/`qrdb_node_compaction`, the embedded
  transport; the CLI's `qrdb admin update-node-compaction` and
  `qrdb admin node-compaction`) -- see [api.md](api.md). Both need
  `admin:node_compaction`, which is `ScopeCross`: only an identity of the
  system tenant can exercise it. The built-in Admin role carries it, so the
  system tenant's Admin can; a tenant's Admin, or any tenant role granted it,
  is refused on every transport, and the admin service itself refuses a
  caller bound to any other tenant (`tenant.CrossTenantDeniedError`), so an
  in-process caller scoped to a tenant is refused too.
- **A replicated record, not a node's configuration.** The policy is the
  `compaction.NodeScope` record of the shared store's compaction table
  ("Scheduled compaction" below), written through `OpCompactionState` like a
  database's policy: every replica holds it, a snapshot or backup carries it,
  and the shared store's leader runs it, with no node-local copy anywhere.
- **It never touches a tenant prefix, by construction.** A key is in a
  database namespace when it has the shape `tenant.ComposeNamespace` produces
  -- a non-empty segment, `/`, a non-empty segment, `/`
  (`tenantid.NamespaceOf`) -- whatever the segments are, whether or not the
  tenant exists. The compaction (`OpMVCCCompactNode`,
  `mvcc.Store.CompactNodeKeyspaceInBatch`) skips every such key's data
  entries and change log, so every tenant database, the system tenant's own
  included, is outside its reach. A node key that merely has that shape (an
  internal record such as `__sys:cluster/features/nodes/1`) is skipped too:
  the rule errs toward keeping history.
- **Its floor covers the node keyspace only.** It is stored apart from the
  per-prefix floors (`[0x06]`); a read of a namespaced key never consults it,
  and a range consults it unless the whole range lies in one database
  namespace.
- **It runs on the same scheduler** as the databases' policies, through the
  raw shard service (`service.NodeKeyspaceCompactor`), behind the cluster
  feature `node-compaction`, on the shared store's leader only (a standalone
  node leads its only store). A former leader's compaction to a revision the
  new leader already compacted to is the already-compacted refusal.
- **It costs a scan of the shard's whole data keyspace, in the apply loop.**
  Node keys are not one contiguous range -- a data key sorts by key length
  first, and node keys share lengths with every database's -- so the
  compaction visits every data entry on the shard, every tenant's included,
  to find the ones outside all namespaces. It runs as a Raft command, so
  every replica does that scan inside its apply loop, and no later entry on
  the shard applies until it finishes. Size the interval and the retention so
  it runs rarely on a large shared shard; a per-database compaction has no
  such cost (see "Compaction cost" below).

### Compaction cost

A per-database compaction costs the entries under the database's own prefix,
however much other databases on the shard hold. The data scan seeks to the
prefix once per key length present, and the change log is collected in the
same pass: every change is a data entry and a change-log entry at the same
`(key, revision)`, so each data entry in `(previous floor, revision]` has its
change-log record deleted as it is visited. The revision-ordered change log,
which interleaves every database's changes, is never walked.
`TestCompactPrefix_WorkIsBoundedByTheCompactedDatabase` pins this.

### Scheduled compaction

A database may carry a compaction policy. **It is off unless the owning tenant
configured one**; a database with no policy is never compacted except by an
explicit `Compact` from that tenant.

| Field (YAML / JSON) | Values | Meaning |
|---|---|---|
| `mode` / `mode` | `off` (or absent), `revision`, `period` | Off, retention by revision count, or retention by age. |
| `retain_revisions` / `retainRevisions` | integer >= 1 | `revision` mode: keep the last N revisions of the store serving the database. |
| `retain_period` / `retainPeriod` | Go duration >= `1s` | `period` mode: keep the history newer than this age. |

A field that does not belong to the mode is refused
(`compaction.InvalidPolicyError`) rather than ignored. The policy is
set with `DatabaseService.UpdateCompactionForTenant` (gRPC
`DatabaseUpdateCompaction`; `mvcc:compact`, owning tenant only); see
[../databases/](../databases/). There is no configuration-file form.

**The policy and its samples are replicated state in the database's own
store** -- never the node's catalog (`meta.json`), never a file in a node's
data directory. Each scope's state is one reserved record in the raw keyspace
of the store that holds the data it governs, beside the durable lease table
(`pkg/compaction`):

```
"\x00qrdb.compaction/" + scope  ->  compaction.State (JSON: policy, binding, samples)
```

The scope is the database's `<tenant>/<database>/` namespace on a store many
databases share (the shared shard; a volume tenant's own host), `""` on a raft
group that is one database's own, and `compaction.NodeScope` for the
platform's node keyspace ("Node-keyspace compaction" above). Only a raft
group's own services take `""` -- as a scope or as a compaction prefix
(`service.WithDedicatedKeyspace`, set for dedicated raft groups alone); on a
shared store it would be every database at once, and is refused
(`service.CompactPrefixInvalidError`) before anything is proposed. The `0x00` prefix
is the state machines' reserved-record prefix: the record is never a user key,
consumes no revision, fires no watch and is never itself compacted.

- **Written only through the log.** Every change -- setting or clearing a
  policy, recording a sample -- is `protocol.OpCompactionState` (16), proposed
  to the database's own group and applied in the apply batch, so every replica
  holds the same record. It is proposed only once the cluster feature
  `prefix-compaction` is activated (`featuregate.NotActiveError` before),
  the gate that carries `OpMVCCCompactPrefix` too. A state machine that does
  not apply it -- the pebble KV state machine, the KV engine, the state
  machines of `pkg/statemachine` -- refuses it as an unknown opcode. Such
  a store therefore holds no record, and a read of its table answers empty:
  dropping a database on it has nothing to clear, and the scheduler nothing
  to run.
- **Carried by snapshots and backups, covered by the replica hash.** The
  compaction table is exempt from the node-local filter (`pkg/store`
  `isNodeLocalKey`), exactly as the lease table is.
- **Bound to the database's incarnation.** Setting a policy writes a
  `compaction.Binding`: the policy, the owning tenant, the database and its
  incarnation. The write goes through the database's own view, so it carries
  the database's drop fence. A drop's keyspace reclamation clears the record
  the dropped incarnation set -- never one bound to another incarnation, which
  a database created again under the name wrote and a re-issued drop of the
  old one must leave alone -- and a policy bound to another incarnation
  replaces the record outright, samples included.
- **Read back** through `database info` (`DatabaseService.GetForTenant`,
  gRPC `DatabaseGet`; `DatabaseInfo.compaction`), from the same record. A sealed database's
  policy is not reported while it is sealed -- its views are refused by
  design -- and `list` does not report policies. A store that cannot be
  read -- a degraded raft database -- does not fail the call: the rest of
  the information is reported, `compaction` is absent and
  `compaction_unavailable` is true, meaning
  unknown rather than off; the service logs the cause
  (`service.CompactionPolicyReadError`). An update that committed is
  reported as done the same way. The read is bounded
  (`DatabaseService.boundedInfoRead`, 5 s, or the caller's own deadline if
  shorter), as is the placement refresh that fills in `replicas` and
  `shardId`: both are linearizable, and on a raft database just created the
  policy read reaches a shard still electing its leader, where a ReadIndex
  waits for as long as its ctx lives. A read that outlives the bound is
  reported the way an unreadable store is.

The compaction scheduler (`pkg/server`, every
`server.DefaultCompactionInterval`, one minute) runs on every node and holds
no state of its own. Each pass visits the stores this node may act on, with
the collection sweeper's gating: the shared store and each raft group **only
where this node leads the group** (`service.ShardLeader`), a volume tenant's
own host where it is started; a standalone node leads its only store. It reads
each store's compaction table and, for each enabled policy:

- `revision`: target = current revision - `retain_revisions`.
- `period`: the leader records the store's current revision with the time,
  as a sample, into the replicated record -- only once the newest sample is
  `retain_period`/64 old, so a policy holds about 64 samples however often the
  scheduler runs (`compaction.SampleResolution`). Target = the newest usable
  sample at least `retain_period` old, which keeps at most one sample interval
  more history than the period asks. **After a leader change the new leader
  continues from the replicated samples**, and a restarted standalone node
  from its own store, so the retention clock never starts over. A fresh
  policy compacts nothing until a full period has been observed.
- Samples that cannot describe the store are never a target: a sample
  stamped in the future, samples out of order, or a sample above the store's
  current revision -- the revisions started over, after a restore or a
  re-created store -- make the whole series unusable, and a newer sample with
  a lower revision starts the series over. A delayed sample from a former
  leader, no newer than the newest, is ignored.
- Every sample and compaction the schedule proposes for a database carries
  that database's drop fence for the record's incarnation
  (`service.CompactionScheduleGuard`), so a record that outlived its
  database -- or one a database re-created under the name never wrote --
  compacts nothing: the state machine refuses the proposal.
- A target below 1 is skipped. A target the database is already compacted to
  is the typed already-compacted refusal, not a failure: floors only move
  forward, so a former leader's proposal landing after a new leader's is
  harmless.

Exactly one node runs a policy at a time: the group's leader. A leadership
change hands the schedule to the new leader with its samples; nothing is
duplicated and nothing restarts.

The scheduler stamps and ages samples by the wall clock unless the server is
given `server.WithCompactionClock`, a `clock.Clock` for this schedule alone;
`server.WithClock` does not reach it. The samples are replicated, so every node
of a cluster is given the same clock. The node tests drive the schedule on a
fake clock this way, which makes "the new leader continues from the replicated
samples" a property they assert rather than a race against the wall clock.

**Clock skew between leaders.** A sample's time is the wall clock of the
leader that took it, and a period policy's target is read against the wall
clock of the leader running it. When leadership moves between nodes whose
clocks disagree by `d`:

- **The new leader's clock is behind.** The newest sample is in its future,
  so the series is unusable (`compaction.State.UsableSamples`: a
  future-stamped sample is never a target) and a new sample is not newer than
  the newest, so it is not kept. Sampling and period compaction pause until
  the new leader's clock passes the newest sample -- at most `d` -- and then
  resume from the same series. Nothing is compacted early.
- **The new leader's clock is ahead.** The samples look `d` older than they
  are, so the target can move up to `d` sooner: the database keeps up to `d`
  less history than `retain_period` asks for.
- **A `revision` policy** reads no clock and is unaffected.

Keep the nodes' clocks synchronised (NTP) well inside the retention a period
policy is configured with; skew is bounded by how far apart the clocks are,
not by how often leadership moves.

## Persistence

- **Key layout** is owned by the state machine. The MVCC engine encodes
  `(key, revision)` composite keys so a range scan at a revision is a bounded
  seek rather than a full scan.
- **Revision triple**: each value carries create-revision, mod-revision and
  version, deliberately etcd-shaped so
  [../etcd-compatibility/](../etcd-compatibility/) is a translation.
- **Compatibility**: the reserved-prefix set is additive. Adding a prefix
  narrows the user keyspace, so a new one is a breaking change for any consumer
  already writing under it and must be treated as such under
  `conventions/release-versioning.md`.

## Test strategy

- **Unit**: `pkg/kv`, `pkg/mvcc`, `pkg/service` — happy path plus error path per
  function, 90%+ coverage goal.
- **Integration, standalone stage**: `make integration-test-standalone`; CI runs it
  in the `integration-standalone` job.
- **Integration, cluster stage**: `make integration-test-cluster` — the 3-node
  Raft suites (`isolation_raft_cluster_test.go`, `cluster_test.go`,
  `drain_test.go`, `backup_restore_test.go` and siblings). CI runs those
  suites in the `integration-features` job (`.github/workflows/ci.yml`), one leg per feature (`make integration-test-<feature>`), required by
  `ci-gate`.
- **Feature integration target**: `make integration-test-key-value` runs only
  this feature's self-spawned `qrdbd` suites (no Docker) — the `kv_*`,
  `batch_*` CLI tests plus `kv_test.go`'s cluster-correctness suite, selected
  by `test/integration/features.mk`. The per-transport accounting, including
  the REST-only cluster suite, is the `key-value` row of
  [`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
- **Transport parity**: the `_PerProtocol` CLI tests run the same table of
  commands over every network transport and assert the same output, so a
  `NotImplementedError` on one transport fails parity rather than passing as
  "partial".
- **Fault injection seams**: the storage backend and the state machine are both
  interfaces, so a failing backend is injected rather than simulated.

## Observability

- **Metrics** are exposed on `GET /metrics` in Prometheus format, by default.
  Storage syncs are timed in `qrdb_storage_sync_duration_seconds{backend}` and
  a slow one logs a warning naming the backend and database; see
  [../api-server/user.md](../api-server/user.md#disk-sync-latency).
- **Health** is `GET /health`; it is a public route by design so a load balancer
  can probe an unauthenticated node.
- **Audit**: KV mutations are recorded through [../audit/](../audit/) when the
  audit logger is configured.
- **Logging** uses the shared `pkg/logger`; levels are `trace | debug | info |
  warn | error`.
