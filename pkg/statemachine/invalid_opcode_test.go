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

package statemachine

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// unimplementedOpcode is an operation code no state machine in this package
// registers a handler for.
//
// It USED to stand in for a reachable case: protocol.OpCompareAndDelete (66)
// and protocol.OpCompareAndSwap (67) were absent from every handler map here
// while the SDK's GuardedKVClient proposed them (go-qrdb#119/#123). go-qrdb#175
// wired both, so this constant is now synthetic — which is the whole reason
// the refusal contract still needs its own opcode. The contract below is about
// what happens to an entry naming a verb the state machine was not built with,
// and that must stay pinned whether or not any real opcode is currently in
// that position. TestStateMachines_DispatchEveryGuardedOpcode is what now
// covers 66 and 67.
const unimplementedOpcode byte = 0xFE

// assertOpcodeRefused asserts the SM.Update contract for an entry naming an
// operation the state machine has no handler for — whether that is the entry's
// leading op byte or a sub-operation inside a batch.
//
// Before go-qrdb#129 nine tests in this package asserted the opposite: that
// Update RETURNED an InvalidOpcodeError. Those assertions pinned the defect in
// place. Returning it is what tripped the shard's circuit breaker and cost the
// cluster the shard, so the corrected contract is a refusal the proposer can
// read — a nil error to Raft, plus the error encoded in Result.Data where
// checkResultError finds it.
//
// The encoding half is not decoration. An unprefixed payload does not decode,
// and the proposer then reads a refusal as an ordinary zero result — which for
// a guarded verb means reporting "another writer won" for an operation that
// never ran (go-qrdb#62).
func assertOpcodeRefused(t *testing.T, err error, result sm.Result) {
	t.Helper()
	if err != nil {
		t.Fatalf("Update returned %v; an operation the state machine does not implement is a "+
			"client-input fault and must never be returned to Raft — doing so wedges the "+
			"shard (go-qrdb#129)", err)
	}
	if _, ok := protocol.DecodeAppError(result.Data); !ok {
		t.Errorf("result.Data = %q, want a protocol-encoded application error so the proposer "+
			"surfaces a typed refusal instead of a zero result", result.Data)
	}
}

// assertRefusalNamesOpcode keeps the identification the pre-go-qrdb#129 tests
// made through InvalidOpcodeError.Op. Now that the refusal travels as an
// encoded message rather than a typed error, the operator's only clue to WHICH
// verb was refused is that message, so a refusal that does not name the opcode
// is a worse diagnostic than the error it replaced.
func assertRefusalNamesOpcode(t *testing.T, result sm.Result, op byte) {
	t.Helper()
	msg, ok := protocol.DecodeAppError(result.Data)
	if !ok {
		t.Fatalf("result.Data = %q, want an encoded application error", result.Data)
	}
	if want := strconv.Itoa(int(op)); !strings.Contains(msg, want) {
		t.Errorf("refusal message %q does not name the refused opcode %d", msg, op)
	}
}

// Every state machine in this package dispatches Update through a handler map
// keyed by the leading op byte. An entry naming an op the map has no handler
// for is a CLIENT-INPUT fault — the proposer asked for a verb this state
// machine was not built with — and the SM.Update contract, stated in
// resolveApplyError's own doc comment, is that such a condition must never be
// returned to Raft.
//
// go-qrdb#129: it was. Returning it makes go-quicraft treat the entry as a
// fatal Apply failure, which trips the shard's circuit breaker. Measured
// consequence on a single-replica raft node running sm_type=memory: one
// ordinary authorized CompareAndDelete killed the shard, after which every
// later proposal — INCLUDING a plain Put that has nothing to do with the
// guarded verbs — failed with "SM circuit breaker tripped", and a
// linearizable Get with no context deadline never returned at all.
//
// So the assertions below are deliberately not just "Update returned nil".
// The regression that matters is that the state machine keeps WORKING: a
// later entry in the same batch must still apply, and a subsequent Update
// must still apply. A fix that returned nil but stopped processing the batch
// would satisfy a nil-check and still lose every write behind the bad entry.
func TestUpdate_UnimplementedOpcodeDoesNotPoisonRaft(t *testing.T) {
	// One batch: a bad entry between two good ones. The trailing Put is the
	// load-bearing assertion — it can only be applied if the loop continued
	// past the entry it could not dispatch.
	batch := func(t *testing.T) ([]sm.Entry, []sm.Result) {
		t.Helper()
		before, err := kv.EncodePut([]byte("before"), []byte("1"))
		if err != nil {
			t.Fatalf("EncodePut(before): %v", err)
		}
		after, err := kv.EncodePut([]byte("after"), []byte("2"))
		if err != nil {
			t.Fatalf("EncodePut(after): %v", err)
		}
		entries := []sm.Entry{
			{Index: 1, Cmd: before},
			{Index: 2, Cmd: []byte{unimplementedOpcode, 0x00, 0x01}},
			{Index: 3, Cmd: after},
		}
		return entries, make([]sm.Result, len(entries))
	}

	// assertOutcome checks the shared contract: no error to Raft, the bad
	// entry reported as a decodable application error naming the opcode, and
	// both good entries applied.
	assertOutcome := func(t *testing.T, err error, results []sm.Result, lookup func(key string) []byte) {
		t.Helper()
		if err != nil {
			t.Fatalf("Update returned %v; an unimplemented opcode must never be returned to Raft "+
				"— it trips the shard circuit breaker and wedges the shard (go-qrdb#129)", err)
		}
		msg, ok := protocol.DecodeAppError(results[1].Data)
		if !ok {
			t.Errorf("results[1].Data = %q, want a protocol-encoded application error; "+
				"without the prefix checkResultError cannot decode it and the proposer "+
				"reads the refusal as an ordinary result", results[1].Data)
		} else if !strings.Contains(msg, "254") {
			t.Errorf("application error = %q, want it to name the unrecognised opcode (254)", msg)
		}
		if got := lookup("before"); string(got) != "1" {
			t.Errorf("entry before the unimplemented opcode: got %q, want %q", got, "1")
		}
		if got := lookup("after"); string(got) != "2" {
			t.Errorf("entry AFTER the unimplemented opcode: got %q, want %q — the dispatch loop "+
				"stopped instead of continuing, so every write behind a bad entry is lost", got, "2")
		}
	}

	t.Run("KVStateMachine", func(t *testing.T) {
		k := newTestKVSM()
		entries, results := batch(t)
		err := k.Update(entries, results)
		assertOutcome(t, err, results, func(key string) []byte {
			v, _ := k.backend.Get([]byte(key))
			return v
		})
	})

	t.Run("RegularStateMachine", func(t *testing.T) {
		r := NewRegularStateMachine()
		entries, results := batch(t)
		err := r.Update(context.Background(), entries, results)
		assertOutcome(t, err, results, func(key string) []byte {
			v, _ := r.Lookup(context.Background(), []byte(key))
			b, _ := v.([]byte)
			return b
		})
	})

	t.Run("FastStateMachine", func(t *testing.T) {
		f := NewFastStateMachine(1, 1, memory.NewStorage())
		entries, results := batch(t)
		err := f.Update(context.Background(), entries, results)
		assertOutcome(t, err, results, func(key string) []byte {
			v, _ := f.Lookup(context.Background(), []byte(key))
			b, _ := v.([]byte)
			return b
		})
	})

	t.Run("ConcurrentStateMachine", func(t *testing.T) {
		c := NewConcurrentStateMachine(1, 1, memory.NewStorage())
		entries, results := batch(t)
		err := c.Update(context.Background(), entries, results)
		assertOutcome(t, err, results, func(key string) []byte {
			v, _ := c.Lookup(context.Background(), []byte(key))
			b, _ := v.([]byte)
			return b
		})
	})
}

// The guarded-verb refusal test that stood here is gone, deliberately.
//
// It asserted that protocol.OpCompareAndDelete and protocol.OpCompareAndSwap
// were refused-but-not-fatal, and it carried a t.Skip for the day they were
// implemented. go-qrdb#175 implemented them, so keeping it would leave a test
// that skips forever and pins nothing. What it was really protecting — that a
// verb this state machine does not implement is refused rather than returned
// to Raft — is covered above with a synthetic opcode, which cannot be made
// vacuous by someone implementing it. What replaced its reachable half is
// TestStateMachines_DispatchEveryGuardedOpcode and the behavioural contract
// tests in guarded_ops_test.go.

// TestResolveApplyError_ClassifiesInvalidOpcodeAsApplicationError covers the
// classification seam directly. resolveApplyError is the single place that
// decides whether an error halts Apply, so the rule lives here rather than
// being restated in each of the four Update loops.
func TestResolveApplyError_ClassifiesInvalidOpcodeAsApplicationError(t *testing.T) {
	t.Run("invalid opcode is encoded and does not stop", func(t *testing.T) {
		res, stop, err := resolveApplyError(nil, 7, sm.Result{}, &InvalidOpcodeError{Op: unimplementedOpcode})
		if stop {
			t.Error("stop = true; an unrecognised opcode is a client-input fault, not an " +
				"infrastructure failure, and must not halt Apply")
		}
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		msg, ok := protocol.DecodeAppError(res.Data)
		if !ok {
			t.Fatalf("Data = %q, want an encoded application error", res.Data)
		}
		if !strings.Contains(msg, "254") {
			t.Errorf("message = %q, want it to name the opcode", msg)
		}
	})

	// The control. Widening the application-error class must not swallow
	// genuine infrastructure faults, which still have to halt Apply — that is
	// the whole reason Update is allowed to return an error at all.
	t.Run("a storage failure still stops", func(t *testing.T) {
		storageErr := &StorageError{Op: "put", Inner: context.DeadlineExceeded}
		_, stop, err := resolveApplyError(nil, 7, sm.Result{}, storageErr)
		if !stop {
			t.Error("stop = false for a StorageError; an infrastructure failure must still halt Apply")
		}
		if err == nil {
			t.Error("err = nil for a StorageError; the fault must reach Raft")
		}
	})
}

// A batch is atomic or it is nothing. Every handleBatch in this package walks
// its operations applying each in turn and only refuses when it REACHES an
// operation it does not implement — by which point the earlier ones are
// already written and their watch notifications already delivered.
//
// That was a latent atomicity hole before go-qrdb#129: the refusal reached
// Raft as an Apply error, which poisoned the shard, so the partial write was
// the lesser of two problems. Classifying InvalidOpcodeError as an application
// error made the shard survive — and turned the hole into a lie the caller now
// acts on: it is told the batch was refused, and half of it is committed.
//
// So the opcodes are validated BEFORE anything is applied. A batch naming an
// operation the state machine cannot execute is refused whole: no writes, no
// notifications, and a typed refusal the proposer can read.
//
// The assertions on the untouched key and the silent watch manager are the
// load-bearing ones. Asserting only "Update returned nil and encoded a
// refusal" is satisfied by the broken version too — the defect is not in what
// the caller is TOLD, it is in what the store is left holding.
func TestUpdate_BatchWithUnimplementedOpIsRefusedWhole(t *testing.T) {
	// The valid Put comes FIRST, so a state machine that validates as it goes
	// has already committed it by the time it reaches the bad operation. A
	// batch with the bad operation first passes either way and proves nothing.
	batchCmd := func(t *testing.T) []byte {
		t.Helper()
		ops := [][]byte{
			mustEncode(protocol.EncodeOp(kv.OpPut, []byte("survivor"), []byte("v"), nil, 0)),
			mustEncode(protocol.EncodeOp(unimplementedOpcode, []byte("bad"), []byte("v"), nil, 0)),
		}
		return mustEncode(protocol.EncodeBatch(ops))
	}

	assertRefusedWhole := func(t *testing.T, err error, result sm.Result, wm *mockWatchManager, lookup func(string) []byte) {
		t.Helper()
		assertOpcodeRefused(t, err, result)
		if got := lookup("survivor"); got != nil {
			t.Errorf("the operation BEFORE the unimplemented one was committed (%q); a batch the "+
				"state machine cannot execute whole must not be executed in part", got)
		}
		wm.mu.Lock()
		n := len(wm.events)
		wm.mu.Unlock()
		if n != 0 {
			t.Errorf("%d watch notification(s) were delivered for a refused batch; a watcher was "+
				"told about a write the store does not hold", n)
		}
	}

	t.Run("KVStateMachine", func(t *testing.T) {
		wm := &mockWatchManager{}
		k := NewKVStateMachine(NewStandardBackend(), WithKVWatchManager(wm))
		results := make([]sm.Result, 1)
		err := k.Update([]sm.Entry{{Index: 1, Cmd: batchCmd(t)}}, results)
		assertRefusedWhole(t, err, results[0], wm, func(key string) []byte {
			v, _ := k.backend.Get([]byte(key))
			return v
		})
	})

	t.Run("RegularStateMachine", func(t *testing.T) {
		wm := &mockWatchManager{}
		r := NewRegularStateMachine(WithRegularWatchManager(wm))
		results := make([]sm.Result, 1)
		err := r.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: batchCmd(t)}}, results)
		assertRefusedWhole(t, err, results[0], wm, func(key string) []byte {
			v, _ := r.Lookup(context.Background(), []byte(key))
			b, _ := v.([]byte)
			return b
		})
	})

	t.Run("FastStateMachine", func(t *testing.T) {
		wm := &mockWatchManager{}
		f := NewFastStateMachine(1, 1, memory.NewStorage(), WithWatchManager(wm))
		results := make([]sm.Result, 1)
		err := f.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: batchCmd(t)}}, results)
		assertRefusedWhole(t, err, results[0], wm, func(key string) []byte {
			v, _ := f.Lookup(context.Background(), []byte(key))
			b, _ := v.([]byte)
			return b
		})
	})

	t.Run("ConcurrentStateMachine", func(t *testing.T) {
		wm := &mockWatchManager{}
		c := NewConcurrentStateMachine(1, 1, memory.NewStorage(), WithConcurrentWatchManager(wm))
		results := make([]sm.Result, 1)
		err := c.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: batchCmd(t)}}, results)
		assertRefusedWhole(t, err, results[0], wm, func(key string) []byte {
			v, _ := c.Lookup(context.Background(), []byte(key))
			b, _ := v.([]byte)
			return b
		})
	})
}

// TestBatchValidationMatchesDispatch pins the pre-flight check to what the
// batch loops actually execute, over every possible opcode.
//
// The two are separate pieces of code that must agree. If validateBatchOps
// ever accepts an op no dispatch implements, that op reaches the loop, the
// loop's default refuses it partway through, and the partial-write hole this
// fix closed is silently reopened — for that opcode only, which is exactly the
// kind of regression a hand-picked example misses. Enumerating all 256 values
// costs nothing and makes the invariant impossible to break quietly.
//
// The yardstick is the dispatch's own behaviour, obtained by running a
// single-op batch through a real state machine, not a second copy of the
// supported-op list — a list compared against itself would agree no matter
// what either side did.
func TestBatchValidationMatchesDispatch(t *testing.T) {
	for op := 0; op <= 0xFF; op++ {
		opcode := byte(op)

		accepted := validateBatchOps([]protocol.DecodedOp{{Type: opcode}}) == nil

		// Ask the dispatch directly, with validation bypassed, whether it has a
		// case for this opcode: apply a one-op batch and see whether the loop's
		// default arm rejects it.
		k := newTestKVSM()
		cmd := mustEncode(protocol.EncodeBatch([][]byte{
			mustEncode(protocol.EncodeOp(opcode, []byte("k"), []byte("v"), nil, 0)),
		}))
		ops, err := protocol.DecodeBatch(cmd[1:])
		if err != nil {
			t.Fatalf("opcode %d: DecodeBatch: %v", opcode, err)
		}
		dispatched := true
		for _, o := range ops {
			if !batchOpSupported(o.Type) {
				dispatched = false
			}
		}
		// batchOpSupported is the shared definition, so compare the VALIDATOR's
		// verdict on a decoded batch against a real handleBatch outcome.
		results := make([]sm.Result, 1)
		updErr := k.Update([]sm.Entry{{Index: 1, Cmd: cmd}}, results)
		_, refused := protocol.DecodeAppError(results[0].Data)
		executed := updErr == nil && !refused

		if accepted != executed {
			t.Errorf("opcode %d: validateBatchOps accepts=%v but handleBatch executed=%v "+
				"(dispatch-supported=%v) — a batch op the validator lets through and the "+
				"loop refuses is applied in part before it is rejected",
				opcode, accepted, executed, dispatched)
		}
	}
}
