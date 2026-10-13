<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Vector Database

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

QRDB includes a namespace-scoped vector database subsystem for similarity
search operations. The vector feature is gated behind the `qrdb_vector`
build tag. When disabled, CLI commands return a "feature disabled" error
and the GUI hides vector views based on the `capabilities.vector` flag.

## Build Tag Behavior

| Build Tag        | CLI File                          | Behavior                           |
|------------------|-----------------------------------|------------------------------------|
| `qrdb_vector`    | `cmd/qrdb/vector_enabled.go`    | Full vector command tree           |
| `!qrdb_vector`   | `cmd/qrdb/vector_stub.go`       | `newVectorCmd()` returns nil       |

The stub implementation returns nil from `newVectorCmd()`, which prevents
the `vector` subcommand from being registered in the Cobra command tree.
On the server side, the `VectorService` implementation is similarly
stubbed, returning `FeatureDisabledError` for all operations.

## Multi-Tenancy & Encryption

### Tenant-Partitioned Namespaces

Vector namespaces are **tenant-partitioned** — the physical storage key is
qualified by the request-context tenant ID, preventing namespace-name collision
between tenants. Two tenants can create a namespace named `embeddings` with no
conflict; the underlying keys are isolated as `tenant-a:embeddings` and
`tenant-b:embeddings`.

Under a `strict` cluster isolation-policy floor (a FIPS 140-3 build where
`crypto/fips140.Enabled()` is true, or an operator-raised floor — see
[multi-tenancy/architecture.md#the-isolation-policy](multi-tenancy/architecture.md#the-isolation-policy)),
namespace creation without a tenant context is rejected — no implicit shared
or system namespaces. Under `operator-access` (today's behavior, and the
default for any deployment that has not raised the floor), the `system`
tenant is the implicit owner for un-tenanted requests, suitable for
single-tenant deployments.

**Example: CreateNamespace behavior**

```
CreateNamespace(ctx, &CreateNamespaceRequest{Namespace: "embeddings"})
  → Binds the owner tenant from ctx (defaults to "system" if omitted under operator-access)
  → Under a strict floor, error if no tenant context
  → Returns namespace scoped to that tenant's isolation context
```

### Barrier Encryption at Rest

Vector data is **barrier-encrypted at rest** via the cluster `StorageBarrier` —
the same cryptographic seal that protects the KV/MVCC store. The vector
PebbleDB store's filesystem is wrapped by the barrier's encrypting FS
(`encfs`), so all persisted vector bytes — vectors, metadata, and index data —
are encrypted under the cluster barrier's data-encryption key. This ensures:

- **At-rest confidentiality**: vector data is not readable without the cluster
  barrier unsealed (matches the KV/MVCC encryption model).
- **Unified seal lifecycle**: barrier init/unseal/seal applies to the vector
  store alongside KV data.
- **Plaintext fallback only when seal is disabled**: with no barrier
  configured the vector store is plaintext, exactly as the KV store is.

Tenant isolation of vector data is provided by the **tenant-partitioned
namespace keys** described above (logical key isolation), *not* by per-tenant
encryption keys — vectors are encrypted under the single cluster `StorageBarrier`
DEK, just like KV data. (Per-tenant DEK encryption of vector data — reusing the
`BarrierRegistry`/`TenantBarrier` mechanism that backs `IsolationBarrier` tenant
KV data — is a possible future hardening, not the current behavior.)

**Example: Vector data encryption flow**

```
1. A client calls UpsertVector (tenant taken from request context)
2. Vector + metadata → Raft proposal → state machine Update
3. The namespace key is qualified by the tenant (tenant-partitioned isolation)
4. The StorageBarrier's encrypting FS encrypts the bytes written to PebbleDB
5. On Read: SyncRead → Lookup → the encrypting FS transparently decrypts
```

### Auto-Provisioning for RAG

When a RAG ingest request is processed, a tenant-scoped vector namespace is
automatically provisioned (if not already present) with sensible defaults
(dimension from the embedding model, cosine distance metric, HNSW index).
This avoids requiring explicit namespace pre-creation and ensures RAG
documents are stored in the requesting tenant's isolated namespace.

## Service Interface

Source: `pkg/service/vector_service.go`

The `VectorService` interface (aliased as `VectorServicer` for backward
compatibility) defines 20 methods organized into five categories:

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
    IndexType      string              // HNSW, FLAT, IVF
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
metadata. All write operations go through Raft consensus.

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

## Index Types

| Type       | CLI Flag   | Description                                         |
|------------|------------|-----------------------------------------------------|
| `HNSW`     | `hnsw`     | Hierarchical Navigable Small World (default)        |
| `FLAT`     | `flat`     | Brute-force exact search                            |
| `IVF`      | `ivf-flat` | Inverted file with flat quantization                |

HNSW is the default index type. It offers sub-linear search time with
tunable accuracy via the `M` (number of connections) and `efConstruction`
(build-time search breadth) parameters.

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

Write operations (upsert, delete, namespace create/delete) are proposed
through Raft consensus. Read operations (get, search, list, stats) use
the `Lookup` path with `SyncRead` for linearizable reads.

Example binary encoding for upsert:

```
[op:1][nsLen:2 LE][namespace][idLen:2 LE][id][dim:4 LE][vector:dim*4 float32 LE][metaLen:4 LE][metadata]
```

### API Endpoints

| Protocol | Source File                          |
|----------|--------------------------------------|
| gRPC     | `pkg/api/grpc/service_vector.go`     |
| REST     | `pkg/api/rest/handlers_vector.go`    |
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
qrdb vector upsert --namespace embeddings --id vec-001 \
    --vector "0.1,0.2,0.3,0.4" --metadata '{"category":"news"}'

# Retrieve a vector
qrdb vector get --namespace embeddings --id vec-001

# Delete a vector
qrdb vector delete --namespace embeddings --id vec-001

# Batch upsert from JSON file (or stdin with "-")
qrdb vector batch-upsert --namespace embeddings --file vectors.json

# Batch delete by IDs
qrdb vector batch-delete --namespace embeddings --ids "vec-001,vec-002,vec-003"

# List vectors with pagination
qrdb vector list --namespace embeddings --limit 50 --cursor <token>

# Update metadata without modifying the vector
qrdb vector update-metadata --namespace embeddings --id vec-001 \
    --metadata '{"category":"sports"}'
```

### Search

```bash
# k-NN similarity search
qrdb vector search --namespace embeddings --vector "0.1,0.2,0.3,0.4" \
    --k 10 --ef-search 100

# Search by existing vector ID
qrdb vector search-by-id --namespace embeddings --id vec-001 --k 10

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
qrdb vector compact-progress --namespace embeddings --job-id <job-id>
```

### Common Flags

| Flag                | Default   | Description                              |
|---------------------|-----------|------------------------------------------|
| `--namespace`       | required  | Target vector namespace                  |
| `--shard-id`        | `0`       | Target shard ID                          |
| `--consistency`     | `default` | Read consistency: default, leader, any   |
| `--output`          | `table`   | Output format: table, json, yaml         |

## SDK Integration

### Go SDK

```go
client, _ := sdk.NewClient(sdk.WithTransport("grpc"), sdk.WithAddress("localhost:63003"))

vectorClient := client.Vector()
if vectorClient == nil {
    // Vector feature is disabled on the server
}
```

The Go SDK `Vector()` method returns nil when the server reports that the
vector capability is disabled.

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

QRDB includes optional GPU acceleration for vector similarity search powered by NVIDIA cuVS. The feature is gated behind the `qrdb_vector && gpu` build tags. When not compiled in, GPU operations return graceful fallback errors.

### GPU Index Types

The GPU acceleration layer provides four index types optimized for NVIDIA GPUs:

| Index Type | CLI Flag | Description | Use Case |
|------------|----------|-------------|----------|
| `CAGRA` | `gpu_cagra` | Proximity graph; high-throughput approximate search | High-dimensional vectors, large-scale search |
| `IVF-Flat` | `gpu_ivf_flat` | Inverted file with flat quantization | Memory-constrained GPU; balanced precision/performance |
| `IVF-PQ` | `gpu_ivf_pq` | Inverted file with product quantization | Large billion-scale vector indexes |
| `Brute Force` | `gpu_brute_force` | Exhaustive exact search on GPU | Small indexes requiring 100% accuracy |

### When to Use Each GPU Index

**CAGRA (default GPU index):**
- Best for sub-linear search on large indexes
- Graph-based construction; excellent quality at high throughput
- Recommended for most production workloads with 10M+ vectors
- Supports dynamic insert/delete with rebuild thresholds

**IVF-Flat:**
- When GPU memory is limited but GPU compute is available
- Flat quantization preserves full precision
- Good for search + filtering workflows
- Cluster-based partitioning reduces search space

**IVF-PQ:**
- For billion-scale indexes on modest GPU memory
- Product quantization trades some accuracy for massive memory savings
- Best with very large high-dimensional embeddings
- Useful for metric learning and approximate nearest neighbor at extreme scale

**Brute Force:**
- When accuracy is critical over speed
- Exhaustive search guarantees exact k-NN results
- Suitable for small indexes (<100K vectors) or when recall = 100% is required
- No preprocessing; instant indexing of new vectors

### GPU Configuration

GPU acceleration is configured in the vector namespace creation request. The GPU layer automatically falls back to CPU (HNSW) if GPU operations fail.

**Vector namespace with GPU CAGRA index:**

```go
req := &CreateNamespaceRequest{
    Namespace:       "embeddings",
    Dimension:       1536,
    DistanceMetric:  "l2",
    IndexType:       "gpu_cagra",
    IndexConfig: map[string]string{
        "graph_degree":  "32",      // CAGRA graph connectivity
        "build_algo":    "ivf_pq",  // Build algorithm
    },
    GPUConfig: &GPUConfig{
        DeviceID:         0,           // CUDA device ordinal
        MemoryLimitBytes: 8589934592,  // 8 GB
        BuildBatchSize:   100000,      // vectors per transfer batch
        Enabled:          true,
    },
}
```

### GPU Build Requirements

Compiling with GPU support requires:

- **NVIDIA CUDA Toolkit** (version 11.0+)
- **NVIDIA cuVS** (installed via CUDA or standalone)
- **CMake** (3.18+)
- **Go** with CGO enabled

Build command:

```bash
go build -tags "qrdb_vector,gpu" ./cmd/qrdbd
```

Without the `gpu` tag, GPU index operations return `ErrGPUNotCompiled`.

### CPU Fallback

When GPU operations fail (device error, memory exhaustion, compilation), the vector database automatically uses the CPU HNSW index:

```go
// Request uses GPU CAGRA but falls back to HNSW on GPU failure
resp, err := vectorService.SearchSimilar(ctx, &SearchSimilarRequest{
    Namespace:      "embeddings",
    QueryVector:    queryVec,
    K:              10,
    Consistency:    ConsistencyLinearizable,
})
// Returns results from GPU (fast) or CPU (slower but guaranteed to work)
```

### GPU Adapter

Source: `pkg/vector/gpu/factory.go`, `pkg/vector/gpu/adapter.go`

`GPUIndexAdapter` wraps a `GPUIndex` as an `interfaces.HNSWIndex`
(`pkg/interfaces/hnsw.go`), allowing transparent fallback:

```go
type HNSWIndex interface {
    Insert(id uint64, vector []float32) error
    Delete(id uint64) error
    Search(query []float32, k int) ([]*HNSWSearchResult, error)
    Count() int64
    GetStats() *HNSWStats
}
```

GPU indexes wrap C calls to cuVS via CGO. `GPUIndexFactory.CreateIndex` builds the fallback CPU index when GPU creation fails.

### Device Management

The GPU layer provides device health checks and memory monitoring:

```go
dm, _ := gpu.NewDeviceManager(config)
dm.Initialize()
defer dm.Close()

if err := dm.HealthCheck(); err != nil {
    // GPU unavailable; use CPU fallback
}

memInfo, _ := dm.GetMemoryInfo()
fmt.Printf("GPU Memory: %d MB used / %d MB total\n",
    memInfo.UsedBytes / 1024 / 1024,
    memInfo.TotalBytes / 1024 / 1024)
```

### Performance Characteristics

GPU indexes outperform CPU indexes for large-scale similarity search:

| Index Type | Insert Latency | Search Latency (k=10) | Memory | Precision@10 |
|------------|----------------|-----------------------|--------|--------------|
| HNSW (CPU) | 50-200 µs | 5-50 ms | 2.5 GB (10M vectors) | 95%+ |
| CAGRA (GPU) | 10-50 µs | 1-10 ms | 1.2 GB (10M vectors) | 97%+ |
| IVF-Flat (GPU) | 20-100 µs | 2-20 ms | 1.5 GB (10M vectors) | 90%+ |
| IVF-PQ (GPU) | 5-30 µs | 1-15 ms | 500 MB (10M vectors) | 85%+ |
| Brute Force (GPU) | 50 µs | 20-100 ms | 2.5 GB (10M vectors) | 100% |

Benchmarks from RTX 4090; results vary with vector dimension and GPU model.

## Error Types

Source: `pkg/service/vector_errors.go`

| Error Type                | Condition                                          |
|---------------------------|----------------------------------------------------|
| `*NamespaceNotFoundError` | Namespace does not exist                           |
| `*NamespaceExistsError`   | Namespace name already in use                      |
| `*VectorNotFoundError`    | Vector ID not found in namespace                   |
| `*DimensionMismatchError` | Vector dimension does not match namespace config   |
| `*InvalidVectorError`     | Vector data is malformed or contains invalid values|

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
- [CLI Reference](api-server/cli.md) -- Full command tree including vector subcommands
- [API Reference](api-server/api.md) -- REST and gRPC endpoint specifications
- [Configuration](12-configuration.md) -- YAML configuration reference
- [Go SDK](api-server/engineering.md) -- Go client library, `Vector()` method
- [TypeScript SDK](typescript-sdk/) -- `VectorClient` interface
- [GUI System](gui/README.md) -- Vector views in the desktop/browser GUI
