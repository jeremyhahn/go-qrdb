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

Storage constant, `pkg/service`:

```go
const collectionMetadataPrefix = "__sys:collections/"
```

Metadata is keyed `__sys:collections/{database}/{name}`.

## Invariants

- **Document IDs are `uint64` and serialize as JSON *strings*** (`json:"id,string"`).
  This is not cosmetic: a `uint64` above 2^53 loses precision as a JSON number,
  and JavaScript clients are first-class consumers here.
- **The ID strategy is fixed at creation** and cannot be changed afterwards.
  `field_hash` requires `FieldHash` to be set; the request is refused without it.
- **`field_hash` is deterministic** — re-inserting the same document field value
  yields the same ID, which is what makes re-insertion idempotent rather than
  duplicating.
- **`UpdateDocument` replaces the whole document.** There is no partial update
  or merge.
- **`DropCollection` deletes every document and is irreversible.**
- **`ListDocuments` is paginated** and must be driven to completion by following
  `NextPageToken`. A caller that reads one page and stops has read one page, not
  the collection.
- **Documents and metadata live under `__sys:`**, inside the internal-key guard,
  so they never appear in user KV enumeration.
- **The database is a request field, the tenant is not.** Tenant comes from the
  authenticated identity.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Creating a collection that exists | `service.CollectionAlreadyExistsError` | yes |
| Operating on an unknown collection | not-found | **yes** — never auto-created |
| `id_strategy: field_hash` without `field_hash` | invalid-argument | **yes** |
| Unknown `id_strategy` value | invalid-argument | **yes** |
| Unknown document ID on get/update/delete | not-found | yes |
| Malformed document JSON | invalid-argument | **yes** |
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

## Persistence

- Metadata: `__sys:collections/{database}/{name}` → `CollectionInfo`.
- Documents: stored beneath the collection, encoded with the collection's
  storage codec.
- **Compatibility**: the storage codec is recorded on the collection, so a
  collection created with one codec keeps reading with it. Changing the default
  codec does not rewrite existing collections.

## Test strategy

- **Unit**: `pkg/service` collection service — create/info/list/drop, document
  CRUD, each ID strategy (including `field_hash` determinism and the missing
  `field_hash` refusal), and pagination token round-trip.
- **Error tests** per function: happy path plus error path, per the repo rule.
- **JSON round-trip test for the `id,string` encoding** — this is exactly the
  kind of detail that regresses silently.
- **Integration**: collection parity across transports in the standalone stage.

## Observability

- Collection create and drop, and document insert/update/delete, are audited
  through [../audit/](../audit/).
- `CollectionInfo.DocumentCount` is the cheap health signal for "did my import
  land"; it is served from metadata rather than by counting.
