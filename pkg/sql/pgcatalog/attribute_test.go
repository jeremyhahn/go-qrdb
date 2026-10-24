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
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttributeReportsEveryColumnJoinableToPgClass covers what makes
// pg_attribute usable at all: gorm.io/driver/postgres reaches a table's
// columns by joining attrelid to pg_class.oid, so the two must agree exactly.
// A mismatch yields no rows and every column looks absent.
func TestAttributeReportsEveryColumnJoinableToPgClass(t *testing.T) {
	catalog, ctx := newIndexesCatalog(t)

	byTable := map[uint32][]string{}
	for _, row := range scan(t, ctx, assigned(t, AttributeRelation, catalog)) {
		byTable[row[0].(uint32)] = append(byTable[row[0].(uint32)], row[1].(string)) //nolint:errcheck // declared column types
	}

	assert.Equal(t, []string{"id", "email", "name"}, byTable[ClassOID("idxtest", "people")],
		"columns are reported in declaration order, addressed by the same OID pg_class reports")
	assert.Equal(t, []string{"id", "note"}, byTable[ClassOID("idxtest", "orders")])
}

// TestAttributeNumbersColumnsFromOne pins the convention PostgreSQL clients
// filter on: `WHERE a.attnum > 0` is how they exclude system columns, so a
// zero-based attnum would hide every table's first column.
func TestAttributeNumbersColumnsFromOne(t *testing.T) {
	catalog, ctx := newIndexesCatalog(t)

	seen := map[string]int16{}
	for _, row := range scan(t, ctx, assigned(t, AttributeRelation, catalog)) {
		if row[0].(uint32) != ClassOID("idxtest", "people") { //nolint:errcheck // declared column type
			continue
		}
		seen[row[1].(string)] = row[4].(int16) //nolint:errcheck // declared column types
		assert.Equal(t, int32(-1), row[3], "no column carries a type modifier")
		assert.Equal(t, false, row[6], "no column is dropped")
	}

	assert.Equal(t, map[string]int16{"id": 1, "email": 2, "name": 3}, seen)
}

// TestFormatTypeNameRendersSQLSpellings covers the strings the function
// returns. gorm.io/driver/postgres parses them to decide whether a column
// needs altering, so the catalogue name ("int8") would be read as a different
// type from the SQL name ("bigint") it expects.
func TestFormatTypeNameRendersSQLSpellings(t *testing.T) {
	cases := map[uint32]string{
		16:   "boolean",
		20:   "bigint",
		21:   "smallint",
		23:   "integer",
		25:   "text",
		701:  "double precision",
		1043: "character varying",
		1184: "timestamp with time zone",
		1700: "numeric",
	}
	for oid, want := range cases {
		assert.Equalf(t, want, FormatTypeName(oid), "OID %d", oid)
	}

	assert.Equal(t, "???", FormatTypeName(999999),
		"an OID this catalogue does not serve gets PostgreSQL's own answer for an unknown type, not an error")
}

// TestFormatTypeEvaluatesItsOID drives the registered function the way the
// analyzer does.
func TestFormatTypeEvaluatesItsOID(t *testing.T) {
	ctx := sqle.NewEmptyContext()

	fn := NewFormatType()
	two, ok := fn.(sqle.Function2)
	require.True(t, ok)

	call := two.Fn(ctx,
		expression.NewLiteral(uint32(20), types.Uint32),
		expression.NewLiteral(int32(-1), types.Int32))

	value, err := call.Eval(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "bigint", value)

	assert.Equal(t, FormatTypeFunction, two.Name)
	assert.True(t, call.Resolved())
	assert.True(t, call.IsNullable(ctx))
	assert.Equal(t, types.LongText, call.Type(ctx))
	assert.Len(t, call.Children(), 2)
}

// TestFormatTypeOfNullIsNull covers the NULL case: an OID that is not there
// describes nothing, and NULL is what PostgreSQL returns.
func TestFormatTypeOfNullIsNull(t *testing.T) {
	ctx := sqle.NewEmptyContext()
	two := NewFormatType().(sqle.Function2) //nolint:errcheck // constructed directly above

	call := two.Fn(ctx,
		expression.NewLiteral(nil, types.Null),
		expression.NewLiteral(int32(-1), types.Int32))

	value, err := call.Eval(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, value)
}

// TestFormatTypeWithChildrenRebuilds covers the analyzer's rewrite path.
func TestFormatTypeWithChildrenRebuilds(t *testing.T) {
	ctx := sqle.NewEmptyContext()
	two := NewFormatType().(sqle.Function2) //nolint:errcheck // constructed directly above
	call := two.Fn(ctx,
		expression.NewLiteral(uint32(25), types.Uint32),
		expression.NewLiteral(int32(-1), types.Int32))

	rebuilt, err := call.WithChildren(ctx,
		expression.NewLiteral(uint32(16), types.Uint32),
		expression.NewLiteral(int32(-1), types.Int32))
	require.NoError(t, err)

	value, err := rebuilt.Eval(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "boolean", value)

	_, err = call.WithChildren(ctx, expression.NewLiteral(uint32(16), types.Uint32))
	require.Error(t, err, "the arity is part of the contract")
}

// failingExpression is a resolved literal whose evaluation fails with err.
type failingExpression struct {
	sqle.Expression
	err error
}

func (f failingExpression) Eval(*sqle.Context, sqle.Row) (interface{}, error) {
	return nil, f.err
}

// TestFormatTypeEvalPropagatesAnArgumentFailure covers both arguments: a
// failure evaluating either one is returned, never a type name.
func TestFormatTypeEvalPropagatesAnArgumentFailure(t *testing.T) {
	ctx := sqle.NewEmptyContext()
	want := &unreadableDatabaseError{Name: "format_type"}
	oid := expression.NewLiteral(uint32(20), types.Uint32)
	typmod := expression.NewLiteral(int32(-1), types.Int32)

	cases := []struct {
		name        string
		oid, typmod sqle.Expression
	}{
		{"oid", failingExpression{Expression: oid, err: want}, typmod},
		{"typmod", oid, failingExpression{Expression: typmod, err: want}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			two := NewFormatType().(sqle.Function2) //nolint:errcheck // constructed directly above
			value, err := two.Fn(ctx, tc.oid, tc.typmod).Eval(ctx, nil)
			assert.Nil(t, value)
			require.ErrorIs(t, err, want)
		})
	}
}

// TestFormatTypeStringNamesTheCall covers the expression's rendering in
// plans and error messages.
func TestFormatTypeStringNamesTheCall(t *testing.T) {
	two := NewFormatType().(sqle.Function2) //nolint:errcheck // constructed directly above
	call := two.Fn(sqle.NewEmptyContext(),
		expression.NewLiteral(uint32(20), types.Uint32),
		expression.NewLiteral(int32(-1), types.Int32))
	assert.Equal(t, FormatTypeFunction+"(20, -1)", call.String())
}

func TestTypeOIDErrorNamesTheFunctionAndValue(t *testing.T) {
	err := &TypeOIDError{Value: "not-an-oid"}
	assert.Equal(t, "pgcatalog: format_type cannot read not-an-oid as a type OID", err.Error())
}

func TestTypeOIDErrorIsFoundThroughAWrap(t *testing.T) {
	err := errors.Join(errors.New("query failed"), &TypeOIDError{Value: int64(-1)})
	var want *TypeOIDError
	require.ErrorAs(t, err, &want)
	assert.Equal(t, int64(-1), want.Value)
}
