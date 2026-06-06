<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Retrieval-Augmented Generation (RAG)

QRDB includes a thin, tenant-scoped RAG ingest/retrieve **primitive** for document chunking,
embedding generation, and similarity-based retrieval. It is NOT a complete RAG orchestration
pipeline — full orchestration (ranking strategies, query rewriting, context assembly, LLM
text generation) is the responsibility of higher layers (go-quicbaas, application code).
The RAG primitive is gated behind the `qrdb_vector` build tag.

## RAG Primitive Overview

The RAG primitive provides two core operations: **ingestion** and **retrieval**. It handles
document chunking, embedding generation, and vector search. Higher layers orchestrate ranking,
query rewriting, context assembly, and LLM generation.

**Ingest Phase (qrdb primitive responsibility):**
```
Document Text
     ↓
Document Chunker (fixed/sentence/paragraph strategy)
     ↓
Text Chunks
     ↓
Embedding Service (provider-proxy)
     ↓
Vectors + KV Store Entries (tenant-scoped)
     ↓
Vector Namespace + KV Backend (barrier-encrypted)
```

**Query Phase (qrdb primitive responsibility):**
```
Query Text
     ↓
Embedding Service (provider-proxy)
     ↓
Query Vector
     ↓
Vector Search (k-NN)
     ↓
Vector IDs
     ↓
KV Retrieval (chunk text + metadata)
     ↓
Ranked Results (k results by similarity score)
```

**Orchestration Phase (L4 responsibility, NOT qrdb):**
```
Ranked Results
     ↓
Query Rewriting / Caching
     ↓
Result Fusion (BM25, hybrid search, RRF)
     ↓
Context Assembly (prompt building)
     ↓
LLM Generation (text synthesis)
     ↓
Final Response
```

## Service Interface

Source: `pkg/service/rag_service.go`

The `RAGServicer` interface provides four methods:

```go
type RAGServicer interface {
    // IngestDocument chunks, embeds, and stores a document
    IngestDocument(ctx context.Context, req *IngestDocumentRequest) (*IngestDocumentResponse, error)

    // Query embeds a question and retrieves similar passages
    Query(ctx context.Context, req *RAGQueryRequest) (*RAGQueryResponse, error)

    // DeleteDocument removes a document and all its chunk vectors
    DeleteDocument(ctx context.Context, req *DeleteRAGDocumentRequest) (*DeleteRAGDocumentResponse, error)

    // ListDocuments returns document metadata with pagination
    ListDocuments(ctx context.Context, req *ListRAGDocumentsRequest) (*ListRAGDocumentsResponse, error)

    // Close releases all resources
    Close() error
}
```

## Chunking Strategies

Documents are split into overlapping chunks before embedding. Choose a strategy based on your document structure.

### Fixed-Size Chunking

Splits text into fixed-size chunks with overlap:

```yaml
rag:
  default_chunk_strategy: "fixed"
  default_chunk_size: 512
  default_overlap: 50
```

- **Use when:** Documents are prose or unstructured text
- **Parameters:**
  - `chunk_size`: Target number of characters per chunk (default: 512)
  - `overlap`: Number of characters to repeat between adjacent chunks (default: 50)
- **Pros:** Simple, predictable, works for most use cases
- **Cons:** May split sentences awkwardly; repeated content uses storage

### Sentence-Based Chunking

Splits on sentence boundaries, respecting `chunk_size` as a guideline:

- **Use when:** Preserving semantic units (sentences) is important
- **Parameters:**
  - `chunk_size`: Target number of characters per chunk (default: 512)
  - `overlap`: Number of sentences to repeat between chunks (default: 2)
- **Pros:** Cleaner chunk boundaries; preserves sentence integrity
- **Cons:** Chunks may vary more in size; requires sentence detection

### Paragraph-Based Chunking

Splits on paragraph boundaries:

- **Use when:** Documents have clear paragraph structure (essays, articles)
- **Parameters:**
  - `chunk_size`: Target number of characters per chunk (default: 512)
  - `min_size`: Minimum chunk size; smaller paragraphs are merged (default: 100)
- **Pros:** Semantically coherent chunks; respects document structure
- **Cons:** Paragraph size variation may exceed `chunk_size` significantly

## Configuration Reference

### Configuration Section: `rag`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `default_chunk_strategy` | string | `"fixed"` | Chunking strategy: "fixed", "sentence", "paragraph" |
| `default_chunk_size` | int | `512` | Target chunk size in characters |
| `default_overlap` | int | `50` | Overlapping characters/sentences between chunks |
| `max_document_size` | int64 | `0` | Max document size in bytes; 0 = unlimited |
| `default_top_k` | int | `10` | Default number of retrieval results |

## Ingestion Flow

### Ingest a Document

```go
resp, err := ragService.IngestDocument(ctx, &IngestDocumentRequest{
    DocumentID: "doc-001",
    Text:       "Long document text...",
    Namespace:  "documents",
    Model:      "mxbai-embed-large",
    Provider:   "ollama",
    ChunkConfig: &chunker.Config{
        Strategy:  "fixed",
        ChunkSize: 512,
        Overlap:   50,
    },
    Metadata: map[string]string{
        "source": "wiki",
        "category": "science",
    },
})

fmt.Printf("Ingested %d chunks\n", resp.ChunkCount)
```

**What happens internally:**

1. Document is split into chunks using the configured strategy
2. Each chunk is embedded using the embedding service (hits cache if available)
3. Vectors are stored in the vector namespace (via `VectorService`)
4. Chunk metadata and text are stored in KV backend with key format: `rag:chunks:{namespace}:{document_id}:{chunk_index}`
5. Document metadata is stored in KV backend with key format: `rag:documents:{namespace}:{document_id}`

## Retrieval Flow

### Query for Similar Passages

```go
resp, err := ragService.Query(ctx, &RAGQueryRequest{
    Query:     "What is machine learning?",
    Namespace: "documents",
    TopK:      5,
    Model:     "mxbai-embed-large",
    Provider:  "ollama",
    Filter: map[string]string{
        "category": "science",  // optional metadata filter
    },
})

for _, result := range resp.Results {
    fmt.Printf("Score: %.4f\nText: %s\n\n", result.Score, result.ChunkText)
}
```

**What happens internally:**

1. Query text is embedded using the embedding service
2. Vector search is performed in the namespace using the query vector
3. Top-K results are returned as vector IDs
4. Chunk text and metadata are fetched from KV backend
5. Results are sorted by similarity score (descending)

### Document Management

**List documents in a namespace:**

```go
resp, err := ragService.ListDocuments(ctx, &ListRAGDocumentsRequest{
    Namespace: "documents",
    Limit:     50,
    Cursor:    "",  // pagination token
})

for _, doc := range resp.Documents {
    fmt.Printf("ID: %s, Chunks: %d, Created: %v\n",
        doc.DocumentID, doc.ChunkCount, doc.CreatedAt)
}
```

**Delete a document and all chunks:**

```go
resp, err := ragService.DeleteDocument(ctx, &DeleteRAGDocumentRequest{
    DocumentID: "doc-001",
    Namespace:  "documents",
})

fmt.Printf("Deleted %d chunks\n", resp.ChunkCount)
```

## Transport Exposure & Authorization

The RAG pipeline is exposed on **every** API transport. Each operation is
guarded by a permission under the default-deny posture. The Unix-domain-socket
transport inherits the full gRPC surface (gRPC over UDS). RAG requires the
vector store, so it is only built under the `qrdb_vector` build tag.

| Operation | Permission | gRPC RPC (`qrdb.v1.QRDBService/`) | REST | QUIC method | MCP tool |
|-----------|-----------|-----------------------------------|------|-------------|----------|
| Ingest | `rag:ingest` | `RAGIngestDocument` | `POST /v1/rag/ingest` | `rag.IngestDocument` | `qrdb_rag_ingest` |
| Query | `rag:query` | `RAGQuery` | `POST /v1/rag/query` | `rag.Query` | `qrdb_rag_query` |
| Delete | `rag:delete_document` | `RAGDeleteDocument` | `POST /v1/rag/documents/delete` | `rag.DeleteDocument` | `qrdb_rag_delete_document` |
| List | `rag:list_documents` | `RAGListDocuments` | `GET /v1/rag/documents?namespace=&limit=&offset=` | `rag.ListDocuments` | `qrdb_rag_list_documents` |

RAG RPCs are methods on the unified `QRDBService` (not a separate service).
Sources: `pkg/api/grpc/service_rag.go`, `pkg/api/rest/handlers_rag.go`,
`pkg/api/quic/handlers_rag.go`, `pkg/api/mcp/tools_rag.go`.

Request body for `POST /v1/rag/ingest` (chunk config is supplied as flat
fields):

```json
{
  "document_id": "doc-001",
  "text": "Document content...",
  "namespace": "documents",
  "model": "mxbai-embed-large",
  "provider": "ollama",
  "chunk_strategy": "fixed",
  "chunk_size": 512,
  "chunk_overlap": 50,
  "chunk_min_size": 0,
  "metadata": { "source": "wiki", "category": "science" }
}
```

Request body for `POST /v1/rag/query`:

```json
{
  "query": "What is machine learning?",
  "namespace": "documents",
  "top_k": 5,
  "model": "mxbai-embed-large",
  "provider": "ollama",
  "filter": { "category": "science" }
}
```

### SDK access

The Go SDK exposes the RAG pipeline uniformly across every transport via
`client.RAG()` (available under the `qrdb_vector` tag), which returns a
`transport.RAGClient`:

```go
resp, err := client.RAG().RAGQuery(ctx, &transport.RAGQueryRequest{
    Query:     "what is machine learning?",
    Namespace: "documents",
    TopK:      5,
})
```

The `qrdb rag ingest|query|delete|list` CLI commands wrap the same client.

## Best Practices

### Chunk Size Tuning

- **Small chunks (256-512):** Better precision, more network overhead, higher token usage
- **Medium chunks (512-1024):** Balanced; good default for most use cases
- **Large chunks (1024+):** Fewer vectors, lower cost, may miss relevant passages

For embedded models like mxbai-embed-large, 512 characters is ideal. For larger models, 1024+ may work better.

### Overlap Configuration

- **No overlap:** Lower storage, may miss relevant passages at chunk boundaries
- **Small overlap (50):** Recommended; catches context across boundaries
- **Large overlap (100+):** Higher storage; diminishing returns

For sentence-based chunking, 2-3 sentence overlap is typically sufficient.

### TopK Selection

- **TopK = 5:** Fast, suitable for real-time queries
- **TopK = 10:** Balanced; provides good coverage for most use cases
- **TopK = 20+:** Slower, useful for exploratory search or when context window is large

Retrieve more results and let the LLM rerank if needed.

### Metadata Filtering

Use metadata filters to narrow search scope:

```go
Filter: map[string]string{
    "source": "wiki",
    "language": "en",
}
```

Filters reduce vector search space and improve relevance. Add metadata during ingestion to enable filtering during retrieval.

## Error Types

Source: `pkg/service/rag_errors.go`

| Error Type | Condition |
|------------|-----------|
| `*RAGServiceClosedError` | Service has been closed |
| `*RAGDocumentNotFoundError` | Document does not exist in namespace |
| `*RAGNamespaceNotFoundError` | Vector namespace does not exist |
| `*RAGIngestionError` | Document ingestion failed (chunking or embedding) |

## Cross-References

- [Embedding Service](../embedding/README.md) -- Generates vectors for documents and queries
- [Vector Database](../15-vectors.md) -- Stores and searches chunk vectors
- [Configuration](../12-configuration.md) -- `rag` section
- [API Reference](../api.md) -- REST endpoint specifications
