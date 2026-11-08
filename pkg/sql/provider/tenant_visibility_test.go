//go:build qrdb_sql

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

package provider

import (
	"context"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestAllDatabasesIsScopedToTheSessionTenant pins the isolation guarantee of
// the SQL catalog surface: the database manager is node-local and holds every
// tenant's databases side by side, so an unfiltered enumeration hands one
// tenant the names of every other tenant's databases -- and, through
// pg_catalog.pg_class, their tables.
//
// Data access was never reachable this way, because service.Registry resolves
// every read and write against the session's own tenant and a cross-tenant
// name simply is not found. The names were, via SHOW DATABASES,
// information_schema, and pg_catalog.pg_namespace, all of which enumerate
// exactly this method.
func TestAllDatabasesIsScopedToTheSessionTenant(t *testing.T) {
	mgr := newTestManager(t)

	created := []struct {
		name   string
		tenant string
	}{
		{"acme_orders", "acme"},
		{"acme_billing", "acme"},
		{"globex_orders", "globex"},
		{"bootstrap", ""}, // no TenantID: a node-level database, system-owned
	}
	for _, c := range created {
		_, err := mgr.Create(database.DatabaseConfig{
			Name:     c.name,
			TenantID: c.tenant,
			Backend:  database.BackendMemory,
		})
		require.NoError(t, err)
	}

	p := NewManagerProvider(mgr, &mockKVService{})

	tests := []struct {
		name   string
		tenant string
		want   []string
	}{
		{
			name:   "a tenant sees only its own databases",
			tenant: "acme",
			want:   []string{"acme_billing", "acme_orders"},
		},
		{
			name:   "another tenant sees only its own",
			tenant: "globex",
			want:   []string{"globex_orders"},
		},
		{
			// An untagged database is system-owned, not ownerless and not
			// everyone's; the system tenant is the only one that sees it.
			name:   "the system tenant owns the untagged bootstrap database",
			tenant: tenantid.System,
			want:   []string{"bootstrap"},
		},
		{
			name:   "an unknown tenant sees nothing",
			tenant: "nobody",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names := databaseNames(p.AllDatabases(tenantContext(tt.tenant)))
			assert.Equal(t, tt.want, names)

			for _, other := range created {
				owner := other.tenant
				if owner == "" {
					owner = tenantid.System
				}
				if owner == tt.tenant {
					continue
				}
				assert.NotContains(t, names, other.name,
					"tenant %q must not learn that %q exists", tt.tenant, other.name)
			}
		})
	}
}

// TestAllDatabasesWithoutASessionIsSystemScoped covers the fallback: a context
// carrying no QRDB session is the system tenant, the same reading
// Database.sessionTenantID applies at the other end of the comparison. It must
// not mean "every database", which would restore the disclosure for any code
// path that reaches the provider without a session.
func TestAllDatabasesWithoutASessionIsSystemScoped(t *testing.T) {
	mgr := newTestManager(t)
	for _, c := range []struct{ name, tenantID string }{
		{"acme_orders", "acme"},
		{"bootstrap", ""},
	} {
		_, err := mgr.Create(database.DatabaseConfig{
			Name:     c.name,
			TenantID: c.tenantID,
			Backend:  database.BackendMemory,
		})
		require.NoError(t, err)
	}

	p := NewManagerProvider(mgr, &mockKVService{})

	assert.Equal(t, []string{"bootstrap"}, databaseNames(p.AllDatabases(newTestContext())))
	assert.Equal(t, []string{"bootstrap"}, databaseNames(p.AllDatabases(nil)))
}

// TestSessionLookupsReachTheSessionTenantsOwnDatabase pins that two tenants
// holding a database of one name each reach their own over SQL. The name
// alone is ambiguous once two tenants own it, so every session lookup
// resolves the session's (tenant, name) pair: the database itself, its SQL
// settings and its place in the catalog listing.
func TestSessionLookupsReachTheSessionTenantsOwnDatabase(t *testing.T) {
	mgr := newTestManager(t)
	owners := []struct {
		tenant string
		sql    *database.SQLConfig
	}{
		{"acme", &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic}},
		{"globex", &database.SQLConfig{Concurrency: database.ConcurrencyOptimistic}},
	}
	for _, o := range owners {
		_, err := mgr.Create(database.DatabaseConfig{
			Name: "orders", TenantID: o.tenant, Backend: database.BackendMemory, SQL: o.sql,
		})
		require.NoError(t, err)
	}
	p := NewManagerProvider(mgr, &mockKVService{})

	for _, o := range owners {
		t.Run(o.tenant, func(t *testing.T) {
			ctx := tenantContext(o.tenant)
			db, ok := p.sessionDatabase(ctx, "orders")
			require.True(t, ok)
			assert.Equal(t, o.tenant, db.TenantID())
			assert.True(t, p.HasDatabase(ctx, "orders"))
			_, err := p.Database(ctx, "orders")
			require.NoError(t, err)
			assert.Equal(t, o.sql.EffectiveConcurrency(),
				p.managerSQLConfig(o.tenant, "orders").EffectiveConcurrency())
			assert.Equal(t, []string{"orders"}, databaseNames(p.AllDatabases(ctx)))
		})
	}

	stranger := tenantContext("initech")
	assert.False(t, p.HasDatabase(stranger, "orders"))
	assert.Nil(t, p.managerSQLConfig("initech", "orders"))
	assert.Empty(t, databaseNames(p.AllDatabases(stranger)))
}

// TestSharedDefaultDatabaseFallsBackToTheSystemTenant pins the shared
// default's resolution: a tenant that owns no database of that name reaches
// the system tenant's, and a tenant that owns one reaches its own -- once,
// in the listing, never beside the system tenant's.
func TestSharedDefaultDatabaseFallsBackToTheSystemTenant(t *testing.T) {
	mgr := newTestManager(t)
	_, err := mgr.Create(database.DatabaseConfig{
		Name: "default", Backend: database.BackendMemory,
		SQL: &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic},
	})
	require.NoError(t, err)
	_, err = mgr.Create(database.DatabaseConfig{
		Name: "default", TenantID: "acme", Backend: database.BackendMemory,
		SQL: &database.SQLConfig{Concurrency: database.ConcurrencyOptimistic},
	})
	require.NoError(t, err)
	p := NewManagerProvider(mgr, &mockKVService{}, WithSharedDefaultDatabase("default"))

	db, ok := p.sessionDatabase(tenantContext("globex"), "default")
	require.True(t, ok)
	assert.Equal(t, tenantid.System, tenantid.Normalize(db.TenantID()))
	assert.Equal(t, database.ConcurrencyPessimistic,
		p.managerSQLConfig("globex", "default").EffectiveConcurrency())

	db, ok = p.sessionDatabase(tenantContext("acme"), "default")
	require.True(t, ok)
	assert.Equal(t, "acme", db.TenantID())
	assert.Equal(t, database.ConcurrencyOptimistic,
		p.managerSQLConfig("acme", "default").EffectiveConcurrency())
	assert.Equal(t, []string{"default"}, databaseNames(p.AllDatabases(tenantContext("acme"))))

	_, ok = p.sessionDatabase(tenantContext("globex"), "orders")
	assert.False(t, ok, "only the shared default falls back to the system tenant")
}

// tenantContext builds a *sqle.Context whose session is a QRDBSession
// authenticated to the named tenant.
func tenantContext(tenantID string) *sqle.Context {
	session := NewSession(sqle.NewBaseSession(), &auth.Identity{TenantID: tenantID})
	return sqle.NewContext(context.Background(), sqle.WithSession(session))
}

// databaseNames extracts and returns the names of dbs, or nil when empty, so
// that assertions read against a plain literal.
func databaseNames(dbs []sqle.Database) []string {
	if len(dbs) == 0 {
		return nil
	}
	names := make([]string, 0, len(dbs))
	for _, db := range dbs {
		names = append(names, db.Name())
	}
	return names
}
