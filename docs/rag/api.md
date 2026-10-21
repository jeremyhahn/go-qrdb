# RAG — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Ingest document | `POST /v1/rag/ingest` | `RAGIngestDocument` | `rag.IngestDocument` | `qrdb_rag_ingest` | `RAGClient.RAGIngestDocument` | `rag:ingest` |
| Query | `POST /v1/rag/query` | `RAGQuery` | `rag.Query` | `qrdb_rag_query` | `RAGClient.RAGQuery` | `rag:query` |
| List documents | `GET /v1/rag/documents` | `RAGListDocuments` | `rag.ListDocuments` | `qrdb_rag_list_documents` | `RAGClient.RAGListDocuments` | `rag:list_documents` |
| Delete document | `POST /v1/rag/documents/delete` | `RAGDeleteDocument` | `rag.DeleteDocument` | `qrdb_rag_delete_document` | `RAGClient.RAGDeleteDocument` | `rag:delete_document` |

All four are present on all six transports. Routes are registered only when the
RAG service is wired (`server.WithRAG`), which requires an embedding service.

Delete is a `POST` to `/v1/rag/documents/delete` rather than a `DELETE` on the
document path, because the request carries both a document ID and a namespace.

## Schemas

`POST /v1/rag/ingest` — `RAGIngestRequest`:

```json
{ "document_id": "manual-v2", "text": "...", "namespace": "docs",
  "model": "mxbai-embed-large", "provider": "ollama",
  "metadata": { "source": "manual.pdf", "language": "en" },
  "chunk_strategy": "paragraph", "chunk_size": 512,
  "chunk_overlap": 50, "chunk_min_size": 100 }
```

`chunk_strategy` is `fixed`, `sentence` or `paragraph`; omitted chunk fields
take the built-in defaults (`fixed`, 512, 50, 100). Response:

```json
{ "document_id": "manual-v2", "chunk_count": 37, "namespace": "docs" }
```

`POST /v1/rag/query` — `RAGQueryRequest`:

```json
{ "query": "how do I rotate the barrier key?", "namespace": "docs",
  "top_k": 5, "model": "mxbai-embed-large", "provider": "ollama",
  "filter": { "source": "manual.pdf" } }
```

`filter` narrows the search by chunk metadata. **It has no CLI flag** — it is
reachable through the SDK and this API only.

Response — `RAGQueryResponse`:

```json
{ "results": [
    { "chunk_text": "...", "score": 0.87,
      "document_id": "manual-v2", "chunk_index": 12,
      "metadata": { "source": "manual.pdf" } } ] }
```

Results are ordered by `score`, best first, bounded by `top_k`. The score's
scale follows the vector namespace's distance metric.

`GET /v1/rag/documents` — `namespace`, `limit`, `offset`. Response:

```json
{ "documents": [ { "id": "manual-v2", "namespace": "docs",
                   "chunk_count": 37, "metadata": {} } ],
  "total": 128 }
```

`POST /v1/rag/documents/delete` — `{"document_id": ..., "namespace": ...}`.
Response: `{"document_id": ..., "chunks_removed": 37}`.

## Pagination

`RAGListDocuments` is **offset-based**: `limit` and `offset`, with `total`
reporting the corpus size. `limit: 0` means no limit. Query results are bounded
by `top_k` and are not paginated.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Unknown vector namespace on query | 404 | `NOT_FOUND` — `*NamespaceNotFoundError`; ingest creates the namespace on first use |
| Unknown document on delete | 404 | `NOT_FOUND` — `*RAGDocumentNotFoundError` |
| Chunking or embedding failure during ingest | 500 | `INTERNAL` — `*RAGIngestError` |
| Document over `max_document_size` | 500 | `INTERNAL` — `*RAGIngestError` wrapping `*rag.DocumentTooLargeError`, refused before chunking |
| Unknown embedding provider | 404 | `NOT_FOUND` — nothing is stored |
| Service closed | 503 | `UNAVAILABLE` — `*RAGServiceClosedError` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| No RAG service configured | 404 (route unregistered) | `UNIMPLEMENTED` |

## Scope note

This API stops at ranked results. Query rewriting, result fusion (BM25, hybrid,
RRF), context assembly and LLM generation are the services layer's
responsibility and have no endpoint here — see
[architecture.md](architecture.md).

## Versioning

RAG REST paths are under `/v1/`. Nothing here is deprecated.
