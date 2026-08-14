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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DatabaseClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DatabaseClient implementation
// ---------------------------------------------------------------------------

// CreateDatabase creates a new database by delegating to the injected
// DatabaseServicer. Returns NilServiceError when no database service
// has been configured.
func (t *Transport) CreateDatabase(ctx context.Context, req transport.CreateDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	cfg := database.DatabaseConfig{
		Name:     req.Name,
		Engine:   database.EngineType(req.Engine),
		Backend:  database.BackendType(req.Backend),
		DataDir:  req.DataDir,
		Mode:     database.DatabaseMode(req.Mode),
		Replicas: req.Replicas,
		Protocol: database.ProtocolConfig{
			MySQL:      req.Protocol.MySQL,
			PostgreSQL: req.Protocol.PostgreSQL,
		},
		Raft:       fromTransportRaftConfig(req.Raft),
		Barrier:    fromTransportBarrierConfig(req.Barrier),
		Connection: fromTransportConnectionConfig(req.Connection),
	}

	var info *database.DatabaseInfo
	var err error
	if req.Barrier != nil {
		creds := seal.Credentials{Secret: []byte(req.Barrier.Secret)}
		if cc, ok := t.dbSvc.(service.DatabaseCredentialCreator); ok {
			info, err = cc.CreateWithCredentials(ctx, cfg, creds)
		} else {
			info, err = t.dbSvc.Create(cfg)
		}
	} else {
		info, err = t.dbSvc.Create(cfg)
	}
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// GetDatabase returns information about a specific database by delegating
// to the injected DatabaseServicer. Returns NilServiceError when no
// database service has been configured.
func (t *Transport) GetDatabase(_ context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.Get(name)
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// ListDatabases returns information about all databases by delegating
// to the injected DatabaseServicer. Returns NilServiceError when no
// database service has been configured.
func (t *Transport) ListDatabases(_ context.Context) ([]transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	infos, err := t.dbSvc.List()
	if err != nil {
		return nil, err
	}
	out := make([]transport.DatabaseInfo, len(infos))
	for i, info := range infos {
		out[i] = *toTransportDatabaseInfo(&info)
	}
	return out, nil
}

// DropDatabase removes a database by name by delegating to the injected
// DatabaseServicer. Returns NilServiceError when no database service
// has been configured.
func (t *Transport) DropDatabase(_ context.Context, name string) error {
	if t.dbSvc == nil {
		return &NilServiceError{Service: "DatabaseServicer"}
	}
	return t.dbSvc.Drop(name)
}

// UpdateDatabaseProtocol updates the wire protocol configuration for a
// database by delegating to the injected DatabaseServicer. Returns
// NilServiceError when no database service has been configured.
func (t *Transport) UpdateDatabaseProtocol(_ context.Context, req transport.UpdateDatabaseProtocolRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateProtocol(req.Name, database.ProtocolConfig{
		MySQL:      req.MySQL,
		PostgreSQL: req.PostgreSQL,
	})
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// UpdateDatabaseEtcd updates the etcd wire protocol configuration for a
// database by delegating to the injected DatabaseServicer. Returns
// NilServiceError when no database service has been configured.
func (t *Transport) UpdateDatabaseEtcd(_ context.Context, req transport.UpdateDatabaseEtcdRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateEtcd(req.Name, &database.EtcdConfig{
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientURLs: req.AdvertiseClientURLs,
	})
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// UpdateDatabaseRaft updates the Raft consensus configuration for a
// database by delegating to the injected DatabaseServicer.
func (t *Transport) UpdateDatabaseRaft(_ context.Context, req transport.UpdateDatabaseRaftRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateRaft(req.Name, fromTransportRaftConfig(req.Raft))
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// ScaleDatabase changes the replication factor of a dedicated-raft database
// by delegating to the injected DatabaseServicer's tenant-scoped
// ScaleForTenant (unlike UpdateDatabaseRaft/UpdateDatabaseBarrier, scaling
// has no non-tenant plain variant on DatabaseServicer -- it always enforces
// tenant quota, so ctx is threaded through here).
func (t *Transport) ScaleDatabase(ctx context.Context, req transport.ScaleDatabaseRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.ScaleForTenant(ctx, req.Name, req.TargetReplicas)
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// UpdateDatabaseBarrier updates the encryption barrier configuration for
// a database by delegating to the injected DatabaseServicer.
func (t *Transport) UpdateDatabaseBarrier(_ context.Context, req transport.UpdateDatabaseBarrierRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateBarrier(req.Name, fromTransportBarrierConfig(req.Barrier))
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// UpdateDatabaseConnection updates the remote connection configuration for
// a database by delegating to the injected DatabaseServicer.
func (t *Transport) UpdateDatabaseConnection(_ context.Context, req transport.UpdateDatabaseConnectionRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateConnection(req.Name, fromTransportConnectionConfig(req.Connection))
	if err != nil {
		return nil, err
	}
	return toTransportDatabaseInfo(info), nil
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// toTransportDatabaseInfo converts a database.DatabaseInfo to a
// transport.DatabaseInfo.
func toTransportDatabaseInfo(info *database.DatabaseInfo) *transport.DatabaseInfo {
	ti := &transport.DatabaseInfo{
		Name:    info.Name,
		Engine:  string(info.Engine),
		Backend: string(info.Backend),
		Status:  string(info.Status),
		Protocol: transport.ProtocolConfig{
			MySQL:      info.Protocol.MySQL,
			PostgreSQL: info.Protocol.PostgreSQL,
		},
		DataDir:  info.DataDir,
		ShardID:  info.ShardID,
		Mode:     string(info.Mode),
		Replicas: info.Replicas,
	}
	if info.Protocol.Etcd != nil {
		ti.Protocol.Etcd = &transport.EtcdProtocolConfig{
			Enabled:             info.Protocol.Etcd.Enabled,
			ListenAddr:          info.Protocol.Etcd.ListenAddr,
			AdvertiseClientURLs: info.Protocol.Etcd.AdvertiseClientURLs,
		}
	}
	if info.Raft != nil {
		ti.Raft = &transport.DatabaseRaftConfig{
			ClusterMode:    string(info.Raft.ClusterMode),
			ShardID:        info.Raft.ShardID,
			ReplicaID:      info.Raft.ReplicaID,
			RaftAddress:    info.Raft.RaftAddress,
			Peers:          info.Raft.Peers,
			LocalNodeCount: info.Raft.LocalNodeCount,
		}
	}
	if info.Barrier != nil {
		ti.Barrier = &transport.DatabaseBarrierConfig{
			Enabled:        info.Barrier.Enabled,
			Mode:           info.Barrier.Mode,
			SharesRequired: info.Barrier.SharesRequired,
			SharesTotal:    info.Barrier.SharesTotal,
		}
	}
	if info.Connection != nil {
		ti.Connection = &transport.DatabaseConnectionConfig{
			Transport: info.Connection.Transport,
			Address:   info.Connection.Address,
			CAFile:    info.Connection.CAFile,
			CertFile:  info.Connection.CertFile,
			KeyFile:   info.Connection.KeyFile,
		}
	}
	return ti
}

// fromTransportRaftConfig converts transport raft config to domain type.
func fromTransportRaftConfig(rc *transport.DatabaseRaftConfig) *database.DatabaseRaftConfig {
	if rc == nil {
		return nil
	}
	return &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftClusterMode(rc.ClusterMode),
		ShardID:        rc.ShardID,
		ReplicaID:      rc.ReplicaID,
		RaftAddress:    rc.RaftAddress,
		Peers:          rc.Peers,
		LocalNodeCount: rc.LocalNodeCount,
	}
}

// fromTransportBarrierConfig converts transport barrier config to domain type.
func fromTransportBarrierConfig(bc *transport.DatabaseBarrierConfig) *database.DatabaseBarrierConfig {
	if bc == nil {
		return nil
	}
	return &database.DatabaseBarrierConfig{
		Enabled:        bc.Enabled,
		Mode:           bc.Mode,
		SharesRequired: bc.SharesRequired,
		SharesTotal:    bc.SharesTotal,
	}
}

// fromTransportConnectionConfig converts transport connection config to domain type.
func fromTransportConnectionConfig(cc *transport.DatabaseConnectionConfig) *database.DatabaseConnectionConfig {
	if cc == nil {
		return nil
	}
	return &database.DatabaseConnectionConfig{
		Transport: cc.Transport,
		Address:   cc.Address,
		CAFile:    cc.CAFile,
		CertFile:  cc.CertFile,
		KeyFile:   cc.KeyFile,
	}
}
