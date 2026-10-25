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
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// The extended query protocol separates Parse from Execute: Parse prepares a
// statement, and the statement runs once per Execute of a portal bound from
// it, seeing the data as of that execution. These tests Parse once (pgconn's
// Prepare: Parse and Describe) and Bind/Execute many times (ExecPrepared),
// which is what pgx's default statement cache does.

// extendedConn returns a connection to a server over a kv table holding rows.
func extendedConn(t *testing.T, rows map[string][]byte, opts ...ServerOption) (*pgx.Conn, context.Context) {
	t.Helper()
	conn := pgWireConn(t, &mockKVService{scanData: rows}, pgx.QueryExecModeCacheStatement, opts...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return conn, ctx
}

// kvRowCount counts the kv table's rows over the simple query protocol.
func kvRowCount(t *testing.T, ctx context.Context, conn *pgx.Conn) int {
	t.Helper()
	var n int
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM kv", pgx.QueryExecModeSimpleProtocol).Scan(&n))
	return n
}

func TestPostgresWire_PreparedSelectSeesDataAsOfEachExecute(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{"a": []byte("1")})
	pc := conn.PgConn()
	_, err := pc.Prepare(ctx, "sel", `SELECT "key" FROM kv`, nil)
	require.NoError(t, err)

	run := func() int {
		res := pc.ExecPrepared(ctx, "sel", nil, nil, nil).Read()
		require.NoError(t, res.Err)
		return len(res.Rows)
	}
	assert.Equal(t, 1, run(), "the first execution")

	_, err = conn.Exec(ctx, `INSERT INTO kv ("key", "value") VALUES ('b', '2')`)
	require.NoError(t, err)
	assert.Equal(t, 2, run(), "an execution after an insert sees the inserted row")
	assert.Equal(t, 2, run(), "and so does the next one")
}

func TestPostgresWire_PreparedInsertRunsAtEachExecute(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{})
	pc := conn.PgConn()
	_, err := pc.Prepare(ctx, "ins", `INSERT INTO kv ("key", "value") VALUES ('k', 'v')`, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, kvRowCount(t, ctx, conn), "Parse prepares the insert and does not run it")

	for round := 1; round <= 2; round++ {
		res := pc.ExecPrepared(ctx, "ins", nil, nil, nil).Read()
		require.NoError(t, res.Err)
		assert.Equal(t, "INSERT 0 1", res.CommandTag.String(), "round %d", round)
		assert.Equal(t, 1, kvRowCount(t, ctx, conn), "round %d: the Execute ran the insert", round)
		_, err = conn.Exec(ctx, `DELETE FROM kv`)
		require.NoError(t, err)
	}
}

func TestPostgresWire_DescribeBeforeExecuteOfAParameterlessSelect(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{"a": []byte("1")})
	cases := map[string]string{
		"select": `SELECT "key", "value" FROM kv`,
		"with":   `WITH t AS (SELECT "key", "value" FROM kv) SELECT "key", "value" FROM t`,
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			desc, err := conn.PgConn().Prepare(ctx, name, query, nil)
			require.NoError(t, err)
			require.Len(t, desc.Fields, 2, "Describe answers the statement's columns before any Execute")
			assert.Equal(t, "key", desc.Fields[0].Name)
			assert.Equal(t, "value", desc.Fields[1].Name)

			res := conn.PgConn().ExecPrepared(ctx, name, nil, nil, nil).Read()
			require.NoError(t, res.Err)
			assert.Len(t, res.Rows, 1)
		})
	}
}

func TestPostgresWire_ParameterlessStatementTimesOutAtExecute(t *testing.T) {
	const timeout = 300 * time.Millisecond
	conn, ctx := extendedConn(t, map[string][]byte{}, WithQueryTimeout(timeout))
	pc := conn.PgConn()
	_, err := pc.Prepare(ctx, "slow", "SELECT pg_sleep(5)", nil)
	require.NoError(t, err, "Parse prepares the statement and does not run it")

	start := time.Now()
	res := pc.ExecPrepared(ctx, "slow", nil, nil, nil).Read()
	elapsed := time.Since(start)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(res.Err, &pgErr), "err = %v, want a PostgreSQL error", res.Err)
	assert.Equal(t, "57014", pgErr.Code)
	assert.Less(t, elapsed, 3*time.Second, "the execution is bounded by the %s timeout", timeout)
}

func TestPostgresWire_ExtendedParseRefusesSeveralStatements(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{})
	_, err := conn.PgConn().Prepare(ctx, "multi", `INSERT INTO kv ("key", "value") VALUES ('a', '1'); SELECT 1`, nil)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "err = %v, want a PostgreSQL error", err)
	assert.Equal(t, "42601", pgErr.Code)
	assert.Equal(t, "cannot insert multiple commands into a prepared statement", pgErr.Message)
	assert.Equal(t, 0, kvRowCount(t, ctx, conn), "no statement of the refused Parse ran")
}

func TestPostgresWire_SimpleQueryRunsEveryStatementOnce(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{})
	_, err := conn.Exec(ctx, `INSERT INTO kv ("key", "value") VALUES ('a', '1'); INSERT INTO kv ("key", "value") VALUES ('b', '2')`)
	require.NoError(t, err)
	assert.Equal(t, 2, kvRowCount(t, ctx, conn))
}

func TestPostgresWire_ExtendedTransactionControl(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{})
	pc := conn.PgConn()
	for _, query := range []string{"BEGIN", `SELECT "key" FROM kv`, "COMMIT", "BEGIN", "ROLLBACK", "START TRANSACTION", "END"} {
		res := pc.ExecParams(ctx, query, nil, nil, nil, nil).Read()
		require.NoError(t, res.Err, query)
	}
	assert.Equal(t, 0, kvRowCount(t, ctx, conn))
}

// catalogConn is an extendedConn whose server has a relational catalog, so
// it serves tables and sequences.
func catalogConn(t *testing.T) (*pgx.Conn, context.Context) {
	t.Helper()
	hs, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	return extendedConn(t, map[string][]byte{}, WithServiceRegistry(hs.Registry))
}

// simpleExec runs each query through the simple query protocol, which
// executes a statement in the same step that parses it.
func simpleExec(t *testing.T, ctx context.Context, conn *pgx.Conn, queries ...string) {
	t.Helper()
	for _, query := range queries {
		_, err := conn.Exec(ctx, query, pgx.QueryExecModeSimpleProtocol)
		require.NoError(t, err, query)
	}
}

func nextOrderID(t *testing.T, ctx context.Context, conn *pgx.Conn) int64 {
	t.Helper()
	var n int64
	require.NoError(t, conn.QueryRow(ctx, "SELECT nextval('orders_seq')", pgx.QueryExecModeSimpleProtocol).Scan(&n))
	return n
}

// TestPostgresWire_PreparedDropSequenceRunsAtEachExecute: DROP SEQUENCE is
// served from the sequence catalog rather than the engine, and in the
// extended protocol it resets the sequence at each Execute, never at Parse.
func TestPostgresWire_PreparedDropSequenceRunsAtEachExecute(t *testing.T) {
	conn, ctx := catalogConn(t)
	pc := conn.PgConn()
	simpleExec(t, ctx, conn, "CREATE SEQUENCE orders_seq")
	assert.EqualValues(t, 1, nextOrderID(t, ctx, conn))
	assert.EqualValues(t, 2, nextOrderID(t, ctx, conn))

	_, err := pc.Prepare(ctx, "drop", "DROP SEQUENCE orders_seq", nil)
	require.NoError(t, err)
	assert.EqualValues(t, 3, nextOrderID(t, ctx, conn), "Parse prepares the DROP and does not reset the sequence")

	for round := 1; round <= 2; round++ {
		res := pc.ExecPrepared(ctx, "drop", nil, nil, nil).Read()
		require.NoError(t, res.Err, "round %d", round)
		assert.EqualValues(t, 1, nextOrderID(t, ctx, conn), "round %d: the Execute reset the sequence", round)
		assert.EqualValues(t, 2, nextOrderID(t, ctx, conn), "round %d", round)
	}
}

// TestPostgresWire_SimpleDropSequenceRunsOnce: the simple query protocol
// still resets the sequence in the one step that parses DROP SEQUENCE.
func TestPostgresWire_SimpleDropSequenceRunsOnce(t *testing.T) {
	conn, ctx := catalogConn(t)
	simpleExec(t, ctx, conn, "CREATE SEQUENCE orders_seq", "ALTER SEQUENCE orders_seq AS bigint")
	assert.EqualValues(t, 1, nextOrderID(t, ctx, conn))
	simpleExec(t, ctx, conn, "DROP SEQUENCE orders_seq")
	assert.EqualValues(t, 1, nextOrderID(t, ctx, conn), "the DROP reset the sequence")
	assert.EqualValues(t, 2, nextOrderID(t, ctx, conn))
}

// createPeopleAndPets creates people and a pets table whose foreign key
// aims at it, so dropping people needs CASCADE.
func createPeopleAndPets(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	simpleExec(t, ctx, conn,
		"DROP TABLE IF EXISTS pets",
		"CREATE TABLE people (id BIGINT PRIMARY KEY)",
		"CREATE TABLE pets (id BIGINT PRIMARY KEY, owner_id BIGINT, "+
			"CONSTRAINT fk_pets_owner FOREIGN KEY (owner_id) REFERENCES people (id))")
}

// TestPostgresWire_PreparedDropTableCascadeRunsAtEachExecute: the foreign
// keys CASCADE drops are dropped at each Execute of the statement, never at
// Parse.
func TestPostgresWire_PreparedDropTableCascadeRunsAtEachExecute(t *testing.T) {
	conn, ctx := catalogConn(t)
	pc := conn.PgConn()
	createPeopleAndPets(t, ctx, conn)

	_, err := pc.Prepare(ctx, "drop", "DROP TABLE people CASCADE", nil)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO pets (id, owner_id) VALUES (1, 99)", pgx.QueryExecModeSimpleProtocol)
	require.Error(t, err, "Parse prepares the DROP and does not drop the foreign key")

	for round := 1; round <= 2; round++ {
		res := pc.ExecPrepared(ctx, "drop", nil, nil, nil).Read()
		require.NoError(t, res.Err, "round %d: the Execute dropped the foreign key, then the table", round)
		simpleExec(t, ctx, conn, "INSERT INTO pets (id, owner_id) VALUES (1, 99)")
		createPeopleAndPets(t, ctx, conn)
	}
}

// TestPostgresWire_SimpleDropTableCascadeRunsOnce: the simple query protocol
// still drops the foreign keys and the table in one step.
func TestPostgresWire_SimpleDropTableCascadeRunsOnce(t *testing.T) {
	conn, ctx := catalogConn(t)
	createPeopleAndPets(t, ctx, conn)
	simpleExec(t, ctx, conn, "DROP TABLE people CASCADE", "INSERT INTO pets (id, owner_id) VALUES (1, 99)")
	_, err := conn.Exec(ctx, "SELECT id FROM people", pgx.QueryExecModeSimpleProtocol)
	require.Error(t, err, "the table was dropped")
}

// rangeReadRefused reports whether a range read inside a transaction is
// refused, which it is under qrdb_isolation='written+reads'. The notes table
// it reads is created by the caller.
func rangeReadRefused(t *testing.T, ctx context.Context, conn *pgx.Conn) bool {
	t.Helper()
	simpleExec(t, ctx, conn, "BEGIN")
	_, err := conn.Exec(ctx, "SELECT id FROM notes", pgx.QueryExecModeSimpleProtocol)
	simpleExec(t, ctx, conn, "ROLLBACK")
	return err != nil
}

// TestPostgresWire_PreparedSetRunsAtEachExecute: a SET of a QRDB setting
// changes the session at each Execute, never at Parse.
func TestPostgresWire_PreparedSetRunsAtEachExecute(t *testing.T) {
	conn, ctx := catalogConn(t)
	pc := conn.PgConn()
	simpleExec(t, ctx, conn, "CREATE TABLE notes (id BIGINT PRIMARY KEY)")
	_, err := pc.Prepare(ctx, "set", "SET qrdb_isolation TO 'written+reads'", nil)
	require.NoError(t, err)
	assert.False(t, rangeReadRefused(t, ctx, conn), "Parse prepares the SET and does not change the session")

	for round := 1; round <= 2; round++ {
		res := pc.ExecPrepared(ctx, "set", nil, nil, nil).Read()
		require.NoError(t, res.Err, "round %d", round)
		assert.True(t, rangeReadRefused(t, ctx, conn), "round %d: the Execute changed the session", round)
		simpleExec(t, ctx, conn, "SET qrdb_isolation TO 'written'")
		assert.False(t, rangeReadRefused(t, ctx, conn), "round %d", round)
	}
}

// TestPostgresWire_SimpleSetRunsOnce: the simple query protocol still
// changes the session in the one step that parses the SET, and a SET of a
// PostgreSQL session setting is still accepted as a no-op.
func TestPostgresWire_SimpleSetRunsOnce(t *testing.T) {
	conn, ctx := catalogConn(t)
	simpleExec(t, ctx, conn, "CREATE TABLE notes (id BIGINT PRIMARY KEY)", "SET search_path TO public", "SET qrdb_isolation TO 'written+reads'")
	assert.True(t, rangeReadRefused(t, ctx, conn))
}

// TestPostgresWire_DescribeOfAPreparedShow: a prepared SHOW is described
// before it is executed and completes at each Execute.
func TestPostgresWire_DescribeOfAPreparedShow(t *testing.T) {
	conn, ctx := extendedConn(t, map[string][]byte{})
	pc := conn.PgConn()
	sd, err := pc.Prepare(ctx, "show", "SHOW search_path", nil)
	require.NoError(t, err)
	assert.Empty(t, sd.Fields, "a SHOW answers no columns")
	for round := 1; round <= 2; round++ {
		res := pc.ExecPrepared(ctx, "show", nil, nil, nil).Read()
		require.NoError(t, res.Err, "round %d", round)
		assert.Equal(t, "OK", res.CommandTag.String(), "round %d", round)
	}
}
