# Vector Database — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-23

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

### k-means++ initialisation is O(n·k·d), and always returns exactly k centroids

Both k-means++ implementations in this package family —
`ivf.KMeans.initializeKMeansPlusPlus` and `quantization.kmeansCluster`'s
initialisation — pick each of the k centroids in O(n·d) by maintaining a
running per-vector minimum distance to the nearest centroid chosen so far, and
updating it with **one pass against only the newly-added centroid** each time
a centroid is chosen. Total cost is O(n·k·d).

That is not the obvious way to write it. The obvious way recomputes each
vector's minimum distance from scratch, rescanning every previously-chosen
centroid, which is O(n·k²·d): choosing the k-th centroid re-derives every
minimum already found while choosing the first k-1. At `DefaultConfig`'s 256
centroids over 1000 training vectors that is ~32.6 million distance
evaluations instead of ~256 thousand — a 128x difference that only shows up
once something amplifies it. The race detector's per-memory-access
instrumentation was that amplifier: the from-scratch form took 77 seconds for
one call under `-race`, and three of those in one `-count=3` invocation
crossed testing's 10-minute alarm, killing the test binary before it could
write `pkg/vector/ivf`'s coverage profile — which is why the failure mode was
a CI coverage gate reporting the package **missing**, not a normal test
failure.

**Both implementations always return exactly k centroids, for any input that
passes their own upfront validation** (non-empty, and at least k vectors).
That includes every degenerate case: all points identical, or fewer distinct
positions than k. The naive way to handle "every remaining point already
coincides with a chosen centroid" is to scan for one that does not — but with
fewer distinct points than k, that scan can come up empty, and skipping the
append on that iteration returns a centroid slice **shorter than k**. That
silently violates an invariant `recomputeCentroids` (ivf) and the refinement
loop (PQ) both assume: they size their per-centroid working arrays to k
unconditionally. The fix in both places is to draw uniformly at random
instead of scanning for an "unchosen" point when every remaining distance is
zero. `TestKMeans_Cluster_FewerDistinctPointsThanK` asserts this directly on
`initializeKMeansPlusPlus`'s own return value, not merely on `Cluster()`'s:
`Cluster()`'s first call to `recomputeCentroids` always allocates a full
k-length slice regardless of how many centroids initialisation actually
produced, which hides a short initializer result from any assertion made
only at the `Cluster()` level.

**The duplicate centroid this uniform draw can produce is handled
differently by the two refinement loops, and only one of them is
self-healing.** ivf's `handleEmptyClusters` (see above) moves the farthest
point of the largest cluster into an empty one once real cluster sizes are
known, so a duplicate centroid there is transient. PQ's refinement loop does
not: its own comment says so plainly — "Empty clusters keep their previous
centroid" — so a cluster that starts empty (because k-means++ handed it a
duplicate of another centroid, and no point was ever nearer to the duplicate
than to the original) stays exactly that duplicate for the rest of training
and for the life of the trained codebook. This is pre-existing PQ behavior,
not something the k-means++ initialisation fix above changed or introduced,
and it is not fixed here: it is a real reduction in effective codebook
cardinality on degenerate training data (fewer distinct vectors than
`nCentroids`), traded for not having ported ivf's largest-cluster-donor
repair into a code path this change did not otherwise need to touch.

**The running minimum must compare the metric's raw (unsquared) output,
never its square, before taking the minimum.** `DistanceMetric` is not always
non-negative — `DotProductDistance` returns a signed value where a MORE
NEGATIVE number is CLOSER (see its doc comment) — so `min(d)²` and `min(d²)`
disagree whenever a signed metric is in play: for candidate distances -10 and
3, the correct nearest-centroid distance is -10 (giving sampling weight 100),
but comparing squares first picks 3 as nearer (3²=9 < (-10)²=100) and assigns
a weight of 9 instead, silently changing the k-means++ selection distribution
for every signed metric. `ivf.KMeans` tracks the raw minimum and squares it
only once, when forming the sampling weight; `quantization.kmeansCluster`
does not have this hazard because it hardcodes `l2Squared`, which is already
non-negative. `TestKMeans_InitializeKMeansPlusPlus_SignedMetricWeights` pins
the raw-then-square ordering against an independent from-scratch reference
implementation over 2,000 seeds using `DotProductDistance`.

**The running minimum is clamped against overflow and NaN, not assigned
directly.** Both implementations track it through a small `clampedMin(current,
candidate)` helper — sentinel-initialised to `math.MaxFloat32` and only ever
lowered by `<`, the same idiom `findNearestCentroid` uses — so a distance
computation that overflows float32 to `+Inf`, or that produces `NaN`, can
never replace a smaller (or the sentinel) value and propagate into the
weighted-sampling sum. An unconditional assignment would let a single
overflowing pair make one point's sampling weight `+Inf`; multiplying that
into a random draw that happens to land on exactly zero produces `NaN`,
against which every subsequent `>=` comparison is false, silently defaulting
selection to the last-scanned vector instead of an actual weighted choice.
`TestClampedMin_OverflowAndNaN` (in both `pkg/vector/ivf` and
`pkg/vector/quantization`) pins `clampedMin` directly and deterministically;
`TestKMeansCluster_OverflowDistanceClamped` runs the full PQ initialisation
against vectors whose pairwise distance genuinely overflows float32.

### `KMeansDimensionMismatchError` guards `KMeans.Cluster` called directly

`ivf.Index.Train` validates every training vector's length against the
index's configured dimensionality before it ever calls `KMeans.Cluster`, so
callers going through the `Index` interface get `IVFDimensionMismatchError`
and never reach k-means++ with mismatched input. `KMeans.Cluster` is exported
in its own right, though, and a caller invoking it directly with a mismatched
vector would otherwise feed the distance functions' `math.MaxFloat32`
length-mismatch sentinel into k-means++'s weighted-sampling sum, where
squaring it overflows float32 to `+Inf` — exactly the overflow hazard the
clamping above exists to contain, but from a cause worth refusing outright
instead. `Cluster` therefore validates every vector's dimensionality itself
before initialising centroids and returns the typed
`api.KMeansDimensionMismatchError` (mirroring `IVFDimensionMismatchError`'s
shape: index, got, expected) rather than relying on the clamp to make a bad
input merely harmless.

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
