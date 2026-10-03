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

package provider

import (
	"context"
	"io"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/src-d/go-errors.v1"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/pgcatalog"
)

// gormColumnTypesQuery is verbatim the statement gorm.io/driver/postgres
// v1.6.2 issues from Migrator.ColumnTypes (migrator.go), with its three
// bind parameters made literal. Every clause of it has to resolve for
// AutoMigrate to survive a table that already exists, which is why it is
// pinned here in full rather than approximated: it is the single query this
// whole feature exists to answer.
const gormColumnTypesQuery = `SELECT c.column_name, c.is_nullable = 'YES', c.udt_name, ` +
	`c.character_maximum_length, c.numeric_precision, c.numeric_precision_radix, ` +
	`c.numeric_scale, c.datetime_precision, 8 * typlen, c.column_default, ` +
	`pd.description, c.identity_increment ` +
	`FROM information_schema.columns AS c ` +
	`JOIN pg_type AS pgt ON c.udt_name = pgt.typname ` +
	`LEFT JOIN pg_catalog.pg_description as pd ON pd.objsubid = c.ordinal_position ` +
	`AND pd.objoid = (SELECT oid FROM pg_catalog.pg_class WHERE relname = c.table_name ` +
	`AND relnamespace = (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname = c.table_schema)) ` +
	`where table_catalog = 'gorm' AND table_schema = DATABASE() AND table_name = 'users'`

// newPGProvider returns a Provider serving dbName over the PostgreSQL
// protocol filter, wired to h's registry.
func (h *ddlHarness) newPGProvider(dbName string) *Provider {
	return NewProviderWithEngine(dbName, h.kvSvc, "",
		WithRegistry(h.registry), WithProtocol(ProtocolPostgreSQL))
}

// execPGQuery runs query against dbName through a PostgreSQL-dialect
// engine, which is what installs pkg/sql/pgcatalog's information_schema.
func execPGQuery(prov *Provider, dbName, query string) (sqle.Schema, []sqle.Row, error) {
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	defer func() { _ = eng.Close() }()

	sqlCtx := sqle.NewContext(context.Background())
	sqlCtx.SetCurrentDatabase(dbName)

	schema, iter, _, err := eng.Query(sqlCtx, query)
	if err != nil {
		return nil, nil, err
	}
	var rows []sqle.Row
	for {
		row, nerr := iter.Next(sqlCtx)
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			_ = iter.Close(sqlCtx)
			return nil, nil, nerr
		}
		rows = append(rows, row)
	}
	if cerr := iter.Close(sqlCtx); cerr != nil {
		return nil, nil, cerr
	}
	return schema, rows, nil
}

func TestProviderServesPGCatalogOnlyToPostgreSQL(t *testing.T) {
	h := newDDLHarness()
	ctx := sqle.NewContext(context.Background())

	cases := []struct {
		name     string
		protocol ProtocolType
		want     bool
	}{
		{name: "postgresql", protocol: ProtocolPostgreSQL, want: true},
		{name: "mysql", protocol: ProtocolMySQL, want: false},
		{name: "any", protocol: ProtocolAny, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := NewProviderWithEngine("gorm", h.kvSvc, "",
				WithRegistry(h.registry), WithProtocol(tc.protocol))

			db, err := prov.Database(ctx, pgcatalog.SchemaName)
			assert.Equal(t, tc.want, prov.HasDatabase(ctx, pgcatalog.SchemaName))
			if !tc.want {
				var notFound *DatabaseNotFoundError
				require.ErrorAs(t, err, &notFound)
			} else {
				require.NoError(t, err)
				assert.Equal(t, pgcatalog.SchemaName, db.Name())
			}

			user, err := prov.Database(ctx, "gorm")
			require.NoError(t, err)
			_, found, err := user.GetTableInsensitive(ctx, pgcatalog.TypeRelation)
			require.NoError(t, err)
			assert.Equalf(t, tc.want, found, "unqualified %s", pgcatalog.TypeRelation)
		})
	}
}

func TestProviderResolvesPGCatalogCaseInsensitively(t *testing.T) {
	h := newDDLHarness()
	ctx := sqle.NewContext(context.Background())
	prov := h.newPGProvider("gorm")

	db, err := prov.Database(ctx, "PG_CATALOG")
	require.NoError(t, err)
	assert.Equal(t, pgcatalog.SchemaName, db.Name())
	assert.True(t, prov.HasDatabase(ctx, "Pg_Catalog"))

	user, err := prov.Database(ctx, "gorm")
	require.NoError(t, err)
	for _, name := range []string{"PG_TYPE", "Pg_Class", "pg_namespace", "PG_DESCRIPTION"} {
		tbl, found, err := user.GetTableInsensitive(ctx, name)
		require.NoError(t, err)
		require.Truef(t, found, "unqualified %q", name)
		assert.NotEmpty(t, tbl.Schema(ctx))
	}
}

func TestProviderDoesNotEnumeratePGCatalog(t *testing.T) {
	h := newDDLHarness()
	ctx := sqle.NewContext(context.Background())
	prov := h.newPGProvider("gorm")

	for _, db := range prov.AllDatabases(ctx) {
		assert.NotEqual(t, pgcatalog.SchemaName, db.Name())
	}

	user, err := prov.Database(ctx, "gorm")
	require.NoError(t, err)
	names, err := user.GetTableNames(ctx)
	require.NoError(t, err)
	for _, name := range names {
		_, isPGCatalog := pgcatalog.Relation(name)
		assert.Falsef(t, isPGCatalog, "%q must not be listed as a table of the user schema", name)
	}
}

func TestProviderReservesPGCatalogName(t *testing.T) {
	h := newDDLHarness()
	ctx := sqle.NewContext(context.Background())
	prov := h.newPGProvider("gorm")

	for _, name := range []string{pgcatalog.SchemaName, "PG_CATALOG", MySQLSchemaName} {
		var reserved *ReservedDatabaseNameError
		require.ErrorAsf(t, prov.CreateDatabase(ctx, name), &reserved, "CREATE DATABASE %q", name)
		assert.Equal(t, name, reserved.Name)
		require.ErrorAsf(t, prov.DropDatabase(ctx, name), &reserved, "DROP DATABASE %q", name)
	}
}

func TestPGCatalogRelationsAreQueryable(t *testing.T) {
	h := newDDLHarness()
	prov := h.newPGProvider("gorm")
	_, _, err := execPGQuery(prov, "gorm", "CREATE TABLE users (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, name LONGTEXT)")
	require.NoError(t, err)

	t.Run("unqualified pg_type", func(t *testing.T) {
		_, rows, err := execPGQuery(prov, "gorm", "SELECT typname, typlen FROM pg_type WHERE typname = 'int8'")
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "int8", rows[0][0])
		assert.Equal(t, int32(8), rows[0][1])
	})

	t.Run("qualified pg_namespace", func(t *testing.T) {
		_, rows, err := execPGQuery(prov, "gorm",
			"SELECT oid FROM pg_catalog.pg_namespace WHERE nspname = 'gorm'")
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, pgcatalog.NamespaceOID("gorm"), rows[0][0])
	})

	t.Run("qualified pg_class points at its namespace", func(t *testing.T) {
		_, rows, err := execPGQuery(prov, "gorm",
			"SELECT oid, relnamespace FROM pg_catalog.pg_class WHERE relname = 'users'")
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, pgcatalog.ClassOID("gorm", "users"), rows[0][0])
		assert.Equal(t, pgcatalog.NamespaceOID("gorm"), rows[0][1])
	})

	t.Run("qualified pg_description is empty", func(t *testing.T) {
		_, rows, err := execPGQuery(prov, "gorm", "SELECT * FROM pg_catalog.pg_description")
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}

func TestPGCatalogRelationsRefuseDML(t *testing.T) {
	h := newDDLHarness()
	prov := h.newPGProvider("gorm")

	// Each statement is refused by go-mysql-server's ANALYZER, not by a
	// runtime check inside a write path, because a pg_catalog relation does
	// not implement the corresponding write-capability interface at all (see
	// pkg/sql/pgcatalog's interface assertions). Pinning the exact sentinel
	// rather than "some error" is what makes that structural: a relation
	// that accidentally grew a write interface would start failing somewhere
	// else, and this test would catch it instead of still passing.
	cases := []struct {
		stmt string
		kind *errors.Kind
	}{
		{stmt: "INSERT INTO pg_catalog.pg_type (oid, typname, typlen) VALUES (1, 'x', 1)", kind: plan.ErrInsertIntoNotSupported},
		{stmt: "INSERT INTO pg_type (oid, typname, typlen) VALUES (1, 'x', 1)", kind: plan.ErrInsertIntoNotSupported},
		{stmt: "UPDATE pg_catalog.pg_class SET relname = 'x'", kind: plan.ErrUpdateNotSupported},
		{stmt: "UPDATE pg_class SET relname = 'x'", kind: plan.ErrUpdateNotSupported},
		{stmt: "DELETE FROM pg_catalog.pg_namespace", kind: plan.ErrDeleteFromNotSupported},
		{stmt: "DELETE FROM pg_namespace", kind: plan.ErrDeleteFromNotSupported},
		{stmt: "REPLACE INTO pg_catalog.pg_description (objoid, objsubid, description) VALUES (1, 1, 'x')", kind: plan.ErrInsertIntoNotSupported},
	}
	for _, tc := range cases {
		_, _, err := execPGQuery(prov, "gorm", tc.stmt)
		require.Errorf(t, err, "statement %q must be refused", tc.stmt)
		assert.Truef(t, tc.kind.Is(err), "statement %q refused with %v, want %s", tc.stmt, err, tc.kind)
	}
}

func TestPGCatalogSchemaRefusesDDL(t *testing.T) {
	h := newDDLHarness()
	prov := h.newPGProvider("gorm")

	// DDL against the relations is refused for the same structural reason:
	// pgcatalog's database implements sql.Database and neither
	// sql.TableCreator nor sql.TableDropper.
	for _, stmt := range []string{
		"CREATE TABLE pg_catalog.pg_attribute (attname LONGTEXT)",
		"DROP TABLE pg_catalog.pg_type",
	} {
		_, _, err := execPGQuery(prov, "gorm", stmt)
		assert.Errorf(t, err, "statement %q must be refused", stmt)
	}
}

func TestInformationSchemaColumnsAnswersTheGormMigratorQuery(t *testing.T) {
	h := newDDLHarness()
	prov := h.newPGProvider("gorm")

	_, _, err := execPGQuery(prov, "gorm",
		"CREATE TABLE users ("+
			"id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, "+
			"name LONGTEXT, "+
			"nickname VARCHAR(255), "+
			"age INT NOT NULL, "+
			"active TINYINT, "+
			"score DOUBLE, "+
			"balance DECIMAL(10,2), "+
			"created_at DATETIME, "+
			"payload LONGBLOB)")
	require.NoError(t, err)

	schema, rows, err := execPGQuery(prov, "gorm", gormColumnTypesQuery)
	require.NoError(t, err, "the whole join must resolve")
	require.Len(t, schema, 12, "gorm scans exactly twelve result columns")

	type column struct {
		nullable  bool
		udtName   string
		bitWidth  int64
		increment interface{}
		comment   interface{}
	}
	got := make(map[string]column, len(rows))
	for _, row := range rows {
		name, ok := row[0].(string)
		require.Truef(t, ok, "column_name is a string, got %T", row[0])
		width, ok := row[8].(int64)
		require.Truef(t, ok, "8 * typlen is an integer, got %T", row[8])
		got[name] = column{
			nullable:  row[1] == true,
			udtName:   row[2].(string),
			bitWidth:  width,
			increment: row[11],
			comment:   row[10],
		}
	}

	// One row per column: the JOIN on udt_name = typname must not drop any.
	require.Len(t, got, 9)

	assert.Equal(t, "int8", got["id"].udtName)
	assert.Equal(t, int64(64), got["id"].bitWidth, "8 * typlen is how a client reads a fixed-width column's size")
	assert.Equal(t, "1", got["id"].increment, "an AUTO_INCREMENT column is an identity column")
	assert.False(t, got["id"].nullable)

	assert.Equal(t, "text", got["name"].udtName)
	assert.Equal(t, int64(-8), got["name"].bitWidth, "a variable-length type reports typlen -1")
	assert.Nil(t, got["name"].increment)
	assert.True(t, got["name"].nullable)

	assert.Equal(t, "varchar", got["nickname"].udtName)
	assert.Equal(t, "int4", got["age"].udtName)
	assert.Equal(t, "bool", got["active"].udtName)
	assert.Equal(t, "float8", got["score"].udtName)
	assert.Equal(t, "numeric", got["balance"].udtName)
	assert.Equal(t, "timestamptz", got["created_at"].udtName)
	assert.Equal(t, "bytea", got["payload"].udtName)

	// pg_description is empty, so the LEFT JOIN contributes a NULL comment
	// for every column rather than removing it.
	for name, col := range got {
		assert.Nilf(t, col.comment, "column %q has no comment", name)
	}
}

func TestInformationSchemaColumnsReportsTheRadix(t *testing.T) {
	h := newDDLHarness()
	prov := h.newPGProvider("gorm")
	_, _, err := execPGQuery(prov, "gorm",
		"CREATE TABLE t (n BIGINT, f DOUBLE, s LONGTEXT)")
	require.NoError(t, err)

	_, rows, err := execPGQuery(prov, "gorm",
		"SELECT column_name, numeric_precision_radix FROM information_schema.columns "+
			"WHERE table_schema = DATABASE() AND table_name = 't' ORDER BY ordinal_position")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, sqle.Row{"n", uint32(10)}, rows[0])
	assert.Equal(t, sqle.Row{"f", uint32(2)}, rows[1])
	assert.Equal(t, sqle.Row{"s", nil}, rows[2])
}

func TestInformationSchemaColumnsReportsTheSchemaAsTheCatalog(t *testing.T) {
	h := newDDLHarness()
	prov := h.newPGProvider("gorm")
	_, _, err := execPGQuery(prov, "gorm", "CREATE TABLE t (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)

	// A PostgreSQL client filters on the value current_database() returned,
	// which on this surface is the same name current_schema() returns.
	_, rows, err := execPGQuery(prov, "gorm",
		"SELECT table_catalog, table_schema FROM information_schema.columns "+
			"WHERE table_catalog = DATABASE() AND table_name = 't'")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sqle.Row{"gorm", "gorm"}, rows[0])
}

func TestMySQLDialectInformationSchemaIsUnchanged(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("gorm")
	_, _, err := execQuery(prov, "gorm", "CREATE TABLE t (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)

	// The MySQL endpoint keeps go-mysql-server's constant catalog name...
	_, rows, err := execQuery(prov, "gorm",
		"SELECT table_catalog FROM information_schema.columns WHERE table_name = 't'")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sqle.Row{"def"}, rows[0])

	// ...and does not carry the PostgreSQL-only columns at all.
	_, _, err = execQuery(prov, "gorm",
		"SELECT udt_name FROM information_schema.columns WHERE table_name = 't'")
	assert.Error(t, err, "udt_name is a PostgreSQL column and must not leak onto the MySQL endpoint")
}
