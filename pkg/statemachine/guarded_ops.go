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
	"sync"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// This file holds every implementation of protocol.OpCompareAndDelete (66) and
// protocol.OpCompareAndSwap (67), together rather than each beside its own
// state machine's Delete and Put — the arrangement putifabsent.go adopted for
// the same reason, and for the same defect.
//
// go-qrdb#86 was a PARTIAL opcode: kv.OpPutIfAbsent existed in pkg/store and
// both engines in pkg/engine and in none of the four state machines here.
// go-qrdb#175 is the identical shape one opcode later. pkg/store/pebble.go and
// both pkg/engine engines dispatch 66 and 67; none of the four state machines
// in this package did, so a node on any sm_type served by this package —
// memory, standard, concurrent, fast, disk, highperf, regular, and the
// since-removed mvcc-fast type over FastStateMachine — refused an
// ordinary authorized CompareAndDelete with "statemachine: unknown opcode: 66"
// and CompareAndSwap with 67. Measured on this branch before the fix, on both
// FastStateMachine and KVStateMachine.
//
// Nothing failed at BUILD time, because a handler map is data. The conformance
// test in guarded_ops_test.go is what makes the next omission fail here.
//
// These two are together with each other rather than with PutIfAbsent because
// they share a decision — "the key holds exactly this value" — that the
// conditional create does not have.
//
// The contract, identical in all four:
//
//   - Result is protocol.ResultDeleted / protocol.ResultSwapped when this
//     entry performed the write and protocol.ResultNotDeleted /
//     protocol.ResultNotSwapped when the guard declined. BOTH are successful
//     applications — "the value did not match" is the answer, not an error —
//     because returning an error from Update stops Raft advancing lastApplied
//     and stalls every subsequent linearizable read. pkg/service reads exactly
//     these constants off Result.Value (KVService.CompareAndDelete /
//     CompareAndSwap).
//   - An ABSENT key matches no expectation. Neither verb creates: a swap moves
//     a record between two states and a record that does not exist has no
//     first state, and a delete of an absent key has nothing to release. Use
//     kv.OpPutIfAbsent to create.
//   - The comparison is evaluated HERE, inside apply, against the state this
//     replica has at this entry's log position. Every replica sees the same
//     entry against the same state and reaches the same decision. Comparing on
//     the proposer and then proposing a plain Delete is two operations with a
//     window between them, and that window is the whole defect: two releasers
//     both read holder A, the first deletes, a third party legitimately claims
//     the now-absent key, and the second delete removes THEIR value
//     (go-qrdb#41).
//   - A genuine read FAULT is returned as a Go error and never folded into
//     "the value differs". Folding it would leave the key on this replica
//     while replicas whose reads succeeded removed it — divergence from an
//     identical log.
//
// Atomicity, which is the property that makes these worth having at all:
//
//   - KVStateMachine and RegularStateMachine decide and write inside one
//     handler call. go-quicraft's sm contract runs Update single-threaded
//     (Lookup may run concurrently with Update, never Update with Update), so
//     no other entry can land between the read and the write. This is the same
//     basis KVStateMachine.handlePutIfAbsent already stands on.
//   - RegularStateMachine additionally takes r.mu ONCE across the check and
//     the write, so a concurrent Lookup cannot observe a half-applied swap.
//   - FastStateMachine takes the shard lock ONCE
//     across the check and the cache write, through compareAndSetLocked, and
//     then persist SYNCHRONOUSLY through the same claim machinery
//     handlePutIfAbsent uses. Splitting the check from the write would reopen
//     the very window these operations exist to close.
//
// DURABILITY AND ORDERING are separate properties from atomicity, and one
// composition cannot provide them. A guarded result promises more than "the
// guard passed": it promises the write is on disk, that a storage failure was
// reported rather than logged, and that no earlier write to the same key can
// still overwrite it.
//
// FastStateMachine provides all three directly —
// it owns its storage, persist synchronously through persistClaimLocked,
// and order every write through persistOrder (the go-qrdb#94 ruling of
// 2026-08-23, and go-qrdb#86 for the ordering). KVStateMachine reaches storage
// only through the Backend seam, so it gets them by ASKING for them:
// Backend.PutDurable and Backend.DeleteDurable carry exactly those three
// promises, and every Backend implements them (owner ruling 2026-09-03,
// go-qrdb#216, option 2).
//
// The three guarded verbs here therefore call the durable pair while handlePut,
// handleDelete and handleBatch keep the ordinary Put and Delete. That split is
// the point: "your write is queued" and "your claim succeeded" are different
// promises, and only the second is worth a disk wait on the apply path.
//
// What this replaced, and why the interface changed rather than the backend:
// FastBackend acknowledged a guarded write before it was durable and logged
// its storage failure instead of returning it, so an acknowledged PutIfAbsent
// could leave the key absent across a restart and a second caller could win
// the same claim. Between go-qrdb#175 and this change the verbs were REFUSED
// there with a typed error rather than answered wrongly. Making Put and Delete
// themselves synchronous was rejected — it discards the asynchronous contract
// that backend exists to provide — and so was an optional interface, because a
// backend that skipped it would fall back to the asynchronous path silently,
// which is the degradation the guarded plane exists to refuse.

// guardMatches reports whether the key's current state satisfies the caller's
// expectation.
//
// current is nil for an absent key — every Backend and every storage.Storage
// in this tree reports a miss as (nil, nil), and the sharded caches record a
// delete as a nil tombstone — and an absent key matches nothing, including an
// empty expected value. That distinction is real and load-bearing: a stored
// empty value arrives here as a non-nil zero-length slice, so `CompareAndSwap`
// guarding on "" still matches a key that holds "" and still declines a key
// that holds nothing.
func guardMatches(current, expected []byte) bool {
	return current != nil && bytes.Equal(current, expected)
}

// ---------------------------------------------------------------------------
// KVStateMachine
// ---------------------------------------------------------------------------

// handleCompareAndDelete removes the key only when the backend still holds the
// expected value.
//
// Backend.Get documents a missing key as (nil, nil), so a non-nil error is a
// real fault and is propagated rather than read as "the value differs".
//
// The removal goes through Backend.DeleteDurable, not Delete — see the
// durability-and-ordering note at the top of this file. protocol.ResultDeleted
// is the answer to "did I release the claim I was holding?", and a release
// acknowledged but not durable is a key that comes back after a restart, still
// holding a value its owner was told was gone.
func (k *KVStateMachine) handleCompareAndDelete(payload []byte, revision uint64) (sm.Result, error) {
	key, expected, err := protocol.DecodeCompareAndDelete(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "compare_and_delete: " + err.Error()}
	}
	if kerr := validateClientKey("compare_and_delete", key); kerr != nil {
		return sm.Result{}, kerr
	}

	current, gerr := k.backend.Get(key)
	if gerr != nil {
		return sm.Result{}, &StorageError{Op: "compare_and_delete_get", Inner: gerr}
	}
	if !guardMatches(current, expected) {
		return sm.Result{Value: protocol.ResultNotDeleted}, nil
	}

	if derr := k.deleteKey(key, true); derr != nil {
		return sm.Result{}, &StorageError{Op: "compare_and_delete", Inner: derr}
	}

	// Watchers are notified only AFTER the removal is durable, so no observer
	// learns of a delete the caller was told had failed.
	if k.watchManager != nil {
		k.watchManager.Notify(string(key), nil, revision, true)
	}

	return sm.Result{Value: protocol.ResultDeleted}, nil
}

// handleCompareAndSwap replaces the key's value only when the backend still
// holds the expected one. An absent key is left absent.
//
// The replacement goes through Backend.PutDurable, as the guarded delete above
// goes through DeleteDurable. protocol.ResultSwapped is the same promise about
// a renewal: acknowledged but not durable, it reverts on restart and the
// holder's next guarded verb is then refused.
func (k *KVStateMachine) handleCompareAndSwap(payload []byte, revision uint64) (sm.Result, error) {
	key, expected, next, err := protocol.DecodeCompareAndSwap(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "compare_and_swap: " + err.Error()}
	}
	if kerr := validateClientKey("compare_and_swap", key); kerr != nil {
		return sm.Result{}, kerr
	}

	current, gerr := k.backend.Get(key)
	if gerr != nil {
		return sm.Result{}, &StorageError{Op: "compare_and_swap_get", Inner: gerr}
	}
	if !guardMatches(current, expected) {
		return sm.Result{Value: protocol.ResultNotSwapped}, nil
	}

	// A compare-and-swap edits the value and leaves the key's lease alone, as
	// the pebble state machine does: it is a renewal of what the holder
	// already has, not a re-attachment, so its binding record is untouched.
	if perr := k.backend.PutDurable(key, next); perr != nil {
		return sm.Result{}, &StorageError{Op: "compare_and_swap", Inner: perr}
	}

	if k.watchManager != nil {
		k.watchManager.Notify(string(key), next, revision, false)
	}

	return sm.Result{Value: protocol.ResultSwapped}, nil
}

// ---------------------------------------------------------------------------
// RegularStateMachine
// ---------------------------------------------------------------------------

// handleCompareAndDelete removes the key only when the map still holds the
// expected value.
//
// The check and the delete take r.mu ONCE. Splitting them into a read followed
// by handleDelete's write would reopen the window the operation exists to
// close.
func (r *RegularStateMachine) handleCompareAndDelete(payload []byte, revision uint64) (sm.Result, error) {
	key, expected, err := protocol.DecodeCompareAndDelete(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "compare_and_delete: " + err.Error()}
	}
	if kerr := validateClientKey("compare_and_delete", key); kerr != nil {
		return sm.Result{}, kerr
	}
	k := string(key)

	r.mu.Lock()
	// This state machine REMOVES a deleted key rather than tombstoning it (see
	// handleDelete), so presence in the map is existence and comma-ok is the
	// whole absence test.
	current, present := r.data[k]
	if !present || !guardMatches(current, expected) {
		r.mu.Unlock()
		return sm.Result{Value: protocol.ResultNotDeleted}, nil
	}
	delete(r.data, k)
	r.mu.Unlock()

	if r.watchManager != nil {
		r.watchManager.Notify(k, nil, revision, true)
	}

	return sm.Result{Value: protocol.ResultDeleted}, nil
}

// handleCompareAndSwap replaces the key's value only when the map still holds
// the expected one. An absent key is left absent.
func (r *RegularStateMachine) handleCompareAndSwap(payload []byte, revision uint64) (sm.Result, error) {
	key, expected, next, err := protocol.DecodeCompareAndSwap(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "compare_and_swap: " + err.Error()}
	}
	if kerr := validateClientKey("compare_and_swap", key); kerr != nil {
		return sm.Result{}, kerr
	}
	k := string(key)

	// Copied before the lock: the entry buffer is recycled after Update
	// returns, and the map would otherwise hold a slice into it.
	ownedNext := make([]byte, len(next))
	copy(ownedNext, next)

	r.mu.Lock()
	current, present := r.data[k]
	if !present || !guardMatches(current, expected) {
		r.mu.Unlock()
		return sm.Result{Value: protocol.ResultNotSwapped}, nil
	}
	r.data[k] = ownedNext
	r.mu.Unlock()

	if r.watchManager != nil {
		r.watchManager.Notify(k, ownedNext, revision, false)
	}

	return sm.Result{Value: protocol.ResultSwapped}, nil
}

// ---------------------------------------------------------------------------
// FastStateMachine
// ---------------------------------------------------------------------------

// handleCompareAndDelete tombstones the key only when the shard still holds
// the expected value, and waits for the removal to reach storage.
func (f *FastStateMachine) handleCompareAndDelete(payload []byte, revision uint64) (sm.Result, error) {
	key, expected, err := protocol.DecodeCompareAndDelete(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "compare_and_delete: " + err.Error()}
	}
	k := string(key)

	// expected is COMPARED, never retained, so it needs no owned copy: the
	// entry buffer outlives this handler call.
	s := f.shardFor(k)
	claimed, cerr := compareAndSetLocked(&s.mu, s.data, s.claims, &s.order, k, expected, nil, true, f.storage.Get)
	if cerr != nil {
		return sm.Result{}, cerr
	}
	if !claimed.stored {
		return sm.Result{Value: protocol.ResultNotDeleted}, nil
	}

	// SYNCHRONOUS, for the reason handlePutIfAbsent's write is (owner ruling
	// 2026-08-23, go-qrdb#94). protocol.ResultDeleted is the answer to "did I
	// release the key I was holding?" — a release acknowledged but not durable
	// is a key that comes back after a restart, still holding a value its
	// owner was told was gone.
	if perr := f.persistClaimDelete(s, claimed, k); perr != nil {
		return sm.Result{}, perr
	}

	// Watchers are notified only AFTER the removal is durable, so no observer
	// learns of a delete the caller was told had failed.
	if f.watchManager != nil {
		f.watchManager.Notify(k, nil, revision, true)
	}

	return sm.Result{Value: protocol.ResultDeleted}, nil
}

// handleCompareAndSwap replaces the key's cached value only when the shard
// still holds the expected one, and waits for the replacement to reach
// storage.
func (f *FastStateMachine) handleCompareAndSwap(payload []byte, revision uint64) (sm.Result, error) {
	key, expected, next, err := protocol.DecodeCompareAndSwap(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "compare_and_swap: " + err.Error()}
	}
	k := string(key)

	ownedNext := make([]byte, len(next))
	copy(ownedNext, next)

	s := f.shardFor(k)
	claimed, cerr := compareAndSetLocked(&s.mu, s.data, s.claims, &s.order, k, expected, ownedNext, false, f.storage.Get)
	if cerr != nil {
		return sm.Result{}, cerr
	}
	if !claimed.stored {
		return sm.Result{Value: protocol.ResultNotSwapped}, nil
	}

	// Synchronous for the same reason as the guarded delete above.
	if perr := f.persistClaim(s, claimed, k, ownedNext, "compare_and_swap_set"); perr != nil {
		return sm.Result{}, perr
	}

	if f.watchManager != nil {
		f.watchManager.Notify(k, ownedNext, revision, false)
	}

	return sm.Result{Value: protocol.ResultSwapped}, nil
}

// ---------------------------------------------------------------------------
// Shared guard for the sharded-cache state machine
// ---------------------------------------------------------------------------

// compareAndSetLocked is the guarded compare-and-write for the sharded-cache
// state machine.
//
// It takes the lock and the map rather than a shard for the same reason
// setIfAbsentLocked does: the guard is one implementation independent of the
// shard type.
//
// The check and the write happen under ONE acquisition of the shard lock, so
// no other apply can land between them.
//
// deleting selects which write the guard authorises: a nil TOMBSTONE for
// OpCompareAndDelete, or next for OpCompareAndSwap. It is an explicit flag
// rather than "next == nil" because a swap to an EMPTY value is legitimate and
// must store a non-nil zero-length slice; inferring the tombstone from a nil
// next would silently turn that swap into a delete.
//
// The two details of the shard cache that decide correctness are the same ones
// setIfAbsentLocked documents, read the other way round:
//
//   - A deleted key is a nil TOMBSTONE, not a removed entry, so presence in
//     the map is not existence. A tombstoned key is ABSENT and matches no
//     expectation.
//   - A key missing from the map entirely has never been written by this
//     replica, which is not the same as never existing: state recovered from a
//     snapshot lives in storage. The miss therefore reads through, under the
//     lock, so the guard compares against recovered state too. Without it
//     every guarded verb on a freshly restarted replica would answer "does not
//     match" for keys that match perfectly.
//
// readThrough is the storage lookup used on a cache miss; it reports a missing
// key as (nil, nil) and a fault as a non-nil error, which is propagated rather
// than read as a mismatch.
//
// The returned claim describes what the cache held BEFORE the write, so a
// caller whose persist fails can put the entry back exactly as it was
// (go-qrdb#94). Unlike the conditional create's, that prior state is usually a
// live VALUE — see claim.prior.
func compareAndSetLocked(
	mu *sync.RWMutex,
	data map[string][]byte,
	claims map[string]uint64,
	order *persistOrder,
	key string,
	expected, next []byte,
	deleting bool,
	readThrough func([]byte) ([]byte, error),
) (claim, error) {
	mu.Lock()
	defer mu.Unlock()

	cached, found := data[key]
	current := cached
	if !found {
		stored, rerr := readThrough([]byte(key))
		if rerr != nil {
			return claim{}, &StorageError{Op: "compare_and_write_get", Inner: rerr}
		}
		current = stored
	}
	if !guardMatches(current, expected) {
		return claim{}, nil
	}

	// cacheClaimLocked draws the ticket under the same lock as the cache write,
	// so this write persists AFTER every write already accepted on this shard.
	// Without that ordering an earlier Put could land last and undo a swap this
	// call already reported as durable.
	//
	// deleting selects a nil TOMBSTONE over next; the two cannot be inferred
	// from next alone, because a swap to an EMPTY value is legitimate and must
	// store a non-nil zero-length slice.
	if deleting {
		next = nil
	}
	return cacheClaimLocked(data, claims, order, key, next), nil
}
