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
// in-memory lease table that is rebuilt from committed log entries on
// startup. Only the Raft leader runs the expiration timer to avoid
// duplicate revocations.
package lease

import (
	"sync"
	"sync/atomic"
	"time"
)

// lease is the internal lease state tracked by the Lessor.
type lease struct {
	id     int64
	ttl    int64 // granted TTL in seconds
	expiry time.Time
	keys   map[string]struct{} // attached keys
	mu     sync.RWMutex
}

// remainingTTL returns the remaining TTL in seconds.
func (l *lease) remainingTTL() int64 {
	remaining := time.Until(l.expiry).Seconds()
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}

// isExpired returns true if the lease has passed its expiry time.
func (l *lease) isExpired() bool {
	return time.Now().After(l.expiry)
}

// Lessor manages the lifecycle of leases in memory. Lease mutations
// (grant, revoke, keep-alive) are driven by the state machine after
// Raft consensus, ensuring all replicas maintain consistent state.
type Lessor struct {
	mu     sync.RWMutex
	leases map[int64]*lease
	idSeq  atomic.Int64
	closed atomic.Bool
}

// NewLessor creates a new Lessor.
func NewLessor() *Lessor {
	return &Lessor{
		leases: make(map[int64]*lease),
	}
}

// Grant registers a new lease with the given TTL in seconds. The lease
// ID is auto-generated using an atomic sequence. Returns the lease info
// including the assigned ID.
func (lr *Lessor) Grant(ttl int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}
	if ttl <= 0 {
		return nil, &InvalidTTLError{TTL: ttl}
	}

	id := lr.idSeq.Add(1)
	return lr.GrantWithID(id, ttl)
}

// GrantWithID registers a new lease with the specified ID and TTL.
// Used by the state machine to replay leases with deterministic IDs.
func (lr *Lessor) GrantWithID(id, ttl int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}
	if ttl <= 0 {
		return nil, &InvalidTTLError{TTL: ttl}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	if _, ok := lr.leases[id]; ok {
		return nil, &ExistsError{ID: id}
	}

	l := &lease{
		id:     id,
		ttl:    ttl,
		expiry: time.Now().Add(time.Duration(ttl) * time.Second),
		keys:   make(map[string]struct{}),
	}
	lr.leases[id] = l

	return lr.toLease(l), nil
}

// Revoke removes a lease and returns the keys that were attached to it.
// The caller is responsible for deleting the attached keys.
func (lr *Lessor) Revoke(id int64) ([][]byte, error) {
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

	delete(lr.leases, id)

	return keys, nil
}

// KeepAlive resets the expiry of a lease to now + TTL. Returns the
// updated lease info.
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

// AttachKey associates a key with a lease. If the lease does not exist
// or is expired, the attachment is silently ignored (the state machine
// handles validation).
func (lr *Lessor) AttachKey(leaseID int64, key []byte) {
	lr.mu.RLock()
	l, ok := lr.leases[leaseID]
	lr.mu.RUnlock()

	if !ok {
		return
	}

	l.mu.Lock()
	l.keys[string(key)] = struct{}{}
	l.mu.Unlock()
}

// DetachKey removes a key's association with a lease. If the lease
// does not exist, the detachment is silently ignored.
func (lr *Lessor) DetachKey(leaseID int64, key []byte) {
	lr.mu.RLock()
	l, ok := lr.leases[leaseID]
	lr.mu.RUnlock()

	if !ok {
		return
	}

	l.mu.Lock()
	delete(l.keys, string(key))
	l.mu.Unlock()
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
		RemainingTTL: l.remainingTTL(),
		Keys:         keys,
	}
}
