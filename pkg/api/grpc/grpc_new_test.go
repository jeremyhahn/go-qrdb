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

package grpc

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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
// stubCeremonyCA
// ---------------------------------------------------------------------------

// stubCeremonyCA is a minimal ceremony.CeremonyCA implementation for tests
// that need to prove a real (non-nil) CA reference reaches the ceremony
// service layer, without exercising actual certificate issuance. Defined in
// this untagged file (rather than one of the qrdb_ts-gated coverage files)
// so it is available under both the "!qrdb_ts" and "qrdb_ts" test build
// universes, which never compile together.
type stubCeremonyCA struct{}

var _ ceremony.CeremonyCA = (*stubCeremonyCA)(nil)

func (*stubCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (*stubCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) { return nil, nil }
func (*stubCeremonyCA) CABundle() ([]byte, error)                                { return []byte("ca-bundle"), nil }
func (*stubCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{CertPEM: []byte("cert-pem")}, nil
}

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

// mockMVCCService implements service.MVCCKVServicer for testing.
type mockMVCCService struct {
	mockKVService
	putWithLeaseResult *kv.KeyValue
	putWithLeaseErr    error
	getRevResult       *kv.KeyValue
	getRevErr          error
	getAtRevResult     *kv.KeyValue
	getAtRevErr        error
	rangeResult        []*kv.KeyValue
	rangeCount         int64
	rangeRevision      int64
	rangeErr           error
	compactErr         error
	currentRev         int64
	currentRevErr      error
	restoreResult      *service.RestoreRevisionResult
	restoreErr         error
	lastRestoreReq     *service.RestoreRevisionRequest
}

func (m *mockMVCCService) PutWithLease(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
	if m.putWithLeaseErr != nil {
		return nil, m.putWithLeaseErr
	}
	return m.putWithLeaseResult, nil
}

func (m *mockMVCCService) GetRevision(_ context.Context, _ string) (*kv.KeyValue, error) {
	if m.getRevErr != nil {
		return nil, m.getRevErr
	}
	return m.getRevResult, nil
}

func (m *mockMVCCService) GetAtRevision(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
	if m.getAtRevErr != nil {
		return nil, m.getAtRevErr
	}
	return m.getAtRevResult, nil
}

func (m *mockMVCCService) Range(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeErr != nil {
		return nil, 0, 0, m.rangeErr
	}
	return m.rangeResult, m.rangeCount, m.rangeRevision, nil
}

func (m *mockMVCCService) Compact(_ context.Context, _ int64) error {
	return m.compactErr
}

func (m *mockMVCCService) CurrentRevision(_ context.Context) (int64, error) {
	if m.currentRevErr != nil {
		return 0, m.currentRevErr
	}
	return m.currentRev, nil
}

func (m *mockMVCCService) PutWithLeaseAndMode(_ context.Context, _ string, _ []byte, _ int64, _ writemode.Mode) (*kv.KeyValue, error) {
	if m.putWithLeaseErr != nil {
		return nil, m.putWithLeaseErr
	}
	return m.putWithLeaseResult, nil
}

func (m *mockMVCCService) GetRevisionWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	if m.getRevErr != nil {
		return nil, m.getRevErr
	}
	return m.getRevResult, nil
}

func (m *mockMVCCService) GetAtRevisionWithConsistency(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	if m.getAtRevErr != nil {
		return nil, m.getAtRevErr
	}
	return m.getAtRevResult, nil
}

func (m *mockMVCCService) RangeWithConsistency(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeErr != nil {
		return nil, 0, 0, m.rangeErr
	}
	return m.rangeResult, m.rangeCount, m.rangeRevision, nil
}

func (m *mockMVCCService) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	m.lastRestoreReq = req
	if m.restoreErr != nil {
		return nil, m.restoreErr
	}
	return m.restoreResult, nil
}

// mockTransactionService implements service.TransactionServicer for testing.
type mockTransactionService struct {
	result *service.TransactionResponse
	err    error
}

func (m *mockTransactionService) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func (m *mockTransactionService) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func (m *mockTransactionService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, nil
}

// mockBatchService implements service.BatchServicer for testing.
type mockBatchService struct {
	result   []service.OpResponse
	revision int64
	err      error
}

func (m *mockBatchService) Execute(_ context.Context, _ []service.Op) (*service.BatchResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &service.BatchResponse{Revision: m.revision, Responses: m.result}, nil
}

func (m *mockBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) (*service.BatchResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &service.BatchResponse{Revision: m.revision, Responses: m.result}, nil
}

func (m *mockBatchService) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, nil
}

func (m *mockBatchService) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return nil, nil
}

func (m *mockBatchService) ImportBegin(_ context.Context, _ service.ImportOptions) (string, error) {
	return "", nil
}

func (m *mockBatchService) ImportAppend(_ context.Context, _ string, _ uint64, _ []byte) error {
	return nil
}

func (m *mockBatchService) ImportCommit(_ context.Context, _ string) (*service.ImportResult, error) {
	return &service.ImportResult{}, nil
}

func (m *mockBatchService) ImportAbort(_ context.Context, _ string) error {
	return nil
}

// mockWatchServiceImpl implements service.WatchServicer for testing.
type mockWatchServiceImpl struct {
	watcher watch.Watcher
	err     error
}

func (m *mockWatchServiceImpl) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.watcher, nil
}

func (m *mockWatchServiceImpl) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.watcher, nil
}

func (m *mockWatchServiceImpl) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

func (m *mockWatchServiceImpl) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// mockWatcher implements watch.Watcher for testing.
type mockWatcher struct {
	events chan watch.WatchResponse
	id     int64
	closed bool
	err    error
}

func newMockWatcher(id int64, bufSize int) *mockWatcher {
	return &mockWatcher{
		events: make(chan watch.WatchResponse, bufSize),
		id:     id,
	}
}

func (m *mockWatcher) Events() <-chan watch.WatchResponse {
	return m.events
}

func (m *mockWatcher) WatchID() int64 {
	return m.id
}

func (m *mockWatcher) Close() error {
	if !m.closed {
		m.closed = true
		close(m.events)
	}
	return nil
}

func (m *mockWatcher) Err() error {
	return m.err
}

// mockLeaseService implements lease.LeaseServicer for testing.
type mockLeaseService struct {
	grantResult     *lease.Lease
	grantErr        error
	revokeErr       error
	keepAliveResult *lease.Lease
	keepAliveErr    error
	ttlResult       *lease.Lease
	ttlErr          error
	leasesResult    []*lease.Lease
	leasesErr       error
}

func (m *mockLeaseService) Grant(_ context.Context, _ int64) (*lease.Lease, error) {
	if m.grantErr != nil {
		return nil, m.grantErr
	}
	return m.grantResult, nil
}

func (m *mockLeaseService) Revoke(_ context.Context, _ int64) error {
	return m.revokeErr
}

func (m *mockLeaseService) KeepAlive(_ context.Context, _ int64) (*lease.Lease, error) {
	if m.keepAliveErr != nil {
		return nil, m.keepAliveErr
	}
	return m.keepAliveResult, nil
}

func (m *mockLeaseService) TimeToLive(_ context.Context, _ int64) (*lease.Lease, error) {
	if m.ttlErr != nil {
		return nil, m.ttlErr
	}
	return m.ttlResult, nil
}

func (m *mockLeaseService) Leases(_ context.Context) ([]*lease.Lease, error) {
	if m.leasesErr != nil {
		return nil, m.leasesErr
	}
	return m.leasesResult, nil
}

// mockIndexService implements index.IndexServicer for testing.
type mockIndexService struct {
	createErr   error
	dropErr     error
	queryResult [][]byte
	queryErr    error
	scanResult  map[string][][]byte
	scanErr     error
	listResult  []string
	listErr     error
}

func (m *mockIndexService) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return m.createErr
}

func (m *mockIndexService) DropIndex(_ context.Context, _ string) error {
	return m.dropErr
}

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (m *mockIndexService) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return m.DropIndex(ctx, name)
}

func (m *mockIndexService) Query(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return m.queryResult, nil
}

func (m *mockIndexService) Scan(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	return m.scanResult, nil
}

func (m *mockIndexService) ListIndexes(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}

// mockTimeSeriesService implements timeseries.TimeSeriesServicer for testing.
type mockTimeSeriesService struct {
	writeErr        error
	writeBatchErr   error
	queryResult     []timeseries.DataPoint
	queryErr        error
	aggregateResult []timeseries.AggResult
	aggregateErr    error
}

func (m *mockTimeSeriesService) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	return m.writeErr
}

func (m *mockTimeSeriesService) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error {
	return m.writeBatchErr
}

func (m *mockTimeSeriesService) Query(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return m.queryResult, nil
}

func (m *mockTimeSeriesService) Aggregate(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	if m.aggregateErr != nil {
		return nil, m.aggregateErr
	}
	return m.aggregateResult, nil
}

func (m *mockTimeSeriesService) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	return m.writeErr
}

func (m *mockTimeSeriesService) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return nil
}

func (m *mockTimeSeriesService) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) SetRetentionPolicy(_ context.Context, _ string, _ *timeseries.RetentionPolicy) error {
	return nil
}

func (m *mockTimeSeriesService) ApplyRetention(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockTimeSeriesService) ListMetrics(_ string, _ int) ([]string, error) {
	return nil, nil
}

func (m *mockTimeSeriesService) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return m.Query(ctx, metric, start, end, opts...)
}

func (m *mockTimeSeriesService) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return m.Aggregate(ctx, metric, start, end, fn, interval)
}

func (m *mockTimeSeriesService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return m.QueryRich(ctx, req)
}

func (m *mockTimeSeriesService) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return m.ListMetrics(prefix, limit)
}

// ---------------------------------------------------------------------------
// Extended test server helper
// ---------------------------------------------------------------------------

// startTestServerFull creates a gRPC server with all optional services
// configured via ServerOption and returns a connected client + cleanup func.
func startTestServerFull(t *testing.T, opts ...ServerOption) (pb.QRDBServiceClient, func()) {
	t.Helper()

	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, "127.0.0.1:0", append([]ServerOption{WithTLS(tlsCfg)}, opts...)...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	conn, err := grpc.NewClient(
		srv.Addr(),
		grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)),
	)
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}

	client := pb.NewQRDBServiceClient(conn)

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}

	return client, cleanup
}

// ---------------------------------------------------------------------------
// ServerOption tests
// ---------------------------------------------------------------------------

func TestWithMVCC(t *testing.T) {
	svc := &mockMVCCService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithMVCC(svc))
	if srv.mvccSvc == nil {
		t.Fatal("expected mvccSvc to be set")
	}
}

func TestWithMVCC_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithMVCC(nil))
	if srv.mvccSvc != nil {
		t.Fatal("expected mvccSvc to be nil")
	}
}

func TestWithTransaction(t *testing.T) {
	svc := &mockTransactionService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTransaction(svc))
	if srv.txSvc == nil {
		t.Fatal("expected txSvc to be set")
	}
}

func TestWithTransaction_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTransaction(nil))
	if srv.txSvc != nil {
		t.Fatal("expected txSvc to be nil")
	}
}

func TestWithBatch(t *testing.T) {
	svc := &mockBatchService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithBatch(svc))
	if srv.batchSvc == nil {
		t.Fatal("expected batchSvc to be set")
	}
}

func TestWithBatch_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithBatch(nil))
	if srv.batchSvc != nil {
		t.Fatal("expected batchSvc to be nil")
	}
}

func TestWithWatch(t *testing.T) {
	svc := &mockWatchServiceImpl{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithWatch(svc))
	if srv.watchSvc == nil {
		t.Fatal("expected watchSvc to be set")
	}
}

func TestWithWatch_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithWatch(nil))
	if srv.watchSvc != nil {
		t.Fatal("expected watchSvc to be nil")
	}
}

func TestWithLease(t *testing.T) {
	svc := &mockLeaseService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithLease(svc))
	if srv.leaseSvc == nil {
		t.Fatal("expected leaseSvc to be set")
	}
}

func TestWithLease_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithLease(nil))
	if srv.leaseSvc != nil {
		t.Fatal("expected leaseSvc to be nil")
	}
}

func TestWithIndex(t *testing.T) {
	svc := &mockIndexService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithIndex(svc))
	if srv.indexSvc == nil {
		t.Fatal("expected indexSvc to be set")
	}
}

func TestWithIndex_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithIndex(nil))
	if srv.indexSvc != nil {
		t.Fatal("expected indexSvc to be nil")
	}
}

func TestWithTimeSeries(t *testing.T) {
	svc := &mockTimeSeriesService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTimeSeries(svc))
	if srv.tsSvc == nil {
		t.Fatal("expected tsSvc to be set")
	}
}

func TestWithTimeSeries_Nil(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTimeSeries(nil))
	if srv.tsSvc != nil {
		t.Fatal("expected tsSvc to be nil")
	}
}

// ---------------------------------------------------------------------------
// Conversion helper tests
// ---------------------------------------------------------------------------

func TestKvToProto_Nil(t *testing.T) {
	result := kvToProto(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestKvToProto_ZeroValues(t *testing.T) {
	k := &kv.KeyValue{}
	result := kvToProto(k)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.GetVersion() != 0 {
		t.Fatalf("expected version 0, got %d", result.GetVersion())
	}
}

func TestKvToProto_FullValues(t *testing.T) {
	k := &kv.KeyValue{
		Key:            []byte("test-key"),
		Value:          []byte("test-value"),
		CreateRevision: 10,
		ModRevision:    20,
		Version:        3,
		LeaseID:        42,
	}
	result := kvToProto(k)
	if !bytes.Equal(result.GetKey(), []byte("test-key")) {
		t.Fatalf("expected key 'test-key', got %q", result.GetKey())
	}
	if !bytes.Equal(result.GetValue(), []byte("test-value")) {
		t.Fatalf("expected value 'test-value', got %q", result.GetValue())
	}
	if result.GetCreateRevision() != 10 {
		t.Fatalf("expected create_revision 10, got %d", result.GetCreateRevision())
	}
	if result.GetModRevision() != 20 {
		t.Fatalf("expected mod_revision 20, got %d", result.GetModRevision())
	}
	if result.GetVersion() != 3 {
		t.Fatalf("expected version 3, got %d", result.GetVersion())
	}
	if result.GetLeaseId() != 42 {
		t.Fatalf("expected lease_id 42, got %d", result.GetLeaseId())
	}
}

func TestProtoToCompare_Version(t *testing.T) {
	c := &pb.Compare{
		Key:         []byte("k"),
		Target:      pb.CompareTarget_COMPARE_VERSION,
		Result:      pb.CompareResult_COMPARE_EQUAL,
		TargetUnion: &pb.Compare_Version{Version: 5},
	}
	sc := protoToCompare(c)
	if !bytes.Equal(sc.Key, []byte("k")) {
		t.Fatalf("expected key 'k', got %q", sc.Key)
	}
	if sc.Target != service.CompareVersion {
		t.Fatalf("expected CompareVersion, got %d", sc.Target)
	}
	if sc.Result != service.CompareEqual {
		t.Fatalf("expected CompareEqual, got %d", sc.Result)
	}
	if sc.Version != 5 {
		t.Fatalf("expected version 5, got %d", sc.Version)
	}
}

func TestProtoToCompare_CreateRevision(t *testing.T) {
	c := &pb.Compare{
		Key:         []byte("k"),
		Target:      pb.CompareTarget_COMPARE_CREATE_REVISION,
		Result:      pb.CompareResult_COMPARE_GREATER,
		TargetUnion: &pb.Compare_CreateRevision{CreateRevision: 10},
	}
	sc := protoToCompare(c)
	if sc.Target != service.CompareCreateRevision {
		t.Fatalf("expected CompareCreateRevision, got %d", sc.Target)
	}
	if sc.CreateRevision != 10 {
		t.Fatalf("expected create_revision 10, got %d", sc.CreateRevision)
	}
}

func TestProtoToCompare_ModRevision(t *testing.T) {
	c := &pb.Compare{
		Key:         []byte("k"),
		Target:      pb.CompareTarget_COMPARE_MOD_REVISION,
		Result:      pb.CompareResult_COMPARE_LESS,
		TargetUnion: &pb.Compare_ModRevision{ModRevision: 20},
	}
	sc := protoToCompare(c)
	if sc.Target != service.CompareModRevision {
		t.Fatalf("expected CompareModRevision, got %d", sc.Target)
	}
	if sc.ModRevision != 20 {
		t.Fatalf("expected mod_revision 20, got %d", sc.ModRevision)
	}
}

func TestProtoToCompare_Value(t *testing.T) {
	c := &pb.Compare{
		Key:         []byte("k"),
		Target:      pb.CompareTarget_COMPARE_VALUE,
		Result:      pb.CompareResult_COMPARE_NOT_EQUAL,
		TargetUnion: &pb.Compare_Value{Value: []byte("val")},
	}
	sc := protoToCompare(c)
	if sc.Target != service.CompareValue {
		t.Fatalf("expected CompareValue, got %d", sc.Target)
	}
	if !bytes.Equal(sc.Value, []byte("val")) {
		t.Fatalf("expected value 'val', got %q", sc.Value)
	}
}

func TestProtoToOp_Put(t *testing.T) {
	op := &pb.Op{
		Type:    pb.OpType_OP_PUT,
		Key:     []byte("k"),
		Value:   []byte("v"),
		LeaseId: 99,
	}
	so := protoToOp(op)
	if so.Type != service.OpPut {
		t.Fatalf("expected OpPut, got %d", so.Type)
	}
	if !bytes.Equal(so.Key, []byte("k")) {
		t.Fatalf("expected key 'k', got %q", so.Key)
	}
	if !bytes.Equal(so.Value, []byte("v")) {
		t.Fatalf("expected value 'v', got %q", so.Value)
	}
	if so.LeaseID != 99 {
		t.Fatalf("expected lease_id 99, got %d", so.LeaseID)
	}
}

func TestProtoToOp_Get(t *testing.T) {
	op := &pb.Op{
		Type: pb.OpType_OP_GET,
		Key:  []byte("k"),
	}
	so := protoToOp(op)
	if so.Type != service.OpGet {
		t.Fatalf("expected OpGet, got %d", so.Type)
	}
}

func TestProtoToOp_Delete(t *testing.T) {
	op := &pb.Op{
		Type: pb.OpType_OP_DELETE,
		Key:  []byte("k"),
	}
	so := protoToOp(op)
	if so.Type != service.OpDelete {
		t.Fatalf("expected OpDelete, got %d", so.Type)
	}
}

func TestProtoToOp_Range(t *testing.T) {
	op := &pb.Op{
		Type:     pb.OpType_OP_RANGE,
		Key:      []byte("start"),
		RangeEnd: []byte("end"),
	}
	so := protoToOp(op)
	if so.Type != service.OpRange {
		t.Fatalf("expected OpRange, got %d", so.Type)
	}
	if !bytes.Equal(so.RangeEnd, []byte("end")) {
		t.Fatalf("expected range_end 'end', got %q", so.RangeEnd)
	}
}

// An enum value this server does not know must not become the zero OpPut.
func TestProtoToOp_UnknownTypeIsNotAPut(t *testing.T) {
	for _, v := range []pb.OpType{pb.OpType(4), pb.OpType(99), pb.OpType(256)} {
		so := protoToOp(&pb.Op{Type: v, Key: []byte("k"), Value: []byte("v")})
		if so.Type != service.OpUnknown {
			t.Fatalf("type %d became %d, want OpUnknown", v, so.Type)
		}
	}
}

func TestOpResponseToProto_WithKvs(t *testing.T) {
	r := service.OpResponse{
		Kvs: []*kv.KeyValue{
			{Key: []byte("a"), Value: []byte("1"), Version: 1},
			{Key: []byte("b"), Value: []byte("2"), Version: 2},
		},
		Count:     2,
		Succeeded: true,
	}
	result := opResponseToProto(r)
	if len(result.GetKvs()) != 2 {
		t.Fatalf("expected 2 kvs, got %d", len(result.GetKvs()))
	}
	if result.GetCount() != 2 {
		t.Fatalf("expected count 2, got %d", result.GetCount())
	}
	if !result.GetSucceeded() {
		t.Fatal("expected succeeded=true")
	}
}

func TestOpResponseToProto_Empty(t *testing.T) {
	r := service.OpResponse{}
	result := opResponseToProto(r)
	if len(result.GetKvs()) != 0 {
		t.Fatalf("expected 0 kvs, got %d", len(result.GetKvs()))
	}
	if result.GetSucceeded() {
		t.Fatal("expected succeeded=false")
	}
}

func TestLeaseToProto_Nil(t *testing.T) {
	result := leaseToProto(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestLeaseToProto_WithKeys(t *testing.T) {
	l := &lease.Lease{
		ID:           100,
		TTL:          60,
		GrantedTTL:   120,
		RemainingTTL: 55,
		Keys:         [][]byte{[]byte("key1"), []byte("key2")},
	}
	result := leaseToProto(l)
	if result.GetId() != 100 {
		t.Fatalf("expected id 100, got %d", result.GetId())
	}
	if result.GetTtl() != 60 {
		t.Fatalf("expected ttl 60, got %d", result.GetTtl())
	}
	if result.GetGrantedTtl() != 120 {
		t.Fatalf("expected granted_ttl 120, got %d", result.GetGrantedTtl())
	}
	if result.GetRemainingTtl() != 55 {
		t.Fatalf("expected remaining_ttl 55, got %d", result.GetRemainingTtl())
	}
	if len(result.GetKeys()) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(result.GetKeys()))
	}
}

func TestLeaseToProto_NoKeys(t *testing.T) {
	l := &lease.Lease{ID: 1, TTL: 10}
	result := leaseToProto(l)
	if result.GetId() != 1 {
		t.Fatalf("expected id 1, got %d", result.GetId())
	}
	if len(result.GetKeys()) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(result.GetKeys()))
	}
}

func TestEventToProto_Nil(t *testing.T) {
	result := eventToProto(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestEventToProto_PutEvent(t *testing.T) {
	e := &watch.Event{
		Type: watch.EventPut,
		Kv: &kv.KeyValue{
			Key:   []byte("k"),
			Value: []byte("v"),
		},
	}
	result := eventToProto(e)
	if result.GetType() != pb.EventType_EVENT_PUT {
		t.Fatalf("expected EVENT_PUT, got %v", result.GetType())
	}
	if !bytes.Equal(result.GetKv().GetKey(), []byte("k")) {
		t.Fatalf("expected key 'k', got %q", result.GetKv().GetKey())
	}
	if result.GetPrevKv() != nil {
		t.Fatal("expected nil PrevKv")
	}
}

func TestEventToProto_DeleteWithPrevKv(t *testing.T) {
	e := &watch.Event{
		Type: watch.EventDelete,
		Kv: &kv.KeyValue{
			Key: []byte("k"),
		},
		PrevKv: &kv.KeyValue{
			Key:   []byte("k"),
			Value: []byte("old"),
		},
	}
	result := eventToProto(e)
	if result.GetType() != pb.EventType_EVENT_DELETE {
		t.Fatalf("expected EVENT_DELETE, got %v", result.GetType())
	}
	if result.GetPrevKv() == nil {
		t.Fatal("expected non-nil PrevKv")
	}
	if !bytes.Equal(result.GetPrevKv().GetValue(), []byte("old")) {
		t.Fatalf("expected prev value 'old', got %q", result.GetPrevKv().GetValue())
	}
}

func TestProtoTagsToMap_Empty(t *testing.T) {
	result := protoTagsToMap(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestProtoTagsToMap_EmptySlice(t *testing.T) {
	result := protoTagsToMap([]*pb.TSTag{})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestProtoTagsToMap_Multiple(t *testing.T) {
	tags := []*pb.TSTag{
		{Key: "host", Value: "server1"},
		{Key: "region", Value: "us-east"},
	}
	result := protoTagsToMap(tags)
	if len(result) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(result))
	}
	if result["host"] != "server1" {
		t.Fatalf("expected host=server1, got %q", result["host"])
	}
	if result["region"] != "us-east" {
		t.Fatalf("expected region=us-east, got %q", result["region"])
	}
}

func TestMapToProtoTags_Empty(t *testing.T) {
	result := mapToProtoTags(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestMapToProtoTags_EmptyMap(t *testing.T) {
	result := mapToProtoTags(map[string]string{})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestMapToProtoTags_Single(t *testing.T) {
	m := map[string]string{"env": "prod"}
	result := mapToProtoTags(m)
	if len(result) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(result))
	}
	if result[0].GetKey() != "env" || result[0].GetValue() != "prod" {
		t.Fatalf("expected env=prod, got %s=%s", result[0].GetKey(), result[0].GetValue())
	}
}

func TestMapToProtoTags_Multiple(t *testing.T) {
	m := map[string]string{"a": "1", "b": "2"}
	result := mapToProtoTags(m)
	if len(result) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(result))
	}
}

func TestDataPointToProto(t *testing.T) {
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	dp := timeseries.DataPoint{
		Metric:    "cpu.usage",
		Value:     42.5,
		Timestamp: ts,
		Tags:      map[string]string{"host": "node1"},
	}
	result := dataPointToProto(dp)
	if result.GetMetric() != "cpu.usage" {
		t.Fatalf("expected metric 'cpu.usage', got %q", result.GetMetric())
	}
	if result.GetValue() != 42.5 {
		t.Fatalf("expected value 42.5, got %f", result.GetValue())
	}
	if result.GetTimestampNs() != ts.UnixNano() {
		t.Fatalf("expected timestamp %d, got %d", ts.UnixNano(), result.GetTimestampNs())
	}
	if len(result.GetTags()) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(result.GetTags()))
	}
}

func TestDataPointToProto_NoTags(t *testing.T) {
	dp := timeseries.DataPoint{
		Metric:    "mem",
		Value:     1024,
		Timestamp: time.Unix(0, 1000),
	}
	result := dataPointToProto(dp)
	if len(result.GetTags()) != 0 {
		t.Fatalf("expected 0 tags, got %d", len(result.GetTags()))
	}
}

func TestProtoToDataPoint(t *testing.T) {
	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	pbDP := &pb.TSDataPoint{
		Metric:      "cpu.usage",
		Value:       42.5,
		TimestampNs: ts.UnixNano(),
		Tags:        []*pb.TSTag{{Key: "host", Value: "node1"}},
	}
	result := protoToDataPoint(pbDP)
	if result.Metric != "cpu.usage" {
		t.Fatalf("expected metric 'cpu.usage', got %q", result.Metric)
	}
	if result.Value != 42.5 {
		t.Fatalf("expected value 42.5, got %f", result.Value)
	}
	if !result.Timestamp.Equal(ts) {
		t.Fatalf("expected timestamp %v, got %v", ts, result.Timestamp)
	}
	if result.Tags["host"] != "node1" {
		t.Fatalf("expected tag host=node1, got %q", result.Tags["host"])
	}
}

func TestProtoToDataPoint_NoTags(t *testing.T) {
	pbDP := &pb.TSDataPoint{
		Metric:      "mem",
		Value:       512,
		TimestampNs: 5000,
	}
	result := protoToDataPoint(pbDP)
	if result.Tags != nil {
		t.Fatalf("expected nil tags, got %v", result.Tags)
	}
}

func TestAggResultToProto(t *testing.T) {
	ts := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	r := timeseries.AggResult{
		Timestamp: ts,
		Value:     99.9,
		Count:     100,
	}
	result := aggResultToProto(r)
	if result.GetTimestampNs() != ts.UnixNano() {
		t.Fatalf("expected timestamp %d, got %d", ts.UnixNano(), result.GetTimestampNs())
	}
	if result.GetValue() != 99.9 {
		t.Fatalf("expected value 99.9, got %f", result.GetValue())
	}
	if result.GetCount() != 100 {
		t.Fatalf("expected count 100, got %d", result.GetCount())
	}
}

func TestAggResultToProto_Zero(t *testing.T) {
	r := timeseries.AggResult{}
	result := aggResultToProto(r)
	if result.GetValue() != 0 {
		t.Fatalf("expected value 0, got %f", result.GetValue())
	}
	if result.GetCount() != 0 {
		t.Fatalf("expected count 0, got %d", result.GetCount())
	}
}

// ---------------------------------------------------------------------------
// Error mapping tests
// ---------------------------------------------------------------------------

func TestMapServiceError_RevisionCompactedError(t *testing.T) {
	err := mapServiceError(&service.RevisionCompactedError{Requested: 5, Compacted: 10})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.OutOfRange {
		t.Fatalf("expected OutOfRange, got %s", st.Code())
	}
}

func TestMapServiceError_RevisionNotFoundError(t *testing.T) {
	err := mapServiceError(&service.RevisionNotFoundError{Revision: 42})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapServiceError_TransactionConflictError(t *testing.T) {
	err := mapServiceError(&service.TransactionConflictError{Key: "k"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Aborted {
		t.Fatalf("expected Aborted, got %s", st.Code())
	}
}

func TestMapServiceError_WatchCanceledError(t *testing.T) {
	err := mapServiceError(&service.WatchCanceledError{Reason: "compacted"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Canceled {
		t.Fatalf("expected Canceled, got %s", st.Code())
	}
}

func TestMapServiceError_ImportErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"InvalidFormat", &service.ImportInvalidFormatError{Format: "bogus"}, codes.InvalidArgument},
		{"MissingKeyField", &service.ImportMissingKeyFieldError{}, codes.InvalidArgument},
		{"InvalidValueMode", &service.ImportInvalidValueModeError{Mode: "bogus"}, codes.InvalidArgument},
		{"MissingValueField", &service.ImportMissingValueFieldError{}, codes.InvalidArgument},
		{"ParseError", &service.ImportParseError{Err: errors.New("boom")}, codes.InvalidArgument},
		{"ArchiveError", &service.ImportArchiveError{Err: errors.New("boom")}, codes.InvalidArgument},
		{"TransactionAborted", &service.ImportTransactionAbortedError{Row: 3, Key: "k", Message: "failed"}, codes.Aborted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mapServiceError(tt.err)
			st, ok := status.FromError(err)
			if !ok {
				t.Fatal("expected gRPC status error")
			}
			if st.Code() != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, st.Code())
			}
		})
	}
}

func TestMapServiceError_LeaseNotFoundError(t *testing.T) {
	err := mapServiceError(&service.LeaseNotFoundError{LeaseID: 42})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// TestMapServiceError_ReadErrorWrappedLeaseNotFound pins the exact shape a
// pre-fix LeaseService.TimeToLive produced: *service.ReadError wrapping
// *service.LeaseNotFoundError. mapServiceError's ReadError arm must not win
// this race — the LeaseNotFoundError arm has to be checked first — or every
// caller of gRPC (and Unix, which shares this classifier) sees Internal
// instead of NotFound for a missing lease (go-qrdb#387).
func TestMapServiceError_ReadErrorWrappedLeaseNotFound(t *testing.T) {
	wrapped := &service.ReadError{Err: &service.LeaseNotFoundError{LeaseID: 42}}
	err := mapServiceError(wrapped)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound for a ReadError-wrapped LeaseNotFoundError, got %s", st.Code())
	}
}

func TestMapServiceError_LeaseExpiredError(t *testing.T) {
	err := mapServiceError(&service.LeaseExpiredError{LeaseID: 42})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapServiceError_LeaseExistsError(t *testing.T) {
	err := mapServiceError(&service.LeaseExistsError{LeaseID: 42})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestMapServiceError_BatchError(t *testing.T) {
	err := mapServiceError(&service.BatchError{Index: 0, Err: errors.New("op failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapServiceError_IndexNotFoundError(t *testing.T) {
	err := mapServiceError(&service.IndexNotFoundError{Name: "idx"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestMapServiceError_IndexExistsError(t *testing.T) {
	err := mapServiceError(&service.IndexExistsError{Name: "idx"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestMapServiceError_TimeSeriesError(t *testing.T) {
	err := mapServiceError(&service.TimeSeriesError{Op: "write", Err: errors.New("ts failed")})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestMapServiceError_ConsistencyError(t *testing.T) {
	err := mapServiceError(&service.ConsistencyError{Level: "strong", Reason: "no quorum"})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// ServiceNotConfiguredError tests
// ---------------------------------------------------------------------------

func TestServiceNotConfiguredError_Error(t *testing.T) {
	e := &ServiceNotConfiguredError{Service: "mvcc"}
	msg := e.Error()
	if !strings.Contains(msg, "mvcc") {
		t.Fatalf("expected message to contain 'mvcc', got %q", msg)
	}
	if !strings.Contains(msg, "not configured") {
		t.Fatalf("expected message to contain 'not configured', got %q", msg)
	}
}

func TestServiceNotConfiguredError_DifferentService(t *testing.T) {
	e := &ServiceNotConfiguredError{Service: "timeseries"}
	msg := e.Error()
	if !strings.Contains(msg, "timeseries") {
		t.Fatalf("expected message to contain 'timeseries', got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// MVCC handler tests
// ---------------------------------------------------------------------------

func TestGRPC_PutWithLease_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		putWithLeaseResult: &kv.KeyValue{
			Key:         []byte("k"),
			Value:       []byte("v"),
			ModRevision: 5,
			LeaseID:     42,
		},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.PutWithLease(context.Background(), &pb.PutWithLeaseRequest{
		Key:     "k",
		Value:   []byte("v"),
		LeaseId: 42,
	})
	if err != nil {
		t.Fatalf("PutWithLease failed: %v", err)
	}
	if resp.GetKv().GetLeaseId() != 42 {
		t.Fatalf("expected lease_id 42, got %d", resp.GetKv().GetLeaseId())
	}
}

func TestGRPC_PutWithLease_Error(t *testing.T) {
	mvccSvc := &mockMVCCService{
		putWithLeaseErr: &service.ProposeError{Err: errors.New("propose failed")},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.PutWithLease(context.Background(), &pb.PutWithLeaseRequest{
		Key: "k", Value: []byte("v"), LeaseId: 1,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_PutWithLease_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.PutWithLease(context.Background(), &pb.PutWithLeaseRequest{
		Key: "k", Value: []byte("v"), LeaseId: 1,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getRevResult: &kv.KeyValue{
			Key:            []byte("k"),
			Value:          []byte("v"),
			CreateRevision: 1,
			ModRevision:    3,
			Version:        3,
		},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.GetRevision(context.Background(), &pb.GetRevisionRequest{Key: "k"})
	if err != nil {
		t.Fatalf("GetRevision failed: %v", err)
	}
	if resp.GetKv().GetVersion() != 3 {
		t.Fatalf("expected version 3, got %d", resp.GetKv().GetVersion())
	}
}

func TestGRPC_GetRevision_Error(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getRevErr: &service.KeyNotFoundError{Key: "k"},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.GetRevision(context.Background(), &pb.GetRevisionRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_GetRevision_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetRevision(context.Background(), &pb.GetRevisionRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_GetAtRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getAtRevResult: &kv.KeyValue{
			Key:         []byte("k"),
			Value:       []byte("old"),
			ModRevision: 2,
		},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.GetAtRevision(context.Background(), &pb.GetAtRevisionRequest{
		Key: "k", Revision: 2,
	})
	if err != nil {
		t.Fatalf("GetAtRevision failed: %v", err)
	}
	if !bytes.Equal(resp.GetKv().GetValue(), []byte("old")) {
		t.Fatalf("expected value 'old', got %q", resp.GetKv().GetValue())
	}
}

func TestGRPC_GetAtRevision_Error(t *testing.T) {
	mvccSvc := &mockMVCCService{
		getAtRevErr: &service.RevisionCompactedError{Requested: 1, Compacted: 5},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.GetAtRevision(context.Background(), &pb.GetAtRevisionRequest{
		Key: "k", Revision: 1,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.OutOfRange {
		t.Fatalf("expected OutOfRange, got %s", st.Code())
	}
}

func TestGRPC_GetAtRevision_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.GetAtRevision(context.Background(), &pb.GetAtRevisionRequest{
		Key: "k", Revision: 1,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_Range_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{
		rangeResult: []*kv.KeyValue{
			{Key: []byte("a"), Value: []byte("1")},
			{Key: []byte("b"), Value: []byte("2")},
		},
		rangeCount: 2,
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.Range(context.Background(), &pb.RangeRequest{
		Start: []byte("a"),
		End:   []byte("c"),
	})
	if err != nil {
		t.Fatalf("Range failed: %v", err)
	}
	if len(resp.GetKvs()) != 2 {
		t.Fatalf("expected 2 kvs, got %d", len(resp.GetKvs()))
	}
	if resp.GetCount() != 2 {
		t.Fatalf("expected count 2, got %d", resp.GetCount())
	}
}

// TestGRPC_Range_RejectsEmptyStart is the defense-in-depth guard for finding
// C-1: an absent range start denotes the start of the shared store, which would
// scan across the (tenant, db) namespace boundary, so the handler must reject it
// with InvalidArgument rather than forward it.
func TestGRPC_Range_RejectsEmptyStart(t *testing.T) {
	// rangeResult is populated: if the handler wrongly forwarded the rejected
	// request, the call would succeed instead of returning InvalidArgument.
	mvccSvc := &mockMVCCService{rangeResult: []*kv.KeyValue{{Key: []byte("leak")}}, rangeCount: 1}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	for _, start := range [][]byte{nil, {}} {
		_, err := client.Range(context.Background(), &pb.RangeRequest{Start: start, End: []byte("z")})
		if err == nil {
			t.Fatalf("Range with empty start must be rejected, not forwarded")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("Range empty start code = %v, want InvalidArgument", status.Code(err))
		}
	}
}

func TestGRPC_Range_WithOptions(t *testing.T) {
	mvccSvc := &mockMVCCService{
		rangeResult: []*kv.KeyValue{{Key: []byte("a")}},
		rangeCount:  10,
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.Range(context.Background(), &pb.RangeRequest{
		Start:      []byte("a"),
		End:        []byte("z"),
		Limit:      1,
		Revision:   5,
		CountOnly:  true,
		KeysOnly:   true,
		SortOrder:  pb.SortOrder_SORT_ASCEND,
		SortTarget: pb.SortTarget_SORT_BY_KEY,
	})
	if err != nil {
		t.Fatalf("Range with options failed: %v", err)
	}
	if resp.GetCount() != 10 {
		t.Fatalf("expected count 10, got %d", resp.GetCount())
	}
}

func TestGRPC_Range_Error(t *testing.T) {
	mvccSvc := &mockMVCCService{
		rangeErr: &service.ReadError{Err: errors.New("range failed")},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.Range(context.Background(), &pb.RangeRequest{
		Start: []byte("a"), End: []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Range_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.Range(context.Background(), &pb.RangeRequest{
		Start: []byte("a"), End: []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_Compact_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.Compact(context.Background(), &pb.CompactRequest{Revision: 10})
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
}

func TestGRPC_Compact_Error(t *testing.T) {
	mvccSvc := &mockMVCCService{
		compactErr: &service.ProposeError{Err: errors.New("compact failed")},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.Compact(context.Background(), &pb.CompactRequest{Revision: 10})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Compact_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.Compact(context.Background(), &pb.CompactRequest{Revision: 10})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_CurrentRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCService{currentRev: 42}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	resp, err := client.CurrentRevision(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("CurrentRevision failed: %v", err)
	}
	if resp.GetRevision() != 42 {
		t.Fatalf("expected revision 42, got %d", resp.GetRevision())
	}
}

func TestGRPC_CurrentRevision_Error(t *testing.T) {
	mvccSvc := &mockMVCCService{
		currentRevErr: &service.ReadError{Err: errors.New("read failed")},
	}
	client, cleanup := startTestServerFull(t, WithMVCC(mvccSvc))
	defer cleanup()

	_, err := client.CurrentRevision(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_CurrentRevision_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.CurrentRevision(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Transaction handler tests
// ---------------------------------------------------------------------------

func TestGRPC_Transaction_Success(t *testing.T) {
	txSvc := &mockTransactionService{
		result: &service.TransactionResponse{
			Succeeded: true,
			Revision:  10,
			Responses: []service.OpResponse{
				{Succeeded: true},
			},
		},
	}
	client, cleanup := startTestServerFull(t, WithTransaction(txSvc))
	defer cleanup()

	resp, err := client.Transaction(context.Background(), &pb.TransactionRequest{
		Compares: []*pb.Compare{
			{
				Key:         []byte("k"),
				Target:      pb.CompareTarget_COMPARE_VERSION,
				Result:      pb.CompareResult_COMPARE_EQUAL,
				TargetUnion: &pb.Compare_Version{Version: 1},
			},
		},
		SuccessOps: []*pb.Op{
			{Type: pb.OpType_OP_PUT, Key: []byte("k"), Value: []byte("v2")},
		},
	})
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}
	if !resp.GetSucceeded() {
		t.Fatal("expected succeeded=true")
	}
	if resp.GetRevision() != 10 {
		t.Fatalf("expected revision 10, got %d", resp.GetRevision())
	}
	if len(resp.GetResponses()) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resp.GetResponses()))
	}
}

func TestGRPC_Transaction_Failure(t *testing.T) {
	txSvc := &mockTransactionService{
		result: &service.TransactionResponse{
			Succeeded: false,
			Revision:  10,
			Responses: []service.OpResponse{
				{Succeeded: true, Kvs: []*kv.KeyValue{{Key: []byte("k"), Value: []byte("v1")}}},
			},
		},
	}
	client, cleanup := startTestServerFull(t, WithTransaction(txSvc))
	defer cleanup()

	resp, err := client.Transaction(context.Background(), &pb.TransactionRequest{
		Compares: []*pb.Compare{
			{Key: []byte("k"), Target: pb.CompareTarget_COMPARE_VERSION, Result: pb.CompareResult_COMPARE_EQUAL, TargetUnion: &pb.Compare_Version{Version: 99}},
		},
		FailureOps: []*pb.Op{
			{Type: pb.OpType_OP_GET, Key: []byte("k")},
		},
	})
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}
	if resp.GetSucceeded() {
		t.Fatal("expected succeeded=false")
	}
}

func TestGRPC_Transaction_Error(t *testing.T) {
	txSvc := &mockTransactionService{
		err: &service.TransactionConflictError{Key: "k"},
	}
	client, cleanup := startTestServerFull(t, WithTransaction(txSvc))
	defer cleanup()

	_, err := client.Transaction(context.Background(), &pb.TransactionRequest{
		Compares: []*pb.Compare{
			{Key: []byte("k"), Target: pb.CompareTarget_COMPARE_VERSION, Result: pb.CompareResult_COMPARE_EQUAL, TargetUnion: &pb.Compare_Version{Version: 1}},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Aborted {
		t.Fatalf("expected Aborted, got %s", st.Code())
	}
}

func TestGRPC_Transaction_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.Transaction(context.Background(), &pb.TransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Batch handler tests
// ---------------------------------------------------------------------------

func TestGRPC_Batch_Success(t *testing.T) {
	batchSvc := &mockBatchService{
		result: []service.OpResponse{
			{Succeeded: true},
			{Succeeded: true},
		},
	}
	client, cleanup := startTestServerFull(t, WithBatch(batchSvc))
	defer cleanup()

	resp, err := client.Batch(context.Background(), &pb.BatchRequest{
		Ops: []*pb.Op{
			{Type: pb.OpType_OP_PUT, Key: []byte("a"), Value: []byte("1")},
			{Type: pb.OpType_OP_PUT, Key: []byte("b"), Value: []byte("2")},
		},
	})
	if err != nil {
		t.Fatalf("Batch failed: %v", err)
	}
	if len(resp.GetResponses()) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(resp.GetResponses()))
	}
}

func TestGRPC_Batch_Error(t *testing.T) {
	batchSvc := &mockBatchService{
		err: &service.BatchError{Index: 0, Err: errors.New("op failed")},
	}
	client, cleanup := startTestServerFull(t, WithBatch(batchSvc))
	defer cleanup()

	_, err := client.Batch(context.Background(), &pb.BatchRequest{
		Ops: []*pb.Op{
			{Type: pb.OpType_OP_PUT, Key: []byte("a"), Value: []byte("1")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Batch_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.Batch(context.Background(), &pb.BatchRequest{
		Ops: []*pb.Op{
			{Type: pb.OpType_OP_PUT, Key: []byte("a"), Value: []byte("1")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Lease handler tests
// ---------------------------------------------------------------------------

func TestGRPC_LeaseGrant_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		grantResult: &lease.Lease{ID: 1, TTL: 60, GrantedTTL: 60},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	resp, err := client.LeaseGrant(context.Background(), &pb.LeaseGrantRequest{Ttl: 60})
	if err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}
	if resp.GetLease().GetId() != 1 {
		t.Fatalf("expected lease id 1, got %d", resp.GetLease().GetId())
	}
	if resp.GetLease().GetTtl() != 60 {
		t.Fatalf("expected ttl 60, got %d", resp.GetLease().GetTtl())
	}
}

func TestGRPC_LeaseGrant_Error(t *testing.T) {
	leaseSvc := &mockLeaseService{
		grantErr: &service.LeaseExistsError{LeaseID: 1},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	_, err := client.LeaseGrant(context.Background(), &pb.LeaseGrantRequest{Ttl: 60})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestGRPC_LeaseGrant_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.LeaseGrant(context.Background(), &pb.LeaseGrantRequest{Ttl: 60})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LeaseRevoke_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	_, err := client.LeaseRevoke(context.Background(), &pb.LeaseRevokeRequest{LeaseId: 1})
	if err != nil {
		t.Fatalf("LeaseRevoke failed: %v", err)
	}
}

func TestGRPC_LeaseRevoke_Error(t *testing.T) {
	leaseSvc := &mockLeaseService{
		revokeErr: &service.LeaseNotFoundError{LeaseID: 99},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	_, err := client.LeaseRevoke(context.Background(), &pb.LeaseRevokeRequest{LeaseId: 99})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_LeaseRevoke_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.LeaseRevoke(context.Background(), &pb.LeaseRevokeRequest{LeaseId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LeaseKeepAlive_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		keepAliveResult: &lease.Lease{ID: 1, TTL: 60, RemainingTTL: 60},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	resp, err := client.LeaseKeepAlive(context.Background(), &pb.LeaseKeepAliveRequest{LeaseId: 1})
	if err != nil {
		t.Fatalf("LeaseKeepAlive failed: %v", err)
	}
	if resp.GetLease().GetRemainingTtl() != 60 {
		t.Fatalf("expected remaining_ttl 60, got %d", resp.GetLease().GetRemainingTtl())
	}
}

func TestGRPC_LeaseKeepAlive_Error(t *testing.T) {
	leaseSvc := &mockLeaseService{
		keepAliveErr: &service.LeaseExpiredError{LeaseID: 1},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	_, err := client.LeaseKeepAlive(context.Background(), &pb.LeaseKeepAliveRequest{LeaseId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_LeaseKeepAlive_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.LeaseKeepAlive(context.Background(), &pb.LeaseKeepAliveRequest{LeaseId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LeaseTimeToLive_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		ttlResult: &lease.Lease{
			ID:           1,
			TTL:          60,
			GrantedTTL:   120,
			RemainingTTL: 45,
			Keys:         [][]byte{[]byte("key1")},
		},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	resp, err := client.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{LeaseId: 1})
	if err != nil {
		t.Fatalf("LeaseTimeToLive failed: %v", err)
	}
	if resp.GetLease().GetRemainingTtl() != 45 {
		t.Fatalf("expected remaining_ttl 45, got %d", resp.GetLease().GetRemainingTtl())
	}
	if len(resp.GetLease().GetKeys()) != 1 {
		t.Fatalf("expected 1 key, got %d", len(resp.GetLease().GetKeys()))
	}
}

func TestGRPC_LeaseTimeToLive_Error(t *testing.T) {
	leaseSvc := &mockLeaseService{
		ttlErr: &service.LeaseNotFoundError{LeaseID: 99},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	_, err := client.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{LeaseId: 99})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_LeaseTimeToLive_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.LeaseTimeToLive(context.Background(), &pb.LeaseTimeToLiveRequest{LeaseId: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_LeaseList_Success(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesResult: []*lease.Lease{
			{ID: 1, TTL: 60},
			{ID: 2, TTL: 120},
		},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	resp, err := client.LeaseList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("LeaseList failed: %v", err)
	}
	if len(resp.GetLeases()) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(resp.GetLeases()))
	}
}

func TestGRPC_LeaseList_Empty(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesResult: []*lease.Lease{},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	resp, err := client.LeaseList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("LeaseList failed: %v", err)
	}
	if len(resp.GetLeases()) != 0 {
		t.Fatalf("expected 0 leases, got %d", len(resp.GetLeases()))
	}
}

func TestGRPC_LeaseList_Error(t *testing.T) {
	leaseSvc := &mockLeaseService{
		leasesErr: &service.ReadError{Err: errors.New("read failed")},
	}
	client, cleanup := startTestServerFull(t, WithLease(leaseSvc))
	defer cleanup()

	_, err := client.LeaseList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_LeaseList_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.LeaseList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Index handler tests
// ---------------------------------------------------------------------------

func TestGRPC_IndexCreate_Success(t *testing.T) {
	indexSvc := &mockIndexService{}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexCreate(context.Background(), &pb.IndexCreateRequest{
		Name:      "idx_email",
		FieldName: "email",
		Unique:    true,
		Sparse:    false,
	})
	if err != nil {
		t.Fatalf("IndexCreate failed: %v", err)
	}
}

func TestGRPC_IndexCreate_Error(t *testing.T) {
	indexSvc := &mockIndexService{
		createErr: &service.IndexExistsError{Name: "idx_email"},
	}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexCreate(context.Background(), &pb.IndexCreateRequest{
		Name: "idx_email", FieldName: "email",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestGRPC_IndexCreate_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IndexCreate(context.Background(), &pb.IndexCreateRequest{
		Name: "idx_email", FieldName: "email",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_IndexDrop_Success(t *testing.T) {
	indexSvc := &mockIndexService{}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexDrop(context.Background(), &pb.IndexDropRequest{Name: "idx_email"})
	if err != nil {
		t.Fatalf("IndexDrop failed: %v", err)
	}
}

func TestGRPC_IndexDrop_Error(t *testing.T) {
	indexSvc := &mockIndexService{
		dropErr: &service.IndexNotFoundError{Name: "idx_missing"},
	}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexDrop(context.Background(), &pb.IndexDropRequest{Name: "idx_missing"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_IndexDrop_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IndexDrop(context.Background(), &pb.IndexDropRequest{Name: "idx"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_IndexQuery_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		queryResult: [][]byte{[]byte("1"), []byte("2"), []byte("3")},
	}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	resp, err := client.IndexQuery(context.Background(), &pb.IndexQueryRequest{
		IndexName: "idx_email",
		Value:     []byte("test@example.com"),
	})
	if err != nil {
		t.Fatalf("IndexQuery failed: %v", err)
	}
	if len(resp.GetKeys()) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(resp.GetKeys()))
	}
}

func TestGRPC_IndexQuery_Error(t *testing.T) {
	indexSvc := &mockIndexService{
		queryErr: &service.IndexNotFoundError{Name: "idx_missing"},
	}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexQuery(context.Background(), &pb.IndexQueryRequest{
		IndexName: "idx_missing", Value: []byte("v"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_IndexQuery_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IndexQuery(context.Background(), &pb.IndexQueryRequest{
		IndexName: "idx", Value: []byte("v"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_IndexScan_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		scanResult: map[string][][]byte{
			"alice": {[]byte("1"), []byte("2")},
			"bob":   {[]byte("3")},
		},
	}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	resp, err := client.IndexScan(context.Background(), &pb.IndexScanRequest{
		IndexName: "idx_name",
		Start:     []byte("a"),
		End:       []byte("c"),
	})
	if err != nil {
		t.Fatalf("IndexScan failed: %v", err)
	}
	if len(resp.GetEntries()) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.GetEntries()))
	}
}

func TestGRPC_IndexScan_Error(t *testing.T) {
	indexSvc := &mockIndexService{
		scanErr: &service.IndexNotFoundError{Name: "idx_missing"},
	}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexScan(context.Background(), &pb.IndexScanRequest{
		IndexName: "idx_missing", Start: []byte("a"), End: []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_IndexScan_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IndexScan(context.Background(), &pb.IndexScanRequest{
		IndexName: "idx", Start: []byte("a"), End: []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TimeSeries handler tests
// ---------------------------------------------------------------------------

func TestGRPC_TSWrite_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	ts := time.Now().UnixNano()
	_, err := client.TSWrite(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "cpu.usage",
			Value:       42.5,
			TimestampNs: ts,
			Tags:        []*pb.TSTag{{Key: "host", Value: "node1"}},
		},
	})
	if err != nil {
		t.Fatalf("TSWrite failed: %v", err)
	}
}

func TestGRPC_TSWrite_NilPoint(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWrite(context.Background(), &pb.TSWriteRequest{})
	if err == nil {
		t.Fatal("expected error for nil point, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_TSWrite_Error(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		writeErr: &service.TimeSeriesError{Op: "write", Err: errors.New("write failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWrite(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{Metric: "m", Value: 1, TimestampNs: 1000},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSWrite_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSWrite(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{Metric: "m", Value: 1, TimestampNs: 1000},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSWriteBatch_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWriteBatch(context.Background(), &pb.TSWriteBatchRequest{
		Points: []*pb.TSDataPoint{
			{Metric: "cpu", Value: 42.5, TimestampNs: 1000},
			{Metric: "mem", Value: 1024, TimestampNs: 2000},
		},
	})
	if err != nil {
		t.Fatalf("TSWriteBatch failed: %v", err)
	}
}

func TestGRPC_TSWriteBatch_Error(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		writeBatchErr: &service.TimeSeriesError{Op: "write_batch", Err: errors.New("batch failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWriteBatch(context.Background(), &pb.TSWriteBatchRequest{
		Points: []*pb.TSDataPoint{
			{Metric: "cpu", Value: 42.5, TimestampNs: 1000},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSWriteBatch_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSWriteBatch(context.Background(), &pb.TSWriteBatchRequest{
		Points: []*pb.TSDataPoint{{Metric: "m", Value: 1, TimestampNs: 1000}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSQuery_Success(t *testing.T) {
	now := time.Now()
	tsSvc := &mockTimeSeriesService{
		queryResult: []timeseries.DataPoint{
			{Metric: "cpu", Value: 42.5, Timestamp: now, Tags: map[string]string{"host": "n1"}},
			{Metric: "cpu", Value: 45.0, Timestamp: now.Add(time.Second)},
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSQuery(context.Background(), &pb.TSQueryRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Minute).UnixNano(),
		EndNs:   now.Add(time.Minute).UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSQuery failed: %v", err)
	}
	if len(resp.GetPoints()) != 2 {
		t.Fatalf("expected 2 points, got %d", len(resp.GetPoints()))
	}
}

func TestGRPC_TSQuery_WithOptions(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryResult: []timeseries.DataPoint{
			{Metric: "cpu", Value: 42.5, Timestamp: time.Now()},
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSQuery(context.Background(), &pb.TSQueryRequest{
		Metric:     "cpu",
		StartNs:    1000,
		EndNs:      2000,
		Tags:       []*pb.TSTag{{Key: "host", Value: "n1"}},
		Limit:      10,
		Descending: true,
	})
	if err != nil {
		t.Fatalf("TSQuery with options failed: %v", err)
	}
	if len(resp.GetPoints()) != 1 {
		t.Fatalf("expected 1 point, got %d", len(resp.GetPoints()))
	}
}

func TestGRPC_TSQuery_Error(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		queryErr: &service.TimeSeriesError{Op: "query", Err: errors.New("query failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSQuery(context.Background(), &pb.TSQueryRequest{
		Metric: "cpu", StartNs: 1000, EndNs: 2000,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSQuery_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSQuery(context.Background(), &pb.TSQueryRequest{
		Metric: "cpu", StartNs: 1000, EndNs: 2000,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSAggregate_Success(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tsSvc := &mockTimeSeriesService{
		aggregateResult: []timeseries.AggResult{
			{Timestamp: ts, Value: 42.5, Count: 100},
			{Timestamp: ts.Add(time.Hour), Value: 45.0, Count: 120},
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSAggregate(context.Background(), &pb.TSAggregateRequest{
		Metric:     "cpu",
		StartNs:    ts.Add(-time.Hour).UnixNano(),
		EndNs:      ts.Add(2 * time.Hour).UnixNano(),
		Function:   pb.AggFunc_AGG_AVG,
		IntervalNs: int64(time.Hour),
	})
	if err != nil {
		t.Fatalf("TSAggregate failed: %v", err)
	}
	if len(resp.GetResults()) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.GetResults()))
	}
	if resp.GetResults()[0].GetValue() != 42.5 {
		t.Fatalf("expected value 42.5, got %f", resp.GetResults()[0].GetValue())
	}
}

func TestGRPC_TSAggregate_Error(t *testing.T) {
	tsSvc := &mockTimeSeriesService{
		aggregateErr: &service.TimeSeriesError{Op: "aggregate", Err: errors.New("agg failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSAggregate(context.Background(), &pb.TSAggregateRequest{
		Metric: "cpu", StartNs: 1000, EndNs: 2000, Function: pb.AggFunc_AGG_SUM, IntervalNs: 1000,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSAggregate_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSAggregate(context.Background(), &pb.TSAggregateRequest{
		Metric: "cpu", StartNs: 1000, EndNs: 2000, Function: pb.AggFunc_AGG_AVG, IntervalNs: 1000,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// Watch handler tests (streaming)
// ---------------------------------------------------------------------------

func TestGRPC_Watch_SingleEvent(t *testing.T) {
	w := newMockWatcher(1, 10)
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Send an event on the watcher channel.
	w.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 5,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("k"), Value: []byte("v1")},
			},
		},
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if resp.GetWatchId() != 1 {
		t.Fatalf("expected watch_id 1, got %d", resp.GetWatchId())
	}
	if len(resp.GetEvents()) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.GetEvents()))
	}
	if resp.GetEvents()[0].GetType() != pb.EventType_EVENT_PUT {
		t.Fatalf("expected EVENT_PUT, got %v", resp.GetEvents()[0].GetType())
	}
	if resp.GetRevision() != 5 {
		t.Fatalf("expected revision 5, got %d", resp.GetRevision())
	}

	cancel()
}

func TestGRPC_Watch_MultipleEvents(t *testing.T) {
	w := newMockWatcher(2, 10)
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Send two events.
	w.events <- watch.WatchResponse{
		WatchID:  2,
		Revision: 10,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("k"), Value: []byte("v1")}},
		},
	}
	w.events <- watch.WatchResponse{
		WatchID:  2,
		Revision: 11,
		Events: []*watch.Event{
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte("k")}},
		},
	}

	resp1, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 1 failed: %v", err)
	}
	if resp1.GetRevision() != 10 {
		t.Fatalf("expected revision 10, got %d", resp1.GetRevision())
	}

	resp2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv 2 failed: %v", err)
	}
	if resp2.GetRevision() != 11 {
		t.Fatalf("expected revision 11, got %d", resp2.GetRevision())
	}
	if resp2.GetEvents()[0].GetType() != pb.EventType_EVENT_DELETE {
		t.Fatalf("expected EVENT_DELETE, got %v", resp2.GetEvents()[0].GetType())
	}

	cancel()
}

func TestGRPC_Watch_PrefixWatch(t *testing.T) {
	w := newMockWatcher(3, 10)
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// When RangeEnd is set, the server calls WatchPrefix.
	stream, err := client.Watch(ctx, &pb.WatchRequest{
		Key:      []byte("prefix/"),
		RangeEnd: []byte("prefix0"),
	})
	if err != nil {
		t.Fatalf("Watch with prefix failed: %v", err)
	}

	w.events <- watch.WatchResponse{
		WatchID:  3,
		Revision: 1,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("prefix/a"), Value: []byte("v")}},
		},
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if resp.GetWatchId() != 3 {
		t.Fatalf("expected watch_id 3, got %d", resp.GetWatchId())
	}

	cancel()
}

func TestGRPC_Watch_ChannelClosed(t *testing.T) {
	w := newMockWatcher(4, 10)
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Close the channel to signal end of watch.
	w.closed = true
	close(w.events)

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error after channel close, got nil")
	}
	// Should get EOF or a status error.
	if err != io.EOF {
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error or EOF, got %T: %v", err, err)
		}
		// The server returns nil when watcher has no error and channel is closed,
		// which translates to EOF on the client side. If the watcher has an error,
		// the server maps it.
		_ = st
	}
}

func TestGRPC_Watch_ChannelClosedWithError(t *testing.T) {
	w := newMockWatcher(5, 10)
	w.err = &service.WatchCanceledError{Reason: "compacted"}
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Close the channel with an error set on the watcher.
	w.closed = true
	close(w.events)

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		// Could be EOF wrapped.
		return
	}
	if st.Code() != codes.Canceled {
		t.Fatalf("expected Canceled, got %s", st.Code())
	}
}

func TestGRPC_Watch_ServiceError(t *testing.T) {
	watchSvc := &mockWatchServiceImpl{
		err: &service.ReadError{Err: errors.New("watch creation failed")},
	}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch call failed: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_Watch_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch call failed: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_Watch_CanceledWatchResponse(t *testing.T) {
	w := newMockWatcher(6, 10)
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Send a canceled watch response.
	w.events <- watch.WatchResponse{
		WatchID:      6,
		Canceled:     true,
		CancelReason: "compacted",
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if !resp.GetCanceled() {
		t.Fatal("expected canceled=true")
	}
	if resp.GetCancelReason() != "compacted" {
		t.Fatalf("expected cancel_reason 'compacted', got %q", resp.GetCancelReason())
	}

	// After canceled response, server should close stream.
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error after canceled response, got nil")
	}
}

func TestGRPC_Watch_WithOptions(t *testing.T) {
	w := newMockWatcher(7, 10)
	watchSvc := &mockWatchServiceImpl{watcher: w}
	client, cleanup := startTestServerFull(t, WithWatch(watchSvc))
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.Watch(ctx, &pb.WatchRequest{
		Key:            []byte("k"),
		StartRevision:  5,
		PrevKv:         true,
		ProgressNotify: true,
	})
	if err != nil {
		t.Fatalf("Watch with options failed: %v", err)
	}

	w.events <- watch.WatchResponse{
		WatchID:  7,
		Revision: 6,
		Events: []*watch.Event{
			{
				Type:   watch.EventPut,
				Kv:     &kv.KeyValue{Key: []byte("k"), Value: []byte("new")},
				PrevKv: &kv.KeyValue{Key: []byte("k"), Value: []byte("old")},
			},
		},
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if resp.GetEvents()[0].GetPrevKv() == nil {
		t.Fatal("expected non-nil PrevKv")
	}
	if !bytes.Equal(resp.GetEvents()[0].GetPrevKv().GetValue(), []byte("old")) {
		t.Fatalf("expected prev value 'old', got %q", resp.GetEvents()[0].GetPrevKv().GetValue())
	}

	cancel()
}

// ---------------------------------------------------------------------------
// Stream interceptor tests
// ---------------------------------------------------------------------------

func TestStreamRecoveryInterceptor_NoPanic(t *testing.T) {
	handler := func(_ any, _ grpc.ServerStream) error {
		return nil
	}
	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}

	err := streamRecoveryInterceptor(nil, nil, info, handler)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestStreamRecoveryInterceptor_Panic(t *testing.T) {
	handler := func(_ any, _ grpc.ServerStream) error {
		panic("stream panic")
	}
	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}

	err := streamRecoveryInterceptor(nil, nil, info, handler)
	if err == nil {
		t.Fatal("expected error after panic, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal code, got %s", st.Code())
	}
}

func TestStreamRecoveryInterceptor_HandlerError(t *testing.T) {
	handler := func(_ any, _ grpc.ServerStream) error {
		return status.Error(codes.NotFound, "not found")
	}
	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}

	err := streamRecoveryInterceptor(nil, nil, info, handler)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// mockServerStream is a minimal grpc.ServerStream for testing.
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

func TestStreamLoggingInterceptor_Success(t *testing.T) {
	handler := func(_ any, _ grpc.ServerStream) error {
		return nil
	}
	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}
	ss := &mockServerStream{ctx: context.Background()}

	err := streamLoggingInterceptor(nil, ss, info, handler)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestStreamLoggingInterceptor_Error(t *testing.T) {
	handler := func(_ any, _ grpc.ServerStream) error {
		return status.Error(codes.NotFound, "not found")
	}
	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}
	ss := &mockServerStream{ctx: context.Background()}

	err := streamLoggingInterceptor(nil, ss, info, handler)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T", err)
	}
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestStreamLoggingInterceptor_NonGRPCError(t *testing.T) {
	handler := func(_ any, _ grpc.ServerStream) error {
		return errors.New("plain error")
	}
	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}
	ss := &mockServerStream{ctx: context.Background()}

	err := streamLoggingInterceptor(nil, ss, info, handler)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Enum conversion map coverage tests
// ---------------------------------------------------------------------------

func TestProtoSortOrderMap_AllEntries(t *testing.T) {
	expected := map[pb.SortOrder]service.SortOrder{
		pb.SortOrder_SORT_NONE:    service.SortNone,
		pb.SortOrder_SORT_ASCEND:  service.SortAscend,
		pb.SortOrder_SORT_DESCEND: service.SortDescend,
	}
	for pbVal, svcVal := range expected {
		got, ok := protoSortOrderMap[pbVal]
		if !ok {
			t.Fatalf("missing entry for %v", pbVal)
		}
		if got != svcVal {
			t.Fatalf("expected %v, got %v for %v", svcVal, got, pbVal)
		}
	}
}

func TestProtoSortTargetMap_AllEntries(t *testing.T) {
	expected := map[pb.SortTarget]service.SortTarget{
		pb.SortTarget_SORT_BY_KEY:             service.SortByKey,
		pb.SortTarget_SORT_BY_VERSION:         service.SortByVersion,
		pb.SortTarget_SORT_BY_CREATE_REVISION: service.SortByCreateRevision,
		pb.SortTarget_SORT_BY_MOD_REVISION:    service.SortByModRevision,
		pb.SortTarget_SORT_BY_VALUE:           service.SortByValue,
	}
	for pbVal, svcVal := range expected {
		got, ok := protoSortTargetMap[pbVal]
		if !ok {
			t.Fatalf("missing entry for %v", pbVal)
		}
		if got != svcVal {
			t.Fatalf("expected %v, got %v for %v", svcVal, got, pbVal)
		}
	}
}

func TestProtoCompareTargetMap_AllEntries(t *testing.T) {
	expected := map[pb.CompareTarget]service.CompareTarget{
		pb.CompareTarget_COMPARE_VERSION:         service.CompareVersion,
		pb.CompareTarget_COMPARE_CREATE_REVISION: service.CompareCreateRevision,
		pb.CompareTarget_COMPARE_MOD_REVISION:    service.CompareModRevision,
		pb.CompareTarget_COMPARE_VALUE:           service.CompareValue,
	}
	for pbVal, svcVal := range expected {
		got, ok := protoCompareTargetMap[pbVal]
		if !ok {
			t.Fatalf("missing entry for %v", pbVal)
		}
		if got != svcVal {
			t.Fatalf("expected %v, got %v for %v", svcVal, got, pbVal)
		}
	}
}

func TestProtoCompareResultMap_AllEntries(t *testing.T) {
	expected := map[pb.CompareResult]service.CompareResult{
		pb.CompareResult_COMPARE_EQUAL:     service.CompareEqual,
		pb.CompareResult_COMPARE_GREATER:   service.CompareGreater,
		pb.CompareResult_COMPARE_LESS:      service.CompareLess,
		pb.CompareResult_COMPARE_NOT_EQUAL: service.CompareNotEqual,
	}
	for pbVal, svcVal := range expected {
		got, ok := protoCompareResultMap[pbVal]
		if !ok {
			t.Fatalf("missing entry for %v", pbVal)
		}
		if got != svcVal {
			t.Fatalf("expected %v, got %v for %v", svcVal, got, pbVal)
		}
	}
}

func TestProtoOpTypeMap_AllEntries(t *testing.T) {
	expected := map[pb.OpType]service.OpType{
		pb.OpType_OP_PUT:    service.OpPut,
		pb.OpType_OP_GET:    service.OpGet,
		pb.OpType_OP_DELETE: service.OpDelete,
		pb.OpType_OP_RANGE:  service.OpRange,
	}
	for pbVal, svcVal := range expected {
		got, ok := protoOpTypeMap[pbVal]
		if !ok {
			t.Fatalf("missing entry for %v", pbVal)
		}
		if got != svcVal {
			t.Fatalf("expected %v, got %v for %v", svcVal, got, pbVal)
		}
	}
}

func TestProtoEventTypeMap_AllEntries(t *testing.T) {
	expected := map[watch.EventType]pb.EventType{
		watch.EventPut:    pb.EventType_EVENT_PUT,
		watch.EventDelete: pb.EventType_EVENT_DELETE,
	}
	for watchVal, pbVal := range expected {
		got, ok := protoEventTypeMap[watchVal]
		if !ok {
			t.Fatalf("missing entry for %v", watchVal)
		}
		if got != pbVal {
			t.Fatalf("expected %v, got %v for %v", pbVal, got, watchVal)
		}
	}
}

func TestProtoAggFuncMap_AllEntries(t *testing.T) {
	expected := map[pb.AggFunc]timeseries.AggFunc{
		pb.AggFunc_AGG_AVG:        timeseries.AggAvg,
		pb.AggFunc_AGG_SUM:        timeseries.AggSum,
		pb.AggFunc_AGG_MIN:        timeseries.AggMin,
		pb.AggFunc_AGG_MAX:        timeseries.AggMax,
		pb.AggFunc_AGG_COUNT:      timeseries.AggCount,
		pb.AggFunc_AGG_PERCENTILE: timeseries.AggPercentile,
	}
	for pbVal, tsVal := range expected {
		got, ok := protoAggFuncMap[pbVal]
		if !ok {
			t.Fatalf("missing entry for %v", pbVal)
		}
		if got != tsVal {
			t.Fatalf("expected %v, got %v for %v", tsVal, got, pbVal)
		}
	}
}
