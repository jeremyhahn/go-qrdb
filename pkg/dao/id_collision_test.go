//go:build codec_json

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// collidingGenerator is a DeterministicIDGenerator stub that maps every
// natural-key value to the SAME id, forcing the collision a real hash
// generator would only hit by chance (go-qrdb#589). It exists so the
// write-path refusal and the read-path detection can be proven
// deterministically, without needing to find or construct a real SHA-256 or
// FNV-1a collision.
type collidingGenerator struct {
	fieldName string
	id        uint64
}

func (g *collidingGenerator) NextID(_ Entity) uint64     { return g.id }
func (g *collidingGenerator) FieldName() string          { return g.fieldName }
func (g *collidingGenerator) IDForValue(_ string) uint64 { return g.id }

var _ DeterministicIDGenerator = (*collidingGenerator)(nil)

// TestGenericDAO_Save_RefusesOnIDCollision proves Save routes a
// DeterministicIDGenerator's first-time ID assignment through an atomic
// create-if-absent write: a second, different entity whose generator
// forces the same ID is refused with *IDCollisionError, and the store
// keeps the first entity unchanged rather than being overwritten.
func TestGenericDAO_Save_RefusesOnIDCollision(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	gen := &collidingGenerator{fieldName: "Email", id: 42}
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := &TestEntity{Name: "alice", Email: "alice@example.com"}
	if err := d.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if first.EntityID() != 42 {
		t.Fatalf("first.EntityID() = %d, want 42", first.EntityID())
	}

	second := &TestEntity{Name: "bob", Email: "bob@example.com"}
	err = d.Save(ctx, second)
	if err == nil {
		t.Fatal("Save(second) = nil error, want *IDCollisionError")
	}
	var collision *IDCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("Save(second) error = %v (%T), want *IDCollisionError", err, err)
	}
	if collision.ID != 42 {
		t.Fatalf("collision.ID = %d, want 42", collision.ID)
	}

	// The store still holds the FIRST entity, not a silently overwritten one.
	got, err := d.Get(ctx, 42)
	if err != nil {
		t.Fatalf("Get(42): %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("Get(42).Email = %q, want %q (second must not have overwritten first)", got.Email, "alice@example.com")
	}
}

// TestGenericDAO_Save_SameNaturalKeyIsUpdate proves the coordinator ruling
// (go-qrdb#589 review): a Save whose hash-derived ID already names a
// record carrying the SAME natural-key value is the SAME logical entity, so
// it is applied as an update, not refused as a collision.
func TestGenericDAO_Save_SameNaturalKeyIsUpdate(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	gen := NewCryptoFieldHashGenerator("Email")
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := &TestEntity{Name: "alice", Email: "alice@example.com"}
	if err := d.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	id := first.EntityID()
	if id == 0 {
		t.Fatal("first.EntityID() = 0, want a hash-derived ID")
	}

	// A second entity value with the SAME email re-derives the SAME ID. It
	// must update the existing record, not refuse as a collision.
	second := &TestEntity{Name: "alice renamed", Email: "alice@example.com"}
	if err := d.Save(ctx, second); err != nil {
		t.Fatalf("Save(second, same natural key) = %v, want nil (an update)", err)
	}
	if second.EntityID() != id {
		t.Fatalf("second.EntityID() = %d, want %d (the same derived ID)", second.EntityID(), id)
	}

	got, err := d.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	if got.Name != "alice renamed" {
		t.Fatalf("Get(%d).Name = %q, want %q (the update must have applied)", id, got.Name, "alice renamed")
	}
}

// failingCompareAndSwapStore wraps an AtomicKVStore so every CompareAndSwap
// reports swapped=false, nil -- a guard that never holds -- simulating
// sustained concurrent-writer contention without an actual concurrency
// harness.
type failingCompareAndSwapStore struct {
	kvstore.AtomicKVStore
}

func (s failingCompareAndSwapStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, nil
}

var _ kvstore.AtomicKVStore = failingCompareAndSwapStore{}

// TestGenericDAO_Save_ContentionExhaustedIsNotACollision proves a Save
// whose update path (same natural key) loses every CompareAndSwap retry to
// simulated contention is refused with *SaveContentionExhaustedError, never
// *IDCollisionError -- the coordinator ruling's error message fix: a
// contended update is not a collision, and must not be reported as one.
func TestGenericDAO_Save_ContentionExhaustedIsNotACollision(t *testing.T) {
	ctx := context.Background()
	inner := newMemoryKVStore().(kvstore.AtomicKVStore)
	store := failingCompareAndSwapStore{inner}
	gen := NewCryptoFieldHashGenerator("Email")
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := &TestEntity{Name: "alice", Email: "alice@example.com"}
	if err := d.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}

	second := &TestEntity{Name: "alice renamed", Email: "alice@example.com"}
	err = d.Save(ctx, second)
	if err == nil {
		t.Fatal("Save(second) = nil error, want *SaveContentionExhaustedError")
	}
	var collision *IDCollisionError
	if errors.As(err, &collision) {
		t.Fatalf("Save(second) error = %v (%T), a contended update must never report *IDCollisionError", err, err)
	}
	var contended *SaveContentionExhaustedError
	if !errors.As(err, &contended) {
		t.Fatalf("Save(second) error = %v (%T), want *SaveContentionExhaustedError", err, err)
	}
	if contended.Attempts != maxSaveDeterministicAttempts {
		t.Fatalf("contended.Attempts = %d, want %d", contended.Attempts, maxSaveDeterministicAttempts)
	}
}

// TestGenericDAO_Save_RefusesOnIDCollision_StoreWithoutAtomicWrites proves
// Save refuses with *AtomicWriteUnsupportedError, rather than silently
// falling back to an unconditional Put, when the store cannot evaluate the
// create-if-absent write at all.
func TestGenericDAO_Save_RefusesOnIDCollision_StoreWithoutAtomicWrites(t *testing.T) {
	ctx := context.Background()
	store := plainKVStore{newMemoryKVStore()}
	gen := &collidingGenerator{fieldName: "Email", id: 7}
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entity := &TestEntity{Name: "alice", Email: "alice@example.com"}
	err = d.Save(ctx, entity)
	if err == nil {
		t.Fatal("Save = nil error, want *AtomicWriteUnsupportedError")
	}
	var unsupported *AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Save error = %v (%T), want *AtomicWriteUnsupportedError", err, err)
	}
}

// TestGenericDAO_GetByNaturalKey_DetectsCollisionOnRead proves the read
// path catches a collision VerifyNaturalKey can see but Get(ctx, id) alone
// cannot: looking up by a DIFFERENT natural-key value that forces the same
// ID as an already-stored entity returns *NaturalKeyMismatchError instead
// of silently handing back the wrong record.
func TestGenericDAO_GetByNaturalKey_DetectsCollisionOnRead(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	gen := &collidingGenerator{fieldName: "Email", id: 99}
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stored := &TestEntity{Name: "alice", Email: "alice@example.com"}
	if err := d.Save(ctx, stored); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// "bob@example.com" was never actually persisted -- the collision was
	// refused at write time -- but it forces the SAME id as alice's record,
	// exactly as a genuine hash collision would if it slipped past the
	// write-path guard (legacy data, a non-DAO write path).
	_, err = d.GetByNaturalKey(ctx, "bob@example.com")
	if err == nil {
		t.Fatal("GetByNaturalKey(bob) = nil error, want *NaturalKeyMismatchError")
	}
	var mismatch NaturalKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("GetByNaturalKey(bob) error = %v (%T), want NaturalKeyMismatchError", err, err)
	}
	if mismatch.Expected != "bob@example.com" || mismatch.Actual != "alice@example.com" {
		t.Fatalf("mismatch = %+v, want Expected=bob@example.com Actual=alice@example.com", mismatch)
	}

	// The matching lookup succeeds and returns the real record.
	got, err := d.GetByNaturalKey(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByNaturalKey(alice): %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("GetByNaturalKey(alice).Email = %q, want alice@example.com", got.Email)
	}
}

// TestGenericDAO_GetByNaturalKey_NotFound proves GetByNaturalKey returns
// Get's own NotFoundError unchanged when nothing is stored under the
// derived ID at all, rather than masking it behind a verification error.
func TestGenericDAO_GetByNaturalKey_NotFound(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	gen := NewCryptoFieldHashGenerator("Email")
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = d.GetByNaturalKey(ctx, "nobody@example.com")
	if !IsNotFound(err) {
		t.Fatalf("GetByNaturalKey error = %v (%T), want NotFoundError", err, err)
	}
}

// TestGenericDAO_GetByNaturalKey_UnsupportedGenerator proves GetByNaturalKey
// refuses with *UnsupportedIDGeneratorError, rather than guessing at an ID,
// when the configured IDGenerator is not deterministic.
func TestGenericDAO_GetByNaturalKey_UnsupportedGenerator(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()
	d, err := New(store, "entities", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = d.GetByNaturalKey(ctx, "alice@example.com")
	if err == nil {
		t.Fatal("GetByNaturalKey = nil error, want *UnsupportedIDGeneratorError")
	}
	var unsupported *UnsupportedIDGeneratorError
	if !errors.As(err, &unsupported) {
		t.Fatalf("GetByNaturalKey error = %v (%T), want *UnsupportedIDGeneratorError", err, err)
	}
}
