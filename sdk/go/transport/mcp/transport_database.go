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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DatabaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DatabaseClient implementation
// ---------------------------------------------------------------------------

// CreateDatabase creates a new database by calling the qrdb_database_create
// MCP tool with the provided configuration.
func (t *Transport) CreateDatabase(ctx context.Context, req transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name":    req.Name,
		"engine":  req.Engine,
		"backend": req.Backend,
	}

	if req.DataDir != "" {
		args["data_dir"] = req.DataDir
	}
	if req.Replicas != 0 {
		args["replicas"] = req.Replicas
	}
	if req.Protocol.MySQL {
		args["mysql"] = true
	}
	if req.Protocol.PostgreSQL {
		args["postgresql"] = true
	}
	if req.Mode != "" {
		args["mode"] = req.Mode
	}

	if req.Raft != nil {
		applyRaftArgs(args, req.Raft, "raft_")
	}

	if req.Barrier != nil {
		args["barrier_enabled"] = req.Barrier.Enabled
		if req.Barrier.Mode != "" {
			args["barrier_mode"] = req.Barrier.Mode
		}
		if req.Barrier.SharesRequired != 0 {
			args["barrier_shares_required"] = req.Barrier.SharesRequired
		}
		if req.Barrier.SharesTotal != 0 {
			args["barrier_shares_total"] = req.Barrier.SharesTotal
		}
		if req.Barrier.Secret != "" {
			args["barrier_secret"] = req.Barrier.Secret
		}
	}

	if req.SQL != nil && req.SQL.Concurrency != "" {
		args["sql_concurrency"] = req.SQL.Concurrency
	}

	text, err := t.callTool(ctx, "qrdb_database_create", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateDatabase", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateDatabase", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// GetDatabase returns information about a specific database by calling the
// qrdb_database_get MCP tool.
func (t *Transport) GetDatabase(ctx context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_database_get", map[string]any{
		"name": name,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetDatabase", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetDatabase", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// ListDatabases returns information about all databases by calling the
// qrdb_database_list MCP tool.
func (t *Transport) ListDatabases(ctx context.Context) ([]transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_database_list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDatabases", Err: err}
	}

	var infos []transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &infos); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListDatabases", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return infos, nil
}

// DropDatabase removes a database by name by calling the qrdb_database_drop
// MCP tool.
func (t *Transport) DropDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_database_drop", map[string]any{
		"name": name,
	})
	if err != nil {
		return &transport.RequestError{Method: "DropDatabase", Err: err}
	}

	return nil
}

// UpdateDatabaseProtocol updates the wire protocol configuration for a
// database by calling the qrdb_database_update_protocol MCP tool.
func (t *Transport) UpdateDatabaseProtocol(ctx context.Context, req transport.UpdateDatabaseProtocolRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name":       req.Name,
		"mysql":      req.MySQL,
		"postgresql": req.PostgreSQL,
	}

	text, err := t.callTool(ctx, "qrdb_database_update_protocol", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseProtocol", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseProtocol", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// UpdateDatabaseEtcd updates the etcd wire protocol configuration for a
// database by calling the qrdb_database_update_etcd MCP tool.
func (t *Transport) UpdateDatabaseEtcd(ctx context.Context, req transport.UpdateDatabaseEtcdRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name":        req.Name,
		"enabled":     req.Enabled,
		"listen_addr": req.ListenAddr,
	}

	text, err := t.callTool(ctx, "qrdb_database_update_etcd", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseEtcd", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseEtcd", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// UpdateDatabaseRaft updates the Raft consensus configuration for a database
// by calling the qrdb_database_update_raft MCP tool.
func (t *Transport) UpdateDatabaseRaft(ctx context.Context, req transport.UpdateDatabaseRaftRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name": req.Name,
	}

	if req.Raft != nil {
		applyRaftArgs(args, req.Raft, "")
	}

	text, err := t.callTool(ctx, "qrdb_database_update_raft", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseRaft", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseRaft", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// ScaleDatabase changes the replication factor of a dedicated-raft database
// by calling the qrdb_database_scale MCP tool.
func (t *Transport) ScaleDatabase(ctx context.Context, req transport.ScaleDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name":            req.Name,
		"target_replicas": req.TargetReplicas,
	}

	text, err := t.callTool(ctx, "qrdb_database_scale", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "ScaleDatabase", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ScaleDatabase", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// UpdateDatabaseBarrier updates the encryption barrier configuration for a
// database by calling the qrdb_database_update_barrier MCP tool.
func (t *Transport) UpdateDatabaseBarrier(ctx context.Context, req transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name":    req.Name,
		"enabled": req.Barrier.Enabled,
	}
	if req.Barrier.Mode != "" {
		args["mode"] = req.Barrier.Mode
	}
	if req.Barrier.SharesRequired != 0 {
		args["shares_required"] = req.Barrier.SharesRequired
	}
	if req.Barrier.SharesTotal != 0 {
		args["shares_total"] = req.Barrier.SharesTotal
	}

	text, err := t.callTool(ctx, "qrdb_database_update_barrier", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseBarrier", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseBarrier", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// UpdateDatabaseSQL updates a database's SQL settings by calling the
// qrdb_database_update_sql MCP tool. A nil SQL, or one without a mode,
// omits the concurrency argument, which clears the settings and restores
// the defaults -- the same as an absent sql on every other transport.
func (t *Transport) UpdateDatabaseSQL(ctx context.Context, req transport.UpdateDatabaseSQLRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{"name": req.Name}
	if req.SQL != nil && req.SQL.Concurrency != "" {
		args["concurrency"] = req.SQL.Concurrency
	}

	text, err := t.callTool(ctx, "qrdb_database_update_sql", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseSQL", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseSQL", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// UpdateDatabaseConnection updates the remote connection configuration for a
// database by calling the qrdb_database_update_connection MCP tool.
func (t *Transport) UpdateDatabaseConnection(ctx context.Context, req transport.UpdateDatabaseConnectionRequest) (*transport.DatabaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"name": req.Name,
	}

	if req.Connection != nil {
		if req.Connection.Transport != "" {
			args["transport"] = req.Connection.Transport
		}
		if req.Connection.Address != "" {
			args["remote_address"] = req.Connection.Address
		}
		if req.Connection.CAFile != "" {
			args["ca_file"] = req.Connection.CAFile
		}
		if req.Connection.CertFile != "" {
			args["cert_file"] = req.Connection.CertFile
		}
		if req.Connection.KeyFile != "" {
			args["key_file"] = req.Connection.KeyFile
		}
	}

	text, err := t.callTool(ctx, "qrdb_database_update_connection", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseConnection", Err: err}
	}

	var info transport.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		return nil, &transport.RequestError{Method: "UpdateDatabaseConnection", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &info, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// applyRaftArgs adds Raft configuration fields to an MCP tool argument map.
// The prefix is used to namespace the shard/replica ID fields: the create
// tool uses "raft_" (e.g., "raft_shard_id") while the update tool uses no
// prefix (e.g., "shard_id").
func applyRaftArgs(args map[string]any, raft *transport.DatabaseRaftConfig, prefix string) {
	if raft.ClusterMode != "" {
		if prefix == "raft_" {
			args["raft_cluster_mode"] = raft.ClusterMode
		} else {
			args["cluster_mode"] = raft.ClusterMode
		}
	}
	if raft.ReplicaID != 0 {
		args[prefix+"replica_id"] = raft.ReplicaID
	}
	if raft.RaftAddress != "" {
		args["raft_address"] = raft.RaftAddress
	}
}
