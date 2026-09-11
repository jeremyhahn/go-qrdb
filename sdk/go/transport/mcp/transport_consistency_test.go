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
	"bytes"
	"context"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// trackingKVService records which service method was last called.
// ---------------------------------------------------------------------------

type trackingKVService struct {
	mu     sync.Mutex
	called string

	// Embedded simple store for happy-path tests.
	store map[string][]byte
}

func newTrackingKVService() *trackingKVService {
	return &trackingKVService{store: make(map[string][]byte)}
}

func (m *trackingKVService) record(method string) {
	m.mu.Lock()
	m.called = method
	m.mu.Unlock()
}

func (m *trackingKVService) Put(_ context.Context, key string, value []byte) error {
	m.record("Put")
	m.mu.Lock()
	m.store[key] = value
	m.mu.Unlock()
	return nil
}

func (m *trackingKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	m.record("PutIfAbsent")
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *trackingKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, key)
	return true, nil
}

func (m *trackingKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	m.record("PutWithMode")
	m.mu.Lock()
	m.store[key] = value
	m.mu.Unlock()
	return nil
}

func (m *trackingKVService) Get(_ context.Context, key string) ([]byte, error) {
	m.record("Get")
	m.mu.Lock()
	v := m.store[key]
	m.mu.Unlock()
	return v, nil
}

func (m *trackingKVService) GetWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	m.record("GetWithConsistency")
	m.mu.Lock()
	v := m.store[key]
	m.mu.Unlock()
	return v, nil
}

func (m *trackingKVService) Delete(_ context.Context, _ string) error {
	m.record("Delete")
	return nil
}

func (m *trackingKVService) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	m.record("DeleteWithMode")
	return nil
}

func (m *trackingKVService) List(_ context.Context, _ string) ([]string, error) {
	m.record("List")
	return nil, nil
}

func (m *trackingKVService) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	m.record("ListWithConsistency")
	return nil, nil
}

func (m *trackingKVService) Scan(_ context.Context, _ string) (map[string][]byte, error) {
	m.record("Scan")
	return map[string][]byte{}, nil
}

func (m *trackingKVService) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	m.record("ScanWithConsistency")
	return map[string][]byte{}, nil
}

func (m *trackingKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}
func (m *trackingKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}
func (m *trackingKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}
func (m *trackingKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}
func (m *trackingKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}
func (m *trackingKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
func (m *trackingKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}
func (m *trackingKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	v, err := m.Get(ctx, key)
	if err != nil {
		return false, err
	}
	return v != nil, nil
}

var _ service.KVServicer = (*trackingKVService)(nil)

// ---------------------------------------------------------------------------
// trackingMVCCSvc records which MVCC method was called.
// ---------------------------------------------------------------------------

type trackingMVCCSvc struct {
	*trackingKVService
	mu2    sync.Mutex
	called string
}

func newTrackingMVCCSvc() *trackingMVCCSvc {
	return &trackingMVCCSvc{trackingKVService: newTrackingKVService()}
}

func (m *trackingMVCCSvc) record(method string) {
	m.mu2.Lock()
	m.called = method
	m.mu2.Unlock()
}

func (m *trackingMVCCSvc) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	m.record("PutWithLease")
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, Version: 1}, nil
}

func (m *trackingMVCCSvc) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	m.record("PutWithLeaseAndMode")
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, Version: 1}, nil
}

func (m *trackingMVCCSvc) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	m.record("GetRevision")
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 5, Version: 1}, nil
}

func (m *trackingMVCCSvc) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	m.record("GetRevisionWithConsistency")
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: 5, Version: 1}, nil
}

func (m *trackingMVCCSvc) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	m.record("GetAtRevision")
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *trackingMVCCSvc) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	m.record("GetAtRevisionWithConsistency")
	return &kv.KeyValue{Key: []byte(key), Value: []byte("v"), ModRevision: revision, Version: 1}, nil
}

func (m *trackingMVCCSvc) Range(_ context.Context, start, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	m.record("Range")
	return []*kv.KeyValue{{Key: start, Value: []byte("v"), ModRevision: 1}}, 1, 1, nil
}

func (m *trackingMVCCSvc) RangeWithConsistency(_ context.Context, start, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	m.record("RangeWithConsistency")
	return []*kv.KeyValue{{Key: start, Value: []byte("v"), ModRevision: 1}}, 1, 1, nil
}

func (m *trackingMVCCSvc) Compact(_ context.Context, _ int64) error         { return nil }
func (m *trackingMVCCSvc) CurrentRevision(_ context.Context) (int64, error) { return 1, nil }

func (m *trackingMVCCSvc) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	m.record("RestoreToRevision")
	return &service.RestoreRevisionResult{
		Revision: req.Revision,
		Changes: []service.RestoreKeyChange{
			{Key: "restored-key", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3, ValueBytes: 7},
		},
		Applied: 1,
		DryRun:  req.DryRun,
	}, nil
}

var _ service.MVCCKVServicer = (*trackingMVCCSvc)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func connectKVTracking(t *testing.T, svc *trackingKVService) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(svc, newMockAdminService(), ":0")
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

func connectMVCCTracking(t *testing.T, svc *trackingMVCCSvc) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), ":0",
		mcpapi.WithMVCC(svc),
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
// KV Get: consistency arg routing
// ---------------------------------------------------------------------------

func TestSDKGet_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingKVService()
	svc.mu.Lock()
	svc.store["k"] = []byte("v")
	svc.mu.Unlock()

	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if _, err := tr.Get(context.Background(), "k"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Get" {
		t.Errorf("called = %q, want Get", called)
	}
}

func TestSDKGet_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newTrackingKVService()
	// Seed the store directly so no Put call contaminates `called`.
	svc.mu.Lock()
	svc.store["k"] = []byte("v")
	svc.mu.Unlock()

	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if _, err := tr.Get(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyStrong),
	); err != nil {
		t.Fatalf("Get: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "GetWithConsistency" {
		t.Errorf("called = %q, want GetWithConsistency", called)
	}
}

func TestSDKGet_EventualSetsAllowStaleOnServer(t *testing.T) {
	svc := newTrackingKVService()
	svc.mu.Lock()
	svc.store["k"] = []byte("v")
	svc.mu.Unlock()

	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	// Eventual should reach GetWithConsistency on the server side.
	if _, err := tr.Get(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyEventual),
	); err != nil {
		t.Fatalf("Get: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "GetWithConsistency" {
		t.Errorf("called = %q, want GetWithConsistency", called)
	}
}

// ---------------------------------------------------------------------------
// KV List: consistency arg routing
// ---------------------------------------------------------------------------

func TestSDKList_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if _, err := tr.List(context.Background(), ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "List" {
		t.Errorf("called = %q, want List", called)
	}
}

func TestSDKList_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if _, err := tr.List(context.Background(), "",
		transport.WithConsistency(transport.ConsistencyLinearizable),
	); err != nil {
		t.Fatalf("List: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "ListWithConsistency" {
		t.Errorf("called = %q, want ListWithConsistency", called)
	}
}

// ---------------------------------------------------------------------------
// KV Scan: consistency arg routing
// ---------------------------------------------------------------------------

func TestSDKScan_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if _, err := tr.Scan(context.Background(), ""); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Scan" {
		t.Errorf("called = %q, want Scan", called)
	}
}

func TestSDKScan_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if _, err := tr.Scan(context.Background(), "",
		transport.WithConsistency(transport.ConsistencyEventual),
	); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "ScanWithConsistency" {
		t.Errorf("called = %q, want ScanWithConsistency", called)
	}
}

// ---------------------------------------------------------------------------
// KV Put: write_mode arg routing
// ---------------------------------------------------------------------------

func TestSDKPut_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if err := tr.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Put" {
		t.Errorf("called = %q, want Put", called)
	}
}

func TestSDKPut_WithWriteModeUsesWithModeMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if err := tr.Put(context.Background(), "k", []byte("v"),
		transport.WithWriteMode(transport.WriteModeWALDurable),
	); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "PutWithMode" {
		t.Errorf("called = %q, want PutWithMode", called)
	}
}

func TestSDKPut_AllWriteModes(t *testing.T) {
	cases := []struct {
		mode   transport.WriteMode
		wantFn string
	}{
		{transport.WriteModeSync, "PutWithMode"},
		{transport.WriteModeWALDurable, "PutWithMode"},
		{transport.WriteModeBatched, "PutWithMode"},
	}

	for _, c := range cases {
		svc := newTrackingKVService()
		tr, cleanup := connectKVTracking(t, svc)

		if err := tr.Put(context.Background(), "k", []byte("v"),
			transport.WithWriteMode(c.mode),
		); err != nil {
			t.Fatalf("Put/%v: %v", c.mode, err)
		}
		svc.mu.Lock()
		called := svc.called
		svc.mu.Unlock()
		if called != c.wantFn {
			t.Errorf("mode=%v: called = %q, want %q", c.mode, called, c.wantFn)
		}
		cleanup()
	}
}

// ---------------------------------------------------------------------------
// KV Delete: write_mode arg routing
// ---------------------------------------------------------------------------

func TestSDKDelete_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if err := tr.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Delete" {
		t.Errorf("called = %q, want Delete", called)
	}
}

func TestSDKDelete_WithWriteModeUsesWithModeMethod(t *testing.T) {
	svc := newTrackingKVService()
	tr, cleanup := connectKVTracking(t, svc)
	defer cleanup()

	if err := tr.Delete(context.Background(), "k",
		transport.WithWriteMode(transport.WriteModeBatched),
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "DeleteWithMode" {
		t.Errorf("called = %q, want DeleteWithMode", called)
	}
}

// ---------------------------------------------------------------------------
// MVCC PutWithLease: write_mode routing
// ---------------------------------------------------------------------------

func TestSDKPutWithLease_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "PutWithLease" {
		t.Errorf("called = %q, want PutWithLease", called)
	}
}

func TestSDKPutWithLease_WithWriteModeUsesWithModeMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1,
		transport.WithWriteMode(transport.WriteModeSync),
	); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "PutWithLeaseAndMode" {
		t.Errorf("called = %q, want PutWithLeaseAndMode", called)
	}
}

// ---------------------------------------------------------------------------
// MVCC GetRevision: consistency routing
// ---------------------------------------------------------------------------

func TestSDKGetRevision_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.GetRevision(context.Background(), "k"); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "GetRevision" {
		t.Errorf("called = %q, want GetRevision", called)
	}
}

func TestSDKGetRevision_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.GetRevision(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyStrong),
	); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "GetRevisionWithConsistency" {
		t.Errorf("called = %q, want GetRevisionWithConsistency", called)
	}
}

// ---------------------------------------------------------------------------
// MVCC GetAtRevision: consistency routing
// ---------------------------------------------------------------------------

func TestSDKGetAtRevision_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.GetAtRevision(context.Background(), "k", 3); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "GetAtRevision" {
		t.Errorf("called = %q, want GetAtRevision", called)
	}
}

func TestSDKGetAtRevision_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.GetAtRevision(context.Background(), "k", 3,
		transport.WithConsistency(transport.ConsistencyLinearizable),
	); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "GetAtRevisionWithConsistency" {
		t.Errorf("called = %q, want GetAtRevisionWithConsistency", called)
	}
}

// ---------------------------------------------------------------------------
// MVCC Range: consistency routing
// ---------------------------------------------------------------------------

func TestSDKRange_DefaultUsesPlainMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); err != nil {
		t.Fatalf("Range: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "Range" {
		t.Errorf("called = %q, want Range", called)
	}
}

func TestSDKRange_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newTrackingMVCCSvc()
	tr, cleanup := connectMVCCTracking(t, svc)
	defer cleanup()

	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"),
		transport.RangeOptions{},
		transport.WithConsistency(transport.ConsistencyEventual),
	); err != nil {
		t.Fatalf("Range: %v", err)
	}
	svc.mu2.Lock()
	called := svc.called
	svc.mu2.Unlock()
	if called != "RangeWithConsistency" {
		t.Errorf("called = %q, want RangeWithConsistency", called)
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*trackingKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *trackingKVService) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return m.List(a0, a1)
}
