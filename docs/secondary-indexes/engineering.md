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

// The store Client.KVStore() returns now satisfies AtomicKVStore for real:
// it forwards PutIfAbsent/CompareAndSwap/CompareAndDelete to the
// transport's GuardedKVClient capability, which every production
// transport (REST, gRPC, QUIC, Unix, MCP, embedded) already carries.
// Before this the adapter implemented only the base KVStore, so
// DAO.SaveIfAbsent always refused with AtomicWriteUnsupportedError
// regardless of transport.
//
// CompareAndSwapWithCurrent composes CompareAndSwap with a follow-up Get on
// a failed compare, so a contended counter (a PIN-lockout attempt count)
// learns the value it should have compared against without a second,
// separately-written round trip. The read is NOT part of the same atomic
// step -- see the function's own doc for the window that leaves open.
func CompareAndSwapWithCurrent(ctx context.Context, store AtomicKVStore, key string, expected, next []byte) (swapped bool, current []byte, err error)

// FencedCreator is kvstore.FencedCreator's alias, for an embedder holding a
// kvstore.KVStore it did not get from Client.KVStore() (a custom
// seal.StorageBackend composition, for one) and needs to assert for the
// same capability AtomicKVStore above is exported for. Neither it nor
// kvstore.MultiGuardedBatchKVStore (the real atomic multi-key write
// primitive pkg/dao.UnitOfWork uses when available) has a wire-level
// implementation on any sdk/go/transport client today, so the type alias
// does not make either capability newly reachable through
// Client.KVStore() -- only through a store the caller built or received
// some other way.
type FencedCreator = kvstore.FencedCreator

// DAO.SaveIfAbsent's refusal when the store cannot write atomically, whether
// it is not an AtomicKVStore at all or refused the call (the store's refusal
// is then reachable through Unwrap). It matches ErrKVAtomicWriteUnsupported,
// the one marker for the condition across the stack.
type DAOAtomicWriteUnsupportedError = dao.AtomicWriteUnsupportedError

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
func NewDAOUnitOfWork, RegisterDAOSave[E], RegisterDAODelete[E], WithDAOUnitOfWorkTimeout
type DAOAtomicBatchUnsupportedError

// DAOUnitOfWork.Commit REQUIRES the underlying store to implement
// kvstore.MultiGuardedBatchKVStore: every queued save/delete lands as ONE
// guarded batch proposal (unconditional -- no guards -- but still atomic).
// A store that does not offer the capability is refused with
// *DAOAtomicBatchUnsupportedError -- there is no sequential
// apply-and-compensate fallback; that path was removed because it was NOT
// atomic against a concurrent reader. This closes the move/rename case: a
// delete and a put in the same UnitOfWork commit together or not at all,
// never partially. NewDAOUnitOfWork, RegisterDAOSave and RegisterDAODelete
// are the generic functions a DAOUnitOfWork needs to be constructed and
// populated from outside the module; the type alias alone cannot cross a
// generic function boundary.
//
// FieldHashGenerator and DynamicFieldHashGenerator hash a natural key with
// FNV-1a, which has no resistance to a deliberately engineered collision.
// CryptoFieldHashGenerator and CryptoDynamicFieldHashGenerator hash with
// SHA-256 instead, for a NEW entity type that needs that resistance; the
// FNV-1a generators are unchanged, since swapping their hash function in
// place would orphan every entity already persisted under it.
// VerifyNaturalKey(entity, entityType, fieldName, expected) detects a
// collision AFTER a hash-derived Get, by comparing the read-back entity's
// natural-key field against the one the caller looked up by -- the one
// place a collision can still be caught, since nothing prevents one from
// being written in the first place.
var NewCryptoFieldHashGenerator, NewCryptoDynamicFieldHashGenerator
var VerifyNaturalKey
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
  (`__col:{database}:{collection}#{incarnation}/`, `{entityType}/`) and the registry does not
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
- **Every index records its OWNER, and creation is idempotent only for it.**
  `index.IndexConfig.Owner` names the object whose lifecycle created the index
  — `index.CollectionOwner(database, collection, incarnation)` for a
  collection's, the empty string for the explicit API and for every index
  created before owners existed. It travels as a trailing extension of the
  index-create command, and every state machine that applies index commands
  keeps it beside the index. Re-creating a name under the SAME owner succeeds
  without reconciling the definition — the existing definition wins. Creating
  a name another owner holds is **refused** (`index.OwnershipError`, reported
  as an application error so the apply advances; 409 / `ALREADY_EXISTS` on the
  wire) instead of silently adopting an index somebody else made. A divergent
  same-owner re-create must still be refused, or the previous index dropped,
  *before* the proposal. This is why an index left behind by a teardown is a
  correctness problem and not a space leak: the survivor silently overrides
  the definition the next creator asked for, and a `unique` constraint that
  creator requested is never enforced.
- **Index drop is idempotent.** Dropping a name that is not registered
  succeeds, so a teardown that is retried after an interruption costs nothing
  where it has already run.
- **Every transport's drop is owner-checked.** `IndexServicer.DropOwnedIndex`
  removes an index only when the recorded owner matches, decided on the apply
  path in the same step as the removal, and otherwise leaves it and returns
  `index.OwnershipError`. The explicit index API drops with the explicit owner
  (`index.ExplicitOwner`), so it removes explicit and pre-ownership indexes and
  refuses one a collection owns — 409 / `FAILED_PRECONDITION` — rather than
  remove a collection's `unique` constraint out from under it.
  `IndexServicer.DropIndex` removes an index whoever owns it; it is the drop
  whole-scope teardowns (`DROP DATABASE`, deleting a tenant) issue, since a
  scope owns everything in it, and no transport reaches it. The owner-checked drop is a trailing extension of the
  index-drop command; a drop command without it is the unconditional drop every
  existing log entry already is. An embedder-supplied
  `statemachine.IndexManager` receives the owner on `CreateIndex` (in its
  options) and the drop options on `DropIndex`, and must enforce both.
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
- **Lifecycle ownership is persisted, not derived from the name.** An index
  created through the explicit API under a name a collection would derive
  refuses the collection's create rather than being adopted, and survives the
  collection's drop; a collection's own indexes are removed by its drop and by
  nothing that does not own them, except a whole-scope teardown or an explicit
  unconditional drop. The owner names a collection's *incarnation*, so a
  collection dropped and created again under the same name cannot adopt or
  remove its predecessor's leftovers either. See
  [../collections/architecture.md](../collections/architecture.md#index-ownership).

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
| DAO `SaveIfAbsent` / `CompareAndSwap` / `DeleteIfMatch` over a store with no atomic write, or one that refused it | `*dao.AtomicWriteUnsupportedError` (matches `kvstore.ErrAtomicWriteUnsupported`; a store refusal stays in the chain) | **yes** — never degrades to `Get`-then-`Save`; any other store error is returned unchanged as outcome-unknown |
| DAO `CompareAndSwap` where `expected`/`next` carry different `EntityID()`s, or either `CompareAndSwap`/`DeleteIfMatch` on an entity with `EntityID() == 0` | `dao.InvalidEntityIDError` | **yes** |
| Store lacks the guarded swap — `CompareAndSwap` | `kvstore.ErrAtomicWriteUnsupported` | **yes** — a read followed by an unguarded put is the race the guard exists to close |
| Guarded verb on an ABSENT key — `CompareAndSwap` / `CompareAndDelete` | `(false, nil)` | **yes** — absence is an outcome, not a failure; an error here cannot be told from a broken store |
| `CompareAndSwap` outcome UNKNOWN (non-nil error) | error, reservation retained by the quota wrapper | **yes** — a Raft proposal can commit and lose its response; releasing would under-count a stored value |

**There is one atomic-refusal marker: `kvstore.ErrAtomicWriteUnsupported`.**
Every layer that refuses a guarded write for want of the capability — the
kvstore implementations, `dao.AtomicWriteUnsupportedError`, the database
backends, the service layer, the custodian service and the invitation store —
returns it or matches it under `errors.Is`
(`sdk.ErrInviteAtomicWriteUnsupported` is an alias of it). The package-local
sentinels that used to exist beside it are gone: five spellings of one
condition meant a caller testing one missed a refusal carrying another. The
marker also matches `kvstore.ErrNotApplied`: a capability refusal happens
before anything commits, so it proves nothing was written and a caller need
not read back to reconcile.

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
