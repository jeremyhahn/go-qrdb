// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"testing"

	"github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatementReturnsRows_Queries(t *testing.T) {
	for _, query := range []string{
		"SELECT 1",
		"WITH t AS (SELECT 1 AS a) SELECT a FROM t",
		"VALUES ROW(1, 2)",
		"SELECT 1 UNION SELECT 2",
		"EXPLAIN SELECT 1",
		"INSERT INTO kv (`key`, `value`) VALUES ('a', '1') RETURNING `key`",
	} {
		assert.True(t, statementReturnsRows(query, nil), query)
	}
}

func TestStatementReturnsRows_Commands(t *testing.T) {
	for _, query := range []string{
		"INSERT INTO kv (`key`, `value`) VALUES ('a', '1')",
		"UPDATE kv SET `value` = '2'",
		"DELETE FROM kv",
		"CREATE TABLE notes (id BIGINT PRIMARY KEY)",
		"BEGIN",
	} {
		assert.False(t, statementReturnsRows(query, nil), query)
	}
}

func TestStatementReturnsRows_PreparsedReturning(t *testing.T) {
	const query = "INSERT INTO kv (`key`, `value`) VALUES ('a', '1') RETURNING `key`"
	stmt, err := sqlparser.Parse(query)
	require.NoError(t, err)
	assert.True(t, statementReturnsRows(query, stmt))
}

func TestStatementReturnsRows_UnparsableFallsBackToSelectPrefix(t *testing.T) {
	assert.True(t, statementReturnsRows("SELECT !@# garbage", nil))
	assert.False(t, statementReturnsRows("!@# garbage", nil))
}

// sessionContext returns a context carrying a connection session, as the
// wire server's session handler builds one per connection.
func sessionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	require.NotNil(t, connectionSessionFromContext(ctx))
	return ctx
}

func TestStatementScope_SetsFlagsForOneExecution(t *testing.T) {
	ctx := sessionContext(t)
	session := connectionSessionFromContext(ctx)

	for execution := 1; execution <= 2; execution++ {
		exit := statementScope{restartIdentity: true, indexOrigin: true}.enter(ctx)
		assert.True(t, session.StatementRestartIdentity(), "execution %d", execution)
		assert.True(t, session.StatementIndexOrigin(), "execution %d", execution)
		exit()
		assert.False(t, session.StatementRestartIdentity(), "execution %d: cleared after it", execution)
		assert.False(t, session.StatementIndexOrigin(), "execution %d: cleared after it", execution)
	}
}

func TestStatementScope_ZeroScopeSetsNothing(t *testing.T) {
	ctx := sessionContext(t)
	session := connectionSessionFromContext(ctx)

	exit := statementScope{}.enter(ctx)
	assert.False(t, session.StatementRestartIdentity())
	assert.False(t, session.StatementIndexOrigin())
	exit()
	assert.False(t, session.StatementRestartIdentity())
	assert.False(t, session.StatementIndexOrigin())
}

func TestStatementScope_NoSession(t *testing.T) {
	exit := statementScope{restartIdentity: true, indexOrigin: true}.enter(context.Background())
	require.NotNil(t, exit)
	assert.NotPanics(t, exit)
}
