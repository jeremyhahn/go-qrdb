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

// qrdbIndex implements sql.Index for a QRDB secondary index. Each instance
// wraps a single-column index registered in the QRDB index service and
// exposes it to the go-mysql-server query planner so that it can accelerate
// WHERE clauses on the indexed field.
type qrdbIndex struct {
	id        string // fully-qualified index name, e.g. "mydb.users.Email"
	database  string
	table     string
	column    string // indexed field name
	unique    bool
	comment   string
	fieldType string // "string", "uint64", "int64", "float64", "boolean"
}

// Compile-time interface assertion.
var _ sqle.Index = (*qrdbIndex)(nil)

// ID returns the unique identifier for this index. The convention is
// "database.table.column" to avoid collisions across tables.
func (i *qrdbIndex) ID() string { return i.id }

// Database returns the database name this index belongs to.
func (i *qrdbIndex) Database() string { return i.database }

// Table returns the table name this index belongs to.
func (i *qrdbIndex) Table() string { return i.table }

// Expressions returns the indexed column expression in "table.column"
// format, as expected by the go-mysql-server analyzer when matching
// index expressions to query predicates.
func (i *qrdbIndex) Expressions() []string {
	return []string{i.table + "." + i.column}
}

// IsUnique reports whether the index enforces uniqueness.
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
// expression. The single entry maps the column expression string to
// its SQL type.
func (i *qrdbIndex) ColumnExpressionTypes() []sqle.ColumnExpressionType {
	return []sqle.ColumnExpressionType{
		{
			Expression: i.table + "." + i.column,
			Type:       i.sqlType(),
		},
	}
}

// CanSupport reports whether this index can satisfy the given range
// filters. QRDB secondary indexes support all non-spatial, non-full-text
// range queries.
func (i *qrdbIndex) CanSupport(_ *sqle.Context, _ ...sqle.Range) bool { return true }

// CanSupportOrderBy reports false; QRDB secondary indexes do not
// optimize ORDER BY expressions (only vector distance functions use this).
func (i *qrdbIndex) CanSupportOrderBy(_ sqle.Expression) bool { return false }

// PrefixLengths returns nil; QRDB secondary indexes do not use column
// prefix lengths.
func (i *qrdbIndex) PrefixLengths() []uint16 { return nil }

// sqlType maps the QRDB field type string to a go-mysql-server SQL type.
// Unknown types default to VARCHAR so that the index remains usable for
// string-typed fields without schema information.
func (i *qrdbIndex) sqlType() sqle.Type {
	switch i.fieldType {
	case "uint64":
		return types.Uint64
	case "int64":
		return types.Int64
	case "float64":
		return types.Float64
	case "boolean":
		return types.Boolean
	default:
		return types.MustCreateString(sqltypes.VarChar, 65535, sqle.Collation_Default)
	}
}

// newQRDBIndex constructs a qrdbIndex with the fully-qualified ID
// formed from database, table, and column. The id field is set to
// "database.table.column" and is stable across restarts.
func newQRDBIndex(database, table, column, fieldType string, unique bool) *qrdbIndex {
	return &qrdbIndex{
		id:        database + "." + table + "." + column,
		database:  database,
		table:     table,
		column:    column,
		fieldType: fieldType,
		unique:    unique,
	}
}
