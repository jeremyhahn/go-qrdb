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

package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// guardedTxnEntry builds a transaction that compares one key against one value
// and writes a different key on each branch, so which branch ran is readable
// from the store afterwards.
func guardedTxnEntry(idx uint64, guardKey, guardValue, successKey, failureKey string) sm.Entry {
	compares := [][]byte{
		protocol.EncodeCompare(
			[]byte(guardKey),
			byte(service.CompareValue),
			byte(service.CompareEqual),
			[]byte(guardValue),
			0, 0, 0,
		),
	}
	successOps := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCPut, []byte(successKey), []byte("ran"), nil, 0),
	}
	failureOps := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCPut, []byte(failureKey), []byte("ran"), nil, 0),
	}
	return sm.Entry{Index: idx, Cmd: protocol.EncodeTransaction(compares, successOps, failureOps)}
}

// A transaction whose COMPARE READ fails must not pick a branch.
//
// The read error used to be discarded, so a storage fault and an absent key
// both reached mvcc.Evaluate as a nil value and the branch was decided on that
// nil. Because such a fault is LOCAL to one replica, replicas that read the
// guard successfully run SuccessOps while the faulting replica runs FailureOps
// — from an identical log entry. That is state-machine divergence, the one
// outcome a Raft state machine may never produce, and it is silent: both
// replicas report success.
//
// The assertion that discriminates is that NEITHER branch ran. A test that only
// checked "the compare did not pass" passes with the defect intact, because the
// defect's whole signature is that the failure branch runs confidently
// (go-qrdb#49).
func TestMVCCEngine_Transaction_AFailedCompareReadPicksNoBranch(t *testing.T) {
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	stg := &readFaultStorage{Storage: inner}
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	// Seed the guard through the fault-free path, so the compare would PASS if
	// the read succeeded. That rules out the failure branch being correct here.
	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("guard"), []byte("acme"), 0)})

	// Arm the fault on the guard's metadata key — the key the MVCC read
	// actually touches.
	stg.failPrefix = mvcc.EncodeMetaKey([]byte("guard"))

	results := make([]sm.Result, 1)
	updateErr := eng.Update(context.Background(),
		[]sm.Entry{guardedTxnEntry(2, "guard", "acme", "success-ran", "failure-ran")}, results)

	if !errors.Is(updateErr, errReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated — a fault evaluated "+
			"as an absent key decides the branch, and replicas that read the guard "+
			"successfully decide it the other way", updateErr)
	}

	// Neither branch may have run, and the guard itself must be untouched.
	stg.failPrefix = nil
	for _, key := range []string{"success-ran", "failure-ran"} {
		if _, err := eng.Store().Get([]byte(key)); err == nil {
			t.Fatalf("%q exists: a transaction whose compare could not be read "+
				"executed a branch anyway", key)
		}
	}

	guard, err := eng.Store().Get([]byte("guard"))
	if err != nil {
		t.Fatalf("guard after a failed compare read: %v", err)
	}
	if string(guard.Value) != "acme" {
		t.Fatalf("guard = %q, want %q — the transaction must leave the store as it "+
			"found it", guard.Value, "acme")
	}
}

// The fault must not be swallowed at any depth: a compare that is not the FIRST
// one still stalls the transaction rather than falling through to a branch.
func TestMVCCEngine_Transaction_AFailedLaterCompareReadPicksNoBranch(t *testing.T) {
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	stg := &readFaultStorage{Storage: inner}
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("first"), []byte("ok"), 0),
		mvccPutEntry(2, []byte("second"), []byte("ok"), 0),
	})

	compares := [][]byte{
		protocol.EncodeCompare([]byte("first"),
			byte(service.CompareValue), byte(service.CompareEqual), []byte("ok"), 0, 0, 0),
		protocol.EncodeCompare([]byte("second"),
			byte(service.CompareValue), byte(service.CompareEqual), []byte("ok"), 0, 0, 0),
	}
	successOps := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCPut, []byte("success-ran"), []byte("ran"), nil, 0),
	}
	failureOps := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCPut, []byte("failure-ran"), []byte("ran"), nil, 0),
	}

	stg.failPrefix = mvcc.EncodeMetaKey([]byte("second"))

	results := make([]sm.Result, 1)
	updateErr := eng.Update(context.Background(), []sm.Entry{{
		Index: 3,
		Cmd:   protocol.EncodeTransaction(compares, successOps, failureOps),
	}}, results)

	if !errors.Is(updateErr, errReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated from the second compare",
			updateErr)
	}

	stg.failPrefix = nil
	for _, key := range []string{"success-ran", "failure-ran"} {
		if _, err := eng.Store().Get([]byte(key)); err == nil {
			t.Fatalf("%q exists: a fault on a later compare still let a branch run", key)
		}
	}
}

// What survives a failed apply is what a RESTART sees, and that is the durable
// state, not the in-memory counter.
//
// Review raised this against the new error return: UpdateEntries applies a batch
// of committed entries in order, so an earlier entry that succeeded has already
// advanced Store.CurrentRevision in memory when a later entry returns an error,
// and Update abandons the batch without putting that counter back. The
// observation about the field is correct. The consequence it predicted — a retry
// assigning different revisions and persisting an inflated counter — is not
// reachable, and this test is why.
//
// The revision is persisted by the SAME batch that is abandoned
// (mvcc_engine.go, Update: batch.Set(mvcc.RevisionKey(), ...) before Commit), so
// nothing inflated is ever written. And go-quicraft treats an SM.Apply error as
// terminal — "Fail-fast: an SM.Apply error is terminal", pkg/internal/engine/apply.go,
// which calls failApply and marks the node failed — so the entries are not
// re-applied against the live in-memory state. Recovery is a restart, and
// mvcc.Store.loadRevision rebuilds the counter from storage on construction.
//
// So the assertion is on a SECOND engine over the same storage, which is what
// the surviving state actually is. This is also not specific to the compare
// path: every handler that returns an error after an earlier entry advanced the
// revision has the same shape, including the conditional-put handlers.
func TestMVCCEngine_Update_AFailedEntryLeavesTheDurableRevisionIntact(t *testing.T) {
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	stg := &readFaultStorage{Storage: inner}
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// One committed apply, so there is a durable revision to compare against.
	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("guard"), []byte("acme"), 0)})
	committedRev := eng.Store().CurrentRevision()

	// Review's case exactly: a good mutation followed by a faulting transaction
	// in ONE Update.
	stg.failPrefix = mvcc.EncodeMetaKey([]byte("guard"))
	results := make([]sm.Result, 2)
	updateErr := eng.Update(context.Background(), []sm.Entry{
		mvccPutEntry(2, []byte("earlier"), []byte("written"), 0),
		guardedTxnEntry(3, "guard", "acme", "success-ran", "failure-ran"),
	}, results)
	if !errors.Is(updateErr, errReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated", updateErr)
	}
	stg.failPrefix = nil

	// The in-memory counter IS ahead — the observation is accurate.
	if eng.Store().CurrentRevision() <= committedRev {
		t.Skip("the earlier entry did not advance the revision; nothing to assert")
	}

	// A restart is the only way forward from a failed apply, and it rebuilds the
	// counter from storage.
	restarted, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("re-opening the engine over the same storage: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	_ = eng.Close(context.Background())

	if got := restarted.Store().CurrentRevision(); got != committedRev {
		t.Fatalf("revision after restart = %d, want the last COMMITTED %d — an "+
			"abandoned batch must not leave an inflated counter behind", got, committedRev)
	}
	for _, key := range []string{"earlier", "success-ran", "failure-ran"} {
		if _, err := restarted.Store().Get([]byte(key)); err == nil {
			t.Fatalf("%q survived a failed apply; the batch was abandoned, so none "+
				"of its writes may be durable", key)
		}
	}
}

// An ABSENT guard is not a fault, and must keep deciding the transaction. This
// is the assertion that stops the fix from being "return an error whenever the
// read did not produce a value", which would break every transaction that
// compares against a key that is legitimately missing.
func TestMVCCEngine_Transaction_AnAbsentGuardStillDecides(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	t.Cleanup(func() { _ = stg.Close() })
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	// "guard" was never written, so the compare against "acme" cannot pass and
	// the failure branch is the correct outcome.
	applyMVCCEntries(t, eng, []sm.Entry{
		guardedTxnEntry(1, "guard", "acme", "success-ran", "failure-ran"),
	})

	if _, err := eng.Store().Get([]byte("success-ran")); err == nil {
		t.Fatal("the success branch ran against an absent guard")
	}
	if _, err := eng.Store().Get([]byte("failure-ran")); err != nil {
		t.Fatalf("the failure branch did not run against an absent guard: %v", err)
	}
}
