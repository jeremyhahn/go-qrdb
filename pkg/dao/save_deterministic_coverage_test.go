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

// atomicFailureStore wraps a real kvstore.MemoryKVStore, letting a test
// force PutIfAbsent, Get, or CompareAndSwap to fail -- or, for Get, to
// report absence the store itself does not have -- exercising
// saveDeterministic's error-translation and raced-delete branches that a
// genuine race is impractical to force deterministically.
type atomicFailureStore struct {
	*kvstore.MemoryKVStore

	failPutIfAbsentErr error
	failGetKey         string
	failGetErr         error
	getNilOnceKey      string
	failSwapErr        error
}

func newAtomicFailureStore() *atomicFailureStore {
	return &atomicFailureStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
}

func (s *atomicFailureStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.failPutIfAbsentErr != nil {
		return false, s.failPutIfAbsentErr
	}
	return s.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (s *atomicFailureStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.failSwapErr != nil {
		return false, s.failSwapErr
	}
	return s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func (s *atomicFailureStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.failGetKey != "" && key == s.failGetKey {
		return nil, s.failGetErr
	}
	if s.getNilOnceKey != "" && key == s.getNilOnceKey {
		s.getNilOnceKey = ""
		// Make the race real: the key this reports absent is actually
		// deleted too, so a retried PutIfAbsent genuinely succeeds rather
		// than disagreeing with what Get just claimed.
		_ = s.MemoryKVStore.Delete(ctx, key)
		return nil, nil
	}
	return s.MemoryKVStore.Get(ctx, key)
}

var _ kvstore.AtomicKVStore = (*atomicFailureStore)(nil)

// TestGenericDAO_Save_Deterministic_MarshalError proves Save refuses with
// SerializationFailedError when the DAO codec cannot marshal an entity
// whose ID is being assigned by a DeterministicIDGenerator.
func TestGenericDAO_Save_Deterministic_MarshalError(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gDAO := d.(*genericDAO[*TestEntity])
	gDAO.codec = &failingCodec[*TestEntity]{}

	err = d.Save(ctx, &TestEntity{Name: "x", Email: "x@example.com"})
	var serErr SerializationFailedError
	if !errors.As(err, &serErr) {
		t.Fatalf("Save with a failing codec = %v (%T), want SerializationFailedError", err, err)
	}
}

// TestGenericDAO_Save_Deterministic_PutIfAbsentUnsupportedError proves
// Save translates a PutIfAbsent failure matching
// kvstore.ErrAtomicWriteUnsupported into *AtomicWriteUnsupportedError,
// carrying the store's own error.
func TestGenericDAO_Save_Deterministic_PutIfAbsentUnsupportedError(t *testing.T) {
	ctx := context.Background()
	store := newAtomicFailureStore()
	store.failPutIfAbsentErr = kvstore.ErrAtomicWriteUnsupported
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = d.Save(ctx, &TestEntity{Name: "x", Email: "x@example.com"})
	var unsupported *AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Save = %v (%T), want *AtomicWriteUnsupportedError", err, err)
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("Save's error does not match kvstore.ErrAtomicWriteUnsupported: %v", err)
	}
}

// TestGenericDAO_Save_Deterministic_PutIfAbsentError proves Save
// propagates a PutIfAbsent failure that does NOT match
// kvstore.ErrAtomicWriteUnsupported verbatim, rather than mislabeling it
// as a capability refusal.
func TestGenericDAO_Save_Deterministic_PutIfAbsentError(t *testing.T) {
	ctx := context.Background()
	store := newAtomicFailureStore()
	wantErr := errors.New("boom: put failed")
	store.failPutIfAbsentErr = wantErr
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = d.Save(ctx, &TestEntity{Name: "x", Email: "x@example.com"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Save = %v, want %v", err, wantErr)
	}
}

// TestGenericDAO_Save_Deterministic_GetErrorAfterOccupiedKey proves Save
// propagates a Get failure verbatim when reading back an occupied key to
// decide update-vs-collision, rather than treating the failure as
// absence (a raced delete) or masking it behind a reported collision.
func TestGenericDAO_Save_Deterministic_GetErrorAfterOccupiedKey(t *testing.T) {
	ctx := context.Background()
	store := newAtomicFailureStore()
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{Name: "first", Email: "first@example.com"}); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	wantErr := errors.New("boom: get failed")
	store.failGetKey = "users/1"
	store.failGetErr = wantErr

	err = d.Save(ctx, &TestEntity{Name: "second", Email: "second@example.com"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Save = %v, want %v", err, wantErr)
	}
}

// TestGenericDAO_Save_Deterministic_RetriesAfterRacedDelete proves Save
// retries the create (rather than reporting a false collision or failing)
// when PutIfAbsent reports the key occupied but a concurrent delete has
// already cleared it by the time Save reads it back.
func TestGenericDAO_Save_Deterministic_RetriesAfterRacedDelete(t *testing.T) {
	ctx := context.Background()
	store := newAtomicFailureStore()
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{Name: "first", Email: "first@example.com"}); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	// The occupied key's very next Get reports absence, as if a
	// concurrent delete landed between PutIfAbsent and Get; Save must
	// retry rather than report a collision against data that is not
	// really there.
	store.getNilOnceKey = "users/1"

	second := &TestEntity{Name: "second", Email: "second@example.com"}
	if err := d.Save(ctx, second); err != nil {
		t.Fatalf("Save (after raced delete): %v", err)
	}

	got, err := d.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1): %v", err)
	}
	if got.Name != "second" {
		t.Errorf("Get(1).Name = %q, want %q (the retried create must have landed)", got.Name, "second")
	}
}

// TestGenericDAO_Save_Deterministic_NoNaturalKeyIsCollision proves Save
// treats an occupied key as a collision -- without even trying to decode
// the existing record -- when the generator's field is absent from the
// entity being saved, since there is then nothing to compare sameness
// against.
func TestGenericDAO_Save_Deterministic_NoNaturalKeyIsCollision(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	gen := &collidingGenerator{fieldName: "NoSuchField", id: 321}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{Name: "first", Email: "first@example.com"}); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	err = d.Save(ctx, &TestEntity{Name: "second", Email: "second@example.com"})
	var collision *IDCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("Save with no natural key to compare = %v (%T), want *IDCollisionError", err, err)
	}
	if collision.ID != 321 {
		t.Errorf("collision.ID = %d, want 321", collision.ID)
	}

	got, err := d.Get(ctx, 321)
	if err != nil {
		t.Fatalf("Get(321): %v", err)
	}
	if got.Name != "first" {
		t.Errorf("Get(321).Name = %q, want %q (second must not have overwritten first)", got.Name, "first")
	}
}

// TestGenericDAO_Save_Deterministic_SwapUnsupportedError and
// TestGenericDAO_Save_Deterministic_SwapError prove Save translates a
// CompareAndSwap failure during the same-natural-key update path the same
// way PutIfAbsent's failures are translated: a kvstore.ErrAtomicWriteUnsupported
// match becomes *AtomicWriteUnsupportedError, anything else propagates
// verbatim.
func TestGenericDAO_Save_Deterministic_SwapUnsupportedError(t *testing.T) {
	ctx := context.Background()
	store := newAtomicFailureStore()
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{Name: "first", Email: "same@example.com"}); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	store.failSwapErr = kvstore.ErrAtomicWriteUnsupported
	err = d.Save(ctx, &TestEntity{Name: "renamed", Email: "same@example.com"})
	var unsupported *AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Save = %v (%T), want *AtomicWriteUnsupportedError", err, err)
	}
}

func TestGenericDAO_Save_Deterministic_SwapError(t *testing.T) {
	ctx := context.Background()
	store := newAtomicFailureStore()
	gen := &collidingGenerator{fieldName: "Email", id: 1}
	d, err := New[*TestEntity](store, "users", func() *TestEntity { return &TestEntity{} }, WithIDGenerator(gen))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Save(ctx, &TestEntity{Name: "first", Email: "same@example.com"}); err != nil {
		t.Fatalf("Save (seed): %v", err)
	}

	wantErr := errors.New("boom: swap failed")
	store.failSwapErr = wantErr
	err = d.Save(ctx, &TestEntity{Name: "renamed", Email: "same@example.com"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Save = %v, want %v", err, wantErr)
	}
}
