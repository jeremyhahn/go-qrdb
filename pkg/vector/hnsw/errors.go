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
	"fmt"
)

var (
	// ErrInvalidConfig is returned when the configuration is invalid.
	ErrInvalidConfig = errors.New("invalid HNSW configuration")

	// ErrNotFound is returned when a node is not found in the index.
	ErrNotFound = errors.New("node not found")

	// ErrEmptyIndex is returned when the index is empty.
	ErrEmptyIndex = errors.New("index is empty")

	// ErrInvalidVector is returned when a vector has invalid dimensions.
	ErrInvalidVector = errors.New("invalid vector dimensions")

	// ErrInvalidID is returned when a node ID is invalid.
	ErrInvalidID = errors.New("invalid node ID")

	// ErrDuplicateID is returned when attempting to insert a duplicate ID.
	ErrDuplicateID = errors.New("duplicate node ID")

	// ErrInternal is returned when an internal error occurs.
	ErrInternal = errors.New("internal error")

	// Transaction-related errors.

	// ErrInvalidTxnID is returned when a transaction ID is invalid.
	ErrInvalidTxnID = errors.New("invalid transaction ID")

	// ErrTxnNotFound is returned when a transaction is not found.
	ErrTxnNotFound = errors.New("transaction not found")

	// ErrTxnAlreadyExists is returned when a transaction already exists.
	ErrTxnAlreadyExists = errors.New("transaction already exists")

	// ErrTxnNotActive is returned when a transaction is not in an active state.
	ErrTxnNotActive = errors.New("transaction is not active")

	// ErrTxnAlreadyCommitted is returned when trying to rollback a committed transaction.
	ErrTxnAlreadyCommitted = errors.New("transaction already committed")

	// ErrTxnAlreadyAborted is returned when trying to operate on an aborted transaction.
	ErrTxnAlreadyAborted = errors.New("transaction already aborted")

	// ErrInvalidLogEntry is returned when a log entry is invalid.
	ErrInvalidLogEntry = errors.New("invalid log entry")

	// ErrRollbackFailed is returned when a rollback operation fails.
	ErrRollbackFailed = errors.New("rollback operation failed")

	// ErrRecoveryFailed is returned when crash recovery fails.
	ErrRecoveryFailed = errors.New("transaction recovery failed")

	// ErrStorageUnavailable is returned when storage is not available.
	ErrStorageUnavailable = errors.New("storage unavailable")

	// ErrInvalidOperation is returned when an invalid operation is requested.
	ErrInvalidOperation = errors.New("invalid operation")

	// ErrNodeNotInTransaction is returned when a node is not part of a transaction.
	ErrNodeNotInTransaction = errors.New("node not in transaction")

	// Distance-related errors.

	// ErrDimensionMismatch is returned when vector dimensions do not match.
	ErrDimensionMismatch = errors.New("dimension mismatch")

	// ErrUnsupportedMetric is returned when an unknown distance metric is requested.
	ErrUnsupportedMetric = errors.New("unsupported distance metric")

	// ErrEmptyBatch is returned when an empty batch of vectors is provided.
	ErrEmptyBatch = errors.New("empty vector batch")
)

// TransactionLogError represents an error that occurred during transaction logging.
type TransactionLogError struct {
	Op    string
	TxnID string
	Err   error
}

// Error implements the error interface.
func (e *TransactionLogError) Error() string {
	if e.TxnID != "" {
		return fmt.Sprintf("txn_log.%s [txn=%s]: %v", e.Op, e.TxnID, e.Err)
	}
	return fmt.Sprintf("txn_log.%s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *TransactionLogError) Unwrap() error {
	return e.Err
}

// RollbackError represents an error that occurred during rollback.
type RollbackError struct {
	Op       string
	TxnID    string
	VectorID uint64
	Err      error
}

// Error implements the error interface.
func (e *RollbackError) Error() string {
	if e.VectorID != 0 {
		return fmt.Sprintf("rollback.%s [txn=%s, vector=%d]: %v", e.Op, e.TxnID, e.VectorID, e.Err)
	}
	return fmt.Sprintf("rollback.%s [txn=%s]: %v", e.Op, e.TxnID, e.Err)
}

// Unwrap returns the underlying error.
func (e *RollbackError) Unwrap() error {
	return e.Err
}

// RecoveryError represents an error that occurred during crash recovery.
type RecoveryError struct {
	Op  string
	Err error
}

// Error implements the error interface.
func (e *RecoveryError) Error() string {
	return fmt.Sprintf("recovery.%s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *RecoveryError) Unwrap() error {
	return e.Err
}

// DimensionMismatchError is returned when a target vector's dimensions do not
// match the query vector's dimensions.
type DimensionMismatchError struct {
	Index    int
	Got      int
	Expected int
}

// Error implements the error interface.
func (e *DimensionMismatchError) Error() string {
	return fmt.Sprintf("dimension mismatch at index %d: got %d, expected %d", e.Index, e.Got, e.Expected)
}

// Unwrap returns the sentinel ErrDimensionMismatch error.
func (e *DimensionMismatchError) Unwrap() error {
	return ErrDimensionMismatch
}

// UnsupportedMetricError is returned when an unknown distance metric is requested.
type UnsupportedMetricError struct {
	Metric DistanceMetric
}

// Error implements the error interface.
func (e *UnsupportedMetricError) Error() string {
	return fmt.Sprintf("unsupported distance metric: %d", e.Metric)
}

// Unwrap returns the sentinel ErrUnsupportedMetric error.
func (e *UnsupportedMetricError) Unwrap() error {
	return ErrUnsupportedMetric
}

// BatchError represents an error that occurred during batch distance computation.
type BatchError struct {
	Op  string
	Err error
}

// Error implements the error interface.
func (e *BatchError) Error() string {
	return fmt.Sprintf("batch.%s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *BatchError) Unwrap() error {
	return e.Err
}
