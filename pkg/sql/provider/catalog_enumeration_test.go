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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
)

// twoNodes returns two Managers over one replicated catalog: a database
// created through the first is recorded for the second, which has not
// opened it -- the state of a node after another node served a CREATE.
func twoNodes(t *testing.T) (creator, lister *database.Manager) {
	t.Helper()
	dir := t.TempDir()
	return databasetest.NewManager(nil, dir), databasetest.NewManager(nil, dir)
}

func TestAllDatabasesListsARecordNotOpenOnThisNode(t *testing.T) {
	creator, lister := twoNodes(t)
	for _, name := range []string{"orders", "billing"} {
		_, err := creator.Create(database.DatabaseConfig{
			Name: name, TenantID: "acme", Backend: database.BackendMemory,
		})
		require.NoError(t, err)
	}
	_, open := lister.Local("orders", "acme")
	require.False(t, open, "the listing node must not have opened the database")

	p := NewManagerProvider(lister, &mockKVService{})
	assert.Equal(t, []string{"billing", "orders"}, databaseNames(p.AllDatabases(tenantContext("acme"))))
	assert.Empty(t, databaseNames(p.AllDatabases(tenantContext("globex"))))

	_, open = lister.Local("orders", "acme")
	assert.False(t, open, "enumeration reads the catalog; it does not open what it lists")
}

func TestAllDatabasesFiltersARecordNotOpenHereByProtocol(t *testing.T) {
	creator, lister := twoNodes(t)
	_, err := creator.Create(database.DatabaseConfig{
		Name: "my", TenantID: "acme", Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true},
	})
	require.NoError(t, err)
	_, err = creator.Create(database.DatabaseConfig{
		Name: "pg", TenantID: "acme", Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{PostgreSQL: true},
	})
	require.NoError(t, err)

	mysql := NewManagerProvider(lister, &mockKVService{}, WithProtocol(ProtocolMySQL))
	assert.Equal(t, []string{"my"}, databaseNames(mysql.AllDatabases(tenantContext("acme"))))
	pg := NewManagerProvider(lister, &mockKVService{}, WithProtocol(ProtocolPostgreSQL))
	assert.Equal(t, []string{"pg"}, databaseNames(pg.AllDatabases(tenantContext("acme"))))
}

func TestAllDatabasesTenantRecordShadowsTheSharedDefaultWhenNotOpenHere(t *testing.T) {
	creator, lister := twoNodes(t)
	_, err := creator.Create(database.DatabaseConfig{
		Name: "default", Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true},
	})
	require.NoError(t, err)
	_, err = creator.Create(database.DatabaseConfig{
		Name: "default", TenantID: "acme", Backend: database.BackendMemory,
		Protocol: database.ProtocolConfig{PostgreSQL: true},
	})
	require.NoError(t, err)
	// The listing node has the shared default open, not acme's own.
	_, err = lister.GetOwned("default", "")
	require.NoError(t, err)

	mysql := NewManagerProvider(lister, &mockKVService{},
		WithSharedDefaultDatabase("default"), WithProtocol(ProtocolMySQL))
	assert.Empty(t, databaseNames(mysql.AllDatabases(tenantContext("acme"))),
		"acme's own default, which MySQL does not serve, replaces the shared one")
	assert.Equal(t, []string{"default"}, databaseNames(mysql.AllDatabases(tenantContext("globex"))),
		"a tenant without its own default sees the shared one")

	pg := NewManagerProvider(lister, &mockKVService{},
		WithSharedDefaultDatabase("default"), WithProtocol(ProtocolPostgreSQL))
	assert.Equal(t, []string{"default"}, databaseNames(pg.AllDatabases(tenantContext("acme"))))
	assert.Empty(t, databaseNames(pg.AllDatabases(tenantContext("globex"))))
}

func TestAllDatabasesOmitsADatabaseBeingDropped(t *testing.T) {
	dir := t.TempDir()
	creator, lister := databasetest.NewManager(nil, dir), databasetest.NewManager(nil, dir)
	for _, name := range []string{"orders", "billing"} {
		_, err := creator.Create(database.DatabaseConfig{
			Name: name, TenantID: "acme", Backend: database.BackendMemory,
		})
		require.NoError(t, err)
	}
	cat := databasetest.Catalog(dir)
	meta, err := cat.Get(context.Background(), "acme", "orders")
	require.NoError(t, err)
	require.NoError(t, cat.Update(context.Background(), "acme", "orders", meta.Incarnation,
		func(m *database.DatabaseMeta) error {
			m.Dropping = true
			return nil
		}))

	p := NewManagerProvider(lister, &mockKVService{})
	assert.Equal(t, []string{"billing"}, databaseNames(p.AllDatabases(tenantContext("acme"))),
		"a fenced database fails every lookup, so it is not listed")
}

// failingListCatalog is a catalog whose tenant listing fails.
type failingListCatalog struct {
	database.Catalog
	err error
}

func (c failingListCatalog) ListTenant(context.Context, string) ([]database.DatabaseMeta, error) {
	return nil, c.err
}

func TestAllDatabasesSurfacesACatalogReadError(t *testing.T) {
	mgr := newTestManager(t)
	readErr := &database.CatalogRequiredError{Operation: "scan"}
	mgr.SetCatalog(failingListCatalog{Catalog: databasetest.Catalog(t.TempDir()), err: readErr})
	p := NewManagerProvider(mgr, &mockKVService{}, WithSharedDefaultDatabase("default"))

	ctx := tenantContext("acme")
	assert.Empty(t, p.AllDatabases(ctx))
	warnings := ctx.Session.Warnings()
	require.Len(t, warnings, 1)
	assert.Equal(t, "Error", warnings[0].Level)
	assert.Contains(t, warnings[0].Message, readErr.Error())
}

func TestAllDatabasesWithoutACatalogSurfacesTheError(t *testing.T) {
	mgr := newTestManager(t)
	mgr.SetCatalog(nil)
	p := NewManagerProvider(mgr, &mockKVService{})

	ctx := tenantContext("acme")
	assert.Empty(t, p.AllDatabases(ctx))
	warnings := ctx.Session.Warnings()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Message, "list tenant databases")
}
