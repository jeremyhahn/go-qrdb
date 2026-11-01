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
	"errors"
	"log/slog"
	"os"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
	if tenantid.IsSystem(tenantID) {
		return nil, &TenantIDRequiredError{Operation: "CreateForTenant"}
	}
	cfg.TenantID = tenantID
	return m.CreateWithCredentials(ctx, cfg, creds)
}

// GetForTenant returns a database by name when the context's tenant owns it
// (GetOwned). A database of another tenant is answered with NotFoundError,
// exactly as a missing one is; the system tenant is held to the same rule.
func (m *Manager) GetForTenant(ctx context.Context, name string) (*Database, error) {
	return m.GetOwned(name, tenant.FromContext(ctx))
}

// GetForTenantIgnoringFence returns a database by name after verifying
// tenant ownership (OwnedBy), WITHOUT refusing when the database's local drop
// fence is set -- unlike GetForTenant, which refuses a dropping database.
//
// A caller resolving THIS information -- Backend/Replicas/DedicatedWAL, the
// bookkeeping DropForTenant needs to release the tenant's quota once its drop
// succeeds -- is the drop resuming its own claim after a restart, not an
// ordinary operation the fence exists to refuse (go-qrdb#482 round 3, finding
// 9). Returns NotFoundError when this node has no local record for name at
// all: that absence is itself meaningful to DropForTenant (finding 6). Also
// NotFoundError -- never a distinguishable access-denied type -- when the
// record belongs to a different tenant (SEC-8(b)). The system tenant is held
// to the same rule as any other.
func (m *Manager) GetForTenantIgnoringFence(ctx context.Context, name string) (*Database, error) {
	return m.GetOwnedIgnoringFence(name, tenant.FromContext(ctx))
}

// ListForTenant returns the databases the catalog records for the tenant
// from the context (OwnedBy), the system tenant included, sorted by name.
//
// The catalog is the authority, not this node's open map: a database
// created through another node is opened here, a local entry whose record
// is gone (dropped through another node) or was replaced by a new
// incarnation is released, and a fence set or rolled back through another
// node is applied to the local entry.
func (m *Manager) ListForTenant(ctx context.Context) ([]DatabaseInfo, error) {
	cat, err := m.catalog("list databases")
	if err != nil {
		return nil, err
	}
	tenantID := tenant.FromContext(ctx)
	metas, err := cat.ListTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	recorded := make(map[string]DatabaseMeta, len(metas))
	for _, meta := range metas {
		recorded[meta.Name] = meta
	}
	m.databases.Range(func(key, value any) bool {
		k, _ := key.(dbKey)
		db := value.(*Database) //nolint:errcheck // type assertion on a value this container only ever holds
		if !OwnedBy(k.tenant, tenantID) {
			return true
		}
		meta, ok := recorded[k.name]
		if _, err := m.reconcile(ctx, cat, k, db, meta, ok); err != nil {
			m.logger.Warn("obsolete database entry not released; it is refused until it is",
				slog.String("tenant", k.tenant), slog.String("name", k.name),
				slog.String("error", err.Error()))
		}
		return true
	})

	infos := make([]DatabaseInfo, 0, len(metas))
	for _, meta := range metas {
		key := keyFor(tenantID, meta.Name)
		// Another node holds it: it is listed as the catalog records it,
		// and nothing is opened here.
		if _, held := m.ownerRefusal(key, meta); held {
			infos = append(infos, remoteInfo(meta))
			continue
		}
		db, err := m.openRecord(key, meta)
		if err != nil {
			m.logger.Warn("database recorded in the catalog did not open",
				slog.String("tenant", tenantID),
				slog.String("name", meta.Name),
				slog.String("error", err.Error()))
			continue
		}
		infos = append(infos, db.Info())
	}
	sortInfos(infos)
	return infos, nil
}

// reconcile brings db, the entry this node holds open for key, in line with
// meta, its catalog record as just read (recorded false: there is none). A
// record that is gone or names another incarnation releases db; one that is
// dropping fences it; one no longer dropping clears a fence db still holds.
// A record of db's incarnation also carries the protocol and SQL settings a
// settings update through any node last wrote, and db takes them, so every
// consumer reading the reconciled entry sees them.
// It reports whether db was handed to release, so a caller resolving the name
// goes back through Open. A release that had to keep the obsolete entry is
// returned as a StaleDatabaseError: the entry is marked obsolete, and Open
// refuses it rather than serve it, until a later release removes it.
func (m *Manager) reconcile(ctx context.Context, cat Catalog, key dbKey, db *Database, meta DatabaseMeta, recorded bool) (bool, error) {
	if !recorded || meta.Incarnation != db.Incarnation() {
		db.obsolete.Store(true)
		return true, m.release(ctx, cat, key, db)
	}
	db.obsolete.Store(false)
	db.adoptSettings(meta.Protocol, meta.SQL)
	switch {
	case meta.Dropping:
		db.SetDropping(true)
	case db.Dropping():
		m.clearDropFence(ctx, cat, key, db)
	}
	return false, nil
}

// release forgets a local entry whose catalog record is gone or was
// replaced: it closes the backend, removes the barrier and this node's
// directory for the database, and drops the entry from the open map. The
// record itself is not touched; the node that removed or replaced it owns
// that.
//
// The listing that found the entry stale is a snapshot, and a create on this
// node can finish after it was taken: that database is in the open map with a
// live record the snapshot does not hold. So release decides on the record as
// it is now, read under the claim a create holds from its catalog insert to
// its map store (inFlight), and keeps an entry whose record still carries its
// incarnation, and clears the obsolete mark reconcile set on it. A record that
// cannot be read, or a name another operation holds, keeps the entry too: the
// release is destructive, and the next resolve or list retries it. Either is
// returned as a StaleDatabaseError, and the entry stays marked obsolete, so it
// is refused rather than served in the meantime.
func (m *Manager) release(ctx context.Context, cat Catalog, key dbKey, db *Database) error {
	unclaim, claimed := m.tryClaim(key)
	if !claimed {
		return &StaleDatabaseError{Name: key.name, TenantID: key.tenant}
	}
	defer unclaim()
	meta, err := cat.Get(ctx, key.tenant, key.name)
	var notFound *NotFoundError
	switch {
	case err == nil && meta.Incarnation == db.Incarnation():
		db.obsolete.Store(false)
		return nil
	case err != nil && !errors.As(err, &notFound):
		m.logger.Warn("database not released: its catalog record could not be read",
			slog.String("tenant", key.tenant), slog.String("name", key.name),
			slog.String("error", err.Error()))
		return &StaleDatabaseError{Name: key.name, TenantID: key.tenant, Err: err}
	}
	if !m.databases.CompareAndDelete(key, db) {
		return nil
	}
	if db.Status() == StatusOpen || db.Status() == StatusSealed {
		if err := db.Close(); err != nil {
			m.logger.Warn("released database did not close",
				slog.String("name", key.name), slog.String("error", err.Error()))
		}
	}
	if db.BarrierEnabled() && m.barrierMgr != nil {
		_ = m.barrierMgr.RemoveBarrier(key.name, key.tenant)
	}
	if err := os.RemoveAll(m.dbDir(key.tenant, key.name)); err != nil {
		m.logger.Warn("released database directory was not removed",
			slog.String("name", key.name), slog.String("error", err.Error()))
	}
	m.logger.Info("database released: its catalog record is gone",
		slog.String("tenant", key.tenant), slog.String("name", key.name))
	return nil
}

// clearDropFence clears the drop fence of a local entry whose listed record
// carries none: a drop through another node fenced it here, then rolled its
// fence back. A drop on this node holds the dropping claim from before it
// sets the fence until after it settles it, so the claim is taken for the
// clear and an active local drop's fence is kept. The record is re-read under
// the claim because the listing is a snapshot: a local drop that failed
// without rolling its fence back may have committed the fence since.
func (m *Manager) clearDropFence(ctx context.Context, cat Catalog, key dbKey, db *Database) {
	if _, dropping := m.dropping.LoadOrStore(key, struct{}{}); dropping {
		return
	}
	defer m.dropping.Delete(key)
	meta, err := cat.Get(ctx, key.tenant, key.name)
	if err != nil || meta.Incarnation != db.Incarnation() || meta.Dropping {
		return
	}
	db.SetDropping(false)
}

// DropForTenant drops a database after verifying tenant ownership (OwnedBy).
// A database of another tenant is answered with NotFoundError, exactly as a
// missing one is; the system tenant is held to the same rule.
//
// Ownership is checked without the drop fence (GetOwnedIgnoringFence): every
// other *ForTenant accessor refuses a dropping database, which is what a read
// or a config update should get, but DropForTenant must still let the OWNING
// tenant retry a drop that a restart (Open restores the fence from the catalog record)
// or a failed reclamation left fenced (go-qrdb#482).
func (m *Manager) DropForTenant(ctx context.Context, name string) error {
	if err := CheckDroppable(name); err != nil {
		return err
	}
	db, err := m.GetOwnedIgnoringFence(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}

	return m.drop(ctx, keyFor(db.TenantID(), name), db)
}

// UpdateProtocolForTenant updates protocol config after verifying tenant ownership.
func (m *Manager) UpdateProtocolForTenant(ctx context.Context, name string, protocol ProtocolConfig) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return m.updateProtocol(db, protocol)
}

// UpdateEtcdForTenant updates etcd config after verifying tenant ownership.
func (m *Manager) UpdateEtcdForTenant(ctx context.Context, name string, etcd *EtcdConfig) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return m.updateEtcd(db, etcd)
}

// UpdateRaftForTenant updates Raft config after verifying tenant ownership.
func (m *Manager) UpdateRaftForTenant(ctx context.Context, name string, raft *DatabaseRaftConfig) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return m.updateRaft(db, raft)
}

// UpdateBarrierForTenant updates barrier config after verifying tenant ownership.
func (m *Manager) UpdateBarrierForTenant(ctx context.Context, name string, barrier *DatabaseBarrierConfig) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return m.updateBarrier(db, barrier)
}

// UpdateConnectionForTenant updates connection config after verifying tenant ownership.
func (m *Manager) UpdateConnectionForTenant(ctx context.Context, name string, conn *DatabaseConnectionConfig) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return m.updateConnection(db, conn)
}

// UpdateSQLForTenant updates the SQL settings after verifying tenant
// ownership.
func (m *Manager) UpdateSQLForTenant(ctx context.Context, name string, sqlCfg *SQLConfig) error {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return err
	}
	return m.updateSQL(db, sqlCfg)
}

// CompactionOwnerForTenant returns the tenant that owns the named database
// when the context's tenant may set its scheduled-compaction policy: its own
// databases only (OwnedBy), the system tenant included -- a policy compacts
// that database's history and no one compacts another tenant's. Another
// tenant's database is answered as missing, the caller's own dropping one as
// dropping (GetOwned). The policy itself is not the catalog's: it is a
// replicated record in the database's own store (pkg/compaction).
func (m *Manager) CompactionOwnerForTenant(ctx context.Context, name string) (string, error) {
	db, err := m.GetOwned(name, tenant.FromContext(ctx))
	if err != nil {
		return "", err
	}
	return tenantid.Normalize(db.TenantID()), nil
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
// It reads the replicated catalog, so every node gives the same answer
// whichever node created the databases; a catalog it cannot read is an
// error, never an empty list, so the refusal fails closed.
//
// An unknown tenant owns no databases, which is not an error. The system
// tenant is answered like any other (OwnedBy): its databases are those
// recorded with no owner or with the system owner, matching GetForTenant's
// view.
func (m *Manager) TenantDatabases(ctx context.Context, tenantID string) ([]string, error) {
	metas, err := m.TenantRecords(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		names = append(names, meta.Name)
	}
	return names, nil
}

// TenantRecords returns tenantID's databases as the replicated catalog
// records them, sorted by name, whether or not this node has opened them: a
// database created through another node is listed before anything here
// opens it. It opens nothing; a caller that needs a database looks it up
// (GetOwned), which opens it on demand.
func (m *Manager) TenantRecords(ctx context.Context, tenantID string) ([]DatabaseMeta, error) {
	cat, err := m.catalog("list tenant databases")
	if err != nil {
		return nil, err
	}
	metas, err := cat.ListTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return metas, nil
}
