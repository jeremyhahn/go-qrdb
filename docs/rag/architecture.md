# RAG — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

Retrieval-augmented generation needs a corpus chunked, embedded, stored and
searched. Building that on top of raw vector CRUD means every consumer writes
the same chunker, the same ingest loop, and the same "search then fetch the
text back" join — and each writes it slightly differently.

## Shape chosen

A **primitive, not a pipeline.** qrdb owns ingest and retrieve; everything above
retrieval belongs to a higher layer.

**Ingest — qrdb's responsibility:**

```
document text
  → chunker (fixed | sentence | paragraph)
  → text chunks
  → embedding service (provider proxy)
  → vectors + KV entries (tenant-scoped)
  → vector namespace + KV backend (barrier-encrypted)
```

**Query — qrdb's responsibility:**

```
query text
  → embedding service
  → query vector
  → vector search (k-NN)
  → vector IDs
  → KV retrieval (chunk text + metadata)
  → ranked results (top-k by similarity score)
```

**Orchestration — explicitly NOT qrdb's responsibility** (it belongs at the
services layer):

```
ranked results
  → query rewriting / caching
  → result fusion (BM25, hybrid search, RRF)
  → context assembly (prompt building)
  → LLM generation
  → final response
```

That boundary is the central design decision here. A database that also does
prompt assembly and LLM calls has stopped being a database, and the composition
layer above it is where model choice, cost policy and prompt strategy actually
live.

### Alternatives rejected

- **A full RAG pipeline including generation.** Rejected on the boundary above.
- **One chunking strategy.** Rejected: prose, structured articles and
  transcript-like text genuinely want different splits, and the wrong split
  degrades retrieval more than a slightly worse embedding model does.
- **Storing chunk text only in the vector metadata.** Rejected: chunk text lives
  in the KV store and the vector carries the reference, so retrieval is a search
  plus a fetch. Metadata stays small and the vector index stays dense.
- **Automatic re-embedding on model change.** Not offered: it would be a silent,
  expensive, unbounded operation. Re-ingest is explicit.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `RAGServicer`, chunkers, ingest and query flows | `pkg/rag`, `pkg/service/rag_service.go` | go-qrdb |
| Embedding generation | `pkg/service/embedding_service.go` — see [../embeddings/](../embeddings/) | go-qrdb |
| Vector storage and k-NN | `pkg/vector` — see [../vectors/](../vectors/) | go-qrdb |
| Chunk text and metadata storage | `pkg/kv` — see [../key-value/](../key-value/) | go-qrdb |
| Orchestration, fusion, generation | — | **the services layer**, not qrdb |
| SDK surface | `sdk/go/transport` (`RAGClient`) | go-qrdb |

## Chunking strategies

| Strategy | Splits on | Use when | Cost |
|---|---|---|---|
| `fixed` | character count with overlap | prose, unstructured text | may split sentences awkwardly; overlap repeats content |
| `sentence` | sentence boundaries, `chunk_size` as a guideline | preserving semantic units matters | chunk sizes vary more; needs sentence detection |
| `paragraph` | paragraph boundaries, merging below `min_size` | clear paragraph structure (essays, articles) | paragraph size variation may exceed `chunk_size` |

## Trust boundaries

- Documents, chunks and vectors are **tenant-scoped and barrier-encrypted**.
  A sealed barrier fails ingest and query closed.
- **Ingest sends document text to the configured embedding provider**, which may
  be external. That is a data-egress decision — see
  [../embeddings/architecture.md](../embeddings/architecture.md#trust-boundaries).
- Four capabilities, four permissions (`rag:ingest`, `rag:query`,
  `rag:list_documents`, `rag:delete_document`). Ingest is a write and is granted
  separately from query.

## What this feature deliberately does not do

- **No LLM generation, prompt assembly, query rewriting, reranking or result
  fusion.** All of that is the layer above.
- **No automatic re-embedding** when the model changes.
- **No document format parsing.** Ingest takes text; extracting text from PDF or
  HTML happens before the call.
- **No cross-namespace query.** A query targets one vector namespace.
