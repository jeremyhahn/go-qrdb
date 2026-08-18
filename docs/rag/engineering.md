# RAG — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

Wired into the protocol servers with `server.WithRAG()`, built over the
embedding service.

## Invariants

- **`DocumentID` is the caller's identity for the document.** Ingesting the same
  ID again is how a document is replaced; delete removes every chunk vector it
  produced and reports `ChunksRemoved`.
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
- **Ingest is not atomic across chunks in the caller-visible sense**: a failure
  partway leaves the document partially ingested, and re-ingesting the same
  `DocumentID` is the recovery.
- **A query targets exactly one namespace.**

## Configuration

Section `rag`:

| Field | Type | Default | Description |
|---|---|---|---|
| `default_chunk_strategy` | string | `"fixed"` | `fixed` \| `sentence` \| `paragraph` |
| `default_chunk_size` | int | `512` | target chunk size in characters |
| `default_overlap` | int | `50` | overlapping characters or sentences between chunks |
| `max_document_size` | int64 | `0` | max document size in bytes; **0 means unlimited** |
| `default_top_k` | int | `10` | default retrieval result count |

Per-request fields override the defaults. Chunk parameters by strategy:

| Strategy | `chunk_size` | `overlap` | `min_size` |
|---|---|---|---|
| `fixed` | target characters (512) | characters repeated (50) | — |
| `sentence` | target characters, a guideline (512) | sentences repeated (2) | — |
| `paragraph` | target characters (512) | — | merge below this (100) |

**`max_document_size: 0` means unlimited.** A deployment taking untrusted input
should set it.

## Error semantics

Source: `pkg/service/rag_errors.go`.

| Error type | Condition | Fails closed? |
|---|---|---|
| `*RAGServiceClosedError` | the service has been closed | yes |
| `*RAGDocumentNotFoundError` | the document does not exist in the namespace | yes |
| `*RAGNamespaceNotFoundError` | the vector namespace does not exist | **yes** — never auto-created |
| `*RAGIngestionError` | ingestion failed during chunking or embedding | **yes** |

| Condition | Behavior |
|---|---|
| Unknown embedding provider | the embedding call fails; nothing is stored |
| Document exceeds `max_document_size` | refused before chunking |
| Missing `rag:*` permission | permission denied |
| Barrier sealed | refused |

## Persistence

- **Chunk text and metadata**: KV entries, tenant-scoped, barrier-encrypted.
- **Chunk embeddings**: vectors in the named vector namespace.
- **Document metadata**: `RAGDocumentInfo` (`id`, `namespace`, `chunk_count`,
  `metadata`).

**Compatibility**: chunks embedded under one model are not comparable with
chunks embedded under another. Changing the model is a re-ingest of the whole
corpus, not a migration.

## Test strategy

- **Unit**: each chunking strategy (boundary behaviour, overlap, `min_size`
  merging), ingest chunk counting, query ordering by score, metadata filter
  pass-through, delete chunk accounting, list pagination.
- **The embedding provider is stubbed** — RAG tests must not need a model.
- **Error tests** per function.
- **Integration**: RAG parity across transports in the standalone stage.

## Observability

- `RAGIngestResponse.ChunkCount` and `RAGDeleteDocumentResponse.ChunksRemoved`
  are the accounting signals; they should agree for a document ingested once.
- `RAGListDocumentsResponse.Total` is the corpus size.
- Ingest and delete are audited through [../audit/](../audit/).
