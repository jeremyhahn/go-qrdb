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
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// TestUnitOfWork_Commit_UsesGuardedBatchWhenAvailable proves Commit routes
// through kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged -- one
// atomic proposal, not the sequential apply-and-compensate path -- when the
// underlying store offers it (go-qrdb#589).
func TestUnitOfWork_Commit_UsesGuardedBatchWhenAvailable(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	alice := &TestEntity{ID: 1, Name: "Alice"}
	bob := &TestEntity{ID: 2, Name: "Bob"}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, alice); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}
	if err := RegisterSave(uow, userDAO, bob); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}

	if err := uow.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got, err := userDAO.Get(ctx, 1); err != nil || got.Name != "Alice" {
		t.Fatalf("Get(1) = (%+v, %v), want Alice", got, err)
	}
	if got, err := userDAO.Get(ctx, 2); err != nil || got.Name != "Bob" {
		t.Fatalf("Get(2) = (%+v, %v), want Bob", got, err)
	}
}

// TestUnitOfWork_Commit_MovesAnEntityAtomically is the motivating case:
// deleting one entity and saving another lands as ONE atomic batch over a
// store that supports it, rather than the delete landing, then (separately)
// the save -- the window a reader could observe the move half-done in.
func TestUnitOfWork_Commit_MovesAnEntityAtomically(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	old := &TestEntity{ID: 1, Name: "old-record"}
	if err := userDAO.Save(ctx, old); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}
	renamed := &TestEntity{ID: 2, Name: "old-record"}

	uow := NewUnitOfWork(store)
	if err := RegisterDelete(uow, userDAO, old); err != nil {
		t.Fatalf("RegisterDelete: %v", err)
	}
	if err := RegisterSave(uow, userDAO, renamed); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}

	if err := uow.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var notFound NotFoundError
	if _, err := userDAO.Get(ctx, 1); !errors.As(err, &notFound) {
		t.Fatalf("Get(1) after move = %v, want NotFoundError", err)
	}
	if got, err := userDAO.Get(ctx, 2); err != nil || got.Name != "old-record" {
		t.Fatalf("Get(2) after move = (%+v, %v), want old-record", got, err)
	}
}

// TestUnitOfWork_Commit_AtomicBatchFailureLeavesNothingApplied proves the
// atomic path's failure mode: when a write in the batch is refused (a
// unique-index collision here), NEITHER operation lands -- unlike the
// compensating path, there is nothing to roll back because nothing was
// ever partially applied.
func TestUnitOfWork_Commit_AtomicBatchFailureLeavesNothingApplied(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()

	if err := store.RegisterEntityIndexes("widget", []kvstore.EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: true},
	}); err != nil {
		t.Fatalf("RegisterEntityIndexes: %v", err)
	}
	widgetDAO, err := New[*TestEntity](store, "widget", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := widgetDAO.Save(ctx, &TestEntity{ID: 1, Name: "taken"}); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, widgetDAO, &TestEntity{ID: 2, Name: "free"}); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}
	if err := RegisterSave(uow, widgetDAO, &TestEntity{ID: 3, Name: "taken"}); err != nil { // collides
		t.Fatalf("RegisterSave: %v", err)
	}

	if err := uow.Commit(ctx); err == nil {
		t.Fatal("Commit with a unique-index collision succeeded, want an error")
	}

	var notFound NotFoundError
	if _, err := widgetDAO.Get(ctx, 2); !errors.As(err, &notFound) {
		t.Fatalf("Get(2) after a refused atomic batch = %v, want NotFoundError (nothing applied)", err)
	}
}

// TestUnitOfWork_Commit_RefusesWithoutGuardedBatch proves a store that does
// NOT implement kvstore.MultiGuardedBatchKVStore is refused outright --
// *AtomicBatchUnsupportedError, nothing written -- rather than falling back
// to a sequential, non-atomic apply. go-qrdb#589's coordinator ruling
// removed that fallback entirely: a store that cannot do atomic batches must
// say so, not emulate one with a compensating rollback that leaves a window
// where a concurrent reader can observe a partially-applied batch.
func TestUnitOfWork_Commit_RefusesWithoutGuardedBatch(t *testing.T) {
	store := newMemoryKVStore() // pkg/dao's own local double: KVStore only.
	ctx := context.Background()

	if _, ok := store.(kvstore.MultiGuardedBatchKVStore); ok {
		t.Fatal("test fixture assumption broken: newMemoryKVStore now implements MultiGuardedBatchKVStore")
	}

	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, &TestEntity{ID: 1, Name: "Alice"}); err != nil {
		t.Fatalf("RegisterSave: %v", err)
	}
	err = uow.Commit(ctx)
	var unsupported *AtomicBatchUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Commit over a non-guarded store = %v (%T), want *AtomicBatchUnsupportedError", err, err)
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("Commit's refusal does not match kvstore.ErrAtomicWriteUnsupported: %v", err)
	}
	if !uow.IsClosed() {
		t.Fatal("a refused Commit must still close the UnitOfWork")
	}

	var notFound NotFoundError
	if _, err := userDAO.Get(ctx, 1); !errors.As(err, &notFound) {
		t.Fatalf("Get(1) after a refused Commit = %v, want NotFoundError (nothing applied)", err)
	}
}

// TestUnitOfWork_RegisterSave_RefusesOnIDCollision_NothingApplies is the
// BLOCKER this file's review found: RegisterSave used to queue an
// unconditional write for a deterministic-ID save, and Commit sent
// PutAllIfAllUnchanged with no guards at all, so a forced collision
// silently overwrote the existing record on every transport. A
// collidingGenerator forces a second, different entity onto the SAME ID as
// an already-stored one; Commit must refuse the WHOLE batch with
// *IDCollisionError, and a harmless, unrelated save queued in the same
// batch must not land either -- proving the guard is evaluated atomically
// with everything else, not as a separate pre-check that lets the rest of
// the batch through regardless.
func TestUnitOfWork_RegisterSave_RefusesOnIDCollision_NothingApplies(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()

	gen := &collidingGenerator{fieldName: "Email", id: 42}
	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	alice := &TestEntity{Name: "alice", Email: "alice@example.com"}
	if err := userDAO.Save(ctx, alice); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	bob := &TestEntity{Name: "bob", Email: "bob@example.com"} // forces the SAME id, a different entity
	harmless := &TestEntity{ID: 999, Name: "unrelated"}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, bob); err != nil {
		t.Fatalf("RegisterSave(bob): %v", err)
	}
	if err := RegisterSave(uow, userDAO, harmless); err != nil {
		t.Fatalf("RegisterSave(harmless): %v", err)
	}

	err = uow.Commit(ctx)
	var collision *IDCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("Commit with a forced collision = %v (%T), want *IDCollisionError", err, err)
	}
	if collision.ID != 42 {
		t.Fatalf("collision.ID = %d, want 42", collision.ID)
	}

	// The original entity is untouched.
	got, err := userDAO.Get(ctx, 42)
	if err != nil {
		t.Fatalf("Get(42): %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("Get(42).Email = %q, want %q (bob must not have overwritten alice)", got.Email, "alice@example.com")
	}

	// The harmless, unrelated save in the SAME batch must not have landed
	// either: the whole batch is one atomic proposal.
	var notFound NotFoundError
	if _, err := userDAO.Get(ctx, 999); !errors.As(err, &notFound) {
		t.Fatalf("Get(999) after a refused batch = %v, want NotFoundError (nothing in the batch applies)", err)
	}
}

// TestUnitOfWork_RegisterSave_SameNaturalKeyIsUpdate proves the coordinator
// ruling applies to UnitOfWork exactly as it does to Save: a RegisterSave
// whose hash-derived ID already names a record carrying the SAME
// natural-key value is the SAME logical entity, so Commit applies it as an
// update rather than refusing the batch.
func TestUnitOfWork_RegisterSave_SameNaturalKeyIsUpdate(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	ctx := context.Background()

	gen := NewCryptoFieldHashGenerator("Email")
	userDAO, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := &TestEntity{Name: "alice", Email: "alice@example.com"}
	if err := userDAO.Save(ctx, first); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}
	id := first.EntityID()

	second := &TestEntity{Name: "alice renamed", Email: "alice@example.com"}
	harmless := &TestEntity{ID: 999, Name: "unrelated"}

	uow := NewUnitOfWork(store)
	if err := RegisterSave(uow, userDAO, second); err != nil {
		t.Fatalf("RegisterSave(second): %v", err)
	}
	if err := RegisterSave(uow, userDAO, harmless); err != nil {
		t.Fatalf("RegisterSave(harmless): %v", err)
	}
	if err := uow.Commit(ctx); err != nil {
		t.Fatalf("Commit (same natural key, want an update): %v", err)
	}

	got, err := userDAO.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	if got.Name != "alice renamed" {
		t.Fatalf("Get(%d).Name = %q, want %q (the update must have applied)", id, got.Name, "alice renamed")
	}
	if _, err := userDAO.Get(ctx, 999); err != nil {
		t.Fatalf("Get(999): %v, want the harmless save to have applied alongside the update", err)
	}
}
