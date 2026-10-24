# Entity-to-SQL Bridge

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

The entity-to-SQL bridge exposes a Go struct stored through the DAO as a SQL table over the same data and the same indexes. `dao.New()` registers the entity type in the process-wide entity registry, and the SQL provider serving the same process builds an `EntityTable` from that registration.

## Architecture

```
pkg/entity/              ← Standalone registry (stdlib only)
   ^          ^
   |          |
pkg/dao/      pkg/sql/provider/
(registers)   (reads registry, creates EntityTable)
```

- `pkg/entity/` imports only the standard library (`reflect`, `sort`, `sync`, `time`).
- `pkg/dao/` registers the entity type at the end of `dao.New()`, through `entity.Global().Register`. The registration error is discarded: registering the same name with the same struct type is a no-op, and registering it with a different struct type leaves the first registration in place.
- `pkg/sql/provider/` reads the registry in `Database.GetTableInsensitive` and `Database.GetTableNames` and creates an `EntityTable` on each lookup.

The registry is `entity.Global()`, a variable of the process. An entity becomes a SQL table only for the SQL provider running in the process that called `dao.New()`, such as an embedded node; a DAO built in a client process registers nothing on a remote server. The `qrdbd` daemon registers one entity type itself, `bridge_users` (`cmd/qrdbd/test_entities.go`), for its end-to-end tests.

### Layers

| Layer | Interface | Path |
|-------|-----------|------|
| 0 | KV service `Put`/`Get` | Raft-backed KV. No entity layer. |
| 1 | `dao.Save`/`dao.Get` | Codec + KVStore. |
| 2 | `dao.Find().Where()` | Query planner + indexes. |
| 3 | SQL `EntityTable` | Same data through the KV service. Same indexes. |
| 4 | SQL `kv` / collections | Raw KV and schemaless collections. |

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

Once `dao.New()` has registered the entity, the `users` table is visible to SQL in the same process:

```sql
-- Read data written by DAO
SELECT * FROM users WHERE email = 'alice@example.com';
-- Uses the same index registered by the DAO

-- Write data readable by DAO
INSERT INTO users (_id, name, email, age) VALUES (456, 'Bob', 'bob@example.com', 25);
-- Stored in same format, visible to both DAO and SQL
```

## Schema and Type Mapping

| Go Type | SQL Type |
|---------|----------|
| `string` | `VARCHAR(65535)` |
| `int64`, `int`, `int32`, `int16`, `int8` | `BIGINT` |
| `uint64`, `uint`, `uint32`, `uint16`, `uint8` | `BIGINT UNSIGNED` |
| `float64`, `float32` | `DOUBLE` |
| `bool` | `BOOLEAN` |
| `[]byte` | `BLOB` |
| `time.Time` | `DATETIME` |
| other slices, `map`, `struct` | `JSON` |

The `_id` column is always `BIGINT UNSIGNED PRIMARY KEY NOT NULL`, mapped to `EntityID()`. The primary-key field itself is not a separate column; the remaining fields follow `_id` as nullable columns named by their JSON names, in alphabetical order (`buildEntitySchema`). A field that holds a `dao.SecretBytes` is never a column, and the DAO refuses an index on one.

## Index Sharing

Indexes declared via struct tags (`index:"true"`, `index:"unique"`) are registered by the DAO at the engine level. `EntityTable.GetIndexes` reports the same indexes to the SQL planner from the registration's `Indexed` and `Unique` flags:

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

Two codec layers apply:

- **DAO layer** (`dao.Codec[E]`): entity-level serialization selected per DAO via `dao.WithCodec()`.
- **Service layer** (`codec.Manager`): storage and wire transcoding, configured per service.

The registration carries `MarshalFunc` and `UnmarshalFunc` closures over the DAO's codec, so `EntityTable` encodes and decodes entities with the codec the DAO uses without importing a codec package:

```go
// JSON (default)
userDAO, _ := dao.New[*User](kvStore, "users", newUser)

// MsgPack
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithCodec(dao.CodecMsgPack))

// CBOR (requires -tags codec_cbor)
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithCodec(dao.CodecCBOR))
```

In each case `SELECT * FROM users` decodes rows with the marshal and unmarshal functions the DAO captured at registration.

### Supported Codecs

`dao.NewCodec` accepts a codec only when `go-codec` reports it compiled in, and each `go-codec` codec is behind its own build tag. `make print-build-tags` prints the default set.

| Codec | Build Tag | In default `BUILD_TAGS` | Format |
|-------|-----------|-------------------------|--------|
| JSON | `codec_json` | yes | Human-readable |
| YAML | `codec_yaml` | yes | Human-readable |
| TOML | `codec_toml` | no | Human-readable |
| MsgPack | `codec_msgpack` | yes | Binary |
| BSON | `codec_bson` | yes | Binary, MongoDB-compatible |
| CBOR | `codec_cbor` | no | Binary |
| ProtoBuf | `codec_protobuf` | yes | Binary, schema-based |

`dao.New` refuses `dao.CodecProtoBuf` with a `CodecCreationError`: a ProtoBuf codec comes from `dao.NewProtoBufCodec[E]()`, which `dao.New` does not take, so a ProtoBuf entity has no SQL table.

## Entity Registry

The entity registry (`pkg/entity/`) is a process-wide, thread-safe singleton keyed by entity type name:

```go
// Accessed by both DAO and SQL provider
registry := entity.Global()

// dao.New() registers the entity type
reg, ok := registry.Get("users")

// List all registered entities
all := registry.List()

// Filter by database
dbEntities := registry.ListForDatabase("myapp")
```

### Registration Fields

Each `entity.Registration` contains:
- `EntityType` — table name (e.g., `"users"`)
- `StructType` — `reflect.Type` for schema derivation
- `Fields` — ordered `FieldMeta` with type info, index flags, PK designation
- `IDFieldJSON` / `IDFieldGo` — JSON and Go names of the primary-key field
- `MarshalFunc` / `UnmarshalFunc` — closures capturing the DAO codec
- `NewFunc` — factory for creating zero-value entities
- `Database` — optional database scope
- `CodecType` — codec identifier string

## Database Scoping

A DAO can record a database scope:

```go
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithDatabase("myapp"))
```

`dao.WithDatabase` qualifies the DAO's index names (`myapp.users.Email`) and sets the registration's `Database` field. Because the registry is keyed by entity type name alone, one process holds one registration per name whatever its database, and the scope affects listing only:

- `GetTableNames` lists an entity in the database it is scoped to. An entity registered without a database is listed in every database.
- `GetTableInsensitive` looks the lower-cased table name up in the registry without checking the scope, so a scoped entity resolves as a table from any database, including those that do not list it. A built-in table of the same name wins over the entity, and the entity wins over a relational table of the same name.
- The lookup lower-cases the name and the registry key is exact, so an entity type registered with upper-case letters is listed but does not resolve.
