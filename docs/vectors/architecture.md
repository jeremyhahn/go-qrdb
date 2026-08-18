# Vector Database — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

> **Build-tag gated.** Available only in builds carrying `qrdb_vector`
> (`WITH_QRDB_VECTOR=1`). GPU acceleration adds the `gpu` tag.
> `qrdb admin capabilities` reports `vector` when it is compiled in.

## Problem

Embedding-based retrieval needs approximate nearest-neighbour search over
high-dimensional vectors, with metadata filtering, at the same durability and
tenancy guarantees as the rest of the store. Bolting on a separate vector
database means a second system to seal, back up, authorize and keep consistent
with the primary data.

## Shape chosen

Vectors live in the same store, organised into **namespaces**. A namespace fixes
its dimensionality, distance metric and index type at creation:

| Property | Values | Default |
|---|---|---|
| dimension | uint32 | — (required) |
| distance metric | `cosine`, `euclidean`, `dot-product` | `cosine` |
| index type | `hnsw`, `flat`, `ivf-flat` | `hnsw` |

Four search modes, deliberately distinct rather than one overloaded call:

- **`SearchSimilar`** — k-NN from a supplied query vector.
- **`SearchByID`** — k-NN from an existing vector's ID, so the caller does not
  round-trip the vector out and back.
- **`SearchByMetadata`** — metadata predicates only, no vector maths.
- **`HybridSearch`** — vector similarity **and** metadata filters together.

Index maintenance is explicit and asynchronous: `RebuildIndex` and
`CompactNamespace` return immediately and expose progress endpoints
(`GetRebuildProgress`, `GetCompactionProgress`).

### Alternatives rejected

- **A separate vector store alongside qrdb.** Rejected: it would need its own
  barrier, its own tenancy, its own backup path and its own authorization
  vocabulary. Vector data is exactly as sensitive as the documents it was
  derived from.
- **One `search` call with optional filters.** Rejected: the four modes have
  genuinely different cost profiles and different permission grants
  (`vector:search_similar`, `vector:search_by_id`,
  `vector:search_by_metadata`, `vector:hybrid_search`). Collapsing them would
  make "may run metadata queries" and "may run k-NN" the same grant.
- **Synchronous index rebuild.** Rejected: a rebuild on a large namespace is a
  long operation; blocking a request on it turns an ordinary client timeout into
  an abandoned rebuild with no handle to check on.
- **Mutable namespace geometry.** Rejected: changing dimension, metric or index
  type invalidates the existing index. Namespaces are created with their
  geometry and it is fixed.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Namespaces, vector CRUD, search, index maintenance | `pkg/vector` | go-qrdb |
| GPU acceleration (`gpu` tag), remote index service | `pkg/vector/gpu`, `pkg/vector/gpu/remote` | go-qrdb |
| Capability registration (`vector`) | `pkg/capabilities` | go-qrdb |
| Embedding generation | `pkg/embedding` — see [../embeddings/](../embeddings/) | go-qrdb |
| RAG pipeline built on top | `pkg/rag` — see [../rag/](../rag/) | go-qrdb |
| Underlying storage and the barrier | `pkg/storage`, `pkg/seal` | go-qrdb |
| SDK surface | `sdk/go` (`Client.Vector()`) | go-qrdb |

## Sharding

Vector operations take an optional **shard ID** (`--shard-id`, `shard_id`).
Namespaces and their indexes are per shard, which is what lets vector storage
scale horizontally with the cluster rather than pinning a namespace to one node.

## Trust boundaries

- **Vector storage is tenant-safe and barrier-encrypted.** Namespaces are
  partitioned by tenant, and vector payloads pass through the same barrier as
  everything else — see [../barrier-seal/](../barrier-seal/). A sealed barrier
  fails vector reads and writes closed.
- Each of the twenty vector capabilities carries its own `vector:*` permission,
  including the read-only ones (`vector:get_index_stats`,
  `vector:namespace_list`), so index topology is not readable by anyone with
  search access.
- Metadata filters are caller-supplied JSON; they are parsed, not interpolated.

## What this feature deliberately does not do

- **No embedding generation.** Turning text into vectors is
  [../embeddings/](../embeddings/); this feature stores and searches vectors that
  already exist.
- **No chunking or retrieval orchestration.** That is [../rag/](../rag/).
- **No namespace geometry migration.** Changing dimension, metric or index type
  means creating a new namespace and re-upserting.
- **No cross-namespace search.** A search targets exactly one namespace.
