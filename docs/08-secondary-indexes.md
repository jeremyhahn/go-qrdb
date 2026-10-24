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

QRDB supports two index types, `index.IndexTypeNormal` and `index.IndexTypeUnique`; sparse is a flag on either. `IndexService.CreateIndex(ctx, name, fieldName, config)` takes the index name and field as arguments and the rest as a `service.IndexConfig`.

### Unique Index

A unique index enforces that no two entities have the same value for the indexed field.

```go
// Enforce unique email addresses
err := indexService.CreateIndex(ctx, "EmailIndex", "Email", service.IndexConfig{
    Type:          index.IndexTypeUnique,
    Unique:        true,
    CaseSensitive: true,
})
// Now: PUT {ID: user2, Email: alice@example.com} fails if alice@ already exists
```

**Characteristics:**
- Insertion fails (conflict) if the value already exists
- Lookup returns exactly one result (O(1))
- Useful for natural keys (email, username, SSN)

### Non-Unique (Normal) Index

A normal index allows multiple entities to have the same value.

```go
// Index user status (many users can be "active")
err := indexService.CreateIndex(ctx, "StatusIndex", "Status", service.IndexConfig{
    Type:          index.IndexTypeNormal,
    CaseSensitive: true,
})
// Now: Can have many users with Status = "active"
```

**Characteristics:**
- Insertion succeeds even if the value exists
- Lookup returns all matching entities (O(k))
- Useful for categorical fields (status, role, type)

### Sparse Index

A sparse index excludes null/missing values:

```go
// Index only users with an "Department" field set
err := indexService.CreateIndex(ctx, "DeptIndex", "Department", service.IndexConfig{
    Type:          index.IndexTypeNormal,
    Sparse:        true, // Null/missing values NOT indexed
    CaseSensitive: true,
})
// Now: Query Department index only sees non-null entries
```

**Characteristics:**
- Null/missing values are omitted
- Reduces index size
- Queries for null values must still scan the full dataset
- Useful for optional fields

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

## 4. Collection Index Definitions

`CollectionService.CreateCollection` takes the collection's indexes inline as `service.IndexDefinition` values; each is named `{database}.{collection}.{field}`:

```go
err := collectionService.CreateCollection(ctx, &service.CreateCollectionRequest{
    Name:     "users",
    Database: "qrdb",
    Indexes: []service.IndexDefinition{
        {FieldName: "Email", Unique: true},
        {FieldName: "Status"},
        {FieldName: "Department", Sparse: true},
    },
})
```

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
queryable through `Find().Where("Field = ?", v)` with automatic index
selection — see [16-query-planner.md](16-query-planner.md).

## 6. Storage

Both engines build every index as an `index.StoragePebbleIndex` (`index.NewStoragePebbleIndex`) over the store's own `storage.Storage`, so an index lives on whichever backend the store uses (memory, file or PebbleDB) and supports ordered range scans. There is no per-index backend option.

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

### Creation

```go
err := indexService.CreateIndex(ctx, "EmailIndex", "Email", service.IndexConfig{
    Type:   index.IndexTypeUnique,
    Unique: true,
})
```

### Lookup

```go
// Exact match: returns the keys of every entity with Email = "alice@example.com"
keys, err := indexService.Query(ctx, "EmailIndex", []byte("alice@example.com"))
```

### Range Scan

```go
// Returns indexed value -> entity keys for values in ["active", "inactive")
byValue, err := indexService.Scan(ctx, "StatusIndex", []byte("active"), []byte("inactive"))
```

### Listing

```go
names, err := indexService.ListIndexes(ctx) // []string
```

### Deletion

```go
err := indexService.DropIndex(ctx, "EmailIndex")
// Queries on this field revert to full scan
```

`DropOwnedIndex(ctx, name, owner)` drops an index only when `owner` matches its `IndexConfig.Owner`.

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


Every write to an indexed field updates the index. With multiple indexes:

```
PUT {ID: user1, Email: alice@, Status: active, Department: eng}
  1. Write to KV store (user:1)
  2. Update Email index (alice@ → user1)
  3. Update Status index (active → user1)
  4. Update Department index (eng → user1)
  = 4 writes instead of 1
```

Use sparse indexes and selective indexing to minimize write amplification.

## 11. Configuration

### Configuration File

```yaml
index:
  max_indexes: 64              # Maximum number of indexes
```

### CLI

```bash
# Create index
qrdb index create EmailIndex --field Email --unique   # also --sparse, --numeric

# List indexes
qrdb index list

# Query index
qrdb index query EmailIndex --value alice@example.com

# Scan index range (start inclusive, end exclusive)
qrdb index scan StatusIndex --start active --end inactive

# Drop index
qrdb index drop EmailIndex
```

## 12. Compound Indexes (Multi-Field)

Compound indexes cover multiple fields in a single index, enabling efficient
queries that filter on two or more fields simultaneously. Without compound
indexes, multi-field queries fall back to single-index lookup + in-memory
post-filtering.

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

The planner compares compound index cost against single-field indexes:

| Plan | Base Cost | With N Fields Matched |
|---|---|---|
| CompoundIndexLookup (non-unique) | 3 | 3 / N |
| CompoundIndexLookup (unique) | 1 | 1 |
| CompoundIndexScan | 20 | 20 / N |
| Single-field IndexLookup | 10 | N/A |

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

- **Memory vs. Speed:** More indexes = faster queries but more memory
- **Write vs. Read:** More indexes = slower writes, faster reads
- **Consistency:** Indexes are updated atomically with the entity (no eventual consistency)

## Cross-References

- [Query Planner](16-query-planner.md) -- Automatic index selection
- [MVCC Engine](key-value/engineering.md) -- Underlying key-value versioning
- [Configuration](12-configuration.md) -- `index.max_indexes` setting
- [API Reference](api-server/api.md) -- Index creation/query endpoints
