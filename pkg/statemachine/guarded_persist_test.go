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

//go:build codec_json && raft && !integration

package statemachine

import (
	"bytes"
	"context"
	stderrors "errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// failSetStorage fails Set on demand while behaving normally otherwise.
//
// The failure is switchable rather than fixed because these tests need the
// state machine to reach a given state through WORKING writes and only then
// meet the fault -- a storage that failed every Set could never be seeded.
type failSetStorage struct {
	*memStorage
	failing atomic.Bool
	// failDelete makes Delete fail too. It exists so a test can leave a
	// RETIRED VALUE in storage under a cache tombstone -- the state that makes
	// the tombstone load-bearing. Delete is asynchronous and swallows its
	// error, so this is a state a real deployment reaches, not a contrivance.
	failDelete atomic.Bool
	// failKey, when non-nil, narrows the Set failure to a single key so a
	// multi-entry batch can commit an earlier entry and fault on a later one
	// (go-qrdb#94 batch replay). It is written before the apply that reads it,
	// on the same goroutine, since the guarded persist is synchronous.
	failKey []byte
	err     error

	// holdSet parks the next Set until it is closed; setHeld announces that
	// the Set is parked. Both are touched only by the test goroutine and the
	// single parked writer, in that order.
	holdSet chan struct{}
	setHeld chan struct{}
}

func newFailSetStorage(err error) *failSetStorage {
	return &failSetStorage{memStorage: newMemStorage(), err: err}
}

func (s *failSetStorage) Set(key, value []byte) error {
	if s.holdSet != nil {
		close(s.setHeld)
		<-s.holdSet
		s.holdSet = nil
	}
	if s.failing.Load() && (s.failKey == nil || bytes.Equal(key, s.failKey)) {
		return s.err
	}
	return s.memStorage.Set(key, value)
}

// holdFirstSet makes the next Set block until the returned release function is
// called, announcing on the returned channel that it is blocked.
//
// It is what makes the failure interleaving DETERMINISTIC: the guarded write
// is parked inside storage, with the shard lock released, which is precisely
// the window a later write has to land in. Racing two goroutines and hoping
// would test the interleaving only some of the time.
func (s *failSetStorage) holdFirstSet() (held <-chan struct{}, release func()) {
	s.setHeld = make(chan struct{})
	hold := make(chan struct{})
	s.holdSet = hold
	return s.setHeld, func() { close(hold) }
}

func (s *failSetStorage) Delete(key []byte) error {
	if s.failDelete.Load() {
		return s.err
	}
	return s.memStorage.Delete(key)
}

// TestShardedStateMachines_PutIfAbsent_IsDurableBeforeItAcknowledges is the
// guarantee the owner ruled for on 2026-08-23 (go-qrdb#94, option 1).
//
// kv.ResultStored is the answer to "did I win the claim?" -- the identity
// index uses it to decide that a role id, tenant id or single-use token
// belongs to this caller and to nobody else. Acknowledging before the write
// can fail means two holders after a restart, the exact outcome the guard
// exists to prevent.
//
// Backing storage is read with NO drain and NO Close: the point is that the
// value is already there when the apply returns, not that it arrives
// eventually. Draining first would make this pass against the asynchronous
// code it replaces.
func TestShardedStateMachines_PutIfAbsent_IsDurableBeforeItAcknowledges(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newMemStorage()
			target, _ := build(store)

			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine")))
			if err != nil {
				t.Fatalf("PutIfAbsent: %v", err)
			}
			if res.Value != kv.ResultStored {
				t.Fatalf("Result.Value = %d, want kv.ResultStored (%d)", res.Value, kv.ResultStored)
			}

			got, gerr := store.Get([]byte("k"))
			if gerr != nil {
				t.Fatalf("storage.Get: %v", gerr)
			}
			if !bytes.Equal(got, []byte("mine")) {
				t.Fatalf("storage holds %q immediately after the acknowledgement, want %q — "+
					"the claim was acknowledged before it was durable, so a restart here "+
					"leaves the key absent and a second caller wins the same claim",
					got, "mine")
			}
		})
	}
}

// TestShardedStateMachines_PutIfAbsent_ReportsAFailedPersist pins the other
// half of the ruled contract: the failure is RETURNED, not logged.
//
// This is the carve-out KVStateMachine.Update's contract documents -- an
// unrecoverable infrastructure error (disk full, corrupt storage) may be
// returned, unlike an application-level condition.
//
// Result.Value is checked as well as the error, because kv.ResultStored
// alongside a non-nil error would still be readable by a caller that checks
// only the value.
func TestShardedStateMachines_PutIfAbsent_ReportsAFailedPersist(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			boom := stderrors.New("disk full")
			store := newFailSetStorage(boom)
			target, _ := build(store)

			store.failing.Store(true)
			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine")))
			if err == nil {
				t.Fatalf("a failed persist was not reported: Result.Value = %d", res.Value)
			}
			if !stderrors.Is(err, boom) {
				t.Errorf("error = %v, want it to wrap the storage fault", err)
			}
			if res.Value == kv.ResultStored {
				t.Error("Result.Value is kv.ResultStored on a claim that never reached disk")
			}
		})
	}
}

// TestShardedStateMachines_PutIfAbsent_WithdrawsAClaimThatDidNotPersist is the
// "leaving cache and watch state unchanged" half of option 1.
//
// A caller told the write failed, and a cache still advertising the key, would
// make the NEXT guarded write on that key answer "already exists" for a value
// no reader will ever find -- a permanently unclaimable key, which is worse
// than the fault that caused it.
//
// The retry is the observable, rather than reaching into the shard map: it is
// what a caller actually does after a failure, and it needs one accessor per
// state machine otherwise.
func TestShardedStateMachines_PutIfAbsent_WithdrawsAClaimThatDidNotPersist(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newFailSetStorage(stderrors.New("disk full"))
			target, _ := build(store)

			store.failing.Store(true)
			if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("first"))); err == nil {
				t.Fatal("a failed persist was not reported")
			}

			// The cache must not be advertising the withdrawn claim.
			if got := target.lookup(t, []byte("k")); got != nil {
				t.Errorf("lookup returned %q after a claim that never persisted", got)
			}

			// The retry, once storage is healthy, must WIN.
			store.failing.Store(false)
			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("second")))
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if res.Value != kv.ResultStored {
				t.Fatalf("the retry got %d, want kv.ResultStored (%d) — the withdrawn claim "+
					"left the key permanently unclaimable", res.Value, kv.ResultStored)
			}
		})
	}
}

// TestShardedStateMachines_PutIfAbsent_WithdrawRestoresATombstone covers the
// case where the two prior cache states are NOT interchangeable.
//
// A deleted key is a nil TOMBSTONE, not a removed entry, and the tombstone is
// what tells the guard it need not read through to storage -- the delete that
// wrote it is newer than anything storage holds. Withdrawing a failed claim by
// DELETING the entry would drop that fact, so the next guarded write would
// read through, find the value the delete had already retired, and refuse a
// key that is genuinely absent.
//
// The state that makes the distinction observable is a cache tombstone over a
// retired value STILL IN STORAGE. Two things are needed to reach it
// deterministically, and this test got both wrong at first:
//
//   - The seed is written with the GUARDED verb, which is synchronous, so the
//     value is provably in storage before anything else runs. Seeding with Put
//     raced: its asynchronous write could still be pending when the test
//     switched Set to failing, so the seed never landed, storage was empty,
//     and a withdraw that dropped the tombstone passed.
//   - The storage delete is made to FAIL, so the retired value survives the
//     delete. Delete is asynchronous and swallows its error, so this is a
//     state a real deployment reaches.
//
// The retired value is asserted BEFORE the retry, because the retry's own
// write puts a value back under that key -- checking afterwards reads the
// retry's value and cannot fail.
func TestShardedStateMachines_PutIfAbsent_WithdrawRestoresATombstone(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newFailSetStorage(stderrors.New("disk full"))
			target, _ := build(store)

			// Seed through the guarded verb: synchronous, so storage holds
			// "old" by the time this returns.
			if res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("old"))); err != nil {
				t.Fatalf("seed PutIfAbsent: %v", err)
			} else if res.Value != kv.ResultStored {
				t.Fatalf("seed PutIfAbsent = %d, want kv.ResultStored", res.Value)
			}

			// Delete with the storage delete failing: cache tombstone over a
			// retired value that stays in storage.
			store.failDelete.Store(true)
			if _, err := target.apply(t, buildKVCmd(kv.OpDelete, []byte("k"), nil)); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			// A guarded write over the tombstone that fails to persist.
			store.failing.Store(true)
			if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("first"))); err == nil {
				t.Fatal("a failed persist was not reported")
			}

			// The precondition the assertion below rests on: storage really
			// does still hold the retired value, so a read-through would find
			// it. Asserted here, before the retry writes over it.
			retired, gerr := store.memStorage.Get([]byte("k"))
			if gerr != nil {
				t.Fatalf("storage.Get: %v", gerr)
			}
			if !bytes.Equal(retired, []byte("old")) {
				t.Fatalf("storage holds %q, want %q — without a retired value in storage "+
					"this test cannot distinguish a restored tombstone from a deleted entry",
					retired, "old")
			}

			// The retry must still see the key as ABSENT. If the withdraw had
			// deleted the entry instead of restoring the tombstone, the guard
			// would read through to storage and find "old".
			store.failing.Store(false)
			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("second")))
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if res.Value != kv.ResultStored {
				t.Fatalf("the retry over a restored tombstone got %d, want kv.ResultStored (%d) — "+
					"the withdraw dropped the tombstone and the guard read through to a "+
					"retired value", res.Value, kv.ResultStored)
			}
		})
	}
}

// TestShardedStateMachines_PutAndDelete_KeepTheAsynchronousContract is the
// control for the whole change.
//
// The ruling applies synchronous persistence to the GUARDED verb only: "your
// write is queued" and "your claim succeeded" are different promises, and only
// the second is worth a disk wait on the apply path. A change that made every
// write synchronous would satisfy every assertion above and quietly put a disk
// write on the path these state machines exist to keep it off.
func TestShardedStateMachines_PutAndDelete_KeepTheAsynchronousContract(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newFailSetStorage(stderrors.New("disk full"))
			target, _ := build(store)

			store.failing.Store(true)
			if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("v"))); err != nil {
				t.Errorf("Put reported a storage failure (%v); it keeps the asynchronous "+
					"contract and must not", err)
			}
			if _, err := target.apply(t, buildKVCmd(kv.OpDelete, []byte("k"), nil)); err != nil {
				t.Errorf("Delete reported a storage failure (%v); it keeps the asynchronous "+
					"contract and must not", err)
			}
		})
	}
}

// TestShardedStateMachines_FailedClaimDoesNotEraseALaterWrite covers the
// blocking finding from the Codex review of PR #110.
//
// persistClaimLocked releases the shard lock while the storage write runs --
// deliberately, since holding it would serialise the whole shard on disk. A
// later Put to the same key can therefore land in that window. Rolling the
// failed claim back UNCONDITIONALLY would then delete, or tombstone, a value
// that Put had legitimately established: the caller of the Put was told its
// write was accepted, and a subsequent guarded write would go on to win a key
// the Put already holds.
//
// The claim is retired by any later write to the key, under the shard lock, so
// the rollback can tell the entry is no longer its own and leaves it alone.
//
// Note this interleaving is not reachable through the Raft apply path today:
// go-quicraft's sm contract runs Lookup concurrently with Update, not Update
// with Update. The test drives Update directly, which is exactly what makes
// the guarantee independent of that -- Update is exported, and a rollback that
// silently relies on an external serialisation is one refactor from wrong.
func TestShardedStateMachines_FailedClaimDoesNotEraseALaterWrite(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newFailSetStorage(stderrors.New("disk full"))
			target, _ := build(store)

			held, release := store.holdFirstSet()
			store.failing.Store(true)

			// The guarded claim, parked inside its storage write.
			done := applyAsync(t, target, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("claimed")))
			<-held

			// A later Put lands while the claim is mid-flight and the shard
			// lock is free. Its own persistence is asynchronous, so this
			// returns without waiting on storage -- and its storage write will
			// fail too, which Put's contract swallows. The cache is what this
			// test is about.
			if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("second"))); err != nil {
				t.Fatalf("Put: %v", err)
			}

			// Now let the guarded write fail.
			release()
			got := <-done
			if got.err == nil {
				t.Fatalf("the guarded write did not report its failure: %+v", got.res)
			}

			// The Put's value must survive the failed claim's rollback.
			if v := target.lookup(t, []byte("k")); !bytes.Equal(v, []byte("second")) {
				t.Fatalf("lookup = %q, want %q — the failed claim's rollback erased a write "+
					"that landed after it, and the caller of that write was told it was accepted",
					v, "second")
			}

			// And the key must still read as PRESENT to a guarded write, or
			// the erasure is merely hidden behind a tombstone.
			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("third")))
			if err != nil {
				t.Fatalf("follow-up PutIfAbsent: %v", err)
			}
			if res.Value != kv.ResultNotStored {
				t.Errorf("a guarded write won a key an earlier Put already holds (Result.Value = %d)",
					res.Value)
			}
		})
	}
}

// TestShardedStateMachines_GuardedFaultMidBatchHaltsForReplay pins the
// semantics the owner ratified for go-qrdb#94 (option A) and answers the
// round-2 Codex finding on PR #110: a guarded write is all-or-nothing by HALT
// plus deterministic replay, not by in-call rollback across the apply loop.
//
// go-quicraft's StateMachine.Update is "all-or-nothing ... or the state
// machine ... returns an error" AND "Returns error only for fatal,
// non-recoverable failures" (pkg/sm/interfaces.go:50). pkg/sm/types.go:38
// records that a returned Update error wedges the shard on replay, which is why
// resolveApplyError returns a genuine storage fault VERBATIM (stop=true): Apply
// HALTS, lastApplied does not advance, and the committed entries replay from the
// log on restart. A mid-batch guarded fault therefore never acknowledges a
// write it then loses -- it halts, and replay converges the replica on the same
// state. In-call rollback of the earlier entry (Codex's round-2 suggestion) is
// unnecessary for correctness and is declined for this release.
//
// The regression this guards: mutate resolveApplyError to SWALLOW the fault
// (return it as a per-entry result with stop=false). Update would then return
// nil -- an ACK -- while "second" never reached disk, and the err!=nil
// assertion below goes red.
func TestShardedStateMachines_GuardedFaultMidBatchHaltsForReplay(t *testing.T) {
	type target struct {
		update func(context.Context, []sm.Entry, []sm.Result) error
		closer persistCloser
	}
	targets := map[string]func(storage.Storage) target{
		"FastStateMachine": func(st storage.Storage) target {
			f := NewFastStateMachine(1, 1, st)
			return target{update: f.Update, closer: f}
		},
		"ConcurrentStateMachine": func(st storage.Storage) target {
			c := NewConcurrentStateMachine(1, 1, st)
			return target{update: c.Update, closer: c}
		},
	}
	for name, build := range targets {
		t.Run(name, func(t *testing.T) {
			boom := stderrors.New("disk full")
			store := newFailSetStorage(boom)
			store.failKey = []byte("b") // only the second claim's persist faults
			tg := build(store)
			t.Cleanup(func() { _ = tg.closer.Close(context.Background()) })

			batch := []sm.Entry{
				{Index: 1, Cmd: buildKVCmd(kv.OpPutIfAbsent, []byte("a"), []byte("first"))},
				{Index: 2, Cmd: buildKVCmd(kv.OpPutIfAbsent, []byte("b"), []byte("second"))},
			}

			store.failing.Store(true)
			results := make([]sm.Result, len(batch))
			err := tg.update(context.Background(), batch, results)

			// HALT, not ack. resolveApplyError returns the fatal fault so Raft
			// stops Apply; the swallow mutant makes this nil.
			if err == nil {
				t.Fatalf("a mid-batch guarded persist fault was not returned; Apply would "+
					"advance lastApplied over a write that never reached disk (results=%v)", results)
			}
			if !stderrors.Is(err, boom) {
				t.Errorf("error = %v, want it to wrap the storage fault", err)
			}

			// The failed claim is not durable -- nothing acked a lost write.
			if got, _ := store.Get([]byte("b")); got != nil {
				t.Errorf("storage holds %q for the failed claim; a halted write must not be durable", got)
			}

			// Deterministic replay: re-applying the SAME committed batch after
			// the fault clears (what a restarted replica does from the log)
			// converges -- both keys durable. If the failed claim had been left
			// in cache instead of withdrawn, replay's PutIfAbsent("b") would
			// decline and storage["b"] would stay absent, failing below.
			store.failing.Store(false)
			replay := make([]sm.Result, len(batch))
			if rerr := tg.update(context.Background(), batch, replay); rerr != nil {
				t.Fatalf("replay of the committed batch failed: %v", rerr)
			}
			if av, _ := store.Get([]byte("a")); !bytes.Equal(av, []byte("first")) {
				t.Errorf("after replay storage[a] = %q, want %q", av, "first")
			}
			if bv, _ := store.Get([]byte("b")); !bytes.Equal(bv, []byte("second")) {
				t.Errorf("after replay storage[b] = %q, want %q — replay did not recover the halted write", bv, "second")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The guarded single-key verbs (go-qrdb#175)
// ---------------------------------------------------------------------------

// The guarded delete and swap are durable before they acknowledge, for the
// reason the guarded create is (owner ruling 2026-08-23, go-qrdb#94).
//
// protocol.ResultDeleted is the answer to "did I release the claim I was
// holding?". Acknowledging a release that has not reached disk means the key
// comes back after a restart, still holding a value its owner was told was
// gone -- and the owner has already moved on and let someone else take it.
// protocol.ResultSwapped is the same promise about a renewal.
//
// Backing storage is read with NO drain and NO Close: the point is that the
// write is already there when the apply returns, not that it arrives
// eventually. Draining first would pass against an asynchronous
// implementation.
func TestShardedStateMachines_GuardedVerbs_AreDurableBeforeTheyAcknowledge(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("CompareAndDelete", func(t *testing.T) {
				store := newMemStorage()
				target, _ := build(store)

				// Seeded through the guarded create, which is synchronous, so
				// the value is provably in storage before the delete runs.
				// Seeding with Put would race its own asynchronous write.
				if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine"))); err != nil {
					t.Fatalf("seed PutIfAbsent: %v", err)
				}

				res, err := target.apply(t, mustEncodeCAD(t, []byte("k"), []byte("mine")))
				if err != nil {
					t.Fatalf("CompareAndDelete: %v", err)
				}
				if res.Value != protocol.ResultDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultDeleted (%d)",
						res.Value, protocol.ResultDeleted)
				}

				got, gerr := store.Get([]byte("k"))
				if gerr != nil {
					t.Fatalf("storage.Get: %v", gerr)
				}
				if got != nil {
					t.Fatalf("storage still holds %q immediately after the acknowledgement — "+
						"the release was acknowledged before it was durable, so a restart "+
						"here resurrects a claim its owner has already given up", got)
				}
			})

			t.Run("CompareAndSwap", func(t *testing.T) {
				store := newMemStorage()
				target, _ := build(store)

				if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("v1"))); err != nil {
					t.Fatalf("seed PutIfAbsent: %v", err)
				}

				res, err := target.apply(t, mustEncodeCAS(t, []byte("k"), []byte("v1"), []byte("v2")))
				if err != nil {
					t.Fatalf("CompareAndSwap: %v", err)
				}
				if res.Value != protocol.ResultSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultSwapped (%d)",
						res.Value, protocol.ResultSwapped)
				}

				got, gerr := store.Get([]byte("k"))
				if gerr != nil {
					t.Fatalf("storage.Get: %v", gerr)
				}
				if !bytes.Equal(got, []byte("v2")) {
					t.Fatalf("storage holds %q immediately after the acknowledgement, want %q — "+
						"a renewal acknowledged before it was durable reverts on restart, and "+
						"the holder's next guarded verb is then refused", got, "v2")
				}
			})
		})
	}
}

// A guarded verb whose storage write fails REPORTS the failure rather than
// logging it, and does not report the write as having happened.
//
// This is the carve-out KVStateMachine.Update's contract documents -- an
// unrecoverable infrastructure error may be returned, unlike an
// application-level condition. Result.Value is checked as well as the error,
// because protocol.ResultDeleted alongside a non-nil error is still readable
// by a caller that checks only the value.
func TestShardedStateMachines_GuardedVerbs_ReportAFailedPersist(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("CompareAndDelete", func(t *testing.T) {
				boom := stderrors.New("disk full")
				store := newFailSetStorage(boom)
				target, _ := build(store)

				if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine"))); err != nil {
					t.Fatalf("seed PutIfAbsent: %v", err)
				}

				store.failDelete.Store(true)
				res, err := target.apply(t, mustEncodeCAD(t, []byte("k"), []byte("mine")))
				if err == nil {
					t.Fatalf("a failed persist was not reported: Result.Value = %d", res.Value)
				}
				if !stderrors.Is(err, boom) {
					t.Errorf("error = %v, want it to wrap the storage fault", err)
				}
				if res.Value == protocol.ResultDeleted {
					t.Error("Result.Value is protocol.ResultDeleted on a release that never reached disk")
				}
			})

			t.Run("CompareAndSwap", func(t *testing.T) {
				boom := stderrors.New("disk full")
				store := newFailSetStorage(boom)
				target, _ := build(store)

				if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("v1"))); err != nil {
					t.Fatalf("seed PutIfAbsent: %v", err)
				}

				store.failing.Store(true)
				res, err := target.apply(t, mustEncodeCAS(t, []byte("k"), []byte("v1"), []byte("v2")))
				if err == nil {
					t.Fatalf("a failed persist was not reported: Result.Value = %d", res.Value)
				}
				if !stderrors.Is(err, boom) {
					t.Errorf("error = %v, want it to wrap the storage fault", err)
				}
				if res.Value == protocol.ResultSwapped {
					t.Error("Result.Value is protocol.ResultSwapped on a swap that never reached disk")
				}
			})
		})
	}
}

// A guarded verb that did not persist restores the value the cache HELD, not
// merely the absence the conditional create restores.
//
// This is the case that made claim carry the prior entry instead of a boolean.
// The conditional create only ever claims an absent or tombstoned key, so
// withdrawing it by removing the cache entry was sufficient. The guarded
// delete and swap replace a LIVE value, and removing the entry drops it: the
// next read falls through to backing storage and answers with whatever is
// there.
//
// Making that observable needs the cache and storage to DISAGREE, which is a
// state a real deployment reaches on its own -- an ordinary Put persists
// asynchronously and swallows its storage error, so a failed Set leaves the
// cache ahead of storage with nothing reported. failKey narrows the fault to
// the one key so the seed can still be written durably first.
//
// Without the prior value, the failed swap below answers "A": a value two
// writes stale, silently restored under a caller who was told nothing changed.
func TestShardedStateMachines_GuardedVerbs_WithdrawRestoresThePriorValue(t *testing.T) {
	// seedDivergence leaves the shard cache holding "B" for key k while backing
	// storage still holds "A", and returns the target.
	seedDivergence := func(t *testing.T, build func(storage.Storage) (conformanceTarget, persistCloser), store *failSetStorage) conformanceTarget {
		t.Helper()
		store.failKey = []byte("k")
		target, _ := build(store)

		// The guarded create is synchronous, so "A" is provably in storage.
		if _, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("A"))); err != nil {
			t.Fatalf("seed PutIfAbsent: %v", err)
		}
		// Now every Set on this key fails. The Put moves the cache to "B" and
		// its background write fails silently, so storage keeps "A".
		store.failing.Store(true)
		if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("B"))); err != nil {
			t.Fatalf("diverging Put: %v", err)
		}
		return target
	}

	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("CompareAndSwap", func(t *testing.T) {
				store := newFailSetStorage(stderrors.New("disk full"))
				target := seedDivergence(t, build, store)

				// The guard matches the cache's "B" and the persist fails.
				if _, err := target.apply(t, mustEncodeCAS(t, []byte("k"), []byte("B"), []byte("C"))); err == nil {
					t.Fatal("a failed persist was not reported")
				}

				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("B")) {
					t.Fatalf("key holds %q after a withdrawn swap, want the untouched %q — "+
						"the rollback dropped the cache entry, so the key silently reverted "+
						"to a value backing storage still held", got, "B")
				}
			})

			t.Run("CompareAndDelete", func(t *testing.T) {
				store := newFailSetStorage(stderrors.New("disk full"))
				target := seedDivergence(t, build, store)

				store.failDelete.Store(true)
				if _, err := target.apply(t, mustEncodeCAD(t, []byte("k"), []byte("B"))); err == nil {
					t.Fatal("a failed persist was not reported")
				}

				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("B")) {
					t.Fatalf("key holds %q after a withdrawn delete, want the untouched %q — "+
						"the rollback dropped the cache entry, so the key silently reverted "+
						"to a value backing storage still held", got, "B")
				}
			})
		})
	}
}
