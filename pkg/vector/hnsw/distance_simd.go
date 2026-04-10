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

package hnsw

import (
	"math"

	"github.com/viterin/vek/vek32"
)

// L2SIMDVek computes the Euclidean (L2) distance between two vectors using
// hardware-accelerated SIMD operations via the vek library. Falls back to
// MaxFloat32 for mismatched dimensions.
func L2SIMDVek(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}
	if len(a) == 0 {
		return 0
	}
	return vek32.Distance(a, b)
}

// CosineSIMDVek computes the cosine distance (1 - cosine similarity) between
// two vectors using hardware-accelerated SIMD operations via the vek library.
// Returns 1.0 for zero vectors (maximum distance).
func CosineSIMDVek(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}
	if len(a) == 0 {
		return 1.0
	}

	// Check for zero vectors before calling vek to avoid NaN.
	normA := vek32.Dot(a, a)
	normB := vek32.Dot(b, b)
	if normA == 0 || normB == 0 {
		return 1.0
	}

	similarity := vek32.CosineSimilarity(a, b)
	return 1.0 - similarity
}

// DotProductSIMDVek computes the negative dot product between two vectors using
// hardware-accelerated SIMD operations via the vek library. The result is negated
// so that higher similarity corresponds to lower distance.
func DotProductSIMDVek(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}
	if len(a) == 0 {
		return 0
	}
	return -vek32.Dot(a, b)
}

// ManhattanSIMD computes the Manhattan (L1/taxicab) distance between two vectors
// using hardware-accelerated SIMD operations via the vek library for the
// subtraction and absolute value steps.
func ManhattanSIMD(a, b []float32) float32 {
	if len(a) != len(b) {
		return float32(math.MaxFloat32)
	}
	if len(a) == 0 {
		return 0
	}

	// Compute element-wise difference, then absolute value, then sum.
	diff := vek32.Sub(a, b)
	vek32.Abs_Inplace(diff)
	return vek32.Sum(diff)
}
