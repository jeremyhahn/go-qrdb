# RAG — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Exported surface

`sdk/go/transport`:

```go
type RAGClient interface {
	RAGIngestDocument(ctx context.Context, req *RAGIngestRequest) (*RAGIngestResponse, error)
	RAGQuery(ctx context.Context, req *RAGQueryRequest) (*RAGQueryResponse, error)
	RAGDeleteDocument(ctx context.Context, req *RAGDeleteDocumentRequest) (*RAGDeleteDocumentResponse, error)
	RAGListDocuments(ctx context.Context, req *RAGListDocumentsRequest) (*RAGListDocumentsResponse, error)
}

type RAGIngestRequest struct {
	DocumentID    string            `json:"document_id"`
	Text          string            `json:"text"`
	Namespace     string            `json:"namespace"`
	Model         string            `json:"model,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	ChunkStrategy string            `json:"chunk_strategy,omitempty"` // fixed | sentence | paragraph
	ChunkSize     int               `json:"chunk_size,omitempty"`
	ChunkOverlap  int               `json:"chunk_overlap,omitempty"`
	ChunkMinSize  int               `json:"chunk_min_size,omitempty"`
}
type RAGIngestResponse struct {
	DocumentID string `json:"document_id"`
	ChunkCount int    `json:"chunk_count"`
	Namespace  string `json:"namespace"`
}

type RAGQueryRequest struct {
	Query     string            `json:"query"`
	Namespace string            `json:"namespace"`
	TopK      int               `json:"top_k,omitempty"`
	Model     string            `json:"model,omitempty"`
	Provider  string            `json:"provider,omitempty"`
	Filter    map[string]string `json:"filter,omitempty"`
}
type RAGQueryResult struct {
	ChunkText  string            `json:"chunk_text"`
	Score      float32           `json:"score"`
	DocumentID string            `json:"document_id"`
	ChunkIndex int               `json:"chunk_index"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}
type RAGQueryResponse struct{ Results []*RAGQueryResult }

type RAGDeleteDocumentRequest  struct{ DocumentID, Namespace string }
type RAGDeleteDocumentResponse struct {
	DocumentID    string `json:"document_id"`
	ChunksRemoved int    `json:"chunks_removed"`
}

type RAGListDocumentsRequest struct {
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}
type RAGDocumentInfo struct {
	ID         string            `json:"id"`
	Namespace  string            `json:"namespace"`
	ChunkCount int               `json:"chunk_count"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}
type RAGListDocumentsResponse struct {
	Documents []*RAGDocumentInfo `json:"documents"`
	Total     int                `json:"total"`
}
```

Server side, `pkg/service/rag_service.go`:

```go
type RAGServicer interface {
	IngestDocument(ctx context.Context, req *IngestDocumentRequest) (*IngestDocumentResponse, error)
	Query(ctx context.Context, req *RAGQueryRequest) (*RAGQueryResponse, error)
	DeleteDocument(ctx context.Context, req *DeleteRAGDocumentRequest) (*DeleteRAGDocumentResponse, error)
	ListDocuments(ctx context.Context, req *ListRAGDocumentsRequest) (*ListRAGDocumentsResponse, error)
	Close() error
}
```

Wired into the protocol servers with `server.WithRAG`, built over the
embedding service.

## Invariants

- **`DocumentID` is the caller's identity for the document.** Ingesting the same
  ID again replaces its whole chunk set: the new chunks overwrite the old ones
  by index, every chunk past the new count (its vector and its text) is
  removed, and the document record is written last. Delete removes each chunk's
  vector, then its text, and the document record last, and reports
  `ChunksRemoved`; a delete that fails part way leaves the record, so deleting
  again finishes it.
- **Chunk text lives in the KV store; the vector carries the reference.**
  Retrieval is search-then-fetch, which is why `RAGQueryResult` can return
  `chunk_text` while the vector index stays dense.
- **`ChunkIndex` is the chunk's position within its document**, so a caller can
  reassemble neighbouring context.
- **`Score` is the similarity score from the underlying vector search**, and its
  scale follows the namespace's distance metric — see
  [../vectors/](../vectors/engineering.md).
- **Results are ordered by score**, best first, bounded by `TopK`.
- **A query embeds with the same provider/model the caller names**, and the
  caller is responsible for that matching the model the corpus was ingested
  with. Mismatched models produce nonsense scores with no error.
- **Ingest is not atomic across chunks in the caller-visible sense**: the chunk
  text and the vectors live in separate stores with no shared transaction, so a
  failure partway leaves the document partially ingested, and re-ingesting the
  same `DocumentID` is the recovery. The document record is the last write, and
  the stale chunks are found by listing the document's chunk keys, so a retry
  removes whatever a failed re-ingest left.
- **A query targets exactly one namespace.**

## Configuration

Section `rag`:

| Field | Type | Default | Description |
|---|---|---|---|
| `default_chunk_strategy` | string | `fixed` | strategy for a request that sets none |
| `default_chunk_size` | int | `512` | chunk size for a request that sets none |
| `default_overlap` | int | `50` | overlap for a request that sets none |
| `default_top_k` | int | `5` | result bound for a query that sets none |
| `max_document_size` | int64 | `0` | max document size in bytes; **0 means unlimited** |

A request's chunk field or `top_k` wins over the key; a zero or omitted one is
unset on every transport and takes the key. A key set to 0 takes the built-in
value shown. `chunk_min_size` has no key and defaults to 100. The node refuses
to start (`*RAGChunkDefaultsError`) when the chunker refuses the three chunk
defaults together, as it does an overlap not below the chunk size for `fixed`.

Chunk parameters by strategy:

| Strategy | `chunk_size` | `chunk_overlap` | `chunk_min_size` |
|---|---|---|---|
| `fixed` | window in characters (512) | characters repeated between windows (50) | a trailing window below this merges into the previous one (100) |
| `sentence` | cap: sentences are packed up to it; a longer sentence is split into fixed windows | applies only when a sentence is split | applies only when a sentence is split |
| `paragraph` | cap: a longer paragraph is split into fixed windows | applies only when a paragraph is split | consecutive paragraphs below this accumulate until the block reaches it; a short final block joins the previous chunk |

**`max_document_size: 0` means unlimited.** A deployment taking untrusted input
should set it.

## Error semantics

Source: `pkg/service/rag_errors.go`.

| Error type | Condition | Fails closed? |
|---|---|---|
| `*RAGServiceClosedError` | the service has been closed | yes |
| `*RAGDocumentNotFoundError` | the document does not exist in the namespace | yes |
| `*RAGIngestError` | ingest failed; wraps the cause: `*rag.DocumentTooLargeError` (a document over `max_document_size`, class TooLarge: 413 / `RESOURCE_EXHAUSTED`), `*rag.ChunkingError` (empty text or a chunk setting the chunker refuses, class InvalidArgument: 400 / `INVALID_ARGUMENT`), `*rag.EmbeddingError` (embedding, namespace creation or a vector write) or `*rag.StoreError` (a KV write), both internal | yes |
| `*RAGQueryError` | query or list failed; wraps the cause, which for a missing vector namespace is `*NamespaceNotFoundError` | yes |
| `*RAGDeleteError` | removing a chunk's vector or text, or the document record, failed after the document was found; wraps the cause; the record survives, so the delete is retried | yes |
| `*RAGTenantRequiredError` (`rag_scope.go`) | an authenticated node received a call carrying no tenant | yes |

| Condition | Behavior |
|---|---|
| Unknown embedding provider | the embedding call fails; nothing is stored |
| Document exceeds `max_document_size` | refused before chunking, as too large (413 / `RESOURCE_EXHAUSTED`) |
| Chunk setting the chunker refuses | refused before embedding, as an invalid argument (400 / `INVALID_ARGUMENT`) |
| Missing `rag:*` permission | permission denied |
| Barrier sealed | refused |

## Persistence

- **Chunk text and metadata**: KV entries at
  `<tenant>/<database>/rag:{namespace}:{document}:{chunk}` and
  `...:meta`, barrier-encrypted. `service.ScopedRAGStore` resolves the KV
  servicer for the request's (tenant, database) through the node's registry
  on every call (`service.RAGBinding`, bound by the server at start), and is
  the only store `service.NewRAGService` accepts, so the pipeline cannot be
  composed unscoped. Records sit inside the database's keyspace, so dropping
  the database reclaims them. Until the binding is set -- and on a node with
  no database registry -- every RAG call is refused with
  `service.CapabilityUnsupportedError`.
- **Chunk embeddings**: vectors in the named vector namespace of the
  request's database, resolved per call through the same binding
  (`service.NewScopedVectorStoreAdapter`); the registry prefixes the namespace
  with `<tenant>/<database>/`.
- **Document metadata**: `RAGDocumentInfo` (`id`, `namespace`, `chunk_count`,
  `metadata`).

**Compatibility**: chunks embedded under one model are not comparable with
chunks embedded under another. Changing the model is a re-ingest of the whole
corpus, not a migration.

## Test strategy

- **Chunkers** (`pkg/rag/chunker`): `fixed_test.go`, `sentence_test.go` and
  `paragraph_test.go` pin each strategy's boundaries, overlap and
  `chunk_min_size` merging as the table above states.
- **Pipeline** (`pkg/rag`): `ingest_test.go`, `reingest_test.go` and
  `retriever_test.go` pin chunk counting and the size limit, re-ingest
  removing a shorter document's surplus chunks, and retrieval with its
  metadata filter and default top-k, against a stub embedder: no RAG test
  needs a model.
- **Service** (`pkg/service`): `rag_service_test.go` pins delete removing
  chunks before the record and keeping the record when a chunk removal fails,
  list pagination and the closed service; `rag_scope_test.go`,
  `rag_scope_store_test.go` and `rag_tenant_test.go` pin that every record
  resolves per call to the request's (tenant, database), and that an unbound
  binding, a call with no tenant, a sealed database and another tenant's
  database are refused.
- **Integration**: `TestRAG_CLI_PerProtocol` (`make integration-test-rag`)
  drives every `qrdb rag` verb over REST, gRPC, QUIC, MCP and the Unix socket
  against a node with no embedding provider: flag validation and the
  not-configured refusal. Ingest, query, list and delete against a configured
  provider run in the Docker `test/integration/api` suite
  (`TestRAGIngestQueryE2E`, `TestRAGListDeleteDocumentE2E`).

## Observability

- `RAGIngestResponse.ChunkCount` and `RAGDeleteDocumentResponse.ChunksRemoved`
  are the accounting signals; they should agree for a document ingested once.
- `RAGListDocumentsResponse.Total` is the corpus size.
- Every RAG call's authorization decision is audited (`auth.authorize`, success
  or denied); see [../audit/](../audit/).
- The RAG pipeline emits no log events and no metrics of its own.
