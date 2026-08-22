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
	stored, ticket, serr := setIfAbsentLocked(&s.mu, s.data, &s.order, k, ownedValue, f.storage.Get)
	if serr != nil {
		return sm.Result{}, serr
	}
	if !stored {
		return sm.Result{Value: kv.ResultNotStored}, nil
	}

	// The write reaches durable storage through the same asynchronous path
	// cacheSet uses, so the guard's decision and the write it authorised stay
	// on one code path — including its place in the shard's persistence order.
	f.persistAsync(s, ticket, k, ownedValue)

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
	stored, ticket, serr := setIfAbsentLocked(&s.mu, s.data, &s.order, k, ownedValue, c.storage.Get)
	if serr != nil {
		return sm.Result{}, serr
	}
	if !stored {
		return sm.Result{Value: kv.ResultNotStored}, nil
	}

	c.persistAsync(s, ticket, k, ownedValue)

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
func setIfAbsentLocked(
	mu *sync.RWMutex,
	data map[string][]byte,
	order *persistOrder,
	key string,
	value []byte,
	readThrough func([]byte) ([]byte, error),
) (stored bool, ticket uint64, err error) {
	mu.Lock()
	defer mu.Unlock()

	if cached, found := data[key]; found {
		if cached != nil {
			return false, 0, nil
		}
		// Tombstone: absent, and storage need not be consulted — the delete
		// that wrote it is newer than anything storage holds.
		data[key] = value
		// Drawn under the same lock as the cache write, so this write persists
		// AFTER the delete that tombstoned the key. Without that ordering the
		// delete could land last and erase a write this call already reported
		// as stored.
		return true, order.ticket(), nil
	}

	existing, rerr := readThrough([]byte(key))
	if rerr != nil {
		return false, 0, &StorageError{Op: "put_if_absent_get", Inner: rerr}
	}
	if existing != nil {
		return false, 0, nil
	}

	data[key] = value
	return true, order.ticket(), nil
}
