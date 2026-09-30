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

package quic

import (
	"context"
	"io"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
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
// mockKVService
// ---------------------------------------------------------------------------

type mockKVService struct {
	putFn                 func(ctx context.Context, key string, value []byte) error
	getFn                 func(ctx context.Context, key string) ([]byte, error)
	deleteFn              func(ctx context.Context, key string) error
	listFn                func(ctx context.Context, prefix string) ([]string, error)
	scanFn                func(ctx context.Context, prefix string) (map[string][]byte, error)
	getStaleFn            func(ctx context.Context, key string) ([]byte, error)
	listStaleFn           func(ctx context.Context, prefix string) ([]string, error)
	scanStaleFn           func(ctx context.Context, prefix string) (map[string][]byte, error)
	putEntityFn           func(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error)
	getEntityFn           func(ctx context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error)
	deleteEntityFn        func(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error)
	listEntitiesFn        func(ctx context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error)
	putWithModeFn         func(ctx context.Context, key string, value []byte, mode writemode.Mode) error
	deleteWithModeFn      func(ctx context.Context, key string, mode writemode.Mode) error
	getWithConsistencyFn  func(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error)
	listWithConsistencyFn func(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error)
	scanWithConsistencyFn func(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error)

	// The guarded write verbs (go-qrdb#124). Injectable like every other
	// method here: the handlers exist to DELEGATE, so a test has to be able to
	// observe the arguments that arrived and choose the answer that goes back.
	// Stubbed constants would let a handler that dropped its request, or sent
	// `next` where `expected` belongs, return the same plausible boolean.
	putIfAbsentFn      func(ctx context.Context, key string, value []byte) (bool, error)
	compareAndDeleteFn func(ctx context.Context, key string, expected []byte) (bool, error)
	compareAndSwapFn   func(ctx context.Context, key string, expected, next []byte) (bool, error)
}

func (m *mockKVService) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	return nil
}

// PutIfAbsent reports a successful create unless the test injects otherwise.
// This mock keeps no state, so there is nothing for the condition to consult.
func (m *mockKVService) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if m.putIfAbsentFn != nil {
		return m.putIfAbsentFn(ctx, key, value)
	}
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if m.compareAndDeleteFn != nil {
		return m.compareAndDeleteFn(ctx, key, expected)
	}
	return false, nil
}

func (m *mockKVService) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, nil
}

func (m *mockKVService) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockKVService) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if m.deleteWithModeFn != nil {
		return m.deleteWithModeFn(ctx, key, mode)
	}
	return nil
}

func (m *mockKVService) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if m.putWithModeFn != nil {
		return m.putWithModeFn(ctx, key, value, mode)
	}
	return nil
}

func (m *mockKVService) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	if m.getWithConsistencyFn != nil {
		return m.getWithConsistencyFn(ctx, key, opts)
	}
	return nil, nil
}

func (m *mockKVService) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	if m.listWithConsistencyFn != nil {
		return m.listWithConsistencyFn(ctx, prefix, opts)
	}
	return nil, nil
}

func (m *mockKVService) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	if m.scanWithConsistencyFn != nil {
		return m.scanWithConsistencyFn(ctx, prefix, opts)
	}
	return nil, nil
}

func (m *mockKVService) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

func (m *mockKVService) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	if m.getStaleFn != nil {
		return m.getStaleFn(ctx, key)
	}
	return nil, nil
}

func (m *mockKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	if m.listStaleFn != nil {
		return m.listStaleFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanStaleFn != nil {
		return m.scanStaleFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVService) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	if m.putEntityFn != nil {
		return m.putEntityFn(ctx, req)
	}
	return nil, nil
}

func (m *mockKVService) GetEntity(ctx context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	if m.getEntityFn != nil {
		return m.getEntityFn(ctx, req)
	}
	return nil, nil
}

func (m *mockKVService) DeleteEntity(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	if m.deleteEntityFn != nil {
		return m.deleteEntityFn(ctx, req)
	}
	return nil, nil
}

func (m *mockKVService) ListEntities(ctx context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	if m.listEntitiesFn != nil {
		return m.listEntitiesFn(ctx, req)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockAdminService
// ---------------------------------------------------------------------------

type mockAdminService struct {
	// addIdentity records the member identity the last AddMember received.
	addIdentity       string
	healthFn          func(ctx context.Context) (*service.HealthStatus, error)
	statusFn          func(ctx context.Context) (*service.StatusInfo, error)
	peersFn           func(ctx context.Context) (*service.PeersInfo, error)
	addMemberFn       func(ctx context.Context, shardID, replicaID uint64, raftAddr string) error
	removeMemberFn    func(ctx context.Context, shardID, replicaID uint64) error
	requestSnapshotFn func(ctx context.Context, shardID uint64) error
	drainFn           func(ctx context.Context) error
	capabilitiesFn    func(ctx context.Context) (*capabilities.Capabilities, error)
	startShardFn      func(ctx context.Context, req *service.StartShardRequest) error
	stopShardFn       func(ctx context.Context, shardID uint64) error
	getShardStateFn   func(ctx context.Context, shardID uint64) (*service.ShardStateResponse, error)
	getTopologyFn     func(ctx context.Context) (*service.TopologyResponse, error)
}

func (m *mockAdminService) Health(ctx context.Context) (*service.HealthStatus, error) {
	if m.healthFn != nil {
		return m.healthFn(ctx)
	}
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(ctx context.Context) (*service.StatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.StatusInfo{}, nil
}

func (m *mockAdminService) Peers(ctx context.Context) (*service.PeersInfo, error) {
	if m.peersFn != nil {
		return m.peersFn(ctx)
	}
	return &service.PeersInfo{}, nil
}

func (m *mockAdminService) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	m.addIdentity = identity
	if m.addMemberFn != nil {
		return m.addMemberFn(ctx, shardID, replicaID, raftAddr)
	}
	return nil
}

func (m *mockAdminService) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if m.removeMemberFn != nil {
		return m.removeMemberFn(ctx, shardID, replicaID)
	}
	return nil
}

func (m *mockAdminService) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if m.requestSnapshotFn != nil {
		return m.requestSnapshotFn(ctx, shardID)
	}
	return nil
}

func (m *mockAdminService) Drain(ctx context.Context) error {
	if m.drainFn != nil {
		return m.drainFn(ctx)
	}
	return nil
}

func (m *mockAdminService) Capabilities(ctx context.Context) (*capabilities.Capabilities, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx)
	}
	return &capabilities.Capabilities{KV: true}, nil
}

func (m *mockAdminService) GetShardState(ctx context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if m.getShardStateFn != nil {
		return m.getShardStateFn(ctx, shardID)
	}
	return &service.ShardStateResponse{}, nil
}

func (m *mockAdminService) GetTopology(ctx context.Context) (*service.TopologyResponse, error) {
	if m.getTopologyFn != nil {
		return m.getTopologyFn(ctx)
	}
	return &service.TopologyResponse{}, nil
}

func (m *mockAdminService) StartShard(ctx context.Context, req *service.StartShardRequest) error {
	if m.startShardFn != nil {
		return m.startShardFn(ctx, req)
	}
	return nil
}

func (m *mockAdminService) StopShard(ctx context.Context, shardID uint64) error {
	if m.stopShardFn != nil {
		return m.stopShardFn(ctx, shardID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// mockMVCCKVService
// ---------------------------------------------------------------------------

type mockMVCCKVService struct {
	mockKVService
	putWithLeaseFn                 func(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error)
	getRevisionFn                  func(ctx context.Context, key string) (*kv.KeyValue, error)
	getAtRevisionFn                func(ctx context.Context, key string, revision int64) (*kv.KeyValue, error)
	rangeFn                        func(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error)
	compactFn                      func(ctx context.Context, revision int64) error
	currentRevisionFn              func(ctx context.Context) (int64, error)
	putWithLeaseAndModeFn          func(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error)
	getRevisionWithConsistencyFn   func(ctx context.Context, key string, opts consistency.ReadOptions) (*kv.KeyValue, error)
	getAtRevisionWithConsistencyFn func(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*kv.KeyValue, error)
	rangeWithConsistencyFn         func(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error)
	restoreToRevisionFn            func(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error)
}

func (m *mockMVCCKVService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	if m.putWithLeaseFn != nil {
		return m.putWithLeaseFn(ctx, key, value, leaseID)
	}
	return &kv.KeyValue{}, nil
}

func (m *mockMVCCKVService) GetRevision(ctx context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevisionFn != nil {
		return m.getRevisionFn(ctx, key)
	}
	return &kv.KeyValue{}, nil
}

func (m *mockMVCCKVService) GetAtRevision(ctx context.Context, key string, revision int64) (*kv.KeyValue, error) {
	if m.getAtRevisionFn != nil {
		return m.getAtRevisionFn(ctx, key, revision)
	}
	return &kv.KeyValue{}, nil
}

func (m *mockMVCCKVService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeFn != nil {
		return m.rangeFn(ctx, start, end, opts...)
	}
	return nil, 0, 0, nil
}

func (m *mockMVCCKVService) Compact(ctx context.Context, revision int64) error {
	if m.compactFn != nil {
		return m.compactFn(ctx, revision)
	}
	return nil
}

func (m *mockMVCCKVService) CurrentRevision(ctx context.Context) (int64, error) {
	if m.currentRevisionFn != nil {
		return m.currentRevisionFn(ctx)
	}
	return 0, nil
}

func (m *mockMVCCKVService) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*kv.KeyValue, error) {
	if m.putWithLeaseAndModeFn != nil {
		return m.putWithLeaseAndModeFn(ctx, key, value, leaseID, mode)
	}
	return &kv.KeyValue{}, nil
}

func (m *mockMVCCKVService) GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*kv.KeyValue, error) {
	if m.getRevisionWithConsistencyFn != nil {
		return m.getRevisionWithConsistencyFn(ctx, key, opts)
	}
	return &kv.KeyValue{}, nil
}

func (m *mockMVCCKVService) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*kv.KeyValue, error) {
	if m.getAtRevisionWithConsistencyFn != nil {
		return m.getAtRevisionWithConsistencyFn(ctx, key, revision, opts)
	}
	return &kv.KeyValue{}, nil
}

func (m *mockMVCCKVService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	if m.rangeWithConsistencyFn != nil {
		return m.rangeWithConsistencyFn(ctx, start, end, opts, rangeOpts...)
	}
	return nil, 0, 0, nil
}

// ---------------------------------------------------------------------------
// mockTransactionService
// ---------------------------------------------------------------------------

type mockTransactionService struct {
	executeFn       func(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error)
	executeEntityFn func(ctx context.Context, req *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error)
}

func (m *mockTransactionService) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, compares, successOps, failureOps)
	}
	return &service.TransactionResponse{}, nil
}

func (m *mockTransactionService) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return &service.TransactionResponse{}, nil
}

func (m *mockTransactionService) ExecuteEntity(ctx context.Context, req *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	if m.executeEntityFn != nil {
		return m.executeEntityFn(ctx, req)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockBatchService
// ---------------------------------------------------------------------------

type mockBatchService struct {
	executeFn       func(ctx context.Context, ops []service.Op) ([]service.OpResponse, error)
	executeEntityFn func(ctx context.Context, req *service.BatchEntityRequest) (*service.BatchEntityResponse, error)
}

func (m *mockBatchService) Execute(ctx context.Context, ops []service.Op) ([]service.OpResponse, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, ops)
	}
	return nil, nil
}

func (m *mockBatchService) ExecuteEntity(ctx context.Context, req *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	if m.executeEntityFn != nil {
		return m.executeEntityFn(ctx, req)
	}
	return nil, nil
}

func (m *mockBatchService) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return nil, nil
}

func (m *mockBatchService) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockWatchService
// ---------------------------------------------------------------------------

type mockWatchService struct {
	watchFn           func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchPrefixFn     func(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchEntityFn     func(ctx context.Context, req *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error)
	watchEntityTypeFn func(ctx context.Context, req *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error)
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

func (m *mockWatchService) WatchEntity(ctx context.Context, req *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	if m.watchEntityFn != nil {
		return m.watchEntityFn(ctx, req)
	}
	return nil, nil
}

func (m *mockWatchService) WatchEntityType(ctx context.Context, req *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	if m.watchEntityTypeFn != nil {
		return m.watchEntityTypeFn(ctx, req)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockLeaseService
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
	return &lease.Lease{}, nil
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
	return &lease.Lease{}, nil
}

func (m *mockLeaseService) TimeToLive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	if m.timeToLiveFn != nil {
		return m.timeToLiveFn(ctx, leaseID)
	}
	return &lease.Lease{}, nil
}

func (m *mockLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	if m.leasesFn != nil {
		return m.leasesFn(ctx)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// mockIndexService
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

// DropOwnedIndex answers as DropIndex does: this double records no
// ownership, and no test that uses it exercises an owner-checked drop.
func (m *mockIndexService) DropOwnedIndex(ctx context.Context, name, _ string) error {
	return m.DropIndex(ctx, name)
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
// mockTimeSeriesService
// ---------------------------------------------------------------------------

type mockTimeSeriesService struct {
	writeFn                      func(ctx context.Context, metric string, value float64, timestamp time.Time, tags map[string]string) error
	writeBatchFn                 func(ctx context.Context, points []timeseries.DataPoint) error
	queryFn                      func(ctx context.Context, metric string, start, end time.Time, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	queryWithConsistencyFn       func(ctx context.Context, metric string, start, end time.Time, ropts consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error)
	aggregateFn                  func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration) ([]timeseries.AggResult, error)
	aggregateWithConsistencyFn   func(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, ropts consistency.ReadOptions) ([]timeseries.AggResult, error)
	writePointFn                 func(ctx context.Context, point *timeseries.DataPoint) error
	queryRichFn                  func(ctx context.Context, req *timeseries.QueryRequest) (*timeseries.QueryResult, error)
	queryRichWithConsistencyFn   func(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error)
	deleteSeriesFn               func(ctx context.Context, req *timeseries.DeleteRequest) error
	getRetentionFn               func(metric string) (*timeseries.RetentionPolicy, error)
	setRetentionFn               func(metric string, policy *timeseries.RetentionPolicy) error
	listMetricsFn                func(prefix string, limit int) ([]string, error)
	listMetricsWithConsistencyFn func(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error)
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
	return nil, nil
}

func (m *mockTimeSeriesService) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, ropts consistency.ReadOptions) (*timeseries.QueryResult, error) {
	if m.queryRichWithConsistencyFn != nil {
		return m.queryRichWithConsistencyFn(ctx, req, ropts)
	}
	return m.QueryRich(ctx, req)
}

func (m *mockTimeSeriesService) DeleteSeries(ctx context.Context, req *timeseries.DeleteRequest) error {
	if m.deleteSeriesFn != nil {
		return m.deleteSeriesFn(ctx, req)
	}
	return nil
}

func (m *mockTimeSeriesService) GetRetentionPolicy(metric string) (*timeseries.RetentionPolicy, error) {
	if m.getRetentionFn != nil {
		return m.getRetentionFn(metric)
	}
	return nil, nil
}

func (m *mockTimeSeriesService) SetRetentionPolicy(_ context.Context, metric string, policy *timeseries.RetentionPolicy) error {
	if m.setRetentionFn != nil {
		return m.setRetentionFn(metric, policy)
	}
	return nil
}

func (m *mockTimeSeriesService) ListMetrics(prefix string, limit int) ([]string, error) {
	if m.listMetricsFn != nil {
		return m.listMetricsFn(prefix, limit)
	}
	return nil, nil
}

func (m *mockTimeSeriesService) ListMetricsWithConsistency(ctx context.Context, prefix string, limit int, ropts consistency.ReadOptions) ([]string, error) {
	if m.listMetricsWithConsistencyFn != nil {
		return m.listMetricsWithConsistencyFn(ctx, prefix, limit, ropts)
	}
	return m.ListMetrics(prefix, limit)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestServer creates a Server with mock KV and admin services plus
// optional service overrides. It does not start the listener.
func newTestServer(kvSvc service.KVServicer, adminSvc service.AdminServicer, opts ...ServerOption) *Server {
	return NewServer(kvSvc, adminSvc, "localhost:0", opts...)
}

// testError is a simple typed error for test assertions.
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// RestoreToRevision satisfies service.MVCCKVServicer.
func (m *mockMVCCKVService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if m.restoreToRevisionFn != nil {
		return m.restoreToRevisionFn(ctx, req)
	}
	return &service.RestoreRevisionResult{}, nil
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (m *mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if m.compareAndSwapFn != nil {
		return m.compareAndSwapFn(ctx, key, expected, next)
	}
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
