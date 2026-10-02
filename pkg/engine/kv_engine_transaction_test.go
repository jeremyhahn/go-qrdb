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

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// The file/objstore KV engine gained OpTransaction with #117: the multi-key
// compare-and-execute transaction previously existed only on the MVCC and
// PebbleDB backends, so a transaction proposed on a file-backed standalone node
// failed the whole proposal with UnsupportedOperationError while the same
// transaction succeeded on memory/pebble. These assertions hold this engine to
// the same contract the other two implementations satisfy, so the three cannot
// drift.

// txnValueGuardEntry builds a transaction that compares guardKey's value and
// writes a different key on each branch, so which branch ran is readable from
// the store afterward.
func txnValueGuardEntry(idx uint64, guardKey, guardValue, successKey, failureKey string) sm.Entry {
	compares := [][]byte{
		mustEncode(protocol.EncodeCompare(
			[]byte(guardKey),
			byte(service.CompareValue),
			byte(service.CompareEqual),
			[]byte(guardValue),
			0, 0, 0,
		)),
	}
	successOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte(successKey), []byte("ran"), nil, 0)),
	}
	failureOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte(failureKey), []byte("ran"), nil, 0)),
	}
	return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeTransaction(compares, successOps, failureOps))}
}

func txnSucceeded(t *testing.T, r sm.Result) bool {
	t.Helper()
	if len(r.Data) != 1 {
		t.Fatalf("transaction result Data = %v, want a 1-byte succeeded flag", r.Data)
	}
	return r.Data[0] == 1
}

func mustLookupAbsent(t *testing.T, eng *KVEngine, key string) {
	t.Helper()
	got, err := eng.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", key, err)
	}
	if got != nil {
		if b, ok := got.([]byte); !ok || b != nil {
			t.Fatalf("key %q = %v, want absent", key, got)
		}
	}
}

func mustLookupValue(t *testing.T, eng *KVEngine, key, want string) {
	t.Helper()
	got, err := eng.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", key, err)
	}
	b, ok := got.([]byte)
	if !ok || string(b) != want {
		t.Fatalf("key %q = %v, want %q", key, got, want)
	}
}

// This engine keeps no leases, so a transaction put naming one is refused
// and the whole op set writes nothing. Stored lease-less, the key would never
// expire.
func TestKVEngine_Transaction_LeasedPutIsRefusedAndWritesNothing(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("plain"), []byte("v"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("leased"), []byte("v"), nil, 7)),
	}
	entry := sm.Entry{Index: 1, Cmd: mustEncode(protocol.EncodeTransaction(nil, ops, nil))}
	results := applyKVEntries(t, eng, []sm.Entry{entry})

	if len(results[0].Data) <= 1 {
		t.Fatalf("result Data = %v, want an encoded refusal", results[0].Data)
	}
	mustLookupAbsent(t, eng, "plain")
	mustLookupAbsent(t, eng, "leased")
}

func TestKVEngine_Transaction_ValueCompareTrue_RunsSuccessOps(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "guard", "expected")})
	results := applyKVEntries(t, eng, []sm.Entry{txnValueGuardEntry(2, "guard", "expected", "success", "failure")})

	if !txnSucceeded(t, results[0]) {
		t.Fatal("expected the compare to hold and the success branch to run")
	}
	mustLookupValue(t, eng, "success", "ran")
	mustLookupAbsent(t, eng, "failure")
}

func TestKVEngine_Transaction_ValueCompareFalse_RunsFailureOps(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "guard", "other")})
	results := applyKVEntries(t, eng, []sm.Entry{txnValueGuardEntry(2, "guard", "expected", "success", "failure")})

	if txnSucceeded(t, results[0]) {
		t.Fatal("expected the compare to fail and the failure branch to run")
	}
	mustLookupValue(t, eng, "failure", "ran")
	mustLookupAbsent(t, eng, "success")
}

// An absent key compares equal only to an empty value; a compare against a
// non-empty value on an absent key must fail. This is what makes a
// compare(value == "") a "key is absent" guard.
func TestKVEngine_Transaction_AbsentKeyValueCompare(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	// Guard absent, compared against empty: succeeds.
	emptyGuard := sm.Entry{Index: 1, Cmd: mustEncode(protocol.EncodeTransaction(
		[][]byte{mustEncode(protocol.EncodeCompare([]byte("guard"), byte(service.CompareValue), byte(service.CompareEqual), nil, 0, 0, 0))},
		[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("absent-hit"), []byte("ran"), nil, 0))},
		nil,
	))}
	if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{emptyGuard})[0]) {
		t.Fatal("compare(value==\"\") on an absent key should succeed")
	}
	mustLookupValue(t, eng, "absent-hit", "ran")

	// Guard absent, compared against a non-empty value: fails.
	nonEmptyGuard := sm.Entry{Index: 2, Cmd: mustEncode(protocol.EncodeTransaction(
		[][]byte{mustEncode(protocol.EncodeCompare([]byte("guard2"), byte(service.CompareValue), byte(service.CompareEqual), []byte("x"), 0, 0, 0))},
		[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("should-not-run"), []byte("ran"), nil, 0))},
		nil,
	))}
	if txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{nonEmptyGuard})[0]) {
		t.Fatal("compare(value==\"x\") on an absent key should fail")
	}
	mustLookupAbsent(t, eng, "should-not-run")
}

// Version degrades honestly on a non-MVCC engine: 0 when the key is absent, 1
// when it is present.
func TestKVEngine_Transaction_VersionCompare(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	versionGuard := func(idx uint64, key string, wantVersion int64, hitKey string) sm.Entry {
		return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeTransaction(
			[][]byte{mustEncode(protocol.EncodeCompare([]byte(key), byte(service.CompareVersion), byte(service.CompareEqual), nil, wantVersion, 0, 0))},
			[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte(hitKey), []byte("ran"), nil, 0))},
			nil,
		))}
	}

	// Absent key: version == 0 holds.
	if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{versionGuard(1, "k", 0, "v0")})[0]) {
		t.Fatal("version==0 on an absent key should hold")
	}
	mustLookupValue(t, eng, "v0", "ran")

	// Present key: version == 1 holds, version == 0 does not.
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, "present", "x")})
	if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{versionGuard(3, "present", 1, "v1")})[0]) {
		t.Fatal("version==1 on a present key should hold")
	}
	if txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{versionGuard(4, "present", 0, "v0b")})[0]) {
		t.Fatal("version==0 on a present key should NOT hold")
	}
	mustLookupAbsent(t, eng, "v0b")
}

// A multi-key transaction applies every op in the selected branch atomically.
func TestKVEngine_Transaction_MultiKeyAtomic(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "guard", "ok")})

	entry := sm.Entry{Index: 2, Cmd: mustEncode(protocol.EncodeTransaction(
		[][]byte{mustEncode(protocol.EncodeCompare([]byte("guard"), byte(service.CompareValue), byte(service.CompareEqual), []byte("ok"), 0, 0, 0))},
		[][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("a"), []byte("1"), nil, 0)),
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("b"), []byte("2"), nil, 0)),
			mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("guard"), nil, nil, 0)),
		},
		nil,
	))}
	results := applyKVEntries(t, eng, []sm.Entry{entry})
	if !txnSucceeded(t, results[0]) {
		t.Fatal("expected the transaction to run its success branch")
	}
	if results[0].Value != 3 {
		t.Fatalf("result Value = %d, want 3 applied ops", results[0].Value)
	}
	mustLookupValue(t, eng, "a", "1")
	mustLookupValue(t, eng, "b", "2")
	mustLookupAbsent(t, eng, "guard")
}

// create_revision / mod_revision compares are refused with an application
// error rather than silently reading revision 0 (which would let a read-then-
// CAS client lose an update with no signal). The refusal is a non-fatal
// Result.Data payload, not a Go error that would stall the apply loop.
func TestKVEngine_Transaction_RejectsRevisionCompare(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	for _, target := range []service.CompareTarget{service.CompareCreateRevision, service.CompareModRevision} {
		entry := sm.Entry{Index: 1, Cmd: mustEncode(protocol.EncodeTransaction(
			[][]byte{mustEncode(protocol.EncodeCompare([]byte("k"), byte(target), byte(service.CompareEqual), nil, 0, 0, 0))},
			[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("hit"), []byte("ran"), nil, 0))},
			nil,
		))}
		results := applyKVEntries(t, eng, []sm.Entry{entry})
		msg, ok := protocol.DecodeAppError(results[0].Data)
		if !ok {
			t.Fatalf("target %v: expected an application error, got Data=%v", target, results[0].Data)
		}
		if msg == "" {
			t.Fatalf("target %v: application error carried no message", target)
		}
		// The guarded write must NOT have run.
		mustLookupAbsent(t, eng, "hit")
	}
}

// TestGuardedVerbs_FileEngineMatchesMVCC is the conformance assertion at the
// heart of #117: the same guarded-verb sequences produce the same succeeded
// outcome and the same final store state on the file/objstore KV engine as on
// the MVCC engine (which backs memory/pebble). Only the semantics are compared
// — the Result.Value field legitimately differs (KV/pebble return an op count,
// MVCC returns a revision), so it is not asserted here.
func TestGuardedVerbs_FileEngineMatchesMVCC(t *testing.T) {
	kvStg := memory.NewStorage()
	defer func() { _ = kvStg.Close() }()
	mvStg := memory.NewStorage()
	defer func() { _ = mvStg.Close() }()

	kvEng := NewKVEngine(kvStg)
	mvEng, err := NewMVCCEngine(mvStg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// One value-guarded transaction whose guard holds, then one whose guard
	// fails, applied to both engines from an identical starting state.
	seed := []sm.Entry{kvPutEntry(1, "guard", "expected")}
	applyKVEntries(t, kvEng, seed)
	applyKVEntries(t, mvEng, seed)

	hit := txnValueGuardEntry(2, "guard", "expected", "success", "failure")
	miss := txnValueGuardEntry(3, "guard", "WRONG", "success2", "failure2")

	for _, tc := range []struct {
		name  string
		entry sm.Entry
	}{{"guard-holds", hit}, {"guard-fails", miss}} {
		kvRes := applyKVEntries(t, kvEng, []sm.Entry{tc.entry})[0]
		mvRes := applyKVEntries(t, mvEng, []sm.Entry{tc.entry})[0]

		kvOK := txnSucceeded(t, kvRes)
		mvOK := txnSucceeded(t, mvRes)
		if kvOK != mvOK {
			t.Fatalf("%s: succeeded flag diverged: file=%v mvcc=%v", tc.name, kvOK, mvOK)
		}
	}

	// Final state must match key-for-key on the keys the branches touched.
	for _, key := range []string{"success", "failure", "success2", "failure2"} {
		kvGot, _ := kvEng.Lookup(context.Background(), key)
		mvGot, _ := mvEng.Lookup(context.Background(), key)
		kvB, _ := kvGot.([]byte)
		mvB, _ := mvGot.([]byte)
		if string(kvB) != string(mvB) {
			t.Fatalf("key %q diverged: file=%q mvcc=%q", key, kvB, mvB)
		}
	}
}

// A compare whose existence read FAILS must not decide the transaction: folding
// the fault into "absent" would let a version==0 guard select the success
// branch on this replica while a replica whose read succeeded selects the
// failure branch — a state-machine divergence from an identical log, and a
// fail-open on absence guards. The apply must return the error and write
// nothing. (readFaultStorage/errReadFault are defined in
// kv_engine_putifabsent_test.go.)
func TestKVEngine_Transaction_CompareReadFaultWritesNothing(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()
	stg := &readFaultStorage{Storage: inner, failKey: "guard"}
	eng := NewKVEngine(stg)

	// A version==0 guard on "guard": read as absence it would run SuccessOps.
	entry := sm.Entry{Index: 1, Cmd: mustEncode(protocol.EncodeTransaction(
		[][]byte{mustEncode(protocol.EncodeCompare([]byte("guard"), byte(service.CompareVersion), byte(service.CompareEqual), nil, 0, 0, 0))},
		[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("success"), []byte("ran"), nil, 0))},
		[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("failure"), []byte("ran"), nil, 0))},
	))}

	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{entry}, results)
	if !errors.Is(err, errReadFault) {
		t.Fatalf("Update = %v, want the compare read fault propagated", err)
	}

	// Neither branch ran.
	stg.failKey = ""
	mustLookupAbsent(t, eng, "success")
	mustLookupAbsent(t, eng, "failure")
}

// createByNameIndex registers a JSON "name" index and returns the engine over
// fresh memory storage. Operations that follow observe the index through both
// backfill and write maintenance.
func newIndexedKVEngine(t *testing.T) *KVEngine {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng := NewKVEngine(stg)
	cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{}))
	applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: cmd}})
	return eng
}

// Two puts to the SAME key in one transaction must be read-your-own-writes:
// the second put's index maintenance must see the first put's value as its
// previous value and retract it, or a stale index entry for the first value
// survives after the data commits as the second. This fails if the op reads
// committed state instead of the batch.
func TestKVEngine_Transaction_ReadsOwnWrites_PutPutIndex(t *testing.T) {
	eng := newIndexedKVEngine(t)

	entry := sm.Entry{Index: 2, Cmd: mustEncode(protocol.EncodeTransaction(
		nil,
		[][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte(`{"name":"Alice"}`), nil, 0)),
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte(`{"name":"Bob"}`), nil, 0)),
		},
		nil,
	))}
	if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{entry})[0]) {
		t.Fatal("an unconditional transaction should run its success branch")
	}

	if got := lookupIndex(t, eng, "by_name", "Alice"); got != 0 {
		t.Errorf("index still matches the overwritten value \"Alice\" (%d hits); "+
			"the second put did not retract the first — read-your-own-writes is broken", got)
	}
	if got := lookupIndex(t, eng, "by_name", "Bob"); got != 1 {
		t.Errorf("index matches final value \"Bob\" %d times; want 1", got)
	}
	mustLookupValue(t, eng, "user:1", `{"name":"Bob"}`)
}

// A put followed by a delete of the SAME key in one transaction must leave no
// index entry: the delete must see the put's value (through the batch) and
// retract the index entry the put added. This fails if the delete reads
// committed state (absent) and retracts nothing.
func TestKVEngine_Transaction_ReadsOwnWrites_PutDeleteIndex(t *testing.T) {
	eng := newIndexedKVEngine(t)

	entry := sm.Entry{Index: 2, Cmd: mustEncode(protocol.EncodeTransaction(
		nil,
		[][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte(`{"name":"Alice"}`), nil, 0)),
			mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("user:1"), nil, nil, 0)),
		},
		nil,
	))}
	if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{entry})[0]) {
		t.Fatal("an unconditional transaction should run its success branch")
	}

	if got := lookupIndex(t, eng, "by_name", "Alice"); got != 0 {
		t.Errorf("index still matches \"Alice\" (%d hits) after the key was deleted "+
			"in the same transaction; the delete did not retract the put's index entry", got)
	}
	mustLookupAbsent(t, eng, "user:1")
}
