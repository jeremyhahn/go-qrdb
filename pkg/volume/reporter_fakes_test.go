// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// fakeMVCC extends fakeKV with PutWithLease, tracking which key each
// granted lease covers so fakeLeases.Revoke can delete it -- the behavior
// service.MVCCKVService documents ("when the lease expires, the key is
// automatically deleted") and Reporter.Close relies on.
type fakeMVCC struct {
	*fakeKV
	leases *fakeLeases
}

func newFakeMVCC(leases *fakeLeases) *fakeMVCC {
	mvcc := &fakeMVCC{fakeKV: newFakeKV(), leases: leases}
	leases.onExpire = mvcc.deleteKeysForLease
	return mvcc
}

func (m *fakeMVCC) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	if err := m.Put(ctx, key, value); err != nil {
		return nil, err
	}
	m.leases.attach(leaseID, key)
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID}, nil
}

// deleteKeysForLease removes every key fakeLeases recorded against
// leaseID, standing in for the engine's own lease-expiry sweep.
func (m *fakeMVCC) deleteKeysForLease(keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
}

// fakeLeases is an in-memory lease.LeaseServicer double. A lease never
// expires on its own (there is no clock in this fake); Expire simulates
// the engine's sweep for tests that need "the lease is gone" without
// waiting out a real TTL.
type fakeLeases struct {
	mu       sync.Mutex
	nextID   int64
	ttl      map[int64]int64
	keys     map[int64][]string
	revoked  map[int64]bool
	onExpire func(keys []string)

	// failGrant / failKeepAlive, when set, are returned instead of a real
	// grant/keepalive -- the fault seam Reporter's self-healing tests need.
	// Both are read under mu; setFailGrant changes one while a refresh loop
	// is running.
	failGrant     error
	failKeepAlive error
}

func newFakeLeases() *fakeLeases {
	return &fakeLeases{
		ttl:     make(map[int64]int64),
		keys:    make(map[int64][]string),
		revoked: make(map[int64]bool),
	}
}

// setFailGrant sets (or, with nil, clears) the error Grant returns.
func (f *fakeLeases) setFailGrant(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failGrant = err
}

func (f *fakeLeases) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGrant != nil {
		return nil, f.failGrant
	}
	f.nextID++
	id := f.nextID
	f.ttl[id] = ttl
	return &lease.Lease{ID: id, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

func (f *fakeLeases) Revoke(_ context.Context, leaseID int64) error {
	f.mu.Lock()
	keys := f.keys[leaseID]
	f.revoked[leaseID] = true
	delete(f.keys, leaseID)
	delete(f.ttl, leaseID)
	onExpire := f.onExpire
	f.mu.Unlock()
	if onExpire != nil {
		onExpire(keys)
	}
	return nil
}

func (f *fakeLeases) KeepAlive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKeepAlive != nil {
		return nil, f.failKeepAlive
	}
	ttl, ok := f.ttl[leaseID]
	if !ok || f.revoked[leaseID] {
		return nil, &InvalidCatalogDependencyError{Reason: "fakeLeases: unknown or revoked lease"}
	}
	return &lease.Lease{ID: leaseID, TTL: ttl, RemainingTTL: ttl}, nil
}

func (f *fakeLeases) TimeToLive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ttl, ok := f.ttl[leaseID]
	if !ok {
		return nil, &InvalidCatalogDependencyError{Reason: "fakeLeases: unknown lease"}
	}
	return &lease.Lease{ID: leaseID, TTL: ttl, RemainingTTL: ttl, Keys: nil}, nil
}

func (f *fakeLeases) Leases(_ context.Context) ([]*lease.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*lease.Lease, 0, len(f.ttl))
	for id, ttl := range f.ttl {
		out = append(out, &lease.Lease{ID: id, TTL: ttl})
	}
	return out, nil
}

// attach records that key is (now also) covered by leaseID.
func (f *fakeLeases) attach(leaseID int64, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[leaseID] = append(f.keys[leaseID], key)
}

// expire simulates the engine deleting every key attached to leaseID
// because its TTL lapsed, without an explicit Revoke call.
func (f *fakeLeases) expire(leaseID int64) {
	f.mu.Lock()
	keys := f.keys[leaseID]
	delete(f.keys, leaseID)
	delete(f.ttl, leaseID)
	onExpire := f.onExpire
	f.mu.Unlock()
	if onExpire != nil {
		onExpire(keys)
	}
}
