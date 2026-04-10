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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// CreateForTenant creates a new database scoped to the tenant from the context.
// It sets cfg.TenantID from the context and delegates to Create.
func (m *Manager) CreateForTenant(ctx context.Context, cfg DatabaseConfig) (*Database, error) {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return nil, &TenantIDRequiredError{Operation: "CreateForTenant"}
	}
	cfg.TenantID = tenantID
	return m.Create(cfg)
}

// GetForTenant returns a database by name after verifying tenant ownership.
// Returns TenantAccessDeniedError if the database belongs to a different tenant.
func (m *Manager) GetForTenant(ctx context.Context, name string) (*Database, error) {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return nil, &TenantIDRequiredError{Operation: "GetForTenant"}
	}

	db, err := m.Get(name)
	if err != nil {
		return nil, err
	}

	if db.TenantID() != tenantID {
		return nil, &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return db, nil
}

// ListForTenant returns databases owned by the tenant from the context.
// Results are sorted by name.
func (m *Manager) ListForTenant(ctx context.Context) ([]DatabaseInfo, error) {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return nil, &TenantIDRequiredError{Operation: "ListForTenant"}
	}

	var infos []DatabaseInfo
	m.databases.Range(func(_, value any) bool {
		db := value.(*Database)
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

// DropForTenant drops a database after verifying tenant ownership.
// Returns TenantAccessDeniedError if the database belongs to a different tenant.
func (m *Manager) DropForTenant(ctx context.Context, name string) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return &TenantIDRequiredError{Operation: "DropForTenant"}
	}

	// Verify ownership before dropping.
	db, err := m.Get(name)
	if err != nil {
		return err
	}
	if db.TenantID() != tenantID {
		return &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return m.Drop(name)
}

// UpdateProtocolForTenant updates protocol config after verifying tenant ownership.
func (m *Manager) UpdateProtocolForTenant(ctx context.Context, name string, protocol ProtocolConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return &TenantIDRequiredError{Operation: "UpdateProtocolForTenant"}
	}

	db, err := m.Get(name)
	if err != nil {
		return err
	}
	if db.TenantID() != tenantID {
		return &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return m.UpdateProtocol(name, protocol)
}

// UpdateEtcdForTenant updates etcd config after verifying tenant ownership.
func (m *Manager) UpdateEtcdForTenant(ctx context.Context, name string, etcd *EtcdConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return &TenantIDRequiredError{Operation: "UpdateEtcdForTenant"}
	}

	db, err := m.Get(name)
	if err != nil {
		return err
	}
	if db.TenantID() != tenantID {
		return &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return m.UpdateEtcd(name, etcd)
}

// UpdateRaftForTenant updates Raft config after verifying tenant ownership.
func (m *Manager) UpdateRaftForTenant(ctx context.Context, name string, raft *DatabaseRaftConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return &TenantIDRequiredError{Operation: "UpdateRaftForTenant"}
	}

	db, err := m.Get(name)
	if err != nil {
		return err
	}
	if db.TenantID() != tenantID {
		return &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return m.UpdateRaft(name, raft)
}

// UpdateBarrierForTenant updates barrier config after verifying tenant ownership.
func (m *Manager) UpdateBarrierForTenant(ctx context.Context, name string, barrier *DatabaseBarrierConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return &TenantIDRequiredError{Operation: "UpdateBarrierForTenant"}
	}

	db, err := m.Get(name)
	if err != nil {
		return err
	}
	if db.TenantID() != tenantID {
		return &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return m.UpdateBarrier(name, barrier)
}

// UpdateConnectionForTenant updates connection config after verifying tenant ownership.
func (m *Manager) UpdateConnectionForTenant(ctx context.Context, name string, conn *DatabaseConnectionConfig) error {
	tenantID := tenant.FromContext(ctx)
	if tenantID == tenant.DefaultTenantID {
		return &TenantIDRequiredError{Operation: "UpdateConnectionForTenant"}
	}

	db, err := m.Get(name)
	if err != nil {
		return err
	}
	if db.TenantID() != tenantID {
		return &TenantAccessDeniedError{
			TenantID:     tenantID,
			DatabaseName: name,
		}
	}

	return m.UpdateConnection(name, conn)
}
