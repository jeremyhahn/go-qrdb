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

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockLeaseSvc is an in-memory service.LeaseServicer for transport tests.
// ---------------------------------------------------------------------------

type mockLeaseSvc struct {
	mu sync.Mutex

	grantErr     error
	revokeErr    error
	keepAliveErr error
	ttlErr       error
	listErr      error

	lastGrantTTL int64
	lastLeaseID  int64

	defaultLease *lease.Lease
	listResp     []*lease.Lease
}

var _ service.LeaseServicer = (*mockLeaseSvc)(nil)

func newMockLeaseSvc() *mockLeaseSvc {
	dl := &lease.Lease{
		ID:           100,
		TTL:          30,
		GrantedTTL:   60,
		RemainingTTL: 25,
		Keys:         [][]byte{[]byte("key1"), []byte("key2")},
	}
	return &mockLeaseSvc{
		defaultLease: dl,
		listResp:     []*lease.Lease{dl, {ID: 101, TTL: 60}},
	}
}

func (m *mockLeaseSvc) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGrantTTL = ttl
	if m.grantErr != nil {
		return nil, m.grantErr
	}
	return m.defaultLease, nil
}

func (m *mockLeaseSvc) Revoke(_ context.Context, leaseID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastLeaseID = leaseID
	return m.revokeErr
}

func (m *mockLeaseSvc) KeepAlive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastLeaseID = leaseID
	if m.keepAliveErr != nil {
		return nil, m.keepAliveErr
	}
	return m.defaultLease, nil
}

func (m *mockLeaseSvc) TimeToLive(_ context.Context, leaseID int64) (*lease.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastLeaseID = leaseID
	if m.ttlErr != nil {
		return nil, m.ttlErr
	}
	return m.defaultLease, nil
}

func (m *mockLeaseSvc) Leases(_ context.Context) ([]*lease.Lease, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func connectLeaseInProcess(t *testing.T, svc service.LeaseServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithLease(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// LeaseGrant
// ---------------------------------------------------------------------------

func TestLeaseGrant_Success(t *testing.T) {
	svc := newMockLeaseSvc()
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.LeaseGrant(context.Background(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	if info.ID != 100 {
		t.Errorf("ID: got %d, want 100", info.ID)
	}
	if info.TTL != 30 {
		t.Errorf("TTL: got %d, want 30", info.TTL)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastGrantTTL != 60 {
		t.Errorf("granted ttl: got %d, want 60", svc.lastGrantTTL)
	}
}

func TestLeaseGrant_ServiceError(t *testing.T) {
	svc := newMockLeaseSvc()
	svc.grantErr = errors.New("ttl too high")
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.LeaseGrant(context.Background(), 10); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LeaseRevoke
// ---------------------------------------------------------------------------

func TestLeaseRevoke_Success(t *testing.T) {
	svc := newMockLeaseSvc()
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	if err := tr.LeaseRevoke(context.Background(), 100); err != nil {
		t.Fatalf("LeaseRevoke: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastLeaseID != 100 {
		t.Errorf("revoked id: got %d, want 100", svc.lastLeaseID)
	}
}

func TestLeaseRevoke_ServiceError(t *testing.T) {
	svc := newMockLeaseSvc()
	svc.revokeErr = errors.New("not found")
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	if err := tr.LeaseRevoke(context.Background(), 999); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LeaseKeepAlive
// ---------------------------------------------------------------------------

func TestLeaseKeepAlive_Success(t *testing.T) {
	svc := newMockLeaseSvc()
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.LeaseKeepAlive(context.Background(), 100)
	if err != nil {
		t.Fatalf("LeaseKeepAlive: %v", err)
	}
	if info.ID != 100 {
		t.Errorf("ID: %d", info.ID)
	}
}

func TestLeaseKeepAlive_ServiceError(t *testing.T) {
	svc := newMockLeaseSvc()
	svc.keepAliveErr = errors.New("expired")
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.LeaseKeepAlive(context.Background(), 100); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LeaseTimeToLive
// ---------------------------------------------------------------------------

func TestLeaseTimeToLive_Success(t *testing.T) {
	svc := newMockLeaseSvc()
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.LeaseTimeToLive(context.Background(), 100)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if info.ID != 100 {
		t.Errorf("ID: %d", info.ID)
	}
}

func TestLeaseTimeToLive_ServiceError(t *testing.T) {
	svc := newMockLeaseSvc()
	svc.ttlErr = errors.New("not found")
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.LeaseTimeToLive(context.Background(), 100); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// LeaseList
// ---------------------------------------------------------------------------

func TestLeaseList_Success(t *testing.T) {
	svc := newMockLeaseSvc()
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	leases, err := tr.LeaseList(context.Background())
	if err != nil {
		t.Fatalf("LeaseList: %v", err)
	}
	if len(leases) != 2 {
		t.Errorf("count: got %d, want 2", len(leases))
	}
}

func TestLeaseList_ServiceError(t *testing.T) {
	svc := newMockLeaseSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectLeaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.LeaseList(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// serverLeaseToTransport
// ---------------------------------------------------------------------------

func TestServerLeaseToTransport(t *testing.T) {
	in := &serverLease{
		ID:           10,
		TTL:          20,
		GrantedTTL:   30,
		RemainingTTL: 5,
		Keys:         [][]byte{[]byte("a"), []byte("b")},
	}
	out := serverLeaseToTransport(in)
	if out.ID != 10 || out.TTL != 20 || out.GrantedTTL != 30 || out.RemainingTTL != 5 {
		t.Errorf("conversion: %+v", out)
	}
	if len(out.Keys) != 2 {
		t.Errorf("keys: %d", len(out.Keys))
	}
}
