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

package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	qrdbErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testLogger creates a Logger suitable for unit tests (error level, minimal noise).
func testLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	return log
}

// newTestStoragePebbleIndex creates a StoragePebbleIndex backed by a
// temporary PebbleDB. The storage is cleaned up when the test finishes.
func newTestStoragePebbleIndex(t *testing.T, config IndexConfig) *StoragePebbleIndex {
	t.Helper()
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	idx, err := NewStoragePebbleIndex(s, config, testLogger(t))
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}
	return idx
}

// normalConfig returns a standard (non-unique, case-sensitive) IndexConfig.
func normalConfig(name string) IndexConfig {
	return IndexConfig{
		Name:          name,
		Type:          IndexTypeNormal,
		CaseSensitive: true,
	}
}

// uniqueConfig returns a unique index config.
func uniqueConfig(name string) IndexConfig {
	return IndexConfig{
		Name:          name,
		Type:          IndexTypeUnique,
		CaseSensitive: true,
	}
}

// caseInsensitiveConfig returns a case-insensitive index config.
func caseInsensitiveConfig(name string) IndexConfig {
	return IndexConfig{
		Name:          name,
		Type:          IndexTypeNormal,
		CaseSensitive: false,
	}
}

// assertDragonErrorCode checks that err is a *DragonError with the expected code.
func assertDragonErrorCode(t *testing.T, err error, code qrdbErrors.ErrorCode, msgContext string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error with code %v, got nil", msgContext, code)
	}
	var de *qrdbErrors.DragonError
	if !errors.As(err, &de) {
		t.Fatalf("%s: expected *DragonError, got %T: %v", msgContext, err, err)
	}
	if de.Code != code {
		t.Fatalf("%s: expected error code %v, got %v: %v", msgContext, code, de.Code, err)
	}
}

// ---------------------------------------------------------------------------
// Error-injecting storage wrapper for testing storage error paths
// ---------------------------------------------------------------------------

// failableStorage wraps a real storage.Storage and allows injecting errors
// on Set, Delete, and Batch operations to test error handling paths.
type failableStorage struct {
	delegate  storage.Storage
	setErr    atomic.Value // stores error
	deleteErr atomic.Value // stores error
	batchErr  atomic.Value // stores error (for batch.Delete and batch.Commit)
}

// newFailableStorage creates a failableStorage wrapping the given storage.
func newFailableStorage(delegate storage.Storage) *failableStorage {
	return &failableStorage{delegate: delegate}
}

func (f *failableStorage) Get(key []byte) ([]byte, error) {
	return f.delegate.Get(key)
}

func (f *failableStorage) Set(key, value []byte) error {
	if v := f.setErr.Load(); v != nil {
		return v.(error)
	}
	return f.delegate.Set(key, value)
}

func (f *failableStorage) Delete(key []byte) error {
	if v := f.deleteErr.Load(); v != nil {
		return v.(error)
	}
	return f.delegate.Delete(key)
}

func (f *failableStorage) Batch() storage.Batch {
	b := f.delegate.Batch()
	if v := f.batchErr.Load(); v != nil {
		return &failableBatch{delegate: b, err: v.(error)}
	}
	return b
}

func (f *failableStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return f.delegate.NewIterator(opts)
}

func (f *failableStorage) Snapshot() (storage.Snapshot, error) {
	return f.delegate.Snapshot()
}

func (f *failableStorage) List(prefix []byte) ([][]byte, error) {
	return f.delegate.List(prefix)
}

func (f *failableStorage) Exists(key []byte) (bool, error) {
	return f.delegate.Exists(key)
}

func (f *failableStorage) Close() error {
	return f.delegate.Close()
}

// failableBatch wraps a real batch and injects errors on Delete and Commit.
type failableBatch struct {
	delegate storage.Batch
	err      error
}

func (b *failableBatch) Set(key, value []byte) error { return b.delegate.Set(key, value) }
func (b *failableBatch) Delete(key []byte) error     { return b.err }
func (b *failableBatch) Commit() error               { return b.err }
func (b *failableBatch) Rollback() error             { return b.delegate.Rollback() }
func (b *failableBatch) Close() error                { return b.delegate.Close() }

// ---------------------------------------------------------------------------
// NewStoragePebbleIndex constructor tests
// ---------------------------------------------------------------------------

func TestNewStoragePebbleIndex_Success(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	idx, err := NewStoragePebbleIndex(s, normalConfig("test"), testLogger(t))
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: unexpected error: %v", err)
	}
	if idx == nil {
		t.Fatal("NewStoragePebbleIndex: returned nil")
	}
}

func TestNewStoragePebbleIndex_NilStorage(t *testing.T) {
	_, err := NewStoragePebbleIndex(nil, normalConfig("test"), testLogger(t))
	assertDragonErrorCode(t, err, qrdbErrors.ErrInvalidInput, "nil storage")
}

func TestNewStoragePebbleIndex_NilLogger(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = NewStoragePebbleIndex(s, normalConfig("test"), nil)
	assertDragonErrorCode(t, err, qrdbErrors.ErrInvalidInput, "nil logger")
}

func TestNewStoragePebbleIndex_EmptyName(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = NewStoragePebbleIndex(s, normalConfig(""), testLogger(t))
	assertDragonErrorCode(t, err, qrdbErrors.ErrInvalidInput, "empty name")
}

// ---------------------------------------------------------------------------
// Put tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Put_Success(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	if err := idx.Put([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("1")) {
		t.Errorf("Get: got %v, want [[1]]", ids)
	}
}

func TestStoragePebbleIndex_Put_MultipleEntitiesSameValue(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	if err := idx.Put([]byte("1"), []byte("shared")); err != nil {
		t.Fatalf("Put(1): %v", err)
	}
	if err := idx.Put([]byte("2"), []byte("shared")); err != nil {
		t.Fatalf("Put(2): %v", err)
	}

	ids, err := idx.Get([]byte("shared"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("Get: got %d entities, want 2", len(ids))
	}
}

func TestStoragePebbleIndex_Put_IdempotentSameEntitySameValue(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	if err := idx.Put([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	if err := idx.Put([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Put(duplicate): %v", err)
	}

	ids, _ := idx.Get([]byte("alice"))
	if len(ids) != 1 {
		t.Errorf("Get: got %d entries, want 1 (idempotent)", len(ids))
	}
}

func TestStoragePebbleIndex_Put_UniqueConstraintViolation(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))

	if err := idx.Put([]byte("1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): %v", err)
	}

	err := idx.Put([]byte("2"), []byte("alice@test.com"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrConflict, "unique violation")
}

func TestStoragePebbleIndex_Put_UniqueSameEntityAllowed(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))

	if err := idx.Put([]byte("1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(first): %v", err)
	}

	// Same entity key updating is allowed even on unique indexes.
	if err := idx.Put([]byte("1"), []byte("alice@test.com")); err != nil {
		t.Fatalf("Put(same entity): unexpected error: %v", err)
	}
}

func TestStoragePebbleIndex_Put_CaseInsensitive(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, caseInsensitiveConfig("test"))

	if err := idx.Put([]byte("1"), []byte("ALICE")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Lookup with different case should find it.
	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("1")) {
		t.Errorf("Get(case insensitive): got %v, want [[1]]", ids)
	}
}

func TestStoragePebbleIndex_Put_StorageSetError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fs := newFailableStorage(s)
	idx, err := NewStoragePebbleIndex(fs, normalConfig("test"), testLogger(t))
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	// Inject a Set error.
	fs.setErr.Store(&qrdbErrors.MessageError{Message: "disk full"})

	putErr := idx.Put([]byte("1"), []byte("alice"))
	assertDragonErrorCode(t, putErr, qrdbErrors.ErrStorage, "Put storage error")
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Get_ExistingValue(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("42"), []byte("alice"))

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("42")) {
		t.Errorf("Get: got %v, want [[42]]", ids)
	}
}

func TestStoragePebbleIndex_Get_NonExistent(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ids, err := idx.Get([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Get(nonexistent): got %v, want empty", ids)
	}
}

func TestStoragePebbleIndex_Get_MultipleEntities(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("10"), []byte("shared"))
	_ = idx.Put([]byte("20"), []byte("shared"))
	_ = idx.Put([]byte("30"), []byte("shared"))

	ids, err := idx.Get([]byte("shared"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("Get: got %d entities, want 3", len(ids))
	}

	// Entity keys should be in sorted order.
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i], ids[j]) < 0 })
	expected := [][]byte{[]byte("10"), []byte("20"), []byte("30")}
	for i, id := range ids {
		if !bytes.Equal(id, expected[i]) {
			t.Errorf("Get[%d]: got %s, want %s", i, id, expected[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Delete_Existing(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alice"))

	if err := idx.Delete([]byte("1"), []byte("alice")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Get after delete: got %v, want empty", ids)
	}
}

func TestStoragePebbleIndex_Delete_NonExistent(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	// Deleting a non-existent key should not error in PebbleDB.
	err := idx.Delete([]byte("999"), []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete(nonexistent): unexpected error: %v", err)
	}
}

func TestStoragePebbleIndex_Delete_LeavesOtherEntities(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("shared"))
	_ = idx.Put([]byte("2"), []byte("shared"))

	if err := idx.Delete([]byte("1"), []byte("shared")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	ids, _ := idx.Get([]byte("shared"))
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("2")) {
		t.Errorf("Get after partial delete: got %v, want [[2]]", ids)
	}
}

func TestStoragePebbleIndex_Delete_StorageDeleteError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fs := newFailableStorage(s)
	idx, err := NewStoragePebbleIndex(fs, normalConfig("test"), testLogger(t))
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	// Inject a Delete error.
	fs.deleteErr.Store(&qrdbErrors.MessageError{Message: "storage failure"})

	delErr := idx.Delete([]byte("1"), []byte("alice"))
	assertDragonErrorCode(t, delErr, qrdbErrors.ErrStorage, "Delete storage error")
}

// ---------------------------------------------------------------------------
// Scan tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Scan_FullRange(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("gamma"))

	result, err := idx.Scan([]byte("alpha"), []byte("zzzz"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Scan: got %d entries, want 3 (got: %v)", len(result), result)
	}
}

func TestStoragePebbleIndex_Scan_PartialRange(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("apple"))
	_ = idx.Put([]byte("2"), []byte("banana"))
	_ = idx.Put([]byte("3"), []byte("cherry"))

	// Range [banana, cherry) should return only banana.
	result, err := idx.Scan([]byte("banana"), []byte("cherry"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Scan: got %d entries, want 1", len(result))
	}
	if _, ok := result["banana"]; !ok {
		t.Error("Scan: expected 'banana' in result")
	}
}

func TestStoragePebbleIndex_Scan_NilBounds(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))

	// Both nil means scan all entries.
	result, err := idx.Scan(nil, nil)
	if err != nil {
		t.Fatalf("Scan(nil, nil): %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Scan(nil, nil): got %d entries, want 2", len(result))
	}
}

func TestStoragePebbleIndex_Scan_NilStart(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("gamma"))

	// nil start means start from beginning.
	result, err := idx.Scan(nil, []byte("beta"))
	if err != nil {
		t.Fatalf("Scan(nil, beta): %v", err)
	}
	// Should get "alpha" only (end is exclusive prefix scan).
	if _, ok := result["alpha"]; !ok {
		t.Error("Scan(nil, beta): expected 'alpha' in result")
	}
}

func TestStoragePebbleIndex_Scan_NilEnd(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("gamma"))

	// nil end means scan to end.
	result, err := idx.Scan([]byte("beta"), nil)
	if err != nil {
		t.Fatalf("Scan(beta, nil): %v", err)
	}
	// Should get beta and gamma.
	if len(result) < 2 {
		t.Errorf("Scan(beta, nil): got %d entries, want at least 2", len(result))
	}
}

func TestStoragePebbleIndex_Scan_EmptyIndex(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	result, err := idx.Scan([]byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("Scan(empty): %v", err)
	}
	if len(result) != 0 {
		t.Errorf("Scan(empty): got %d entries, want 0", len(result))
	}
}

func TestStoragePebbleIndex_Scan_MultipleEntitiesSameValue(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("shared"))
	_ = idx.Put([]byte("2"), []byte("shared"))
	_ = idx.Put([]byte("3"), []byte("other"))

	result, err := idx.Scan([]byte("shared"), []byte("sharez"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if ids, ok := result["shared"]; !ok || len(ids) != 2 {
		t.Errorf("Scan: expected 2 entity keys for 'shared', got %v", result)
	}
}

func TestStoragePebbleIndex_Scan_CaseInsensitive(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, caseInsensitiveConfig("test"))
	_ = idx.Put([]byte("1"), []byte("ALPHA"))
	_ = idx.Put([]byte("2"), []byte("Beta"))

	// Scan with mixed case should normalize.
	result, err := idx.Scan([]byte("ALPHA"), []byte("ZZZZ"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Both should be normalized to lowercase.
	if _, ok := result["alpha"]; !ok {
		t.Error("Scan: expected 'alpha' (lowered) in result")
	}
	if _, ok := result["beta"]; !ok {
		t.Error("Scan: expected 'beta' (lowered) in result")
	}
}

// ---------------------------------------------------------------------------
// GetAll tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_GetAll_MultipleValues(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("3"), []byte("gamma"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("GetAll: got %d IDs, want 3", len(ids))
	}
}

func TestStoragePebbleIndex_GetAll_Deduplicated(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("1"), []byte("beta")) // Same entity key, different field value.

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("1")) {
		t.Errorf("GetAll: got %v, want [[1]] (deduplicated)", ids)
	}
}

func TestStoragePebbleIndex_GetAll_EmptyIndex(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll(empty): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll(empty): got %v, want empty", ids)
	}
}

func TestStoragePebbleIndex_GetAll_LargeDataset(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	const count = 200
	for i := 1; i <= count; i++ {
		if err := idx.Put([]byte(fmt.Sprintf("%d", i)), []byte("value")); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(ids) != count {
		t.Errorf("GetAll: got %d IDs, want %d", len(ids), count)
	}
}

// ---------------------------------------------------------------------------
// Clear tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Clear_RemovesAll(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("gamma"))

	if err := idx.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll after clear: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll after clear: got %v, want empty", ids)
	}
}

func TestStoragePebbleIndex_Clear_EmptyIndex(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	// Clearing an empty index should succeed.
	if err := idx.Clear(); err != nil {
		t.Fatalf("Clear(empty): unexpected error: %v", err)
	}
}

func TestStoragePebbleIndex_Clear_CanPutAfterClear(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("old"))
	_ = idx.Clear()

	if err := idx.Put([]byte("2"), []byte("new")); err != nil {
		t.Fatalf("Put after clear: %v", err)
	}

	ids, _ := idx.Get([]byte("new"))
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("2")) {
		t.Errorf("Get after clear+put: got %v, want [[2]]", ids)
	}
}

func TestStoragePebbleIndex_Clear_DoesNotAffectOtherIndexes(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	log := testLogger(t)

	idx1, err := NewStoragePebbleIndex(s, normalConfig("first"), log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex(first): %v", err)
	}
	idx2, err := NewStoragePebbleIndex(s, normalConfig("second"), log)
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex(second): %v", err)
	}

	_ = idx1.Put([]byte("1"), []byte("alpha"))
	_ = idx2.Put([]byte("2"), []byte("beta"))

	_ = idx1.Clear()

	ids1, _ := idx1.GetAll()
	ids2, _ := idx2.GetAll()

	if len(ids1) != 0 {
		t.Errorf("first index after clear: got %v, want empty", ids1)
	}
	if len(ids2) != 1 {
		t.Errorf("second index after first's clear: got %v, want [[2]]", ids2)
	}
}

func TestStoragePebbleIndex_Clear_BatchDeleteError(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fs := newFailableStorage(s)
	idx, err := NewStoragePebbleIndex(fs, normalConfig("test"), testLogger(t))
	if err != nil {
		t.Fatalf("NewStoragePebbleIndex: %v", err)
	}

	// Put data so Clear has entries to iterate and delete.
	if err := idx.Put([]byte("1"), []byte("alpha")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Inject batch error so batch.Delete fails during Clear.
	fs.batchErr.Store(&qrdbErrors.MessageError{Message: "batch delete failure"})

	clearErr := idx.Clear()
	assertDragonErrorCode(t, clearErr, qrdbErrors.ErrStorage, "Clear batch delete error")
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Close_ReturnsNil(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	err := idx.Close()
	if err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PutWithContext tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_PutWithContext_Success(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx := context.Background()
	if err := idx.PutWithContext(ctx, []byte("1"), []byte("alice")); err != nil {
		t.Fatalf("PutWithContext: %v", err)
	}

	ids, err := idx.Get([]byte("alice"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("1")) {
		t.Errorf("Get: got %v, want [[1]]", ids)
	}
}

func TestStoragePebbleIndex_PutWithContext_CanceledContext(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := idx.PutWithContext(ctx, []byte("1"), []byte("alice"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrCanceled, "PutWithContext canceled")
}

func TestStoragePebbleIndex_PutWithContext_UniqueViolation(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))

	ctx := context.Background()
	_ = idx.PutWithContext(ctx, []byte("1"), []byte("alice@test.com"))

	err := idx.PutWithContext(ctx, []byte("2"), []byte("alice@test.com"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrConflict, "PutWithContext unique violation")
}

// ---------------------------------------------------------------------------
// GetWithContext tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_GetWithContext_Success(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("42"), []byte("alice"))

	ctx := context.Background()
	ids, err := idx.GetWithContext(ctx, []byte("alice"))
	if err != nil {
		t.Fatalf("GetWithContext: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("42")) {
		t.Errorf("GetWithContext: got %v, want [[42]]", ids)
	}
}

func TestStoragePebbleIndex_GetWithContext_CanceledContext(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := idx.GetWithContext(ctx, []byte("alice"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrCanceled, "GetWithContext canceled")
}

func TestStoragePebbleIndex_GetWithContext_NonExistent(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx := context.Background()
	ids, err := idx.GetWithContext(ctx, []byte("nonexistent"))
	if err != nil {
		t.Fatalf("GetWithContext(nonexistent): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetWithContext(nonexistent): got %v, want empty", ids)
	}
}

// ---------------------------------------------------------------------------
// DeleteWithContext tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_DeleteWithContext_Success(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alice"))

	ctx := context.Background()
	if err := idx.DeleteWithContext(ctx, []byte("1"), []byte("alice")); err != nil {
		t.Fatalf("DeleteWithContext: %v", err)
	}

	ids, _ := idx.Get([]byte("alice"))
	if len(ids) != 0 {
		t.Errorf("Get after DeleteWithContext: got %v, want empty", ids)
	}
}

func TestStoragePebbleIndex_DeleteWithContext_CanceledContext(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := idx.DeleteWithContext(ctx, []byte("1"), []byte("alice"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrCanceled, "DeleteWithContext canceled")
}

func TestStoragePebbleIndex_DeleteWithContext_NonExistent(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx := context.Background()
	err := idx.DeleteWithContext(ctx, []byte("999"), []byte("nonexistent"))
	if err != nil {
		t.Fatalf("DeleteWithContext(nonexistent): unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ScanWithContext tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_ScanWithContext_Success(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("gamma"))

	ctx := context.Background()
	result, err := idx.ScanWithContext(ctx, []byte("alpha"), []byte("zzzz"))
	if err != nil {
		t.Fatalf("ScanWithContext: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("ScanWithContext: got %d entries, want 3", len(result))
	}
}

func TestStoragePebbleIndex_ScanWithContext_CanceledContext(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := idx.ScanWithContext(ctx, []byte("a"), []byte("z"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrCanceled, "ScanWithContext canceled")
}

func TestStoragePebbleIndex_ScanWithContext_NilBounds(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))

	ctx := context.Background()
	result, err := idx.ScanWithContext(ctx, nil, nil)
	if err != nil {
		t.Fatalf("ScanWithContext(nil, nil): %v", err)
	}
	if len(result) != 2 {
		t.Errorf("ScanWithContext(nil, nil): got %d entries, want 2", len(result))
	}
}

// ---------------------------------------------------------------------------
// checkUniqueConstraint tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_CheckUniqueConstraint_NoExisting(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))

	// No existing entries, constraint should pass.
	err := idx.checkUniqueConstraint([]byte("1"), []byte("alice@test.com"))
	if err != nil {
		t.Fatalf("checkUniqueConstraint(no existing): unexpected error: %v", err)
	}
}

func TestStoragePebbleIndex_CheckUniqueConstraint_SameEntity(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))
	_ = idx.Put([]byte("1"), []byte("alice@test.com"))

	// Same entity key should be allowed.
	err := idx.checkUniqueConstraint([]byte("1"), []byte("alice@test.com"))
	if err != nil {
		t.Fatalf("checkUniqueConstraint(same entity): unexpected error: %v", err)
	}
}

func TestStoragePebbleIndex_CheckUniqueConstraint_Violation(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))
	_ = idx.Put([]byte("1"), []byte("alice@test.com"))

	// Different entity key with same value should fail.
	err := idx.checkUniqueConstraint([]byte("2"), []byte("alice@test.com"))
	assertDragonErrorCode(t, err, qrdbErrors.ErrConflict, "unique violation")
}

// ---------------------------------------------------------------------------
// buildIndexPrefix tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_BuildIndexPrefix(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("users"))

	prefix := idx.buildIndexPrefix()
	expected := "idx:users:"
	if string(prefix) != expected {
		t.Errorf("buildIndexPrefix: got %q, want %q", string(prefix), expected)
	}
}

func TestStoragePebbleIndex_BuildIndexPrefix_SpecialChars(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("my-index_v2"))

	prefix := idx.buildIndexPrefix()
	expected := "idx:my-index_v2:"
	if string(prefix) != expected {
		t.Errorf("buildIndexPrefix: got %q, want %q", string(prefix), expected)
	}
}

// ---------------------------------------------------------------------------
// extractEntityKey tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_ExtractEntityKey_ValidKey(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	// Build a key for entity "42" with field value "alice".
	key := idx.buildKey([]byte("alice"), []byte("42"))

	entityKey, err := idx.extractEntityKey(key)
	if err != nil {
		t.Fatalf("extractEntityKey: %v", err)
	}
	if !bytes.Equal(entityKey, []byte("42")) {
		t.Errorf("extractEntityKey: got %s, want 42", entityKey)
	}
}

func TestStoragePebbleIndex_ExtractEntityKey_EmptyKey(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	_, err := idx.extractEntityKey([]byte{})
	assertDragonErrorCode(t, err, qrdbErrors.ErrInvalidInput, "empty key")
}

// ---------------------------------------------------------------------------
// parseKey tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_ParseKey_ValidKey(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	key := idx.buildKey([]byte("alice"), []byte("42"))

	fieldValue, entityKey, err := idx.parseKey(key)
	if err != nil {
		t.Fatalf("parseKey: %v", err)
	}
	if string(fieldValue) != "alice" {
		t.Errorf("parseKey field value: got %q, want %q", string(fieldValue), "alice")
	}
	if !bytes.Equal(entityKey, []byte("42")) {
		t.Errorf("parseKey entity key: got %s, want 42", entityKey)
	}
}

func TestStoragePebbleIndex_ParseKey_EmptyFieldValue(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	key := idx.buildKey([]byte(""), []byte("1"))

	fieldValue, entityKey, err := idx.parseKey(key)
	if err != nil {
		t.Fatalf("parseKey(empty field): %v", err)
	}
	if len(fieldValue) != 0 {
		t.Errorf("parseKey field value: got %q, want empty", string(fieldValue))
	}
	if !bytes.Equal(entityKey, []byte("1")) {
		t.Errorf("parseKey entity key: got %s, want 1", entityKey)
	}
}

func TestStoragePebbleIndex_ParseKey_WrongPrefix(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	// Key with a different index name prefix should fail.
	wrongKey := []byte("idx:other:alice:key1")

	_, _, err := idx.parseKey(wrongKey)
	assertDragonErrorCode(t, err, qrdbErrors.ErrInvalidInput, "wrong prefix")
}

func TestStoragePebbleIndex_ParseKey_TooShort(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	// Key that matches prefix but is too short for entity key.
	shortKey := []byte("idx:test:")

	_, _, err := idx.parseKey(shortKey)
	assertDragonErrorCode(t, err, qrdbErrors.ErrInvalidInput, "key too short")
}

func TestStoragePebbleIndex_ParseKey_LargeEntityKey(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	largeKey := []byte("a-very-long-entity-key-that-exceeds-typical-lengths-1234567890")
	key := idx.buildKey([]byte("data"), largeKey)

	fieldValue, entityKey, err := idx.parseKey(key)
	if err != nil {
		t.Fatalf("parseKey(large entity key): %v", err)
	}
	if string(fieldValue) != "data" {
		t.Errorf("parseKey field value: got %q, want %q", string(fieldValue), "data")
	}
	if !bytes.Equal(entityKey, largeKey) {
		t.Errorf("parseKey entity key: got %s, want %s", entityKey, largeKey)
	}
}

// ---------------------------------------------------------------------------
// storageIncrementBytes tests
// ---------------------------------------------------------------------------

func TestStorageIncrementBytes_NormalByte(t *testing.T) {
	input := []byte{0x00, 0x01}
	got := storageIncrementBytes(input)
	if len(got) != 2 || got[0] != 0x00 || got[1] != 0x02 {
		t.Errorf("storageIncrementBytes: got %v, want [0x00, 0x02]", got)
	}
}

func TestStorageIncrementBytes_AllFF(t *testing.T) {
	input := []byte{0xFF, 0xFF}
	got := storageIncrementBytes(input)
	// When all bytes overflow, a 0x00 byte is appended.
	if len(got) != 3 || got[0] != 0 || got[1] != 0 || got[2] != 0 {
		t.Errorf("storageIncrementBytes(all 0xFF): got %v, want [0x00, 0x00, 0x00]", got)
	}
}

func TestStorageIncrementBytes_SingleByte(t *testing.T) {
	input := []byte{0x41}
	got := storageIncrementBytes(input)
	if len(got) != 1 || got[0] != 0x42 {
		t.Errorf("storageIncrementBytes: got %v, want [0x42]", got)
	}
}

func TestStorageIncrementBytes_CarryOver(t *testing.T) {
	input := []byte{0x01, 0xFF}
	got := storageIncrementBytes(input)
	if len(got) != 2 || got[0] != 0x02 || got[1] != 0x00 {
		t.Errorf("storageIncrementBytes(carry): got %v, want [0x02, 0x00]", got)
	}
}

func TestStorageIncrementBytes_Nil(t *testing.T) {
	got := storageIncrementBytes(nil)
	if got != nil {
		t.Errorf("storageIncrementBytes(nil): got %v, want nil", got)
	}
}

func TestStorageIncrementBytes_Empty(t *testing.T) {
	got := storageIncrementBytes([]byte{})
	// Empty slice incremented: no bytes to increment, so append 0x00.
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("storageIncrementBytes(empty): got %v, want [0x00]", got)
	}
}

func TestStorageIncrementBytes_DoesNotMutateInput(t *testing.T) {
	input := []byte{0x41, 0x42}
	original := make([]byte, len(input))
	copy(original, input)

	_ = storageIncrementBytes(input)

	// Original should be unchanged.
	for i := range input {
		if input[i] != original[i] {
			t.Errorf("storageIncrementBytes mutated input at index %d: got 0x%02x, want 0x%02x", i, input[i], original[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Config / StoragePebbleIndexName / StoragePebbleIndexType tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Config_ReturnsConfiguration(t *testing.T) {
	cfg := IndexConfig{
		Name:          "users",
		Type:          IndexTypeUnique,
		CaseSensitive: true,
		Unique:        true,
	}
	idx := newTestStoragePebbleIndex(t, cfg)

	got := idx.Config()
	if got.Name != cfg.Name {
		t.Errorf("Config().Name: got %q, want %q", got.Name, cfg.Name)
	}
	if got.Type != cfg.Type {
		t.Errorf("Config().Type: got %v, want %v", got.Type, cfg.Type)
	}
	if got.CaseSensitive != cfg.CaseSensitive {
		t.Errorf("Config().CaseSensitive: got %v, want %v", got.CaseSensitive, cfg.CaseSensitive)
	}
}

func TestStoragePebbleIndex_Config_PreservesAllFields(t *testing.T) {
	cfg := IndexConfig{
		Name:          "sparse_idx",
		Type:          IndexTypeNormal,
		CaseSensitive: false,
		Unique:        false,
		Sparse:        true,
	}
	idx := newTestStoragePebbleIndex(t, cfg)

	got := idx.Config()
	if got.Sparse != true {
		t.Errorf("Config().Sparse: got %v, want true", got.Sparse)
	}
	if got.CaseSensitive != false {
		t.Errorf("Config().CaseSensitive: got %v, want false", got.CaseSensitive)
	}
}

func TestStoragePebbleIndex_StoragePebbleIndexName_ReturnsName(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("myindex"))

	if got := idx.StoragePebbleIndexName(); got != "myindex" {
		t.Errorf("StoragePebbleIndexName: got %q, want %q", got, "myindex")
	}
}

func TestStoragePebbleIndex_StoragePebbleIndexType_ReturnsType(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("email"))

	if got := idx.StoragePebbleIndexType(); got != IndexTypeUnique {
		t.Errorf("StoragePebbleIndexType: got %v, want %v", got, IndexTypeUnique)
	}
}

func TestStoragePebbleIndex_StoragePebbleIndexType_Normal(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("tags"))

	if got := idx.StoragePebbleIndexType(); got != IndexTypeNormal {
		t.Errorf("StoragePebbleIndexType: got %v, want %v", got, IndexTypeNormal)
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_ImplementsIndex(t *testing.T) {
	var _ Index = (*StoragePebbleIndex)(nil)
}

func TestStoragePebbleIndex_ImplementsIndexWithContext(t *testing.T) {
	var _ IndexWithContext = (*StoragePebbleIndex)(nil)
}

// ---------------------------------------------------------------------------
// Index isolation (multiple indexes in same storage)
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Isolation_DifferentIndexes(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	log := testLogger(t)

	idx1, _ := NewStoragePebbleIndex(s, normalConfig("users"), log)
	idx2, _ := NewStoragePebbleIndex(s, normalConfig("orders"), log)

	_ = idx1.Put([]byte("1"), []byte("alice"))
	_ = idx2.Put([]byte("2"), []byte("order-100"))

	ids1, _ := idx1.GetAll()
	ids2, _ := idx2.GetAll()

	if len(ids1) != 1 || !bytes.Equal(ids1[0], []byte("1")) {
		t.Errorf("users index: got %v, want [[1]]", ids1)
	}
	if len(ids2) != 1 || !bytes.Equal(ids2[0], []byte("2")) {
		t.Errorf("orders index: got %v, want [[2]]", ids2)
	}

	// Get from one index should not see the other.
	crossIDs, _ := idx1.Get([]byte("order-100"))
	if len(crossIDs) != 0 {
		t.Errorf("cross-index Get: got %v, want empty", crossIDs)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_ConcurrentPutGet(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	const goroutines = 10
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers.
	for g := 0; g < goroutines; g++ {
		go func(base int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_ = idx.Put([]byte(fmt.Sprintf("%d", base+i)), []byte("shared"))
			}
		}(g * opsPerGoroutine)
	}

	// Readers.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_, _ = idx.Get([]byte("shared"))
			}
		}()
	}

	wg.Wait()

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	expected := goroutines * opsPerGoroutine
	if len(ids) != expected {
		t.Errorf("GetAll: got %d keys, want %d", len(ids), expected)
	}
}

func TestStoragePebbleIndex_ConcurrentPutDelete(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	const count = 100

	// Populate.
	for i := 0; i < count; i++ {
		_ = idx.Put([]byte(fmt.Sprintf("%d", i)), []byte("val"))
	}

	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(id int) {
			defer wg.Done()
			_ = idx.Delete([]byte(fmt.Sprintf("%d", id)), []byte("val"))
		}(i)
	}
	wg.Wait()

	ids, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("GetAll after concurrent delete: got %d keys, want 0", len(ids))
	}
}

// ---------------------------------------------------------------------------
// End-to-end: full lifecycle
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_FullLifecycle(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("lifecycle"))

	// Put entries.
	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("alpha")) // Multiple entities for same value.

	// Get specific value.
	ids, err := idx.Get([]byte("alpha"))
	if err != nil {
		t.Fatalf("Get(alpha): %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("Get(alpha): got %d, want 2", len(ids))
	}

	// Scan range.
	result, err := idx.Scan([]byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 2 { // "alpha" and "beta"
		t.Errorf("Scan: got %d field values, want 2", len(result))
	}

	// GetAll.
	allIDs, err := idx.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(allIDs) != 3 {
		t.Errorf("GetAll: got %d, want 3", len(allIDs))
	}

	// Delete one.
	_ = idx.Delete([]byte("1"), []byte("alpha"))
	ids, _ = idx.Get([]byte("alpha"))
	if len(ids) != 1 || !bytes.Equal(ids[0], []byte("3")) {
		t.Errorf("Get after delete: got %v, want [[3]]", ids)
	}

	// Clear all.
	_ = idx.Clear()
	allIDs, _ = idx.GetAll()
	if len(allIDs) != 0 {
		t.Errorf("GetAll after clear: got %v, want empty", allIDs)
	}

	// Close.
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// buildKey round-trip tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_BuildKey_RoundTrip(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	testCases := []struct {
		name       string
		fieldValue string
		entityKey  []byte
	}{
		{"simple", "alice", []byte("1")},
		{"large_key", "data", []byte("a-very-long-entity-key-1234567890")},
		{"empty_entity_key", "value", []byte("")},
		{"empty_field", "", []byte("42")},
		{"long_field", "a-very-long-field-value-that-exceeds-typical-lengths", []byte("100")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := idx.buildKey([]byte(tc.fieldValue), tc.entityKey)

			// extractEntityKey
			gotKey, err := idx.extractEntityKey(key)
			if err != nil {
				t.Fatalf("extractEntityKey: %v", err)
			}
			if !bytes.Equal(gotKey, tc.entityKey) {
				t.Errorf("extractEntityKey: got %s, want %s", gotKey, tc.entityKey)
			}

			// parseKey
			gotField, gotKey2, err := idx.parseKey(key)
			if err != nil {
				t.Fatalf("parseKey: %v", err)
			}
			if string(gotField) != tc.fieldValue {
				t.Errorf("parseKey field: got %q, want %q", string(gotField), tc.fieldValue)
			}
			if !bytes.Equal(gotKey2, tc.entityKey) {
				t.Errorf("parseKey entity key: got %s, want %s", gotKey2, tc.entityKey)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scan with complex data
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_Scan_AfterDeleteSomeEntries(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	_ = idx.Put([]byte("1"), []byte("alpha"))
	_ = idx.Put([]byte("2"), []byte("beta"))
	_ = idx.Put([]byte("3"), []byte("gamma"))
	_ = idx.Delete([]byte("2"), []byte("beta"))

	result, err := idx.Scan(nil, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Should have alpha and gamma, not beta.
	if len(result) != 2 {
		t.Errorf("Scan after delete: got %d entries, want 2 (got %v)", len(result), result)
	}
	if _, ok := result["beta"]; ok {
		t.Error("Scan: should not contain deleted 'beta'")
	}
}

func TestStoragePebbleIndex_Scan_SingleEntry(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))
	_ = idx.Put([]byte("1"), []byte("only"))

	result, err := idx.Scan([]byte("only"), []byte("onlz"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Scan(single): got %d entries, want 1", len(result))
	}
}

// ---------------------------------------------------------------------------
// PutBatch / DeleteBatch tests
// ---------------------------------------------------------------------------

func TestStoragePebbleIndex_PutBatch(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	entityKey := []byte("entity-1")
	fieldValue := []byte("value-1")

	if err := idx.PutBatch(batch, entityKey, fieldValue); err != nil {
		t.Fatalf("PutBatch: %v", err)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("batch.Commit: %v", err)
	}

	got, err := idx.Get(fieldValue)
	if err != nil {
		t.Fatalf("Get after PutBatch+Commit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Get: got %d entity keys, want 1", len(got))
	}
	if !bytes.Equal(got[0], entityKey) {
		t.Errorf("Get: got entity key %q, want %q", got[0], entityKey)
	}
}

func TestStoragePebbleIndex_DeleteBatch(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	entityKey := []byte("entity-1")
	fieldValue := []byte("value-1")

	// Put an entry directly first.
	if err := idx.Put(entityKey, fieldValue); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verify it exists before deletion.
	got, err := idx.Get(fieldValue)
	if err != nil {
		t.Fatalf("Get before delete: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Get before delete: got %d entity keys, want 1", len(got))
	}

	// Delete via batch.
	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	if err := idx.DeleteBatch(batch, entityKey, fieldValue); err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("batch.Commit: %v", err)
	}

	got, err = idx.Get(fieldValue)
	if err != nil {
		t.Fatalf("Get after DeleteBatch+Commit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get after DeleteBatch+Commit: got %d entity keys, want 0", len(got))
	}
}

func TestStoragePebbleIndex_PutBatch_UniqueConstraintViolation(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("test"))

	fieldValue := []byte("shared-value")

	// Insert the first entity directly.
	if err := idx.Put([]byte("entity-1"), fieldValue); err != nil {
		t.Fatalf("Put(entity-1): %v", err)
	}

	// Attempt to insert a different entity with the same field value via batch.
	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	err := idx.PutBatch(batch, []byte("entity-2"), fieldValue)
	assertDragonErrorCode(t, err, qrdbErrors.ErrConflict, "PutBatch unique violation")
}

func TestStoragePebbleIndex_PutBatch_UniqueConstraintSameEntity(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, uniqueConfig("test"))

	entityKey := []byte("entity-1")
	fieldValue := []byte("unique-value")

	// Insert the entity directly.
	if err := idx.Put(entityKey, fieldValue); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Re-insert the same entity with the same field value via batch (update).
	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	if err := idx.PutBatch(batch, entityKey, fieldValue); err != nil {
		t.Fatalf("PutBatch same entity: expected no error, got %v", err)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("batch.Commit: %v", err)
	}

	got, err := idx.Get(fieldValue)
	if err != nil {
		t.Fatalf("Get after re-put: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Get after re-put: got %d entity keys, want 1", len(got))
	}
	if !bytes.Equal(got[0], entityKey) {
		t.Errorf("Get after re-put: got entity key %q, want %q", got[0], entityKey)
	}
}

func TestStoragePebbleIndex_BatchAtomicity(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	// Queue two entries in the batch.
	if err := idx.PutBatch(batch, []byte("entity-1"), []byte("field-1")); err != nil {
		t.Fatalf("PutBatch(entity-1): %v", err)
	}
	if err := idx.PutBatch(batch, []byte("entity-2"), []byte("field-2")); err != nil {
		t.Fatalf("PutBatch(entity-2): %v", err)
	}

	// Rollback instead of committing.
	if err := batch.Rollback(); err != nil {
		t.Fatalf("batch.Rollback: %v", err)
	}

	// Neither entry should be visible.
	got1, err := idx.Get([]byte("field-1"))
	if err != nil {
		t.Fatalf("Get(field-1): %v", err)
	}
	if len(got1) != 0 {
		t.Errorf("Get(field-1) after rollback: got %d entity keys, want 0", len(got1))
	}

	got2, err := idx.Get([]byte("field-2"))
	if err != nil {
		t.Fatalf("Get(field-2): %v", err)
	}
	if len(got2) != 0 {
		t.Errorf("Get(field-2) after rollback: got %d entity keys, want 0", len(got2))
	}
}

func TestStoragePebbleIndex_BatchableInterfaceAssertion(t *testing.T) {
	idx := newTestStoragePebbleIndex(t, normalConfig("test"))

	// Verify that the concrete type satisfies BatchableIndex at runtime.
	var bi BatchableIndex = idx
	_ = bi

	// Exercise the interface methods through the interface value.
	batch := idx.storage.Batch()
	defer func() { _ = batch.Close() }()

	entityKey := []byte("iface-entity")
	fieldValue := []byte("iface-value")

	if err := bi.PutBatch(batch, entityKey, fieldValue); err != nil {
		t.Fatalf("BatchableIndex.PutBatch: %v", err)
	}

	if err := batch.Commit(); err != nil {
		t.Fatalf("batch.Commit: %v", err)
	}

	got, err := bi.Get(fieldValue)
	if err != nil {
		t.Fatalf("BatchableIndex.Get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("BatchableIndex.Get: got %d entity keys, want 1", len(got))
	}
	if !bytes.Equal(got[0], entityKey) {
		t.Errorf("BatchableIndex.Get: got %q, want %q", got[0], entityKey)
	}
}
