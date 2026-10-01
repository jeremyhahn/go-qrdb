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

//go:build codec_json

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// plainKVStore is a KVStore with NO atomic conditional write. Embedding the
// interface (not the concrete memory store) is what hides PutIfAbsent: the
// method set is exactly kvstore.KVStore, so the AtomicKVStore assertion in
// SaveIfAbsent fails the way it does for a real non-atomic backend.
type plainKVStore struct {
	kvstore.KVStore
}

// refusingAtomicKVStore implements AtomicKVStore but refuses every atomic
// operation at call time, as the KVStore contract lets a store do when the
// capability is absent on the node rather than on the type: each returns
// kvstore.ErrAtomicWriteUnsupported and writes nothing.
type refusingAtomicKVStore struct {
	kvstore.KVStore
}

// PutIfAbsent refuses per the kvstore.AtomicKVStore contract.
func (refusingAtomicKVStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, kvstore.ErrAtomicWriteUnsupported
}

// CompareAndDelete refuses per the kvstore.AtomicKVStore contract.
func (refusingAtomicKVStore) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, kvstore.ErrAtomicWriteUnsupported
}

// CompareAndSwap refuses per the kvstore.AtomicKVStore contract.
func (refusingAtomicKVStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, kvstore.ErrAtomicWriteUnsupported
}

var _ kvstore.AtomicKVStore = refusingAtomicKVStore{}

// TestSaveIfAbsent_NonAtomicStoreIsRefused: a store with no atomic write is
// refused, never emulated with Get-then-Put, and the refusal is the typed
// AtomicWriteUnsupportedError naming the entity type, matching the canonical
// kvstore.ErrAtomicWriteUnsupported marker.
func TestSaveIfAbsent_NonAtomicStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	inner := newMemoryKVStore()
	d, err := New(plainKVStore{inner}, "certs", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stored, err := d.SaveIfAbsent(ctx, &TestEntity{ID: 7, Name: "a"})
	if stored {
		t.Fatal("a non-atomic store reported the entity stored")
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want it to match kvstore.ErrAtomicWriteUnsupported", err)
	}
	if !errors.Is(err, kvstore.ErrNotApplied) {
		t.Fatalf("err = %v, want it to match kvstore.ErrNotApplied: a capability refusal wrote nothing", err)
	}
	var unsupported *AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %T, want *AtomicWriteUnsupportedError", err)
	}
	if unsupported.EntityType != "certs" || unsupported.Err != nil {
		t.Errorf("refusal = %+v, want entity type certs and no store error", unsupported)
	}
	if n := len(inner.(*memoryKVStore).data); n != 0 {
		t.Errorf("the refused write reached the store: %d keys", n)
	}
}

// TestSaveIfAbsent_StoreRefusalIsTheTypedRefusal: a store that has the method
// but refuses the operation reports the same condition as one without the
// method — the typed AtomicWriteUnsupportedError, still matching the canonical
// marker, with the store's own refusal kept in the chain.
func TestSaveIfAbsent_StoreRefusalIsTheTypedRefusal(t *testing.T) {
	d, err := New(refusingAtomicKVStore{newMemoryKVStore()}, "certs", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stored, err := d.SaveIfAbsent(context.Background(), &TestEntity{ID: 7, Name: "a"})
	if stored {
		t.Fatal("a refused write reported the entity stored")
	}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("err = %v, want it to match kvstore.ErrAtomicWriteUnsupported", err)
	}
	if !errors.Is(err, kvstore.ErrNotApplied) {
		t.Fatalf("err = %v, want it to match kvstore.ErrNotApplied: a capability refusal wrote nothing", err)
	}
	var unsupported *AtomicWriteUnsupportedError
	if !errors.As(err, &unsupported) || unsupported.EntityType != "certs" {
		t.Fatalf("err = %#v, want *AtomicWriteUnsupportedError for certs", err)
	}
	if unsupported.Err != kvstore.ErrAtomicWriteUnsupported {
		t.Errorf("Err = %v, want the store's refusal kept in the chain", unsupported.Err)
	}
}

// TestSaveIfAbsent_OtherStoreErrorsAreNotRelabelled: only the capability
// refusal becomes AtomicWriteUnsupportedError. Any other store failure is an
// outcome-unknown write and must not be reported as "this backend cannot".
func TestSaveIfAbsent_OtherStoreErrorsAreNotRelabelled(t *testing.T) {
	boom := errors.New("raft: proposal dropped")
	d, err := New(failingAtomicKVStore{KVStore: newMemoryKVStore(), err: boom}, "certs", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = d.SaveIfAbsent(context.Background(), &TestEntity{ID: 7, Name: "a"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's error", err)
	}
	var unsupported *AtomicWriteUnsupportedError
	if errors.Is(err, kvstore.ErrAtomicWriteUnsupported) || errors.Is(err, kvstore.ErrNotApplied) || errors.As(err, &unsupported) {
		t.Error("an ordinary store failure was relabelled as unsupported")
	}
}

// failingAtomicKVStore's atomic operations fail with an arbitrary store
// error — the outcome-unknown kind, not a capability refusal.
type failingAtomicKVStore struct {
	kvstore.KVStore
	err error
}

// PutIfAbsent returns the configured error.
func (s failingAtomicKVStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, s.err
}

// CompareAndDelete returns the configured error.
func (s failingAtomicKVStore) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, s.err
}

// CompareAndSwap returns the configured error.
func (s failingAtomicKVStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, s.err
}

var _ kvstore.AtomicKVStore = failingAtomicKVStore{}

func TestAtomicWriteUnsupportedError_Message(t *testing.T) {
	bare := &AtomicWriteUnsupportedError{EntityType: "certs"}
	if got, want := bare.Error(), "dao: certs: underlying store does not support atomic conditional writes"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	wrapped := &AtomicWriteUnsupportedError{EntityType: "certs", Err: kvstore.ErrAtomicWriteUnsupported}
	if got, want := wrapped.Error(), "dao: certs: underlying store does not support atomic conditional writes: kvstore: store does not support atomic conditional writes"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(bare, kvstore.ErrAtomicWriteUnsupported) || !errors.Is(bare, kvstore.ErrNotApplied) {
		t.Error("a bare refusal must match the canonical marker and ErrNotApplied")
	}
}
