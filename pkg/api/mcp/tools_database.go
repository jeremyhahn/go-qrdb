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

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// databaseTools returns the MCP tool registrations for database management
// operations.
func databaseTools(dbSvc service.DatabaseServicer) []serverTool {
	return append([]serverTool{
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
			Tool:    toolDatabaseUpdateSQL(),
			Perm:    permission.DatabasesUpdate,
			Handler: handleDatabaseUpdateSQL(dbSvc),
		},
		{
			// Setting a compaction policy is compacting on a schedule.
			Tool:    toolDatabaseUpdateCompaction(),
			Perm:    permission.MVCCCompact,
			Handler: handleDatabaseUpdateCompaction(dbSvc),
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
	}, databaseHolderTools(dbSvc)...)
}

// toolDatabaseCreate defines the qrdb_database_create tool schema.
func toolDatabaseCreate() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Create a new database with the specified name and backend."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name. Must contain only alphanumeric characters, hyphens, and underscores."),
		),
		mcp.WithString("backend",
			mcp.Required(),
			mcp.Description("The storage backend type: memory, file, pebble, or raft."),
		),
		mcp.WithString("data_dir",
			mcp.Description("An explicit data directory for the database. System-tenant only."),
		),
		mcp.WithNumber("replicas",
			mcp.Description("Replication factor for a dedicated-raft database. Meaningful only for raft-backed databases; unset defaults to 1."),
		),
		mcp.WithBoolean("dedicated_wal",
			mcp.Description("Give a dedicated-raft database its own WAL under the tenant's key (raft backend only). Required for every database of a strict-isolation tenant."),
		),
		mcp.WithBoolean("mysql",
			mcp.Description("Enable MySQL wire protocol for this database."),
		),
		mcp.WithBoolean("postgresql",
			mcp.Description("Enable PostgreSQL wire protocol for this database."),
		),
		mcp.WithBoolean("etcd",
			mcp.Description("Enable etcd wire protocol compatibility for this database."),
		),
		mcp.WithString("etcd_listen_addr",
			mcp.Description("The listen address for the etcd gRPC server (e.g., :2379). Only meaningful when etcd is true."),
		),
		mcp.WithArray("etcd_advertise_urls",
			mcp.Description("Etcd advertise client URLs. Only meaningful when etcd is true."),
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
		withSecretArg("barrier_secret", "Barrier passphrase/credential for the initial Initialize call. Never persisted or echoed back."),
		mcp.WithString("sql_concurrency",
			mcp.Description("The database's SQL concurrency mode: \"optimistic\" (the default) or \"pessimistic\"."),
		),
	}
	return mcp.NewTool("qrdb_database_create", append(opts, raftToolOptions(createRaftArgs)...)...)
}

// handleDatabaseCreate returns a tool handler that delegates to
// dbSvc.Create. When barrier_enabled is set with a non-empty
// barrier_secret, the database is created via the credentialed path,
// mirroring the REST POST /v1/databases handler.
func handleDatabaseCreate(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := wire.RejectUnknownArgs("qrdb_database_create", req.GetArguments(), createDatabaseKnownArgs); err != nil {
			return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
		}

		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		backend := mcp.ParseString(req, "backend", "")
		if backend == "" {
			return mcp.NewToolResultError("backend is required"), nil
		}

		cfg := database.DatabaseConfig{
			Name:         name,
			Backend:      database.BackendType(backend),
			DataDir:      mcp.ParseString(req, "data_dir", ""),
			Replicas:     int(mcp.ParseInt64(req, "replicas", 0)),
			DedicatedWAL: mcp.ParseBoolean(req, "dedicated_wal", false),
			Protocol: database.ProtocolConfig{
				MySQL:      mcp.ParseBoolean(req, "mysql", false),
				PostgreSQL: mcp.ParseBoolean(req, "postgresql", false),
			},
		}

		if concurrency := mcp.ParseString(req, "sql_concurrency", ""); concurrency != "" {
			cfg.SQL = &database.SQLConfig{Concurrency: database.Concurrency(concurrency)}
		}

		if mcp.ParseBoolean(req, "etcd", false) {
			advertiseURLs, err := stringListArg(req, "etcd_advertise_urls")
			if err != nil {
				return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
			}
			cfg.Protocol.Etcd = &database.EtcdConfig{
				Enabled:             true,
				ListenAddr:          mcp.ParseString(req, "etcd_listen_addr", ""),
				AdvertiseClientURLs: advertiseURLs,
			}
		}

		// Build raft config if cluster mode is specified.
		if mcp.ParseString(req, createRaftArgs.clusterMode, "") != "" {
			raftCfg, err := buildRaftConfig(req, createRaftArgs)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
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
			secret, err := secretArg(req, "barrier_secret")
			if err != nil {
				return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
			}
			defer secure.Wipe(secret)
			if creds, err = seal.CredentialsFor(ctx, secret); err != nil {
				return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
			}
			defer seal.DestroyCredentials(creds)
		}

		info, err := service.CreateDatabaseForTenantWithCredentials(ctx, dbSvc, cfg, creds)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_create", Err: err}
		}

		data, jsonErr := json.Marshal(dbwire.Info(info))
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

		data, jsonErr := json.Marshal(dbwire.Infos(infos))
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

		data, jsonErr := json.Marshal(dbwire.Info(info))
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

		data, jsonErr := json.Marshal(dbwire.Info(info))
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

		data, jsonErr := json.Marshal(dbwire.Info(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_etcd", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateRaft defines the qrdb_database_update_raft tool schema.
func toolDatabaseUpdateRaft() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Update the Raft consensus configuration for a database. The update replaces the whole Raft configuration: a discovery block is sent when discovery_method names it or any of its arguments is given, and a block not sent is removed with its secret. A block sent without its secret keeps the stored one while the block's destination is unchanged."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
	}
	return mcp.NewTool("qrdb_database_update_raft", append(opts, raftToolOptions(updateRaftArgs)...)...)
}

// handleDatabaseUpdateRaft returns a tool handler that delegates to
// dbSvc.UpdateRaft.
func handleDatabaseUpdateRaft(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		raftCfg, err := buildRaftConfig(req, updateRaftArgs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		info, err := dbSvc.UpdateRaftForTenant(ctx, name, raftCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_raft", Err: err}
		}

		data, jsonErr := json.Marshal(dbwire.Info(info))
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

		data, jsonErr := json.Marshal(dbwire.Info(info))
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

		data, jsonErr := json.Marshal(dbwire.Info(info))
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

		data, jsonErr := json.Marshal(dbwire.Info(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_connection", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateSQL defines the qrdb_database_update_sql tool schema.
func toolDatabaseUpdateSQL() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_sql",
		mcp.WithDescription("Update a database's SQL settings: its concurrency mode. Omitting concurrency restores the defaults (optimistic)."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithString("concurrency",
			mcp.Description("The SQL concurrency mode: \"optimistic\" or \"pessimistic\". Omit it to restore the defaults."),
		),
	)
}

// handleDatabaseUpdateSQL returns a tool handler that delegates to
// dbSvc.UpdateSQLForTenant. An omitted concurrency clears the database's
// SQL settings, restoring the defaults -- the same as an absent sql on
// REST, gRPC, QUIC and the Unix socket.
func handleDatabaseUpdateSQL(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		var sqlCfg *database.SQLConfig
		if concurrency := mcp.ParseString(req, "concurrency", ""); concurrency != "" {
			sqlCfg = &database.SQLConfig{Concurrency: database.Concurrency(concurrency)}
		}

		info, err := dbSvc.UpdateSQLForTenant(ctx, name, sqlCfg)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_sql", Err: err}
		}

		data, jsonErr := json.Marshal(dbwire.Info(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_sql", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolDatabaseUpdateCompaction defines the qrdb_database_update_compaction
// tool schema.
func toolDatabaseUpdateCompaction() mcp.Tool {
	return mcp.NewTool("qrdb_database_update_compaction",
		mcp.WithDescription("Set a database's scheduled-compaction policy: off, revision (keep the last N revisions) or period (keep history newer than a duration). Omitting mode turns it off. Only the owning tenant may set it."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to update."),
		),
		mcp.WithString("mode",
			mcp.Description("\"off\", \"revision\" or \"period\". Omit it to turn scheduled compaction off."),
		),
		mcp.WithNumber("retainRevisions",
			mcp.Description("Revision mode: how many of the store's most recent revisions to keep (at least 1)."),
		),
		mcp.WithString("retainPeriod",
			mcp.Description("Period mode: how much history to keep by age, a Go duration of at least 1s (e.g. \"24h\")."),
		),
	)
}

// handleDatabaseUpdateCompaction returns a tool handler that delegates to
// dbSvc.UpdateCompactionForTenant. An omitted mode turns scheduled compaction
// off -- the same as an absent compaction on REST, gRPC, QUIC and the Unix
// socket.
func handleDatabaseUpdateCompaction(dbSvc service.DatabaseServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		var policy *compaction.Policy
		if mode := mcp.ParseString(req, "mode", ""); mode != "" {
			policy = &compaction.Policy{
				Mode:            compaction.Mode(mode),
				RetainRevisions: mcp.ParseInt64(req, "retainRevisions", 0),
				RetainPeriod:    mcp.ParseString(req, "retainPeriod", ""),
			}
		}

		info, err := dbSvc.UpdateCompactionForTenant(ctx, name, policy)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_compaction", Err: err}
		}

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_database_update_compaction", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Raft config helpers
// ---------------------------------------------------------------------------

// raftArgs names a Raft tool's top-level arguments. The create tool prefixes
// them with raft_ beside its other arguments; the update tool does not. The
// discovery arguments are named alike in both.
// raftArgNames returns every MCP argument name raftToolOptions(names)
// declares, for building a tool's set of known arguments (see
// createDatabaseKnownArgs). It must stay in sync with raftToolOptions.
func raftArgNames(names raftArgs) []string {
	return []string{
		names.clusterMode, names.replicaID, names.address, names.peers, names.localNodeCount,
		"discovery_method",
		"static_peers", "static_shared_secret",
		"multicast_shared_secret", "multicast_group", "multicast_interface",
		"multicast_min_peers", "multicast_announce_interval", "multicast_discover_timeout",
		"dns_domain", "dns_service", "dns_proto", "dns_lookup_timeout",
		"token_endpoint", "token_value", "token_cluster_size", "token_timeout",
	}
}

type raftArgs struct {
	clusterMode    string
	replicaID      string
	address        string
	peers          string
	localNodeCount string
}

var (
	createRaftArgs = raftArgs{
		clusterMode:    "raft_cluster_mode",
		replicaID:      "raft_replica_id",
		address:        "raft_address",
		peers:          "raft_peers",
		localNodeCount: "raft_local_node_count",
	}
	updateRaftArgs = raftArgs{
		clusterMode:    "cluster_mode",
		replicaID:      "replica_id",
		address:        "raft_address",
		peers:          "peers",
		localNodeCount: "local_node_count",
	}
)

// createDatabaseKnownArgs is the set of argument names qrdb_database_create
// recognizes: every name declared in its schema (toolDatabaseCreate). An
// argument outside this set -- a stray "engine" (OD-13) or any other stale or
// misspelled key -- is refused rather than silently ignored; see
// handleDatabaseCreate and wire.RejectUnknownArgs.
var createDatabaseKnownArgs = func() map[string]struct{} {
	names := append([]string{
		"name", "backend", "data_dir", "mode", "replicas", "dedicated_wal", "mysql", "postgresql",
		"etcd", "etcd_listen_addr", "etcd_advertise_urls",
		"barrier_enabled", "barrier_mode", "barrier_shares_required", "barrier_shares_total",
		"barrier_secret", "barrier_secret" + secretB64Suffix,
		"sql_concurrency",
	}, raftArgNames(createRaftArgs)...)
	known := make(map[string]struct{}, len(names))
	for _, n := range names {
		known[n] = struct{}{}
	}
	return known
}()

// raftToolOptions declares every Raft argument a tool takes: the top-level
// fields under names, then the discovery blocks. Together they reach every
// field of database.DatabaseRaftConfig a caller sets on REST, gRPC and QUIC;
// the shard ID is issued, never sent.
func raftToolOptions(names raftArgs) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString(names.clusterMode,
			mcp.Description("Raft cluster mode: none, local, or distributed."),
		),
		mcp.WithNumber(names.replicaID,
			mcp.Description("Raft replica ID for distributed mode."),
		),
		mcp.WithString(names.address,
			mcp.Description("This node's Raft address (host:port) for distributed mode."),
		),
		mcp.WithArray(names.peers,
			mcp.Description("Raft peer addresses (host:port). Static discovery reaches these as well as static_peers."),
		),
		mcp.WithNumber(names.localNodeCount,
			mcp.Description("Number of replicas on this node in local cluster mode."),
		),
		mcp.WithString("discovery_method",
			mcp.Description("Peer discovery method: static, multicast, dns, or token."),
		),
		// Static discovery
		mcp.WithArray("static_peers",
			mcp.Description("Static peer addresses (host:port) for static discovery."),
		),
		mcp.WithString("static_shared_secret",
			mcp.Description("Shared secret for static peer authentication. Omitted on an update, the stored secret is kept while the static peers are unchanged."),
		),
		// Multicast discovery
		mcp.WithString("multicast_shared_secret",
			mcp.Description("HMAC shared secret for multicast peer authentication (required for multicast). Omitted on an update, the stored secret is kept while the group and interface are unchanged."),
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
		mcp.WithString("multicast_announce_interval",
			mcp.Description("Interval between multicast announcements, as a Go duration (e.g. 2s)."),
		),
		mcp.WithString("multicast_discover_timeout",
			mcp.Description("How long multicast discovery waits for peers, as a Go duration (e.g. 30s)."),
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
		mcp.WithString("dns_lookup_timeout",
			mcp.Description("Timeout for each SRV lookup, as a Go duration (e.g. 5s)."),
		),
		// Token discovery
		mcp.WithString("token_endpoint",
			mcp.Description("Discovery service endpoint URL (required for token)."),
		),
		mcp.WithString("token_value",
			mcp.Description("Bootstrap token for cluster discovery (required for token). Omitted on an update, the stored token is kept while the endpoint is unchanged."),
		),
		mcp.WithNumber("token_cluster_size",
			mcp.Description("Expected cluster size for token discovery (required for token)."),
		),
		mcp.WithString("token_timeout",
			mcp.Description("How long token discovery waits for the cluster, as a Go duration (e.g. 60s)."),
		),
	}
}

// buildRaftConfig builds a DatabaseRaftConfig from a Raft tool's arguments,
// named by names. There is no shard ID argument: the cluster issues a
// database's shard ID at create, and it never changes.
func buildRaftConfig(req mcp.CallToolRequest, names raftArgs) (*database.DatabaseRaftConfig, error) {
	peers, err := stringListArg(req, names.peers)
	if err != nil {
		return nil, err
	}
	raftCfg := &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftClusterMode(mcp.ParseString(req, names.clusterMode, "")),
		ReplicaID:      mcp.ParseUInt64(req, names.replicaID, 0),
		RaftAddress:    mcp.ParseString(req, names.address, ""),
		Peers:          peers,
		LocalNodeCount: mcp.ParseInt(req, names.localNodeCount, 0),
		Discovery:      database.DiscoveryMethod(mcp.ParseString(req, "discovery_method", "")),
	}
	if err := parseDiscoveryConfig(req, raftCfg); err != nil {
		return nil, err
	}
	return raftCfg, nil
}

// discoveryBlock is one discovery method's block: the arguments that carry it
// and how it is built from them.
type discoveryBlock struct {
	args  []string
	build func(mcp.CallToolRequest, *database.DatabaseRaftConfig) error
}

// discoveryBlocks holds every discovery block a Raft tool can send.
var discoveryBlocks = map[database.DiscoveryMethod]discoveryBlock{
	database.DiscoveryStatic: {
		args: []string{"static_peers", "static_shared_secret"},
		build: func(req mcp.CallToolRequest, raftCfg *database.DatabaseRaftConfig) error {
			peers, err := stringListArg(req, "static_peers")
			if err != nil {
				return err
			}
			secret, _, err := stringArg(req, "static_shared_secret")
			if err != nil {
				return err
			}
			raftCfg.Static = &database.StaticDiscoveryConfig{
				Peers:        peers,
				SharedSecret: secret,
			}
			return nil
		},
	},
	database.DiscoveryMulticast: {
		args: []string{"multicast_shared_secret", "multicast_group", "multicast_interface",
			"multicast_min_peers", "multicast_announce_interval", "multicast_discover_timeout"},
		build: func(req mcp.CallToolRequest, raftCfg *database.DatabaseRaftConfig) error {
			secret, _, err := stringArg(req, "multicast_shared_secret")
			if err != nil {
				return err
			}
			raftCfg.Multicast = &database.MulticastDiscoveryConfig{
				SharedSecret:     secret,
				MulticastGroup:   mcp.ParseString(req, "multicast_group", ""),
				Interface:        mcp.ParseString(req, "multicast_interface", ""),
				MinPeers:         mcp.ParseInt(req, "multicast_min_peers", 0),
				AnnounceInterval: mcp.ParseString(req, "multicast_announce_interval", ""),
				DiscoverTimeout:  mcp.ParseString(req, "multicast_discover_timeout", ""),
			}
			return nil
		},
	},
	database.DiscoveryDNS: {
		args: []string{"dns_domain", "dns_service", "dns_proto", "dns_lookup_timeout"},
		build: func(req mcp.CallToolRequest, raftCfg *database.DatabaseRaftConfig) error {
			raftCfg.DNS = &database.DNSDiscoveryConfig{
				Domain:        mcp.ParseString(req, "dns_domain", ""),
				Service:       mcp.ParseString(req, "dns_service", ""),
				Proto:         mcp.ParseString(req, "dns_proto", ""),
				LookupTimeout: mcp.ParseString(req, "dns_lookup_timeout", ""),
			}
			return nil
		},
	},
	database.DiscoveryToken: {
		args: []string{"token_endpoint", "token_value", "token_cluster_size", "token_timeout"},
		build: func(req mcp.CallToolRequest, raftCfg *database.DatabaseRaftConfig) error {
			token, _, err := stringArg(req, "token_value")
			if err != nil {
				return err
			}
			raftCfg.Token = &database.TokenDiscoveryConfig{
				Endpoint:    mcp.ParseString(req, "token_endpoint", ""),
				Token:       token,
				ClusterSize: mcp.ParseInt(req, "token_cluster_size", 0),
				Timeout:     mcp.ParseString(req, "token_timeout", ""),
			}
			return nil
		},
	},
}

// parseDiscoveryConfig sets each discovery block the request sends. Other
// transports send a block by including it; MCP's arguments are flat, so a
// block is sent when discovery_method names it or any of its arguments is
// given, and only then. A block not sent is absent from the update, and the
// update removes it with its secret, as on every other transport. A block
// sent without its secret keeps the stored one while the block's destination
// is unchanged (database.Manager.UpdateRaft).
func parseDiscoveryConfig(req mcp.CallToolRequest, raftCfg *database.DatabaseRaftConfig) error {
	for method, block := range discoveryBlocks {
		if raftCfg.Discovery != method && !anyArgument(req, block.args) {
			continue
		}
		if err := block.build(req, raftCfg); err != nil {
			return err
		}
	}
	return nil
}

// anyArgument reports whether the request gives any of the named arguments.
// A null argument is not given, as mcp.ParseArgument reads it.
func anyArgument(req mcp.CallToolRequest, names []string) bool {
	args := req.GetArguments()
	for _, name := range names {
		if v, ok := args[name]; ok && v != nil {
			return true
		}
	}
	return false
}

// stringListArg reads an optional array-of-strings argument. Absent is nil; an
// argument that is not an array of strings is refused, never half-read.
func stringListArg(req mcp.CallToolRequest, name string) ([]string, error) {
	raw := mcp.ParseArgument(req, name, nil)
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, &ToolArgumentError{Argument: name, Reason: "must be an array of strings"}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, &ToolArgumentError{Argument: name, Reason: "must be an array of strings"}
		}
		out = append(out, s)
	}
	return out, nil
}
