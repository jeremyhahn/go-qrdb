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

package mvcc

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// Store.loadRevision — error path (non-not-found storage error)
// ---------------------------------------------------------------------------

// errGetMemStorage wraps memory.Storage and returns an error on the first Get.
type errGetMemStorage struct {
	inner    *memStorage
	getCount int
	failAt   int
	getErr   error
}

func newErrGetMemStorage(failAt int, getErr error) *errGetMemStorage {
	return &errGetMemStorage{inner: newMemStorage(), failAt: failAt, getErr: getErr}
}

func (e *errGetMemStorage) Get(key []byte) ([]byte, error) {
	e.getCount++
	if e.getCount >= e.failAt && e.getErr != nil {
		return nil, e.getErr
	}
	return e.inner.Get(key)
}

func (e *errGetMemStorage) Set(key, value []byte) error { return e.inner.Set(key, value) }
func (e *errGetMemStorage) Delete(key []byte) error     { return e.inner.Delete(key) }
func (e *errGetMemStorage) DeleteRange(start, end []byte) error {
	return e.inner.DeleteRange(start, end)
}
func (e *errGetMemStorage) Batch() storage.Batch { return e.inner.Batch() }
func (e *errGetMemStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return e.inner.NewIterator(opts)
}
func (e *errGetMemStorage) Snapshot() (storage.Snapshot, error) { return e.inner.Snapshot() }
func (e *errGetMemStorage) Close() error                        { return e.inner.Close() }
func (e *errGetMemStorage) Exists(key []byte) (bool, error)     { return e.inner.Exists(key) }
func (e *errGetMemStorage) List(prefix []byte) ([][]byte, error) {
	return e.inner.List(prefix)
}

func TestStore_LoadRevision_StorageError(t *testing.T) {
	// Fail on the very first Get call (which is loadRevision's RevisionKey lookup).
	es := newErrGetMemStorage(1, errors.New(errors.ErrStorage, "errGetMemStorage.Get", "storage failure"))

	_, err := New(es)
	if err == nil {
		t.Fatal("expected error when storage fails during loadRevision")
	}
}

// ---------------------------------------------------------------------------
// Store.loadCompacted — non-nil val decodes revision
// ---------------------------------------------------------------------------

func TestStore_LoadCompacted_WithPersistedValue(t *testing.T) {
	// Use memory.NewStorage which returns nil for missing keys (not an error).
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })

	// Pre-seed a compacted revision so loadCompacted decodes it.
	rev := int64(10)
	if err := store.Set(CompactionFloorKey(nil), EncodeRevision(rev)); err != nil {
		t.Fatalf("seed CompactedKey: %v", err)
	}

	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := s.CompactionFloor(nil); got != rev {
		t.Errorf("CompactedRevision: got %d, want %d", got, rev)
	}
}

// ---------------------------------------------------------------------------
// Store.Put — writes metadata for existing key (version increment)
// ---------------------------------------------------------------------------

func TestStore_Put_UpdatesExistingKey(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Put([]byte("key"), []byte("v1"), 0)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	kv2, err := s.Put([]byte("key"), []byte("v2"), 0)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if kv2.Version != 2 {
		t.Errorf("expected version 2, got %d", kv2.Version)
	}
}

// ---------------------------------------------------------------------------
// Store.Delete — getDataAtRevision returns nil val (RevisionNotFoundError)
// ---------------------------------------------------------------------------

func TestStore_GetDataAtRevision_NilValue(t *testing.T) {
	// Use memory.NewStorage which returns nil for missing keys (not an error).
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })

	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Write a key, then manually corrupt the data entry by deleting it.
	kv, err := s.Put([]byte("corrupt"), []byte("value"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Delete the data key directly from storage to simulate nil return.
	dataKey := EncodeDataKey([]byte("corrupt"), kv.ModRevision)
	if err := store.Delete(dataKey); err != nil {
		t.Fatalf("Delete data key: %v", err)
	}

	// Now Get should fail with RevisionNotFoundError (nil value in storage).
	_, err = s.Get([]byte("corrupt"))
	if err == nil {
		t.Fatal("expected error for missing data")
	}
}

// ---------------------------------------------------------------------------
// Store.findAtRevision — metadata missing (deleted key path)
// ---------------------------------------------------------------------------

func TestStore_FindAtRevision_MetaMissing(t *testing.T) {
	s := newTestStore(t)

	kv, err := s.Put([]byte("dkey"), []byte("dval"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rev := kv.ModRevision

	// Delete via normal path to remove metadata.
	if _, err := s.Delete([]byte("dkey")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// findAtRevision at the original write revision should still work
	// (tombstone is at rev+1, so the data entry at rev is preserved).
	result, err := s.GetAtRevision([]byte("dkey"), rev)
	if err != nil {
		t.Fatalf("GetAtRevision at write rev: %v", err)
	}
	if string(result.Value) != "dval" {
		t.Errorf("expected dval, got %q", result.Value)
	}
}

// ---------------------------------------------------------------------------
// Store.DeleteInBatch — getDataAtRevision error (data key missing)
// ---------------------------------------------------------------------------

func TestStore_DeleteInBatch_DataMissing(t *testing.T) {
	// Use memory.NewStorage which returns nil for missing keys (not an error).
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })

	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kv, err := s.Put([]byte("bkey"), []byte("bval"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Remove the data key but leave the metadata so DeleteInBatch's
	// getDataAtRevision gets a nil value, making prevKV nil.
	dataKey := EncodeDataKey([]byte("bkey"), kv.ModRevision)
	if err := store.Delete(dataKey); err != nil {
		t.Fatalf("Delete data key: %v", err)
	}

	batch := store.Batch()
	prevKV, err := s.DeleteInBatch(batch, []byte("bkey"), 100)
	if err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	// prevKV is nil because data was not found.
	_ = prevKV
	if err := batch.Commit(); err != nil {
		t.Fatalf("batch.Commit: %v", err)
	}
	_ = batch.Close()
}

// ---------------------------------------------------------------------------
// Store.getDataAtRevision — DecodeValue error path (corrupt value flag)
// ---------------------------------------------------------------------------

// TestStore_GetDataAtRevision_CorruptValueFlag exercises the DecodeValue
// error path in getDataAtRevision by writing a data key with an invalid
// value flag byte directly to storage.
func TestStore_GetDataAtRevision_CorruptValueFlag(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })

	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Write a key normally to get valid meta and data keys.
	kv, err := s.Put([]byte("badval"), []byte("original"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Overwrite the data key with an invalid flag byte.
	dataKey := EncodeDataKey([]byte("badval"), kv.ModRevision)
	if err := store.Set(dataKey, []byte{0xFF, 'b', 'a', 'd'}); err != nil {
		t.Fatalf("Set corrupt data: %v", err)
	}

	// Get should fail with a DecodeValue error.
	_, err = s.Get([]byte("badval"))
	if err == nil {
		t.Fatal("expected error for corrupt value flag in getDataAtRevision")
	}
}

// ---------------------------------------------------------------------------
// Store.Delete — getDataAtRevision error path (corrupt value flag)
// ---------------------------------------------------------------------------

// TestStore_Delete_CorruptValueFlag exercises the getDataAtRevision error path
// in Delete when the stored value has an invalid flag byte.
func TestStore_Delete_CorruptValueFlag(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })

	s, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	kv, err := s.Put([]byte("delcorrupt"), []byte("val"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Corrupt the data entry.
	dataKey := EncodeDataKey([]byte("delcorrupt"), kv.ModRevision)
	if err := store.Set(dataKey, []byte{0xFE}); err != nil {
		t.Fatalf("Set corrupt data: %v", err)
	}

	_, err = s.Delete([]byte("delcorrupt"))
	if err == nil {
		t.Fatal("expected error for corrupt value flag in Delete")
	}
}

// ---------------------------------------------------------------------------
// TestNewRevisionManager_DecodeError exercises the DecodeRevision error path
// in newRevisionManager by pre-seeding an invalid revision value in storage.
// TestNewRevisionManager_StorageError exercises the non-notfound error path
// in newRevisionManager when storage.Get returns a non-not-found error.
// ---------------------------------------------------------------------------
// TestRevisionManager_PersistInBatch_SetError exercises the error path in
// persistInBatch when the batch.Set call fails.
