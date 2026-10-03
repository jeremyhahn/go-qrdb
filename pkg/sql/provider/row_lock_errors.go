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

package provider

// SkipLockedUnsupportedError is the refusal of SKIP LOCKED. Locking only the
// rows nobody else holds would need the read to skip rows by lock state,
// which the lock table does not support; the clause is refused by name
// rather than accepted and silently treated as a plain lock clause. On the
// MySQL wire it is ER_NOT_SUPPORTED_YET (1235).
type SkipLockedUnsupportedError struct{}

// Error returns a human-readable description.
func (e *SkipLockedUnsupportedError) Error() string {
	return "provider: SKIP LOCKED is not supported; use NOWAIT"
}

// TransactionAbortedError is what every statement of a transaction that was
// rolled back as a deadlock victim (or after losing its row locks) fails
// with, until the client ends the transaction with ROLLBACK or COMMIT. It is
// wrapped in go-mysql-server's ER_LOCK_DEADLOCK on the MySQL wire and maps
// to SQLSTATE 25P02 on the PostgreSQL wire.
type TransactionAbortedError struct {
	// Cause is the error that rolled the transaction back.
	Cause error
}

// Error returns a human-readable description.
func (e *TransactionAbortedError) Error() string {
	msg := "provider: current transaction is aborted"
	if e.Cause != nil {
		msg += " (" + e.Cause.Error() + ")"
	}
	return msg + "; commands ignored until end of transaction block"
}

// Unwrap returns the error that rolled the transaction back.
func (e *TransactionAbortedError) Unwrap() error { return e.Cause }

// RowLockingUnavailableError reports a statement that needs a row lock on a
// table whose composition provides no lock table (no lease or watch service
// for its database).
type RowLockingUnavailableError struct {
	// Table names the table.
	Table string
}

// Error returns a human-readable description.
func (e *RowLockingUnavailableError) Error() string {
	return "provider: row locking is unavailable for relational table: " + e.Table
}
