# Entity-to-SQL Bridge

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-26
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
| `map`, `struct`, slices other than `[]byte` | `JSON` |
| any other kind | `VARCHAR(65535)` |

Pointer types map as the type they point to (`entity.GoTypeToSQLType`, `pkg/entity/type_mapping.go`). The `_id` column is always `BIGINT UNSIGNED PRIMARY KEY NOT NULL`, mapped to `EntityID()`; the Go field that holds the ID is not repeated as a column. The remaining fields follow `_id` as nullable columns named by their JSON names, in alphabetical order (`buildEntitySchema`). A field that holds a `dao.SecretBytes` is never a column, and the DAO refuses an index on one.

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

Two codecs are in play:

- **DAO layer** (`dao.Codec[E]`): entity serialization, selected per DAO via `dao.WithCodec()`.
- **Service layer** (`codec.Manager`, `pkg/codec`): storage and wire transcoding, configured per service.

`dao.New()` stores `MarshalFunc` and `UnmarshalFunc` closures over its codec in the registration, so the `EntityTable` encodes and decodes with the DAO's codec without importing a codec library:

```go
// JSON (default)
userDAO, _ := dao.New[*User](kvStore, "users", newUser)

// MsgPack
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithCodec(dao.CodecMsgPack))

// CBOR (requires -tags codec_cbor)
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithCodec(dao.CodecCBOR))
```

In all cases `SELECT * FROM users` returns the same rows: the `EntityTable` calls the marshal and unmarshal closures the DAO stored at registration.

### Supported Codecs

Every `go-codec` codec is compiled in only under its build tag; `make -s print-build-tags` prints the default set.

| Codec | `dao` constant | Build tag | In the default build | Format |
|-------|----------------|-----------|----------------------|--------|
| JSON | `CodecJSON` | `codec_json` | yes | Human-readable |
| YAML | `CodecYAML` | `codec_yaml` | yes | Human-readable |
| TOML | `CodecTOML` | `codec_toml` | no | Human-readable |
| MsgPack | `CodecMsgPack` | `codec_msgpack` | yes | Binary, compact |
| BSON | `CodecBSON` | `codec_bson` | yes | Binary, MongoDB-compatible |
| CBOR | `CodecCBOR` | `codec_cbor` | no | Binary, IoT-friendly |
| ProtoBuf | `CodecProtoBuf` | `codec_protobuf` | yes | Binary, schema-based |

`dao.New()` returns a `CodecCreationError` for a codec not compiled in, and for `CodecProtoBuf`, which needs a `proto.Message` entity built with `dao.NewProtoBufCodec[E]()` and so has no SQL bridge.

### Secret fields

**Store a secret key in an entity.** Declare the field as `dao.SecretBytes`
and build the DAO with the MessagePack codec. The key stays in locked,
protected memory, and you destroy it when you are done with it.

```go
type Credential struct {
	ID    uint64          `msgpack:"id"`
	Label string          `msgpack:"label"`
	Key   dao.SecretBytes `msgpack:"key"`
}

credDAO, err := dao.New[*Credential](store, "creds", newCredential,
	dao.WithCodec(dao.CodecMsgPack))

key, err := dao.NewSecretBytes(raw) // raw is wiped
err = credDAO.Save(ctx, &Credential{Label: "signing", Key: key})
key.Destroy()

got, err := credDAO.Get(ctx, id)
defer got.Key.Destroy()
err = got.Key.WithBytes(func(k []byte) error { return sign(k) })
```

Only MessagePack can carry the field. Every other codec, including the default
JSON, refuses the entity type when the DAO is built. You also cannot index the
field or query it through SQL. At rest the record is encrypted by the
database's barrier, the same as any other value.

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
- `EntityType` — table name (e.g., `"users"`); the registry key
- `StructType` — `reflect.Type` for schema derivation
- `Fields` — ordered `FieldMeta` with type info, index flags, PK designation
- `IDFieldJSON` / `IDFieldGo` — JSON and Go names of the primary-key field
- `MarshalFunc` / `UnmarshalFunc` — closures capturing the DAO codec
- `NewFunc` — factory for creating zero-value entities
- `Database` — optional database scope
- `CodecType` — codec identifier string

`Register` is idempotent for the same `EntityType` and struct type and returns `AlreadyRegisteredError` for the same `EntityType` with a different struct type; `dao.New()` ignores that error, so the first registration of a name wins.

## Database Scoping

A DAO can record a database scope:

```go
userDAO, _ := dao.New[*User](kvStore, "users", newUser, dao.WithDatabase("myapp"))
```

`dao.WithDatabase` qualifies the DAO's index names (`myapp.users.Email`) and sets the registration's `Database` field. Because the registry is keyed by entity type name alone, one process holds one registration per name whatever its database, and the scope affects listing only:

- `GetTableNames` lists an entity in the database it is scoped to. An entity registered without a database is listed in every database.
- `GetTableInsensitive` looks the lower-cased table name up in the registry without checking the scope, so a scoped entity resolves as a table from any database, including those that do not list it. A built-in table of the same name wins over the entity, and the entity wins over a relational table of the same name.
- The lookup lower-cases the name and the registry key is exact, so an entity type registered with upper-case letters is listed but does not resolve.
