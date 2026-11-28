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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// TestMySQLWire_ReservedNameCodes proves a reserved engine table name and a
// reserved database name reach a MySQL client as MySQL's own codes for a
// name the server will not accept -- ER_WRONG_TABLE_NAME (1103) and
// ER_WRONG_DB_NAME (1102), both SQLSTATE 42000 -- not the generic 1105.
func TestMySQLWire_ReservedNameCodes(t *testing.T) {
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)

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

	cases := []struct {
		stmt   string
		number uint16
		name   string
	}{
		{"CREATE TABLE qrdb_ts_metrics (id BIGINT PRIMARY KEY)", 1103, "qrdb_ts_metrics"},
		{"CREATE TABLE QRDB_Vector_Vectors (id BIGINT PRIMARY KEY)", 1103, "QRDB_Vector_Vectors"},
		{"CREATE DATABASE pg_catalog", 1102, "pg_catalog"},
	}
	for _, tc := range cases {
		t.Run(tc.stmt, func(t *testing.T) {
			_, err := db.Exec(tc.stmt)
			var myErr *gomysql.MySQLError
			require.ErrorAs(t, err, &myErr, "got %v", err)
			assert.Equal(t, tc.number, myErr.Number)
			assert.Equal(t, "42000", string(myErr.SQLState[:]))
			assert.Contains(t, myErr.Message, tc.name)
		})
	}
}
