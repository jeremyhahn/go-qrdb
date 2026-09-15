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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// databaseTools returns the MCP tool registrations for database management
// operations.
func databaseTools(dbSvc service.DatabaseServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolDatabaseCreate(),
			Perm:    permission.DatabasesCreate,
			Handler: handleDatabaseCreate(dbSvc),
		},
		{
			Tool:    toolDatabaseList(),
			Perm:    permission.DatabasesList,
			Handler: handleDatabaseList(dbSvc),
		},
		{
			Tool:    toolDatabaseGet(),
			Perm:    permission.DatabasesRead,
			Handler: handleDatabaseGet(dbSvc),
		},
		{
			Tool:    toolDatabaseDrop(),
			Perm:    permission.DatabasesDelete,
			Handler: handleDatabaseDrop(dbSvc),
		},
		{
			Tool:    toolDatabaseUpdateProtocol(),
			Perm:    permission.DatabasesUpdate,
			Handler: handleDatabaseUpdateProtocol(dbSvc),
		},
		{
			Tool:    toolDatabaseUpdateEtcd(),
			Perm:    permission.DatabasesUpdate,
			Handler: handleDatabaseUpdateEtcd(dbSvc),
		},
		{
			Tool:    toolDatabaseUpdateRaft(),
			Perm:    permission.DatabasesUpdate,
			Handler: handleDatabaseUpdateRaft(dbSvc),
		},
		{
			Tool:    toolDatabaseScale(),
			Perm:    permission.DatabasesScale,
			Handler: handleDatabaseScale(dbSvc),
		},
		{
			Tool:    toolDatabaseUpdateBarrier(),
			Perm:    permission.DatabasesUpdate,
			Handler: handleDatabaseUpdateBarrier(dbSvc),
		},
		{
			Tool:    toolDatabaseUpdateConnection(),
			Perm:    permission.DatabasesUpdate,
			Handler: handleDatabaseUpdateConnection(dbSvc),
		},
		{
			Tool:    toolDatabaseSeal(),
			Perm:    permission.DatabasesSeal,
			Handler: handleDatabaseSeal(dbSvc),
		},
		{
			Tool:    toolDatabaseUnseal(),
			Perm:    permission.DatabasesUnseal,
			Handler: handleDatabaseUnseal(dbSvc),
		},
		{
			Tool:    toolDatabaseBarrierStatus(),
			Perm:    permission.DatabasesBarrierStatus,
			Handler: handleDatabaseBarrierStatus(dbSvc),
		},
	}
}

// toolDatabaseCreate defines the qrdb_database_create tool schema.
func toolDatabaseCreate() mcp.Tool {
	return mcp.NewTool("qrdb_database_create",
		mcp.WithDescription("Create a new database with the specified name, engine, and backend."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name. Must contain only alphanumeric characters, hyphens, and underscores."),
		),
		mcp.WithString("engine",
			mcp.Required(),
			mcp.Description("The data engine type: kv, timeseries, or vector."),
		),
		mcp.WithString("backend",
			mcp.Required(),
			mcp.Description("The storage backend type: memory, file, pebble, or raft."),
		),
		mcp.WithNumber("replicas",
			mcp.Description("Replication factor for a dedicated-raft database. Meaningful only for raft-backed databases; unset defaults to 1."),
		),
		mcp.WithBoolean("mysql",
			mcp.Description("Enable MySQL wire protocol for this database."),
		),
		mcp.WithBoolean("postgresql",
			mcp.Description("Enable PostgreSQL wire protocol for this database."),
		),
		mcp.WithString("raft_cluster_mode",
			mcp.Description("Raft cluster mode: none, local, or distributed."),
		),
		mcp.WithNumber("raft_shard_id",
			mcp.Description("Raft shard ID for distributed mode."),
		),
		mcp.WithNumber("raft_replica_id",
			mcp.Description("Raft replica ID for distributed mode."),
		),
		mcp.WithString("raft_address",
			mcp.Description("This node's Raft address (host:port) for distributed mode."),
		),
		mcp.WithString("discovery_method",
			mcp.Description("Peer discovery method: static, multicast, dns, or token."),
		),
		// Static discovery
		mcp.WithString("static_shared_secret",
			mcp.Description("Shared secret for static peer authentication."),
		),
		// Multicast discovery
		mcp.WithString("multicast_shared_secret",
			mcp.Description("HMAC shared secret for multicast peer authentication (required for multicast)."),
		),
		mcp.WithString("multicast_group",
			mcp.Description("Multicast group address (default: 239.255.77.77:63777)."),
		),
		mcp.WithString("multicast_interface",
			mcp.Description("Network interface for multicast discovery."),
		),
		mcp.WithNumber("multicast_min_peers",
			mcp.Description("Minimum number of peers to discover via multicast."),
		),
		// DNS discovery
		mcp.WithString("dns_domain",
			mcp.Description("DNS domain for SRV record lookup (required for dns)."),
		),
		mcp.WithString("dns_service",
			mcp.Description("SRV service name (default: qrdb)."),
		),
		mcp.WithString("dns_proto",
			mcp.Description("SRV protocol (default: udp)."),
		),
		// Token discovery
		mcp.WithString("token_endpoint",
			mcp.Description("Discovery service endpoint URL (required for token)."),
		),
		mcp.WithString("token_value",
			mcp.Description("Bootstrap token for cluster discovery (required for token)."),
		),
		mcp.WithNumber("token_cluster_size",
			mcp.Description("Expected cluster size for token discovery (required for token)."),
		),
		mcp.WithBoolean("barrier_enabled",
			mcp.Description("Bootstrap the database's encryption barrier at creation time."),
		),
		mcp.WithString("barrier_mode",
			mcp.Description("Target keyed strategy for the barrier: \"passphrase\" or \"shamir\"."),
		),
		mcp.WithNumber("barrier_shares_required",
			mcp.Description("Shamir threshold (M) when barrier_mode is \"shamir\"."),
		),
		mcp.WithNumber("barrier_shares_total",
			mcp.Description("Shamir total share count (N) when barrier_mode is \"shamir\"."),
		),
		mcp.WithString("barrier_secret",
			mcp.Description("Barrier passphrase/credential for the initial Initialize call. Never persisted or echoed back."),
		),
	)
}

// handleDatabaseCreate returns a tool handler that delegates to
// dbSvc.Create. When barrier_enabled is set with a non-empty
// barrier_secret, the database is created via the credentialed path,
// mirroring the REST POST /v1/databases handler.
func handleDatabaseCreate(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		engine := mcp.ParseString(req, "engine", "")
		if engine == "" {
			return mcp.NewToolResultError("engine is required"), nil
		}
		backend := mcp.ParseString(req, "backend", "")
		if backend == "" {
			return mcp.NewToolResultError("backend is required"), nil
		}

		cfg := database.DatabaseConfig{
			Name:     name,
			Engine:   database.EngineType(engine),
			Backend:  database.BackendType(backend),
			DataDir:  mcp.ParseString(req, "data_dir", ""),
			Replicas: int(mcp.ParseInt64(req, "replicas", 0)),
			Protocol: database.ProtocolConfig{
				MySQL:      mcp.ParseBoolean(req, "mysql", false),
				PostgreSQL: mcp.ParseBoolean(req, "postgresql", false),
			},
		}

		// Build raft config if cluster mode is specified.
		raftMode := mcp.ParseString(req, "raft_cluster_mode", "")
		if raftMode != "" {
			raftCfg := buildRaftConfigFromRequest(req, raftMode)
			cfg.Raft = raftCfg
		}

		var creds seal.Credentials
		if mcp.ParseBoolean(req, "barrier_enabled", false) {
			cfg.Barrier = &database.DatabaseBarrierConfig{
				Enabled:        true,
				Mode:           mcp.ParseString(req, "barrier_mode", ""),
				SharesRequired: mcp.ParseInt(req, "barrier_shares_required", 0),
				SharesTotal:    mcp.ParseInt(req, "barrier_shares_total", 0),
			}
			var err error
			if creds, err = seal.CredentialsFor([]byte(mcp.ParseString(req, "barrier_secret", ""))); err != nil {
				return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
			}
			defer creds.Secret.Destroy()
		}

		info, err := service.CreateDatabaseForTenantWithCredentials(ctx, dbSvc, cfg, creds)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_create", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseList defines the qrdb_database_list tool schema.
func toolDatabaseList() mcp.Tool {
	return mcp.NewTool("qrdb_database_list",
		mcp.WithDescription("List all open databases and their runtime information."),
	)
}

// handleDatabaseList returns a tool handler that delegates to dbSvc.List.
func handleDatabaseList(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		infos, err := dbSvc.ListForTenant(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_list", Err: err}
		}

		data, jsonErr := json.Marshal(infos)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseGet defines the qrdb_database_get tool schema.
func toolDatabaseGet() mcp.Tool {
	return mcp.NewTool("qrdb_database_get",
		mcp.WithDescription("Get runtime information about a specific database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to retrieve information for."),
		),
	)
}

// handleDatabaseGet returns a tool handler that delegates to dbSvc.Get.
func handleDatabaseGet(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		info, err := dbSvc.GetForTenant(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_get", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_get", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseDrop defines the qrdb_database_drop tool schema.
func toolDatabaseDrop() mcp.Tool {
	return mcp.NewTool("qrdb_database_drop",
		mcp.WithDescription("Drop a database, closing it and deleting its data."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to drop."),
		),
	)
}

// handleDatabaseDrop returns a tool handler that delegates to dbSvc.Drop.
func handleDatabaseDrop(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		if err := dbSvc.DropForTenant(ctx, name); err != nil {
			return nil, &ToolError{Tool: "qrdb_database_drop", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolDatabaseUpdateProtocol defines the qrdb_database_update_protocol tool schema.
func toolDatabaseUpdateProtocol() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_protocol",
		mcp.WithDescription("Update the wire protocol configuration for a database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithBoolean("mysql",
			mcp.Description("Enable or disable MySQL wire protocol for this database."),
		),
		mcp.WithBoolean("postgresql",
			mcp.Description("Enable or disable PostgreSQL wire protocol for this database."),
		),
	)
}

// handleDatabaseUpdateProtocol returns a tool handler that delegates to
// dbSvc.UpdateProtocol.
func handleDatabaseUpdateProtocol(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		protocolCfg := database.ProtocolConfig{
			MySQL:      mcp.ParseBoolean(req, "mysql", false),
			PostgreSQL: mcp.ParseBoolean(req, "postgresql", false),
		}

		info, err := dbSvc.UpdateProtocolForTenant(ctx, name, protocolCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_protocol", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_protocol", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateEtcd defines the qrdb_database_update_etcd tool schema.
func toolDatabaseUpdateEtcd() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_etcd",
		mcp.WithDescription("Update the etcd wire protocol configuration for a database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithBoolean("enabled",
			mcp.Description("Enable or disable etcd wire protocol for this database."),
		),
		mcp.WithString("listen_addr",
			mcp.Description("The listen address for the etcd gRPC server (e.g., :2379)."),
		),
	)
}

// handleDatabaseUpdateEtcd returns a tool handler that delegates to
// dbSvc.UpdateEtcd.
func handleDatabaseUpdateEtcd(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		etcdCfg := &database.EtcdConfig{
			Enabled:    mcp.ParseBoolean(req, "enabled", false),
			ListenAddr: mcp.ParseString(req, "listen_addr", ""),
		}

		info, err := dbSvc.UpdateEtcdForTenant(ctx, name, etcdCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_etcd", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_etcd", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateRaft defines the qrdb_database_update_raft tool schema.
func toolDatabaseUpdateRaft() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_raft",
		mcp.WithDescription("Update the Raft consensus configuration for a database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithString("cluster_mode",
			mcp.Description("Raft cluster mode: none, local, or distributed."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("Raft shard ID for distributed mode."),
		),
		mcp.WithNumber("replica_id",
			mcp.Description("Raft replica ID for distributed mode."),
		),
		mcp.WithString("raft_address",
			mcp.Description("This node's Raft address (host:port) for distributed mode."),
		),
		mcp.WithString("discovery_method",
			mcp.Description("Peer discovery method: static, multicast, dns, or token."),
		),
		// Static discovery
		mcp.WithString("static_shared_secret",
			mcp.Description("Shared secret for static peer authentication."),
		),
		// Multicast discovery
		mcp.WithString("multicast_shared_secret",
			mcp.Description("HMAC shared secret for multicast peer authentication (required for multicast)."),
		),
		mcp.WithString("multicast_group",
			mcp.Description("Multicast group address (default: 239.255.77.77:63777)."),
		),
		mcp.WithString("multicast_interface",
			mcp.Description("Network interface for multicast discovery."),
		),
		mcp.WithNumber("multicast_min_peers",
			mcp.Description("Minimum number of peers to discover via multicast."),
		),
		// DNS discovery
		mcp.WithString("dns_domain",
			mcp.Description("DNS domain for SRV record lookup (required for dns)."),
		),
		mcp.WithString("dns_service",
			mcp.Description("SRV service name (default: qrdb)."),
		),
		mcp.WithString("dns_proto",
			mcp.Description("SRV protocol (default: udp)."),
		),
		// Token discovery
		mcp.WithString("token_endpoint",
			mcp.Description("Discovery service endpoint URL (required for token)."),
		),
		mcp.WithString("token_value",
			mcp.Description("Bootstrap token for cluster discovery (required for token)."),
		),
		mcp.WithNumber("token_cluster_size",
			mcp.Description("Expected cluster size for token discovery (required for token)."),
		),
	)
}

// handleDatabaseUpdateRaft returns a tool handler that delegates to
// dbSvc.UpdateRaft.
func handleDatabaseUpdateRaft(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		clusterMode := mcp.ParseString(req, "cluster_mode", "")
		raftCfg := buildRaftConfigFromUpdateRequest(req, clusterMode)

		info, err := dbSvc.UpdateRaftForTenant(ctx, name, raftCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_raft", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_raft", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseScale defines the qrdb_database_scale tool schema.
func toolDatabaseScale() mcp.Tool {
	return mcp.NewTool("qrdb_database_scale",
		mcp.WithDescription("Change the replication factor of an existing dedicated-raft database, "+
			"adding or removing voting replicas without recreating the database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to scale."),
		),
		mcp.WithNumber("target_replicas",
			mcp.Required(),
			mcp.Description("The desired replication factor (must be >= 1)."),
		),
	)
}

// handleDatabaseScale returns a tool handler that delegates to
// dbSvc.ScaleForTenant.
func handleDatabaseScale(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		targetReplicas := mcp.ParseInt(req, "target_replicas", 0)

		info, err := dbSvc.ScaleForTenant(ctx, name, targetReplicas)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_scale", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_scale", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateBarrier defines the qrdb_database_update_barrier tool schema.
func toolDatabaseUpdateBarrier() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_barrier",
		mcp.WithDescription("Update the encryption barrier configuration for a database. "+
			"Enabling or disabling is not supported here (it requires re-encrypting or "+
			"decrypting existing data, a migration this tool does not perform); the only "+
			"effective call is re-affirming an already-active barrier's non-secret "+
			"bookkeeping (mode/shares_required/shares_total)."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithBoolean("enabled",
			mcp.Description("Enable or disable the encryption barrier for this database."),
		),
		mcp.WithString("mode",
			mcp.Description("Target keyed strategy: \"passphrase\" or \"shamir\"."),
		),
		mcp.WithNumber("shares_required",
			mcp.Description("Shamir threshold (M) when mode is \"shamir\"."),
		),
		mcp.WithNumber("shares_total",
			mcp.Description("Shamir total share count (N) when mode is \"shamir\"."),
		),
	)
}

// handleDatabaseUpdateBarrier returns a tool handler that delegates to
// dbSvc.UpdateBarrier.
func handleDatabaseUpdateBarrier(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		barrierCfg := &database.DatabaseBarrierConfig{
			Enabled:        mcp.ParseBoolean(req, "enabled", false),
			Mode:           mcp.ParseString(req, "mode", ""),
			SharesRequired: mcp.ParseInt(req, "shares_required", 0),
			SharesTotal:    mcp.ParseInt(req, "shares_total", 0),
		}

		info, err := dbSvc.UpdateBarrierForTenant(ctx, name, barrierCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_barrier", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_barrier", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateConnection defines the qrdb_database_update_connection
// tool schema.
func toolDatabaseUpdateConnection() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_connection",
		mcp.WithDescription("Update the remote connection configuration for a database."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithString("transport",
			mcp.Description("Connection transport (tcp, quic)."),
		),
		mcp.WithString("remote_address",
			mcp.Description("The remote server address (host:port) for proxy connections."),
		),
		mcp.WithString("ca_file",
			mcp.Description("TLS CA certificate file path."),
		),
		mcp.WithString("cert_file",
			mcp.Description("TLS client certificate file path."),
		),
		mcp.WithString("key_file",
			mcp.Description("TLS client key file path."),
		),
	)
}

// handleDatabaseUpdateConnection returns a tool handler that delegates to
// dbSvc.UpdateConnection.
func handleDatabaseUpdateConnection(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		connCfg := &database.DatabaseConnectionConfig{
			Transport: mcp.ParseString(req, "transport", ""),
			Address:   mcp.ParseString(req, "remote_address", ""),
			CAFile:    mcp.ParseString(req, "ca_file", ""),
			CertFile:  mcp.ParseString(req, "cert_file", ""),
			KeyFile:   mcp.ParseString(req, "key_file", ""),
		}

		info, err := dbSvc.UpdateConnectionForTenant(ctx, name, connCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_connection", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_connection", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Raft config helpers
// ---------------------------------------------------------------------------

// buildRaftConfigFromRequest builds a DatabaseRaftConfig from MCP request
// parameters used by the database create tool. The raft-specific parameters
// use the "raft_" prefix (e.g., "raft_shard_id").
func buildRaftConfigFromRequest(req mcp.CallToolRequest, raftMode string) *database.DatabaseRaftConfig {
	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftClusterMode(raftMode),
		ShardID:     mcp.ParseUInt64(req, "raft_shard_id", 0),
		ReplicaID:   mcp.ParseUInt64(req, "raft_replica_id", 0),
		RaftAddress: mcp.ParseString(req, "raft_address", ""),
	}

	discoveryMethod := mcp.ParseString(req, "discovery_method", "")
	if discoveryMethod != "" {
		raftCfg.Discovery = database.DiscoveryMethod(discoveryMethod)
	}

	parseDiscoveryConfig(req, raftCfg, discoveryMethod)

	return raftCfg
}

// buildRaftConfigFromUpdateRequest builds a DatabaseRaftConfig from MCP
// request parameters used by the raft update tool. The raft-specific
// parameters use direct names (e.g., "shard_id") without the "raft_" prefix.
func buildRaftConfigFromUpdateRequest(req mcp.CallToolRequest, clusterMode string) *database.DatabaseRaftConfig {
	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftClusterMode(clusterMode),
		ShardID:     mcp.ParseUInt64(req, "shard_id", 0),
		ReplicaID:   mcp.ParseUInt64(req, "replica_id", 0),
		RaftAddress: mcp.ParseString(req, "raft_address", ""),
	}

	discoveryMethod := mcp.ParseString(req, "discovery_method", "")
	if discoveryMethod != "" {
		raftCfg.Discovery = database.DiscoveryMethod(discoveryMethod)
	}

	parseDiscoveryConfig(req, raftCfg, discoveryMethod)

	return raftCfg
}

// parseDiscoveryConfig populates discovery-specific sub-configs on a
// DatabaseRaftConfig based on the discovery method. The discovery parameter
// field names are shared between create and update tools.
func parseDiscoveryConfig(req mcp.CallToolRequest, raftCfg *database.DatabaseRaftConfig, discoveryMethod string) {
	switch database.DiscoveryMethod(discoveryMethod) {
	case database.DiscoveryStatic:
		secret := mcp.ParseString(req, "static_shared_secret", "")
		if secret != "" {
			raftCfg.Static = &database.StaticDiscoveryConfig{
				SharedSecret: secret,
			}
		}
	case database.DiscoveryMulticast:
		raftCfg.Multicast = &database.MulticastDiscoveryConfig{
			SharedSecret:   mcp.ParseString(req, "multicast_shared_secret", ""),
			MulticastGroup: mcp.ParseString(req, "multicast_group", ""),
			Interface:      mcp.ParseString(req, "multicast_interface", ""),
			MinPeers:       mcp.ParseInt(req, "multicast_min_peers", 0),
		}
	case database.DiscoveryDNS:
		raftCfg.DNS = &database.DNSDiscoveryConfig{
			Domain:  mcp.ParseString(req, "dns_domain", ""),
			Service: mcp.ParseString(req, "dns_service", ""),
			Proto:   mcp.ParseString(req, "dns_proto", ""),
		}
	case database.DiscoveryToken:
		raftCfg.Token = &database.TokenDiscoveryConfig{
			Endpoint:    mcp.ParseString(req, "token_endpoint", ""),
			Token:       mcp.ParseString(req, "token_value", ""),
			ClusterSize: mcp.ParseInt(req, "token_cluster_size", 0),
		}
	}
}
