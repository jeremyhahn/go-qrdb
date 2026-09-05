# Vector Database — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

> **Build-tag gated: `qrdb_vector`** (`WITH_QRDB_VECTOR=1`). GPU paths add
> `gpu`. Without the tag the CLI verb group and the transport handlers are
> compiled out and `pkg/capabilities` does not register `vector`.

## Exported surface

`sdk/go`:

```go
func (c *Client) Vector() transport.VectorClient
```

`sdk/go/transport`:

```go
type VectorClient interface {
	// Namespaces
	CreateNamespace(ctx context.Context, req *CreateNamespaceRequest) (*CreateNamespaceResponse, error)
	DeleteNamespace(ctx context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error)
	ListNamespaces(ctx context.Context, req *ListNamespacesRequest) (*ListNamespacesResponse, error)
	GetNamespaceInfo(ctx context.Context, req *GetNamespaceInfoRequest) (*GetNamespaceInfoResponse, error)

	// Vector CRUD
	UpsertVector(ctx context.Context, req *UpsertVectorRequest) (*UpsertVectorResponse, error)
	GetVector(ctx context.Context, req *GetVectorRequest) (*GetVectorResponse, error)
	DeleteVector(ctx context.Context, req *DeleteVectorRequest) (*DeleteVectorResponse, error)
	BatchUpsert(ctx context.Context, req *BatchUpsertRequest) (*BatchUpsertResponse, error)
	BatchDeleteVectors(ctx context.Context, req *BatchDeleteVectorsRequest) (*BatchDeleteVectorsResponse, error)
	ListVectors(ctx context.Context, req *ListVectorsRequest) (*ListVectorsResponse, error)
	UpdateMetadata(ctx context.Context, req *UpdateVectorMetadataRequest) (*UpdateVectorMetadataResponse, error)

	// Search
	SearchSimilar(ctx context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error)
	SearchByMetadata(ctx context.Context, req *SearchByMetadataRequest) (*SearchByMetadataResponse, error)
	HybridSearch(ctx context.Context, req *HybridSearchRequest) (*HybridSearchResponse, error)
	SearchByID(ctx context.Context, req *SearchByIDRequest) (*SearchByIDResponse, error)

	// Index maintenance
	RebuildIndex(ctx context.Context, req *RebuildIndexRequest) (*RebuildIndexResponse, error)
	GetRebuildProgress(ctx context.Context, req *GetRebuildProgressRequest) (*GetRebuildProgressResponse, error)
	GetIndexStats(ctx context.Context, req *GetIndexStatsRequest) (*GetIndexStatsResponse, error)
	CompactNamespace(ctx context.Context, req *CompactNamespaceRequest) (*CompactNamespaceResponse, error)
	GetCompactionProgress(ctx context.Context, req *GetCompactionProgressRequest) (*GetCompactionProgressResponse, error)
}
```

Note the naming: the SDK methods drop the `Vector` prefix that the gRPC RPCs
carry (`SearchSimilar` here, `VectorSearchSimilar` on the wire). The mapping is
one-to-one and is tabulated in [api.md](api.md).

## Namespace geometry

Fixed at creation, immutable thereafter:

| Field | Values | Default |
|---|---|---|
| `dimension` | uint32 | required |
| `distance_metric` | `cosine`, `euclidean`, `dot-product` | `cosine` |
| `index_type` | `hnsw`, `flat`, `ivf-flat` | `hnsw` |

## Invariants

- **Vector IDs are `uint64`.** The CLI takes them as strings to avoid shell and
  JSON precision loss, exactly as document IDs do in
  [../collections/](../collections/engineering.md).
- **Namespace geometry is immutable.** Changing dimension, metric or index type
  requires a new namespace and a re-upsert; there is no in-place migration.
- **Upsert is idempotent by ID** — inserting an existing ID replaces the vector
  and its metadata.
- **A vector's length must equal the namespace dimension.** A mismatch is
  refused, not padded or truncated.
- **Rebuild and compaction are asynchronous.** Both return a handle (job ID /
  operation ID) immediately; progress is polled through the corresponding
  progress call. A caller that does not poll has started an operation it cannot
  observe.
- **Search targets exactly one namespace.** There is no cross-namespace search.
- **`ef_search` applies to HNSW only** and trades recall against latency; it is
  ignored by `flat` and has no meaning for it.
- **Shard ID selects the shard**; namespaces and indexes are per shard.
- **Vector payloads are barrier-encrypted at rest** and tenant-partitioned. A
  sealed barrier fails vector operations closed.

## Search modes

| Method | Input | Cost shape |
|---|---|---|
| `SearchSimilar` | query vector + `k` | index traversal |
| `SearchByID` | existing vector ID + `k` | one fetch, then index traversal — avoids shipping the vector twice |
| `SearchByMetadata` | filter JSON + `limit`/`offset` | metadata scan, no vector maths |
| `HybridSearch` | query vector + filter JSON + `k` | traversal with filtering |

`min_score` is a post-filter on similarity; it reduces results, it does not make
the search cheaper.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_vector` | capability absent; verbs and handlers not registered | **yes** |
| Namespace already exists | `service.NamespaceExistsError` | yes |
| Unknown namespace | not-found | **yes** — never auto-created |
| Vector length ≠ namespace dimension | invalid-argument | **yes** |
| Unknown distance metric or index type | invalid-argument | **yes** |
| Unknown vector ID on get/delete/search-by-id | not-found | yes |
| Malformed metadata filter JSON | invalid-argument | **yes** |
| Progress polled for an unknown job/operation ID | not-found | yes |
| Missing `vector:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

Dimension mismatch fails rather than coercing: a silently padded vector produces
plausible-looking neighbours that are wrong, which is far worse than an error.

## Configuration

Namespace geometry is per namespace, not global. Build flags:
`WITH_QRDB_VECTOR=1` compiles the feature in; `WITH_QRDB_GPU=1` adds the `gpu`
tag and the GPU index paths (`pkg/vector/gpu`, with a remote index service
defined by `pkg/vector/gpu/remote/proto/vector_index.proto`).

### IVF clustering is randomly initialised, and `Config.Seed` makes it reproducible

`ivf.Config.Seed` fixes the randomness k-means uses to choose its initial
centroids, for both `kmeans++` and `random` initialisation.

- **Zero — the default — seeds from the clock**, and that is the production
  setting. A fixed seed across a fleet would have every node make the same
  unlucky choice on the same data; the randomness is doing real work.
- **A nonzero value makes clustering reproducible** for the same input. Use it
  when a result must be repeatable — a benchmark, or a test measuring recall
  against a threshold.

The reproducibility is over the **initialisation**, not over everything: the
same seed and the same vectors give the same centroids, but a different vector
set will not.

It matters more than it looks. `Train` clusters **once**, and every subsequent
query searches that one centroid set — so recall is a property of the
clustering, and a measurement taken without fixing the seed is one draw from a
distribution however many queries it averages.

### A test that asserts on the clustering must set a seed

An unseeded test of a randomised algorithm is a gate that flips a coin on every
run. `TestKMeans_Cluster` asserted that three centroids on twelve planted points
end up more than 1.0 apart and set no seed; measured over 60,000 seeds on that
dataset, **28 land in a local minimum where two centroids collapse to within
1.0** — about 1 in 2,143. One of them reached CI and reddened the gate on a pull
request whose diff could not touch this package, which is how the rate below
came to be measured at all.

The rule is about the assertion, not the test. Asserting an **invariant** —
that an error came back, that the centroid slice has the requested length, that
every assignment is in range — is safe unseeded, because it holds for every
initialisation. Asserting an **outcome** — where the centroids landed, which
vectors share a cluster, which vector a search returns first — needs a seed,
because it is a property of the one clustering that draw happened to find.

### The empty-cluster repair reads stale counts, and that is load-bearing

`Cluster` hands `handleEmptyClusters` the assignments and cluster sizes the
refinement loop left behind. On the `!changed` exit those describe the centroids
being returned. On the other two exits — convergence by centroid movement, and
the iteration bound — the update step replaced the centroids after the counts
were measured, so they describe the previous set.

That reads like a defect and the correction is obvious, so it was measured
before being applied, over 60,000 seeds:

| Config | Runs with stale counts | Degenerate as shipped | Degenerate if recomputed |
|---|---|---|---|
| `maxIter=100, conv=1e-4, kmeans++` | 0 | 28 | 28 |
| `maxIter=2, conv=0, random` | 2,162 | 16,356 | 17,394 |

The first row is the shipped configuration and it cannot discriminate: all
60,000 runs take the `!changed` exit, where the counts are already consistent,
so recomputing is a no-op there. The second row forces the other exits, and
recomputing is **worse by 1,038 runs in 60,000**.

The reason is that a fresh recount finds no empty cluster at all — 1,894 drops
to 0 — so the repair stops firing, and a zero-size cluster is returned holding
the all-zero vector `recomputeCentroids` leaves for a cluster it could not
average. That is a centroid at the origin no vector chose, which then attracts
every query near the origin for the life of the index. The stale counts are what
let the repair run.

So the inconsistency stays, deliberately. It is pinned by
`TestKMeans_Cluster_EmptyClusterRepairIsLoadBearing`, which goes red on exactly
that origin centroid if the recompute is added.

## Persistence

Vectors, their metadata and the namespace index are stored through
`pkg/storage`, per shard, under the barrier. Compaction reclaims space from
deleted vectors; rebuild reconstructs the ANN index from the stored vectors.
**Neither is required for correctness of search results** — they are performance
and space operations.

## Test strategy

- **Unit**, under `qrdb_vector`: namespace lifecycle, each distance metric, each
  index type, dimension-mismatch refusal, upsert idempotence, each of the four
  search modes, metadata filter parsing, and progress reporting for rebuild and
  compaction.
- **GPU tests** run under the `gpu` tag via a dedicated Makefile target and are
  not part of the default suite.
- **No `time.Sleep`** when waiting on asynchronous rebuild/compaction — progress
  is driven through the job seam.
- **Integration**: vector parity across transports in the standalone stage.

## Observability

- Namespace create/delete, rebuild and compaction are audited through
  [../audit/](../audit/).
- `GetIndexStats` is the primary operational signal: index size, vector count
  and structure health per namespace.
- Rebuild and compaction progress are the only way to observe a long-running
  maintenance operation; there is no log-only path.

## Wire-field bounds

The length-field contract for this plane — what each field can describe, that a
longer value is refused rather than truncated, that encoders return
`([]byte, error)` and validate before allocating, and that a refusal is a client
error mapping to `InvalidArgument`/400 — is one rule for the whole extended wire
protocol and is stated once, in
[../key-value/engineering.md](../key-value/engineering.md#wire-field-bounds--the-owning-contract-for-every-plane).

It is not restated here on purpose: four copies of a bound is how the four drift
apart.
