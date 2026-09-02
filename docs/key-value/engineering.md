# Key-Value Store — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

### SDK — `sdk/go`

```go
func (c *Client) KV() transport.KVClient
func (c *Client) MVCC() transport.MVCCClient
func (c *Client) KVStore() KVStore          // kvstore adapter for DAOs
func (c *Client) DatabaseScoped(name string) *Client
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

Every transport carries the compile-time proof that it implements both:
`var _ transport.KVClient = (*Transport)(nil)`.

### Consistency levels — `pkg/consistency`

`consistency.Level` is an `iota` enum, in this order:

| Constant | `String()` | Mechanism | Typical latency |
|---|---|---|---|
| `Strong` | `strong` | full Raft consensus (SyncRead) | 10–50 ms |
| `Linearizable` | `linearizable` | Raft ReadIndex — **the default** | 1–5 ms |
| `LeaseRead` | `lease_read` | leader serves locally under a valid leadership lease | sub-ms |
| `Eventual` | `eventual` | local state machine, no coordination | <1 ms |
| `FollowerRead` | — | follower confirms applied index via ReadIndex, then reads locally | between `Eventual` and `Strong` |

`LeaseRead` trades a staleness window bounded by the election timeout during a
leader failover. `Eventual` has no such bound on a partitioned node.

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

Reserved prefixes: `__sys:`, `__tbl:`, `__idx:`, the table-sequence prefix,
`ts:`, `idx:metric:`, `idx:tag:`, `retention:`.

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
  mutable state is involved. On a transport with no per-database support (the
  embedded transport) the receiver is returned unchanged.
- **Revision monotonicity**: the store's current revision only increases.
  `Compact(r)` discards revisions strictly older than `r`; reads below the
  compaction floor fail closed rather than returning an approximation.
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
arrived at the service layer as a non-nil interface holding a nil slice and was
served as a successful read of an empty value: `Exists` answered true for every
key ever asked about, including keys never written and keys just deleted
(measured 2026-09-02 on `sm_type: disk` / `sm_storage: file`, single-replica
raft). The state machines without `sm.NALookup` took the generic `Lookup` path,
which returns an untyped nil, and were always correct — which is why the defect
was confined to some `sm_type`s.

`service.keyIsAbsent` (`pkg/service/service.go`) therefore treats a nil `[]byte`
as not-found as well as a nil interface. That compensates for the upstream
defect at this repo's four point-read call sites only; it does not fix any other
consumer of `Host.SyncRead`. The defect is tracked against the consensus
library, and when a release carrying its fix is pinned here the second arm
becomes redundant and can be removed. The code comment on `keyIsAbsent` carries
the tracker reference.

### The single-key guarded verbs — available on every state machine but `fast`

`PutIfAbsent`, `CompareAndSwap` and `CompareAndDelete` are one primitive:
acquire a claim, edit it under the claim, release it under the claim. They are
useful only together. A guarded create paired with an unguarded delete is half
a primitive — the delete can un-decide what the create decided — and a create
and a release with no guarded edit between them leaves the transition in the
middle decided nowhere.

- **They are not scoped to `mvcc`/`pebble`.** They are native opcodes
  (`kv.OpPutIfAbsent`, `protocol.OpCompareAndDelete` 66,
  `protocol.OpCompareAndSwap` 67) dispatched against the state machine's own
  store, so they need no MVCC store behind them. This is the one part of the
  guarded plane that is not confined to the default state machine: the
  multi-key transaction plane still requires `sm_type: mvcc`.

  Availability by `raft.sm_type`, which is the row to read before choosing one:

  | `sm_type` | `PutIfAbsent` | `CompareAndDelete` / `CompareAndSwap` |
  |---|---|---|
  | `pebble` (default), `mvcc`, `mvcc-fast` | yes | yes |
  | `memory`, `standard`, `concurrent`, `highperf`, `disk`, `regular` | yes | yes |
  | `fast` | yes | **refused** — `GuardedWriteUnsupportedError`, see below |

  `fast` is the single exception and it is a refusal, not a silent failure: its
  backend cannot keep the durability and ordering promises a guarded result
  makes, so the verbs decline rather than report a success the disk may
  contradict. The next two bullets say why, and go-qrdb#216 tracks making it
  capable. **Do not choose `sm_type: fast` for a workload that needs a guarded
  delete or swap.**
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

- **`sm_type: fast` cannot keep those promises, so it REFUSES the guarded
  delete and swap** with a typed `GuardedWriteUnsupportedError`, which reaches
  the caller as a capability refusal. It is a refusal and not a silent absence
  on purpose: "this backend will not do it" and "this build has no such verb"
  are different facts, and only the first is fixed by changing `raft.sm_type`.

  That type is a `KVStateMachine` over `FastBackend`, which persists on a
  background goroutine that is ordered against nothing and swallows its error.
  Measured deterministically on that backend with **ordinary unguarded writes**,
  no guarded verb involved: `Put(k,"old")` then `Put(k,"new")` leaves durable
  state holding `"old"`, and `Put(k,"v")` then `Delete(k)` leaves the key
  **present**, holding `"v"`. Applied to a guarded delete that is a released
  lock that comes back — the failure this plane exists to prevent — so
  advertising the verbs there would be worse than not offering them.

  Every other state machine keeps the promises and executes the verbs:
  `sm_type: disk` writes through synchronously and returns its error; `memory`,
  `standard`, `concurrent` and `highperf` are in-memory, so a write has landed
  when it returns; `mvcc-fast` owns its storage and orders every write through
  `persistOrder`. Making `fast` capable is **go-qrdb#216**; the unordered write
  path itself is **go-qrdb#224**.

  `PutIfAbsent` is the exception to the exception: it still executes on
  `sm_type: fast` despite having the same hole, because it has been offered
  there since go-qrdb#86 and callers depend on it — the identity index claims
  identities with it and first boot needs it. Withdrawing a capability people
  already build on is a decision for the owner, not a side effect; it is
  recorded on go-qrdb#216.
- **They cannot appear inside a batch.** The batch encoding carries no expected
  value, so a batched operation cannot express a guard. Multi-key atomicity is
  [../transactions/](../transactions/), which does need `sm_type: mvcc`.

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
- **Every encoder returns `([]byte, error)`.** This is source-breaking for an
  external consumer calling `pkg/protocol` directly; SDK consumers are
  unaffected.
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
[../../conventions](../api-server/engineering.md).

| Condition | Behavior | Fails closed? |
|---|---|---|
| Key absent | not-found, distinct from an empty value | n/a |
| Requested revision above current | invalid-argument | yes |
| Requested revision below the compaction floor | error, never an approximate value | **yes** |
| As-of read against a non-MVCC state machine | `service: requires raft.sm_type: mvcc` | **yes** |
| Restore naming a reserved prefix or key | refused, HTTP 403 equivalent, **no override** | **yes** |
| Restore with no scope or both scopes | refused | **yes** |
| Restore matching more than `MaxRestoreKeys` | refused whole | **yes** |
| Restore CAS conflict without `--force` | key left untouched, reported as `CONFLICT` | **yes** |
| Barrier sealed | request refused; no plaintext and no ciphertext served | **yes** |
| Node is not the leader and consistency requires it | routed to the leader, or refused when no resolver is configured | yes |

The restore refusal on `__sys:` has no override by design: restoring
`__sys:roles/…` or `__sys:users/…` to an earlier revision would undo a
permission revocation.

## Configuration

| Key | Values | Default | Effect |
|---|---|---|---|
| `raft.sm_type` | `pebble`, `memory`, `mvcc`, `standard`, `concurrent`, `fast`, `disk`, `highperf`, `regular` | `pebble` | State-machine wrapper. Only `mvcc` supports as-of reads, watch replay and selective restore. Every value supports the single-key guarded verbs EXCEPT `fast`, which refuses `CompareAndDelete`/`CompareAndSwap` (go-qrdb#216); only `mvcc`/`pebble` support transactions. |
| `storage.backend` | `pebble`, `file`, `memory` | `pebble` | Underlying storage engine. |
| `mvcc.compaction_retention` | integer | — | Minimum revisions retained; sets the compaction floor. |

```yaml
raft:
  sm_type: mvcc
mvcc:
  compaction_retention: 1000
```

Full reference: [../configuration/](../configuration/).

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
- **Integration, standalone stage**: `make integration-test-standalone`. Runs in
  CI today.
- **Integration, cluster stage**: `make integration-test-cluster` — the 3-node
  Raft suites (`isolation_raft_cluster_test.go`, `cluster_test.go`,
  `drain_test.go`, `backup_restore_test.go` and siblings). The harness is wired
  in the `Makefile`; **the CI job that runs it is not yet wired**, so these
  suites are dark in CI. Tracked as a known gap.
- **Transport parity**: each transport's `CommandDefinition` set is exercised
  against the same expectations, so a `NotImplementedError` on one transport
  fails parity rather than passing as "partial".
- **Fault injection seams**: the storage backend and the state machine are both
  interfaces, so a failing backend is injected rather than simulated.

## Observability

- **Metrics** are exposed on `GET /metrics` in Prometheus format, gated on a
  configured gatherer.
- **Health** is `GET /health`; it is a public route by design so a load balancer
  can probe an unauthenticated node.
- **Audit**: KV mutations are recorded through [../audit/](../audit/) when the
  audit logger is configured.
- **Logging** uses the shared `pkg/logger`; levels are `trace | debug | info |
  warn | error`.
