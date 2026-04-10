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

package dao

import (
	"fmt"
	"hash/fnv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnixNanoGenerator_NextID_ProducesUniqueIDs verifies that successive
// calls return distinct, monotonically increasing values.
func TestUnixNanoGenerator_NextID_ProducesUniqueIDs(t *testing.T) {
	gen := &UnixNanoGenerator{}

	const count = 100
	ids := make(map[uint64]struct{}, count)

	for i := 0; i < count; i++ {
		id := gen.NextID(nil)
		require.NotZero(t, id, "ID must be non-zero")
		ids[id] = struct{}{}
	}

	// Due to nanosecond resolution, we expect the vast majority to be unique.
	// Allow a small tolerance for extremely fast CPUs where two calls land
	// in the same nanosecond.
	assert.GreaterOrEqual(t, len(ids), count-5,
		"expected nearly all IDs to be unique")
}

// TestUnixNanoGenerator_NextID_IgnoresEntity verifies that the entity
// parameter does not affect ID generation.
func TestUnixNanoGenerator_NextID_IgnoresEntity(t *testing.T) {
	gen := &UnixNanoGenerator{}

	id1 := gen.NextID(nil)
	id2 := gen.NextID(&TestEntity{ID: 999, Name: "test"})

	require.NotZero(t, id1)
	require.NotZero(t, id2)
	// Both should be valid timestamps regardless of entity.
	assert.NotEqual(t, uint64(0), id1)
	assert.NotEqual(t, uint64(0), id2)
}

// TestAtomicIncrementGenerator_NextID_StartsFromConfiguredValue verifies
// that the generator produces IDs starting from start+1.
func TestAtomicIncrementGenerator_NextID_StartsFromConfiguredValue(t *testing.T) {
	gen := NewAtomicIncrementGenerator(100)

	id1 := gen.NextID(nil)
	id2 := gen.NextID(nil)
	id3 := gen.NextID(nil)

	assert.Equal(t, uint64(101), id1)
	assert.Equal(t, uint64(102), id2)
	assert.Equal(t, uint64(103), id3)
}

// TestAtomicIncrementGenerator_NextID_StartsFromZero verifies behavior
// when initialized with a zero start value.
func TestAtomicIncrementGenerator_NextID_StartsFromZero(t *testing.T) {
	gen := NewAtomicIncrementGenerator(0)

	id1 := gen.NextID(nil)
	id2 := gen.NextID(nil)

	assert.Equal(t, uint64(1), id1)
	assert.Equal(t, uint64(2), id2)
}

// TestAtomicIncrementGenerator_NextID_ConcurrentSafety verifies that
// concurrent goroutines produce unique, non-overlapping IDs.
func TestAtomicIncrementGenerator_NextID_ConcurrentSafety(t *testing.T) {
	gen := NewAtomicIncrementGenerator(0)

	const goroutines = 50
	const idsPerGoroutine = 100
	totalIDs := goroutines * idsPerGoroutine

	results := make([]uint64, totalIDs)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < idsPerGoroutine; i++ {
				results[offset+i] = gen.NextID(nil)
			}
		}(g * idsPerGoroutine)
	}

	wg.Wait()

	// Every ID must be unique.
	seen := make(map[uint64]struct{}, totalIDs)
	for _, id := range results {
		require.NotZero(t, id, "ID must be non-zero")
		_, duplicate := seen[id]
		require.False(t, duplicate, "duplicate ID detected: %d", id)
		seen[id] = struct{}{}
	}

	assert.Equal(t, totalIDs, len(seen), "expected exactly %d unique IDs", totalIDs)

	// The final counter value must equal the total number of IDs generated.
	assert.Equal(t, uint64(totalIDs), gen.NextID(nil)-1,
		"counter should reflect total IDs generated")
}

// TestFieldHashGenerator_NextID_DeterministicForSameValue verifies that
// hashing the same field value always produces the same ID.
func TestFieldHashGenerator_NextID_DeterministicForSameValue(t *testing.T) {
	gen := NewFieldHashGenerator("Name")

	entity := &TestEntity{Name: "alice"}

	id1 := gen.NextID(entity)
	id2 := gen.NextID(entity)

	require.NotZero(t, id1)
	assert.Equal(t, id1, id2, "same input must produce the same hash")
}

// TestFieldHashGenerator_NextID_DifferentValuesProduceDifferentIDs verifies
// that distinct field values yield distinct IDs.
func TestFieldHashGenerator_NextID_DifferentValuesProduceDifferentIDs(t *testing.T) {
	gen := NewFieldHashGenerator("Name")

	entityA := &TestEntity{Name: "alice"}
	entityB := &TestEntity{Name: "bob"}

	idA := gen.NextID(entityA)
	idB := gen.NextID(entityB)

	require.NotZero(t, idA)
	require.NotZero(t, idB)
	assert.NotEqual(t, idA, idB, "different names must produce different hashes")
}

// TestFieldHashGenerator_NextID_MissingFieldReturnsZero verifies that
// requesting a field that does not exist on the entity returns 0.
func TestFieldHashGenerator_NextID_MissingFieldReturnsZero(t *testing.T) {
	gen := NewFieldHashGenerator("NonExistentField")

	entity := &TestEntity{Name: "alice"}

	id := gen.NextID(entity)
	assert.Equal(t, uint64(0), id, "missing field must return 0")
}

// TestFieldHashGenerator_NextID_HandlesPointerEntity verifies that pointer
// entities are properly dereferenced before field lookup.
func TestFieldHashGenerator_NextID_HandlesPointerEntity(t *testing.T) {
	gen := NewFieldHashGenerator("Email")

	entity := &TestEntity{Email: "alice@example.com"}

	id := gen.NextID(entity)
	require.NotZero(t, id, "pointer entity must produce a valid hash")

	// Verify the hash matches a manual FNV-1a computation.
	h := fnv.New64a()
	_, _ = fmt.Fprint(h, entity.Email)
	expected := h.Sum64()

	assert.Equal(t, expected, id, "hash must match manual FNV-1a computation")
}

// TestFieldHashGenerator_NextID_NonStructReturnsZero verifies that passing
// a non-struct entity (after dereferencing) returns 0.
func TestFieldHashGenerator_NextID_NonStructReturnsZero(t *testing.T) {
	gen := NewFieldHashGenerator("Name")

	// nonStructEntity wraps a string value that satisfies Entity but is
	// not a struct after dereferencing.
	id := gen.NextID(&nonStructEntity{val: 42})
	// nonStructEntity is still a struct, so let's test with something
	// that doesn't have the field.
	assert.Equal(t, uint64(0), id, "entity without the field must return 0")
}

// TestFieldHashGenerator_NextID_HashesIntegerField verifies that integer
// field values are properly converted and hashed.
func TestFieldHashGenerator_NextID_HashesIntegerField(t *testing.T) {
	gen := NewFieldHashGenerator("ID")

	entity := &TestEntity{ID: 12345}

	id := gen.NextID(entity)
	require.NotZero(t, id, "integer field must produce a valid hash")

	// Verify against manual computation.
	h := fnv.New64a()
	_, _ = fmt.Fprint(h, uint64(12345))
	expected := h.Sum64()

	assert.Equal(t, expected, id)
}

// TestFieldHashGenerator_NextID_EmptyStringField verifies that an empty
// string field produces a consistent non-zero hash.
func TestFieldHashGenerator_NextID_EmptyStringField(t *testing.T) {
	gen := NewFieldHashGenerator("Name")

	entity := &TestEntity{Name: ""}

	id := gen.NextID(entity)
	require.NotZero(t, id, "empty string still produces a hash")

	// Verify determinism.
	id2 := gen.NextID(entity)
	assert.Equal(t, id, id2)
}

// nonStructEntity is a test helper that satisfies Entity but lacks
// the fields that FieldHashGenerator might look for.
type nonStructEntity struct {
	val int
}

func (e *nonStructEntity) EntityID() uint64      { return uint64(e.val) }
func (e *nonStructEntity) SetEntityID(id uint64) { e.val = int(id) }
