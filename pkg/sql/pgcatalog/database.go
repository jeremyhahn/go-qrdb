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
	"sort"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// SchemaName is the name PostgreSQL gives its system catalogue schema. A
// client reaches these relations either through it (pg_catalog.pg_class) or
// unqualified (pg_type), because PostgreSQL puts pg_catalog implicitly
// first on every session's search_path.
const SchemaName = "pg_catalog"

// Relation names served by this package.
const (
	TypeRelation        = "pg_type"
	ClassRelation       = "pg_class"
	NamespaceRelation   = "pg_namespace"
	DescriptionRelation = "pg_description"
	IndexesRelation     = "pg_indexes"
)

// oidType is the column type used for every OID-valued column. PostgreSQL's
// own oid is a 32-bit unsigned integer; derivedOID keeps every value it
// produces inside the signed range so the unsigned declaration here never
// meets a value the wire layer cannot describe (see oidSpan).
var oidType = types.Uint32

// relationSchemas defines the columns of each served relation. Only the
// columns schema introspection reads are present -- real pg_type has thirty
// -- for the reason given in the package comment.
var relationSchemas = map[string]sqle.Schema{
	TypeRelation: {
		{Name: "oid", Type: oidType, Nullable: false, Source: TypeRelation},
		{Name: "typname", Type: types.LongText, Nullable: false, Source: TypeRelation},
		{Name: "typlen", Type: types.Int32, Nullable: false, Source: TypeRelation},
	},
	ClassRelation: {
		{Name: "oid", Type: oidType, Nullable: false, Source: ClassRelation},
		{Name: "relname", Type: types.LongText, Nullable: false, Source: ClassRelation},
		{Name: "relnamespace", Type: oidType, Nullable: false, Source: ClassRelation},
	},
	NamespaceRelation: {
		{Name: "oid", Type: oidType, Nullable: false, Source: NamespaceRelation},
		{Name: "nspname", Type: types.LongText, Nullable: false, Source: NamespaceRelation},
	},
	DescriptionRelation: {
		{Name: "objoid", Type: oidType, Nullable: false, Source: DescriptionRelation},
		{Name: "objsubid", Type: types.Int32, Nullable: false, Source: DescriptionRelation},
		{Name: "description", Type: types.LongText, Nullable: true, Source: DescriptionRelation},
	},
	IndexesRelation: indexesSchema,
}

// relationRows maps each served relation to the function that computes its
// rows. Dispatching through a map rather than a switch keeps the set of
// served relations a single piece of data that relationSchemas,
// GetTableNames and TestRelationTablesAreConsistent can all be checked
// against.
var relationRows = map[string]rowsFunc{
	TypeRelation:        typeRows,
	ClassRelation:       classRows,
	NamespaceRelation:   namespaceRows,
	DescriptionRelation: descriptionRows,
	IndexesRelation:     indexesRows,
}

// Compile-time interface assertion. database implements sqle.Database and
// nothing else: it is not a MutableDatabase, not a TableCreator, not a
// TableDropper, so DDL against pg_catalog fails during analysis.
var _ sqle.Database = (*database)(nil)

// database is the pg_catalog schema.
type database struct{}

// New returns the pg_catalog schema as a sql.Database. Callers hold the
// interface rather than the concrete type, which exists only to satisfy it.
func New() sqle.Database {
	return database{}
}

// Name returns the schema name.
func (database) Name() string { return SchemaName }

// GetTableInsensitive resolves a pg_catalog relation by name,
// case-insensitively.
func (database) GetTableInsensitive(_ *sqle.Context, name string) (sqle.Table, bool, error) {
	tbl, ok := Relation(name)
	return tbl, ok, nil
}

// GetTableNames returns the served relation names in a stable order.
func (database) GetTableNames(_ *sqle.Context) ([]string, error) {
	names := make([]string, 0, len(relationRows))
	for name := range relationRows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Relation returns the named pg_catalog relation, resolved
// case-insensitively, and whether this package serves it.
//
// It is exported so that pkg/sql/provider can resolve an UNQUALIFIED
// pg_type the way PostgreSQL does -- pg_catalog is implicitly first on
// every session's search_path, so a bare pg_type arrives as an ordinary
// table lookup in whatever database the session is connected to, never as a
// lookup in a schema named pg_catalog.
//
// A fresh value is returned per call because go-mysql-server assigns the
// engine catalog onto the resolved table (see relation.AssignCatalog), and
// two sessions on two engines must not share one.
func Relation(name string) (sqle.Table, bool) {
	lower := strings.ToLower(name)
	rows, ok := relationRows[lower]
	if !ok {
		return nil, false
	}
	return &relation{name: lower, schema: relationSchemas[lower], rows: rows}, true
}

// typeRows returns pg_type: the fixed inventory of PostgreSQL base types
// this surface can name as a column's udt_name. It does not depend on the
// catalog -- the type system is the same for every schema.
func typeRows(_ *sqle.Context, _ sqle.Catalog) ([]sqle.Row, error) {
	rows := make([]sqle.Row, 0, len(pgTypes))
	for _, t := range pgTypes {
		rows = append(rows, sqle.Row{t.OID, t.Name, t.Len})
	}
	return rows, nil
}

// namespaceRows returns pg_namespace: one row per database the engine
// serves.
//
// qrdb maps PostgreSQL's schema namespace onto go-mysql-server's database
// namespace -- they are the same level of the hierarchy here, which is why
// pkg/sql/engine's translator rewrites current_schema() to DATABASE() and
// why information_schema reports a table's owning database as its
// TABLE_SCHEMA. A client comparing nspname against a value it read from
// either therefore matches.
func namespaceRows(ctx *sqle.Context, cat sqle.Catalog) ([]sqle.Row, error) {
	dbs := cat.AllDatabases(ctx)
	rows := make([]sqle.Row, 0, len(dbs))
	for _, db := range dbs {
		name := db.Name()
		rows = append(rows, sqle.Row{NamespaceOID(name), name})
	}
	return rows, nil
}

// classRows returns pg_class: one row per table in every database the
// engine serves, each pointing at its schema's pg_namespace row through
// relnamespace.
//
// A database whose table list cannot be read is skipped rather than failing
// the scan. The alternative would let one unavailable database make schema
// introspection fail for every other one, and the client's own query is
// filtered to a single schema anyway.
func classRows(ctx *sqle.Context, cat sqle.Catalog) ([]sqle.Row, error) {
	var rows []sqle.Row
	for _, db := range cat.AllDatabases(ctx) {
		dbName := db.Name()
		names, err := db.GetTableNames(ctx)
		if err != nil {
			continue
		}
		nsOID := NamespaceOID(dbName)
		for _, tblName := range names {
			rows = append(rows, sqle.Row{ClassOID(dbName, tblName), tblName, nsOID})
		}
	}
	return rows, nil
}

// descriptionRows returns pg_description, which is always empty.
//
// That is not a stub: pg_description holds the comments COMMENT ON records,
// qrdb implements no COMMENT ON statement, and a PostgreSQL database on
// which nobody has issued one has an empty pg_description too. Schema
// introspection LEFT JOINs this relation, so empty means every column
// reports a NULL comment -- which is the true answer.
func descriptionRows(_ *sqle.Context, _ sqle.Catalog) ([]sqle.Row, error) {
	return nil, nil
}
