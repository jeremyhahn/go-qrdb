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

package vtab

import "context"

// ---------------------------------------------------------------------------
// Operation types
// ---------------------------------------------------------------------------

// OpType identifies a buffered write operation kind.
type OpType uint8

const (
	// OpPut queues a key-value write.
	OpPut OpType = iota

	// OpDelete queues a key removal.
	OpDelete
)

// ---------------------------------------------------------------------------
// Buffered operation
// ---------------------------------------------------------------------------

// BufferedOp is a single write operation queued inside a transaction.
type BufferedOp struct {
	Op    OpType
	Key   string
	Value []byte
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// TxAlreadyActiveError is returned when begin is called on a transaction
// buffer that already has an in-progress transaction.
type TxAlreadyActiveError struct{}

// Error implements the error interface.
func (e *TxAlreadyActiveError) Error() string {
	return "vtab: transaction already active"
}

// TxCommitError is returned when the batch executor fails during commit.
// Err is the underlying failure returned by the executor.
type TxCommitError struct {
	Err error
}

// Error implements the error interface.
func (e *TxCommitError) Error() string {
	return "vtab: transaction commit failed: " + e.Err.Error()
}

// Unwrap returns the underlying executor error.
func (e *TxCommitError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// txBuffer
// ---------------------------------------------------------------------------

// txBuffer collects write operations during a SQLite transaction and flushes
// them as a single atomic batch on commit. This maps one SQLite transaction to
// one QRDB batch, preserving all-or-nothing semantics for the write set.
//
// txBuffer is not safe for concurrent use. SQLite serializes virtual-table
// transaction callbacks on the connection goroutine, so no additional
// synchronization is needed.
type txBuffer struct {
	ops      []BufferedOp
	executor batchExecutor
	active   bool
}

// newTxBuffer creates a txBuffer that delegates batch execution to the given
// executor. executor must not be nil.
func newTxBuffer(executor batchExecutor) *txBuffer {
	return &txBuffer{
		executor: executor,
	}
}

// begin marks the transaction as active. Returns TxAlreadyActiveError if a
// transaction is already in progress.
func (t *txBuffer) begin() error {
	if t.active {
		return &TxAlreadyActiveError{}
	}
	t.active = true
	return nil
}

// put buffers a Put operation. Callers should only invoke put after a
// successful begin; buffering without an active transaction is a programming
// error but is otherwise silent for simplicity.
func (t *txBuffer) put(key string, value []byte) {
	t.ops = append(t.ops, BufferedOp{Op: OpPut, Key: key, Value: value})
}

// del buffers a Delete operation. Same preconditions as put.
func (t *txBuffer) del(key string) {
	t.ops = append(t.ops, BufferedOp{Op: OpDelete, Key: key})
}

// commit flushes all buffered operations to the executor as a single batch,
// then resets the buffer. If the buffer is empty the executor is not called.
// If the executor returns an error, the buffer is discarded and the
// transaction is marked inactive, and a TxCommitError is returned.
// commit is a no-op when no transaction is active.
func (t *txBuffer) commit(ctx context.Context) error {
	if !t.active {
		return nil
	}

	// Capture and clear the buffer before calling the executor so that the
	// txBuffer is always in a clean state even on error.
	ops := t.ops
	t.ops = nil
	t.active = false

	if len(ops) == 0 {
		return nil
	}

	if err := t.executor.executeBatch(ctx, ops); err != nil {
		return &TxCommitError{Err: err}
	}
	return nil
}

// rollback discards all buffered operations and marks the transaction
// inactive. rollback is a no-op when no transaction is active.
func (t *txBuffer) rollback() {
	if !t.active {
		return
	}
	t.ops = nil
	t.active = false
}

// isActive returns true when a transaction has been started with begin and
// has not yet been committed or rolled back.
func (t *txBuffer) isActive() bool {
	return t.active
}

// pending returns the number of operations currently buffered.
func (t *txBuffer) pending() int {
	return len(t.ops)
}
