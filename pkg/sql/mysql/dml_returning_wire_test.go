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
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// newDMLReturningWire starts a MySQL wire server over a plain in-memory
// harness (sqltest.NewHarness: no row-lock machinery needed here) and
// returns a client pool connected to it, with one relational table seeded
// for the upsert-RETURNING probes below.
func newDMLReturningWire(t *testing.T) *sql.DB {
	t.Helper()
	h := sqltest.NewHarness()

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
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

	_, err = db.Exec("CREATE TABLE people (id BIGINT PRIMARY KEY, name VARCHAR(64))")
	require.NoError(t, err)
	return db
}

// TestMySQLWire_OnDuplicateKeyReturning_RowMatchesDeclaredSchema pins
// docs/sql/upstream-gaps.md #2 over the REAL MySQL wire, not just the
// in-memory go-mysql-server engine pkg/sql/engine's TestOnDuplicateKeyReturning
// tests already pin it against: an upsert that hits go-mysql-server's
// ON DUPLICATE KEY UPDATE path emits, for the row it updated, the OLD and
// NEW tuples concatenated -- a row wider than the RETURNING projection the
// server already told the client to expect. The MySQL text protocol sends
// the column count before any row data, so go-sql-driver/mysql reads
// exactly that many values off each row packet; a wider row desyncs the
// stream instead of silently handing back the pre-update value, exactly as
// the PostgreSQL wire fails closed on the same statement shape (see
// CachedPlanResultTypeError in pkg/sql/pgsql/errors.go).
func TestMySQLWire_OnDuplicateKeyReturning_RowMatchesDeclaredSchema(t *testing.T) {
	db := newDMLReturningWire(t)

	_, err := db.Exec("INSERT INTO people (id, name) VALUES (7, 'carol')")
	require.NoError(t, err)

	var id int64
	var name string
	err = db.QueryRow(
		"INSERT INTO people (id, name) VALUES (7, 'carol-2') ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id, name",
	).Scan(&id, &name)
	require.NoError(t, err, "a wider-than-declared row desyncs the wire protocol here")
	require.Equal(t, int64(7), id)
	require.Equal(t, "carol-2", name, "RETURNING must reflect the post-update value, not the pre-update one a naive truncation would leak")

	var readback string
	require.NoError(t, db.QueryRow("SELECT name FROM people WHERE id = 7").Scan(&readback))
	require.Equal(t, "carol-2", readback)
}

// TestMySQLWire_OnDuplicateKeyReturning_InsertPathUnaffected covers the same
// statement when NO conflict occurs: the plain-insert branch already
// projects its RETURNING row correctly upstream, and the repair must leave
// it alone rather than re-projecting an already-projected row.
func TestMySQLWire_OnDuplicateKeyReturning_InsertPathUnaffected(t *testing.T) {
	db := newDMLReturningWire(t)

	var id int64
	var name string
	err := db.QueryRow(
		"INSERT INTO people (id, name) VALUES (9, 'dave') ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id, name",
	).Scan(&id, &name)
	require.NoError(t, err)
	require.Equal(t, int64(9), id)
	require.Equal(t, "dave", name)
}
