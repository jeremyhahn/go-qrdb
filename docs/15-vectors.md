# Vector Database

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-22
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

QRDB includes a namespace-scoped vector database subsystem for similarity
search operations. The vector feature is gated behind the `qrdb_vector`
build tag. Without it the `qrdb vector` command group and the transport
handlers are compiled out, `pkg/capabilities` does not register `vector`, and
the GUI hides vector views based on the `capabilities.vector` flag.

## Build Tag Behavior

| Build Tag        | CLI File                          | Behavior                           |
|------------------|-----------------------------------|------------------------------------|
| `qrdb_vector`    | `cmd/qrdb/vector_enabled.go`    | Full vector command tree           |
| `!qrdb_vector`   | `cmd/qrdb/vector_stub.go`       | `newVectorCmd()` returns nil       |

The stub implementation returns nil from `newVectorCmd()`, which prevents
the `vector` subcommand from being registered in the Cobra command tree.
In the desktop GUI, `cmd/qrdb/bindings_vector_stub.go` returns
`FeatureDisabledError` from every vector binding.

## Multi-Tenancy & Encryption

### Tenant-Partitioned Namespaces

Vector namespaces are **tenant-partitioned**: the physical storage key is the
request-context tenant ID, a Unit Separator (0x1f) and the namespace name
(`scopedNamespace`, `pkg/service/vector.go`); the `system` tenant's key is the
separator and the name. Two tenants can each create a namespace named
`embeddings` with no conflict.

Under a `strict` cluster isolation-policy floor (a FIPS 140-3 build where
`crypto/fips140.Enabled()` is true, or an operator-raised floor — see
[multi-tenancy/architecture.md#the-isolation-policy](multi-tenancy/architecture.md#the-isolation-policy)),
`CreateNamespace` without a tenant context fails with `TenantRequiredError` —
no implicit shared or system namespaces. Under `operator-access`, the default
for any deployment that has not raised the floor, the `system` tenant is the
implicit owner for un-tenanted requests, suitable for single-tenant
deployments.

### Barrier Encryption at Rest

Vector data is **encrypted at rest under the node's at-rest encryptor**
(`atrest.Encryptor`, passed to the Pebble vector store with
`vectorstorage.WithEncryptor`), the same instance the MVCC state machine uses.
When `lifecycle.Infra.Encryptor` is nil, the lifecycle derives one from the
seal barrier (`atrest.New(infra.SealBarrier)`). All persisted vector bytes —
vectors, metadata and index data — go through it:

- **At-rest confidentiality**: vector data is not readable without the node's
  barrier unsealed, as for KV/MVCC data. A sealed node barrier fails vector
  operations closed.
- **Plaintext only when seal is disabled**: with no barrier configured the
  vector store is plaintext, exactly as the KV store is.

Tenant isolation of vector data is the **tenant-partitioned namespace key**
described above, *not* a per-tenant key: a tenant's barrier does not cover its
vectors, and sealing it does not fail its vector operations closed (see
[vectors/architecture.md#current-state-measured](vectors/architecture.md#current-state-measured)).

### Auto-Provisioning for RAG

When a RAG ingest request is processed, a tenant-scoped vector namespace is
automatically provisioned (if not already present) with sensible defaults
(dimension from the embedding model, cosine distance metric, HNSW index).
This avoids requiring explicit namespace pre-creation and ensures RAG
documents are stored in the requesting tenant's isolated namespace.

## Service Interface

Source: `pkg/service/vector_service.go`

The `VectorService` interface (`VectorServicer` is an alias) defines 20 methods organized into five categories:

### Namespace Management

| Method             | Request Type               | Response Type              |
|--------------------|----------------------------|----------------------------|
| `CreateNamespace`  | `*CreateNamespaceRequest`  | `*CreateNamespaceResponse` |
| `DeleteNamespace`  | `*DeleteNamespaceRequest`  | `*DeleteNamespaceResponse` |
| `ListNamespaces`   | `*ListNamespacesRequest`   | `*ListNamespacesResponse`  |
| `GetNamespaceInfo` | `*GetNamespaceInfoRequest` | `*GetNamespaceInfoResponse`|

A namespace defines the vector dimensionality, distance metric, and index
configuration. All vectors within a namespace must match the configured
dimension.

```go
type CreateNamespaceRequest struct {
    Namespace      string
    Dimension      uint32
    DistanceMetric string              // L2, COSINE, DOT_PRODUCT, MANHATTAN
    IndexType      string              // HNSW (default), FLAT, etc.
    IndexConfig    map[string]string   // HNSW: M, efConstruction
    Metadata       map[string]interface{}
    ShardID        uint64
}
```

### Vector CRUD

| Method           | Request Type                     | Response Type                     |
|------------------|----------------------------------|-----------------------------------|
| `UpsertVector`   | `*UpsertVectorRequest`           | `*UpsertVectorResponse`           |
| `GetVector`      | `*GetVectorRequest`              | `*GetVectorResponse`              |
| `DeleteVector`   | `*DeleteVectorRequest`           | `*DeleteVectorResponse`           |
| `BatchUpsert`    | `*BatchUpsertRequest`            | `*BatchUpsertResponse`            |
| `BatchDeleteVectors` | `*BatchDeleteVectorsRequest` | `*BatchDeleteVectorsResponse`     |
| `ListVectors`    | `*ListVectorsRequest`            | `*ListVectorsResponse`            |
| `UpdateMetadata` | `*UpdateVectorMetadataRequest`   | `*UpdateVectorMetadataResponse`   |

Vectors are identified by a `uint64` ID within a namespace. Each vector
stores `[]float32` embedding values along with optional `map[string]interface{}`
metadata. Writes go through Raft only when the service is given a Raft node;
no production path gives it one, so each node's store holds only the writes
that node served.

```go
type VectorData struct {
    ID        uint64
    Vector    []float32
    Metadata  map[string]interface{}
    Version   uint64
    CreatedAt int64
    UpdatedAt int64
}
```

### Search Operations

| Method             | Request Type                       | Response Type                      |
|--------------------|------------------------------------|------------------------------------|
| `SearchSimilar`    | `*SearchSimilarRequest`            | `*SearchSimilarResponse`           |
| `SearchByMetadata` | `*SearchByMetadataRequest`         | `*SearchByMetadataResponse`        |
| `HybridSearch`     | `*HybridSearchRequest`             | `*HybridSearchResponse`            |
| `SearchByID`       | `*SearchByIDRequest`               | `*SearchByIDResponse`              |

Four search modes are supported:

**Similarity search** (`SearchSimilar`): k-nearest neighbor search using a
query vector. Supports `EfSearch` tuning for HNSW, minimum score threshold,
and optional metadata pre-filtering.

```go
type SearchSimilarRequest struct {
    Namespace      string
    QueryVector    []float32
    K              int
    EfSearch       int               // HNSW: higher = more accurate, slower
    MinScore       float32
    IncludeVectors bool
    IncludeMetadata bool
    MetadataFilter map[string]interface{}
    ShardID        uint64
    Consistency    ConsistencyLevel
}
```

**Metadata search** (`SearchByMetadata`): Filter vectors by metadata
key-value pairs with configurable AND/OR operators. Returns `VectorData`
without similarity scores.

**Hybrid search** (`HybridSearch`): Combines vector similarity with
metadata filtering. Supports reranking algorithms for result fusion.

**Search by ID** (`SearchByID`): Uses an existing vector's embedding as
the query, finding its nearest neighbors. Optionally excludes the source
vector from results.

Search results include distance and similarity scores:

```go
type VectorSearchResult struct {
    ID       uint64
    Score    float32
    Vector   []float32               // included when requested
    Metadata map[string]interface{}  // included when requested
    Distance float32
    Rank     int                     // 1-based result rank
}
```

### Index Management

| Method                | Request Type                     | Response Type                        |
|-----------------------|----------------------------------|--------------------------------------|
| `RebuildIndex`        | `*RebuildIndexRequest`           | `*RebuildIndexResponse`              |
| `GetRebuildProgress`  | `*GetRebuildProgressRequest`     | `*GetRebuildProgressResponse`        |
| `GetIndexStats`       | `*GetIndexStatsRequest`          | `*GetIndexStatsResponse`             |

Index rebuilds can be synchronous or asynchronous. Async rebuilds return a
`JobID` for progress tracking. The progress response includes vectors
processed, total vectors, percent complete, and estimated time remaining.

Index statistics include performance metrics:

```go
type IndexPerformanceMetrics struct {
    AverageSearchTimeMs float64
    P50SearchTimeMs     float64
    P95SearchTimeMs     float64
    P99SearchTimeMs     float64
    TotalSearches       uint64
    CacheHitRate        float64
}
```

### Compaction

| Method                  | Request Type                     | Response Type                        |
|-------------------------|----------------------------------|--------------------------------------|
| `CompactNamespace`      | `*CompactNamespaceRequest`       | `*CompactNamespaceResponse`          |
| `GetCompactionProgress` | `*GetCompactionProgressRequest`  | `*GetCompactionProgressResponse`     |

Compaction reclaims disk space by removing tombstones and old MVCC versions
within a namespace. Like index rebuilds, compaction supports async operation
with progress tracking.

## Distance Metrics

| Metric         | CLI Flag       | Description                                |
|----------------|----------------|--------------------------------------------|
| `COSINE`       | `cosine`       | Cosine similarity (normalized dot product) |
| `L2`           | `euclidean`    | Euclidean (L2) distance                    |
| `DOT_PRODUCT`  | `dot-product`  | Raw dot product similarity                 |
| `MANHATTAN`    | --             | Manhattan (L1) distance                    |

The node-local service's HNSW index ranks `COSINE` and `DOT_PRODUCT` by those
metrics and every other value, `MANHATTAN` included, by L2
(`distanceMetricFromString`, `pkg/service/vector.go`).

## Index Types

| Type       | CLI Flag   | Description                                         |
|------------|------------|-----------------------------------------------------|
| `HNSW`     | `hnsw`     | Hierarchical Navigable Small World (default)        |
| `FLAT`     | `flat`     | Brute-force exact search                            |
| `IVF`      | `ivf-flat` | Inverted file with flat quantization                |

HNSW is the default index type. It offers sub-linear search time with
tunable accuracy via the `M` (number of connections) and `efConstruction`
(build-time search breadth) parameters. The node-local service stores and
reports the requested index type but builds an HNSW index for every namespace.

## Protocol Integration

### Binary Protocol

Source: `pkg/protocol/vector.go`

Vector operations use op codes 70-77 in the binary wire protocol:

| Op Code | Constant                   | Description                                  |
|---------|----------------------------|----------------------------------------------|
| 70      | `OpVectorNamespaceCreate`  | Create a vector namespace                    |
| 71      | `OpVectorNamespaceDelete`  | Delete a vector namespace                    |
| 72      | `OpVectorUpsert`           | Insert or replace a single vector            |
| 73      | `OpVectorBatchUpsert`      | Batch insert/replace vectors                 |
| 74      | `OpVectorDelete`           | Delete a single vector                       |
| 75      | `OpVectorBatchDelete`      | Batch delete vectors                         |
| 76      | `OpVectorUpdateMetadata`   | Update vector metadata only                  |
| 77      | `OpVectorIndexReconfigure` | Record new index parameters                  |

Index rebuild and namespace compaction are service operations
(`RebuildIndex`, `CompactNamespace`), not wire opcodes.

These opcodes are the vector engine's apply-batch encoding; nothing proposes
them yet, and production traffic uses the node-local service (see
[vectors/architecture.md#current-state-measured](vectors/architecture.md#current-state-measured)).

Binary encoding for upsert (`EncodeVectorUpsert`):

```
[op:1][nameLen:2 LE][namespace][timestampUnixNano:8 LE][hasID:1][id:8 LE][dim:4 LE][embedding:dim*4 float32 LE][metadata param block]
```

### API Endpoints

| Protocol | Source File                          |
|----------|--------------------------------------|
| gRPC     | `pkg/api/grpc/service_vector.go`     |
| REST     | `pkg/api/rest/handlers_vector.go`    |
| QUIC     | `pkg/api/quic/handlers_vector.go`    |
| MCP      | `pkg/api/mcp/tools_vector.go`        |

All API handlers delegate to the `VectorService` interface following the
project's service-layer abstraction pattern.

## CLI Commands

Source: `cmd/qrdb/vector_enabled.go`

The CLI uses map-based dispatch tables for distance metric, index type,
and consistency level parsing.

### Namespace Management

```bash
# Create a namespace with 768 dimensions using cosine similarity
qrdb vector namespace create --namespace embeddings --dimension 768 \
    --distance-metric cosine --index-type hnsw

# List all namespaces
qrdb vector namespace list

# Show namespace metadata
qrdb vector namespace info --namespace embeddings

# Delete a namespace and all vectors
qrdb vector namespace delete --namespace embeddings
```

### Vector CRUD

```bash
# Upsert a vector with metadata
qrdb vector upsert --namespace embeddings --id 1 \
    --vector "0.1,0.2,0.3,0.4" --metadata '{"category":"news"}'

# Retrieve a vector
qrdb vector get --namespace embeddings --id 1

# Delete a vector
qrdb vector delete --namespace embeddings --id 1

# Batch upsert from JSON file (or stdin with "-")
qrdb vector batch-upsert --namespace embeddings --file vectors.json

# Batch delete by IDs
qrdb vector batch-delete --namespace embeddings --ids "1,2,3"

# List vectors with pagination
qrdb vector list --namespace embeddings --limit 50 --cursor <token>

# Update metadata without modifying the vector
qrdb vector update-metadata --namespace embeddings --id 1 \
    --metadata '{"category":"sports"}'
```

### Search

```bash
# k-NN similarity search
qrdb vector search --namespace embeddings --vector "0.1,0.2,0.3,0.4" \
    --k 10 --ef-search 100

# Search by existing vector ID
qrdb vector search-by-id --namespace embeddings --id 1 --k 10

# Search by metadata filters
qrdb vector search-metadata --namespace embeddings \
    --filters '{"category":"news"}' --limit 50

# Hybrid search (similarity + metadata)
qrdb vector search-hybrid --namespace embeddings \
    --vector "0.1,0.2,0.3,0.4" --filters '{"category":"news"}' --k 10
```

### Index Maintenance

```bash
# Trigger async index rebuild
qrdb vector rebuild-index --namespace embeddings

# Check rebuild progress
qrdb vector rebuild-progress --namespace embeddings --job-id <job-id>

# Show index statistics
qrdb vector index-stats --namespace embeddings

# Trigger namespace compaction
qrdb vector compact --namespace embeddings

# Check compaction progress
qrdb vector compact-progress --namespace embeddings --operation-id <operation-id>
```

### Common Flags

| Flag                | Default   | Description                              |
|---------------------|-----------|------------------------------------------|
| `--namespace`       | required  | Target vector namespace                  |
| `--shard-id`        | `0`       | Target shard ID                          |
| `--consistency`     | (empty)   | Read consistency: default, leader, any   |
| `--output`          | `table`   | Output format: table, json, yaml         |

## SDK Integration

### Go SDK

`Client.Vector()` (`sdk/go/client.go`) returns a `transport.VectorClient`. It
never returns nil: it panics when the client's transport does not implement
`transport.VectorClient`. Against a server built without `qrdb_vector` the
calls fail, because the handlers are not registered.

### TypeScript SDK

```typescript
const client = await getClient({ baseUrl: 'https://localhost:63002' });

if (client.vector !== null) {
    const results = await client.vector.searchSimilar({
        namespace: 'embeddings',
        vector: [0.1, 0.2, 0.3, 0.4],
        k: 10,
    });
}
```

The TypeScript SDK `vector` property is `VectorClient | null`.

## GPU Acceleration

`pkg/vector/gpu` provides GPU index implementations over NVIDIA cuVS, built
under `qrdb_vector && gpu`. Under `qrdb_vector && !gpu` the same types compile
as stubs whose operations return `ErrGPUNotCompiled`.

The package is not wired into `VectorService`: `CreateNamespaceRequest` has no
GPU field, and the service builds an HNSW index for every namespace. Outside
the package, only `sdk/go/types_vector_index.go` imports it.

### GPU Index Types

| Index Type | `GPUIndexType.String()` | Description |
|------------|-------------------------|-------------|
| `GPUIndexCAGRA` | `gpu_cagra` | Proximity graph; high-throughput approximate search |
| `GPUIndexIVFFlat` | `gpu_ivf_flat` | Inverted file with flat quantization |
| `GPUIndexIVFPQ` | `gpu_ivf_pq` | Inverted file with product quantization |
| `GPUIndexBruteForce` | `gpu_brute_force` | Exhaustive exact search on GPU |

`GPUConfig` (`pkg/vector/gpu/config.go`) carries `DeviceID`,
`MemoryLimitBytes` (0 = unlimited), `BuildBatchSize` and `Enabled`.

### GPU Build Requirements

Compiling with GPU support requires (`pkg/vector/gpu/cuvs_bridge.go`):

- **NVIDIA CUDA Toolkit** 12.x or later
- **NVIDIA cuVS** (`libcuvs`)
- **Go** with CGO enabled; the bridge links `-lcuvs -lcudart -lraft`

Build command:

```bash
make build WITH_QRDB_VECTOR=1 WITH_QRDB_GPU=1
```

### GPU Index Factory

Source: `pkg/vector/gpu/factory.go`, `pkg/vector/gpu/adapter.go`

`GPUIndexFactory.CreateIndex` returns an `interfaces.HNSWIndex`
(`pkg/interfaces/hnsw.go`):

- a remote GPU index when the factory's `RemoteGPUConfig` is enabled;
- otherwise a CAGRA index wrapped in a `GPUIndexAdapter` when `GPUConfig` is
  enabled and a GPU was detected; an error creating it is returned, not
  replaced by a CPU index;
- otherwise the index built by the fallback `interfaces.HNSWIndexFactory`
  passed to `NewGPUIndexFactory`.

```go
type HNSWIndex interface {
    Insert(id uint64, vector []float32) error
    Delete(id uint64) error
    Search(query []float32, k int) ([]*HNSWSearchResult, error)
    Count() int64
    GetStats() *HNSWStats
}
```

### Device Management

`NewDeviceManager(*GPUConfig)` returns a `DeviceManager` with `Initialize`,
`HealthCheck`, `GetMemoryInfo` (`MemoryInfo`: `TotalBytes`, `UsedBytes`,
`AvailableBytes`) and `Close`.

## Error Types

Source: `pkg/service/vector_errors.go`

| Error Type                | Condition                                          |
|---------------------------|----------------------------------------------------|
| `*NamespaceNotFoundError` | Namespace does not exist                           |
| `*NamespaceExistsError`   | Namespace name already in use                      |
| `*VectorNotFoundError`    | Vector ID not found in namespace                   |
| `*DimensionMismatchError` | Vector dimension does not match namespace config   |
| `*InvalidVectorError`     | Vector data is malformed or contains invalid values|
| `*InvalidNamespaceNameError` | Namespace name is empty, too long, or contains a control character or the 0x1f separator |
| `*TenantRequiredError`    | Strict isolation floor and no tenant in the request context |
| `*VectorJobNotFoundError` | Named rebuild or compaction job does not exist      |
| `*VectorOperationError`   | Storage or index fault, not the caller's            |
| `*VectorServiceClosedError` | Service or its storage is closed                  |

All error types are struct types with `Error() string` methods, following
the project convention of typed errors only (no `fmt.Errorf`).

```go
type DimensionMismatchError struct {
    Expected int
    Actual   int
}

func (e *DimensionMismatchError) Error() string {
    return "service: dimension mismatch: expected " +
        strconv.Itoa(e.Expected) + ", got " + strconv.Itoa(e.Actual)
}
```

## Cross-References

- [Architecture](01-architecture.md) -- System overview and package dependencies
- [Vectors feature folder](vectors/architecture.md) -- the contract; wins on any disagreement
- [CLI Reference](vectors/cli.md) -- `qrdb vector` command tree
- [API Reference](vectors/api.md) -- REST, gRPC, QUIC and MCP surface
- [Configuration](12-configuration.md) -- YAML configuration reference
- [Go SDK](vectors/engineering.md#exported-surface) -- Go client library, `Vector()` method
- [TypeScript SDK](typescript-sdk/) -- `VectorClient` interface
- [GUI System](gui/README.md) -- Vector views in the desktop/browser GUI
