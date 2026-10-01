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

package database

import (
	"context"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// CreateForTenant creates a new database scoped to the tenant from the context.
// It sets cfg.TenantID from the context and delegates to Create. Equivalent
// to CreateForTenantWithCredentials with an empty seal.Credentials -- barrier
// initialization with an empty secret only succeeds when cfg.Barrier is nil
// or not enabled.
func (m *Manager) CreateForTenant(ctx context.Context, cfg DatabaseConfig) (*Database, error) {
	return m.CreateForTenantWithCredentials(ctx, cfg, seal.Credentials{})
}

// CreateForTenantWithCredentials creates a new database scoped to the tenant
// from the context, using creds to initialize a barrier when cfg.Barrier is
// enabled. It sets cfg.TenantID from the context and delegates to
// CreateWithCredentials.
func (m *Manager) CreateForTenantWithCredentials(ctx context.Context, cfg DatabaseConfig, creds seal.Credentials) (*Database, error) {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return nil, &TenantIDRequiredError{Operation: "CreateForTenant"}
	}
	cfg.TenantID = tenantID
	return m.CreateWithCredentials(ctx, cfg, creds)
}

// GetForTenant returns a database by name after verifying tenant ownership.
// A database of another tenant is answered with NotFoundError, exactly as a
// missing one is (see getOwned).
func (m *Manager) GetForTenant(ctx context.Context, name string) (*Database, error) {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return nil, &TenantIDRequiredError{Operation: "GetForTenant"}
	}

	return m.getOwned(name, tenantID)
}

// ListForTenant returns databases owned by the tenant from the context.
// Results are sorted by name.
func (m *Manager) ListForTenant(ctx context.Context) ([]DatabaseInfo, error) {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return nil, &TenantIDRequiredError{Operation: "ListForTenant"}
	}

	var infos []DatabaseInfo
	m.databases.Range(func(_, value any) bool {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		if db.TenantID() == tenantID {
			infos = append(infos, db.Info())
		}
		return true
	})
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos, nil
}

// DropForTenant drops a database after verifying tenant ownership. A database
// of another tenant is answered with NotFoundError, exactly as a missing one
// is (see getOwned).
func (m *Manager) DropForTenant(ctx context.Context, name string) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "DropForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.Drop(ctx, name)
}

// UpdateProtocolForTenant updates protocol config after verifying tenant ownership.
func (m *Manager) UpdateProtocolForTenant(ctx context.Context, name string, protocol ProtocolConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "UpdateProtocolForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.UpdateProtocol(name, protocol)
}

// UpdateEtcdForTenant updates etcd config after verifying tenant ownership.
func (m *Manager) UpdateEtcdForTenant(ctx context.Context, name string, etcd *EtcdConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "UpdateEtcdForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.UpdateEtcd(name, etcd)
}

// UpdateRaftForTenant updates Raft config after verifying tenant ownership.
func (m *Manager) UpdateRaftForTenant(ctx context.Context, name string, raft *DatabaseRaftConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "UpdateRaftForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.UpdateRaft(name, raft)
}

// UpdateBarrierForTenant updates barrier config after verifying tenant ownership.
func (m *Manager) UpdateBarrierForTenant(ctx context.Context, name string, barrier *DatabaseBarrierConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "UpdateBarrierForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.UpdateBarrier(name, barrier)
}

// UpdateConnectionForTenant updates connection config after verifying tenant ownership.
func (m *Manager) UpdateConnectionForTenant(ctx context.Context, name string, conn *DatabaseConnectionConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "UpdateConnectionForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.UpdateConnection(name, conn)
}

// UpdateSQLForTenant updates the SQL settings after verifying tenant
// ownership.
func (m *Manager) UpdateSQLForTenant(ctx context.Context, name string, sqlCfg *SQLConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenant.IsSystemTenant(tenantID) {
		return &TenantIDRequiredError{Operation: "UpdateSQLForTenant"}
	}

	if _, err := m.getOwned(name, tenantID); err != nil {
		return err
	}

	return m.UpdateSQL(name, sqlCfg)
}

// getOwned returns the named database when tenantID owns it, and
// NotFoundError otherwise -- for a database that does not exist and for one
// another tenant owns alike. Database names are one node-wide namespace, so
// answering another tenant's database with an access denial told the caller
// which names other tenants use; the two answers are now the same.
func (m *Manager) getOwned(name, tenantID string) (*Database, error) {
	db, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if db.TenantID() != tenantID {
		return nil, &NotFoundError{Name: name}
	}
	return db, nil
}

// Compile-time interface check: the tenant teardown's refusal reads this
// catalog, and a drift in its signature must fail here rather than at the
// wiring site.
var _ tenant.DatabaseLister = (*Manager)(nil)

// TenantDatabases returns the names of the databases owned by tenantID,
// sorted.
//
// It is the catalog behind tenant.TenantService's bottom-up refusal, which is
// why it takes the tenant as an ARGUMENT rather than reading it from the
// context the way ListForTenant does: the caller is a tenant teardown acting
// ON a tenant, not a request acting AS one, and binding a synthetic tenant
// context just to read it back would be the kind of round trip that hides a
// mismatch.
//
// It reports what THIS node's catalog holds. A database's meta.json is
// node-local while its keys are in the replicated store, so a node whose
// catalog is incomplete under-reports -- which is the reason the tenant
// reclamation scans the tenant's whole namespace rather than trusting this
// list to have bounded it.
//
// An unknown tenant owns no databases, which is not an error. The system
// tenant is answered like any other: its databases are those recorded with
// no owner or with the system owner, matching GetForTenant's view.
func (m *Manager) TenantDatabases(_ context.Context, tenantID string) ([]string, error) {
	var names []string
	m.databases.Range(func(_, value any) bool {
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		owner := db.TenantID()
		if owner == tenantID || (tenant.IsSystemTenant(tenantID) && tenant.IsSystemTenant(owner)) {
			names = append(names, db.Name())
		}
		return true
	})
	sort.Strings(names)
	return names, nil
}
