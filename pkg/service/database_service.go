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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// DatabaseServicer defines CRUD operations for database management.
type DatabaseServicer interface {
	// Create creates a new database with the given configuration.
	Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)

	// Get returns information about a specific database.
	Get(name string) (*database.DatabaseInfo, error)

	// List returns information about all open databases.
	List() ([]database.DatabaseInfo, error)

	// Drop closes and removes a database, deleting its data.
	Drop(name string) error

	// UpdateProtocol updates the wire protocol configuration for a database
	// and returns the updated runtime information.
	UpdateProtocol(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)

	// UpdateEtcd updates the etcd wire protocol configuration for a database
	// and returns the updated runtime information.
	UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)

	// UpdateRaft updates the Raft consensus configuration for a database.
	UpdateRaft(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)

	// UpdateBarrier updates the encryption barrier configuration for a database.
	UpdateBarrier(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)

	// UpdateConnection updates the remote connection configuration for a database.
	UpdateConnection(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)

	// SealDatabase seals the encryption barrier for a database.
	SealDatabase(ctx context.Context, name string) error

	// UnsealDatabase unseals the encryption barrier for a database.
	UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error

	// DatabaseBarrierStatus returns the barrier status for a database.
	DatabaseBarrierStatus(name string) (*seal.BarrierStatus, error)

	// Tenant-scoped operations (context-based tenant enforcement)

	// CreateForTenant creates a new database scoped to the context's tenant.
	CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error)

	// GetForTenant returns database info after verifying tenant ownership.
	GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error)

	// ListForTenant returns databases owned by the context's tenant.
	ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error)

	// DropForTenant drops a database after verifying tenant ownership.
	DropForTenant(ctx context.Context, name string) error

	// UpdateProtocolForTenant updates protocol config with tenant enforcement.
	UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error)

	// UpdateEtcdForTenant updates etcd config with tenant enforcement.
	UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error)

	// UpdateRaftForTenant updates Raft config with tenant enforcement.
	UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)

	// UpdateBarrierForTenant updates barrier config with tenant enforcement.
	UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)

	// UpdateConnectionForTenant updates connection config with tenant enforcement.
	UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
}

// DatabaseServiceOption configures optional DatabaseService behavior.
type DatabaseServiceOption func(*DatabaseService)

// WithBarrierManager sets the DatabaseBarrierManager for seal/unseal operations.
func WithBarrierManager(bm *database.DatabaseBarrierManager) DatabaseServiceOption {
	return func(s *DatabaseService) { s.barrierMgr = bm }
}

// WithQuotaChecker sets a quota.Checker that is consulted before tenant-scoped
// database creation. When nil the check is skipped.
func WithQuotaChecker(checker quota.Checker) DatabaseServiceOption {
	return func(s *DatabaseService) { s.quotaChecker = checker }
}

// DatabaseService implements DatabaseServicer using a database.Manager.
type DatabaseService struct {
	manager      *database.Manager
	barrierMgr   *database.DatabaseBarrierManager
	quotaChecker quota.Checker
}

// Compile-time interface check.
var _ DatabaseServicer = (*DatabaseService)(nil)

// NewDatabaseService creates a new DatabaseService backed by the given manager.
func NewDatabaseService(manager *database.Manager, opts ...DatabaseServiceOption) DatabaseServicer {
	s := &DatabaseService{manager: manager}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Create creates a new database with the given configuration and returns
// its runtime information.
func (s *DatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	db, err := s.manager.Create(cfg)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// Get returns runtime information about a specific database.
func (s *DatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// List returns runtime information about all open databases, sorted by name.
func (s *DatabaseService) List() ([]database.DatabaseInfo, error) {
	return s.manager.List(), nil
}

// Drop closes and removes a database, deleting its data directory.
func (s *DatabaseService) Drop(name string) error {
	return s.manager.Drop(name)
}

// UpdateProtocol updates the wire protocol configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateProtocol(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateProtocol(name, protocol); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateEtcd updates the etcd wire protocol configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateEtcd(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateEtcd(name, etcd); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateRaft updates the Raft consensus configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateRaft(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateRaft(name, raft); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateBarrier updates the encryption barrier configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateBarrier(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateBarrier(name, barrier); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// UpdateConnection updates the remote connection configuration for the named
// database and returns the updated runtime information.
func (s *DatabaseService) UpdateConnection(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if err := s.manager.UpdateConnection(name, conn); err != nil {
		return nil, err
	}
	db, err := s.manager.Get(name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// SealDatabase seals the encryption barrier for the named database.
func (s *DatabaseService) SealDatabase(ctx context.Context, name string) error {
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.SealDatabase(name)
}

// UnsealDatabase unseals the encryption barrier for the named database.
func (s *DatabaseService) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if s.barrierMgr == nil {
		return &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.UnsealDatabase(ctx, name, creds)
}

// DatabaseBarrierStatus returns the barrier status for the named database.
func (s *DatabaseService) DatabaseBarrierStatus(name string) (*seal.BarrierStatus, error) {
	if s.barrierMgr == nil {
		return nil, &BarrierManagerNotConfiguredError{}
	}
	return s.barrierMgr.BarrierStatus(name)
}

// CreateForTenant creates a new database scoped to the context's tenant
// and returns its runtime information.
func (s *DatabaseService) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.Create(cfg)
	}
	if s.quotaChecker != nil {
		tenantID := tenant.FromContext(ctx)
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceDatabases); err != nil {
			return nil, err
		}
	}
	db, err := s.manager.CreateForTenant(ctx, cfg)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// GetForTenant returns runtime information about a database after verifying
// tenant ownership via the context.
func (s *DatabaseService) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.Get(name)
	}
	db, err := s.manager.GetForTenant(ctx, name)
	if err != nil {
		return nil, err
	}
	info := db.Info()
	return &info, nil
}

// ListForTenant returns runtime information about databases owned by the
// context's tenant, sorted by name.
func (s *DatabaseService) ListForTenant(ctx context.Context) ([]database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.List()
	}
	return s.manager.ListForTenant(ctx)
}

// DropForTenant drops a database after verifying tenant ownership via the context.
func (s *DatabaseService) DropForTenant(ctx context.Context, name string) error {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.Drop(name)
	}
	return s.manager.DropForTenant(ctx, name)
}

// UpdateProtocolForTenant updates the wire protocol configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateProtocolForTenant(ctx context.Context, name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateProtocol(name, protocol)
	}
	if err := s.manager.UpdateProtocolForTenant(ctx, name, protocol); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateEtcdForTenant updates the etcd wire protocol configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateEtcdForTenant(ctx context.Context, name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateEtcd(name, etcd)
	}
	if err := s.manager.UpdateEtcdForTenant(ctx, name, etcd); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateRaftForTenant updates the Raft consensus configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateRaftForTenant(ctx context.Context, name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateRaft(name, raft)
	}
	if err := s.manager.UpdateRaftForTenant(ctx, name, raft); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateBarrierForTenant updates the encryption barrier configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateBarrierForTenant(ctx context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateBarrier(name, barrier)
	}
	if err := s.manager.UpdateBarrierForTenant(ctx, name, barrier); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}

// UpdateConnectionForTenant updates the remote connection configuration for the named
// database with tenant enforcement and returns the updated runtime information.
func (s *DatabaseService) UpdateConnectionForTenant(ctx context.Context, name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if tenant.FromContext(ctx) == tenant.SystemTenantID {
		return s.UpdateConnection(name, conn)
	}
	if err := s.manager.UpdateConnectionForTenant(ctx, name, conn); err != nil {
		return nil, err
	}
	return s.GetForTenant(ctx, name)
}
