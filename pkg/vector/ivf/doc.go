//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
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
package ivf
