# Transactions and Batch — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
- **`--transactional` import is all-or-nothing.** Any row error rolls back the
  entire import. Without it, rows are applied independently and `ImportResult`
  reports per-row failures.
- **Imports always target the raw KV keyspace.** There is no collection-targeted
  import path.
- **Import size ceilings are per transport**: 100 MB on REST, gRPC, Unix and
  MCP; approximately 63 MB on QUIC, set by the QUIC wire protocol's shared
  per-RPC payload cap.

## Error semantics

Typed errors throughout; `fmt.Errorf` is not used.

| Condition | Behavior | Fails closed? |
|---|---|---|
| Compare does not hold | `Succeeded: false`, `FailureOps` applied, no error | n/a — this is a result |
| Malformed request (unknown op type, missing key) | invalid-argument | yes |
| Import file exceeds the transport ceiling | refused before parsing | **yes** |
| Import format cannot be inferred and none given | refused | **yes** |
| `--transactional` import, any row fails | whole import rolled back, `ImportResult.Failed > 0` | **yes** |
| Missing `transaction:execute` / `batch:execute` | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

## Configuration

No feature-specific configuration keys. Behavior follows the store's
`raft.sm_type` and `storage.backend`, and write durability follows the request's
write mode (`sync`, `wal_durable`, `batched`) — see
[../key-value/engineering.md](../key-value/engineering.md).

Group commit for `batched` writes is the batch aggregator's job, which is a
storage-layer concern rather than part of this API.

## Persistence

Transactions and batches produce ordinary MVCC revisions. There is no separate
transaction log and no distinct on-disk representation: after commit, a
transaction is indistinguishable from the writes it made.

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
