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
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"

	bloomfilter "github.com/bits-and-blooms/bloom/v3"
)

// defaultExpectedElements is used when zero is provided.
const defaultExpectedElements = 1000

// defaultFalsePositiveRate is used when an invalid rate is provided.
const defaultFalsePositiveRate = 0.01

// VectorBloomFilter provides probabilistic PK existence checks for segment pruning.
// During vector search, bloom filters can quickly eliminate segments/partitions
// that definitely do not contain target vector IDs, reducing unnecessary I/O.
type VectorBloomFilter struct {
	filter           *bloomfilter.BloomFilter
	count            atomic.Int64
	expectedElements uint
	fpRate           float64
	mu               sync.RWMutex
}

// NewVectorBloomFilter creates a bloom filter sized for the expected number of elements
// with the given false positive rate. If expectedElements is 0, defaults to 1000.
// If falsePositiveRate is invalid, defaults to 0.01 (1%).
func NewVectorBloomFilter(expectedElements uint, falsePositiveRate float64) *VectorBloomFilter {
	if expectedElements == 0 {
		expectedElements = defaultExpectedElements
	}
	if falsePositiveRate <= 0 || falsePositiveRate >= 1 {
		falsePositiveRate = defaultFalsePositiveRate
	}

	return &VectorBloomFilter{
		filter:           bloomfilter.NewWithEstimates(expectedElements, falsePositiveRate),
		expectedElements: expectedElements,
		fpRate:           falsePositiveRate,
	}
}

// Add adds a vector ID to the bloom filter.
func (f *VectorBloomFilter) Add(vectorID uint64) {
	b := uint64ToBytes(vectorID)
	f.mu.Lock()
	f.filter.Add(b)
	f.mu.Unlock()
	f.count.Add(1)
}

// Contains checks if a vector ID might be in the filter.
// Returns true if the ID might exist (may be false positive).
// Returns false if the ID definitely does not exist (never false negative).
func (f *VectorBloomFilter) Contains(vectorID uint64) bool {
	b := uint64ToBytes(vectorID)
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.filter.Test(b)
}

// AddBatch adds multiple vector IDs to the bloom filter.
func (f *VectorBloomFilter) AddBatch(vectorIDs []uint64) {
	f.mu.Lock()
	for _, id := range vectorIDs {
		f.filter.Add(uint64ToBytes(id))
	}
	f.mu.Unlock()
	f.count.Add(int64(len(vectorIDs)))
}

// ContainsBatch checks multiple vector IDs and returns those that might exist.
func (f *VectorBloomFilter) ContainsBatch(vectorIDs []uint64) []uint64 {
	result := make([]uint64, 0, len(vectorIDs))
	f.mu.RLock()
	for _, id := range vectorIDs {
		if f.filter.Test(uint64ToBytes(id)) {
			result = append(result, id)
		}
	}
	f.mu.RUnlock()
	return result
}

// Count returns the number of elements added.
func (f *VectorBloomFilter) Count() int64 {
	return f.count.Load()
}

// FalsePositiveRate returns the estimated current false positive rate based on
// the number of elements added and the filter's capacity.
func (f *VectorBloomFilter) FalsePositiveRate() float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Use the formula: (1 - e^(-kn/m))^k
	// where k = number of hash functions, n = items added, m = bits in filter.
	k := float64(f.filter.K())
	m := float64(f.filter.Cap())
	n := float64(f.count.Load())

	if m == 0 || k == 0 {
		return 1.0
	}

	return math.Pow(1-math.Exp(-k*n/m), k)
}

// Clear resets the bloom filter, removing all elements.
func (f *VectorBloomFilter) Clear() {
	f.mu.Lock()
	f.filter.ClearAll()
	f.mu.Unlock()
	f.count.Store(0)
}

// MergeWith merges another bloom filter into this one. Both filters must have
// the same bit size and number of hash functions (created with the same parameters).
func (f *VectorBloomFilter) MergeWith(other *VectorBloomFilter) error {
	if other == nil {
		return &InvalidParameterError{
			Parameter: "other",
			Reason:    "cannot merge with nil filter",
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	other.mu.RLock()
	defer other.mu.RUnlock()

	if f.filter.Cap() != other.filter.Cap() {
		return &IncompatibleFilterError{
			Reason: "filters have different bit capacities",
		}
	}
	if f.filter.K() != other.filter.K() {
		return &IncompatibleFilterError{
			Reason: "filters have different hash function counts",
		}
	}

	if err := f.filter.Merge(other.filter); err != nil {
		return &IncompatibleFilterError{
			Reason: err.Error(),
		}
	}

	f.count.Add(other.count.Load())
	return nil
}

// uint64ToBytes converts a uint64 to a byte slice using little-endian encoding.
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}
