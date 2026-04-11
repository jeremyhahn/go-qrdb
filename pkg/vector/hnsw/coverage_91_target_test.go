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
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetTransactionState_EmptyTxnID verifies that GetTransactionState returns
// ErrInvalidTxnID when called with an empty transaction ID.
func TestGetTransactionState_EmptyTxnID(t *testing.T) {
	store := newMockStorage()
	log := NewIndexTransactionLog(store, "test-ns")

	_, err := log.GetTransactionState("")
	assert.ErrorIs(t, err, ErrInvalidTxnID)
}

// TestCommit_AlreadyAbortedTransaction verifies that Commit returns
// ErrTxnNotActive when called on a transaction that has already been aborted.
func TestCommit_AlreadyAbortedTransaction(t *testing.T) {
	store := newMockStorage()
	log := NewIndexTransactionLog(store, "test-ns")

	// Directly store a txnData in aborted state.
	log.transactions.Store("aborted-txn", &txnData{
		state: TxnStateAborted,
	})

	err := log.Commit("aborted-txn")
	assert.ErrorIs(t, err, ErrTxnNotActive)
}

// TestRollback_AlreadyCommittedTransaction verifies that Rollback returns
// ErrTxnAlreadyCommitted when called on a transaction that has been committed.
func TestRollback_AlreadyCommittedTransaction(t *testing.T) {
	store := newMockStorage()
	log := NewIndexTransactionLog(store, "test-ns")

	// Directly store a txnData in committed state.
	log.transactions.Store("committed-txn", &txnData{
		state: TxnStateCommitted,
	})

	err := log.Rollback("committed-txn")
	assert.ErrorIs(t, err, ErrTxnAlreadyCommitted)
}

// TestRollback_AlreadyAbortedTransaction verifies that Rollback returns
// ErrTxnAlreadyAborted when called on a transaction that has already been aborted.
func TestRollback_AlreadyAbortedTransaction(t *testing.T) {
	store := newMockStorage()
	log := NewIndexTransactionLog(store, "test-ns")

	// Directly store a txnData in aborted state.
	log.transactions.Store("aborted-txn", &txnData{
		state: TxnStateAborted,
	})

	err := log.Rollback("aborted-txn")
	assert.ErrorIs(t, err, ErrTxnAlreadyAborted)
}
