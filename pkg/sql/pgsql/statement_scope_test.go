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
	"context"
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// TestPGWire_StatementResolvesEachTableOnce: the PostgreSQL wire delivers
// statement boundaries, so a statement resolves each table definition once,
// as it does on the MySQL wire.
func TestPGWire_StatementResolvesEachTableOnce(t *testing.T) {
	hs := sqltest.NewHarness()
	counting := hs.CountTableReads()
	prov := provider.NewProvider("qrdb", hs.KVSvc, provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")

	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	run := func(q string) {
		t.Helper()
		_, err := h.HandleParse(ctx, q)
		require.NoError(t, err, q)
	}

	run("CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))")
	run("INSERT INTO notes (id, body) VALUES (1, 'a')")
	counting.Reset()
	run("UPDATE notes SET body = 'b' WHERE id = 1")
	assert.Equal(t, int32(1), counting.Gets(), "an UPDATE reads its table's definition once")

	run("ALTER TABLE notes ADD COLUMN extra INT")
	run("UPDATE notes SET extra = 1 WHERE id = 1")
}

// TestPGWire_PrepareResolvesEachTableOnce: describing a parameterized
// statement (PrepareQuery, at Parse) is a command too, so it resolves each
// table definition once, like the statement's execution.
func TestPGWire_PrepareResolvesEachTableOnce(t *testing.T) {
	hs := sqltest.NewHarness()
	counting := hs.CountTableReads()
	prov := provider.NewProvider("qrdb", hs.KVSvc, provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")
	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)

	_, err = h.HandleParse(ctx, "CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))")
	require.NoError(t, err)
	counting.Reset()
	_, err = h.HandleParse(ctx, "SELECT body FROM notes WHERE id = $1")
	require.NoError(t, err)
	assert.Equal(t, int32(1), counting.Gets(), "describing a statement reads its table's definition once")
}

// refusingSession is a session whose commands cannot begin.
type refusingSession struct {
	*sql.BaseSession
	ended int
}

func (r *refusingSession) CommandBegin() error { return errCommandRefused }
func (r *refusingSession) CommandEnd()         { r.ended++ }
func (r *refusingSession) SessionEnd()         {}

var errCommandRefused = errors.New("command refused")

func TestBeginCommand(t *testing.T) {
	sess := &refusingSession{BaseSession: sql.NewBaseSession()}
	_, err := beginCommand(sql.NewContext(context.Background(), sql.WithSession(sess)))
	require.ErrorIs(t, err, errCommandRefused, "a command that cannot begin fails the statement")

	plain := sql.NewContext(context.Background(), sql.WithSession(provider.NewSession(sql.NewBaseSession(), nil)))
	end, err := beginCommand(plain)
	require.NoError(t, err)
	end()
}

// newScopeHandler builds a PostgreSQL parse handler over a relational
// harness, and a connection context carrying its session.
func newScopeHandler(t *testing.T) (*ParseHandler, context.Context, *provider.QRDBSession) {
	t.Helper()
	hs := sqltest.NewHarness()
	prov := provider.NewProvider("qrdb", hs.KVSvc, provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")
	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	sess := connectionSessionFromContext(ctx)
	require.NotNil(t, sess)
	return h, ctx, sess
}

// TestPGWire_StatementWhileACommandIsOpenIsRefused: each place the
// PostgreSQL wire begins a command -- executing a statement at Parse,
// describing a parameterized one, and executing it at Execute -- fails the
// statement when its session's previous command has not ended, and the
// statement writes nothing.
func TestPGWire_StatementWhileACommandIsOpenIsRefused(t *testing.T) {
	h, ctx, sess := newScopeHandler(t)
	_, err := h.HandleParse(ctx, "CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))")
	require.NoError(t, err)

	require.NoError(t, sess.CommandBegin())
	var inProgress *provider.CommandInProgressError

	_, err = h.HandleParse(ctx, "INSERT INTO notes (id, body) VALUES (1, 'a')")
	require.ErrorAs(t, err, &inProgress, "a statement executed at Parse")

	const query = "SELECT body FROM notes WHERE id = $1"
	_, err = h.HandleParse(ctx, query)
	require.ErrorAs(t, err, &inProgress, "a parameterized statement's describe")

	translated, err := h.translator.Translate(query)
	require.NoError(t, err)
	err = h.executeDeferredCallback(ctx, &mockDataWriter{}, nil, query, translated, nil, nil, nil)
	require.ErrorAs(t, err, &inProgress, "a parameterized statement's execution")
	sess.CommandEnd()

	rows := &mockDataWriter{}
	require.NoError(t, h.executeDeferredCallback(ctx, rows, nil, "SELECT * FROM notes", "SELECT * FROM notes", nil, nil, nil))
	assert.Zero(t, rows.rowCount, "the refused INSERT wrote nothing")
}

// TestPGWire_SequenceDDL: CREATE and ALTER SEQUENCE are accepted as no-ops
// and DROP SEQUENCE resets the counter, so a re-created sequence starts at 1
// again, as PostgreSQL's drop-and-recreate does.
func TestPGWire_SequenceDDL(t *testing.T) {
	h, ctx, _ := newScopeHandler(t)
	nextval := func() any {
		t.Helper()
		sqlCtx := h.buildSQLContext(ctx)
		_, iter, _, err := h.sqlEngine.Query(sqlCtx, "SELECT NEXTVAL('orders_seq')")
		require.NoError(t, err)
		row, err := iter.Next(sqlCtx)
		require.NoError(t, err)
		require.NoError(t, iter.Close(sqlCtx))
		return row[0]
	}

	for _, q := range []string{"CREATE SEQUENCE orders_seq", "ALTER SEQUENCE orders_seq AS bigint"} {
		_, err := h.HandleParse(ctx, q)
		require.NoError(t, err, q)
	}
	assert.EqualValues(t, 1, nextval())
	assert.EqualValues(t, 2, nextval())

	_, err := h.HandleParse(ctx, "DROP SEQUENCE IF EXISTS orders_seq")
	require.NoError(t, err)
	assert.EqualValues(t, 1, nextval(), "a dropped sequence starts again at 1")

	handled, err := h.handleSequenceDDL(h.buildSQLContext(ctx), "no_such_db", "DROP SEQUENCE orders_seq")
	assert.True(t, handled)
	require.Error(t, err, "DROP SEQUENCE in a database that does not exist")
}
