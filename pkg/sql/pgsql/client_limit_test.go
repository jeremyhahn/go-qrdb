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
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientLimitedPG starts a PostgreSQL listener whose client-address bucket
// holds burst tokens and never refills within a test, and returns a function
// that opens a connection to it in mode.
func clientLimitedPG(t *testing.T, burst int, mode pgx.QueryExecMode) func() (*pgx.Conn, error) {
	t.Helper()
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: burst}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	srv := NewServer(&mockKVService{}, nil, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithClientLimiter(l),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)
	cfg, err := pgx.ParseConfig("postgres://qrdb@" + srv.Addr() + "/qrdb?sslmode=disable")
	require.NoError(t, err)
	cfg.DefaultQueryExecMode = mode
	return func() (*pgx.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			t.Cleanup(func() { _ = conn.Close(context.Background()) })
		}
		return conn, err
	}
}

// requirePgRefusal asserts err is an ErrorResponse with the SQLSTATE and
// severity given.
func requirePgRefusal(t *testing.T, err error, code codes.Code, severity psqlerr.Severity) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want an ErrorResponse, got %v", err)
	require.Equal(t, string(code), pgErr.Code)
	require.Equal(t, string(severity), pgErr.Severity)
	require.Contains(t, pgErr.Message, "rate limit exceeded for client 127.0.0.1")
}

func TestClientLimit_RefusesConnectionOverRate(t *testing.T) {
	connect := clientLimitedPG(t, 1, pgx.QueryExecModeSimpleProtocol)
	_, err := connect()
	require.NoError(t, err)

	_, err = connect()
	requirePgRefusal(t, err, "08004", psqlerr.LevelFatal)
}

func TestClientLimit_RefusesSimpleQueryOverRate(t *testing.T) {
	conn, err := clientLimitedPG(t, 2, pgx.QueryExecModeSimpleProtocol)() // connection
	require.NoError(t, err)

	var one int
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT 1").Scan(&one)) // query
	err = conn.QueryRow(context.Background(), "SELECT 1").Scan(&one)
	requirePgRefusal(t, err, "53400", psqlerr.LevelError)

	// A refused statement leaves the session open; it stays refused only
	// while the bucket is empty.
	require.False(t, conn.IsClosed())
}

func TestClientLimit_RefusesExtendedExecuteOverRate(t *testing.T) {
	conn, err := clientLimitedPG(t, 3, pgx.QueryExecModeCacheStatement)() // connection
	require.NoError(t, err)

	var v string
	// The first query is a Parse (one token) and an Execute (one token);
	// the second reuses the cached statement and is an Execute alone.
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT $1", "7").Scan(&v))
	require.Equal(t, "7", v)
	err = conn.QueryRow(context.Background(), "SELECT $1", "7").Scan(&v)
	requirePgRefusal(t, err, "53400", psqlerr.LevelError)
}

func TestAdmitClient_PassesThroughWithoutLimiter(t *testing.T) {
	require.NoError(t, admitClient(context.Background(), nil, codes.ConfigurationLimitExceeded, psqlerr.LevelError))
	require.NoError(t, (&ParseHandler{}).admitStatement(context.Background()))
}
