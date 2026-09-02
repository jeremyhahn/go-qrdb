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
	stderrors "errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// The guarded single-key opcodes every state machine in this package must
// apply.
//
// go-qrdb#175 was the second PARTIAL opcode of the same shape as go-qrdb#86:
// pkg/store/pebble.go and both pkg/engine engines dispatched 66 and 67, and
// none of the four state machines here did. Measured on this branch before the
// fix, applying an ordinary CompareAndDelete produced
// `statemachine: unknown opcode: 66` on FastStateMachine and on
// KVStateMachine, encoded into Result.Data — so the caller was refused a verb
// the cluster advertises and the key was never released.
//
// Listing the set once and asserting every state machine covers it is what
// makes the NEXT addition fail here rather than in a daemon.
var guardedOpcodes = map[byte]string{
	protocol.OpCompareAndDelete: "protocol.OpCompareAndDelete",
	protocol.OpCompareAndSwap:   "protocol.OpCompareAndSwap",
}

// Every state machine dispatches every guarded single-key opcode.
//
// Structural on purpose, exactly as TestStateMachines_DispatchEveryBaseKVOpcode
// is: the behavioural tests below would also catch a missing handler, but only
// on the state machines someone remembered to add to the table. This one fails
// for any opcode added to guardedOpcodes and wired into some maps and not
// others, which is the shape of both #86 and #175.
func TestStateMachines_DispatchEveryGuardedOpcode(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			target := build(t)
			for op, opName := range guardedOpcodes {
				if _, ok := target.handlers[op]; !ok {
					t.Errorf("%s has no handler for %s (opcode %d): an entry carrying it "+
						"fails apply with InvalidOpcodeError, so the caller is refused a "+
						"verb the cluster advertises and no build breaks to say so",
						name, opName, op)
				}
			}
		})
	}
}

// applyGuarded applies a guarded command and fails if the state machine
// REFUSED it rather than executing it.
//
// This check is what keeps every assertion below from being vacuous.
// protocol.ResultNotDeleted and protocol.ResultNotSwapped are both 0, and a
// refused opcode also yields a zero Result.Value — so "declines a mismatch"
// and every other negative assertion passes just as well against a state
// machine that has no handler for the opcode at all. The refusal itself
// travels in Result.Data (resolveApplyError encodes it there so the proposer
// can read it), and that is the only place the two outcomes differ.
//
// Measured on this branch: with the four handler-map entries removed, the
// contract tests pass WITHOUT this check and fail with it, naming
// "unknown opcode: 66".
func applyGuarded(t *testing.T, target conformanceTarget, cmd []byte) (sm.Result, error) {
	t.Helper()
	res, err := target.apply(t, cmd)
	if msg, refused := protocol.DecodeAppError(res.Data); refused {
		t.Fatalf("the state machine REFUSED the guarded verb instead of executing it: %s", msg)
	}
	return res, err
}

// mustEncodeCAD encodes a conditional-delete command.
func mustEncodeCAD(t *testing.T, key, expected []byte) []byte {
	t.Helper()
	cmd, err := protocol.EncodeCompareAndDelete(key, expected)
	if err != nil {
		t.Fatalf("EncodeCompareAndDelete: %v", err)
	}
	return cmd
}

// mustEncodeCAS encodes a conditional-replace command.
func mustEncodeCAS(t *testing.T, key, expected, next []byte) []byte {
	t.Helper()
	cmd, err := protocol.EncodeCompareAndSwap(key, expected, next)
	if err != nil {
		t.Fatalf("EncodeCompareAndSwap: %v", err)
	}
	return cmd
}

// Every state machine honours the same CompareAndDelete contract.
func TestStateMachines_CompareAndDeleteContract(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("removes a key holding the expected value", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("mine"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), []byte("mine")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultDeleted (%d)",
						res.Value, protocol.ResultDeleted)
				}
				if got := target.lookup(t, []byte("k")); got != nil {
					t.Errorf("key still holds %q after a matched guarded delete", got)
				}
			})

			t.Run("declines a mismatch and preserves the holder", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("theirs"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), []byte("mine")))
				// A declined guarded delete is an ANSWER, not a fault. Returning
				// an error from Update would stop Raft advancing lastApplied and
				// stall every subsequent linearizable read.
				if err != nil {
					t.Fatalf("Update returned an error for a declined delete: %v", err)
				}
				if res.Value != protocol.ResultNotDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotDeleted (%d)",
						res.Value, protocol.ResultNotDeleted)
				}
				// The load-bearing assertion. This is go-qrdb#41 itself: a
				// releaser holding a stale read must not remove the value a
				// third party legitimately took.
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("theirs")) {
					t.Errorf("stored value = %q, want the holder's %q — a declined guarded "+
						"delete removed the value it was supposed to protect", got, "theirs")
				}
			})

			t.Run("declines an absent key", func(t *testing.T) {
				target := build(t)
				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), []byte("mine")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultNotDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotDeleted (%d) — "+
						"an absent key matches no expectation", res.Value, protocol.ResultNotDeleted)
				}
			})

			t.Run("declines a deleted key", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("mine"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}
				if _, err := target.apply(t, buildKVCmd(kv.OpDelete, []byte("k"), nil)); err != nil {
					t.Fatalf("Delete: %v", err)
				}

				// The sharded caches record a delete as a nil TOMBSTONE rather
				// than removing the entry. A guard that read presence in the
				// map as existence would compare the caller's expectation
				// against nil and, for a nil-safe bytes.Equal on an empty
				// expectation, delete a key that does not exist.
				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), []byte("mine")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultNotDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotDeleted (%d) — "+
						"a tombstoned key is absent", res.Value, protocol.ResultNotDeleted)
				}
			})

			t.Run("an empty expectation does not match an absent key", func(t *testing.T) {
				target := build(t)
				// bytes.Equal(nil, []byte{}) is TRUE, so a guard written as a
				// bare bytes.Equal would delete a key that does not exist and
				// report ResultDeleted. That is not a rounding error: the
				// caller is told it released a claim it never held.
				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), nil))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultNotDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotDeleted (%d) — "+
						"an absent key was matched by an empty expectation",
						res.Value, protocol.ResultNotDeleted)
				}
			})

			t.Run("an empty expectation matches a key holding an empty value", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), nil)); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				// The other half of the pair above: absence and a stored empty
				// value must stay distinguishable in BOTH directions, or the
				// guard is unusable for callers whose values can be empty.
				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), nil))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultDeleted (%d) — a key "+
						"holding an empty value was read as absent",
						res.Value, protocol.ResultDeleted)
				}
			})

			t.Run("rejects a truncated payload", func(t *testing.T) {
				target := build(t)
				// keyLen claims 9 bytes; only 1 follows.
				cmd := []byte{protocol.OpCompareAndDelete, 0x09, 0x00, 'k'}
				if _, err := target.apply(t, cmd); err == nil {
					t.Fatal("a truncated CompareAndDelete was accepted")
				}
			})
		})
	}
}

// Every state machine honours the same CompareAndSwap contract.
func TestStateMachines_CompareAndSwapContract(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("replaces a key holding the expected value", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("v1"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				res, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), []byte("v1"), []byte("v2")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultSwapped (%d)",
						res.Value, protocol.ResultSwapped)
				}
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("v2")) {
					t.Errorf("stored value = %q, want %q", got, "v2")
				}
			})

			t.Run("declines a mismatch and preserves the holder", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("theirs"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				res, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), []byte("mine"), []byte("v2")))
				if err != nil {
					t.Fatalf("Update returned an error for a declined swap: %v", err)
				}
				if res.Value != protocol.ResultNotSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotSwapped (%d)",
						res.Value, protocol.ResultNotSwapped)
				}
				if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("theirs")) {
					t.Errorf("stored value = %q, want the holder's %q — a declined guarded "+
						"swap overwrote the value it was supposed to protect", got, "theirs")
				}
			})

			t.Run("does not create an absent key", func(t *testing.T) {
				target := build(t)
				res, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), nil, []byte("v2")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultNotSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotSwapped (%d)",
						res.Value, protocol.ResultNotSwapped)
				}
				// A swap moves a record between two states, and a record that
				// does not exist has no first state. Creating here would give
				// callers a second, unguarded way to claim a key and undo what
				// kv.OpPutIfAbsent decides.
				if got := target.lookup(t, []byte("k")); got != nil {
					t.Errorf("guarded swap CREATED the key with %q; creation is "+
						"kv.OpPutIfAbsent's job", got)
				}
			})

			t.Run("declines a deleted key", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("v1"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}
				if _, err := target.apply(t, buildKVCmd(kv.OpDelete, []byte("k"), nil)); err != nil {
					t.Fatalf("Delete: %v", err)
				}

				res, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), []byte("v1"), []byte("v2")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultNotSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotSwapped (%d) — "+
						"a tombstoned key is absent", res.Value, protocol.ResultNotSwapped)
				}
			})

			t.Run("swaps to an empty value without deleting the key", func(t *testing.T) {
				target := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("v1"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}

				res, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), []byte("v1"), nil))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultSwapped (%d)",
						res.Value, protocol.ResultSwapped)
				}
				// The sharded caches spell a delete as a nil cache entry, so a
				// guard that inferred "delete" from an empty replacement would
				// turn this swap into a removal — and the next guarded verb on
				// the key would then read it as absent. The key must still be
				// present, holding nothing.
				got := target.lookup(t, []byte("k"))
				if got == nil {
					t.Fatal("a swap to an empty value DELETED the key; an empty replacement " +
						"is a value, and removal is protocol.OpCompareAndDelete's job")
				}
				if len(got) != 0 {
					t.Errorf("stored value = %q, want an empty value", got)
				}
			})

			t.Run("rejects a truncated payload", func(t *testing.T) {
				target := build(t)
				// keyLen claims 9 bytes; only 1 follows.
				cmd := []byte{protocol.OpCompareAndSwap, 0x09, 0x00, 'k'}
				if _, err := target.apply(t, cmd); err == nil {
					t.Fatal("a truncated CompareAndSwap was accepted")
				}
			})
		})
	}
}

// The guarded verbs compose with the guarded create into one primitive.
//
// This is the sequence the identity index runs and the reason all three
// opcodes exist: claim a key, edit it under the claim, release it under the
// claim, and have a contender who read the ORIGINAL value be refused at every
// step. Testing the verbs only in isolation would miss that the three agree on
// what "the key holds this value" means — which is precisely what a
// tombstone-versus-absence disagreement between them would break.
func TestStateMachines_GuardedVerbsComposeIntoOneClaim(t *testing.T) {
	for name, build := range conformanceTargets() {
		t.Run(name, func(t *testing.T) {
			target := build(t)

			res, err := target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("lock"), []byte("A")))
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if res.Value != kv.ResultStored {
				t.Fatalf("claim: Result.Value = %d, want kv.ResultStored (%d)", res.Value, kv.ResultStored)
			}

			// A contender that read nothing cannot take the claim.
			res, err = target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("lock"), []byte("B")))
			if err != nil {
				t.Fatalf("contender claim: %v", err)
			}
			if res.Value != kv.ResultNotStored {
				t.Fatalf("contender took a held claim: Result.Value = %d", res.Value)
			}

			// The holder renews under its own value.
			res, err = applyGuarded(t, target, mustEncodeCAS(t, []byte("lock"), []byte("A"), []byte("A2")))
			if err != nil {
				t.Fatalf("renew: %v", err)
			}
			if res.Value != protocol.ResultSwapped {
				t.Fatalf("renew: Result.Value = %d, want protocol.ResultSwapped (%d)",
					res.Value, protocol.ResultSwapped)
			}

			// A contender holding the PRE-renewal value is now stale and must
			// be refused both ways round. This is go-qrdb#41: without it the
			// stale releaser removes the renewed holder's value.
			res, err = applyGuarded(t, target, mustEncodeCAD(t, []byte("lock"), []byte("A")))
			if err != nil {
				t.Fatalf("stale release: %v", err)
			}
			if res.Value != protocol.ResultNotDeleted {
				t.Fatalf("a stale releaser removed the renewed claim: Result.Value = %d", res.Value)
			}
			res, err = applyGuarded(t, target, mustEncodeCAS(t, []byte("lock"), []byte("A"), []byte("B")))
			if err != nil {
				t.Fatalf("stale swap: %v", err)
			}
			if res.Value != protocol.ResultNotSwapped {
				t.Fatalf("a stale swapper overwrote the renewed claim: Result.Value = %d", res.Value)
			}
			if got := target.lookup(t, []byte("lock")); !bytes.Equal(got, []byte("A2")) {
				t.Fatalf("claim holds %q, want the renewed %q", got, "A2")
			}

			// The holder releases under the current value, and only then can
			// the contender claim.
			res, err = applyGuarded(t, target, mustEncodeCAD(t, []byte("lock"), []byte("A2")))
			if err != nil {
				t.Fatalf("release: %v", err)
			}
			if res.Value != protocol.ResultDeleted {
				t.Fatalf("release: Result.Value = %d, want protocol.ResultDeleted (%d)",
					res.Value, protocol.ResultDeleted)
			}
			res, err = target.apply(t, buildKVCmd(kv.OpPutIfAbsent, []byte("lock"), []byte("B")))
			if err != nil {
				t.Fatalf("re-claim: %v", err)
			}
			if res.Value != kv.ResultStored {
				t.Fatalf("a released claim could not be retaken: Result.Value = %d — the "+
					"guarded delete and the guarded create disagree about what absence is",
					res.Value)
			}
		})
	}
}

// A key present only in BACKING STORAGE is present.
//
// This is the state a replica is in after recovering from a snapshot: the keys
// exist in storage and the shard cache is empty. A guard that compared against
// the cache alone would read every recovered key as absent and refuse every
// guarded verb on it — so a lock taken before the restart could never be
// renewed or released, and the caller would see a permanent, silent "value
// does not match".
//
// Separate from the conformance table above because it cannot be reached
// through it: every table case seeds its key by APPLYING an entry, which
// populates the cache, so the read-through branch never runs (the same gap
// Codex caught on the go-qrdb#86 PR).
func TestShardedStateMachines_GuardedVerbs_ReadThroughToStorage(t *testing.T) {
	for name, build := range shardedCacheTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("CompareAndDelete", func(t *testing.T) {
				store := newMemStorage()
				// Seeded DIRECTLY into storage, never through the state
				// machine, so the shard cache has no entry for it.
				if err := store.Set([]byte("recovered"), []byte("holder")); err != nil {
					t.Fatalf("seed storage: %v", err)
				}

				target := build(store)
				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("recovered"), []byte("holder")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultDeleted (%d) — a key held "+
						"only in storage was read as absent", res.Value, protocol.ResultDeleted)
				}
				if got := target.lookup(t, []byte("recovered")); got != nil {
					t.Errorf("key still resolves to %q after a matched guarded delete", got)
				}
			})

			t.Run("CompareAndSwap", func(t *testing.T) {
				store := newMemStorage()
				if err := store.Set([]byte("recovered"), []byte("holder")); err != nil {
					t.Fatalf("seed storage: %v", err)
				}

				target := build(store)
				res, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("recovered"), []byte("holder"), []byte("next")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultSwapped {
					t.Fatalf("Result.Value = %d, want protocol.ResultSwapped (%d) — a key held "+
						"only in storage was read as absent", res.Value, protocol.ResultSwapped)
				}
				if got := target.lookup(t, []byte("recovered")); !bytes.Equal(got, []byte("next")) {
					t.Errorf("stored value = %q, want %q", got, "next")
				}
			})

			t.Run("a mismatch against recovered state preserves it", func(t *testing.T) {
				store := newMemStorage()
				if err := store.Set([]byte("recovered"), []byte("holder")); err != nil {
					t.Fatalf("seed storage: %v", err)
				}

				target := build(store)
				res, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("recovered"), []byte("stale")))
				if err != nil {
					t.Fatalf("Update: %v", err)
				}
				if res.Value != protocol.ResultNotDeleted {
					t.Fatalf("Result.Value = %d, want protocol.ResultNotDeleted (%d)",
						res.Value, protocol.ResultNotDeleted)
				}
				stored, gerr := store.Get([]byte("recovered"))
				if gerr != nil {
					t.Fatalf("storage.Get: %v", gerr)
				}
				if !bytes.Equal(stored, []byte("holder")) {
					t.Errorf("storage value = %q, want the holder's %q — a declined guarded "+
						"delete removed recovered state", stored, "holder")
				}
			})
		})
	}
}

// A storage FAULT is a fault, not a mismatch.
//
// Reading a failed Get as "the value differs" would refuse a delete or swap
// the caller is entitled to. Worse, the fault is LOCAL to one replica:
// replicas whose reads succeeded would apply the entry this one declined, and
// the group diverges from an identical log. A non-deterministic fault is
// exactly the case a state machine must hand back as a Go error rather than
// decide.
func TestShardedStateMachines_GuardedVerbs_PropagateAStorageFault(t *testing.T) {
	for _, verb := range []struct {
		name string
		cmd  func(t *testing.T) []byte
	}{
		{"CompareAndDelete", func(t *testing.T) []byte {
			return mustEncodeCAD(t, []byte("k"), []byte("holder"))
		}},
		{"CompareAndSwap", func(t *testing.T) []byte {
			return mustEncodeCAS(t, []byte("k"), []byte("holder"), []byte("next"))
		}},
	} {
		t.Run(verb.name, func(t *testing.T) {
			for name, build := range shardedCacheTargets() {
				t.Run(name, func(t *testing.T) {
					boom := stderrors.New("storage get failed")
					store := &gapErrGetStorage{memStorage: newMemStorage(), getErr: boom}
					// Seeded directly so the guard must read through to reach
					// it — and meets the fault instead.
					store.memStorage.data["k"] = []byte("holder")

					target := build(store)
					res, err := target.apply(t, verb.cmd(t))
					if err == nil {
						t.Fatalf("a failed storage read was treated as a decision: Result.Value = %d", res.Value)
					}
					if !stderrors.Is(err, boom) {
						t.Errorf("error = %v, want it to wrap the storage fault", err)
					}

					// Nothing may be cached or persisted on the fault path: the
					// guard never established what the key holds.
					//
					// The cache is observed by HEALING the storage first and
					// then reading. Reading through the still-faulting store
					// would fail on the storage error and tell us nothing about
					// the cache, and reaching into the shard map directly would
					// need one accessor per state machine for a fact Lookup
					// already reports.
					store.getErr = nil
					if got := target.lookup(t, []byte("k")); !bytes.Equal(got, []byte("holder")) {
						t.Errorf("key resolves to %q, want the untouched %q — the guard mutated "+
							"state despite never completing", got, "holder")
					}
					if v, ok := store.memStorage.data["k"]; !ok || !bytes.Equal(v, []byte("holder")) {
						t.Errorf("storage holds (%q, present=%v), want the untouched %q",
							v, ok, "holder")
					}
				})
			}
		})
	}
}

// The guarded verbs notify watchers exactly when they mutate, and never
// otherwise.
//
// A watcher told about a delete that the guard declined would report a lock as
// released while it is still held — which for a consumer driving a lease off
// the watch stream is worse than no notification at all, because it acts on
// it.
func TestStateMachines_GuardedVerbs_NotifyOnlyOnMutation(t *testing.T) {
	for name, build := range watchingTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("a declined verb notifies nothing", func(t *testing.T) {
				target, wm := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("theirs"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}
				// The seed's own notification is the baseline; only what the
				// guarded verbs add is under test.
				base := len(wm.getEvents())

				if _, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), []byte("mine"))); err != nil {
					t.Fatalf("CompareAndDelete: %v", err)
				}
				if _, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), []byte("mine"), []byte("v2"))); err != nil {
					t.Fatalf("CompareAndSwap: %v", err)
				}
				if n := len(wm.getEvents()) - base; n != 0 {
					t.Errorf("%d watch notification(s) for two declined guarded verbs; a watcher "+
						"was told about a write the store never took", n)
				}
			})

			t.Run("an accepted verb notifies once, with the right shape", func(t *testing.T) {
				target, wm := build(t)
				if _, err := target.apply(t, buildKVCmd(kv.OpPut, []byte("k"), []byte("v1"))); err != nil {
					t.Fatalf("seed Put: %v", err)
				}
				base := len(wm.getEvents())

				if _, err := applyGuarded(t, target, mustEncodeCAS(t, []byte("k"), []byte("v1"), []byte("v2"))); err != nil {
					t.Fatalf("CompareAndSwap: %v", err)
				}
				if _, err := applyGuarded(t, target, mustEncodeCAD(t, []byte("k"), []byte("v2"))); err != nil {
					t.Fatalf("CompareAndDelete: %v", err)
				}

				events := wm.getEvents()[base:]
				if len(events) != 2 {
					t.Fatalf("%d watch notifications, want 2 (one swap, one delete)", len(events))
				}
				if events[0].deleted {
					t.Error("the swap was notified as a DELETE; a watcher would drop the record")
				}
				if !bytes.Equal(events[0].value, []byte("v2")) {
					t.Errorf("swap notification carried %q, want the new value %q", events[0].value, "v2")
				}
				if !events[1].deleted {
					t.Error("the guarded delete was notified as a PUT; a watcher would keep the record")
				}
			})
		})
	}
}

// watchingTargets builds every state machine with a watch manager attached.
//
// It is separate from conformanceTargets because the watch manager is a
// construction-time option on four different constructors with four different
// option types, and threading it through the shared table would change every
// caller of that table for the benefit of one test.
func watchingTargets() map[string]func(*testing.T) (conformanceTarget, *mockWatchManager) {
	return map[string]func(*testing.T) (conformanceTarget, *mockWatchManager){
		"KVStateMachine": func(t *testing.T) (conformanceTarget, *mockWatchManager) {
			t.Helper()
			wm := &mockWatchManager{}
			backend := NewStandardBackend()
			k := NewKVStateMachine(backend, WithKVWatchManager(wm))
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
			}, wm
		},
		"RegularStateMachine": func(t *testing.T) (conformanceTarget, *mockWatchManager) {
			t.Helper()
			wm := &mockWatchManager{}
			r := NewRegularStateMachine(WithRegularWatchManager(wm))
			return conformanceTarget{
				apply:    updaterWithContext(r.Update),
				lookup:   lookupWithContext(r.Lookup),
				handlers: r.handlers,
			}, wm
		},
		"FastStateMachine": func(t *testing.T) (conformanceTarget, *mockWatchManager) {
			t.Helper()
			wm := &mockWatchManager{}
			f := NewFastStateMachine(1, 1, newMemStorage(), WithWatchManager(wm))
			return conformanceTarget{
				apply:    updaterWithContext(f.Update),
				lookup:   lookupWithContext(f.Lookup),
				handlers: f.handlers,
			}, wm
		},
		"ConcurrentStateMachine": func(t *testing.T) (conformanceTarget, *mockWatchManager) {
			t.Helper()
			wm := &mockWatchManager{}
			c := NewConcurrentStateMachine(1, 1, newMemStorage(), WithConcurrentWatchManager(wm))
			return conformanceTarget{
				apply:    updaterWithContext(c.Update),
				lookup:   lookupWithContext(c.Lookup),
				handlers: c.handlers,
			}, wm
		},
	}
}

// ---------------------------------------------------------------------------
// The FastBackend refusal (go-qrdb#175 review, go-qrdb#216/#221)
// ---------------------------------------------------------------------------

// A guarded verb is REFUSED on a backend whose writes cannot carry what its
// result promises, and refusing does not cost the shard.
//
// FastBackend persists on a background goroutine that is ordered against
// nothing and swallows its error, so an acknowledged guarded write there can
// be silently reversed by an older pending write. Measured on that backend
// with ordinary unguarded writes and no guarded verb involved (go-qrdb#224,
// and see TestFastBackend_UnguardedWritesCanLandOutOfOrder below):
//
//	Put(k,"old"); Put(k,"new")  -> durable value "old"
//	Put(k,"v");   Delete(k)     -> key PRESENT, holding "v"
//
// The second line applied to CompareAndDelete is a released lock that comes
// back, which is the failure the guarded plane exists to prevent. So the verbs
// refuse there rather than reporting a success the disk may contradict.
//
// This test is what makes re-advertising them a DELETION rather than an
// addition: someone who makes FastBackend implement GuardedWriteBackend
// without fixing the ordering has to remove an assertion that says why not.
func TestKVStateMachine_GuardedVerbsRefuseOnAnUnorderedBackend(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func(t *testing.T) []byte
	}{
		{"CompareAndDelete", func(t *testing.T) []byte {
			return mustEncodeCAD(t, []byte("k"), []byte("v"))
		}},
		{"CompareAndSwap", func(t *testing.T) []byte {
			return mustEncodeCAS(t, []byte("k"), []byte("v"), []byte("v2"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStorage()
			backend := NewFastBackend(1, 1, store, nil)
			k := NewKVStateMachine(backend)
			t.Cleanup(func() { _ = backend.Close() })

			// The opcode is DISPATCHED, not absent. A caller must be able to
			// tell "this backend will not do it" from "this build has no such
			// verb", and only a dispatched handler can say the first.
			if _, ok := k.handlers[tc.cmd(t)[0]]; !ok {
				t.Fatalf("opcode %d is not dispatched at all; the refusal must be a typed "+
					"answer from the handler, not a missing map entry", tc.cmd(t)[0])
			}

			results := make([]sm.Result, 1)
			err := k.Update([]sm.Entry{{Index: 1, Cmd: tc.cmd(t)}}, results)
			if err != nil {
				t.Fatalf("Update returned %v; a backend that cannot honour a guarded verb is a "+
					"COMPOSITION fault, not an infrastructure one, and returning it to Raft "+
					"trips the shard's circuit breaker (go-qrdb#129)", err)
			}

			msg, refused := protocol.DecodeAppError(results[0].Data)
			if !refused {
				t.Fatalf("results[0] = %+v, want an encoded application error; the guarded verb "+
					"was EXECUTED on a backend whose acknowledged write can be silently "+
					"reversed by an older pending one (go-qrdb#224)", results[0])
			}
			// The refusal has to be actionable: an operator reads it and has to
			// know which knob to turn.
			if !strings.Contains(msg, "FastBackend") {
				t.Errorf("refusal %q does not name the backend, so it does not say what to change", msg)
			}
			if want := strconv.Itoa(int(tc.cmd(t)[0])); !strings.Contains(msg, want) {
				t.Errorf("refusal %q does not name the refused opcode %s", msg, want)
			}

			// The shard must still take writes. This is the difference between
			// a refused verb and a dead cluster.
			putCmd := buildKVCmd(kv.OpPut, []byte("still-alive"), []byte("yes"))
			next := make([]sm.Result, 1)
			if err := k.Update([]sm.Entry{{Index: 2, Cmd: putCmd}}, next); err != nil {
				t.Fatalf("a plain Put after the refused verb failed: %v", err)
			}
			if v, _ := backend.Get([]byte("still-alive")); string(v) != "yes" {
				t.Errorf("Put after the refused verb stored %q, want %q", v, "yes")
			}
		})
	}
}

// Every other backend in this package DOES carry the guarantee, and says so.
//
// The pair with the test above is the point: a blanket refusal would also pass
// that one. This pins which side of the line each backend is on, so narrowing
// the guarantee by accident — a new backend that forgets to declare it, or an
// existing one that stops — fails here instead of silently removing a verb
// from an sm_type that had it.
func TestBackends_DeclareWhetherTheyCarryGuardedWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend Backend
		want    bool
		why     string
	}{
		{"StandardBackend", NewStandardBackend(), true,
			"a map under a mutex: the write has landed when Put returns"},
		{"ConcurrentBackend", NewConcurrentBackend(), true,
			"sharded in-memory; sharding changes the mutex, not when the write lands"},
		{"HighPerfBackend", NewHighPerfBackend(nil), true,
			"in-memory only"},
		{"DiskBackend", NewDiskBackend(newMemStorage()), true,
			"writes through synchronously on the calling goroutine and returns the error"},
		{"FastBackend", NewFastBackend(1, 1, newMemStorage(), nil), false,
			"persists on a goroutine ordered against nothing, and swallows the error (go-qrdb#216)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendSupportsGuardedWrites(tc.backend); got != tc.want {
				t.Errorf("backendSupportsGuardedWrites(%s) = %v, want %v — %s",
					tc.name, got, tc.want, tc.why)
			}
			if got := backendName(tc.backend); got != tc.name {
				t.Errorf("backendName = %q, want %q; the refusal names the backend and an "+
					"operator acts on that name", got, tc.name)
			}
		})
	}
}

// The hazard itself, on the backend, with no state machine and no guarded verb.
//
// This is the measurement the refusal above rests on, kept as a test so the
// claim stays true rather than becoming folklore in a comment. It asserts the
// CURRENT broken behaviour deliberately: when go-qrdb#224 is fixed this test
// fails, and that failure is the signal to flip FastBackend to
// GuardedWriteBackend and delete the refusal.
//
// The interleaving is forced, not raced: twoOpStorage parks the first storage
// operation and only releases it after the second has actually landed. Racing
// two goroutines and hoping would exercise the inversion once in a thousand
// runs — the first version of this probe did exactly that and reported no
// inversion at all, because the second write had not yet reached storage when
// the first was released.
func TestFastBackend_UnguardedWritesCanLandOutOfOrder(t *testing.T) {
	t.Run("a later Put is overwritten by an earlier one", func(t *testing.T) {
		store := newTwoOpStorage()
		b := NewFastBackend(1, 1, store, nil)

		if err := b.Put([]byte("k"), []byte("old")); err != nil {
			t.Fatalf("Put(old): %v", err)
		}
		<-store.firstIn // the Set("old") goroutine is parked inside storage
		if err := b.Put([]byte("k"), []byte("new")); err != nil {
			t.Fatalf("Put(new): %v", err)
		}
		<-store.secondIn     // Set("new") has actually landed
		close(store.release) // only now let the older write proceed
		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		got, err := store.memStorage.Get([]byte("k"))
		if err != nil {
			t.Fatalf("storage.Get: %v", err)
		}
		if string(got) != "old" {
			t.Fatalf("durable value = %q, want %q — go-qrdb#224 appears to be FIXED. "+
				"If FastBackend now orders its writes, make it implement "+
				"GuardedWriteBackend and delete the refusal in "+
				"TestKVStateMachine_GuardedVerbsRefuseOnAnUnorderedBackend.", got, "old")
		}
	})

	t.Run("a delete is undone by an earlier pending write", func(t *testing.T) {
		store := newTwoOpStorage()
		b := NewFastBackend(1, 1, store, nil)

		if err := b.Put([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		<-store.firstIn
		if err := b.Delete([]byte("k")); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		<-store.secondIn
		close(store.release)
		if err := b.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Read the map directly: Get would answer from the cache tombstone and
		// hide what storage is actually left holding.
		v, present := store.memStorage.data["k"]
		if !present {
			t.Fatalf("the key is absent from storage — go-qrdb#224 appears to be FIXED; "+
				"see the sibling case for what to do. (value %q)", v)
		}
		if string(v) != "v" {
			t.Fatalf("storage holds %q, want the resurrected %q", v, "v")
		}
	})
}

// twoOpStorage parks the FIRST storage operation and lets the SECOND run to
// completion before the first is released, so the inversion is deterministic
// rather than a race the scheduler obliges once in a while.
type twoOpStorage struct {
	*memStorage
	n        atomic.Int32
	firstIn  chan struct{}
	secondIn chan struct{}
	release  chan struct{}
}

func newTwoOpStorage() *twoOpStorage {
	return &twoOpStorage{
		memStorage: newMemStorage(),
		firstIn:    make(chan struct{}),
		secondIn:   make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (s *twoOpStorage) enter() int32 {
	n := s.n.Add(1)
	if n == 1 {
		close(s.firstIn)
		<-s.release
	}
	return n
}

// leave signals AFTER the write has landed, which is what makes the handoff
// exact: the test releases the parked first write only once the second is
// genuinely durable.
func (s *twoOpStorage) leave(n int32) {
	if n == 2 {
		close(s.secondIn)
	}
}

func (s *twoOpStorage) Set(key, value []byte) error {
	n := s.enter()
	err := s.memStorage.Set(key, value)
	s.leave(n)
	return err
}

func (s *twoOpStorage) Delete(key []byte) error {
	n := s.enter()
	err := s.memStorage.Delete(key)
	s.leave(n)
	return err
}
