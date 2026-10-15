# Transactions and Batch — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-04

## Exported surface

`sdk/go`:

```go
func (c *Client) Transaction() transport.TransactionClient
func (c *Client) Batch() transport.BatchClient
```

`sdk/go/transport`:

```go
type TransactionClient interface {
	ExecuteTransaction(ctx context.Context, req *TransactionRequest, opts ...WriteOption) (*TransactionResponse, error)
}

type BatchClient interface {
	ExecuteBatch(ctx context.Context, ops []Op, opts ...WriteOption) (*BatchResponse, error)

	// ImportFile drives the whole chunked import session lifecycle
	// (ImportBegin, as many ImportAppend calls as data needs, ImportCommit)
	// transparently: the caller never sees a session ID or a sequence
	// number. data is read and split into transport-cap-sized chunks.
	ImportFile(ctx context.Context, filename string, data io.Reader, opts ImportOptions) (*ImportResult, error)
}
```

### `kvstore.MultiGuardedBatchKVStore` over `Client.KVStore()`

`sdk.Client.KVStore()`'s adapter implements `kvstore.MultiGuardedBatchKVStore`
(`PutAllIfUnchanged` / `PutAllIfAllUnchanged`) on every transport by
translating the call into exactly one `ExecuteTransaction` request and
reading `TransactionResponse.Succeeded` back as `applied`:

- Each `kvstore.Guard{Key, Value: nil}` ("must be absent") becomes one
  `Compare{Target: CompareVersion, Result: CompareEqual, Version: 0}`.
- Each `kvstore.Guard{Key, Value: v}` ("must be present and equal to `v`")
  becomes TWO compares — `{Target: CompareVersion, Result: CompareGreater,
  Version: 0}` (exists) plus `{Target: CompareValue, Result: CompareEqual,
  Value: v}` — because a value compare alone cannot distinguish an absent
  key from one whose stored value happens to be empty.
- Each `kvstore.KV{Delete: true}` becomes an `OpDelete`; otherwise an `OpPut`.

This is the identical translation `pkg/server/lifecycle.ExecuteGuardedBatch`
performs on the node side of the same `service.TransactionServicer.Execute`
call (`guardCompares`), so the guard/write encoding cannot drift between the
two ends of the wire. **No new RPC, proto message, or per-transport wire
plumbing was added for this**: `ExecuteTransaction` already carries
arbitrary compares plus put/delete ops as one atomic Raft/MVCC transaction on
every transport (REST, gRPC, QUIC, Unix, MCP, embedded) and every
`raft.sm_type`, which is exactly what `PutAllIfAllUnchanged`'s contract
needs. Adding a second, parallel verb that expressed the identical
guarantee under a different name would duplicate this RPC rather than reach
anywhere it cannot, so the capability is reached by composition at the SDK
boundary instead. `pkg/dao.UnitOfWork.Commit` (re-exported as
`sdk.NewDAOUnitOfWork`) requires this capability unconditionally and refuses
with `*dao.AtomicBatchUnsupportedError` otherwise -- there is no sequential,
non-atomic fallback -- so an embedder's cross-DAO move or rename reaches
this path too. The transport conformance matrix
(`sdk/go/conformance`) proves the translation atomic — a move lands as a
whole, and a failed guard applies neither write — on every engine, mode and
transport cell, through `sdk.NewKVStoreAdapter` exactly as a consumer
reaches it.

**BREAKING: `UnitOfWork.Commit` now requires `transaction:execute`, not
`kv:put`/`kv:delete`.** The removed sequential apply-and-compensate
fallback applied each queued operation through the DAO's own `Save`/`Delete`,
which over the SDK is a plain `kv.Put`/`kv.Delete` call -- gated by
`kv:put`/`kv:delete` on every transport (the proto's
`option (qrdb.v1.permission)` on `rpc Put`/`rpc Delete`). `Commit` now
always goes through the SAME `ExecuteTransaction` RPC `PutAllIfAllUnchanged`
does, gated by `transaction:execute` (`rpc Transaction`'s own proto option,
identical on REST/QUIC/MCP's route tables and the etcd shim). A role or a
caller that held `kv:put`/`kv:delete` but not `transaction:execute` could
commit a `UnitOfWork` before; it is refused with `PermissionDenied` now.
The built-in `Operator` role holds both and is unaffected; a caller running
under a narrower, hand-authored role needs `transaction:execute` added.

### Types

```go
type CompareTarget uint8
const (
	CompareVersion CompareTarget = iota
	CompareCreateRevision
	CompareModRevision
	CompareValue
)

type CompareResult uint8
const (
	CompareEqual CompareResult = iota
	CompareGreater
	CompareLess
	CompareNotEqual
)

type Compare struct {
	Key            []byte
	Target         CompareTarget
	Result         CompareResult
	Value          []byte
	Version        int64
	CreateRevision int64
	ModRevision    int64
}

type OpType uint8
const (
	OpPut OpType = iota
	OpGet
	OpDelete
	OpRange
)

type Op struct {
	Type     OpType
	Key      []byte
	Value    []byte
	RangeEnd []byte
	LeaseID  int64
}

type TransactionRequest struct {
	Compares   []Compare
	SuccessOps []Op
	FailureOps []Op
}

type TransactionResponse struct {
	Succeeded bool
	Revision  int64
	Responses []OpResponse
}

type OpResponse struct {
	Kvs       []*MVCCKeyValue
	Count     int64
	Succeeded bool
}

type BatchRequest struct{ Ops []Op }
type BatchResponse struct{ Responses []OpResponse }
```

`encodeServiceOp` maps only `OpPut` and `OpDelete` to protocol opcodes. `OpGet`,
`OpRange` and any unmapped value in a transaction branch or batch fail closed
with `UnsupportedTransactionOpError` before anything is proposed, rather than
defaulting to a write that would corrupt the key.

Import session — `pkg/service.BatchServicer`:

```go
type ImportOptions struct {
	Format        string // auto | csv | json | ndjson | yaml | archive
	Filename      string
	KeyField      string
	ValueMode     string // field | row
	ValueField    string
	SkipHeader    bool
	Transactional bool
	KeyPrefix     string // set by the per-database wrapper, not the caller
}

type ImportResult struct {
	Total     int
	Succeeded int
	Failed    int
	Errors    []ImportError // {Row, Key, Message}
}

type BatchServicer interface {
	Execute(ctx context.Context, ops []Op) ([]OpResponse, error)
	ExecuteWithMode(ctx context.Context, ops []Op, mode writemode.Mode) ([]OpResponse, error)
	ExecuteEntity(ctx context.Context, req *BatchEntityRequest) (*BatchEntityResponse, error)

	// ImportBegin starts a chunked import session scoped to the caller's
	// (tenant, database) and (tenant, identity). opts configures the
	// import exactly as a single-message import always has.
	ImportBegin(ctx context.Context, opts ImportOptions) (sessionID string, err error)

	// ImportAppend stages chunk as sequence seq of sessionID and renews
	// the session's lease. seq must be exactly one more than the last
	// appended sequence, or equal to it with identical bytes (an
	// idempotent replay); any other value is refused. The caller's
	// (tenant, identity) must match the session's.
	ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error

	// ImportCommit applies every staged chunk of sessionID atomically,
	// exactly as a single-message import applied its one file, and
	// releases the session. The caller's (tenant, identity) must match
	// the session's.
	ImportCommit(ctx context.Context, sessionID string) (*ImportResult, error)

	// ImportAbort releases sessionID -- its lease, and therefore its
	// metadata record and every staged chunk -- without applying
	// anything. The caller's (tenant, identity) must match the session's.
	ImportAbort(ctx context.Context, sessionID string) error
}
```

There is no separate `ImportSessionServicer`: the four lifecycle methods
are part of `BatchServicer` itself, alongside `Execute`/`ExecuteWithMode`/
`ExecuteEntity`, because every wrapper that already implements
`BatchServicer` for one reason (namespace confinement, per-tenant quota,
per-call service resolution) needs to wrap all seven together — splitting
import into its own interface would not reduce how many types implement
it, only how many `var _ Xxx = (*T)(nil)` lines prove they still do.

Note the enum ordering: `CompareTarget` and `CompareResult` are `iota` enums, so
the JSON wire form carries **integers**, not names. A client that hardcodes
`"target": 3` is asking for `CompareValue`.

## Invariants

- **A transaction is one Raft proposal.** Every compare is evaluated against one
  snapshot, and either `SuccessOps` or `FailureOps` applies in full. There is no
  interleaving with another proposal in between.
- **`Succeeded == false` is a normal result, not an error.** The request
  succeeded; the predicate did not hold, and `FailureOps` ran.
- **`Responses` is positional** — one entry per op in the branch that ran, in
  order.
- **A batch is unconditional and atomic.** All ops apply or none do.
- **The same request answers the same on every `sm_type`**, except that only
  `mvcc` accepts revision compares. `pkg/server/lifecycle`'s
  `TestGuardedPlaneConformsOnEverySMType` holds every type to the default's
  answers.
- **`--transactional` import is all-or-nothing.** Any row error rolls back the
  entire import. Without it, rows are applied independently and `ImportResult`
  reports per-row failures.
- **Imports always target the raw KV keyspace.** There is no collection-targeted
  import path.
- **Every call in the session lifecycle is bounded by the universal
  pre-auth message cap** (`transport.max_message_bytes`, default 4 MiB),
  identical on every transport. There is no larger-message opt-in; a
  dataset bigger than one chunk needs more `ImportAppend` calls, not a
  bigger one.
- **`ImportAppend`'s `seq` is strictly sequential, with exactly one
  exception: replay.** `seq` must equal `lastSeq + 1` (a new chunk) or
  `lastSeq` with byte-identical content (idempotent replay of the call
  whose response the caller may not have seen). `lastSeq` with DIFFERENT
  content, or any other value, is refused — a gap or a regression is
  never silently accepted.
- **A session belongs to the (tenant, identity) that opened it.**
  `ImportAppend`/`ImportCommit`/`ImportAbort` compare the request's
  authenticated identity and tenant against what `ImportBegin` recorded,
  and refuse a mismatch on EITHER dimension independently — same tenant,
  different identity is refused; same identity shape, different tenant is
  refused. A session ID grants no authority of its own.
- **`ImportCommit` applies all staged chunks as one atomic unit** — the
  combined bytes feed the SAME parse-and-apply path a single-message
  import always used, so `--transactional` and per-row semantics are
  identical to the pre-session behavior. A failure partway through commit
  (a chunk whose concatenation cannot be parsed, a proposal that is
  refused) applies nothing: the staged chunks and the session survive a
  failed commit so the caller can inspect and retry, and are released only
  on success, on `ImportAbort`, or on lease expiry.
- **An abandoned session is reclaimed by lease expiry, not a background
  sweep of its own.** The session's lease is the only liveness mechanism;
  there is no separate timer, cron, or GC pass for import sessions.

## Error semantics

Typed errors throughout; `fmt.Errorf` is not used.

| Condition | Behavior | Fails closed? |
|---|---|---|
| Compare does not hold | `Succeeded: false`, `FailureOps` applied, no error | n/a — this is a result |
| Malformed request (unknown op type, missing key) | invalid-argument | yes |
| Empty key in a compare or an op | refused before proposal (`service.InvalidKeyError` inside `EncodeRequestError`, invalid-argument); a state machine that receives one anyway refuses it as an application result and keeps applying | **yes** |
| Create- or mod-revision compare on any `sm_type` but `mvcc` | refused, nothing evaluated or written | **yes** |
| Lease-bearing op on a `pkg/statemachine` `sm_type` (`memory`, `standard`, `concurrent`, `fast`, `disk`, `highperf`) | the key is bound to the lease in the same commit; a lease the state machine does not hold refuses the whole transaction or batch with lease-not-found, nothing written | **yes** |
| Lease-bearing op on the legacy `regular` `sm_type` | whole transaction or batch refused as an unsupported capability, nothing written | **yes** |
| A chunk, or any other single call, exceeds the transport's pre-auth cap | refused before parsing, before authentication on every transport | **yes** |
| `ImportBegin`'s format cannot be inferred and none given | refused | **yes** |
| `--transactional` commit, any row fails | whole import rolled back, `ImportResult.Failed > 0` | **yes** |
| `ImportAppend`'s `seq` is not `lastSeq+1` and not an identical replay of `lastSeq` | refused (`*ImportSequenceError`), nothing staged | **yes** |
| `ImportAppend`/`ImportCommit`/`ImportAbort` naming an unknown or expired session | refused (`*ImportSessionNotFoundError`) | **yes** |
| `ImportAppend`/`ImportCommit`/`ImportAbort` from a different (tenant, identity) than `ImportBegin` recorded | refused (`*ImportSessionOwnerError`) | **yes** |
| A session's staged bytes, chunk count, or its tenant's concurrent session count would exceed its configured quota | refused (`*ImportQuotaExceededError`) | **yes** |
| A staging command (`protocol.OpImportStage`) reaches an engine that keeps no leases: the plain KV engine of a standalone node on the `file` or `objstore` backend, or any engine built without a lessor | refused as an unsupported capability (`ServiceNotConfiguredError` naming `lease`) in the apply result, nothing staged; apply keeps advancing. On those backends `ImportBegin`'s own lease grant is refused first | **yes** |
| `ImportCommit` carries more encoded ops than one Raft proposal admits (`maxImportProposalBytes`, 8 MiB) and `--transactional` is set | refused before anything is proposed (`*ImportProposalTooLargeError`); all-or-nothing has no multi-proposal path, so this is the hard ceiling on a transactional import's size | **yes** |
| A chunked (non-transactional) import's Raft proposal for one chunk suffers an outcome-unknown fault (`errors.Is(err, service.ErrOutcomeUnknown)` — the entry may or may not have committed) | the WHOLE `ImportCommit` call fails; no later chunk is attempted, and the session is NOT revoked, so the caller can retry `ImportCommit` — never downgraded to a per-row failure in either mode, because the chunk is not provably "nothing stored" | **yes** |
| Same, but the chunk's proposal commits and is refused ON ITS MERITS (a result-level `AppError` — `checkResultError`'s path, provably nothing stored) | that chunk's rows are recorded failed; later chunks are still attempted (non-transactional) or the whole commit is refused (transactional) | **yes** |
| `ImportCommit` reaches a replica that knows no leader for the shard: mid-election, or right after a leader change when the new leader steps down on losing quorum | every session read and chunk proposal is held by the Raft host adapter until a leader is known, then reissued — the leader-wait contract in [../clustering/engineering.md](../clustering/engineering.md) — and a read the new leader cannot serve yet (`quicraft.ErrReadIndexNotReady`: nothing committed in its term) is retried for up to 5 s on top. A session read a leadership change stranded is reissued to the new leader; a proposal refused naming a leader returns at once, for the transport to forward. At the 10 s bound a refused session read fails the call with `UNAVAILABLE` / REST 503 `NOT_LEADER` before anything is applied, leaving the session retryable; a refused chunk proposal stored nothing and is handled like the refusal on its merits above | **yes** |
| Missing `transaction:execute` / `batch:execute` | permission denied | **yes** |
| `UnitOfWork.Commit` by a caller holding `kv:put`/`kv:delete` but not `transaction:execute` | permission denied (**BREAKING**: the removed compensating fallback needed only `kv:put`/`kv:delete`) | **yes** |
| Barrier sealed | refused | **yes** |

## Configuration

Behavior follows the store's `raft.sm_type` and `storage.backend`, and
write durability follows the request's write mode (`sync`, `wal_durable`,
`batched`) — see [../key-value/engineering.md](../key-value/engineering.md).

An import session is additionally bounded by `pkg/config.ImportSessionConfig`
(`import_session:` in YAML; see `docs/12-configuration.md`):

| Field | Default | Meaning |
|---|---|---|
| `max_session_bytes` | 64 MiB | total staged bytes a single session may accumulate across all its chunks |
| `max_chunks_per_session` | 16384 | chunk-count ceiling per session |
| `max_sessions_per_tenant` | 16 | concurrent (not yet committed/aborted/expired) sessions a tenant may hold |
| `session_ttl_seconds` | 3600 | the session's lease TTL; renewed on every `ImportAppend` |

All four resolve a non-positive configured value to their default (the
same convention `transport.*` uses), so a `Config` built without going
through `DefaultConfig` still carries production bounds.

**Staged bytes are not metered against the tenant's storage quota; this is
the design boundary, not a gap.** `pkg/quota.Quotas.MaxStorageBytes` (and
`MaxKeys`) track committed data — the same counters every other write
updates — and a session's chunks are not yet applied to the state machine,
so they do not touch those counters while staged. The ONLY cap on staging
is `import_session`'s own four fields above (`max_session_bytes`,
`max_chunks_per_session`, `max_sessions_per_tenant`,
`session_ttl_seconds`), which bound the resource a session can hold
independent of the tenant's storage quota. `ImportCommit` is the one place
staged bytes become metered: committing feeds the combined chunks through
the SAME parse-and-apply path, and therefore the same per-write quota
accounting, as every other write — a session is invisible to
`MaxStorageBytes` right up to the commit that makes it real.

Group commit for `batched` writes is the batch aggregator's job, which is a
storage-layer concern rather than part of this API.

## Persistence

On `sm_type: mvcc` transactions and batches produce ordinary MVCC revisions;
on every other type they are ordinary writes to the state machine's keyspace.
There is no separate transaction log and no distinct on-disk representation:
after commit, a transaction is indistinguishable from the writes it made.

A transaction or batch commits all or nothing (`statemachine.Backend.ApplyAtomic`
on the `pkg/statemachine` types): no concurrent read sees part of it, and on
`disk` the entry's applied index commits in the same storage batch, so a crash
never replays it over its own result.

## Test strategy

- **Unit**: predicate evaluation for each `CompareTarget` × `CompareResult`
  combination, branch selection, positional response ordering, and import
  parsing per format.
- **Error tests** per function, as required by the repo's TDD rule: at minimum a
  happy path and an error path.
- **Integration**: `make integration-test-standalone` covers transaction and
  batch parity across transports; the multi-node atomicity cases live in the
  cluster stage (`make integration-test-cluster`), which runs in CI as the
  three legs of the `integration-cluster` job (`.github/workflows/ci.yml`),
  required by `ci-gate`.
- **Fault injection**: the import path takes an `io.Reader`, so a truncated or
  malformed stream is injected directly rather than staged on disk. A
  multi-chunk `ImportCommit` is covered separately: a fake host's
  `SyncPropose` returns a generic (outcome-unknown) error on the SECOND of
  several chunk proposals, proving the whole commit fails,
  `errors.Is(err, service.ErrOutcomeUnknown)` holds, no later chunk is
  attempted, and the session survives for a retry
  (`TestImportCommit_ProposalFaultMidImport_LeavesSessionRetryable`).
- **Import session**: sequence ordering (gap refused, regression refused,
  exact replay accepted as a no-op), wrong caller refused (same tenant,
  different identity), cross-tenant refused (different tenant, matching
  identity shape — a SEPARATE test from the previous one: either check
  alone can be vacuous in a way the other catches), cross-DATABASE refused
  (same tenant, same identity, a different namespaced database wrapper than
  `ImportBegin` was called through), abort discards staged data, a failure
  parsing the assembled file applies nothing and leaves the session intact,
  a mid-commit outcome-unknown proposal fault aborts the whole commit
  without revoking the session (above — this does NOT roll back whichever
  earlier chunk already committed in the same `ImportCommit` call: only a
  single-proposal, `--transactional` commit at or under 8 MiB is genuinely
  atomic; a larger `--transactional` import is refused outright rather than
  attempted non-atomically), lease expiry reclaims an abandoned session
  (`pkg/server`'s `TestBuildDataPlaneServices_ImportSession_LeaseExpiryReclaims`
  drives a real `lease.Lessor`'s TTL to actual elapse, not a simulated one),
  and a leader change between `ImportBegin` and `ImportCommit` does not
  lose a staged chunk (`pkg/service`'s
  `TestImportSession_LeaderChangeDoesNotLoseStagedChunk`, a real 3-node
  Raft group under `sdk/go/testutil/raftharness` with an actual
  `RequestLeaderTransfer`, `ImportCommit` reissued against the new leader
  through a separate `BatchService` instance sharing nothing with the one
  that staged the chunk). If the new leader steps down before that commit
  lands, the commit holds through the re-election rather than failing; the
  hold itself is unit-tested in `pkg/backend/raft/leader_wait_test.go`.

## Observability

- Transaction and batch mutations are audited through [../audit/](../audit/).
- Import failures are reported per row in `ImportResult.Errors` (`Row`, `Key`,
  `Message`) rather than collapsed into one error string.
- An import session's lifecycle (`Begin`, each `Append`, `Commit`/`Abort`,
  and a lease-expiry reclaim) is audited through [../audit/](../audit/) the
  same way every other mutation is.

## Wire-field bounds

The length-field contract for this plane — what each field can describe, that a
longer value is refused rather than truncated, that encoders return
`([]byte, error)` and validate before allocating, and that a refusal is a client
error mapping to `InvalidArgument`/400 — is one rule for the whole extended wire
protocol and is stated once, in
[../key-value/engineering.md](../key-value/engineering.md#wire-field-bounds--the-owning-contract-for-every-plane).

It is not restated here on purpose: four copies of a bound is how the four drift
apart.

## Integration testing

`make integration-test-transactions` runs this feature's self-spawned integration tests: `tx_cli_test.go` and `sql_transaction_test.go`. The per-transport accounting, including every gap, is the `transactions` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
