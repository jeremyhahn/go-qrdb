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
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// sealedSessionDB starts a MySQL listener over a locking harness, gated on
// probe and subscribed to streams, and returns one held connection.
func sealedSessionDB(t *testing.T, probe sealedWindowProbe, streams *clusterseal.Streams) (*sql.DB, *sql.Conn) {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithSealedWindowProbe(probe),
		WithSealStreams(streams),
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
	return db, conn
}

// TestOpenSession_SealClosesTheConnection: a session open across a seal has
// its connection closed by the server, not merely its next statement refused
// -- closing is the only thing that ends what the statement gate never sees:
// a server-side cursor (vitess answers COM_STMT_FETCH itself) or a
// transaction held across the seal. Both ways a seal reaches the listener are
// covered: the node probe (a node-local seal) and the cluster-seal stream
// registry (a cluster seal or cut-off).
func TestOpenSession_SealClosesTheConnection(t *testing.T) {
	cases := map[string]func(*fakeSealedProbe, *clusterseal.Streams){
		"node probe closes": func(p *fakeSealedProbe, _ *clusterseal.Streams) { p.sealed.Store(true) },
		"cluster cut-off ends the session": func(p *fakeSealedProbe, s *clusterseal.Streams) {
			p.sealed.Store(true) // the gate closes first, as the monitor closes it
			s.EndAll(&clusterseal.SealStateUnconfirmedError{})
		},
	}
	for name, seal := range cases {
		t.Run(name, func(t *testing.T) {
			probe := &fakeSealedProbe{}
			streams := clusterseal.NewStreams()
			_, conn := sealedSessionDB(t, probe, streams)
			ctx := context.Background()
			require.NoError(t, conn.PingContext(ctx))
			seal(probe, streams)
			deadline := time.Now().Add(5 * time.Second)
			for {
				// A closed connection reads as driver.ErrBadConn; a refusal
				// on a connection still open does not.
				err := conn.Raw(func(dc any) error {
					return dc.(driver.Pinger).Ping(ctx) //nolint:errcheck // the go-sql-driver connection is a driver.Pinger
				})
				if errors.Is(err, driver.ErrBadConn) || errors.Is(err, gomysql.ErrInvalidConn) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("the session's connection stayed open after the seal (last: %v)", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

// TestOpenTransaction_ClusterSealClosesTheConnection: a transaction held
// across a cluster seal of its tenant ends with its connection -- the session
// is subscribed to the tenant's streams -- so its writes are never committed
// after the seal, and its connection is unusable.
func TestOpenTransaction_ClusterSealClosesTheConnection(t *testing.T) {
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	streams := clusterseal.NewStreams()
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithSealStreams(streams),
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
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(ctx, "CREATE TABLE t (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "INSERT INTO t VALUES (1)")
	require.NoError(t, err)

	// The system tenant -- this unauthenticated session's -- is sealed.
	streams.EndTenant("system", &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier("system")})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := tx.ExecContext(ctx, "SELECT 1"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			_ = tx.Rollback() // an open transaction would hold the cleanup's Close
			t.Fatal("the transaction kept serving after its tenant was sealed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Error(t, tx.Commit(), "a transaction held across the seal committed")

	// Nothing of it was written.
	var n int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t").Scan(&n))
	require.Zero(t, n, "the transaction's write survived the seal")
}
