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
	"crypto/sha256"
	"encoding/binary"
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

// structFieldString extracts the named field from entity via reflection and
// renders it with fmt.Sprint, the same stringification both FieldHashGenerator
// and CryptoFieldHashGenerator hash. ok is false when entity does not
// dereference to a struct or the field does not exist, in which case the
// caller's own zero/absent behavior applies.
func structFieldString(entity Entity, fieldName string) (value string, ok bool) {
	val := reflect.ValueOf(entity)
	for val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return "", false
	}
	field := val.FieldByName(fieldName)
	if !field.IsValid() {
		return "", false
	}
	return fmt.Sprint(field.Interface()), true
}

// foldSHA256ToUint64 folds a SHA-256 digest down to a uint64 by XOR-ing each
// of its four 8-byte lanes together, rather than truncating to the digest's
// first 8 bytes. Truncation uses only the hash's first lane and discards the
// rest, so two inputs engineered to share a first lane would already
// collide before the fold does anything; XOR-folding makes every bit of the
// 256-bit digest participate in the result.
func foldSHA256ToUint64(sum [sha256.Size]byte) uint64 {
	var id uint64
	for i := 0; i < len(sum); i += 8 {
		id ^= binary.BigEndian.Uint64(sum[i : i+8])
	}
	return id
}

// CryptoFieldHashGenerator generates deterministic entity IDs by hashing a
// named struct field with SHA-256, folded to 64 bits (foldSHA256ToUint64),
// rather than FieldHashGenerator's FNV-1a.
//
// The difference is resistance, not collision probability: both land in the
// same 64-bit ID space and share its birthday bound (a collision becomes
// likely somewhere around 2^32 distinct natural-key values), so a
// CryptoFieldHashGenerator user still needs VerifyNaturalKey on every read
// if it must DETECT a collision rather than merely resist an attacker
// deliberately engineering one. What SHA-256 adds is that engineering a
// second natural-key value that lands on a chosen target's ID requires a
// preimage attack; FNV-1a has no such resistance at all -- a second value
// colliding with a known FNV-1a sum is constructible directly, not merely
// unlikely to occur by chance (go-qrdb#589).
//
// FieldHashGenerator itself is unchanged and keeps its own hash: swapping
// its algorithm in place would silently orphan every entity already
// persisted under an FNV-1a-derived ID. A caller choosing collision
// resistance for a NEW entity type uses this generator instead; nothing
// migrates an existing one automatically, because nothing safely can.
type CryptoFieldHashGenerator struct {
	fieldName string
}

// NewCryptoFieldHashGenerator creates a CryptoFieldHashGenerator that
// hashes the value of the named struct field to produce entity IDs.
func NewCryptoFieldHashGenerator(fieldName string) *CryptoFieldHashGenerator {
	return &CryptoFieldHashGenerator{fieldName: fieldName}
}

// NextID extracts the named field from the entity struct and returns the
// SHA-256 digest of its string form, folded to 64 bits. Returns 0 if the
// field is not found, matching FieldHashGenerator's contract.
func (g *CryptoFieldHashGenerator) NextID(entity Entity) uint64 {
	value, ok := structFieldString(entity, g.fieldName)
	if !ok {
		return 0
	}
	sum := sha256.Sum256([]byte(value))
	return foldSHA256ToUint64(sum)
}

// FieldName returns the field name used for hashing.
func (g *CryptoFieldHashGenerator) FieldName() string {
	return g.fieldName
}

// NaturalKeyMismatchError is returned by VerifyNaturalKey when an entity
// read back by a hash-derived ID does not carry the natural-key value the
// caller looked it up by -- two distinct natural keys hashed to the same ID
// (go-qrdb#589). The lookup itself cannot detect this: the ID looked right
// and Get succeeded, but the record it returned is not the one the caller
// asked for.
type NaturalKeyMismatchError struct {
	EntityType string
	FieldName  string
	Expected   string
	Actual     string
}

// Error returns a human-readable description.
func (e NaturalKeyMismatchError) Error() string {
	return fmt.Sprintf("dao: %s: natural key collision on field %q: expected %q, found %q (hash-derived ID aliasing)",
		e.EntityType, e.FieldName, e.Expected, e.Actual)
}

// VerifyNaturalKey reads fieldName off entity and reports a
// *NaturalKeyMismatchError when its string form does not equal expected.
//
// A caller that looks an entity up by a hash-derived ID (FieldHashGenerator,
// CryptoFieldHashGenerator, DynamicFieldHashGenerator,
// CryptoDynamicFieldHashGenerator) and must not silently accept a DIFFERENT
// entity that happened to hash to the same ID calls this after Get, with
// expected set to the natural-key value it looked up by:
//
//	user, err := userDAO.Get(ctx, dao.NewCryptoFieldHashGenerator("Email").NextID(&User{Email: email}))
//	if err == nil {
//	    err = dao.VerifyNaturalKey(user, "users", "Email", email)
//	}
//
// It is a verification, not a defense: nothing here prevents a collision
// from being written in the first place (two different emails hashing to
// the same ID both SaveIfAbsent-ing would have the second one refused,
// correctly, as "already exists" — for the wrong reason, aliasing a
// different user). VerifyNaturalKey only ensures the collision is NOISY
// rather than silent, at the one place it can still be caught: the read
// that would otherwise hand back someone else's record.
//
// entity not dereferencing to a struct, or fieldName not existing on it, is
// not an error here: there is nothing to verify, and the caller gets
// whatever Get already gave it. DynamicEntity is handled directly, since it
// carries no reflectable struct fields of its own.
func VerifyNaturalKey(entity Entity, entityType, fieldName, expected string) error {
	if de, ok := entity.(*DynamicEntity); ok {
		if de.Fields == nil {
			return nil
		}
		val, exists := de.Fields[fieldName]
		if !exists {
			return nil
		}
		actual := fmt.Sprint(val)
		if actual != expected {
			return NaturalKeyMismatchError{EntityType: entityType, FieldName: fieldName, Expected: expected, Actual: actual}
		}
		return nil
	}

	actual, ok := structFieldString(entity, fieldName)
	if !ok {
		return nil
	}
	if actual != expected {
		return NaturalKeyMismatchError{EntityType: entityType, FieldName: fieldName, Expected: expected, Actual: actual}
	}
	return nil
}
