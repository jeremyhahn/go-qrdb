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
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// timeoutPGConn starts a PostgreSQL listener with the given statement timeout
// and returns a connection that speaks mode.
func timeoutPGConn(t *testing.T, timeout time.Duration, mode pgx.QueryExecMode) *pgx.Conn {
	t.Helper()
	serverTLS, clientTLS := pgMTLS(t)
	mw := authmw.New(pgAllowAll{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(&pgFixedIdentity{
		identity: &auth.Identity{Subject: "alice", TenantID: "acme"},
	}))
	srv := NewServer(&mockKVService{}, nil, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithQueryTimeout(timeout),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	cfg, err := pgx.ParseConfig("postgres://alice@" + srv.Addr() + "/qrdb?sslmode=require")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.TLSConfig = clientTLS
	cfg.DefaultQueryExecMode = mode
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("ConnectConfig: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// TestPostgresWire_QueryTimeoutBoundsEveryStatement: sql.query_timeout bounds
// every statement on the PostgreSQL wire, as on MySQL -- a simple query, an
// extended-protocol statement executed at Parse (no parameters), and one
// executed at Bind/Execute through its portal (parameters) -- and a statement
// past it is answered SQLSTATE 57014 (query_canceled), the code PostgreSQL's
// own statement_timeout raises. A statement inside the timeout is unaffected.
func TestPostgresWire_QueryTimeoutBoundsEveryStatement(t *testing.T) {
	const timeout = 300 * time.Millisecond
	cases := map[string]struct {
		mode pgx.QueryExecMode
		sql  string
		args []any
	}{
		"simple query":                {pgx.QueryExecModeSimpleProtocol, "SELECT pg_sleep(5)", nil},
		"extended, executed at Parse": {pgx.QueryExecModeCacheStatement, "SELECT pg_sleep(5)", nil},
		"extended, Bind and Execute":  {pgx.QueryExecModeCacheStatement, "SELECT pg_sleep($1)", []any{5}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			conn := timeoutPGConn(t, timeout, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
				t.Fatalf("a statement inside the timeout: %v", err)
			}
			start := time.Now()
			_, err := conn.Exec(ctx, tc.sql, tc.args...)
			elapsed := time.Since(start)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
				t.Fatalf("a statement past the timeout: err = %v after %s, want SQLSTATE 57014", err, elapsed)
			}
			if elapsed > 3*time.Second {
				t.Fatalf("the statement ran %s, far past the %s timeout", elapsed, timeout)
			}
		})
	}
}
