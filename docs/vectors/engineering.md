# Vector Database — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> Sections headed **Target** are the vector half of the multi-model database
> program — see [architecture.md](architecture.md) and
> [../databases/engineering.md](../databases/engineering.md). They are pending
> owner review and not implemented. OD-n refers to the open decisions in
> [../databases/architecture.md](../databases/architecture.md).

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
- **Shard ID selects nothing** (measured). Namespaces live in one node-local
  store; see [architecture.md](architecture.md) "Current state". Removed in the
  target design (OD-14).
- **Vector payloads are encrypted at rest under the node's encryptor** and
  partitioned by a `<tenant>/<db>/` name prefix. A sealed node barrier fails
  vector operations closed; a sealed tenant or database barrier does not
  (measured). The target design puts them under the database binding's
  encryptor.

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
| Target: namespace index still loading after open | `service.VectorIndexWarmingError`, retryable | **yes** — never a partial answer |
| Target: write after the database's drop fence | the guard refusal | **yes** |

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
  setting for a caller of the package. A fixed seed across a fleet would have
  every node make the same unlucky choice on the same data; the randomness is
  doing real work.
- **Target: a namespace in a database binding never uses the clock seed.** The
  engine derives the seed from the namespace and its training generation, so
  every replica trains the same centroids from the same records (see "Target:
  determinism" below). Different namespaces still get different draws, which is
  the property the clock seed was protecting.
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

**The duplicate centroid this uniform draw can produce is repaired by a
mechanism BOTH refinement loops now share:
`pkg/vector/kmeans.RepairEmptyClusters`.** ivf's `handleEmptyClusters` (see
above) is now a one-line call into it; PQ's `kmeansCluster` gained the same
call it did not have before, at the same point in its own refinement loop —
after the loop, using whatever `assignments`/`counts` it left behind, exactly
as ivf's own call does. `pkg/vector/kmeans` was extracted rather than PQ
reimplementing ivf's repair a second time: it takes vectors, assignments,
per-cluster sizes, centroids and a `func(a, b []float32) float32` distance
callback — needing no true metric, since it only ever compares outputs to
find a maximum. The two callers do not even agree on what that callback
computes: PQ passes `l2Squared`, its own hardcoded squared distance, while
ivf passes `km.distanceFunc` — the index's **configured** metric (`L2`,
`Cosine`, or the signed `DotProduct`), raw and unsquared, whichever one
`Config.DistanceMetric` selected. `RepairEmptyClusters` is written to that
looser contract on purpose: it only ever compares two calls' outputs to each
other, never assumes a particular scale, sign, or squaring, and has no
imports of its own besides the standard library, so nothing importing it can
create a cycle back to either caller.

The repair moves the single farthest displaceable point into each empty
cluster, in turn, so a duplicate centroid is transient rather than permanent
for BOTH implementations now, **provided the training data actually contains
enough distinct positions to fill every cluster**.

**The donor search considers EVERY cluster with more than one member, not
only the largest one.** Restricting the search to the single largest cluster
was a real bug caught by review, not a simplification: a cluster can be large
in COUNT while every one of its points sits exactly on its own centroid (a
mass of exact duplicates), in which case every distance in it is zero and the
largest cluster has nothing usable to donate — even though a SMALLER cluster
elsewhere holds a point that is genuinely far from its own centroid and would
happily fill the empty one. A largest-cluster-only search picks the large,
degenerate one, finds no usable donor, and gives up without ever looking
elsewhere.
`TestRepairEmptyClusters_LargestClusterIsDegenerate` (`pkg/vector/kmeans`)
and `TestKMeans_HandleEmptyClusters_LargestClusterIsDegenerate`
(`pkg/vector/ivf`) both pin this exact shape — a large duplicate-cored
cluster beside a smaller but genuinely spread one — and were verified by hand
to fail against the largest-cluster-only version.

**The donor search also does not assume distFunc is non-negative.** It seeds
its running maximum from the first eligible candidate rather than a fixed
zero, and compares candidates only against each other. A version seeded at a
fixed zero is a second bug review caught: `ivf.KMeans`'s own call passes its
signed `DistanceFunc` (`DotProductDistance`, where a MORE NEGATIVE value is
CLOSER — see below), and a cluster whose points are all reasonably close can
have every one of its raw distances negative, in which case a
zero-seeded threshold rejects every candidate and reports no donor even
though a real one exists. `TestRepairEmptyClusters_SignedDistanceFunction`
pins this with a synthetic signed distance function and was verified by hand
to fail against the zero-seeded version.

A vector whose value is **identical to its own centroid is excluded from the
donor search** rather than rejected after it. Moving such a vector could only
duplicate a value that already exists, so it is never a candidate, and a
genuinely displaceable vector elsewhere still wins even when an identical one
would score as "farthest" (which a signed metric such as `DotProductDistance`
can produce). A cluster is therefore left exactly as it was found (no
reassignment, no size change) only when no eligible candidate remains: every
splittable cluster consists of vectors identical to their own centroid (the
totally-degenerate case), every candidate distance was NaN, or no cluster has
more than one member. `TestRepairEmptyClusters_IdenticalCandidateDoesNotBlockAnotherDonor`
pins the exclusion under a signed metric;
`TestRepairEmptyClusters_YieldsKDistinctCentroidsWhereDataAllows` pins the
achievable case (k distinct centroids when the data has them); and
`TestRepairEmptyClusters_CannotExceedTheDistinctPointsAvailable` pins the
boundary, asserting assignments and cluster sizes are left completely
untouched when the data has only one distinct position.

**A candidate distance that is NaN is skipped outright, and one that is
±Inf is clamped to a finite sentinel, the same way `clampedMin` protects
ivf and PQ's own initialisation from the identical overflow (see above).**
ivf's configurable distance functions can produce either: `Cosine` returns
NaN for large-but-finite input (dividing one float32 overflow by another is
Inf/Inf), and `DotProductDistance` can overflow to a clean `+Inf` or `-Inf`.
A NaN candidate cannot be meaningfully compared as farther or nearer than
anything — IEEE 754 makes both `x > NaN` and `NaN > x` false — so accepting
one as the running maximum (an earlier version of this code did, for
whichever candidate happened to be scanned first) poisons every later
comparison and the search silently keeps the first candidate scanned
instead of the true farthest one, regardless of scale.
`TestRepairEmptyClusters_SkipsNaNCandidates` reproduces this with
`Cosine({1e20, 0}, {5e19, 0})` — perfectly finite input — and was verified by
hand to fail against the version that does not skip NaN.
Clamping cannot change the ORDER of candidates (-Inf < any finite value <
+Inf either way); what it changes is ties. A clamped `+Inf` ties with, rather
than beats, an earlier `+MaxFloat32` candidate, and a clamped `-Inf` ties with
a later `-MaxFloat32`. `TestRepairEmptyClusters_ClampTiesInfinityWithTheFiniteBound`
and `TestRepairEmptyClusters_ClampTiesNegativeInfinityWithTheFiniteBound` pin
exactly those ties, and each fails if its clamp branch is removed;
`TestRepairEmptyClusters_ClampsInfiniteCandidates` additionally checks that no
literal infinity reaches a centroid.

`quantization`'s own suite additionally pins the specific call site PQ's
`kmeansCluster` uses —
`TestKMeansCluster_RepairsEmptyClustersWiredWithL2Squared` calls
`kmeans.RepairEmptyClusters` directly with PQ's exact arguments, and
`TestKMeansCluster_RepairsEmptyClusterThroughRealRefinement` calls the real
production function containing PQ's call site
(`refineAndRepair`) with hand-picked initial centroids chosen to force an
empty cluster deterministically — since k-means++ initialization is
specifically designed to avoid leaving an empty cluster behind in the first
place (an exhaustive seed search across several adversarial datasets, over
50,000 seeds through `kmeansCluster`'s own public entry point, found none),
which is consistent with ivf's own measurement of its analogous `kmeans++`
configuration elsewhere in this document: 0 degenerate runs in 60,000 seeds,
against thousands for `random` initialization, which PQ does not offer.

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
computation that overflows float32, or that produces `NaN`, can never replace
a smaller (or the sentinel) value and propagate into the weighted-sampling
sum. An unconditional assignment would let a single overflowing pair make one
point's sampling weight `+Inf`; multiplying that into a random draw that
happens to land on exactly zero produces `NaN`, against which every
subsequent `>=` comparison is false, silently defaulting selection to the
last-scanned vector instead of an actual weighted choice.

`ivf.KMeans`'s `clampedMin` clamps BOTH `+Inf` and `-Inf` (as well as `NaN`)
to a finite bound (`±math.MaxFloat32`) before ever comparing a candidate —
not `+Inf` alone. `DotProductDistance`'s raw output is signed (a MORE
NEGATIVE value is CLOSER), and its own float32 accumulation can overflow to
`-Inf` from perfectly finite, merely large-magnitude and well-aligned input
just as validly as it can overflow to `+Inf` from anti-aligned input. An
unclamped `-Inf` compares less than every finite value — including the
sentinel — so it would be accepted as a legitimately closer point rather than
recognised as the overflow artifact it is, and squaring it back into a weight
reproduces exactly the `+Inf` failure the clamp exists to prevent.
`quantization.kmeansCluster`'s `clampedMin` only needs the `+Inf`/NaN side:
its distance function, `l2Squared`, is unsigned by construction and cannot
overflow negative. `TestClampedMin_OverflowAndNaN` (in both `pkg/vector/ivf`
and `pkg/vector/quantization`) pins each package's `clampedMin` directly and
deterministically; `TestKMeans_DotProductOverflow_ClampedToFiniteWeight` and
`TestKMeans_InitializeKMeansPlusPlus_DotProductOverflow` exercise the ivf
`-Inf` case specifically, through the real `DotProduct` function rather than
a synthetic `-Inf` literal; `TestKMeansPlusPlusInit_OverflowDistanceClamped`
runs PQ's initializer against vectors whose pairwise distance genuinely
overflows float32, comparing it seed-for-seed against an unclamped reference
oracle to confirm the two select identical centroids on ordinary input and
diverge once the overflow is real.

**Squaring the clamped minimum is a second, independent overflow hazard in
`ivf.KMeans`, and clamping alone does not close it.** `clampedMin` only
prevents an already-non-finite DISTANCE from replacing a smaller one; it does
nothing to stop the SQUARING of a large-but-finite value — including its own
sentinel, `math.MaxFloat32` — from overflowing float32's ~3.4e38 range on its
own (`math.MaxFloat32 * math.MaxFloat32` computed in float32 is itself
`+Inf`). The k-means++ sampling weight is therefore computed by
`squaredWeight`, which squares in float64 (headroom to ~1.16e77, against a
~1.8e308 ceiling) rather than float32.
`TestSquaredWeight_LargeFiniteDistanceNoOverflow` pins this directly: values
that overflow when squared in plain float32 arithmetic do not overflow
through `squaredWeight`. `quantization.kmeansCluster` has no equivalent
second step: `l2Squared` is already the squared distance in one step, so
there is no separate "square the clamped minimum" operation to protect.

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

Shipped: vectors, their metadata and namespace records are stored in the
node-local Pebble vector store, under the node's encryptor. ANN indexes are
in memory only and are not reloaded at startup (see architecture.md "Current
state"). Compaction reclaims space from deleted vectors; rebuild reconstructs
the ANN index from the stored vectors.

## Target: the vector engine

### Commands

Every vector write is a command in the extended protocol, proposed to the
database binding's host and applied by the engine. Opcode numbers are assigned
in the implementing change and are wire format from then on.

| Command | Applies |
|---|---|
| `VectorNamespaceCreate` | namespace record with geometry, index parameters, `seq = 0`, `next_id = 1`, training generation 0 |
| `VectorNamespaceDelete` | deletes the namespace record, every record, metadata index entry and mutation-log entry under it |
| `VectorUpsert`, `VectorBatchUpsert` | records, metadata index entries, `seq`, `next_id` for omitted IDs, count and byte totals, mutation-log entries |
| `VectorDelete`, `VectorBatchDelete` | the same, removing |
| `VectorUpdateMetadata` | the record's metadata and its index entries; no ANN change |
| `VectorIndexReconfigure` | new index parameters and training generation; each replica rebuilds its index locally |

Each is wrapped by the guarded-write wrapper carrying the database's drop fence,
exactly like a KV write. Reads — get, list, the four searches, namespace info,
index stats — are engine lookups through the host with the request's
consistency level.

`RebuildIndex` with unchanged parameters is a node-local rebuild on the serving
node; with changed parameters it proposes `VectorIndexReconfigure`. Rebuild and
compaction job handles are node-local: the progress call is routed to the node
that issued the handle, and a handle that node no longer knows (after a restart
or a leader change) is not-found, after which `GetIndexStats` reports the
index's state.

### Keys in the database's store

Reserved internal prefixes, covered by the internal-key guard so they never
appear in KV enumeration:

```
__vec:ns/<ns>                                     namespace record
__vec:v/<ns>/<id: 8 bytes big-endian>             vector record: embedding, metadata, version, timestamps
__vec:md/<ns>/<field> 0x00 <value> 0x00 <id: 8 bytes>  metadata equality index entry
__vec:ml/<ns>/<seq: 8 bytes big-endian>           mutation-log entry: operation and ID
```

Namespace names follow DNS-label rules, so they contain no `/` and no zero
byte. No tenant or database prefix is part of any key: the store belongs to one
database.

### Apply and the index

1. The handler validates against the namespace record read through the apply
   batch (dimension, geometry, quotas), and writes every key it changes into the
   batch. A validation failure is an application-level refusal in the result,
   not an apply fault, so the log keeps advancing.
2. The batch commits with the entry.
3. After the commit, in log order, the engine applies the entry's index effect
   to the in-memory index: insert, update or delete by ID. An index error here
   marks the namespace for rebuild; it never fails the committed entry.

The mutation log keeps the last N entries per namespace (N is a node setting
with a default in the tens of thousands) and is trimmed in apply by sequence,
so every replica holds the same window.

### Checkpoints

- Path: `<binding data root>/vector-index/<ns>/<seq>.idx`, written to a temporary
  name, synced, and renamed; older checkpoints for the namespace are removed
  after the rename.
- Content: index format version, namespace sequence, index parameters and
  training generation, the serialized index. Encrypted under the binding's
  encryptor, because a checkpoint holds every embedding in the namespace.
- Written on clean close, before a state-machine snapshot, and when the
  namespace's sequence has advanced by a configured amount since the last
  checkpoint — off the apply goroutine, under the namespace's index read lock.
- A `memory` binding writes none. A drop removes them with the store.

### Open

For each namespace record: load the newest checkpoint whose format version is
current and whose sequence is at least the oldest mutation-log sequence; replay
the log from the checkpoint's sequence; otherwise rebuild from the records.
Until a namespace is ready, searches on it return a retryable
`service.VectorIndexWarmingError` (new); get, list and metadata search work
from the store immediately.

### Snapshot

The state-machine snapshot of a raft binding appends one optional section per
namespace: format version, sequence, checkpoint bytes. On restore a section is
used only when its sequence equals the restored namespace record's; otherwise
the namespace is rebuilt. The section is optional in both directions, so a
snapshot without it restores correctly.

### Target: determinism

- Omitted IDs come from `next_id` in the namespace record, in apply.
- HNSW node level is `floor(-ln(u) * mL)` with `u` derived from a hash of the
  namespace and the ID, not from a random source.
- IVF k-means seeds from a hash of the namespace and its training generation.
- Timestamps in records come from the proposed command, not from the applying
  replica's clock.

### Target: what is deleted

`vectorstorage.NewPebbleVectorStorage` as the service's store, the vector store
in `pkg/server/lifecycle`, `shard.ShardVector` and `service.VectorShardID`,
`statemachine.VectorStateMachine`, `statemachine.VectorConsensusFSM` and their
factory, `service.WithRaftNode` for vectors, `service.WithStrictTenantIsolation`
and the service's tenant scoping of namespace names, and the namespaced vector
wrapper in `pkg/server` for any database with its own store. The vector
storage schema package is kept only if the engine's record codec reuses it.

### Target: acceptance tests (lane 1B)

1. **Restart keeps search working.** Upsert, restart, search returns the same
   top-k. This fails against the shipped code, whose startup index is empty.
2. **The namespace's metric survives restart.** A cosine namespace searched
   after restart ranks by cosine.
3. **Replicas agree.** On a three-replica database, upsert through the leader
   and search with follower reads on each replica: identical top-k.
4. **Snapshot install.** A replica added after 10,000 upserts serves searches
   without a rebuild when the snapshot carries a matching checkpoint, and after
   one when it does not; both give the same results.
5. **Crash between commit and index update.** Inject the crash; after reopen the
   vector is searchable.
6. **The drop fence.** An upsert proposed before a database's drop fence and
   applied after it is refused, and the store holds no record of it.
7. **Atomicity.** A batch upsert whose apply batch is forced to fail leaves no
   record, no metadata entry and no sequence change.
8. **Every backend.** The vector conformance suite passes on memory, file,
   pebble, objstore and raft bindings.
9. **Tenant key.** Records and checkpoints of a barrier, volume or raft tenant's
   database cannot be read with the node's encryptor; sealing the tenant's
   barrier fails every vector capability closed.
10. **Deterministic IDs.** Omitted IDs are identical on every replica.

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
- **Target**: the acceptance tests above; placement is asserted by opening the
  database's own store and probing `__vec:` keys by exact key, never through the
  vector service.

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
