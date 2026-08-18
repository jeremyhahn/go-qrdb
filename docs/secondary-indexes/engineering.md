# Secondary Indexes and the Query Planner — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Index() transport.IndexClient
func (c *Client) KVStore() KVStore   // routes index ops to IndexClient
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

The unknown-index refusal matters: degrading to a full scan would turn a typo
into a silent performance cliff on a production dataset.

## Configuration

Indexes are declared per entity (struct tags) or created through the API. There
is **no per-index configuration through the config file today** — per-index
settings are API-only. Storage backend selection follows
`storage.backend` (`pebble`, `memory`), which determines the index backend too.

## Persistence

- Index entries live under `__idx:{db}:{tableID}:{index}/`.
- Compound entries store the concatenated order-preserving encoded field values
  plus the entity key — slightly larger than single-field entries.
- **Compatibility**: the key encoding is part of the on-disk format. Changing it
  requires an index rebuild, not a migration in place.

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
