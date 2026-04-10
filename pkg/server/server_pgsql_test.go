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

package server

import (
	"log/slog"
	"net"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// startPgSQL: success with port 0
// ---------------------------------------------------------------------------

func TestStartPgSQL_Success(t *testing.T) {
	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL failed: %v", err)
	}
	defer stopPgSQL(srv)

	if srv.pgsqlSrv == nil {
		t.Fatal("expected pgsqlSrv to be set")
	}
	addr := srv.pgsqlSrv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr from PgSQL server")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("PgSQL server Addr returned unresolved :0")
	}
}

// ---------------------------------------------------------------------------
// startPgSQL: failure with occupied port
// ---------------------------------------------------------------------------

func TestStartPgSQL_Failure_PortOccupied(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = blockedPort
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := startPgSQL(srv); err == nil {
		stopPgSQL(srv)
		t.Fatal("expected startPgSQL to fail on occupied port")
	}
}

// ---------------------------------------------------------------------------
// startPgSQL: with TLS option
// ---------------------------------------------------------------------------

func TestStartPgSQL_WithTLS(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL with TLS failed: %v", err)
	}
	defer stopPgSQL(srv)

	if srv.pgsqlSrv == nil {
		t.Fatal("expected pgsqlSrv to be set")
	}
}

// ---------------------------------------------------------------------------
// startPgSQL: empty DefaultDatabase (no WithDatabaseName option)
// ---------------------------------------------------------------------------

func TestStartPgSQL_EmptyDefaultDatabase(t *testing.T) {
	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{}

	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL failed: %v", err)
	}
	defer stopPgSQL(srv)

	if srv.pgsqlSrv == nil {
		t.Fatal("expected pgsqlSrv to be set")
	}
}

// ---------------------------------------------------------------------------
// stopPgSQL: nil pgsqlSrv (no panic)
// ---------------------------------------------------------------------------

func TestStopPgSQL_NilPgsqlSrv(t *testing.T) {
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	// stopPgSQL should not panic when pgsqlSrv is nil.
	stopPgSQL(srv)
}

// ---------------------------------------------------------------------------
// stopPgSQL: active server
// ---------------------------------------------------------------------------

func TestStopPgSQL_ActiveServer(t *testing.T) {
	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL failed: %v", err)
	}

	// Stop should complete without panic.
	stopPgSQL(srv)
}

// ---------------------------------------------------------------------------
// Full orchestration: Start() with PgSQL through function pointer
// ---------------------------------------------------------------------------

func TestStartPgSQL_WithDatabaseManager(t *testing.T) {
	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	// Create a database so the manager-backed provider has something to serve.
	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "testdb",
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
	})
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithDatabaseManager(mgr))

	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL with dbManager failed: %v", err)
	}
	defer stopPgSQL(srv)

	if srv.pgsqlSrv == nil {
		t.Fatal("expected pgsqlSrv to be set")
	}
	addr := srv.pgsqlSrv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr from PgSQL server")
	}
}

func TestStartPgSQL_WithoutDatabaseManager(t *testing.T) {
	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	// No WithDatabaseManager: legacy single-database mode.
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := startPgSQL(srv); err != nil {
		t.Fatalf("startPgSQL without dbManager failed: %v", err)
	}
	defer stopPgSQL(srv)

	if srv.pgsqlSrv == nil {
		t.Fatal("expected pgsqlSrv to be set")
	}
	if srv.opts.dbManager != nil {
		t.Fatal("expected dbManager to be nil")
	}
}

// ---------------------------------------------------------------------------
// Full orchestration: Start() with PgSQL through function pointer
// ---------------------------------------------------------------------------

func TestStart_PgSQLViaOrchestrator(t *testing.T) {
	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	cfg.Server.PgSQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.PgSQLAddr()
	if addr == "" {
		t.Fatal("expected non-empty PgSQLAddr via orchestrator")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("PgSQLAddr returned unresolved :0")
	}
}
