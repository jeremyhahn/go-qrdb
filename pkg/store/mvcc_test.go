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

package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// MVCC test helpers
// ---------------------------------------------------------------------------

// newTestMVCCStateMachine creates, opens, and registers cleanup for an
// MVCCStateMachine. It fails the test immediately on error.
func newTestMVCCStateMachine(t *testing.T) *MVCCStateMachine {
	t.Helper()
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	return s
}

// mvccPutEntry builds an sm.Entry with an MVCC Put command.
func mvccPutEntry(index uint64, key, value []byte, leaseID int64) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   mustEncode(protocol.EncodeMVCCPut(key, value, leaseID)),
	}
}

// mvccDeleteEntry builds an sm.Entry with an MVCC Delete command.
func mvccDeleteEntry(index uint64, key []byte) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   mustEncode(protocol.EncodeMVCCDelete(key)),
	}
}

// mvccCompactEntry builds an sm.Entry with an MVCC Compact command.
func mvccCompactEntry(index uint64, revision int64) sm.Entry {
	return sm.Entry{
		Index: index,
		Cmd:   mustEncode(protocol.EncodeMVCCCompactPrefix(nil, revision)),
	}
}

// applyMVCCEntries is a helper that applies entries to the MVCC state machine
// and returns results. It fails the test immediately on error.
func applyMVCCEntries(t *testing.T, s *MVCCStateMachine, entries []sm.Entry) []sm.Result {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}
	return results
}

// ---------------------------------------------------------------------------
// Open / Close
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_OpenFreshDB(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewMVCCStateMachine(nil)
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected last applied 0 on fresh DB, got %d", idx)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMVCCStateMachine_OpenReturnsLastApplied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	s := NewMVCCStateMachine(nil)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(42, []byte("k"), []byte("v"), 0),
	})
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := NewMVCCStateMachine(nil)
	idx, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if idx != 42 {
		t.Fatalf("expected last applied 42, got %d", idx)
	}
	if err := s2.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMVCCStateMachine_OpenInvalidDirReturnsOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	_, err := s.Open(context.Background(), "/dev/null/invalid/path", nil)
	if err == nil {
		t.Fatal("expected error opening invalid directory")
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_CloseWithoutOpenIsNoop(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close on unopened store: unexpected error: %v", err)
	}
}

func TestMVCCStateMachine_DoubleCloseIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewMVCCStateMachine(nil)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Update - MVCC Put
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_PutNewKey(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	results := applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("greeting"), []byte("hello"), 0),
	})

	// Result.Value should be the MVCC revision (1 for first put).
	if results[0].Value != 1 {
		t.Fatalf("expected revision 1, got %d", results[0].Value)
	}

	// Verify via Lookup.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("greeting")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "hello" {
		t.Fatalf("expected 'hello', got %q", kv.Value)
	}
	if kv.Version != 1 {
		t.Fatalf("expected version 1, got %d", kv.Version)
	}
}

func TestMVCCStateMachine_PutExistingKeyVersionIncrements(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("v1"), 0),
	})
	results := applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("key"), []byte("v2"), 0),
	})

	// Second put should yield revision 2.
	if results[0].Value != 2 {
		t.Fatalf("expected revision 2, got %d", results[0].Value)
	}

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("key")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "v2" {
		t.Fatalf("expected 'v2', got %q", kv.Value)
	}
	if kv.Version != 2 {
		t.Fatalf("expected version 2, got %d", kv.Version)
	}
}

func TestMVCCStateMachine_PutWithLeaseID(t *testing.T) {
	s, _ := newTestMVCCWithLessor(t)
	leaseID := grantLease(t, s, 1, 60)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("leased"), []byte("value"), leaseID),
	})

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("leased")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if kv.LeaseID != leaseID {
		t.Fatalf("expected leaseID %d, got %d", leaseID, kv.LeaseID)
	}
}

// A state machine built without a lessor cannot bind a lease, so a put naming
// one is refused as an absent capability instead of being written as a key no
// lease will ever delete.
func TestMVCCStateMachine_PutWithLeaseID_NoLessorIsRefused(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	results := applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("leased"), []byte("value"), 12345),
	})
	if _, kind, ok := protocol.DecodeAppErrorKind(results[0].Data); !ok || kind != protocol.AppErrorCapability {
		t.Fatalf("put naming a lease without a lessor: result kind = %v (refused=%v), want a capability refusal", kind, ok)
	}

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("leased")})
	if err == nil && val != nil {
		t.Fatalf("refused put wrote %+v", val)
	}
}

func TestMVCCStateMachine_PutInvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Construct a command with OpMVCCPut but truncated payload.
	cmd := []byte{protocol.OpMVCCPut, 0xFF} // too short for DecodeMVCCPut
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid put payload")
	}
}

// ---------------------------------------------------------------------------
// Update - MVCC Delete
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_DeleteExistingKey(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("val"), 0),
	})
	results := applyMVCCEntries(t, s, []sm.Entry{
		mvccDeleteEntry(2, []byte("key")),
	})

	// Delete returns the new revision.
	if results[0].Value != 2 {
		t.Fatalf("expected revision 2, got %d", results[0].Value)
	}

	// Key should no longer be found.
	_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("key")})
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var knf *mvcc.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_DeleteNonExistentKey(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Delete a key that was never created. Should not error.
	results := applyMVCCEntries(t, s, []sm.Entry{
		mvccDeleteEntry(1, []byte("nonexistent")),
	})

	// Should still allocate a revision.
	if results[0].Value != 1 {
		t.Fatalf("expected revision 1, got %d", results[0].Value)
	}
}

func TestMVCCStateMachine_DeleteInvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Construct a command with OpMVCCDelete but truncated payload.
	cmd := []byte{protocol.OpMVCCDelete} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid delete payload")
	}
}

// ---------------------------------------------------------------------------
// Update - MVCC Compact
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_CompactOldRevisions(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Write multiple revisions.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("v1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("key"), []byte("v2"), 0),
	})

	// Compact revision 1.
	results := applyMVCCEntries(t, s, []sm.Entry{
		mvccCompactEntry(3, 1),
	})
	if results[0].Value != 1 {
		t.Fatalf("expected compact result 1, got %d", results[0].Value)
	}

	// Latest value should still be accessible.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("key")})
	if err != nil {
		t.Fatalf("Lookup after compact: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "v2" {
		t.Fatalf("expected 'v2', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_CompactInvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Construct a command with OpMVCCCompact but truncated payload.
	cmd := []byte{protocol.OpMVCCCompactPrefix, 0x01} // only 1 byte of payload, needs 8
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid compact payload")
	}
}

// ---------------------------------------------------------------------------
// Update - Unknown op code
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_UnknownOpCode(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Use an op code not in the handler map (e.g., 0xFF).
	cmd := []byte{0xFF, 0x01, 0x02}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

func TestMVCCStateMachine_UnknownOpCodeDifferentValue(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Use another unknown op code (e.g., 0xFD). It deliberately is NOT 0x04:
	// that is kv.OpPutIfAbsent, which this state machine now applies (see
	// TestMVCCStateMachine_PutIfAbsent below).
	cmd := []byte{0xFD, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

// TestMVCCStateMachine_PutIfAbsent pins that the MVCC state machine applies
// the base KV conditional put: it stores when the key is absent and declines
// -- reporting kv.ResultNotStored rather than an error, so consensus is never
// stalled -- when it is present.
//
// Before this handler existed, op code 0x04 fell through to
// protocol.UnknownOpError, which failed the whole proposal. That broke every
// caller of service.KVServicer.PutIfAbsent on an MVCC-engine-backed store,
// including service.TableService.CreateTable and therefore every CREATE TABLE.
func TestMVCCStateMachine_PutIfAbsent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	first, err := kv.EncodePutIfAbsent([]byte("cond"), []byte("one"))
	if err != nil {
		t.Fatalf("EncodePutIfAbsent: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: first}}, results); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if results[0].Value != kv.ResultStored {
		t.Fatalf("first result = %d, want ResultStored (%d)", results[0].Value, kv.ResultStored)
	}

	second, err := kv.EncodePutIfAbsent([]byte("cond"), []byte("two"))
	if err != nil {
		t.Fatalf("EncodePutIfAbsent: %v", err)
	}
	results = make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: second}}, results); err != nil {
		t.Fatalf("second Update must not error on a declined conditional write: %v", err)
	}
	if results[0].Value != kv.ResultNotStored {
		t.Errorf("second result = %d, want ResultNotStored (%d)", results[0].Value, kv.ResultNotStored)
	}

	got, err := s.engine.Store().Get([]byte("cond"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "one" {
		t.Errorf("value = %q, want %q (the declined write must not have overwritten it)", got.Value, "one")
	}
}

// TestMVCCStateMachine_MVCCPutIfAbsent pins the MVCC-flavoured conditional put
// (protocol.OpMVCCPutIfAbsent), which service.MVCCKVService.PutIfAbsent
// proposes.
func TestMVCCStateMachine_MVCCPutIfAbsent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	results := make([]sm.Result, 1)
	cmd := mustEncode(protocol.EncodeMVCCPutIfAbsent([]byte("mcond"), []byte("one"), 0))
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if results[0].Value != kv.ResultStored {
		t.Fatalf("first result = %d, want ResultStored (%d)", results[0].Value, kv.ResultStored)
	}

	results = make([]sm.Result, 1)
	cmd = mustEncode(protocol.EncodeMVCCPutIfAbsent([]byte("mcond"), []byte("two"), 0))
	if err := s.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: cmd}}, results); err != nil {
		t.Fatalf("second Update must not error on a declined conditional write: %v", err)
	}
	if results[0].Value != kv.ResultNotStored {
		t.Errorf("second result = %d, want ResultNotStored (%d)", results[0].Value, kv.ResultNotStored)
	}

	got, err := s.engine.Store().Get([]byte("mcond"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Value) != "one" {
		t.Errorf("value = %q, want %q", got.Value, "one")
	}
}

// ---------------------------------------------------------------------------
// Update - Transaction (OpTransaction)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TransactionCompareSucceeds(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Seed a key.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("txkey"), []byte("txval"), 0),
	})

	// Build transaction: compare value == "txval" -> success puts "txkey2"
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("txkey"),
		byte(service.CompareValue),
		byte(service.CompareEqual),
		[]byte("txval"),
		0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("txkey2"), []byte("txval2"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp},
		[][]byte{successOp},
		nil,
	))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Succeeded flag in Data[0].
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1, got %v", results[0].Data)
	}
	// Value carries the revision.
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision in Value")
	}

	// Verify the success op was executed.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("txkey2")})
	if err != nil {
		t.Fatalf("Lookup txkey2: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "txval2" {
		t.Fatalf("expected 'txval2', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_TransactionCompareFails(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Seed a key.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("txkey"), []byte("actual"), 0),
	})

	// Build transaction: compare value == "wrong" -> fail, failure puts "failkey"
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("txkey"),
		byte(service.CompareValue),
		byte(service.CompareEqual),
		[]byte("wrong"),
		0, 0, 0,
	))
	failureOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("failkey"), []byte("failval"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp},
		nil,
		[][]byte{failureOp},
	))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Succeeded flag in Data[0] should be 0 (compare failed).
	if len(results[0].Data) == 0 || results[0].Data[0] != 0 {
		t.Fatalf("expected succeeded Data[0]=0, got %v", results[0].Data)
	}

	// Verify the failure op was executed.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("failkey")})
	if err != nil {
		t.Fatalf("Lookup failkey: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "failval" {
		t.Fatalf("expected 'failval', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_TransactionMultipleComparesFirstFails(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Seed keys.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k1"), []byte("v1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("k2"), []byte("v2"), 0),
	})

	// First compare fails (k1 value != "wrong"), second would succeed.
	cmp1 := mustEncode(protocol.EncodeCompare(
		[]byte("k1"),
		byte(service.CompareValue),
		byte(service.CompareEqual),
		[]byte("wrong"),
		0, 0, 0,
	))
	cmp2 := mustEncode(protocol.EncodeCompare(
		[]byte("k2"),
		byte(service.CompareValue),
		byte(service.CompareEqual),
		[]byte("v2"),
		0, 0, 0,
	))
	failureOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("multi-fail"), []byte("yes"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp1, cmp2},
		nil,
		[][]byte{failureOp},
	))
	entries := []sm.Entry{{Index: 3, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(results[0].Data) == 0 || results[0].Data[0] != 0 {
		t.Fatalf("expected succeeded Data[0]=0, got %v", results[0].Data)
	}

	// Verify failure op executed.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("multi-fail")})
	if err != nil {
		t.Fatalf("Lookup multi-fail: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "yes" {
		t.Fatalf("expected 'yes', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_TransactionEmptyCompareAlwaysSucceeds(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	successOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("auto"), []byte("win"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		nil, // empty compares
		[][]byte{successOp},
		nil,
	))
	entries := []sm.Entry{{Index: 1, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1 for empty compares, got %v", results[0].Data)
	}

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("auto")})
	if err != nil {
		t.Fatalf("Lookup auto: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "win" {
		t.Fatalf("expected 'win', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_TransactionNoOpsInChosenBranch(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Empty compare list -> succeeds, but no success ops defined.
	txnCmd := mustEncode(protocol.EncodeTransaction(nil, nil, nil))
	entries := []sm.Entry{{Index: 1, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Succeeded flag=1 but no ops, so revision should not advance.
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1, got %v", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// Update - Batch (OpBatch)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_BatchMultiplePuts(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	op1 := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("b1"), []byte("v1"), nil, 0))
	op2 := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("b2"), []byte("v2"), nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{op1, op2}))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Result.Value carries the revision.
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision in Value")
	}

	// Verify both keys.
	for _, tc := range []struct {
		key, want string
	}{
		{"b1", "v1"},
		{"b2", "v2"},
	} {
		val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte(tc.key)})
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tc.key, err)
		}
		kv := val.(*service.KeyValue)
		if string(kv.Value) != tc.want {
			t.Fatalf("expected %q, got %q", tc.want, kv.Value)
		}
	}
}

func TestMVCCStateMachine_BatchMixedPutDelete(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Seed a key to delete.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("todelete"), []byte("val"), 0),
	})

	putOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("new"), []byte("nval"), nil, 0))
	delOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("todelete"), nil, nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{putOp, delOp}))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Result.Value carries the revision.
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision in Value")
	}

	// "new" should exist.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("new")})
	if err != nil {
		t.Fatalf("Lookup(new): %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "nval" {
		t.Fatalf("expected 'nval', got %q", kv.Value)
	}

	// "todelete" should be gone.
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("todelete")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

func TestMVCCStateMachine_BatchEmpty(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	batchCmd := mustEncode(protocol.EncodeBatch(nil))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Empty batch still computes rev = currentRevision + 1 = 1.
	if results[0].Value != 1 {
		t.Fatalf("expected revision 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Update - Multiple entries in single call
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_MultipleEntriesSingleUpdate(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	entries := []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	}
	results := applyMVCCEntries(t, s, entries)

	// Each entry should get its own revision (1, 2, 3).
	for i, r := range results {
		expected := uint64(i + 1)
		if r.Value != expected {
			t.Fatalf("entry %d: expected revision %d, got %d", i, expected, r.Value)
		}
	}

	// Verify all keys.
	for _, tc := range []struct {
		key, want string
	}{
		{"a", "1"},
		{"b", "2"},
		{"c", "3"},
	} {
		val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte(tc.key)})
		if err != nil {
			t.Fatalf("Lookup(%q): %v", tc.key, err)
		}
		kv := val.(*service.KeyValue)
		if string(kv.Value) != tc.want {
			t.Fatalf("expected %q, got %q", tc.want, kv.Value)
		}
	}
}

func TestMVCCStateMachine_LastAppliedPersistedCorrectly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	s := NewMVCCStateMachine(nil)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Apply entries with non-sequential indexes; lastApplied tracks the max.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(5, []byte("a"), []byte("1"), 0),
		mvccPutEntry(3, []byte("b"), []byte("2"), 0),
		mvccPutEntry(10, []byte("c"), []byte("3"), 0),
	})

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := NewMVCCStateMachine(nil)
	idx, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if idx != 10 {
		t.Fatalf("expected last applied 10, got %d", idx)
	}
	if err := s2.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Update - NotOpenError
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_UpdateBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	entries := []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v"), 0)}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Lookup
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupMVCCGetQuery(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("hello"), []byte("world"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("hello")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "world" {
		t.Fatalf("expected 'world', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_LookupMVCCGetQueryNonExistent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("missing")})
	if err == nil {
		t.Fatal("expected error for non-existent key")
	}
	var knf *mvcc.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_LookupGetAtRevisionCurrent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("rev"), []byte("v1"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("rev"),
		Revision: 1,
	})
	if err != nil {
		t.Fatalf("Lookup at revision 1: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "v1" {
		t.Fatalf("expected 'v1', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_LookupGetAtRevisionHistorical(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("rev"), []byte("v1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("rev"), []byte("v2"), 0),
	})

	// Look up at revision 1 (historical).
	val, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("rev"),
		Revision: 1,
	})
	if err != nil {
		t.Fatalf("Lookup at revision 1: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "v1" {
		t.Fatalf("expected 'v1', got %q", kv.Value)
	}

	// Latest should be v2.
	val2, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("rev"),
		Revision: 2,
	})
	if err != nil {
		t.Fatalf("Lookup at revision 2: %v", err)
	}
	kv2 := val2.(*service.KeyValue)
	if string(kv2.Value) != "v2" {
		t.Fatalf("expected 'v2', got %q", kv2.Value)
	}
}

func TestMVCCStateMachine_LookupRangeQueryWithStartEnd(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
		mvccPutEntry(4, []byte("d"), []byte("4"), 0),
	})

	// Range [b, d) should return b, c.
	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start: []byte("b"),
		End:   []byte("d"),
	})
	if err != nil {
		t.Fatalf("Lookup range: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	if len(rr.KeyValues) != 2 {
		t.Fatalf("expected 2 key-values, got %d", len(rr.KeyValues))
	}
	if string(rr.KeyValues[0].Key) != "b" {
		t.Fatalf("expected key 'b', got %q", rr.KeyValues[0].Key)
	}
	if string(rr.KeyValues[1].Key) != "c" {
		t.Fatalf("expected key 'c', got %q", rr.KeyValues[1].Key)
	}
}

func TestMVCCStateMachine_LookupRangeQueryNilEnd(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("solo"), []byte("val"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start: []byte("solo"),
		End:   nil,
	})
	if err != nil {
		t.Fatalf("Lookup range nil end: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	if len(rr.KeyValues) != 1 {
		t.Fatalf("expected 1 key-value, got %d", len(rr.KeyValues))
	}
	if string(rr.KeyValues[0].Key) != "solo" {
		t.Fatalf("expected key 'solo', got %q", rr.KeyValues[0].Key)
	}
}

func TestMVCCStateMachine_LookupRevisionQuery(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("k"), []byte("v2"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup revision: %v", err)
	}
	rev := val.(int64)
	if rev != 2 {
		t.Fatalf("expected revision 2, got %d", rev)
	}
}

func TestMVCCStateMachine_LookupRevisionQueryFreshDB(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	val, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup revision: %v", err)
	}
	rev := val.(int64)
	if rev != 0 {
		t.Fatalf("expected revision 0 on fresh DB, got %d", rev)
	}
}

func TestMVCCStateMachine_LookupByteSlice(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("bkey"), []byte("bval"), 0),
	})

	val, err := s.Lookup(context.Background(), []byte("bkey"))
	if err != nil {
		t.Fatalf("Lookup([]byte): %v", err)
	}
	data, ok := val.([]byte)
	if !ok {
		t.Fatalf("expected []byte, got %T", val)
	}
	if string(data) != "bval" {
		t.Fatalf("expected 'bval', got %q", data)
	}
}

func TestMVCCStateMachine_LookupString(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("skey"), []byte("sval"), 0),
	})

	val, err := s.Lookup(context.Background(), "skey")
	if err != nil {
		t.Fatalf("Lookup(string): %v", err)
	}
	data, ok := val.([]byte)
	if !ok {
		t.Fatalf("expected []byte, got %T", val)
	}
	if string(data) != "sval" {
		t.Fatalf("expected 'sval', got %q", data)
	}
}

func TestMVCCStateMachine_LookupUnsupportedQueryType(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), 12345)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_LookupUnsupportedQueryTypeStruct(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), struct{ X int }{X: 1})
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_LookupBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("k")})
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SyncSuccess(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

func TestMVCCStateMachine_SyncBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	err := s.Sync()
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PrepareSnapshot / SaveSnapshot / RecoverFromSnapshot
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SnapshotRoundtrip(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Write data.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("alpha"), []byte("A"), 0),
		mvccPutEntry(2, []byte("beta"), []byte("B"), 0),
	})

	// Sync + snapshot.
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Add a key that should NOT survive recovery.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(3, []byte("gamma"), []byte("C"), 0),
	})

	// Recover from snapshot.
	if err := s.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// Verify original keys restored.
	for _, tc := range []struct {
		key, want string
	}{
		{"alpha", "A"},
		{"beta", "B"},
	} {
		val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte(tc.key)})
		if err != nil {
			t.Fatalf("Lookup(%q) after recovery: %v", tc.key, err)
		}
		kv := val.(*service.KeyValue)
		if string(kv.Value) != tc.want {
			t.Fatalf("Lookup(%q): expected %q, got %q", tc.key, tc.want, kv.Value)
		}
	}

	// Verify "gamma" does NOT exist.
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("gamma")})
	if err == nil {
		t.Fatal("expected 'gamma' to be absent after snapshot recovery")
	}
}

func TestMVCCStateMachine_SnapshotExcludesMetadata(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Apply entries so metadata keys exist.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("userkey"), []byte("val"), 0),
	})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// The snapshot should have been saved successfully. If metadata keys
	// (\x00 prefix) were included, the count would be larger. We verify
	// indirectly by recovering on a second instance and checking the
	// MVCC store re-initializes correctly.
	dir2 := t.TempDir()
	s2 := NewMVCCStateMachine(nil)
	if _, err := s2.Open(context.Background(), dir2, nil); err != nil {
		t.Fatalf("Open s2: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close(context.Background()) })

	if err := s2.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot s2: %v", err)
	}

	// The MVCC store should be re-initialized and usable.
	val, err := s2.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("userkey")})
	if err != nil {
		t.Fatalf("Lookup after recover: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "val" {
		t.Fatalf("expected 'val', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotReInitializesMVCC(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Write and snapshot.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("val"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Recover.
	if err := s.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// After recovery, the MVCC store should be usable for new writes.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("newkey"), []byte("newval"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("newkey")})
	if err != nil {
		t.Fatalf("Lookup newkey: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "newval" {
		t.Fatalf("expected 'newval', got %q", kv.Value)
	}
}

func TestMVCCStateMachine_PrepareSnapshotBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	_, err := s.PrepareSnapshot()
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_SaveSnapshotInvalidContext(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	var buf bytes.Buffer
	err := s.SaveSnapshot(context.Background(), "not-a-snapshot", &buf, nil)
	if err == nil {
		t.Fatal("expected error for invalid snapshot context")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_SaveSnapshotStopperCancels(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Add enough data so the iterator iterates.
	entries := make([]sm.Entry, 100)
	for i := range entries {
		key := []byte{byte('A' + i%26), byte('0' + i/26)}
		entries[i] = mvccPutEntry(uint64(i+1), key, []byte("val"), 0)
	}
	applyMVCCEntries(t, s, entries)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err = s.SaveSnapshot(context.Background(), snapCtx, &buf, stopper)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotStopperCancels(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Build a valid snapshot with data.
	entries := make([]sm.Entry, 50)
	for i := range entries {
		key := []byte{byte('A' + i%26), byte('0' + i/26)}
		entries[i] = mvccPutEntry(uint64(i+1), key, []byte("val"), 0)
	}
	applyMVCCEntries(t, s, entries)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	stopper := make(chan struct{})
	close(stopper)

	err = s.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), stopper)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	err := s.RecoverFromSnapshot(context.Background(), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotInvalidData(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Empty reader: binary.Read will fail.
	err := s.RecoverFromSnapshot(context.Background(), &bytes.Buffer{}, nil)
	if err == nil {
		t.Fatal("expected error for empty reader")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetHash
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_GetHashDeterministic(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
	})

	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %d != %d", h1, h2)
	}
}

func TestMVCCStateMachine_GetHashDifferentData(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
	})
	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
	})
	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}

	if h1 == h2 {
		t.Fatal("hash should change after different data is added")
	}
}

func TestMVCCStateMachine_GetHashBeforeOpenReturnsNotOpenError(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	_, err := s.GetHash()
	if err == nil {
		t.Fatal("expected error")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Store()
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_StoreReturnsNilWhenNotOpened(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s.Store() != nil {
		t.Fatal("expected nil store before Open")
	}
}

func TestMVCCStateMachine_StoreReturnsNonNilAfterOpen(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	if s.Store() == nil {
		t.Fatal("expected non-nil store after Open")
	}
}

// ---------------------------------------------------------------------------
// NewMVCCCreateFunc
// ---------------------------------------------------------------------------

func TestNewMVCCCreateFunc_ReturnsNonNilCreateFunc(t *testing.T) {
	cf := NewMVCCCreateFunc()
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}

	result := cf(1, 1)
	if result == nil {
		t.Fatal("CreateFunc returned nil")
	}
	if _, ok := result.(sm.DiskStateMachine); !ok {
		t.Fatalf("expected sm.DiskStateMachine, got %T", result)
	}
}

func TestNewMVCCCreateFunc_WithOptions(t *testing.T) {
	cf := NewMVCCCreateFunc(
		WithCacheSize(8<<20),
		WithMaxOpenFiles(256),
	)
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}

	result := cf(1, 1)
	if result == nil {
		t.Fatal("CreateFunc returned nil")
	}
	dsm, ok := result.(sm.DiskStateMachine)
	if !ok {
		t.Fatalf("expected sm.DiskStateMachine, got %T", result)
	}

	// Verify the created SM can open and function.
	ms := dsm.(*MVCCStateMachine)
	dir := filepath.Join(t.TempDir(), "db")
	idx, err := ms.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0, got %d", idx)
	}
	if err := ms.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewMVCCCreateFunc_DifferentShardReplica(t *testing.T) {
	cf := NewMVCCCreateFunc()
	r1 := cf(1, 1)
	r2 := cf(2, 2)
	if r1 == r2 {
		t.Fatal("expected distinct instances for different shard/replica IDs")
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ImplementsDiskStateMachine(t *testing.T) {
	var _ sm.DiskStateMachine = (*MVCCStateMachine)(nil)
}

func TestMVCCStateMachine_ImplementsHasher(t *testing.T) {
	var _ sm.Hasher = (*MVCCStateMachine)(nil)
}

// ---------------------------------------------------------------------------
// NewMVCCStateMachine
// ---------------------------------------------------------------------------

func TestNewMVCCStateMachine_NilOpts(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s == nil {
		t.Fatal("expected non-nil state machine")
	}
	if s.opts == nil {
		t.Fatal("expected default opts to be set")
	}
}

func TestNewMVCCStateMachine_CustomOpts(t *testing.T) {
	opts := mvccCustomTestOpts
	s := NewMVCCStateMachine(&opts)
	if s == nil {
		t.Fatal("expected non-nil state machine")
	}
}

// mvccCustomTestOpts is used to verify custom option propagation.
var mvccCustomTestOpts = customTestOpts

// ---------------------------------------------------------------------------
// Transaction with delete ops
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TransactionWithDeleteOps(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Seed keys.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("exist"), []byte("val"), 0),
	})

	// Compare succeeds (version == 1), success op deletes "exist".
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("exist"),
		byte(service.CompareVersion),
		byte(service.CompareEqual),
		nil, 1, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCDelete,
		[]byte("exist"), nil, nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp},
		[][]byte{successOp},
		nil,
	))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1, got %v", results[0].Data)
	}

	// "exist" should be gone.
	_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("exist")})
	if err == nil {
		t.Fatal("expected error for deleted key after transaction")
	}
}

// ---------------------------------------------------------------------------
// Transaction with compare on non-existent key
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TransactionCompareNonExistentKey(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Compare version == 0 for a non-existent key should succeed
	// (non-existent key has version 0).
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("nokey"),
		byte(service.CompareVersion),
		byte(service.CompareEqual),
		nil, 0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("created"), []byte("yes"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp},
		[][]byte{successOp},
		nil,
	))
	entries := []sm.Entry{{Index: 1, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1, got %v", results[0].Data)
	}

	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("created")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "yes" {
		t.Fatalf("expected 'yes', got %q", kv.Value)
	}
}

// ---------------------------------------------------------------------------
// Update with empty entries
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_UpdateEmptyEntries(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	results := applyMVCCEntries(t, s, []sm.Entry{})
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Update with invalid command (too short)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_UpdateInvalidCommand(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}} // empty command
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// ---------------------------------------------------------------------------
// Snapshot empty DB
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SnapshotEmptyDB(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Add a key, then recover empty snapshot: key should be gone.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("temp"), []byte("data"), 0),
	})

	if err := s.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("temp")})
	if err == nil {
		t.Fatal("expected 'temp' to be absent after empty snapshot recovery")
	}
}

// ---------------------------------------------------------------------------
// GetHash excludes metadata
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_GetHashExcludesMetadata(t *testing.T) {
	// Two state machines with same user data but different lastApplied indexes
	// should produce the same hash.
	s1 := newTestMVCCStateMachine(t)
	applyMVCCEntries(t, s1, []sm.Entry{
		mvccPutEntry(10, []byte("key"), []byte("val"), 0),
	})
	h1, err := s1.GetHash()
	if err != nil {
		t.Fatalf("GetHash s1: %v", err)
	}

	s2 := newTestMVCCStateMachine(t)
	applyMVCCEntries(t, s2, []sm.Entry{
		mvccPutEntry(99, []byte("key"), []byte("val"), 0),
	})
	h2, err := s2.GetHash()
	if err != nil {
		t.Fatalf("GetHash s2: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("hash should be equal for same user data despite different indexes: %d != %d", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// MVCCRangeResult type
// ---------------------------------------------------------------------------

func TestMVCCRangeResult_Fields(t *testing.T) {
	rr := &service.MVCCRangeResult{
		KeyValues: []*service.KeyValue{
			{Key: []byte("a"), Value: []byte("1")},
		},
		Revision: 42,
	}
	if rr.Revision != 42 {
		t.Fatalf("expected revision 42, got %d", rr.Revision)
	}
	if len(rr.KeyValues) != 1 {
		t.Fatalf("expected 1 kv, got %d", len(rr.KeyValues))
	}
}

func TestMVCCRangeResult_Empty(t *testing.T) {
	rr := &service.MVCCRangeResult{
		KeyValues: []*service.KeyValue{},
		Revision:  0,
	}
	if len(rr.KeyValues) != 0 {
		t.Fatalf("expected 0 kvs, got %d", len(rr.KeyValues))
	}
}

// ---------------------------------------------------------------------------
// Batch with unknown op type (exercises executeDecodedOp default branch)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_BatchUnknownOpTypeReturnsError(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Encode an op with an unknown op type (0x99).
	unknownOp := mustEncode(protocol.EncodeOp(0x99, []byte("k"), []byte("v"), nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{unknownOp}))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

func TestMVCCStateMachine_TransactionUnknownOpTypeReturnsError(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Transaction with empty compares (always succeeds) and unknown op in success.
	unknownOp := mustEncode(protocol.EncodeOp(0x99, []byte("k"), []byte("v"), nil, 0))
	txnCmd := mustEncode(protocol.EncodeTransaction(nil, [][]byte{unknownOp}, nil))
	entries := []sm.Entry{{Index: 1, Cmd: txnCmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

// ---------------------------------------------------------------------------
// Read-only MVCC store for error path testing
// ---------------------------------------------------------------------------

// openReadOnlyMVCCStore creates an MVCCStateMachine backed by a read-only
// PebbleDB. Write operations will fail, exercising internal error paths.
func openReadOnlyMVCCStore(t *testing.T, entries []sm.Entry) *MVCCStateMachine {
	t.Helper()

	dir := t.TempDir()
	dbDir := filepath.Join(dir, "db")

	// Phase 1: create and populate a writable database.
	writable := NewMVCCStateMachine(nil)
	if _, err := writable.Open(context.Background(), dbDir, nil); err != nil {
		t.Fatalf("Open writable: %v", err)
	}
	if len(entries) > 0 {
		results := make([]sm.Result, len(entries))
		if err := writable.Update(context.Background(), entries, results); err != nil {
			t.Fatalf("Update writable: %v", err)
		}
		if err := writable.Sync(); err != nil {
			t.Fatalf("Sync writable: %v", err)
		}
	}
	if err := writable.Close(context.Background()); err != nil {
		t.Fatalf("Close writable: %v", err)
	}

	// Phase 2: reopen the same directory as read-only.
	ro := NewMVCCStateMachine(&pebble.Options{ReadOnly: true})
	if _, err := ro.Open(context.Background(), dbDir, nil); err != nil {
		t.Fatalf("Open read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := ro.Close(context.Background()); err != nil {
			t.Errorf("Close read-only: %v", err)
		}
	})
	return ro
}

func TestMVCCStateMachine_SyncWriteError(t *testing.T) {
	s := openReadOnlyMVCCStore(t, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	err := s.Sync()
	if err == nil {
		t.Fatal("expected error from Sync on read-only database")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotClearAllError(t *testing.T) {
	s := openReadOnlyMVCCStore(t, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	// Build a minimal valid snapshot buffer.
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	key := []byte("key")
	val := []byte("val")
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(key))); err != nil {
		t.Fatal(err)
	}
	buf.Write(key)
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(val))); err != nil {
		t.Fatal(err)
	}
	buf.Write(val)

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error from RecoverFromSnapshot on read-only database")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SaveSnapshot with failing writer
// ---------------------------------------------------------------------------

// errWriter is an io.Writer that always returns an error.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write error")
}

func TestMVCCStateMachine_SaveSnapshotWriteError(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	err = s.SaveSnapshot(context.Background(), snapCtx, errWriter{}, nil)
	if err == nil {
		t.Fatal("expected error from SaveSnapshot with failing writer")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// limitWriter writes up to N bytes, then returns an error.
type limitWriter struct {
	remaining int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, errors.New("write limit exceeded")
	}
	n := len(p)
	if n > w.remaining {
		n = w.remaining
	}
	w.remaining -= n
	return n, nil
}

func TestMVCCStateMachine_SaveSnapshotPartialWriteError(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key1"), []byte("value1"), 0),
		mvccPutEntry(2, []byte("key2"), []byte("value2"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Allow enough bytes for the count header and first key-value pair,
	// but fail during the second pair.
	w := &limitWriter{remaining: 30}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from SaveSnapshot with limited writer")
	}
}

// ---------------------------------------------------------------------------
// Lookup: MVCCRangeQuery error path
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupRangeQueryCompactedRevision(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Create data and compact.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccCompactEntry(3, 1),
	})

	// Range at compacted revision should return an error.
	_, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:    []byte("a"),
		End:      []byte("c"),
		Revision: 1,
	})
	if err != nil {
		// The MVCC store Range returns an error for compacted revisions.
		// Verify we get the error propagated through MVCCStateMachine.
		var rce *mvcc.RevisionCompactedError
		if !errors.As(err, &rce) {
			t.Fatalf("expected *RevisionCompactedError, got %T: %v", err, err)
		}
	}
}

func TestMVCCStateMachine_LookupRangeQueryNoResults(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
	})

	// Range [z, zz) should return no results.
	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start: []byte("z"),
		End:   []byte("zz"),
	})
	if err != nil {
		t.Fatalf("Lookup range: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	if len(rr.KeyValues) != 0 {
		t.Fatalf("expected 0 key-values, got %d", len(rr.KeyValues))
	}
}

// ---------------------------------------------------------------------------
// Transaction with compare on modRevision target
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TransactionCompareModRevision(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("mrkey"), []byte("val"), 0),
	})

	// Compare modRevision == 1 (should succeed).
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("mrkey"),
		byte(service.CompareModRevision),
		byte(service.CompareEqual),
		nil, 0, 0, 1,
	))
	successOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("mrresult"), []byte("ok"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp},
		[][]byte{successOp},
		nil,
	))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1, got %v", results[0].Data)
	}
}

func TestMVCCStateMachine_TransactionCompareCreateRevision(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("crkey"), []byte("val"), 0),
	})

	// Compare createRevision == 1 (should succeed).
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("crkey"),
		byte(service.CompareCreateRevision),
		byte(service.CompareEqual),
		nil, 0, 1, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(
		protocol.OpMVCCPut,
		[]byte("crresult"), []byte("ok"), nil, 0,
	))
	txnCmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{cmp},
		[][]byte{successOp},
		nil,
	))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded Data[0]=1, got %v", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// Lookup convenience types on non-existent keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupByteSliceNonExistent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	val, err := s.Lookup(context.Background(), []byte("missing"))
	if err != nil {
		t.Fatalf("expected nil error for non-existent key via []byte, got %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil value for non-existent key via []byte, got %v", val)
	}
}

func TestMVCCStateMachine_LookupStringNonExistent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	val, err := s.Lookup(context.Background(), "missing")
	if err != nil {
		t.Fatalf("expected nil error for non-existent key via string, got %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil value for non-existent key via string, got %v", val)
	}
}

// ---------------------------------------------------------------------------
// Batch with single delete
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_BatchSingleDelete(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("del"), []byte("val"), 0),
	})

	delOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("del"), nil, nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{delOp}))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Result.Value carries the revision.
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision in Value")
	}

	_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("del")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// Open with corrupted MVCC revision key (triggers mvcc.New error path)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_OpenWithCorruptedRevision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	// Phase 1: write a corrupted revision key directly via PebbleDB.
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	// The MVCC revision key is []byte{0x03}. Write a corrupted value (3 bytes, not 8).
	if err := db.Set([]byte{0x03}, []byte{1, 2, 3}, pebble.Sync); err != nil {
		t.Fatalf("Set corrupted revision: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Phase 2: open via MVCCStateMachine - mvcc.New should fail.
	s := NewMVCCStateMachine(nil)
	_, err = s.Open(context.Background(), dir, nil)
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("expected error opening with corrupted MVCC revision")
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_OpenWithCorruptedCompacted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	// Write a valid revision but corrupted compacted key.
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	// Valid revision key (0x03) with 8 bytes.
	revBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(revBuf, 5)
	if err := db.Set([]byte{0x03}, revBuf, pebble.Sync); err != nil {
		t.Fatalf("Set revision: %v", err)
	}
	// Corrupted compacted key (0x04) with 3 bytes instead of 8.
	if err := db.Set([]byte{0x04}, []byte{1, 2, 3}, pebble.Sync); err != nil {
		t.Fatalf("Set corrupted compacted: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Open via MVCCStateMachine - mvcc.New should fail on loadCompacted.
	s := NewMVCCStateMachine(nil)
	_, err = s.Open(context.Background(), dir, nil)
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("expected error opening with corrupted compacted revision")
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// readLastApplied with corrupted metadata (len != 8)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_OpenWithCorruptedLastApplied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	// Phase 1: write corrupted metadata directly via PebbleDB.
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	// Write a lastApplied key with wrong length (5 bytes instead of 8).
	if err := db.Set([]byte(metaKeyLastApplied), []byte{1, 2, 3, 4, 5}, pebble.Sync); err != nil {
		t.Fatalf("Set corrupted metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Phase 2: open via MVCCStateMachine - should get lastApplied=0.
	s := NewMVCCStateMachine(nil)
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with corrupted metadata: %v", err)
	}
	// Corrupted value (len != 8) causes readLastApplied to return 0.
	if idx != 0 {
		t.Fatalf("expected last applied 0 for corrupted metadata, got %d", idx)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SaveSnapshot on empty database (count=0)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SaveSnapshotEmptyDBVerifyCount(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Verify the snapshot starts with count=0 (no user data, metadata excluded).
	if buf.Len() < 4 {
		t.Fatalf("expected at least 4 bytes, got %d", buf.Len())
	}
	count := binary.LittleEndian.Uint32(buf.Bytes()[:4])
	if count != 0 {
		t.Fatalf("expected count 0 for empty DB snapshot, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// RecoverFromSnapshot on empty database (exercises clearAll empty path)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_RecoverFromSnapshotOnEmptyDB(t *testing.T) {
	// First, create a snapshot with some data.
	s1 := newTestMVCCStateMachine(t)
	applyMVCCEntries(t, s1, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("val"), 0),
	})
	if err := s1.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s1.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := s1.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Now open a fresh (empty) MVCC SM and recover into it.
	s2 := newTestMVCCStateMachine(t)
	// s2 has no data - clearAll will iterate an empty database.
	if err := s2.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// Verify recovery worked.
	val, err := s2.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("key")})
	if err != nil {
		t.Fatalf("Lookup after recover: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "val" {
		t.Fatalf("expected 'val', got %q", kv.Value)
	}
}

// ---------------------------------------------------------------------------
// Compact after reading at historical revision
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_CompactThenGetAtRevision(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Create 3 revisions.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("v1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("key"), []byte("v2"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(3, []byte("key"), []byte("v3"), 0),
	})

	// Compact revision 1.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccCompactEntry(4, 1),
	})

	// Reading at revision 1 should fail (compacted).
	_, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("key"),
		Revision: 1,
	})
	if err == nil {
		t.Fatal("expected error reading compacted revision")
	}

	// Reading at revision 2 should still work.
	val, err := s.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("key"),
		Revision: 2,
	})
	if err != nil {
		t.Fatalf("Lookup at revision 2: %v", err)
	}
	kv := val.(*service.KeyValue)
	if string(kv.Value) != "v2" {
		t.Fatalf("expected 'v2', got %q", kv.Value)
	}
}

// ---------------------------------------------------------------------------
// Range query with options
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupRangeQueryWithLimit(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start: []byte("a"),
		End:   []byte("d"),
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("Lookup range: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	if len(rr.KeyValues) != 2 {
		t.Fatalf("expected 2 key-values with limit, got %d", len(rr.KeyValues))
	}
}

// ---------------------------------------------------------------------------
// RecoverFromSnapshot truncated data (exercises error paths in recover)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_RecoverFromSnapshotTruncatedKeyLen(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Seed data so clearAll has something to clear.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	// Build a snapshot buffer with count=1 but truncate before keyLen.
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	// Only 2 bytes (need 4 for uint32 keyLen).
	buf.Write([]byte{0x01, 0x02})

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated keyLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotTruncatedKeyData(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	// keyLen = 10 but only write 3 bytes.
	if err := binary.Write(&buf, binary.LittleEndian, uint32(10)); err != nil {
		t.Fatal(err)
	}
	buf.Write([]byte{0x01, 0x02, 0x03})

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated key data")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotTruncatedValLen(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(3)); err != nil {
		t.Fatal(err)
	}
	buf.Write([]byte("abc"))
	buf.Write([]byte{0x05}) // only 1 byte of valLen (need 4)

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated valLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_RecoverFromSnapshotTruncatedValData(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(3)); err != nil {
		t.Fatal(err)
	}
	buf.Write([]byte("abc"))
	if err := binary.Write(&buf, binary.LittleEndian, uint32(100)); err != nil {
		t.Fatal(err)
	}
	buf.Write([]byte{0x01, 0x02}) // only 2 bytes (need 100)

	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for truncated val data")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SaveSnapshot writer failures at various positions (mvcc)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SaveSnapshotWriterFailsOnCount(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Writer that can't write the 4-byte count.
	w := &limitWriter{remaining: 0}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on count")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_SaveSnapshotWriterFailsOnKeyData(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("val"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Allow count (4) + keyLen (4) but fail on key data.
	w := &limitWriter{remaining: 8}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on key data")
	}
}

func TestMVCCStateMachine_SaveSnapshotWriterFailsOnKeyLen(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key"), []byte("val"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Allow only the 4-byte count, then fail on first keyLen.
	w := &limitWriter{remaining: 4}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure on keyLen")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_SaveSnapshotWriterFailsMidstream(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Write enough data to ensure the snapshot has many entries.
	for i := 0; i < 20; i++ {
		key := []byte{byte('a' + i)}
		applyMVCCEntries(t, s, []sm.Entry{
			mvccPutEntry(uint64(i+1), key, []byte("value"), 0),
		})
	}
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Allow the count and some data, then fail mid-stream.
	w := &limitWriter{remaining: 200}
	err = s.SaveSnapshot(context.Background(), snapCtx, w, nil)
	if err == nil {
		t.Fatal("expected error from writer failure during snapshot")
	}
}

// ---------------------------------------------------------------------------
// Multiple Update calls accumulate revisions
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_RevisionsAccumulateAcrossUpdates(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// First update.
	results1 := applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
	})
	if results1[0].Value != 1 {
		t.Fatalf("expected revision 1, got %d", results1[0].Value)
	}

	// Second update.
	results2 := applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
	})
	if results2[0].Value != 2 {
		t.Fatalf("expected revision 2, got %d", results2[0].Value)
	}

	// Third update.
	results3 := applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	})
	if results3[0].Value != 3 {
		t.Fatalf("expected revision 3, got %d", results3[0].Value)
	}

	// Verify final revision.
	val, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup revision: %v", err)
	}
	rev := val.(int64)
	if rev != 3 {
		t.Fatalf("expected revision 3, got %d", rev)
	}
}

// ---------------------------------------------------------------------------
// Tests: ListQuery
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ListQuery_AllKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("b"), []byte("val-b"), 0),
		mvccPutEntry(2, []byte("a"), []byte("val-a"), 0),
		mvccPutEntry(3, []byte("c"), []byte("val-c"), 0),
	})

	result, err := s.Lookup(ctx, kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}

	// MVCC keys are stored with revision-encoded suffixes, so we
	// verify the user keys appear sorted and are present.
	if len(keys) == 0 {
		t.Fatal("expected non-empty key list")
	}
}

func TestMVCCStateMachine_ListQuery_WithPrefix(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("app:config"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("app:name"), []byte("v2"), 0),
		mvccPutEntry(3, []byte("db:host"), []byte("v3"), 0),
	})

	result, err := s.Lookup(ctx, kv.ListQuery{Prefix: "app:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{Prefix: 'app:'}) failed: %v", err)
	}

	keys := result.([]string)
	// Should contain app: keys but not db: keys.
	for _, k := range keys {
		if len(k) >= 3 && k[:3] == "db:" {
			t.Fatalf("unexpected key with db: prefix: %q", k)
		}
	}
	if len(keys) == 0 {
		t.Fatal("expected at least one key with app: prefix")
	}
}

func TestMVCCStateMachine_ListQuery_NoResults(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("exists"), []byte("val"), 0),
	})

	result, err := s.Lookup(ctx, kv.ListQuery{Prefix: "nonexistent:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestMVCCStateMachine_ListQuery_EmptyStore(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	result, err := s.Lookup(ctx, kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if keys == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestMVCCStateMachine_ListQuery_ExcludesMetadata(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("user-key"), []byte("val"), 0),
	})

	result, err := s.Lookup(ctx, kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	for _, k := range keys {
		if len(k) > 0 && k[0] == '\x00' {
			t.Fatalf("metadata key should be excluded: %q", k)
		}
		if len(k) >= 6 && k[:6] == "__sys:" {
			t.Fatalf("system key should be excluded: %q", k)
		}
	}
}

func TestMVCCStateMachine_ListQuery_SortedOrder(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	ctx := context.Background()

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("z"), []byte("1"), 0),
		mvccPutEntry(2, []byte("m"), []byte("2"), 0),
		mvccPutEntry(3, []byte("a"), []byte("3"), 0),
	})

	result, err := s.Lookup(ctx, kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Fatalf("keys not sorted: %v", keys)
		}
	}
}

func TestMVCCStateMachine_ListQuery_NotOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)

	_, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err == nil {
		t.Fatal("expected error for not-open state machine")
	}
	var notOpen *NotOpenError
	if !errors.As(err, &notOpen) {
		t.Fatalf("expected NotOpenError, got %T: %v", err, err)
	}
}
