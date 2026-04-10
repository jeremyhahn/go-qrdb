//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"errors"
	"sync"
	"testing"
)

// createTestIndex creates an HNSW index for testing.
func createTestIndex(t *testing.T) *HNSWIndex {
	t.Helper()

	config := DefaultConfig()
	idx, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	return idx
}

// createTestTransactionalIndex creates a full transactional setup for testing.
func createTestTransactionalIndex(t *testing.T) (*TransactionalIndex, *HNSWIndex) {
	t.Helper()

	idx := createTestIndex(t)
	store := newMockStorage()
	log := NewIndexTransactionLog(store, "test")
	txnIdx := NewTransactionalIndex(idx, log)

	if txnIdx == nil {
		t.Fatal("failed to create transactional index")
	}

	return txnIdx, idx
}

// TestNewIndexRollback tests the creation of a new rollback handler.
func TestNewIndexRollback(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		rollback := NewIndexRollback(idx, log)

		if rollback == nil {
			t.Fatal("expected non-nil rollback handler")
		}

		if rollback.index != idx {
			t.Error("expected index to be set")
		}

		if rollback.log != log {
			t.Error("expected log to be set")
		}
	})

	t.Run("nil index returns nil", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		rollback := NewIndexRollback(nil, log)

		if rollback != nil {
			t.Error("expected nil with nil index")
		}
	})

	t.Run("nil log returns nil", func(t *testing.T) {
		idx := createTestIndex(t)

		rollback := NewIndexRollback(idx, nil)

		if rollback != nil {
			t.Error("expected nil with nil log")
		}
	})
}

// TestRollbackInsert tests rolling back an insert operation.
func TestRollbackInsert(t *testing.T) {
	t.Run("rollback single insert restores empty state", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Begin transaction and insert
		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify insert was applied
		if idx.Count() != 1 {
			t.Errorf("expected 1 node, got %d", idx.Count())
		}

		// Abort (rollback) the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the node is removed
		if idx.Count() != 0 {
			t.Errorf("expected 0 nodes after rollback, got %d", idx.Count())
		}

		// Verify node is not accessible
		_, err = idx.Get(1)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("rollback insert removes connections from neighbors", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Insert initial nodes outside transaction
		err := idx.Insert(1, []float32{1.0, 0.0, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = idx.Insert(2, []float32{0.0, 1.0, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		initialCount := idx.Count()

		// Begin transaction and insert a third node
		err = txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Insert("txn-1", 3, []float32{0.0, 0.0, 1.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Abort the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify count is restored
		if idx.Count() != initialCount {
			t.Errorf("expected %d nodes, got %d", initialCount, idx.Count())
		}

		// Verify node 3 is not accessible
		_, err = idx.Get(3)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}

		// Verify existing nodes don't have connections to node 3
		node1, err := idx.Get(1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for layer := 0; layer <= node1.Layer; layer++ {
			conns := node1.GetConnections(layer)
			for _, conn := range conns {
				if conn == 3 {
					t.Errorf("node 1 still has connection to node 3 at layer %d", layer)
				}
			}
		}
	})

	t.Run("rollback insert nil entry returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		err := rollback.RollbackInsert(nil)
		if !errors.Is(err, ErrInvalidLogEntry) {
			t.Errorf("expected ErrInvalidLogEntry, got %v", err)
		}
	})

	t.Run("rollback insert wrong operation returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		entry := &IndexLogEntry{
			TxnID:     "txn-1",
			Operation: OpDelete,
			VectorID:  1,
		}

		err := rollback.RollbackInsert(entry)
		if err == nil {
			t.Error("expected error for wrong operation")
		}
	})
}

// TestRollbackDelete tests rolling back a delete operation.
func TestRollbackDelete(t *testing.T) {
	t.Run("rollback delete restores vector", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Insert initial node outside transaction
		originalVector := []float32{1.0, 2.0, 3.0}
		err := idx.Insert(1, originalVector)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Begin transaction and delete
		err = txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Delete("txn-1", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify delete was applied
		if idx.Count() != 0 {
			t.Errorf("expected 0 nodes after delete, got %d", idx.Count())
		}

		// Abort the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the node is restored
		if idx.Count() != 1 {
			t.Errorf("expected 1 node after rollback, got %d", idx.Count())
		}

		// Verify the vector data is correct
		node, err := idx.Get(1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i, v := range originalVector {
			if node.Vector[i] != v {
				t.Errorf("vector mismatch at %d: expected %f, got %f", i, v, node.Vector[i])
			}
		}
	})

	t.Run("rollback delete restores connections", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Insert multiple nodes to create connections
		err := idx.Insert(1, []float32{1.0, 0.0, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = idx.Insert(2, []float32{0.9, 0.1, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = idx.Insert(3, []float32{0.8, 0.2, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Get initial state of node 2
		node2Before, err := idx.Get(2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		originalConns := make(map[int][]uint64)
		node2Before.mu.RLock()
		for layer, conns := range node2Before.Connections {
			connsCopy := make([]uint64, len(conns))
			copy(connsCopy, conns)
			originalConns[layer] = connsCopy
		}
		node2Before.mu.RUnlock()

		// Begin transaction and delete node 2
		err = txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Delete("txn-1", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Abort the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify node 2 is restored with connections
		node2After, err := idx.Get(2)
		if err != nil {
			t.Fatalf("unexpected error getting restored node: %v", err)
		}

		node2After.mu.RLock()
		for layer, expectedConns := range originalConns {
			actualConns := node2After.Connections[layer]
			if len(actualConns) != len(expectedConns) {
				t.Errorf("layer %d: expected %d connections, got %d", layer, len(expectedConns), len(actualConns))
			}
		}
		node2After.mu.RUnlock()
	})

	t.Run("rollback delete nil entry returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		err := rollback.RollbackDelete(nil)
		if !errors.Is(err, ErrInvalidLogEntry) {
			t.Errorf("expected ErrInvalidLogEntry, got %v", err)
		}
	})

	t.Run("rollback delete wrong operation returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		entry := &IndexLogEntry{
			TxnID:     "txn-1",
			Operation: OpInsert,
			VectorID:  1,
		}

		err := rollback.RollbackDelete(entry)
		if err == nil {
			t.Error("expected error for wrong operation")
		}
	})

	t.Run("rollback delete missing pre-image returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		entry := &IndexLogEntry{
			TxnID:          "txn-1",
			Operation:      OpDelete,
			VectorID:       1,
			PreviousVector: nil, // No pre-image
		}

		err := rollback.RollbackDelete(entry)
		if err == nil {
			t.Error("expected error for missing pre-image")
		}
	})
}

// TestRollbackUpdate tests rolling back an update operation.
func TestRollbackUpdate(t *testing.T) {
	t.Run("rollback update restores previous value", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Insert initial node outside transaction
		originalVector := []float32{1.0, 2.0, 3.0}
		err := idx.Insert(1, originalVector)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Begin transaction and update
		err = txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		newVector := []float32{4.0, 5.0, 6.0}
		err = txnIdx.Update("txn-1", 1, newVector)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify update was applied
		node, err := idx.Get(1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i, v := range newVector {
			if node.Vector[i] != v {
				t.Errorf("update not applied at %d: expected %f, got %f", i, v, node.Vector[i])
			}
		}

		// Abort the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the vector is restored
		node, err = idx.Get(1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i, v := range originalVector {
			if node.Vector[i] != v {
				t.Errorf("rollback failed at %d: expected %f, got %f", i, v, node.Vector[i])
			}
		}
	})

	t.Run("rollback update nil entry returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		err := rollback.RollbackUpdate(nil)
		if !errors.Is(err, ErrInvalidLogEntry) {
			t.Errorf("expected ErrInvalidLogEntry, got %v", err)
		}
	})

	t.Run("rollback update wrong operation returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		entry := &IndexLogEntry{
			TxnID:     "txn-1",
			Operation: OpInsert,
			VectorID:  1,
		}

		err := rollback.RollbackUpdate(entry)
		if err == nil {
			t.Error("expected error for wrong operation")
		}
	})
}

// TestRollbackMultipleOperations tests rolling back multiple operations.
func TestRollbackMultipleOperations(t *testing.T) {
	t.Run("multi-vector transaction rollback", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Insert initial nodes outside transaction
		err := idx.Insert(1, []float32{1.0, 0.0, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		initialCount := idx.Count()

		// Begin transaction with multiple operations
		err = txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Insert multiple nodes
		for i := uint64(2); i <= 5; i++ {
			err = txnIdx.Insert("txn-1", i, []float32{float32(i), 0.0, 0.0})
			if err != nil {
				t.Fatalf("unexpected error inserting node %d: %v", i, err)
			}
		}

		// Verify all inserts were applied
		if idx.Count() != 5 {
			t.Errorf("expected 5 nodes, got %d", idx.Count())
		}

		// Abort the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify all inserts are rolled back
		if idx.Count() != initialCount {
			t.Errorf("expected %d nodes after rollback, got %d", initialCount, idx.Count())
		}

		// Verify only node 1 exists
		_, err = idx.Get(1)
		if err != nil {
			t.Errorf("node 1 should exist: %v", err)
		}

		for i := uint64(2); i <= 5; i++ {
			_, err = idx.Get(i)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("node %d should not exist after rollback", i)
			}
		}
	})

	t.Run("mixed operations rollback", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Insert initial nodes
		err := idx.Insert(1, []float32{1.0, 0.0, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = idx.Insert(2, []float32{0.0, 1.0, 0.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Begin transaction
		err = txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Insert new node
		err = txnIdx.Insert("txn-1", 3, []float32{0.0, 0.0, 1.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Delete existing node
		err = txnIdx.Delete("txn-1", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Abort the transaction
		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify state is restored
		// Node 1 should exist (delete rolled back)
		_, err = idx.Get(1)
		if err != nil {
			t.Errorf("node 1 should exist after rollback: %v", err)
		}

		// Node 2 should exist (unchanged)
		_, err = idx.Get(2)
		if err != nil {
			t.Errorf("node 2 should exist: %v", err)
		}

		// Node 3 should not exist (insert rolled back)
		_, err = idx.Get(3)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("node 3 should not exist after rollback")
		}
	})
}

// TestTransactionalIndexCommit tests successful transaction commit.
func TestTransactionalIndexCommit(t *testing.T) {
	t.Run("commit persists changes", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		// Begin transaction and insert
		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Commit the transaction
		err = txnIdx.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the node still exists
		if idx.Count() != 1 {
			t.Errorf("expected 1 node after commit, got %d", idx.Count())
		}

		node, err := idx.Get(1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(node.Vector) != 3 {
			t.Errorf("expected 3-dimensional vector, got %d", len(node.Vector))
		}
	})

	t.Run("commit clears pending operations", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify pending operations are cleared
		_, err = txnIdx.GetTransactionLog().GetPendingOps("txn-1")
		if !errors.Is(err, ErrTxnNotFound) {
			t.Errorf("expected ErrTxnNotFound after commit, got %v", err)
		}
	})
}

// TestTransactionalIndexPrepare tests 2PC prepare phase.
func TestTransactionalIndexPrepare(t *testing.T) {
	t.Run("prepare marks transaction as prepared", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Prepare("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		state, err := txnIdx.GetTransactionLog().GetTransactionState("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if state != TxnStatePrepared {
			t.Errorf("expected TxnStatePrepared, got %v", state)
		}
	})

	t.Run("prepared transaction can be committed", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Prepare("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Commit("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("prepared transaction can be aborted", func(t *testing.T) {
		txnIdx, idx := createTestTransactionalIndex(t)

		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Prepare("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Abort("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify rollback occurred
		if idx.Count() != 0 {
			t.Errorf("expected 0 nodes after abort, got %d", idx.Count())
		}
	})
}

// TestRecoveryWithPendingEntries tests crash recovery.
func TestRecoveryWithPendingEntries(t *testing.T) {
	t.Run("recover pending transactions", func(t *testing.T) {
		store := newMockStorage()
		idx := createTestIndex(t)

		// Create first transactional index and add operations
		log1 := NewIndexTransactionLog(store, "test")
		txnIdx1 := NewTransactionalIndex(idx, log1)

		err := txnIdx1.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx1.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Simulate crash (don't commit or abort)

		// Create new transactional index (simulating restart)
		log2 := NewIndexTransactionLog(store, "test")
		txnIdx2 := NewTransactionalIndex(idx, log2)

		// Recover pending transactions
		err = txnIdx2.Recover()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify pending transaction is recovered
		count := log2.PendingTransactionCount()
		if count != 1 {
			t.Errorf("expected 1 pending transaction, got %d", count)
		}
	})

	t.Run("recover and rollback pending transactions", func(t *testing.T) {
		store := newMockStorage()
		idx := createTestIndex(t)

		// Create first transactional index and add operations
		log1 := NewIndexTransactionLog(store, "test")
		txnIdx1 := NewTransactionalIndex(idx, log1)

		err := txnIdx1.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx1.Insert("txn-1", 1, []float32{1.0, 2.0, 3.0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify node was inserted
		if idx.Count() != 1 {
			t.Errorf("expected 1 node, got %d", idx.Count())
		}

		// Simulate crash and restart
		log2 := NewIndexTransactionLog(store, "test")
		txnIdx2 := NewTransactionalIndex(idx, log2)

		// Recover and rollback all pending transactions
		err = txnIdx2.RecoverAndRollback()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify pending transactions are cleared
		count := log2.PendingTransactionCount()
		if count != 0 {
			t.Errorf("expected 0 pending transactions after rollback, got %d", count)
		}

		// Verify the insert was rolled back
		if idx.Count() != 0 {
			t.Errorf("expected 0 nodes after rollback, got %d", idx.Count())
		}
	})
}

// TestConcurrentTransactionIsolation tests concurrent transaction isolation.
func TestConcurrentTransactionIsolation(t *testing.T) {
	t.Run("concurrent transactions isolated", func(t *testing.T) {
		store := newMockStorage()
		idx := createTestIndex(t)
		log := NewIndexTransactionLog(store, "test")
		txnIdx := NewTransactionalIndex(idx, log)

		var wg sync.WaitGroup
		numTxns := 10

		for i := 0; i < numTxns; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				txnID := padLeft(int64(id), 10)

				err := txnIdx.Begin(txnID)
				if err != nil {
					t.Errorf("unexpected error beginning txn %s: %v", txnID, err)
					return
				}

				err = txnIdx.Insert(txnID, uint64(id+1), []float32{float32(id), 0.0, 0.0})
				if err != nil {
					t.Errorf("unexpected error inserting in txn %s: %v", txnID, err)
					return
				}

				// Alternate between commit and abort
				if id%2 == 0 {
					err = txnIdx.Commit(txnID)
				} else {
					err = txnIdx.Abort(txnID)
				}

				if err != nil {
					t.Errorf("unexpected error finalizing txn %s: %v", txnID, err)
				}
			}(i)
		}

		wg.Wait()

		// Verify only committed nodes exist (even ids: 0, 2, 4, 6, 8 -> nodes 1, 3, 5, 7, 9)
		expectedCount := int64(numTxns / 2)
		if idx.Count() != expectedCount {
			t.Errorf("expected %d nodes, got %d", expectedCount, idx.Count())
		}
	})
}

// TestNewTransactionalIndex tests transactional index creation.
func TestNewTransactionalIndex(t *testing.T) {
	t.Run("valid creation", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		txnIdx := NewTransactionalIndex(idx, log)

		if txnIdx == nil {
			t.Fatal("expected non-nil transactional index")
		}

		if txnIdx.GetIndex() != idx {
			t.Error("GetIndex should return underlying index")
		}

		if txnIdx.GetTransactionLog() != log {
			t.Error("GetTransactionLog should return transaction log")
		}

		if txnIdx.GetRollback() == nil {
			t.Error("GetRollback should return non-nil rollback handler")
		}
	})

	t.Run("nil index returns nil", func(t *testing.T) {
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")

		txnIdx := NewTransactionalIndex(nil, log)

		if txnIdx != nil {
			t.Error("expected nil with nil index")
		}
	})

	t.Run("nil log returns nil", func(t *testing.T) {
		idx := createTestIndex(t)

		txnIdx := NewTransactionalIndex(idx, nil)

		if txnIdx != nil {
			t.Error("expected nil with nil log")
		}
	})
}

// TestRollbackErrorMessages tests error message formatting.
func TestRollbackErrorMessages(t *testing.T) {
	t.Run("RollbackError with vectorID", func(t *testing.T) {
		err := &RollbackError{
			Op:       "RollbackInsert",
			TxnID:    "txn-123",
			VectorID: 456,
			Err:      errors.New("underlying error"),
		}

		expected := "rollback.RollbackInsert [txn=txn-123, vector=456]: underlying error"
		if err.Error() != expected {
			t.Errorf("expected %q, got %q", expected, err.Error())
		}
	})

	t.Run("RollbackError without vectorID", func(t *testing.T) {
		err := &RollbackError{
			Op:    "RollbackTransaction",
			TxnID: "txn-123",
			Err:   errors.New("underlying error"),
		}

		expected := "rollback.RollbackTransaction [txn=txn-123]: underlying error"
		if err.Error() != expected {
			t.Errorf("expected %q, got %q", expected, err.Error())
		}
	})

	t.Run("RollbackError Unwrap", func(t *testing.T) {
		underlying := errors.New("underlying error")
		err := &RollbackError{
			Op:  "Test",
			Err: underlying,
		}

		if !errors.Is(err, underlying) {
			t.Error("expected Unwrap to return underlying error")
		}
	})

	t.Run("RecoveryError", func(t *testing.T) {
		err := &RecoveryError{
			Op:  "RecoverAndRollback",
			Err: errors.New("underlying error"),
		}

		expected := "recovery.RecoverAndRollback: underlying error"
		if err.Error() != expected {
			t.Errorf("expected %q, got %q", expected, err.Error())
		}
	})
}

// TestRollbackTransactionErrors tests error handling in RollbackTransaction.
func TestRollbackTransactionErrors(t *testing.T) {
	t.Run("empty txnID returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		err := rollback.RollbackTransaction("")
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("non-existent transaction returns error", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		err := rollback.RollbackTransaction("non-existent")
		if err == nil {
			t.Error("expected error for non-existent transaction")
		}
	})

	t.Run("empty transaction rollback succeeds", func(t *testing.T) {
		idx := createTestIndex(t)
		store := newMockStorage()
		log := NewIndexTransactionLog(store, "test")
		rollback := NewIndexRollback(idx, log)

		err := log.BeginTransaction("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// No operations logged

		err = rollback.RollbackTransaction("txn-1")
		if err != nil {
			t.Errorf("unexpected error rolling back empty transaction: %v", err)
		}
	})
}

// TestTransactionalIndexValidation tests input validation.
func TestTransactionalIndexValidation(t *testing.T) {
	t.Run("insert with empty txnID returns error", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Insert("", 1, []float32{1.0, 2.0, 3.0})
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("delete with empty txnID returns error", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Delete("", 1)
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("update with empty txnID returns error", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Update("", 1, []float32{1.0, 2.0, 3.0})
		if !errors.Is(err, ErrInvalidTxnID) {
			t.Errorf("expected ErrInvalidTxnID, got %v", err)
		}
	})

	t.Run("delete non-existent node returns error", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Delete("txn-1", 999)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("update non-existent node returns error", func(t *testing.T) {
		txnIdx, _ := createTestTransactionalIndex(t)

		err := txnIdx.Begin("txn-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		err = txnIdx.Update("txn-1", 999, []float32{1.0, 2.0, 3.0})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})
}
