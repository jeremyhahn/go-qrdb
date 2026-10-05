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

// DeterministicIDGenerator is implemented by every hash-derived IDGenerator
// in this package (FieldHashGenerator, CryptoFieldHashGenerator,
// DynamicFieldHashGenerator, CryptoDynamicFieldHashGenerator): NextID's
// result depends only on one named natural-key value, never on process
// state (a counter, the clock), so two different entities can land on the
// same ID through nothing worse than an unlucky draw from the 64-bit output
// space (go-qrdb#589; see CryptoFieldHashGenerator's doc for how large that
// draw has to be).
//
// genericDAO uses it for two things. Save routes a generator's own
// first-time ID assignment (EntityID() == 0) through the store's atomic
// create-if-absent write (SaveIfAbsent) instead of an unconditional Put, so
// a collision REFUSES with *IDCollisionError rather than silently
// overwriting whichever entity got there first. GetByNaturalKey derives the
// lookup ID from IDForValue and verifies the result against the natural-key
// value with VerifyNaturalKey before returning it -- the read-side half of
// the same guarantee, not opt-in because there is no way to call
// GetByNaturalKey without it.
type DeterministicIDGenerator interface {
	IDGenerator

	// FieldName returns the struct field (FieldHashGenerator,
	// CryptoFieldHashGenerator) or DynamicEntity.Fields key
	// (DynamicFieldHashGenerator, CryptoDynamicFieldHashGenerator) this
	// generator's NextID hashes.
	FieldName() string

	// IDForValue returns the ID NextID would derive from an entity whose
	// FieldName() field equals value, without needing to construct one.
	// It is what lets GetByNaturalKey compute a lookup ID from a bare
	// value.
	IDForValue(value string) uint64
}

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
	value, ok := structFieldString(entity, g.fieldName)
	if !ok {
		return 0
	}
	return g.IDForValue(value)
}

// IDForValue returns the FNV-1a 64-bit hash of value, the same hash NextID
// derives from the named field's string form.
func (g *FieldHashGenerator) IDForValue(value string) uint64 {
	return fnv64aHash(value)
}

// FieldName returns the field name used for hashing.
func (g *FieldHashGenerator) FieldName() string {
	return g.fieldName
}

// Compile-time proof that FieldHashGenerator carries the FieldName/
// IDForValue pair DeterministicIDGenerator requires.
var _ DeterministicIDGenerator = (*FieldHashGenerator)(nil)

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

// fnv64aHash returns the FNV-1a 64-bit hash of value. It is the one place
// FieldHashGenerator and DynamicFieldHashGenerator compute their hash, so
// their results stay identical for the same stringified value regardless of
// whether it came from a struct field or a DynamicEntity.Fields entry.
func fnv64aHash(value string) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprint(h, value)
	return h.Sum64()
}

// truncateSHA256ToUint64 returns the first 8 bytes of a SHA-256 digest,
// big-endian, as a uint64 -- the standard construction for deriving a
// shorter fixed-width value from a cryptographic hash (e.g. RFC 9562 §5.8's
// UUIDv8, which this codebase's own role.SystemRoleID already truncates
// SHA-256 for).
//
// An earlier version of this function XOR-folded all four 8-byte lanes of
// the digest together instead, reasoning that truncation "uses only the
// hash's first lane and discards the rest." That reasoning does not hold:
// SHA-256 is a pseudorandom function, not an identity map over lanes, so
// finding a second input whose digest shares just the first 8 bytes with a
// target's is already as hard as a generic preimage search against those 64
// bits -- there is no cheaper attack that touches "only" one lane, and
// XOR-folding the other three lanes in does not raise that cost. It
// produces a differently-distributed 64-bit value, not a more resistant
// one, which makes it home-made construction rather than the textbook one
// (go-qrdb#589).
func truncateSHA256ToUint64(sum [sha256.Size]byte) uint64 {
	return binary.BigEndian.Uint64(sum[:8])
}

// sha256TruncatedHash returns the SHA-256 digest of value, truncated to 64
// bits (truncateSHA256ToUint64). It is the one place CryptoFieldHashGenerator
// and CryptoDynamicFieldHashGenerator compute their hash.
func sha256TruncatedHash(value string) uint64 {
	sum := sha256.Sum256([]byte(value))
	return truncateSHA256ToUint64(sum)
}

// CryptoFieldHashGenerator generates deterministic entity IDs by hashing a
// named struct field with SHA-256, truncated to 64 bits
// (truncateSHA256ToUint64), rather than FieldHashGenerator's FNV-1a.
//
// The difference is resistance, not collision probability: both land in the
// same 64-bit ID space and share its bounds. An attacker who controls BOTH
// colliding natural-key values needs roughly 2^32 attempts to find any pair
// that collides (the birthday bound on a 64-bit space); an attacker who
// must hit one FIXED victim ID needs roughly 2^64 (a generic preimage
// search), and FNV-1a offers no resistance at all to that second case -- a
// value whose FNV-1a sum equals a chosen target's is constructible
// directly, by solving for it, not merely found by brute force (go-qrdb#589).
// SHA-256 does not change either bound; it closes the direct-construction
// attack FNV-1a is open to by requiring a real preimage search instead. A
// CryptoFieldHashGenerator user that must DETECT a collision that occurred
// despite this -- rather than merely resist one being engineered -- still
// needs VerifyNaturalKey on every read.
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
// SHA-256 digest of its string form, truncated to 64 bits. Returns 0 if the
// field is not found, matching FieldHashGenerator's contract.
func (g *CryptoFieldHashGenerator) NextID(entity Entity) uint64 {
	value, ok := structFieldString(entity, g.fieldName)
	if !ok {
		return 0
	}
	return g.IDForValue(value)
}

// IDForValue returns the SHA-256 digest of value, truncated to 64 bits, the
// same hash NextID derives from the named field's string form.
func (g *CryptoFieldHashGenerator) IDForValue(value string) uint64 {
	return sha256TruncatedHash(value)
}

// FieldName returns the field name used for hashing.
func (g *CryptoFieldHashGenerator) FieldName() string {
	return g.fieldName
}

// Compile-time proof that CryptoFieldHashGenerator carries the
// FieldName/IDForValue pair DeterministicIDGenerator requires.
var _ DeterministicIDGenerator = (*CryptoFieldHashGenerator)(nil)

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
// GenericDAO.GetByNaturalKey calls this on every read for an entity
// configured with a DeterministicIDGenerator, so a caller using it gets the
// check with no separate step to remember. A caller that instead computes
// the hash-derived ID itself (FieldHashGenerator, CryptoFieldHashGenerator,
// DynamicFieldHashGenerator, CryptoDynamicFieldHashGenerator) and calls Get
// directly must not silently accept a DIFFERENT entity that happened to
// hash to the same ID, and calls this explicitly after Get, with expected
// set to the natural-key value it looked up by:
//
//	user, err := userDAO.Get(ctx, dao.NewCryptoFieldHashGenerator("Email").NextID(&User{Email: email}))
//	if err == nil {
//	    err = dao.VerifyNaturalKey(user, "users", "Email", email)
//	}
//
// It is a verification, not a defense: nothing here prevents a collision
// from being written in the first place through a path other than Save or
// SaveIfAbsent (a store write that bypasses GenericDAO entirely, or data
// persisted before the create-if-absent write-path guard existed).
// VerifyNaturalKey only ensures such a collision is NOISY rather than
// silent, at the one place it can still be caught: the read that would
// otherwise hand back someone else's record.
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
