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

package dao

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"sync/atomic"
	"time"
)

// IDGenerator generates unique identifiers for entities.
// Implementations must be safe for concurrent use.
type IDGenerator interface {
	// NextID returns the next unique identifier for the given entity.
	NextID(entity Entity) uint64
}

// IDSequence is an IDGenerator whose values are drawn from a counter, and
// which can therefore be told about an identifier a caller assigned itself.
//
// Advance(n) guarantees no later NextID returns a value less than or equal
// to n. That is what lets an explicitly supplied identifier reserve itself
// against every identifier the generator produces afterwards: without it, a
// caller that assigns its own identifiers and a generator that allocates
// them are two allocators over one keyspace, and the second one eventually
// hands out a value the first one already used.
//
// Generators whose values do not come from a counter -- UnixNanoGenerator's
// clock reading, FieldHashGenerator's hash of a natural key -- cannot
// honour this and deliberately do not implement it. A caller discovers the
// capability by type assertion and has nothing to reserve when it is
// absent.
//
// It is the process-local counterpart of the replicated counter's Advance
// verb, and the two are defined identically so that a caller which may be
// backed by either does not have to know which it got.
type IDSequence interface {
	IDGenerator

	// Advance raises the underlying counter so that no subsequent NextID
	// returns a value less than or equal to atLeast. It is a no-op, not an
	// error, when the counter is already at or past atLeast.
	Advance(atLeast uint64)
}

// UnixNanoGenerator generates IDs from the current Unix nanosecond timestamp.
// This is the default generator used when no IDGenerator is configured.
type UnixNanoGenerator struct{}

// NextID returns the current time as Unix nanoseconds.
func (g *UnixNanoGenerator) NextID(_ Entity) uint64 {
	return uint64(time.Now().UnixNano()) //nolint:gosec // G115: UnixNano always positive after 1970
}

// AtomicIncrementGenerator generates monotonically increasing IDs starting
// from a configurable start value. It uses sync/atomic for lock-free
// thread safety.
type AtomicIncrementGenerator struct {
	counter atomic.Uint64
}

// NewAtomicIncrementGenerator creates an AtomicIncrementGenerator that begins
// producing IDs from the given start value. The first call to NextID returns
// start+1.
func NewAtomicIncrementGenerator(start uint64) *AtomicIncrementGenerator {
	g := &AtomicIncrementGenerator{}
	g.counter.Store(start)
	return g
}

// Compile-time proof that the counter-backed generator carries the Advance
// verb IDSequence requires. It sits beside the methods rather than at the
// top of the file so that removing either one fails here.
var _ IDSequence = (*AtomicIncrementGenerator)(nil)

// NextID atomically increments the counter and returns the new value.
func (g *AtomicIncrementGenerator) NextID(_ Entity) uint64 {
	return g.counter.Add(1)
}

// Advance raises the counter to atLeast when it is below it, so the next
// NextID returns atLeast+1; it is a no-op when the counter is already at or
// past atLeast.
//
// Lock-free, like NextID: a compare-and-swap that loses to a concurrent
// NextID or Advance re-reads the winner's value and re-decides against it,
// so the counter only ever moves up and no interleaving can lower it. The
// loop is bounded in practice by the same thing that bounds Add's — there
// is no unbounded work between the load and the swap.
func (g *AtomicIncrementGenerator) Advance(atLeast uint64) {
	for {
		current := g.counter.Load()
		if current >= atLeast {
			return
		}
		if g.counter.CompareAndSwap(current, atLeast) {
			return
		}
	}
}

// FieldHashGenerator generates deterministic IDs by hashing a named struct
// field using FNV-1a 64-bit. The field value is extracted via reflection
// at NextID time, making the generated ID reproducible for the same input.
type FieldHashGenerator struct {
	fieldName string
}

// NewFieldHashGenerator creates a FieldHashGenerator that hashes the value
// of the named struct field to produce entity IDs.
func NewFieldHashGenerator(fieldName string) *FieldHashGenerator {
	return &FieldHashGenerator{fieldName: fieldName}
}

// NextID extracts the named field from the entity struct and returns
// its FNV-1a 64-bit hash. Returns 0 if the field is not found.
func (g *FieldHashGenerator) NextID(entity Entity) uint64 {
	val := reflect.ValueOf(entity)

	// Dereference pointers to get the underlying struct.
	for val.Kind() == reflect.Pointer {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return 0
	}

	field := val.FieldByName(g.fieldName)
	if !field.IsValid() {
		return 0
	}

	h := fnv.New64a()
	_, _ = fmt.Fprint(h, field.Interface())
	return h.Sum64()
}
