// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package database

import "github.com/jeremyhahn/go-qrdb/pkg/tenantid"

// OwnedBy reports whether tenantID owns a database whose recorded owner is
// owner. It is THE ownership rule: every path that decides whether a caller
// may address a database by name -- the manager's *ForTenant operations, the
// database service, the service registry and catalog, the raft backend, the
// SQL provider -- asks it, and none restates it.
//
// Both sides are normalized (tenantid.Normalize), so an empty owner -- the
// bootstrap databases a node creates before any tenant exists carry none -- is
// the system tenant's, and the system tenant is confined to its own databases
// exactly as every other tenant is: there is no cross-tenant database access.
func OwnedBy(owner, tenantID string) bool {
	return tenantid.Normalize(owner) == tenantid.Normalize(tenantID)
}

// GetOwned returns the named database when tenantID owns it (OwnedBy), and
// *NotFoundError otherwise -- for a database that does not exist and for one
// another tenant owns alike. Database names are one node-wide namespace, so
// answering another tenant's database any other way would tell the caller
// which names other tenants use.
//
// Ownership is decided before the drop fence: the owner of a dropping
// database gets *DroppingError, everyone else *NotFoundError. Checking the
// fence first (through Get) answered a cross-tenant probe of someone else's
// dropping database with 409/ABORTED while a missing name got 404, the
// oracle GetOwned exists to close (SEC-8(b)).
func (m *Manager) GetOwned(name, tenantID string) (*Database, error) {
	db, err := m.GetOwnedIgnoringFence(name, tenantID)
	if err != nil {
		return nil, err
	}
	if db.Dropping() {
		return nil, &DroppingError{Name: name, TenantID: db.TenantID()}
	}
	return db, nil
}

// GetOwnedIgnoringFence is GetOwned without the drop-fence refusal, for the
// drop itself and the bookkeeping it releases (DropForTenant,
// GetForTenantIgnoringFence, the dedicated-raft info source resuming a drop): the owner resuming an interrupted drop must
// reach the database its own fence refuses. Ownership is read from the
// catalog directly, never through Get, for the reason GetOwned gives.
func (m *Manager) GetOwnedIgnoringFence(name, tenantID string) (*Database, error) {
	val, ok := m.databases.Load(name)
	if !ok {
		return nil, &NotFoundError{Name: name}
	}
	db := val.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
	if !OwnedBy(db.TenantID(), tenantID) {
		return nil, &NotFoundError{Name: name}
	}
	return db, nil
}
