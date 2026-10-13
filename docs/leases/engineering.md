# Leases — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-04

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
  `KeepAlive` writes `expiry` from the Raft apply goroutine while the sweeper
  reads it from its own; an unlocked read there is a real data race, measured
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
  resets `RemainingTTL` to it, it does not change the grant.
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
  one record per lease (ID -> granted TTL), one binding record per bound key
  (key -> lease ID), and the highest ID ever granted. The 0x00 prefix hides it
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
- **Restored leases start a full TTL.** Remaining TTL is wall-clock state that
  was never replicated, so after a gap of unknown length the only safe value is
  the granted one. Expiring early deletes keys whose owner may still be renewing;
  expiring a TTL late costs one TTL of staleness.
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
  whose record cannot be written is revoked and the grant fails. A write may
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
| Keep-alive against an already-expired lease | not-found; the keys are already gone | **yes** |

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

No lease-specific configuration keys. The sweep runs on the leader, once every
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
- **No `time.Sleep`.** Expiry is exercised by driving the clock/tick seam, not
  by waiting — the repo rule and the only way these tests stay deterministic on
  a loaded runner.
- **Integration**: lease parity across transports in the standalone stage.
  Multi-node expiry (leader change mid-lease) belongs to the cluster stage
  (`make integration-test-cluster`), which runs in CI as the
  `integration-cluster` job (`.github/workflows/ci.yml`), required by
  `ci-gate`.
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

`make integration-test-leases` runs this feature's self-spawned integration tests: `lease_binding_test.go` and `lease_cli_test.go`. The per-transport accounting, including every gap, is the `leases` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
