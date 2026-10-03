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

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// timeoutDB starts a MySQL listener with the given query timeout and returns
// one held connection to it.
func timeoutDB(t *testing.T, timeout time.Duration) *sql.Conn {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithQueryTimeout(timeout),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.DBName = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestQueryTimeout_BoundsEveryStatement: with the connection watcher off (a
// client that disconnects no longer cancels its query), the query timeout is
// what bounds a statement -- a plain query and a prepared statement alike --
// and it is answered as MySQL's own ER_QUERY_TIMEOUT (3024), which clients
// already classify. A statement inside the timeout is unaffected.
func TestQueryTimeout_BoundsEveryStatement(t *testing.T) {
	const timeout = 300 * time.Millisecond
	conn := timeoutDB(t, timeout)
	ctx := context.Background()

	var one int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT 1").Scan(&one), "a statement inside the timeout")

	cases := map[string]func() error{
		"query": func() error {
			_, err := conn.ExecContext(ctx, "SELECT SLEEP(5)")
			return err
		},
		"prepared statement": func() error {
			stmt, err := conn.PrepareContext(ctx, "SELECT SLEEP(?)")
			if err != nil {
				return err
			}
			defer stmt.Close()
			_, err = stmt.ExecContext(ctx, 5)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			err := run()
			elapsed := time.Since(start)
			var myErr *gomysql.MySQLError
			if !errors.As(err, &myErr) || myErr.Number != 3024 {
				t.Fatalf("a statement past the timeout: err = %v after %s, want ER_QUERY_TIMEOUT (3024)", err, elapsed)
			}
			if elapsed > 3*time.Second {
				t.Fatalf("the statement ran %s, far past the %s timeout", elapsed, timeout)
			}
		})
	}
}
