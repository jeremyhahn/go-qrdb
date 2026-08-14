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

package rest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Extended mocks with capture hooks for consistency/write-mode params.
// ---------------------------------------------------------------------------

// captureKVService wraps mockKVService and records which *WithConsistency
// / *WithMode methods were called, together with the argument values.
type captureKVService struct {
	mockKVService

	// read captures
	getWithConsistencyFn  func(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error)
	listWithConsistencyFn func(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error)
	scanWithConsistencyFn func(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error)
	scanFn                func(ctx context.Context, prefix string) (map[string][]byte, error)
	getStaleFn            func(ctx context.Context, key string) ([]byte, error)
	listStaleFn           func(ctx context.Context, prefix string) ([]string, error)
	scanStaleFn           func(ctx context.Context, prefix string) (map[string][]byte, error)

	// write captures
	putWithModeFn    func(ctx context.Context, key string, value []byte, mode writemode.Mode) error
	deleteWithModeFn func(ctx context.Context, key string, mode writemode.Mode) error
}

func (m *captureKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	return m.mockKVService.Scan(ctx, prefix)
}

func (m *captureKVService) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	if m.getWithConsistencyFn != nil {
		return m.getWithConsistencyFn(ctx, key, opts)
	}
	return []byte("cons-val"), nil
}

func (m *captureKVService) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	if m.listWithConsistencyFn != nil {
		return m.listWithConsistencyFn(ctx, prefix, opts)
	}
	return []string{"cons-key"}, nil
}

func (m *captureKVService) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	if m.scanWithConsistencyFn != nil {
		return m.scanWithConsistencyFn(ctx, prefix, opts)
	}
	return map[string][]byte{"cons-k": []byte("v")}, nil
}

func (m *captureKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	if m.getStaleFn != nil {
		return m.getStaleFn(ctx, key)
	}
	return m.mockKVService.GetStale(ctx, key)
}

func (m *captureKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	if m.listStaleFn != nil {
		return m.listStaleFn(ctx, prefix)
	}
	return m.mockKVService.ListStale(ctx, prefix)
}

func (m *captureKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanStaleFn != nil {
		return m.scanStaleFn(ctx, prefix)
	}
	return m.mockKVService.ScanStale(ctx, prefix)
}

func (m *captureKVService) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if m.putWithModeFn != nil {
		return m.putWithModeFn(ctx, key, value, mode)
	}
	return nil
}

func (m *captureKVService) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if m.deleteWithModeFn != nil {
		return m.deleteWithModeFn(ctx, key, mode)
	}
	return nil
}

// captureMVCCService wraps mockMVCCService and records WithConsistency /
// WithMode calls.
type captureMVCCService struct {
	mockMVCCService

	getRevisionWithConsistencyFn   func(ctx context.Context, key string, opts consistency.ReadOptions) (*kv.KeyValue, error)
	getAtRevisionWithConsistencyFn func(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*kv.KeyValue, error)
	rangeWithConsistencyFn         func(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error)
	putWithLeaseAndModeFn          func(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error)
}

func (m *captureMVCCService) GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*kv.KeyValue, error) {
	if m.getRevisionWithConsistencyFn != nil {
		return m.getRevisionWithConsistencyFn(ctx, key, opts)
	}
	return &kv.KeyValue{Key: []byte(key), Value: []byte("rev-cons")}, nil
}

func (m *captureMVCCService) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*kv.KeyValue, error) {
	if m.getAtRevisionWithConsistencyFn != nil {
		return m.getAtRevisionWithConsistencyFn(ctx, key, revision, opts)
	}
	return &kv.KeyValue{Key: []byte(key), Value: []byte("atrev-cons")}, nil
}

func (m *captureMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeWithConsistencyFn != nil {
		return m.rangeWithConsistencyFn(ctx, start, end, opts, rangeOpts...)
	}
	return []*kv.KeyValue{{Key: start, Value: []byte("range-cons")}}, 1, 1, nil
}

func (m *captureMVCCService) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error) {
	if m.putWithLeaseAndModeFn != nil {
		return m.putWithLeaseAndModeFn(ctx, key, value, leaseID, mode)
	}
	return &kv.KeyValue{Key: []byte(key), Value: value}, nil
}

// ---------------------------------------------------------------------------
// Helper: build a server backed by captureKVService/captureMVCCService.
// ---------------------------------------------------------------------------

func newCaptureSrv(kvSvc *captureKVService, mvccSvc *captureMVCCService) *Server {
	srv := NewServer(kvSvc, &mockAdminService{}, "127.0.0.1:0")
	if mvccSvc != nil {
		WithMVCC(mvccSvc)(srv)
	}
	return srv
}

// ---------------------------------------------------------------------------
// KV Get – consistency routing
// ---------------------------------------------------------------------------

func TestHandleKVGet_ConsistencyStrong_CallsGetWithConsistency(t *testing.T) {
	var capturedLevel consistency.Level
	kvSvc := &captureKVService{}
	kvSvc.getWithConsistencyFn = func(_ context.Context, _ string, opts consistency.ReadOptions) ([]byte, error) {
		capturedLevel = opts.Consistency
		return []byte("strong-val"), nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

func TestHandleKVGet_ConsistencyLinearizable_CallsGetWithConsistency(t *testing.T) {
	var capturedLevel consistency.Level
	kvSvc := &captureKVService{}
	kvSvc.getWithConsistencyFn = func(_ context.Context, _ string, opts consistency.ReadOptions) ([]byte, error) {
		capturedLevel = opts.Consistency
		return []byte("lin-val"), nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?consistency=linearizable", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Linearizable {
		t.Fatalf("expected Linearizable, got %v", capturedLevel)
	}
}

func TestHandleKVGet_ConsistencyEventual_CallsGetStale(t *testing.T) {
	staleCalled := false
	kvSvc := &captureKVService{}
	kvSvc.getStaleFn = func(_ context.Context, _ string) ([]byte, error) {
		staleCalled = true
		return []byte("stale-val"), nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?consistency=eventual", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if !staleCalled {
		t.Fatal("expected GetStale to be called for eventual consistency")
	}
}

func TestHandleKVGet_StaleTrue_CallsGetStale(t *testing.T) {
	staleCalled := false
	kvSvc := &captureKVService{}
	kvSvc.getStaleFn = func(_ context.Context, _ string) ([]byte, error) {
		staleCalled = true
		return []byte("stale-legacy"), nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?stale=true", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if !staleCalled {
		t.Fatal("expected GetStale to be called for ?stale=true")
	}
}

func TestHandleKVGet_NoParams_CallsPlainGet(t *testing.T) {
	plainCalled := false
	kvSvc := &captureKVService{}
	kvSvc.getFn = func(_ context.Context, _ string) ([]byte, error) {
		plainCalled = true
		return []byte("plain"), nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain Get to be called when no consistency param")
	}
}

func TestHandleKVGet_InvalidConsistency_Returns400(t *testing.T) {
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?consistency=bogus", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleKVGet_ConsistencyWinsOverStale(t *testing.T) {
	var capturedLevel consistency.Level
	kvSvc := &captureKVService{}
	kvSvc.getWithConsistencyFn = func(_ context.Context, _ string, opts consistency.ReadOptions) ([]byte, error) {
		capturedLevel = opts.Consistency
		return []byte("strong-wins"), nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	// Both set: ?consistency= should win.
	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?consistency=strong&stale=true", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

// ---------------------------------------------------------------------------
// KV Get – MVCC revision + consistency
// ---------------------------------------------------------------------------

func TestHandleKVGet_RevisionTrue_ConsistencyStrong_CallsGetRevisionWithConsistency(t *testing.T) {
	var capturedLevel consistency.Level
	mvccSvc := &captureMVCCService{}
	mvccSvc.getRevisionWithConsistencyFn = func(_ context.Context, _ string, opts consistency.ReadOptions) (*kv.KeyValue, error) {
		capturedLevel = opts.Consistency
		return &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?revision=true&consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

func TestHandleKVGet_RevisionTrue_NoConsistency_CallsPlainGetRevision(t *testing.T) {
	plainCalled := false
	mvccSvc := &captureMVCCService{}
	mvccSvc.getRevisionFn = func(_ context.Context, _ string) (*kv.KeyValue, error) {
		plainCalled = true
		return &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?revision=true", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain GetRevision when no consistency param")
	}
}

func TestHandleKVGet_AtRevision_ConsistencyStrong_CallsGetAtRevisionWithConsistency(t *testing.T) {
	var capturedRev int64
	var capturedLevel consistency.Level
	mvccSvc := &captureMVCCService{}
	mvccSvc.getAtRevisionWithConsistencyFn = func(_ context.Context, _ string, rev int64, opts consistency.ReadOptions) (*kv.KeyValue, error) {
		capturedRev = rev
		capturedLevel = opts.Consistency
		return &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?at-revision=42&consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if capturedRev != 42 {
		t.Fatalf("expected rev=42, got %d", capturedRev)
	}
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

func TestHandleKVGet_AtRevision_NoConsistency_CallsPlainGetAtRevision(t *testing.T) {
	plainCalled := false
	mvccSvc := &captureMVCCService{}
	mvccSvc.getAtRevisionFn = func(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
		plainCalled = true
		return &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?at-revision=5", nil)
	rec := httptest.NewRecorder()
	srv.handleKVGet(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain GetAtRevision when no consistency param")
	}
}

// ---------------------------------------------------------------------------
// KV Put – write mode routing
// ---------------------------------------------------------------------------

func TestHandleKVPut_WriteModeBatched_CallsPutWithMode(t *testing.T) {
	var capturedMode writemode.Mode
	kvSvc := &captureKVService{}
	kvSvc.putWithModeFn = func(_ context.Context, _ string, _ []byte, mode writemode.Mode) error {
		capturedMode = mode
		return nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?write_mode=batched", bytes.NewReader([]byte("val")))
	rec := httptest.NewRecorder()
	srv.handleKVPut(rec, req, "mykey")

	assertStatus(t, rec, http.StatusNoContent)
	if capturedMode != writemode.Batched {
		t.Fatalf("expected Batched, got %v", capturedMode)
	}
}

func TestHandleKVPut_WriteModeSync_CallsPutWithMode(t *testing.T) {
	var capturedMode writemode.Mode
	kvSvc := &captureKVService{}
	kvSvc.putWithModeFn = func(_ context.Context, _ string, _ []byte, mode writemode.Mode) error {
		capturedMode = mode
		return nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?write_mode=sync", bytes.NewReader([]byte("val")))
	rec := httptest.NewRecorder()
	srv.handleKVPut(rec, req, "mykey")

	assertStatus(t, rec, http.StatusNoContent)
	if capturedMode != writemode.Sync {
		t.Fatalf("expected Sync, got %v", capturedMode)
	}
}

func TestHandleKVPut_NoWriteMode_CallsPlainPut(t *testing.T) {
	plainCalled := false
	kvSvc := &captureKVService{}
	kvSvc.putFn = func(_ context.Context, _ string, _ []byte) error {
		plainCalled = true
		return nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey", bytes.NewReader([]byte("val")))
	rec := httptest.NewRecorder()
	srv.handleKVPut(rec, req, "mykey")

	assertStatus(t, rec, http.StatusNoContent)
	if !plainCalled {
		t.Fatal("expected plain Put when no write_mode param")
	}
}

func TestHandleKVPut_InvalidWriteMode_Returns400(t *testing.T) {
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?write_mode=bogus", bytes.NewReader([]byte("val")))
	rec := httptest.NewRecorder()
	srv.handleKVPut(rec, req, "mykey")

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// KV Put with lease – write mode via PutWithLeaseAndMode
// ---------------------------------------------------------------------------

func TestHandleKVPut_LeaseAndWriteMode_CallsPutWithLeaseAndMode(t *testing.T) {
	var capturedMode writemode.Mode
	mvccSvc := &captureMVCCService{}
	mvccSvc.putWithLeaseAndModeFn = func(_ context.Context, _ string, _ []byte, _ int64, mode writemode.Mode) (*kv.KeyValue, error) {
		capturedMode = mode
		return &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=1&write_mode=wal_durable", bytes.NewReader([]byte("val")))
	rec := httptest.NewRecorder()
	srv.handleKVPut(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if capturedMode != writemode.WALDurable {
		t.Fatalf("expected WALDurable, got %v", capturedMode)
	}
}

func TestHandleKVPut_LeaseNoWriteMode_CallsPlainPutWithLease(t *testing.T) {
	plainCalled := false
	mvccSvc := &captureMVCCService{}
	mvccSvc.putWithLeaseFn = func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
		plainCalled = true
		return &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=1", bytes.NewReader([]byte("val")))
	rec := httptest.NewRecorder()
	srv.handleKVPut(rec, req, "mykey")

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain PutWithLease when no write_mode param")
	}
}

// ---------------------------------------------------------------------------
// KV Delete – write mode routing
// ---------------------------------------------------------------------------

func TestHandleKVDelete_WriteModeBatched_CallsDeleteWithMode(t *testing.T) {
	var capturedMode writemode.Mode
	kvSvc := &captureKVService{}
	kvSvc.deleteWithModeFn = func(_ context.Context, _ string, mode writemode.Mode) error {
		capturedMode = mode
		return nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/kv/mykey?write_mode=batched", nil)
	rec := httptest.NewRecorder()
	srv.handleKVDelete(rec, req, "mykey")

	assertStatus(t, rec, http.StatusNoContent)
	if capturedMode != writemode.Batched {
		t.Fatalf("expected Batched, got %v", capturedMode)
	}
}

func TestHandleKVDelete_NoWriteMode_CallsPlainDelete(t *testing.T) {
	plainCalled := false
	kvSvc := &captureKVService{}
	kvSvc.deleteFn = func(_ context.Context, _ string) error {
		plainCalled = true
		return nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/kv/mykey", nil)
	rec := httptest.NewRecorder()
	srv.handleKVDelete(rec, req, "mykey")

	assertStatus(t, rec, http.StatusNoContent)
	if !plainCalled {
		t.Fatal("expected plain Delete when no write_mode param")
	}
}

func TestHandleKVDelete_InvalidWriteMode_Returns400(t *testing.T) {
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodDelete, "/kv/mykey?write_mode=invalid", nil)
	rec := httptest.NewRecorder()
	srv.handleKVDelete(rec, req, "mykey")

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// KV List – consistency routing
// ---------------------------------------------------------------------------

func TestHandleKVList_ConsistencyStrong_CallsListWithConsistency(t *testing.T) {
	var capturedLevel consistency.Level
	kvSvc := &captureKVService{}
	kvSvc.listWithConsistencyFn = func(_ context.Context, _ string, opts consistency.ReadOptions) ([]string, error) {
		capturedLevel = opts.Consistency
		return []string{"k1"}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv?consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.handleKVList(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

func TestHandleKVList_ConsistencyEventual_CallsListStale(t *testing.T) {
	staleCalled := false
	kvSvc := &captureKVService{}
	kvSvc.listStaleFn = func(_ context.Context, _ string) ([]string, error) {
		staleCalled = true
		return []string{"stale-k"}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv?consistency=eventual", nil)
	rec := httptest.NewRecorder()
	srv.handleKVList(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !staleCalled {
		t.Fatal("expected ListStale for eventual consistency")
	}
}

func TestHandleKVList_StaleTrue_CallsListStale(t *testing.T) {
	staleCalled := false
	kvSvc := &captureKVService{}
	kvSvc.listStaleFn = func(_ context.Context, _ string) ([]string, error) {
		staleCalled = true
		return []string{"stale-k"}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv?stale=true", nil)
	rec := httptest.NewRecorder()
	srv.handleKVList(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !staleCalled {
		t.Fatal("expected ListStale for ?stale=true")
	}
}

func TestHandleKVList_NoParams_CallsPlainList(t *testing.T) {
	plainCalled := false
	kvSvc := &captureKVService{}
	kvSvc.listFn = func(_ context.Context, _ string) ([]string, error) {
		plainCalled = true
		return []string{"k"}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv", nil)
	rec := httptest.NewRecorder()
	srv.handleKVList(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain List when no params")
	}
}

func TestHandleKVList_InvalidConsistency_Returns400(t *testing.T) {
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv?consistency=nope", nil)
	rec := httptest.NewRecorder()
	srv.handleKVList(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// KV Scan – consistency routing
// ---------------------------------------------------------------------------

func TestHandleKVScan_ConsistencyStrong_CallsScanWithConsistency(t *testing.T) {
	var capturedLevel consistency.Level
	kvSvc := &captureKVService{}
	kvSvc.scanWithConsistencyFn = func(_ context.Context, _ string, opts consistency.ReadOptions) (map[string][]byte, error) {
		capturedLevel = opts.Consistency
		return map[string][]byte{"k": []byte("v")}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/scan?consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.handleKVScan(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

func TestHandleKVScan_ConsistencyEventual_CallsScanStale(t *testing.T) {
	staleCalled := false
	kvSvc := &captureKVService{}
	kvSvc.scanStaleFn = func(_ context.Context, _ string) (map[string][]byte, error) {
		staleCalled = true
		return map[string][]byte{"k": []byte("v")}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/scan?consistency=eventual", nil)
	rec := httptest.NewRecorder()
	srv.handleKVScan(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !staleCalled {
		t.Fatal("expected ScanStale for eventual consistency")
	}
}

func TestHandleKVScan_StaleTrue_CallsScanStale(t *testing.T) {
	staleCalled := false
	kvSvc := &captureKVService{}
	kvSvc.scanStaleFn = func(_ context.Context, _ string) (map[string][]byte, error) {
		staleCalled = true
		return map[string][]byte{"k": []byte("v")}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/scan?stale=true", nil)
	rec := httptest.NewRecorder()
	srv.handleKVScan(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !staleCalled {
		t.Fatal("expected ScanStale for ?stale=true")
	}
}

func TestHandleKVScan_NoParams_CallsPlainScan(t *testing.T) {
	plainCalled := false
	kvSvc := &captureKVService{}
	kvSvc.scanFn = func(_ context.Context, _ string) (map[string][]byte, error) {
		plainCalled = true
		return map[string][]byte{"k": []byte("v")}, nil
	}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/scan", nil)
	rec := httptest.NewRecorder()
	srv.handleKVScan(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain Scan when no params")
	}
}

func TestHandleKVScan_InvalidConsistency_Returns400(t *testing.T) {
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, nil)

	req := httptest.NewRequest(http.MethodGet, "/kv/scan?consistency=bad", nil)
	rec := httptest.NewRecorder()
	srv.handleKVScan(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// MVCC Range – consistency routing
// ---------------------------------------------------------------------------

func TestHandleRange_ConsistencyStrong_CallsRangeWithConsistency(t *testing.T) {
	var capturedLevel consistency.Level
	mvccSvc := &captureMVCCService{}
	mvccSvc.rangeWithConsistencyFn = func(_ context.Context, start, _ []byte, opts consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
		capturedLevel = opts.Consistency
		return []*kv.KeyValue{{Key: start, Value: []byte("v")}}, 1, 1, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a&end=z&consistency=strong", nil)
	rec := httptest.NewRecorder()
	srv.handleRange(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if capturedLevel != consistency.Strong {
		t.Fatalf("expected Strong, got %v", capturedLevel)
	}
}

func TestHandleRange_NoConsistency_CallsPlainRange(t *testing.T) {
	plainCalled := false
	mvccSvc := &captureMVCCService{}
	mvccSvc.rangeFn = func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
		plainCalled = true
		return nil, 0, 0, nil
	}
	kvSvc := &captureKVService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a", nil)
	rec := httptest.NewRecorder()
	srv.handleRange(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !plainCalled {
		t.Fatal("expected plain Range when no consistency param")
	}
}

func TestHandleRange_InvalidConsistency_Returns400(t *testing.T) {
	kvSvc := &captureKVService{}
	mvccSvc := &captureMVCCService{}
	srv := newCaptureSrv(kvSvc, mvccSvc)

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a&consistency=junk", nil)
	rec := httptest.NewRecorder()
	srv.handleRange(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// RestoreToRevision satisfies service.MVCCKVServicer.
func (c *captureMVCCService) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}
