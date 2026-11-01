# Secondary Indexes

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

QRDB provides secondary indexes for efficient lookups and range scans on entity attributes without scanning the entire dataset.

## 1. Overview

Secondary indexes enable fast queries on any field, not just the primary key. When you create an index on a field, QRDB maintains an inverted mapping from field values to entity keys.

```
Entity KV Store:           Index on "Email":
user:1 → {email: alice@}   alice@ → user:1
user:2 → {email: bob@}     bob@   → user:2
user:3 → {email: alice@}   alice@ → user:3
```

When you query `Email = "alice@example.com"`, the index is consulted instead of scanning all entities.

## 2. Index Types

An index's behaviour is set by the `service.IndexConfig` (`index.IndexConfig`)
passed to `IndexService.CreateIndex(ctx, name, fieldName, config)`.
`CaseSensitive` names the default, so its zero value folds case: set it
explicitly.

### Unique Index

A unique index enforces that no two entities have the same value for the indexed field.

```go
// Enforce unique email addresses
err := indexService.CreateIndex(ctx, "qrdb.User.Email", "Email", service.IndexConfig{
    Unique:        true, // No duplicates allowed
    CaseSensitive: true,
})
// Now: a second entity with Email = alice@example.com is refused
```

**Characteristics:**
- A write whose value already exists is refused; it never overwrites the entry
- Lookup returns at most one result
- Useful for natural keys (email, username, SSN)

### Non-Unique (Normal) Index

A normal index allows multiple entities to have the same value.

```go
// Index user status (many users can be "active")
err := indexService.CreateIndex(ctx, "qrdb.User.Status", "Status", service.IndexConfig{
    CaseSensitive: true,
})
// Now: Can have many users with Status = "active"
```

**Characteristics:**
- Insertion succeeds even if the value exists
- Lookup returns all matching entities (O(k))
- Useful for categorical fields (status, role, type)

### Entities missing the field

Every index omits an entity that does not carry the indexed field; it is never
indexed under a zero value. `Sparse: true` is recorded on the index definition
(and set by the CLI's `--sparse` and the `sparse` struct tag), but no index
maintenance path reads it, so a sparse and a non-sparse index hold the same
entries.

```go
// Index only users with a "Department" field set
err := indexService.CreateIndex(ctx, "qrdb.User.Department", "Department", service.IndexConfig{
    Sparse:        true,
    CaseSensitive: true,
})
```

A query for entities that lack the field cannot use the index and must scan.

## 3. Database-Scoped Index Names

Index names are scoped to a database. A DAO names a tag-declared index by entity type (`genericDAO.indexName`, `pkg/dao/generic_dao.go`); a compound index joins its fields with `_`. A collection names its indexes by collection (`collectionIndexName`, `pkg/service/collection_types.go`):

```
{database}.{entityType}.{field}     # DAO; {entityType}.{field} with no database
{database}.{collection}.{field}     # collection
```

**Examples:**

```
qrdb.User.Email                 # DAO index on User entities in "qrdb"
User.ConversationID_Timestamp   # DAO compound index, no database
qrdb.users.Email                # Email index on the "users" collection in "qrdb"
myapp.users.Email               # Email index in "myapp" (different from qrdb.users.Email)
```

**Benefits:**

- Prevents name collisions across entity types and collections
- Supports multi-database deployments

**Creation example:**

```go
err := indexService.CreateIndex(ctx, "qrdb.User.Email", "Email", service.IndexConfig{
    Unique:        true,
    CaseSensitive: true,
})
```

## 4. Collection Index Definitions

A collection declares its indexes in the create request, by field:

```go
info, err := collectionService.CreateCollection(ctx, &service.CreateCollectionRequest{
    Name:     "users",
    Database: "myapp",
    Indexes: []service.IndexDefinition{
        {FieldName: "Email", Unique: true},
        {FieldName: "Status"},
        {FieldName: "Department", Sparse: true},
    },
})
```

Each index is named `{database}.{collection}.{field}` (here `myapp.users.Email`)
and owned by the collection's incarnation (`index.CollectionOwner`). The
collection serves no documents until every declared index exists; a create
whose indexes cannot all be built unwinds and leaves nothing behind.

## 5. Struct Tag Integration

Entity structs use Go struct tags to declare which fields are indexed.
The DAO parses these tags at construction time and registers the
resulting indexes in the planner's catalog automatically — no runtime
`CREATE INDEX` call is required for tag-declared indexes.

```go
type User struct {
    ID         uint64 `json:"id"`                          // Primary key (EntityID)
    Email      string `json:"email"      index:"unique"`   // Unique index
    Status     string `json:"status"     index:"true"`     // Non-unique index
    Username   string `json:"username"   index:"unique,case_insensitive"`
    Name       string `json:"name"`                        // No index
}
```

**Tag syntax:**

```go
`index:"true"`                      // Non-unique index (also "normal")
`index:"unique"`                    // Unique index
`index:"sparse"`                    // Skip absent or empty values
`index:"case_insensitive"`          // Case-insensitive strings (also "ci")
`index:"case_sensitive"`            // Case-sensitive strings (also "cs")
`index:"fulltext"`                  // Full-text index (also "fts")
`index:"unique,case_insensitive"`   // Combine options (comma-separated)
```

The canonical tag key is `index`. Parsing lives in
`pkg/index/metadata.go` (`ParseIndexTags`). Once tagged, fields are
queryable through `Find(ctx).Where("Field = ?", v)` with automatic index
selection — see [16-query-planner.md](16-query-planner.md).

## 6. Storage

An index has no backend of its own: it lives in the store of the database it
indexes, so it persists exactly as that store does.

| Where | Index implementation | Backing |
|---|---|---|
| `KVEngine`, `MVCCEngine` | `index.StoragePebbleIndex`; `KVEngine` builds a compound index as `index.CompoundIndex` | the engine's `storage.Storage`: memory, file, pebble or objstore |
| Pebble state machine (`pkg/store`) | `pebbleindex.NewWithConfig` | the state machine's `*pebble.DB` |
| In-process `kvstore.MemoryKVStore` | `index.ExtendedMemoryIndex` | process memory, lost on exit |

`storage.backend` selects the database's store, never a separate index store.
`StoragePebbleIndex` keeps one entry per (value, entity key), ordered by value,
which is what serves range scans:

```
idx:{indexName}:{fieldValue}:{entityKey}{uint32BE(len(entityKey))} → (empty)
```

`index.MemoryIndex` (`pkg/index/memory_index.go`) is a map under a `sync.RWMutex`; the engines do not use it.

## 7. Query Planner Integration

The query planner automatically selects indexes based on query predicates. When you issue a query, the planner evaluates:

```
Query: WHERE Email = "alice@example.com" AND Status = "active"

Available indexes:
  - Email (unique)     → Cost 1 (O(1) lookup)
  - Status (non-unique) → Cost 10 (O(k) lookup)
  - Full scan          → Cost 1000 (O(n))

Selected plan:
  1. Use Email index (cost 1)
  2. Post-filter Status = "active"
  3. Return results
```

The planner picks the lowest-cost strategy.

## 8. Index Lifecycle

The methods below are `service.IndexServicer`'s (`index.IndexServicer`).

### Creation

```go
err := indexService.CreateIndex(ctx, "qrdb.User.Email", "Email", service.IndexConfig{
    Unique:        true,
    CaseSensitive: true,
})
if err != nil {
    // e.g. service.IndexExistsError when the name is taken
}
```

### Lookup

```go
// Exact match: the keys of every entity whose Email is the value
keys, err := indexService.Query(ctx, "qrdb.User.Email", []byte("alice@example.com"))
```

### Range Scan

```go
// Half-open range [start, end): index value → matching keys
entries, err := indexService.Scan(ctx, "qrdb.User.Status", []byte("active"), []byte("inactive"))
// Returns every value v with "active" <= v < "inactive"
```

### Listing

```go
// The names of every registered index
names, err := indexService.ListIndexes(ctx)
```

### Deletion

```go
// Drop an index created through the index API
err := indexService.DropOwnedIndex(ctx, "qrdb.User.Email", index.ExplicitOwner)
// Query and Scan on the name now return not-found
```

`DropOwnedIndex` refuses an index a collection owns (`*index.OwnershipError`);
an absent index is success. `DropIndex` drops whoever owns the index and is
reserved for whole-scope teardown (DROP DATABASE, tenant deletion); no
transport reaches it.

There is no rebuild call: drop the index and create it again, and the create
backfills it from the entities already stored.

## 9. Index-Accelerated WHERE Clauses

When using the DAO fluent API, index acceleration is automatic. This is
the recommended entry point for application code — the planner picks
the best index based on the predicates and registered catalog:

```go
// Index-accelerated query. Context is passed once to Find().
users, err := userDAO.Find(ctx).
    Where("Email = ?", "alice@example.com").
    Where("Status = ?", "active").
    OrderBy("CreatedAt").                // default ascending
    Execute()
// Planner picks best index, applies post-filters
```

Equivalent explicit index query (power-user escape hatch — bypasses
the planner, requires you to know the field name):

```go
results, err := userDAO.QueryByIndex(ctx, "Email", "alice@example.com")
```

See [16-query-planner.md](16-query-planner.md) for the full fluent API
reference, supported operators, and when to prefer each path.

## 10. Write Amplification

Every write to an indexed entity also writes one entry per index that covers
it, in the same atomic write as the entity:

```
PUT {ID: user1, Email: alice@, Status: active, Department: eng}
  1. Write to KV store (user:1)
  2. Update Email index (alice@ → user1)
  3. Update Status index (active → user1)
  4. Update Department index (eng → user1)
  = 4 writes instead of 1
```

Index only the fields you query on to keep write amplification down.

## 11. Configuration

### Configuration File

```yaml
index:
  max_indexes: 64   # validated as >= 0; 0 means unlimited
```

`index.max_indexes` caps the indexes each shard holds: a create for a new name
past it is refused at apply with `IndexLimitError` (HTTP 429, gRPC
`RESOURCE_EXHAUSTED`), however many nodes the creates arrive through.
Per-index settings are API-only; the configuration file has none.

### CLI

```bash
# Create index
qrdb index create qrdb.User.Email --field Email --unique   # also --sparse, --numeric

# List indexes
qrdb index list

# Query index
qrdb index query qrdb.User.Email --value alice@example.com

# Scan index range [start, end)
qrdb index scan qrdb.User.Status --start active --end inactive

# Drop index
qrdb index drop qrdb.User.Email
```

Every verb and flag is in [secondary-indexes/cli.md](secondary-indexes/cli.md).

## 12. Compound Indexes (Multi-Field)

Compound indexes cover multiple fields in a single index, enabling efficient
queries that filter on two or more fields simultaneously. Without compound
indexes, multi-field queries fall back to single-field index lookups,
intersected or post-filtered in memory.

### Defining Compound Indexes

Entity types declare compound indexes by implementing `CompoundIndexRegisterer`:

```go
type Message struct {
    ID             uint64 `json:"id"`
    ConversationID string `json:"conversationId" index:"true"`
    Timestamp      int64  `json:"timestamp" index:"true"`
    IsRead         bool   `json:"isRead" index:"true"`
    Body           string `json:"body"`
}

func (m Message) EntityID() uint64      { return m.ID }
func (m *Message) SetEntityID(id uint64) { m.ID = id }

func (m Message) CompoundIndexes() []index.CompoundIndexDef {
    return []index.CompoundIndexDef{
        {Fields: []string{"ConversationID", "Timestamp"}},
        {Fields: []string{"ConversationID", "IsRead"}},
    }
}
```

Compound indexes are registered automatically when the DAO is constructed.
The field order matters: the first field is the **leading** field used for
prefix scans.

### Prefix Matching Rules

A compound index `(A, B, C)` can be used for queries that match a
contiguous prefix of its fields:

| Query Pattern | Usable? | Plan Type |
|---|---|---|
| `A = ?` | **No** (a compound plan needs 2+ matched fields) | Falls back to single-field |
| `A = ? AND B = ?` | Yes (2 field prefix) | CompoundIndexLookup |
| `A = ? AND B = ? AND C = ?` | Yes (full match) | CompoundIndexLookup |
| `A = ? AND B > ?` | Yes (prefix + range) | CompoundIndexScan |
| `B = ?` alone | **No** (no leading prefix) | Falls back to single-field |
| `A = ? AND C = ?` (gap) | **No** (only 1 field matched) | Falls back to single-field |

### Query Builder Integration

The query planner automatically selects compound indexes when they cover
2+ predicates. No special syntax is needed:

```go
// Automatically uses (ConversationID, Timestamp) compound index
messages, err := messageDAO.Find(ctx).
    Where("ConversationID = ?", "conv-123").
    Where("Timestamp > ?", 1709000000).
    Execute()
```

### Cost Model

The planner compares compound index cost against single-field indexes; the
costs are in
[secondary-indexes/architecture.md](secondary-indexes/architecture.md#cost-model).
A compound index covering 2+ predicates is typically cheaper than any
single-field index, because it avoids post-filtering.

### Order-Preserving Encoding

Compound index keys use order-preserving encoding so that lexicographic
byte comparison matches logical field ordering:

- **Strings**: Byte-stuffed null-terminated encoding (`\x00` escaped as
  `\x00\xFF`, terminated with `\x00\x00`)
- **Numerics**: the self-delimiting order-preserving numeric term (same as
  single-field numeric indexes; see
  [secondary-indexes/architecture.md](secondary-indexes/architecture.md))
- **Fixed64**: eight caller-encoded order-preserving bytes passed through
  unchanged (SQL primary-key components)

This enables efficient prefix scans: all entries with the same leading
field values sort contiguously in storage.

### Performance Characteristics

- **Write overhead**: Each compound index adds one additional storage write
  per entity Put/Delete, similar to a single-field index.
- **Read performance**: Compound index lookups avoid post-filtering,
  reducing the number of entities loaded from storage.
- **Storage cost**: Each compound index entry stores the concatenated
  encoded field values plus the entity key. Slightly larger than single-
  field entries.

## 13. Limitations and Trade-offs

**Current limitations:**

- No expression indexes (e.g., `LOWER(name)`)
- No partial indexes (e.g., `WHERE status = 'active'`)
- No covering indexes (queries still fetch the entity)

**Trade-offs:**

- **Storage vs. Speed:** More indexes = faster queries but more storage
- **Write vs. Read:** More indexes = slower writes, faster reads
- **Consistency:** Indexes are updated atomically with the entity (no eventual consistency)

## Cross-References

- [Query Planner](16-query-planner.md) -- Automatic index selection
- [MVCC Engine](key-value/engineering.md) -- Underlying key-value versioning
- [Configuration](12-configuration.md) -- `index.max_indexes` setting
- [API Reference](secondary-indexes/api.md) -- Index creation/query endpoints
