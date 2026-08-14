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
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// CREATE TABLE
// ---------------------------------------------------------------------------

func TestDatabase_CreateTable_RoundTripsThroughCatalogAndIsQueryable(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255) NOT NULL)")

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	assert.Equal(t, "users", def.Name)
	require.Len(t, def.Columns, 2)
	assert.Equal(t, "id", def.Columns[0].Name)
	assert.Equal(t, "email", def.Columns[1].Name)
	require.Len(t, def.PrimaryKey, 1)
	assert.Equal(t, "id", def.PrimaryKey[0].Name)

	// Queryable with zero rows.
	schema, rows := mustExecQuery(t, prov, "shop", "SELECT * FROM users")
	require.Len(t, schema, 2)
	assert.Empty(t, rows)

	// Seed a row directly (DML is a later phase) and confirm it becomes
	// visible through the same table -- proving CreateTable produced a
	// genuinely queryable table, not just a catalog entry.
	rowCodec, err := catalog.NewRowCodec(def)
	require.NoError(t, err)
	keyCodec, err := catalog.NewKeyCodec(def)
	require.NoError(t, err)
	encodedRow, err := rowCodec.Encode(sqle.Row{uint64(1), "alice@example.com"})
	require.NoError(t, err)
	encodedKey, err := keyCodec.EncodeValues([]any{uint64(1)})
	require.NoError(t, err)
	require.NoError(t, h.kvSvc.Put(context.Background(), service.TableDataPrefix("shop", def.TableID)+encodedKey, encodedRow))

	_, rows = mustExecQuery(t, prov, "shop", "SELECT * FROM users")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(1), rows[0][0])
	assert.Equal(t, "alice@example.com", rows[0][1])
}

func TestDatabase_CreateTable_PrefixLengthPrimaryKey(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop",
		"CREATE TABLE docs (title VARCHAR(500) NOT NULL, PRIMARY KEY (title(10)))")

	def, err := h.tableSvc.GetTable(context.Background(), "shop", "docs")
	require.NoError(t, err)
	require.Len(t, def.PrimaryKey, 1)
	assert.Equal(t, "title", def.PrimaryKey[0].Name)
	assert.Equal(t, uint16(10), def.PrimaryKey[0].PrefixLength)
}

func TestDatabase_CreateTable_Duplicate(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY)")

	_, _, err := execQuery(prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY)")
	require.Error(t, err)
	var exists *service.TableAlreadyExistsError
	require.ErrorAs(t, err, &exists)
}

func TestDatabase_CreateTable_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	err := db.CreateTable(newTestContext(), "users", sqle.PrimaryKeySchema{}, sqle.Collation_Default, "")
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestDatabase_CreateTable_RejectsUnmappableSchema(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")

	schema := sqle.NewPrimaryKeySchema(sqle.Schema{
		{Name: "g", Type: types.GeometryType{}, Nullable: true, Source: "shapes"},
	})
	err := db.CreateTable(newTestContext(), "shapes", schema, sqle.Collation_Default, "")
	var e *catalog.SpatialTypeUnsupportedError
	require.ErrorAs(t, err, &e)

	_, err = h.tableSvc.GetTable(context.Background(), "shop", "shapes")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound, "a schema-invalid CREATE TABLE must never reach the catalog")
}

func TestDatabase_CreateTable_RejectsGeneratedColumn(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")

	def, err := sqle.NewColumnDefaultValue(nil, types.Int64, false, false, true)
	require.NoError(t, err)
	schema := sqle.NewPrimaryKeySchema(sqle.Schema{
		{Name: "id", Type: types.Int64, Nullable: false, Source: "t", PrimaryKey: true},
		{Name: "computed", Type: types.Int64, Nullable: true, Source: "t", Generated: def},
	})
	err = db.CreateTable(newTestContext(), "t", schema, sqle.Collation_Default, "")
	var e *GeneratedColumnUnsupportedError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// DROP TABLE
// ---------------------------------------------------------------------------

func TestDatabase_DropTable_ReclaimsRowsAndIndexEntries(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)

	// Seed rows and a physical secondary-index entry directly.
	dataPrefix := service.TableDataPrefix("shop", def.TableID)
	require.NoError(t, h.kvSvc.Put(context.Background(), dataPrefix+"row1", []byte("data1")))
	require.NoError(t, h.kvSvc.Put(context.Background(), dataPrefix+"row2", []byte("data2")))

	require.NoError(t, h.tableSvc.CreateIndex(context.Background(), &service.CreateTableIndexRequest{
		Database: "shop", Table: "users",
		Index: service.TableIndex{Name: "idx_email", Columns: []service.IndexCol{{Name: "email"}}},
	}))
	indexPrefix := service.TableIndexPrefix("shop", def.TableID, "idx_email")
	require.NoError(t, h.kvSvc.Put(context.Background(), indexPrefix+"entry1", []byte("row1")))

	mustExecQuery(t, prov, "shop", "DROP TABLE users")

	rows, err := h.kvSvc.Scan(context.Background(), dataPrefix)
	require.NoError(t, err)
	assert.Empty(t, rows, "DROP TABLE must reclaim every row")

	idxRows, err := h.kvSvc.Scan(context.Background(), indexPrefix)
	require.NoError(t, err)
	assert.Empty(t, idxRows, "DROP TABLE must reclaim every secondary-index entry")

	_, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestDatabase_DropTable_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	err := db.DropTable(newTestContext(), "users")
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestDatabase_DropTable_NotFound(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	_, _, err := execQuery(prov, "shop", "DROP TABLE users")
	require.Error(t, err)
}

func TestDatabase_DropTable_BatchServiceError(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")

	_, err := h.tableSvc.CreateTable(context.Background(), &service.CreateTableRequest{
		Database: "shop", Name: "users", Columns: []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
	})
	require.NoError(t, err)

	reg := h.registry.(*fakeRelationalRegistry)
	reg.batchErr = assert.AnError
	err = db.DropTable(newTestContext(), "users")
	require.ErrorIs(t, err, assert.AnError)
}

// ---------------------------------------------------------------------------
// RENAME TABLE
// ---------------------------------------------------------------------------

func TestDatabase_RenameTable_DataStaysInPlace(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)

	dataPrefix := service.TableDataPrefix("shop", before.TableID)
	require.NoError(t, h.kvSvc.Put(context.Background(), dataPrefix+"row1", []byte("data1")))

	mustExecQuery(t, prov, "shop", "RENAME TABLE users TO accounts")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "accounts")
	require.NoError(t, err)
	assert.Equal(t, before.TableID, after.TableID, "TableID must not change across a rename")

	// The row is still readable under the exact same physical key: no data
	// moved.
	v, err := h.kvSvc.Get(context.Background(), dataPrefix+"row1")
	require.NoError(t, err)
	assert.Equal(t, []byte("data1"), v)

	_, err = h.tableSvc.GetTable(context.Background(), "shop", "users")
	var notFound *service.TableNotFoundError
	require.ErrorAs(t, err, &notFound)
}

func TestDatabase_RenameTable_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	err := db.RenameTable(newTestContext(), "users", "accounts")
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// Collation
// ---------------------------------------------------------------------------

func TestDatabase_Collation_RoundTrip(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()

	assert.Equal(t, sqle.Collation_Default, db.GetCollation(ctx), "no collation set yet")

	require.NoError(t, db.SetCollation(ctx, sqle.Collation_utf8mb4_unicode_ci))
	assert.Equal(t, sqle.Collation_utf8mb4_unicode_ci, db.GetCollation(ctx))
}

func TestDatabase_Collation_NoRegistry(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	ctx := newTestContext()

	assert.Equal(t, sqle.Collation_Default, db.GetCollation(ctx))

	err := db.SetCollation(ctx, sqle.Collation_utf8mb4_unicode_ci)
	var e *RelationalCatalogUnavailableError
	require.ErrorAs(t, err, &e)
}

func TestDatabase_Collation_UnspecifiedRoundTripsToDefault(t *testing.T) {
	h := newDDLHarness()
	db := h.newDDLDatabase("shop")
	ctx := newTestContext()

	require.NoError(t, db.SetCollation(ctx, sqle.Collation_Unspecified))
	assert.Equal(t, sqle.Collation_Default, db.GetCollation(ctx))
}

// ---------------------------------------------------------------------------
// SchemaValidator
// ---------------------------------------------------------------------------

func TestDatabase_ValidateSchema_AcceptsMappableSchema(t *testing.T) {
	db := &Database{name: "shop"}
	err := db.ValidateSchema(sqle.Schema{
		{Name: "id", Type: types.Int64, Source: "t"},
		{Name: "name", Type: types.MustCreateStringWithDefaults(sqltypes.VarChar, 255), Source: "t"},
	})
	assert.NoError(t, err)
}

func TestDatabase_ValidateSchema_RejectsSpatialType(t *testing.T) {
	db := &Database{name: "shop"}
	err := db.ValidateSchema(sqle.Schema{
		{Name: "g", Type: types.PointType{}, Source: "t"},
	})
	var e *catalog.SpatialTypeUnsupportedError
	require.ErrorAs(t, err, &e)
}

// ---------------------------------------------------------------------------
// Structural interface assertions on the concrete *Database -- the positive
// control for the read-only guard tests in readonly_database_test.go: these
// prove the DDL interfaces are real (reachable on the unwrapped type),
// which is exactly what newReadOnlyDatabase must make unreachable.
// ---------------------------------------------------------------------------

func TestDatabase_ImplementsDDLInterfaces(t *testing.T) {
	var d sqle.Database = &Database{}
	_, ok := d.(sqle.TableCreator)
	assert.True(t, ok)
	_, ok = d.(sqle.IndexedTableCreator)
	assert.True(t, ok)
	_, ok = d.(sqle.TableDropper)
	assert.True(t, ok)
	_, ok = d.(sqle.TableRenamer)
	assert.True(t, ok)
	_, ok = d.(sqle.CollatedDatabase)
	assert.True(t, ok)
	_, ok = d.(sqle.SchemaValidator)
	assert.True(t, ok)
}
