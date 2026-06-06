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

// NextID atomically increments the counter and returns the new value.
func (g *AtomicIncrementGenerator) NextID(_ Entity) uint64 {
	return g.counter.Add(1)
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
