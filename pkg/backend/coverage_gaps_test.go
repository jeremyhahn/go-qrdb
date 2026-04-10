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

package backend

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// failStorage is a test-only storage.Storage that returns errors on demand.
// ---------------------------------------------------------------------------

// failStorage wraps a delegate storage.Storage and injects errors for
// specific operations. Fields are toggled by individual tests to exercise
// error paths in SimpleStateMachine.
type failStorage struct {
	delegate storage.Storage
	failSet  bool
	failDel  bool
	failList bool
	setErr   error
	delErr   error
	listErr  error
}

func newFailStorage(d storage.Storage) *failStorage {
	return &failStorage{delegate: d}
}

func (f *failStorage) Get(key []byte) ([]byte, error) {
	return f.delegate.Get(key)
}

func (f *failStorage) Set(key, value []byte) error {
	if f.failSet {
		return f.setErr
	}
	return f.delegate.Set(key, value)
}

func (f *failStorage) Delete(key []byte) error {
	if f.failDel {
		return f.delErr
	}
	return f.delegate.Delete(key)
}

func (f *failStorage) List(prefix []byte) ([][]byte, error) {
	if f.failList {
		return nil, f.listErr
	}
	return f.delegate.List(prefix)
}

func (f *failStorage) Exists(key []byte) (bool, error) {
	return f.delegate.Exists(key)
}

func (f *failStorage) Batch() storage.Batch {
	return f.delegate.Batch()
}

func (f *failStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return f.delegate.NewIterator(opts)
}

func (f *failStorage) Snapshot() (storage.Snapshot, error) {
	return f.delegate.Snapshot()
}

func (f *failStorage) Close() error {
	return f.delegate.Close()
}

// ---------------------------------------------------------------------------
// SimpleStateMachine.handlePut — storage.Set error branch
// ---------------------------------------------------------------------------

// TestSimpleStateMachine_HandlePut_StorageError verifies that when the
// underlying storage returns an error from Set, Update propagates that error
// and marks the result with Value=1.
func TestSimpleStateMachine_HandlePut_StorageError(t *testing.T) {
	storeErr := stderrors.New("storage set failure")
	fs := newFailStorage(memory.NewStorage())
	fs.failSet = true
	fs.setErr = storeErr

	ssm := NewSimpleStateMachine(fs, nil)
	defer func() { _ = ssm.Close(context.Background()) }()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	gotErr := ssm.Update(context.Background(), entries, results)
	if gotErr == nil {
		t.Fatal("expected error from Update when storage.Set fails")
	}
	if !stderrors.Is(gotErr, storeErr) {
		t.Errorf("expected storeErr, got %v", gotErr)
	}
	if results[0].Value != 1 {
		t.Errorf("expected failure marker Value=1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// SimpleStateMachine.handleDelete — storage.Delete error branch
// ---------------------------------------------------------------------------

// TestSimpleStateMachine_HandleDelete_StorageError verifies that when the
// underlying storage returns an error from Delete, Update propagates that
// error and marks the result with Value=1.
func TestSimpleStateMachine_HandleDelete_StorageError(t *testing.T) {
	delErr := stderrors.New("storage delete failure")
	fs := newFailStorage(memory.NewStorage())
	fs.failDel = true
	fs.delErr = delErr

	ssm := NewSimpleStateMachine(fs, nil)
	defer func() { _ = ssm.Close(context.Background()) }()

	cmd, err := kv.EncodeDelete([]byte("k1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	gotErr := ssm.Update(context.Background(), entries, results)
	if gotErr == nil {
		t.Fatal("expected error from Update when storage.Delete fails")
	}
	if !stderrors.Is(gotErr, delErr) {
		t.Errorf("expected delErr, got %v", gotErr)
	}
	if results[0].Value != 1 {
		t.Errorf("expected failure marker Value=1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// SimpleStateMachine.Lookup — storage.List error branch
// ---------------------------------------------------------------------------

// TestSimpleStateMachine_Lookup_ListError verifies that a storage.List
// failure is propagated through Lookup for a kv.ListQuery.
func TestSimpleStateMachine_Lookup_ListError(t *testing.T) {
	listErr := stderrors.New("storage list failure")
	fs := newFailStorage(memory.NewStorage())
	fs.failList = true
	fs.listErr = listErr

	ssm := NewSimpleStateMachine(fs, nil)
	defer func() { _ = ssm.Close(context.Background()) }()

	_, gotErr := ssm.Lookup(context.Background(), kv.ListQuery{Prefix: "app/"})
	if gotErr == nil {
		t.Fatal("expected error from Lookup when storage.List fails")
	}
	if !stderrors.Is(gotErr, listErr) {
		t.Errorf("expected listErr, got %v", gotErr)
	}
}

// ---------------------------------------------------------------------------
// LocalBackend.SyncPropose — sm.Update error branch
// ---------------------------------------------------------------------------

// TestLocalBackend_SyncPropose_UpdateError verifies that SyncPropose
// propagates errors returned by the state machine's Update method.
func TestLocalBackend_SyncPropose_UpdateError(t *testing.T) {
	updateErr := stderrors.New("state machine update failure")
	fs := newFailStorage(memory.NewStorage())
	fs.failSet = true
	fs.setErr = updateErr

	ssm := NewSimpleStateMachine(fs, nil)
	lb := NewLocalBackend(ssm)
	defer func() { _ = lb.Close() }()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	_, gotErr := lb.SyncPropose(context.Background(), 1, cmd)
	if gotErr == nil {
		t.Fatal("expected error from SyncPropose when Update fails")
	}
	if !stderrors.Is(gotErr, updateErr) {
		t.Errorf("expected updateErr, got %v", gotErr)
	}
}
