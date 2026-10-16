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
// SDK. It uses the same protobuf client as the gRPC transport, connecting
// via a local Unix domain socket. The socket file mode is defense-in-depth;
// the transport is "verified TLS 1.3 only" in production, presenting the
// client certificate and verifying the server exactly like the TCP gRPC
// transport (mirrored here via the same transport.Config TLS fields).
package unix

import (
	"context"
	"crypto/tls"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/refusalinfo"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/replicaconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/retryinfo"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealedinfo"
)

// Compile-time interface assertions.
var (
	_ transport.Transport        = (*Transport)(nil)
	_ transport.KVClient         = (*Transport)(nil)
	_ transport.AdminClient      = (*Transport)(nil)
	_ transport.TimeSeriesClient = (*Transport)(nil)
	_ transport.DatabaseScoper   = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface using gRPC over
// a Unix domain socket. The Address field in the transport config is the
// socket path (e.g., "/var/run/qrdb/qrdb.sock"). When the config enables
// TLS (transport.WithTLS / transport.WithMTLS / transport.WithTLSConfig),
// the connection is secured with a verified TLS 1.3 handshake and the
// client certificate presented as the mTLS identity, exactly like the
// gRPC transport; when TLS is not configured, the connection falls back
// to insecure credentials (kernel-level process isolation only), which
// the server-side daemon rejects unless it too was started without TLS.
type Transport struct {
	config *transport.Config
	conn   *grpc.ClientConn
	client pb.QRDBServiceClient
	closed atomic.Bool

	// activeDatabase, when non-empty, is appended as x-qrdb-database outgoing
	// metadata on every RPC so the server resolves data-plane operations under
	// the {tenant}/{database}/ keyspace. Empty means unscoped (system database).
	// Set only via WithDatabase; immutable per Transport.
	activeDatabase string
}

// dialOptions builds the gRPC dial options for the Unix socket connection.
// When the transport config enables TLS it dials with verified TLS 1.3
// credentials (client certificate + CA verification), mirroring the gRPC
// transport; otherwise it falls back to insecure credentials. It also
// installs the database-scope metadata interceptors when a database is set.
func (t *Transport) dialOptions() ([]grpc.DialOption, error) {
	var opts []grpc.DialOption

	if t.config.TLSEnabled {
		tlsCfg, err := t.buildTLSConfig()
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// A context bound by transport.WithSOPIN carries the SO PIN in the
	// x-qrdb-so-pin metadata entry, on every RPC made with it.
	// Only over TLS credentials: on a plaintext connection a PIN-bound RPC is
	// refused with *transport.SOPINRequiresTLSError and never sent.
	opts = append(opts,
		grpc.WithChainUnaryInterceptor(transport.SOPINUnaryClientInterceptor("unix", t.config.TLSEnabled)),
		grpc.WithChainStreamInterceptor(transport.SOPINStreamClientInterceptor("unix", t.config.TLSEnabled)),
	)

	if t.activeDatabase != "" {
		opts = append(opts,
			grpc.WithChainUnaryInterceptor(transport.DatabaseUnaryClientInterceptor(t.activeDatabase)),
			grpc.WithChainStreamInterceptor(transport.DatabaseStreamClientInterceptor(t.activeDatabase)),
		)
	}

	// Override grpc-go's own 4 MiB CLIENT-side receive default: see
	// transport.DefaultClientMaxRecvMessageBytes's doc comment for why a
	// response the server chooses to send is not bound by the pre-auth
	// receive cap the server applies to incoming requests.
	opts = append(opts, grpc.WithDefaultCallOptions(
		grpc.MaxCallRecvMsgSize(transport.DefaultClientMaxRecvMessageBytes)))

	return opts, nil
}

// buildTLSConfig constructs the TLS configuration from the transport
// config. The TLSEnabled gate lives at the dialOptions call site, so the
// builder is always invoked with TLS enabled.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	return transport.BuildTLSConfig(transport.TLSBuildOptions{
		Custom:   t.config.TLSConfig,
		Enabled:  true,
		CAFile:   t.config.TLSCAFile,
		CertFile: t.config.TLSCertFile,
		KeyFile:  t.config.TLSKeyFile,
	})
}

// WithDatabase returns a database-scoped clone of the transport. When the
// parent is already connected, the clone dials its own (lazy) connection
// carrying the x-qrdb-database metadata interceptor; when not yet connected,
// the clone's Connect dials the scoped connection. Scoping to the default
// ("system") database or the empty string yields an unscoped clone that shares
// the parent's connection.
func (t *Transport) withDatabase(database string) *Transport {
	db := transport.NormalizeDatabaseScope(database)
	clone := &Transport{config: t.config, activeDatabase: db}
	clone.closed.Store(t.closed.Load())

	if t.conn == nil {
		return clone
	}
	if db == "" {
		clone.conn = t.conn
		clone.client = t.client
		return clone
	}

	dialOpts, err := clone.dialOptions()
	if err == nil {
		var conn *grpc.ClientConn
		if conn, err = grpc.NewClient("unix://"+t.config.Address, dialOpts...); err == nil {
			clone.conn = conn
			clone.client = pb.NewQRDBServiceClient(conn)
			return clone
		}
	}
	// dialOptions / NewClient derive solely from static config that the
	// parent already dialed successfully, so this branch is unreachable in
	// practice; share the parent's connection rather than return an
	// unusable client.
	clone.conn = t.conn
	clone.client = t.client
	return clone
}

// WithDatabase implements transport.DatabaseScoper. Scoping a network
// transport never fails: the database travels with every data-plane request
// and the server resolves it.
func (t *Transport) WithDatabase(database string) (transport.Transport, error) {
	return t.withDatabase(database), nil
}

// New creates a new Unix socket transport with the given options. The
// Address option must be set to the socket file path. TLS is optional
// (transport.WithTLS / transport.WithMTLS / transport.WithTLSConfig); when
// enabled its cert/key/CA options are validated the same way as the gRPC
// transport. The transport is not connected until Connect is called.
func New(opts ...transport.Option) (*Transport, error) {
	cfg := transport.ApplyOptions(opts...)

	// Unix transports require an address (the socket path).
	if cfg.Address == "" {
		return nil, &transport.ConfigError{Field: "Address", Reason: "socket path is required for Unix transports"}
	}
	if cfg.TLSEnabled && cfg.TLSConfig == nil && cfg.TLSCAFile == "" {
		return nil, &transport.ConfigError{Field: "TLSCAFile", Reason: "CA file is required when TLS is enabled without custom TLSConfig"}
	}
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile == "" {
		return nil, &transport.ConfigError{Field: "TLSKeyFile", Reason: "key file is required when cert file is specified"}
	}
	if cfg.TLSKeyFile != "" && cfg.TLSCertFile == "" {
		return nil, &transport.ConfigError{Field: "TLSCertFile", Reason: "cert file is required when key file is specified"}
	}

	return &Transport{config: cfg}, nil
}

// ensureClient lazily dials the gRPC connection over the Unix domain socket
// when none exists yet, without the health check Connect performs:
// RedeemInvitation is the one call this transport serves to an unconnected,
// uncredentialed caller (see transport.InvitationClient), so it cannot
// require an ordinary Connect first. An already-connected transport
// (t.client != nil, whether through Connect or an earlier call here) is a
// no-op.
func (t *Transport) ensureClient() error {
	if t.client != nil {
		return nil
	}

	target := "unix://" + t.config.Address

	dialOpts, err := t.dialOptions()
	if err != nil {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &dialError{Err: err},
		}
	}

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &dialError{Err: err},
		}
	}

	t.conn = conn
	t.client = pb.NewQRDBServiceClient(conn)
	return nil
}

// Connect establishes the gRPC connection over the Unix domain socket.
// It uses insecure credentials since Unix sockets provide kernel-level
// process isolation.
func (t *Transport) Connect(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if err := t.ensureClient(); err != nil {
		return err
	}

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
// responds to a health check. An authenticated identity that is reachable
// but not personally authorized to call Health (see
// reachableButUnauthorized) still counts as healthy: authorization is a
// per-RPC decision, not a connectivity signal -- a barrier officer's
// short-lived claim-cert identity, for example, is never granted the
// admin:health permission but must still be able to Connect() and drive
// the share-claim RPCs it IS authorized for.
func (t *Transport) Healthy(ctx context.Context) bool {
	if t.closed.Load() || t.client == nil {
		return false
	}

	resp, err := t.client.Health(ctx, &emptypb.Empty{})
	if err == nil {
		return resp.GetStatus() != ""
	}
	return reachableButUnauthorized(err) || reachableButSealed(err)
}

// reachableButUnauthorized reports whether err is a gRPC status error
// indicating the server was reached, authenticated the caller's TLS client
// certificate, and answered the Health RPC, but rejected it purely on
// AUTHORIZATION grounds (PermissionDenied) rather than a connectivity
// failure. codes.Unauthenticated is deliberately EXCLUDED -- see
// TestStart_UnixOnly_AuthMiddlewareRejectsUnauthenticated in pkg/server,
// which pins Connect() failing closed for a caller with no/invalid client
// certificate. See the identical helper in
// sdk/go/transport/grpc/transport.go for the full rationale.
func reachableButUnauthorized(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return true
	default:
		return false
	}
}

// reachableButSealed reports whether err is a sealed node's refusal: the
// server was reached and answered, and serves nothing but the unseal call
// until its barrier is unsealed. A sealed node is up, so it counts as
// connected — the caller connecting to it is usually the one about to unseal.
func reachableButSealed(err error) bool {
	st, ok := status.FromError(err)
	return ok && sealedinfo.Sealed(st)
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
		Status:             resp.GetStatus(),
		IsLeader:           resp.GetLeader(),
		DiagnosticsDropped: resp.GetDiagnosticsDropped(),
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
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.AddMember(ctx, &pb.AddMemberRequest{
		ShardId:        shardID,
		ReplicaId:      replicaID,
		RaftAddress:    raftAddr,
		MemberIdentity: identity,
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

// PromoteMember promotes a committed observer of the specified shard to a
// voter.
func (t *Transport) PromoteMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.PromoteMember(ctx, &pb.PromoteMemberRequest{
		ShardId:   shardID,
		ReplicaId: replicaID,
	})
	if err != nil {
		return mapGRPCError("PromoteMember", err)
	}
	return nil
}

// RetireJoinIdentity permanently refuses any future admission for a node
// identity.
func (t *Transport) RetireJoinIdentity(ctx context.Context, nodeID, reason string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.RetireJoinIdentity(ctx, &pb.RetireJoinIdentityRequest{
		NodeId: nodeID,
		Reason: reason,
	})
	if err != nil {
		return mapGRPCError("RetireJoinIdentity", err)
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

// Capabilities returns the effective feature set of the QRDB node.
func (t *Transport) Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.Capabilities(ctx, &pb.CapabilitiesRequest{})
	if err != nil {
		return nil, mapGRPCError("Capabilities", err)
	}

	return &transport.CapabilitiesResponse{
		Mode:        resp.GetMode(),
		KV:          resp.GetKv(),
		MVCC:        resp.GetMvcc(),
		Transaction: resp.GetTransaction(),
		Batch:       resp.GetBatch(),
		Watch:       resp.GetWatch(),
		Lease:       resp.GetLease(),
		Lock:        resp.GetLock(),
		Index:       resp.GetIndex(),
		Cluster:     resp.GetCluster(),
		Backup:      resp.GetBackup(),
		Rebalance:   resp.GetRebalance(),
		TimeSeries:  resp.GetTimeSeries(),
		Vector:      resp.GetVector(),
		SQL:         resp.GetSql(),
		MySQL:       resp.GetMysql(),
		PostgreSQL:  resp.GetPostgresql(),
		Etcd:        resp.GetEtcd(),
		FTS:         resp.GetFts(),

		UnencryptedAtRest: resp.GetUnencryptedAtRest(),
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

// ReplicaStates implements transport.AdminClient.
func (t *Transport) ReplicaStates(ctx context.Context, shardID uint64) (*transport.ReplicaStatesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetReplicaStates(ctx, &pb.GetReplicaStatesRequest{ShardId: shardID})
	if err != nil {
		return nil, mapGRPCError("ReplicaStates", err)
	}
	return replicaconv.FromProto(resp), nil
}

// MintJoinToken mints a single-use, identity-bound member-side CSR join
// token. Implements transport.ClusterJoinTokenMinter.
func (t *Transport) MintJoinToken(ctx context.Context, req *transport.MintJoinTokenRequest) (*transport.MintJoinTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.MintJoinToken(ctx, &pb.MintJoinTokenRequest{
		NodeId:     req.NodeID,
		TtlSeconds: req.TTLSeconds,
	})
	if err != nil {
		return nil, mapGRPCError("MintJoinToken", err)
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, resp.GetExpiresAt())
	if parseErr != nil {
		return nil, &transport.RequestError{Method: "MintJoinToken", Err: parseErr}
	}
	return &transport.MintJoinTokenResponse{
		Token:     resp.GetToken(),
		ExpiresAt: expiresAt,
	}, nil
}

var _ transport.ClusterJoinTokenMinter = (*Transport)(nil)

// AddObserverMember adds a new replica to the specified shard as a
// non-voting observer rather than a voter. Implements
// transport.ObserverMemberAdder.
func (t *Transport) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.AddObserverMember(ctx, &pb.AddObserverMemberRequest{
		ShardId:        shardID,
		ReplicaId:      replicaID,
		RaftAddress:    raftAddr,
		MemberIdentity: identity,
	})
	if err != nil {
		return mapGRPCError("AddObserverMember", err)
	}
	return nil
}

var _ transport.ObserverMemberAdder = (*Transport)(nil)

// ListPendingJoinRequests lists tokenless member-side CSR join requests
// awaiting an operator's decision. Implements transport.JoinRequestReviewer.
func (t *Transport) ListPendingJoinRequests(ctx context.Context) ([]*transport.PendingJoinRequest, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ListPendingJoinRequests(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListPendingJoinRequests", err)
	}

	out := make([]*transport.PendingJoinRequest, 0, len(resp.GetRequests()))
	for _, r := range resp.GetRequests() {
		submittedAt, perr := time.Parse(time.RFC3339, r.GetSubmittedAt())
		if perr != nil {
			return nil, &transport.RequestError{Method: "ListPendingJoinRequests", Err: perr}
		}
		expiresAt, perr := time.Parse(time.RFC3339, r.GetExpiresAt())
		if perr != nil {
			return nil, &transport.RequestError{Method: "ListPendingJoinRequests", Err: perr}
		}
		out = append(out, &transport.PendingJoinRequest{
			ID:          r.GetId(),
			NodeURI:     r.GetNodeUri(),
			ReplicaID:   r.GetReplicaId(),
			Features:    r.GetFeatures(),
			SubmittedAt: submittedAt,
			ExpiresAt:   expiresAt,
		})
	}
	return out, nil
}

// ApproveJoinRequest binds id's queued CSR to an operator-chosen node
// identity. Implements transport.JoinRequestReviewer.
func (t *Transport) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.ApproveJoinRequest(ctx, &pb.ApproveJoinRequestRequest{Id: id, NodeId: nodeID})
	if err != nil {
		return mapGRPCError("ApproveJoinRequest", err)
	}
	return nil
}

// RejectJoinRequest records a terminal refusal of id's queued CSR.
// Implements transport.JoinRequestReviewer.
func (t *Transport) RejectJoinRequest(ctx context.Context, id, reason string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.RejectJoinRequest(ctx, &pb.RejectJoinRequestRequest{Id: id, Reason: reason})
	if err != nil {
		return mapGRPCError("RejectJoinRequest", err)
	}
	return nil
}

var _ transport.JoinRequestReviewer = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// mapGRPCError translates a gRPC status error into the appropriate SDK
// transport error type. A nil err is success and maps to nil: status.FromError
// reports a nil error as codes.OK with ok=true, which the switch below would
// otherwise have turned into a RequestError, so VerifyFactor and RevokeFactor,
// which return the mapped error unconditionally, failed every successful call.
func mapGRPCError(method string, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return &transport.RequestError{Method: method, Err: err}
	}
	if sealedinfo.Sealed(st) {
		return &transport.BarrierSealedError{Message: st.Message()}
	}

	switch st.Code() {
	case codes.NotFound:
		return &transport.KeyNotFoundError{Key: st.Message()}
	case codes.Unimplemented:
		// The unix transport is gRPC over a domain socket, talking to the same
		// pkg/api/grpc server as the TCP transport, so it receives the same
		// Unimplemented a capability refusal is mapped to (go-qrdb#141).
		// Without this case it would fall through to a generic RequestError and
		// a unix client would lose the graceful degradation its TCP twin gets,
		// against the identical server-side condition.
		return &transport.CapabilityUnsupportedError{Message: st.Message()}
	default:
		if typed := refusalinfo.Error(st); typed != nil {
			err = typed
		}
		return &transport.RequestError{Method: method, StatusCode: int(st.Code()), Err: err, RetryAfter: retryinfo.Delay(st)}
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
