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

package engine

import (
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
	ast "github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslateColumnTypeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "serial gains auto increment", in: "serial", want: "INT AUTO_INCREMENT"},
		{name: "bigserial gains auto increment", in: " BIGSERIAL ", want: "BIGINT AUTO_INCREMENT"},
		{name: "kept modifier", in: "numeric(10, 2)", want: "DECIMAL(10, 2)"},
		{name: "default modifier", in: "numeric", want: "DECIMAL(65,30)"},
		{name: "multi-word phrase", in: "timestamp   with time zone", want: "DATETIME(6)"},
		{name: "modifier dropped when not kept", in: "uuid(4)", want: "CHAR(36)"},
		{name: "unknown passes through trimmed", in: "  VARCHAR(64) ", want: "VARCHAR(64)"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TranslateColumnTypeText(tt.in))
		})
	}
}

func TestSplitUpdateDeleteReturning(t *testing.T) {
	tests := []struct {
		name          string
		query         string
		wantHead      string
		wantReturning string
		wantOK        bool
	}{
		{
			name:          "update",
			query:         "UPDATE t SET a = 1 WHERE id = 2 RETURNING id, a;",
			wantHead:      "UPDATE t SET a = 1 WHERE id = 2",
			wantReturning: "id, a",
			wantOK:        true,
		},
		{
			name:          "delete",
			query:         "delete from t where id = 3 returning *",
			wantHead:      "delete from t where id = 3",
			wantReturning: "*",
			wantOK:        true,
		},
		{name: "insert is not split", query: "INSERT INTO t VALUES (1) RETURNING id"},
		{name: "select is not split", query: "SELECT 1"},
		{name: "no returning clause", query: "UPDATE t SET a = 1"},
		{name: "returning only in a literal", query: "UPDATE t SET a = 'x RETURNING y'"},
		{name: "empty returning list", query: "DELETE FROM t RETURNING ;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head, returning, ok := SplitUpdateDeleteReturning(tt.query)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantHead, head)
			assert.Equal(t, tt.wantReturning, returning)
		})
	}
}

const havingSubqueryQuery = "SELECT name FROM users GROUP BY name HAVING COUNT(*) > (SELECT COUNT(*) FROM users WHERE users.age > 1)"

func TestHavingSubqueryParser_EntryPoints(t *testing.T) {
	p := newHavingSubqueryParser(sql.NewMysqlParser())
	ctx := sql.NewEmptyContext()

	parsers := map[string]func(string) (ast.Statement, error){
		"ParseSimple": p.ParseSimple,
		"Parse": func(q string) (ast.Statement, error) {
			stmt, _, _, err := p.Parse(ctx, q, false)
			return stmt, err
		},
		"ParseOneWithOptions": func(q string) (ast.Statement, error) {
			stmt, _, err := p.ParseOneWithOptions(ctx, q, ast.ParserOptions{})
			return stmt, err
		},
		"ParseWithOptions": func(q string) (ast.Statement, error) {
			stmt, _, _, err := p.ParseWithOptions(ctx, q, ';', false, ast.ParserOptions{})
			return stmt, err
		},
	}
	for name, parse := range parsers {
		t.Run(name+"/repairs having subquery", func(t *testing.T) {
			stmt, err := parse(havingSubqueryQuery)
			require.NoError(t, err)
			rendered := ast.String(stmt)
			assert.Contains(t, rendered, "__qrdb_hsq")
			assert.NotContains(t, rendered, "users.age", "qualifier must follow the alias")
		})
		t.Run(name+"/syntax error passes through", func(t *testing.T) {
			stmt, err := parse("SELEC broken FROM")
			require.Error(t, err)
			assert.Nil(t, stmt)
		})
	}
}

// sequenceDB is a memory database that also allocates sequence values,
// standing in for the qrdb database the nextval function resolves.
type sequenceDB struct {
	*memory.Database
	next int64
	err  error
	seen []string
}

func (d *sequenceDB) NextSequenceValue(_ *sql.Context, name string) (int64, error) {
	d.seen = append(d.seen, name)
	if d.err != nil {
		return 0, d.err
	}
	d.next++
	return d.next, nil
}

var _ sequenceValueDatabase = (*sequenceDB)(nil)

// failingExpr is a resolved expression whose evaluation always fails.
type failingExpr struct {
	sql.Expression
	err error
}

func (f failingExpr) Eval(*sql.Context, sql.Row) (interface{}, error) { return nil, f.err }

func newNextvalContext(t *testing.T, db sql.Database) (*sql.Context, sql.DatabaseProvider) {
	t.Helper()
	provider := memory.NewDBProvider(db)
	ctx := sql.NewContext(t.Context(), sql.WithSession(memory.NewSession(sql.NewBaseSession(), provider)))
	ctx.SetCurrentDatabase(db.Name())
	return ctx, provider
}

func newNextval(t *testing.T, provider sql.DatabaseProvider, arg sql.Expression) sql.Expression {
	t.Helper()
	fn, ok := newNextvalFunction(provider).(sql.Function1)
	require.True(t, ok)
	return fn.Fn(sql.NewEmptyContext(), arg)
}

func TestNextval_ExpressionContract(t *testing.T) {
	arg := expression.NewLiteral("users_id_seq", types.LongText)
	expr := newNextval(t, nil, arg)

	assert.True(t, expr.Resolved())
	assert.Equal(t, "nextval('users_id_seq')", expr.String())
	assert.Equal(t, types.Int64, expr.Type(nil))
	assert.False(t, expr.IsNullable(nil))
	nd, ok := expr.(sql.NonDeterministicExpression)
	require.True(t, ok)
	assert.True(t, nd.IsNonDeterministic())
	assert.Equal(t, []sql.Expression{arg}, expr.Children())

	other := expression.NewLiteral("orders_id_seq", types.LongText)
	replaced, err := expr.WithChildren(nil, other)
	require.NoError(t, err)
	assert.Equal(t, []sql.Expression{other}, replaced.Children())

	for _, children := range [][]sql.Expression{nil, {arg, other}} {
		_, err := expr.WithChildren(nil, children...)
		require.Error(t, err)
		assert.True(t, sql.ErrInvalidChildrenNumber.Is(err), "got %v", err)
	}
}

func TestNextval_Eval(t *testing.T) {
	errArg := errors.New("arg failed")
	errAlloc := errors.New("allocate failed")

	t.Run("string and bytes names allocate", func(t *testing.T) {
		db := &sequenceDB{Database: memory.NewDatabase("seqdb")}
		ctx, provider := newNextvalContext(t, db)
		for i, arg := range []sql.Expression{
			expression.NewLiteral("s1", types.LongText),
			expression.NewLiteral([]byte("s2"), types.LongBlob),
		} {
			got, err := newNextval(t, provider, arg).Eval(ctx, nil)
			require.NoError(t, err)
			assert.Equal(t, int64(i+1), got)
		}
		assert.Equal(t, []string{"s1", "s2"}, db.seen)
	})

	t.Run("null name yields null", func(t *testing.T) {
		db := &sequenceDB{Database: memory.NewDatabase("seqdb")}
		ctx, provider := newNextvalContext(t, db)
		got, err := newNextval(t, provider, expression.NewLiteral(nil, types.Null)).Eval(ctx, nil)
		require.NoError(t, err)
		assert.Nil(t, got)
		assert.Empty(t, db.seen)
	})

	t.Run("argument error propagates", func(t *testing.T) {
		db := &sequenceDB{Database: memory.NewDatabase("seqdb")}
		ctx, provider := newNextvalContext(t, db)
		_, err := newNextval(t, provider, failingExpr{err: errArg}).Eval(ctx, nil)
		assert.ErrorIs(t, err, errArg)
	})

	t.Run("non-string name rejected", func(t *testing.T) {
		db := &sequenceDB{Database: memory.NewDatabase("seqdb")}
		ctx, provider := newNextvalContext(t, db)
		_, err := newNextval(t, provider, expression.NewLiteral(int64(7), types.Int64)).Eval(ctx, nil)
		require.Error(t, err)
		assert.True(t, sql.ErrInvalidArgumentDetails.Is(err), "got %v", err)
		assert.Empty(t, db.seen)
	})

	t.Run("unknown current database", func(t *testing.T) {
		db := &sequenceDB{Database: memory.NewDatabase("seqdb")}
		ctx, provider := newNextvalContext(t, db)
		ctx.SetCurrentDatabase("missing")
		_, err := newNextval(t, provider, expression.NewLiteral("s", types.LongText)).Eval(ctx, nil)
		require.Error(t, err)
		assert.True(t, sql.ErrDatabaseNotFound.Is(err), "got %v", err)
	})

	t.Run("database without sequences", func(t *testing.T) {
		ctx, provider := newNextvalContext(t, memory.NewDatabase("plain"))
		_, err := newNextval(t, provider, expression.NewLiteral("s", types.LongText)).Eval(ctx, nil)
		require.Error(t, err)
		assert.True(t, sql.ErrDatabaseNotFound.Is(err), "got %v", err)
	})

	t.Run("allocation error propagates", func(t *testing.T) {
		db := &sequenceDB{Database: memory.NewDatabase("seqdb"), err: errAlloc}
		ctx, provider := newNextvalContext(t, db)
		_, err := newNextval(t, provider, expression.NewLiteral("s", types.LongText)).Eval(ctx, nil)
		assert.ErrorIs(t, err, errAlloc)
	})
}
