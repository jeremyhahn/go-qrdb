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
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// handleDatabaseCreate unmarshals a JSON database creation request, creates
// the database via the service layer, and returns the serialized database info.
func (s *Server) handleDatabaseCreate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}

	var req databaseCreateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	cfg := database.DatabaseConfig{
		TenantID: req.TenantID,
		Name:     req.Name,
		Engine:   database.EngineType(req.Engine),
		Backend:  database.BackendType(req.Backend),
		DataDir:  req.DataDir,
		Mode:     database.DatabaseMode(req.Mode),
		Protocol: req.Protocol.toDomain(),
		Raft:     req.Raft,
		Barrier:  req.Barrier,
	}
	if req.Connection != nil {
		cfg.Connection = req.Connection
	}

	info, err := s.databaseSvc.Create(cfg)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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

	info, err := s.databaseSvc.Get(req.Name)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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

	infos, err := s.databaseSvc.List()
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(infos)
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

	if err := s.databaseSvc.Drop(req.Name); err != nil {
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

	info, err := s.databaseSvc.UpdateProtocol(req.Name, database.ProtocolConfig{
		MySQL:      req.MySQL,
		PostgreSQL: req.PostgreSQL,
	})
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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

	info, err := s.databaseSvc.UpdateEtcd(req.Name, &database.EtcdConfig{
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientURLs: req.AdvertiseClientURLs,
	})
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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

	info, err := s.databaseSvc.UpdateRaft(req.Name, req.Raft)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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

	info, err := s.databaseSvc.UpdateBarrier(req.Name, req.Barrier)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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

	info, err := s.databaseSvc.UpdateConnection(req.Name, req.Connection)
	if err != nil {
		return nil, err
	}

	resp, err := json.Marshal(info)
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
	TenantID   string                             `json:"tenantId,omitempty"`
	Name       string                             `json:"name"`
	Engine     string                             `json:"engine"`
	Backend    string                             `json:"backend"`
	DataDir    string                             `json:"dataDir,omitempty"`
	Mode       string                             `json:"mode,omitempty"`
	Protocol   databaseCreateProtocolConfig       `json:"protocol,omitempty"`
	Raft       *database.DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier    *database.DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection *database.DatabaseConnectionConfig `json:"connection,omitempty"`
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
	Name string                       `json:"name"`
	Raft *database.DatabaseRaftConfig `json:"raft"`
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
