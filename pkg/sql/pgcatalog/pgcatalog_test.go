//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pgcatalog

import (
	"math"
	"strings"
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCatalog returns a catalog over the named in-memory databases.
func newTestCatalog(names ...string) sqle.Catalog {
	dbs := make([]sqle.Database, 0, len(names))
	for _, n := range names {
		dbs = append(dbs, memory.NewDatabase(n))
	}
	return analyzer.NewCatalog(memory.NewDBProvider(dbs...), sqle.EngineOverrides{})
}

// scan reads every row of tbl through its single partition.
func scan(t *testing.T, ctx *sqle.Context, tbl sqle.Table) []sqle.Row {
	t.Helper()
	parts, err := tbl.Partitions(ctx)
	require.NoError(t, err)
	var rows []sqle.Row
	for {
		part, err := parts.Next(ctx)
		if err != nil {
			break
		}
		iter, err := tbl.PartitionRows(ctx, part)
		require.NoError(t, err)
		for {
			row, err := iter.Next(ctx)
			if err != nil {
				break
			}
			rows = append(rows, row)
		}
		require.NoError(t, iter.Close(ctx))
	}
	require.NoError(t, parts.Close(ctx))
	return rows
}

// assigned resolves name and hands it the catalog the way go-mysql-server's
// plan builder does.
func assigned(t *testing.T, name string, cat sqle.Catalog) sqle.Table {
	t.Helper()
	tbl, ok := Relation(name)
	require.True(t, ok)
	ct, ok := tbl.(sqle.CatalogTable)
	require.True(t, ok)
	return ct.AssignCatalog(cat)
}

func TestUDTNameMapsEveryGoMySQLServerType(t *testing.T) {
	cases := map[string]string{
		"bigint":     "int8",
		"BIGINT":     "int8",
		"int":        "int4",
		"smallint":   "int2",
		"tinyint":    "bool",
		"decimal":    "numeric",
		"double":     "float8",
		"float":      "float4",
		"char":       "bpchar",
		"varchar":    "varchar",
		"longtext":   "text",
		"longblob":   "bytea",
		"datetime":   "timestamptz",
		"timestamp":  "timestamptz",
		"date":       "date",
		"time":       "time",
		"json":       "json",
		"geometry":   DefaultUDTName,
		"":           DefaultUDTName,
		"not-a-type": DefaultUDTName,
	}
	for dataType, want := range cases {
		assert.Equalf(t, want, UDTName(dataType), "udt_name for %q", dataType)
	}
}

func TestPGTypeCoversEveryUDTName(t *testing.T) {
	known := make(map[string]bool, len(pgTypes))
	for _, ty := range pgTypes {
		assert.Falsef(t, known[ty.Name], "duplicate pg_type row %q", ty.Name)
		known[ty.Name] = true
	}

	// Every name UDTName can return must have a pg_type row, or the
	// introspection join silently drops the column (see the package doc).
	require.True(t, known[DefaultUDTName], "fallback %q missing from pg_type", DefaultUDTName)
	used := map[string]bool{DefaultUDTName: true}
	for dataType, udt := range udtNameByDataType {
		assert.Truef(t, known[udt], "udt_name %q (from %q) has no pg_type row", udt, dataType)
		used[udt] = true
	}

	// And nothing unreachable is carried: pg_type is an inventory of what
	// this surface can name, not a transcription of PostgreSQL's.
	for name := range known {
		assert.Truef(t, used[name], "pg_type row %q is unreachable from any data type", name)
	}
}

func TestPGTypeOIDsAndLengthsAreFaithful(t *testing.T) {
	byName := make(map[string]Type, len(pgTypes))
	seen := make(map[uint32]string, len(pgTypes))
	for _, ty := range pgTypes {
		byName[ty.Name] = ty
		prev, dup := seen[ty.OID]
		assert.Falsef(t, dup, "OID %d shared by %q and %q", ty.OID, prev, ty.Name)
		seen[ty.OID] = ty.Name
		assert.Lessf(t, ty.OID, uint32(firstUserOID), "%q must use a reserved system OID", ty.Name)
	}

	// A representative sample against PostgreSQL's own pg_type.
	assert.Equal(t, Type{Name: "int8", OID: 20, Len: 8}, byName["int8"])
	assert.Equal(t, Type{Name: "bool", OID: 16, Len: 1}, byName["bool"])
	assert.Equal(t, Type{Name: "text", OID: 25, Len: -1}, byName["text"])
	assert.Equal(t, Type{Name: "timestamptz", OID: 1184, Len: 8}, byName["timestamptz"])
}

func TestNumericRadixMatchesPostgreSQL(t *testing.T) {
	for _, udt := range []string{"int2", "int4", "int8", "numeric"} {
		radix, ok := NumericRadix(udt)
		assert.Truef(t, ok, "%q is numeric", udt)
		assert.Equalf(t, uint32(10), radix, "%q counts decimal digits", udt)
	}
	for _, udt := range []string{"float4", "float8"} {
		radix, ok := NumericRadix(udt)
		assert.Truef(t, ok, "%q is numeric", udt)
		assert.Equalf(t, uint32(2), radix, "%q counts bits", udt)
	}
	for _, udt := range []string{"text", "bytea", "timestamptz", "bogus"} {
		_, ok := NumericRadix(udt)
		assert.Falsef(t, ok, "%q is not numeric", udt)
	}
}

func TestDerivedOIDsAreStableAndSeparated(t *testing.T) {
	assert.Equal(t, NamespaceOID("gorm"), NamespaceOID("gorm"))
	assert.Equal(t, NamespaceOID("gorm"), NamespaceOID("GORM"))
	assert.NotEqual(t, NamespaceOID("gorm"), NamespaceOID("qrdb"))

	// Domain separation: a schema and a relation of the same name must not
	// collide, and neither may the parts of a qualified relation name.
	assert.NotEqual(t, uint32(NamespaceOID("users")), ClassOID("users", "users"))
	assert.NotEqual(t, ClassOID("ab", "c"), ClassOID("a", "bc"))
	assert.NotEqual(t, ClassOID("gorm", "users"), ClassOID("other", "users"))
}

func TestDerivedOIDsStayInTheWireRepresentableRange(t *testing.T) {
	for _, name := range []string{"", "a", "gorm", "information_schema", strings.Repeat("x", 300)} {
		ns := NamespaceOID(name)
		cls := ClassOID(name, name)
		for _, oid := range []uint32{ns, cls} {
			assert.GreaterOrEqual(t, oid, uint32(firstUserOID))
			assert.LessOrEqual(t, oid, uint32(math.MaxInt32))
		}
	}
}

func TestNewServesExactlyTheSupportedRelations(t *testing.T) {
	db := New()
	assert.Equal(t, SchemaName, db.Name())

	names, err := db.GetTableNames(sqle.NewEmptyContext())
	require.NoError(t, err)
	assert.Equal(t, []string{AttributeRelation, ClassRelation, DescriptionRelation, IndexesRelation, NamespaceRelation, TypeRelation, IndexColumnsRelation}, names)

	for _, name := range names {
		tbl, ok, err := db.GetTableInsensitive(sqle.NewEmptyContext(), strings.ToUpper(name))
		require.NoError(t, err)
		require.Truef(t, ok, "%q resolves case-insensitively", name)
		assert.Equal(t, name, tbl.Name())
		assert.Equal(t, SchemaName+"."+name, tbl.String())
		assert.NotEmpty(t, tbl.Schema(sqle.NewEmptyContext()))
	}

	_, ok, err := db.GetTableInsensitive(sqle.NewEmptyContext(), "pg_constraint")
	require.NoError(t, err)
	assert.False(t, ok, "only the measured relations are served")
}

func TestRelationsAreStructurallyReadOnly(t *testing.T) {
	for name := range relationRows {
		tbl, ok := Relation(name)
		require.True(t, ok)
		_, insertable := tbl.(sqle.InsertableTable)
		_, updatable := tbl.(sqle.UpdatableTable)
		_, deletable := tbl.(sqle.DeletableTable)
		_, replaceable := tbl.(sqle.ReplaceableTable)
		assert.Falsef(t, insertable, "%s must not be insertable", name)
		assert.Falsef(t, updatable, "%s must not be updatable", name)
		assert.Falsef(t, deletable, "%s must not be deletable", name)
		assert.Falsef(t, replaceable, "%s must not be replaceable", name)
	}
}

func TestRelationSchemasCoverEveryServedRelation(t *testing.T) {
	assert.Equal(t, len(relationRows), len(relationSchemas))
	for name := range relationRows {
		schema, ok := relationSchemas[name]
		require.Truef(t, ok, "%s has no schema", name)
		for _, col := range schema {
			assert.Equalf(t, name, col.Source, "column %s.%s reports its source", name, col.Name)
		}
	}
}

func TestRelationRequiresACatalog(t *testing.T) {
	tbl, ok := Relation(ClassRelation)
	require.True(t, ok)
	ctx := sqle.NewEmptyContext()
	parts, err := tbl.Partitions(ctx)
	require.NoError(t, err)
	part, err := parts.Next(ctx)
	require.NoError(t, err)

	_, err = tbl.PartitionRows(ctx, part)
	var want *CatalogUnavailableError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, ClassRelation, want.Relation)
	assert.Contains(t, err.Error(), "pg_catalog.pg_class")
}

func TestRelationRejectsAForeignPartition(t *testing.T) {
	cat := newTestCatalog("gorm")
	ctx := sqle.NewEmptyContext()
	tbl := assigned(t, TypeRelation, cat)

	_, err := tbl.PartitionRows(ctx, relationPartition("pg_class"))
	var want *UnknownPartitionError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, TypeRelation, want.Relation)
	assert.Contains(t, err.Error(), "pg_class")
}

func TestAssignCatalogDoesNotMutateTheReceiver(t *testing.T) {
	tbl, ok := Relation(NamespaceRelation)
	require.True(t, ok)
	original, isRelation := tbl.(*relation)
	require.True(t, isRelation)

	withCatalog := original.AssignCatalog(newTestCatalog("gorm"))
	assert.Nil(t, original.catalog, "the receiver is untouched")
	assert.NotSame(t, original, withCatalog)
	assert.Equal(t, original.Name(), withCatalog.Name())
}

func TestTypeRowsMirrorThePGTypeInventory(t *testing.T) {
	ctx := sqle.NewEmptyContext()
	rows := scan(t, ctx, assigned(t, TypeRelation, newTestCatalog("gorm")))
	require.Len(t, rows, len(pgTypes))
	for i, ty := range pgTypes {
		assert.Equal(t, sqle.Row{ty.OID, ty.Name, ty.Len}, rows[i])
	}
}

func TestNamespaceRowsCoverEveryDatabase(t *testing.T) {
	cat := newTestCatalog("gorm", "other")
	ctx := sqle.NewEmptyContext()
	rows := scan(t, ctx, assigned(t, NamespaceRelation, cat))

	byName := make(map[string]uint32, len(rows))
	for _, row := range rows {
		byName[row[1].(string)] = row[0].(uint32)
	}
	assert.Equal(t, NamespaceOID("gorm"), byName["gorm"])
	assert.Equal(t, NamespaceOID("other"), byName["other"])
	assert.Contains(t, byName, sqle.InformationSchemaDatabaseName)
}

func TestClassRowsPointAtTheirNamespace(t *testing.T) {
	cat := newTestCatalog("gorm")
	ctx := sqle.NewEmptyContext()
	rows := scan(t, ctx, assigned(t, ClassRelation, cat))
	require.NotEmpty(t, rows, "information_schema alone supplies relations")

	nsOIDs := make(map[uint32]bool)
	for _, row := range rows {
		relname := row[1].(string)
		nsOID := row[2].(uint32)
		nsOIDs[nsOID] = true
		// Whatever schema the relation belongs to, its own OID is derived
		// from that schema plus its name.
		if nsOID == NamespaceOID(sqle.InformationSchemaDatabaseName) {
			assert.Equal(t, ClassOID(sqle.InformationSchemaDatabaseName, relname), row[0].(uint32))
		}
	}
	assert.Contains(t, nsOIDs, NamespaceOID(sqle.InformationSchemaDatabaseName))
}

func TestDescriptionIsEmpty(t *testing.T) {
	ctx := sqle.NewEmptyContext()
	rows := scan(t, ctx, assigned(t, DescriptionRelation, newTestCatalog("gorm")))
	assert.Empty(t, rows, "qrdb implements no COMMENT ON, so nothing is described")
}

func TestColumnsTableAppendsThePostgreSQLColumns(t *testing.T) {
	tbl := NewColumnsTable()
	schema := tbl.Schema(sqle.NewEmptyContext())
	require.Len(t, schema, mysqlColumnCount+len(postgresColumns))

	// Every go-mysql-server column keeps its position and its type.
	base := information_schema.NewColumnsTable().TableSchema
	for i, col := range base {
		assert.Equalf(t, col.Name, schema[i].Name, "column %d is unmoved", i)
	}
	assert.Equal(t, "UDT_NAME", schema[mysqlColumnCount].Name)
	assert.Equal(t, "NUMERIC_PRECISION_RADIX", schema[mysqlColumnCount+1].Name)
	assert.Equal(t, "IDENTITY_INCREMENT", schema[mysqlColumnCount+2].Name)
}

func TestColumnsTableDoesNotMutateGoMySQLServersSchema(t *testing.T) {
	before := len(information_schema.NewColumnsTable().TableSchema)
	_ = NewColumnsTable()
	_ = NewColumnsTable()
	assert.Equal(t, before, len(information_schema.NewColumnsTable().TableSchema),
		"go-mysql-server's own columns schema is shared with every MySQL engine in this process")
}

func TestColumnsSchemaLayout(t *testing.T) {
	// The row positions extendRow reads are indices into go-mysql-server's
	// schema; pin them so a change there is a test failure and not a
	// silently wrong udt_name.
	schema := information_schema.NewColumnsTable().TableSchema
	assert.Equal(t, "TABLE_CATALOG", schema[tableCatalogIdx].Name)
	assert.Equal(t, "TABLE_SCHEMA", schema[tableSchemaIdx].Name)
	assert.Equal(t, "DATA_TYPE", schema[dataTypeIdx].Name)
	assert.Equal(t, "NUMERIC_PRECISION", schema[numericPrecisionIdx].Name)
	assert.Equal(t, "EXTRA", schema[extraIdx].Name)
}

// baseRow builds a go-mysql-server information_schema.columns row with the
// three fields extendRow reads set as given.
func baseRow(dataType string, numericPrecision interface{}, extra string) sqle.Row {
	row := make(sqle.Row, mysqlColumnCount)
	row[tableCatalogIdx] = "def"
	row[tableSchemaIdx] = "gorm"
	row[dataTypeIdx] = dataType
	row[numericPrecisionIdx] = numericPrecision
	row[extraIdx] = extra
	return row
}

func TestExtendRow(t *testing.T) {
	cases := []struct {
		name          string
		row           sqle.Row
		wantUDT       string
		wantRadix     interface{}
		wantIncrement interface{}
	}{
		{
			name:          "auto increment bigint",
			row:           baseRow("bigint", uint64(19), autoIncrementExtra),
			wantUDT:       "int8",
			wantRadix:     uint32(10),
			wantIncrement: identityIncrement,
		},
		{
			name:      "plain text column",
			row:       baseRow("longtext", nil, ""),
			wantUDT:   "text",
			wantRadix: nil,
		},
		{
			name:      "float reports a binary radix",
			row:       baseRow("double", uint64(22), ""),
			wantUDT:   "float8",
			wantRadix: uint32(2),
		},
		{
			name:      "numeric type without a precision reports no radix",
			row:       baseRow("bigint", nil, ""),
			wantUDT:   "int8",
			wantRadix: nil,
		},
		{
			name:      "unknown type falls back to text",
			row:       baseRow("geometrycollection", nil, "DEFAULT_GENERATED"),
			wantUDT:   DefaultUDTName,
			wantRadix: nil,
		},
		{
			name:          "extra is matched case-insensitively",
			row:           baseRow("int", uint64(10), "AUTO_INCREMENT"),
			wantUDT:       "int4",
			wantRadix:     uint32(10),
			wantIncrement: identityIncrement,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extendRow(tc.row)
			require.NoError(t, err)
			require.Len(t, got, mysqlColumnCount+len(postgresColumns))

			assert.Equal(t, "gorm", got[tableCatalogIdx], "table_catalog reports the schema")
			assert.Equal(t, "def", tc.row[tableCatalogIdx], "the source row is not mutated")
			assert.Equal(t, tc.wantUDT, got[mysqlColumnCount])
			assert.Equal(t, tc.wantRadix, got[mysqlColumnCount+1])
			assert.Equal(t, tc.wantIncrement, got[mysqlColumnCount+2])
		})
	}
}

func TestExtendRowRejectsAWrongWidthRow(t *testing.T) {
	_, err := extendRow(sqle.Row{"def", "gorm"})
	var want *RowWidthError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, mysqlColumnCount, want.Want)
	assert.Equal(t, 2, want.Got)
	assert.Contains(t, err.Error(), "information_schema.columns")
}

func TestInfoSchemaOverridesOnlyTheColumnsTable(t *testing.T) {
	db := InfoSchema()
	ctx := sqle.NewEmptyContext()
	assert.Equal(t, sqle.InformationSchemaDatabaseName, db.Name())

	columns, ok, err := db.GetTableInsensitive(ctx, "COLUMNS")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Len(t, columns.Schema(ctx), mysqlColumnCount+len(postgresColumns))

	tables, ok, err := db.GetTableInsensitive(ctx, information_schema.TablesTableName)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, information_schema.TablesTableName, tables.Name())

	names, err := db.GetTableNames(ctx)
	require.NoError(t, err)
	assert.Contains(t, names, information_schema.ColumnsTableName)
}

func TestInfoSchemaUnknownTable(t *testing.T) {
	_, ok, err := InfoSchema().GetTableInsensitive(sqle.NewEmptyContext(), "no_such_view")
	require.NoError(t, err)
	assert.False(t, ok)
}
