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

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

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
		SQL:        fromTransportSQLConfig(req.SQL),
	}

	// The tenant-scoped create, as on every network transport: the database
	// is owned by the context's tenant and metered against its quota, and a
	// database service composed with the provisioning check enforces
	// databases:create_raft and databases:create_volume here too. A context
	// carrying no tenant creates for the system tenant.
	var creds seal.Credentials
	if req.Barrier != nil {
		var err error
		if creds, err = seal.CredentialsFor([]byte(req.Barrier.Secret)); err != nil {
			return nil, err
		}
		defer creds.Secret.Destroy()
	}
	info, err := service.CreateDatabaseForTenantWithCredentials(ctx, t.dbSvc, cfg, creds)
	if err != nil {
		return nil, err
	}
	return dbwire.Info(info), nil
}

// GetDatabase returns information about a specific database by delegating
// to the injected DatabaseServicer, as every remote transport does: through
// GetForTenant, which reads the database's scheduled-compaction policy from
// its own store. Returns NilServiceError when no database service has been
// configured.
func (t *Transport) GetDatabase(ctx context.Context, name string) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.GetForTenant(ctx, name)
	if err != nil {
		return nil, err
	}
	return dbwire.Info(info), nil
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
	return dbwire.Infos(infos), nil
}

// DropDatabase removes a database by name by delegating to the injected
// DatabaseServicer. Returns NilServiceError when no database service
// has been configured.
func (t *Transport) DropDatabase(ctx context.Context, name string) error {
	if t.dbSvc == nil {
		return &NilServiceError{Service: "DatabaseServicer"}
	}
	return t.dbSvc.Drop(ctx, name)
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
	return dbwire.Info(info), nil
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
	return dbwire.Info(info), nil
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
	return dbwire.Info(info), nil
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
	return dbwire.Info(info), nil
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
	return dbwire.Info(info), nil
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
	return dbwire.Info(info), nil
}

// UpdateDatabaseSQL updates a database's SQL settings by delegating to the
// injected DatabaseServicer.
func (t *Transport) UpdateDatabaseSQL(_ context.Context, req transport.UpdateDatabaseSQLRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateSQL(req.Name, fromTransportSQLConfig(req.SQL))
	if err != nil {
		return nil, err
	}
	return dbwire.Info(info), nil
}

// UpdateDatabaseCompaction sets a database's scheduled-compaction policy by
// delegating to the injected DatabaseServicer for the context's tenant. Unlike
// the other embedded updates it is tenant-scoped: a policy compacts that
// database's history, and the service confines every caller -- the system
// tenant an unbound context resolves to included -- to its own databases.
func (t *Transport) UpdateDatabaseCompaction(ctx context.Context, req transport.UpdateDatabaseCompactionRequest) (*transport.DatabaseInfo, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	info, err := t.dbSvc.UpdateCompactionForTenant(ctx, req.Name, fromTransportCompactionConfig(req.Compaction))
	if err != nil {
		return nil, err
	}
	return dbwire.Info(info), nil
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// fromTransportCompactionConfig converts a transport compaction policy to the
// domain type; nil stays nil (off).
func fromTransportCompactionConfig(c *transport.DatabaseCompactionConfig) *compaction.Policy {
	if c == nil {
		return nil
	}
	return &compaction.Policy{
		Mode:            compaction.Mode(c.Mode),
		RetainRevisions: c.RetainRevisions,
		RetainPeriod:    c.RetainPeriod,
	}
}

// fromTransportSQLConfig converts transport SQL settings to the domain type;
// nil stays nil (the defaults).
func fromTransportSQLConfig(c *transport.DatabaseSQLConfig) *database.SQLConfig {
	if c == nil {
		return nil
	}
	return &database.SQLConfig{Concurrency: database.Concurrency(c.Concurrency)}
}

// fromTransportRaftConfig converts transport raft config to domain type.
func fromTransportRaftConfig(rc *transport.DatabaseRaftConfig) *database.DatabaseRaftConfig {
	if rc == nil {
		return nil
	}
	return &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftClusterMode(rc.ClusterMode),
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
