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
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

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
	rangeFn           func(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error)
	compactFn         func(ctx context.Context, revision int64) error
	currentRevisionFn func(ctx context.Context) (int64, error)
}

func newMockMVCCService() *mockMVCCService {
	return &mockMVCCService{
		mockKVService: mockKVService{store: make(map[string][]byte)},
	}
}

func (m *mockMVCCService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	if m.putWithLeaseFn != nil {
		return m.putWithLeaseFn(ctx, key, value, leaseID)
	}
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, CreateRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return &kv.KeyValue{Key: []byte(key), Value: []byte("val"), ModRevision: 5, CreateRevision: 1, Version: 3}, nil
}

func (m *mockMVCCService) GetAtRevision(ctx context.Context, key string, revision int64) (*kv.KeyValue, error) {
	if m.getAtRevisionFn != nil {
		return m.getAtRevisionFn(ctx, key, revision)
	}
	return &kv.KeyValue{Key: []byte(key), Value: []byte("old"), ModRevision: revision, CreateRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	kvs := []*kv.KeyValue{
		{Key: start, Value: []byte("v1"), ModRevision: 1},
	}
	return kvs, 1, nil
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
	return 42, nil
}

func (m *mockMVCCService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *mockMVCCService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *mockMVCCService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *mockMVCCService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (m *mockMVCCService) PutWithLeaseAndMode(_ context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: value, LeaseID: leaseID, ModRevision: 1, CreateRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) GetRevisionWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("val"), ModRevision: 5, CreateRevision: 1, Version: 3}, nil
}

func (m *mockMVCCService) GetAtRevisionWithConsistency(_ context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return &kv.KeyValue{Key: []byte(key), Value: []byte("old"), ModRevision: revision, CreateRevision: 1, Version: 1}, nil
}

func (m *mockMVCCService) RangeWithConsistency(_ context.Context, start, end []byte, _ consistency.ReadOptions, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
	kvs := []*kv.KeyValue{
		{Key: start, Value: []byte("v1"), ModRevision: 1},
	}
	return kvs, 1, nil
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
	return &service.TransactionResponse{Succeeded: true, Revision: 10, Responses: []service.OpResponse{{Succeeded: true}}}, nil
}

func (m *mockTransactionService) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return m.Execute(ctx, compares, successOps, failureOps)
}

func (m *mockTransactionService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: BatchServicer
// ---------------------------------------------------------------------------

type mockBatchService struct {
	executeFn func(ctx context.Context, ops []service.Op) ([]service.OpResponse, error)
}

func (m *mockBatchService) Execute(ctx context.Context, ops []service.Op) ([]service.OpResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, ops)
	}
	resp := make([]service.OpResponse, len(ops))
	for i := range ops {
		resp[i] = service.OpResponse{Succeeded: true}
	}
	return resp, nil
}

func (m *mockBatchService) ExecuteWithMode(ctx context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return m.Execute(ctx, ops)
}

func (m *mockBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, nil
}

func (m *mockBatchService) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
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
	return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
}

func (m *mockWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if m.watchPrefixFn != nil {
		return m.watchPrefixFn(ctx, prefix, opts...)
	}
	return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
}

func (m *mockWatchService) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (m *mockWatchService) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: Watcher
// ---------------------------------------------------------------------------

type mockWatcher struct {
	eventsCh chan watch.WatchResponse
	watchID  int64
	closeErr error
	watchErr error
}

func (m *mockWatcher) Events() <-chan watch.WatchResponse { return m.eventsCh }
func (m *mockWatcher) WatchID() int64                     { return m.watchID }
func (m *mockWatcher) Close() error                       { return m.closeErr }
func (m *mockWatcher) Err() error                         { return m.watchErr }

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
	return &lease.Lease{ID: 100, TTL: ttl, GrantedTTL: ttl}, nil
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
	return &lease.Lease{ID: leaseID, TTL: 60, GrantedTTL: 60}, nil
}

func (m *mockLeaseService) TimeToLive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	if m.timeToLiveFn != nil {
		return m.timeToLiveFn(ctx, leaseID)
	}
	return &lease.Lease{ID: leaseID, TTL: 60, GrantedTTL: 60, RemainingTTL: 45}, nil
}

func (m *mockLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return []*lease.Lease{{ID: 1, TTL: 60}, {ID: 2, TTL: 120}}, nil
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
	return [][]byte{[]byte("1"), []byte("2"), []byte("3")}, nil
}

func (m *mockIndexService) Scan(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, indexName, start, end)
	}
	return map[string][][]byte{"a": {[]byte("1")}, "b": {[]byte("2")}}, nil
}

func (m *mockIndexService) ListIndexes(ctx context.Context) ([]string, error) {
	if m.listIndexesFn != nil {
		return m.listIndexesFn(ctx)
	}
	return []string{}, nil
}

// ---------------------------------------------------------------------------
// Mock: TimeSeriesServicer
// ---------------------------------------------------------------------------

type mockTimeSeriesService struct {
	writeFn              func(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error
	writeBatchFn         func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn              func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn          func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	writePointFn         func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn          func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	deleteSeriesFn       func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetentionPolicyFn func(metric string) (*timeseries.RetentionPolicy, error)
	setRetentionPolicyFn func(metric string, policy *timeseries.RetentionPolicy) error
	listMetricsFn        func(prefix string, limit int) ([]string, error)
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
	return []timeseries.DataPoint{
		{Metric: metric, Value: 42.0, Timestamp: start},
	}, nil
}

func (m *mockTimeSeriesService) Aggregate(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateFn != nil {
		return m.aggregateFn(ctx, metric, start, end, fn, interval)
	}
	return []timeseries.AggResult{
		{Timestamp: start, Value: 42.0, Count: 10},
	}, nil
}

func (m *mockTimeSeriesService) WritePoint(ctx context.Context, point *timeseries.DataPoint) error {
	if m.writePointFn != nil {
		return m.writePointFn(ctx, point)
	}
	return nil
}

func (m *mockTimeSeriesService) QueryRich(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	if m.queryRichFn != nil {
		return m.queryRichFn(ctx, req)
	}
	return &timeseries.QueryResult{}, nil
}

func (m *mockTimeSeriesService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteSeriesFn != nil {
		return m.deleteSeriesFn(ctx, req)
	}
	return nil
}

func (m *mockTimeSeriesService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetentionPolicyFn != nil {
		return m.getRetentionPolicyFn(metric)
	}
	return &timeseries.RetentionPolicy{}, nil
}

func (m *mockTimeSeriesService) SetRetentionPolicy(metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetentionPolicyFn != nil {
		return m.setRetentionPolicyFn(metric, policy)
	}
	return nil
}

func (m *mockTimeSeriesService) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return []string{}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func makeReq(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}

// ---------------------------------------------------------------------------
// MVCC tool tests
// ---------------------------------------------------------------------------

func TestHandleMVCCPutWithLease_Success(t *testing.T) {
	svc := newMockMVCCService()
	tools := mvccTools(svc)
	var handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_put_with_lease" {
			handler = tool.Handler
			break
		}
	}
	if handler == nil {
		t.Fatal("tool qrdb_mvcc_put_with_lease not found")
	}

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k1",
		"value":    "v1",
		"lease_id": float64(100),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var kvResult kv.KeyValue
	if jsonErr := json.Unmarshal([]byte(text), &kvResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if string(kvResult.Key) != "k1" {
		t.Errorf("expected key 'k1', got %q", string(kvResult.Key))
	}
	if kvResult.LeaseID != 100 {
		t.Errorf("expected leaseID=100, got %d", kvResult.LeaseID)
	}
}

func TestHandleMVCCPutWithLease_MissingKey(t *testing.T) {
	svc := newMockMVCCService()
	tools := mvccTools(svc)
	var handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_put_with_lease" {
			handler = tool.Handler
			break
		}
	}

	result, err := handler(context.Background(), makeReq(map[string]any{
		"value":    "v1",
		"lease_id": float64(100),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandleMVCCPutWithLease_MissingValue(t *testing.T) {
	svc := newMockMVCCService()
	tools := mvccTools(svc)
	var handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_put_with_lease" {
			handler = tool.Handler
			break
		}
	}

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k1",
		"lease_id": float64(100),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing value")
	}
}

func TestHandleMVCCPutWithLease_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.putWithLeaseFn = func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
		return nil, &service.ProposeError{Err: errors.New("raft timeout")}
	}
	tools := mvccTools(svc)
	var handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_put_with_lease" {
			handler = tool.Handler
			break
		}
	}

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k1",
		"value":    "v1",
		"lease_id": float64(100),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleMVCCGetRevision_Success(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCGetRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{"key": "k1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var kvResult kv.KeyValue
	if jsonErr := json.Unmarshal([]byte(text), &kvResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if string(kvResult.Key) != "k1" {
		t.Errorf("expected key 'k1', got %q", string(kvResult.Key))
	}
}

func TestHandleMVCCGetRevision_MissingKey(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCGetRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandleMVCCGetRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.getRevisionFn = func(_ context.Context, _ string) (*kv.KeyValue, error) {
		return nil, &service.KeyNotFoundError{Key: "k1"}
	}
	handler := handleMVCCGetRevision(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{"key": "k1"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleMVCCGetAtRevision_Success(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCGetAtRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k1",
		"revision": float64(5),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var kvResult kv.KeyValue
	if jsonErr := json.Unmarshal([]byte(text), &kvResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if string(kvResult.Key) != "k1" {
		t.Errorf("expected key 'k1', got %q", string(kvResult.Key))
	}
}

func TestHandleMVCCGetAtRevision_MissingKey(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCGetAtRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandleMVCCGetAtRevision_MissingRevision(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCGetAtRevision(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key": "k1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing revision")
	}
}

func TestHandleMVCCGetAtRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.getAtRevisionFn = func(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
		return nil, &service.RevisionCompactedError{Requested: 5, Compacted: 10}
	}
	handler := handleMVCCGetAtRevision(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k1",
		"revision": float64(5),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleMVCCRange_Success(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCRange(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start": "a",
		"end":   "z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp mvccRangeResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Count != 1 {
		t.Errorf("expected count=1, got %d", resp.Count)
	}
}

func TestHandleMVCCRange_WithOptions(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCRange(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start":      "a",
		"end":        "z",
		"limit":      float64(10),
		"revision":   float64(5),
		"count_only": true,
		"keys_only":  true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleMVCCRange_MissingStart(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCRange(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"end": "z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing start")
	}
}

func TestHandleMVCCRange_MissingEnd(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCRange(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start": "a",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing end")
	}
}

func TestHandleMVCCRange_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.rangeFn = func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
		return nil, 0, &service.ReadError{Err: errors.New("read timeout")}
	}
	handler := handleMVCCRange(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"start": "a",
		"end":   "z",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleMVCCCompact_Success(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCCompact(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleMVCCCompact_MissingRevision(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCCompact(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing revision")
	}
}

func TestHandleMVCCCompact_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.compactFn = func(_ context.Context, _ int64) error {
		return &service.ProposeError{Err: errors.New("raft timeout")}
	}
	handler := handleMVCCCompact(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(10),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleMVCCCurrentRevision_Success(t *testing.T) {
	svc := newMockMVCCService()
	handler := handleMVCCCurrentRevision(svc)

	result, err := handler(context.Background(), makeReq(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp map[string]int64
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp["revision"] != 42 {
		t.Errorf("expected revision=42, got %d", resp["revision"])
	}
}

func TestHandleMVCCCurrentRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.currentRevisionFn = func(_ context.Context) (int64, error) {
		return 0, &service.ReadError{Err: errors.New("read timeout")}
	}
	handler := handleMVCCCurrentRevision(svc)

	_, err := handler(context.Background(), makeReq(nil))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Transaction tool tests
// ---------------------------------------------------------------------------

func TestHandleTxExecute_Success(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	compares := `[{"key":"k1","target":"version","result":"equal","version":1}]`
	success := `[{"type":"put","key":"k1","value":"djI="}]`

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": compares,
		"success":  success,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp service.TransactionResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !resp.Succeeded {
		t.Error("expected succeeded=true")
	}
}

func TestHandleTxExecute_SucceededFalse(t *testing.T) {
	svc := &mockTransactionService{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return &service.TransactionResponse{Succeeded: false, Revision: 10}, nil
		},
	}
	handler := handleTxExecute(svc)

	compares := `[{"key":"k1","target":"version","result":"equal","version":1}]`
	success := `[{"type":"put","key":"k1","value":"djI="}]`

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": compares,
		"success":  success,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp service.TransactionResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Succeeded {
		t.Error("expected succeeded=false")
	}
}

func TestHandleTxExecute_MissingCompares(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"success": `[{"type":"put","key":"k1"}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing compares")
	}
}

func TestHandleTxExecute_MissingSuccess(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"version","result":"equal","version":1}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing success")
	}
}

func TestHandleTxExecute_BadComparesJSON(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": "not-json",
		"success":  `[{"type":"put","key":"k1"}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for bad JSON")
	}
}

func TestHandleTxExecute_InvalidCompareTarget(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"invalid","result":"equal"}]`,
		"success":  `[{"type":"put","key":"k1"}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid compare target")
	}
}

func TestHandleTxExecute_InvalidCompareResult(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"version","result":"bogus"}]`,
		"success":  `[{"type":"put","key":"k1"}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid compare result")
	}
}

func TestHandleTxExecute_BadSuccessJSON(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"version","result":"equal","version":1}]`,
		"success":  "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for bad success JSON")
	}
}

func TestHandleTxExecute_BadFailureJSON(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"version","result":"equal","version":1}]`,
		"success":  `[{"type":"put","key":"k1"}]`,
		"failure":  "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for bad failure JSON")
	}
}

func TestHandleTxExecute_ServiceError(t *testing.T) {
	svc := &mockTransactionService{
		executeFn: func(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
			return nil, &service.ProposeError{Err: errors.New("raft timeout")}
		},
	}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"version","result":"equal","version":1}]`,
		"success":  `[{"type":"put","key":"k1"}]`,
	}))
	if result != nil {
		t.Error("expected nil result on service error")
	}
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleTxExecute_AllCompareTargets(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	targets := []string{"version", "create_revision", "mod_revision", "value"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			compares := `[{"key":"k1","target":"` + target + `","result":"equal","version":1}]`
			result, err := handler(context.Background(), makeReq(map[string]any{
				"compares": compares,
				"success":  `[{"type":"put","key":"k1"}]`,
			}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IsError {
				t.Errorf("unexpected error result for target %s", target)
			}
		})
	}
}

func TestHandleTxExecute_AllCompareResults(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	results := []string{"equal", "greater", "less", "not_equal"}
	for _, r := range results {
		t.Run(r, func(t *testing.T) {
			compares := `[{"key":"k1","target":"version","result":"` + r + `","version":1}]`
			result, err := handler(context.Background(), makeReq(map[string]any{
				"compares": compares,
				"success":  `[{"type":"put","key":"k1"}]`,
			}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IsError {
				t.Errorf("unexpected error result for result %s", r)
			}
		})
	}
}

func TestParseOps_ValidOps(t *testing.T) {
	opsStr := `[{"type":"put","key":"k1","value":"djE="},{"type":"delete","key":"k2"},{"type":"get","key":"k3"},{"type":"range","key":"a","range_end":"eg=="}]`
	ops, err := parseOps(opsStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 4 {
		t.Fatalf("expected 4 ops, got %d", len(ops))
	}
	if ops[0].Type != service.OpPut {
		t.Errorf("expected OpPut, got %d", ops[0].Type)
	}
	if ops[1].Type != service.OpDelete {
		t.Errorf("expected OpDelete, got %d", ops[1].Type)
	}
	if ops[2].Type != service.OpGet {
		t.Errorf("expected OpGet, got %d", ops[2].Type)
	}
	if ops[3].Type != service.OpRange {
		t.Errorf("expected OpRange, got %d", ops[3].Type)
	}
}

func TestParseOps_InvalidJSON(t *testing.T) {
	_, err := parseOps("not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseOps_EmptyArray(t *testing.T) {
	ops, err := parseOps("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops, got %d", len(ops))
	}
}

func TestParseOps_UnknownType(t *testing.T) {
	ops, err := parseOps(`[{"type":"unknown","key":"k1"}]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unknown type defaults to OpPut.
	if ops[0].Type != service.OpPut {
		t.Errorf("expected default OpPut for unknown type, got %d", ops[0].Type)
	}
}

func TestHandleTxExecute_WithFailureOps(t *testing.T) {
	svc := &mockTransactionService{}
	handler := handleTxExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"compares": `[{"key":"k1","target":"version","result":"equal","version":1}]`,
		"success":  `[{"type":"put","key":"k1"}]`,
		"failure":  `[{"type":"delete","key":"k1"}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
}

// ---------------------------------------------------------------------------
// Batch tool tests
// ---------------------------------------------------------------------------

func TestHandleBatchExecute_Success(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchExecute(svc)

	ops := `[{"type":"put","key":"k1","value":"djE="},{"type":"delete","key":"k2"}]`
	result, err := handler(context.Background(), makeReq(map[string]any{
		"ops": ops,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var responses []service.OpResponse
	if jsonErr := json.Unmarshal([]byte(text), &responses); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(responses) != 2 {
		t.Errorf("expected 2 responses, got %d", len(responses))
	}
}

func TestHandleBatchExecute_MissingOps(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing ops")
	}
}

func TestHandleBatchExecute_BadJSON(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"ops": "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for bad JSON")
	}
}

func TestHandleBatchExecute_EmptyOps(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchExecute(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"ops": "[]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty ops")
	}
}

func TestHandleBatchExecute_ServiceError(t *testing.T) {
	svc := &mockBatchService{
		executeFn: func(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
			return nil, &service.ProposeError{Err: errors.New("raft timeout")}
		},
	}
	handler := handleBatchExecute(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"ops": `[{"type":"put","key":"k1"}]`,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Watch tool tests
// ---------------------------------------------------------------------------

func TestHandleWatch_SuccessWithEvents(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  42,
		Revision: 10,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("k1"), Value: []byte("v1")},
			},
		},
	}
	close(ch)

	watcher := &mockWatcher{eventsCh: ch, watchID: 42}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"timeout_seconds": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(wr.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(wr.Events))
	}
	if wr.Events[0].Type != "PUT" {
		t.Errorf("expected PUT event, got %q", wr.Events[0].Type)
	}
	if wr.WatchID != 42 {
		t.Errorf("expected watchID=42, got %d", wr.WatchID)
	}
	if wr.Revision != 10 {
		t.Errorf("expected revision=10, got %d", wr.Revision)
	}
}

func TestHandleWatch_NoEvents(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	watcher := &mockWatcher{eventsCh: ch, watchID: 1}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"timeout_seconds": float64(0.1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(wr.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(wr.Events))
	}
}

func TestHandleWatch_MissingKey(t *testing.T) {
	svc := &mockWatchService{}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandleWatch_ServiceError(t *testing.T) {
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, &service.WatchCanceledError{Reason: "test"}
		},
	}
	handler := handleWatch(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key": "k1",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleWatch_PrefixMode(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	watcher := &mockWatcher{eventsCh: ch, watchID: 5}
	svc := &mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "prefix/",
		"prefix":          true,
		"timeout_seconds": float64(0.1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wr.WatchID != 5 {
		t.Errorf("expected watchID=5, got %d", wr.WatchID)
	}
}

func TestHandleWatch_WithStartRevisionAndPrevKV(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  1,
		Revision: 10,
		Events: []*watch.Event{
			{
				Type:   watch.EventPut,
				Kv:     &kv.KeyValue{Key: []byte("k1"), Value: []byte("new")},
				PrevKv: &kv.KeyValue{Key: []byte("k1"), Value: []byte("old")},
			},
		},
	}
	close(ch)

	watcher := &mockWatcher{eventsCh: ch, watchID: 1}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"start_revision":  float64(5),
		"prev_kv":         true,
		"timeout_seconds": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(wr.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(wr.Events))
	}
	if wr.Events[0].PrevKv == nil {
		t.Error("expected prev_kv in event")
	}
}

func TestHandleWatch_DeleteEvent(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  1,
		Revision: 10,
		Events: []*watch.Event{
			{
				Type: watch.EventDelete,
				Kv:   &kv.KeyValue{Key: []byte("k1")},
			},
		},
	}
	close(ch)

	watcher := &mockWatcher{eventsCh: ch, watchID: 1}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"timeout_seconds": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wr.Events[0].Type != "DELETE" {
		t.Errorf("expected DELETE event, got %q", wr.Events[0].Type)
	}
}

func TestHandleWatch_NilEventSkipped(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  1,
		Revision: 10,
		Events: []*watch.Event{
			nil,
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("k1"), Value: []byte("v1")},
			},
		},
	}
	close(ch)

	watcher := &mockWatcher{eventsCh: ch, watchID: 1}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"timeout_seconds": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(wr.Events) != 1 {
		t.Errorf("expected 1 event (nil skipped), got %d", len(wr.Events))
	}
}

func TestWatchEventTypeName_Unknown(t *testing.T) {
	name := watchEventTypeName(watch.EventType(99))
	if name != "UNKNOWN" {
		t.Errorf("expected UNKNOWN, got %q", name)
	}
}

func TestWatchEventTypeName_Put(t *testing.T) {
	name := watchEventTypeName(watch.EventPut)
	if name != "PUT" {
		t.Errorf("expected PUT, got %q", name)
	}
}

func TestWatchEventTypeName_Delete(t *testing.T) {
	name := watchEventTypeName(watch.EventDelete)
	if name != "DELETE" {
		t.Errorf("expected DELETE, got %q", name)
	}
}

// ---------------------------------------------------------------------------
// Lease tool tests
// ---------------------------------------------------------------------------

func TestHandleLeaseGrant_Success(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseGrant(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"ttl": float64(60),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var l lease.Lease
	if jsonErr := json.Unmarshal([]byte(text), &l); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if l.ID != 100 {
		t.Errorf("expected lease ID=100, got %d", l.ID)
	}
	if l.TTL != 60 {
		t.Errorf("expected TTL=60, got %d", l.TTL)
	}
}

func TestHandleLeaseGrant_MissingTTL(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseGrant(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing ttl")
	}
}

func TestHandleLeaseGrant_ZeroTTL(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseGrant(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"ttl": float64(0),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for zero ttl")
	}
}

func TestHandleLeaseGrant_NegativeTTL(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseGrant(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"ttl": float64(-5),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for negative ttl")
	}
}

func TestHandleLeaseGrant_ServiceError(t *testing.T) {
	svc := &mockLeaseService{
		grantFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, &service.ProposeError{Err: errors.New("raft timeout")}
		},
	}
	handler := handleLeaseGrant(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"ttl": float64(60),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleLeaseRevoke_Success(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseRevoke(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"lease_id": float64(100),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleLeaseRevoke_MissingLeaseID(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseRevoke(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing lease_id")
	}
}

func TestHandleLeaseRevoke_ServiceError(t *testing.T) {
	svc := &mockLeaseService{
		revokeFn: func(_ context.Context, _ int64) error {
			return &service.LeaseNotFoundError{LeaseID: 100}
		},
	}
	handler := handleLeaseRevoke(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"lease_id": float64(100),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleLeaseKeepAlive_Success(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseKeepAlive(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"lease_id": float64(100),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var l lease.Lease
	if jsonErr := json.Unmarshal([]byte(text), &l); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if l.ID != 100 {
		t.Errorf("expected lease ID=100, got %d", l.ID)
	}
}

func TestHandleLeaseKeepAlive_MissingLeaseID(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseKeepAlive(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing lease_id")
	}
}

func TestHandleLeaseKeepAlive_ServiceError(t *testing.T) {
	svc := &mockLeaseService{
		keepAliveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, &service.LeaseNotFoundError{LeaseID: 100}
		},
	}
	handler := handleLeaseKeepAlive(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"lease_id": float64(100),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleLeaseTTL_Success(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseTTL(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"lease_id": float64(100),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var l lease.Lease
	if jsonErr := json.Unmarshal([]byte(text), &l); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if l.RemainingTTL != 45 {
		t.Errorf("expected remainingTTL=45, got %d", l.RemainingTTL)
	}
}

func TestHandleLeaseTTL_MissingLeaseID(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseTTL(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing lease_id")
	}
}

func TestHandleLeaseTTL_ServiceError(t *testing.T) {
	svc := &mockLeaseService{
		timeToLiveFn: func(_ context.Context, _ int64) (*lease.Lease, error) {
			return nil, &service.LeaseNotFoundError{LeaseID: 100}
		},
	}
	handler := handleLeaseTTL(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"lease_id": float64(100),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleLeaseList_Success(t *testing.T) {
	svc := &mockLeaseService{}
	handler := handleLeaseList(svc)

	result, err := handler(context.Background(), makeReq(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var leases []*lease.Lease
	if jsonErr := json.Unmarshal([]byte(text), &leases); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(leases) != 2 {
		t.Errorf("expected 2 leases, got %d", len(leases))
	}
}

func TestHandleLeaseList_Empty(t *testing.T) {
	svc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return []*lease.Lease{}, nil
		},
	}
	handler := handleLeaseList(svc)

	result, err := handler(context.Background(), makeReq(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var leases []*lease.Lease
	if jsonErr := json.Unmarshal([]byte(text), &leases); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(leases) != 0 {
		t.Errorf("expected 0 leases, got %d", len(leases))
	}
}

func TestHandleLeaseList_ServiceError(t *testing.T) {
	svc := &mockLeaseService{
		leasesFn: func(_ context.Context) ([]*lease.Lease, error) {
			return nil, &service.ReadError{Err: errors.New("read timeout")}
		},
	}
	handler := handleLeaseList(svc)

	_, err := handler(context.Background(), makeReq(nil))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Index tool tests
// ---------------------------------------------------------------------------

func TestHandleIndexCreate_Success(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name":       "idx_email",
		"field_name": "email",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleIndexCreate_WithOptions(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name":       "idx_email",
		"field_name": "email",
		"unique":     true,
		"sparse":     true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleIndexCreate_MissingName(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"field_name": "email",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleIndexCreate_MissingFieldName(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexCreate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name": "idx_email",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing field_name")
	}
}

func TestHandleIndexCreate_ServiceError(t *testing.T) {
	svc := &mockIndexService{
		createIndexFn: func(_ context.Context, _, _ string, _ index.IndexConfig) error {
			return &service.IndexExistsError{Name: "idx_email"}
		},
	}
	handler := handleIndexCreate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"name":       "idx_email",
		"field_name": "email",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleIndexDrop_Success(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexDrop(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"name": "idx_email",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleIndexDrop_MissingName(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexDrop(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleIndexDrop_ServiceError(t *testing.T) {
	svc := &mockIndexService{
		dropIndexFn: func(_ context.Context, _ string) error {
			return &service.IndexNotFoundError{Name: "idx_email"}
		},
	}
	handler := handleIndexDrop(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"name": "idx_email",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleIndexQuery_Success(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
		"value":      "user@example.com",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var keys []string
	if jsonErr := json.Unmarshal([]byte(text), &keys); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestHandleIndexQuery_MissingIndexName(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"value": "user@example.com",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing index_name")
	}
}

func TestHandleIndexQuery_MissingValue(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing value")
	}
}

func TestHandleIndexQuery_ServiceError(t *testing.T) {
	svc := &mockIndexService{
		queryFn: func(_ context.Context, _ string, _ []byte) ([][]byte, error) {
			return nil, &service.IndexNotFoundError{Name: "idx_email"}
		},
	}
	handler := handleIndexQuery(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
		"value":      "user@example.com",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleIndexScan_Success(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexScan(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
		"start":      "a",
		"end":        "z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var entries map[string][]string
	if jsonErr := json.Unmarshal([]byte(text), &entries); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestHandleIndexScan_MissingIndexName(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexScan(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start": "a",
		"end":   "z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing index_name")
	}
}

func TestHandleIndexScan_MissingStart(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexScan(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
		"end":        "z",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing start")
	}
}

func TestHandleIndexScan_MissingEnd(t *testing.T) {
	svc := &mockIndexService{}
	handler := handleIndexScan(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
		"start":      "a",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing end")
	}
}

func TestHandleIndexScan_ServiceError(t *testing.T) {
	svc := &mockIndexService{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return nil, &service.IndexNotFoundError{Name: "idx_email"}
		},
	}
	handler := handleIndexScan(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"index_name": "idx_email",
		"start":      "a",
		"end":        "z",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TimeSeries tool tests
// ---------------------------------------------------------------------------

func TestHandleTSWrite_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWrite(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(42.5),
		"timestamp": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleTSWrite_WithTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWrite(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(42.5),
		"timestamp": float64(1000000000),
		"tags":      `{"host":"web-01","region":"us-east"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleTSWrite_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWrite(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"value":     float64(42.5),
		"timestamp": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing metric")
	}
}

func TestHandleTSWrite_MissingTimestamp(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWrite(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"value":  float64(42.5),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing timestamp")
	}
}

func TestHandleTSWrite_InvalidTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWrite(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(42.5),
		"timestamp": float64(1000000000),
		"tags":      "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid tags")
	}
}

func TestHandleTSWrite_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		writeFn: func(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
			return &service.TimeSeriesError{Op: "write", Err: errors.New("raft timeout")}
		},
	}
	handler := handleTSWrite(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(42.5),
		"timestamp": float64(1000000000),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleTSWriteBatch_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWriteBatch(svc)

	points := `[{"metric":"cpu.usage","value":42.5,"timestamp":1000000000},{"metric":"mem.usage","value":75.0,"timestamp":1000000000}]`
	result, err := handler(context.Background(), makeReq(map[string]any{
		"points": points,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleTSWriteBatch_MissingPoints(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWriteBatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing points")
	}
}

func TestHandleTSWriteBatch_BadJSON(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWriteBatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"points": "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for bad JSON")
	}
}

func TestHandleTSWriteBatch_EmptyPoints(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWriteBatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"points": "[]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty points")
	}
}

func TestHandleTSWriteBatch_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		writeBatchFn: func(_ context.Context, _ []timeseries.DataPoint) error {
			return &service.TimeSeriesError{Op: "write_batch", Err: errors.New("raft timeout")}
		},
	}
	handler := handleTSWriteBatch(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"points": `[{"metric":"cpu","value":1.0,"timestamp":1000000000}]`,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleTSQuery_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var points []timeseries.DataPoint
	if jsonErr := json.Unmarshal([]byte(text), &points); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(points) != 1 {
		t.Errorf("expected 1 point, got %d", len(points))
	}
}

func TestHandleTSQuery_WithAllOptions(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":     "cpu.usage",
		"start":      float64(1000000000),
		"end":        float64(2000000000),
		"tags":       `{"host":"web-01"}`,
		"limit":      float64(10),
		"descending": true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
}

func TestHandleTSQuery_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start": float64(1000000000),
		"end":   float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing metric")
	}
}

func TestHandleTSQuery_MissingStart(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"end":    float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing start")
	}
}

func TestHandleTSQuery_MissingEnd(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing end")
	}
}

func TestHandleTSQuery_InvalidTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
		"tags":   "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid tags")
	}
}

func TestHandleTSQuery_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		queryFn: func(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
			return nil, &service.TimeSeriesError{Op: "query", Err: errors.New("timeout")}
		},
	}
	handler := handleTSQuery(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleTSAggregate_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"func":        "avg",
		"interval_ns": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var results []timeseries.AggResult
	if jsonErr := json.Unmarshal([]byte(text), &results); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestHandleTSAggregate_AllFunctions(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	funcs := []string{"avg", "sum", "min", "max", "count", "percentile"}
	for _, fn := range funcs {
		t.Run(fn, func(t *testing.T) {
			result, err := handler(context.Background(), makeReq(map[string]any{
				"metric":      "cpu.usage",
				"start":       float64(1000000000),
				"end":         float64(2000000000),
				"func":        fn,
				"interval_ns": float64(1000000000),
			}))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IsError {
				t.Errorf("unexpected error result for func %s", fn)
			}
		})
	}
}

func TestHandleTSAggregate_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"func":        "avg",
		"interval_ns": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing metric")
	}
}

func TestHandleTSAggregate_MissingStart(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"end":         float64(2000000000),
		"func":        "avg",
		"interval_ns": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing start")
	}
}

func TestHandleTSAggregate_MissingEnd(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"func":        "avg",
		"interval_ns": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing end")
	}
}

func TestHandleTSAggregate_MissingFunc(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"interval_ns": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing func")
	}
}

func TestHandleTSAggregate_InvalidFunc(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"func":        "invalid",
		"interval_ns": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid func")
	}
}

func TestHandleTSAggregate_MissingIntervalNs(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSAggregate(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
		"func":   "avg",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing interval_ns")
	}
}

func TestHandleTSAggregate_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		aggregateFn: func(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
			return nil, &service.TimeSeriesError{Op: "aggregate", Err: errors.New("timeout")}
		},
	}
	handler := handleTSAggregate(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"func":        "avg",
		"interval_ns": float64(1000000000),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestParseTags_Empty(t *testing.T) {
	req := makeReq(map[string]any{})
	tags, err := parseTags(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Errorf("expected nil tags, got %v", tags)
	}
}

func TestParseTags_Valid(t *testing.T) {
	req := makeReq(map[string]any{
		"tags": `{"host":"web-01"}`,
	})
	tags, err := parseTags(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags["host"] != "web-01" {
		t.Errorf("expected host=web-01, got %q", tags["host"])
	}
}

func TestParseTags_Invalid(t *testing.T) {
	req := makeReq(map[string]any{
		"tags": "not-json",
	})
	_, err := parseTags(req)
	if err == nil {
		t.Fatal("expected error for invalid tags JSON")
	}
}

// ---------------------------------------------------------------------------
// Server option tests
// ---------------------------------------------------------------------------

func TestWithMVCC_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	mvccSvc := newMockMVCCService()

	s := NewServer(kvSvc, adminSvc, ":0", WithMVCC(mvccSvc))
	tools := s.MCPServer().ListTools()

	mvccToolNames := []string{
		"qrdb_mvcc_put_with_lease",
		"qrdb_mvcc_get_revision",
		"qrdb_mvcc_get_at_revision",
		"qrdb_mvcc_range",
		"qrdb_mvcc_compact",
		"qrdb_mvcc_current_revision",
	}
	for _, name := range mvccToolNames {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestWithMVCC_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_mvcc_put_with_lease"]; ok {
		t.Error("mvcc tools should not be registered when service is nil")
	}
}

func TestWithTransaction_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	txSvc := &mockTransactionService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithTransaction(txSvc))
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_tx_execute"]; !ok {
		t.Error("tool qrdb_tx_execute not registered")
	}
}

func TestWithTransaction_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_tx_execute"]; ok {
		t.Error("transaction tools should not be registered when service is nil")
	}
}

func TestWithBatch_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	batchSvc := &mockBatchService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithBatch(batchSvc))
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_batch_execute"]; !ok {
		t.Error("tool qrdb_batch_execute not registered")
	}
}

func TestWithBatch_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_batch_execute"]; ok {
		t.Error("batch tools should not be registered when service is nil")
	}
}

func TestWithWatch_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	watchSvc := &mockWatchService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithWatch(watchSvc))
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_watch"]; !ok {
		t.Error("tool qrdb_watch not registered")
	}
}

func TestWithWatch_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_watch"]; ok {
		t.Error("watch tools should not be registered when service is nil")
	}
}

func TestWithLease_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	leaseSvc := &mockLeaseService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithLease(leaseSvc))
	tools := s.MCPServer().ListTools()

	leaseToolNames := []string{
		"qrdb_lease_grant",
		"qrdb_lease_revoke",
		"qrdb_lease_keepalive",
		"qrdb_lease_ttl",
		"qrdb_lease_list",
	}
	for _, name := range leaseToolNames {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestWithLease_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_lease_grant"]; ok {
		t.Error("lease tools should not be registered when service is nil")
	}
}

func TestWithIndex_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	indexSvc := &mockIndexService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithIndex(indexSvc))
	tools := s.MCPServer().ListTools()

	indexToolNames := []string{
		"qrdb_index_create",
		"qrdb_index_drop",
		"qrdb_index_query",
		"qrdb_index_scan",
	}
	for _, name := range indexToolNames {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestWithIndex_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_index_create"]; ok {
		t.Error("index tools should not be registered when service is nil")
	}
}

func TestWithTimeSeries_ToolsRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tsSvc := &mockTimeSeriesService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithTimeSeries(tsSvc))
	tools := s.MCPServer().ListTools()

	tsToolNames := []string{
		"qrdb_ts_write",
		"qrdb_ts_write_batch",
		"qrdb_ts_query",
		"qrdb_ts_aggregate",
	}
	for _, name := range tsToolNames {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestWithTimeSeries_NilNotRegistered(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	tools := s.MCPServer().ListTools()

	if _, ok := tools["qrdb_ts_write"]; ok {
		t.Error("timeseries tools should not be registered when service is nil")
	}
}

func TestNewServer_AllOptions(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	mvccSvc := newMockMVCCService()
	txSvc := &mockTransactionService{}
	batchSvc := &mockBatchService{}
	watchSvc := &mockWatchService{}
	leaseSvc := &mockLeaseService{}
	indexSvc := &mockIndexService{}
	tsSvc := &mockTimeSeriesService{}

	s := NewServer(kvSvc, adminSvc, ":0",
		WithMVCC(mvccSvc),
		WithTransaction(txSvc),
		WithBatch(batchSvc),
		WithWatch(watchSvc),
		WithLease(leaseSvc),
		WithIndex(indexSvc),
		WithTimeSeries(tsSvc),
	)

	tools := s.MCPServer().ListTools()

	// 5 KV + 7 admin + 1 capabilities + 4 shard-admin + 6 MVCC + 1 TX + 1 batch + 1 watch + 5 lease + 5 index + 10 TS = 46
	expectedCount := 5 + 7 + 1 + 4 + 6 + 1 + 1 + 1 + 5 + 5 + 10
	if len(tools) != expectedCount {
		t.Errorf("expected %d tools, got %d", expectedCount, len(tools))
	}
}

// ---------------------------------------------------------------------------
// Tool function return tests
// ---------------------------------------------------------------------------

func TestMvccTools_ReturnsCorrectCount(t *testing.T) {
	svc := newMockMVCCService()
	tools := mvccTools(svc)
	if len(tools) != 6 {
		t.Errorf("expected 6 mvcc tools, got %d", len(tools))
	}
}

func TestTransactionTools_ReturnsCorrectCount(t *testing.T) {
	svc := &mockTransactionService{}
	tools := transactionTools(svc)
	if len(tools) != 1 {
		t.Errorf("expected 1 transaction tool, got %d", len(tools))
	}
}

func TestBatchTools_ReturnsCorrectCount(t *testing.T) {
	svc := &mockBatchService{}
	tools := batchTools(svc)
	if len(tools) != 1 {
		t.Errorf("expected 1 batch tool, got %d", len(tools))
	}
}

func TestWatchTools_ReturnsCorrectCount(t *testing.T) {
	svc := &mockWatchService{}
	tools := watchTools(svc)
	if len(tools) != 1 {
		t.Errorf("expected 1 watch tool, got %d", len(tools))
	}
}

func TestLeaseTools_ReturnsCorrectCount(t *testing.T) {
	svc := &mockLeaseService{}
	tools := leaseTools(svc)
	if len(tools) != 5 {
		t.Errorf("expected 5 lease tools, got %d", len(tools))
	}
}

func TestIndexTools_ReturnsCorrectCount(t *testing.T) {
	svc := &mockIndexService{}
	tools := indexTools(svc)
	if len(tools) != 5 {
		t.Errorf("expected 5 index tools, got %d", len(tools))
	}
}

func TestTimeSeriesTools_ReturnsCorrectCount(t *testing.T) {
	svc := &mockTimeSeriesService{}
	tools := timeSeriesTools(svc)
	if len(tools) != 10 {
		t.Errorf("expected 10 timeseries tools, got %d", len(tools))
	}
}

// ---------------------------------------------------------------------------
// Tool schema tests (cover tool*() functions)
// ---------------------------------------------------------------------------

func TestToolSchemas(t *testing.T) {
	schemas := []struct {
		name string
		tool mcp.Tool
	}{
		{"qrdb_mvcc_put_with_lease", toolMVCCPutWithLease()},
		{"qrdb_mvcc_get_revision", toolMVCCGetRevision()},
		{"qrdb_mvcc_get_at_revision", toolMVCCGetAtRevision()},
		{"qrdb_mvcc_range", toolMVCCRange()},
		{"qrdb_mvcc_compact", toolMVCCCompact()},
		{"qrdb_mvcc_current_revision", toolMVCCCurrentRevision()},
		{"qrdb_tx_execute", toolTxExecute()},
		{"qrdb_batch_execute", toolBatchExecute()},
		{"qrdb_watch", toolWatch()},
		{"qrdb_lease_grant", toolLeaseGrant()},
		{"qrdb_lease_revoke", toolLeaseRevoke()},
		{"qrdb_lease_keepalive", toolLeaseKeepAlive()},
		{"qrdb_lease_ttl", toolLeaseTTL()},
		{"qrdb_lease_list", toolLeaseList()},
		{"qrdb_index_create", toolIndexCreate()},
		{"qrdb_index_drop", toolIndexDrop()},
		{"qrdb_index_query", toolIndexQuery()},
		{"qrdb_index_scan", toolIndexScan()},
		{"qrdb_ts_write", toolTSWrite()},
		{"qrdb_ts_write_batch", toolTSWriteBatch()},
		{"qrdb_ts_query", toolTSQuery()},
		{"qrdb_ts_aggregate", toolTSAggregate()},
		{"qrdb_ts_delete_series", toolTSDeleteSeries()},
		{"qrdb_ts_get_retention_policy", toolTSGetRetentionPolicy()},
		{"qrdb_ts_set_retention_policy", toolTSSetRetentionPolicy()},
		{"qrdb_ts_list_metrics", toolTSListMetrics()},
		{"qrdb_ts_write_point", toolTSWritePoint()},
		{"qrdb_ts_rich_query", toolTSRichQuery()},
	}

	for _, s := range schemas {
		t.Run(s.name, func(t *testing.T) {
			if s.tool.Name != s.name {
				t.Errorf("expected tool name %q, got %q", s.name, s.tool.Name)
			}
			if s.tool.Description == "" {
				t.Errorf("tool %q has empty description", s.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Watch with event having nil Kv field
// ---------------------------------------------------------------------------

func TestHandleWatch_EventWithNilKv(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	ch <- watch.WatchResponse{
		WatchID:  1,
		Revision: 10,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				// Kv is nil
			},
		},
	}
	close(ch)

	watcher := &mockWatcher{eventsCh: ch, watchID: 1}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"timeout_seconds": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var wr watchResult
	if jsonErr := json.Unmarshal([]byte(text), &wr); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	// Event with nil Kv should still be included (only nil events are skipped).
	if len(wr.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(wr.Events))
	}
}

// ---------------------------------------------------------------------------
// Watch with prefix mode and service error
// ---------------------------------------------------------------------------

func TestHandleWatch_PrefixServiceError(t *testing.T) {
	svc := &mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, &service.WatchCanceledError{Reason: "test"}
		},
	}
	handler := handleWatch(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key":    "prefix/",
		"prefix": true,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TS write batch with tags
// ---------------------------------------------------------------------------

func TestHandleTSWriteBatch_WithTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWriteBatch(svc)

	points := `[{"metric":"cpu.usage","value":42.5,"timestamp":1000000000,"tags":{"host":"web-01"}}]`
	result, err := handler(context.Background(), makeReq(map[string]any{
		"points": points,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

// ---------------------------------------------------------------------------
// TS delete series handler tests
// ---------------------------------------------------------------------------

func TestHandleTSDeleteSeries_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSDeleteSeries(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleTSDeleteSeries_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSDeleteSeries(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing metric")
	}
}

func TestHandleTSDeleteSeries_WithTimeRange(t *testing.T) {
	var captured *timeseries.DeleteRequest
	svc := &mockTimeSeriesService{
		deleteSeriesFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			captured = req
			return nil
		},
	}
	handler := handleTSDeleteSeries(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
	if captured == nil {
		t.Fatal("expected delete request to be captured")
	}
	if captured.StartTime.UnixNano() != 1000000000 {
		t.Errorf("expected start 1000000000, got %d", captured.StartTime.UnixNano())
	}
	if captured.EndTime.UnixNano() != 2000000000 {
		t.Errorf("expected end 2000000000, got %d", captured.EndTime.UnixNano())
	}
}

func TestHandleTSDeleteSeries_WithTags(t *testing.T) {
	var captured *timeseries.DeleteRequest
	svc := &mockTimeSeriesService{
		deleteSeriesFn: func(_ context.Context, req *timeseries.DeleteRequest) error {
			captured = req
			return nil
		},
	}
	handler := handleTSDeleteSeries(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"tags":   `{"host":"web-01"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
	if captured == nil {
		t.Fatal("expected delete request to be captured")
	}
	if captured.Tags["host"] != "web-01" {
		t.Errorf("expected tag host=web-01, got %v", captured.Tags)
	}
}

func TestHandleTSDeleteSeries_InvalidTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSDeleteSeries(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"tags":   "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid tags")
	}
}

func TestHandleTSDeleteSeries_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		deleteSeriesFn: func(_ context.Context, _ *timeseries.DeleteRequest) error {
			return errors.New("storage failure")
		},
	}
	handler := handleTSDeleteSeries(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TS get retention policy handler tests
// ---------------------------------------------------------------------------

func TestHandleTSGetRetentionPolicy_Success(t *testing.T) {
	svc := &mockTimeSeriesService{
		getRetentionPolicyFn: func(metric string) (*timeseries.RetentionPolicy, error) {
			return &timeseries.RetentionPolicy{
				Duration:   24 * time.Hour,
				Resolution: time.Minute,
			}, nil
		},
	}
	handler := handleTSGetRetentionPolicy(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var policy timeseries.RetentionPolicy
	if jsonErr := json.Unmarshal([]byte(text), &policy); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if policy.Duration != 24*time.Hour {
		t.Errorf("expected duration 24h, got %v", policy.Duration)
	}
	if policy.Resolution != time.Minute {
		t.Errorf("expected resolution 1m, got %v", policy.Resolution)
	}
}

func TestHandleTSGetRetentionPolicy_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSGetRetentionPolicy(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing metric")
	}
}

func TestHandleTSGetRetentionPolicy_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		getRetentionPolicyFn: func(_ string) (*timeseries.RetentionPolicy, error) {
			return nil, errors.New("not found")
		},
	}
	handler := handleTSGetRetentionPolicy(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TS set retention policy handler tests
// ---------------------------------------------------------------------------

func TestHandleTSSetRetentionPolicy_Success(t *testing.T) {
	var capturedMetric string
	var capturedPolicy *timeseries.RetentionPolicy
	svc := &mockTimeSeriesService{
		setRetentionPolicyFn: func(metric string, policy *timeseries.RetentionPolicy) error {
			capturedMetric = metric
			capturedPolicy = policy
			return nil
		},
	}
	handler := handleTSSetRetentionPolicy(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":             "cpu.usage",
		"duration_seconds":   float64(86400),
		"resolution_seconds": float64(60),
		"aggregation":        "avg",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
	if capturedMetric != "cpu.usage" {
		t.Errorf("expected metric cpu.usage, got %q", capturedMetric)
	}
	if capturedPolicy == nil {
		t.Fatal("expected policy to be captured")
	}
	if capturedPolicy.Duration != 86400*time.Second {
		t.Errorf("expected duration 86400s, got %v", capturedPolicy.Duration)
	}
	if capturedPolicy.Resolution != 60*time.Second {
		t.Errorf("expected resolution 60s, got %v", capturedPolicy.Resolution)
	}
	if capturedPolicy.Aggregation != timeseries.AggAvg {
		t.Errorf("expected aggregation avg, got %v", capturedPolicy.Aggregation)
	}
}

func TestHandleTSSetRetentionPolicy_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSSetRetentionPolicy(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"duration_seconds": float64(86400),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing metric")
	}
}

func TestHandleTSSetRetentionPolicy_InvalidAggregation(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSSetRetentionPolicy(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":           "cpu.usage",
		"duration_seconds": float64(86400),
		"aggregation":      "invalid_func",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid aggregation")
	}
}

func TestHandleTSSetRetentionPolicy_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		setRetentionPolicyFn: func(_ string, _ *timeseries.RetentionPolicy) error {
			return errors.New("storage failure")
		},
	}
	handler := handleTSSetRetentionPolicy(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric":           "cpu.usage",
		"duration_seconds": float64(86400),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleTSSetRetentionPolicy_NoAggregation(t *testing.T) {
	var capturedPolicy *timeseries.RetentionPolicy
	svc := &mockTimeSeriesService{
		setRetentionPolicyFn: func(_ string, policy *timeseries.RetentionPolicy) error {
			capturedPolicy = policy
			return nil
		},
	}
	handler := handleTSSetRetentionPolicy(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":           "cpu.usage",
		"duration_seconds": float64(3600),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
	if capturedPolicy.Aggregation != timeseries.AggNone {
		t.Errorf("expected no aggregation, got %v", capturedPolicy.Aggregation)
	}
}

// ---------------------------------------------------------------------------
// TS list metrics handler tests
// ---------------------------------------------------------------------------

func TestHandleTSListMetrics_Success(t *testing.T) {
	svc := &mockTimeSeriesService{
		listMetricsFn: func(prefix string, limit int) ([]string, error) {
			return []string{"cpu.usage", "cpu.idle", "cpu.system"}, nil
		},
	}
	handler := handleTSListMetrics(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"prefix": "cpu.",
		"limit":  float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var metrics []string
	if jsonErr := json.Unmarshal([]byte(text), &metrics); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if len(metrics) != 3 {
		t.Errorf("expected 3 metrics, got %d", len(metrics))
	}
}

func TestHandleTSListMetrics_DefaultLimit(t *testing.T) {
	var capturedLimit int
	svc := &mockTimeSeriesService{
		listMetricsFn: func(_ string, limit int) ([]string, error) {
			capturedLimit = limit
			return []string{}, nil
		},
	}
	handler := handleTSListMetrics(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedLimit != 100 {
		t.Errorf("expected default limit 100, got %d", capturedLimit)
	}
}

func TestHandleTSListMetrics_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return nil, errors.New("storage failure")
		},
	}
	handler := handleTSListMetrics(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleTSListMetrics_EmptyResult(t *testing.T) {
	svc := &mockTimeSeriesService{
		listMetricsFn: func(_ string, _ int) ([]string, error) {
			return []string{}, nil
		},
	}
	handler := handleTSListMetrics(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"prefix": "nonexistent.",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var metrics []string
	if jsonErr := json.Unmarshal([]byte(text), &metrics); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if len(metrics) != 0 {
		t.Errorf("expected 0 metrics, got %d", len(metrics))
	}
}

// ---------------------------------------------------------------------------
// WritePoint handler tests
// ---------------------------------------------------------------------------

func TestHandleTSWritePoint_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWritePoint(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(85.5),
		"timestamp": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleTSWritePoint_WithTags(t *testing.T) {
	var captured *timeseries.DataPoint
	svc := &mockTimeSeriesService{
		writePointFn: func(_ context.Context, point *timeseries.DataPoint) error {
			captured = point
			return nil
		},
	}
	handler := handleTSWritePoint(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(85.5),
		"timestamp": float64(1000000000),
		"tags":      `{"host":"web-01","region":"us-east"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
	if captured == nil {
		t.Fatal("expected data point to be captured")
	}
	if captured.Metric != "cpu.usage" {
		t.Errorf("expected metric 'cpu.usage', got %q", captured.Metric)
	}
	if captured.Value != 85.5 {
		t.Errorf("expected value 85.5, got %f", captured.Value)
	}
	if captured.Timestamp.UnixNano() != 1000000000 {
		t.Errorf("expected timestamp 1000000000, got %d", captured.Timestamp.UnixNano())
	}
	if captured.Tags["host"] != "web-01" {
		t.Errorf("expected tag host=web-01, got %v", captured.Tags)
	}
	if captured.Tags["region"] != "us-east" {
		t.Errorf("expected tag region=us-east, got %v", captured.Tags)
	}
}

func TestHandleTSWritePoint_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWritePoint(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"value":     float64(85.5),
		"timestamp": float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing metric")
	}
}

func TestHandleTSWritePoint_MissingTimestamp(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWritePoint(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"value":  float64(85.5),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing timestamp")
	}
}

func TestHandleTSWritePoint_InvalidTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSWritePoint(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(85.5),
		"timestamp": float64(1000000000),
		"tags":      "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid tags")
	}
}

func TestHandleTSWritePoint_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		writePointFn: func(_ context.Context, _ *timeseries.DataPoint) error {
			return &timeseries.InvalidMetricError{}
		},
	}
	handler := handleTSWritePoint(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric":    "cpu.usage",
		"value":     float64(85.5),
		"timestamp": float64(1000000000),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_ts_write_point" {
		t.Errorf("expected tool 'qrdb_ts_write_point', got %q", toolErr.Tool)
	}
}

// ---------------------------------------------------------------------------
// RichQuery handler tests
// ---------------------------------------------------------------------------

func TestHandleTSRichQuery_Success(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var qr timeseries.QueryResult
	if jsonErr := json.Unmarshal([]byte(text), &qr); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
}

func TestHandleTSRichQuery_WithAllOptions(t *testing.T) {
	var captured *timeseries.QueryRequest
	svc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			captured = req
			return &timeseries.QueryResult{Metric: req.Metric}, nil
		},
	}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"tags":        `{"host":"web-01"}`,
		"aggregation": "avg",
		"interval_ns": float64(60000000000),
		"group_by":    `["host","region"]`,
		"limit":       float64(100),
		"percentile":  float64(95.0),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var qr timeseries.QueryResult
	if jsonErr := json.Unmarshal([]byte(text), &qr); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if captured == nil {
		t.Fatal("expected query request to be captured")
	}
	if captured.Metric != "cpu.usage" {
		t.Errorf("expected metric 'cpu.usage', got %q", captured.Metric)
	}
	if captured.StartTime.UnixNano() != 1000000000 {
		t.Errorf("expected start 1000000000, got %d", captured.StartTime.UnixNano())
	}
	if captured.EndTime.UnixNano() != 2000000000 {
		t.Errorf("expected end 2000000000, got %d", captured.EndTime.UnixNano())
	}
	if captured.Tags["host"] != "web-01" {
		t.Errorf("expected tag host=web-01, got %v", captured.Tags)
	}
	if captured.Aggregation != timeseries.AggAvg {
		t.Errorf("expected AggAvg, got %d", captured.Aggregation)
	}
	if captured.Interval != time.Duration(60000000000) {
		t.Errorf("expected interval 60s, got %v", captured.Interval)
	}
	if len(captured.GroupBy) != 2 || captured.GroupBy[0] != "host" || captured.GroupBy[1] != "region" {
		t.Errorf("expected group_by [host, region], got %v", captured.GroupBy)
	}
	if captured.Limit != 100 {
		t.Errorf("expected limit 100, got %d", captured.Limit)
	}
	if captured.Percentile != 95.0 {
		t.Errorf("expected percentile 95.0, got %f", captured.Percentile)
	}
}

func TestHandleTSRichQuery_MissingMetric(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"start": float64(1000000000),
		"end":   float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing metric")
	}
}

func TestHandleTSRichQuery_MissingStart(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"end":    float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing start")
	}
}

func TestHandleTSRichQuery_MissingEnd(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing end")
	}
}

func TestHandleTSRichQuery_InvalidTags(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
		"tags":   "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid tags")
	}
}

func TestHandleTSRichQuery_InvalidAggregation(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":      "cpu.usage",
		"start":       float64(1000000000),
		"end":         float64(2000000000),
		"aggregation": "invalid_func",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid aggregation")
	}
}

func TestHandleTSRichQuery_InvalidGroupBy(t *testing.T) {
	svc := &mockTimeSeriesService{}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric":   "cpu.usage",
		"start":    float64(1000000000),
		"end":      float64(2000000000),
		"group_by": "not-json-array",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid group_by")
	}
}

func TestHandleTSRichQuery_ServiceError(t *testing.T) {
	svc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			return nil, &timeseries.InvalidMetricError{}
		},
	}
	handler := handleTSRichQuery(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_ts_rich_query" {
		t.Errorf("expected tool 'qrdb_ts_rich_query', got %q", toolErr.Tool)
	}
}

func TestHandleTSRichQuery_NoAggregation(t *testing.T) {
	var captured *timeseries.QueryRequest
	svc := &mockTimeSeriesService{
		queryRichFn: func(_ context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
			captured = req
			return &timeseries.QueryResult{Metric: req.Metric}, nil
		},
	}
	handler := handleTSRichQuery(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"metric": "cpu.usage",
		"start":  float64(1000000000),
		"end":    float64(2000000000),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var qr timeseries.QueryResult
	if jsonErr := json.Unmarshal([]byte(text), &qr); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if captured == nil {
		t.Fatal("expected query request to be captured")
	}
	if captured.Aggregation != timeseries.AggNone {
		t.Errorf("expected AggNone, got %d", captured.Aggregation)
	}
}
