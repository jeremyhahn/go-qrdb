//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package ivf implements an IVF (Inverted File) index for fast approximate nearest neighbor search.
//
// The IVF index provides 10-100x faster vector search compared to flat/brute-force search
// by using k-means clustering to partition the vector space. This enables sub-linear search
// time with minimal accuracy loss.
//
// # Basic Usage
//
//	// Create and configure index
//	config := ivf.DefaultConfig(1536) // 1536 dimensions
//	idx, err := ivf.NewIndex(config)
//
//	// Train on dataset
//	err = idx.Train(trainingVectors)
//
//	// Add vectors
//	err = idx.Add("vec1", vector1, metadata)
//
//	// Search
//	results, err := idx.Search(query, 10, 20)
//	// Returns top 10 results, searching 20 clusters
//
// # Auto Configuration
//
//	// Automatically determine optimal settings
//	config := ivf.AutoConfig(1536, 100000)
//	// For 100K vectors, configures:
//	// - NumCentroids: 316 (sqrt of dataset)
//	// - DefaultNProbe: 15 (~5% of clusters)
//	// - UseQuantization: true (for large datasets)
//
// # Performance
//
// The IVF index achieves significant speedups over flat search:
//   - Training: O(k * n * d * iterations) - typically 25 iterations
//   - Search: O(nprobe * (n/k) * d) - much faster than O(n * d)
//   - Recall@10: >95% with proper nprobe tuning
//
// # Distance Metrics
//
// Supports multiple distance metrics:
//   - L2Distance: Euclidean distance (default)
//   - CosineDistance: Cosine similarity
//   - DotProductDistance: Maximum inner product search
//
// # Seeded clustering
//
// [Config].Seed fixes the randomness [KMeans] uses to choose its initial
// centroids, for both kmeans++ and random initialisation. Zero, the default,
// seeds from the clock: a fixed seed across a fleet would have every node make
// the same unlucky choice on the same data. A nonzero seed makes clustering
// reproducible for the same input; the same seed over a different vector set
// gives different centroids.
//
// [Index.Train] clusters once, and every later query searches that one
// centroid set, so recall is a property of the clustering. A recall
// measurement taken without a fixed seed is one draw from a distribution,
// however many queries it averages. A test that asserts an outcome of the
// clustering, such as where the centroids landed, which vectors share a
// cluster or which vector a search returns first, therefore sets a seed. A
// test that asserts an invariant holding for every initialisation, such as an
// error, the number of centroids or the range of every assignment, need not.
//
// # k-means++ initialisation
//
// k-means++ initialisation costs O(n·k·d). It keeps each vector's running
// minimum distance to the centroids chosen so far and updates it with one pass
// against only the newly chosen centroid, rather than rescanning every chosen
// centroid, which would cost O(n·k²·d).
//
// It returns exactly k centroids for any input that passes validation,
// including input with fewer distinct positions than k. When every remaining
// distance is zero it draws a vector uniformly at random instead of scanning
// for one not yet chosen, a scan that can come up empty and return fewer than
// k centroids to a refinement loop that sizes its per-centroid arrays to k.
// The duplicate centroid that draw can produce is repaired after refinement by
// kmeans.RepairEmptyClusters (see package pkg/vector/kmeans).
//
// The running minimum compares the distance function's raw output and squares
// it only when forming the sampling weight. [DotProductDistance] is signed, a
// more negative value being closer, and for a signed function the minimum of
// the squares is not the square of the minimum.
//
// The running minimum is clamped: NaN, +Inf and -Inf candidates are replaced by
// ±[math.MaxFloat32] before they are compared, and the minimum is only ever
// lowered. Without the clamp one overflowing pair makes a sampling weight
// +Inf, a draw landing on zero turns it into NaN, every later comparison is
// false, and selection silently falls to the last vector scanned. Dot product
// accumulation overflows to -Inf as readily as to +Inf, and an unclamped -Inf
// would be accepted as the closest point. The sampling weight is squared in
// float64, because squaring a large but finite float32 distance, the
// [math.MaxFloat32] sentinel included, overflows float32.
//
// # Empty clusters
//
// After refinement, [KMeans.Cluster] repairs empty clusters with the
// assignments and cluster sizes the refinement loop left behind. When the loop
// exits because no assignment changed, these describe the returned centroids;
// when it exits on centroid movement or on the iteration bound, the update step
// ran after they were measured, so they describe the previous centroid set.
// That is deliberate. A fresh recount finds no empty cluster, so the repair
// would not run, and a cluster of size zero would be returned holding the zero
// vector the update step leaves for a cluster it cannot average: a centroid at
// the origin that no vector chose, which then attracts every query near the
// origin for the life of the index.
//
// # Dimension validation
//
// [Index.Train] validates every training vector against the configured
// dimensionality and returns api.IVFDimensionMismatchError. [KMeans.Cluster] is
// exported in its own right and validates every vector itself, returning
// api.KMeansDimensionMismatchError (index, got, expected), rather than letting
// the distance functions' [math.MaxFloat32] length-mismatch sentinel reach the
// sampling weights.
package ivf
