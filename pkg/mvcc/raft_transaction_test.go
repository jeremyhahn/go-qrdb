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

package mvcc

import (
	"bytes"
	"context"
	stderrors "errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// ---------------------------------------------------------------------------
// Transaction creation
// ---------------------------------------------------------------------------

func TestRaftTransaction_New_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if txn == nil {
		t.Fatal("expected non-nil transaction")
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Transaction Put
// ---------------------------------------------------------------------------

func TestRaftTransaction_Put_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.Put([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Clean up
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestRaftTransaction_Put_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	if err := txn.Put(nil, []byte("value")); err == nil {
		t.Fatal("expected error for nil key")
	}

	if err := txn.Put([]byte{}, []byte("value")); err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestRaftTransaction_Put_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	err = txn.Put([]byte("key"), []byte("value"))
	if err == nil {
		t.Fatal("expected error for put after close")
	}
}

// ---------------------------------------------------------------------------
// Transaction Delete
// ---------------------------------------------------------------------------

func TestRaftTransaction_Delete_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.Delete([]byte("key1")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestRaftTransaction_Delete_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	if err := txn.Delete(nil); err == nil {
		t.Fatal("expected error for nil key")
	}

	if err := txn.Delete([]byte{}); err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestRaftTransaction_Delete_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	err = txn.Delete([]byte("key"))
	if err == nil {
		t.Fatal("expected error for delete after close")
	}
}

// ---------------------------------------------------------------------------
// Transaction Get
// ---------------------------------------------------------------------------

func TestRaftTransaction_Get_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Seed data before transaction
	_, _, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	kv, err := txn.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("value1")) {
		t.Errorf("value: got %q, want %q", kv.Value, "value1")
	}
}

func TestRaftTransaction_Get_NotFound(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	_, err = txn.Get([]byte("nonexistent"))
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestRaftTransaction_Get_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	if _, err := txn.Get(nil); err == nil {
		t.Fatal("expected error for nil key")
	}

	if _, err := txn.Get([]byte{}); err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestRaftTransaction_Get_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	_, err = txn.Get([]byte("key"))
	if err == nil {
		t.Fatal("expected error for get after close")
	}
}

func TestRaftTransaction_Get_CachesReads(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Put(ctx, []byte("cached"), []byte("original"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	// First read
	kv1, err := txn.Get([]byte("cached"))
	if err != nil {
		t.Fatalf("Get 1: %v", err)
	}

	// Modify externally
	_, _, err = s.Put(ctx, []byte("cached"), []byte("modified"))
	if err != nil {
		t.Fatalf("Put modified: %v", err)
	}

	// Second read should return cached value (snapshot isolation)
	kv2, err := txn.Get([]byte("cached"))
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}

	if !bytes.Equal(kv1.Value, kv2.Value) {
		t.Errorf("read cache broken: first=%q, second=%q", kv1.Value, kv2.Value)
	}
}

// ---------------------------------------------------------------------------
// AddCompare
// ---------------------------------------------------------------------------

func TestRaftTransaction_AddCompare_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	cmp := &RaftCompare{
		Key:      []byte("key1"),
		Target:   RaftCompareVersion,
		Result:   RaftCompareEqual,
		Revision: 0,
	}
	if err := txn.AddCompare(cmp); err != nil {
		t.Fatalf("AddCompare: %v", err)
	}
}

func TestRaftTransaction_AddCompare_InvalidCompare(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	// Empty key is invalid
	cmp := &RaftCompare{
		Key:      nil,
		Target:   RaftCompareVersion,
		Result:   RaftCompareEqual,
		Revision: 0,
	}
	if err := txn.AddCompare(cmp); err == nil {
		t.Fatal("expected error for invalid compare")
	}
}

func TestRaftTransaction_AddCompare_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	cmp := &RaftCompare{
		Key:      []byte("key1"),
		Target:   RaftCompareVersion,
		Result:   RaftCompareEqual,
		Revision: 0,
	}
	if err := txn.AddCompare(cmp); err == nil {
		t.Fatal("expected error for AddCompare after close")
	}
}

// ---------------------------------------------------------------------------
// SetSuccess / SetFailure
// ---------------------------------------------------------------------------

func TestRaftTransaction_SetSuccess_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	ops := []RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("key1"), Value: []byte("val1")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}
}

func TestRaftTransaction_SetSuccess_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	err = txn.SetSuccess([]RaftTxnOp{})
	if err == nil {
		t.Fatal("expected error for SetSuccess after close")
	}
}

func TestRaftTransaction_SetFailure_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	defer func() { _ = txn.Rollback() }()

	ops := []RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("fallback"), Value: []byte("default")},
	}
	if err := txn.SetFailure(ops); err != nil {
		t.Fatalf("SetFailure: %v", err)
	}
}

func TestRaftTransaction_SetFailure_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	err = txn.SetFailure([]RaftTxnOp{})
	if err == nil {
		t.Fatal("expected error for SetFailure after close")
	}
}

// ---------------------------------------------------------------------------
// Commit (local mode - no Raft)
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_LocalPut(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	// Set success operations
	ops := []RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("txn-key"), Value: []byte("txn-val")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true with no compares")
	}
	if result.Revision < 1 {
		t.Errorf("expected positive revision, got %d", result.Revision)
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	if !bytes.Equal(result.Responses[0].Value, []byte("txn-val")) {
		t.Errorf("response value: got %q, want %q", result.Responses[0].Value, "txn-val")
	}

	// Verify key exists in storage
	kv, err := s.Get(ctx, []byte("txn-key"), 0)
	if err != nil {
		t.Fatalf("Get after commit: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("txn-val")) {
		t.Errorf("stored value: got %q, want %q", kv.Value, "txn-val")
	}
}

func TestRaftTransaction_Commit_LocalDelete(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Seed a key
	_, _, err := s.Put(ctx, []byte("to-delete"), []byte("victim"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpDelete, Key: []byte("to-delete")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	// Previous value should be returned
	if result.Responses[0] != nil && !bytes.Equal(result.Responses[0].Value, []byte("victim")) {
		t.Errorf("delete response: got %q, want %q", result.Responses[0].Value, "victim")
	}

	// Verify key is gone
	_, err = s.Get(ctx, []byte("to-delete"), 0)
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

func TestRaftTransaction_Commit_LocalGet(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Put(ctx, []byte("readable"), []byte("read-val"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpGet, Key: []byte("readable")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	if !bytes.Equal(result.Responses[0].Value, []byte("read-val")) {
		t.Errorf("get response: got %q, want %q", result.Responses[0].Value, "read-val")
	}
}

func TestRaftTransaction_Commit_LocalGetNotFound(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpGet, Key: []byte("missing")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	if result.Responses[0] != nil {
		t.Errorf("expected nil response for missing key, got: %v", result.Responses[0])
	}
}

func TestRaftTransaction_Commit_EmptyOps(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	// No success/failure ops set
	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true with no compares and no ops")
	}
	if len(result.Responses) != 0 {
		t.Errorf("expected 0 responses, got %d", len(result.Responses))
	}
}

func TestRaftTransaction_Commit_AfterClose(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	_, err = txn.Commit()
	if err == nil {
		t.Fatal("expected error for Commit after close")
	}
}

func TestRaftTransaction_Commit_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	cancel() // Cancel before commit

	_, err = txn.Commit()
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

// ---------------------------------------------------------------------------
// Compare-and-swap: success path
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_CompareSucceeds(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Seed a key at version 1
	_, _, err := s.Put(ctx, []byte("cas-key"), []byte("old-val"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	// Compare: version == 1
	cmp := &RaftCompare{
		Key:      []byte("cas-key"),
		Target:   RaftCompareVersion,
		Result:   RaftCompareEqual,
		Revision: 1,
	}
	if err := txn.AddCompare(cmp); err != nil {
		t.Fatalf("AddCompare: %v", err)
	}

	// Success: update value
	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("cas-key"), Value: []byte("new-val")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	// Failure: do nothing
	if err := txn.SetFailure([]RaftTxnOp{}); err != nil {
		t.Fatalf("SetFailure: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected comparison to succeed")
	}

	// Verify value was updated
	kv, err := s.Get(ctx, []byte("cas-key"), 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("new-val")) {
		t.Errorf("value: got %q, want %q", kv.Value, "new-val")
	}
}

// ---------------------------------------------------------------------------
// Compare-and-swap: failure path
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_CompareFails(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Seed a key at version 1
	_, _, err := s.Put(ctx, []byte("cas-key"), []byte("old-val"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	// Compare: version == 99 (will fail since version is 1)
	cmp := &RaftCompare{
		Key:      []byte("cas-key"),
		Target:   RaftCompareVersion,
		Result:   RaftCompareEqual,
		Revision: 99,
	}
	if err := txn.AddCompare(cmp); err != nil {
		t.Fatalf("AddCompare: %v", err)
	}

	// Success: this should NOT execute
	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("cas-key"), Value: []byte("should-not-apply")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	// Failure: apply fallback
	if err := txn.SetFailure([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("fallback-key"), Value: []byte("fallback-val")},
	}); err != nil {
		t.Fatalf("SetFailure: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Succeeded {
		t.Error("expected comparison to fail")
	}

	// Original value should be unchanged
	kv, err := s.Get(ctx, []byte("cas-key"), 0)
	if err != nil {
		t.Fatalf("Get cas-key: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("old-val")) {
		t.Errorf("cas-key value: got %q, want %q", kv.Value, "old-val")
	}

	// Fallback key should exist
	fbkv, err := s.Get(ctx, []byte("fallback-key"), 0)
	if err != nil {
		t.Fatalf("Get fallback-key: %v", err)
	}
	if !bytes.Equal(fbkv.Value, []byte("fallback-val")) {
		t.Errorf("fallback-key value: got %q, want %q", fbkv.Value, "fallback-val")
	}
}

// ---------------------------------------------------------------------------
// Compare with nonexistent key
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_CompareNonexistentKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	// Compare: nonexistent key version == 0 (should succeed since missing key has version 0)
	cmp := &RaftCompare{
		Key:      []byte("no-such-key"),
		Target:   RaftCompareVersion,
		Result:   RaftCompareEqual,
		Revision: 0,
	}
	if err := txn.AddCompare(cmp); err != nil {
		t.Fatalf("AddCompare: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("created"), Value: []byte("new")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected comparison to succeed (nonexistent key has version 0)")
	}
}

// ---------------------------------------------------------------------------
// Rollback
// ---------------------------------------------------------------------------

func TestRaftTransaction_Rollback_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestRaftTransaction_Rollback_Idempotent(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback 1: %v", err)
	}

	// Second rollback should be a no-op (returns nil)
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback 2: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Raft-backed transaction (executeOpsViaRaft)
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_ViaRaft_Put(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("raft-key"), Value: []byte("raft-val")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if result.Revision < 1 {
		t.Errorf("expected positive revision, got %d", result.Revision)
	}
	if raft.proposalCount() != 1 {
		t.Errorf("expected 1 proposal, got %d", raft.proposalCount())
	}

	// Response should contain the key
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	if !bytes.Equal(result.Responses[0].Key, []byte("raft-key")) {
		t.Errorf("response key: got %q, want %q", result.Responses[0].Key, "raft-key")
	}
}

func TestRaftTransaction_Commit_ViaRaft_Delete(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	// Seed data
	_, _, err := s.Put(ctx, []byte("del-key"), []byte("del-val"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpDelete, Key: []byte("del-key")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if raft.proposalCount() != 1 {
		t.Errorf("expected 1 proposal, got %d", raft.proposalCount())
	}
}

func TestRaftTransaction_Commit_ViaRaft_Get(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	_, _, err := s.Put(ctx, []byte("get-key"), []byte("get-val"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpGet, Key: []byte("get-key")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Gets don't go through Raft
	if raft.proposalCount() != 0 {
		t.Errorf("expected 0 proposals for get-only ops, got %d", raft.proposalCount())
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	if !bytes.Equal(result.Responses[0].Value, []byte("get-val")) {
		t.Errorf("response value: got %q, want %q", result.Responses[0].Value, "get-val")
	}
}

func TestRaftTransaction_Commit_ViaRaft_GetNotFound(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpGet, Key: []byte("missing")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	if result.Responses[0] != nil {
		t.Errorf("expected nil for missing key, got: %v", result.Responses[0])
	}
}

func TestRaftTransaction_Commit_ViaRaft_ProposeError(t *testing.T) {
	raft := &mockRaftNode{err: errors.New(errors.ErrRaft, "mock", "propose failed")}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("fail-key"), Value: []byte("fail-val")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	_, err = txn.Commit()
	if err == nil {
		t.Fatal("expected error for propose failure")
	}
}

func TestRaftTransaction_Commit_ViaRaft_MixedOps(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	// Seed a key
	_, _, err := s.Put(ctx, []byte("existing"), []byte("old"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpGet, Key: []byte("existing")},
		{Type: RaftTxnOpPut, Key: []byte("new-key"), Value: []byte("new-val")},
		{Type: RaftTxnOpDelete, Key: []byte("existing")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.Responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(result.Responses))
	}
	// Only 1 proposal for the put+delete batch (get is local)
	if raft.proposalCount() != 1 {
		t.Errorf("expected 1 batch proposal, got %d", raft.proposalCount())
	}
}

// ---------------------------------------------------------------------------
// Unknown operation type
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_UnknownOpType_Local(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpType(99), Key: []byte("bad")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	_, err = txn.Commit()
	if err == nil {
		t.Fatal("expected error for unknown op type")
	}
	var unknownErr *RaftUnknownOperationTypeError
	if !stderrors.As(err, &unknownErr) {
		t.Errorf("expected RaftUnknownOperationTypeError, got: %T: %v", err, err)
	}
}

func TestRaftTransaction_Commit_UnknownOpType_Raft(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpType(99), Key: []byte("bad")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	_, err = txn.Commit()
	if err == nil {
		t.Fatal("expected error for unknown op type via raft")
	}
}

// ---------------------------------------------------------------------------
// RaftUnknownOperationTypeError
// ---------------------------------------------------------------------------

func TestRaftUnknownOperationTypeError_Error(t *testing.T) {
	err := &RaftUnknownOperationTypeError{OpType: RaftTxnOpType(42)}
	msg := err.Error()
	if msg != "unknown operation type: 42" {
		t.Errorf("error message: got %q, want %q", msg, "unknown operation type: 42")
	}
}

// ---------------------------------------------------------------------------
// Commit with local multi-put
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_LocalMultiplePuts(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("multi-a"), Value: []byte("val-a")},
		{Type: RaftTxnOpPut, Key: []byte("multi-b"), Value: []byte("val-b")},
		{Type: RaftTxnOpPut, Key: []byte("multi-c"), Value: []byte("val-c")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.Responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(result.Responses))
	}

	// Verify all keys exist
	for _, k := range []string{"multi-a", "multi-b", "multi-c"} {
		kv, err := s.Get(ctx, []byte(k), 0)
		if err != nil {
			t.Errorf("Get %q: %v", k, err)
			continue
		}
		expected := "val-" + k[len("multi-"):]
		if !bytes.Equal(kv.Value, []byte(expected)) {
			t.Errorf("Get %q: got %q, want %q", k, kv.Value, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Local delete of nonexistent key in transaction
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_LocalDeleteNonexistent(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpDelete, Key: []byte("ghost")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
	// Delete of nonexistent key returns nil
	if result.Responses[0] != nil {
		t.Errorf("expected nil for delete of nonexistent, got: %v", result.Responses[0])
	}
}

// ---------------------------------------------------------------------------
// Raft-backed Put for existing key (tests version info path)
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_ViaRaft_PutExistingKey(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	// Create existing key
	_, _, err := s.Put(ctx, []byte("existing"), []byte("v1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("existing"), Value: []byte("v2")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	// Version should be incremented (existing had version 1)
	if result.Responses[0].Version != 2 {
		t.Errorf("version: got %d, want 2", result.Responses[0].Version)
	}
}

// ---------------------------------------------------------------------------
// Raft-backed Delete for existing key (tests version info path)
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_ViaRaft_DeleteExistingKey(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	// Create existing key
	_, _, err := s.Put(ctx, []byte("to-del"), []byte("val"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpDelete, Key: []byte("to-del")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.Responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(result.Responses))
	}
}

// ---------------------------------------------------------------------------
// Raft-backed empty ops (no write proposals needed)
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_ViaRaft_EmptyOps(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 1)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if raft.proposalCount() != 0 {
		t.Errorf("expected 0 proposals for empty ops, got %d", raft.proposalCount())
	}
	if len(result.Responses) != 0 {
		t.Errorf("expected 0 responses, got %d", len(result.Responses))
	}
}

// ---------------------------------------------------------------------------
// Local update of existing key in transaction
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_LocalPutUpdateExisting(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Seed key
	_, _, err := s.Put(ctx, []byte("update-me"), []byte("v1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	ops := []RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("update-me"), Value: []byte("v2")},
	}
	if err := txn.SetSuccess(ops); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if result.Responses[0].Version != 2 {
		t.Errorf("version: got %d, want 2", result.Responses[0].Version)
	}
}

// ---------------------------------------------------------------------------
// Compare with value target
// ---------------------------------------------------------------------------

func TestRaftTransaction_Commit_CompareValue(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Put(ctx, []byte("val-cmp"), []byte("expected"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}

	cmp := &RaftCompare{
		Key:    []byte("val-cmp"),
		Target: RaftCompareValue,
		Result: RaftCompareEqual,
		Value:  []byte("expected"),
	}
	if err := txn.AddCompare(cmp); err != nil {
		t.Fatalf("AddCompare: %v", err)
	}

	if err := txn.SetSuccess([]RaftTxnOp{
		{Type: RaftTxnOpPut, Key: []byte("val-cmp"), Value: []byte("updated")},
	}); err != nil {
		t.Fatalf("SetSuccess: %v", err)
	}

	result, err := txn.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected comparison to succeed")
	}
}
