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

package server

import (
	"log/slog"
	"net"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
)

// ---------------------------------------------------------------------------
// startMySQL: success with port 0
// ---------------------------------------------------------------------------

func TestStartMySQL_Success(t *testing.T) {
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL failed: %v", err)
	}
	defer stopMySQL(srv)

	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be set")
	}
	addr := srv.mysqlSrv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr from MySQL server")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("MySQL server Addr returned unresolved :0")
	}
}

// ---------------------------------------------------------------------------
// startMySQL: failure with occupied port
// ---------------------------------------------------------------------------

func TestStartMySQL_Failure_PortOccupied(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = blockedPort
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := startMySQL(srv); err == nil {
		stopMySQL(srv)
		t.Fatal("expected startMySQL to fail on occupied port")
	}
}

// ---------------------------------------------------------------------------
// startMySQL: with TLS option
// ---------------------------------------------------------------------------

func TestStartMySQL_WithTLS(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL with TLS failed: %v", err)
	}
	defer stopMySQL(srv)

	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be set")
	}
}

// ---------------------------------------------------------------------------
// startMySQL: empty DefaultDatabase (no WithDatabaseName option)
// ---------------------------------------------------------------------------

func TestStartMySQL_EmptyDefaultDatabase(t *testing.T) {
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	// SQL.DefaultDatabase is empty, so WithDatabaseName should not be called.
	cfg.SQL = config.SQLConfig{}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL failed: %v", err)
	}
	defer stopMySQL(srv)

	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be set")
	}
}

// ---------------------------------------------------------------------------
// stopMySQL: nil mysqlSrv (no panic)
// ---------------------------------------------------------------------------

func TestStopMySQL_NilMysqlSrv(t *testing.T) {
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{})
	// stopMySQL should not panic when mysqlSrv is nil.
	stopMySQL(srv)
}

// ---------------------------------------------------------------------------
// stopMySQL: active server
// ---------------------------------------------------------------------------

func TestStopMySQL_ActiveServer(t *testing.T) {
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL failed: %v", err)
	}

	// Stop should complete without panic.
	stopMySQL(srv)
}

// ---------------------------------------------------------------------------
// Full orchestration: Start() with MySQL through function pointer
// ---------------------------------------------------------------------------

func TestStartMySQL_WithDatabaseManager(t *testing.T) {
	dataDir := t.TempDir()
	mgr := databasetest.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	// Create a database so the manager-backed provider has something to serve.
	_, err := mgr.Create(database.DatabaseConfig{
		Name:    "testdb",
		Backend: database.BackendPebble,
	})
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithDatabaseManager(mgr))

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL with dbManager failed: %v", err)
	}
	defer stopMySQL(srv)

	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be set")
	}
	addr := srv.mysqlSrv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr from MySQL server")
	}
}

func TestStartMySQL_WithoutDatabaseManager(t *testing.T) {
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	// No WithDatabaseManager: legacy single-database mode.
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL without dbManager failed: %v", err)
	}
	defer stopMySQL(srv)

	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be set")
	}
	if srv.opts.dbManager != nil {
		t.Fatal("expected dbManager to be nil")
	}
}

// ---------------------------------------------------------------------------
// Full orchestration: Start() with MySQL through function pointer
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// startMySQL: registry / collection / index wiring (Phase 9a)
//
// These pin the isolation-closing wiring at the orchestration layer: when
// the server has a database service registry, a CollectionServicer, and/or
// an IndexServicer configured, startMySQL must thread all three into the
// mysql.Server it builds (mysqlapi.WithServiceRegistry/WithCollectionService/
// WithIndex) rather than only wiring kvSvc/adminSvc as before. The deeper
// proof that the registry/collection/index options actually reach the
// served provider lives in pkg/sql/mysql's own test suite; this is the
// orchestration-layer plumbing check.
// ---------------------------------------------------------------------------

func TestStartMySQL_WithServiceRegistryCollectionIndex(t *testing.T) {
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	dbRegistry := &DatabaseServiceRegistry{}
	collectionSvc := &stubCollectionService{}
	indexSvc := &stubIndexService{}

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseServiceRegistry(dbRegistry),
		WithCollectionService(collectionSvc),
		WithIndex(indexSvc),
	)

	if err := startMySQL(srv); err != nil {
		t.Fatalf("startMySQL with registry/collection/index failed: %v", err)
	}
	defer stopMySQL(srv)

	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be set")
	}
	addr := srv.mysqlSrv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr from MySQL server")
	}
}

func TestStart_MySQLViaOrchestrator(t *testing.T) {
	cfg := testConfig(t, false, false)
	cfg.Protocols.MySQL = true
	cfg.Server.MySQLPort = 0
	cfg.SQL = config.SQLConfig{
		DefaultDatabase: "testdb",
		MaxConnections:  10,
		QueryTimeout:    "5s",
	}

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.MySQLAddr()
	if addr == "" {
		t.Fatal("expected non-empty MySQLAddr via orchestrator")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("MySQLAddr returned unresolved :0")
	}
}
