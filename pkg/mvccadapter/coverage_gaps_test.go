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

package mvccadapter

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newTestAdapterWithStore creates a test Adapter backed by the provided store.
// This allows tests to close or manipulate the store after adapter construction.
func newTestAdapterWithStore(t *testing.T, store storage.Storage) *Adapter {
	t.Helper()
	rs, err := mvcc.NewRaftStorage(&mvcc.RaftStorageConfig{
		Store:  store,
		Logger: newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	return NewAdapter(rs)
}

// ---------------------------------------------------------------------------
// Transaction — BeginRaftTxn error branch
// ---------------------------------------------------------------------------

// TestAdapter_Transaction_BeginRaftTxnError verifies that Transaction
// returns an error when BeginRaftTxn fails because the underlying storage
// is closed (causing snapshot creation to fail).
func TestAdapter_Transaction_BeginRaftTxnError(t *testing.T) {
	adapter := newTestAdapter(t)

	// Close the underlying memStorage so Snapshot() returns ClosedError,
	// which causes BeginRaftTxn to fail.
	//
	// To access the internal store we need to retrieve it. Since we used
	// newTestAdapter which creates a memStorage, we must use a variant
	// that exposes the store. Build a fresh adapter with an exposed store.
	store := newMemStorage()
	adapter2 := newTestAdapterWithStore(t, store)

	// Close the store so BeginRaftTxn → Snapshot() fails.
	_ = store.Close()

	_, _, _, err := adapter2.Transaction(nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from Transaction when BeginRaftTxn fails")
	}
	_ = adapter // suppress unused warning
}

// ---------------------------------------------------------------------------
// NewMVCCFastCreateFunc — panic path
// ---------------------------------------------------------------------------

// TestNewMVCCFastCreateFunc_PanicOnNilLogger verifies that calling the
// CreateFunc returned by NewMVCCFastCreateFunc panics when the logger is nil,
// because mvcc.NewRaftStorage requires a non-nil logger.
func TestNewMVCCFastCreateFunc_PanicOnNilLogger(t *testing.T) {
	createFunc := NewMVCCFastCreateFunc(
		func(shardID, replicaID uint64) storage.Storage {
			return memory.NewStorage()
		},
		nil, // nil logger causes NewRaftStorage to return an error
	)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when logger is nil, got none")
		}
	}()

	// Invoke the factory to trigger the panic inside NewMVCCFastCreateFunc.
	createFunc(1, 1)
}
