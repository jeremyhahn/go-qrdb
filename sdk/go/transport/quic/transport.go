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

// Package quic implements the QUIC transport for the QRDB SDK. It
// communicates with the QRDB native QUIC server using a binary wire
// protocol over QUIC streams, providing type-safe KV and admin
// operations with minimal framing overhead.
package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport   = (*Transport)(nil)
	_ transport.KVClient    = (*Transport)(nil)
	_ transport.AdminClient = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface using native
// QUIC streams with a binary wire protocol. It communicates with the
// QRDB QUIC server, sending protobuf-encoded request payloads and
// receiving protobuf-encoded response payloads over per-RPC
// bidirectional streams.
type Transport struct {
	config    *transport.Config
	conn      *quicgo.Conn
	closed    atomic.Bool
	requestID atomic.Uint64

	// database is the target database scope conveyed on data-plane request
	// frames. An empty string (or the default "system" scope) selects the
	// server's implicit bootstrap database. It is set via WithDatabase.
	database string
}

// Compile-time assertion that the QUIC transport supports database scoping.
var _ transport.DatabaseScoper = (*Transport)(nil)

// WithDatabase returns a database-scoped clone of the transport that conveys
// the given database scope on its data-plane request frames while sharing the
// parent's underlying QUIC connection. Scoping to the empty string or the
// default "system" database yields an unscoped clone (no database is sent). It
// implements transport.DatabaseScoper.
func (t *Transport) WithDatabase(database string) transport.Transport {
	clone := &Transport{
		config:   t.config,
		conn:     t.conn,
		database: transport.NormalizeDatabaseScope(database),
	}
	clone.closed.Store(t.closed.Load())
	return clone
}

// New creates a new QUIC transport with the given options. The transport
// is not connected until Connect is called.
func New(opts ...transport.Option) (*Transport, error) {
	cfg := transport.ApplyOptions(opts...)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &Transport{config: cfg}, nil
}

// Connect establishes the QUIC connection to the QRDB server. QUIC
// always requires TLS; ALPN must select wire.ProtocolV2 (see
// buildTLSConfig), and any other selection is refused.
// quicConnectBudget bounds QUIC connection establishment (dial + health
// probe) so a stalled UDP handshake fails fast rather than hanging until the
// caller's outer deadline, leaving the rest of that deadline for the RPC.
const quicConnectBudget = 10 * time.Second

func (t *Transport) Connect(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	tlsCfg, err := t.buildTLSConfig()
	if err != nil {
		return err
	}

	addr := stripScheme(t.config.Address)

	quicCfg := &quicgo.Config{
		MaxIdleTimeout:     30 * time.Second,
		KeepAlivePeriod:    10 * time.Second,
		MaxIncomingStreams: 1000,
	}

	// Bound connection establishment. QUIC rides UDP, so a degraded datagram
	// path (small kernel buffers, loss under load) can stall the handshake;
	// without a deadline DialAddr/Healthy would hang until the caller's outer
	// deadline (or indefinitely on a background context), surfacing as an
	// opaque process kill. Cap it so a stalled connect fails fast and leaves
	// the rest of the caller's budget for the actual RPC.
	cctx, cancel := context.WithTimeout(ctx, quicConnectBudget)
	defer cancel()

	conn, err := quicgo.DialAddr(cctx, addr, tlsCfg, quicCfg)
	if err != nil {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &dialError{Err: err},
		}
	}

	// A caller-supplied TLS config may offer other protocols alongside
	// wire.ProtocolV2, and a server that is not a qrdb QUIC listener could
	// select one of them. Every call on this connection relies on ProtocolV2's
	// stream discipline, so anything else is refused rather than spoken to.
	if got := conn.ConnectionState().TLS.NegotiatedProtocol; got != wire.ProtocolV2 {
		_ = conn.CloseWithError(0, "unsupported protocol")
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &alpnMismatchError{Negotiated: got},
		}
	}

	t.conn = conn

	// Verify connectivity with a health check.
	if !t.Healthy(cctx) {
		_ = conn.CloseWithError(0, "health check failed")
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &dialError{Err: errors.New("health check failed")},
		}
	}

	return nil
}

// Close shuts down the QUIC connection and releases all resources.
func (t *Transport) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	if t.conn != nil {
		return t.conn.CloseWithError(0, "client closed")
	}
	return nil
}

// Healthy returns true if the QUIC connection is active and the server
// responds to a health check. An authenticated identity that is reachable
// but not personally authorized to call admin.Health (see
// reachableButUnauthorized) still counts as healthy: authorization is a
// per-RPC decision, not a connectivity signal -- a barrier officer's
// short-lived claim-cert identity, for example, is never granted the
// admin:health permission but must still be able to Connect() and drive
// the share-claim RPCs it IS authorized for.
func (t *Transport) Healthy(ctx context.Context) bool {
	if t.closed.Load() || t.conn == nil {
		return false
	}

	resp, err := t.Health(ctx)
	if err == nil {
		return resp.Status != ""
	}
	return reachableButUnauthorized(err)
}

// reachableButUnauthorized reports whether err is a *transport.RequestError
// wrapping a QUIC wire-protocol status indicating the server was reached,
// authenticated the caller's TLS client certificate, and answered the
// admin.Health RPC, but rejected it purely on AUTHORIZATION grounds
// (wire.StatusPermissionDenied) rather than a connectivity failure.
// wire.StatusUnauthenticated is deliberately EXCLUDED -- it means the
// caller never authenticated at all (no/invalid TLS client certificate),
// which Connect() must continue to fail closed on. See the identical
// helper in sdk/go/transport/grpc/transport.go for the full rationale.
func reachableButUnauthorized(err error) bool {
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		return false
	}
	switch uint16(reqErr.StatusCode) {
	case wire.StatusPermissionDenied:
		return true
	default:
		return false
	}
}

// Request performs a generic QUIC wire protocol request by method name.
// This method is not supported for the QUIC transport; callers should
// use the typed KVClient and AdminClient methods instead.
func (t *Transport) Request(_ context.Context, method string, _, _ any) error {
	return &transport.NotImplementedError{Method: "Request(" + method + ")"}
}

// ---------------------------------------------------------------------------
// KVClient implementation
// ---------------------------------------------------------------------------

// Put stores a key-value pair through Raft consensus. When opts specify a
// write mode, the WriteMode proto field is set on the request so the server
// routes to the appropriate write path. Omitting opts preserves the existing
// default (sync/quorum) behavior.
func (t *Transport) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.PutRequest{
		Key:       key,
		Value:     value,
		WriteMode: protoWriteMode(opts...),
	})
	if err != nil {
		return &transport.RequestError{Method: "Put", Err: err}
	}

	_, err = t.call(ctx, "kv.Put", payload)
	if err != nil {
		return err
	}
	return nil
}

// Get retrieves a value by key. When opts specify a consistency level, the
// ConsistencyLevel proto field is set on the request so the server routes to
// the appropriate read path. Omitting opts preserves the existing default
// (linearizable) behavior. Returns KeyNotFoundError when the server responds
// with a key-not-found error.
func (t *Transport) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.GetRequest{
		Key:         key,
		Consistency: protoConsistency(opts...),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "Get", Err: err}
	}

	respPayload, err := t.call(ctx, "kv.Get", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.GetResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Get", Err: err}
	}
	return resp.GetValue(), nil
}

// Delete removes a key through Raft consensus. When opts specify a write
// mode, the WriteMode proto field is set on the request.
func (t *Transport) Delete(ctx context.Context, key string, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.DeleteRequest{
		Key:       key,
		WriteMode: protoWriteMode(opts...),
	})
	if err != nil {
		return &transport.RequestError{Method: "Delete", Err: err}
	}

	_, err = t.call(ctx, "kv.Delete", payload)
	if err != nil {
		return err
	}
	return nil
}

// List returns sorted keys matching the given prefix. When opts specify a
// consistency level, the ConsistencyLevel proto field is set on the request.
// An empty prefix returns all keys.
func (t *Transport) List(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.ListRequest{
		Prefix:      prefix,
		Consistency: protoConsistency(opts...),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "List", Err: err}
	}

	respPayload, err := t.call(ctx, "kv.List", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.ListResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "List", Err: err}
	}
	return resp.GetKeys(), nil
}

// Scan returns all key-value pairs matching the given prefix. When opts
// specify a consistency level, the ConsistencyLevel proto field is set on
// the request.
func (t *Transport) Scan(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.ScanRequest{
		Prefix:      prefix,
		Consistency: protoConsistency(opts...),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "Scan", Err: err}
	}

	respPayload, err := t.call(ctx, "kv.Scan", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.ScanResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Scan", Err: err}
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

	respPayload, err := t.call(ctx, "admin.Health", nil)
	if err != nil {
		return nil, err
	}

	var resp pb.HealthResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Health", Err: err}
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

	respPayload, err := t.call(ctx, "admin.Status", nil)
	if err != nil {
		return nil, err
	}

	var resp pb.StatusResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Status", Err: err}
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

	respPayload, err := t.call(ctx, "admin.Peers", nil)
	if err != nil {
		return nil, err
	}

	var resp pb.PeersResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Peers", Err: err}
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

	payload, err := proto.Marshal(&pb.AddMemberRequest{
		ShardId:     shardID,
		ReplicaId:   replicaID,
		RaftAddress: raftAddr,
	})
	if err != nil {
		return &transport.RequestError{Method: "AddMember", Err: err}
	}

	_, err = t.call(ctx, "admin.AddMember", payload)
	if err != nil {
		return err
	}
	return nil
}

// RemoveMember removes a replica from the specified shard.
func (t *Transport) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.RemoveMemberRequest{
		ShardId:   shardID,
		ReplicaId: replicaID,
	})
	if err != nil {
		return &transport.RequestError{Method: "RemoveMember", Err: err}
	}

	_, err = t.call(ctx, "admin.RemoveMember", payload)
	if err != nil {
		return err
	}
	return nil
}

// RequestSnapshot triggers an asynchronous snapshot for the specified shard.
func (t *Transport) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.RequestSnapshotMessage{
		ShardId: shardID,
	})
	if err != nil {
		return &transport.RequestError{Method: "RequestSnapshot", Err: err}
	}

	_, err = t.call(ctx, "admin.RequestSnapshot", payload)
	if err != nil {
		return err
	}
	return nil
}

// Drain transitions the host into drain mode.
func (t *Transport) Drain(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.call(ctx, "admin.Drain", nil)
	if err != nil {
		return err
	}
	return nil
}

// Capabilities returns the compile-time feature set of the QRDB node.
func (t *Transport) Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	respPayload, err := t.call(ctx, "admin.Capabilities", nil)
	if err != nil {
		return nil, err
	}

	var resp pb.CapabilitiesResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "Capabilities", Err: err}
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
	}, nil
}

// StartShard initializes and starts a Raft group for the given shard.
func (t *Transport) StartShard(ctx context.Context, req *transport.StartShardRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.StartShardRequest{
		ShardId:   req.ShardID,
		ReplicaId: req.ReplicaID,
		Members:   req.Members,
		Join:      req.Join,
	})
	if err != nil {
		return &transport.RequestError{Method: "StartShard", Err: err}
	}

	_, err = t.call(ctx, "admin.StartShard", payload)
	if err != nil {
		return err
	}
	return nil
}

// StopShard stops the Raft group for the given shard.
func (t *Transport) StopShard(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.StopShardRequest{
		ShardId: shardID,
	})
	if err != nil {
		return &transport.RequestError{Method: "StopShard", Err: err}
	}

	_, err = t.call(ctx, "admin.StopShard", payload)
	if err != nil {
		return err
	}
	return nil
}

// GetShardState returns the Raft state for a shard.
func (t *Transport) GetShardState(ctx context.Context, shardID uint64) (*transport.ShardStateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.GetShardStateRequest{
		ShardId: shardID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetShardState", Err: err}
	}

	respPayload, err := t.call(ctx, "admin.GetShardState", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.ShardStateResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "GetShardState", Err: err}
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

	respPayload, err := t.call(ctx, "admin.GetTopology", nil)
	if err != nil {
		return nil, err
	}

	var resp pb.TopologyResponse
	if err := proto.Unmarshal(respPayload, &resp); err != nil {
		return nil, &transport.RequestError{Method: "GetTopology", Err: err}
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

// callContext derives the per-call context for a unary QUIC RPC. When the
// configured timeout is positive it bounds the call's round-trip (stream
// open, request write, and response read) with context.WithTimeout,
// mirroring the REST transport's http.Client.Timeout. When the timeout is
// zero the context is returned unchanged with a no-op cancel, preserving
// the existing no-timeout behavior. Long-lived server-streaming RPCs (such
// as Watch) deliberately bypass this helper so they remain unbounded.
func (t *Transport) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if t.config.Timeout > 0 {
		return context.WithTimeout(ctx, t.config.Timeout)
	}
	return ctx, func() {}
}

// call opens a bidirectional QUIC stream, writes a wire-protocol request
// frame, reads the response frame, and maps non-OK status codes to SDK
// error types. Each RPC uses a dedicated stream that is closed after the
// response is read.
//
// The stream's write side is NOT closed until after ReadResponse returns --
// the deferred Close is the only close -- so its FIN is this client's
// acknowledgement that the response arrived, which the server waits for
// before it can close the connection during a system-scope migration's
// recycle (go-qrdb#550). See wire.ProtocolV2.
func (t *Transport) call(ctx context.Context, method string, payload []byte) ([]byte, error) {
	ctx, cancel := t.callContext(ctx)
	defer cancel()

	stream, err := t.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, &streamError{Err: err}
	}
	defer stream.Close()

	// OpenStreamSync only used ctx to establish the stream. Bound the reads
	// and writes on it by ctx too, so a server that never answers cannot hold
	// the caller past its deadline or its cancellation: the deadline is set on
	// the stream, and cancellation resets both directions, which also ends the
	// server's wait for this client's acknowledgement at once. stopCancel is
	// deferred after stream.Close, so it runs first and a context canceled
	// after the call returns cannot reset a stream that already finished.
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	stopCancel := context.AfterFunc(ctx, func() {
		stream.CancelRead(0)
		stream.CancelWrite(0)
	})
	defer stopCancel()

	reqID := t.requestID.Add(1)

	if err := wire.WriteRequest(stream, method, t.database, reqID, payload); err != nil {
		stream.CancelWrite(0)
		return nil, &transport.RequestError{Method: method, Err: callContextError(ctx, err)}
	}

	_, statusCode, respPayload, err := wire.ReadResponse(stream)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: callContextError(ctx, err)}
	}

	if statusCode != wire.StatusOK {
		return nil, mapWireError(method, statusCode, respPayload)
	}

	return respPayload, nil
}

// callContextError reports err as the context's own error when the context is
// what ended the stream operation, so a caller's errors.Is(err,
// context.DeadlineExceeded) or context.Canceled holds on this transport as it
// does on the others. The stream deadline and the context's timer are set from
// the same instant but fire independently, so a stream timeout on a context
// with a deadline is the deadline even if the context's timer has not fired
// yet.
func callContextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if _, ok := ctx.Deadline(); ok && errors.As(err, &netErr) && netErr.Timeout() {
		return context.DeadlineExceeded
	}
	return err
}

// buildTLSConfig constructs the TLS configuration from the transport
// config. QUIC always requires TLS; ALPN offers wire.ProtocolV2, the only
// protocol a qrdb QUIC listener accepts.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	return transport.BuildTLSConfig(transport.TLSBuildOptions{
		Custom:     t.config.TLSConfig,
		Enabled:    true,
		CAFile:     t.config.TLSCAFile,
		CertFile:   t.config.TLSCertFile,
		KeyFile:    t.config.TLSKeyFile,
		NextProtos: []string{wire.ProtocolV2},
	})
}

// typedWireErrors are the wire statuses this client reconstitutes into a TYPED
// SDK error rather than into a generic request error.
//
// A map of constructors rather than a switch: adding a typed reconstitution is
// one row, and every status NOT listed falls through to the generic error by
// construction — which is exactly the behaviour a client built before a status
// existed has to show when a newer server sends it.
//
// Both entries mirror the gRPC transport's mapGRPCError, including its guess:
// a bare NotFound says the subject is absent but not WHICH subject, so the
// shared mapper reconstitutes the key flavour and the call-site mappers below
// override it where the subject is known. That is the same division of labour
// mapUserNotFound and mapLeaseAbsent have on gRPC.
var typedWireErrors = map[uint16]func(msg string) error{
	// The node is healthy and does not implement this verb — an opcode its
	// state machine has no handler for, a subsystem it was constructed
	// without, or a method this server build does not register at all.
	// Reconstituting the typed error here is what lets a consumer degrade
	// gracefully over QUIC exactly as it can embedded, over gRPC and over
	// unix, instead of matching message text (go-qrdb#141).
	//
	// This mirrors mapGRPCError's codes.Unimplemented case exactly, including
	// its conflation of the two: gRPC returns Unimplemented both for a
	// capability refusal and for an unregistered method, and a
	// GuardedKVClient talking to an older server that does not carry the verbs
	// needs precisely that answer — "this node cannot do that" — not a generic
	// request failure it might retry forever (go-qrdb#124).
	wire.StatusUnimplemented: func(msg string) error {
		return &transport.CapabilityUnsupportedError{Message: msg}
	},

	// This case REPLACES a strings.Contains(msg, "key not found") test that
	// ran under StatusError, because the wire had no not-found status and the
	// message was the only signal left. That is the fail-open classification
	// go-qrdb#289 exists to delete: a transport failure whose text happened to
	// contain the phrase read as absence. The server now says which condition
	// it is and the client no longer reads the prose.
	wire.StatusNotFound: func(msg string) error {
		return &transport.KeyNotFoundError{Key: msg}
	},
}

// wireStatusPrefixes label the generic request error with the condition the
// server named, so a client log says which of the seventeen it was instead of
// repeating the server's sentence with no classification attached.
//
// StatusUnavailable's label used to read "barrier sealed", which was true of
// the only condition that produced it when it was written and wrong for the
// four that produce it now — a closed store, a drain, a non-leader and an
// exhausted cluster all arrive here.
var wireStatusPrefixes = map[uint16]string{
	wire.StatusUnauthenticated:    "unauthenticated: ",
	wire.StatusPermissionDenied:   "permission denied: ",
	wire.StatusUnavailable:        "unavailable: ",
	wire.StatusInvalidArgument:    "invalid argument: ",
	wire.StatusAlreadyExists:      "already exists: ",
	wire.StatusFailedPrecondition: "failed precondition: ",
	wire.StatusResourceExhausted:  "resource exhausted: ",
	wire.StatusAborted:            "aborted: ",
	wire.StatusOutOfRange:         "out of range: ",
	wire.StatusCanceled:           "canceled: ",
	wire.StatusDataLoss:           "data loss: ",
	wire.StatusDeadlineExceeded:   "deadline exceeded: ",
	wire.StatusUnknown:            "unknown: ",
}

// mapWireError translates a QUIC wire status code and payload into the
// appropriate SDK transport error type.
//
// A status this build does not recognise — a newer server's — keeps its
// number on the RequestError and its message unprefixed, so it degrades to the
// generic failure this client would have reported anyway and never to a
// misclassification.
func mapWireError(method string, statusCode uint16, payload []byte) error {
	msg := string(payload)

	if build, ok := typedWireErrors[statusCode]; ok {
		return build(msg)
	}

	return &transport.RequestError{
		Method:     method,
		StatusCode: int(statusCode),
		Err:        errors.New(wireStatusPrefixes[statusCode] + msg),
	}
}

// isWireNotFound reports whether err is the not-found the shared mapper
// reconstitutes from wire.StatusNotFound.
//
// The call-site mappers below ask this and then rebuild the error around the
// subject THEY named, so the typed error carries which user or which lease was
// missing rather than a descriptor parsed back out of the server's prose.
func isWireNotFound(err error) bool {
	var knf *transport.KeyNotFoundError
	return errors.As(err, &knf)
}

// stripScheme removes http:// or https:// prefixes from addresses so
// that QUIC DialAddr receives a bare host:port.
func stripScheme(addr string) string {
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "http://")
	return addr
}
