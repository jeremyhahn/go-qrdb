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
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// protoSortOrderMap converts proto SortOrder values to service SortOrder.
var protoSortOrderMap = map[pb.SortOrder]service.SortOrder{
	pb.SortOrder_SORT_NONE:    service.SortNone,
	pb.SortOrder_SORT_ASCEND:  service.SortAscend,
	pb.SortOrder_SORT_DESCEND: service.SortDescend,
}

// protoSortTargetMap converts proto SortTarget values to service SortTarget.
var protoSortTargetMap = map[pb.SortTarget]service.SortTarget{
	pb.SortTarget_SORT_BY_KEY:             service.SortByKey,
	pb.SortTarget_SORT_BY_VERSION:         service.SortByVersion,
	pb.SortTarget_SORT_BY_CREATE_REVISION: service.SortByCreateRevision,
	pb.SortTarget_SORT_BY_MOD_REVISION:    service.SortByModRevision,
	pb.SortTarget_SORT_BY_VALUE:           service.SortByValue,
}

// protoCompareTargetMap converts proto CompareTarget values to service CompareTarget.
var protoCompareTargetMap = map[pb.CompareTarget]service.CompareTarget{
	pb.CompareTarget_COMPARE_VERSION:         service.CompareVersion,
	pb.CompareTarget_COMPARE_CREATE_REVISION: service.CompareCreateRevision,
	pb.CompareTarget_COMPARE_MOD_REVISION:    service.CompareModRevision,
	pb.CompareTarget_COMPARE_VALUE:           service.CompareValue,
}

// protoCompareResultMap converts proto CompareResult values to service CompareResult.
var protoCompareResultMap = map[pb.CompareResult]service.CompareResult{
	pb.CompareResult_COMPARE_EQUAL:     service.CompareEqual,
	pb.CompareResult_COMPARE_GREATER:   service.CompareGreater,
	pb.CompareResult_COMPARE_LESS:      service.CompareLess,
	pb.CompareResult_COMPARE_NOT_EQUAL: service.CompareNotEqual,
}

// protoOpTypeMap converts proto OpType values to service OpType.
var protoOpTypeMap = map[pb.OpType]service.OpType{
	pb.OpType_OP_PUT:    service.OpPut,
	pb.OpType_OP_GET:    service.OpGet,
	pb.OpType_OP_DELETE: service.OpDelete,
	pb.OpType_OP_RANGE:  service.OpRange,
}

// protoEventTypeMap converts watch EventType values to proto EventType.
var protoEventTypeMap = map[watch.EventType]pb.EventType{
	watch.EventPut:    pb.EventType_EVENT_PUT,
	watch.EventDelete: pb.EventType_EVENT_DELETE,
}

// protoAggFuncMap converts proto AggFunc values to timeseries AggFunc.
var protoAggFuncMap = map[pb.AggFunc]timeseries.AggFunc{
	pb.AggFunc_AGG_AVG:        timeseries.AggAvg,
	pb.AggFunc_AGG_SUM:        timeseries.AggSum,
	pb.AggFunc_AGG_MIN:        timeseries.AggMin,
	pb.AggFunc_AGG_MAX:        timeseries.AggMax,
	pb.AggFunc_AGG_COUNT:      timeseries.AggCount,
	pb.AggFunc_AGG_PERCENTILE: timeseries.AggPercentile,
}

// aggFuncToProtoMap converts timeseries AggFunc values to proto AggFunc.
var aggFuncToProtoMap = map[timeseries.AggFunc]pb.AggFunc{
	timeseries.AggAvg:        pb.AggFunc_AGG_AVG,
	timeseries.AggSum:        pb.AggFunc_AGG_SUM,
	timeseries.AggMin:        pb.AggFunc_AGG_MIN,
	timeseries.AggMax:        pb.AggFunc_AGG_MAX,
	timeseries.AggCount:      pb.AggFunc_AGG_COUNT,
	timeseries.AggPercentile: pb.AggFunc_AGG_PERCENTILE,
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// kvToProto converts a service-layer KeyValue to its protobuf representation.
func kvToProto(k *kv.KeyValue) *pb.KeyValue {
	if k == nil {
		return nil
	}
	return &pb.KeyValue{
		Key:            k.Key,
		Value:          k.Value,
		CreateRevision: k.CreateRevision,
		ModRevision:    k.ModRevision,
		Version:        k.Version,
		LeaseId:        k.LeaseID,
	}
}

// protoToCompare converts a proto Compare to a service Compare.
func protoToCompare(c *pb.Compare) service.Compare {
	sc := service.Compare{
		Key:    c.GetKey(),
		Target: protoCompareTargetMap[c.GetTarget()],
		Result: protoCompareResultMap[c.GetResult()],
	}

	switch c.GetTarget() {
	case pb.CompareTarget_COMPARE_VERSION:
		sc.Version = c.GetVersion()
	case pb.CompareTarget_COMPARE_CREATE_REVISION:
		sc.CreateRevision = c.GetCreateRevision()
	case pb.CompareTarget_COMPARE_MOD_REVISION:
		sc.ModRevision = c.GetModRevision()
	case pb.CompareTarget_COMPARE_VALUE:
		sc.Value = c.GetValue()
	}

	return sc
}

// protoToOp converts a proto Op to a service Op.
func protoToOp(op *pb.Op) service.Op {
	return service.Op{
		Type:     protoOpTypeMap[op.GetType()],
		Key:      op.GetKey(),
		Value:    op.GetValue(),
		RangeEnd: op.GetRangeEnd(),
		LeaseID:  op.GetLeaseId(),
	}
}

// opResponseToProto converts a service OpResponse to its protobuf representation.
func opResponseToProto(r service.OpResponse) *pb.OpResponse {
	pbKvs := make([]*pb.KeyValue, len(r.Kvs))
	for i, k := range r.Kvs {
		pbKvs[i] = kvToProto(k)
	}
	return &pb.OpResponse{
		Kvs:       pbKvs,
		Count:     r.Count,
		Succeeded: r.Succeeded,
	}
}

// leaseToProto converts a service-layer Lease to its protobuf representation.
func leaseToProto(l *lease.Lease) *pb.Lease {
	if l == nil {
		return nil
	}
	return &pb.Lease{
		Id:           l.ID,
		Ttl:          l.TTL,
		GrantedTtl:   l.GrantedTTL,
		RemainingTtl: l.RemainingTTL,
		Keys:         l.Keys,
	}
}

// eventToProto converts a watch Event to its protobuf representation.
func eventToProto(e *watch.Event) *pb.Event {
	if e == nil {
		return nil
	}
	return &pb.Event{
		Type:   protoEventTypeMap[e.Type],
		Kv:     kvToProto(e.Kv),
		PrevKv: kvToProto(e.PrevKv),
	}
}

// protoTagsToMap converts repeated TSTag proto messages to a Go map.
func protoTagsToMap(tags []*pb.TSTag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.GetKey()] = t.GetValue()
	}
	return m
}

// mapToProtoTags converts a Go map to repeated TSTag proto messages.
func mapToProtoTags(m map[string]string) []*pb.TSTag {
	if len(m) == 0 {
		return nil
	}
	tags := make([]*pb.TSTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, &pb.TSTag{Key: k, Value: v})
	}
	return tags
}

// dataPointToProto converts a service-layer DataPoint to its protobuf representation.
func dataPointToProto(dp timeseries.DataPoint) *pb.TSDataPoint {
	return &pb.TSDataPoint{
		Metric:      dp.Metric,
		Value:       dp.Value,
		TimestampNs: dp.Timestamp.UnixNano(),
		Tags:        mapToProtoTags(dp.Tags),
	}
}

// dataPointPtrToProto converts a pointer to a service-layer DataPoint to its
// protobuf representation.
func dataPointPtrToProto(dp *timeseries.DataPoint) *pb.TSDataPoint {
	return &pb.TSDataPoint{
		Metric:      dp.Metric,
		Value:       dp.Value,
		TimestampNs: dp.Timestamp.UnixNano(),
		Tags:        mapToProtoTags(dp.Tags),
	}
}

// protoToDataPoint converts a proto TSDataPoint to a service-layer DataPoint.
func protoToDataPoint(dp *pb.TSDataPoint) timeseries.DataPoint {
	return timeseries.DataPoint{
		Metric:    dp.GetMetric(),
		Value:     dp.GetValue(),
		Timestamp: time.Unix(0, dp.GetTimestampNs()),
		Tags:      protoTagsToMap(dp.GetTags()),
	}
}

// aggResultToProto converts a service-layer AggResult to its protobuf representation.
func aggResultToProto(r timeseries.AggResult) *pb.TSAggResult {
	return &pb.TSAggResult{
		TimestampNs: r.Timestamp.UnixNano(),
		Value:       r.Value,
		Count:       r.Count,
	}
}

// ---------------------------------------------------------------------------
// Service server
// ---------------------------------------------------------------------------

// qrdbServiceServer implements the generated QRDBServiceServer interface.
// It delegates all operations to the service layer through KVService and
// AdminService interfaces, with optional MVCC, Transaction, Batch, Watch,
// Lease, Lock, Index, TimeSeries, Vector, and Database services. When an
// optional service is nil, its handlers return codes.Unimplemented.
type qrdbServiceServer struct {
	pb.UnimplementedQRDBServiceServer
	kvSvc         service.KVServicer
	adminSvc      service.AdminServicer
	mvccSvc       service.MVCCKVServicer
	txSvc         service.TransactionServicer
	batchSvc      service.BatchServicer
	watchSvc      service.WatchServicer
	leaseSvc      lease.LeaseServicer
	lockSvc       service.LockServicer
	indexSvc      index.IndexServicer
	tsSvc         timeseries.TimeSeriesServicer
	vectorSvc     service.VectorServicer
	embeddingSvc  service.EmbeddingServicer
	ragSvc        service.RAGServicer
	databaseSvc   service.DatabaseServicer
	barrierSvc    service.BarrierServicer
	encMigrator   service.EncryptionMigrator
	pinBackend    pin.PINBackend
	ceremonySvc   service.CeremonyServicer
	ceremonyCA    ceremony.CeremonyCA
	credentialSvc service.CredentialServicer
	tenantSvc     tenant.TenantServicer
	userSvc       user.UserServicer
	roleSvc       role.RoleServicer
	certProv      cert.CertProvider
	collectionSvc service.CollectionServicer
	backupSvc     service.BackupServicer
	rebalanceSvc  service.RebalanceServicer
	sqlSvc        service.SQLServicer

	// userAuthFactorSvc is a registration carrier only: RegisterService
	// registers it as the separate qrdb.v1.UserAuthFactorService (see
	// RegisterUserAuthFactorService). No QRDBService RPC dispatches to it.
	userAuthFactorSvc service.UserAuthFactorServicer

	// registry, when set, resolves data-plane services per (tenant, database)
	// scope. When nil the singleton service fields above are used (embedded /
	// single-database deployments).
	registry service.Registry
}

// ---------------------------------------------------------------------------
// KV handlers (existing)
// ---------------------------------------------------------------------------

// Put stores a key-value pair via Raft consensus.
func (s *qrdbServiceServer) Put(ctx context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}
	if mode, ok := writeModeFromProto(req.GetWriteMode()); ok {
		if err := kvSvc.PutWithMode(ctx, req.GetKey(), req.GetValue(), mode); err != nil {
			return nil, mapServiceError(err)
		}
		return &pb.PutResponse{}, nil
	}
	if err := kvSvc.Put(ctx, req.GetKey(), req.GetValue()); err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.PutResponse{}, nil
}

// Get retrieves a value by key using a linearizable read.
func (s *qrdbServiceServer) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}
	var (
		data []byte
		err  error
	)
	if opts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		data, err = kvSvc.GetWithConsistency(ctx, req.GetKey(), opts)
	} else {
		data, err = kvSvc.Get(ctx, req.GetKey())
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.GetResponse{Value: data}, nil
}

// Delete removes a key via Raft consensus.
func (s *qrdbServiceServer) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}
	if mode, ok := writeModeFromProto(req.GetWriteMode()); ok {
		if err := kvSvc.DeleteWithMode(ctx, req.GetKey(), mode); err != nil {
			return nil, mapServiceError(err)
		}
		return &pb.DeleteResponse{}, nil
	}
	if err := kvSvc.Delete(ctx, req.GetKey()); err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.DeleteResponse{}, nil
}

// List returns sorted keys matching a prefix.
func (s *qrdbServiceServer) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}
	var (
		keys []string
		err  error
	)
	if opts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		keys, err = kvSvc.ListWithConsistency(ctx, req.GetPrefix(), opts)
	} else {
		keys, err = kvSvc.List(ctx, req.GetPrefix())
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.ListResponse{Keys: keys}, nil
}

// Scan returns key-value pairs matching a prefix.
func (s *qrdbServiceServer) Scan(ctx context.Context, req *pb.ScanRequest) (*pb.ScanResponse, error) {
	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}
	var (
		data map[string][]byte
		err  error
	)
	if opts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		data, err = kvSvc.ScanWithConsistency(ctx, req.GetPrefix(), opts)
	} else {
		data, err = kvSvc.Scan(ctx, req.GetPrefix())
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.ScanResponse{Pairs: data}, nil
}

// ---------------------------------------------------------------------------
// Admin handlers (existing)
// ---------------------------------------------------------------------------

// Health returns the node health status.
//
// admin.Health is on the sealed-window allowlist (pkg/api/sealedwindow.Table
// VerbHealth), so this handler is reachable BEFORE adminSvc exists (adminSvc
// is nil throughout the pre-host sealed window -- cmd/qrdbd/seal_subsystem.go's
// awaitUnseal only wires the barrier/ceremony/credential services). Rather
// than fail the RPC in that state, report the documented "sealed"
// HealthStatus verdict directly: sdk/go/transport/grpc and .../unix's
// Connect() require a successful Health RPC before returning, so erroring
// here would make a perfectly reachable, correctly-sealed daemon look
// unreachable to every gRPC and Unix-socket client.
func (s *qrdbServiceServer) Health(ctx context.Context, _ *emptypb.Empty) (*pb.HealthResponse, error) {
	if s.adminSvc == nil {
		return &pb.HealthResponse{Status: service.HealthStatusSealed}, nil
	}
	info, err := s.adminSvc.Health(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.HealthResponse{
		Status: info.Status,
		Leader: info.Leader,
	}, nil
}

// Status returns detailed node status information.
func (s *qrdbServiceServer) Status(ctx context.Context, _ *emptypb.Empty) (*pb.StatusResponse, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	info, err := s.adminSvc.Status(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.StatusResponse{
		NodeId:      info.NodeID,
		RaftAddress: info.RaftAddress,
		IsLeader:    info.IsLeader,
		LeaderId:    info.LeaderID,
		ShardCount:  int32(info.ShardCount),
		IsDraining:  info.IsDraining,
	}, nil
}

// Peers returns the peer list for all loaded shards.
func (s *qrdbServiceServer) Peers(ctx context.Context, _ *emptypb.Empty) (*pb.PeersResponse, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	info, err := s.adminSvc.Peers(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	shards := make([]*pb.ShardPeers, 0, len(info.Shards))
	for _, sp := range info.Shards {
		peers := make([]*pb.PeerInfo, 0, len(sp.Peers))
		for _, p := range sp.Peers {
			peers = append(peers, &pb.PeerInfo{
				ReplicaId: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			})
		}
		shards = append(shards, &pb.ShardPeers{
			ShardId:  sp.ShardID,
			LeaderId: sp.LeaderID,
			Peers:    peers,
		})
	}

	return &pb.PeersResponse{Shards: shards}, nil
}

// AddMember adds a new node to a shard.
func (s *qrdbServiceServer) AddMember(ctx context.Context, req *pb.AddMemberRequest) (*emptypb.Empty, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	if err := s.adminSvc.AddMember(ctx, req.GetShardId(), req.GetReplicaId(), req.GetRaftAddress()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// RemoveMember removes a node from a shard.
func (s *qrdbServiceServer) RemoveMember(ctx context.Context, req *pb.RemoveMemberRequest) (*emptypb.Empty, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	if err := s.adminSvc.RemoveMember(ctx, req.GetShardId(), req.GetReplicaId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// RequestSnapshot triggers a snapshot for the given shard.
func (s *qrdbServiceServer) RequestSnapshot(ctx context.Context, req *pb.RequestSnapshotMessage) (*emptypb.Empty, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	if err := s.adminSvc.RequestSnapshot(ctx, req.GetShardId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// Drain transitions the node into drain mode.
func (s *qrdbServiceServer) Drain(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	if err := s.adminSvc.Drain(ctx); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// Capabilities returns the compile-time feature set of the node.
func (s *qrdbServiceServer) Capabilities(ctx context.Context, _ *pb.CapabilitiesRequest) (*pb.CapabilitiesResponse, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	caps, err := s.adminSvc.Capabilities(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.CapabilitiesResponse{
		Mode:        caps.Mode,
		Kv:          caps.KV,
		Mvcc:        caps.MVCC,
		Transaction: caps.Transaction,
		Batch:       caps.Batch,
		Watch:       caps.Watch,
		Lease:       caps.Lease,
		Lock:        caps.Lock,
		Index:       caps.Index,
		Cluster:     caps.Cluster,
		Backup:      caps.Backup,
		Rebalance:   caps.Rebalance,
		TimeSeries:  caps.TimeSeries,
		Vector:      caps.Vector,
		Sql:         caps.SQL,
		Mysql:       caps.MySQL,
		Postgresql:  caps.PostgreSQL,
		Etcd:        caps.Etcd,
		Fts:         caps.FTS,
	}, nil
}

// StartShard initializes and starts a Raft group for the given shard.
func (s *qrdbServiceServer) StartShard(ctx context.Context, req *pb.StartShardRequest) (*emptypb.Empty, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	svcReq := &service.StartShardRequest{
		ShardID:   req.GetShardId(),
		ReplicaID: req.GetReplicaId(),
		Members:   req.GetMembers(),
		Join:      req.GetJoin(),
	}
	if err := s.adminSvc.StartShard(ctx, svcReq); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// StopShard stops the Raft group for the given shard.
func (s *qrdbServiceServer) StopShard(ctx context.Context, req *pb.StopShardRequest) (*emptypb.Empty, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	if err := s.adminSvc.StopShard(ctx, req.GetShardId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// GetShardState returns the Raft state for a shard.
func (s *qrdbServiceServer) GetShardState(ctx context.Context, req *pb.GetShardStateRequest) (*pb.ShardStateResponse, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	resp, err := s.adminSvc.GetShardState(ctx, req.GetShardId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.ShardStateResponse{
		ShardId:           resp.ShardID,
		ConfigChangeIndex: resp.ConfigChangeIndex,
		Term:              resp.Term,
		LeaderId:          resp.LeaderID,
		HasLeader:         resp.HasLeader,
	}, nil
}

// GetTopology returns the full cluster topology.
func (s *qrdbServiceServer) GetTopology(ctx context.Context, _ *emptypb.Empty) (*pb.TopologyResponse, error) {
	if s.adminSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "admin"}).Error())
	}
	resp, err := s.adminSvc.GetTopology(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	shards := make([]*pb.ShardInfoProto, 0, len(resp.Shards))
	for _, si := range resp.Shards {
		shards = append(shards, &pb.ShardInfoProto{
			ShardId:   si.ShardID,
			ReplicaId: si.ReplicaID,
			IsLeader:  si.IsLeader,
			LeaderId:  si.LeaderID,
			Nodes:     si.Nodes,
		})
	}
	return &pb.TopologyResponse{
		NodeId:      resp.NodeID,
		RaftAddress: resp.RaftAddress,
		ShardCount:  int32(resp.ShardCount),
		Shards:      shards,
	}, nil
}

// ---------------------------------------------------------------------------
// MVCC handlers
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease.
func (s *qrdbServiceServer) PutWithLease(ctx context.Context, req *pb.PutWithLeaseRequest) (*pb.PutWithLeaseResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	var (
		result *service.KeyValue
		err    error
	)
	if mode, ok := writeModeFromProto(req.GetWriteMode()); ok {
		result, err = mvccSvc.PutWithLeaseAndMode(ctx, req.GetKey(), req.GetValue(), req.GetLeaseId(), mode)
	} else {
		result, err = mvccSvc.PutWithLease(ctx, req.GetKey(), req.GetValue(), req.GetLeaseId())
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.PutWithLeaseResponse{Kv: kvToProto(result)}, nil
}

// GetRevision retrieves a key-value pair with full MVCC revision metadata.
func (s *qrdbServiceServer) GetRevision(ctx context.Context, req *pb.GetRevisionRequest) (*pb.GetRevisionResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	var (
		result *service.KeyValue
		err    error
	)
	if opts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		result, err = mvccSvc.GetRevisionWithConsistency(ctx, req.GetKey(), opts)
	} else {
		result, err = mvccSvc.GetRevision(ctx, req.GetKey())
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.GetRevisionResponse{Kv: kvToProto(result)}, nil
}

// GetAtRevision retrieves a key-value pair at a historical revision.
func (s *qrdbServiceServer) GetAtRevision(ctx context.Context, req *pb.GetAtRevisionRequest) (*pb.GetAtRevisionResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	var (
		result *service.KeyValue
		err    error
	)
	if opts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		result, err = mvccSvc.GetAtRevisionWithConsistency(ctx, req.GetKey(), req.GetRevision(), opts)
	} else {
		result, err = mvccSvc.GetAtRevision(ctx, req.GetKey(), req.GetRevision())
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.GetAtRevisionResponse{Kv: kvToProto(result)}, nil
}

// Range returns key-value pairs in the half-open interval [start, end).
func (s *qrdbServiceServer) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	// An absent range start must be rejected rather than forwarded: a nil lower
	// bound otherwise denotes the start of the shared store, which would scan
	// across the (tenant, db) namespace boundary. This matches the REST handler,
	// which rejects an empty start with 400.
	if len(req.GetStart()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "start is required")
	}

	var opts []service.RangeOption

	if req.GetLimit() > 0 {
		opts = append(opts, service.WithLimit(req.GetLimit()))
	}
	if req.GetRevision() > 0 {
		opts = append(opts, service.WithRevision(req.GetRevision()))
	}
	if req.GetCountOnly() {
		opts = append(opts, service.WithCountOnly())
	}
	if req.GetKeysOnly() {
		opts = append(opts, service.WithKeysOnly())
	}
	if req.GetSortOrder() != pb.SortOrder_SORT_NONE || req.GetSortTarget() != pb.SortTarget_SORT_BY_KEY {
		order := protoSortOrderMap[req.GetSortOrder()]
		target := protoSortTargetMap[req.GetSortTarget()]
		opts = append(opts, service.WithSort(order, target))
	}

	var (
		kvs      []*service.KeyValue
		count    int64
		revision int64
		err      error
	)
	if ropts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		kvs, count, revision, err = mvccSvc.RangeWithConsistency(ctx, req.GetStart(), req.GetEnd(), ropts, opts...)
	} else {
		kvs, count, revision, err = mvccSvc.Range(ctx, req.GetStart(), req.GetEnd(), opts...)
	}
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbKvs := make([]*pb.KeyValue, len(kvs))
	for i, k := range kvs {
		pbKvs[i] = kvToProto(k)
	}

	return &pb.RangeResponse{Kvs: pbKvs, Count: count, Revision: revision}, nil
}

// Compact discards all MVCC revisions older than the specified revision.
func (s *qrdbServiceServer) Compact(ctx context.Context, req *pb.CompactRequest) (*emptypb.Empty, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	if err := mvccSvc.Compact(ctx, req.GetRevision()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// CurrentRevision returns the latest committed MVCC revision.
func (s *qrdbServiceServer) CurrentRevision(ctx context.Context, _ *emptypb.Empty) (*pb.CurrentRevisionResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	rev, err := mvccSvc.CurrentRevision(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.CurrentRevisionResponse{Revision: rev}, nil
}

// ---------------------------------------------------------------------------
// Transaction handler
// ---------------------------------------------------------------------------

// Transaction executes an atomic compare-and-swap transaction.
func (s *qrdbServiceServer) Transaction(ctx context.Context, req *pb.TransactionRequest) (*pb.TransactionResponse, error) {
	txSvc, svcErr := s.txSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	compares := make([]service.Compare, len(req.GetCompares()))
	for i, c := range req.GetCompares() {
		compares[i] = protoToCompare(c)
	}

	successOps := make([]service.Op, len(req.GetSuccessOps()))
	for i, op := range req.GetSuccessOps() {
		successOps[i] = protoToOp(op)
	}

	failureOps := make([]service.Op, len(req.GetFailureOps()))
	for i, op := range req.GetFailureOps() {
		failureOps[i] = protoToOp(op)
	}

	result, err := txSvc.Execute(ctx, compares, successOps, failureOps)
	if err != nil {
		return nil, mapServiceError(err)
	}

	responses := make([]*pb.OpResponse, len(result.Responses))
	for i, r := range result.Responses {
		responses[i] = opResponseToProto(r)
	}

	return &pb.TransactionResponse{
		Succeeded: result.Succeeded,
		Revision:  result.Revision,
		Responses: responses,
	}, nil
}

// ---------------------------------------------------------------------------
// Batch handler
// ---------------------------------------------------------------------------

// Batch executes multiple operations atomically.
func (s *qrdbServiceServer) Batch(ctx context.Context, req *pb.BatchRequest) (*pb.BatchResponse, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	ops := make([]service.Op, len(req.GetOps()))
	for i, op := range req.GetOps() {
		ops[i] = protoToOp(op)
	}

	results, err := batchSvc.Execute(ctx, ops)
	if err != nil {
		return nil, mapServiceError(err)
	}

	responses := make([]*pb.OpResponse, len(results))
	for i, r := range results {
		responses[i] = opResponseToProto(r)
	}

	return &pb.BatchResponse{Responses: responses}, nil
}

// BatchImport parses and imports key-value pairs from the file carried in
// the request. It resolves the batch service through the SAME per-(tenant,
// database) scope as Batch, so the namespaced wrapper's KeyPrefix is applied
// to every imported key exactly as it is for every other write to the
// database. The file must fit within the server's configured max receive
// message size (see WithMaxImportMessageSize).
func (s *qrdbServiceServer) BatchImport(ctx context.Context, req *pb.BatchImportRequest) (*pb.BatchImportResponse, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	opts := service.ImportOptions{
		Format:        req.GetFormat(),
		Filename:      req.GetFilename(),
		KeyField:      req.GetKeyField(),
		ValueMode:     req.GetValueMode(),
		ValueField:    req.GetValueField(),
		SkipHeader:    req.GetSkipHeader(),
		Transactional: req.GetTransactional(),
	}

	result, err := batchSvc.Import(ctx, bytes.NewReader(req.GetFile()), opts)
	if err != nil {
		return nil, mapServiceError(err)
	}

	errs := make([]*pb.BatchImportError, len(result.Errors))
	for i, e := range result.Errors {
		errs[i] = &pb.BatchImportError{
			Row:     int32(e.Row),
			Key:     e.Key,
			Message: e.Message,
		}
	}

	return &pb.BatchImportResponse{
		Total:     int32(result.Total),
		Succeeded: int32(result.Succeeded),
		Failed:    int32(result.Failed),
		Errors:    errs,
	}, nil
}

// ---------------------------------------------------------------------------
// Watch handler (server streaming)
// ---------------------------------------------------------------------------

// Watch creates a server-streaming watcher that delivers events for the
// specified key or prefix.
func (s *qrdbServiceServer) Watch(req *pb.WatchRequest, stream grpc.ServerStreamingServer[pb.WatchResponse]) error {
	ctx := stream.Context()
	watchSvc, svcErr := s.watchSvcFor(ctx)
	if svcErr != nil {
		return mapServiceError(svcErr)
	}

	// Build watch options from the request.
	var opts []watch.WatchOption
	if req.GetStartRevision() > 0 {
		opts = append(opts, watch.WithStartRevision(req.GetStartRevision()))
	}
	if req.GetPrevKv() {
		opts = append(opts, watch.WithPrevKV())
	}
	if req.GetProgressNotify() {
		opts = append(opts, watch.WithProgressNotify())
	}

	// Determine whether this is a single key or prefix watch.
	var w watch.Watcher
	var err error

	if len(req.GetRangeEnd()) > 0 {
		w, err = watchSvc.WatchPrefix(ctx, string(req.GetKey()), opts...)
	} else {
		w, err = watchSvc.Watch(ctx, string(req.GetKey()), opts...)
	}
	if err != nil {
		return mapServiceError(err)
	}
	defer func() {
		_ = w.Close()
	}()

	// Stream events to the client until the context is canceled or
	// the watcher's event channel is closed.
	for {
		select {
		case <-ctx.Done():
			return status.Error(codes.Canceled, ctx.Err().Error())
		case resp, ok := <-w.Events():
			if !ok {
				// Channel closed. Check for watcher error.
				if wErr := w.Err(); wErr != nil {
					return mapServiceError(wErr)
				}
				return nil
			}

			events := make([]*pb.Event, len(resp.Events))
			for i, e := range resp.Events {
				events[i] = eventToProto(e)
			}

			pbResp := &pb.WatchResponse{
				WatchId:      resp.WatchID,
				Events:       events,
				Revision:     resp.Revision,
				Canceled:     resp.Canceled,
				CancelReason: resp.CancelReason,
			}

			if sendErr := stream.Send(pbResp); sendErr != nil {
				return sendErr
			}

			// If the server canceled the watch, exit after sending
			// the cancellation notification.
			if resp.Canceled {
				return nil
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Lease handlers
// ---------------------------------------------------------------------------

// LeaseGrant creates a new lease with the specified TTL.
func (s *qrdbServiceServer) LeaseGrant(ctx context.Context, req *pb.LeaseGrantRequest) (*pb.LeaseGrantResponse, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	l, err := leaseSvc.Grant(ctx, req.GetTtl())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LeaseGrantResponse{Lease: leaseToProto(l)}, nil
}

// LeaseRevoke immediately expires a lease and deletes attached keys.
func (s *qrdbServiceServer) LeaseRevoke(ctx context.Context, req *pb.LeaseRevokeRequest) (*emptypb.Empty, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	if err := leaseSvc.Revoke(ctx, req.GetLeaseId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// LeaseKeepAlive resets the TTL on an existing lease.
func (s *qrdbServiceServer) LeaseKeepAlive(ctx context.Context, req *pb.LeaseKeepAliveRequest) (*pb.LeaseKeepAliveResponse, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	l, err := leaseSvc.KeepAlive(ctx, req.GetLeaseId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LeaseKeepAliveResponse{Lease: leaseToProto(l)}, nil
}

// LeaseTimeToLive returns the current state of a lease.
func (s *qrdbServiceServer) LeaseTimeToLive(ctx context.Context, req *pb.LeaseTimeToLiveRequest) (*pb.LeaseTimeToLiveResponse, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	l, err := leaseSvc.TimeToLive(ctx, req.GetLeaseId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LeaseTimeToLiveResponse{Lease: leaseToProto(l)}, nil
}

// LeaseList returns all active leases.
func (s *qrdbServiceServer) LeaseList(ctx context.Context, _ *emptypb.Empty) (*pb.LeaseListResponse, error) {
	leaseSvc, svcErr := s.leaseSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	leases, err := leaseSvc.Leases(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbLeases := make([]*pb.Lease, len(leases))
	for i, l := range leases {
		pbLeases[i] = leaseToProto(l)
	}
	return &pb.LeaseListResponse{Leases: pbLeases}, nil
}

// ---------------------------------------------------------------------------
// Lock handlers
// ---------------------------------------------------------------------------

// lockToProto converts a lock.Lock to its protobuf representation.
func lockToProto(l *lock.Lock) *pb.LockInfo {
	if l == nil {
		return nil
	}
	return &pb.LockInfo{
		Key:          l.Key,
		FencingToken: l.FencingToken,
		LeaseId:      l.LeaseID,
		HolderId:     l.HolderID,
		Reentrant:    l.Reentrant,
		AcquireCount: l.AcquireCount,
		Ttl:          l.TTL,
		AcquiredAt:   l.AcquiredAt,
	}
}

// LockAcquire acquires a distributed lock, blocking until acquired or the
// context is cancelled.
func (s *qrdbServiceServer) LockAcquire(ctx context.Context, req *pb.LockAcquireRequest) (*pb.LockAcquireResponse, error) {
	if s.lockSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "lock"}).Error())
	}

	l, err := s.lockSvc.Lock(ctx, req.GetKey(), req.GetHolderId(), time.Duration(req.GetTtl())*time.Second, req.GetReentrant())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LockAcquireResponse{Lock: lockToProto(l)}, nil
}

// LockTryLock attempts to acquire a lock immediately without blocking.
func (s *qrdbServiceServer) LockTryLock(ctx context.Context, req *pb.LockTryLockRequest) (*pb.LockTryLockResponse, error) {
	if s.lockSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "lock"}).Error())
	}

	l, err := s.lockSvc.TryLock(ctx, req.GetKey(), req.GetHolderId(), time.Duration(req.GetTtl())*time.Second, req.GetReentrant())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LockTryLockResponse{Lock: lockToProto(l)}, nil
}

// LockRelease releases a held lock. The fencing token must match.
func (s *qrdbServiceServer) LockRelease(ctx context.Context, req *pb.LockReleaseRequest) (*emptypb.Empty, error) {
	if s.lockSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "lock"}).Error())
	}

	if err := s.lockSvc.Unlock(ctx, req.GetKey(), req.GetFencingToken()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// LockExtend refreshes the TTL on a held lock without releasing it.
func (s *qrdbServiceServer) LockExtend(ctx context.Context, req *pb.LockExtendRequest) (*pb.LockExtendResponse, error) {
	if s.lockSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "lock"}).Error())
	}

	l, err := s.lockSvc.Extend(ctx, req.GetKey(), req.GetFencingToken())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LockExtendResponse{Lock: lockToProto(l)}, nil
}

// LockStatus returns the current lock state for a key.
func (s *qrdbServiceServer) LockStatus(ctx context.Context, req *pb.LockStatusRequest) (*pb.LockStatusResponse, error) {
	if s.lockSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "lock"}).Error())
	}

	l, err := s.lockSvc.Status(ctx, req.GetKey())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.LockStatusResponse{Lock: lockToProto(l)}, nil
}

// LockList returns all active locks, optionally filtered by prefix.
func (s *qrdbServiceServer) LockList(ctx context.Context, req *pb.LockListRequest) (*pb.LockListResponse, error) {
	if s.lockSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "lock"}).Error())
	}

	locks, err := s.lockSvc.List(ctx, req.GetPrefix())
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbLocks := make([]*pb.LockInfo, len(locks))
	for i, l := range locks {
		pbLocks[i] = lockToProto(l)
	}
	return &pb.LockListResponse{Locks: pbLocks}, nil
}

// ---------------------------------------------------------------------------
// Index handlers
// ---------------------------------------------------------------------------

// IndexCreate creates a new secondary index.
func (s *qrdbServiceServer) IndexCreate(ctx context.Context, req *pb.IndexCreateRequest) (*emptypb.Empty, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	cfg := index.IndexConfig{
		Unique: req.GetUnique(),
		Sparse: req.GetSparse(),
		// index.IndexConfig names the field for the DEFAULT, so its zero value
		// is case-INSENSITIVE. It must be set explicitly at every construction
		// site now that the engine derives the index's case behaviour from the
		// config instead of hardcoding CaseSensitive:true (go-qrdb#34).
		CaseSensitive: !req.GetCaseInsensitive(),
	}
	if req.GetNumeric() {
		cfg.ValueType = index.IndexValueTypeNumeric
	}
	if err := indexSvc.CreateIndex(ctx, req.GetName(), req.GetFieldName(), cfg); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// IndexDrop removes a secondary index.
func (s *qrdbServiceServer) IndexDrop(ctx context.Context, req *pb.IndexDropRequest) (*emptypb.Empty, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	if err := indexSvc.DropIndex(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// IndexQuery returns entry keys matching an exact value in an index.
func (s *qrdbServiceServer) IndexQuery(ctx context.Context, req *pb.IndexQueryRequest) (*pb.IndexQueryResponse, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	keys, err := indexSvc.Query(ctx, req.GetIndexName(), req.GetValue())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.IndexQueryResponse{Keys: keys}, nil
}

// IndexScan returns entries in an index within a value range.
func (s *qrdbServiceServer) IndexScan(ctx context.Context, req *pb.IndexScanRequest) (*pb.IndexScanResponse, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	result, err := indexSvc.Scan(ctx, req.GetIndexName(), req.GetStart(), req.GetEnd())
	if err != nil {
		return nil, mapServiceError(err)
	}

	entries := make([]*pb.IndexScanEntry, 0, len(result))
	for value, keys := range result {
		entries = append(entries, &pb.IndexScanEntry{
			Value: value,
			Keys:  keys,
		})
	}
	return &pb.IndexScanResponse{Entries: entries}, nil
}

// IndexList returns the names of all registered indexes.
func (s *qrdbServiceServer) IndexList(ctx context.Context, _ *emptypb.Empty) (*pb.IndexListResponse, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	names, err := indexSvc.ListIndexes(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.IndexListResponse{Names: names}, nil
}

// ---------------------------------------------------------------------------
// TimeSeries handlers
// ---------------------------------------------------------------------------

// TSWrite stores a single time-series data point.
func (s *qrdbServiceServer) TSWrite(ctx context.Context, req *pb.TSWriteRequest) (*emptypb.Empty, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	p := req.GetPoint()
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "point is required")
	}

	ts := time.Unix(0, p.GetTimestampNs())
	tags := protoTagsToMap(p.GetTags())

	if err := tsSvc.Write(ctx, p.GetMetric(), p.GetValue(), ts, tags); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// TSWriteBatch stores multiple time-series data points atomically.
func (s *qrdbServiceServer) TSWriteBatch(ctx context.Context, req *pb.TSWriteBatchRequest) (*emptypb.Empty, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	points := make([]timeseries.DataPoint, len(req.GetPoints()))
	for i, p := range req.GetPoints() {
		points[i] = protoToDataPoint(p)
	}

	if err := tsSvc.WriteBatch(ctx, points); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// TSQuery retrieves data points for a metric within a time range.
func (s *qrdbServiceServer) TSQuery(ctx context.Context, req *pb.TSQueryRequest) (*pb.TSQueryResponse, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	start := time.Unix(0, req.GetStartNs())
	end := time.Unix(0, req.GetEndNs())

	var opts []timeseries.TSQueryOption

	if tags := protoTagsToMap(req.GetTags()); len(tags) > 0 {
		opts = append(opts, timeseries.WithTags(tags))
	}
	if req.GetLimit() > 0 {
		opts = append(opts, timeseries.WithTSLimit(int(req.GetLimit())))
	}
	if req.GetDescending() {
		opts = append(opts, timeseries.WithDescending())
	}

	var (
		points []timeseries.DataPoint
		err    error
	)
	if ropts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		points, err = tsSvc.QueryWithConsistency(ctx, req.GetMetric(), start, end, ropts, opts...)
	} else {
		points, err = tsSvc.Query(ctx, req.GetMetric(), start, end, opts...)
	}
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbPoints := make([]*pb.TSDataPoint, len(points))
	for i, p := range points {
		pbPoints[i] = dataPointToProto(p)
	}
	return &pb.TSQueryResponse{Points: pbPoints}, nil
}

// TSAggregate computes an aggregation over data points in a time range.
func (s *qrdbServiceServer) TSAggregate(ctx context.Context, req *pb.TSAggregateRequest) (*pb.TSAggregateResponse, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	start := time.Unix(0, req.GetStartNs())
	end := time.Unix(0, req.GetEndNs())
	fn := protoAggFuncMap[req.GetFunction()]
	interval := time.Duration(req.GetIntervalNs())

	var (
		results []timeseries.AggResult
		err     error
	)
	if ropts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		results, err = tsSvc.AggregateWithConsistency(ctx, req.GetMetric(), start, end, fn, interval, ropts)
	} else {
		results, err = tsSvc.Aggregate(ctx, req.GetMetric(), start, end, fn, interval)
	}
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbResults := make([]*pb.TSAggResult, len(results))
	for i, r := range results {
		pbResults[i] = aggResultToProto(r)
	}
	return &pb.TSAggregateResponse{Results: pbResults}, nil
}

// TSWritePoint stores a single validated DataPoint.
func (s *qrdbServiceServer) TSWritePoint(ctx context.Context, req *pb.TSWriteRequest) (*emptypb.Empty, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	p := req.GetPoint()
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "point is required")
	}

	dp := &timeseries.DataPoint{
		Metric:    p.GetMetric(),
		Value:     p.GetValue(),
		Timestamp: time.Unix(0, p.GetTimestampNs()),
		Tags:      protoTagsToMap(p.GetTags()),
	}

	if err := tsSvc.WritePoint(ctx, dp); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// TSRichQuery performs a rich query with filtering, aggregation,
// downsampling, and grouping.
func (s *qrdbServiceServer) TSRichQuery(ctx context.Context, req *pb.TSRichQueryRequest) (*pb.TSRichQueryResponse, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	qr := &timeseries.QueryRequest{
		Metric:      req.GetMetric(),
		Tags:        protoTagsToMap(req.GetTags()),
		StartTime:   time.Unix(0, req.GetStartNs()),
		EndTime:     time.Unix(0, req.GetEndNs()),
		Aggregation: protoAggFuncMap[req.GetAggregation()],
		Interval:    time.Duration(req.GetIntervalNs()),
		GroupBy:     req.GetGroupBy(),
		Limit:       int(req.GetLimit()),
		Percentile:  req.GetPercentile(),
	}

	var (
		result *timeseries.QueryResult
		err    error
	)
	if ropts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		result, err = tsSvc.QueryRichWithConsistency(ctx, qr, ropts)
	} else {
		result, err = tsSvc.QueryRich(ctx, qr)
	}
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbPoints := make([]*pb.TSDataPoint, len(result.Points))
	for i, p := range result.Points {
		pbPoints[i] = dataPointPtrToProto(p)
	}

	resp := &pb.TSRichQueryResponse{
		Metric: result.Metric,
		Tags:   mapToProtoTags(result.Tags),
		Points: pbPoints,
	}

	if result.AggregatedValue != nil {
		resp.AggregatedValue = *result.AggregatedValue
		resp.HasAggregatedValue = true
	}

	if len(result.GroupedResults) > 0 {
		grouped := make([]*pb.TSGroupedResult, len(result.GroupedResults))
		for i, gr := range result.GroupedResults {
			grPoints := make([]*pb.TSDataPoint, len(gr.Points))
			for j, p := range gr.Points {
				grPoints[j] = dataPointPtrToProto(p)
			}
			g := &pb.TSGroupedResult{
				Tags:   mapToProtoTags(gr.Tags),
				Points: grPoints,
			}
			if gr.AggregatedValue != nil {
				g.AggregatedValue = *gr.AggregatedValue
				g.HasAggregatedValue = true
			}
			grouped[i] = g
		}
		resp.GroupedResults = grouped
	}

	return resp, nil
}

// TSDeleteSeries deletes data points matching the request criteria.
func (s *qrdbServiceServer) TSDeleteSeries(ctx context.Context, req *pb.TSDeleteSeriesRequest) (*emptypb.Empty, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	dr := &timeseries.DeleteRequest{
		Metric: req.GetMetric(),
		Tags:   protoTagsToMap(req.GetTags()),
	}

	if req.GetStartNs() != 0 {
		dr.StartTime = time.Unix(0, req.GetStartNs())
	}
	if req.GetEndNs() != 0 {
		dr.EndTime = time.Unix(0, req.GetEndNs())
	}

	if err := tsSvc.DeleteSeries(ctx, dr); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// TSGetRetention retrieves the retention policy for a metric.
func (s *qrdbServiceServer) TSGetRetention(ctx context.Context, req *pb.TSGetRetentionRequest) (*pb.TSRetentionPolicyResponse, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	policy, err := tsSvc.GetRetentionPolicy(req.GetMetric())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.TSRetentionPolicyResponse{
		DurationNs:   int64(policy.Duration),
		ResolutionNs: int64(policy.Resolution),
		Aggregation:  aggFuncToProtoMap[policy.Aggregation],
	}, nil
}

// TSSetRetention sets the retention policy for a metric.
func (s *qrdbServiceServer) TSSetRetention(ctx context.Context, req *pb.TSSetRetentionRequest) (*emptypb.Empty, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	policy := &timeseries.RetentionPolicy{
		Duration:    time.Duration(req.GetDurationNs()),
		Resolution:  time.Duration(req.GetResolutionNs()),
		Aggregation: protoAggFuncMap[req.GetAggregation()],
	}

	if err := tsSvc.SetRetentionPolicy(req.GetMetric(), policy); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// TSListMetrics returns metric names matching the optional prefix.
func (s *qrdbServiceServer) TSListMetrics(ctx context.Context, req *pb.TSListMetricsRequest) (*pb.TSListMetricsResponse, error) {
	tsSvc, svcErr := s.tsSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	var (
		metrics []string
		err     error
	)
	if ropts, ok := readOptionsFromProto(req.GetConsistency()); ok {
		metrics, err = tsSvc.ListMetricsWithConsistency(ctx, req.GetPrefix(), int(req.GetLimit()), ropts)
	} else {
		metrics, err = tsSvc.ListMetrics(req.GetPrefix(), int(req.GetLimit()))
	}
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.TSListMetricsResponse{Metrics: metrics}, nil
}
