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
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/sqltypes"
)

// indexColumn is one column of a qrdbIndex, carrying the column's name and
// the coarse field-type bucket its SQL type maps to (see
// indexFieldSQLType). Order within qrdbIndex.columns is the index's own
// declared column order, which is the order every consumer -- the query
// planner's range construction, information_schema.STATISTICS'
// SEQ_IN_INDEX, and this package's physical key encoding -- depends on.
type indexColumn struct {
	name      string
	fieldType string // "string", "uint64", "int64", "float64", "boolean"
}

// qrdbIndex implements sql.Index for a QRDB secondary index. Each instance
// wraps an index registered in the QRDB index service (KV/entity/collection
// tables) or declared in the relational table catalog (RelationalTable), and
// exposes it to the go-mysql-server query planner so that it can accelerate
// WHERE clauses on the indexed fields.
//
// An index may cover more than one column. Reporting every column is not
// cosmetic: go-mysql-server derives the shape of the sql.Range set it pushes
// down from Expressions()/ColumnExpressionTypes(), and derives
// information_schema.STATISTICS (and, from that, COLUMN_KEY in
// information_schema.COLUMNS) from the same list. An index that reported
// only its leading column would have its uniqueness attributed to that
// column alone -- MySQL's 'UNI' where the real answer is 'MUL' -- and would
// never receive a predicate on any of its other columns.
type qrdbIndex struct {
	id       string // see ID's doc comment for what this must be
	database string
	table    string
	columns  []indexColumn // indexed columns, in declared index order
	unique   bool
	comment  string
}

// Compile-time interface assertion.
var _ sqle.Index = (*qrdbIndex)(nil)

// ID returns the unique identifier for this index.
//
// For the KV/entity/collection tables (see newQRDBIndex), it is the
// fully-qualified "database.table.column" form those tables have always
// used, since their indexes are unnamed (one implicit index per indexed
// field).
//
// For a RelationalTable secondary index (see newRelationalTableIndex), it
// is instead the index's own catalog name (e.g. "idx_email"), matching the
// bare, table-scoped name MySQL index names use: go-mysql-server's analyzer
// validates ALTER TABLE ... DROP/RENAME INDEX by comparing the statement's
// index name against exactly this value (see GetTableIndexNames in
// go-mysql-server's analyzer package), not against the indexed column or
// any database/table-qualified form. RelationalTable.LookupPartitions
// resolves the lookup back to its indexSpec through this same name.
func (i *qrdbIndex) ID() string { return i.id }

// Database returns the database name this index belongs to.
func (i *qrdbIndex) Database() string { return i.database }

// Table returns the table name this index belongs to.
func (i *qrdbIndex) Table() string { return i.table }

// Expressions returns one indexed column expression per index column, in
// "table.column" format and in declared index order, as expected by the
// go-mysql-server analyzer when matching index expressions to query
// predicates.
func (i *qrdbIndex) Expressions() []string {
	out := make([]string, len(i.columns))
	for n, c := range i.columns {
		out[n] = i.table + "." + c.name
	}
	return out
}

// CoversColumns reports whether every requested column can be answered from
// the index alone, without reading the base row.
//
// It reports false unconditionally. A QRDB secondary index stores only the
// indexed value and the keys of the rows carrying it, so any projection has to
// fetch the row regardless — an index-only scan is never available. Claiming
// otherwise would let the planner skip the row fetch and return empty columns.
func (i *qrdbIndex) CoversColumns(_ []string) bool { return false }

// IsUnique reports whether the index enforces uniqueness. For a multi-column
// index that constraint is over the whole tuple, not over any one column.
func (i *qrdbIndex) IsUnique() bool { return i.unique }

// IsSpatial reports false; QRDB secondary indexes are not spatial.
func (i *qrdbIndex) IsSpatial() bool { return false }

// IsFullText reports false; QRDB secondary indexes are not full-text.
func (i *qrdbIndex) IsFullText() bool { return false }

// IsVector reports false; QRDB secondary indexes are not vector indexes.
func (i *qrdbIndex) IsVector() bool { return false }

// Comment returns the index comment string.
func (i *qrdbIndex) Comment() string { return i.comment }

// IndexType returns the storage type label reported to MySQL clients.
// QRDB secondary indexes use a sorted key structure equivalent to BTREE.
func (i *qrdbIndex) IndexType() string { return "BTREE" }

// IsGenerated reports false; QRDB secondary indexes are explicitly
// created by the user or application.
func (i *qrdbIndex) IsGenerated() bool { return false }

// ColumnExpressionTypes returns the type metadata for each indexed
// expression, one entry per index column in declared index order, mapping
// the column expression string to its SQL type.
func (i *qrdbIndex) ColumnExpressionTypes(_ *sqle.Context) []sqle.ColumnExpressionType {
	out := make([]sqle.ColumnExpressionType, len(i.columns))
	for n, c := range i.columns {
		out[n] = sqle.ColumnExpressionType{
			Expression: i.table + "." + c.name,
			Type:       indexFieldSQLType(c.fieldType),
		}
	}
	return out
}

// CanSupport reports whether this index can satisfy the given range
// filters. QRDB secondary indexes support all non-spatial, non-full-text
// range queries.
func (i *qrdbIndex) CanSupport(_ *sqle.Context, _ ...sqle.Range) bool { return true }

// CanSupportOrderBy reports false; QRDB secondary indexes do not
// optimize ORDER BY expressions (only vector distance functions use this).
func (i *qrdbIndex) CanSupportOrderBy(_ sqle.Expression) bool { return false }

// PrefixLengths returns nil; QRDB secondary indexes do not report column
// prefix lengths to the planner. An index whose catalog definition does
// declare one is still encoded (and its uniqueness still enforced) over the
// truncated prefix -- matching MySQL -- but is never used to answer a
// lookup, since a prefix-truncated entry cannot distinguish two values
// sharing that prefix; see indexSpec.prefixed.
func (i *qrdbIndex) PrefixLengths() []uint16 { return nil }

// firstColumn returns the leading indexed column's name. Every qrdbIndex has
// at least one column: both constructors require one.
func (i *qrdbIndex) firstColumn() string { return i.columns[0].name }

// indexFieldSQLTypes maps the coarse QRDB field-type bucket to the
// go-mysql-server SQL type reported for an indexed expression. Map-based
// dispatch, per this package's convention; an unlisted bucket falls through
// to VARCHAR (see indexFieldSQLType) so that an index remains usable for
// string-typed fields without schema information.
var indexFieldSQLTypes = map[string]sqle.Type{
	"uint64":  types.Uint64,
	"int64":   types.Int64,
	"float64": types.Float64,
	"boolean": types.Boolean,
}

// indexFieldSQLType maps a QRDB field type string to a go-mysql-server SQL
// type. Unknown types default to VARCHAR.
func indexFieldSQLType(fieldType string) sqle.Type {
	if t, ok := indexFieldSQLTypes[fieldType]; ok {
		return t
	}
	return types.MustCreateString(sqltypes.VarChar, 65535, sqle.Collation_Default)
}

// newQRDBIndex constructs a single-column qrdbIndex with the
// fully-qualified ID formed from database, table, and column. The id field
// is set to "database.table.column" and is stable across restarts. Used by
// the KV/entity/collection tables, whose indexes are unnamed and always
// cover exactly one field.
func newQRDBIndex(database, table, column, fieldType string, unique bool) *qrdbIndex {
	return &qrdbIndex{
		id:       database + "." + table + "." + column,
		database: database,
		table:    table,
		columns:  []indexColumn{{name: column, fieldType: fieldType}},
		unique:   unique,
	}
}

// newRelationalTableIndex constructs a qrdbIndex for a RelationalTable
// index, identified by its own catalog name (indexName) rather than the
// database.table.column form newQRDBIndex uses -- see ID's doc comment for
// why RelationalTable indexes need this distinction. columns lists every
// indexed column in declared index order.
func newRelationalTableIndex(database, table, indexName string, columns []indexColumn, unique bool, comment string) *qrdbIndex {
	return &qrdbIndex{
		id:       indexName,
		database: database,
		table:    table,
		columns:  columns,
		unique:   unique,
		comment:  comment,
	}
}
