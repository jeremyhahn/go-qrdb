// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Round-trip tests for lease, lock, and index services. These services have
// simple interfaces with no complex domain dependencies, making them ideal
// targets for additional coverage via real-server round-trip testing.

package unix

import (
	"context"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockLeaseSvc — lease.LeaseServicer stub
// ---------------------------------------------------------------------------

type mockLeaseSvc struct {
	grantFn     func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn    func(ctx context.Context, id int64) error
	keepAliveFn func(ctx context.Context, id int64) (*lease.Lease, error)
	ttlFn       func(ctx context.Context, id int64) (*lease.Lease, error)
	leasesFn    func(ctx context.Context) ([]*lease.Lease, error)
}

func (m *mockLeaseSvc) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if m.grantFn != nil {
		return m.grantFn(ctx, ttl)
	}
	return &lease.Lease{ID: 42, TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

func (m *mockLeaseSvc) Revoke(ctx context.Context, id int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, id)
	}
	return nil
}

func (m *mockLeaseSvc) KeepAlive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, GrantedTTL: 60, RemainingTTL: 60}, nil
}

func (m *mockLeaseSvc) TimeToLive(ctx context.Context, id int64) (*lease.Lease, error) {
	if m.ttlFn != nil {
		return m.ttlFn(ctx, id)
	}
	return &lease.Lease{ID: id, TTL: 60, RemainingTTL: 30, Keys: [][]byte{[]byte("k1"), []byte("k2")}}, nil
}

func (m *mockLeaseSvc) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return []*lease.Lease{{ID: 1, TTL: 60}, {ID: 2, TTL: 120}}, nil
}

var _ lease.LeaseServicer = (*mockLeaseSvc)(nil)

// ---------------------------------------------------------------------------
// mockLockSvc — service.LockServicer stub
// ---------------------------------------------------------------------------

type mockLockSvc struct {
	lockFn    func(ctx context.Context, key, holder string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holder string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, fencingToken int64) error
	extendFn  func(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

func (m *mockLockSvc) Lock(ctx context.Context, key, holder string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holder, ttl, reentrant)
	}
	return &lock.Lock{Key: key, FencingToken: 1, LeaseID: 7, HolderID: holder, TTL: int64(ttl.Seconds())}, nil
}

func (m *mockLockSvc) TryLock(ctx context.Context, key, holder string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holder, ttl, reentrant)
	}
	return &lock.Lock{Key: key, FencingToken: 2, LeaseID: 8, HolderID: holder}, nil
}

func (m *mockLockSvc) Unlock(ctx context.Context, key string, fencingToken int64) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, key, fencingToken)
	}
	return nil
}

func (m *mockLockSvc) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	if m.extendFn != nil {
		return m.extendFn(ctx, key, fencingToken)
	}
	return &lock.Lock{Key: key, FencingToken: fencingToken + 1, TTL: 120}, nil
}

func (m *mockLockSvc) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return &lock.Lock{Key: key, FencingToken: 99, HolderID: "owner"}, nil
}

func (m *mockLockSvc) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return []*lock.Lock{{Key: "k1"}, {Key: "k2"}}, nil
}

var _ service.LockServicer = (*mockLockSvc)(nil)

// ---------------------------------------------------------------------------
// mockIndexSvc — index.IndexServicer stub
// ---------------------------------------------------------------------------

type mockIndexSvc struct {
	createFn func(ctx context.Context, name, field string, cfg index.IndexConfig) error
	dropFn   func(ctx context.Context, name string) error
	queryFn  func(ctx context.Context, name string, value []byte) ([][]byte, error)
	scanFn   func(ctx context.Context, name string, start, end []byte) (map[string][][]byte, error)
	listFn   func(ctx context.Context) ([]string, error)
}

func (m *mockIndexSvc) CreateIndex(ctx context.Context, name, field string, cfg index.IndexConfig) error {
	if m.createFn != nil {
		return m.createFn(ctx, name, field, cfg)
	}
	return nil
}

func (m *mockIndexSvc) DropIndex(ctx context.Context, name string) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, name)
	}
	return nil
}

func (m *mockIndexSvc) Query(ctx context.Context, name string, value []byte) ([][]byte, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, name, value)
	}
	return [][]byte{[]byte("k1"), []byte("k2")}, nil
}

func (m *mockIndexSvc) Scan(ctx context.Context, name string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, name, start, end)
	}
	return map[string][][]byte{"v1": {[]byte("k1")}, "v2": {[]byte("k2")}}, nil
}

func (m *mockIndexSvc) ListIndexes(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{"idx1", "idx2"}, nil
}

var _ index.IndexServicer = (*mockIndexSvc)(nil)

// ---------------------------------------------------------------------------
// Lease round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_LeaseGrant(t *testing.T) {
	svc := &mockLeaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	got, err := tr.LeaseGrant(context.Background(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	if got.ID != 42 || got.TTL != 60 {
		t.Errorf("got %+v, want ID=42 TTL=60", got)
	}
}

func TestUnixRT_LeaseRevoke(t *testing.T) {
	svc := &mockLeaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	if err := tr.LeaseRevoke(context.Background(), 42); err != nil {
		t.Fatalf("LeaseRevoke: %v", err)
	}
}

func TestUnixRT_LeaseKeepAlive(t *testing.T) {
	svc := &mockLeaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	got, err := tr.LeaseKeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseKeepAlive: %v", err)
	}
	if got.ID != 42 {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_LeaseTimeToLive(t *testing.T) {
	svc := &mockLeaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	got, err := tr.LeaseTimeToLive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if got.ID != 42 || len(got.Keys) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_LeaseList(t *testing.T) {
	svc := &mockLeaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLease(svc))
	defer cleanup()

	got, err := tr.LeaseList(context.Background())
	if err != nil {
		t.Fatalf("LeaseList: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d, want 2", len(got))
	}
}

// ---------------------------------------------------------------------------
// Lock round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_LockAcquire(t *testing.T) {
	svc := &mockLockSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	got, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{
		Key:      "lock1",
		HolderID: "owner1",
		TTL:      60,
	})
	if err != nil {
		t.Fatalf("LockAcquire: %v", err)
	}
	if got.Key != "lock1" || got.FencingToken != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_LockTryLock(t *testing.T) {
	svc := &mockLockSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	got, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{
		Key:      "lock1",
		HolderID: "owner1",
		TTL:      60,
	})
	if err != nil {
		t.Fatalf("LockTryLock: %v", err)
	}
	if got.Key != "lock1" || got.FencingToken != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_LockRelease(t *testing.T) {
	svc := &mockLockSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{
		Key:          "lock1",
		FencingToken: 1,
	}); err != nil {
		t.Fatalf("LockRelease: %v", err)
	}
}

func TestUnixRT_LockExtend(t *testing.T) {
	svc := &mockLockSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	got, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{
		Key:          "lock1",
		FencingToken: 1,
	})
	if err != nil {
		t.Fatalf("LockExtend: %v", err)
	}
	if got.FencingToken != 2 {
		t.Errorf("got FencingToken=%d, want 2", got.FencingToken)
	}
}

func TestUnixRT_LockStatus(t *testing.T) {
	svc := &mockLockSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	got, err := tr.LockStatus(context.Background(), "lock1")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if got == nil || got.Key != "lock1" || got.HolderID != "owner" {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_LockList(t *testing.T) {
	svc := &mockLockSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithLock(svc))
	defer cleanup()

	got, err := tr.LockList(context.Background(), "")
	if err != nil {
		t.Fatalf("LockList: %v", err)
	}
	if got == nil || len(got.Locks) != 2 {
		t.Errorf("got %+v, want 2 locks", got)
	}
}

// ---------------------------------------------------------------------------
// Index round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_IndexCreate(t *testing.T) {
	svc := &mockIndexSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithIndex(svc))
	defer cleanup()

	if err := tr.IndexCreate(context.Background(), "idx1", "field1", transport.IndexConfig{}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
}

func TestUnixRT_IndexDrop(t *testing.T) {
	svc := &mockIndexSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithIndex(svc))
	defer cleanup()

	if err := tr.IndexDrop(context.Background(), "idx1"); err != nil {
		t.Fatalf("IndexDrop: %v", err)
	}
}

func TestUnixRT_IndexQuery(t *testing.T) {
	svc := &mockIndexSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithIndex(svc))
	defer cleanup()

	got, err := tr.IndexQuery(context.Background(), "idx1", "value")
	if err != nil {
		t.Fatalf("IndexQuery: %v", err)
	}
	if got == nil || len(got.Keys) != 2 {
		t.Errorf("got %+v, want 2 keys", got)
	}
}

func TestUnixRT_IndexScan(t *testing.T) {
	svc := &mockIndexSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithIndex(svc))
	defer cleanup()

	got, err := tr.IndexScan(context.Background(), "idx1", "a", "z")
	if err != nil {
		t.Fatalf("IndexScan: %v", err)
	}
	if got == nil || len(got.Entries) != 2 {
		t.Errorf("got %+v, want 2 entries", got)
	}
}

func TestUnixRT_IndexList(t *testing.T) {
	svc := &mockIndexSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithIndex(svc))
	defer cleanup()

	got, err := tr.IndexList(context.Background())
	if err != nil {
		t.Fatalf("IndexList: %v", err)
	}
	if got == nil || len(got.Names) != 2 {
		t.Errorf("got %+v, want 2 names", got)
	}
}
