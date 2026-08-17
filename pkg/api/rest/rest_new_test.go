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
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock: MVCCKVServicer
// ---------------------------------------------------------------------------

type mockMVCCService struct {
	mockKVService
	putWithLeaseFn    func(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error)
	getRevisionFn     func(ctx context.Context, key string) (*kv.KeyValue, error)
	getAtRevisionFn   func(ctx context.Context, key string, revision int64) (*kv.KeyValue, error)
	rangeFn           func(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error)
	rangeRevision     int64
	compactFn         func(ctx context.Context, revision int64) error
	currentRevisionFn func(ctx context.Context) (int64, error)
	restoreFn         func(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error)
}

func (m *mockMVCCService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	if m.putWithLeaseFn != nil {
		return m.putWithLeaseFn(ctx, key, value, leaseID)
	}
	return nil, nil
}

func (m *mockMVCCService) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return nil, nil
}

func (m *mockMVCCService) GetAtRevision(ctx context.Context, key string, revision int64) (*kv.KeyValue, error) {
	if m.getAtRevisionFn != nil {
		return m.getAtRevisionFn(ctx, key, revision)
	}
	return nil, nil
}

func (m *mockMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	return nil, 0, m.rangeRevision, nil
}

func (m *mockMVCCService) Compact(ctx context.Context, revision int64) error {
	if m.compactFn != nil {
		return m.compactFn(ctx, revision)
	}
	return nil
}

func (m *mockMVCCService) CurrentRevision(ctx context.Context) (int64, error) {
	if m.currentRevisionFn != nil {
		return m.currentRevisionFn(ctx)
	}
	return 0, nil
}

func (m *mockMVCCService) GetAtRevisionWithConsistency(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCService) GetRevisionWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCService) PutWithLeaseAndMode(_ context.Context, _ string, _ []byte, _ int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return nil, nil
}

func (m *mockMVCCService) RangeWithConsistency(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return nil, 0, m.rangeRevision, nil
}

// ---------------------------------------------------------------------------
// Mock: TransactionServicer
// ---------------------------------------------------------------------------

type mockTransactionService struct {
	executeFn func(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error)
}

func (m *mockTransactionService) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, successOps, failureOps)
	}
	return nil, nil
}

func (m *mockTransactionService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, nil
}

func (m *mockTransactionService) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: BatchServicer
// ---------------------------------------------------------------------------

type mockBatchService struct {
	executeFn func(ctx context.Context, ops []service.Op) ([]service.OpResponse, error)
	importFn  func(ctx context.Context, file io.Reader, opts service.ImportOptions) (*service.ImportResult, error)
}

func (m *mockBatchService) Execute(ctx context.Context, ops []service.Op) ([]service.OpResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, ops)
	}
	return nil, nil
}

func (m *mockBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, nil
}

func (m *mockBatchService) Import(ctx context.Context, file io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	if m.importFn != nil {
		return m.importFn(ctx, file, opts)
	}
	return nil, nil
}

func (m *mockBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: WatchServicer
// ---------------------------------------------------------------------------

type mockWatchService struct {
	watchFn       func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchPrefixFn func(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

func (m *mockWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchFn != nil {
		return m.watchFn(ctx, key, opts...)
	}
	return nil, nil
}

func (m *mockWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchPrefixFn != nil {
		return m.watchPrefixFn(ctx, prefix, opts...)
	}
	return nil, nil
}

func (m *mockWatchService) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (m *mockWatchService) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// mockWatcher implements watch.Watcher for testing.
type mockWatcher struct {
	eventsCh chan watch.WatchResponse
	id       int64
	closeErr error
	watchErr error
}

func (w *mockWatcher) Events() <-chan watch.WatchResponse { return w.eventsCh }
func (w *mockWatcher) WatchID() int64                     { return w.id }
func (w *mockWatcher) Close() error                       { return w.closeErr }
func (w *mockWatcher) Err() error                         { return w.watchErr }

// ---------------------------------------------------------------------------
// Mock: LeaseServicer
// ---------------------------------------------------------------------------

type mockLeaseService struct {
	grantFn      func(ctx context.Context, ttl int64) (*lease.Lease, error)
	revokeFn     func(ctx context.Context, leaseID int64) error
	keepAliveFn  func(ctx context.Context, leaseID int64) (*lease.Lease, error)
	timeToLiveFn func(ctx context.Context, leaseID int64) (*lease.Lease, error)
	leasesFn     func(ctx context.Context) ([]*lease.Lease, error)
}

func (m *mockLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if m.grantFn != nil {
		return m.grantFn(ctx, ttl)
	}
	return nil, nil
}

func (m *mockLeaseService) Revoke(ctx context.Context, leaseID int64) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, leaseID)
	}
	return nil
}

func (m *mockLeaseService) KeepAlive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	if m.keepAliveFn != nil {
		return m.keepAliveFn(ctx, leaseID)
	}
	return nil, nil
}

func (m *mockLeaseService) TimeToLive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	if m.timeToLiveFn != nil {
		return m.timeToLiveFn(ctx, leaseID)
	}
	return nil, nil
}

func (m *mockLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: IndexServicer
// ---------------------------------------------------------------------------

type mockIndexService struct {
	createIndexFn func(ctx context.Context, name, fieldName string, config index.IndexConfig) error
	dropIndexFn   func(ctx context.Context, name string) error
	queryFn       func(ctx context.Context, indexName string, value []byte) ([][]byte, error)
	scanFn        func(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)
	listIndexesFn func(ctx context.Context) ([]string, error)
}

func (m *mockIndexService) CreateIndex(ctx context.Context, name, fieldName string, config index.IndexConfig) error {
	if m.createIndexFn != nil {
		return m.createIndexFn(ctx, name, fieldName, config)
	}
	return nil
}

func (m *mockIndexService) DropIndex(ctx context.Context, name string) error {
	if m.dropIndexFn != nil {
		return m.dropIndexFn(ctx, name)
	}
	return nil
}

func (m *mockIndexService) Query(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, indexName, value)
	}
	return nil, nil
}

func (m *mockIndexService) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, indexName, start, end)
	}
	return nil, nil
}

func (m *mockIndexService) ListIndexes(ctx context.Context) ([]string, error) {
	if m.listIndexesFn != nil {
		return m.listIndexesFn(ctx)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: TimeSeriesServicer
// ---------------------------------------------------------------------------

type mockTimeSeriesService struct {
	writeFn                      func(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error
	writeBatchFn                 func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn                      func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	queryWithConsistencyFn       func(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn                  func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	aggregateWithConsistencyFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error)
	listMetricsWithConsistencyFn func(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error)
	queryRichWithConsistencyFn   func(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error)
}

func (m *mockTimeSeriesService) Write(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error {
	if m.writeFn != nil {
		return m.writeFn(ctx, metric, value, timestamp, tags)
	}
	return nil
}

func (m *mockTimeSeriesService) WriteBatch(ctx context.Context, points []timeseries.DataPoint) error {
	if m.writeBatchFn != nil {
		return m.writeBatchFn(ctx, points)
	}
	return nil
}

func (m *mockTimeSeriesService) Query(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, metric, start, end, opts...)
	}
	return nil, nil
}

func (m *mockTimeSeriesService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryWithConsistencyFn != nil {
		return m.queryWithConsistencyFn(ctx, metric, start, end, ropts, opts...)
	}
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *mockTimeSeriesService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return nil, nil
}

func (m *mockTimeSeriesService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error) {
	if m.aggregateWithConsistencyFn != nil {
		return m.aggregateWithConsistencyFn(ctx, metric, start, end, fn, interval, ropts)
	}
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *mockTimeSeriesService) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return nil
}

func (m *mockTimeSeriesService) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	return nil
}

func (m *mockTimeSeriesService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	if m.queryRichWithConsistencyFn != nil {
		return m.queryRichWithConsistencyFn(ctx, req, ropts)
	}
	return m.QueryRich(ctx, req)
}

func (m *mockTimeSeriesService) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) SetRetentionPolicy(_ string, _ *timeseries.RetentionPolicy) error {
	return nil
}

func (m *mockTimeSeriesService) ListMetrics(_ string, _ int) ([]string, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) ListMetricsWithConsistency(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	if m.listMetricsWithConsistencyFn != nil {
		return m.listMetricsWithConsistencyFn(ctx, prefix, limit, ropts)
	}
	return m.ListMetrics(prefix, limit)
}

// ---------------------------------------------------------------------------
// Mock: flushableRecorder for SSE tests
// ---------------------------------------------------------------------------

type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func newFlushableRecorder() *flushableRecorder {
	return &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushableRecorder) Flush() { f.flushed++ }

// ---------------------------------------------------------------------------
// Server option tests
// ---------------------------------------------------------------------------

func TestWithMVCC(t *testing.T) {
	svc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(svc))
	if srv.mvccSvc == nil {
		t.Fatal("mvccSvc should not be nil after WithMVCC")
	}
}

func TestWithTransaction(t *testing.T) {
	svc := &mockTransactionService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTransaction(svc))
	if srv.txSvc == nil {
		t.Fatal("txSvc should not be nil after WithTransaction")
	}
}

func TestWithBatch(t *testing.T) {
	svc := &mockBatchService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(svc))
	if srv.batchSvc == nil {
		t.Fatal("batchSvc should not be nil after WithBatch")
	}
}

func TestWithWatch(t *testing.T) {
	svc := &mockWatchService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(svc))
	if srv.watchSvc == nil {
		t.Fatal("watchSvc should not be nil after WithWatch")
	}
}

func TestWithLease(t *testing.T) {
	svc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(svc))
	if srv.leaseSvc == nil {
		t.Fatal("leaseSvc should not be nil after WithLease")
	}
}

func TestWithIndex(t *testing.T) {
	svc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(svc))
	if srv.indexSvc == nil {
		t.Fatal("indexSvc should not be nil after WithIndex")
	}
}

func TestWithTimeSeries(t *testing.T) {
	svc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(svc))
	if srv.tsSvc == nil {
		t.Fatal("tsSvc should not be nil after WithTimeSeries")
	}
}

func TestWithCACertPool(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithCACertPool(nil))
	if srv.caCertPool != nil {
		t.Fatal("caCertPool should be nil when nil is passed")
	}
}

// ---------------------------------------------------------------------------
// KV Put with lease-id (MVCC extension in handlers.go)
// ---------------------------------------------------------------------------

func TestHandleKVPut_WithLeaseID_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		putWithLeaseFn: func(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key: []byte(key), Value: value,
				CreateRevision: 1, ModRevision: 2, Version: 1, LeaseID: leaseID,
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=42", strings.NewReader("val"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp kvResponse
	decodeJSON(t, rec, &resp)
	if resp.LeaseID != 42 {
		t.Fatalf("lease_id = %d, want 42", resp.LeaseID)
	}
	if string(resp.Key) != "mykey" {
		t.Fatalf("key = %q, want %q", resp.Key, "mykey")
	}
}

func TestHandleKVPut_WithLeaseID_NilMVCC(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=42", strings.NewReader("val"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleKVPut_WithLeaseID_InvalidID(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=abc", strings.NewReader("val"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleKVPut_WithLeaseID_ServiceError(t *testing.T) {
	mvccSvc := &mockMVCCService{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return nil, &service.LeaseNotFoundError{LeaseID: 42}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=42", strings.NewReader("val"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleKVPut_WithLeaseID_ForwardOnNotLeader(t *testing.T) {
	mvccSvc := &mockMVCCService{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return nil, &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
		},
	}
	// No resolver -> forward returns false -> mapHTTPError
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey?lease-id=42", strings.NewReader("val"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

// ---------------------------------------------------------------------------
// KV Get with revision (MVCC extension in handlers.go)
// ---------------------------------------------------------------------------

func TestHandleKVGet_WithRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key: []byte(key), Value: []byte("val"),
				CreateRevision: 1, ModRevision: 5, Version: 3,
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?revision=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp kvResponse
	decodeJSON(t, rec, &resp)
	if resp.ModRevision != 5 {
		t.Fatalf("mod_revision = %d, want 5", resp.ModRevision)
	}
}

func TestHandleKVGet_WithRevision_NilMVCC(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?revision=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleKVGet_WithRevision_ServiceError(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, &service.KeyNotFoundError{Key: "mykey"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?revision=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleKVGet_WithAtRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getAtRevisionFn: func(_ context.Context, key string, rev int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key: []byte(key), Value: []byte("old-val"),
				CreateRevision: 1, ModRevision: rev, Version: 2,
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?at-revision=5", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp kvResponse
	decodeJSON(t, rec, &resp)
	if resp.ModRevision != 5 {
		t.Fatalf("mod_revision = %d, want 5", resp.ModRevision)
	}
}

func TestHandleKVGet_WithAtRevision_NilMVCC(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?at-revision=5", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleKVGet_WithAtRevision_InvalidParam(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?at-revision=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleKVGet_WithAtRevision_ServiceError(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getAtRevisionFn: func(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
			return nil, &service.RevisionCompactedError{Requested: 2, Compacted: 5}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey?at-revision=2", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusRequestedRangeNotSatisfiable)
}

// ---------------------------------------------------------------------------
// handlers_mvcc.go: Range
// ---------------------------------------------------------------------------

func TestHandleRange_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		rangeFn: func(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return []*kv.KeyValue{
				{Key: start, Value: []byte("v1"), ModRevision: 10},
			}, 1, 42, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a&end=z", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp rangeResponse
	decodeJSON(t, rec, &resp)
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("kvs length = %d, want 1", len(resp.Kvs))
	}
	// Revision is distinct from count and must be threaded through from the
	// MVCC read, not left at its zero value.
	if resp.Revision != 42 {
		t.Fatalf("revision = %d, want 42", resp.Revision)
	}
}

func TestHandleRange_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRange_MissingStart(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/range", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRange_InvalidLimit(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a&limit=xyz", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRange_InvalidRevision(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a&revision=xyz", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleRange_ServiceError(t *testing.T) {
	mvccSvc := &mockMVCCService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 0, 0, &service.KeyNotFoundError{Key: "a"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/range?start=a", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleRange_WithAllOptions(t *testing.T) {
	mvccSvc := &mockMVCCService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
			return nil, 5, 7, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet,
		"/kv/range?start=a&end=z&limit=10&revision=5&count-only=true&keys-only=true&sort-order=descend&sort-target=mod_revision", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleRange_WrongMethod(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPost, "/kv/range?start=a", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// handlers_mvcc.go: Compact
// ---------------------------------------------------------------------------

func TestHandleCompact_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		compactFn: func(_ context.Context, rev int64) error {
			if rev != 10 {
				t.Fatalf("compact revision = %d, want 10", rev)
			}
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPost, "/kv/compact", strings.NewReader(`{"revision":10}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp okResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
}

func TestHandleCompact_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/kv/compact", strings.NewReader(`{"revision":10}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCompact_BadBody(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPost, "/kv/compact", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCompact_ServiceError(t *testing.T) {
	mvccSvc := &mockMVCCService{
		compactFn: func(_ context.Context, _ int64) error {
			return &service.RevisionNotFoundError{Revision: 999}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodPost, "/kv/compact", strings.NewReader(`{"revision":999}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handlers_mvcc.go: CurrentRevision
// ---------------------------------------------------------------------------

func TestHandleCurrentRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 42, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/revision", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp revisionResponse
	decodeJSON(t, rec, &resp)
	if resp.Revision != 42 {
		t.Fatalf("revision = %d, want 42", resp.Revision)
	}
}

func TestHandleCurrentRevision_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/revision", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCurrentRevision_ServiceError(t *testing.T) {
	mvccSvc := &mockMVCCService{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 0, errors.New("internal")
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithMVCC(mvccSvc))

	req := httptest.NewRequest(http.MethodGet, "/kv/revision", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// Sort parsing helpers
// ---------------------------------------------------------------------------

func TestParseSortOrder_AllValues(t *testing.T) {
	tests := []struct {
		input string
		want  service.SortOrder
	}{
		{"", service.SortNone},
		{"none", service.SortNone},
		{"ascend", service.SortAscend},
		{"descend", service.SortDescend},
		{"invalid", service.SortNone},
	}
	for _, tt := range tests {
		got := parseSortOrder(tt.input)
		if got != tt.want {
			t.Fatalf("parseSortOrder(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseSortTarget_AllValues(t *testing.T) {
	tests := []struct {
		input string
		want  service.SortTarget
	}{
		{"", service.SortByKey},
		{"key", service.SortByKey},
		{"version", service.SortByVersion},
		{"create_revision", service.SortByCreateRevision},
		{"mod_revision", service.SortByModRevision},
		{"value", service.SortByValue},
		{"invalid", service.SortByKey},
	}
	for _, tt := range tests {
		got := parseSortTarget(tt.input)
		if got != tt.want {
			t.Fatalf("parseSortTarget(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// handlers_transaction.go
// ---------------------------------------------------------------------------

func TestHandleTransaction_Success(t *testing.T) {
	txSvc := &mockTransactionService{
		executeFn: func(_ context.Context, compares []service.Compare, successOps, _ []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{
				Succeeded: true,
				Revision:  10,
				Responses: []service.OpResponse{
					{Succeeded: true},
				},
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTransaction(txSvc))

	body := `{
		"compares": [{"key":"k","target":"version","result":"equal","version":1}],
		"success_ops": [{"type":"put","key":"k","value":"v"}],
		"failure_ops": []
	}`
	req := httptest.NewRequest(http.MethodPost, "/tx", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp transactionResponseJSON
	decodeJSON(t, rec, &resp)
	if !resp.Succeeded {
		t.Fatal("succeeded = false, want true")
	}
	if resp.Revision != 10 {
		t.Fatalf("revision = %d, want 10", resp.Revision)
	}
}

func TestHandleTransaction_FailureBranch(t *testing.T) {
	txSvc := &mockTransactionService{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{
				Succeeded: false,
				Revision:  11,
				Responses: []service.OpResponse{
					{Succeeded: true, Kvs: []*kv.KeyValue{{Key: []byte("k"), Value: []byte("v")}}},
				},
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTransaction(txSvc))

	body := `{
		"compares": [{"key":"k","target":"value","result":"equal","value":"expected"}],
		"success_ops": [],
		"failure_ops": [{"type":"get","key":"k"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/tx", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp transactionResponseJSON
	decodeJSON(t, rec, &resp)
	if resp.Succeeded {
		t.Fatal("succeeded = true, want false")
	}
}

func TestHandleTransaction_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/tx", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTransaction_BadJSON(t *testing.T) {
	txSvc := &mockTransactionService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTransaction(txSvc))

	req := httptest.NewRequest(http.MethodPost, "/tx", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTransaction_ServiceError(t *testing.T) {
	txSvc := &mockTransactionService{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return nil, &service.TransactionConflictError{Key: "k"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTransaction(txSvc))

	body := `{"compares":[],"success_ops":[],"failure_ops":[]}`
	req := httptest.NewRequest(http.MethodPost, "/tx", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

// ---------------------------------------------------------------------------
// JSON-to-service conversion helpers
// ---------------------------------------------------------------------------

func TestJsonToOps_AllTypes(t *testing.T) {
	ops := []opJSON{
		{Type: "put", Key: "k1", Value: "v1", LeaseID: 5},
		{Type: "get", Key: "k2"},
		{Type: "delete", Key: "k3"},
		{Type: "range", Key: "k4", RangeEnd: "k9"},
		{Type: "unknown", Key: "k5"},
	}
	result := jsonToOps(ops)
	if len(result) != 5 {
		t.Fatalf("len = %d, want 5", len(result))
	}
	if result[0].Type != service.OpPut {
		t.Fatalf("ops[0].Type = %d, want OpPut", result[0].Type)
	}
	if result[0].LeaseID != 5 {
		t.Fatalf("ops[0].LeaseID = %d, want 5", result[0].LeaseID)
	}
	if result[1].Type != service.OpGet {
		t.Fatalf("ops[1].Type = %d, want OpGet", result[1].Type)
	}
	if result[2].Type != service.OpDelete {
		t.Fatalf("ops[2].Type = %d, want OpDelete", result[2].Type)
	}
	if result[3].Type != service.OpRange {
		t.Fatalf("ops[3].Type = %d, want OpRange", result[3].Type)
	}
	// unknown defaults to OpPut
	if result[4].Type != service.OpPut {
		t.Fatalf("ops[4].Type = %d, want OpPut (default)", result[4].Type)
	}
}

func TestParseCompareTarget_AllValues(t *testing.T) {
	tests := []struct {
		input string
		want  service.CompareTarget
	}{
		{"version", service.CompareVersion},
		{"create_revision", service.CompareCreateRevision},
		{"mod_revision", service.CompareModRevision},
		{"value", service.CompareValue},
		{"unknown", service.CompareVersion},
	}
	for _, tt := range tests {
		got := parseCompareTarget(tt.input)
		if got != tt.want {
			t.Fatalf("parseCompareTarget(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseCompareResult_AllValues(t *testing.T) {
	tests := []struct {
		input string
		want  service.CompareResult
	}{
		{"equal", service.CompareEqual},
		{"greater", service.CompareGreater},
		{"less", service.CompareLess},
		{"not_equal", service.CompareNotEqual},
		{"unknown", service.CompareEqual},
	}
	for _, tt := range tests {
		got := parseCompareResult(tt.input)
		if got != tt.want {
			t.Fatalf("parseCompareResult(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseOpType_AllValues(t *testing.T) {
	tests := []struct {
		input string
		want  service.OpType
	}{
		{"put", service.OpPut},
		{"get", service.OpGet},
		{"delete", service.OpDelete},
		{"range", service.OpRange},
		{"unknown", service.OpPut},
	}
	for _, tt := range tests {
		got := parseOpType(tt.input)
		if got != tt.want {
			t.Fatalf("parseOpType(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// handlers_batch.go
// ---------------------------------------------------------------------------

func TestHandleBatch_Success(t *testing.T) {
	batchSvc := &mockBatchService{
		executeFn: func(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
			resp := make([]service.OpResponse, len(ops))
			for i := range ops {
				resp[i] = service.OpResponse{Succeeded: true}
			}
			return resp, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(batchSvc))

	body := `{"ops":[{"type":"put","key":"k1","value":"v1"},{"type":"delete","key":"k2"}]}`
	req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp batchResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Responses) != 2 {
		t.Fatalf("responses count = %d, want 2", len(resp.Responses))
	}
}

func TestHandleBatch_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleBatch_BadBody(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(batchSvc))

	req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBatch_ServiceError(t *testing.T) {
	batchSvc := &mockBatchService{
		executeFn: func(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
			return nil, &service.BatchError{Index: 0, Err: errors.New("fail")}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(batchSvc))

	body := `{"ops":[{"type":"put","key":"k","value":"v"}]}`
	req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleBatch_EmptyOps(t *testing.T) {
	batchSvc := &mockBatchService{
		executeFn: func(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
			return []service.OpResponse{}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(batchSvc))

	body := `{"ops":[]}`
	req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------------------
// handlers_watch.go
// ---------------------------------------------------------------------------

func TestHandleWatch_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleWatch_MissingKey(t *testing.T) {
	watchSvc := &mockWatchService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleWatch_InvalidStartRevision(t *testing.T) {
	watchSvc := &mockWatchService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey&start-revision=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleWatch_WatchError(t *testing.T) {
	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, &service.WatchCanceledError{Reason: "test"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// nonFlushableWriter is a ResponseWriter that does NOT implement http.Flusher.
type nonFlushableWriter struct {
	code   int
	header http.Header
	body   []byte
}

func (w *nonFlushableWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *nonFlushableWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}

func (w *nonFlushableWriter) WriteHeader(code int) { w.code = code }

func TestHandleWatch_SSENotSupported(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	watcher := &mockWatcher{eventsCh: ch, id: 1}

	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	// Use a writer that does NOT implement http.Flusher.
	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	w := &nonFlushableWriter{}
	srv.handleWatch(w, req)

	if w.code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.code, http.StatusInternalServerError)
	}
}

func TestHandleWatch_SSEStream(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  1,
		Revision: 5,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("k"), Value: []byte("v")}},
		},
		Canceled:     true,
		CancelReason: "done",
	}
	watcher := &mockWatcher{eventsCh: ch, id: 1}

	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey&prev-kv=true", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.flushed < 1 {
		t.Fatal("expected at least one Flush call")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("body should contain SSE data frame, got: %s", body)
	}
	if !strings.Contains(body, `"type":"PUT"`) {
		t.Fatalf("body should contain PUT event type, got: %s", body)
	}
}

func TestHandleWatch_SSEStreamPrefix(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  2,
		Revision: 3,
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte("prefix/k")}},
		},
		Canceled: true,
	}
	watcher := &mockWatcher{eventsCh: ch, id: 2}

	watchSvc := &mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=prefix/&prefix=true&start-revision=1", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"DELETE"`) {
		t.Fatalf("body should contain DELETE event type, got: %s", body)
	}
}

func TestHandleWatch_SSEStreamContextCancel(t *testing.T) {
	ch := make(chan watch.WatchResponse) // blocking channel
	watcher := &mockWatcher{eventsCh: ch, id: 1}

	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	req = req.WithContext(ctx)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleWatch_SSEStreamChannelClose(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch) // closed immediately
	watcher := &mockWatcher{eventsCh: ch, id: 1}

	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestEventTypeToString(t *testing.T) {
	if got := eventTypeToString(watch.EventPut); got != "PUT" {
		t.Fatalf("eventTypeToString(EventPut) = %q, want PUT", got)
	}
	if got := eventTypeToString(watch.EventDelete); got != "DELETE" {
		t.Fatalf("eventTypeToString(EventDelete) = %q, want DELETE", got)
	}
}

func TestHandleWatch_WatchPrefixError(t *testing.T) {
	watchSvc := &mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, errors.New("prefix watch failed")
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=prefix/&prefix=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// handlers_lease.go
// ---------------------------------------------------------------------------

func TestHandleLeaseGrant_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		grantFn: func(_ context.Context, ttl int64) (*lease.Lease, error) {
			return &lease.Lease{ID: 100, TTL: ttl, GrantedTTL: ttl, Keys: [][]byte{[]byte("k1")}}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/grant", strings.NewReader(`{"ttl":60}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp leaseResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 100 {
		t.Fatalf("id = %d, want 100", resp.ID)
	}
	if resp.TTL != 60 {
		t.Fatalf("ttl = %d, want 60", resp.TTL)
	}
	if len(resp.Keys) != 1 || resp.Keys[0] != "k1" {
		t.Fatalf("keys = %v, want [k1]", resp.Keys)
	}
}

func TestHandleLeaseGrant_NilService(t *testing.T) {
	// With route gating, /lease/grant is not registered without a lease service.
	// The dispatcher returns 404 (route not found) rather than reaching a handler
	// that would propose to a SM without a lease subsystem (which would stall reads).
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/lease/grant", strings.NewReader(`{"ttl":60}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseGrant_BadBody(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/grant", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleLeaseGrant_ServiceError(t *testing.T) {
	leaseSvc := &mockLeaseService{
		grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, &service.LeaseExistsError{LeaseID: 100}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/grant", strings.NewReader(`{"ttl":60}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleLeaseRevoke_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		revokeFn: func(_ context.Context, id int64) error {
			if id != 100 {
				t.Fatalf("revoke id = %d, want 100", id)
			}
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/revoke", strings.NewReader(`{"lease_id":100}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleLeaseRevoke_NilService(t *testing.T) {
	// Route not registered without lease service — 404 from dispatcher.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/lease/revoke", strings.NewReader(`{"lease_id":100}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseRevoke_BadBody(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/revoke", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleLeaseRevoke_ServiceError(t *testing.T) {
	leaseSvc := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			return &service.LeaseNotFoundError{LeaseID: 100}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/revoke", strings.NewReader(`{"lease_id":100}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseKeepAlive_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		keepAliveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{ID: id, TTL: 60, GrantedTTL: 60, RemainingTTL: 60}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/keepalive", strings.NewReader(`{"lease_id":100}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp leaseResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 100 {
		t.Fatalf("id = %d, want 100", resp.ID)
	}
}

func TestHandleLeaseKeepAlive_NilService(t *testing.T) {
	// Route not registered without lease service — 404 from dispatcher.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/lease/keepalive", strings.NewReader(`{"lease_id":100}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseKeepAlive_BadBody(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/keepalive", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleLeaseKeepAlive_ServiceError(t *testing.T) {
	leaseSvc := &mockLeaseService{
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, &service.LeaseExpiredError{LeaseID: 100}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/keepalive", strings.NewReader(`{"lease_id":100}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseTimeToLive_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		timeToLiveFn: func(_ context.Context, id int64) (*lease.Lease, error) {
			return &lease.Lease{ID: id, TTL: 60, GrantedTTL: 120, RemainingTTL: 45}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodGet, "/lease/ttl?lease_id=100", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp leaseResponse
	decodeJSON(t, rec, &resp)
	if resp.RemainingTTL != 45 {
		t.Fatalf("remaining_ttl = %d, want 45", resp.RemainingTTL)
	}
}

func TestHandleLeaseTimeToLive_NilService(t *testing.T) {
	// Route not registered without lease service — 404 from dispatcher.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/lease/ttl?lease_id=100", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseTimeToLive_MissingParam(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodGet, "/lease/ttl", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleLeaseTimeToLive_InvalidParam(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodGet, "/lease/ttl?lease_id=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleLeaseTimeToLive_ServiceError(t *testing.T) {
	leaseSvc := &mockLeaseService{
		timeToLiveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, &service.LeaseNotFoundError{LeaseID: 999}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodGet, "/lease/ttl?lease_id=999", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseList_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{
				{ID: 1, TTL: 60, GrantedTTL: 60},
				{ID: 2, TTL: 120, GrantedTTL: 120},
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodGet, "/lease/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp leasesResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Leases) != 2 {
		t.Fatalf("leases count = %d, want 2", len(resp.Leases))
	}
}

func TestHandleLeaseList_NilService(t *testing.T) {
	// Route not registered without lease service — 404 from dispatcher.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/lease/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleLeaseList_ServiceError(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return nil, errors.New("list failed")
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodGet, "/lease/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestLeaseToResponse_Nil(t *testing.T) {
	resp := leaseToResponse(nil)
	if resp != nil {
		t.Fatal("leaseToResponse(nil) should return nil")
	}
}

// ---------------------------------------------------------------------------
// handlers_index.go
// ---------------------------------------------------------------------------

func TestHandleIndexCreate_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		createIndexFn: func(_ context.Context, name, fieldName string, config index.IndexConfig) error {
			if name != "idx1" || fieldName != "email" {
				t.Fatalf("CreateIndex(%q, %q) unexpected", name, fieldName)
			}
			if !config.Unique {
				t.Fatal("expected unique=true")
			}
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	body := `{"name":"idx1","field_name":"email","unique":true,"sparse":false}`
	req := httptest.NewRequest(http.MethodPost, "/index/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleIndexCreate_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/index/create", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleIndexCreate_BadBody(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodPost, "/index/create", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexCreate_ServiceError(t *testing.T) {
	indexSvc := &mockIndexService{
		createIndexFn: func(_ context.Context, _ string, _ string, _ index.IndexConfig) error {
			return &service.IndexExistsError{Name: "idx1"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	body := `{"name":"idx1","field_name":"email"}`
	req := httptest.NewRequest(http.MethodPost, "/index/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleIndexDrop_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		dropIndexFn: func(_ context.Context, name string) error {
			if name != "idx1" {
				t.Fatalf("DropIndex(%q) unexpected", name)
			}
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodPost, "/index/drop", strings.NewReader(`{"name":"idx1"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleIndexDrop_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/index/drop", strings.NewReader(`{"name":"idx1"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleIndexDrop_BadBody(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodPost, "/index/drop", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexDrop_ServiceError(t *testing.T) {
	indexSvc := &mockIndexService{
		dropIndexFn: func(_ context.Context, _ string) error {
			return &service.IndexNotFoundError{Name: "idx1"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodPost, "/index/drop", strings.NewReader(`{"name":"idx1"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleIndexQuery_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		queryFn: func(_ context.Context, _ string, _ []byte) ([][]byte, error) {
			return [][]byte{[]byte("1"), []byte("2"), []byte("3")}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/query?index_name=idx1&value=foo", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp indexQueryResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Keys) != 3 {
		t.Fatalf("keys count = %d, want 3", len(resp.Keys))
	}
}

func TestHandleIndexQuery_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/index/query?index_name=idx1&value=foo", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleIndexQuery_MissingIndexName(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/query?value=foo", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexQuery_MissingValue(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/query?index_name=idx1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexQuery_ServiceError(t *testing.T) {
	indexSvc := &mockIndexService{
		queryFn: func(_ context.Context, _ string, _ []byte) ([][]byte, error) {
			return nil, &service.IndexNotFoundError{Name: "idx1"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/query?index_name=idx1&value=foo", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleIndexScan_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return map[string][][]byte{"a": {[]byte("1")}, "b": {[]byte("2"), []byte("3")}}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/scan?index_name=idx1&start=a&end=z", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp indexScanResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Entries) != 2 {
		t.Fatalf("entries count = %d, want 2", len(resp.Entries))
	}
}

func TestHandleIndexScan_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/index/scan?index_name=idx1&start=a&end=z", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleIndexScan_MissingIndexName(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/scan?start=a&end=z", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexScan_MissingStart(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/scan?index_name=idx1&end=z", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexScan_MissingEnd(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/scan?index_name=idx1&start=a", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleIndexScan_ServiceError(t *testing.T) {
	indexSvc := &mockIndexService{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return nil, &service.IndexNotFoundError{Name: "idx1"}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(indexSvc))

	req := httptest.NewRequest(http.MethodGet, "/index/scan?index_name=idx1&start=a&end=z", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handlers_timeseries.go
// ---------------------------------------------------------------------------

func TestHandleTSWrite_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		writeFn: func(_ context.Context, metric string, value float64, ts time.Time, tags map[string]string) error {
			if metric != "cpu" {
				t.Fatalf("metric = %q, want cpu", metric)
			}
			if value != 95.5 {
				t.Fatalf("value = %f, want 95.5", value)
			}
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	body := `{"metric":"cpu","value":95.5,"timestamp":1000000000,"tags":{"host":"a"}}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleTSWrite_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/ts/write", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSWrite_BadBody(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodPost, "/ts/write", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSWrite_ServiceError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		writeFn: func(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
			return &service.TimeSeriesError{Op: "write", Err: errors.New("fail")}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	body := `{"metric":"cpu","value":95.5,"timestamp":1000}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSWriteBatch_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		writeBatchFn: func(_ context.Context, points []timeseries.DataPoint) error {
			if len(points) != 2 {
				t.Fatalf("points count = %d, want 2", len(points))
			}
			return nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	body := `{"points":[{"metric":"cpu","value":1.0,"timestamp":1000},{"metric":"mem","value":2.0,"timestamp":2000}]}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write-batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleTSWriteBatch_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPost, "/ts/write-batch", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSWriteBatch_BadBody(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodPost, "/ts/write-batch", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSWriteBatch_ServiceError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		writeBatchFn: func(_ context.Context, _ []timeseries.DataPoint) error {
			return &service.TimeSeriesError{Op: "write_batch", Err: errors.New("fail")}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	body := `{"points":[{"metric":"cpu","value":1.0,"timestamp":1000}]}`
	req := httptest.NewRequest(http.MethodPost, "/ts/write-batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSQuery_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return []timeseries.DataPoint{
				{Metric: "cpu", Value: 95.5, Timestamp: time.Unix(0, 1000), Tags: map[string]string{"host": "a"}},
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=0&end_ns=2000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp tsQueryResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Points) != 1 {
		t.Fatalf("points count = %d, want 1", len(resp.Points))
	}
	if resp.Points[0].Metric != "cpu" {
		t.Fatalf("metric = %q, want cpu", resp.Points[0].Metric)
	}
}

func TestHandleTSQuery_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=0&end_ns=2000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSQuery_MissingMetric(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?start_ns=0&end_ns=2000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSQuery_InvalidStartNs(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=abc&end_ns=2000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSQuery_InvalidEndNs(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=0&end_ns=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSQuery_InvalidLimit(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=0&end_ns=2000&limit=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSQuery_WithAllOptions(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return []timeseries.DataPoint{}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=0&end_ns=2000&tags=region%3Dus-east%2Cenv%3Dprod&limit=10&descending=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleTSQuery_ServiceError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, &service.TimeSeriesError{Op: "query", Err: errors.New("fail")}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/query?metric=cpu&start_ns=0&end_ns=2000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSAggregate_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			return []timeseries.AggResult{
				{Timestamp: time.Unix(0, 1000), Value: 42.0, Count: 10},
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&function=avg&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp tsAggregateResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Results) != 1 {
		t.Fatalf("results count = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Value != 42.0 {
		t.Fatalf("value = %f, want 42.0", resp.Results[0].Value)
	}
}

func TestHandleTSAggregate_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&function=avg&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleTSAggregate_MissingMetric(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?start_ns=0&end_ns=2000&function=avg&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSAggregate_InvalidStartNs(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=abc&end_ns=2000&function=avg&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSAggregate_InvalidEndNs(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=abc&function=avg&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSAggregate_MissingFunction(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSAggregate_InvalidFunction(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&function=badname&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSAggregate_InvalidIntervalNs(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&function=avg&interval_ns=abc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleTSAggregate_ServiceError(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			return nil, &service.TimeSeriesError{Op: "aggregate", Err: errors.New("fail")}
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&function=sum&interval_ns=1000", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

func TestHandleTSAggregate_AllFunctions(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			return []timeseries.AggResult{}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	functions := []string{"avg", "sum", "min", "max", "count", "percentile"}
	for _, fn := range functions {
		req := httptest.NewRequest(http.MethodGet, "/ts/aggregate?metric=cpu&start_ns=0&end_ns=2000&function="+fn+"&interval_ns=1000", nil)
		rec := httptest.NewRecorder()
		srv.dispatch(rec, req)
		assertStatus(t, rec, http.StatusOK)
	}
}

// ---------------------------------------------------------------------------
// parseTags helper
// ---------------------------------------------------------------------------

func TestParseTags(t *testing.T) {
	tests := []struct {
		input string
		want  map[string]string
	}{
		{"region=us-east,env=prod", map[string]string{"region": "us-east", "env": "prod"}},
		{"single=val", map[string]string{"single": "val"}},
		{"", map[string]string{}},
		{"no-equals-sign", map[string]string{}},
		{" key = val ", map[string]string{"key": "val"}},
	}
	for _, tt := range tests {
		got := parseTags(tt.input)
		if len(got) != len(tt.want) {
			t.Fatalf("parseTags(%q) len = %d, want %d", tt.input, len(got), len(tt.want))
		}
		for k, v := range tt.want {
			if got[k] != v {
				t.Fatalf("parseTags(%q)[%q] = %q, want %q", tt.input, k, got[k], v)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// kvToResponse / kvsToResponse helpers
// ---------------------------------------------------------------------------

func TestKvToResponse_Nil(t *testing.T) {
	if got := kvToResponse(nil); got != nil {
		t.Fatal("kvToResponse(nil) should return nil")
	}
}

func TestKvToResponse_NonNil(t *testing.T) {
	kv := &kv.KeyValue{Key: []byte("k"), Value: []byte("v"), CreateRevision: 1, ModRevision: 2, Version: 3, LeaseID: 4}
	got := kvToResponse(kv)
	if !bytes.Equal(got.Key, []byte("k")) || !bytes.Equal(got.Value, []byte("v")) ||
		got.CreateRevision != 1 || got.ModRevision != 2 || got.Version != 3 || got.LeaseID != 4 {
		t.Fatalf("unexpected kvResponse: %+v", got)
	}
}

func TestKvsToResponse(t *testing.T) {
	kvs := []*kv.KeyValue{
		{Key: []byte("a"), Value: []byte("1")},
		{Key: []byte("b"), Value: []byte("2")},
	}
	got := kvsToResponse(kvs)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !bytes.Equal(got[0].Key, []byte("a")) || !bytes.Equal(got[1].Key, []byte("b")) {
		t.Fatalf("unexpected keys: %s, %s", got[0].Key, got[1].Key)
	}
}

// TestKvToResponse_NonUTF8RoundTrip verifies that a KV value containing
// invalid UTF-8 bytes round-trips byte-identical through kvToResponse and
// JSON marshal/unmarshal. kvResponse fields are []byte so encoding/json
// base64-encodes them (RFC 4648) rather than mangling invalid UTF-8
// sequences into U+FFFD, as a string field would.
func TestKvToResponse_NonUTF8RoundTrip(t *testing.T) {
	rawKey := []byte{0xff, 0xfe, 0x00, 'k'}
	rawValue := []byte{0xff, 0xfe, 0x00, 'v', 0x80, 0x81}

	kv := &kv.KeyValue{Key: rawKey, Value: rawValue, CreateRevision: 1, ModRevision: 2, Version: 3}
	got := kvToResponse(kv)

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded kvResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if !bytes.Equal(decoded.Key, rawKey) {
		t.Fatalf("key round-trip mismatch: got %v, want %v", decoded.Key, rawKey)
	}
	if !bytes.Equal(decoded.Value, rawValue) {
		t.Fatalf("value round-trip mismatch: got %v, want %v", decoded.Value, rawValue)
	}
}

// ---------------------------------------------------------------------------
// errors.go: new error types
// ---------------------------------------------------------------------------

func TestServiceNotImplementedError(t *testing.T) {
	err := &ServiceNotImplementedError{Service: "mvcc"}
	want := "rest: service not implemented: mvcc"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestMissingParameterError(t *testing.T) {
	err := &MissingParameterError{Param: "key"}
	want := "rest: missing required parameter: key"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestInvalidParameterError(t *testing.T) {
	inner := errors.New("parse error")
	err := &InvalidParameterError{Param: "revision", Err: inner}
	want := "rest: invalid parameter revision: parse error"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap() did not return inner error")
	}
}

func TestSSENotSupportedError(t *testing.T) {
	err := &SSENotSupportedError{}
	want := "rest: server-sent events not supported by response writer"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestInvalidAggFuncError(t *testing.T) {
	err := &InvalidAggFuncError{Func: "median"}
	want := "rest: invalid aggregation function: median"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// mapHTTPError
// ---------------------------------------------------------------------------

func TestMapHTTPError_AllTypes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"KeyNotFound", &service.KeyNotFoundError{Key: "k"}, http.StatusNotFound},
		{"RevisionCompacted", &service.RevisionCompactedError{Requested: 1, Compacted: 5}, http.StatusRequestedRangeNotSatisfiable},
		{"RevisionNotFound", &service.RevisionNotFoundError{Revision: 10}, http.StatusNotFound},
		{"TransactionConflict", &service.TransactionConflictError{Key: "k"}, http.StatusConflict},
		{"WatchCanceled", &service.WatchCanceledError{Reason: "compact"}, http.StatusInternalServerError},
		{"LeaseNotFound", &service.LeaseNotFoundError{LeaseID: 1}, http.StatusNotFound},
		{"LeaseExpired", &service.LeaseExpiredError{LeaseID: 1}, http.StatusNotFound},
		{"LeaseExists", &service.LeaseExistsError{LeaseID: 1}, http.StatusConflict},
		{"BatchError", &service.BatchError{Index: 0, Err: errors.New("fail")}, http.StatusInternalServerError},
		{"IndexNotFound", &service.IndexNotFoundError{Name: "idx"}, http.StatusNotFound},
		{"IndexExists", &service.IndexExistsError{Name: "idx"}, http.StatusConflict},
		{"TimeSeriesError", &service.TimeSeriesError{Op: "w", Err: errors.New("f")}, http.StatusInternalServerError},
		{"ConsistencyError", &service.ConsistencyError{Level: "strong", Reason: "stale"}, http.StatusPreconditionFailed},
		{"NotLeader", &service.NotLeaderError{ShardID: 1, LeaderID: 2}, http.StatusServiceUnavailable},
		{"ImportInvalidFormat", &service.ImportInvalidFormatError{Format: "xml"}, http.StatusBadRequest},
		{"ImportInvalidValueMode", &service.ImportInvalidValueModeError{Mode: "bad"}, http.StatusBadRequest},
		{"ImportMissingKeyField", &service.ImportMissingKeyFieldError{}, http.StatusBadRequest},
		{"ImportMissingValueField", &service.ImportMissingValueFieldError{}, http.StatusBadRequest},
		{"ImportParseError", &service.ImportParseError{Err: errors.New("parse")}, http.StatusBadRequest},
		{"ImportArchiveError", &service.ImportArchiveError{Err: errors.New("archive")}, http.StatusBadRequest},
		{"ImportTransactionAborted", &service.ImportTransactionAbortedError{Row: 1, Key: "k", Message: "fail"}, http.StatusConflict},
		{"Unknown", errors.New("generic"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		got := mapHTTPError(tt.err)
		if got != tt.want {
			t.Fatalf("mapHTTPError(%s) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Batch Import Handler Tests
// ---------------------------------------------------------------------------

// createMultipartRequest creates a multipart/form-data request for testing.
func createMultipartRequest(t *testing.T, fields map[string]string, filename, fileContent string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add form fields.
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("failed to write field %s: %v", k, err)
		}
	}

	// Add file.
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("failed to create form file: %v", err)
		}
		if _, err := part.Write([]byte(fileContent)); err != nil {
			t.Fatalf("failed to write file content: %v", err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/batch/import", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestHandleBatchImport_Success(t *testing.T) {
	var capturedOpts service.ImportOptions
	batchSvc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
			capturedOpts = opts
			return &service.ImportResult{
				Total:     2,
				Succeeded: 2,
				Failed:    0,
				Errors:    nil,
			}, nil
		},
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":     "csv",
		"keyField":   "id",
		"valueMode":  "field",
		"valueField": "value",
		"skipHeader": "true",
	}, "data.csv", "id,value\nkey1,v1\nkey2,v2\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp batchImportResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("expected Total=2, got %d", resp.Total)
	}
	if resp.Succeeded != 2 {
		t.Fatalf("expected Succeeded=2, got %d", resp.Succeeded)
	}
	if resp.Failed != 0 {
		t.Fatalf("expected Failed=0, got %d", resp.Failed)
	}

	// Verify options were passed correctly.
	if capturedOpts.Format != "csv" {
		t.Fatalf("expected format 'csv', got %q", capturedOpts.Format)
	}
	if capturedOpts.KeyField != "id" {
		t.Fatalf("expected keyField 'id', got %q", capturedOpts.KeyField)
	}
	if capturedOpts.ValueMode != "field" {
		t.Fatalf("expected valueMode 'field', got %q", capturedOpts.ValueMode)
	}
	if capturedOpts.ValueField != "value" {
		t.Fatalf("expected valueField 'value', got %q", capturedOpts.ValueField)
	}
	if !capturedOpts.SkipHeader {
		t.Fatal("expected skipHeader=true")
	}
}

func TestHandleBatchImport_WithErrors(t *testing.T) {
	batchSvc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total:     3,
				Succeeded: 1,
				Failed:    2,
				Errors: []service.ImportError{
					{Row: 2, Key: "bad-key", Message: "invalid value format"},
					{Row: 3, Key: "other-key", Message: "key too long"},
				},
			}, nil
		},
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "csv",
		"keyField":  "id",
		"valueMode": "row",
	}, "data.csv", "id\nkey1\nbad-key\nother-key\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp batchImportResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Failed != 2 {
		t.Fatalf("expected Failed=2, got %d", resp.Failed)
	}
	if len(resp.Errors) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(resp.Errors))
	}
	if resp.Errors[0].Row != 2 {
		t.Fatalf("expected first error row=2, got %d", resp.Errors[0].Row)
	}
}

func TestHandleBatchImport_ServiceNotImplemented(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0")

	req := createMultipartRequest(t, map[string]string{
		"format":    "csv",
		"keyField":  "id",
		"valueMode": "row",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected status 501, got %d", rr.Code)
	}
}

func TestHandleBatchImport_MissingFile(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	// Request without file.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	_ = writer.WriteField("format", "csv")
	_ = writer.WriteField("keyField", "id")
	_ = writer.WriteField("valueMode", "row")
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/batch/import", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_MissingFormat(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"keyField":  "id",
		"valueMode": "row",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_InvalidFormat(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "xml",
		"keyField":  "id",
		"valueMode": "row",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_MissingKeyField(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "csv",
		"valueMode": "row",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_MissingValueMode(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":   "csv",
		"keyField": "id",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_InvalidValueMode(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "csv",
		"keyField":  "id",
		"valueMode": "invalid",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_MissingValueFieldWhenFieldMode(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "csv",
		"keyField":  "id",
		"valueMode": "field",
	}, "data.csv", "id,value\nkey1,v1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_InvalidSkipHeader(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":     "csv",
		"keyField":   "id",
		"valueMode":  "row",
		"skipHeader": "notabool",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_InvalidTransactional(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":        "csv",
		"keyField":      "id",
		"valueMode":     "row",
		"transactional": "notabool",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_ServiceError(t *testing.T) {
	batchSvc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, &service.ImportParseError{Err: errors.New("invalid csv")}
		},
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "csv",
		"keyField":  "id",
		"valueMode": "row",
	}, "data.csv", "not valid csv")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleBatchImport_TransactionalMode(t *testing.T) {
	var capturedOpts service.ImportOptions
	batchSvc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
			capturedOpts = opts
			return &service.ImportResult{Total: 1, Succeeded: 1}, nil
		},
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":        "csv",
		"keyField":      "id",
		"valueMode":     "row",
		"transactional": "true",
	}, "data.csv", "id\nkey1\n")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	if !capturedOpts.Transactional {
		t.Fatal("expected transactional=true")
	}
}

func TestHandleBatchImport_JSONFormat(t *testing.T) {
	var capturedOpts service.ImportOptions
	batchSvc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
			capturedOpts = opts
			return &service.ImportResult{Total: 1, Succeeded: 1}, nil
		},
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":     "json",
		"keyField":   "id",
		"valueMode":  "field",
		"valueField": "value",
	}, "data.json", `[{"id":"key1","value":"v1"}]`)

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	if capturedOpts.Format != "json" {
		t.Fatalf("expected format 'json', got %q", capturedOpts.Format)
	}
}

func TestHandleBatchImport_ArchiveFormat(t *testing.T) {
	var capturedOpts service.ImportOptions
	batchSvc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
			capturedOpts = opts
			return &service.ImportResult{Total: 1, Succeeded: 1}, nil
		},
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(batchSvc))

	req := createMultipartRequest(t, map[string]string{
		"format":    "archive",
		"keyField":  "id",
		"valueMode": "row",
	}, "data.zip", "mock archive content")

	rr := httptest.NewRecorder()
	srv.handleBatchImport(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	if capturedOpts.Format != "archive" {
		t.Fatalf("expected format 'archive', got %q", capturedOpts.Format)
	}
}

// ---------------------------------------------------------------------------
// Stale read tests -- exercises ?stale=true code paths
// ---------------------------------------------------------------------------

func TestHandleKVGet_StaleRead_Success(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key != "test-key" {
				t.Fatalf("unexpected key: %s", key)
			}
			return []byte("stale-value"), nil
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/test-key?stale=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if got := rec.Body.String(); got != "stale-value" {
		t.Fatalf("body = %q, want %q", got, "stale-value")
	}
}

func TestHandleKVGet_StaleRead_NotFound(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "missing"}
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/missing?stale=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleKVList_StaleRead_Success(t *testing.T) {
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			if prefix != "app/" {
				t.Fatalf("unexpected prefix: %s", prefix)
			}
			return []string{"app/key1", "app/key2"}, nil
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv?prefix=app/&stale=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp kvListResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Keys) != 2 {
		t.Fatalf("keys count = %d, want 2", len(resp.Keys))
	}
}

func TestHandleKVList_StaleRead_ServiceError(t *testing.T) {
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return nil, errors.New("storage failure")
		},
	}
	srv := NewServer(kvSvc, newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv?prefix=app/&stale=true", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// KV Put body read error
// ---------------------------------------------------------------------------

// errorReader is an io.ReadCloser that always returns an error.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failure") }
func (errorReader) Close() error             { return nil }

func TestHandleKVPut_BodyReadError(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPut, "/kv/test-key", errorReader{})
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Direct handler calls with nil services (defensive guard coverage)
// ---------------------------------------------------------------------------
// These test the nil-service checks inside handlers that are normally
// unreachable through dispatch because routes are conditionally registered.
// Calling the handler directly on a server without the service exercises
// the defensive guard.

func TestHandleCreateUser_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"username":"alice","password":"pass"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleCreateUser(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListUsers_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/users/list", nil)
	rec := httptest.NewRecorder()
	srv.handleListUsers(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUser_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/users/9001", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCreateRole_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"name":"admin","permissions":["kv:put"]}`
	req := httptest.NewRequest(http.MethodPost, "/roles", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleCreateRole(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListRoles_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/roles/list", nil)
	rec := httptest.NewRecorder()
	srv.handleListRoles(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRole_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/roles/role-001", nil)
	rec := httptest.NewRecorder()
	srv.handleRole(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleAssignRole_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"user_id":"1","role_id":"r1"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/assign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAssignRole(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUnassignRole_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"user_id":"1","role_id":"r1"}`
	req := httptest.NewRequest(http.MethodPost, "/roles/unassign", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleUnassignRole(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleGenerateCert_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"username":"alice","tenantID":"t1"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/generate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleGenerateCert(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleExportCert_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"certPEM":"c","keyPEM":"k","passphrase":"p"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/export", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleExportCert(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleRevokeCert_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"serial":"abc123"}`
	req := httptest.NewRequest(http.MethodPost, "/certs/revoke", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleRevokeCert(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListCerts_NilService_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/certs/list?tenantID=t1", nil)
	rec := httptest.NewRecorder()
	srv.handleListCerts(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleTSSeries empty metric via direct handler call
// ---------------------------------------------------------------------------

func TestHandleTSSeries_EmptyMetric_Direct(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithTimeSeries(tsSvc))

	// Path is exactly "/ts/series/" so after TrimPrefix the rest is "".
	req := httptest.NewRequest(http.MethodDelete, "/ts/series/", nil)
	rec := httptest.NewRecorder()
	srv.handleTSSeries(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleCABootstrap empty cert bytes via direct handler call
// ---------------------------------------------------------------------------

func TestHandleCABootstrap_EmptyCert_Direct(t *testing.T) {
	// Construct a server with empty caBootstrapCert by calling the handler
	// directly (the route won't be registered because len(cert)==0).
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.handleCABootstrap(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

// ---------------------------------------------------------------------------
// handleUser empty ID via direct handler call (defensive guard coverage)
// ---------------------------------------------------------------------------

func TestHandleUser_EmptyID_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithUserService(&mockUserService{}))

	// Path "/users/" yields empty string after TrimPrefix.
	req := httptest.NewRequest(http.MethodGet, "/users/", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleRole empty ID via direct handler call (defensive guard coverage)
// ---------------------------------------------------------------------------

func TestHandleRole_EmptyID_Direct(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithRoleService(&mockRoleService{}))

	// Path "/roles/" yields empty string after TrimPrefix.
	req := httptest.NewRequest(http.MethodGet, "/roles/", nil)
	rec := httptest.NewRecorder()
	srv.handleRole(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleBatchImport ParseMultipartForm error
// ---------------------------------------------------------------------------

func TestHandleBatchImport_InvalidMultipartBody(t *testing.T) {
	batchSvc := &mockBatchService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithBatch(batchSvc))

	// Send a non-multipart body with the multipart content type.
	req := httptest.NewRequest(http.MethodPost, "/batch/import", strings.NewReader("not multipart data"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=invalid")
	rec := httptest.NewRecorder()
	srv.handleBatchImport(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// PutWithLease forward success via tryForwardWrite
// ---------------------------------------------------------------------------

func TestHandleKVPut_WithLeaseID_ForwardSuccess(t *testing.T) {
	// Set up a fake leader HTTP server that returns 200 with a response body.
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer leaderSrv.Close()

	// Create a mock MVCC service that returns a NotLeader error with the
	// Raft address that resolves to our fake leader.
	mvccSvc := &mockMVCCService{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return nil, &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "127.0.0.1:9001"}
		},
	}

	// Create a mock resolver that maps the Raft address to our fake leader.
	registry := NewPeerAdminRegistry("")
	registry.Register("127.0.0.1:9001", leaderSrv.URL)

	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithMVCC(mvccSvc),
		WithLeaderForwarding(registry),
	)

	req := httptest.NewRequest(http.MethodPut, "/kv/test-key?lease-id=42",
		strings.NewReader("value-data"))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// The server should forward to the leader and return the leader's response.
	assertStatus(t, rec, http.StatusOK)
}

// RestoreToRevision satisfies service.MVCCKVServicer.
func (m *mockMVCCService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if m.restoreFn != nil {
		return m.restoreFn(ctx, req)
	}
	return nil, nil
}

// TestRESTServiceNotImplementedErrorHint verifies the optional Hint is appended to the message
// so refusals can name the remedy (e.g. "requires mode: raft").
func TestRESTServiceNotImplementedErrorHint(t *testing.T) {
	err := &ServiceNotImplementedError{Service: "watch", Hint: "requires mode: raft"}
	if got := err.Error(); !strings.Contains(got, "watch") || !strings.Contains(got, "requires mode: raft") {
		t.Fatalf("Error() = %q, want the service name and the hint", got)
	}
	bare := &ServiceNotImplementedError{Service: "watch"}
	if strings.Contains(bare.Error(), ":  ") || strings.HasSuffix(bare.Error(), ": ") {
		t.Fatalf("Error() without hint = %q, must not carry a dangling separator", bare.Error())
	}
}
