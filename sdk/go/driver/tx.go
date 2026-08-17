//go:build qrdb_sql

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

package driver

import (
	"context"
	"database/sql/driver"
	"sync/atomic"
)

// Compile-time interface assertion.
var _ driver.Tx = (*embeddedTx)(nil)

// embeddedTx implements driver.Tx for embedded connections by issuing
// COMMIT/ROLLBACK SQL statements to the GMS engine.
type embeddedTx struct {
	conn *embeddedConn
	done atomic.Bool
}

// Commit commits the transaction by issuing a COMMIT statement.
func (tx *embeddedTx) Commit() error {
	if !tx.done.CompareAndSwap(false, true) {
		return &TxDoneError{}
	}
	tx.conn.inTx.Store(false)
	return tx.execTxStmt("COMMIT")
}

// Rollback aborts the transaction by issuing a ROLLBACK statement.
func (tx *embeddedTx) Rollback() error {
	if !tx.done.CompareAndSwap(false, true) {
		return &TxDoneError{}
	}
	tx.conn.inTx.Store(false)
	return tx.execTxStmt("ROLLBACK")
}

// execTxStmt executes a transaction control statement (COMMIT or ROLLBACK)
// on the connection's own session, so the engine applies it to the
// transaction that BeginTx started rather than to a fresh one.
func (tx *embeddedTx) execTxStmt(query string) error {
	_, err := tx.conn.execInternal(context.Background(), query)
	return err
}

// networkTx is a no-op transaction for network connections.
// Network mode currently uses auto-commit semantics.
type networkTx struct {
	done atomic.Bool
}

// Compile-time interface assertion.
var _ driver.Tx = (*networkTx)(nil)

// Commit is a no-op for network transactions (auto-commit).
func (tx *networkTx) Commit() error {
	if !tx.done.CompareAndSwap(false, true) {
		return &TxDoneError{}
	}
	return nil
}

// Rollback is a no-op for network transactions (auto-commit).
func (tx *networkTx) Rollback() error {
	if !tx.done.CompareAndSwap(false, true) {
		return &TxDoneError{}
	}
	return nil
}
