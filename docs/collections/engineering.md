# Document Collections — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Collections() transport.CollectionClient
```

`sdk/go/transport`:

```go
type CollectionClient interface {
	CreateCollection(ctx context.Context, req CreateCollectionRequest) (*CollectionInfo, error)
	GetCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error)
	ListCollections(ctx context.Context, database string) (*ListCollectionsResponse, error)
	DropCollection(ctx context.Context, database, name string) error
	InsertDocument(ctx context.Context, req InsertDocumentRequest) (*InsertDocumentResponse, error)
	GetDocument(ctx context.Context, database, collection string, id uint64) (*DocumentResponse, error)
	UpdateDocument(ctx context.Context, req UpdateDocumentRequest) error
	DeleteDocument(ctx context.Context, database, collection string, id uint64) error
	ListDocuments(ctx context.Context, req ListDocumentsRequest) (*ListDocumentsResponse, error)
}
```

### Types

```go
type CollectionInfo struct {
	Name          string            `json:"name"`
	Database      string            `json:"database"`
	IDStrategy    string            `json:"id_strategy"`
	FieldHash     string            `json:"field_hash,omitempty"`
	Schema        map[string]string `json:"schema,omitempty"`
	DocumentCount int64             `json:"document_count"`
	StorageCodec  string            `json:"storage_codec,omitempty"`
	CreatedAt     string            `json:"created_at"`
}

type CreateCollectionRequest struct {
	Database     string `json:"database"`
	Name         string `json:"name"`
	IDStrategy   string `json:"id_strategy,omitempty"`   // unix_nano | increment | field_hash
	FieldHash    string `json:"field_hash,omitempty"`    // required when IDStrategy == field_hash
	StorageCodec string `json:"storage_codec,omitempty"`
}
```

**Index definitions are not on the transport type.** `service.CollectionInfo`
and `service.CreateCollectionRequest` both carry
`Indexes []service.IndexDefinition`, and `CreateCollection` builds a secondary
index per definition — but no transport request, protobuf field or CLI flag
carries them, so only an in-process caller holding the `pkg/service` type can
declare one. That is an exposure gap, not a design decision, and it is tracked
separately; it is recorded here because it bounds who can reach the behaviour
this contract describes.

```go
type IndexDefinition struct { // pkg/service only
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique,omitempty"`
	Sparse    bool   `json:"sparse,omitempty"`
}

type DocumentResponse struct {
	ID       uint64                 `json:"id,string"`
	Document map[string]interface{} `json:"document"`
}

type InsertDocumentRequest struct {
	Database   string
	Collection string
	Document   map[string]interface{}
}
type InsertDocumentResponse struct{ ID uint64 `json:"id,string"` }

type UpdateDocumentRequest struct {
	Database   string
	Collection string
	ID         uint64 `json:"id,string"`
	Document   map[string]interface{}
}

type ListDocumentsRequest struct {
	Database   string
	Collection string
	PageSize   int32  `json:"page_size,omitempty"`
	PageToken  string `json:"page_token,omitempty"`
}
type ListDocumentsResponse struct {
	Documents     []DocumentResponse
	NextPageToken string `json:"next_page_token,omitempty"`
	TotalCount    int64  `json:"total_count"`
}
```

Storage constants, `pkg/service`:

```go
const collectionMetadataPrefix = "__sys:collections/"
const collectionDataPrefix     = "__col:"
```

Metadata is keyed `__sys:collections/{database}/{name}`; documents are keyed
`__col:{database}:{collection}/{id}`. Both are relative to the
`<tenant>/<database>/` namespace the resolved servicer applies — see
[Tenant scoping](#tenant-scoping).

## Invariants

- **Document IDs are `uint64` and serialize as JSON *strings*** (`json:"id,string"`).
  This is not cosmetic: a `uint64` above 2^53 loses precision as a JSON number,
  and JavaScript clients are first-class consumers here.
- **The ID strategy is fixed at creation** and cannot be changed afterwards.
  `field_hash` requires `FieldHash` to be set; the request is refused without it.
- **`field_hash` is deterministic** — re-inserting the same document field value
  yields the same ID, which is what makes re-insertion idempotent rather than
  duplicating. It is the **only** case in which an insert may write over a
  stored document; see the two invariants below.
- **An explicit `_id` reserves the collection's counter.** `InsertDocument`
  honours an `_id` carried in the document when it is a positive number, and on
  an `increment` collection it raises the sequence past that value
  (`SequenceServicer.Advance`) before storing anything, so no subsequently
  generated ID can collide with it. `Advance` is a no-op once the sequence is
  at or past the target, so the steady-state cost is one read and zero Raft
  proposals. Both implementations of `increment` do this — the replicated
  sequence and the process-local `dao.AtomicIncrementGenerator`, which
  satisfies `dao.IDSequence` for exactly this purpose — because a wiring that
  skipped it would reissue IDs the other one had reserved. A reservation that
  fails fails the insert (`CollectionMetadataError{Op: "reserve-id"}`): storing
  the document under an ID the collection is still free to hand out is the
  defect itself.
- **`InsertDocument` refuses to write over a stored document.** The only
  exception is a `field_hash`-**generated** ID, whose collision is the
  documented idempotent re-insert. An explicit `_id` that is already taken is
  refused on every strategy, as is a generated ID on `increment` or
  `unix_nano`. The check is a read, and a read that FAILS fails the insert
  (`CollectionMetadataError{Op: "check-document"}`) rather than being treated
  as absence — reporting a failed read as "nothing there" would wave through
  the overwrite the check exists to prevent.
- **`UpdateDocument` replaces the whole document.** There is no partial update
  or merge. It is also the verb for "replace whatever is stored under this ID";
  `InsertDocument` is not, and will not do it. Its ID comes from the request,
  never from the body.
- **`_id` is consumed into the identity, never stored as a field.** Both
  `InsertDocument` and `UpdateDocument` strip it from the document they write
  (`service.documentFields`), so `GetDocument` returns the document's data and
  its ID beside it, and two documents in one collection have the same shape
  whether their IDs were claimed or allocated. The caller's map is not mutated.
  This is load-bearing for the SQL surface: `updateSchema` records every key it
  is given, and `CollectionInfo.Schema` is what
  `pkg/sql/provider.buildCollectionSchema` builds a collection table's columns
  from, on top of the `_id` it already declares as column 0.
- **The SQL surface carries an explicit `_id` like every other transport.**
  `pkg/sql/provider.collectionWriter.Insert` reads column 0 and passes it to
  `InsertDocument` under `service.DocumentIDField`, so the claim reserves the
  counter and is refused on a collision through the same code path. A zero or
  absent column 0 is not a claim (`claimedDocID`); the `_id` column carries
  `DEFAULT 0` so the statement may omit it. A refused claim is reported as
  `sql.NewUniqueKeyErr("PRIMARY", true, existingRow)` — MySQL 1062, PostgreSQL
  `23505`, `gorm.ErrDuplicatedKey` — because go-mysql-server implements
  `REPLACE INTO` and `INSERT ... ON DUPLICATE KEY UPDATE` on that error and its
  `Existing` row.
- **An `UPDATE` that changes `_id` claims before it releases.**
  `collectionWriter.Update` inserts the document under its new ID and only then
  deletes the old one, so a move onto an occupied ID fails with nothing
  destroyed.
- **`DropCollection` deletes every document, drops every secondary index the
  collection declared, reclaims the collection's replicated `increment`
  counter, and is irreversible.** The order is documents → indexes → sequence →
  metadata, and the metadata key is deleted **last** so the operation is
  retryable at every interruption point — see the architecture note. A
  collection recreated under a dropped name therefore issues ID 1 again; it does
  not inherit the previous collection's high-water mark.
- **A drop that cannot reclaim a step fails, and is retryable.** Every step
  returns `service.CollectionMetadataError` with the failing `Op`
  (`list-documents`, `delete-document`, `drop-index`, `delete-sequence`,
  `delete-metadata`), and the metadata — the record that makes the collection
  resolvable — survives until the end, so re-issuing the same `DropCollection`
  completes it. Each reclamation step is idempotent, so a retry costs nothing
  where it already ran: a step reported as *already absent* is treated as
  success (`index.IndexServicer` permits `IndexNotFoundError` from `DropIndex`,
  and `SequenceServicer.Delete` requires not-found-is-success of
  implementations), so a retry is never wedged short of the metadata delete.
- **Collection metadata that cannot be parsed fails the drop**, with
  `service.CollectionMetadataError{Op: "unmarshal-metadata"}`. The index
  definitions live in that metadata, so a drop that cannot read it does not know
  what the collection owns; proceeding would leave the orphan the ordering rule
  exists to prevent. Every other verb on the service already refuses a
  collection in that state.
- **A collection's secondary indexes are named
  `{database}.{collection}.{field}`** — the same database-scoped hierarchical
  form the rest of the index subsystem uses, produced by one function shared by
  the create and drop paths.
- **`ListDocuments` is paginated** and must be driven to completion by following
  `NextPageToken`. A caller that reads one page and stops has read one page, not
  the collection.
- **Documents live under `__col:` and metadata under `__sys:collections/`**,
  both inside the internal-key guard, so neither appears in user KV
  enumeration.
- **The database is a request field, the tenant is not.** Tenant comes from the
  authenticated identity.
- **The collection servicer is resolved per `(tenant, database)`, never held as
  a process singleton.** Every transport calls
  `service.Registry.CollectionService(tenant, database)` with the database the
  REQUEST names — the URL path or body on REST, the `database` field on
  gRPC/QUIC/Unix, the `database` tool argument on MCP, the connected database on
  the MySQL and PostgreSQL wire protocols. A transport that captured one
  servicer at startup would serve every tenant from the same keyspace; see
  [Tenant scoping](#tenant-scoping).

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Creating a collection that exists | `service.CollectionAlreadyExistsError` | yes |
| Operating on an unknown collection | not-found | **yes** — never auto-created |
| `id_strategy: field_hash` without `field_hash` | invalid-argument | **yes** |
| Unknown `id_strategy` value | invalid-argument | **yes** |
| Unknown document ID on get/update/delete | not-found | yes |
| Insert whose `_id` already names a stored document | `service.CollectionDocumentExistsError{Explicit: true}` — 409 / `AlreadyExists`; over SQL, MySQL 1062 / `SQLSTATE 23505` | **yes** |
| SQL `INSERT` into a collection table whose `_id` is present but unreadable as an ID | `provider.InvalidKeyError`; nothing is stored | **yes** |
| SQL `UPDATE ... SET _id = N` where N is taken | MySQL 1062; **both** documents survive | **yes** |
| SQL insert refused repeatedly while the occupant is never readable | `provider.TransactionConflictRetriesExhaustedError` after `writeMaxAttempts` | **yes** |
| Insert whose GENERATED ID already names a stored document (`increment`, `unix_nano`) | `service.CollectionDocumentExistsError{Explicit: false}` — 409 / `AlreadyExists` | **yes** |
| Re-insert into a `field_hash` collection under the same natural key | success — the document is replaced, which is the strategy's purpose | n/a |
| Counter cannot be reserved for an explicit `_id` | `CollectionMetadataError{Op: "reserve-id"}`; nothing is stored | **yes** |
| Existence check cannot be read on insert | `CollectionMetadataError{Op: "check-document"}`; nothing is stored | **yes** |
| Malformed document JSON | invalid-argument | **yes** |
| Index drop fails during `DropCollection` | `CollectionMetadataError{Op: "drop-index"}`; metadata survives, drop is retryable | **yes** |
| Index already absent during `DropCollection` | success — a teardown asked to remove something that is not there has got what it asked for | n/a |
| Unparseable collection metadata on drop | `CollectionMetadataError{Op: "unmarshal-metadata"}`; nothing is deleted | **yes** |
| Missing `collections:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

Collections are never implicitly created by an insert. An insert into a
misspelled collection name must fail, not silently create a second collection
that then diverges.

## Configuration

No collection-specific configuration keys. `StorageCodec` is a per-collection
override chosen at creation; the available codecs are those compiled in
(`codec_json` is always present; `codec_msgpack`, `codec_yaml`, `codec_bson`,
`codec_protobuf` are build-tag gated — see the Makefile's `WITH_CODEC_*` flags).

## Tenant scoping

A collection key names the database and never the tenant, so a document's
isolation is entirely the isolation of the engines its collection service was
built over. Two pieces make that hold:

1. **`service.Registry` carries a `CollectionService(tenant, db)` accessor**,
   resolving like `KVService` and `MVCCService`. Every transport uses it; none
   holds a process-wide servicer when a registry is configured.
2. **The registry builds the collection service over the WRAPPED engines.** On
   the shared-shard path the resolved bundle's `Collection` is constructed from
   the `<tenant>/<database>/`-namespaced KV and index servicers, not copied from
   the bundle the raw engines were bound into. The resulting physical key is
   `<tenant>/<database>/__col:{database}:{collection}/{id}`.

On a path that already reached a dedicated store — a `raft` tenant's
per-database shard, a `volume` tenant's own Host, a database with its own
barrier over its own store — the bundle's own collection service is returned
unchanged, because that store already belongs to exactly one
`(tenant, database)` pair.

`IDStrategyIncrement` is affected the same way and is worth stating on its own:
the counter key `__seq:{database}:__collection:{name}` is namespaced by the same
wrapper, so each tenant advances its own counter. The resolved servicer is
wired with a `SequenceServicer` whenever the resolved bundle carries both MVCC
and transaction servicers, which is every registry-served path except the
dedicated-store one documented in `buildDedicatedStoreServices`.

**Configuration set on a bundle's own collection service is not inherited by
the composite one** — `WithDatabaseCodecLookup` and
`WithCollectionQuotaChecker` — because the composite is built from the bundle's
ENGINES rather than cloned from its collection service. No composer sets either
today; one that starts to must teach the builder about it.

**Collection document bytes are not counted against `MaxStorageBytes`.** The
storage-byte quota wrapper is applied to the resolved KV servicer after the
collection service has been built over it, so a document write bypasses it. This
is unchanged by the tenant-scoping work — before it, the collection service
bypassed the wrapper entirely — and it is recorded here rather than fixed in
passing, because closing it changes quota accounting semantics. The
`MaxCollections` count quota is enforced and unaffected.

## Persistence

- Metadata: `__sys:collections/{database}/{name}` → `CollectionInfo`.
- Documents: `__col:{database}:{collection}/{id}`, encoded with the collection's
  storage codec.
- Increment counters: `__seq:{database}:__collection:{name}`.
- All three are relative to the resolved servicer's `<tenant>/<database>/`
  namespace.
- **Compatibility**: the storage codec is recorded on the collection, so a
  collection created with one codec keeps reading with it. Changing the default
  codec does not rewrite existing collections.

### Upgrading across the tenant-namespace change

**Collections written before the collection surface was tenant-scoped are not
visible after the upgrade, and nothing moves them.** This is a decision, stated
plainly rather than deferred.

The tenant dimension was never written down. A legacy key is
`__col:{database}:{collection}/{id}` — the database is in it, the tenant is
not — so there is nothing in the store, the Raft log, or the metadata from
which the owning tenant can be READ. Any migration must therefore GUESS a
tenant, and the only available guess ("it all belongs to the system tenant") is
the same guess the defect made. Worse, it would be guessed with a durable write:
a deployment where a non-system tenant had written collections — exactly the
deployment the defect exposed — would have that tenant's documents handed to the
system tenant permanently.

Read-through fallback is worse still and is rejected outright: a servicer that
looks in its namespace and then in the bare keyspace reinstates the shared
keyspace for every tenant, which is the vulnerability itself, written down as a
feature.

So the legacy keys are **left in place, inert**. Nothing deletes them, because
the server cannot attribute data it cannot attribute; nothing reads them,
because every transport's KV surface is namespaced too, so no caller can compose
the bare key. An operator upgrading a deployment with existing collections
exports them with the previous binary and re-imports them per tenant.

**"Nothing deletes them" includes `DROP DATABASE` and `DELETE TENANT`**, and it
is spelled out because leaving it implicit was enough to lose it. A keyspace
reclamation added afterwards read `__col:{db}:` as database-scoped — which it
is — and deleted it on a drop, which made any tenant's drop of a database named
`X` destroy **every** tenant's legacy records for a database named `X`. Nothing
was exposed, because that data is unreadable by design; what was destroyed was
the export above, the only migration this document offers for it. The rule is
not "a drop may not delete collections" — a drop reclaims the namespaced
collection keys a collection writes today, and must. The rule is the sentence
this section already contained: **the server does not delete data it cannot
attribute**, and a key with a database in it and no tenant cannot be attributed
by a call one tenant issues. `service.LegacyCollectionKeyPrefixes` is the one
place that shape is named, and it is passed to no reclamation.

**There is no interrupted-migration state, because nothing moves.** That is the
decisive property, not a convenience: a migration that rewrote `__col:` keys
through Raft would have a half-state, and no ordering makes that half-state
safe — the metadata key is what makes a collection resolvable and the documents
are what it resolves to, so moving the metadata first strands a resolvable
collection with no documents and moving the documents first strands documents no
name reaches. The single atomic step here is the code change itself.

A narrower, correct-by-measurement migration is available if the owner wants
one: in a deployment whose tenant list contains ONLY the system tenant, every
legacy key provably belongs to the system tenant, so moving them under
`system/{database}/` is a fact rather than a guess. It is deliberately not
shipped here — it is a replicated, restartable, leader-only boot-time job with
its own interrupted state (partially moved keyspace, idempotent on retry), which
is a separate capability rather than part of closing the isolation hole.

## Test strategy

- **Unit**: `pkg/service` collection service — create/info/list/drop, document
  CRUD, each ID strategy (including `field_hash` determinism and the missing
  `field_hash` refusal), and pagination token round-trip.
- **Error tests** per function: happy path plus error path, per the repo rule.
- **JSON round-trip test for the `id,string` encoding** — this is exactly the
  kind of detail that regresses silently.
- **Integration**: collection parity across transports in the standalone stage.
- **Cross-tenant isolation**: two tenants under `prefix` isolation, the same
  database name and the same collection name, over ONE shared store — neither
  may read, list, overwrite or deny the other's collection, and every key
  written must carry its own tenant's namespace. `prefix` is covered first
  because it is the exposed mode; `volume` and `raft` bound the blast radius at
  the shard.
- **Cross-tenant teardown**: the same two tenants, plus legacy (tenant-less)
  collection records naming that database. One tenant drops it; the other's
  records — and the legacy pile as a whole, which belongs to neither as far as
  the server can tell — must survive, while the dropping tenant's own
  namespaced collection data is still reclaimed. Both halves are asserted,
  because a teardown that stops deleting everything by also stopping the
  legitimate reclamation is not a fix. The same is asserted one scope up, for
  a tenant delete.
- **Per-transport resolution**: each transport resolves its collection servicer
  from the registry using the database the request names, and does not reach the
  process-wide singleton when a registry is configured. Asserted per transport,
  because the resolution is per transport.
- **Lifecycle symmetry**: what `CreateCollection` creates, `DropCollection`
  destroys — asserted on the index catalog, on the drop ORDER relative to the
  metadata delete, and on the retryability of a failed drop. The engine
  behaviour that makes an orphaned index a correctness problem rather than a
  space leak (index creation is idempotent by name, so the surviving definition
  wins over a divergent re-create) is pinned separately in `pkg/engine`.

## Observability

- Collection create and drop, and document insert/update/delete, are audited
  through [../audit/](../audit/).
- `CollectionInfo.DocumentCount` is the cheap health signal for "did my import
  land"; it is served from metadata rather than by counting.
