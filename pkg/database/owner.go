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

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

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
// reach the database its own fence refuses. It resolves the (tenant, name)
// pair, never the name alone through Get, so another tenant's database of
// the same name is never reached. A database the catalog records but this
// node has not opened is opened here (see Open). One this node holds open is
// first reconciled with its catalog record (revalidate): a database dropped
// through another node is released and answered *NotFoundError -- or, when
// the name was created again, resolved to its successor -- and a drop fence
// set or rolled back through another node is applied, so GetOwned refuses
// or admits the caller exactly as the record says.
func (m *Manager) GetOwnedIgnoringFence(name, tenantID string) (*Database, error) {
	ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
	defer cancel()
	if db, ok := m.Local(name, tenantID); ok {
		released, err := m.revalidate(ctx, keyFor(tenantID, name), db)
		if err != nil {
			return nil, err
		}
		if !released {
			return db, nil
		}
	}
	// Not open here, or released just now: the catalog may record it,
	// created through another node -- again, under a new incarnation -- or
	// held in a tenant home that was not reachable at boot.
	return m.Open(ctx, tenantID, name)
}

// RecordedIncarnation returns the incarnation the catalog records for
// tenantID's database name, read from the catalog itself rather than from
// the database open here. ok is false when the catalog holds no record of
// the name: it was dropped, or never created. Nothing is opened.
func (m *Manager) RecordedIncarnation(ctx context.Context, tenantID, name string) (incarnation string, ok bool, err error) {
	cat, err := m.catalog("resolve incarnation")
	if err != nil {
		return "", false, err
	}
	meta, err := cat.Get(ctx, tenantID, name)
	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return meta.Incarnation, true, nil
}

// revalidate reconciles db, the entry this node holds open for key, with
// the catalog record before a caller is authorized against it, and reports
// whether db was released because the record is gone or names another
// incarnation (see reconcile).
//
// The read is Catalog.Get, which a node serves through the system KV
// service's linearizable read (a ReadIndex, no Raft proposal). That is the
// cheapest read that observes every drop and re-create committed before the
// call through any node; a local or lease read could authorize a caller
// against a database already dropped elsewhere. A record that cannot be
// read fails the resolution closed: the error is returned, and nothing is
// authorized against an entry the catalog could not confirm. So does a stale
// entry release had to keep: *StaleDatabaseError is returned, never the
// entry.
func (m *Manager) revalidate(ctx context.Context, key dbKey, db *Database) (bool, error) {
	cat, err := m.catalog("resolve database")
	if err != nil {
		return false, err
	}
	meta, err := cat.Get(ctx, key.tenant, key.name)
	var notFound *NotFoundError
	if err != nil && !errors.As(err, &notFound) {
		return false, err
	}
	return m.reconcile(ctx, cat, key, db, meta, err == nil)
}

// Local returns the tenant's database when it is open on this node, dropping
// or not, and never consults the catalog. It is the lookup for a layer the
// catalog itself reads through: an isolated tenant's catalog lives in its home
// store, and resolving that store must not ask the catalog again.
func (m *Manager) Local(name, tenantID string) (*Database, bool) {
	val, ok := m.databases.Load(keyFor(tenantID, name))
	if !ok {
		return nil, false
	}
	return val.(*Database), true //nolint:errcheck // type assertion on a value this container only ever holds
}
