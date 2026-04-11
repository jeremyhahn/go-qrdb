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
	"sort"
	"sync"
)

// IndexRollback handles rollback operations for HNSW index transactions.
// It integrates with the transaction log to provide atomic rollback capabilities.
type IndexRollback struct {
	index *HNSWIndex
	log   *IndexTransactionLog

	// operationHandlers dispatches rollback operations by type.
	operationHandlers map[IndexOperation]func(*IndexLogEntry) error
}

// NewIndexRollback creates a new rollback handler.
func NewIndexRollback(index *HNSWIndex, log *IndexTransactionLog) *IndexRollback {
	if index == nil || log == nil {
		return nil
	}

	r := &IndexRollback{
		index: index,
		log:   log,
	}

	// Initialize operation handler dispatch map for O(1) lookup
	r.operationHandlers = map[IndexOperation]func(*IndexLogEntry) error{
		OpInsert: r.RollbackInsert,
		OpDelete: r.RollbackDelete,
		OpUpdate: r.RollbackUpdate,
	}

	return r
}

// RollbackTransaction rolls back all operations in a transaction.
// Operations are rolled back in reverse order to maintain consistency.
func (r *IndexRollback) RollbackTransaction(txnID string) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	// Get all pending operations
	ops, err := r.log.GetPendingOps(txnID)
	if err != nil {
		return &RollbackError{
			Op:    "RollbackTransaction",
			TxnID: txnID,
			Err:   err,
		}
	}

	if len(ops) == 0 {
		// No operations to rollback, just mark as aborted
		return r.log.Rollback(txnID)
	}

	// Sort operations in reverse order by sequence
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].Sequence > ops[j].Sequence
	})

	// Rollback each operation in reverse order
	var rollbackErrors []error
	for _, entry := range ops {
		if err := r.rollbackEntry(entry); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}

	// Mark transaction as aborted in log
	if err := r.log.Rollback(txnID); err != nil {
		return &RollbackError{
			Op:    "RollbackTransaction",
			TxnID: txnID,
			Err:   err,
		}
	}

	// Return first error if any occurred
	if len(rollbackErrors) > 0 {
		return rollbackErrors[0]
	}

	return nil
}

// rollbackEntry dispatches to the appropriate rollback handler.
func (r *IndexRollback) rollbackEntry(entry *IndexLogEntry) error {
	if entry == nil {
		return ErrInvalidLogEntry
	}

	handler, ok := r.operationHandlers[entry.Operation]
	if !ok {
		return &RollbackError{
			Op:       "rollbackEntry",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInvalidOperation,
		}
	}

	return handler(entry)
}

// RollbackInsert removes an inserted vector, restoring empty state.
func (r *IndexRollback) RollbackInsert(entry *IndexLogEntry) error {
	if entry == nil {
		return ErrInvalidLogEntry
	}

	if entry.Operation != OpInsert {
		return &RollbackError{
			Op:       "RollbackInsert",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInvalidOperation,
		}
	}

	// Check if node exists
	nodeVal, exists := r.index.nodes.Load(entry.VectorID)
	if !exists {
		// Node already doesn't exist, consider rollback successful
		return nil
	}

	node, ok := nodeVal.(*HNSWNode)
	if !ok {
		return &RollbackError{
			Op:       "RollbackInsert",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInternal,
		}
	}

	// Remove all connections from neighbors to this node
	r.index.nodes.Range(func(key, value interface{}) bool {
		otherNode, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		if otherNode.ID == entry.VectorID {
			return true
		}

		for layer := 0; layer <= otherNode.Layer; layer++ {
			otherNode.RemoveConnection(layer, entry.VectorID)
		}
		return true
	})

	// Remove the node itself
	r.index.nodes.Delete(entry.VectorID)
	r.index.nodeCount.Add(-1)

	// Update entry point if necessary
	if r.index.entryPoint.Load() == entry.VectorID {
		r.updateEntryPoint()
	}

	// Update max layer if necessary
	if node.Layer == int(r.index.maxLayer.Load()) {
		r.updateMaxLayer()
	}

	return nil
}

// RollbackDelete restores a deleted vector with its connections.
func (r *IndexRollback) RollbackDelete(entry *IndexLogEntry) error {
	if entry == nil {
		return ErrInvalidLogEntry
	}

	if entry.Operation != OpDelete {
		return &RollbackError{
			Op:       "RollbackDelete",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInvalidOperation,
		}
	}

	// Check if the pre-image data exists
	if entry.PreviousVector == nil {
		return &RollbackError{
			Op:       "RollbackDelete",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInvalidLogEntry,
		}
	}

	// Create the node from pre-image
	vectorCopy := make([]float32, len(entry.PreviousVector))
	copy(vectorCopy, entry.PreviousVector)

	node := &HNSWNode{
		ID:          entry.VectorID,
		Vector:      vectorCopy,
		Layer:       entry.PreviousLayer,
		Connections: make(map[int][]uint64),
	}

	// Restore connections from pre-image
	if entry.PreviousNeighbors != nil {
		for layer, neighbors := range entry.PreviousNeighbors {
			neighborsCopy := make([]uint64, len(neighbors))
			copy(neighborsCopy, neighbors)
			node.Connections[layer] = neighborsCopy
		}
	}

	// Store the restored node
	r.index.nodes.Store(entry.VectorID, node)
	r.index.nodeCount.Add(1)

	// Restore bidirectional connections
	if entry.PreviousNeighbors != nil {
		for layer, neighbors := range entry.PreviousNeighbors {
			for _, neighborID := range neighbors {
				neighborVal, exists := r.index.nodes.Load(neighborID)
				if !exists {
					continue
				}

				neighbor, ok := neighborVal.(*HNSWNode)
				if !ok {
					continue
				}

				neighbor.AddConnection(layer, entry.VectorID)
			}
		}
	}

	// Update entry point and max layer if necessary
	currentMaxLayer := r.index.maxLayer.Load()
	if int32(node.Layer) > currentMaxLayer { //nolint:gosec // G115: Safe conversion
		r.index.maxLayer.Store(int32(node.Layer)) //nolint:gosec // G115: Safe conversion
		r.index.entryPoint.Store(entry.VectorID)
	} else if currentMaxLayer == -1 {
		// First node after rollback
		r.index.maxLayer.Store(int32(node.Layer)) //nolint:gosec // G115: Safe conversion
		r.index.entryPoint.Store(entry.VectorID)
	}

	return nil
}

// RollbackUpdate restores the previous vector value and connections.
func (r *IndexRollback) RollbackUpdate(entry *IndexLogEntry) error {
	if entry == nil {
		return ErrInvalidLogEntry
	}

	if entry.Operation != OpUpdate {
		return &RollbackError{
			Op:       "RollbackUpdate",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInvalidOperation,
		}
	}

	// Check if the pre-image data exists
	if entry.PreviousVector == nil {
		return &RollbackError{
			Op:       "RollbackUpdate",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInvalidLogEntry,
		}
	}

	// Get the current node
	nodeVal, exists := r.index.nodes.Load(entry.VectorID)
	if !exists {
		// Node doesn't exist, need to recreate it
		return r.RollbackDelete(entry)
	}

	node, ok := nodeVal.(*HNSWNode)
	if !ok {
		return &RollbackError{
			Op:       "RollbackUpdate",
			TxnID:    entry.TxnID,
			VectorID: entry.VectorID,
			Err:      ErrInternal,
		}
	}

	// Restore previous vector
	node.mu.Lock()

	vectorCopy := make([]float32, len(entry.PreviousVector))
	copy(vectorCopy, entry.PreviousVector)
	node.Vector = vectorCopy

	// Restore previous layer
	oldLayer := node.Layer
	node.Layer = entry.PreviousLayer

	// Restore previous connections
	if entry.PreviousNeighbors != nil {
		// Clear current connections first
		node.Connections = make(map[int][]uint64)

		for layer, neighbors := range entry.PreviousNeighbors {
			neighborsCopy := make([]uint64, len(neighbors))
			copy(neighborsCopy, neighbors)
			node.Connections[layer] = neighborsCopy
		}
	}

	node.mu.Unlock()

	// Update entry point and max layer if layer changed
	if oldLayer != entry.PreviousLayer {
		if oldLayer == int(r.index.maxLayer.Load()) {
			r.updateMaxLayer()
		}
	}

	return nil
}

// updateEntryPoint updates the entry point to the node with the highest layer.
func (r *IndexRollback) updateEntryPoint() {
	var maxLayer int32 = -1
	var newEntryPoint uint64

	r.index.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		if int32(node.Layer) > maxLayer { //nolint:gosec // G115: Safe conversion
			maxLayer = int32(node.Layer) //nolint:gosec // G115: Safe conversion
			newEntryPoint = node.ID
		}
		return true
	})

	if newEntryPoint != 0 {
		r.index.entryPoint.Store(newEntryPoint)
		r.index.maxLayer.Store(maxLayer)
	} else {
		// No nodes left, reset to empty state
		r.index.maxLayer.Store(-1)
	}
}

// updateMaxLayer recalculates the maximum layer in the index.
func (r *IndexRollback) updateMaxLayer() {
	var maxLayer int32 = -1

	r.index.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		if int32(node.Layer) > maxLayer { //nolint:gosec // G115: Safe conversion
			maxLayer = int32(node.Layer) //nolint:gosec // G115: Safe conversion
		}
		return true
	})

	r.index.maxLayer.Store(maxLayer)
}

// TransactionalIndex provides transactional operations on the HNSW index.
// It integrates the index, transaction log, and rollback handler.
type TransactionalIndex struct {
	index    *HNSWIndex
	log      *IndexTransactionLog
	rollback *IndexRollback

	// activeTxns tracks active transaction nodes
	activeTxns sync.Map // map[string]map[uint64]bool
}

// NewTransactionalIndex creates a new transactional index wrapper.
func NewTransactionalIndex(index *HNSWIndex, log *IndexTransactionLog) *TransactionalIndex {
	if index == nil || log == nil {
		return nil
	}

	rollback := NewIndexRollback(index, log)
	if rollback == nil {
		return nil
	}

	return &TransactionalIndex{
		index:    index,
		log:      log,
		rollback: rollback,
	}
}

// Begin starts a new transaction.
func (t *TransactionalIndex) Begin(txnID string) error {
	if err := t.log.BeginTransaction(txnID); err != nil {
		return err
	}

	t.activeTxns.Store(txnID, make(map[uint64]bool))
	return nil
}

// Insert adds a vector within a transaction.
// The vector is immediately added to the index but can be rolled back.
func (t *TransactionalIndex) Insert(txnID string, id uint64, vector []float32) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	// Apply the insert first - only log if successful
	if err := t.index.Insert(id, vector); err != nil {
		return err
	}

	// Log the operation for potential rollback (only after successful insert)
	entry := &IndexLogEntry{
		TxnID:     txnID,
		Operation: OpInsert,
		VectorID:  id,
		NewVector: make([]float32, len(vector)),
	}
	copy(entry.NewVector, vector)

	if err := t.log.LogOperation(entry); err != nil {
		// Insert succeeded but logging failed - we need to rollback the insert
		_ = t.index.Delete(id)
		return err
	}

	// Track the node in the transaction
	txnNodesVal, _ := t.activeTxns.Load(txnID)
	if txnNodes, ok := txnNodesVal.(map[uint64]bool); ok {
		txnNodes[id] = true
	}

	return nil
}

// Delete removes a vector within a transaction.
// The pre-image is saved for potential rollback.
func (t *TransactionalIndex) Delete(txnID string, id uint64) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	// Get the current node state for pre-image
	node, err := t.index.Get(id)
	if err != nil {
		return err
	}

	// Create log entry with pre-image (before delete)
	entry := &IndexLogEntry{
		TxnID:             txnID,
		Operation:         OpDelete,
		VectorID:          id,
		PreviousVector:    make([]float32, len(node.Vector)),
		PreviousLayer:     node.Layer,
		PreviousNeighbors: make(map[int][]uint64),
	}
	copy(entry.PreviousVector, node.Vector)

	// Copy connections
	node.mu.RLock()
	for layer, neighbors := range node.Connections {
		neighborsCopy := make([]uint64, len(neighbors))
		copy(neighborsCopy, neighbors)
		entry.PreviousNeighbors[layer] = neighborsCopy
	}
	node.mu.RUnlock()

	// Apply the delete first
	if err := t.index.Delete(id); err != nil {
		return err
	}

	// Log the operation only after successful delete
	if err := t.log.LogOperation(entry); err != nil {
		// Delete succeeded but logging failed - restore the node
		_ = t.index.Insert(id, entry.PreviousVector)
		return err
	}

	return nil
}

// Update modifies a vector within a transaction.
// The pre-image is saved for potential rollback.
func (t *TransactionalIndex) Update(txnID string, id uint64, newVector []float32) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	// Get the current node state for pre-image
	node, err := t.index.Get(id)
	if err != nil {
		return err
	}

	// Create log entry with pre-image (before update)
	entry := &IndexLogEntry{
		TxnID:             txnID,
		Operation:         OpUpdate,
		VectorID:          id,
		PreviousVector:    make([]float32, len(node.Vector)),
		PreviousLayer:     node.Layer,
		PreviousNeighbors: make(map[int][]uint64),
		NewVector:         make([]float32, len(newVector)),
	}
	copy(entry.PreviousVector, node.Vector)
	copy(entry.NewVector, newVector)

	// Copy connections
	node.mu.RLock()
	for layer, neighbors := range node.Connections {
		neighborsCopy := make([]uint64, len(neighbors))
		copy(neighborsCopy, neighbors)
		entry.PreviousNeighbors[layer] = neighborsCopy
	}
	node.mu.RUnlock()

	// Apply the update first - delete and re-insert with new vector
	if err := t.index.Delete(id); err != nil {
		return err
	}

	if err := t.index.Insert(id, newVector); err != nil {
		// Insert failed, restore the deleted node
		_ = t.index.Insert(id, entry.PreviousVector)
		return err
	}

	// Log the operation only after successful update
	if err := t.log.LogOperation(entry); err != nil {
		// Update succeeded but logging failed - restore the previous state
		_ = t.index.Delete(id)
		_ = t.index.Insert(id, entry.PreviousVector)
		return err
	}

	return nil
}

// Prepare prepares the transaction for commit (2PC).
func (t *TransactionalIndex) Prepare(txnID string) error {
	return t.log.Prepare(txnID)
}

// Commit finalizes the transaction.
func (t *TransactionalIndex) Commit(txnID string) error {
	if err := t.log.Commit(txnID); err != nil {
		return err
	}

	t.activeTxns.Delete(txnID)
	return nil
}

// Abort rolls back the transaction.
func (t *TransactionalIndex) Abort(txnID string) error {
	if err := t.rollback.RollbackTransaction(txnID); err != nil {
		return err
	}

	t.activeTxns.Delete(txnID)
	return nil
}

// Recover restores pending transactions from storage.
func (t *TransactionalIndex) Recover() error {
	return t.log.Recover()
}

// RecoverAndRollback recovers pending transactions and rolls them back.
// This is typically called on startup to clean up incomplete transactions.
func (t *TransactionalIndex) RecoverAndRollback() error {
	if err := t.log.Recover(); err != nil {
		return err
	}

	// Get all pending transactions and roll them back
	pendingCount := t.log.PendingTransactionCount()
	if pendingCount == 0 {
		return nil
	}

	// Get pending transaction IDs
	txnIDs := t.log.GetPendingTransactionIDs()

	// Roll back all pending transactions
	var rollbackErrors []error
	for _, txnID := range txnIDs {
		if err := t.rollback.RollbackTransaction(txnID); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}

	if len(rollbackErrors) > 0 {
		return &RecoveryError{
			Op:  "RecoverAndRollback",
			Err: rollbackErrors[0],
		}
	}

	return nil
}

// GetIndex returns the underlying HNSW index.
func (t *TransactionalIndex) GetIndex() *HNSWIndex {
	return t.index
}

// GetTransactionLog returns the transaction log.
func (t *TransactionalIndex) GetTransactionLog() *IndexTransactionLog {
	return t.log
}

// GetRollback returns the rollback handler.
func (t *TransactionalIndex) GetRollback() *IndexRollback {
	return t.rollback
}
