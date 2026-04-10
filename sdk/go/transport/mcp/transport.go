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
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

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

	var (
		client *mcpclient.Client
		err    error
	)

	if t.mcpServer != nil {
		// In-process connection.
		client, err = mcpclient.NewInProcessClient(t.mcpServer)
	} else {
		// Network connection via Streamable HTTP.
		var httpOpts []mcptransport.StreamableHTTPCOption
		if tlsCfg, tlsErr := t.buildTLSConfig(); tlsErr != nil {
			return &transport.ConnectionError{
				Address: t.config.Address,
				Err:     tlsErr,
			}
		} else if tlsCfg != nil {
			httpOpts = append(httpOpts, mcptransport.WithHTTPBasicClient(&http.Client{
				Transport: &http.Transport{TLSClientConfig: tlsCfg},
			}))
		}
		client, err = mcpclient.NewStreamableHttpClient(t.config.Address, httpOpts...)
	}
	if err != nil {
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &initializeError{Err: err},
		}
	}

	// Start the transport.
	if startErr := client.Start(ctx); startErr != nil {
		return &transport.ConnectionError{
			Address: t.config.Address,
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

	_, err = client.Initialize(ctx, initReq)
	if err != nil {
		_ = client.Close()
		return &transport.ConnectionError{
			Address: t.config.Address,
			Err:     &initializeError{Err: err},
		}
	}

	t.client = client
	return nil
}

// buildTLSConfig constructs the TLS configuration from the transport config.
func (t *Transport) buildTLSConfig() (*tls.Config, error) {
	if t.config.TLSConfig != nil {
		return t.config.TLSConfig, nil
	}

	if !t.config.TLSEnabled {
		return nil, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	if t.config.TLSCAFile != "" {
		caCert, err := os.ReadFile(t.config.TLSCAFile)
		if err != nil {
			return nil, &transport.TLSError{Err: err}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, &transport.TLSError{Err: errors.New("failed to parse CA certificate")}
		}
		tlsCfg.RootCAs = pool
	}

	if t.config.TLSCertFile != "" && t.config.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.config.TLSCertFile, t.config.TLSKeyFile)
		if err != nil {
			return nil, &transport.TLSError{Err: err}
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
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
// MCP tool.
func (t *Transport) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	args := map[string]any{
		"key":   key,
		"value": string(value),
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
// a "not found" error.
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
		// Detect key-not-found errors from the tool response.
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: key}
		}
		return nil, err
	}

	return []byte(text), nil
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
		Status:   raw.Status,
		IsLeader: raw.Leader,
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
func (t *Transport) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_add_member", map[string]any{
		"shard_id":   shardID,
		"replica_id": replicaID,
		"raft_addr":  raftAddr,
	})
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
		KV:          raw.KV,
		MVCC:        raw.MVCC,
		Transaction: raw.Transaction,
		Batch:       raw.Batch,
		Watch:       raw.Watch,
		Lease:       raw.Lease,
		Index:       raw.Index,
		TimeSeries:  raw.TimeSeries,
		Vector:      raw.Vector,
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
	Status string `json:"Status"`
	Leader bool   `json:"Leader"`
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
	KV          bool `json:"kv"`
	MVCC        bool `json:"mvcc"`
	Transaction bool `json:"transaction"`
	Batch       bool `json:"batch"`
	Watch       bool `json:"watch"`
	Lease       bool `json:"lease"`
	Index       bool `json:"index"`
	TimeSeries  bool `json:"timeSeries"`
	Vector      bool `json:"vector"`
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
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	if args != nil {
		req.Params.Arguments = args
	}

	result, err := t.client.CallTool(ctx, req)
	if err != nil {
		return "", &transport.RequestError{Method: tool, Err: &toolCallError{Tool: tool, Err: err}}
	}

	if result.IsError {
		text := extractTextContent(result)
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

// isNotFoundError checks whether an error indicates a key-not-found
// condition. MCP tool errors propagate the service-layer error message,
// which contains "not found".
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found")
}

// applyReadArgs resolves read options and adds them to the MCP tool
// argument map.
func applyReadArgs(args map[string]any, opts ...transport.ReadOption) {
	ro := transport.ResolveReadOptions(opts...)
	if ro.ConsistencySet {
		args["consistency"] = ro.Consistency.String()
	}
	if ro.TimeoutMs > 0 {
		args["read_timeout_ms"] = ro.TimeoutMs
	}
	if ro.AllowStale {
		args["allow_stale"] = true
	}
}
