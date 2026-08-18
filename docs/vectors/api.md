# Vector Database — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column. Every capability
here requires a `qrdb_vector` build; without it the handlers are not registered
and `admin capabilities` omits `vector`.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Vector()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create namespace | `POST /vector/namespace` | `VectorCreateNamespace` | `vector.CreateNamespace` | `qrdb_vector_create_namespace` | `CreateNamespace` | `vector:namespace_create` |
| Delete namespace | `POST /vector/namespace/delete` | `VectorDeleteNamespace` | `vector.DeleteNamespace` | `qrdb_vector_delete_namespace` | `DeleteNamespace` | `vector:namespace_delete` |
| List namespaces | `GET /vector/namespaces` | `VectorListNamespaces` | `vector.ListNamespaces` | `qrdb_vector_list_namespaces` | `ListNamespaces` | `vector:namespace_list` |
| Namespace info | `GET /vector/namespace/info` | `VectorGetNamespaceInfo` | `vector.GetNamespaceInfo` | `qrdb_vector_get_namespace_info` | `GetNamespaceInfo` | `vector:namespace_info` |
| Upsert | `POST /vector/upsert` | `VectorUpsert` | `vector.Upsert` | `qrdb_vector_upsert` | `UpsertVector` | `vector:upsert` |
| Get | `GET /vector/get` | `VectorGet` | `vector.Get` | `qrdb_vector_get` | `GetVector` | `vector:get` |
| Delete | `POST /vector/delete` | `VectorDelete` | `vector.Delete` | `qrdb_vector_delete` | `DeleteVector` | `vector:delete` |
| Batch upsert | `POST /vector/batch/upsert` | `VectorBatchUpsert` | `vector.BatchUpsert` | `qrdb_vector_batch_upsert` | `BatchUpsert` | `vector:batch_upsert` |
| Batch delete | `POST /vector/batch/delete` | `VectorBatchDelete` | `vector.BatchDelete` | `qrdb_vector_batch_delete` | `BatchDeleteVectors` | `vector:batch_delete` |
| List vectors | `GET /vector/list` | `VectorList` | `vector.List` | `qrdb_vector_list` | `ListVectors` | `vector:list` |
| Update metadata | `POST /vector/metadata` | `VectorUpdateMetadata` | `vector.UpdateMetadata` | `qrdb_vector_update_metadata` | `UpdateMetadata` | `vector:update_metadata` |
| Search similar (k-NN) | `POST /vector/search/similar` | `VectorSearchSimilar` | `vector.SearchSimilar` | `qrdb_vector_search_similar` | `SearchSimilar` | `vector:search_similar` |
| Search by metadata | `POST /vector/search/metadata` | `VectorSearchByMetadata` | `vector.SearchByMetadata` | `qrdb_vector_search_by_metadata` | `SearchByMetadata` | `vector:search_by_metadata` |
| Hybrid search | `POST /vector/search/hybrid` | `VectorHybridSearch` | `vector.HybridSearch` | `qrdb_vector_hybrid_search` | `HybridSearch` | `vector:hybrid_search` |
| Search by ID | `POST /vector/search/by-id` | `VectorSearchByID` | `vector.SearchByID` | `qrdb_vector_search_by_id` | `SearchByID` | `vector:search_by_id` |
| Rebuild index | `POST /vector/index/rebuild` | `VectorRebuildIndex` | `vector.RebuildIndex` | `qrdb_vector_rebuild_index` | `RebuildIndex` | `vector:rebuild_index` |
| Rebuild progress | `GET /vector/index/rebuild/progress` | `VectorGetRebuildProgress` | `vector.GetRebuildProgress` | `qrdb_vector_get_rebuild_progress` | `GetRebuildProgress` | `vector:get_rebuild_progress` |
| Index stats | `GET /vector/index/stats` | `VectorGetIndexStats` | `vector.GetIndexStats` | `qrdb_vector_get_index_stats` | `GetIndexStats` | `vector:get_index_stats` |
| Compact namespace | `POST /vector/compact` | `VectorCompactNamespace` | `vector.CompactNamespace` | `qrdb_vector_compact_namespace` | `CompactNamespace` | `vector:compact` |
| Compaction progress | `GET /vector/compact/progress` | `VectorGetCompactionProgress` | `vector.GetCompactionProgress` | `qrdb_vector_get_compaction_progress` | `GetCompactionProgress` | `vector:get_compaction_progress` |

Twenty capabilities, all present on all six transports. The SDK method names
drop the `Vector` prefix the gRPC RPCs carry; the mapping above is the
authority.

## Schemas

Namespace creation:

```json
{ "namespace": "docs", "dimension": 768,
  "distance_metric": "cosine", "index_type": "hnsw", "shard_id": 0 }
```

`distance_metric`: `cosine` (default) · `euclidean` · `dot-product`.
`index_type`: `hnsw` (default) · `flat` · `ivf-flat`. Both immutable after
creation.

Upsert:

```json
{ "namespace": "docs", "id": "1",
  "vector": [0.12, 0.98, -0.44],
  "metadata": { "source": "manual.pdf", "page": 3 },
  "shard_id": 0 }
```

Vector IDs are `uint64`; they travel as JSON strings so large values survive
JavaScript clients. `vector` length must equal the namespace `dimension`.

Similarity search:

```json
{ "namespace": "docs", "vector": [0.1, 0.2, 0.3],
  "k": 10, "min_score": 0.75, "ef_search": 200, "shard_id": 0 }
```

`ef_search` applies to HNSW indexes only. `min_score` is a post-filter.

Hybrid search adds `filters` (a JSON metadata filter object). Metadata search
takes `filters` plus `limit`/`offset` and no vector.

Asynchronous maintenance: `RebuildIndex` returns a job ID, `CompactNamespace`
an operation ID; the corresponding progress calls take that handle and return
progress state.

## Pagination

- **`ListVectors`** — cursor-based: `limit` plus an opaque `cursor`; follow the
  returned cursor until it is empty.
- **`SearchByMetadata`** — offset-based: `limit` and `offset`.
- **Searches with `k`** are bounded by `k` and are not paginated.
- **`ListNamespaces`** is not paginated; the namespace count is bounded by an
  operator action.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Namespace exists | 409 | `ALREADY_EXISTS` — `service.NamespaceExistsError` |
| Unknown namespace | 404 | `NOT_FOUND` |
| Vector length ≠ dimension | 400 | `INVALID_ARGUMENT` |
| Unknown metric or index type | 400 | `INVALID_ARGUMENT` |
| Unknown vector ID | 404 | `NOT_FOUND` |
| Malformed metadata filter | 400 | `INVALID_ARGUMENT` |
| Unknown job/operation ID on a progress call | 404 | `NOT_FOUND` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Built without `qrdb_vector` | 404 (route not registered) | `UNIMPLEMENTED` |

## Versioning

Vector REST paths are unversioned (`/vector/...`). Nothing here is deprecated.
