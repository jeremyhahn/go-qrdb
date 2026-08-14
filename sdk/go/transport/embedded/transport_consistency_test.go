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

package embedded

import (
	"context"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// captureKV wraps mockKVService and records which variant was called.
// ---------------------------------------------------------------------------

type captureKVSvc struct {
	*mockKVService
	mu     sync.Mutex
	called string
	mode   writemode.Mode
	ropts  consistency.ReadOptions
}

func newCaptureKVSvc() *captureKVSvc {
	return &captureKVSvc{mockKVService: newMockKVService()}
}

func (m *captureKVSvc) Put(ctx context.Context, key string, value []byte) error {
	m.mu.Lock()
	m.called = "Put"
	m.mu.Unlock()
	return m.mockKVService.Put(ctx, key, value)
}

func (m *captureKVSvc) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	m.mu.Lock()
	m.called = "PutWithMode"
	m.mode = mode
	m.mu.Unlock()
	return m.mockKVService.Put(ctx, key, value)
}

func (m *captureKVSvc) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	m.called = "Get"
	m.mu.Unlock()
	return m.mockKVService.Get(ctx, key)
}

func (m *captureKVSvc) GetWithConsistency(ctx context.Context, key string, ropts consistency.ReadOptions) ([]byte, error) {
	m.mu.Lock()
	m.called = "GetWithConsistency"
	m.ropts = ropts
	m.mu.Unlock()
	return m.mockKVService.Get(ctx, key)
}

func (m *captureKVSvc) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	m.called = "Delete"
	m.mu.Unlock()
	return m.mockKVService.Delete(ctx, key)
}

func (m *captureKVSvc) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	m.mu.Lock()
	m.called = "DeleteWithMode"
	m.mode = mode
	m.mu.Unlock()
	return m.mockKVService.Delete(ctx, key)
}

func (m *captureKVSvc) List(ctx context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	m.called = "List"
	m.mu.Unlock()
	return m.mockKVService.List(ctx, prefix)
}

func (m *captureKVSvc) ListWithConsistency(ctx context.Context, prefix string, ropts consistency.ReadOptions) ([]string, error) {
	m.mu.Lock()
	m.called = "ListWithConsistency"
	m.ropts = ropts
	m.mu.Unlock()
	return m.mockKVService.List(ctx, prefix)
}

func (m *captureKVSvc) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	m.mu.Lock()
	m.called = "Scan"
	m.mu.Unlock()
	return m.mockKVService.Scan(ctx, prefix)
}

func (m *captureKVSvc) ScanWithConsistency(ctx context.Context, prefix string, ropts consistency.ReadOptions) (map[string][]byte, error) {
	m.mu.Lock()
	m.called = "ScanWithConsistency"
	m.ropts = ropts
	m.mu.Unlock()
	return m.mockKVService.Scan(ctx, prefix)
}

var _ service.KVServicer = (*captureKVSvc)(nil)

// ---------------------------------------------------------------------------
// captureMVCCSvcCapture wraps mockMVCCService and records which variant called.
// ---------------------------------------------------------------------------

type captureMVCCSvcCapture struct {
	*mockMVCCService
	mu     sync.Mutex
	called string
	mode   writemode.Mode
	ropts  consistency.ReadOptions
}

func newCaptureMVCCSvcCapture() *captureMVCCSvcCapture {
	return &captureMVCCSvcCapture{mockMVCCService: newMockMVCCService()}
}

func (m *captureMVCCSvcCapture) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	m.mu.Lock()
	m.called = "PutWithLease"
	m.mu.Unlock()
	return m.mockMVCCService.PutWithLease(ctx, key, value, leaseID)
}

func (m *captureMVCCSvcCapture) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error) {
	m.mu.Lock()
	m.called = "PutWithLeaseAndMode"
	m.mode = mode
	m.mu.Unlock()
	return m.mockMVCCService.PutWithLease(ctx, key, value, leaseID)
}

func (m *captureMVCCSvcCapture) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	m.mu.Lock()
	m.called = "GetRevision"
	m.mu.Unlock()
	return m.mockMVCCService.GetRevision(ctx, key)
}

func (m *captureMVCCSvcCapture) GetRevisionWithConsistency(ctx context.Context, key string, ropts consistency.ReadOptions) (*kv.KeyValue, error) {
	m.mu.Lock()
	m.called = "GetRevisionWithConsistency"
	m.ropts = ropts
	m.mu.Unlock()
	return m.mockMVCCService.GetRevision(ctx, key)
}

func (m *captureMVCCSvcCapture) GetAtRevision(ctx context.Context, key string, revision int64) (*kv.KeyValue, error) {
	m.mu.Lock()
	m.called = "GetAtRevision"
	m.mu.Unlock()
	return m.mockMVCCService.GetAtRevision(ctx, key, revision)
}

func (m *captureMVCCSvcCapture) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, ropts consistency.ReadOptions) (*kv.KeyValue, error) {
	m.mu.Lock()
	m.called = "GetAtRevisionWithConsistency"
	m.ropts = ropts
	m.mu.Unlock()
	return m.mockMVCCService.GetAtRevision(ctx, key, revision)
}

func (m *captureMVCCSvcCapture) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	m.mu.Lock()
	m.called = "Range"
	m.mu.Unlock()
	return m.mockMVCCService.Range(ctx, start, end, opts...)
}

func (m *captureMVCCSvcCapture) RangeWithConsistency(ctx context.Context, start, end []byte, ropts consistency.ReadOptions, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	m.mu.Lock()
	m.called = "RangeWithConsistency"
	m.ropts = ropts
	m.mu.Unlock()
	return m.mockMVCCService.Range(ctx, start, end, opts...)
}

var _ service.MVCCKVServicer = (*captureMVCCSvcCapture)(nil)

// ---------------------------------------------------------------------------
// KV Put: write_mode routing
// ---------------------------------------------------------------------------

func TestEmbedded_Put_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestEmbedded_Put_WithWriteModeUsesWithModeMethod(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Put(context.Background(), "k", []byte("v"),
		transport.WithWriteMode(transport.WriteModeWALDurable),
	); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	mode := svc.mode
	svc.mu.Unlock()
	if called != "PutWithMode" {
		t.Errorf("called = %q, want PutWithMode", called)
	}
	if mode != writemode.WALDurable {
		t.Errorf("mode = %v, want WALDurable", mode)
	}
}

func TestEmbedded_Put_BatchedMode(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Put(context.Background(), "k", []byte("v"),
		transport.WithWriteMode(transport.WriteModeBatched),
	); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc.mu.Lock()
	mode := svc.mode
	svc.mu.Unlock()
	if mode != writemode.Batched {
		t.Errorf("mode = %v, want Batched", mode)
	}
}

// ---------------------------------------------------------------------------
// KV Delete: write_mode routing
// ---------------------------------------------------------------------------

func TestEmbedded_Delete_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestEmbedded_Delete_WithWriteModeUsesWithModeMethod(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Delete(context.Background(), "k",
		transport.WithWriteMode(transport.WriteModeBatched),
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	mode := svc.mode
	svc.mu.Unlock()
	if called != "DeleteWithMode" {
		t.Errorf("called = %q, want DeleteWithMode", called)
	}
	if mode != writemode.Batched {
		t.Errorf("mode = %v, want Batched", mode)
	}
}

// ---------------------------------------------------------------------------
// KV Get: consistency routing
// ---------------------------------------------------------------------------

func TestEmbedded_Get_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureKVSvc()
	_ = svc.mockKVService.Put(context.Background(), "k", []byte("v"))
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestEmbedded_Get_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newCaptureKVSvc()
	// Seed via the underlying store so Put doesn't set called.
	svc.mockKVService.mu.Lock()
	svc.mockKVService.store["k"] = []byte("v")
	svc.mockKVService.mu.Unlock()

	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.Get(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyStrong),
	); err != nil {
		t.Fatalf("Get: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.ropts
	svc.mu.Unlock()
	if called != "GetWithConsistency" {
		t.Errorf("called = %q, want GetWithConsistency", called)
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", ropts.Consistency)
	}
}

func TestEmbedded_Get_EventualSetsAllowStale(t *testing.T) {
	svc := newCaptureKVSvc()
	// Seed via the underlying store so no prior method sets called.
	svc.mockKVService.mu.Lock()
	svc.mockKVService.store["k"] = []byte("v")
	svc.mockKVService.mu.Unlock()

	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.Get(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyEventual),
	); err != nil {
		t.Fatalf("Get: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.ropts
	svc.mu.Unlock()
	if called != "GetWithConsistency" {
		t.Errorf("called = %q, want GetWithConsistency", called)
	}
	if !ropts.AllowStale {
		t.Error("AllowStale should be true for Eventual")
	}
}

// ---------------------------------------------------------------------------
// KV List: consistency routing
// ---------------------------------------------------------------------------

func TestEmbedded_List_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestEmbedded_List_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.List(context.Background(), "",
		transport.WithConsistency(transport.ConsistencyLinearizable),
	); err != nil {
		t.Fatalf("List: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.ropts
	svc.mu.Unlock()
	if called != "ListWithConsistency" {
		t.Errorf("called = %q, want ListWithConsistency", called)
	}
	if ropts.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", ropts.Consistency)
	}
}

// ---------------------------------------------------------------------------
// KV Scan: consistency routing
// ---------------------------------------------------------------------------

func TestEmbedded_Scan_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

func TestEmbedded_Scan_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newCaptureKVSvc()
	tr, err := New(svc, &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
// MVCC PutWithLease: write_mode routing
// ---------------------------------------------------------------------------

func TestEmbedded_PutWithLease_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "PutWithLease" {
		t.Errorf("called = %q, want PutWithLease", called)
	}
}

func TestEmbedded_PutWithLease_WithWriteModeUsesAndMode(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1,
		transport.WithWriteMode(transport.WriteModeSync),
	); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	mode := svc.mode
	svc.mu.Unlock()
	if called != "PutWithLeaseAndMode" {
		t.Errorf("called = %q, want PutWithLeaseAndMode", called)
	}
	if mode != writemode.Sync {
		t.Errorf("mode = %v, want Sync", mode)
	}
}

// ---------------------------------------------------------------------------
// MVCC GetRevision: consistency routing
// ---------------------------------------------------------------------------

func TestEmbedded_GetRevision_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.GetRevision(context.Background(), "k"); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "GetRevision" {
		t.Errorf("called = %q, want GetRevision", called)
	}
}

func TestEmbedded_GetRevision_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.GetRevision(context.Background(), "k",
		transport.WithConsistency(transport.ConsistencyStrong),
	); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.ropts
	svc.mu.Unlock()
	if called != "GetRevisionWithConsistency" {
		t.Errorf("called = %q, want GetRevisionWithConsistency", called)
	}
	if ropts.Consistency != consistency.Strong {
		t.Errorf("consistency = %v, want Strong", ropts.Consistency)
	}
}

// ---------------------------------------------------------------------------
// MVCC GetAtRevision: consistency routing
// ---------------------------------------------------------------------------

func TestEmbedded_GetAtRevision_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.GetAtRevision(context.Background(), "k", 3); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "GetAtRevision" {
		t.Errorf("called = %q, want GetAtRevision", called)
	}
}

func TestEmbedded_GetAtRevision_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.GetAtRevision(context.Background(), "k", 3,
		transport.WithConsistency(transport.ConsistencyLinearizable),
	); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.ropts
	svc.mu.Unlock()
	if called != "GetAtRevisionWithConsistency" {
		t.Errorf("called = %q, want GetAtRevisionWithConsistency", called)
	}
	if ropts.Consistency != consistency.Linearizable {
		t.Errorf("consistency = %v, want Linearizable", ropts.Consistency)
	}
}

// ---------------------------------------------------------------------------
// MVCC Range: consistency routing
// ---------------------------------------------------------------------------

func TestEmbedded_Range_PlainWhenNoOpts(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); err != nil {
		t.Fatalf("Range: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	svc.mu.Unlock()
	if called != "Range" {
		t.Errorf("called = %q, want Range", called)
	}
}

func TestEmbedded_Range_WithConsistencyUsesConsistencyMethod(t *testing.T) {
	svc := newCaptureMVCCSvcCapture()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithMVCCService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"),
		transport.RangeOptions{},
		transport.WithConsistency(transport.ConsistencyEventual),
	); err != nil {
		t.Fatalf("Range: %v", err)
	}
	svc.mu.Lock()
	called := svc.called
	ropts := svc.ropts
	svc.mu.Unlock()
	if called != "RangeWithConsistency" {
		t.Errorf("called = %q, want RangeWithConsistency", called)
	}
	if ropts.Consistency != consistency.Eventual {
		t.Errorf("consistency = %v, want Eventual", ropts.Consistency)
	}
	if !ropts.AllowStale {
		t.Error("AllowStale should be true for Eventual")
	}
}

// TestResolveReadConsistency_AllowStaleOnly verifies that WithAllowStale()
// without an explicit level maps to an eventual read (parity with the network
// transports), rather than being dropped to the default linearizable path.
func TestResolveReadConsistency_AllowStaleOnly(t *testing.T) {
	ro, set := resolveReadConsistency([]transport.ReadOption{transport.WithAllowStale()})
	if !set {
		t.Fatal("expected specified=true for WithAllowStale()")
	}
	if ro.Consistency != consistency.Eventual {
		t.Errorf("Consistency = %v, want Eventual", ro.Consistency)
	}
	if !ro.AllowStale {
		t.Error("AllowStale = false, want true")
	}
}

// TestResolveReadConsistency_NoOptions verifies that the absence of any read
// option leaves consistency unspecified (server default).
func TestResolveReadConsistency_NoOptions(t *testing.T) {
	if _, set := resolveReadConsistency(nil); set {
		t.Error("expected specified=false when no read options are given")
	}
}
