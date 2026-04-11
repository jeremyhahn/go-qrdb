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

package ivf

import (
	"math"
)

// DistanceFunc is a function that computes the distance between two vectors.
type DistanceFunc func(a, b []float32) float32

// GetDistanceFunc returns the distance function for the given metric.
func GetDistanceFunc(metric DistanceMetric) DistanceFunc {
	switch metric {
	case L2Distance:
		return L2
	case CosineDistance:
		return Cosine
	case DotProductDistance:
		return DotProduct
	default:
		return L2
	}
}

// L2 computes the Euclidean distance between two vectors.
// Returns sqrt(Sigma(a_i - b_i)^2).
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

// L2Squared computes the squared Euclidean distance (faster, no sqrt).
// Returns Sigma(a_i - b_i)^2.
func L2Squared(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32
	for i := range a {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	return sum
}

// Cosine computes the cosine distance between two vectors.
// Returns 1 - (a*b)/(||a||*||b||).
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
// Returns -Sigma(a_i * b_i) (negative for use as distance).
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

// L2SIMD is an optimized version of L2 using SIMD-friendly patterns.
// Modern Go compilers will auto-vectorize this when possible.
func L2SIMD(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32

	// Process in chunks of 4 for potential SIMD optimization
	i := 0
	for ; i+3 < len(a); i += 4 {
		diff0 := a[i] - b[i]
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

// L2SquaredSIMD is an optimized version of L2Squared using SIMD-friendly patterns.
func L2SquaredSIMD(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}

	var sum float32

	// Process in chunks of 4
	i := 0
	for ; i+3 < len(a); i += 4 {
		diff0 := a[i] - b[i]
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

	return sum
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
		dotProduct += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
		normA += a[i]*a[i] + a[i+1]*a[i+1] + a[i+2]*a[i+2] + a[i+3]*a[i+3]
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
