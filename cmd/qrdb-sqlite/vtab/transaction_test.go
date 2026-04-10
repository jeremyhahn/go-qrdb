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

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock executor
// ---------------------------------------------------------------------------

// mockExecutor records executeBatch invocations for assertion in tests.
type mockExecutor struct {
	// calls holds every slice of ops passed to executeBatch.
	calls [][]BufferedOp

	// err, when non-nil, is returned by executeBatch.
	err error
}

func (m *mockExecutor) executeBatch(_ context.Context, ops []BufferedOp) error {
	if m.err != nil {
		return m.err
	}
	// Copy ops so mutations to the txBuffer's internal slice don't affect the
	// recorded snapshot.
	copied := make([]BufferedOp, len(ops))
	copy(copied, ops)
	m.calls = append(m.calls, copied)
	return nil
}

// callCount returns the number of times executeBatch was invoked.
func (m *mockExecutor) callCount() int {
	return len(m.calls)
}

// allOps returns a flat slice of every op seen across all invocations.
func (m *mockExecutor) allOps() []BufferedOp {
	var out []BufferedOp
	for _, call := range m.calls {
		out = append(out, call...)
	}
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestTxBuffer_Empty(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := buf.commit(context.Background()); err != nil {
		t.Fatalf("commit empty buffer: %v", err)
	}
	if exec.callCount() != 0 {
		t.Fatalf("expected 0 executor calls for empty commit, got %d", exec.callCount())
	}
	if buf.isActive() {
		t.Fatal("buffer should not be active after commit")
	}
}

func TestTxBuffer_SingleWrite(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	buf.put("k1", []byte("v1"))

	if buf.pending() != 1 {
		t.Fatalf("pending: want 1, got %d", buf.pending())
	}

	if err := buf.commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if exec.callCount() != 1 {
		t.Fatalf("expected 1 executor call, got %d", exec.callCount())
	}
	ops := exec.allOps()
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != OpPut {
		t.Errorf("op type: want OpPut, got %v", ops[0].Op)
	}
	if ops[0].Key != "k1" {
		t.Errorf("key: want k1, got %s", ops[0].Key)
	}
	if string(ops[0].Value) != "v1" {
		t.Errorf("value: want v1, got %s", ops[0].Value)
	}
	if buf.isActive() {
		t.Fatal("buffer should not be active after commit")
	}
}

func TestTxBuffer_MultipleWrites(t *testing.T) {
	const n = 100
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < n; i++ {
		buf.put("key", []byte("val"))
	}

	if buf.pending() != n {
		t.Fatalf("pending: want %d, got %d", n, buf.pending())
	}

	if err := buf.commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// All writes must be delivered in exactly one executeBatch call.
	if exec.callCount() != 1 {
		t.Fatalf("expected 1 executeBatch call, got %d", exec.callCount())
	}
	if len(exec.calls[0]) != n {
		t.Fatalf("expected %d ops in single call, got %d", n, len(exec.calls[0]))
	}
}

func TestTxBuffer_MixedOps(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	buf.put("a", []byte("1"))
	buf.del("b")
	buf.put("c", []byte("3"))
	buf.del("d")

	if err := buf.commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	ops := exec.allOps()
	if len(ops) != 4 {
		t.Fatalf("expected 4 ops, got %d", len(ops))
	}

	wantTypes := []OpType{OpPut, OpDelete, OpPut, OpDelete}
	wantKeys := []string{"a", "b", "c", "d"}
	for i, op := range ops {
		if op.Op != wantTypes[i] {
			t.Errorf("op[%d] type: want %v, got %v", i, wantTypes[i], op.Op)
		}
		if op.Key != wantKeys[i] {
			t.Errorf("op[%d] key: want %s, got %s", i, wantKeys[i], op.Key)
		}
	}
}

func TestTxBuffer_Rollback(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	buf.put("x", []byte("should be discarded"))
	buf.del("y")

	buf.rollback()

	if buf.isActive() {
		t.Fatal("buffer should not be active after rollback")
	}
	if buf.pending() != 0 {
		t.Fatalf("pending after rollback: want 0, got %d", buf.pending())
	}
	if exec.callCount() != 0 {
		t.Fatal("executor must not be called after rollback")
	}
}

func TestTxBuffer_CommitError(t *testing.T) {
	sentinel := errors.New("raft failure")
	exec := &mockExecutor{err: sentinel}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	buf.put("k", []byte("v"))

	err := buf.commit(context.Background())
	if err == nil {
		t.Fatal("expected error from commit, got nil")
	}

	var commitErr *TxCommitError
	if !errors.As(err, &commitErr) {
		t.Fatalf("want TxCommitError, got %T: %v", err, err)
	}
	if !errors.Is(commitErr.Unwrap(), sentinel) {
		t.Fatalf("wrapped error: want sentinel, got %v", commitErr.Unwrap())
	}

	// Buffer must be fully reset even on error.
	if buf.isActive() {
		t.Fatal("buffer should not be active after failed commit")
	}
	if buf.pending() != 0 {
		t.Fatalf("pending after failed commit: want 0, got %d", buf.pending())
	}
}

func TestTxBuffer_Pending(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if buf.pending() != 0 {
		t.Fatalf("initial pending: want 0, got %d", buf.pending())
	}

	if err := buf.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if buf.pending() != 0 {
		t.Fatalf("pending after begin: want 0, got %d", buf.pending())
	}

	buf.put("a", []byte("1"))
	if buf.pending() != 1 {
		t.Fatalf("pending after 1 put: want 1, got %d", buf.pending())
	}

	buf.del("b")
	if buf.pending() != 2 {
		t.Fatalf("pending after del: want 2, got %d", buf.pending())
	}

	buf.put("c", []byte("3"))
	if buf.pending() != 3 {
		t.Fatalf("pending after 2nd put: want 3, got %d", buf.pending())
	}

	if err := buf.commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if buf.pending() != 0 {
		t.Fatalf("pending after commit: want 0, got %d", buf.pending())
	}
}

func TestTxBuffer_Begin_AlreadyActive(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	if err := buf.begin(); err != nil {
		t.Fatalf("first begin: %v", err)
	}

	err := buf.begin()
	if err == nil {
		t.Fatal("expected error on second begin, got nil")
	}
	var activeErr *TxAlreadyActiveError
	if !errors.As(err, &activeErr) {
		t.Fatalf("want TxAlreadyActiveError, got %T: %v", err, err)
	}
}

func TestTxBuffer_Commit_NotActive(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	// commit on a buffer that was never started must be a no-op.
	if err := buf.commit(context.Background()); err != nil {
		t.Fatalf("commit on inactive buffer: unexpected error: %v", err)
	}
	if exec.callCount() != 0 {
		t.Fatalf("executor called on inactive commit: got %d calls", exec.callCount())
	}
}

func TestTxErrors_Messages(t *testing.T) {
	t.Run("TxAlreadyActiveError", func(t *testing.T) {
		err := &TxAlreadyActiveError{}
		if err.Error() == "" {
			t.Fatal("TxAlreadyActiveError.Error() must not be empty")
		}
	})

	t.Run("TxCommitError", func(t *testing.T) {
		inner := errors.New("raft unavailable")
		err := &TxCommitError{Err: inner}
		if err.Error() == "" {
			t.Fatal("TxCommitError.Error() must not be empty")
		}
		if !errors.Is(err.Unwrap(), inner) {
			t.Fatal("TxCommitError.Unwrap() must return inner error")
		}
	})
}

func TestTxBuffer_Rollback_NotActive(t *testing.T) {
	exec := &mockExecutor{}
	buf := newTxBuffer(exec)

	// rollback on a buffer that was never started must be a no-op.
	buf.rollback()

	if buf.isActive() {
		t.Fatal("isActive should remain false after rollback on inactive buffer")
	}
	if exec.callCount() != 0 {
		t.Fatal("executor must not be called on rollback of inactive buffer")
	}
}
