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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newTestBackend creates a StandaloneBackend backed by in-memory storage
// for use in tests.
func newTestBackend(t *testing.T) *StandaloneBackend {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() {
		_ = store.Close()
	})
	return NewStandaloneBackend(store)
}

func TestStandaloneBackend_InterfaceCompliance(t *testing.T) {
	// Compile-time check: StandaloneBackend must implement Backend.
	var _ Backend = (*StandaloneBackend)(nil)
}

func TestStandaloneBackend_Get(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	key := []byte("greeting")
	value := []byte("hello")

	if err := b.Put(ctx, key, value); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if string(got) != string(value) {
		t.Errorf("Get: got %q, want %q", got, value)
	}
}

func TestStandaloneBackend_Get_NonExistent(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	got, err := b.Get(ctx, []byte("does-not-exist"))
	if err != nil {
		t.Fatalf("Get non-existent: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get non-existent: got %q, want nil", got)
	}
}

func TestStandaloneBackend_Put(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	key := []byte("color")
	value := []byte("blue")

	if err := b.Put(ctx, key, value); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Put: unexpected error: %v", err)
	}
	if string(got) != string(value) {
		t.Errorf("Get after Put: got %q, want %q", got, value)
	}
}

func TestStandaloneBackend_Put_Overwrite(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	key := []byte("counter")
	first := []byte("1")
	second := []byte("2")

	if err := b.Put(ctx, key, first); err != nil {
		t.Fatalf("Put first: unexpected error: %v", err)
	}
	if err := b.Put(ctx, key, second); err != nil {
		t.Fatalf("Put second: unexpected error: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after overwrite: unexpected error: %v", err)
	}
	if string(got) != string(second) {
		t.Errorf("Get after overwrite: got %q, want %q", got, second)
	}
}

func TestStandaloneBackend_Delete(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	key := []byte("ephemeral")
	value := []byte("temporary")

	if err := b.Put(ctx, key, value); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Delete: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get after Delete: got %q, want nil", got)
	}
}

func TestStandaloneBackend_Delete_NonExistent(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Deleting a key that was never stored should not return an error.
	err := b.Delete(ctx, []byte("phantom"))
	if err != nil {
		t.Fatalf("Delete non-existent: unexpected error: %v", err)
	}
}

func TestStandaloneBackend_List(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Insert keys with a shared prefix.
	pairs := []struct {
		key, value string
	}{
		{"users/alice", "alice-data"},
		{"users/bob", "bob-data"},
		{"users/charlie", "charlie-data"},
		{"config/timeout", "30s"},
	}
	for _, p := range pairs {
		if err := b.Put(ctx, []byte(p.key), []byte(p.value)); err != nil {
			t.Fatalf("Put %q: unexpected error: %v", p.key, err)
		}
	}

	keys, err := b.List(ctx, "users/")
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("List: got %d keys, want 3", len(keys))
	}

	// Keys should be sorted by the btree.
	expected := []string{"users/alice", "users/bob", "users/charlie"}
	for i, want := range expected {
		if keys[i] != want {
			t.Errorf("List[%d]: got %q, want %q", i, keys[i], want)
		}
	}
}

func TestStandaloneBackend_List_Empty(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	keys, err := b.List(ctx, "nonexistent/")
	if err != nil {
		t.Fatalf("List empty: unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List empty: got %d keys, want 0", len(keys))
	}
}

func TestStandaloneBackend_Range(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Insert keys a, b, c, d.
	for _, k := range []string{"a", "b", "c", "d"} {
		if err := b.Put(ctx, []byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Put %q: unexpected error: %v", k, err)
		}
	}

	// Range [b, d) should return b, c.
	results, err := b.Range(ctx, []byte("b"), []byte("d"), 0)
	if err != nil {
		t.Fatalf("Range: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Range: got %d results, want 2", len(results))
	}

	if string(results[0].Key) != "b" || string(results[0].Value) != "val-b" {
		t.Errorf("Range[0]: got key=%q value=%q, want key=%q value=%q",
			results[0].Key, results[0].Value, "b", "val-b")
	}
	if string(results[1].Key) != "c" || string(results[1].Value) != "val-c" {
		t.Errorf("Range[1]: got key=%q value=%q, want key=%q value=%q",
			results[1].Key, results[1].Value, "c", "val-c")
	}
}

func TestStandaloneBackend_Range_WithLimit(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := b.Put(ctx, []byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Put %q: unexpected error: %v", k, err)
		}
	}

	// Range [b, d) with limit=1 should return only b.
	results, err := b.Range(ctx, []byte("b"), []byte("d"), 1)
	if err != nil {
		t.Fatalf("Range with limit: unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Range with limit: got %d results, want 1", len(results))
	}
	if string(results[0].Key) != "b" {
		t.Errorf("Range with limit: got key=%q, want %q", results[0].Key, "b")
	}
}

func TestStandaloneBackend_Range_Empty(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Range over an empty store should return an empty (nil) slice.
	results, err := b.Range(ctx, []byte("a"), []byte("z"), 0)
	if err != nil {
		t.Fatalf("Range empty: unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Range empty: got %d results, want 0", len(results))
	}
}

func TestStandaloneBackend_Batch(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	ops := []BatchOp{
		{Op: BatchOpPut, Key: []byte("batch-a"), Value: []byte("alpha")},
		{Op: BatchOpPut, Key: []byte("batch-b"), Value: []byte("bravo")},
		{Op: BatchOpPut, Key: []byte("batch-c"), Value: []byte("charlie")},
	}

	if err := b.Batch(ctx, ops); err != nil {
		t.Fatalf("Batch: unexpected error: %v", err)
	}

	// Verify all keys were written.
	for _, op := range ops {
		got, err := b.Get(ctx, op.Key)
		if err != nil {
			t.Fatalf("Get %q after batch: unexpected error: %v", op.Key, err)
		}
		if string(got) != string(op.Value) {
			t.Errorf("Get %q after batch: got %q, want %q", op.Key, got, op.Value)
		}
	}
}

func TestStandaloneBackend_Batch_MixedOps(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Pre-populate a key that will be deleted in the batch.
	if err := b.Put(ctx, []byte("remove-me"), []byte("doomed")); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	ops := []BatchOp{
		{Op: BatchOpPut, Key: []byte("new-key"), Value: []byte("new-value")},
		{Op: BatchOpDelete, Key: []byte("remove-me")},
	}

	if err := b.Batch(ctx, ops); err != nil {
		t.Fatalf("Batch mixed: unexpected error: %v", err)
	}

	// Verify the put succeeded.
	got, err := b.Get(ctx, []byte("new-key"))
	if err != nil {
		t.Fatalf("Get new-key: unexpected error: %v", err)
	}
	if string(got) != "new-value" {
		t.Errorf("Get new-key: got %q, want %q", got, "new-value")
	}

	// Verify the delete succeeded.
	got, err = b.Get(ctx, []byte("remove-me"))
	if err != nil {
		t.Fatalf("Get remove-me: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get remove-me: got %q, want nil", got)
	}
}

func TestStandaloneBackend_Batch_Empty(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// An empty batch should succeed without error.
	if err := b.Batch(ctx, []BatchOp{}); err != nil {
		t.Fatalf("Batch empty: unexpected error: %v", err)
	}
}

func TestStandaloneBackend_Close(t *testing.T) {
	store := memory.NewStorage()
	b := NewStandaloneBackend(store)

	if err := b.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

func TestStandaloneBackend_List_StorageError(t *testing.T) {
	// Use a mock storage that returns an error from List.
	listErr := &NotFoundError{Name: "list-storage-fail"}
	mock := &mockStandaloneStorage{listErr: listErr}
	b := NewStandaloneBackend(mock)
	ctx := context.Background()

	_, err := b.List(ctx, "prefix/")
	if err == nil {
		t.Fatal("List: expected error from storage, got nil")
	}
	if err != listErr {
		t.Errorf("List: got error %v, want %v", err, listErr)
	}
}

func TestStandaloneBackend_Batch_SetError(t *testing.T) {
	// Use a mock storage that returns an error from Batch.Set.
	setErr := errors.New("batch set failed")
	mock := &mockStandaloneStorage{batchSetErr: setErr}
	b := NewStandaloneBackend(mock)
	ctx := context.Background()

	ops := []BatchOp{
		{Op: BatchOpPut, Key: []byte("k"), Value: []byte("v")},
	}
	err := b.Batch(ctx, ops)
	if err == nil {
		t.Fatal("Batch: expected error from batch.Set, got nil")
	}
	if err != setErr {
		t.Errorf("Batch: got error %v, want %v", err, setErr)
	}
}

func TestStandaloneBackend_Batch_CommitError(t *testing.T) {
	// Use a mock storage that returns an error from Batch.Commit.
	commitErr := errors.New("batch commit failed")
	mock := &mockStandaloneStorage{batchCommitErr: commitErr}
	b := NewStandaloneBackend(mock)
	ctx := context.Background()

	// A delete-only batch skips Set and exercises the Commit error path.
	ops := []BatchOp{
		{Op: BatchOpDelete, Key: []byte("k")},
	}
	err := b.Batch(ctx, ops)
	if err == nil {
		t.Fatal("Batch: expected error from batch.Commit, got nil")
	}
	if err != commitErr {
		t.Errorf("Batch: got error %v, want %v", err, commitErr)
	}
}

// ---------------------------------------------------------------------------
// Mock storage and batch for standalone backend error-path tests
// ---------------------------------------------------------------------------

// mockStandaloneStorage implements storage.Storage for test error injection.
type mockStandaloneStorage struct {
	listErr        error
	batchSetErr    error
	batchCommitErr error
}

func (m *mockStandaloneStorage) Get(_ []byte) ([]byte, error) { return nil, nil }
func (m *mockStandaloneStorage) Set(_, _ []byte) error        { return nil }
func (m *mockStandaloneStorage) Delete(_ []byte) error        { return nil }

func (m *mockStandaloneStorage) DeleteRange(_, _ []byte) error { return nil }
func (m *mockStandaloneStorage) Exists(_ []byte) (bool, error) { return false, nil }
func (m *mockStandaloneStorage) Close() error                  { return nil }
func (m *mockStandaloneStorage) List(_ []byte) ([][]byte, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return nil, nil
}
func (m *mockStandaloneStorage) Batch() storage.Batch {
	return &mockStandaloneBatch{
		setErr:    m.batchSetErr,
		commitErr: m.batchCommitErr,
	}
}
func (m *mockStandaloneStorage) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return &emptyStandaloneIterator{}
}
func (m *mockStandaloneStorage) Snapshot() (storage.Snapshot, error) { return nil, nil }

// mockStandaloneBatch implements storage.Batch.
type mockStandaloneBatch struct {
	setErr    error
	commitErr error
}

func (b *mockStandaloneBatch) Set(_, _ []byte) error { return b.setErr }
func (b *mockStandaloneBatch) Delete(_ []byte) error { return nil }

func (b *mockStandaloneBatch) DeleteRange(_, _ []byte) error { return nil }
func (b *mockStandaloneBatch) Get(_ []byte) ([]byte, error)  { return nil, nil }
func (b *mockStandaloneBatch) Commit() error                 { return b.commitErr }
func (b *mockStandaloneBatch) Rollback() error               { return nil }
func (b *mockStandaloneBatch) Close() error                  { return nil }
func (b *mockStandaloneBatch) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return &emptyStandaloneIterator{}
}

// emptyStandaloneIterator implements storage.Iterator and always returns false.
type emptyStandaloneIterator struct{}

func (i *emptyStandaloneIterator) SeekToFirst() bool  { return false }
func (i *emptyStandaloneIterator) SeekToLast() bool   { return false }
func (i *emptyStandaloneIterator) Seek(_ []byte) bool { return false }
func (i *emptyStandaloneIterator) Next() bool         { return false }
func (i *emptyStandaloneIterator) Prev() bool         { return false }
func (i *emptyStandaloneIterator) Valid() bool        { return false }
func (i *emptyStandaloneIterator) Key() []byte        { return nil }
func (i *emptyStandaloneIterator) Value() []byte      { return nil }
func (i *emptyStandaloneIterator) Error() error       { return nil }
func (i *emptyStandaloneIterator) Close() error       { return nil }

func TestStandaloneBackend_RangeIteratorError(t *testing.T) {
	// Verify the RangeIteratorError type satisfies the error interface
	// and correctly formats and unwraps.
	underlying := errors.New("disk failure")
	rangeErr := &RangeIteratorError{Err: underlying}

	want := "database: range iterator error: disk failure"
	if rangeErr.Error() != want {
		t.Errorf("RangeIteratorError.Error: got %q, want %q", rangeErr.Error(), want)
	}

	if !errors.Is(rangeErr, underlying) {
		t.Errorf("RangeIteratorError.Unwrap: errors.Is did not match underlying error")
	}

	var target *RangeIteratorError
	if !errors.As(rangeErr, &target) {
		t.Errorf("RangeIteratorError: errors.As did not match *RangeIteratorError")
	}
}
