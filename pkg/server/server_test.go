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
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockKVService implements service.KVServicer for testing.
type mockKVService struct {
	store map[string][]byte
}

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	m.store[key] = value
	return nil
}

func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// mockAdminService implements service.AdminServicer for testing.
type mockAdminService struct{}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{
		NodeID:      "test-node",
		RaftAddress: "127.0.0.1:63001",
		IsLeader:    true,
		LeaderID:    1,
		ShardCount:  1,
	}, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{Shards: []service.ShardPeers{
		{ShardID: 1, LeaderID: 1, Peers: []service.PeerInfo{
			{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
		}},
	}}, nil
}

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
	return nil
}

func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return nil
}

func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockAdminService) Drain(_ context.Context) error {
	return nil
}

// mockHost implements rest.Host for testing.
type mockHost struct{}

func (m *mockHost) RaftAddress() string { return "127.0.0.1:63001" }

// mockLeaderAdminResolver implements rest.LeaderAdminResolver for testing.
type mockLeaderAdminResolver struct{}

func (m *mockLeaderAdminResolver) ResolveAdminURL(_ string) string { return "" }
func (m *mockLeaderAdminResolver) Register(_, _ string)            {}

// protocolFlags holds per-protocol enable/disable flags for testConfigFull.
type protocolFlags struct {
	rest bool
	grpc bool
	unix bool
	mcp  bool
}

// testConfig returns a config with port 0 for all TCP listeners and valid
// raft settings to pass validation. Only REST and GRPC protocols can be
// toggled; Unix and MCP are disabled.
func testConfig(restEnabled, grpcEnabled bool) *config.Config {
	return testConfigFull(protocolFlags{
		rest: restEnabled,
		grpc: grpcEnabled,
	}, "")
}

// testConfigFull returns a config with all four protocols configurable.
// socketPath is used for the Unix socket; when empty and unix is enabled,
// a default temp path is used. MCP always uses port 0.
func testConfigFull(flags protocolFlags, socketPath string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			AdminPort: 0,
			GRPCPort:  0,
			Host:      "127.0.0.1",
		},
		Protocols: config.ProtocolsConfig{
			REST: flags.rest,
			GRPC: flags.grpc,
			Unix: flags.unix,
			MCP:  flags.mcp,
		},
		Raft: config.RaftConfig{
			DataDir:     "./test-data",
			ShardID:     1,
			ReplicaID:   1,
			RaftAddress: "127.0.0.1:63001",
			SMType:      "memory",
		},
		Storage: config.StorageConfig{
			Backend: "memory",
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "text",
		},
		Discovery: config.DiscoveryConfig{
			Method: "static",
		},
		Unix: config.UnixConfig{
			SocketPath: socketPath,
		},
		MCP: config.MCPConfig{
			Port:     0,
			Endpoint: "/mcp",
		},
	}
}

// writeTestFile creates a regular file at path for use in filesystem
// failure tests.
func writeTestFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// ---------------------------------------------------------------------------
// New() tests
// ---------------------------------------------------------------------------

func TestNew_CreatesServer(t *testing.T) {
	cfg := testConfig(true, true)
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}

	srv := New(cfg, kvSvc, adminSvc)
	if srv == nil {
		t.Fatal("New returned nil")
	}
	if srv.config != cfg {
		t.Fatal("config not set")
	}
	if srv.kvSvc == nil {
		t.Fatal("kvSvc not set")
	}
	if srv.adminSvc == nil {
		t.Fatal("adminSvc not set")
	}
}

func TestNew_WithOptions(t *testing.T) {
	cfg := testConfig(true, true)
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	gatherer := prometheus.NewRegistry()
	resolver := &mockLeaderAdminResolver{}
	host := &mockHost{}

	srv := New(cfg, kvSvc, adminSvc,
		WithTLS(tlsCfg),
		WithGatherer(gatherer),
		WithLeaderForwarding(resolver),
		WithHost(host),
	)

	if srv.opts.tlsConfig != tlsCfg {
		t.Fatal("WithTLS option not applied")
	}
	if srv.opts.gatherer != gatherer {
		t.Fatal("WithGatherer option not applied")
	}
	if srv.opts.resolver != resolver {
		t.Fatal("WithLeaderForwarding option not applied")
	}
	if srv.opts.host != host {
		t.Fatal("WithHost option not applied")
	}
}

// ---------------------------------------------------------------------------
// RESTAddr / GRPCAddr / UnixAddr / MCPAddr before Start
// ---------------------------------------------------------------------------

func TestRESTAddr_BeforeStart(t *testing.T) {
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{})
	if addr := srv.RESTAddr(); addr != "" {
		t.Fatalf("expected empty RESTAddr before start, got %q", addr)
	}
}

func TestGRPCAddr_BeforeStart(t *testing.T) {
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{})
	if addr := srv.GRPCAddr(); addr != "" {
		t.Fatalf("expected empty GRPCAddr before start, got %q", addr)
	}
}

func TestUnixAddr_BeforeStart(t *testing.T) {
	cfg := testConfigFull(protocolFlags{unix: true}, "/tmp/test.sock")
	srv := New(cfg, newMockKVService(), &mockAdminService{})
	if addr := srv.UnixAddr(); addr != "" {
		t.Fatalf("expected empty UnixAddr before start, got %q", addr)
	}
}

func TestMCPAddr_BeforeStart(t *testing.T) {
	cfg := testConfigFull(protocolFlags{mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{})
	if addr := srv.MCPAddr(); addr != "" {
		t.Fatalf("expected empty MCPAddr before start, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with REST only
// ---------------------------------------------------------------------------

func TestStart_RESTOnly(t *testing.T) {
	cfg := testConfig(true, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	restAddr := srv.RESTAddr()
	if restAddr == "" {
		t.Fatal("expected non-empty RESTAddr")
	}
	if restAddr == "127.0.0.1:0" {
		t.Fatal("RESTAddr returned unresolved :0")
	}

	grpcAddr := srv.GRPCAddr()
	if grpcAddr != "" {
		t.Fatalf("expected empty GRPCAddr, got %q", grpcAddr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with GRPC only
// ---------------------------------------------------------------------------

func TestStart_GRPCOnly(t *testing.T) {
	cfg := testConfig(false, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	grpcAddr := srv.GRPCAddr()
	if grpcAddr == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}
	if grpcAddr == "127.0.0.1:0" {
		t.Fatal("GRPCAddr returned unresolved :0")
	}

	restAddr := srv.RESTAddr()
	if restAddr != "" {
		t.Fatalf("expected empty RESTAddr, got %q", restAddr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with Unix only
// ---------------------------------------------------------------------------

func TestStart_UnixOnly(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(protocolFlags{unix: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	unixAddr := srv.UnixAddr()
	if unixAddr == "" {
		t.Fatal("expected non-empty UnixAddr")
	}

	// Other protocols should be empty.
	if addr := srv.RESTAddr(); addr != "" {
		t.Fatalf("expected empty RESTAddr, got %q", addr)
	}
	if addr := srv.GRPCAddr(); addr != "" {
		t.Fatalf("expected empty GRPCAddr, got %q", addr)
	}
	if addr := srv.MCPAddr(); addr != "" {
		t.Fatalf("expected empty MCPAddr, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with MCP only
// ---------------------------------------------------------------------------

func TestStart_MCPOnly(t *testing.T) {
	cfg := testConfigFull(protocolFlags{mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	mcpAddr := srv.MCPAddr()
	if mcpAddr == "" {
		t.Fatal("expected non-empty MCPAddr")
	}
	if mcpAddr == "127.0.0.1:0" {
		t.Fatal("MCPAddr returned unresolved :0")
	}

	// Other protocols should be empty.
	if addr := srv.RESTAddr(); addr != "" {
		t.Fatalf("expected empty RESTAddr, got %q", addr)
	}
	if addr := srv.GRPCAddr(); addr != "" {
		t.Fatalf("expected empty GRPCAddr, got %q", addr)
	}
	if addr := srv.UnixAddr(); addr != "" {
		t.Fatalf("expected empty UnixAddr, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with both original protocols
// ---------------------------------------------------------------------------

func TestStart_BothProtocols(t *testing.T) {
	cfg := testConfig(true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	restAddr := srv.RESTAddr()
	if restAddr == "" {
		t.Fatal("expected non-empty RESTAddr")
	}

	grpcAddr := srv.GRPCAddr()
	if grpcAddr == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}

	// Addresses should be different (different ports).
	if restAddr == grpcAddr {
		t.Fatalf("REST and gRPC addresses should differ, both are %q", restAddr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with all four protocols
// ---------------------------------------------------------------------------

func TestStart_AllProtocols(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	restAddr := srv.RESTAddr()
	if restAddr == "" {
		t.Fatal("expected non-empty RESTAddr")
	}

	grpcAddr := srv.GRPCAddr()
	if grpcAddr == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}

	unixAddr := srv.UnixAddr()
	if unixAddr == "" {
		t.Fatal("expected non-empty UnixAddr")
	}

	mcpAddr := srv.MCPAddr()
	if mcpAddr == "" {
		t.Fatal("expected non-empty MCPAddr")
	}

	// All TCP addresses should be on different ports.
	addrs := map[string]string{
		"REST": restAddr,
		"gRPC": grpcAddr,
		"MCP":  mcpAddr,
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
// Start with neither protocol
// ---------------------------------------------------------------------------

func TestStart_NeitherProtocol(t *testing.T) {
	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with no protocols should succeed, got: %v", err)
	}
	defer srv.Stop()

	if addr := srv.RESTAddr(); addr != "" {
		t.Fatalf("expected empty RESTAddr, got %q", addr)
	}
	if addr := srv.GRPCAddr(); addr != "" {
		t.Fatalf("expected empty GRPCAddr, got %q", addr)
	}
	if addr := srv.UnixAddr(); addr != "" {
		t.Fatalf("expected empty UnixAddr, got %q", addr)
	}
	if addr := srv.MCPAddr(); addr != "" {
		t.Fatalf("expected empty MCPAddr, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// Stop graceful
// ---------------------------------------------------------------------------

func TestStop_Graceful(t *testing.T) {
	cfg := testConfig(true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop should complete without panic or hang.
	srv.Stop()

	// After stop, addresses should still be accessible (servers point to
	// the underlying servers which retain their listener state).
}

func TestStop_BeforeStart(t *testing.T) {
	cfg := testConfig(true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{})
	// Stop before Start should not panic.
	srv.Stop()
}

func TestStop_CalledTwice(t *testing.T) {
	cfg := testConfig(true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	srv.Stop()
	// Second Stop should not panic.
	srv.Stop()
}

// ---------------------------------------------------------------------------
// Stop all four protocols gracefully
// ---------------------------------------------------------------------------

func TestStop_AllProtocols(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify all four are running.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to be created")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to be created")
	}
	if srv.unixSrv == nil {
		t.Fatal("expected unixSrv to be created")
	}
	if srv.mcpSrv == nil {
		t.Fatal("expected mcpSrv to be created")
	}

	// Stop should complete without panic or hang.
	srv.Stop()
}

// ---------------------------------------------------------------------------
// Start error: GRPC fails, REST rolls back
// ---------------------------------------------------------------------------

func TestStart_GRPCFail_RESTRollback(t *testing.T) {
	// Occupy a port so that the GRPC server cannot bind to it.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	// REST on port 0 succeeds; GRPC on the occupied port fails.
	cfg := testConfig(true, true)
	cfg.Server.AdminPort = 0
	cfg.Server.GRPCPort = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{})
	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected GRPC start to fail on occupied port")
	}

	// The REST server should have been created before rollback.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// Start error: Unix fails, REST and GRPC roll back
// ---------------------------------------------------------------------------

func TestStart_UnixFail_Rollback(t *testing.T) {
	// Use a socket path inside a non-writable directory to force failure.
	// Create a temp file (not directory) so MkdirAll on its child fails.
	tmpDir := t.TempDir()
	blockerFile := filepath.Join(tmpDir, "blocker")
	if err := writeTestFile(blockerFile); err != nil {
		t.Fatalf("failed to create blocker file: %v", err)
	}
	// Socket path under a regular file, so MkdirAll will fail.
	badSocketPath := filepath.Join(blockerFile, "sub", "qrdb.sock")

	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
	}, badSocketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected Unix start to fail with invalid socket path")
	}

	// REST and GRPC should have been created before the Unix failure.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// Start error: MCP fails, REST + GRPC + Unix roll back
// ---------------------------------------------------------------------------

func TestStart_MCPFail_Rollback(t *testing.T) {
	// Occupy a port so MCP cannot bind to it.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.MCP.Port = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{})
	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected MCP start to fail on occupied port")
	}

	// All three earlier servers should have been created before MCP failed.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
	if srv.unixSrv == nil {
		t.Fatal("expected unixSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// Start error: REST fails
// ---------------------------------------------------------------------------

func TestStart_RESTFail(t *testing.T) {
	cfg := testConfig(true, false)
	cfg.Server.Host = "invalid-no-resolve"
	cfg.Server.AdminPort = 1 // privileged port

	srv := New(cfg, newMockKVService(), &mockAdminService{})
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected REST start to fail with invalid host")
	}
}

// ---------------------------------------------------------------------------
// Start error: MCP fails without other protocols
// ---------------------------------------------------------------------------

func TestStart_MCPFail_NoRollbackNeeded(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	cfg := testConfigFull(protocolFlags{mcp: true}, "")
	cfg.MCP.Port = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{})
	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected MCP start to fail on occupied port")
	}

	// No other servers should have been created.
	if srv.restSrv != nil {
		t.Fatal("expected restSrv to be nil")
	}
	if srv.grpcSrv != nil {
		t.Fatal("expected grpcSrv to be nil")
	}
	if srv.unixSrv != nil {
		t.Fatal("expected unixSrv to be nil")
	}
}

// ---------------------------------------------------------------------------
// Options: WithTLS applied to inner servers
// ---------------------------------------------------------------------------

func TestWithTLS_Applied(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
	)
	if srv.opts.tlsConfig != tlsCfg {
		t.Fatal("TLS config not stored in opts")
	}
}

func TestWithGatherer_Applied(t *testing.T) {
	g := prometheus.NewRegistry()
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithGatherer(g),
	)
	if srv.opts.gatherer != g {
		t.Fatal("gatherer not stored in opts")
	}
}

func TestWithLeaderForwarding_Applied(t *testing.T) {
	resolver := &mockLeaderAdminResolver{}
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithLeaderForwarding(resolver),
	)
	if srv.opts.resolver != resolver {
		t.Fatal("resolver not stored in opts")
	}
}

func TestWithHost_Applied(t *testing.T) {
	h := &mockHost{}
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithHost(h),
	)
	if srv.opts.host != h {
		t.Fatal("host not stored in opts")
	}
}

func TestWithCACertPool_Applied(t *testing.T) {
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithCACertPool(nil),
	)
	if srv.opts.caCertPool != nil {
		t.Fatal("expected nil caCertPool")
	}
}

// ---------------------------------------------------------------------------
// Options propagation through Start
// ---------------------------------------------------------------------------

func TestStart_WithGatherer_Propagated(t *testing.T) {
	cfg := testConfig(true, false) // REST only so we can check gatherer propagation.
	g := prometheus.NewRegistry()
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithGatherer(g))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.restSrv == nil {
		t.Fatal("expected REST server to be created")
	}
}

func TestStart_WithLeaderForwarding_Propagated(t *testing.T) {
	cfg := testConfig(true, false)
	resolver := &mockLeaderAdminResolver{}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithLeaderForwarding(resolver))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.restSrv == nil {
		t.Fatal("expected REST server to be created")
	}
}

func TestStart_WithHost_Propagated(t *testing.T) {
	cfg := testConfig(true, false)
	h := &mockHost{}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithHost(h))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.restSrv == nil {
		t.Fatal("expected REST server to be created")
	}
}

// ---------------------------------------------------------------------------
// Unix socket path resolution
// ---------------------------------------------------------------------------

func TestStart_UnixAddrResolved(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test-resolved.sock")
	cfg := testConfigFull(protocolFlags{unix: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.UnixAddr()
	if addr == "" {
		t.Fatal("expected non-empty UnixAddr")
	}
	// Unix socket Addr() should contain the socket path.
	if !strings.Contains(addr, "test-resolved.sock") {
		t.Fatalf("expected UnixAddr to contain socket filename, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// MCP port 0 resolution
// ---------------------------------------------------------------------------

func TestStart_MCPAddrResolved(t *testing.T) {
	cfg := testConfigFull(protocolFlags{mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.MCPAddr()
	if addr == "" {
		t.Fatal("expected non-empty MCPAddr")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("MCPAddr returned unresolved :0")
	}
}

// ---------------------------------------------------------------------------
// Verify interface compliance at compile time
// ---------------------------------------------------------------------------

var _ rest.LeaderAdminResolver = (*mockLeaderAdminResolver)(nil)
var _ rest.Host = (*mockHost)(nil)

// Suppress unused import warning for os package.
var _ = os.Remove
