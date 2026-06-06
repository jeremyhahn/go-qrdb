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
	"crypto/x509"
	"errors"
	"os"
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
// always requires TLS; the ALPN protocol is set to "qrdb-quic" to
// match the server-side configuration.
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
// responds to a health check.
func (t *Transport) Healthy(ctx context.Context) bool {
	if t.closed.Load() || t.conn == nil {
		return false
	}

	resp, err := t.Health(ctx)
	if err != nil {
		return false
	}

	return resp.Status != ""
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
func (t *Transport) call(ctx context.Context, method string, payload []byte) ([]byte, error) {
	ctx, cancel := t.callContext(ctx)
	defer cancel()

	stream, err := t.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, &streamError{Err: err}
	}
	defer stream.Close()

	reqID := t.requestID.Add(1)

	if err := wire.WriteRequest(stream, method, reqID, payload); err != nil {
		stream.CancelWrite(0)
		return nil, &transport.RequestError{Method: method, Err: err}
	}

	// Signal end of request data so the server reads the complete frame.
	// Stream.Close sends a FIN on the write side while keeping the read
	// side open for the response. The deferred Close above is idempotent.
	if err := stream.Close(); err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}

	_, statusCode, respPayload, err := wire.ReadResponse(stream)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}

	if statusCode != wire.StatusOK {
		return nil, mapWireError(method, statusCode, respPayload)
	}

	return respPayload, nil
}

// buildTLSConfig constructs the TLS configuration from the transport
// config. QUIC always requires TLS; the ALPN protocol is set to
// "qrdb-quic" to match the server-side configuration.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	if t.config.TLSConfig != nil {
		cfg := t.config.TLSConfig.Clone()
		cfg.NextProtos = appendALPN(cfg.NextProtos)
		return cfg, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"qrdb-quic"},
	}

	// Load CA certificate pool.
	if t.config.TLSCAFile != "" {
		caCert, err := os.ReadFile(t.config.TLSCAFile)
		if err != nil {
			return nil, &transport.TLSError{Err: &caFileReadError{Path: t.config.TLSCAFile, Err: err}}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, &transport.TLSError{Err: &caParseError{Path: t.config.TLSCAFile}}
		}
		tlsCfg.RootCAs = pool
	}

	// Load client certificate for mTLS.
	if t.config.TLSCertFile != "" && t.config.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.config.TLSCertFile, t.config.TLSKeyFile)
		if err != nil {
			return nil, &transport.TLSError{Err: &certLoadError{Err: err}}
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}

// mapWireError translates a QUIC wire status code and payload into the
// appropriate SDK transport error type.
func mapWireError(method string, statusCode uint16, payload []byte) error {
	msg := string(payload)

	switch statusCode {
	case wire.StatusError:
		if strings.Contains(msg, "key not found") {
			return &transport.KeyNotFoundError{Key: msg}
		}
		return &transport.RequestError{
			Method:     method,
			StatusCode: int(statusCode),
			Err:        errors.New(msg),
		}
	case wire.StatusUnimplemented:
		return &transport.RequestError{
			Method:     method,
			StatusCode: int(statusCode),
			Err:        errors.New(msg),
		}
	case wire.StatusUnauthenticated:
		return &transport.RequestError{
			Method:     method,
			StatusCode: int(statusCode),
			Err:        errors.New("unauthenticated: " + msg),
		}
	case wire.StatusPermissionDenied:
		return &transport.RequestError{
			Method:     method,
			StatusCode: int(statusCode),
			Err:        errors.New("permission denied: " + msg),
		}
	default:
		return &transport.RequestError{
			Method:     method,
			StatusCode: int(statusCode),
			Err:        errors.New(msg),
		}
	}
}

// stripScheme removes http:// or https:// prefixes from addresses so
// that QUIC DialAddr receives a bare host:port.
func stripScheme(addr string) string {
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "http://")
	return addr
}

// appendALPN ensures "qrdb-quic" is present in the ALPN protocol list.
func appendALPN(protos []string) []string {
	for _, p := range protos {
		if p == "qrdb-quic" {
			return protos
		}
	}
	return append(protos, "qrdb-quic")
}
