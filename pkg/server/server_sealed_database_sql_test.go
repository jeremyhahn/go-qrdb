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

//go:build qrdb_mysql && qrdb_pgsql

package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// sqlWireStatements are a read and a write of the kv table, the statements a
// sealed database must refuse on both SQL wire protocols.
var sqlWireStatements = []string{
	"SELECT * FROM kv",
	"INSERT INTO kv (`key`, value) VALUES ('secret', 'v')",
}

// TestSealedDatabase_RefusedOverSQLWire is the MySQL and PostgreSQL leg of
// TestSealedDatabase_RefusedOnEveryTransport. The kv and entity tables used
// to be served from the node's raw KV service under a composed prefix, which
// never asked whether the database was sealed, so a sealed database stayed
// readable and writable over both wire protocols.
func TestSealedDatabase_RefusedOverSQLWire(t *testing.T) {
	mgr, db := openLegacyVault(t)

	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true
	cfg.Server.MySQLPort = 0
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{DefaultDatabase: "legacyvault", MaxConnections: 10, QueryTimeout: "5s"}
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseServiceRegistry(newLockedKVRegistry()),
		WithDatabaseManager(mgr),
	)
	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL: %v", err)
	}
	t.Cleanup(func() { stopMySQL(srv) })
	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL: %v", err)
	}
	t.Cleanup(func() { stopPgSQL(srv) })

	mcfg := gomysql.NewConfig()
	mcfg.Net = "tcp"
	mcfg.Addr = srv.MySQLAddr()
	mcfg.User = "root"
	mcfg.DBName = "legacyvault"
	mcfg.Timeout = 10 * time.Second
	my, err := sql.Open("mysql", mcfg.FormatDSN())
	if err != nil {
		t.Fatalf("sql.Open(mysql): %v", err)
	}
	t.Cleanup(func() { _ = my.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pcfg, err := pgx.ParseConfig("postgres://root@" + srv.PgSQLAddr() + "/legacyvault?sslmode=disable")
	if err != nil {
		t.Fatalf("pgx.ParseConfig: %v", err)
	}
	pcfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pg, err := pgx.ConnectConfig(ctx, pcfg)
	if err != nil {
		t.Fatalf("pgx.ConnectConfig: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close(context.Background()) })

	run := map[string]func(string) error{
		"mysql": func(q string) error {
			rows, err := my.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
			}
			return rows.Err()
		},
		"postgresql": func(q string) error {
			if q != sqlWireStatements[0] {
				q = "INSERT INTO kv (key, value) VALUES ('secret', 'v')"
			}
			rows, err := pg.Query(ctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
			}
			return rows.Err()
		},
	}
	for proto, exec := range run {
		for _, q := range sqlWireStatements {
			if err := exec(q); !isSealedRefusal(err) {
				t.Errorf("%s %q on a sealed database: err = %v, want a sealed-barrier refusal", proto, q, err)
			}
		}
	}

	// Unsealed, the same statements are served, so the refusals above are
	// the seal and not a broken route.
	db.SetStatus(database.StatusOpen)
	for proto, exec := range run {
		if err := exec(sqlWireStatements[0]); err != nil {
			t.Errorf("%s SELECT after unseal: %v", proto, err)
		}
	}
}
