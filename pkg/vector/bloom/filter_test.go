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

package bloom

import (
	"errors"
	"sync"
	"testing"
)

func TestBloomFilter_AddAndContains(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)

	ids := []uint64{1, 42, 100, 999, 12345}
	for _, id := range ids {
		bf.Add(id)
	}

	for _, id := range ids {
		if !bf.Contains(id) {
			t.Errorf("expected Contains(%d) to return true", id)
		}
	}
}

func TestBloomFilter_AddAndContainsLargeIDs(t *testing.T) {
	bf := NewVectorBloomFilter(100, 0.01)
	largeID := uint64(1<<63 - 1)
	bf.Add(largeID)

	if !bf.Contains(largeID) {
		t.Error("expected Contains to return true for large ID")
	}
}

func TestBloomFilter_NotContainsEmpty(t *testing.T) {
	bf := NewVectorBloomFilter(100, 0.01)
	if bf.Contains(42) {
		t.Error("expected empty filter to not contain any ID")
	}
}

func TestBloomFilter_FalsePositiveRate(t *testing.T) {
	n := uint(10000)
	configuredRate := 0.01
	bf := NewVectorBloomFilter(n, configuredRate)

	// Add exactly n elements.
	for i := uint64(0); i < uint64(n); i++ {
		bf.Add(i)
	}

	// Test with disjoint set.
	checks := 100000
	falsePositives := 0
	for i := uint64(n); i < uint64(n)+uint64(checks); i++ {
		if bf.Contains(i) {
			falsePositives++
		}
	}

	actualRate := float64(falsePositives) / float64(checks)
	// Allow 3x the configured rate as margin.
	if actualRate > configuredRate*3 {
		t.Errorf("actual FP rate %.4f exceeds 3x configured rate %.4f", actualRate, configuredRate)
	}
}

func TestBloomFilter_FalsePositiveRateEstimate(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)
	if bf.FalsePositiveRate() != 0 {
		t.Errorf("expected 0 FP rate for empty filter, got %f", bf.FalsePositiveRate())
	}

	for i := uint64(0); i < 1000; i++ {
		bf.Add(i)
	}

	rate := bf.FalsePositiveRate()
	if rate <= 0 || rate >= 1 {
		t.Errorf("expected FP rate between 0 and 1, got %f", rate)
	}
}

func TestBloomFilter_AddBatch(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)
	ids := []uint64{10, 20, 30, 40, 50}
	bf.AddBatch(ids)

	for _, id := range ids {
		if !bf.Contains(id) {
			t.Errorf("expected Contains(%d) to return true after AddBatch", id)
		}
	}
	if bf.Count() != int64(len(ids)) {
		t.Errorf("expected count %d, got %d", len(ids), bf.Count())
	}
}

func TestBloomFilter_AddBatchEmpty(t *testing.T) {
	bf := NewVectorBloomFilter(100, 0.01)
	bf.AddBatch(nil)
	if bf.Count() != 0 {
		t.Errorf("expected count 0 after empty AddBatch, got %d", bf.Count())
	}
	bf.AddBatch([]uint64{})
	if bf.Count() != 0 {
		t.Errorf("expected count 0 after empty slice AddBatch, got %d", bf.Count())
	}
}

func TestBloomFilter_ContainsBatch(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)
	added := []uint64{1, 2, 3, 4, 5}
	bf.AddBatch(added)

	query := []uint64{1, 3, 5, 99, 100}
	found := bf.ContainsBatch(query)

	// IDs 1, 3, 5 should definitely be found.
	foundSet := make(map[uint64]bool)
	for _, id := range found {
		foundSet[id] = true
	}

	for _, id := range []uint64{1, 3, 5} {
		if !foundSet[id] {
			t.Errorf("expected ID %d to be found in ContainsBatch result", id)
		}
	}
}

func TestBloomFilter_ContainsBatchEmpty(t *testing.T) {
	bf := NewVectorBloomFilter(100, 0.01)
	result := bf.ContainsBatch(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %d", len(result))
	}
	result = bf.ContainsBatch([]uint64{})
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %d", len(result))
	}
}

func TestBloomFilter_Clear(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)
	bf.Add(42)
	bf.Add(100)

	if bf.Count() != 2 {
		t.Errorf("expected count 2 before clear, got %d", bf.Count())
	}

	bf.Clear()

	if bf.Count() != 0 {
		t.Errorf("expected count 0 after clear, got %d", bf.Count())
	}
	if bf.Contains(42) {
		t.Error("expected Contains(42) to return false after Clear")
	}
	if bf.Contains(100) {
		t.Error("expected Contains(100) to return false after Clear")
	}
}

func TestBloomFilter_MergeWith(t *testing.T) {
	bf1 := NewVectorBloomFilter(1000, 0.01)
	bf2 := NewVectorBloomFilter(1000, 0.01)

	bf1.Add(1)
	bf1.Add(2)
	bf2.Add(3)
	bf2.Add(4)

	if err := bf1.MergeWith(bf2); err != nil {
		t.Fatalf("unexpected merge error: %v", err)
	}

	// bf1 should now contain all IDs.
	for _, id := range []uint64{1, 2, 3, 4} {
		if !bf1.Contains(id) {
			t.Errorf("expected merged filter to contain %d", id)
		}
	}

	if bf1.Count() != 4 {
		t.Errorf("expected count 4 after merge, got %d", bf1.Count())
	}
}

func TestBloomFilter_MergeWithNil(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)
	err := bf.MergeWith(nil)
	if err == nil {
		t.Error("expected error when merging with nil")
	}
	var paramErr *InvalidParameterError
	if !errors.As(err, &paramErr) {
		t.Errorf("expected InvalidParameterError, got %T", err)
	}
}

func TestBloomFilter_MergeWithIncompatible(t *testing.T) {
	bf1 := NewVectorBloomFilter(100, 0.01)
	bf2 := NewVectorBloomFilter(10000, 0.001) // Different parameters.

	err := bf1.MergeWith(bf2)
	if err == nil {
		t.Error("expected error when merging incompatible filters")
	}
	var incompErr *IncompatibleFilterError
	if !errors.As(err, &incompErr) {
		t.Errorf("expected IncompatibleFilterError, got %T", err)
	}
}

func TestBloomFilter_Count(t *testing.T) {
	bf := NewVectorBloomFilter(1000, 0.01)
	if bf.Count() != 0 {
		t.Errorf("expected initial count 0, got %d", bf.Count())
	}

	bf.Add(1)
	bf.Add(2)
	bf.Add(3)

	if bf.Count() != 3 {
		t.Errorf("expected count 3, got %d", bf.Count())
	}
}

func TestBloomFilter_CountAfterClear(t *testing.T) {
	bf := NewVectorBloomFilter(100, 0.01)
	bf.Add(1)
	bf.Clear()
	bf.Add(2)
	if bf.Count() != 1 {
		t.Errorf("expected count 1 after clear+add, got %d", bf.Count())
	}
}

func TestBloomFilter_ConcurrentAccess(t *testing.T) {
	bf := NewVectorBloomFilter(10000, 0.01)
	var wg sync.WaitGroup
	const goroutines = 100
	const opsPerGoroutine = 100

	// Concurrent adds.
	wg.Add(goroutines)
	for g := range goroutines {
		go func(offset int) {
			defer wg.Done()
			for i := range opsPerGoroutine {
				bf.Add(uint64(offset*opsPerGoroutine + i))
			}
		}(g)
	}
	wg.Wait()

	expectedCount := int64(goroutines * opsPerGoroutine)
	if bf.Count() != expectedCount {
		t.Errorf("expected count %d, got %d", expectedCount, bf.Count())
	}

	// Concurrent reads.
	wg.Add(goroutines)
	for g := range goroutines {
		go func(offset int) {
			defer wg.Done()
			for i := range opsPerGoroutine {
				bf.Contains(uint64(offset*opsPerGoroutine + i))
			}
		}(g)
	}
	wg.Wait()
}

func TestBloomFilter_ConcurrentAddAndContains(t *testing.T) {
	bf := NewVectorBloomFilter(10000, 0.01)
	var wg sync.WaitGroup

	// Concurrent mixed reads and writes.
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := uint64(0); i < 1000; i++ {
			bf.Add(i)
		}
	}()
	go func() {
		defer wg.Done()
		for i := uint64(0); i < 1000; i++ {
			bf.Contains(i)
		}
	}()
	wg.Wait()
}

func TestBloomFilter_ZeroSize(t *testing.T) {
	// Zero expected elements should use defaults.
	bf := NewVectorBloomFilter(0, 0.01)
	bf.Add(42)
	if !bf.Contains(42) {
		t.Error("expected Contains(42) to return true with zero-size filter")
	}
	if bf.Count() != 1 {
		t.Errorf("expected count 1, got %d", bf.Count())
	}
}

func TestBloomFilter_ZeroSizeInvalidRate(t *testing.T) {
	// Both zero expected elements and invalid FP rate.
	bf := NewVectorBloomFilter(0, 0)
	bf.Add(1)
	if !bf.Contains(1) {
		t.Error("expected Contains(1) to return true with zero defaults")
	}

	bf2 := NewVectorBloomFilter(0, -0.5)
	bf2.Add(1)
	if !bf2.Contains(1) {
		t.Error("expected Contains(1) to return true with negative FP rate")
	}

	bf3 := NewVectorBloomFilter(0, 1.5)
	bf3.Add(1)
	if !bf3.Contains(1) {
		t.Error("expected Contains(1) to return true with >1 FP rate")
	}
}

func TestIncompatibleFilterError(t *testing.T) {
	err := &IncompatibleFilterError{Reason: "different sizes"}
	expected := "incompatible bloom filter: different sizes"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestInvalidParameterError(t *testing.T) {
	err := &InvalidParameterError{Parameter: "other", Reason: "cannot be nil"}
	expected := "invalid bloom filter parameter other: cannot be nil"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func BenchmarkBloomFilter_Add(b *testing.B) {
	bf := NewVectorBloomFilter(uint(b.N), 0.01)
	b.ResetTimer()
	for i := range b.N {
		bf.Add(uint64(i))
	}
}

func BenchmarkBloomFilter_Contains(b *testing.B) {
	bf := NewVectorBloomFilter(100000, 0.01)
	for i := uint64(0); i < 100000; i++ {
		bf.Add(i)
	}
	b.ResetTimer()
	for i := range b.N {
		bf.Contains(uint64(i))
	}
}

func BenchmarkBloomFilter_AddBatch(b *testing.B) {
	ids := make([]uint64, 1000)
	for i := range ids {
		ids[i] = uint64(i)
	}
	bf := NewVectorBloomFilter(uint(b.N*1000), 0.01)
	b.ResetTimer()
	for range b.N {
		bf.AddBatch(ids)
	}
}

func BenchmarkBloomFilter_ContainsBatch(b *testing.B) {
	bf := NewVectorBloomFilter(100000, 0.01)
	for i := uint64(0); i < 100000; i++ {
		bf.Add(i)
	}
	ids := make([]uint64, 1000)
	for i := range ids {
		ids[i] = uint64(i)
	}
	b.ResetTimer()
	for range b.N {
		bf.ContainsBatch(ids)
	}
}
