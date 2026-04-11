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

package hnsw

import (
	"math"
)

// DistanceMetric represents a distance function type.
type DistanceMetric int

const (
	// L2Distance represents Euclidean distance.
	L2Distance DistanceMetric = iota
	// CosineDistance represents cosine distance (1 - cosine similarity).
	CosineDistance
	// DotProductDistance represents dot product distance (negative for similarity).
	DotProductDistance
	// ManhattanDistance represents Manhattan (L1/taxicab) distance.
	ManhattanDistance
	// HammingDistance represents Hamming distance for binary vectors.
	HammingDistance
)

// DistanceFunc is a function that computes the distance between two vectors.
type DistanceFunc func(a, b []float32) float32

// distanceFuncMap provides O(1) lookup from DistanceMetric to its corresponding
// distance function. SIMD-accelerated variants are used by default where available;
// scalar fallbacks are used for metrics without SIMD implementations.
var distanceFuncMap = map[DistanceMetric]DistanceFunc{
	L2Distance:         L2SIMDVek,
	CosineDistance:     CosineSIMDVek,
	DotProductDistance: DotProductSIMDVek,
	ManhattanDistance:  ManhattanSIMD,
	HammingDistance:    Hamming,
}

// GetDistanceFunc returns the distance function for the given metric.
// Returns the SIMD-accelerated variant when available, falling back to L2
// for unknown metrics.
func GetDistanceFunc(metric DistanceMetric) DistanceFunc {
	if fn, ok := distanceFuncMap[metric]; ok {
		return fn
	}
	return L2SIMDVek
}

// L2 computes the Euclidean distance between two vectors.
// Returns sqrt(Σ(a_i - b_i)²).
func L2(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32
	for i := range a {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	return float32(math.Sqrt(float64(sum)))
}

// Cosine computes the cosine distance between two vectors.
// Returns 1 - (a·b)/(||a||*||b||).
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var dotProduct float32
	var normA float32
	var normB float32

	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 1.0 // Maximum distance for zero vectors
	}

	similarity := dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
	return 1.0 - similarity
}

// DotProduct computes the dot product between two vectors.
// Returns -Σ(a_i * b_i) (negative for use as distance).
func DotProduct(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return -sum // Negative because higher dot product means closer
}

// Manhattan computes the Manhattan (L1/taxicab) distance between two vectors.
// Returns Σ|a_i - b_i|.
func Manhattan(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32
	for i := range a {
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		sum += diff
	}
	return sum
}

// Hamming computes the Hamming distance between two binary vectors.
// Values are thresholded at 0.5: values >= 0.5 are treated as 1, values < 0.5
// are treated as 0. Returns the count of positions where the binary values differ.
func Hamming(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var count float32
	for i := range a {
		aBit := a[i] >= 0.5
		bBit := b[i] >= 0.5
		if aBit != bBit {
			count++
		}
	}
	return count
}

// L2SIMD is an optimized version of L2 using SIMD-friendly patterns.
// Modern Go compilers will auto-vectorize this when possible.
func L2SIMD(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32

	// Process in chunks of 4 for potential SIMD optimization
	i := 0
	for ; i+3 < len(a); i += 4 { //nolint:gosec // G602: Loop bounds manually checked for SIMD operations
		diff0 := a[i] - b[i] //nolint:gosec // G602: Loop bounds manually checked above (i+3 < len)
		diff1 := a[i+1] - b[i+1]
		diff2 := a[i+2] - b[i+2]
		diff3 := a[i+3] - b[i+3]

		sum += diff0*diff0 + diff1*diff1 + diff2*diff2 + diff3*diff3
	}

	// Handle remaining elements
	for ; i < len(a); i++ {
		diff := a[i] - b[i]
		sum += diff * diff
	}

	return float32(math.Sqrt(float64(sum)))
}

// CosineSIMD is an optimized version of Cosine using SIMD-friendly patterns.
func CosineSIMD(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var dotProduct, normA, normB float32

	// Process in chunks of 4
	i := 0
	for ; i+3 < len(a); i += 4 {
		//nolint:gosec // G602: Loop bounds manually checked above (i+3 < len)
		dotProduct += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
		//nolint:gosec // G602: Loop bounds manually checked above (i+3 < len)
		normA += a[i]*a[i] + a[i+1]*a[i+1] + a[i+2]*a[i+2] + a[i+3]*a[i+3]
		//nolint:gosec // G602: Loop bounds manually checked above (i+3 < len)
		normB += b[i]*b[i] + b[i+1]*b[i+1] + b[i+2]*b[i+2] + b[i+3]*b[i+3]
	}

	// Handle remaining elements
	for ; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 1.0
	}

	similarity := dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
	return 1.0 - similarity
}
