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
	"bytes"
	"context"
	"encoding/binary"
	stderrors "errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// The base KV opcode set every state machine in this package must apply.
//
// go-qrdb#86 was a PARTIAL opcode: kv.OpPutIfAbsent was implemented in
// pkg/store and both engines in pkg/engine but in none of the four state
// machines here, so a node on --sm-type memory applied Put and Delete and
// failed opcode 4 with "unknown opcode: 4". Because the identity index claims
// identities with PutIfAbsent, role creation could not apply and first boot
// could not complete — and nothing failed at BUILD time, because a handler map
// is data.
//
// Listing the set once, and asserting every state machine covers it, is what
// makes the next addition fail here rather than in a daemon.
var baseKVOpcodes = map[byte]string{
	kv.OpPut:         "kv.OpPut",
	kv.OpDelete:      "kv.OpDelete",
	kv.OpPutIfAbsent: "kv.OpPutIfAbsent",
}

// conformanceTarget is one state machine expressed as the three things the
// contract is stated in: apply an entry, read a key back, and expose the
// dispatch table.
type conformanceTarget struct {
	apply    func(t *testing.T, cmd []byte) (sm.Result, error)
	lookup   func(t *testing.T, key []byte) []byte
	handlers map[byte]updateHandler
}

// conformanceTargets builds every state machine under test. Adding a state
// machine to the package without adding it here leaves it unpinned, so the
// list is deliberately exhaustive rather than representative.
func conformanceTargets() map[string]func(t *testing.T) conformanceTarget {
	return map[string]func(t *testing.T) conformanceTarget{
		"KVStateMachine": func(t *testing.T) conformanceTarget {
			t.Helper()
			backend := NewHighPerfBackend(nil)
			k := NewKVStateMachine(backend)
			return conformanceTarget{
				apply: func(t *testing.T, cmd []byte) (sm.Result, error) {
					t.Helper()
					results := make([]sm.Result, 1)
					err := k.Update([]sm.Entry{{Index: 1, Cmd: cmd}}, results)
					return results[0], err
				},
				lookup: func(t *testing.T, key []byte) []byte {
					t.Helper()
					v, err := backend.Get(key)
					if err != nil {
						t.Fatalf("backend.Get: %v", err)
					}
					return v
				},
				handlers: k.handlers,
			}
		},
		"RegularStateMachine": func(t *testing.T) conformanceTarget {
			t.Helper()
			r := NewRegularStateMachine()
			return conformanceTarget{
				apply:    updaterWithContext(r.Update),
				lookup:   lookupWithContext(r.Lookup),
				handlers: r.handlers,
			}
		},
		"FastStateMachine": func(t *testing.T) conformanceTarget {
			t.Helper()
			f := NewFastStateMachine(1, 1, newMemStorage())
			return conformanceTarget{
				apply:    updaterWithContext(f.Update),
				lookup:   lookupWithContext(f.Lookup),
				handlers: f.handlers,
			}
		},
		"ConcurrentStateMachine": func(t *testing.T) conformanceTarget {
			t.Helper()
			c := NewConcurrentStateMachine(1, 1, newMemStorage())
			return conformanceTarget{
				apply:    updaterWithContext(c.Update),
				lookup:   lookupWithContext(c.Lookup),
				handlers: c.handlers,
			}
		},
	}
}

func updaterWithContext(
	update func(context.Context, []sm.Entry, []sm.Result) error,
) func(*testing.T, []byte) (sm.Result, error) {
	return func(t *testing.T, cmd []byte) (sm.Result, error) {
		t.Helper()
		results := make([]sm.Result, 1)
		err := update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results)
		return results[0], err
	}
}

func lookupWithContext(
	lookup func(context.Context, interface{}) (interface{}, error),
) func(*testing.T, []byte) []byte {
	return func(t *testing.T, key []byte) []byte {
		t.Helper()
		got, err := lookup(context.Background(), key)
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if got == nil {
			return nil
		}
		v, ok := got.([]byte)
		if !ok {
			t.Fatalf("Lookup returned %T, want []byte", got)
		}
		return v
	}
}

// buildKVCmd encodes [op][keyLen:2 LE][key][value], the base KV command shape.
func buildKVCmd(op byte, key, value []byte) []byte {
	cmd := make([]byte, 3+len(key)+len(value))
	cmd[0] = op
	binary.LittleEndian.PutUint16(cmd[1:3], uint16(len(key)))
	copy(cmd[3:], key)
	copy(cmd[3+len(key):], value)
	return cmd
}

// Every state machine dispatches every base KV opcode.
//
// This is a structural assertion on purpose. The behavioural tests below would
// also catch a missing PutIfAbsent, but only for the opcodes someone remembered
// to write a test for; this one fails for any opcode added to baseKVOpcodes and
// wired into some state machines and not others, which is the shape of #86.
func TestStateMachines_DispatchEveryBaseKVOpcode(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			target := build(t)
			for op, opName := range baseKVOpcodes {
				if _, ok := target.handlers[op]; !ok {
					t.Errorf("%s has no handler for %s (opcode %d): an entry carrying it "+
						"fails apply with InvalidOpcodeError, which stalls the caller "+
						"rather than failing the build", name, opName, op)
				}
			}
		})
	}
}

// Every state machine honours the same PutIfAbsent contract.
func TestStateMachines_PutIfAbsentContract(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("stores an absent key", func(t *testing.T) {
				target := build(t)
				res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != kv.ResultStored {
					t.Fatalf("Result.Value = %d, want kv.ResultStored (%d)", res.Value, kv.ResultStored)
				}
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("mine")) {
					t.Errorf("stored value = %q, want %q", got, "mine")
				}
			})

			t.Run("declines a present key and preserves the holder", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("holder"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine")))
				// A declined conditional write is an ANSWER, not a fault. Returning
				// an error from Update would stop Raft advancing lastApplied and
				// stall every subsequent linearizable read.
				if err != nil {
					t.Fatalf("Update returned an error for a declined write: %v", err)
				}
				if res.Value != kv.ResultNotStored {
					t.Fatalf("Result.Value = %d, want kv.ResultNotStored (%d)", res.Value, kv.ResultNotStored)
				}
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("holder")) {
					t.Errorf("stored value = %q, want the holder's %q — a declined "+
						"write overwrote the value it was supposed to protect", got, "holder")
				}
			})

			t.Run("admits exactly one of several contenders", func(t *testing.T) {
				target := build(t)
				// Applied in sequence, as a replicated log delivers them: the
				// guard, not the arrival order, is what makes this one winner.
				stored := 0
				for i := 0; i < 8; i++ {
					res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte{byte('a' + i)}))
					if err != nil {
						t.Fatalf("contender %d: %v", i, err)
					}
					if res.Value == kv.ResultStored {
						stored++
					}
				}
				if stored != 1 {
					t.Fatalf("%d of 8 contenders stored, want exactly 1", stored)
				}
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("a")) {
					t.Errorf("stored value = %q, want the first contender's %q", got, "a")
				}
			})

			t.Run("re-creates a deleted key", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("first"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}
				if _, err := target.apply(t, buildKVCmd(kv.OpDelete, []byte("k"), nil)); err != nil {
					t.Fatalf("Delete: %v", err)
				}

				// The sharded caches record a delete as a nil TOMBSTONE rather
				// than removing the entry, so a guard that read presence in the
				// map as existence would refuse this forever.
				res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("second")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != kv.ResultStored {
					t.Fatalf("Result.Value = %d, want kv.ResultStored (%d) — a deleted "+
						"key is absent and must be storable again", res.Value, kv.ResultStored)
				}
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("second")) {
					t.Errorf("stored value = %q, want %q", got, "second")
				}
			})

			t.Run("rejects a truncated payload", func(t *testing.T) {
				target := build(t)
				// keyLen claims 9 bytes; only 1 follows.
				cmd := []byte{kv.OpPutIfAbsent, 0x09, 0x00, 'k'}
				if _, err := target.apply(t, cmd); err == nil {
					t.Fatal("a truncated PutIfAbsent was accepted")
				}
			})
		})
	}
}

// The read-through and storage-fault branches of the sharded caches.
//
// These are separate from the conformance table above because they cannot be
// reached through it: every table case seeds its key by APPLYING an entry,
// which populates the cache, so the guard always answers from memory and the
// cache-miss path in setIfAbsentLocked never runs. That is the gap Codex
// caught on this PR — the two branches carrying the most reasoning were the
// two with no coverage, and a regression in either (including the documented
// fail-open one) would have passed the suite (go-qrdb#86).
//
// Only FastStateMachine and ConcurrentStateMachine have this path; the other
// two hold no cache in front of their backend.
type shardedCacheTarget struct {
	build  func(storage.Storage) conformanceTarget
	shards string
}

func shardedCacheTargets() map[string]func(storage.Storage) conformanceTarget {
	return map[string]func(storage.Storage) conformanceTarget{
		"FastStateMachine": func(store storage.Storage) conformanceTarget {
			f := NewFastStateMachine(1, 1, store)
			return conformanceTarget{
				apply:    updaterWithContext(f.Update),
				lookup:   lookupWithContext(f.Lookup),
				handlers: f.handlers,
			}
		},
		"ConcurrentStateMachine": func(store storage.Storage) conformanceTarget {
			c := NewConcurrentStateMachine(1, 1, store)
			return conformanceTarget{
				apply:    updaterWithContext(c.Update),
				lookup:   lookupWithContext(c.Lookup),
				handlers: c.handlers,
			}
		},
	}
}

// A key present only in BACKING STORAGE is present.
//
// This is the state a replica is in after recovering from a snapshot: the
// keys exist in storage and the shard cache is empty. A guard that answered
// from the cache alone would read every recovered key as absent and overwrite
// it — silently destroying state that survived the restart, which for the
// identity index means handing out a claim someone else already holds.
func TestShardedStateMachines_PutIfAbsent_ReadsThroughToStorage(t *testing.T) {
	for name, build := range shardedCacheTargets() {
		t.Run(name, func(t *testing.T) {
			store := newMemStorage()
			// Seeded DIRECTLY into storage, never through the state machine,
			// so the shard cache has no entry for it.
			if err := store.Set([]byte("recovered"), []byte("holder")); err != nil {
				t.Fatalf("seed storage: %v", err)
			}

			target := build(store)
			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("recovered"), []byte("mine")))
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if res.Value != kv.ResultNotStored {
				t.Fatalf("Result.Value = %d, want kv.ResultNotStored (%d) — a key held "+
					"only in storage was read as absent", res.Value, kv.ResultNotStored)
			}

			stored, gerr := store.Get([]byte("recovered"))
			if gerr != nil {
				t.Fatalf("storage.Get: %v", gerr)
			}
			if !bytes.Equal(stored, []byte("holder")) {
				t.Errorf("storage value = %q, want the holder's %q — the guard "+
					"overwrote recovered state", stored, "holder")
			}
		})
	}
}

// A storage FAULT is a fault, not an absence.
//
// Reading a failed Get as "absent" would overwrite the holder and report
// ResultStored. Worse, the fault is LOCAL to one replica: replicas whose
// reads succeeded would decline the entry this one stored, and the group
// diverges from an identical log. A non-deterministic fault is exactly the
// case a state machine must hand back as a Go error rather than decide.
func TestShardedStateMachines_PutIfAbsent_PropagatesAStorageFault(t *testing.T) {
	for name, build := range shardedCacheTargets() {
		t.Run(name, func(t *testing.T) {
			boom := stderrors.New("storage get failed")
			store := &gapErrGetStorage{memStorage: newMemStorage(), getErr: boom}

			target := build(store)
			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("mine")))
			if err == nil {
				t.Fatalf("a failed storage read was treated as absence: Result.Value = %d", res.Value)
			}
			if !stderrors.Is(err, boom) {
				t.Errorf("error = %v, want it to wrap the storage fault", err)
			}

			// Nothing may be cached or persisted on the fault path: the guard
			// never established the key was absent.
			//
			// The cache is observed by HEALING the storage first and then
			// reading. Reading through the still-faulting store would fail on
			// the storage error and tell us nothing about the cache, and
			// reaching into the shard map directly would need one accessor per
			// state machine for a fact Lookup already reports.
			store.getErr = nil
			if got := target.lookup(t, []byte("k")); got != nil {
				t.Errorf("value %q was cached despite the guard never completing", got)
			}
			if v, ok := store.memStorage.data["k"]; ok {
				t.Errorf("value %q was persisted despite the guard never completing", v)
			}
		})
	}
}

// reorderingStorage delays the FIRST storage operation until released, so a
// later one is free to overtake it.
//
// This is the condition, not a stand-in for it: the two sharded state machines
// persist on independent goroutines, and independent goroutines racing is
// exactly how a delete lands after the write that replaced it. Blocking the
// first operation makes the inversion happen on demand instead of waiting for
// a scheduler that obliges once in a thousand runs (go-qrdb#86).
type reorderingStorage struct {
	*memStorage

	release chan struct{}
	held    chan struct{}
	// first claims the one operation that gets held. An atomic flag rather
	// than sync.Once: Once.Do BLOCKS later callers until the first returns,
	// so it would serialise every write behind the held one and the double
	// would enforce the very ordering it exists to violate. Measured — with
	// Once, removing the ordering under test still passed.
	first atomic.Bool

	mu    sync.Mutex
	order []string // storage operations, in the order storage saw them
}

func newReorderingStorage() *reorderingStorage {
	return &reorderingStorage{
		memStorage: newMemStorage(),
		release:    make(chan struct{}),
		held:       make(chan struct{}),
	}
}

// hold blocks the FIRST operation to arrive and announces that it is blocked.
// Every later operation passes straight through, free to overtake it.
func (s *reorderingStorage) hold() {
	if s.first.CompareAndSwap(false, true) {
		close(s.held)
		<-s.release
	}
}

// snapshotOrder returns a copy of the operations storage has seen, in order.
func (s *reorderingStorage) snapshotOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// equalOrder compares two operation sequences.
func equalOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (s *reorderingStorage) record(op string) {
	s.mu.Lock()
	s.order = append(s.order, op)
	s.mu.Unlock()
}

func (s *reorderingStorage) Set(key, value []byte) error {
	s.hold()
	s.record("set:" + string(key))
	return s.memStorage.Set(key, value)
}

func (s *reorderingStorage) Delete(key []byte) error {
	s.hold()
	s.record("delete:" + string(key))
	return s.memStorage.Delete(key)
}

// closer is the state machine half these tests need to drain.
type persistCloser interface {
	Close(ctx context.Context) error
}

// applyResult carries an apply's outcome back from its own goroutine.
type applyResult struct {
	res sm.Result
	err error
}

// applyAsync drives one apply on its own goroutine.
//
// The guarded verbs persist synchronously (go-qrdb#94), so an apply that has
// writes held ahead of it -- or whose own write is the held one -- cannot
// return until the test goroutine releases them. Every reordering test below
// therefore drives the guarded apply here and releases from the test
// goroutine.
//
// Nothing here reports a failure: conformanceTarget.apply returns (result,
// error) and never calls t.Fatalf, which matters because t.Fatalf ends only
// the goroutine it runs on -- a failure raised here would leave the test
// goroutine waiting on a channel that never receives. The outcome travels
// back and awaitApply judges it.
func applyAsync(t *testing.T, target conformanceTarget, cmd []byte) <-chan applyResult {
	t.Helper()
	done := make(chan applyResult, 1)
	go func() {
		res, err := target.apply(t, cmd)
		done <- applyResult{res: res, err: err}
	}()
	return done
}

// awaitApply waits for an applyAsync outcome under a bound and fails the test
// if it does not arrive.
//
// The bound is on the WAIT, not asserted after the call returns: a regression
// that stopped releasing the guarded write would otherwise hang the package
// until go test's own timeout killed it, and the failure would read as an
// infrastructure problem rather than as this test.
func awaitApply(t *testing.T, done <-chan applyResult) sm.Result {
	t.Helper()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("apply: %v", got.err)
		}
		return got.res
	case <-time.After(30 * time.Second):
		t.Fatal("the apply never returned — the guarded write is not being released")
		return sm.Result{}
	}
}

func shardedPersistTargets() map[string]func(storage.Storage) (conformanceTarget, persistCloser) {
	return map[string]func(storage.Storage) (conformanceTarget, persistCloser){
		"FastStateMachine": func(store storage.Storage) (conformanceTarget, persistCloser) {
			f := NewFastStateMachine(1, 1, store)
			return conformanceTarget{
				apply:    updaterWithContext(f.Update),
				lookup:   lookupWithContext(f.Lookup),
				handlers: f.handlers,
			}, f
		},
		"ConcurrentStateMachine": func(store storage.Storage) (conformanceTarget, persistCloser) {
			c := NewConcurrentStateMachine(1, 1, store)
			return conformanceTarget{
				apply:    updaterWithContext(c.Update),
				lookup:   lookupWithContext(c.Lookup),
				handlers: c.handlers,
			}, c
		},
	}
}

// An accepted PutIfAbsent survives a slow Delete ahead of it.
//
// Delete(k) then PutIfAbsent(k, "new"): the guarded write returns ResultStored
// and the cache holds "new". If the delete's storage goroutine finishes LAST,
// backing storage ends with no key at all — and the accepted write is gone at
// the next restart, with nothing having reported a fault. Reading through the
// cache cannot see this; only storage can.
func TestShardedStateMachines_PutIfAbsent_SurvivesASlowDeleteAhead(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newReorderingStorage()
			target, closer := build(store)

			if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("old"))); err != nil {
				t.Fatalf("seed Put: %v", err)
			}
			if _, err := target.apply(t, buildKVCmd(kv.OpDelete, []byte("k"), nil)); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			// The seed Put's storage write is the one being held.
			<-store.held

			// The guarded apply runs OFF the test goroutine because it is now
			// synchronous (go-qrdb#94): it waits for the writes queued ahead of
			// it, and those are blocked on a release only this goroutine can
			// perform. Driving it inline would deadlock the test rather than
			// exercise the ordering.
			done := applyAsync(t, target, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("new")))

			// Everything is now queued behind the held write; let it all go.
			close(store.release)

			res := awaitApply(t, done)
			if res.Value != kv.ResultStored {
				t.Fatalf("Result.Value = %d, want kv.ResultStored (%d)", res.Value, kv.ResultStored)
			}

			if cerr := closer.Close(context.Background()); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}

			// The ORDER storage saw is asserted, not just the value it ended
			// with. The final value depends on which of the unblocked writers
			// wins, so it detects the defect only most of the time; the order
			// is the property itself and is exact. Applied: Put, Delete,
			// PutIfAbsent.
			if want := []string{"set:k", "delete:k", "set:k"}; !equalOrder(store.snapshotOrder(), want) {
				t.Fatalf("storage saw %v, want %v — writes reached storage out of "+
					"apply order, so an accepted conditional write can be erased by "+
					"a delete that landed after it", store.snapshotOrder(), want)
			}

			got, gerr := store.memStorage.Get([]byte("k"))
			if gerr != nil {
				t.Fatalf("storage.Get: %v", gerr)
			}
			if !bytes.Equal(got, []byte("new")) {
				t.Fatalf("storage holds %q, want %q — an accepted conditional write "+
					"was erased by a delete that persisted after it; storage order = %v",
					got, "new", store.snapshotOrder())
			}
		})
	}
}

// A Put after an accepted PutIfAbsent is the value that survives.
//
// The inversion in the other direction: if the guarded write's storage goroutine
// finishes after the Put that replaced it, storage keeps the older value while
// the cache holds the newer one, and a restart silently rolls the record back.
func TestShardedStateMachines_PutAfterPutIfAbsent_PersistsLast(t *testing.T) {
	for name, build := range shardedPersistTargets() {
		t.Run(name, func(t *testing.T) {
			store := newReorderingStorage()
			target, closer := build(store)

			// The guarded apply runs OFF the test goroutine: its own storage
			// set is the one reorderingStorage holds, and the guarded write is
			// synchronous now (go-qrdb#94), so it cannot return until this
			// goroutine releases it.
			done := applyAsync(t, target, buildKVCmd(kv.OpPutIfAbsent, []byte("k"), []byte("first")))

			// The guarded write's storage set is the one being held.
			<-store.held

			// Applied while the guarded write is still mid-flight, so the Put
			// really is racing it rather than following a settled state.
			if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("second"))); err != nil {
				t.Fatalf("Put: %v", err)
			}

			close(store.release)

			res := awaitApply(t, done)
			if res.Value != kv.ResultStored {
				t.Fatalf("Result.Value = %d, want kv.ResultStored", res.Value)
			}

			if cerr := closer.Close(context.Background()); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}

			// Applied: PutIfAbsent, Put.
			if want := []string{"set:k", "set:k"}; !equalOrder(store.snapshotOrder(), want) {
				t.Fatalf("storage saw %d writes %v, want %d in apply order",
					len(store.snapshotOrder()), store.snapshotOrder(), len(want))
			}

			got, gerr := store.memStorage.Get([]byte("k"))
			if gerr != nil {
				t.Fatalf("storage.Get: %v", gerr)
			}
			if !bytes.Equal(got, []byte("second")) {
				t.Fatalf("storage holds %q, want %q — an older write persisted last "+
					"and rolled the record back; storage order = %v",
					got, "second", store.snapshotOrder())
			}
		})
	}
}
