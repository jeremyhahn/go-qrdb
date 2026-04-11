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
	"encoding/gob"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// IndexOperation represents the type of index operation.
type IndexOperation int

const (
	// OpInsert represents a vector insertion operation.
	OpInsert IndexOperation = iota

	// OpDelete represents a vector deletion operation.
	OpDelete

	// OpUpdate represents a vector update operation.
	OpUpdate
)

// String returns the string representation of the operation.
func (op IndexOperation) String() string {
	switch op {
	case OpInsert:
		return "insert"
	case OpDelete:
		return "delete"
	case OpUpdate:
		return "update"
	default:
		return "unknown"
	}
}

const (
	// Key prefixes for transaction log storage.
	prefixTxnLog     = "hnsw:txn:"
	prefixTxnPending = "hnsw:txn:pending:"
)

// IndexLogEntry represents a single logged operation for rollback.
type IndexLogEntry struct {
	// TxnID is the unique transaction identifier.
	TxnID string

	// Operation is the type of operation performed.
	Operation IndexOperation

	// VectorID is the unique identifier for the vector (as string for storage).
	VectorID uint64

	// Namespace is the vector namespace.
	Namespace string

	// PreviousVector stores the vector data before modification (for rollback).
	PreviousVector []float32

	// PreviousMetadata stores metadata before modification.
	PreviousMetadata map[string]string

	// PreviousNeighbors stores connections per layer before modification.
	PreviousNeighbors map[int][]uint64

	// PreviousLayer stores the layer value before modification.
	PreviousLayer int

	// NewVector stores the new vector data (for staged inserts).
	NewVector []float32

	// NewNeighbors stores the new connections per layer (for staged updates).
	NewNeighbors map[int][]uint64

	// NewLayer stores the new layer value.
	NewLayer int

	// Timestamp is when the operation was logged.
	Timestamp time.Time

	// Sequence is the order of the operation within the transaction.
	Sequence int64
}

// Copy creates a deep copy of the log entry.
func (e *IndexLogEntry) Copy() *IndexLogEntry {
	if e == nil {
		return nil
	}

	entry := &IndexLogEntry{
		TxnID:         e.TxnID,
		Operation:     e.Operation,
		VectorID:      e.VectorID,
		Namespace:     e.Namespace,
		PreviousLayer: e.PreviousLayer,
		NewLayer:      e.NewLayer,
		Timestamp:     e.Timestamp,
		Sequence:      e.Sequence,
	}

	if e.PreviousVector != nil {
		entry.PreviousVector = make([]float32, len(e.PreviousVector))
		copy(entry.PreviousVector, e.PreviousVector)
	}

	if e.NewVector != nil {
		entry.NewVector = make([]float32, len(e.NewVector))
		copy(entry.NewVector, e.NewVector)
	}

	if e.PreviousMetadata != nil {
		entry.PreviousMetadata = make(map[string]string, len(e.PreviousMetadata))
		for k, v := range e.PreviousMetadata {
			entry.PreviousMetadata[k] = v
		}
	}

	if e.PreviousNeighbors != nil {
		entry.PreviousNeighbors = make(map[int][]uint64, len(e.PreviousNeighbors))
		for layer, neighbors := range e.PreviousNeighbors {
			neighborsCopy := make([]uint64, len(neighbors))
			copy(neighborsCopy, neighbors)
			entry.PreviousNeighbors[layer] = neighborsCopy
		}
	}

	if e.NewNeighbors != nil {
		entry.NewNeighbors = make(map[int][]uint64, len(e.NewNeighbors))
		for layer, neighbors := range e.NewNeighbors {
			neighborsCopy := make([]uint64, len(neighbors))
			copy(neighborsCopy, neighbors)
			entry.NewNeighbors[layer] = neighborsCopy
		}
	}

	return entry
}

// txnData holds transaction-specific data with proper synchronization.
type txnData struct {
	mu       sync.Mutex
	ops      []*IndexLogEntry
	sequence int64
	state    TxnState
}

// IndexTransactionLog manages transaction logs for vector index operations.
// It provides atomic logging, rollback, and crash recovery capabilities.
type IndexTransactionLog struct {
	store     storage.Storage
	namespace string

	// transactions stores per-transaction data with synchronization.
	transactions sync.Map // map[string]*txnData
}

// TxnState represents the state of a transaction.
type TxnState int

const (
	// TxnStatePending indicates the transaction is in progress.
	TxnStatePending TxnState = iota

	// TxnStatePrepared indicates the transaction is prepared for commit.
	TxnStatePrepared

	// TxnStateCommitted indicates the transaction is committed.
	TxnStateCommitted

	// TxnStateAborted indicates the transaction is aborted.
	TxnStateAborted
)

// NewIndexTransactionLog creates a new transaction log.
func NewIndexTransactionLog(store storage.Storage, namespace string) *IndexTransactionLog {
	if store == nil {
		return nil
	}

	return &IndexTransactionLog{
		store:     store,
		namespace: namespace,
	}
}

// BeginTransaction starts a new transaction.
func (l *IndexTransactionLog) BeginTransaction(txnID string) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	// Check if transaction already exists
	if _, exists := l.transactions.Load(txnID); exists {
		return ErrTxnAlreadyExists
	}

	// Initialize transaction data
	data := &txnData{
		ops:      make([]*IndexLogEntry, 0),
		sequence: 0,
		state:    TxnStatePending,
	}

	// Use LoadOrStore to handle concurrent BeginTransaction calls
	if _, loaded := l.transactions.LoadOrStore(txnID, data); loaded {
		return ErrTxnAlreadyExists
	}

	return nil
}

// LogOperation logs an index operation for potential rollback.
func (l *IndexTransactionLog) LogOperation(entry *IndexLogEntry) error {
	if entry == nil {
		return ErrInvalidLogEntry
	}

	if entry.TxnID == "" {
		return ErrInvalidTxnID
	}

	// Get transaction data
	dataVal, exists := l.transactions.Load(entry.TxnID)
	if !exists {
		return ErrTxnNotFound
	}

	data, ok := dataVal.(*txnData)
	if !ok {
		return ErrInternal
	}

	// Lock the transaction for modification
	data.mu.Lock()
	defer data.mu.Unlock()

	// Check transaction state
	if data.state != TxnStatePending && data.state != TxnStatePrepared {
		return ErrTxnNotActive
	}

	// Create a copy with timestamp and sequence
	entryCopy := entry.Copy()
	entryCopy.Timestamp = time.Now()
	entryCopy.Sequence = data.sequence
	data.sequence++

	// Persist to storage first (for crash recovery)
	if err := l.persistEntry(entryCopy); err != nil {
		return err
	}

	// Add to pending operations
	data.ops = append(data.ops, entryCopy)

	return nil
}

// GetPendingOps returns all pending operations for a transaction.
func (l *IndexTransactionLog) GetPendingOps(txnID string) ([]*IndexLogEntry, error) {
	if txnID == "" {
		return nil, ErrInvalidTxnID
	}

	dataVal, exists := l.transactions.Load(txnID)
	if !exists {
		return nil, ErrTxnNotFound
	}

	data, ok := dataVal.(*txnData)
	if !ok {
		return nil, ErrInternal
	}

	data.mu.Lock()
	defer data.mu.Unlock()

	// Return deep copies to prevent external modification
	result := make([]*IndexLogEntry, len(data.ops))
	for i, op := range data.ops {
		result[i] = op.Copy()
	}

	return result, nil
}

// Prepare marks a transaction as prepared for commit (2PC).
func (l *IndexTransactionLog) Prepare(txnID string) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	dataVal, exists := l.transactions.Load(txnID)
	if !exists {
		return ErrTxnNotFound
	}

	data, ok := dataVal.(*txnData)
	if !ok {
		return ErrInternal
	}

	data.mu.Lock()
	defer data.mu.Unlock()

	if data.state != TxnStatePending {
		return ErrTxnNotActive
	}

	// Update state
	data.state = TxnStatePrepared

	// Persist prepared state
	return l.persistTxnState(txnID, TxnStatePrepared)
}

// Commit finalizes a transaction and clears log entries.
func (l *IndexTransactionLog) Commit(txnID string) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	dataVal, exists := l.transactions.Load(txnID)
	if !exists {
		return ErrTxnNotFound
	}

	data, ok := dataVal.(*txnData)
	if !ok {
		return ErrInternal
	}

	data.mu.Lock()

	if data.state != TxnStatePending && data.state != TxnStatePrepared {
		data.mu.Unlock()
		return ErrTxnNotActive
	}

	// Update state
	data.state = TxnStateCommitted

	data.mu.Unlock()

	// Clear persisted log entries
	if err := l.clearPersistedEntries(txnID); err != nil {
		return err
	}

	// Remove transaction data from map
	l.transactions.Delete(txnID)

	return nil
}

// Rollback reverts all operations in a transaction.
func (l *IndexTransactionLog) Rollback(txnID string) error {
	if txnID == "" {
		return ErrInvalidTxnID
	}

	dataVal, exists := l.transactions.Load(txnID)
	if !exists {
		return ErrTxnNotFound
	}

	data, ok := dataVal.(*txnData)
	if !ok {
		return ErrInternal
	}

	data.mu.Lock()

	if data.state == TxnStateCommitted {
		data.mu.Unlock()
		return ErrTxnAlreadyCommitted
	}

	if data.state == TxnStateAborted {
		data.mu.Unlock()
		return ErrTxnAlreadyAborted
	}

	// Update state
	data.state = TxnStateAborted

	data.mu.Unlock()

	// Clear persisted log entries
	if err := l.clearPersistedEntries(txnID); err != nil {
		return err
	}

	// Remove transaction data from map
	l.transactions.Delete(txnID)

	return nil
}

// Recover restores pending transactions from storage on startup.
func (l *IndexTransactionLog) Recover() error {
	// Create iterator options for the transaction log prefix
	opts := &storage.IteratorOptions{
		LowerBound: []byte(l.buildKeyPrefix("")),
		UpperBound: []byte(l.buildKeyPrefix("\xff")),
	}

	iter := l.store.NewIterator(opts)
	if iter == nil {
		return nil // No entries to recover
	}
	defer func() { _ = iter.Close() }()

	// Track recovered transactions
	recoveredTxns := make(map[string][]*IndexLogEntry)

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		if err := iter.Error(); err != nil {
			return &TransactionLogError{
				Op:    "Recover",
				TxnID: "",
				Err:   err,
			}
		}

		// Deserialize entry
		entry, err := l.deserializeEntry(iter.Value())
		if err != nil {
			continue // Skip corrupted entries
		}

		if entries, exists := recoveredTxns[entry.TxnID]; exists {
			recoveredTxns[entry.TxnID] = append(entries, entry)
		} else {
			recoveredTxns[entry.TxnID] = []*IndexLogEntry{entry}
		}
	}

	// Restore recovered transactions to pending state
	for txnID, entries := range recoveredTxns {
		var maxSeq int64
		for _, e := range entries {
			if e.Sequence >= maxSeq {
				maxSeq = e.Sequence + 1
			}
		}

		data := &txnData{
			ops:      entries,
			sequence: maxSeq,
			state:    TxnStatePending,
		}

		l.transactions.Store(txnID, data)
	}

	return nil
}

// GetTransactionState returns the current state of a transaction.
func (l *IndexTransactionLog) GetTransactionState(txnID string) (TxnState, error) {
	if txnID == "" {
		return TxnStatePending, ErrInvalidTxnID
	}

	dataVal, exists := l.transactions.Load(txnID)
	if !exists {
		return TxnStatePending, ErrTxnNotFound
	}

	data, ok := dataVal.(*txnData)
	if !ok {
		return TxnStatePending, ErrInternal
	}

	data.mu.Lock()
	state := data.state
	data.mu.Unlock()

	return state, nil
}

// PendingTransactionCount returns the number of pending transactions.
func (l *IndexTransactionLog) PendingTransactionCount() int {
	var count int32
	l.transactions.Range(func(key, value interface{}) bool {
		if data, ok := value.(*txnData); ok {
			data.mu.Lock()
			if data.state == TxnStatePending || data.state == TxnStatePrepared {
				atomic.AddInt32(&count, 1)
			}
			data.mu.Unlock()
		}
		return true
	})
	return int(count)
}

// GetPendingTransactionIDs returns the IDs of all pending transactions.
func (l *IndexTransactionLog) GetPendingTransactionIDs() []string {
	var txnIDs []string
	l.transactions.Range(func(key, value interface{}) bool {
		txnID, ok := key.(string)
		if !ok {
			return true
		}

		data, ok := value.(*txnData)
		if !ok {
			return true
		}

		data.mu.Lock()
		isPending := data.state == TxnStatePending || data.state == TxnStatePrepared
		data.mu.Unlock()

		if isPending {
			txnIDs = append(txnIDs, txnID)
		}
		return true
	})
	return txnIDs
}

// buildKey creates a storage key for a log entry.
func (l *IndexTransactionLog) buildKey(txnID string, sequence int64) []byte {
	return []byte(l.buildKeyPrefix(txnID) + ":" + formatSequence(sequence))
}

// buildKeyPrefix creates the key prefix for a transaction.
func (l *IndexTransactionLog) buildKeyPrefix(txnID string) string {
	return prefixTxnPending + l.namespace + ":" + txnID
}

// formatSequence formats a sequence number for lexicographic sorting.
func formatSequence(seq int64) string {
	// Use fixed-width formatting for proper lexicographic sorting
	return padLeft(seq, 20)
}

// padLeft pads a number with leading zeros.
func padLeft(n int64, width int) string {
	result := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		result[i] = byte('0' + n%10)
		n /= 10
	}
	return string(result)
}

// persistEntry writes a log entry to storage.
func (l *IndexTransactionLog) persistEntry(entry *IndexLogEntry) error {
	key := l.buildKey(entry.TxnID, entry.Sequence)

	data, err := l.serializeEntry(entry)
	if err != nil {
		return &TransactionLogError{
			Op:    "persistEntry",
			TxnID: entry.TxnID,
			Err:   err,
		}
	}

	if err := l.store.Set(key, data); err != nil {
		return &TransactionLogError{
			Op:    "persistEntry",
			TxnID: entry.TxnID,
			Err:   err,
		}
	}

	return nil
}

// persistTxnState writes transaction state to storage.
func (l *IndexTransactionLog) persistTxnState(txnID string, state TxnState) error {
	key := []byte(prefixTxnLog + l.namespace + ":state:" + txnID)
	data := []byte{byte(state)}

	if err := l.store.Set(key, data); err != nil {
		return &TransactionLogError{
			Op:    "persistTxnState",
			TxnID: txnID,
			Err:   err,
		}
	}

	return nil
}

// clearPersistedEntries removes all persisted entries for a transaction.
func (l *IndexTransactionLog) clearPersistedEntries(txnID string) error {
	prefix := []byte(l.buildKeyPrefix(txnID))
	upperBound := append(prefix, 0xFF)

	opts := &storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	}

	iter := l.store.NewIterator(opts)
	if iter == nil {
		return nil
	}
	defer func() { _ = iter.Close() }()

	keysToDelete := make([][]byte, 0)

	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		if err := iter.Error(); err != nil {
			return &TransactionLogError{
				Op:    "clearPersistedEntries",
				TxnID: txnID,
				Err:   err,
			}
		}

		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		keysToDelete = append(keysToDelete, keyCopy)
	}

	// Delete collected keys
	for _, key := range keysToDelete {
		if err := l.store.Delete(key); err != nil {
			return &TransactionLogError{
				Op:    "clearPersistedEntries",
				TxnID: txnID,
				Err:   err,
			}
		}
	}

	// Also delete state key
	stateKey := []byte(prefixTxnLog + l.namespace + ":state:" + txnID)
	if err := l.store.Delete(stateKey); err != nil {
		// Ignore not found errors
		return nil
	}

	return nil
}

// serializeEntry converts a log entry to bytes.
func (l *IndexTransactionLog) serializeEntry(entry *IndexLogEntry) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)

	if err := enc.Encode(entry); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// deserializeEntry converts bytes back to a log entry.
func (l *IndexTransactionLog) deserializeEntry(data []byte) (*IndexLogEntry, error) {
	var entry IndexLogEntry

	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)

	if err := dec.Decode(&entry); err != nil {
		return nil, err
	}

	return &entry, nil
}
