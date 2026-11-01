# Secondary Indexes and the Query Planner — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go` (the `kvstore.KVStore` surface the DAO runs over — `AtomicKVStore`,
`FencedCreator`, `MemoryKVStore`, `UsableAsMapKey` — is the key-value
feature's, in [../key-value/engineering.md](../key-value/engineering.md#the-kvstorekvstore-surface--sdkgo)):

```go
func (c *Client) Index() transport.IndexClient
func (c *Client) KVStore() KVStore   // routes index ops to IndexClient

// DAO.SaveIfAbsent's refusal when the store cannot write atomically, whether
// it is not an AtomicKVStore at all or refused the call (the store's refusal
// is then reachable through Unwrap). It matches ErrKVAtomicWriteUnsupported,
// the one marker for the condition across the stack.
type DAOAtomicWriteUnsupportedError = dao.AtomicWriteUnsupportedError

// DAO options, codec constants and sort directions. Prefixed because the SDK
// root has ClientOption namesakes — sdk.WithTimeout is a ClientOption.
var DAOWithShard, DAOWithCodec, DAOWithTimeout, DAOWithPartition, DAOWithDatabase
const DAOCodecJSON, DAOCodecMsgPack, DAOCodecCBOR
const DAOSortAscending, DAOSortDescending
type DAOUnitOfWork, DAOTransactional[E], DAOVersioned, DAOTimestamped
func NewDAOUnitOfWork, RegisterDAOSave[E], RegisterDAODelete[E], WithDAOUnitOfWorkTimeout
type DAOAtomicBatchUnsupportedError

// A secret entity field held in go-enclave protected memory, and the
// refusals that keep it there -- see "Secret entity fields" below.
type DAOSecretBytes
var NewDAOSecretBytes, DAOSecretBytesFromBuffer
type DAOSecretCodecUnsupportedError, DAOSecretEncodingSizeError, DAOSecretFieldIndexedError

// DAOs built from a factory for a named database. The database is resolved
// by DAOFactory.DatabaseKVStore as the partition of its own name, and NEVER
// falls back to the factory's default store: a name the factory cannot
// resolve is refused with DAODatabaseStoreUnresolvedError and nothing is
// built. NewClientDAOFactory resolves a database name to
// Client.DatabaseScoped(name).KVStore(), so against a node the DAO lands in
// that database of the caller's tenant. The database argument selects the
// store only; DAOWithDatabase is the index-name qualifier for databases that
// share one store, a different setting.
func NewDAOTransactional[E](store KVStore, entityType string, newEntity func() E, opts ...DAOOption) (DAOTransactional[E], error)
func NewDatabaseDAO[E](factory *DAOFactory, database, entityType string, newEntity func() E, opts ...DAOOption) (GenericDAO[E], error)
func NewDatabaseDAOTransactional[E](factory *DAOFactory, database, entityType string, newEntity func() E, opts ...DAOOption) (DAOTransactional[E], error)
func NewClientDAOFactory(c *Client) *DAOFactory
type DAODatabaseStoreUnresolvedError

// DAOUnitOfWork.Commit REQUIRES the underlying store to implement
// kvstore.MultiGuardedBatchKVStore: every queued save/delete lands as ONE
// guarded batch proposal (unconditional -- no guards -- but still atomic).
// A store that does not offer the capability is refused with
// *DAOAtomicBatchUnsupportedError -- there is no sequential
// apply-and-compensate fallback, because one is NOT atomic against a
// concurrent reader. A move or rename is therefore safe: a
// delete and a put in the same UnitOfWork commit together or not at all,
// never partially. NewDAOUnitOfWork, RegisterDAOSave and RegisterDAODelete
// are the generic functions a DAOUnitOfWork needs to be constructed and
// populated from outside the module; the type alias alone cannot cross a
// generic function boundary.
//
// FieldHashGenerator and DynamicFieldHashGenerator hash a natural key with
// FNV-1a, which has no resistance to a deliberately engineered collision.
// CryptoFieldHashGenerator and CryptoDynamicFieldHashGenerator hash with
// SHA-256 instead, truncated to 64 bits (the standard construction;
// XOR-folding resists no chosen-collision attack truncation does not).
// Both families land in the same 64-bit ID space and
// share its bounds: roughly 2^32 attempts to find any colliding PAIR when
// the attacker controls both natural-key values (the birthday bound), and
// roughly 2^64 to hit one FIXED victim ID (a generic preimage search).
// SHA-256 does not change either bound; it closes FNV-1a's direct-
// construction attack, which needs no search at all. The FNV-1a generators
// keep FNV-1a, since swapping their hash function in place would orphan
// every entity already persisted under it.
//
// GenericDAO closes the write- and read-side gaps a collision leaves open,
// for every generator that implements DeterministicIDGenerator (all four).
// Save and DAOUnitOfWork's queued saves route a NEW entity's hash-derived
// ID through the store's atomic conditional write. An occupied ID means: a
// record carrying the SAME natural-key value is the SAME logical entity --
// an update, via CompareAndSwap against the record just read so it stays
// atomic -- and only a record carrying a DIFFERENT natural-key value is a
// genuine collision, refused with *DAOIDCollisionError rather than
// silently overwritten. A same-key update whose CompareAndSwap loses every
// retry to sustained concurrent writers refuses with
// *DAOSaveContentionExhaustedError instead -- never *DAOIDCollisionError,
// which is reserved for a confirmed different natural key.
// GetByNaturalKey(ctx, value) derives the lookup ID and calls
// VerifyNaturalKey(entity, entityType, fieldName, value) on the result
// before returning it -- comparing the read-back entity's natural-key
// field against the value the caller looked up by -- so the check is not
// opt-in the way calling VerifyNaturalKey manually after a plain Get is.
// *DAOUnsupportedIDGeneratorError is GetByNaturalKey's refusal when the
// configured generator is not deterministic.
//
// Only the create/save paths verify the natural key, and only
// GetByNaturalKey verifies on read: Get(id), Page, ForEachPage and Delete
// do NOT -- each takes a bare id or an already-loaded entity, never the
// natural-key value a mismatch would be checked against, so there is
// nothing for them to verify.
type DeterministicIDGenerator
type DAOIDCollisionError
type DAOSaveContentionExhaustedError
type DAOUnsupportedIDGeneratorError
var NewCryptoFieldHashGenerator, NewCryptoDynamicFieldHashGenerator
var VerifyNaturalKey
```

`sdk/go/transport`:

```go
type IndexClient interface {
	IndexCreate(ctx context.Context, name, fieldName string, config IndexConfig) error
	IndexDrop(ctx context.Context, name string) error
	IndexQuery(ctx context.Context, indexName, value string) (*IndexQueryResponse, error)
	IndexScan(ctx context.Context, indexName string, start, end *string) (*IndexScanResponse, error) // nil = open bound
	IndexList(ctx context.Context) (*IndexListResponse, error)
}

type IndexConfig struct {
	Unique          bool `json:"unique"`
	Sparse          bool `json:"sparse"`
	Numeric         bool `json:"numeric"`          // order-preserving numeric terms
	CaseInsensitive bool `json:"case_insensitive"`
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
  Numerics: the self-delimiting numeric term (`index.EncodeNumericValue`).
  Changing either encoding invalidates every existing compound index on disk,
  which is why the numeric one is versioned (`index.NumericEncodingVersion`).
- **Numeric terms are exact and versioned.** `index.EncodeNumericValue` parses
  decimal text into a prefix-free term whose byte order is numeric order
  (`-Inf` < negatives < `0` < positives < `+Inf`; `-0` is `0`; `NaN` is never
  indexed and is refused as a bound). `index.IndexConfig.EncodingVersion`
  records the version entries were written under; `IndexConfig.CheckEncoding`
  — called by every index constructor — refuses a numeric definition whose
  version is not current with `*index.StaleEncodingError`. Every create
  handler (`KVEngine`, `MVCCEngine`, the Pebble state machine) sets the
  current version and calls `Index.Clear` before its backfill, so a create is
  also a rebuild. A persisted catalog must store the version per definition
  and rebuild stale ones; it must never open them.
- **A numeric definition reaches the engine from every entry point.**
  `kvstore.EntityIndex.Numeric` is the DAO's request; the system-tenant
  adapter (`lifecycle.kvStoreAdapter`) turns it into
  `IndexConfig.ValueType = IndexValueTypeNumeric`, the SDK adapter into
  `transport.IndexConfig.Numeric`, and every transport — REST, gRPC, Unix,
  QUIC, MCP, embedded — carries that flag (and `CaseInsensitive`) to
  `protocol.IndexCreateOptions`. `MemoryKVStore` builds the same terms, and a
  re-registration with a different value type is an `IndexRegistrationError`
  there, as any divergent definition is.
- **The DAO asks for numeric terms only where the JSON is a number.**
  `pkg/dao.hasNumericTerms` is true for an integer or float kind whose type
  implements neither `json.Marshaler` nor `encoding.TextMarshaler`. Only such
  single-field indexes are advertised to the planner with their `FieldType`;
  a compound index with a numeric member is not advertised, because
  `EntityIndex` has no per-member value type.
- **A consumed range predicate is answered exactly by the scan.** The planner
  does not post-filter the predicate a scan plan consumes, so
  `planner.Plan.RangeStartExclusive` (`>`) and `RangeEndExclusive` (`<`) are
  binding. `pkg/dao.scanInterval` turns a plan into the half-open
  `[start, end)` `kvstore.KVStore.ScanIndex` answers; "the value after v" is
  `v` followed by one `0x00` byte (`successorBound`), which on a numeric index
  `index.ResolveNumericScanBound` reads as the next term after the number.
  `TestDAO_NumericRangeMatchesBruteForce` checks every operator against a
  brute-force filter over random integers and floats.
- **PREFIX is a post-filter even when it picks the index.** A prefix scan has
  a lower bound only, so `Plan.PostFilters` keeps the `OpPrefix` predicate
  (`prefixIsResidual`) and trims what the scan read past the prefix. On a
  numeric field PREFIX is never planned on the index at all
  (`isNumericFieldType`): it asks about the decimal spelling.
- **An invalid numeric bound is the caller's error.**
  `index.InvalidNumericBoundError` classifies as `InvalidArgument` (400 /
  `INVALID_ARGUMENT`) through the `ReadError` that carries it, never as a
  server fault.
- **`index.IndexValueTypeFixed64` is not a numeric index.** It is the compound
  member type for values the caller already encoded as eight ordered bytes —
  SQL primary-key columns (`catalog.KeyCodec`) — and splits back at a fixed
  width. Their stored key format is independent of the numeric index term.
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
  collection's, the empty string for the explicit API and for an index with
  no recorded owner. It travels as a trailing extension of the
  index-create command, and every state machine that applies index commands
  keeps it beside the index. Re-creating a name under the SAME owner succeeds
  without reconciling the definition — the existing definition wins — with one
  exception: a re-create asking for a different value type rebuilds the index
  at that type (`engine.IndexCreateValueType`; see
  [architecture.md](architecture.md#numeric-order)), so a field declared
  numeric after it was indexed as text gets numeric terms. Creating
  a name another owner holds is **refused** (`index.OwnershipError`, reported
  as an application error so the apply advances; 409 / `FAILED_PRECONDITION` on
  the wire) instead of silently adopting an index somebody else made. Any other
  divergent same-owner re-create must still be refused, or the previous index
  dropped, *before* the proposal. This is why an index left behind by a teardown is a
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
  (`index.ExplicitOwner`), so it removes an index with no recorded owner and
  refuses one a collection owns — 409 / `FAILED_PRECONDITION` — rather than
  remove a collection's `unique` constraint out from under it.
  `IndexServicer.DropIndex` removes an index whoever owns it; it is the drop
  whole-scope teardowns (`DROP DATABASE`, deleting a tenant) issue, since a
  scope owns everything in it, and no transport reaches it. The owner-checked drop is a trailing extension of the
  index-drop command; a drop command without it is unconditional. An
  embedder-supplied
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

`Find(ctx).GroupBy(...).Aggregate(...)` runs on top of whatever plan the
planner chose, so it inherits index selection. The aggregation API, its result
types and its limits are the DAO's, documented in
[../16-query-planner.md](../16-query-planner.md#dao-aggregations).

## Secret entity fields

`dao.SecretBytes` (`sdk.DAOSecretBytes`) is an entity field for secret key
material. Its bytes live in a `secure.Buffer` (go-enclave protected memory:
mlocked, guard-paged, outside the Go heap) and the entity codec moves them
between that buffer and the record without leaving a plaintext copy on the
heap that nothing can wipe.

```go
type Credential struct {
	ID    uint64           `msgpack:"id"`
	Label string           `msgpack:"label" index:"true"`
	Key   dao.SecretBytes  `msgpack:"key"`
}

key, err := dao.NewSecretBytes(raw)   // raw is wiped, success or failure
credDAO, err := dao.New[*Credential](store, "creds", newCredential,
	dao.WithCodec(dao.CodecMsgPack))
```

- **Construction.** `NewSecretBytes(src)` moves `src` into protected memory and
  wipes `src` on every path; an empty `src` is `*secure.InvalidSizeError`.
  `SecretBytesFromBuffer(buf)` takes ownership of a buffer that never touched
  the heap. Access is `WithBytes(fn)` (a borrow for the call), `Len`,
  `IsZero`, `Buffer`; `Destroy` / `DestroyErr` wipe and release.
- **A handle, not a value.** Copies of a `SecretBytes` share one buffer, and
  `Destroy` through any copy wipes it for all. Every `Get`, `Find` or page
  decodes a new buffer the caller owns and must `Destroy`; decoding into an
  entity replaces the handle it held without destroying it. The zero value
  holds nothing and encodes as nil.
- **The codec is chosen by the entity type.** `dao.NewCodec` (and so
  `dao.New`) inspects the entity type statically; a type holding a
  `SecretBytes` — directly, or inside a struct, pointer, slice, array or map —
  gets the secret-carrying encoding for the requested codec, or is refused
  when the codec has none. A `SecretBytes` stored in an interface-typed field
  is invisible to that inspection; declare the field with its static type.

| Codec | Secret fields | Why |
|---|---|---|
| MessagePack (`CodecMsgPack`, build tag `codec_msgpack`) | **supported**, as one raw bin item | encoding measures the record, then writes the secret straight from its protected pages into a record allocated at exactly that size; decoding reads the bin item straight from the record into a new protected buffer |
| JSON (the DAO default) | refused | base64 of the plaintext is built in the encoder's pooled buffer |
| YAML, TOML | refused | encode through `encoding.TextMarshaler` and growing buffers |
| BSON, CBOR | refused | encode through pooled writer buffers the DAO cannot wipe |
| ProtoBuf | refused | encodes generated `proto.Message` types, which cannot declare the field |

The refusal is `dao.CodecCreationError` wrapping
`dao.SecretCodecUnsupportedError{CodecType}`, returned when the codec is built
— an entity type with a secret field cannot reach a store through a codec that
would copy it. A build without `codec_msgpack` refuses every codec.
`SecretBytes` also refuses `encoding/json` and `encoding.TextMarshaler`
directly, so an entity marshalled outside the DAO (into a log line) fails
rather than leaks, and every `fmt` verb prints `dao.SecretBytes(redacted)`.

**The one heap copy is the record.** On a save the encoded record is the only
heap copy of the plaintext the codec makes, and it is handed to the store; on
a read the record the store returns is the one the codec decodes from. Neither
is wiped by the DAO: a store owns the slices it is given and returns, and an
in-memory store may alias them. Copies the store path makes beyond the codec —
transport frames, the Raft log in memory, the barrier's plaintext input — are
outside this guarantee.

**At rest the record is a value like any other.** The store encrypts it under
the database's barrier before it reaches storage, so the secret is at rest
only as ciphertext. A database on a plaintext barrier (strategy `none`) stores
it in plaintext; the field does not add encryption of its own.

**A secret field is never indexed and never a SQL column.** An `index:` tag or
a compound index naming a secret field fails `dao.New` with
`dao.SecretFieldIndexedError{EntityType, Field}` — an index entry is the
field's plaintext in the clear keyspace. The entity-SQL bridge registers no
column for it, since a result row would read the secret out of protected
memory.

**Encoding is two-pass, and checks itself.** The first pass counts bytes into
a discarding writer; the second writes into a fixed-capacity writer that
refuses to grow. A type whose encoding changes size between the passes is
refused with `dao.SecretEncodingSizeError{Measured, Encoded}` and the partial
record is wiped. Only entity types holding a secret pay for the second pass.

Tests: `pkg/dao/secret_bytes_test.go` (refusals, type inspection, index and
column refusal) and `pkg/dao/secret_bytes_msgpack_test.go` (a DAO round trip
that wipes the decoded secret, byte-equality with a plain `[]byte` field, and
heap-allocation measurements showing decode allocates no copy of the secret
and encode allocates only the record, each beside a control that allocates the
copies and so proves the measurement can fail).

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| `IndexCreate` for a name that exists under the same owner | idempotent success, decided on apply; a create asking for a different value type (`numeric`) rebuilds the index, and one diverging only in field or uniqueness is a no-op (the original wins) | n/a |
| `IndexCreate` or `IndexDrop` for a name another owner holds | `*index.OwnershipError` (409 / `FAILED_PRECONDITION`) | **yes** — refused on apply, never adopted |
| `IndexCreate` for a new name when the shard already holds `index.max_indexes` indexes | `*service.IndexLimitError` (429 / `RESOURCE_EXHAUSTED`) | **yes** — refused at apply, nothing written |
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
| DAO `SaveIfAbsent` / `CompareAndSwap` / `DeleteIfMatch` over a store with no atomic write, or one that refused it | `*dao.AtomicWriteUnsupportedError` (matches `kvstore.ErrAtomicWriteUnsupported`; a store refusal stays in the chain) | **yes** — never degrades to `Get`-then-`Save`; any other store error is returned unchanged as outcome-unknown |
| DAO `CompareAndSwap` where `expected`/`next` carry different `EntityID()`s, or either `CompareAndSwap`/`DeleteIfMatch` on an entity with `EntityID() == 0` | `dao.InvalidEntityIDError` | **yes** |
| Store lacks the guarded swap — `CompareAndSwap` | `kvstore.ErrAtomicWriteUnsupported` | **yes** — a read followed by an unguarded put is the race the guard exists to close |
| Guarded verb on an ABSENT key — `CompareAndSwap` / `CompareAndDelete` | `(false, nil)` | **yes** — absence is an outcome, not a failure; an error here cannot be told from a broken store |
| `CompareAndSwap` outcome UNKNOWN (non-nil error) | error, reservation retained by the quota wrapper | **yes** — a Raft proposal can commit and lose its response; releasing would under-count a stored value |
| `Save`/`UnitOfWork.Commit` for a NEW entity (`EntityID() == 0`) whose `DeterministicIDGenerator`-derived ID already names a DIFFERENT stored record (a confirmed collision) | `*dao.IDCollisionError` | **yes** — routed through the store's atomic conditional write, never an unconditional `Put` |
| `Save`/`UnitOfWork.Commit` for a NEW entity whose derived ID already names a record with the SAME natural-key value (the same logical entity) | applied as an update (`CompareAndSwap` against the record just read), no error | n/a — not a collision |
| `Save`'s same-key update path exhausts its bounded retry budget under sustained concurrent writers | `*dao.SaveContentionExhaustedError` | **yes** — never `*dao.IDCollisionError`, which is reserved for a confirmed different natural key |
| `Save` of a NEW entity with a `DeterministicIDGenerator` configured, over a store with no atomic write | `*dao.AtomicWriteUnsupportedError` | **yes** — the same refusal `SaveIfAbsent` gives; `Save` does not silently fall back to an unconditional `Put` for this case |
| `GetByNaturalKey` where the configured `IDGenerator` is not a `DeterministicIDGenerator` | `*dao.UnsupportedIDGeneratorError` | **yes** — there is no deterministic way to derive an ID from a bare value |
| `GetByNaturalKey` where the derived ID names a DIFFERENT entity | `dao.NaturalKeyMismatchError` | **yes** — the mismatched record is never returned |
| `Get(id)`, `Page`, `ForEachPage`, `Delete` | never verify a natural key | n/a — each takes a bare id or an already-loaded entity, never the natural-key value a mismatch would be checked against |
| `dao.New` / `dao.NewCodec` for an entity type holding a `SecretBytes` with any codec but MessagePack (or without `codec_msgpack`) | `dao.CodecCreationError` wrapping `dao.SecretCodecUnsupportedError` | **yes** — refused before any record exists |
| `index:` tag or compound index naming a `SecretBytes` field | `dao.SecretFieldIndexedError` from `dao.New` | **yes** |
| `json.Marshal` / `MarshalText` of a `SecretBytes` outside the DAO | `dao.SecretCodecUnsupportedError` (`"json"` / `"text"`) | **yes** |
| Saving an entity whose `SecretBytes` was destroyed | `*secure.NilBufferError` | **yes** — never stored as empty |
| A secret entity's encoding changes size between the measuring and writing pass | `dao.SecretEncodingSizeError`; partial record wiped | **yes** |

**There is one atomic-refusal marker: `kvstore.ErrAtomicWriteUnsupported`.**
Every layer that refuses a guarded write for want of the capability — the
kvstore implementations, `dao.AtomicWriteUnsupportedError`, the database
backends, the service layer, the custodian service and the invitation store —
returns it or matches it under `errors.Is`
(`sdk.ErrInviteAtomicWriteUnsupported` is an alias of it). No package defines
a sentinel of its own beside it: a second spelling of the condition would let
a caller testing one miss a refusal carrying the other. The
marker also matches `kvstore.ErrNotApplied`: a capability refusal happens
before anything commits, so it proves nothing was written and a caller need
not read back to reconcile.

The unknown-index refusal matters: degrading to a full scan would turn a typo
into a silent performance cliff on a production dataset.

## Configuration

Indexes are declared per entity (struct tags) or created through the API. There
is **no per-index configuration through the config file** — per-index
settings are API-only. The index itself is storage-backed
(`index.StoragePebbleIndex`) over whatever `storage.Storage` the database's
engine was built on — memory, file, pebble or objstore — so `storage.backend`
selects the database's store but never a separate index implementation.

`index.max_indexes` caps the secondary indexes one shard holds: the system
shard, each tenant's shard and each dedicated Raft shard count separately, and
every index in the shard counts, whichever database or collection created it.
`0` (the default) is unlimited. `service.IndexService.CreateIndex` carries the
cap in the `IndexCreate` command (a 4-byte trailer after the owner, absent when
uncapped) and reads nothing before proposing. The apply path
(`index.CreateExceedsCap`, in `KVEngine`, `MVCCEngine` and the Pebble store)
counts the indexes the create applies over and refuses a create for a new name
at the cap with a limit refusal result, which the service returns as
`*service.IndexLimitError`; a create naming an index that already exists is not
counted and gets its usual answer. A dropped index frees its slot. Because the
decision is made in log order over replicated state, concurrent creates through
any number of nodes take the last slot exactly once (#1088). The cap is
node-local config, but the value applied is the one the proposing node wrote
into the command, so replicas configured differently still apply the same
verdict. A binary from before the cap ignores the field, so the command
carries it only once the group activates `command-caps`
(`featuregate.CommandCaps`); until every replica advertises the feature the
create is proposed uncapped, byte-identical to an older binary's, and the cap
is not enforced. A standalone node gates nothing and carries it at once.

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
  `[prefixLen:2 LE][prefix]` block.** A command that ends at the flag byte
  decodes as an empty declared prefix. An index created by such a command
  still gets the namespace half of its scope, because that half is read back
  off the index name.

## Test strategy

- **Unit**: `pkg/index` (tag parsing per tag form, compound definition
  registration, encoding round-trips including the `\x00` escape), `pkg/planner`
  (plan selection for each row of the prefix-matching table, cost comparison,
  plan cache hit/miss).
- **Conformance**: `pkg/index/indextest` holds the numeric-scan, string-scan
  and case-folding contracts as suites. `index.MemoryIndex`,
  `index.StoragePebbleIndex` and `pebbleindex.Index` each run them against
  themselves, so one contract holds every implementation without `pkg/index`
  importing the backends.
- **Security tests** are first-class here: unknown field, non-whitelisted
  operator, and an attempted injection through a value must each be refused.
- **Error tests** per function, happy path plus error path.
- **Integration**: index parity across transports in the standalone stage.
  `pkg/server/lifecycle` drives `kvStoreAdapter` over a REAL `KVEngine`
  (`kv_adapter_real_engine_test.go`), not a mock, for case-insensitivity
  and for a DAO built `WithDatabase`: a mock can
  only show the adapter's `CreateIndex`/`Query` requests were shaped
  correctly, not that the engine actually answers a post-registration write
  with a non-empty result.

## Observability

- Index create and drop are audited through [../audit/](../audit/).
- Plan selection is observable at debug log level, which is the way to confirm a
  query used the index you expected rather than a full scan.

## Integration testing

`make integration-test-secondary-indexes` runs this feature's self-spawned integration tests: `index_cli_test.go`. The per-transport accounting, including every gap, is the `secondary-indexes` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
