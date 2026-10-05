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

Import:

```go
type ImportOptions struct {
	Format        string // auto | csv | json | ndjson | yaml | archive
	Filename      string
	KeyField      string
	ValueMode     string // field | row
	ValueField    string
	SkipHeader    bool
	Transactional bool
}

type ImportResult struct {
	Total     int
	Succeeded int
	Failed    int
	Errors    []ImportError // {Row, Key, Message}
}
```

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
- **Import size ceilings are per transport**: 100 MB on REST, gRPC, Unix and
  MCP; approximately 63 MB on QUIC, set by the QUIC wire protocol's shared
  per-RPC payload cap. On gRPC and Unix the 100 MB ceiling is an explicit
  opt-in (`grpcapi.WithMaxImportMessageSize` / `unixapi.WithMaxImportMessageSize`,
  re-exported as `sdk.WithGRPCMaxImportMessageSize`): grpc-go buffers an
  incoming message up to the server's configured receive bound before any
  interceptor runs, so leaving every RPC able to accept 100+ MB by default
  would let an unauthenticated peer force that allocation with no rate limit
  of its own. `cmd/qrdbd` opts in, since BatchImport over every
  transport is one of its shipped capabilities; an embedder that builds
  `pkg/api/grpc` or `pkg/api/unix` (or `pkg/server`) directly without opting in
  gets a 4 MiB default on EVERY RPC those servers expose, not just import.

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
| Import file exceeds the transport ceiling | refused before parsing | **yes** |
| Import format cannot be inferred and none given | refused | **yes** |
| `--transactional` import, any row fails | whole import rolled back, `ImportResult.Failed > 0` | **yes** |
| Missing `transaction:execute` / `batch:execute` | permission denied | **yes** |
| `UnitOfWork.Commit` by a caller holding `kv:put`/`kv:delete` but not `transaction:execute` | permission denied (**BREAKING**: the removed compensating fallback needed only `kv:put`/`kv:delete`) | **yes** |
| Barrier sealed | refused | **yes** |

## Configuration

No feature-specific configuration keys. Behavior follows the store's
`raft.sm_type` and `storage.backend`, and write durability follows the request's
write mode (`sync`, `wal_durable`, `batched`) — see
[../key-value/engineering.md](../key-value/engineering.md).

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
  cluster stage (`make integration-test-cluster`), which is **not currently
  wired into CI**.
- **Fault injection**: the import path takes an `io.Reader`, so a truncated or
  malformed stream is injected directly rather than staged on disk.

## Observability

- Transaction and batch mutations are audited through [../audit/](../audit/).
- Import failures are reported per row in `ImportResult.Errors` (`Row`, `Key`,
  `Message`) rather than collapsed into one error string.

## Wire-field bounds

The length-field contract for this plane — what each field can describe, that a
longer value is refused rather than truncated, that encoders return
`([]byte, error)` and validate before allocating, and that a refusal is a client
error mapping to `InvalidArgument`/400 — is one rule for the whole extended wire
protocol and is stated once, in
[../key-value/engineering.md](../key-value/engineering.md#wire-field-bounds--the-owning-contract-for-every-plane).

It is not restated here on purpose: four copies of a bound is how the four drift
apart.
