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
	"context"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	ast "github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLockClauseParser_ForShare is red against an engine without the
// wrapper: the vitess grammar has no FOR SHARE production.
func TestLockClauseParser_ForShare_ParsesOnEveryEntryPoint(t *testing.T) {
	p := newLockClauseParser(sql.NewMysqlParser())
	const q = "SELECT * FROM t WHERE id = 1 FOR SHARE"

	stmt, err := p.ParseSimple(q)
	require.NoError(t, err)
	assertShareLock(t, stmt)

	stmt, _, _, err = p.Parse(sql.NewEmptyContext(), q, false)
	require.NoError(t, err)
	assertShareLock(t, stmt)

	stmt, _, _, err = p.ParseWithOptions(context.Background(), q, ';', false, ast.ParserOptions{})
	require.NoError(t, err)
	assertShareLock(t, stmt)

	stmt, _, err = p.ParseOneWithOptions(context.Background(), q, ast.ParserOptions{})
	require.NoError(t, err)
	assertShareLock(t, stmt)

	_, err = sql.NewMysqlParser().ParseSimple(q)
	require.Error(t, err, "the unwrapped grammar refuses FOR SHARE; the wrapper is what accepts it")
}

func assertShareLock(t *testing.T, stmt ast.Statement) {
	t.Helper()
	sel, ok := stmt.(*ast.Select)
	require.True(t, ok, "%T", stmt)
	require.NotNil(t, sel.Lock)
	assert.Equal(t, ast.ShareModeStr, sel.Lock.Type)
}

func TestLockClauseParser_ParseOneWithOptions_MapsIndexToOriginal(t *testing.T) {
	p := newLockClauseParser(sql.NewMysqlParser())
	const q = "SELECT * FROM t FOR SHARE; SELECT 2"
	_, idx, err := p.ParseOneWithOptions(context.Background(), q, ast.ParserOptions{})
	require.NoError(t, err)
	assert.Equal(t, " SELECT 2", q[idx:], "the index must address the text the caller passed")
}

func TestLockClauseParser_MultiStatementRemainder(t *testing.T) {
	p := newLockClauseParser(sql.NewMysqlParser())
	stmt, _, remainder, err := p.Parse(sql.NewEmptyContext(), "SELECT * FROM t FOR SHARE; SELECT * FROM u FOR SHARE", true)
	require.NoError(t, err)
	assertShareLock(t, stmt)
	next, err := p.ParseSimple(remainder)
	require.NoError(t, err)
	assertShareLock(t, next)
}

func TestEngine_ForShareAndForUpdate_Execute(t *testing.T) {
	run := newHavingEngine(t)
	for _, q := range []string{
		"SELECT name FROM users WHERE id = 1 FOR SHARE",
		"SELECT name FROM users WHERE id = 1 LOCK IN SHARE MODE",
		"SELECT name FROM users WHERE id = 1 FOR UPDATE",
		"SELECT name FROM users WHERE id = 1 FOR SHARE NOWAIT",
	} {
		_, err := run(q)
		require.NoError(t, err, q)
	}
}

func TestTranslate_PostgreSQLLockStrengths(t *testing.T) {
	tr := NewTranslator()
	cases := map[string]string{
		"SELECT * FROM t WHERE id = $1 FOR NO KEY UPDATE": "SELECT * FROM t WHERE id = ? FOR UPDATE",
		"SELECT * FROM t WHERE id = $1 for key share":     "SELECT * FROM t WHERE id = ? FOR SHARE",
		"SELECT * FROM t FOR UPDATE":                      "SELECT * FROM t FOR UPDATE",
		"SELECT * FROM t FOR SHARE":                       "SELECT * FROM t FOR SHARE",
		"SELECT 'FOR NO KEY UPDATE' FROM t":               "SELECT 'FOR NO KEY UPDATE' FROM t",
		"SELECT * FROM t FOR NO KEY UPDATE OF t NOWAIT":   "SELECT * FROM t FOR UPDATE OF t NOWAIT",
	}
	for in, want := range cases {
		got, err := tr.Translate(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}
