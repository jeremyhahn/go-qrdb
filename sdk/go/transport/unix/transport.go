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

// Package unix implements a gRPC-over-Unix-socket transport for the QRDB
// SDK. It uses the same protobuf client as the gRPC transport but connects
// via a local Unix domain socket without TLS, providing efficient
// inter-process communication.
package unix

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport        = (*Transport)(nil)
	_ transport.KVClient         = (*Transport)(nil)
	_ transport.AdminClient      = (*Transport)(nil)
	_ transport.TimeSeriesClient = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface using gRPC over
// a Unix domain socket. The Address field in the transport config is the
// socket path (e.g., "/var/run/qrdb/qrdb.sock"). TLS is not used for
// Unix socket communication.
type Transport struct {
	config *transport.Config
	conn   *grpc.ClientConn
	client pb.QRDBServiceClient
	closed atomic.Bool
}

// New creates a new Unix socket transport with the given options. The
// Address option must be set to the socket file path. The transport is
// not connected until Connect is called.
func New(opts ...transport.Option) (*Transport, error) {
	cfg := transport.ApplyOptions(opts...)

	// Unix transports require an address (the socket path) but not TLS.
	if cfg.Address == "" {
		return nil, &transport.ConfigError{Field: "Address", Reason: "socket path is required for Unix transports"}
	}

	return &Transport{config: cfg}, nil
}

// Connect establishes the gRPC connection over the Unix domain socket.
// It uses insecure credentials since Unix sockets provide kernel-level
// process isolation.
func (t *Transport) Connect(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	target := "unix://" + t.config.Address

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &dialError{Err: err},
		}
	}

	t.conn = conn
	t.client = pb.NewQRDBServiceClient(conn)

	// Verify connectivity with a health check.
	if !t.Healthy(ctx) {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &dialError{Err: status.Error(codes.Unavailable, "health check failed")},
		}
	}

	return nil
}

// Close shuts down the gRPC connection and releases all resources.
func (t *Transport) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

// Healthy returns true if the gRPC connection is active and the server
// responds to a health check.
func (t *Transport) Healthy(ctx context.Context) bool {
	if t.closed.Load() || t.client == nil {
		return false
	}

	resp, err := t.client.Health(ctx, &emptypb.Empty{})
	if err != nil {
		return false
	}

	return resp.GetStatus() != ""
}

// Request performs a generic gRPC request dispatch by method name.
func (t *Transport) Request(ctx context.Context, method string, req, resp any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.conn.Invoke(ctx, method, req, resp)
}

// ---------------------------------------------------------------------------
// KVClient implementation
// ---------------------------------------------------------------------------

// Put stores a key-value pair through Raft consensus.
func (t *Transport) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	ctx = applyWriteMetadata(ctx, opts...)

	_, err := t.client.Put(ctx, &pb.PutRequest{Key: key, Value: value, WriteMode: protoWriteMode(opts...)})
	if err != nil {
		return mapGRPCError("Put", err)
	}
	return nil
}

// Get retrieves a value by key with a linearizable read. Returns
// KeyNotFoundError when the server responds with NotFound.
func (t *Transport) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyReadMetadata(ctx, opts...)

	resp, err := t.client.Get(ctx, &pb.GetRequest{Key: key, Consistency: protoConsistency(opts...)})
	if err != nil {
		return nil, mapGRPCError("Get", err)
	}
	return resp.GetValue(), nil
}

// Delete removes a key through Raft consensus.
func (t *Transport) Delete(ctx context.Context, key string, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	ctx = applyWriteMetadata(ctx, opts...)

	_, err := t.client.Delete(ctx, &pb.DeleteRequest{Key: key, WriteMode: protoWriteMode(opts...)})
	if err != nil {
		return mapGRPCError("Delete", err)
	}
	return nil
}

// List returns sorted keys matching the given prefix. An empty prefix
// returns all keys.
func (t *Transport) List(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyReadMetadata(ctx, opts...)

	resp, err := t.client.List(ctx, &pb.ListRequest{Prefix: prefix, Consistency: protoConsistency(opts...)})
	if err != nil {
		return nil, mapGRPCError("List", err)
	}
	return resp.GetKeys(), nil
}

// Scan returns all key-value pairs matching the given prefix.
func (t *Transport) Scan(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyReadMetadata(ctx, opts...)

	resp, err := t.client.Scan(ctx, &pb.ScanRequest{Prefix: prefix, Consistency: protoConsistency(opts...)})
	if err != nil {
		return nil, mapGRPCError("Scan", err)
	}

	result := make(map[string][]byte, len(resp.GetPairs()))
	for k, v := range resp.GetPairs() {
		result[k] = v
	}
	return result, nil
}

// Exists checks whether a key exists. Performs a check via Get.
func (t *Transport) Exists(ctx context.Context, key string, opts ...transport.ReadOption) (bool, error) {
	_, err := t.Get(ctx, key, opts...)
	if err != nil {
		var knf *transport.KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// AdminClient implementation
// ---------------------------------------------------------------------------

// Health returns the health status of the QRDB node.
func (t *Transport) Health(ctx context.Context) (*transport.HealthResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.Health(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("Health", err)
	}

	return &transport.HealthResponse{
		Status:   resp.GetStatus(),
		IsLeader: resp.GetLeader(),
	}, nil
}

// Status returns detailed node and shard status information.
func (t *Transport) Status(ctx context.Context) (*transport.StatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.Status(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("Status", err)
	}

	return &transport.StatusResponse{
		NodeID:      resp.GetNodeId(),
		RaftAddress: resp.GetRaftAddress(),
		IsLeader:    resp.GetIsLeader(),
		LeaderID:    resp.GetLeaderId(),
		ShardCount:  int(resp.GetShardCount()),
		IsDraining:  resp.GetIsDraining(),
	}, nil
}

// Peers returns the peer membership for all loaded shards.
func (t *Transport) Peers(ctx context.Context) (*transport.PeersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.Peers(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("Peers", err)
	}

	shards := make([]transport.ShardPeers, 0, len(resp.GetShards()))
	for _, sp := range resp.GetShards() {
		peers := make([]transport.PeerInfo, 0, len(sp.GetPeers()))
		for _, p := range sp.GetPeers() {
			peers = append(peers, transport.PeerInfo{
				ReplicaID: p.GetReplicaId(),
				Address:   p.GetAddress(),
				IsLeader:  p.GetIsLeader(),
			})
		}
		shards = append(shards, transport.ShardPeers{
			ShardID:  sp.GetShardId(),
			LeaderID: sp.GetLeaderId(),
			Peers:    peers,
		})
	}

	return &transport.PeersResponse{Shards: shards}, nil
}

// AddMember adds a new replica to the specified shard.
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.AddMember(ctx, &pb.AddMemberRequest{
		ShardId:     shardID,
		ReplicaId:   replicaID,
		RaftAddress: raftAddr,
	})
	if err != nil {
		return mapGRPCError("AddMember", err)
	}
	return nil
}

// RemoveMember removes a replica from the specified shard.
func (t *Transport) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.RemoveMember(ctx, &pb.RemoveMemberRequest{
		ShardId:   shardID,
		ReplicaId: replicaID,
	})
	if err != nil {
		return mapGRPCError("RemoveMember", err)
	}
	return nil
}

// RequestSnapshot triggers an asynchronous snapshot for the specified shard.
func (t *Transport) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.RequestSnapshot(ctx, &pb.RequestSnapshotMessage{
		ShardId: shardID,
	})
	if err != nil {
		return mapGRPCError("RequestSnapshot", err)
	}
	return nil
}

// Drain transitions the host into drain mode.
func (t *Transport) Drain(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.Drain(ctx, &emptypb.Empty{})
	if err != nil {
		return mapGRPCError("Drain", err)
	}
	return nil
}

// Capabilities returns the compile-time feature set of the QRDB node.
func (t *Transport) Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.Capabilities(ctx, &pb.CapabilitiesRequest{})
	if err != nil {
		return nil, mapGRPCError("Capabilities", err)
	}

	return &transport.CapabilitiesResponse{
		KV:          resp.GetKv(),
		MVCC:        resp.GetMvcc(),
		Transaction: resp.GetTransaction(),
		Batch:       resp.GetBatch(),
		Watch:       resp.GetWatch(),
		Lease:       resp.GetLease(),
		Index:       resp.GetIndex(),
		TimeSeries:  resp.GetTimeSeries(),
		Vector:      resp.GetVector(),
	}, nil
}

// StartShard initializes and starts a Raft group for the given shard.
func (t *Transport) StartShard(ctx context.Context, req *transport.StartShardRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.StartShard(ctx, &pb.StartShardRequest{
		ShardId:   req.ShardID,
		ReplicaId: req.ReplicaID,
		Members:   req.Members,
		Join:      req.Join,
	})
	if err != nil {
		return mapGRPCError("StartShard", err)
	}
	return nil
}

// StopShard stops the Raft group for the given shard.
func (t *Transport) StopShard(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.StopShard(ctx, &pb.StopShardRequest{
		ShardId: shardID,
	})
	if err != nil {
		return mapGRPCError("StopShard", err)
	}
	return nil
}

// GetShardState returns the Raft state for a shard.
func (t *Transport) GetShardState(ctx context.Context, shardID uint64) (*transport.ShardStateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetShardState(ctx, &pb.GetShardStateRequest{
		ShardId: shardID,
	})
	if err != nil {
		return nil, mapGRPCError("GetShardState", err)
	}

	return &transport.ShardStateResponse{
		ShardID:           resp.GetShardId(),
		ConfigChangeIndex: resp.GetConfigChangeIndex(),
		Term:              resp.GetTerm(),
		LeaderID:          resp.GetLeaderId(),
		HasLeader:         resp.GetHasLeader(),
	}, nil
}

// GetTopology returns the full cluster topology.
func (t *Transport) GetTopology(ctx context.Context) (*transport.TopologyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetTopology(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("GetTopology", err)
	}

	shards := make([]transport.ShardInfo, 0, len(resp.GetShards()))
	for _, si := range resp.GetShards() {
		shards = append(shards, transport.ShardInfo{
			ShardID:   si.GetShardId(),
			ReplicaID: si.GetReplicaId(),
			IsLeader:  si.GetIsLeader(),
			LeaderID:  si.GetLeaderId(),
			Nodes:     si.GetNodes(),
		})
	}

	return &transport.TopologyResponse{
		NodeID:      resp.GetNodeId(),
		RaftAddress: resp.GetRaftAddress(),
		ShardCount:  int(resp.GetShardCount()),
		Shards:      shards,
	}, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// mapGRPCError translates a gRPC status error into the appropriate SDK
// transport error type.
func mapGRPCError(method string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return &transport.RequestError{Method: method, Err: err}
	}

	switch st.Code() {
	case codes.NotFound:
		return &transport.KeyNotFoundError{Key: st.Message()}
	default:
		return &transport.RequestError{Method: method, StatusCode: int(st.Code()), Err: err}
	}
}

// ConnState returns the current gRPC connectivity state. This is useful
// for diagnostics and testing.
func (t *Transport) ConnState() connectivity.State {
	if t.conn == nil {
		return connectivity.Shutdown
	}
	return t.conn.GetState()
}

// applyWriteMetadata resolves write options and appends gRPC metadata
// to the outgoing context.
func applyWriteMetadata(ctx context.Context, opts ...transport.WriteOption) context.Context {
	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-qrdb-write-mode", wo.Mode.String())
	}
	return ctx
}

// applyReadMetadata resolves read options and appends gRPC metadata
// to the outgoing context.
func applyReadMetadata(ctx context.Context, opts ...transport.ReadOption) context.Context {
	ro := transport.ResolveReadOptions(opts...)
	if ro.ConsistencySet {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-qrdb-consistency", ro.Consistency.String())
	}
	if ro.TimeoutMs > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-qrdb-read-timeout", strconv.FormatInt(ro.TimeoutMs, 10))
	}
	if ro.AllowStale {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-qrdb-allow-stale", "true")
	}
	return ctx
}

// vectorConsistencyToProto maps a transport.ConsistencyLevel stored as a
// uint8 in vector request types to its proto ConsistencyLevel equivalent.
// The zero value (ConsistencyStrong) maps to STRONG so that the server
// preserves the same behavior clients received when the field was int32.
func vectorConsistencyToProto(level uint8) pb.ConsistencyLevel {
	switch transport.ConsistencyLevel(level) {
	case transport.ConsistencyLinearizable:
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE
	case transport.ConsistencyEventual:
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	case transport.ConsistencyFollower:
		// No dedicated wire value yet; remote clients receive the closest
		// non-blocking semantic (local read) instead of a leader round-trip.
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	default:
		// ConsistencyStrong (0) and any unrecognized value → STRONG.
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG
	}
}

// protoConsistency resolves read options into the proto ConsistencyLevel
// carried in the request body. UNSPECIFIED is returned when no consistency
// was requested, letting the server apply its default. An explicit
// WithAllowStale (without a level) maps to EVENTUAL.
func protoConsistency(opts ...transport.ReadOption) pb.ConsistencyLevel {
	ro := transport.ResolveReadOptions(opts...)
	if ro.ConsistencySet {
		switch ro.Consistency {
		case transport.ConsistencyStrong:
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG
		case transport.ConsistencyLinearizable:
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE
		case transport.ConsistencyEventual:
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
		case transport.ConsistencyFollower:
			// No dedicated wire value yet; map to the closest non-blocking
			// semantic so remote clients never hang on a leader round-trip.
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
		}
	}
	if ro.AllowStale {
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	}
	return pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED
}

// protoWriteMode resolves write options into the proto WriteMode carried in
// the request body. UNSPECIFIED is returned when no mode was requested.
func protoWriteMode(opts ...transport.WriteOption) pb.WriteMode {
	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		switch wo.Mode {
		case transport.WriteModeSync:
			return pb.WriteMode_WRITE_MODE_SYNC
		case transport.WriteModeWALDurable:
			return pb.WriteMode_WRITE_MODE_WAL_DURABLE
		case transport.WriteModeBatched:
			return pb.WriteMode_WRITE_MODE_BATCHED
		}
	}
	return pb.WriteMode_WRITE_MODE_UNSPECIFIED
}
