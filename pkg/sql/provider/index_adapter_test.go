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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Construction helpers
// ---------------------------------------------------------------------------

func newTestQRDBIndex(
	database, table, column, fieldType string,
	unique bool,
) *qrdbIndex {
	return newQRDBIndex(database, table, column, fieldType, unique)
}

// ---------------------------------------------------------------------------
// ID
// ---------------------------------------------------------------------------

func TestQRDBIndexID(t *testing.T) {
	idx := newTestQRDBIndex("mydb", "users", "Email", "string", false)
	assert.Equal(t, "mydb.users.Email", idx.ID())
}

func TestQRDBIndexID_Empty(t *testing.T) {
	idx := newTestQRDBIndex("", "", "", "string", false)
	assert.Equal(t, "..", idx.ID())
}

// ---------------------------------------------------------------------------
// Database / Table
// ---------------------------------------------------------------------------

func TestQRDBIndexDatabase(t *testing.T) {
	idx := newTestQRDBIndex("testdb", "orders", "CustomerID", "uint64", false)
	assert.Equal(t, "testdb", idx.Database())
}

func TestQRDBIndexDatabase_Empty(t *testing.T) {
	idx := newTestQRDBIndex("", "kv", "field", "string", false)
	assert.Equal(t, "", idx.Database())
}

func TestQRDBIndexTable(t *testing.T) {
	idx := newTestQRDBIndex("db", "products", "SKU", "string", true)
	assert.Equal(t, "products", idx.Table())
}

// ---------------------------------------------------------------------------
// Expressions
// ---------------------------------------------------------------------------

func TestQRDBIndexExpressions(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	exprs := idx.Expressions()
	require.Len(t, exprs, 1)
	assert.Equal(t, "users.Email", exprs[0])
}

func TestQRDBIndexExpressions_UnderscoreField(t *testing.T) {
	idx := newTestQRDBIndex("db", "events", "event_type", "string", false)
	exprs := idx.Expressions()
	require.Len(t, exprs, 1)
	assert.Equal(t, "events.event_type", exprs[0])
}

// ---------------------------------------------------------------------------
// Type flags
// ---------------------------------------------------------------------------

func TestQRDBIndexIsUnique_True(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", true)
	assert.True(t, idx.IsUnique())
}

func TestQRDBIndexIsUnique_False(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "City", "string", false)
	assert.False(t, idx.IsUnique())
}

func TestQRDBIndexNotSpatial(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Lat", "float64", false)
	assert.False(t, idx.IsSpatial())
}

func TestQRDBIndexNotFullText(t *testing.T) {
	idx := newTestQRDBIndex("db", "articles", "Body", "string", false)
	assert.False(t, idx.IsFullText())
}

func TestQRDBIndexNotVector(t *testing.T) {
	idx := newTestQRDBIndex("db", "items", "Embedding", "string", false)
	assert.False(t, idx.IsVector())
}

func TestQRDBIndexNotGenerated(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	assert.False(t, idx.IsGenerated())
}

// ---------------------------------------------------------------------------
// IndexType / Comment / PrefixLengths
// ---------------------------------------------------------------------------

func TestQRDBIndexType(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	assert.Equal(t, "BTREE", idx.IndexType())
}

func TestQRDBIndexComment_Default(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	assert.Equal(t, "", idx.Comment())
}

func TestQRDBIndexComment_Set(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	idx.comment = "user email lookup"
	assert.Equal(t, "user email lookup", idx.Comment())
}

func TestQRDBIndexPrefixLengths_Nil(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	assert.Nil(t, idx.PrefixLengths())
}

// ---------------------------------------------------------------------------
// CanSupport / CanSupportOrderBy
// ---------------------------------------------------------------------------

func TestQRDBIndexCanSupport(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	// CanSupport should always return true for QRDB indexes.
	assert.True(t, idx.CanSupport(nil))
}

func TestQRDBIndexCanSupportOrderBy_False(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	assert.False(t, idx.CanSupportOrderBy(nil))
}

// ---------------------------------------------------------------------------
// ColumnExpressionTypes
// ---------------------------------------------------------------------------

func TestQRDBIndexColumnExpressionTypes_StringField(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	cets := idx.ColumnExpressionTypes()
	require.Len(t, cets, 1)
	assert.Equal(t, "users.Email", cets[0].Expression)
	assert.NotNil(t, cets[0].Type)
}

func TestQRDBIndexColumnExpressionTypes_Uint64Field(t *testing.T) {
	idx := newTestQRDBIndex("db", "orders", "CustomerID", "uint64", false)
	cets := idx.ColumnExpressionTypes()
	require.Len(t, cets, 1)
	assert.Equal(t, "orders.CustomerID", cets[0].Expression)
}

func TestQRDBIndexColumnExpressionTypes_Int64Field(t *testing.T) {
	idx := newTestQRDBIndex("db", "events", "Timestamp", "int64", false)
	cets := idx.ColumnExpressionTypes()
	require.Len(t, cets, 1)
	assert.Equal(t, "events.Timestamp", cets[0].Expression)
}

func TestQRDBIndexColumnExpressionTypes_Float64Field(t *testing.T) {
	idx := newTestQRDBIndex("db", "metrics", "Value", "float64", false)
	cets := idx.ColumnExpressionTypes()
	require.Len(t, cets, 1)
	assert.Equal(t, "metrics.Value", cets[0].Expression)
}

func TestQRDBIndexColumnExpressionTypes_BooleanField(t *testing.T) {
	idx := newTestQRDBIndex("db", "users", "Active", "boolean", false)
	cets := idx.ColumnExpressionTypes()
	require.Len(t, cets, 1)
	assert.Equal(t, "users.Active", cets[0].Expression)
}

// ---------------------------------------------------------------------------
// sql.Index interface compliance
// ---------------------------------------------------------------------------

func TestQRDBIndexImplementsSQLIndex(t *testing.T) {
	var _ sqle.Index = (*qrdbIndex)(nil)
	idx := newTestQRDBIndex("db", "users", "Email", "string", false)
	require.NotNil(t, idx)
}

// ---------------------------------------------------------------------------
// newQRDBIndex constructor
// ---------------------------------------------------------------------------

func TestNewQRDBIndex_IDFormat(t *testing.T) {
	idx := newQRDBIndex("mydb", "users", "Email", "string", false)
	assert.Equal(t, "mydb.users.Email", idx.id)
	assert.Equal(t, "mydb", idx.database)
	assert.Equal(t, "users", idx.table)
	assert.Equal(t, "Email", idx.column)
	assert.Equal(t, "string", idx.fieldType)
	assert.False(t, idx.unique)
}

func TestNewQRDBIndex_UniqueFlag(t *testing.T) {
	idx := newQRDBIndex("db", "users", "Email", "string", true)
	assert.True(t, idx.unique)
}
