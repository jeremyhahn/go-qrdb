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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ddlCapableDatabase is a stub sql.Database that also implements every
// DDL-capability interface readOnlyDatabase is meant to hide. It exists so the
// structural guarantee can be asserted directly, independently of which of
// those interfaces the production Database happens to implement today — the
// assertions below therefore keep their meaning as DDL support lands.
type ddlCapableDatabase struct {
	name          string
	tableNames    []string
	createCalled  bool
	dropCalled    bool
	renameCalled  bool
	indexedCalled bool
}

var (
	_ sqle.Database            = (*ddlCapableDatabase)(nil)
	_ sqle.TableCreator        = (*ddlCapableDatabase)(nil)
	_ sqle.IndexedTableCreator = (*ddlCapableDatabase)(nil)
	_ sqle.TableDropper        = (*ddlCapableDatabase)(nil)
	_ sqle.TableRenamer        = (*ddlCapableDatabase)(nil)
)

func (d *ddlCapableDatabase) Name() string { return d.name }

func (d *ddlCapableDatabase) GetTableInsensitive(_ *sqle.Context, name string) (sqle.Table, bool, error) {
	for _, n := range d.tableNames {
		if n == name {
			return nil, true, nil
		}
	}
	return nil, false, nil
}

func (d *ddlCapableDatabase) GetTableNames(_ *sqle.Context) ([]string, error) {
	return d.tableNames, nil
}

func (d *ddlCapableDatabase) CreateTable(_ *sqle.Context, _ string, _ sqle.PrimaryKeySchema, _ sqle.CollationID, _ string) error {
	d.createCalled = true
	return nil
}

func (d *ddlCapableDatabase) CreateIndexedTable(_ *sqle.Context, _ string, _ sqle.PrimaryKeySchema, _ sqle.IndexDef, _ sqle.CollationID, _ string) error {
	d.indexedCalled = true
	return nil
}

func (d *ddlCapableDatabase) DropTable(_ *sqle.Context, _ string) error {
	d.dropCalled = true
	return nil
}

func (d *ddlCapableDatabase) RenameTable(_ *sqle.Context, _, _ string) error {
	d.renameCalled = true
	return nil
}

// TestReadOnlyDatabase_HidesDDLInterfaces is the security-critical assertion:
// wrapping a DDL-capable database must make every DDL interface structurally
// unreachable, so go-mysql-server's analyzer cannot route CREATE/DROP/RENAME
// TABLE to it.
func TestReadOnlyDatabase_HidesDDLInterfaces(t *testing.T) {
	inner := &ddlCapableDatabase{name: "qrdb"}

	// Sanity check: the stub really does implement these, so a passing
	// assertion below reflects the wrapper and not a hollow stub.
	_, ok := sqle.Database(inner).(sqle.TableCreator)
	require.True(t, ok, "stub must implement TableCreator for this test to mean anything")

	wrapped := newReadOnlyDatabase(inner)

	_, ok = wrapped.(sqle.TableCreator)
	assert.False(t, ok, "read-only database must not expose sql.TableCreator")
	_, ok = wrapped.(sqle.IndexedTableCreator)
	assert.False(t, ok, "read-only database must not expose sql.IndexedTableCreator")
	_, ok = wrapped.(sqle.TableDropper)
	assert.False(t, ok, "read-only database must not expose sql.TableDropper")
	_, ok = wrapped.(sqle.TableRenamer)
	assert.False(t, ok, "read-only database must not expose sql.TableRenamer")

	// The wrapper must not have reached any of them either.
	assert.False(t, inner.createCalled)
	assert.False(t, inner.dropCalled)
	assert.False(t, inner.renameCalled)
	assert.False(t, inner.indexedCalled)
}

// TestReadOnlyDatabase_HidesRelationalDDLInterfaces is the same
// security-critical assertion as TestReadOnlyDatabase_HidesDDLInterfaces,
// but against the real, concrete *Database (with a working registry) rather
// than a hand-written stub: this is the actual production type the ad-hoc
// SQL query surface (POST /sql/query, and the QUIC/Unix/MCP equivalents,
// all authorized as permission.KVRange -- a read permission) serves once
// wrapped read-only. It also covers CollatedDatabase and SchemaValidator,
// added by Phase 5, which TestReadOnlyDatabase_HidesDDLInterfaces's stub
// predates.
func TestReadOnlyDatabase_HidesRelationalDDLInterfaces(t *testing.T) {
	h := newDDLHarness()
	inner := h.newDDLDatabase("shop")

	// Sanity check: the concrete type really does implement every DDL
	// interface, so the assertions below reflect the wrapper and not a
	// database that never had the capability in the first place.
	var _ sqle.TableCreator = inner
	var _ sqle.IndexedTableCreator = inner
	var _ sqle.TableDropper = inner
	var _ sqle.TableRenamer = inner
	var _ sqle.CollatedDatabase = inner
	var _ sqle.SchemaValidator = inner

	wrapped := newReadOnlyDatabase(inner)

	_, ok := wrapped.(sqle.TableCreator)
	assert.False(t, ok, "read-only database must not expose sql.TableCreator")
	_, ok = wrapped.(sqle.IndexedTableCreator)
	assert.False(t, ok, "read-only database must not expose sql.IndexedTableCreator")
	_, ok = wrapped.(sqle.TableDropper)
	assert.False(t, ok, "read-only database must not expose sql.TableDropper")
	_, ok = wrapped.(sqle.TableRenamer)
	assert.False(t, ok, "read-only database must not expose sql.TableRenamer")
	_, ok = wrapped.(sqle.CollatedDatabase)
	assert.False(t, ok, "read-only database must not expose sql.CollatedDatabase")
	_, ok = wrapped.(sqle.SchemaValidator)
	assert.False(t, ok, "read-only database must not expose sql.SchemaValidator")
}

// TestProvider_WithReadOnly_RelationalTable_HidesDDLInterfaces verifies the
// same structural guarantee end to end through the Provider: a relational
// table resolved from a read-only Provider must not expose any of
// RelationalTable's DDL capability interfaces (AlterableTable,
// IndexAlterableTable, IndexBuildingTable, RewritableTable), even though
// the concrete *RelationalTable genuinely implements all of them.
func TestProvider_WithReadOnly_RelationalTable_HidesDDLInterfaces(t *testing.T) {
	h := newDDLHarness()
	_, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users",
		Columns: []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
	})
	require.NoError(t, err)

	p := NewProviderWithEngine("shop", h.kvSvc, "", WithRegistry(h.registry), WithReadOnly())
	sqlDB, err := p.Database(newTestContext(), "shop")
	require.NoError(t, err)

	tbl, found, err := sqlDB.GetTableInsensitive(newTestContext(), "users")
	require.NoError(t, err)
	require.True(t, found)

	_, ok := tbl.(sqle.AlterableTable)
	assert.False(t, ok, "read-only relational table must not expose sql.AlterableTable")
	_, ok = tbl.(sqle.IndexAlterableTable)
	assert.False(t, ok, "read-only relational table must not expose sql.IndexAlterableTable")
	_, ok = tbl.(sqle.IndexBuildingTable)
	assert.False(t, ok, "read-only relational table must not expose sql.IndexBuildingTable")
	_, ok = tbl.(sqle.RewritableTable)
	assert.False(t, ok, "read-only relational table must not expose sql.RewritableTable")
}

// TestReadOnlyDatabase_PreservesReadSurface verifies the wrapper is
// transparent for the sql.Database surface it is meant to pass through.
func TestReadOnlyDatabase_PreservesReadSurface(t *testing.T) {
	inner := &ddlCapableDatabase{name: "qrdb", tableNames: []string{"kv", "metrics"}}
	wrapped := newReadOnlyDatabase(inner)

	assert.Equal(t, "qrdb", wrapped.Name())

	names, err := wrapped.GetTableNames(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"kv", "metrics"}, names)

	_, found, err := wrapped.GetTableInsensitive(nil, "kv")
	require.NoError(t, err)
	assert.True(t, found)

	_, found, err = wrapped.GetTableInsensitive(nil, "absent")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestReadOnlyDatabase_NilPassthrough verifies newReadOnlyDatabase mirrors
// newReadOnlyTable's nil handling rather than wrapping a nil interface value.
func TestReadOnlyDatabase_NilPassthrough(t *testing.T) {
	assert.Nil(t, newReadOnlyDatabase(nil))
}

// TestProvider_WithReadOnly_HidesDatabaseDDL verifies the provider wiring:
// every database served by a read-only Provider is wrapped, so DDL cannot be
// reached through Database() regardless of what the concrete Database
// implements.
func TestProvider_WithReadOnly_HidesDatabaseDDL(t *testing.T) {
	svc := &mockKVService{}
	p := NewProviderWithEngine("qrdb", svc, "", WithReadOnly())
	ctx := newTestContext()

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)

	_, ok := db.(*readOnlyDatabase)
	assert.True(t, ok, "read-only provider must serve a wrapped database")

	_, ok = db.(sqle.TableCreator)
	assert.False(t, ok, "read-only provider must not serve a TableCreator")
	_, ok = db.(sqle.TableDropper)
	assert.False(t, ok, "read-only provider must not serve a TableDropper")
}

// TestProvider_WithReadOnly_HidesDatabaseDDL_AllDatabases verifies the wrap is
// applied on the AllDatabases path too — the analyzer enumerates databases
// there, so an unwrapped entry would reopen the DDL path.
func TestProvider_WithReadOnly_HidesDatabaseDDL_AllDatabases(t *testing.T) {
	svc := &mockKVService{}
	p := NewProviderWithEngine("qrdb", svc, "", WithReadOnly())
	ctx := newTestContext()

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 1)

	_, ok := dbs[0].(*readOnlyDatabase)
	assert.True(t, ok, "AllDatabases must wrap every served database when read-only")
}

// TestProvider_WithoutReadOnly_ServesConcreteDatabase verifies the default
// path is unchanged and still hands back the concrete *Database, which is what
// DDL support will attach to.
func TestProvider_WithoutReadOnly_ServesConcreteDatabase(t *testing.T) {
	svc := &mockKVService{}
	p := NewProviderWithEngine("qrdb", svc, "")
	ctx := newTestContext()

	db, err := p.Database(ctx, "qrdb")
	require.NoError(t, err)

	_, ok := db.(*Database)
	assert.True(t, ok, "default provider must serve the concrete *Database")

	dbs := p.AllDatabases(ctx)
	require.Len(t, dbs, 1)
	_, ok = dbs[0].(*Database)
	assert.True(t, ok, "default AllDatabases must serve the concrete *Database")
}
