# Secondary Indexes and the Query Planner — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Index() transport.IndexClient
func (c *Client) KVStore() KVStore   // routes index ops to IndexClient

// Store capability, for consumers that must know before they depend on it.
// PutIfAbsent (take), CompareAndSwap (edit) and CompareAndDelete (release).
// Discovered by RUNTIME type assertion, so an implementation missing any one
// of them fails at run time rather than at build time — widening this
// interface means implementing it everywhere in the same change (#56).
type AtomicKVStore = kvstore.AtomicKVStore
var  ErrKVIndexUnsupported       = kvstore.ErrIndexUnsupported
var  ErrKVAtomicWriteUnsupported = kvstore.ErrAtomicWriteUnsupported

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

// DAO options, codec constants and sort directions. Prefixed because the SDK
// root has ClientOption namesakes — sdk.WithTimeout is a ClientOption.
var DAOWithShard, DAOWithCodec, DAOWithTimeout, DAOWithPartition, DAOWithDatabase
const DAOCodecJSON, DAOCodecMsgPack, DAOCodecCBOR
const DAOSortAscending, DAOSortDescending
type DAOUnitOfWork, DAOTransactional[E], DAOVersioned, DAOTimestamped
```

`sdk/go/transport`:

```go
type IndexClient interface {
	IndexCreate(ctx context.Context, name, fieldName string, config IndexConfig) error
	IndexDrop(ctx context.Context, name string) error
	IndexQuery(ctx context.Context, indexName, value string) (*IndexQueryResponse, error)
	IndexScan(ctx context.Context, indexName, start, end string) (*IndexScanResponse, error)
	IndexList(ctx context.Context) (*IndexListResponse, error)
}

type IndexConfig struct {
	Unique  bool `json:"unique"`
	Sparse  bool `json:"sparse"`
	Numeric bool `json:"numeric"`
}

type IndexQueryResponse struct{ Keys []string }
type IndexScanEntry    struct{ Value string; Keys []string }
type IndexScanResponse struct{ Entries []IndexScanEntry }
type IndexListResponse struct{ Names []string }
```

`pkg/index`:

```go
func ParseIndexTags(structType reflect.Type) (map[string]*IndexMetadata, error)

type CompoundIndexDef struct { Fields []string /* ... */ }

type CompoundIndexRegisterer interface {
	CompoundIndexes() []CompoundIndexDef
}
```

### Struct tags

The canonical tag key is `index`; parsing lives in `pkg/index/metadata.go`.

```go
type User struct {
	ID       uint64 `json:"id"`                                   // primary key (EntityID)
	Email    string `json:"email"    index:"unique"`
	Status   string `json:"status"   index:"true"`
	Username string `json:"username" index:"unique,case_insensitive"`
	Name     string `json:"name"`                                  // not indexed
}
```

| Tag value | Meaning |
|---|---|
| `index:"true"` | non-unique index |
| `index:"unique"` | unique index |
| `index:"case_insensitive"` | case-insensitive (strings) |
| `index:"unique,case_insensitive"` | options combine, comma-separated |

Tag-declared indexes are registered in the planner catalog at DAO construction.
**No runtime `IndexCreate` call is required for them.**

### Compound indexes

An entity opts in by implementing `CompoundIndexRegisterer`:

```go
func (m Message) CompoundIndexes() []index.CompoundIndexDef {
	return []index.CompoundIndexDef{
		{Fields: []string{"ConversationID", "Timestamp"}},
		{Fields: []string{"ConversationID", "IsRead"}},
	}
}
```

Registered automatically when the DAO is constructed. **Field order is
significant** — the first field is the leading prefix field.

## Invariants

- **Index names are `{database}.{entityType}.{FieldName}`.** A bare field name
  is not a valid index name and will not route.
- **An index takes only the keys inside its key scope.** The scope is
  `{tenant}/{database}/` read back off the index name, plus the key prefix the
  creator declared relative to it (`index.IndexConfig.KeyPrefix`, carried on
  the wire by `protocol.IndexCreateOptions.KeyPrefix`). Maintenance, the
  unique-constraint pre-check and the create-time backfill all apply it. An
  index with neither half is unscoped and takes every JSON value in the state
  machine — no transport can create one. See
  [architecture.md](architecture.md#index-key-scope--a-name-is-not-a-predicate).
- **The declared prefix is relative, and the engine composes it.** A caller
  below the namespacing registry declares the prefix it knows
  (`__col:{database}:{collection}/`, `{entityType}/`) and the registry does not
  touch it; prefixing it there as well would apply the namespace twice.
- **Index updates are atomic with the entity write.** There is no window in
  which the entity is visible and its index entry is not, which is what makes a
  unique constraint enforceable.
- **A unique index rejects the write, it does not overwrite.** Violating
  uniqueness fails the entity write.
- **A sparse index simply omits entities missing the field** — it does not index
  them under a zero value, which would make "missing" and "empty" the same
  query.
- **Compound prefix rule**: an index `(A, B, C)` serves a query only for a
  contiguous prefix. `B = ?` alone does not use it; `A = ? AND C = ?` uses the
  `A` prefix and post-filters `C`.
- **Compound key encoding is order-preserving.** Strings: byte-stuffed,
  null-terminated, `\x00` escaped as `\x00\xFF`, terminated `\x00\x00`.
  Numerics: 8-byte big-endian with sign-bit flip. Changing either encoding
  invalidates every existing compound index on disk.
- **Every index adds one storage write per entity `Put`/`Delete`.** Read speed
  is bought with write amplification, and that is the whole trade.
- **Index entries live under the reserved `__idx:` namespace** and are invisible
  to user KV enumeration — see
  [../key-value/engineering.md](../key-value/engineering.md).
- **A store that cannot index says so; it never answers empty.**
  `kvstore.KVStore` implementations return `kvstore.ErrIndexUnsupported` from
  `RegisterEntityIndexes` and from every index query method when they maintain
  no indexes. `(nil, nil)` is forbidden — it is indistinguishable from a
  maintained index with no matches.
- **The planner's catalog reflects the STORE, not just the entity.** `pkg/dao`
  probes the capability at construction; a refusing store yields an empty
  catalog, so tagged fields are answered by full scan rather than by an index
  nobody maintains. Without this, adding an `index:` tag is what breaks a
  query.
- **Only `ErrIndexUnsupported` degrades.** Any other registration error fails
  DAO construction. Treating a transient index-subsystem failure as a
  capability statement would permanently downgrade the DAO to full scans.
- **Index creation is idempotent BY NAME, and the EXISTING definition wins.**
  Re-creating a name that already exists succeeds without reconciling the
  definition — it cannot fail, because returning an error from the apply path
  would stall consensus. A divergent re-create must therefore be refused, or
  the previous index dropped, *before* the proposal. This is why an index left
  behind by a teardown is a correctness problem and not a space leak: the
  survivor silently overrides the definition the next creator asked for, and a
  `unique` constraint that creator requested is never enforced.
- **Index drop is idempotent.** Dropping a name that is not registered
  succeeds, so a teardown that is retried after an interruption costs nothing
  where it has already run.
- **An index created as part of another object's lifecycle is dropped by that
  object's teardown.** A collection's indexes are named
  `{database}.{collection}.{field}` and are dropped by `DropCollection`, after
  its documents and before its metadata (see
  [../collections/architecture.md](../collections/architecture.md)); a
  relational table's index data is reclaimed by `DROP TABLE` before its catalog
  entry. `DROP INDEX` on a relational table hands the index's entries to the
  reclaim sweeper instead of deleting them inline, and `RENAME INDEX` moves no
  entries at all (see `docs/sql/engineering.md`, "Index entries are keyed by
  incarnation, not name").
- **Lifecycle ownership is derived from the index NAME; nothing is persisted.**
  There is no owner field on an index and no registry of which object created
  which name. An index created through the explicit API under a name a
  collection would derive is adopted by that collection on create (creation is
  a name-idempotent no-op) and destroyed by its drop. Two consequences follow
  and both are current behaviour, not aspiration: the explicit API does **not**
  retain ownership against a colliding name, and a caller choosing an index
  name should avoid the `{database}.{collection}.{field}` shape for anything it
  intends to outlive that collection. Real ownership tracking is a separate
  capability.

## Query security

The fluent query API takes caller text and is hardened accordingly:

- **Field validation** — the field must resolve on the entity type; unknown
  fields are rejected.
- **Operator whitelist** — only whitelisted operators parse.
- **Parameterized values** — values bind as parameters and are never
  concatenated into expression text.
- **Zero-allocation expression parser** — no intermediate strings are built from
  caller input.

A change that relaxes any of these is a security change and needs review under
`conventions/security-review.md`.

## DAO aggregations

The fluent query API can aggregate as well as filter. Aggregation runs on top of
whatever plan the planner chose, so it inherits index selection.

```go
result, err := orderDAO.Find(ctx).
	GroupBy("CustomerID").
	Aggregate(dao.Count(), dao.Sum("Amount"), dao.Avg("Amount"),
		dao.Min("Amount"), dao.Max("Amount"))
```

| Constructor | Result alias | Result type |
|---|---|---|
| `dao.Count()` | `count` | `int64` |
| `dao.Sum(field)` | `sum_{field}` | `float64` |
| `dao.Avg(field)` | `avg_{field}` | `float64` |
| `dao.Min(field)` | `min_{field}` | `float64` |
| `dao.Max(field)` | `max_{field}` | `float64` |

```go
type AggregateResult struct{ Groups []GroupResult }
type GroupResult struct {
	Key    interface{}            // group key value; nil when no GroupBy
	Values map[string]interface{} // alias -> computed value
}
```

**Without `GroupBy`, every matching entity is one group**, so the result is
`Groups[0]`.

Execution is two-phase: the base query runs under the planner's chosen index,
then matching entities stream through in-memory accumulators. **Memory is
proportional to the number of groups, not the number of entities** — 10,000
unread messages across 50 conversations allocates 50 accumulators.

Aggregation limitations, all current:

- Values are `float64` except `Count`, which is `int64`. **Integer precision is
  lost above 2^53.**
- **`GroupBy` supports a single field only.** Multi-field grouping does not
  exist.
- Results are unsorted; sort in application code.
- All matching entities are loaded before grouping, so a wide query is bounded
  only by its `Where` filters.

Time-series aggregation is a different mechanism with its own functions and
bucketing — see [../time-series/](../time-series/engineering.md).

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| `IndexCreate` for a name that exists | `service.IndexExistsError` | yes |
| Query or scan against an unknown index | not-found | **yes** — never silently falls back to a full scan |
| Unique constraint violated on entity write | write refused | **yes** |
| Unknown field in `Where` | parse error | **yes** |
| Non-whitelisted operator | parse error | **yes** |
| Missing `index:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |
| Store maintains no indexes — `RegisterEntityIndexes` | `kvstore.ErrIndexUnsupported`; DAO plans full scans | **yes** — the capability is recorded, results stay correct |
| Store maintains no indexes — `QueryByIndex` / `ScanByIndex` / compound | `kvstore.ErrIndexUnsupported` | **yes** — a named access path cannot be silently substituted |
| Store maintains no indexes — planned `Find().Where(...)` | full scan, correct results | **yes** — no plan routes to an index that does not exist |
| Registration fails for any other reason | DAO construction fails | **yes** |
| Shared store lacks compare-and-set — `tenantSharedKVStore.PutIfAbsent` | `kvstore.ErrAtomicWriteUnsupported` | **yes** — never degrades to `Exists`-then-`Put` |
| Store lacks the guarded swap — `CompareAndSwap` | `kvstore.ErrAtomicWriteUnsupported` | **yes** — a read followed by an unguarded put is the race the guard exists to close |
| Guarded verb on an ABSENT key — `CompareAndSwap` / `CompareAndDelete` | `(false, nil)` | **yes** — absence is an outcome, not a failure; an error here cannot be told from a broken store |
| `CompareAndSwap` outcome UNKNOWN (non-nil error) | error, reservation retained by the quota wrapper | **yes** — a Raft proposal can commit and lose its response; releasing would under-count a stored value |

The unknown-index refusal matters: degrading to a full scan would turn a typo
into a silent performance cliff on a production dataset.

## Configuration

Indexes are declared per entity (struct tags) or created through the API. There
is **no per-index configuration through the config file today** — per-index
settings are API-only. Storage backend selection follows
`storage.backend` (`pebble`, `memory`), which determines the index backend too.

## Persistence

- A relational table's index entries live under
  `__idx:{db}:{tableID}:{generation}:{incarnation}/`. The generation segment is
  the table's current TRUNCATE generation (see `docs/sql/engineering.md`'s
  TRUNCATE TABLE section): it advances on every TRUNCATE, which is what lets a
  truncated table's old index entries be reclaimed as an ordinary
  orphaned-keyspace prefix instead of requiring a synchronous per-entry delete.
  The incarnation segment identifies the index itself: it is allocated when the
  index is created and never handed out twice within the table -- index DDL
  refuses while an `ALTER` rewrite is open, and the rewrite's write-back
  refuses a definition that moved under it, so the counter cannot go back --
  so renaming an index moves nothing and a dropped index's keyspace --
  reclaimed in the background -- can never become a newer index's.
- Compound entries store the concatenated order-preserving encoded field values
  plus the entity key — slightly larger than single-field entries.
- **Compatibility**: the key encoding is part of the on-disk format. Changing it
  requires an index rebuild, not a migration in place.
- **`OpIndexCreate` carries the key scope as a trailing
  `[prefixLen:2 LE][prefix]` block.** A command written before that block
  existed ends at the flag byte and decodes as an empty declared prefix, so a
  Raft log holding both shapes replays without a migration. An index recorded
  in such a log still gets the namespace half of its scope, because that half
  is read back off the name the log already carries.
- **An index PERSISTED before key scoping keeps the out-of-scope entries it
  already accumulated.** Maintenance no longer adds any, and a memory-backed
  index is rebuilt scoped on the next replay, but entries already written into
  a Pebble-backed index are only removed by dropping and recreating it — which
  backfills inside the scope.

## Test strategy

- **Unit**: `pkg/index` (tag parsing per tag form, compound definition
  registration, encoding round-trips including the `\x00` escape), `pkg/planner`
  (plan selection for each row of the prefix-matching table, cost comparison,
  plan cache hit/miss).
- **Security tests** are first-class here: unknown field, non-whitelisted
  operator, and an attempted injection through a value must each be refused.
- **Error tests** per function, happy path plus error path.
- **Integration**: index parity across transports in the standalone stage.

## Observability

- Index create and drop are audited through [../audit/](../audit/).
- Plan selection is observable at debug log level, which is the way to confirm a
  query used the index you expected rather than a full scan.
