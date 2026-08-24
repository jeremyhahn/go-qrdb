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
	"encoding/binary"
	"sync"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// This file holds every implementation of kv.OpPutIfAbsent, together rather
// than each beside its own state machine's Put.
//
// They are together because the defect that prompted them was a PARTIAL
// opcode: pkg/store and both engines in pkg/engine handled opcode 4 while all
// four state machines here did not, so a node on --sm-type memory applied
// every other base KV op and failed this one with "unknown opcode: 4". The
// identity index claims identities with PutIfAbsent, so role creation could
// not apply and first boot could not complete (go-qrdb#86).
//
// An opcode implemented four times in four files is easy to add three times.
// Keeping them in one file, and pinning them with the conformance test in
// putifabsent_test.go, makes the omission visible.
//
// The contract, identical in all four:
//
//   - Result is kv.ResultStored when this entry created the key and
//     kv.ResultNotStored when the key was already present. BOTH are successful
//     applications — "not stored" is the answer, not an error — because
//     returning an error from Update stops Raft advancing lastApplied and
//     stalls every subsequent linearizable read.
//   - The guard is evaluated HERE, inside apply, against the state this
//     replica has at this entry's log position. Every replica sees the same
//     entry against the same state and reaches the same decision.
//   - A genuine read FAULT is returned as a Go error and never folded into
//     "absent". Folding would overwrite the holder and report ResultStored,
//     and because such a fault is local to one replica, replicas whose reads
//     succeeded would decline the entry this one stored — divergence from an
//     identical log.

// decodeKeyValuePayload parses the [keyLen:2 LE][key][value] body shared by
// kv.OpPut and kv.OpPutIfAbsent. The op byte has already been stripped by the
// dispatcher.
//
// The returned slices alias payload, which the caller may not retain: entry
// buffers are recycled, so a handler that stores either must copy first.
func decodeKeyValuePayload(payload []byte, op string) (key, value []byte, err error) {
	if len(payload) < 2 {
		return nil, nil, &IncompleteDataError{Reason: op + ": payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return nil, nil, &IncompleteDataError{Reason: op + ": key length exceeds payload"}
	}
	return payload[2 : 2+keyLen], payload[2+keyLen:], nil
}

// handlePutIfAbsent stores the pair only when the key is absent from the
// backend.
//
// Backend.Get documents a missing key as (nil, nil), so a non-nil error is a
// real fault and is propagated rather than read as absence.
func (k *KVStateMachine) handlePutIfAbsent(payload []byte, revision uint64) (sm.Result, error) {
	key, value, err := decodeKeyValuePayload(payload, "put_if_absent")
	if err != nil {
		return sm.Result{}, err
	}

	existing, gerr := k.backend.Get(key)
	if gerr != nil {
		return sm.Result{}, &StorageError{Op: "put_if_absent_get", Inner: gerr}
	}
	if existing != nil {
		return sm.Result{Value: kv.ResultNotStored}, nil
	}

	if perr := k.backend.Put(key, value); perr != nil {
		return sm.Result{}, &StorageError{Op: "put_if_absent", Inner: perr}
	}

	if k.watchManager != nil {
		k.watchManager.Notify(string(key), value, revision, false)
	}

	return sm.Result{Value: kv.ResultStored}, nil
}

// handlePutIfAbsent stores the pair only when the key is absent from the map.
//
// The check and the write take r.mu ONCE. Splitting them into a read followed
// by handlePut's write would reopen the very window the operation exists to
// close.
func (r *RegularStateMachine) handlePutIfAbsent(payload []byte, revision uint64) (sm.Result, error) {
	key, value, err := decodeKeyValuePayload(payload, "put_if_absent")
	if err != nil {
		return sm.Result{}, err
	}
	k := string(key)

	// Copied before the lock: the entry buffer is recycled after Update
	// returns, and the map would otherwise hold a slice into it.
	ownedValue := make([]byte, len(value))
	copy(ownedValue, value)

	r.mu.Lock()
	if _, present := r.data[k]; present {
		r.mu.Unlock()
		return sm.Result{Value: kv.ResultNotStored}, nil
	}
	r.data[k] = ownedValue
	r.mu.Unlock()

	if r.watchManager != nil {
		r.watchManager.Notify(k, ownedValue, revision, false)
	}

	return sm.Result{Value: kv.ResultStored}, nil
}

// handlePutIfAbsent stores the pair only when the key is absent.
func (f *FastStateMachine) handlePutIfAbsent(payload []byte, revision uint64) (sm.Result, error) {
	key, value, err := decodeKeyValuePayload(payload, "put_if_absent")
	if err != nil {
		return sm.Result{}, err
	}
	k := string(key)

	ownedValue := make([]byte, len(value))
	copy(ownedValue, value)

	s := f.shardFor(k)
	claimed, serr := setIfAbsentLocked(&s.mu, s.data, s.claims, &s.order, k, ownedValue, f.storage.Get)
	if serr != nil {
		return sm.Result{}, serr
	}
	if !claimed.stored {
		return sm.Result{Value: kv.ResultNotStored}, nil
	}

	// SYNCHRONOUS, unlike Put and Delete (owner ruling 2026-08-23,
	// go-qrdb#94). kv.ResultStored is the answer to "did I win the claim?" —
	// the identity index uses it to decide that a role id, tenant id or
	// single-use token belongs to this caller and to nobody else. A claim
	// acknowledged but not durable is two holders after a restart, the exact
	// outcome the guard exists to prevent. Put and Delete keep their
	// asynchronous contract: "your write is queued" and "your claim
	// succeeded" are different promises, and only the second is worth a disk
	// wait on the apply path.
	if err := f.persistClaim(s, claimed, k, ownedValue); err != nil {
		return sm.Result{}, err
	}

	// Watchers are notified only AFTER the write is durable, so no observer
	// learns of a claim the caller was told had failed.
	if f.watchManager != nil {
		f.watchManager.Notify(k, ownedValue, revision, false)
	}

	return sm.Result{Value: kv.ResultStored}, nil
}

// handlePutIfAbsent stores the pair only when the key is absent.
func (c *ConcurrentStateMachine) handlePutIfAbsent(payload []byte, revision uint64) (sm.Result, error) {
	key, value, err := decodeKeyValuePayload(payload, "put_if_absent")
	if err != nil {
		return sm.Result{}, err
	}
	k := string(key)

	ownedValue := make([]byte, len(value))
	copy(ownedValue, value)

	s := c.concurrentShardFor(k)
	claimed, serr := setIfAbsentLocked(&s.mu, s.data, s.claims, &s.order, k, ownedValue, c.storage.Get)
	if serr != nil {
		return sm.Result{}, serr
	}
	if !claimed.stored {
		return sm.Result{Value: kv.ResultNotStored}, nil
	}

	// Synchronous for the same reason as FastStateMachine's — see the comment
	// there (owner ruling 2026-08-23, go-qrdb#94).
	if err := c.persistClaim(s, claimed, k, ownedValue); err != nil {
		return sm.Result{}, err
	}

	if c.watchManager != nil {
		c.watchManager.Notify(k, ownedValue, revision, false)
	}

	return sm.Result{Value: kv.ResultStored}, nil
}

// setIfAbsentLocked is the guarded write for the two sharded-cache state
// machines.
//
// It takes the lock and the map rather than a shard because FastStateMachine
// and ConcurrentStateMachine have structurally identical but distinct shard
// types; passing the two fields keeps one implementation of the guard instead
// of two that could drift, which is the failure this whole change is about.
//
// The check and the write happen under ONE acquisition of the shard lock, so
// no other apply can land between them.
//
// Two details of the shard cache decide correctness here:
//
//   - A deleted key is a nil TOMBSTONE, not a removed entry, so presence in
//     the map is not existence. A tombstoned key is absent and may be stored
//     over; treating it as present would refuse every re-creation of a
//     deleted key.
//   - A key missing from the map entirely has never been written by this
//     replica, which is not the same as never existing: state recovered from
//     a snapshot lives in storage. The miss therefore reads through, under
//     the lock, so the guard sees recovered state too.
//
// readThrough is the storage lookup used on a cache miss; it reports a missing
// key as (nil, nil) and a fault as a non-nil error, which is propagated.
//
// claimed describes what the cache held BEFORE the claim, so a caller whose
// persist fails can put the entry back exactly as it was (go-qrdb#94). The two
// prior states are not interchangeable: a tombstone must be restored as a
// tombstone, since deleting the entry instead would let the next guarded write
// read through to storage and see a value the delete had already retired.
func setIfAbsentLocked(
	mu *sync.RWMutex,
	data map[string][]byte,
	claims map[string]uint64,
	order *persistOrder,
	key string,
	value []byte,
	readThrough func([]byte) ([]byte, error),
) (claimed claim, err error) {
	mu.Lock()
	defer mu.Unlock()

	// record marks the claim in flight so a rollback can tell whether the
	// cache entry it would restore is still this claim's. Drawn under the same
	// lock as the cache write, both for the ticket ordering described below
	// and so no later write can slip between the two.
	record := func(overTombstone bool) claim {
		c := claim{stored: true, ticket: order.ticket(), overTombstone: overTombstone}
		claims[key] = c.ticket
		return c
	}

	if cached, found := data[key]; found {
		if cached != nil {
			return claim{}, nil
		}
		// Tombstone: absent, and storage need not be consulted — the delete
		// that wrote it is newer than anything storage holds.
		data[key] = value
		// The ticket is drawn under the same lock as the cache write, so this
		// write persists AFTER the delete that tombstoned the key. Without
		// that ordering the delete could land last and erase a write this call
		// already reported as stored.
		return record(true), nil
	}

	existing, rerr := readThrough([]byte(key))
	if rerr != nil {
		return claim{}, &StorageError{Op: "put_if_absent_get", Inner: rerr}
	}
	if existing != nil {
		return claim{}, nil
	}

	data[key] = value
	return record(false), nil
}

// claim is the outcome of setIfAbsentLocked: whether the guard admitted the
// write, its place in the shard's persistence order, and what the cache held
// beforehand so the claim can be withdrawn if the write does not reach disk.
type claim struct {
	// stored reports that the guard admitted the write and the cache now
	// holds it.
	stored bool

	// ticket is this write's place in the shard's persistence order. Only
	// meaningful when stored.
	ticket uint64

	// overTombstone records that the entry the claim replaced was a delete
	// tombstone rather than a genuine absence.
	overTombstone bool
}

// persistClaimLocked writes a guarded claim to storage SYNCHRONOUSLY, on the
// apply goroutine, and returns the failure instead of logging it
// (go-qrdb#94).
//
// It waits for this shard's earlier asynchronous writes first, through the
// same persistOrder ticket cacheSet uses. Skipping the wait would let a Put or
// Delete accepted BEFORE this claim land after it, so a claim reported durable
// could be overwritten by an older write. The wait cannot stall: the writers
// ahead run on their own goroutines and need nothing this one holds.
//
// It takes the lock, the map and the storage write rather than a shard for the
// same reason setIfAbsentLocked does: FastStateMachine and
// ConcurrentStateMachine have structurally identical but distinct shard types,
// and one implementation cannot drift from itself.
//
// One property is worth stating because it is not removable here: between the
// guard admitting the claim and this write completing, the claim is visible in
// the shard cache to concurrent readers. Holding the shard's data lock across
// the storage call would close that window and serialise the whole shard on
// disk, which is the cost persistOrder exists to avoid. A withdrawn claim is
// therefore briefly observable; a claim the CALLER was told succeeded is not,
// which is the guarantee the issue is about.
func persistClaimLocked(
	mu *sync.RWMutex,
	data map[string][]byte,
	claims map[string]uint64,
	order *persistOrder,
	set func(key, value []byte) error,
	claimed claim,
	key string,
	value []byte,
) error {
	var setErr error
	order.run(claimed.ticket, func() {
		setErr = set([]byte(key), value)
	})
	if setErr == nil {
		mu.Lock()
		claimed.settle(claims, key)
		mu.Unlock()
		return nil
	}
	claimed.withdraw(mu, data, claims, key)
	return &StorageError{Op: "put_if_absent_set", Inner: setErr}
}

// settle removes the in-flight record for a claim that reached disk. Callers
// hold the shard lock.
func (c claim) settle(claims map[string]uint64, key string) {
	// Comma-ok, not a bare lookup: the first ticket a shard issues is ZERO, so
	// a missing entry and this claim's own ticket are indistinguishable by
	// value. Measured — the failure-interleaving test below caught exactly
	// that, restoring over a later write because `claims[key] != c.ticket`
	// read 0 != 0.
	if t, ok := claims[key]; ok && t == c.ticket {
		delete(claims, key)
	}
}

// withdraw undoes a claim whose storage write failed, restoring the cache to
// what it held before (go-qrdb#94).
//
// The guarded verbs report DURABLE success, so a claim that never reached disk
// must leave nothing behind: a caller told the write failed, and a cache still
// advertising the key, would make the next guarded write on it answer
// "already exists" for a value no reader will ever find.
//
// It restores ONLY while the entry is still this claim's, which the in-flight
// record proves (Codex review on PR #110). The shard lock is released while
// the storage write runs, so a later write to the same key can land in
// between; rolling back unconditionally would then delete or tombstone a value
// that write had legitimately established, and a subsequent guarded write
// could win a key the earlier one already holds. Any such write removes the
// in-flight record under the shard lock, so its absence — or a different
// ticket — is exactly the signal that this claim is no longer what the cache
// holds.
//
// Note the apply worker is single-threaded (go-quicraft's sm contract: Lookup
// may run concurrently with Update, not Update with Update), so a write cannot
// currently interleave here through the Raft path. This does not depend on
// that: Update is exported, the guarantee is stated nowhere in this package,
// and a rollback that silently relies on an external serialisation is one
// refactor away from being wrong.
func (c claim) withdraw(mu *sync.RWMutex, data map[string][]byte, claims map[string]uint64, key string) {
	mu.Lock()
	defer mu.Unlock()

	// Comma-ok, not a bare lookup: the first ticket a shard issues is ZERO, so
	// an ABSENT entry — which is precisely what a later write leaves behind —
	// compares equal to the first claim's ticket and the rollback proceeds.
	t, ok := claims[key]
	if !ok || t != c.ticket {
		// A later write superseded this claim. It owns the entry now.
		return
	}
	delete(claims, key)

	if c.overTombstone {
		data[key] = nil
		return
	}
	delete(data, key)
}
