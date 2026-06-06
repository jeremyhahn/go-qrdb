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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockLockSvc is an in-memory service.LockServicer for transport round-trip
// tests.
// ---------------------------------------------------------------------------

type mockLockSvc struct {
	mu sync.Mutex

	lockErr    error
	tryLockErr error
	unlockErr  error
	extendErr  error
	statusErr  error
	listErr    error

	lastKey      string
	lastHolderID string
	lastTTL      time.Duration
	lastReent    bool
	lastFencing  int64
	lastPrefix   string

	statusResp  *lock.Lock
	defaultResp *lock.Lock
	listResp    []*lock.Lock
}

var _ service.LockServicer = (*mockLockSvc)(nil)

func newMockLockSvc() *mockLockSvc {
	defaultLock := &lock.Lock{
		Key:          "resource",
		FencingToken: 42,
		LeaseID:      1001,
		HolderID:     "holder-1",
		Reentrant:    false,
		AcquireCount: 1,
		TTL:          30,
		AcquiredAt:   1_700_000_000,
	}
	return &mockLockSvc{
		defaultResp: defaultLock,
		listResp: []*lock.Lock{
			defaultLock,
			{Key: "other", FencingToken: 43, HolderID: "holder-2", TTL: 60, AcquiredAt: 1_700_000_500},
		},
	}
}

func (m *mockLockSvc) Lock(_ context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastKey = key
	m.lastHolderID = holderID
	m.lastTTL = ttl
	m.lastReent = reentrant
	if m.lockErr != nil {
		return nil, m.lockErr
	}
	return m.defaultResp, nil
}

func (m *mockLockSvc) TryLock(_ context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastKey = key
	m.lastHolderID = holderID
	m.lastTTL = ttl
	m.lastReent = reentrant
	if m.tryLockErr != nil {
		return nil, m.tryLockErr
	}
	return m.defaultResp, nil
}

func (m *mockLockSvc) Unlock(_ context.Context, key string, fencingToken int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastKey = key
	m.lastFencing = fencingToken
	return m.unlockErr
}

func (m *mockLockSvc) Extend(_ context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastKey = key
	m.lastFencing = fencingToken
	if m.extendErr != nil {
		return nil, m.extendErr
	}
	return m.defaultResp, nil
}

func (m *mockLockSvc) Status(_ context.Context, key string) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastKey = key
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResp, nil
}

func (m *mockLockSvc) List(_ context.Context, prefix string) ([]*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPrefix = prefix
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

func connectLockInProcess(t *testing.T, svc service.LockServicer) (*Transport, func()) {
	t.Helper()

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithLock(svc),
	)

	mcpSrv := srv.MCPServer()
	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// LockAcquire
// ---------------------------------------------------------------------------

func TestLockAcquire_Success(t *testing.T) {
	svc := newMockLockSvc()
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	info, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{
		Key:       "resource",
		HolderID:  "holder-1",
		TTL:       30,
		Reentrant: false,
	})
	if err != nil {
		t.Fatalf("LockAcquire: %v", err)
	}
	if info.Key != "resource" {
		t.Errorf("key: got %q, want %q", info.Key, "resource")
	}
	if info.FencingToken != 42 {
		t.Errorf("fencing: got %d, want 42", info.FencingToken)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastKey != "resource" {
		t.Errorf("recorded key: %q", svc.lastKey)
	}
	if svc.lastHolderID != "holder-1" {
		t.Errorf("recorded holder: %q", svc.lastHolderID)
	}
}

func TestLockAcquire_ServiceError(t *testing.T) {
	svc := newMockLockSvc()
	svc.lockErr = errors.New("timeout")
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	_, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{Key: "x", TTL: 10})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockTryLock
// ---------------------------------------------------------------------------

func TestLockTryLock_Success(t *testing.T) {
	svc := newMockLockSvc()
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	info, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{
		Key:      "resource",
		HolderID: "holder-1",
		TTL:      10,
	})
	if err != nil {
		t.Fatalf("LockTryLock: %v", err)
	}
	if info.HolderID != "holder-1" {
		t.Errorf("holder: %q", info.HolderID)
	}
}

func TestLockTryLock_ServiceError(t *testing.T) {
	svc := newMockLockSvc()
	svc.tryLockErr = errors.New("lock held")
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	_, err := tr.LockTryLock(context.Background(), &transport.LockAcquireRequest{Key: "x", TTL: 10})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockRelease
// ---------------------------------------------------------------------------

func TestLockRelease_Success(t *testing.T) {
	svc := newMockLockSvc()
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{
		Key:          "resource",
		FencingToken: 42,
	})
	if err != nil {
		t.Fatalf("LockRelease: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastFencing != 42 {
		t.Errorf("fencing: %d", svc.lastFencing)
	}
}

func TestLockRelease_ServiceError(t *testing.T) {
	svc := newMockLockSvc()
	svc.unlockErr = errors.New("stale token")
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	if err := tr.LockRelease(context.Background(), &transport.LockReleaseRequest{Key: "x", FencingToken: 1}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockExtend
// ---------------------------------------------------------------------------

func TestLockExtend_Success(t *testing.T) {
	svc := newMockLockSvc()
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	info, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{
		Key:          "resource",
		FencingToken: 42,
	})
	if err != nil {
		t.Fatalf("LockExtend: %v", err)
	}
	if info.Key != "resource" {
		t.Errorf("key: %q", info.Key)
	}
}

func TestLockExtend_ServiceError(t *testing.T) {
	svc := newMockLockSvc()
	svc.extendErr = errors.New("expired")
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	_, err := tr.LockExtend(context.Background(), &transport.LockExtendRequest{Key: "x", FencingToken: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockStatus
// ---------------------------------------------------------------------------

func TestLockStatus_Success(t *testing.T) {
	svc := newMockLockSvc()
	svc.statusResp = svc.defaultResp
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	info, err := tr.LockStatus(context.Background(), "resource")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil status")
	}
	if info.Key != "resource" {
		t.Errorf("key: %q", info.Key)
	}
}

func TestLockStatus_NotHeld(t *testing.T) {
	svc := newMockLockSvc()
	svc.statusResp = nil // null indicates no lock held
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	info, err := tr.LockStatus(context.Background(), "unheld")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for unheld lock, got %+v", info)
	}
}

func TestLockStatus_ServiceError(t *testing.T) {
	svc := newMockLockSvc()
	svc.statusErr = errors.New("backend error")
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	_, err := tr.LockStatus(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LockList
// ---------------------------------------------------------------------------

func TestLockList_Success(t *testing.T) {
	svc := newMockLockSvc()
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	resp, err := tr.LockList(context.Background(), "")
	if err != nil {
		t.Fatalf("LockList: %v", err)
	}
	if len(resp.Locks) != 2 {
		t.Errorf("locks: got %d, want 2", len(resp.Locks))
	}
}

func TestLockList_WithPrefix(t *testing.T) {
	svc := newMockLockSvc()
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	_, err := tr.LockList(context.Background(), "res")
	if err != nil {
		t.Fatalf("LockList: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastPrefix != "res" {
		t.Errorf("prefix: %q", svc.lastPrefix)
	}
}

func TestLockList_ServiceError(t *testing.T) {
	svc := newMockLockSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectLockInProcess(t, svc)
	defer cleanup()

	if _, err := tr.LockList(context.Background(), ""); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// serverLockToTransport
// ---------------------------------------------------------------------------

func TestServerLockToTransport(t *testing.T) {
	in := &serverLock{
		Key:          "k",
		FencingToken: 1,
		LeaseID:      2,
		HolderID:     "h",
		Reentrant:    true,
		AcquireCount: 3,
		TTL:          30,
		AcquiredAt:   1_700_000_000,
	}
	out := serverLockToTransport(in)
	if out.Key != "k" || out.FencingToken != 1 || out.LeaseID != 2 || out.HolderID != "h" {
		t.Errorf("conversion: %+v", out)
	}
	if !out.Reentrant || out.AcquireCount != 3 || out.TTL != 30 {
		t.Errorf("conversion: %+v", out)
	}
}
