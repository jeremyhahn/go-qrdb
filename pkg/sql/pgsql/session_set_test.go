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

//go:build qrdb_pgsql

package pgsql

import (
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// sessionValue reads a session variable of c's connection session.
func (c *pgLockConn) sessionValue(name string) any {
	c.t.Helper()
	sess := connectionSessionFromContext(c.ctx)
	require.NotNil(c.t, sess)
	v, err := sess.GetSessionVariable(sql.NewContext(c.ctx, sql.WithSession(sess)), name)
	require.NoError(c.t, err)
	return v
}

// SET LOCAL scopes a setting to the current transaction. Neither
// lock_timeout nor a qrdb_* setting has a transaction scope here, and
// accepting the statement as a no-op would leave the client believing a
// timeout is in force that is not. It is refused by name.
func TestPGWire_SetLocal_IsRefusedForRoutedSettings(t *testing.T) {
	h := newPGLockHandler(t)
	c := h.lockConn(t)
	c.must("BEGIN")
	for _, q := range []string{
		"SET LOCAL lock_timeout = '1s'",
		"SET LOCAL lock_timeout TO 500",
		"set local qrdb_concurrency = 'pessimistic'",
	} {
		err := c.exec(q)
		require.Error(t, err, q)
		assert.Equal(t, "0A000", psqlErrorCode(err), q)
		assert.Contains(t, err.Error(), "SET LOCAL", q)
	}
	// A SET LOCAL of an unrouted setting stays the no-op it always was.
	c.must("SET LOCAL search_path TO public")
	c.must("ROLLBACK")
	assert.EqualValues(t, provider.DefaultLockWaitTimeoutSeconds, c.sessionValue(provider.LockWaitTimeoutVariable))
}

// A simple-query message carrying several statements is split before any
// SET is routed, so each SET is read on its own and its value never runs
// into the next statement -- whatever the statements around it contain.
func TestPGWire_MultiStatementSimpleQuery_SplitsEverySet(t *testing.T) {
	h := newPGLockHandler(t)
	c := h.lockConn(t)

	c.must("SET lock_timeout = '3s'; SET qrdb_concurrency TO pessimistic")
	assert.EqualValues(t, 3, c.sessionValue(provider.LockWaitTimeoutVariable))
	assert.Equal(t, "pessimistic", c.sessionValue(provider.ConcurrencyVariable))

	// A quoted semicolon is not a statement boundary.
	c.must("SET application_name = 'a;b'; SET qrdb_concurrency = 'optimistic'")
	assert.Equal(t, "optimistic", c.sessionValue(provider.ConcurrencyVariable))

	// A "$1" inside a string literal is not a bind parameter, so the text is
	// still split: the SET is applied with its own value only.
	c.must("SET lock_timeout = '4s'; SELECT 'costs $1'")
	assert.EqualValues(t, 4, c.sessionValue(provider.LockWaitTimeoutVariable))
}

// A qrdb_* SET reaches the engine, so an unknown name or an invalid value is
// an error the client sees -- not the silent success every SET used to be.
func TestPGWire_SetUnknownQrdbSetting_IsAnError(t *testing.T) {
	h := newPGLockHandler(t)
	c := h.lockConn(t)
	require.Error(t, c.exec("SET qrdb_no_such_setting = 'x'"))
	require.Error(t, c.exec("SET qrdb_concurrency = 'eager'"))
	assert.Equal(t, provider.ConcurrencyDatabase, c.sessionValue(provider.ConcurrencyVariable))
}

func TestRouteSessionSet_RefusesMoreThanOneStatement(t *testing.T) {
	_, ok, err := routeSessionSet("SET qrdb_concurrency = 'pessimistic'; SELECT $1")
	assert.True(t, ok)
	var multi *MultipleStatementsError
	require.ErrorAs(t, err, &multi)
}
