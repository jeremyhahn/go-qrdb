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
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockMVCCSvc embeds mockKVService and adds MVCC-specific methods.
// ---------------------------------------------------------------------------

type mockMVCCSvc struct {
	*mockKVService

	mu sync.Mutex

	putLeaseErr   error
	getRevErr     error
	getAtRevErr   error
	rangeErr      error
	compactErr    error
	currentRevErr error

	lastLeaseID    int64
	lastRevision   int64
	lastRangeStart []byte
	lastRangeEnd   []byte
	lastCompact    int64

	defaultKV  *kv.KeyValue
	rangeKVs   []*kv.KeyValue
	rangeCount int64
	currentRev int64
}

var _ service.MVCCKVServicer = (*mockMVCCSvc)(nil)

func newMockMVCCSvc() *mockMVCCSvc {
	dkv := &kv.KeyValue{
		Key:            []byte("k1"),
		Value:          []byte("v1"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
		LeaseID:        100,
	}
	return &mockMVCCSvc{
		mockKVService: newMockKVService(),
		defaultKV:     dkv,
		rangeKVs:      []*kv.KeyValue{dkv, {Key: []byte("k2"), Value: []byte("v2"), CreateRevision: 1, ModRevision: 3, Version: 1}},
		rangeCount:    2,
		currentRev:    42,
	}
}

func (m *mockMVCCSvc) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastLeaseID = leaseID
	if m.putLeaseErr != nil {
		return nil, m.putLeaseErr
	}
	return m.defaultKV, nil
}

func (m *mockMVCCSvc) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return m.PutWithLease(ctx, key, value, leaseID)
}

func (m *mockMVCCSvc) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevErr != nil {
		return nil, m.getRevErr
	}
	return m.defaultKV, nil
}

func (m *mockMVCCSvc) GetRevisionWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return m.GetRevision(ctx, key)
}

func (m *mockMVCCSvc) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRevision = revision
	if m.getAtRevErr != nil {
		return nil, m.getAtRevErr
	}
	return m.defaultKV, nil
}

func (m *mockMVCCSvc) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return m.GetAtRevision(ctx, key, revision)
}

func (m *mockMVCCSvc) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRangeStart = append([]byte(nil), start...)
	m.lastRangeEnd = append([]byte(nil), end...)
	if m.rangeErr != nil {
		return nil, 0, m.rangeErr
	}
	return m.rangeKVs, m.rangeCount, nil
}

func (m *mockMVCCSvc) RangeWithConsistency(ctx context.Context, start, end []byte, _ consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	return m.Range(ctx, start, end, rangeOpts...)
}

func (m *mockMVCCSvc) Compact(_ context.Context, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCompact = revision
	return m.compactErr
}

func (m *mockMVCCSvc) CurrentRevision(_ context.Context) (int64, error) {
	if m.currentRevErr != nil {
		return 0, m.currentRevErr
	}
	return m.currentRev, nil
}

func connectMVCCInProcess(t *testing.T, svc service.MVCCKVServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
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
// PutWithLease
// ---------------------------------------------------------------------------

func TestPutWithLease_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	kvOut, err := tr.PutWithLease(context.Background(), "k1", []byte("v1"), 100)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if string(kvOut.Key) != "k1" {
		t.Errorf("key: %q", string(kvOut.Key))
	}
	if kvOut.LeaseID != 100 {
		t.Errorf("lease: %d", kvOut.LeaseID)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastLeaseID != 100 {
		t.Errorf("recorded lease: %d", svc.lastLeaseID)
	}
}

func TestPutWithLease_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.putLeaseErr = errors.New("lease expired")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetRevision
// ---------------------------------------------------------------------------

func TestGetRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	kvOut, err := tr.GetRevision(context.Background(), "k1")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if kvOut.ModRevision != 2 {
		t.Errorf("mod revision: %d", kvOut.ModRevision)
	}
}

func TestGetRevision_NotFound(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getRevErr = errors.New("key not found")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetRevision(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Errorf("expected *transport.KeyNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// GetAtRevision
// ---------------------------------------------------------------------------

func TestGetAtRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	kvOut, err := tr.GetAtRevision(context.Background(), "k1", 5)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if kvOut == nil {
		t.Fatal("expected non-nil kv")
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRevision != 5 {
		t.Errorf("revision: %d", svc.lastRevision)
	}
}

func TestGetAtRevision_NotFound(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getAtRevErr = errors.New("key not found")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetAtRevision(context.Background(), "k", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Errorf("expected *transport.KeyNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Range
// ---------------------------------------------------------------------------

func TestRange_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	resp, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count: %d", resp.Count)
	}
	if len(resp.Kvs) != 2 {
		t.Errorf("kvs: %d", len(resp.Kvs))
	}
}

func TestRange_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.rangeErr = errors.New("scan failed")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Compact
// ---------------------------------------------------------------------------

func TestCompact_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if err := tr.Compact(context.Background(), 42); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastCompact != 42 {
		t.Errorf("compact revision: %d", svc.lastCompact)
	}
}

func TestCompact_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.compactErr = errors.New("compact failed")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if err := tr.Compact(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CurrentRevision
// ---------------------------------------------------------------------------

func TestCurrentRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	rev, err := tr.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 42 {
		t.Errorf("revision: got %d, want 42", rev)
	}
}

func TestCurrentRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.currentRevErr = errors.New("backend down")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CurrentRevision(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// serverKeyValueToTransport
// ---------------------------------------------------------------------------

func TestServerKeyValueToTransport(t *testing.T) {
	in := &serverKeyValue{
		Key:            []byte("k"),
		Value:          []byte("v"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        3,
		LeaseID:        4,
	}
	out := serverKeyValueToTransport(in)
	if string(out.Key) != "k" || string(out.Value) != "v" {
		t.Errorf("key/value: %s/%s", string(out.Key), string(out.Value))
	}
	if out.CreateRevision != 1 || out.ModRevision != 2 || out.Version != 3 || out.LeaseID != 4 {
		t.Errorf("metadata: %+v", out)
	}
}
