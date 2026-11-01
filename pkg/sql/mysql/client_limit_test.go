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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dolthub/vitess/go/mysql"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientLimitedMySQL starts a MySQL listener whose client-address bucket
// holds burst tokens and never refills within a test.
func clientLimitedMySQL(t *testing.T, burst int, dsnOpts ...func(*gomysql.Config)) *sql.DB {
	t.Helper()
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: burst}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithClientLimiter(l),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	for _, opt := range dsnOpts {
		opt(cfg)
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// requireUserLimitReached asserts err is the ERR packet MySQL defines for an
// exceeded resource limit: ER_USER_LIMIT_REACHED, SQLSTATE 42000.
func requireUserLimitReached(t *testing.T, err error) {
	t.Helper()
	var myErr *gomysql.MySQLError
	require.True(t, errors.As(err, &myErr), "want a MySQL ERR packet, got %v", err)
	require.Equal(t, uint16(mysql.ERUserLimitReached), myErr.Number)
	require.Equal(t, mysql.SSClientError, string(myErr.SQLState[:]))
	require.Contains(t, myErr.Message, "rate limit exceeded for client 127.0.0.1")
}

func TestClientLimit_RefusesStatementOverRate(t *testing.T) {
	db := clientLimitedMySQL(t, 2)
	conn, err := db.Conn(context.Background()) // first token: the connection
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	var one int
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&one)) // second token
	err = conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&one)
	requireUserLimitReached(t, err)
}

func TestClientLimit_RefusesConnectionOverRate(t *testing.T) {
	db := clientLimitedMySQL(t, 1)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The bucket is empty: a second connection from the same address is
	// refused while it is established.
	_, err = db.Conn(context.Background())
	requireUserLimitReached(t, err)
}

func TestClientLimit_RefusesPreparedExecutionOverRate(t *testing.T) {
	db := clientLimitedMySQL(t, 3)
	conn, err := db.Conn(context.Background()) // connection
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	stmt, err := conn.PrepareContext(context.Background(), "SELECT ?") // prepare
	require.NoError(t, err)
	t.Cleanup(func() { _ = stmt.Close() })
	var v int
	require.NoError(t, stmt.QueryRowContext(context.Background(), 7).Scan(&v)) // execute
	require.Equal(t, 7, v)
	requireUserLimitReached(t, stmt.QueryRowContext(context.Background(), 7).Scan(&v))
}

// TestClientLimit_RefusesPrepareOverRate pins that COM_STMT_PREPARE is
// charged too: with the burst spent by the connection, the prepare itself
// is refused with ER_USER_LIMIT_REACHED.
func TestClientLimit_RefusesPrepareOverRate(t *testing.T) {
	db := clientLimitedMySQL(t, 1)
	conn, err := db.Conn(context.Background()) // connection
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.PrepareContext(context.Background(), "SELECT ?")
	requireUserLimitReached(t, err)
}

// TestClientLimit_RefusesMultiStatementQueryOverRate pins the
// multi-statement path: a client that negotiates CLIENT_MULTI_STATEMENTS
// sends its queries through ComMultiQuery, which is charged like a single
// query.
func TestClientLimit_RefusesMultiStatementQueryOverRate(t *testing.T) {
	db := clientLimitedMySQL(t, 2, func(c *gomysql.Config) { c.MultiStatements = true })
	conn, err := db.Conn(context.Background()) // connection
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	var v int
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&v)) // statement
	require.Equal(t, 1, v)
	requireUserLimitReached(t, conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&v))
}

// TestClientLimitGate_ParsedQueryDelegatesToComQuery pins that a parsed
// query the gate admits reaches the chain's ComQuery, the path every other
// statement entry point takes once admitted. A gate with no limiter admits
// without reading the connection, so the double needs none.
func TestClientLimitGate_ParsedQueryDelegatesToComQuery(t *testing.T) {
	chain := &countingChain{}
	require.NoError(t, clientLimitGate{}.ParsedQuery(chain, nil, "SELECT 1", nil, nil))
	require.Equal(t, []string{"query"}, chain.calls)
}

// TestClientLimitGate_RunsBeforeEveryOtherInterceptor pins the gate's
// priority below the sealed-window gate's and the query timeout's, so a
// client over its rate is refused before either does any work for it.
func TestClientLimitGate_RunsBeforeEveryOtherInterceptor(t *testing.T) {
	gate := clientLimitGate{}.Priority()
	require.Less(t, gate, sealedQueryGate{}.Priority())
	require.Less(t, gate, queryTimeoutInterceptor{}.Priority())
}

func TestClientLimitRefusal_PassesOtherErrorsThrough(t *testing.T) {
	other := errors.New("other")
	require.Same(t, other, clientLimitRefusal(other))
	require.NoError(t, clientLimitRefusal(nil))
	require.NoError(t, admitClient(nil, nil))
}

func TestHandlerOptions_ClientLimiterAloneInstallsTheChain(t *testing.T) {
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1, Burst: 1}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	require.Len(t, handlerOptions(l, nil, nil, 0), 1)
	require.Empty(t, handlerOptions(nil, nil, nil, 0))
}
