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

// Package mcp implements the MCP (Model Context Protocol) transport for
// the QRDB SDK. It communicates with the QRDB MCP server by calling
// registered tools via the mcp-go client library.
package mcp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.Transport   = (*Transport)(nil)
	_ transport.KVClient    = (*Transport)(nil)
	_ transport.AdminClient = (*Transport)(nil)
)

// Transport implements the QRDB SDK transport interface using the MCP
// protocol. It calls QRDB tools (qrdb_put, qrdb_get, etc.) on the MCP
// server and parses the text/JSON results back into SDK types.
//
// The transport supports two connection modes:
//   - Network: connects to an MCP server via HTTP using Streamable HTTP
//   - In-process: connects directly to an MCPServer instance (for testing)
type Transport struct {
	config    *transport.Config
	client    *mcpclient.Client
	mcpServer *mcpserver.MCPServer
	closed    atomic.Bool

	// database is the target database scope passed as the "database" argument
	// on data-plane tool calls. An empty string (or the default "system"
	// scope) selects the server's implicit bootstrap database. It is set via
	// WithDatabase.
	database string
}

// Compile-time assertion that the MCP transport supports database scoping.
var _ transport.DatabaseScoper = (*Transport)(nil)

// WithDatabase returns a database-scoped clone of the transport that passes the
// given database scope as the "database" argument on its data-plane tool calls
// while sharing the parent's MCP client connection. Scoping to the empty string
// or the default "system" database yields an unscoped clone (no database
// argument is added). It implements transport.DatabaseScoper.
func (t *Transport) withDatabase(database string) *Transport {
	clone := &Transport{
		config:    t.config,
		client:    t.client,
		mcpServer: t.mcpServer,
		database:  transport.NormalizeDatabaseScope(database),
	}
	clone.closed.Store(t.closed.Load())
	return clone
}

// WithDatabase implements transport.DatabaseScoper. Scoping a network
// transport never fails: the database travels with every data-plane request
// and the server resolves it.
func (t *Transport) WithDatabase(database string) (transport.Transport, error) {
	return t.withDatabase(database), nil
}

// mcpDataPlaneExactTools is the set of data-plane MCP tool names with no shared
// prefix (KV and watch). Only data-plane tool calls carry the database scope
// argument; management, admin, and barrier tools are tenant- or cluster-global
// and ignore it.
// The three guarded write verbs belong here for the same reason the plain
// ones do, and more sharply: a guard decided against the WRONG database would
// compare a claim's value against a key of the same name in another keyspace,
// so a missing scope argument would not fail — it would answer confidently and
// wrongly.
var mcpDataPlaneExactTools = map[string]bool{
	"qrdb_put":                true,
	"qrdb_get":                true,
	"qrdb_delete":             true,
	"qrdb_list":               true,
	"qrdb_scan":               true,
	"qrdb_watch":              true,
	"qrdb_put_if_absent":      true,
	"qrdb_compare_and_delete": true,
	"qrdb_compare_and_swap":   true,
}

// mcpDataPlanePrefixes are the name prefixes of the remaining data-plane MCP
// tool families (MVCC, transaction, batch, lease, secondary index, time-series,
// and vector).
var mcpDataPlanePrefixes = []string{
	"qrdb_mvcc_",
	"qrdb_tx_",
	"qrdb_batch_",
	"qrdb_lease_",
	"qrdb_index_",
	"qrdb_ts_",
	"qrdb_vector_",
}

// isDataPlaneTool reports whether the named tool targets a data-plane endpoint
// and therefore should carry the database scope argument.
func isDataPlaneTool(tool string) bool {
	if mcpDataPlaneExactTools[tool] {
		return true
	}
	for _, p := range mcpDataPlanePrefixes {
		if strings.HasPrefix(tool, p) {
			return true
		}
	}
	return false
}

// New creates a new MCP transport with the given options. The Address
// option should be the MCP server URL (e.g., "http://localhost:63004/mcp").
// The transport is not connected until Connect is called.
func New(opts ...transport.Option) (*Transport, error) {
	cfg := transport.ApplyOptions(opts...)

	if cfg.Address == "" {
		return nil, &transport.ConfigError{Field: "Address", Reason: "MCP server URL is required"}
	}

	return &Transport{config: cfg}, nil
}

// NewInProcess creates a new MCP transport that connects directly to an
// MCPServer instance in the same process. This avoids network overhead
// and is primarily used for testing.
func NewInProcess(srv *mcpserver.MCPServer) (*Transport, error) {
	if srv == nil {
		return nil, &transport.ConfigError{Field: "MCPServer", Reason: "MCPServer must not be nil"}
	}

	return &Transport{
		config:    transport.DefaultConfig(),
		mcpServer: srv,
	}, nil
}

// Connect establishes the MCP client connection and performs protocol
// initialization. For network transports, it connects to the HTTP
// endpoint; for in-process transports, it creates a direct connection.
func (t *Transport) Connect(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	client, err := t.dialMCP(ctx, t.config.Address, nil)
	if err != nil {
		return err
	}

	t.client = client
	return nil
}

// connCapture pins a dialled MCP client's HTTP traffic to one connection and
// records that connection's TLS state, so a tenant barrier init's begin and
// complete tool calls travel -- and are proved -- over one TLS session. Should
// the server close the connection between them, complete is sent on another
// one and the node refuses it as a connection mismatch: the pin fails closed.
type connCapture struct {
	next  http.RoundTripper
	state atomic.Pointer[tls.ConnectionState]
}

// RoundTrip sends r and records the TLS state of the connection it was
// answered on.
func (c *connCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err == nil && resp.TLS != nil {
		c.state.Store(resp.TLS)
	}
	return resp, err
}

// TLSState returns the TLS state of the connection the last response arrived
// on, or nil when none arrived over TLS.
func (c *connCapture) TLSState() *tls.ConnectionState {
	return c.state.Load()
}

// dialMCP builds and initializes an MCP client against address: an
// in-process client when this transport wraps an *mcpserver.MCPServer
// directly, otherwise a Streamable HTTP client. It is Connect's network
// setup, factored out so RedeemInvitation can dial the separate, public
// enrolment endpoint (enrollAddress) the same way without disturbing this
// transport's own t.client. A non-nil capture pins the network client to one
// connection and records its TLS state (connCapture); an in-process client
// has no connection, and capture records nothing.
func (t *Transport) dialMCP(ctx context.Context, address string, capture *connCapture) (*mcpclient.Client, error) {
	var (
		client *mcpclient.Client
		err    error
	)

	if t.mcpServer != nil {
		// In-process connection.
		client, err = mcpclient.NewInProcessClient(t.mcpServer)
	} else {
		// Network connection via Streamable HTTP.
		tlsCfg, tlsErr := t.buildTLSConfig()
		if tlsErr != nil {
			return nil, &transport.ConnectionError{
				Address: address,
				Err:     tlsErr,
			}
		}
		base := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck,forcetypeassert // net/http always installs a *http.Transport as DefaultTransport
		if tlsCfg != nil {
			base.TLSClientConfig = tlsCfg
		}
		// A call made with a context bound by transport.WithSOPIN carries the
		// SO PIN in the X-QRDB-SO-PIN header of its HTTP request -- over
		// https to the configured host only, on every hop including
		// redirects.
		rt := transport.SOPINRoundTripper(base, "mcp", transport.ConfiguredHost(address))
		if capture != nil {
			base.MaxConnsPerHost = 1
			capture.next = rt
			rt = capture
		}
		client, err = mcpclient.NewStreamableHttpClient(address,
			mcptransport.WithHTTPBasicClient(&http.Client{Transport: rt}))
	}
	if err != nil {
		return nil, &transport.ConnectionError{
			Address: address,
			Err:     &initializeError{Err: err},
		}
	}

	// Start the transport.
	if startErr := client.Start(ctx); startErr != nil {
		return nil, &transport.ConnectionError{
			Address: address,
			Err:     &initializeError{Err: startErr},
		}
	}

	// Perform MCP protocol initialization.
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "qrdb-sdk-go",
		Version: "1.0.0",
	}

	initCtx, cancel := t.callContext(ctx)
	_, err = client.Initialize(initCtx, initReq)
	cancel()
	if err != nil {
		_ = client.Close()
		return nil, &transport.ConnectionError{
			Address: address,
			Err:     &initializeError{Err: err},
		}
	}

	return client, nil
}

// buildTLSConfig constructs the TLS configuration from the transport config.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	return transport.BuildTLSConfig(transport.TLSBuildOptions{
		Custom:   t.config.TLSConfig,
		Enabled:  t.config.TLSEnabled,
		CAFile:   t.config.TLSCAFile,
		CertFile: t.config.TLSCertFile,
		KeyFile:  t.config.TLSKeyFile,
	})
}

// Close shuts down the MCP client and releases all resources.
func (t *Transport) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	if t.client != nil {
		return t.client.Close()
	}
	return nil
}

// Healthy returns true if the MCP server responds to a health check tool.
func (t *Transport) Healthy(ctx context.Context) bool {
	if t.closed.Load() || t.client == nil {
		return false
	}

	result, err := t.callTool(ctx, "qrdb_health", nil)
	if err != nil {
		return false
	}

	return result != ""
}

// Request performs a generic tool call by name. The req parameter should
// be a map[string]any of tool arguments, and resp is ignored (tool
// results are returned as text).
func (t *Transport) Request(ctx context.Context, method string, req, resp any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args, _ := req.(map[string]any)
	_, err := t.callTool(ctx, method, args)
	return err
}

// ---------------------------------------------------------------------------
// KVClient implementation
// ---------------------------------------------------------------------------

// Put stores a key-value pair through Raft consensus. Calls the qrdb_put
// MCP tool. The value is sent base64-encoded (RFC 4648), matching the
// server's binary-safe wire contract, so arbitrary binary content round-trips
// byte-for-byte.
func (t *Transport) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"key":   key,
		"value": base64.StdEncoding.EncodeToString(value),
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		args["write_mode"] = wo.Mode.String()
	}

	_, err := t.callTool(ctx, "qrdb_put", args)
	return err
}

// Get retrieves a value by key with a linearizable read. Calls the
// qrdb_get MCP tool. Returns KeyNotFoundError when the tool returns
// a "not found" error. The server returns the value base64-encoded (RFC
// 4648), so it is decoded here to recover the original bytes byte-for-byte.
func (t *Transport) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"key": key,
	}

	applyReadArgs(args, opts...)

	text, err := t.callTool(ctx, "qrdb_get", args)
	if err != nil {
		// The key this call site asked for, not one read back out of the
		// server's prose.
		if isToolNotFound(err) {
			return nil, &transport.KeyNotFoundError{Key: key}
		}
		return nil, err
	}

	value, decodeErr := base64.StdEncoding.DecodeString(text)
	if decodeErr != nil {
		return nil, &transport.RequestError{Method: "Get", Err: &valueDecodeError{Err: decodeErr}}
	}

	return value, nil
}

// Delete removes a key through Raft consensus. Calls the qrdb_delete
// MCP tool.
func (t *Transport) Delete(ctx context.Context, key string, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"key": key,
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		args["write_mode"] = wo.Mode.String()
	}

	_, err := t.callTool(ctx, "qrdb_delete", args)
	return err
}

// List returns sorted keys matching the given prefix. Calls the
// qrdb_list MCP tool and parses the JSON array result.
func (t *Transport) List(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if prefix != "" {
		args["prefix"] = prefix
	}

	applyReadArgs(args, opts...)

	text, err := t.callTool(ctx, "qrdb_list", args)
	if err != nil {
		return nil, err
	}

	var keys []string
	if jsonErr := json.Unmarshal([]byte(text), &keys); jsonErr != nil {
		return nil, &transport.RequestError{Method: "List", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix. Calls the
// qrdb_scan MCP tool and parses the JSON map result.
func (t *Transport) Scan(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{}
	if prefix != "" {
		args["prefix"] = prefix
	}

	applyReadArgs(args, opts...)

	text, err := t.callTool(ctx, "qrdb_scan", args)
	if err != nil {
		return nil, err
	}

	var pairs map[string][]byte
	if jsonErr := json.Unmarshal([]byte(text), &pairs); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Scan", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return pairs, nil
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

// Health returns the health status of the QRDB node. Calls the
// qrdb_health MCP tool and parses the JSON response. The server
// serializes service.HealthStatus (PascalCase fields), which is
// decoded into an intermediate struct and mapped to the SDK type.
func (t *Transport) Health(ctx context.Context) (*transport.HealthResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_health", nil)
	if err != nil {
		return nil, err
	}

	var raw serverHealthStatus
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Health", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.HealthResponse{
		Status:             raw.Status,
		IsLeader:           raw.Leader,
		DiagnosticsDropped: raw.DiagnosticsDropped,
	}, nil
}

// Status returns detailed node and shard status information. Calls the
// qrdb_status MCP tool and parses the JSON response.
func (t *Transport) Status(ctx context.Context) (*transport.StatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_status", nil)
	if err != nil {
		return nil, err
	}

	var raw serverStatusInfo
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Status", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.StatusResponse{
		NodeID:      raw.NodeID,
		RaftAddress: raw.RaftAddress,
		IsLeader:    raw.IsLeader,
		LeaderID:    raw.LeaderID,
		ShardCount:  raw.ShardCount,
		IsDraining:  raw.IsDraining,
	}, nil
}

// Peers returns the peer membership for all loaded shards. Calls the
// qrdb_peers MCP tool and parses the JSON response.
func (t *Transport) Peers(ctx context.Context) (*transport.PeersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_peers", nil)
	if err != nil {
		return nil, err
	}

	var raw serverPeersInfo
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Peers", Err: &jsonDecodeError{Err: jsonErr}}
	}

	shards := make([]transport.ShardPeers, 0, len(raw.Shards))
	for _, sp := range raw.Shards {
		peers := make([]transport.PeerInfo, 0, len(sp.Peers))
		for _, p := range sp.Peers {
			peers = append(peers, transport.PeerInfo{
				ReplicaID: p.ReplicaID,
				Address:   p.Address,
				IsLeader:  p.IsLeader,
			})
		}
		shards = append(shards, transport.ShardPeers{
			ShardID:  sp.ShardID,
			LeaderID: sp.LeaderID,
			Peers:    peers,
		})
	}

	return &transport.PeersResponse{Shards: shards}, nil
}

// AddMember adds a new replica to the specified shard. Calls the
// qrdb_add_member MCP tool.
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"shard_id":   shardID,
		"replica_id": replicaID,
		"raft_addr":  raftAddr,
	}
	if identity != "" {
		args["identity"] = identity
	}
	_, err := t.callTool(ctx, "qrdb_add_member", args)
	return err
}

// RemoveMember removes a replica from the specified shard. Calls the
// qrdb_remove_member MCP tool.
func (t *Transport) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_remove_member", map[string]any{
		"shard_id":   shardID,
		"replica_id": replicaID,
	})
	return err
}

// PromoteMember promotes a committed observer of the specified shard to a
// voter. Calls the qrdb_promote_member MCP tool. Implements
// transport.MemberPromoter.
func (t *Transport) PromoteMember(ctx context.Context, shardID, replicaID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_promote_member", map[string]any{
		"shard_id":   shardID,
		"replica_id": replicaID,
	})
	return err
}

// RetireJoinIdentity permanently refuses any future admission for a node
// identity. Calls the qrdb_retire_join_identity MCP tool.
func (t *Transport) RetireJoinIdentity(ctx context.Context, nodeID, reason string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_retire_join_identity", map[string]any{
		"node_id": nodeID,
		"reason":  reason,
	})
	return err
}

// RequestSnapshot triggers an asynchronous snapshot for the specified
// shard. Calls the qrdb_snapshot MCP tool.
func (t *Transport) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_snapshot", map[string]any{
		"shard_id": shardID,
	})
	return err
}

// Drain transitions the host into drain mode. Calls the qrdb_drain
// MCP tool.
func (t *Transport) Drain(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_drain", nil)
	return err
}

// Capabilities returns the compile-time feature set of the QRDB node.
// Calls the qrdb_capabilities MCP tool and parses the JSON response.
func (t *Transport) Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_capabilities", nil)
	if err != nil {
		return nil, err
	}

	var raw serverCapabilities
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Capabilities", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.CapabilitiesResponse{
		Mode:        raw.Mode,
		KV:          raw.KV,
		MVCC:        raw.MVCC,
		Transaction: raw.Transaction,
		Batch:       raw.Batch,
		Watch:       raw.Watch,
		Lease:       raw.Lease,
		Lock:        raw.Lock,
		Index:       raw.Index,
		Cluster:     raw.Cluster,
		Backup:      raw.Backup,
		Rebalance:   raw.Rebalance,
		TimeSeries:  raw.TimeSeries,
		Vector:      raw.Vector,
		SQL:         raw.SQL,
		MySQL:       raw.MySQL,
		PostgreSQL:  raw.PostgreSQL,
		Etcd:        raw.Etcd,
		FTS:         raw.FTS,

		UnencryptedAtRest: raw.UnencryptedAtRest,
	}, nil
}

// StartShard initializes and starts a Raft group for the given shard.
// Calls the qrdb_start_shard MCP tool.
func (t *Transport) StartShard(ctx context.Context, req *transport.StartShardRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"shard_id":   req.ShardID,
		"replica_id": req.ReplicaID,
		"join":       req.Join,
	}

	if len(req.Members) > 0 {
		// Serialize the members map as JSON. Keys must be strings.
		raw := make(map[string]string, len(req.Members))
		for k, v := range req.Members {
			raw[strconv.FormatUint(k, 10)] = v
		}
		data, jsonErr := json.Marshal(raw)
		if jsonErr != nil {
			return &transport.RequestError{Method: "StartShard", Err: &jsonEncodeError{Err: jsonErr}}
		}
		args["members_json"] = string(data)
	}

	_, err := t.callTool(ctx, "qrdb_start_shard", args)
	return err
}

// StopShard stops the Raft group for the given shard. Calls the
// qrdb_stop_shard MCP tool.
func (t *Transport) StopShard(ctx context.Context, shardID uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_stop_shard", map[string]any{
		"shard_id": shardID,
	})
	return err
}

// GetShardState returns the Raft state for a shard. Calls the
// qrdb_shard_state MCP tool and parses the JSON response.
func (t *Transport) GetShardState(ctx context.Context, shardID uint64) (*transport.ShardStateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_shard_state", map[string]any{
		"shard_id": shardID,
	})
	if err != nil {
		return nil, err
	}

	var raw serverShardState
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetShardState", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.ShardStateResponse{
		ShardID:           raw.ShardID,
		ConfigChangeIndex: raw.ConfigChangeIndex,
		Term:              raw.Term,
		LeaderID:          raw.LeaderID,
		HasLeader:         raw.HasLeader,
	}, nil
}

// GetTopology returns the full cluster topology. Calls the
// qrdb_topology MCP tool and parses the JSON response.
func (t *Transport) GetTopology(ctx context.Context) (*transport.TopologyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_topology", nil)
	if err != nil {
		return nil, err
	}

	var raw serverTopology
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetTopology", Err: &jsonDecodeError{Err: jsonErr}}
	}

	shards := make([]transport.ShardInfo, 0, len(raw.Shards))
	for _, si := range raw.Shards {
		shards = append(shards, transport.ShardInfo{
			ShardID:   si.ShardID,
			ReplicaID: si.ReplicaID,
			IsLeader:  si.IsLeader,
			LeaderID:  si.LeaderID,
			Nodes:     si.Nodes,
		})
	}

	return &transport.TopologyResponse{
		NodeID:      raw.NodeID,
		RaftAddress: raw.RaftAddress,
		ShardCount:  raw.ShardCount,
		Shards:      shards,
	}, nil
}

// MintJoinToken mints a single-use, identity-bound member-side CSR join
// token. Calls the qrdb_mint_join_token MCP tool. Implements
// transport.ClusterJoinTokenMinter.
func (t *Transport) MintJoinToken(ctx context.Context, req *transport.MintJoinTokenRequest) (*transport.MintJoinTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{"node_id": req.NodeID}
	if req.TTLSeconds > 0 {
		args["ttl_seconds"] = req.TTLSeconds
	}
	text, err := t.callTool(ctx, "qrdb_mint_join_token", args)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "MintJoinToken", Err: &jsonDecodeError{Err: jsonErr}}
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, raw.ExpiresAt)
	if parseErr != nil {
		return nil, &transport.RequestError{Method: "MintJoinToken", Err: parseErr}
	}
	return &transport.MintJoinTokenResponse{
		Token:     raw.Token,
		ExpiresAt: expiresAt,
	}, nil
}

var _ transport.ClusterJoinTokenMinter = (*Transport)(nil)

// AddObserverMember adds a new replica to the specified shard as a
// non-voting observer rather than a voter. Calls the
// qrdb_add_observer_member MCP tool. Implements transport.ObserverMemberAdder.
func (t *Transport) AddObserverMember(ctx context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"shard_id":   shardID,
		"replica_id": replicaID,
		"raft_addr":  raftAddr,
	}
	if identity != "" {
		args["identity"] = identity
	}
	_, err := t.callTool(ctx, "qrdb_add_observer_member", args)
	return err
}

var _ transport.ObserverMemberAdder = (*Transport)(nil)

// ListPendingJoinRequests lists tokenless member-side CSR join requests
// awaiting an operator's decision. Calls the
// qrdb_list_pending_join_requests MCP tool. Implements
// transport.JoinRequestReviewer.
func (t *Transport) ListPendingJoinRequests(ctx context.Context) ([]*transport.PendingJoinRequest, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_list_pending_join_requests", nil)
	if err != nil {
		return nil, err
	}

	var raw []transport.PendingJoinRequest
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListPendingJoinRequests", Err: &jsonDecodeError{Err: jsonErr}}
	}
	out := make([]*transport.PendingJoinRequest, len(raw))
	for i := range raw {
		out[i] = &raw[i]
	}
	return out, nil
}

// ApproveJoinRequest binds id's queued CSR to an operator-chosen node
// identity. Calls the qrdb_approve_join_request MCP tool. Implements
// transport.JoinRequestReviewer.
func (t *Transport) ApproveJoinRequest(ctx context.Context, id, nodeID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_approve_join_request", map[string]any{
		"id":      id,
		"node_id": nodeID,
	})
	return err
}

// RejectJoinRequest records a terminal refusal of id's queued CSR. Calls
// the qrdb_reject_join_request MCP tool. Implements
// transport.JoinRequestReviewer.
func (t *Transport) RejectJoinRequest(ctx context.Context, id, reason string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{"id": id}
	if reason != "" {
		args["reason"] = reason
	}
	_, err := t.callTool(ctx, "qrdb_reject_join_request", args)
	return err
}

var _ transport.JoinRequestReviewer = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Server-side JSON decode types
// ---------------------------------------------------------------------------
//
// The MCP server serializes service-layer types (HealthStatus, StatusInfo,
// PeersInfo) using json.Marshal with Go's default PascalCase field names
// (no json tags). The SDK transport types use snake_case json tags. These
// intermediate structs decode the server's PascalCase JSON and are then
// mapped to the SDK transport response types.

// serverHealthStatus mirrors service.HealthStatus for JSON decoding.
type serverHealthStatus struct {
	Status             string `json:"Status"`
	Leader             bool   `json:"Leader"`
	DiagnosticsDropped uint64 `json:"DiagnosticsDropped"`
}

// serverStatusInfo mirrors service.StatusInfo for JSON decoding.
type serverStatusInfo struct {
	NodeID      string `json:"NodeID"`
	RaftAddress string `json:"RaftAddress"`
	IsLeader    bool   `json:"IsLeader"`
	LeaderID    uint64 `json:"LeaderID"`
	ShardCount  int    `json:"ShardCount"`
	IsDraining  bool   `json:"IsDraining"`
}

// serverPeersInfo mirrors service.PeersInfo for JSON decoding.
type serverPeersInfo struct {
	Shards []serverShardPeers `json:"Shards"`
}

// serverShardPeers mirrors service.ShardPeers for JSON decoding.
type serverShardPeers struct {
	ShardID  uint64           `json:"ShardID"`
	LeaderID uint64           `json:"LeaderID"`
	Peers    []serverPeerInfo `json:"Peers"`
}

// serverPeerInfo mirrors service.PeerInfo for JSON decoding.
type serverPeerInfo struct {
	ReplicaID uint64 `json:"ReplicaID"`
	Address   string `json:"Address"`
	IsLeader  bool   `json:"IsLeader"`
}

// serverCapabilities mirrors capabilities.Capabilities for JSON decoding.
// The capabilities package uses json tags with camelCase, matching the
// SDK transport's CapabilitiesResponse field names.
type serverCapabilities struct {
	Mode        string `json:"mode"`
	KV          bool   `json:"kv"`
	MVCC        bool   `json:"mvcc"`
	Transaction bool   `json:"transaction"`
	Batch       bool   `json:"batch"`
	Watch       bool   `json:"watch"`
	Lease       bool   `json:"lease"`
	Lock        bool   `json:"lock"`
	Index       bool   `json:"index"`
	Cluster     bool   `json:"cluster"`
	Backup      bool   `json:"backup"`
	Rebalance   bool   `json:"rebalance"`
	TimeSeries  bool   `json:"timeSeries"`
	Vector      bool   `json:"vector"`
	SQL         bool   `json:"sql"`
	MySQL       bool   `json:"mysql"`
	PostgreSQL  bool   `json:"postgresql"`
	Etcd        bool   `json:"etcd"`
	FTS         bool   `json:"fts"`

	UnencryptedAtRest []string `json:"unencryptedAtRest"`
}

// serverShardState mirrors service.ShardStateResponse for JSON decoding.
// The service layer uses PascalCase field names with no json tags.
type serverShardState struct {
	ShardID           uint64 `json:"ShardID"`
	ConfigChangeIndex uint64 `json:"ConfigChangeIndex"`
	Term              uint64 `json:"Term"`
	LeaderID          uint64 `json:"LeaderID"`
	HasLeader         bool   `json:"HasLeader"`
}

// serverTopology mirrors service.TopologyResponse for JSON decoding.
type serverTopology struct {
	NodeID      string            `json:"NodeID"`
	RaftAddress string            `json:"RaftAddress"`
	ShardCount  int               `json:"ShardCount"`
	Shards      []serverShardInfo `json:"Shards"`
}

// serverShardInfo mirrors service.TopologyShardInfo for JSON decoding.
type serverShardInfo struct {
	ShardID   uint64            `json:"ShardID"`
	ReplicaID uint64            `json:"ReplicaID"`
	IsLeader  bool              `json:"IsLeader"`
	LeaderID  uint64            `json:"LeaderID"`
	Nodes     map[uint64]string `json:"Nodes"`
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// callTool calls an MCP tool by name with the given arguments and returns
// the text content from the first TextContent result. If the result
// contains an error (IsError=true), a toolResultError is returned.
func (t *Transport) callTool(ctx context.Context, tool string, args map[string]any) (string, error) {
	// Carry the database scope on data-plane tool calls so the server resolves
	// the request against the correct (tenant, database) keyspace.
	if t.database != "" && isDataPlaneTool(tool) {
		if args == nil {
			args = map[string]any{}
		}
		args["database"] = t.database
	}

	ctx, cancel := t.callContext(ctx)
	defer cancel()
	return callToolOn(ctx, t.client, tool, args)
}

// callContext derives the per-call context for one MCP request. When the
// configured timeout is positive it bounds the request's round-trip with
// context.WithTimeout, as the REST transport's http.Client.Timeout, the gRPC
// transport's unary interceptor and the QUIC transport's callContext bound
// theirs; a caller's earlier deadline still wins. When the timeout is zero
// the context is returned unchanged with a no-op cancel.
func (t *Transport) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if t.config.Timeout > 0 {
		return context.WithTimeout(ctx, t.config.Timeout)
	}
	return ctx, func() {}
}

// callToolOn is callTool's wire logic against an explicit client, so
// RedeemInvitation can run it against the one-off client it dials for the
// separate, public enrolment endpoint rather than this transport's own
// t.client.
func callToolOn(ctx context.Context, client *mcpclient.Client, tool string, args map[string]any) (string, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	if args != nil {
		req.Params.Arguments = args
	}

	result, err := client.CallTool(ctx, req)
	if err != nil {
		return "", &transport.RequestError{Method: tool, Err: &toolCallError{Tool: tool, Err: err}}
	}

	if result.IsError {
		text := extractTextContent(result)

		// A coded failure is classified by its code; an UNCODED one degrades
		// to the generic request error this transport has always returned.
		//
		// Uncoded is not a fault to paper over. It is what a tool's ARGUMENT
		// refusals still are — the server builds those results itself and
		// stamping a code on them would mean guessing one from a string — and
		// it is also what an older server sends. Both want the same answer:
		// report the failure, classify nothing (go-qrdb#438).
		if payload, ok := wire.DecodeErrorPayload(result.StructuredContent); ok {
			return "", mapToolError(tool, payload)
		}
		return "", &transport.RequestError{Method: tool, Err: &toolResultError{Tool: tool, Message: text}}
	}

	text := extractTextContent(result)
	if text == "" && len(result.Content) > 0 {
		// Content exists but isn't text.
		return "", &transport.RequestError{Method: tool, Err: &unexpectedContentTypeError{Tool: tool}}
	}

	return text, nil
}

// extractTextContent retrieves the text from the first TextContent item
// in a CallToolResult. Returns an empty string if no text content is found.
func extractTextContent(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}

	for _, content := range result.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			return tc.Text
		}
	}

	return ""
}

// leaseExpiredMessage reports whether a lease not-found says the lease
// EXPIRED rather than never existed.
//
// It is only ever consulted from mapLeaseAbsent, AFTER the failure has been
// established as a not-found by its code and at a call site that already knows
// its subject is a lease. That gate is the whole difference between this and
// the classification go-qrdb#438 removed: nothing but a not-found reaches it,
// so a server fault carrying the words can no longer be read as a gone lease.
//
// A message test remains necessary because no transport in this repository has
// an expired-specific code. gRPC maps service.LeaseNotFoundError and
// service.LeaseExpiredError both to codes.NotFound, and its own mapLeaseAbsent
// splits them exactly this way; MCP matching it is what keeps one condition
// from meaning two different things depending on how it was reached.
func leaseExpiredMessage(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "lease expired")
}

// applyReadArgs resolves read options and adds them to the MCP tool
// argument map.
func applyReadArgs(args map[string]any, opts ...transport.ReadOption) {
	ro := transport.ResolveReadOptions(opts...)
	switch {
	case ro.ConsistencySet:
		args["consistency"] = ro.Consistency.String()
	case ro.AllowStale:
		// WithAllowStale() on its own (no explicit level) is an eventual read.
		// Express it as the consistency level the server actually parses, since
		// the server-side tool handlers read "consistency", not "allow_stale".
		args["consistency"] = transport.ConsistencyEventual.String()
	}
	if ro.TimeoutMs > 0 {
		args["read_timeout_ms"] = ro.TimeoutMs
	}
	if ro.AllowStale {
		args["allow_stale"] = true
	}
}
