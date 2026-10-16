//go:build qrdb_vector

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

package hnsw

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// mockStorage provides a thread-safe in-memory storage for testing.
type mockStorage struct {
	data map[string][]byte
	mu   sync.RWMutex
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		data: make(map[string][]byte),
	}
}

func (m *mockStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if val, ok := m.data[string(key)]; ok {
		result := make([]byte, len(val))
		copy(result, val)
		return result, nil
	}
	return nil, errors.New("not found")
}

func (m *mockStorage) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)
	m.data[string(key)] = valueCopy
	return nil
}

func (m *mockStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, string(key))
	return nil
}

func (m *mockStorage) DeleteRange(start, end []byte) error {
	if err := storage.ValidateRange(start, end); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.data {
		if inDeleteRange([]byte(k), start, end) {
			delete(m.data, k)
		}
	}
	return nil
}

// inDeleteRange reports whether key falls in [start, end); a nil end runs to
// the end of the keyspace, as storage.Storage.DeleteRange defines it.
func inDeleteRange(key, start, end []byte) bool {
	return bytes.Compare(key, start) >= 0 && (len(end) == 0 || bytes.Compare(key, end) < 0)
}

func (m *mockStorage) List(prefix []byte) ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var keys [][]byte
	p := string(prefix)
	for k := range m.data {
		if len(p) == 0 || len(k) >= len(p) && k[:len(p)] == p {
			keys = append(keys, []byte(k))
		}
	}
	return keys, nil
}

func (m *mockStorage) Exists(key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *mockStorage) Batch() storage.Batch {
	return nil
}

func (m *mockStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return newMockIterator(m, opts)
}

func (m *mockStorage) Snapshot() (storage.Snapshot, error) {
	return nil, errors.New("not implemented")
}

func (m *mockStorage) Close() error {
	return nil
}

// mockIterator provides iteration over the mock storage.
type mockIterator struct {
	storage *mockStorage
	opts    *storage.IteratorOptions
	keys    []string
	idx     int
	valid   bool
}

func newMockIterator(s *mockStorage, opts *storage.IteratorOptions) *mockIterator {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		// Apply bounds
		if opts != nil {
			if opts.LowerBound != nil && k < string(opts.LowerBound) {
				continue
			}
			if opts.UpperBound != nil && k >= string(opts.UpperBound) {
				continue
			}
		}
		keys = append(keys, k)
	}

	// Sort keys for consistent iteration
	for i := 0; i < len(keys)-1; i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	return &mockIterator{
		storage: s,
		opts:    opts,
		keys:    keys,
		idx:     -1,
		valid:   false,
	}
}

func (m *mockIterator) SeekToFirst() bool {
	if len(m.keys) == 0 {
		m.valid = false
		return false
	}
	m.idx = 0
	m.valid = true
	return true
}

func (m *mockIterator) SeekToLast() bool {
	if len(m.keys) == 0 {
		m.valid = false
		return false
	}
	m.idx = len(m.keys) - 1
	m.valid = true
	return true
}

func (m *mockIterator) Seek(target []byte) bool {
	for i, k := range m.keys {
		if k >= string(target) {
			m.idx = i
			m.valid = true
			return true
		}
	}
	m.valid = false
	return false
}

func (m *mockIterator) Next() bool {
	if m.idx >= len(m.keys)-1 {
		m.valid = false
		return false
	}
	m.idx++
	m.valid = true
	return true
}

func (m *mockIterator) Prev() bool {
	if m.idx <= 0 {
		m.valid = false
		return false
	}
	m.idx--
	m.valid = true
	return true
}

func (m *mockIterator) Valid() bool {
	return m.valid && m.idx >= 0 && m.idx < len(m.keys)
}

func (m *mockIterator) Key() []byte {
	if !m.Valid() {
		return nil
	}
	return []byte(m.keys[m.idx])
}

func (m *mockIterator) Value() []byte {
	if !m.Valid() {
		return nil
	}
	m.storage.mu.RLock()
	defer m.storage.mu.RUnlock()

	if val, ok := m.storage.data[m.keys[m.idx]]; ok {
		result := make([]byte, len(val))
		copy(result, val)
		return result
	}
	return nil
}

func (m *mockIterator) Error() error {
	return nil
}

func (m *mockIterator) Close() error {
	return nil
}

// TestNewIndexTransactionLog tests the creation of a new transaction log.
func TestNewIndexTransactionLog(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test-namespace")

		if log == nil {
			t.Fatal("expected non-nil transaction log")
		}

		if log.store != store {
			t.Error("expected store to be set")
		}

		if log.namespace != "test-namespace" {
			t.Errorf("expected namespace 'test-namespace', got %q", log.namespace)
		}
	})

	t.Run("nil storage returns nil", func(t *testing.T) {
		log := NewIndexTransactionLog(nil, "test-namespace")

		if log != nil {
			t.Error("expected nil transaction log with nil storage")
		}
	})
}

// TestBeginTransaction tests transaction initialization.
func TestBeginTransaction(t *testing.T) {
	t.Run("successful begin", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		state, err := log.GetTransactionState("txn-1")
		if err != nil {
			t.Fatalf("unexpected error getting state: %v", err)
		}

		if state != TxnStatePending {
			t.Errorf("expected TxnStatePending, got %v", state)
		}
	})

	t.Run("empty txnID returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("")
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("duplicate transaction returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.BeginTransaction("txn-1")
		if !errors.Is(err, ErrTxnAlreadyExists) {
			t.Errorf("expected ErrTxnAlreadyExists, got %v", err)
		}
	})
}

// TestLogOperation tests logging of index operations.
func TestLogOperation(t *testing.T) {
	t.Run("successful insert log", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		entry := &IndexLogEntry{
			TxnID:     "txn-1",
			Operation: OpInsert,
			VectorID:  100,
			Namespace: "vectors",
			NewVector: []float32{1.0, 2.0, 3.0},
		}

		err = log.LogOperation(entry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ops, err := log.GetPendingOps("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(ops) != 1 {
			t.Fatalf("expected 1 operation, got %d", len(ops))
		}

		if ops[0].VectorID != 100 {
			t.Errorf("expected VectorID 100, got %d", ops[0].VectorID)
		}
	})

	t.Run("nil entry returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.LogOperation(nil)
		if !errors.Is(err, ErrInvalidLogEntry) {
			t.Errorf("expected ErrInvalidLogEntry, got %v", err)
		}
	})

	t.Run("empty txnID in entry returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		entry := &IndexLogEntry{
			TxnID:     "",
			Operation: OpInsert,
			VectorID:  100,
		}

		err := log.LogOperation(entry)
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("log to non-existent transaction returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		entry := &IndexLogEntry{
			TxnID:     "non-existent",
			Operation: OpInsert,
			VectorID:  100,
		}

		err := log.LogOperation(entry)
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound, got %v", err)
		}
	})

	t.Run("multiple operations sequenced correctly", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := uint64(1); i <= 5; i++ {
			entry := &IndexLogEntry{
				TxnID:     "txn-1",
				Operation: OpInsert,
				VectorID:  i,
				NewVector: []float32{float32(i)},
			}

			err = log.LogOperation(entry)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		ops, err := log.GetPendingOps("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(ops) != 5 {
			t.Fatalf("expected 5 operations, got %d", len(ops))
		}

		// Verify sequences are increasing
		for i := 0; i < len(ops)-1; i++ {
			if ops[i].Sequence >= ops[i+1].Sequence {
				t.Errorf("expected increasing sequences, got %d >= %d", ops[i].Sequence, ops[i+1].Sequence)
			}
		}
	})
}

// TestPrepare tests the prepare phase of 2PC.
func TestPrepare(t *testing.T) {
	t.Run("successful prepare", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Prepare("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		state, err := log.GetTransactionState("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if state != TxnStatePrepared {
			t.Errorf("expected TxnStatePrepared, got %v", state)
		}
	})

	t.Run("prepare non-existent transaction returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.Prepare("non-existent")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound, got %v", err)
		}
	})

	t.Run("prepare empty txnID returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.Prepare("")
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})
}

// TestCommit tests transaction commit.
func TestCommit(t *testing.T) {
	t.Run("successful commit from pending", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		entry := &IndexLogEntry{
			TxnID:     "txn-1",
			Operation: OpInsert,
			VectorID:  100,
		}

		err = log.LogOperation(entry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// After commit, transaction is removed from map
		_, err = log.GetTransactionState("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound after commit, got %v", err)
		}

		// Verify pending ops are cleared
		_, err = log.GetPendingOps("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound after commit, got %v", err)
		}
	})

	t.Run("successful commit from prepared", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Prepare("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// After commit, transaction is removed from map
		_, err = log.GetTransactionState("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound after commit, got %v", err)
		}
	})

	t.Run("commit non-existent transaction returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.Commit("non-existent")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound, got %v", err)
		}
	})

	t.Run("commit empty txnID returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.Commit("")
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})
}

// TestRollbackLog tests transaction rollback in the log.
func TestRollbackLog(t *testing.T) {
	t.Run("successful rollback", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		entry := &IndexLogEntry{
			TxnID:     "txn-1",
			Operation: OpInsert,
			VectorID:  100,
		}

		err = log.LogOperation(entry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Rollback("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// After rollback, transaction is removed from map
		_, err = log.GetTransactionState("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound after rollback, got %v", err)
		}
	})

	t.Run("rollback committed transaction returns not found", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// After commit, transaction is removed, so rollback returns not found
		err = log.Rollback("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound, got %v", err)
		}
	})

	t.Run("rollback already aborted transaction returns not found", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Rollback("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// After rollback, transaction is removed, so second rollback returns not found
		err = log.Rollback("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound, got %v", err)
		}
	})

	t.Run("rollback empty txnID returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.Rollback("")
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})
}

// TestGetPendingOps tests retrieval of pending operations.
func TestGetPendingOps(t *testing.T) {
	t.Run("returns deep copies", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		entry := &IndexLogEntry{
			TxnID:          "txn-1",
			Operation:      OpInsert,
			VectorID:       100,
			PreviousVector: []float32{1.0, 2.0, 3.0},
		}

		err = log.LogOperation(entry)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ops1, err := log.GetPendingOps("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Modify the returned entry
		ops1[0].VectorID = 999

		// Get again and verify it's unchanged
		ops2, err := log.GetPendingOps("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if ops2[0].VectorID != 100 {
			t.Errorf("expected VectorID 100, got %d (deep copy failed)", ops2[0].VectorID)
		}
	})

	t.Run("empty txnID returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		_, err := log.GetPendingOps("")
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("non-existent transaction returns error", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		_, err := log.GetPendingOps("non-existent")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound, got %v", err)
		}
	})
}

// TestRecovery tests crash recovery of pending transactions.
func TestRecovery(t *testing.T) {
	t.Run("recover pending transactions", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		// Create and log some operations
		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := uint64(1); i <= 3; i++ {
			entry := &IndexLogEntry{
				TxnID:     "txn-1",
				Operation: OpInsert,
				VectorID:  i,
				NewVector: []float32{float32(i)},
			}

			err = log.LogOperation(entry)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		// Create a new log instance (simulating restart)
		log2 := NewIndexTransactionLog(store, "test")

		// Recover should restore the pending transaction
		err = log2.Recover()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify pending transactions are recovered
		count := log2.PendingTransactionCount()
		if count != 1 {
			t.Errorf("expected 1 pending transaction, got %d", count)
		}

		ops, err := log2.GetPendingOps("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(ops) != 3 {
			t.Errorf("expected 3 operations, got %d", len(ops))
		}
	})

	t.Run("recover empty storage", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.Recover()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		count := log.PendingTransactionCount()
		if count != 0 {
			t.Errorf("expected 0 pending transactions, got %d", count)
		}
	})
}

// TestPendingTransactionCount tests counting of pending transactions.
func TestPendingTransactionCount(t *testing.T) {
	t.Run("counts pending and prepared", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		// Create pending transaction
		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		count := log.PendingTransactionCount()
		if count != 1 {
			t.Errorf("expected 1, got %d", count)
		}

		// Create prepared transaction
		err = log.BeginTransaction("txn-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Prepare("txn-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		count = log.PendingTransactionCount()
		if count != 2 {
			t.Errorf("expected 2, got %d", count)
		}

		// Commit one
		err = log.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		count = log.PendingTransactionCount()
		if count != 1 {
			t.Errorf("expected 1, got %d", count)
		}
	})

	t.Run("excludes committed and aborted", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.BeginTransaction("txn-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Rollback("txn-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		count := log.PendingTransactionCount()
		if count != 0 {
			t.Errorf("expected 0, got %d", count)
		}
	})
}

// TestIndexLogEntryCopy tests deep copy of log entries.
func TestIndexLogEntryCopy(t *testing.T) {
	t.Run("nil entry returns nil", func(t *testing.T) {
		var entry *IndexLogEntry
		copy := entry.Copy()

		if copy != nil {
			t.Error("expected nil copy for nil entry")
		}
	})

	t.Run("deep copy all fields", func(t *testing.T) {
		entry := &IndexLogEntry{
			TxnID:          "txn-1",
			Operation:      OpUpdate,
			VectorID:       100,
			Namespace:      "test",
			PreviousVector: []float32{1.0, 2.0, 3.0},
			PreviousMetadata: map[string]string{
				"key1": "value1",
			},
			PreviousNeighbors: map[int][]uint64{
				0: {1, 2, 3},
				1: {4, 5},
			},
			PreviousLayer: 2,
			NewVector:     []float32{4.0, 5.0, 6.0},
			NewNeighbors: map[int][]uint64{
				0: {6, 7, 8},
			},
			NewLayer:  3,
			Timestamp: time.Now(),
			Sequence:  42,
		}

		copy := entry.Copy()

		// Verify all fields are copied
		if copy.TxnID != entry.TxnID {
			t.Errorf("TxnID mismatch: got %q, want %q", copy.TxnID, entry.TxnID)
		}

		if copy.VectorID != entry.VectorID {
			t.Errorf("VectorID mismatch: got %d, want %d", copy.VectorID, entry.VectorID)
		}

		// Modify original and verify copy is unchanged
		entry.PreviousVector[0] = 999.0
		if copy.PreviousVector[0] == 999.0 {
			t.Error("PreviousVector was not deep copied")
		}

		entry.PreviousNeighbors[0][0] = 999
		if copy.PreviousNeighbors[0][0] == 999 {
			t.Error("PreviousNeighbors was not deep copied")
		}

		entry.NewVector[0] = 999.0
		if copy.NewVector[0] == 999.0 {
			t.Error("NewVector was not deep copied")
		}
	})
}

// TestIndexOperationString tests operation string conversion.
func TestIndexOperationString(t *testing.T) {
	tests := []struct {
		op       IndexOperation
		expected string
	}{
		{OpInsert, "insert"},
		{OpDelete, "delete"},
		{OpUpdate, "update"},
		{IndexOperation(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.op.String()
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestConcurrentOperations tests thread-safety of the transaction log.
func TestConcurrentOperations(t *testing.T) {
	t.Run("concurrent log operations", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var wg sync.WaitGroup
		numOps := 100

		for i := 0; i < numOps; i++ {
			wg.Add(1)
			go func(id uint64) {
				defer wg.Done()

				entry := &IndexLogEntry{
					TxnID:     "txn-1",
					Operation: OpInsert,
					VectorID:  id,
					NewVector: []float32{float32(id)},
				}

				if err := log.LogOperation(entry); err != nil {
					t.Errorf("unexpected error logging operation %d: %v", id, err)
				}
			}(uint64(i))
		}

		wg.Wait()

		ops, err := log.GetPendingOps("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(ops) != numOps {
			t.Errorf("expected %d operations, got %d", numOps, len(ops))
		}
	})

	t.Run("concurrent transactions", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		var wg sync.WaitGroup
		numTxns := 50

		for i := 0; i < numTxns; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				txnID := padLeft(int64(id), 10)

				err := log.BeginTransaction(txnID)
				if err != nil {
					t.Errorf("unexpected error beginning txn %s: %v", txnID, err)
					return
				}

				entry := &IndexLogEntry{
					TxnID:     txnID,
					Operation: OpInsert,
					VectorID:  uint64(id),
				}

				err = log.LogOperation(entry)
				if err != nil {
					t.Errorf("unexpected error logging operation for txn %s: %v", txnID, err)
					return
				}

				err = log.Commit(txnID)
				if err != nil {
					t.Errorf("unexpected error committing txn %s: %v", txnID, err)
				}
			}(i)
		}

		wg.Wait()

		// Verify all transactions are committed
		count := log.PendingTransactionCount()
		if count != 0 {
			t.Errorf("expected 0 pending transactions, got %d", count)
		}
	})
}

// TestTransactionLogErrorMessages tests error message formatting.
func TestTransactionLogErrorMessages(t *testing.T) {
	t.Run("TransactionLogError with txnID", func(t *testing.T) {
		err := &TransactionLogError{
			Op:    "LogOperation",
			TxnID: "txn-123",
			Err:   errors.New("underlying error"),
		}

		expected := "txn_log.LogOperation [txn=txn-123]: underlying error"
		if err.Error() != expected {
			t.Errorf("expected %q, got %q", expected, err.Error())
		}
	})

	t.Run("TransactionLogError without txnID", func(t *testing.T) {
		err := &TransactionLogError{
			Op:  "Recover",
			Err: errors.New("underlying error"),
		}

		expected := "txn_log.Recover: underlying error"
		if err.Error() != expected {
			t.Errorf("expected %q, got %q", expected, err.Error())
		}
	})

	t.Run("TransactionLogError Unwrap", func(t *testing.T) {
		underlying := errors.New("underlying error")
		err := &TransactionLogError{
			Op:  "Test",
			Err: underlying,
		}

		if !errors.Is(err, underlying) {
			t.Error("expected Unwrap to return underlying error")
		}
	})
}

// TestGetPendingTransactionIDs tests retrieval of pending transaction IDs.
func TestGetPendingTransactionIDs(t *testing.T) {
	t.Run("returns pending and prepared", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.BeginTransaction("txn-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Prepare("txn-2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ids := log.GetPendingTransactionIDs()
		if len(ids) != 2 {
			t.Errorf("expected 2 IDs, got %d", len(ids))
		}
	})

	t.Run("excludes finalized transactions", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = log.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ids := log.GetPendingTransactionIDs()
		if len(ids) != 0 {
			t.Errorf("expected 0 IDs, got %d", len(ids))
		}
	})
}
