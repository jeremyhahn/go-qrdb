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

package quic

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleDatabaseCreate unmarshals a JSON database creation request, creates
// the database via the service layer, and returns the serialized database
// info. When the request carries a barrier config with a non-empty secret,
// the database is created via the credentialed path (routing to
// CreateForTenantWithCredentials when the resolved DatabaseServicer
// supports it), mirroring the REST POST /v1/databases handler.
func (s *Server) handleDatabaseCreate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	// Strict decoding: an unknown member (a stale "engine" key, OD-13) is
	// refused rather than silently ignored, the generic rule REST's
	// decodeGuardedRequest applies to every state-changing endpoint.
	var req databaseCreateRequest
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	cfg := database.DatabaseConfig{
		TenantID:     req.TenantID,
		Name:         req.Name,
		Backend:      database.BackendType(req.Backend),
		DataDir:      req.DataDir,
		Mode:         database.DatabaseMode(req.Mode),
		Replicas:     req.Replicas,
		DedicatedWAL: req.DedicatedWAL,
		Protocol:     req.Protocol.toDomain(),
		Raft:         req.Raft.toDomain(),
	}
	if req.Connection != nil {
		cfg.Connection = req.Connection
	}
	cfg.SQL = req.SQL

	var creds seal.Credentials
	if req.Barrier != nil {
		cfg.Barrier = &database.DatabaseBarrierConfig{
			Enabled:        req.Barrier.Enabled,
			Mode:           req.Barrier.Mode,
			SharesRequired: req.Barrier.SharesRequired,
			SharesTotal:    req.Barrier.SharesTotal,
		}
		var err error
		if creds, err = seal.CredentialsFor(ctx, []byte(req.Barrier.Secret)); err != nil {
			return nil, err
		}
		defer seal.DestroyCredentials(creds)
	}

	info, err := service.CreateDatabaseForTenantWithCredentials(ctx, s.databaseSvc, cfg, creds)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseGet unmarshals a JSON request containing a database name,
// retrieves its info from the service layer, and returns the serialized result.
func (s *Server) handleDatabaseGet(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseNameRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	info, err := s.databaseSvc.GetForTenant(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseList returns a JSON-serialized list of all database info objects.
func (s *Server) handleDatabaseList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	infos, err := s.databaseSvc.ListForTenant(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Infos(infos))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseDrop unmarshals a JSON request containing a database name and
// drops the database via the service layer.
func (s *Server) handleDatabaseDrop(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseNameRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	if err := s.databaseSvc.DropForTenant(ctx, req.Name); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleDatabaseUpdateProtocol unmarshals a JSON protocol update request,
// applies it via the service layer, and returns the updated database info.
func (s *Server) handleDatabaseUpdateProtocol(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateProtocolRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	info, err := s.databaseSvc.UpdateProtocolForTenant(ctx, req.Name, database.ProtocolConfig{
		MySQL:      req.MySQL,
		PostgreSQL: req.PostgreSQL,
	})
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseUpdateEtcd unmarshals a JSON etcd configuration update
// request, applies it via the service layer, and returns the updated info.
func (s *Server) handleDatabaseUpdateEtcd(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateEtcdRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	info, err := s.databaseSvc.UpdateEtcdForTenant(ctx, req.Name, &database.EtcdConfig{
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientURLs: req.AdvertiseClientURLs,
	})
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseUpdateRaft unmarshals a JSON Raft configuration update
// request, applies it via the service layer, and returns the updated info.
func (s *Server) handleDatabaseUpdateRaft(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateRaftRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}
	if req.Raft == nil {
		return nil, &InvalidArgumentError{Reason: "raft configuration is required"}
	}

	info, err := s.databaseSvc.UpdateRaftForTenant(ctx, req.Name, req.Raft.toDomain())
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseScale unmarshals a JSON replica-count scale request, applies
// it via the service layer, and returns the updated database info.
func (s *Server) handleDatabaseScale(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseScaleRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	info, err := s.databaseSvc.ScaleForTenant(ctx, req.Name, req.TargetReplicas)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseUpdateBarrier unmarshals a JSON barrier configuration update
// request, applies it via the service layer, and returns the updated info.
func (s *Server) handleDatabaseUpdateBarrier(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateBarrierRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}
	if req.Barrier == nil {
		return nil, &InvalidArgumentError{Reason: "barrier configuration is required"}
	}

	info, err := s.databaseSvc.UpdateBarrierForTenant(ctx, req.Name, req.Barrier)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseUpdateConnection unmarshals a JSON connection configuration
// update request, applies it via the service layer, and returns the updated info.
func (s *Server) handleDatabaseUpdateConnection(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateConnectionRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}
	if req.Connection == nil {
		return nil, &InvalidArgumentError{Reason: "connection configuration is required"}
	}

	info, err := s.databaseSvc.UpdateConnectionForTenant(ctx, req.Name, req.Connection)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseUpdateSQL applies a database's SQL settings -- its
// sql.concurrency -- via the service layer and returns the updated info. An
// absent sql restores the defaults.
func (s *Server) handleDatabaseUpdateSQL(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateSQLRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	info, err := s.databaseSvc.UpdateSQLForTenant(ctx, req.Name, req.SQL)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleDatabaseUpdateCompaction sets a database's scheduled-compaction
// policy via the service layer and returns the updated info. An absent
// compaction turns it off. The method requires mvcc:compact, and the service
// confines it to the caller's own databases.
func (s *Server) handleDatabaseUpdateCompaction(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseUpdateCompactionRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if req.Name == "" {
		return nil, &InvalidArgumentError{Reason: "database name is required"}
	}

	info, err := s.databaseSvc.UpdateCompactionForTenant(ctx, req.Name, req.Compaction)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(dbwire.Info(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Wire-format request types for JSON-based database handlers
// ---------------------------------------------------------------------------

// databaseNameRequest is the JSON wire format for operations that only
// require a database name.
// databaseCreateRequest is the JSON wire format for creating a database.
// It uses camelCase JSON tags to match the SDK transport types.
type databaseCreateRequest struct {
	TenantID string `json:"tenantId,omitempty"`
	Name     string `json:"name"`
	Backend  string `json:"backend"`
	DataDir  string `json:"dataDir,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Replicas int    `json:"replicas,omitempty"`
	// DedicatedWAL requests a dedicated WAL for a dedicated-raft database;
	// required for every database of a strict-isolation tenant.
	DedicatedWAL bool                               `json:"dedicatedWal,omitempty"`
	Protocol     databaseCreateProtocolConfig       `json:"protocol,omitempty"`
	Raft         *databaseRaftRequest               `json:"raft,omitempty"`
	Barrier      *databaseCreateBarrierConfig       `json:"barrier,omitempty"`
	Connection   *database.DatabaseConnectionConfig `json:"connection,omitempty"`
	SQL          *database.SQLConfig                `json:"sql,omitempty"`
}

// databaseCreateBarrierConfig is the JSON wire format for the barrier
// section of a database creation request. Secret carries the barrier
// passphrase for the initial Initialize call; it is never persisted (the
// domain database.DatabaseBarrierConfig has no secret field) and is only
// used to build the seal.Credentials passed to EnsureBarrier.
type databaseCreateBarrierConfig struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"`
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`
	Secret         string `json:"secret,omitempty"`
}

// databaseCreateProtocolConfig matches the SDK's camelCase JSON format.
type databaseCreateProtocolConfig struct {
	MySQL      bool                      `json:"mysql"`
	PostgreSQL bool                      `json:"postgresql"`
	Etcd       *databaseCreateEtcdConfig `json:"etcd,omitempty"`
}

// databaseCreateEtcdConfig matches the SDK's camelCase JSON format.
type databaseCreateEtcdConfig struct {
	Enabled             bool     `json:"enabled"`
	ListenAddr          string   `json:"listenAddr"`
	AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
}

// toDomain converts the wire format to the domain ProtocolConfig.
func (p databaseCreateProtocolConfig) toDomain() database.ProtocolConfig {
	cfg := database.ProtocolConfig{
		MySQL:      p.MySQL,
		PostgreSQL: p.PostgreSQL,
	}
	if p.Etcd != nil {
		cfg.Etcd = &database.EtcdConfig{
			Enabled:             p.Etcd.Enabled,
			ListenAddr:          p.Etcd.ListenAddr,
			AdvertiseClientURLs: p.Etcd.AdvertiseClientURLs,
		}
	}
	return cfg
}

type databaseNameRequest struct {
	Name string `json:"name"`
}

// databaseUpdateProtocolRequest is the JSON wire format for updating the
// wire protocol configuration of a database.
type databaseUpdateProtocolRequest struct {
	Name       string `json:"name"`
	MySQL      bool   `json:"mysql"`
	PostgreSQL bool   `json:"postgresql"`
}

// databaseUpdateEtcdRequest is the JSON wire format for updating the etcd
// wire protocol configuration of a database.
type databaseUpdateEtcdRequest struct {
	Name                string   `json:"name"`
	Enabled             bool     `json:"enabled"`
	ListenAddr          string   `json:"listenAddr"`
	AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
}

// databaseUpdateRaftRequest is the JSON wire format for updating the Raft
// consensus configuration of a database.
type databaseUpdateRaftRequest struct {
	Name string               `json:"name"`
	Raft *databaseRaftRequest `json:"raft"`
}

// databaseRaftRequest is the Raft configuration a create or Raft-update
// request carries. It has no shard ID: the cluster issues a database's shard
// ID at create, and no request names one, so a `shardId` key is not a field
// and is ignored, as on every other transport. The discovery blocks carry
// their secrets in; responses report them only as set.
type databaseRaftRequest struct {
	ClusterMode    string                             `json:"clusterMode"`
	ReplicaID      uint64                             `json:"replicaId,omitempty"`
	RaftAddress    string                             `json:"raftAddress,omitempty"`
	Peers          []string                           `json:"peers,omitempty"`
	LocalNodeCount int                                `json:"localNodeCount,omitempty"`
	Discovery      string                             `json:"discovery,omitempty"`
	Static         *database.StaticDiscoveryConfig    `json:"static,omitempty"`
	Multicast      *database.MulticastDiscoveryConfig `json:"multicast,omitempty"`
	DNS            *database.DNSDiscoveryConfig       `json:"dns,omitempty"`
	Token          *database.TokenDiscoveryConfig     `json:"token,omitempty"`
}

// toDomain converts the wire request to the domain configuration; nil stays
// nil (no Raft configuration).
func (r *databaseRaftRequest) toDomain() *database.DatabaseRaftConfig {
	if r == nil {
		return nil
	}
	return &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftClusterMode(r.ClusterMode),
		ReplicaID:      r.ReplicaID,
		RaftAddress:    r.RaftAddress,
		Peers:          r.Peers,
		LocalNodeCount: r.LocalNodeCount,
		Discovery:      database.DiscoveryMethod(r.Discovery),
		Static:         r.Static,
		Multicast:      r.Multicast,
		DNS:            r.DNS,
		Token:          r.Token,
	}
}

// databaseScaleRequest is the JSON wire format for changing the replication
// factor of a dedicated-raft database.
type databaseScaleRequest struct {
	Name           string `json:"name"`
	TargetReplicas int    `json:"targetReplicas"`
}

// databaseUpdateBarrierRequest is the JSON wire format for updating the
// encryption barrier configuration of a database.
type databaseUpdateBarrierRequest struct {
	Name    string                          `json:"name"`
	Barrier *database.DatabaseBarrierConfig `json:"barrier"`
}

// databaseUpdateConnectionRequest is the JSON wire format for updating the
// remote connection configuration of a database.
type databaseUpdateConnectionRequest struct {
	Name       string                             `json:"name"`
	Connection *database.DatabaseConnectionConfig `json:"connection"`
}

// databaseUpdateSQLRequest is the JSON wire format for updating a
// database's SQL settings.
type databaseUpdateSQLRequest struct {
	Name string              `json:"name"`
	SQL  *database.SQLConfig `json:"sql"`
}

// databaseUpdateCompactionRequest is the JSON wire format for setting a
// database's scheduled-compaction policy.
type databaseUpdateCompactionRequest struct {
	Name       string             `json:"name"`
	Compaction *compaction.Policy `json:"compaction"`
}
