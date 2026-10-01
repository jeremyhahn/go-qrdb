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

// Package lease provides distributed time-to-live lease management for
// the MVCC key-value store. Leases allow keys to be automatically
// deleted when their lease expires, enabling session management,
// service discovery, and distributed locking patterns.
//
// Leases are replicated through Raft consensus. The lessor maintains an
// in-memory lease table, and the state machine stages every change to it into
// the same apply batch as the data write that caused it (see table.go), so
// the table is durable, travels in snapshots, and is restored with Restore
// when a state machine opens or recovers. Only the Raft leader runs the
// expiration timer to avoid duplicate revocations.
package lease

import (
	"bytes"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// lease is the internal lease state tracked by the Lessor.
//
// mu guards BOTH keys and expiry. expiry is mutated by KeepAlive, which runs
// on the Raft apply goroutine, and read by the lease-expiry sweeper on its own
// goroutine -- so an unsynchronised read of it is a genuine data race, not a
// benign one. It went unobserved because the only composition that ran the
// sweeper (cmd/qrdbd) has no -race test driving a keep-alive concurrently with
// a sweep; the race detector reported it the first time sdk/go/node started
// the sweeper under the embedded raft lease tests (go-qrdb#174).
type lease struct {
	id     int64
	ttl    int64 // granted TTL in seconds
	expiry time.Time
	keys   map[string]struct{} // attached keys
	mu     sync.RWMutex
}

// remainingTTLLocked returns the remaining TTL in seconds.
//
// The CALLER MUST HOLD l.mu (read or write). The locking variants below do not
// wrap this one for a reason: toLease already holds l.mu when it needs the
// value, and sync.RWMutex is not reentrant -- a nested RLock deadlocks the
// moment a writer is queued between the two acquisitions. Splitting the
// locked/unlocked pair is what keeps that from being reintroduced by someone
// "adding the missing lock".
func (l *lease) remainingTTLLocked() int64 {
	remaining := time.Until(l.expiry).Seconds()
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}

// isExpiredLocked reports whether the lease has passed its expiry time.
// The CALLER MUST HOLD l.mu -- see remainingTTLLocked.
func (l *lease) isExpiredLocked() bool {
	return time.Now().After(l.expiry)
}

// isExpired reports whether the lease has passed its expiry time, taking
// l.mu itself. Callers that do not already hold l.mu use this one.
//
// Lock ordering across this package is lr.mu then l.mu, never the reverse, so
// calling this while holding lr.mu (as Leases and ExpiredLeaseIDs do) is safe.
func (l *lease) isExpired() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.isExpiredLocked()
}

// NoLease is the lease ID that means "no lease". Binding a key to it releases
// the key from whatever lease held it, which is what a write without a lease
// does to a key that had one.
const NoLease int64 = 0

// Lessor manages the lifecycle of leases in memory. Lease mutations
// (grant, revoke, keep-alive, key binding) are driven by the state machine
// after Raft consensus, ensuring all replicas maintain consistent state.
//
// Every mutation except KeepAlive is also staged into the lease table
// (table.go) through the Writer the state machine passes in -- its apply
// batch -- so the table commits atomically with the data write that caused
// it, and Restore can rebuild this state after a restart or a snapshot
// recovery. The table is written FIRST: a mutation whose write fails leaves
// the in-memory state untouched.
//
// A Lessor serves exactly one state machine. Restore replaces the whole
// in-memory table with the one state machine's persisted copy, so sharing a
// Lessor across shards would let one shard's restore erase another's leases.
//
// The binding invariant: a key is bound to at most one lease, bindings[k]
// names it, and that lease's keys set holds k. Revoke therefore deletes only
// keys whose most recent write named the revoked lease -- a key re-put under
// another lease, or with none, has already left it.
type Lessor struct {
	mu       sync.RWMutex
	leases   map[int64]*lease
	bindings map[string]int64 // key -> the lease it is bound to
	idSeq    int64            // highest ID granted; guarded by mu
	closed   atomic.Bool
}

// NewLessor creates a new Lessor.
func NewLessor() *Lessor {
	return &Lessor{
		leases:   make(map[int64]*lease),
		bindings: make(map[string]int64),
	}
}

// newLease builds the in-memory state of lease id with a full TTL ahead of it.
func newLease(id, ttl int64) *lease {
	return &lease{
		id:     id,
		ttl:    ttl,
		expiry: time.Now().Add(time.Duration(ttl) * time.Second),
		keys:   make(map[string]struct{}),
	}
}

// Grant registers a new lease with the given TTL in seconds under the next
// ID in sequence, and records the lease and the advanced sequence through w.
// The sequence is part of the table, so a replica that restarts or recovers
// from a snapshot allocates the same next ID as its peers.
func (lr *Lessor) Grant(w Writer, ttl int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}
	if ttl <= 0 {
		return nil, &InvalidTTLError{TTL: ttl}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	id := lr.idSeq + 1
	if _, ok := lr.leases[id]; ok {
		return nil, &ExistsError{ID: id}
	}

	if err := w.Set(leaseRecordKey(id), encodeInt64(ttl)); err != nil {
		return nil, &TableWriteError{Err: err}
	}
	if err := w.Set([]byte(sequenceKey), encodeInt64(id)); err != nil {
		return nil, &TableWriteError{Err: err}
	}

	lr.idSeq = id
	l := newLease(id, ttl)
	lr.leases[id] = l

	return lr.toLease(l), nil
}

// Revoke removes a lease, releases every key bound to it, and returns those
// keys in ascending order. The caller is responsible for deleting them; the
// order is fixed so every replica emits the resulting delete events in the
// same sequence.
func (lr *Lessor) Revoke(w Writer, id int64) ([][]byte, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	l, ok := lr.leases[id]
	if !ok {
		return nil, &NotFoundError{ID: id}
	}

	l.mu.RLock()
	keys := make([][]byte, 0, len(l.keys))
	for k := range l.keys {
		keys = append(keys, []byte(k))
	}
	l.mu.RUnlock()
	slices.SortFunc(keys, bytes.Compare)

	for _, k := range keys {
		if err := w.Delete(bindingKey(k)); err != nil {
			return nil, &TableWriteError{Err: err}
		}
	}
	if err := w.Delete(leaseRecordKey(id)); err != nil {
		return nil, &TableWriteError{Err: err}
	}

	for _, k := range keys {
		delete(lr.bindings, string(k))
	}
	delete(lr.leases, id)

	return keys, nil
}

// KeepAlive resets the expiry of a lease to now + TTL. Returns the
// updated lease info.
//
// The reset is not written to the lease table. Expiry is wall-clock state
// local to each replica, and a replica rebuilding its table starts every lease
// on a full TTL (see Restore), so there is nothing durable to record.
func (lr *Lessor) KeepAlive(id int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}

	lr.mu.RLock()
	l, ok := lr.leases[id]
	lr.mu.RUnlock()

	if !ok {
		return nil, &NotFoundError{ID: id}
	}

	l.mu.Lock()
	l.expiry = time.Now().Add(time.Duration(l.ttl) * time.Second)
	l.mu.Unlock()

	return lr.toLease(l), nil
}

// Bind makes leaseID the one lease key belongs to, recording the change
// through w. It is called for EVERY write of key, with the lease that write
// named:
//
//   - a write naming a lease moves the key to it, out of any lease it was in;
//   - a write naming NoLease releases the key from any lease it was in.
//
// Either way, revoking or expiring a lease the key has left no longer deletes
// it. This is etcd's semantics: a key belongs to the lease of its latest
// value, not to every lease any of its values ever named.
//
// A leaseID the Lessor does not hold is a *NotFoundError and changes nothing.
// The state machine checks for that BEFORE writing the key (see Has), so the
// error here is a backstop rather than the refusal path.
func (lr *Lessor) Bind(w Writer, key []byte, leaseID int64) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	k := string(key)
	prev, bound := lr.bindings[k]

	if leaseID == NoLease {
		if !bound {
			return nil
		}
		if err := w.Delete(bindingKey(key)); err != nil {
			return &TableWriteError{Err: err}
		}
		lr.releaseLocked(prev, k)
		delete(lr.bindings, k)
		return nil
	}

	l, ok := lr.leases[leaseID]
	if !ok {
		return &NotFoundError{ID: leaseID}
	}
	if bound && prev == leaseID {
		return nil
	}
	if err := w.Set(bindingKey(key), encodeInt64(leaseID)); err != nil {
		return &TableWriteError{Err: err}
	}
	if bound {
		lr.releaseLocked(prev, k)
	}
	l.mu.Lock()
	l.keys[k] = struct{}{}
	l.mu.Unlock()
	lr.bindings[k] = leaseID
	return nil
}

// Unbind releases key from whatever lease it is bound to. It is what a delete
// of key does to its lease: Bind with NoLease.
func (lr *Lessor) Unbind(w Writer, key []byte) error {
	return lr.Bind(w, key, NoLease)
}

// releaseLocked removes k from lease id's key set. The CALLER MUST HOLD lr.mu
// for writing.
func (lr *Lessor) releaseLocked(id int64, k string) {
	l, ok := lr.leases[id]
	if !ok {
		return
	}
	l.mu.Lock()
	delete(l.keys, k)
	l.mu.Unlock()
}

// Has reports whether the Lessor holds lease id. NoLease is always held: a
// write naming it attaches nothing and needs no lease to exist.
func (lr *Lessor) Has(id int64) bool {
	if id == NoLease {
		return true
	}
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	_, ok := lr.leases[id]
	return ok
}

// LeaseOf returns the lease key is bound to, or NoLease.
func (lr *Lessor) LeaseOf(key []byte) int64 {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	return lr.bindings[string(key)]
}

// Restore replaces the Lessor's entire in-memory state with the lease table
// persisted in r. A state machine calls it when it opens and after it
// recovers from a snapshot -- the two moments its storage changes underneath
// the Lessor without passing through apply.
//
// Every restored lease starts a full TTL from now. The remaining TTL is
// wall-clock state that was never replicated, so the only safe value after a
// gap of unknown length is the granted one: expiring a lease early deletes
// keys whose owner may still be renewing it, while expiring it a TTL late
// costs one TTL of staleness. etcd makes the same choice when a new leader
// takes over its lessor.
//
// A table that fails to read or validate leaves the Lessor unchanged.
func (lr *Lessor) Restore(r storage.Reader) error {
	t, err := readTable(r)
	if err != nil {
		return err
	}

	leases := make(map[int64]*lease, len(t.ttls))
	for id, ttl := range t.ttls {
		leases[id] = newLease(id, ttl)
	}
	for k, id := range t.bindings {
		leases[id].keys[k] = struct{}{}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.leases = leases
	lr.bindings = t.bindings
	lr.idSeq = t.seq
	return nil
}

// Lookup returns the lease info for the given ID. Returns nil if the
// lease does not exist.
func (lr *Lessor) Lookup(id int64) *Lease {
	lr.mu.RLock()
	l, ok := lr.leases[id]
	lr.mu.RUnlock()

	if !ok {
		return nil
	}

	return lr.toLease(l)
}

// LookupOrNotFound is Lookup with the miss turned into a *NotFoundError
// instead of a nil result. It exists because every LeaseTimeToLiveQuery
// dispatcher (pkg/engine's MVCCEngine and pkg/store's PebbleStateMachine --
// each backs a different raft.sm_type, and both hold a live Lessor in a
// clustered deployment) needs to answer a missing lease identically, and
// this is the single place that decision is made, so the two callers
// cannot drift apart on what "not found" means. Callers translate the
// returned *NotFoundError into their own layer's typed not-found error
// (e.g. service.LeaseNotFoundError) at the boundary, the same way
// Revoke/KeepAlive's callers already do.
func (lr *Lessor) LookupOrNotFound(id int64) (*Lease, error) {
	l := lr.Lookup(id)
	if l == nil {
		return nil, &NotFoundError{ID: id}
	}
	return l, nil
}

// Leases returns info for all active (non-expired) leases.
func (lr *Lessor) Leases() []*Lease {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	result := make([]*Lease, 0, len(lr.leases))
	for _, l := range lr.leases {
		if l.isExpired() {
			continue
		}
		result = append(result, lr.toLease(l))
	}
	return result
}

// ExpiredLeaseIDs returns the IDs of all expired leases.
func (lr *Lessor) ExpiredLeaseIDs() []int64 {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	var expired []int64
	for id, l := range lr.leases {
		if l.isExpired() {
			expired = append(expired, id)
		}
	}
	return expired
}

// Count returns the number of active leases.
func (lr *Lessor) Count() int {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	return len(lr.leases)
}

// Close marks the lessor as closed. Subsequent operations will return
// LessorClosedError.
func (lr *Lessor) Close() {
	lr.closed.Store(true)
}

// toLease converts an internal lease to the exported Lease type.
func (lr *Lessor) toLease(l *lease) *Lease {
	l.mu.RLock()
	defer l.mu.RUnlock()

	keys := make([][]byte, 0, len(l.keys))
	for k := range l.keys {
		keys = append(keys, []byte(k))
	}

	return &Lease{
		ID:           l.id,
		TTL:          l.ttl,
		GrantedTTL:   l.ttl,
		RemainingTTL: l.remainingTTLLocked(),
		Keys:         keys,
	}
}
