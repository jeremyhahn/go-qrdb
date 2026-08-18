# Secondary Indexes

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

QRDB supports two index types, configured via flags during creation:

### Unique Index

A unique index enforces that no two entities have the same value for the indexed field.

```go
// Enforce unique email addresses
createIndex := &service.IndexConfig{
    Name:      "EmailIndex",
    Field:     "Email",
    Unique:    true,  // No duplicates allowed
    Sparse:    false, // Null values indexed
}

err := indexService.CreateIndex(ctx, createIndex)
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
createIndex := &service.IndexConfig{
    Name:      "StatusIndex",
    Field:     "Status",
    Unique:    false, // Multiple entities allowed
    Sparse:    false, // Null values indexed
}

err := indexService.CreateIndex(ctx, createIndex)
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
createIndex := &service.IndexConfig{
    Name:      "DeptIndex",
    Field:     "Department",
    Unique:    false,
    Sparse:    true,  // Null/missing values NOT indexed
}

err := indexService.CreateIndex(ctx, createIndex)
// Now: Query Department index only sees non-null entries
```

**Characteristics:**
- Null/missing values are omitted
- Reduces index size
- Queries for null values must still scan the full dataset
- Useful for optional fields

## 3. Database-Scoped Index Names

Index names are scoped to a database and entity type, following a hierarchical naming convention:

```
{database}.{entityType}.{FieldName}
```

**Examples:**

```
qrdb.User.Email       # Email index on User entities in "qrdb" database
qrdb.User.Status      # Status index on User entities in "qrdb" database
qrdb.Order.Total      # Total index on Order entities in "qrdb" database
myapp.User.Email      # Email index in "myapp" database (different from qrdb.User.Email)
```

**Benefits:**

- Prevents name collisions across entity types
- Enables the query planner to route queries to the correct index
- Supports multi-database deployments

**Creation example:**

```go
createIndex := &service.IndexConfig{
    Name:   "qrdb.User.Email",  // Scoped name
    Field:  "Email",
    Unique: true,
}
err := indexService.CreateIndex(ctx, createIndex)
```

## 4. Collection Index Definitions

When creating a collection via the DAO or API, you can define indexes inline:

```go
// Create collection with indexes
collection := &service.CollectionConfig{
    Name:   "users",
    Entity: User{},
    Indexes: []*service.IndexConfig{
        {
            Name:   "users.User.Email",
            Field:  "Email",
            Unique: true,
        },
        {
            Name:   "users.User.Status",
            Field:  "Status",
            Unique: false,
            Sparse: false,
        },
        {
            Name:   "users.User.Department",
            Field:  "Department",
            Unique: false,
            Sparse: true,
        },
    },
}

err := collectionService.Create(ctx, collection)
// All three indexes are created atomically
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
`index:"true"`                      // Non-unique index
`index:"unique"`                    // Unique index
`index:"case_insensitive"`          // Case-insensitive (strings)
`index:"unique,case_insensitive"`   // Combine options (comma-separated)
```

The canonical tag key is `index`. Parsing lives in
`pkg/index/metadata.go` (`ParseIndexTags`). Once tagged, fields are
queryable through `Find().Where("Field = ?", v)` with automatic index
selection — see [16-query-planner.md](16-query-planner.md).

The DAO layer reads these tags at collection creation and automatically creates the declared indexes.

## 6. Storage Backends

Indexes are stored in separate key-value namespaces, allowing different backend implementations per index:

### PebbleDB Index

The default backend for production. Indexes are stored in PebbleDB, enabling fast ordered scans and range queries.

```
Index storage:
email:alice@example.com → [user1, user3]
email:bob@example.com   → [user2]
```

**Characteristics:**
- Persistent to disk
- Ordered key-value store
- Supports range scans
- Suitable for large indexes

### Memory Index

For testing or non-persistent indexes, the memory backend stores indexes in-memory in a sync.Map:

```go
// Create in-memory index
createIndex := &service.IndexConfig{
    Name:    "SessionIndex",
    Field:   "SessionID",
    Unique:  true,
    Backend: "memory",  // In-memory
}

err := indexService.CreateIndex(ctx, createIndex)
// Index is fast but lost on restart
```

**Characteristics:**
- Fast for small datasets
- Lost on restart
- No persistence
- Useful for ephemeral data (sessions, caches)

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
createIndex := &service.IndexConfig{
    Name:   "qrdb.User.Email",
    Field:  "Email",
    Unique: true,
}

err := indexService.CreateIndex(ctx, createIndex)
if err != nil {
    // Handle error (index exists, field not found, etc.)
}
```

### Lookup

```go
// Exact match
results, err := indexService.Query(ctx, &service.IndexQueryRequest{
    IndexName: "qrdb.User.Email",
    Value:     "alice@example.com",
})
// Returns all entities with Email = "alice@example.com"
```

### Range Scan

```go
// Range query
results, err := indexService.Scan(ctx, &service.IndexScanRequest{
    IndexName: "qrdb.User.Status",
    Start:     "active",
    End:       "inactive",
})
// Returns all entities with Status in ["active", "inactive"]
```

### Listing

```go
// List all indexes
indexes, err := indexService.List(ctx, &service.IndexListRequest{
    Database: "qrdb",
})

for _, idx := range indexes {
    fmt.Printf("%s: %s (unique=%v, sparse=%v)\n",
        idx.Name, idx.Field, idx.Unique, idx.Sparse)
}
```

### Deletion

```go
// Drop index
err := indexService.Delete(ctx, "qrdb.User.Email")
if err != nil {
    // Handle error (not found, in use, etc.)
}
// Index is removed; queries on this field revert to full scan
```

### Rebuild

```go
// Rebuild index (compaction, deduplication)
err := indexService.Rebuild(ctx, "qrdb.User.Email")
// Index is rebuilt from entity data; old entries cleaned up
```

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

## 10. Performance Characteristics

### Lookup Time

| Index Type | Cardinality | Time |
|-----------|------------|------|
| Unique index | 1M entries | <1 ms |
| Non-unique (100 matches) | 1M entries | 1–5 ms |
| Index range scan (1000 matches) | 1M entries | 5–20 ms |
| Full scan (1000 matches) | 1M entries | 100–500 ms |

### Storage Overhead

```
Original data: 1M users × 500 bytes = 500 MB

Index storage (Email, unique):
  ~1M entries × 64 bytes (hash + key + value) = 64 MB

Index storage (Status, non-unique):
  ~1M entries × 64 bytes = 64 MB

Total overhead: ~128 MB for two indexes (25% of original)
```

### Write Amplification

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
  # per-index configuration not yet exposed; use API
```

### CLI

```bash
# Create index
qrdb index create EmailIndex --field Email --unique

# List indexes
qrdb index list

# Query index
qrdb index query EmailIndex alice@example.com

# Scan index range
qrdb index scan StatusIndex active inactive

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
| `A = ?` | Yes (1 field prefix) | CompoundIndexScan |
| `A = ? AND B = ?` | Yes (2 field prefix) | CompoundIndexLookup |
| `A = ? AND B = ? AND C = ?` | Yes (full match) | CompoundIndexLookup |
| `A = ? AND B > ?` | Yes (prefix + range) | CompoundIndexScan |
| `B = ?` alone | **No** (no leading prefix) | Falls back to single-field |
| `A = ? AND C = ?` (gap) | Partial: uses `A=?` prefix only, `C=?` is post-filter | CompoundIndexScan |

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
- **Numerics**: 8-byte big-endian with sign-bit flip (same as single-field
  numeric indexes)

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
