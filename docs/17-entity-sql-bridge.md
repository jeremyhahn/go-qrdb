# Entity-to-SQL Bridge

The entity-to-SQL bridge provides seamless interoperability between the DAO (NoSQL) and SQL query interfaces. A Go struct registered via `dao.New()` is automatically visible as a SQL table — same data, same indexes, zero extra configuration.

## Architecture

```
pkg/entity/              ← Standalone registry (stdlib only)
   ^          ^
   |          |
pkg/dao/      pkg/sql/provider/
(registers)   (reads registry, creates EntityTable)
```

- `pkg/entity/` has **zero external dependencies** (only `reflect`, `sync`, `sort`, `time`).
- `pkg/dao/` registers entity types during `dao.New()`.
- `pkg/sql/provider/` reads the registry and creates `EntityTable` instances on demand.

### Layer Separation

| Layer | Interface | Overhead |
|-------|-----------|----------|
| 0 | `kvSvc.Put/Get` | Direct Raft. No entity layer. |
| 1 | `dao.Save/Get` | Codec + KVStore. Registers entity. |
| 2 | `dao.Find().Where()` | Planner + indexes. |
| 3 | SQL `EntityTable` | Reads same data via KVServicer. Same indexes. |
| 4 | SQL `kv` / collections | Raw KV and schemaless collections. Unchanged. |

## Usage

### Define an entity once

```go
type User struct {
    ID    uint64 `json:"id" index:"unique"`
    Name  string `json:"name" index:"true"`
    Email string `json:"email" index:"unique"`
    Age   int64  `json:"age"`
}

func (u *User) EntityID() uint64      { return u.ID }
func (u *User) SetEntityID(id uint64) { u.ID = id }
```

### Use via DAO (NoSQL)

```go
userDAO, _ := dao.New[*User](kvStore, "users", func() *User { return &User{} })

// Save
userDAO.Save(ctx, &User{Name: "Alice", Email: "alice@example.com", Age: 30})

// Query by index
user, _ := userDAO.QueryOneByIndex(ctx, "Email", "alice@example.com")

// Fluent query
users, _ := userDAO.Find(ctx).Where("Email = ?", "alice@example.com").First()
```

### Same data via SQL

After `dao.New()` registers the entity, the `users` table appears automatically in SQL:

```sql
-- Read data written by DAO
SELECT * FROM users WHERE email = 'alice@example.com';
-- Uses the same index registered by the DAO

-- Write data readable by DAO
INSERT INTO users (_id, name, email, age) VALUES (456, 'Bob', 'bob@example.com', 25);
-- Stored in same format, visible to both DAO and SQL
```

## Type Mapping

| Go Type | SQL Type |
|---------|----------|
| `string` | `VARCHAR(65535)` |
| `int64`, `int`, `int32`, `int16`, `int8` | `BIGINT` |
| `uint64`, `uint`, `uint32`, `uint16`, `uint8` | `BIGINT UNSIGNED` |
| `float64`, `float32` | `DOUBLE` |
| `bool` | `BOOLEAN` |
| `[]byte` | `BLOB` |
| `time.Time` | `DATETIME` |
| `map`, `struct` | `JSON` |

The `_id` column is always `BIGINT UNSIGNED PRIMARY KEY NOT NULL`, mapped to `EntityID()`.

## Index Sharing

Indexes declared via struct tags (`index:"true"`, `index:"unique"`) are registered by the DAO at the engine level. The SQL query planner uses these same indexes for WHERE clause acceleration:

```go
type Product struct {
    ID       uint64 `json:"id"`
    Category string `json:"category" index:"true"`
    SKU      string `json:"sku" index:"unique"`
}
```

```sql
-- Uses the "category" index
SELECT * FROM products WHERE category = 'electronics';

-- Uses the "sku" unique index
SELECT * FROM products WHERE sku = 'ABC-123';
```

## Codec Handling

The dual-codec architecture is preserved:

- **DAO layer** (`dao.Codec[E]`): Entity-level serialization selected per-DAO via `dao.WithCodec()`.
- **Service layer** (`codec.Manager`): Storage and wire transcoding, configured per-service.

The `EntityTable` uses closure injection to serialize/deserialize entities with the exact same codec the DAO uses — without importing the codec library directly:

```go
// JSON (default)
userDAO, _ := dao.New[*User](kvStore, "users", newUser)

// MsgPack
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithCodec(dao.CodecMsgPack))

// CBOR (requires -tags codec_cbor)
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithCodec(dao.CodecCBOR))
```

In all cases, `SELECT * FROM users` works identically — the EntityTable calls the same marshal/unmarshal functions the DAO captured at registration time.

### Supported Codecs

All codecs supported by `go-codec` work transparently:

| Codec | Build Tag | Format |
|-------|-----------|--------|
| JSON | (always available) | Human-readable |
| YAML | (always available) | Human-readable |
| TOML | (always available) | Human-readable |
| MsgPack | (always available) | Binary, compact |
| BSON | (always available) | Binary, MongoDB-compatible |
| CBOR | `codec_cbor` | Binary, IoT-friendly |
| Avro | `codec_avro` | Binary, schema-based |
| ProtoBuf | `codec_protobuf` | Binary, schema-based |

## Entity Registry

The entity registry (`pkg/entity/`) is a thread-safe global singleton:

```go
// Accessed by both DAO and SQL provider
registry := entity.Global()

// Registration happens automatically in dao.New()
reg, ok := registry.Get("users")

// List all registered entities
all := registry.List()

// Filter by database
dbEntities := registry.ListForDatabase("myapp")
```

### Registration Fields

Each registration contains:
- `EntityType` — table name (e.g., `"users"`)
- `StructType` — `reflect.Type` for schema derivation
- `Fields` — ordered `FieldMeta` with type info, index flags, PK designation
- `MarshalFunc` / `UnmarshalFunc` — closures capturing the DAO codec
- `NewFunc` — factory for creating zero-value entities
- `Database` — optional database scope
- `CodecType` — codec identifier string

## Database Scoping

Entities can be scoped to a specific database:

```go
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithDatabase("myapp"))
```

The entity appears as the `users` table in the `myapp` database. Without `WithDatabase`, entities appear in all databases (legacy mode).
