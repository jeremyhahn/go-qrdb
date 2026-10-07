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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Provider tests
// ---------------------------------------------------------------------------

func TestNewProvider(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)

	require.NotNil(t, p)
}

func TestProvider_Database_Found(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)
	require.NotNil(t, db)
	assert.Equal(t, "qrdb", db.Name())
}

func TestProvider_Database_CaseInsensitive(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	tests := []string{"QRDB", "Qrdb", "QrDb", "qRdB"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			db, err := p.Database(ctx, name)
			require.NoError(t, err)
			assert.Equal(t, "qrdb", db.Name())
		})
	}
}

func TestProvider_Database_NotFound(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	_, err := p.Database(ctx, "nonexistent")
	assert.IsType(t, &DatabaseNotFoundError{}, err)
}

func TestProvider_HasDatabase_True(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("testdb", svc)
	ctx := newTestContext()

	assert.True(t, p.HasDatabase(ctx, "testdb"))
}

func TestProvider_HasDatabase_CaseInsensitive(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("testdb", svc)
	ctx := newTestContext()

	assert.True(t, p.HasDatabase(ctx, "TESTDB"))
	assert.True(t, p.HasDatabase(ctx, "TestDb"))
}

func TestProvider_HasDatabase_False(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("testdb", svc)
	ctx := newTestContext()

	assert.False(t, p.HasDatabase(ctx, "other"))
}

func TestProvider_AllDatabases(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("mydb", svc)
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 1)
	assert.Equal(t, "mydb", dbs[0].Name())
}

// ---------------------------------------------------------------------------
// Database tests
// ---------------------------------------------------------------------------

func TestDatabase_Name(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	assert.Equal(t, "qrdb", db.Name())
}

func TestDatabase_GetTableInsensitive_KV(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "kv", tbl.Name())
}

func TestDatabase_GetTableInsensitive_CaseInsensitive(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tests := []string{"KV", "Kv", "kV"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			tbl, found, err := db.GetTableInsensitive(ctx, name)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, "kv", tbl.Name())
		})
	}
}

func TestDatabase_GetTableInsensitive_Datapoints(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "datapoints")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "datapoints", tbl.Name())
	assert.IsType(t, &DatapointsTable{}, tbl)
}

func TestDatabase_GetTableInsensitive_Metrics(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "metrics")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "metrics", tbl.Name())
	assert.IsType(t, &MetricsTable{}, tbl)
}

func TestDatabase_GetTableInsensitive_Namespaces(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "namespaces")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "namespaces", tbl.Name())
	assert.IsType(t, &NamespacesTable{}, tbl)
}

func TestDatabase_GetTableInsensitive_Vectors(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "vectors")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "vectors", tbl.Name())
	assert.IsType(t, &VectorsTable{}, tbl)
}

func TestDatabase_GetTableInsensitive_CaseInsensitive_AllTables(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tests := []struct {
		input    string
		expected string
	}{
		{"DATAPOINTS", "datapoints"},
		{"Datapoints", "datapoints"},
		{"METRICS", "metrics"},
		{"Metrics", "metrics"},
		{"NAMESPACES", "namespaces"},
		{"Namespaces", "namespaces"},
		{"VECTORS", "vectors"},
		{"Vectors", "vectors"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			tbl, found, err := db.GetTableInsensitive(ctx, tc.input)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, tc.expected, tbl.Name())
		})
	}
}

func TestDatabase_GetTableInsensitive_NotFound(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, tbl)
}

func TestDatabase_GetTableInsensitive_WithSession(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}

	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "alice",
		TenantID: "acme",
	}
	sess := NewSession(base, identity)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	kvTbl, ok := tbl.(*KVTable)
	require.True(t, ok)
	assert.Equal(t, tenant.ComposeNamespace("acme", "qrdb"), kvTbl.prefix)
}

func TestDatabase_GetTableInsensitive_WithoutSession(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	kvTbl, ok := tbl.(*KVTable)
	require.True(t, ok)
	assert.Equal(t, tenant.ComposeNamespace("system", "qrdb"), kvTbl.prefix)
}

func TestDatabase_GetTableNames(t *testing.T) {
	db := &Database{name: "qrdb", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	names, err := db.GetTableNames(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"datapoints", "kv", "metrics", "namespaces", "vectors"}, names)
}

// ---------------------------------------------------------------------------
// Interface compliance tests
// ---------------------------------------------------------------------------

func TestProvider_ImplementsDatabaseProvider(t *testing.T) {
	var _ sqle.DatabaseProvider = (*Provider)(nil)
}

func TestDatabase_ImplementsDatabase(t *testing.T) {
	var _ sqle.Database = (*Database)(nil)
}

// ---------------------------------------------------------------------------
// Context helper
// ---------------------------------------------------------------------------

func TestProvider_Database_WithSession_TenantScoping(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)

	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "bob",
		TenantID: "corp",
	}
	sess := NewSession(base, identity)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	kvTbl, ok := tbl.(*KVTable)
	require.True(t, ok)
	assert.Equal(t, tenant.ComposeNamespace("corp", "qrdb"), kvTbl.prefix)
}

// ---------------------------------------------------------------------------
// WithReadOnly / WithPrescopedKV tests
// ---------------------------------------------------------------------------

// TestProvider_WithReadOnly_StripsWriteCapability verifies that a Provider
// constructed with WithReadOnly serves tables with no write-capability
// interfaces reachable, regardless of what the underlying concrete table
// implements.
func TestProvider_WithReadOnly_StripsWriteCapability(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc, WithReadOnly())
	ctx := newTestContext()

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	_, ok := tbl.(sqle.InsertableTable)
	assert.False(t, ok, "read-only provider must not serve an InsertableTable")
	_, ok = tbl.(sqle.DeletableTable)
	assert.False(t, ok, "read-only provider must not serve a DeletableTable")
}

// TestProvider_WithoutReadOnly_PreservesWriteCapability verifies the
// default (no WithReadOnly) behavior is unchanged: served tables keep
// their write-capability interfaces.
func TestProvider_WithoutReadOnly_PreservesWriteCapability(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	_, ok := tbl.(sqle.InsertableTable)
	assert.True(t, ok, "default provider must serve an InsertableTable")
}

// TestProvider_WithPrescopedKV_UsesBareKeys verifies that a Provider
// constructed with WithPrescopedKV composes no additional namespace prefix
// on top of kvSvc, even when the session carries a non-default tenant —
// the caller (e.g. the ad-hoc SQL query service backed by
// service.Registry.KVService) is responsible for having already scoped
// kvSvc to the correct (tenant, database) namespace.
func TestProvider_WithPrescopedKV_UsesBareKeys(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc, WithPrescopedKV())

	base := sqle.NewBaseSession()
	sess := NewSession(base, &auth.Identity{Subject: "bob", TenantID: "corp"})
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	kvTbl, ok := tbl.(*KVTable)
	require.True(t, ok)
	assert.Equal(t, "", kvTbl.prefix, "prescoped provider must not add a namespace prefix")
}

// ---------------------------------------------------------------------------
// NewManagerProvider tests
// ---------------------------------------------------------------------------

// newTestManager creates a database.Manager with a temp directory for testing.
func newTestManager(t *testing.T) *database.Manager {
	t.Helper()
	dir := t.TempDir()
	return database.NewManager(nil, dir)
}

func TestNewManagerProvider(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)

	require.NotNil(t, p)
	assert.NotNil(t, p.manager)
	assert.NotNil(t, p.kvSvc)
}

func TestNewManagerProvider_NilManager(t *testing.T) {
	svc := &mockKVService{}
	p := NewManagerProvider(nil, svc)

	require.NotNil(t, p)
	assert.Nil(t, p.manager)
}

// ---------------------------------------------------------------------------
// Manager-backed Provider.Database() tests
// ---------------------------------------------------------------------------

func TestManagerProvider_Database_Found(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "testdb",
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	db, err := p.Database(ctx, "testdb")
	require.NoError(t, err)
	require.NotNil(t, db)
	assert.Equal(t, "testdb", db.Name())
}

func TestManagerProvider_Database_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	_, err := p.Database(ctx, "nonexistent")
	assert.IsType(t, &DatabaseNotFoundError{}, err)
}

func TestManagerProvider_Database_KVTableAccess(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "mydb",
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	sqlDB, err := p.Database(ctx, "mydb")
	require.NoError(t, err)

	tbl, found, err := sqlDB.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "kv", tbl.Name())
}

// ---------------------------------------------------------------------------
// Manager-backed Provider.HasDatabase() tests
// ---------------------------------------------------------------------------

func TestManagerProvider_HasDatabase_True(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "exists",
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	assert.True(t, p.HasDatabase(ctx, "exists"))
}

func TestManagerProvider_HasDatabase_False(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	assert.False(t, p.HasDatabase(ctx, "nosuchdb"))
}

// ---------------------------------------------------------------------------
// Manager-backed Provider.AllDatabases() tests
// ---------------------------------------------------------------------------

func TestManagerProvider_AllDatabases_Empty(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	assert.Empty(t, dbs)
}

func TestManagerProvider_AllDatabases_Multiple(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	for _, name := range []string{"alpha", "beta", "gamma"} {
		_, err := mgr.Create(database.DatabaseConfig{
			Name:    name,
			Backend: database.BackendMemory,
		})
		require.NoError(t, err)
	}

	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 3)

	// Manager.List() returns sorted by name.
	assert.Equal(t, "alpha", dbs[0].Name())
	assert.Equal(t, "beta", dbs[1].Name())
	assert.Equal(t, "gamma", dbs[2].Name())
}

// ---------------------------------------------------------------------------
// CreateDatabase tests
// ---------------------------------------------------------------------------

func TestManagerProvider_CreateDatabase(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	err := p.CreateDatabase(ctx, "newdb")
	require.NoError(t, err)

	// Verify the database was created.
	assert.True(t, p.HasDatabase(ctx, "newdb"))

	db, err := p.Database(ctx, "newdb")
	require.NoError(t, err)
	assert.Equal(t, "newdb", db.Name())

	_, ok := db.(*Database)
	require.True(t, ok)
}

func TestManagerProvider_CreateDatabase_Duplicate(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	err := p.CreateDatabase(ctx, "dupdb")
	require.NoError(t, err)

	err = p.CreateDatabase(ctx, "dupdb")
	assert.IsType(t, &CreateDatabaseError{}, err)
}

func TestManagerProvider_CreateDatabase_InvalidName(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	err := p.CreateDatabase(ctx, "invalid name with spaces")
	assert.IsType(t, &CreateDatabaseError{}, err)
}

func TestLegacyProvider_CreateDatabase_NotSupported(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	err := p.CreateDatabase(ctx, "newdb")
	assert.IsType(t, &MutableOperationNotSupportedError{}, err)
}

// ---------------------------------------------------------------------------
// DropDatabase tests
// ---------------------------------------------------------------------------

func TestManagerProvider_DropDatabase(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	err := p.CreateDatabase(ctx, "dropme")
	require.NoError(t, err)
	assert.True(t, p.HasDatabase(ctx, "dropme"))

	err = p.DropDatabase(ctx, "dropme")
	require.NoError(t, err)
	assert.False(t, p.HasDatabase(ctx, "dropme"))
}

func TestManagerProvider_DropDatabase_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	err := p.DropDatabase(ctx, "nonexistent")
	assert.IsType(t, &DropDatabaseError{}, err)
}

func TestLegacyProvider_DropDatabase_NotSupported(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	err := p.DropDatabase(ctx, "qrdb")
	assert.IsType(t, &MutableOperationNotSupportedError{}, err)
}

// ---------------------------------------------------------------------------
// Database.Engine() tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Interface compliance tests (manager-backed)
// ---------------------------------------------------------------------------

func TestProvider_ImplementsMutableDatabaseProvider(t *testing.T) {
	var _ sqle.MutableDatabaseProvider = (*Provider)(nil)
}

// ---------------------------------------------------------------------------
// Legacy backward compatibility tests
// ---------------------------------------------------------------------------

func TestLegacyProvider_ManagerIsNil(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)

	assert.Nil(t, p.manager)
	assert.Equal(t, "qrdb", p.dbName)
}

func TestLegacyProvider_StillWorksSameAsBefore(t *testing.T) {
	svc := &mockKVService{}
	p := NewProvider("qrdb", svc)
	ctx := newTestContext()

	// Database lookup works.
	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)
	assert.Equal(t, "qrdb", db.Name())

	// HasDatabase works.
	assert.True(t, p.HasDatabase(ctx, "qrdb"))
	assert.False(t, p.HasDatabase(ctx, "other"))

	// AllDatabases works.
	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 1)
	assert.Equal(t, "qrdb", dbs[0].Name())

	// Table access works.
	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "kv", tbl.Name())

	// Table names works.
	names, err := db.GetTableNames(ctx)
	require.NoError(t, err)
	assert.Contains(t, names, "kv")
}

// ---------------------------------------------------------------------------
// Manager-backed Provider with session tests
// ---------------------------------------------------------------------------

func TestManagerProvider_Database_WithSession_TenantScoping(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		TenantID: "acme",
		Name:     "tenantdb",
		Backend:  database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc)

	base := sqle.NewBaseSession()
	identity := &auth.Identity{
		Subject:  "alice",
		TenantID: "acme",
	}
	sess := NewSession(base, identity)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))

	db, err := p.Database(ctx, "tenantdb")
	require.NoError(t, err)

	tbl, found, err := db.GetTableInsensitive(ctx, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	kvTbl, ok := tbl.(*KVTable)
	require.True(t, ok)
	assert.Equal(t, tenant.ComposeNamespace("acme", "tenantdb"), kvTbl.prefix)
}

// ---------------------------------------------------------------------------
// CreateDatabase + AllDatabases round-trip test
// ---------------------------------------------------------------------------

func TestManagerProvider_CreateDatabase_ShowsInAllDatabases(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)
	ctx := newTestContext()

	// Start empty.
	assert.Empty(t, p.AllDatabases(ctx))

	// Create two databases.
	require.NoError(t, p.CreateDatabase(ctx, "db1"))
	require.NoError(t, p.CreateDatabase(ctx, "db2"))

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 2)
	assert.Equal(t, "db1", dbs[0].Name())
	assert.Equal(t, "db2", dbs[1].Name())

	// Drop one.
	require.NoError(t, p.DropDatabase(ctx, "db1"))

	dbs = p.AllDatabases(ctx)
	require.Len(t, dbs, 1)
	assert.Equal(t, "db2", dbs[0].Name())
}

// ---------------------------------------------------------------------------
// ProtocolType constants tests
// ---------------------------------------------------------------------------

func TestProtocolType_Constants(t *testing.T) {
	assert.Equal(t, ProtocolType(""), ProtocolAny)
	assert.Equal(t, ProtocolType("mysql"), ProtocolMySQL)
	assert.Equal(t, ProtocolType("postgresql"), ProtocolPostgreSQL)
}

// ---------------------------------------------------------------------------
// Engine-to-tables map tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// WithProtocol option tests
// ---------------------------------------------------------------------------

func TestWithProtocol_MySQL(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))

	assert.Equal(t, ProtocolMySQL, p.protocol)
}

func TestWithProtocol_PostgreSQL(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolPostgreSQL))

	assert.Equal(t, ProtocolPostgreSQL, p.protocol)
}

func TestWithProtocol_Any(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolAny))

	assert.Equal(t, ProtocolAny, p.protocol)
}

func TestNewManagerProvider_NoOptions(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}
	p := NewManagerProvider(mgr, svc)

	assert.Equal(t, ProtocolAny, p.protocol)
}

// ---------------------------------------------------------------------------
// NewProviderWithEngine tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Engine-scoped GetTableNames tests
// ---------------------------------------------------------------------------

func TestDatabase_GetTableNames_LegacyAllTables(t *testing.T) {
	db := &Database{name: "legacy", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	names, err := db.GetTableNames(ctx)
	require.NoError(t, err)
	assert.Equal(t, allTableNames, names)
	assert.Len(t, names, 5)
}

func TestDatabase_GetTableInsensitive_Legacy_AllTablesAccessible(t *testing.T) {
	db := &Database{name: "legacy", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	for _, name := range allTableNames {
		tbl, found, err := db.GetTableInsensitive(ctx, name)
		require.NoError(t, err, "table: %s", name)
		assert.True(t, found, "table: %s", name)
		assert.NotNil(t, tbl, "table: %s", name)
	}
}

// ---------------------------------------------------------------------------
// Protocol filtering tests
// ---------------------------------------------------------------------------

func TestManagerProvider_ProtocolMySQL_AllowsMySQLDatabase(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "mysqldb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: false},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	db, err := p.Database(ctx, "mysqldb")
	require.NoError(t, err)
	assert.Equal(t, "mysqldb", db.Name())
}

func TestManagerProvider_ProtocolMySQL_RejectsPostgreSQLOnly(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "pgdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: false, PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	_, err = p.Database(ctx, "pgdb")
	assert.IsType(t, &DatabaseNotFoundError{}, err)
}

func TestManagerProvider_ProtocolPostgreSQL_AllowsPostgreSQLDatabase(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "pgdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: false, PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolPostgreSQL))
	ctx := newTestContext()

	db, err := p.Database(ctx, "pgdb")
	require.NoError(t, err)
	assert.Equal(t, "pgdb", db.Name())
}

func TestManagerProvider_ProtocolPostgreSQL_RejectsMySQLOnly(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "mysqldb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: false},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolPostgreSQL))
	ctx := newTestContext()

	_, err = p.Database(ctx, "mysqldb")
	assert.IsType(t, &DatabaseNotFoundError{}, err)
}

func TestManagerProvider_ProtocolAny_AllowsAllDatabases(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "nodb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: false, PostgreSQL: false},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolAny))
	ctx := newTestContext()

	db, err := p.Database(ctx, "nodb")
	require.NoError(t, err)
	assert.Equal(t, "nodb", db.Name())
}

func TestManagerProvider_ProtocolMySQL_RejectsNoProtocolConfig(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "nodb",
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	_, err = p.Database(ctx, "nodb")
	assert.IsType(t, &DatabaseNotFoundError{}, err)
}

// ---------------------------------------------------------------------------
// Protocol filtering - AllDatabases tests
// ---------------------------------------------------------------------------

func TestManagerProvider_AllDatabases_ProtocolMySQL_FiltersCorrectly(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "mysqldb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: false},
	})
	require.NoError(t, err)

	_, err = mgr.Create(database.DatabaseConfig{
		Name:     "pgdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: false, PostgreSQL: true},
	})
	require.NoError(t, err)

	_, err = mgr.Create(database.DatabaseConfig{
		Name:     "bothdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 2)

	names := make([]string, len(dbs))
	for i, db := range dbs {
		names[i] = db.Name()
	}
	assert.Contains(t, names, "mysqldb")
	assert.Contains(t, names, "bothdb")
	assert.NotContains(t, names, "pgdb")
}

func TestManagerProvider_AllDatabases_ProtocolPostgreSQL_FiltersCorrectly(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "mysqldb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: false},
	})
	require.NoError(t, err)

	_, err = mgr.Create(database.DatabaseConfig{
		Name:     "pgdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: false, PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolPostgreSQL))
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 1)
	assert.Equal(t, "pgdb", dbs[0].Name())
}

func TestManagerProvider_AllDatabases_ProtocolAny_ReturnsAll(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "db1",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true},
	})
	require.NoError(t, err)

	_, err = mgr.Create(database.DatabaseConfig{
		Name:    "db2",
		Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc) // default: ProtocolAny
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	assert.Len(t, dbs, 2)
}

func TestManagerProvider_AllDatabases_ProtocolMySQL_EmptyWhenNoneMatch(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "pgonly",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	assert.Empty(t, dbs)
}

// ---------------------------------------------------------------------------
// Database.hasTable tests
// ---------------------------------------------------------------------------

func TestDatabase_hasTable_Legacy_AllAllowed(t *testing.T) {
	db := &Database{name: "legacy", kvSvc: &mockKVService{}}
	for _, name := range allTableNames {
		assert.True(t, db.hasTable(name), "table: %s", name)
	}
}

// ---------------------------------------------------------------------------
// Protocol filtering with both enabled
// ---------------------------------------------------------------------------

func TestManagerProvider_ProtocolMySQL_BothEnabled_Allowed(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "bothdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolMySQL))
	ctx := newTestContext()

	db, err := p.Database(ctx, "bothdb")
	require.NoError(t, err)
	assert.Equal(t, "bothdb", db.Name())
}

func TestManagerProvider_ProtocolPostgreSQL_BothEnabled_Allowed(t *testing.T) {
	mgr := newTestManager(t)
	svc := &mockKVService{}

	_, err := mgr.Create(database.DatabaseConfig{
		Name:     "bothdb",
		Backend:  database.BackendMemory,
		Protocol: database.ProtocolConfig{MySQL: true, PostgreSQL: true},
	})
	require.NoError(t, err)

	p := NewManagerProvider(mgr, svc, WithProtocol(ProtocolPostgreSQL))
	ctx := newTestContext()

	db, err := p.Database(ctx, "bothdb")
	require.NoError(t, err)
	assert.Equal(t, "bothdb", db.Name())
}
