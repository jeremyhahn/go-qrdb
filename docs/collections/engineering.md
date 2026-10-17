# Document Collections — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25
> Conflicts with this document are defects. Fix them here first.

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
	Name          string                      `json:"name"`
	Database      string                      `json:"database"`
	IDStrategy    string                      `json:"id_strategy"`
	FieldHash     string                      `json:"field_hash,omitempty"`
	Schema        map[string]string           `json:"schema,omitempty"`
	Indexes       []CollectionIndexDefinition `json:"indexes,omitempty"`
	DocumentCount int64                       `json:"document_count"`
	StorageCodec  string                      `json:"storage_codec,omitempty"`
	CreatedAt     string                      `json:"created_at"`
	State         string                      `json:"state,omitempty"`       // active | creating | dropping
	StateSince    string                      `json:"state_since,omitempty"` // RFC 3339
}

type CreateCollectionRequest struct {
	Database     string                      `json:"database"`
	Name         string                      `json:"name"`
	IDStrategy   string                      `json:"id_strategy,omitempty"`   // unix_nano | increment | field_hash
	FieldHash    string                      `json:"field_hash,omitempty"`    // required when IDStrategy == field_hash
	StorageCodec string                      `json:"storage_codec,omitempty"`
	Indexes      []CollectionIndexDefinition `json:"indexes,omitempty"`
}

type CollectionIndexDefinition struct {
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique,omitempty"`
	Sparse    bool   `json:"sparse,omitempty"`
}

const (
	CollectionStateActive   = "active"
	CollectionStateCreating = "creating"
	CollectionStateDropping = "dropping"
)
```

The index definitions and the lifecycle state are carried on every transport:
the protobuf messages (`CollectionIndexDefinition`,
`CreateCollectionRequest.indexes` and `.storage_codec`,
`CollectionInfoResponse.indexes`, `.storage_codec`, `.state`, `.state_since`)
for gRPC, Unix and QUIC; the JSON bodies for REST; the `indexes` and
`storage_codec` arguments of `qrdb_collection_create` for MCP, `indexes` as a
JSON-encoded array like every other array-valued MCP argument; the service
types directly for the embedded transport. The protobuf conversion lives once
per side (`pkg/api/internal/collectionpb` on the servers,
`sdk/go/transport/internal/collectionconv` in the SDK), so a field added to a
collection reaches every protobuf transport or none of them.

The service-layer counterparts are `service.CollectionInfo`,
`service.CreateCollectionRequest` and `service.IndexDefinition`, with
`CreatedAt` and `StateSince` as `time.Time` and one more field the transports
do not carry: `IndexOwner`, the owner recorded on the collection's indexes.

`CollectionServicer` also carries `UpsertDocument` — the guarded
insert-or-replace the SQL surface builds `REPLACE INTO`, `ON DUPLICATE KEY
UPDATE` and `UPDATE` on — and `service.CollectionSweeper` is the interface the
server's sweeper drives (`SweepStuckCollections`). Neither is a transport verb.

```go
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
`__col:{database}:{collection}#{incarnation}/{id}` (`CollectionInfo.documentPrefix`),
and an `increment` collection's counter is the sequence
`__collection:{collection}#{incarnation}` (`collectionSequenceName`). Metadata
written before incarnations existed has none, and keeps the unsuffixed
`__col:{database}:{collection}/{id}` and `__collection:{collection}` its data
already has — no migration. All of them are relative to the
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
  destroyed. The release is guarded by the revision the statement read. If the
  release is refused because the old document changed or is gone
  (`CollectionDocumentConflictError`, `CollectionDocumentNotFoundError`), the
  new copy is withdrawn — guarded by the revision the claim wrote — and the
  statement fails with MySQL 1213; a withdrawal that fails is joined into that
  error. If the release fails any other way its outcome is unknown, so both
  documents are left and the statement fails with
  `provider.DocumentMoveOutcomeUnknownError`, which matches
  `service.ErrOutcomeUnknown`.
- **Every SQL write on a collection table that is computed from a row is
  guarded by that row.** `collectionWriter.Update` — `UPDATE` and the update
  half of `ON DUPLICATE KEY UPDATE` — reads the stored document back, compares
  it with the row go-mysql-server handed it, and writes through
  `UpsertDocument` with `IfRevision` set to the revision it read. A lost race
  is `service.CollectionDocumentConflictError`, reported as
  `sql.ErrLockDeadlock` — MySQL 1213 / `SQLSTATE 40001`.
- **`REPLACE INTO` is one guarded write per row.** The replacer's `Insert` calls
  `UpsertDocument`; when it replaced a document it reports the primary-key
  violation carrying the displaced document (two affected rows) and the delete
  and re-insert go-mysql-server issues next for that row are acknowledged
  without writing. A row with no `_id` is an ordinary insert.
- **`UpsertDocument` decides and writes in one guarded write.** An absent
  document is stored with `PutIfAbsent`, a present one replaced with
  `CompareAndSwap` against the bytes read; a guard lost to a concurrent writer
  is re-decided, up to a bound, then refused with
  `CollectionDocumentConflictError`. `Previous` is the document the write
  displaced. With `IfRevision` it replaces only that revision and never
  inserts. The ID is a claim: it reserves an `increment` counter.
- **Only an `active` collection serves documents.** Every document verb —
  `InsertDocument`, `UpdateDocument`, `UpsertDocument`, `DeleteDocument`,
  `GetDocument` and `ListDocuments` — refuses a `creating` or `dropping`
  collection with `service.CollectionNotActiveError`. `GetCollectionInfo` and
  `ListCollections` report every state.
- **A `field_hash` re-insert is a guarded upsert.** An insert whose generated
  ID may overwrite (`generatedIDMayOverwrite`) writes through the same guarded
  decide-and-write as `UpsertDocument`, and `InsertDocumentResponse` reports
  `Replaced` and the exact `Previous` document it displaced (in-process
  fields), which is what the quota wrapper releases. Like every insert it is
  synchronous; `WriteMode` is not honoured.
- **`CreateCollection` builds the indexes before the collection is active.**
  Metadata is written `creating` with `PutIfAbsent` — the claim on the name —
  the indexes are built, and a guarded swap makes it `active`. A create whose
  indexes cannot all be built is unwound through the drop's teardown and
  returns `CollectionMetadataError{Op: "create-index"}` wrapping the cause; a
  failed unwind is joined to it and left to the sweeper. A create whose
  activation swap loses — the collection was marked `dropping` meanwhile —
  removes the indexes it built and returns
  `service.CollectionStateConflictError{Op: "activate"}`.
- **Index definitions are refused before anything is stored** when a
  definition names no field, when two name the same field, or when the service
  has no index service to build them with
  (`service.CollectionInvalidIndexError`). Definitions used to be
  recorded and silently not built on the second path.
- **Every per-incarnation piece of state is in an incarnation-scoped
  keyspace.** `CollectionInfo.Incarnation` is a random identifier drawn by every
  create. The document prefix, the `increment` sequence name, the process-local
  ID generator's cache key (`collectionIDGenKey`) and every index's owner and
  `KeyPrefix` are derived from it; only the metadata key is shared by the
  incarnations of a name. A write, create or teardown working on a dropped
  incarnation therefore reaches only that incarnation's keys, whatever has
  happened to the name since. There is no purge on create and no guard in the
  teardown against another incarnation: neither has anything to protect.
- **A document write is fenced against a concurrent drop.** After the write
  commits, the metadata is re-read; a collection no longer `active`, no longer
  present, or no longer the incarnation the write was decided from refuses the
  caller (`CollectionNotActiveError`, `CollectionNotFoundError` or
  `CollectionStateConflictError{Op: "write"}`, 409 / `ABORTED`) and retracts the
  write from its own incarnation's keyspace with a compare-and-delete of the
  bytes written, together with that incarnation's `increment` sequence and
  cached generator, which a stale allocation may have recreated. A retraction
  that finds the write already removed or replaced by somebody else — the
  teardown, which counted it — returns the refusal wrapped in
  `service.CollectionWriteSupersededError`, so the quota wrapper keeps the
  write's reservation instead of releasing it twice. A re-read that fails for
  any other reason than absence is reported (`CollectionMetadataError{Op:
  "fence"}`) and the write stands: retracting on an unknown answer would undo a
  write into a live collection. `CollectionMetadataError.OutcomeUnknown` is true
  for `fence`, `retract-write` and the write operations themselves; transports
  report those as internal errors ahead of any lifecycle refusal they wrap, and
  the quota wrapper keeps their reservation. A failed sequence reclamation is
  `CollectionMetadataError{Op: "retract-sequence"}` wrapping the refusal; the
  write itself was retracted, so its outcome is known.
- **A teardown reclaims one incarnation's keyspace and reports what it freed.**
  `DropCollection`, the sweeper and a failed create's unwind run the same
  teardown over a `dropping` record: `Scan` of the incarnation's document
  prefix, then a `CompareAndDelete` of each scanned value in scan order — no
  sort, no metadata re-read — checking its context before every delete, re-scanned
  up to four passes if documents changed under it
  (`CollectionDocumentsChangingError` inside `delete-document` after that). The
  compare is how it counts: it sums `DocumentBodyBytes` of exactly the
  documents its own deletes removed, and returns that sum even when a later
  step fails. Then the indexes, the ledger entry, the incarnation's sequence,
  and last the metadata with `CompareAndDelete` against the exact record, which
  is the one step that can find the name taken over
  (`CollectionStateConflictError{Op: "teardown"}`, which the sweeper treats as
  done). `CollectionService.DropCollectionMetered`
  (`service.CollectionDropMeter`) returns the freed bytes.
- **The index ledger.** A create with indexes writes
  `__sys:collection-index-ledger/{database}/{incarnation}` — owner, index
  names, time — before building the first index; the teardown deletes it after
  dropping the indexes. The sweeper drops the indexes of a stale entry whose
  incarnation no metadata names, owner-checked, then compare-and-deletes the
  entry. The ledger lies inside the database's namespace, so `DROP DATABASE`
  reclaims it.
- **Every metadata write is a guarded swap.** Lifecycle transitions and the
  schema merge an insert performs swap against the bytes they were computed
  from, re-reading on a lost swap; a schema merge never writes into a
  collection that is not `active`, and is best effort — it never fails the
  document write it describes.
- **`DropCollection` marks the collection `dropping`, then deletes every
  document, drops every secondary index the collection owns, reclaims the
  collection's replicated `increment` counter, and is irreversible.** The order
  is mark → documents → indexes → sequence → metadata, and the metadata key is
  deleted **last** so the operation is retryable at every interruption point —
  see the architecture note. Re-issuing it on a collection already `dropping`
  resumes the teardown. A collection recreated under a dropped name therefore
  issues ID 1 again; it does not inherit the previous collection's high-water
  mark.
- **A drop that cannot reclaim a step fails, and is retryable.** Every step
  returns `service.CollectionMetadataError` with the failing `Op`
  (`mark-dropping`, `list-documents`, `delete-document`, `drop-index`,
  `delete-sequence`, `delete-metadata`), and the metadata — the record that
  makes the collection resolvable — survives until the end, so re-issuing the
  same `DropCollection`, or the sweeper, completes it. Each reclamation step is idempotent, so a retry costs nothing
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
- **A collection's indexes are owned by that incarnation of it.** The create
  records `index.CollectionOwner(database, name, incarnation)` — a fresh random
  incarnation per create — on the metadata (`IndexOwner`) and on every index it
  creates. A name another owner holds refuses the create
  (`*index.OwnershipError`, reached through the `create-index` error); the drop
  removes indexes with `DropOwnedIndex` and leaves one owned elsewhere in
  place. Metadata with no `IndexOwner` predates ownership; its declared
  indexes were created ownerless and are dropped with `DropOwnedIndex(name,
  index.ExplicitOwner)`, so no collection drop is unconditional.
- **Stuck collections are finished by the sweeper.**
  `CollectionService.SweepStuckCollections(ctx, database, staleAfter)` tears
  down every collection `dropping` for at least `staleAfter` and unwinds every
  one `creating` that long (marking it `dropping` with a guarded swap first), and
  returns `service.CollectionSweepResult{Swept, FreedBytes}`. A record whose key,
  database, name, incarnation and index owner do not derive from one another —
  or a ledger entry whose owner or index names do not derive from its own
  collection and incarnation — is refused with
  `service.CollectionInconsistentRecordError` and left in place. A collection
  that fails does not stop the others; failures are returned joined.
  The server drives it — `server.DefaultCollectionSweepInterval` (1 minute)
  and `server.DefaultCollectionStaleAfter` (5 minutes), overridable with
  `server.WithCollectionSweep` — over the databases the node already serves
  and, where the store is replicated, leads:
  - the shared store's databases, found from the **collection work journal**
    (`service.CollectionWorkJournal`, keys
    `__sys:collection-work/{tenant}/{database}/{name}` in the store's raw KV,
    outside every namespace) where the `server.WithCollectionSweepLeader`
    probe reports the system shard led here (the daemon wires it; without one
    the server is a single node). Each is resolved through the registry's
    journaled resolver, which applies tenant resolution and the quota wrapper
    but not the node-local catalog, since only the creating node catalogues a
    prefix tenant's named database;
  - each started volume tenant Host's databases, from the journal in its own
    store, on every node where it is started;
  - a raft tenant's databases where `RaftBackendFactory.ListStarted` reports
    the shard and `RaftBackendFactory.LeadsShard` reports this node's replica
    leading it;
  - every catalogued database served from its own node-local store
    (`servesFromNodeLocalStore`), on every node, with no leadership gating.

  The composite collection service records a journal entry — a fresh nonce
  each time — before a create writes its `creating` record and before a drop
  writes its `dropping` mark; a journal that cannot be written fails the
  operation (`CollectionMetadataError{Op: "journal"}`). After sweeping a
  journaled database the sweeper compare-and-deletes each entry whose
  collection is active or gone, and an entry of a tenant absent from the
  replicated tenant list once it is older than the staleness threshold.
  Deleting a tenant removes its entries directly: `service.TenantKeyPrefixes`
  includes `__sys:collection-work/{tenant}/`, so `ReclaimTenantKeyspace`
  deletes them with the tenant's keyspace. A
  journaled database that fails to resolve for `collectionSweepWarnAfter` (3)
  consecutive passes is logged at Warn. Each database's sweep runs under
  `server.DefaultCollectionSweepTargetTimeout` (30 seconds).
  `StateSince` is the transitioning node's wall clock compared with the
  sweeping node's, so clock skew moves when a stuck collection is swept by up
  to the skew, and nothing else.
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
| A document field named `id`, or an object-valued `fields` as a document's only field | `service.CollectionReservedFieldError` — 400 / `INVALID_ARGUMENT`; nothing is stored. The stored layout keeps a document's identity under `id` and reads a lone `fields` object as the legacy wrapper, so either would be stored as something other than what was sent | **yes** |
| Creating a collection whose name contains `#` | `service.CollectionInvalidNameError` — 400 / `INVALID_ARGUMENT`; nothing is stored. An existing collection with such a name keeps working | **yes** |
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
| Document verb on a `creating` or `dropping` collection | `service.CollectionNotActiveError` — 409 / `FAILED_PRECONDITION` | **yes** |
| A write whose collection was dropped while it ran | the write is retracted; `CollectionNotActiveError` or `CollectionNotFoundError` | **yes** |
| A refused write whose document the teardown (or another writer) removed first | the refusal, wrapped in `service.CollectionWriteSupersededError`; the quota reservation is kept | **yes** |
| Index definition naming no field, or indexes on a service without an index service | `service.CollectionInvalidIndexError` — 400 / `INVALID_ARGUMENT`; nothing is stored | **yes** |
| A derived index name another owner holds | `CollectionMetadataError{Op: "create-index"}` wrapping `*index.OwnershipError` — 409 / `FAILED_PRECONDITION`; the create is unwound | **yes** |
| Two index definitions naming one field | `service.CollectionInvalidIndexError` — 400 / `INVALID_ARGUMENT`; nothing is stored | **yes** |
| A write decided against a collection since dropped and created again | the write is retracted; `CollectionStateConflictError{Op: "write"}` — 409 / `ABORTED` | **yes** |
| A teardown whose record was finished by somebody else | reclaims only its own incarnation's keyspace; its metadata compare-and-delete finds a newer record and removes nothing of it — `CollectionStateConflictError{Op: "teardown"}`, treated as done by the sweeper | **yes** |
| A stored record or ledger entry whose fields do not derive from one another | the sweeper refuses it with `service.CollectionInconsistentRecordError` and leaves it for an operator | **yes** |
| SQL `UPDATE ... SET _id = N` whose release of the old `_id` fails with an unknown outcome | `provider.DocumentMoveOutcomeUnknownError` (matches `service.ErrOutcomeUnknown`); **both** documents are left | **yes** |
| A fenced-out write that cannot be retracted, or a fence that cannot read the metadata | `CollectionMetadataError{Op: "retract-write"}` / `{Op: "fence"}` — 500 / `INTERNAL`: the document may be stored | **yes** |
| Index build fails partway through a create | the create is unwound — nothing remains, a retry succeeds; `CollectionMetadataError{Op: "create-index"}` | **yes** |
| Create's activation loses to a concurrent drop | `service.CollectionStateConflictError{Op: "activate"}` — 409 / `ABORTED`; its indexes are removed | **yes** |
| Guarded write loses to a concurrent writer | `service.CollectionDocumentConflictError` — 409 / `ABORTED`; over SQL, MySQL 1213 / `SQLSTATE 40001` | **yes** |
| Document body would exceed the tenant's `MaxStorageBytes` | `quota.ExceededError`; nothing is written | **yes** |
| Missing `collections:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

Collections are never implicitly created by an insert. An insert into a
misspelled collection name must fail, not silently create a second collection
that then diverges.

## Configuration

No collection-specific configuration keys. The collection sweeper's interval
and staleness threshold are `server.WithCollectionSweep` options for a
composer, defaulting to one minute and five minutes. `StorageCodec` is a per-collection
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
   `<tenant>/<database>/__col:{database}:{collection}#{incarnation}/{id}`.

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

**Collection document bytes are counted against `MaxStorageBytes`, per call.**
They were not: the collection service writes documents through the KV
servicer it was built over, and the composite one is built — and cached, for
its process-local increment generators — before `applyQuota` wraps the
bundle's KV. `applyQuota` now also wraps the bundle's collection service
(`quotaCollectionService`), on every resolve and with the tenant's CURRENT
limit, so a changed limit is never pinned by the cache and every transport,
the SQL surface included, is metered.

- **The cost of a document is its body**: the JSON encoding of its fields,
  `_id` excluded. The metadata a write touches (schema discovery) and an
  `increment` collection's counter are not counted — a stated undercount.
- **Enforcement is immediate.** A tenant already over its limit through
  documents written before this accounting existed has its next document
  write refused.
- **Releases follow the KV wrapper's three-way rule.** A write refused or
  failed before it could store anything releases its reservation; a write
  whose outcome is unknown (`CollectionMetadataError.OutcomeUnknown`) retains
  it, and so does a refused write whose document somebody else had already
  removed and released (`service.CollectionWriteSupersededError`).
- **Every release is of exactly what the release's own guarded write
  removed.** An update is a guarded replace of the revision it just read and
  releases the document that replace displaced; a delete is a guarded delete
  of the revision it read and releases that document; an upsert, and a
  `field_hash` insert that displaced a document, release the `Previous`
  document the guarded write reports. A lost guard re-reads and tries again.
  Every write the wrapper makes is guarded: a read that finds no document, or
  no active collection, is returned as the refusal it is, never handed to the
  service's unguarded update or delete, whose effect the wrapper could not
  measure. So two concurrent callers never release one document twice, and
  without drops in play the usage equals the stored body bytes exactly.
- **A drop releases what its teardown removed.** `DropCollection` goes
  through `service.CollectionDropMeter.DropCollectionMetered`, and the sweeper
  through `service.CollectionSweepResult.FreedBytes`: the sum of
  `DocumentBodyBytes` over exactly the documents the teardown's own
  compare-and-deletes removed, reported and released even when the teardown
  fails part-way. A document a concurrent guarded delete removed first is
  released by that delete and not counted again. A wrapped service that
  cannot report what its drop freed is refused rather than dropped unmetered.
- **What can still be over-counted.** Every remaining drift charges the tenant
  for MORE than is stored, never less, and comes from a write that raced a drop
  of its collection or whose outcome is unknown:
  - a write refused because its collection was dropped under it, that had
    already displaced a document in the dropped incarnation before its fence
    refused it: the displaced document's bytes are not released;
  - a write whose outcome is unknown -- its own storage call, its fence's
    metadata read, or its retraction failed -- that in fact stored nothing:
    its reservation is kept;
  - a writer that dies between its write and its fence, after the drop's
    teardown scanned the keyspace: its document is left, unreachable, in the
    dropped incarnation's keyspace, still charged.

  For a `volume` tenant the reconciler corrects the counter from the store.
  For a `prefix` or `raft` tenant nothing re-measures it: **the over-count is
  permanent for prefix/raft tenants** until an operator raises the limit or
  the tenant is recreated.
- `MaxCollections` is enforced by the service itself and is unchanged.

## Persistence

- Metadata: `__sys:collections/{database}/{name}` → `CollectionInfo`, including
  its lifecycle `state` and `state_since` and, for a collection with indexes,
  its `index_owner`. A record without them predates them and reads as an
  `active` collection whose indexes were created ownerless.
- Documents: `__col:{database}:{collection}#{incarnation}/{id}`, encoded with
  the collection's storage codec; `__col:{database}:{collection}/{id}` for a
  collection created before incarnations existed.
- Increment counters: `__seq:{database}:__collection:{name}#{incarnation}`;
  `__seq:{database}:__collection:{name}` for a collection created before
  incarnations existed.
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
  metadata delete, and on the retryability of a failed drop.
- **Lifecycle states**, against faithful doubles whose guarded writes are
  genuinely atomic: a create failing on its second of three indexes leaves
  nothing and its retry succeeds; a `creating` collection is reported and
  refused by every document verb; a drop landing between a write and its
  fence retracts the write; a schema merge racing a drop does not revert the
  mark; the sweeper finishes a stale `dropping` collection, unwinds a stale
  `creating` one and leaves fresh and active ones alone. Each is
  mutation-checked: disabling the fix makes its test fail.
- **Ownership**, on every index-applying state machine and at the service: a
  foreign owner's create is refused, an owner-checked drop by a stranger
  removes nothing, a dotted-name collision is refused, a recreated collection
  gets a new incarnation.
- **The guarded upsert under concurrency**: N writers on one ID, and every
  value written is displaced exactly once by a replace that reported it, or
  survives. Over SQL, a forced interleaving that the three-call `REPLACE`
  failed — the second statement reporting an insert over an existing document —
  and N concurrent `REPLACE INTO`s that must all report a replacement or 1213.
  Both fail against the previous writer.
- **Quota**: a tenant at its limit is refused a document write of the size a KV
  write is refused; a document's lifecycle is charged its body bytes exactly.
- **Transports**: the index definitions and the state survive a round trip on
  each of the six transports, and the CLI's `--index` flag. The engine
  behaviour that makes an orphaned index a correctness problem rather than a
  space leak (index creation is idempotent by name, so the surviving definition
  wins over a divergent re-create) is pinned separately in `pkg/engine`.

## Observability

- Collection create and drop, and document insert/update/delete, are audited
  through [../audit/](../audit/).
- `CollectionInfo.DocumentCount` is the cheap health signal for "did my import
  land"; it is served from metadata rather than by counting.

## Integration testing

`make integration-test-collections` runs this feature's self-spawned integration tests: `collection_cli_test.go`. The per-transport accounting, including every gap, is the `collections` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
