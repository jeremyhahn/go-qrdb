//go:build qrdb_vector && gpu

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

package gpu

import (
	"math/rand"
	"testing"
)

const (
	benchDimension = 128
	benchCorpus    = 10000
	benchK         = 10
)

func makeRandomVectors(n, dim int) map[uint64][]float32 {
	vectors := make(map[uint64][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rand.Float32()
		}
		vectors[uint64(i+1)] = v
	}
	return vectors
}

func makeRandomQuery(dim int) []float32 {
	q := make([]float32, dim)
	for i := range q {
		q[i] = rand.Float32()
	}
	return q
}

func BenchmarkCAGRAIndex_Search(b *testing.B) {
	cfg := DefaultGPUConfig()
	idx, err := NewCAGRAIndex(cfg, benchDimension, "l2")
	if err != nil {
		b.Fatalf("NewCAGRAIndex: %v", err)
	}
	defer idx.Close()

	if err := idx.Build(makeRandomVectors(benchCorpus, benchDimension)); err != nil {
		b.Fatalf("Build: %v", err)
	}

	query := makeRandomQuery(benchDimension)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := idx.Search(query, benchK); err != nil {
			b.Fatalf("Search: %v", err)
		}
	}
}

func BenchmarkBruteForceGPU_Search(b *testing.B) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, benchDimension, "l2")
	if err != nil {
		b.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	if err := idx.Build(makeRandomVectors(benchCorpus, benchDimension)); err != nil {
		b.Fatalf("Build: %v", err)
	}

	query := makeRandomQuery(benchDimension)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := idx.Search(query, benchK); err != nil {
			b.Fatalf("Search: %v", err)
		}
	}
}

func BenchmarkIVFFlatGPU_Search(b *testing.B) {
	cfg := DefaultGPUConfig()
	idx, err := NewIVFFlatGPU(cfg, benchDimension, 256, "l2")
	if err != nil {
		b.Fatalf("NewIVFFlatGPU: %v", err)
	}
	defer idx.Close()

	trainingVecs := make([][]float32, benchCorpus)
	for i := range trainingVecs {
		v := make([]float32, benchDimension)
		for j := range v {
			v[j] = rand.Float32()
		}
		trainingVecs[i] = v
	}
	if err := idx.Train(trainingVecs); err != nil {
		b.Fatalf("Train: %v", err)
	}
	if err := idx.Build(makeRandomVectors(benchCorpus, benchDimension)); err != nil {
		b.Fatalf("Build: %v", err)
	}

	query := makeRandomQuery(benchDimension)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := idx.Search(query, benchK); err != nil {
			b.Fatalf("Search: %v", err)
		}
	}
}

func BenchmarkIVFPQGPU_Search(b *testing.B) {
	cfg := DefaultGPUConfig()
	// pqDim=16 sub-spaces with 8-bit codes for 128-dim vectors.
	idx, err := NewIVFPQGPU(cfg, benchDimension, 256, 16, 8, "l2")
	if err != nil {
		b.Fatalf("NewIVFPQGPU: %v", err)
	}
	defer idx.Close()

	trainingVecs := make([][]float32, benchCorpus)
	for i := range trainingVecs {
		v := make([]float32, benchDimension)
		for j := range v {
			v[j] = rand.Float32()
		}
		trainingVecs[i] = v
	}
	if err := idx.Train(trainingVecs); err != nil {
		b.Fatalf("Train: %v", err)
	}
	if err := idx.Build(makeRandomVectors(benchCorpus, benchDimension)); err != nil {
		b.Fatalf("Build: %v", err)
	}

	query := makeRandomQuery(benchDimension)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := idx.Search(query, benchK); err != nil {
			b.Fatalf("Search: %v", err)
		}
	}
}

func BenchmarkCAGRAIndex_Insert(b *testing.B) {
	cfg := DefaultGPUConfig()
	idx, err := NewCAGRAIndex(cfg, benchDimension, "l2")
	if err != nil {
		b.Fatalf("NewCAGRAIndex: %v", err)
	}
	defer idx.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		v := make([]float32, benchDimension)
		for j := range v {
			v[j] = rand.Float32()
		}
		if err := idx.Insert(uint64(i+1), v); err != nil {
			b.Fatalf("Insert: %v", err)
		}
	}
}
