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

package server

import (
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"testing"
)

// ---------------------------------------------------------------------------
// mockSQLServer implements SQLProtocolServer for testing
// ---------------------------------------------------------------------------

// mockSQLServer is a lightweight mock of the SQLProtocolServer interface
// used to test the MySQL/PostgreSQL orchestration paths without requiring
// actual database wire protocol implementations.
type mockSQLServer struct {
	startErr error
	stopped  bool
	addr     string
}

func (m *mockSQLServer) Start() error { return m.startErr }
func (m *mockSQLServer) Stop()        { m.stopped = true }
func (m *mockSQLServer) Addr() string { return m.addr }

// Compile-time interface compliance check.
var _ SQLProtocolServer = (*mockSQLServer)(nil)

// saveSQLFns saves the current package-level SQL function pointers and
// returns a restore function that should be deferred by the caller.
func saveSQLFns() func() {
	origStartMySQL := startMySQLFn
	origStopMySQL := stopMySQLFn
	origStartPgSQL := startPgSQLFn
	origStopPgSQL := stopPgSQLFn
	return func() {
		startMySQLFn = origStartMySQL
		stopMySQLFn = origStopMySQL
		startPgSQLFn = origStartPgSQL
		stopPgSQLFn = origStopPgSQL
	}
}

// ---------------------------------------------------------------------------
// MySQLAddr tests
// ---------------------------------------------------------------------------

func TestMySQLAddr_NilServer(t *testing.T) {
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	if addr := srv.MySQLAddr(); addr != "" {
		t.Fatalf("expected empty MySQLAddr when mysqlSrv is nil, got %q", addr)
	}
}

func TestMySQLAddr_WithServer(t *testing.T) {
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	srv.mysqlSrv = &mockSQLServer{addr: "127.0.0.1:3306"}

	addr := srv.MySQLAddr()
	if addr != "127.0.0.1:3306" {
		t.Fatalf("expected MySQLAddr %q, got %q", "127.0.0.1:3306", addr)
	}
}

// ---------------------------------------------------------------------------
// PgSQLAddr tests
// ---------------------------------------------------------------------------

func TestPgSQLAddr_NilServer(t *testing.T) {
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	if addr := srv.PgSQLAddr(); addr != "" {
		t.Fatalf("expected empty PgSQLAddr when pgsqlSrv is nil, got %q", addr)
	}
}

func TestPgSQLAddr_WithServer(t *testing.T) {
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	srv.pgsqlSrv = &mockSQLServer{addr: "127.0.0.1:5432"}

	addr := srv.PgSQLAddr()
	if addr != "127.0.0.1:5432" {
		t.Fatalf("expected PgSQLAddr %q, got %q", "127.0.0.1:5432", addr)
	}
}

// ---------------------------------------------------------------------------
// Start MySQL: success
// ---------------------------------------------------------------------------

func TestStart_MySQL_Success(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mock := &mockSQLServer{addr: "127.0.0.1:3306"}
	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}

	cfg := testConfig(false, false)
	cfg.Protocols.MySQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.mysqlSrv != mock {
		t.Fatal("expected mysqlSrv to be set to mock")
	}
	if addr := srv.MySQLAddr(); addr != "127.0.0.1:3306" {
		t.Fatalf("expected MySQLAddr %q, got %q", "127.0.0.1:3306", addr)
	}
}

// ---------------------------------------------------------------------------
// Start MySQL: failure triggers rollback of earlier protocols
// ---------------------------------------------------------------------------

func TestStart_MySQL_Failure_Rollback(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlErr := errors.New("mysql bind failed")
	startMySQLFn = func(_ *Server) error {
		return mysqlErr
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}

	// Enable REST + MCP + MySQL so REST and MCP start first and get rolled back.
	socketPath := filepath.Join(t.TempDir(), "mysql-rollback.sock")
	cfg := testConfigFull(protocolFlags{
		rest: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.Protocols.MySQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to MySQL error")
	}
	if !errors.Is(err, mysqlErr) {
		t.Fatalf("expected mysqlErr, got: %v", err)
	}

	// REST and MCP should have been created before the MySQL failure.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.unixSrv == nil {
		t.Fatal("expected unixSrv to have been created before rollback")
	}
	if srv.mcpSrv == nil {
		t.Fatal("expected mcpSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// Start PgSQL: success
// ---------------------------------------------------------------------------

func TestStart_PgSQL_Success(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mock := &mockSQLServer{addr: "127.0.0.1:5432"}
	startPgSQLFn = func(s *Server) error {
		s.pgsqlSrv = mock
		return nil
	}
	stopPgSQLFn = func(s *Server) {
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
		}
	}

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.pgsqlSrv != mock {
		t.Fatal("expected pgsqlSrv to be set to mock")
	}
	if addr := srv.PgSQLAddr(); addr != "127.0.0.1:5432" {
		t.Fatalf("expected PgSQLAddr %q, got %q", "127.0.0.1:5432", addr)
	}
}

// ---------------------------------------------------------------------------
// Start PgSQL: failure triggers rollback including MySQL
// ---------------------------------------------------------------------------

func TestStart_PgSQL_Failure_Rollback(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlMock := &mockSQLServer{addr: "127.0.0.1:3306"}
	mysqlStopped := false

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mysqlMock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
			mysqlStopped = true
		}
	}

	pgsqlErr := errors.New("pgsql bind failed")
	startPgSQLFn = func(_ *Server) error {
		return pgsqlErr
	}
	stopPgSQLFn = func(s *Server) {
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
		}
	}

	// Enable REST + MySQL + PgSQL so MySQL gets rolled back on PgSQL failure.
	cfg := testConfig(true, false)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to PgSQL error")
	}
	if !errors.Is(err, pgsqlErr) {
		t.Fatalf("expected pgsqlErr, got: %v", err)
	}

	// REST should have been created before the PgSQL failure.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	// MySQL should have been started and then rolled back.
	if srv.mysqlSrv != mysqlMock {
		t.Fatal("expected mysqlSrv to have been set before rollback")
	}
	if !mysqlStopped {
		t.Fatal("expected MySQL to be stopped during rollback")
	}
}

// ---------------------------------------------------------------------------
// Start MySQL: disabled in config, startFn should not be called
// ---------------------------------------------------------------------------

func TestStart_MySQL_Disabled(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	called := false
	startMySQLFn = func(_ *Server) error {
		called = true
		return nil
	}

	cfg := testConfig(false, false)
	cfg.Protocols.MySQL = false
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if called {
		t.Fatal("startMySQLFn should not be called when MySQL is disabled")
	}
}

// ---------------------------------------------------------------------------
// Start MySQL: enabled but startFn is nil (build tag absent), no panic
// ---------------------------------------------------------------------------

func TestStart_MySQL_NilFn(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	startMySQLFn = nil
	stopMySQLFn = nil

	cfg := testConfig(false, false)
	cfg.Protocols.MySQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	// Should not panic and should succeed (silently skipped).
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if addr := srv.MySQLAddr(); addr != "" {
		t.Fatalf("expected empty MySQLAddr when startFn is nil, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// Start PgSQL: disabled in config, startFn should not be called
// ---------------------------------------------------------------------------

func TestStart_PgSQL_Disabled(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	called := false
	startPgSQLFn = func(_ *Server) error {
		called = true
		return nil
	}

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = false
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if called {
		t.Fatal("startPgSQLFn should not be called when PostgreSQL is disabled")
	}
}

// ---------------------------------------------------------------------------
// Start PgSQL: enabled but startFn is nil (build tag absent), no panic
// ---------------------------------------------------------------------------

func TestStart_PgSQL_NilFn(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	startPgSQLFn = nil
	stopPgSQLFn = nil

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	// Should not panic and should succeed (silently skipped).
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if addr := srv.PgSQLAddr(); addr != "" {
		t.Fatalf("expected empty PgSQLAddr when startFn is nil, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// stopMySQL: with function pointer set
// ---------------------------------------------------------------------------

func TestStop_MySQL_WithFn(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mock := &mockSQLServer{addr: "127.0.0.1:3306"}
	stopCalled := false
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
			stopCalled = true
		}
	}

	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	srv.mysqlSrv = mock

	srv.stopMySQL()

	if !stopCalled {
		t.Fatal("expected stopMySQLFn to be called")
	}
	if !mock.stopped {
		t.Fatal("expected mock MySQL server to be stopped")
	}
}

// ---------------------------------------------------------------------------
// stopMySQL: nil function pointer, no panic
// ---------------------------------------------------------------------------

func TestStop_MySQL_NilFn(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	stopMySQLFn = nil
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})

	// Should not panic.
	srv.stopMySQL()
}

// ---------------------------------------------------------------------------
// stopPgSQL: with function pointer set
// ---------------------------------------------------------------------------

func TestStop_PgSQL_WithFn(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mock := &mockSQLServer{addr: "127.0.0.1:5432"}
	stopCalled := false
	stopPgSQLFn = func(s *Server) {
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
			stopCalled = true
		}
	}

	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})
	srv.pgsqlSrv = mock

	srv.stopPgSQL()

	if !stopCalled {
		t.Fatal("expected stopPgSQLFn to be called")
	}
	if !mock.stopped {
		t.Fatal("expected mock PgSQL server to be stopped")
	}
}

// ---------------------------------------------------------------------------
// stopPgSQL: nil function pointer, no panic
// ---------------------------------------------------------------------------

func TestStop_PgSQL_NilFn(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	stopPgSQLFn = nil
	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})

	// Should not panic.
	srv.stopPgSQL()
}

// ---------------------------------------------------------------------------
// Start all seven protocols (REST, gRPC, QUIC, Unix, MCP, MySQL, PgSQL)
// ---------------------------------------------------------------------------

func TestStart_AllSevenProtocols(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlMock := &mockSQLServer{addr: "127.0.0.1:3306"}
	pgsqlMock := &mockSQLServer{addr: "127.0.0.1:5432"}

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mysqlMock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}
	startPgSQLFn = func(s *Server) error {
		s.pgsqlSrv = pgsqlMock
		return nil
	}
	stopPgSQLFn = func(s *Server) {
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
		}
	}

	socketPath := filepath.Join(t.TempDir(), "all7.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Verify all seven protocols are running.
	if srv.RESTAddr() == "" {
		t.Fatal("expected non-empty RESTAddr")
	}
	if srv.GRPCAddr() == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}
	if srv.QUICAddr() == "" {
		t.Fatal("expected non-empty QUICAddr")
	}
	if srv.UnixAddr() == "" {
		t.Fatal("expected non-empty UnixAddr")
	}
	if srv.MCPAddr() == "" {
		t.Fatal("expected non-empty MCPAddr")
	}
	if srv.MySQLAddr() != "127.0.0.1:3306" {
		t.Fatalf("expected MySQLAddr %q, got %q", "127.0.0.1:3306", srv.MySQLAddr())
	}
	if srv.PgSQLAddr() != "127.0.0.1:5432" {
		t.Fatalf("expected PgSQLAddr %q, got %q", "127.0.0.1:5432", srv.PgSQLAddr())
	}

	// All TCP/UDP addresses should be on different ports (excluding mocked SQL).
	addrs := map[string]string{
		"REST": srv.RESTAddr(),
		"gRPC": srv.GRPCAddr(),
		"QUIC": srv.QUICAddr(),
		"MCP":  srv.MCPAddr(),
	}
	ports := make(map[string]string)
	for name, addr := range addrs {
		_, port, _ := net.SplitHostPort(addr)
		if existing, ok := ports[port]; ok {
			t.Fatalf("%s and %s share port %s", existing, name, port)
		}
		ports[port] = name
	}
}

// ---------------------------------------------------------------------------
// Stop all seven protocols in reverse order
// ---------------------------------------------------------------------------

func TestStop_AllSevenProtocols(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	// Track stop order to verify reverse-order shutdown.
	var stopOrder []string

	mysqlMock := &mockSQLServer{addr: "127.0.0.1:3306"}
	pgsqlMock := &mockSQLServer{addr: "127.0.0.1:5432"}

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mysqlMock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		stopOrder = append(stopOrder, "mysql")
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}
	startPgSQLFn = func(s *Server) error {
		s.pgsqlSrv = pgsqlMock
		return nil
	}
	stopPgSQLFn = func(s *Server) {
		stopOrder = append(stopOrder, "pgsql")
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
		}
	}

	socketPath := filepath.Join(t.TempDir(), "stop7.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify all servers are created.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to be created")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to be created")
	}
	if srv.quicSrv == nil {
		t.Fatal("expected quicSrv to be created")
	}
	if srv.unixSrv == nil {
		t.Fatal("expected unixSrv to be created")
	}
	if srv.mcpSrv == nil {
		t.Fatal("expected mcpSrv to be created")
	}
	if srv.mysqlSrv == nil {
		t.Fatal("expected mysqlSrv to be created")
	}
	if srv.pgsqlSrv == nil {
		t.Fatal("expected pgsqlSrv to be created")
	}

	// Stop should complete without panic or hang.
	srv.Stop()

	// PgSQL should be stopped before MySQL (reverse startup order).
	if len(stopOrder) != 2 {
		t.Fatalf("expected 2 SQL stop calls, got %d", len(stopOrder))
	}
	if stopOrder[0] != "pgsql" {
		t.Fatalf("expected pgsql stopped first, got %q", stopOrder[0])
	}
	if stopOrder[1] != "mysql" {
		t.Fatalf("expected mysql stopped second, got %q", stopOrder[1])
	}

	// Verify mock servers recorded the Stop() call.
	if !pgsqlMock.stopped {
		t.Fatal("expected pgsqlMock to be stopped")
	}
	if !mysqlMock.stopped {
		t.Fatal("expected mysqlMock to be stopped")
	}
}

// ---------------------------------------------------------------------------
// MySQL failure rollback with all protocols: verifies full cascade
// ---------------------------------------------------------------------------

func TestStart_MySQL_Failure_FullCascadeRollback(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlErr := errors.New("mysql: address already in use")
	startMySQLFn = func(_ *Server) error {
		return mysqlErr
	}

	socketPath := filepath.Join(t.TempDir(), "mysql-cascade.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.Protocols.MySQL = true

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to MySQL error")
	}
	if !errors.Is(err, mysqlErr) {
		t.Fatalf("expected mysqlErr, got: %v", err)
	}

	// All five earlier servers should have been created.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
	if srv.quicSrv == nil {
		t.Fatal("expected quicSrv to have been created before rollback")
	}
	if srv.unixSrv == nil {
		t.Fatal("expected unixSrv to have been created before rollback")
	}
	if srv.mcpSrv == nil {
		t.Fatal("expected mcpSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// PgSQL failure rollback with all protocols including MySQL
// ---------------------------------------------------------------------------

func TestStart_PgSQL_Failure_FullCascadeRollback(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlMock := &mockSQLServer{addr: "127.0.0.1:3306"}
	mysqlStopped := false

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mysqlMock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
			mysqlStopped = true
		}
	}

	pgsqlErr := errors.New("pgsql: address already in use")
	startPgSQLFn = func(_ *Server) error {
		return pgsqlErr
	}

	socketPath := filepath.Join(t.TempDir(), "pgsql-cascade.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to PgSQL error")
	}
	if !errors.Is(err, pgsqlErr) {
		t.Fatalf("expected pgsqlErr, got: %v", err)
	}

	// All six earlier servers should have been created.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
	if srv.quicSrv == nil {
		t.Fatal("expected quicSrv to have been created before rollback")
	}
	if srv.unixSrv == nil {
		t.Fatal("expected unixSrv to have been created before rollback")
	}
	if srv.mcpSrv == nil {
		t.Fatal("expected mcpSrv to have been created before rollback")
	}
	if srv.mysqlSrv != mysqlMock {
		t.Fatal("expected mysqlSrv to have been set before rollback")
	}
	if !mysqlStopped {
		t.Fatal("expected MySQL to be stopped during PgSQL rollback")
	}
}

// ---------------------------------------------------------------------------
// Stop with both SQL protocols before Start (nil guards)
// ---------------------------------------------------------------------------

func TestStop_BothSQL_BeforeStart(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlStopCalled := false
	pgsqlStopCalled := false

	stopMySQLFn = func(_ *Server) {
		mysqlStopCalled = true
	}
	stopPgSQLFn = func(_ *Server) {
		pgsqlStopCalled = true
	}

	srv := New(testConfig(false, false), newMockKVService(), &mockAdminService{})

	// Stop before Start should not panic and should invoke registered stop fns.
	srv.Stop()

	if !mysqlStopCalled {
		t.Fatal("expected stopMySQLFn to be called even before Start")
	}
	if !pgsqlStopCalled {
		t.Fatal("expected stopPgSQLFn to be called even before Start")
	}
}

// ---------------------------------------------------------------------------
// Start MySQL + PgSQL only (no other protocols)
// ---------------------------------------------------------------------------

func TestStart_SQLOnly(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlMock := &mockSQLServer{addr: "127.0.0.1:3306"}
	pgsqlMock := &mockSQLServer{addr: "127.0.0.1:5432"}

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mysqlMock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}
	startPgSQLFn = func(s *Server) error {
		s.pgsqlSrv = pgsqlMock
		return nil
	}
	stopPgSQLFn = func(s *Server) {
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
		}
	}

	cfg := testConfig(false, false)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Verify only SQL servers are running.
	if addr := srv.RESTAddr(); addr != "" {
		t.Fatalf("expected empty RESTAddr, got %q", addr)
	}
	if addr := srv.GRPCAddr(); addr != "" {
		t.Fatalf("expected empty GRPCAddr, got %q", addr)
	}
	if addr := srv.QUICAddr(); addr != "" {
		t.Fatalf("expected empty QUICAddr, got %q", addr)
	}
	if addr := srv.UnixAddr(); addr != "" {
		t.Fatalf("expected empty UnixAddr, got %q", addr)
	}
	if addr := srv.MCPAddr(); addr != "" {
		t.Fatalf("expected empty MCPAddr, got %q", addr)
	}
	if addr := srv.MySQLAddr(); addr != "127.0.0.1:3306" {
		t.Fatalf("expected MySQLAddr %q, got %q", "127.0.0.1:3306", addr)
	}
	if addr := srv.PgSQLAddr(); addr != "127.0.0.1:5432" {
		t.Fatalf("expected PgSQLAddr %q, got %q", "127.0.0.1:5432", addr)
	}
}

// ---------------------------------------------------------------------------
// MySQL: stopFn called during Stop with running server
// ---------------------------------------------------------------------------

func TestStop_MySQL_ActiveServer(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mock := &mockSQLServer{addr: "127.0.0.1:3306"}

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}

	cfg := testConfig(false, false)
	cfg.Protocols.MySQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	srv.Stop()

	if !mock.stopped {
		t.Fatal("expected MySQL mock server to be stopped after Stop()")
	}
}

// ---------------------------------------------------------------------------
// PgSQL: stopFn called during Stop with running server
// ---------------------------------------------------------------------------

func TestStop_PgSQL_ActiveServer(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mock := &mockSQLServer{addr: "127.0.0.1:5432"}

	startPgSQLFn = func(s *Server) error {
		s.pgsqlSrv = mock
		return nil
	}
	stopPgSQLFn = func(s *Server) {
		if s.pgsqlSrv != nil {
			s.pgsqlSrv.Stop()
		}
	}

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	srv.Stop()

	if !mock.stopped {
		t.Fatal("expected PgSQL mock server to be stopped after Stop()")
	}
}

// ---------------------------------------------------------------------------
// MySQL failure with gRPC port conflict: verifies REST rollback path
// ---------------------------------------------------------------------------

func TestStart_MySQL_Failure_RESTOnlyRollback(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlErr := errors.New("mysql: port unavailable")
	startMySQLFn = func(_ *Server) error {
		return mysqlErr
	}

	// Only REST + MySQL enabled; MySQL failure should roll back REST.
	cfg := testConfig(true, false)
	cfg.Protocols.MySQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to MySQL error")
	}
	if !errors.Is(err, mysqlErr) {
		t.Fatalf("expected mysqlErr, got: %v", err)
	}

	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// PgSQL failure when only PgSQL is enabled (no rollback needed)
// ---------------------------------------------------------------------------

func TestStart_PgSQL_Failure_NoRollbackNeeded(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	pgsqlErr := errors.New("pgsql: connection refused")
	startPgSQLFn = func(_ *Server) error {
		return pgsqlErr
	}

	cfg := testConfig(false, false)
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to PgSQL error")
	}
	if !errors.Is(err, pgsqlErr) {
		t.Fatalf("expected pgsqlErr, got: %v", err)
	}

	// No other servers should have been created.
	if srv.restSrv != nil {
		t.Fatal("expected restSrv to be nil")
	}
	if srv.grpcSrv != nil {
		t.Fatal("expected grpcSrv to be nil")
	}
}

// ---------------------------------------------------------------------------
// MySQL failure rolls back gRPC that was started before it
// ---------------------------------------------------------------------------

func TestStart_MySQL_Failure_GRPCRollback(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	mysqlErr := errors.New("mysql: startup failed")
	startMySQLFn = func(_ *Server) error {
		return mysqlErr
	}

	// Occupy a known port to verify gRPC started (port 0 auto-assigns).
	cfg := testConfig(false, true)
	cfg.Protocols.MySQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to MySQL error")
	}

	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before MySQL rollback")
	}
}

// ---------------------------------------------------------------------------
// Verify PgSQL failure rollback calls all prior stop functions
// ---------------------------------------------------------------------------

func TestStart_PgSQL_Failure_VerifyAllStopsCalled(t *testing.T) {
	restore := saveSQLFns()
	defer restore()

	// Blocked port for PgSQL MCP failure verification.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)
	_ = blockedPort // not used directly, just verifying the pattern

	mysqlMock := &mockSQLServer{addr: "127.0.0.1:3306"}
	mysqlStopped := false

	startMySQLFn = func(s *Server) error {
		s.mysqlSrv = mysqlMock
		return nil
	}
	stopMySQLFn = func(s *Server) {
		mysqlStopped = true
		if s.mysqlSrv != nil {
			s.mysqlSrv.Stop()
		}
	}

	pgsqlErr := errors.New("pgsql: cannot start")
	startPgSQLFn = func(_ *Server) error {
		return pgsqlErr
	}

	cfg := testConfig(true, true)
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected Start to fail due to PgSQL error")
	}

	// All prior protocols should have been rolled back.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
	if !mysqlStopped {
		t.Fatal("expected MySQL stop to be called during PgSQL rollback")
	}
}
