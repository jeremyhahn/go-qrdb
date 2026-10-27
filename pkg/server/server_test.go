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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
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

// PutIfAbsent stores only when the key is absent, matching the real engines.
func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, key)
	return true, nil
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

func (m *mockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	delete(m.store, key)
	return nil
}

func (m *mockKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	m.store[key] = value
	return nil
}

func (m *mockKVService) GetWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) ListWithConsistency(_ context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVService) ScanWithConsistency(_ context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKVService) ExistsWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	_, ok := m.store[key]
	return ok, nil
}

// List is the USER-FACING enumeration and applies the internal-key discovery
// guard, exactly as every production KVServicer does. See memoryKVSvc.List in
// pkg/service for why a double that skips the guard is narrower than
// production in the one dimension a teardown depends on (go-qrdb#412).
func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	return service.FilterInternalKeys(m.listAll(prefix), prefix), nil
}

// ListAll is the unfiltered enumeration, matching service.KVServicer.ListAll.
func (m *mockKVService) ListAll(_ context.Context, prefix string) ([]string, error) {
	return m.listAll(prefix), nil
}

// listAll is the shared half of List and ListAll, so the two cannot differ in
// anything but the filter.
func (m *mockKVService) listAll(prefix string) []string {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Scan applies the internal-key guard for the reason List does.
func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return service.FilterInternalMap(result, prefix), nil
}

func (m *mockKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}

func (m *mockKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (m *mockKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
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

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _, _ string) error {
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

func (m *mockAdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	return &capabilities.Capabilities{
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Index:       true,
	}, nil
}

func (m *mockAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{ShardID: shardID}, nil
}

func (m *mockAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{}, nil
}

func (m *mockAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (m *mockAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
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
	quic bool
	unix bool
	mcp  bool
}

// testConfig returns a config with port 0 for all TCP listeners and valid
// raft settings to pass validation. Only REST and GRPC protocols can be
// toggled; Unix and MCP are disabled.
func testConfig(t *testing.T, restEnabled, grpcEnabled bool) *config.Config {
	t.Helper()
	return testConfigFull(t, protocolFlags{
		rest: restEnabled,
		grpc: grpcEnabled,
	}, "")
}

// testConfigFull returns a config with all four protocols configurable.
// socketPath is used for the Unix socket; when empty and unix is enabled,
// a default temp path is used. MCP always uses port 0.
func testConfigFull(t *testing.T, flags protocolFlags, socketPath string) *config.Config {
	t.Helper()
	return &config.Config{
		Server: config.ServerConfig{
			// AdminPort is chosen so the port is free on BOTH TCP and UDP,
			// not left at 0 for the kernel to pick a TCP port from. The REST
			// listener takes this port on TCP and its HTTP/3 sibling takes the
			// same number on UDP; a kernel-chosen free TCP port says nothing
			// about UDP, and a parallel test in this package holds ephemeral
			// UDP ports of its own (go-qrdb#248).
			//
			// GRPCPort and QUICPort stay at 0 deliberately: gRPC is TCP-only
			// and QUIC is UDP-only, so neither has a same-numbered sibling on
			// the other stack and neither can collide this way.
			AdminPort: freeDualStackPort(t),
			GRPCPort:  0,
			QUICPort:  0,
			Host:      "127.0.0.1",
		},
		Protocols: config.ProtocolsConfig{
			REST: flags.rest,
			GRPC: flags.grpc,
			QUIC: flags.quic,
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
	cfg := testConfig(t, true, true)
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
	cfg := testConfig(t, true, true)
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
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{})
	if addr := srv.RESTAddr(); addr != "" {
		t.Fatalf("expected empty RESTAddr before start, got %q", addr)
	}
}

func TestGRPCAddr_BeforeStart(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{})
	if addr := srv.GRPCAddr(); addr != "" {
		t.Fatalf("expected empty GRPCAddr before start, got %q", addr)
	}
}

func TestUnixAddr_BeforeStart(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{unix: true}, "/tmp/test.sock")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if addr := srv.UnixAddr(); addr != "" {
		t.Fatalf("expected empty UnixAddr before start, got %q", addr)
	}
}

func TestMCPAddr_BeforeStart(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if addr := srv.MCPAddr(); addr != "" {
		t.Fatalf("expected empty MCPAddr before start, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with REST only
// ---------------------------------------------------------------------------

func TestStart_RESTOnly(t *testing.T) {
	cfg := testConfig(t, true, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfig(t, false, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfig(t, true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfig(t, true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop should complete without panic or hang.
	srv.Stop()

	// After stop, addresses should still be accessible (servers point to
	// the underlying servers which retain their listener state).
}

func TestStop_BeforeStart(t *testing.T) {
	cfg := testConfig(t, true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	// Stop before Start should not panic.
	srv.Stop()
}

func TestStop_CalledTwice(t *testing.T) {
	cfg := testConfig(t, true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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

	// REST succeeds on the builder's dual-stack-safe admin port; GRPC fails on
	// the occupied one. The admin port is deliberately NOT overridden here:
	// this test is about a gRPC bind failure rolling REST back, and setting it
	// to 0 would hand REST a kernel-chosen TCP port whose UDP side HTTP/3 then
	// has to take -- reintroducing exactly the collision this file's allocator
	// exists to prevent (go-qrdb#248).
	cfg := testConfig(t, true, true)
	cfg.Server.GRPCPort = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
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

	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
	}, badSocketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.MCP.Port = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
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
	cfg := testConfig(t, true, false)
	cfg.Server.Host = "invalid-no-resolve"
	cfg.Server.AdminPort = 1 // privileged port

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
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

	cfg := testConfigFull(t, protocolFlags{mcp: true}, "")
	cfg.MCP.Port = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
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
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
	)
	if srv.opts.tlsConfig != tlsCfg {
		t.Fatal("TLS config not stored in opts")
	}
}

func TestWithGatherer_Applied(t *testing.T) {
	g := prometheus.NewRegistry()
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithGatherer(g),
	)
	if srv.opts.gatherer != g {
		t.Fatal("gatherer not stored in opts")
	}
}

// Both boot paths (qrdbd and the SDK node) build the server without
// WithGatherer, so the default is what makes /metrics exist at all.
func TestNew_DefaultGathererServesQuicraftAndQrdbMetrics(t *testing.T) {
	database.NewRaftMetrics().ObserveFsyncLatency(time.Millisecond)
	syncwatch.ForStorage(syncwatch.BackendPebble).Observe(time.Millisecond)

	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{})
	if srv.opts.gatherer == nil {
		t.Fatal("no default gatherer: /metrics is not registered")
	}
	for _, name := range []string{"quicraft_logdb_fsync_latency_seconds", "qrdb_storage_sync_duration_seconds", "go_goroutines"} {
		if !syncwatchtest.HasFamily(t, srv.opts.gatherer, name) {
			t.Fatalf("default gatherer does not expose %s", name)
		}
	}
}

func TestWithGatherer_NilLeavesMetricsUnregistered(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithGatherer(nil))
	if srv.opts.gatherer != nil {
		t.Fatal("WithGatherer(nil) did not override the default gatherer")
	}
}

func TestWithLeaderForwarding_Applied(t *testing.T) {
	resolver := &mockLeaderAdminResolver{}
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithLeaderForwarding(resolver),
	)
	if srv.opts.resolver != resolver {
		t.Fatal("resolver not stored in opts")
	}
}

func TestWithHost_Applied(t *testing.T) {
	h := &mockHost{}
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithHost(h),
	)
	if srv.opts.host != h {
		t.Fatal("host not stored in opts")
	}
}

func TestWithCACertPool_Applied(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
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
	cfg := testConfig(t, true, false) // REST only so we can check gatherer propagation.
	g := prometheus.NewRegistry()
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithGatherer(g), WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.restSrv == nil {
		t.Fatal("expected REST server to be created")
	}
}

func TestStart_WithLeaderForwarding_Propagated(t *testing.T) {
	cfg := testConfig(t, true, false)
	resolver := &mockLeaderAdminResolver{}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithLeaderForwarding(resolver), WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.restSrv == nil {
		t.Fatal("expected REST server to be created")
	}
}

func TestStart_WithHost_Propagated(t *testing.T) {
	cfg := testConfig(t, true, false)
	h := &mockHost{}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithHost(h), WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfigFull(t, protocolFlags{mcp: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
// ServerOption coverage tests
// ---------------------------------------------------------------------------

func TestWithUserService(t *testing.T) {
	var opts serverOptions
	opt := WithUserService(nil)
	opt(&opts)
	if opts.userSvc != nil {
		t.Error("expected nil userSvc")
	}
}

func TestWithRoleService(t *testing.T) {
	var opts serverOptions
	opt := WithRoleService(nil)
	opt(&opts)
	if opts.roleSvc != nil {
		t.Error("expected nil roleSvc")
	}
}

func TestWithCertProvider(t *testing.T) {
	var opts serverOptions
	opt := WithCertProvider(nil)
	opt(&opts)
	if opts.certProv != nil {
		t.Error("expected nil certProv")
	}
}

// mockCertEnrollmentServicer is a no-op service.CertEnrollmentServicer double,
// used only to prove WithCertEnrollment assigns the field it configures.
type mockCertEnrollmentServicer struct{}

func (mockCertEnrollmentServicer) SignCertificate(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	return nil, nil
}

func (mockCertEnrollmentServicer) EnrollCertificate(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	return nil, nil
}

func (mockCertEnrollmentServicer) AddTrustAnchors(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
	return nil, nil
}

func (mockCertEnrollmentServicer) ListTrustAnchors(context.Context, string) ([]trustanchor.Anchor, error) {
	return nil, nil
}

func (mockCertEnrollmentServicer) RemoveTrustAnchor(context.Context, string, string) error {
	return nil
}

func (mockCertEnrollmentServicer) IssueDelegation(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, nil
}

func TestWithCertEnrollment_Nil(t *testing.T) {
	var opts serverOptions
	opt := WithCertEnrollment(nil)
	opt(&opts)
	if opts.certEnroll != nil {
		t.Error("expected nil certEnroll")
	}
}

func TestWithCertEnrollment_NonNil(t *testing.T) {
	var opts serverOptions
	svc := mockCertEnrollmentServicer{}
	opt := WithCertEnrollment(svc)
	opt(&opts)
	if opts.certEnroll != svc {
		t.Error("expected certEnroll to be set to the supplied CertEnrollmentServicer")
	}
}

// mockTenantOnboardingServicer is a no-op service.TenantOnboardingServicer
// double, used only to prove WithTenantOnboarding assigns the field it
// configures.
type mockTenantOnboardingServicer struct{}

func (mockTenantOnboardingServicer) OnboardTenant(context.Context, *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	return nil, nil
}

func TestWithTenantOnboarding_Nil(t *testing.T) {
	var opts serverOptions
	opt := WithTenantOnboarding(nil)
	opt(&opts)
	if opts.onboarding != nil {
		t.Error("expected nil onboarding")
	}
}

func TestWithTenantOnboarding_NonNil(t *testing.T) {
	var opts serverOptions
	svc := mockTenantOnboardingServicer{}
	opt := WithTenantOnboarding(svc)
	opt(&opts)
	if opts.onboarding != svc {
		t.Error("expected onboarding to be set to the supplied TenantOnboardingServicer")
	}
}

func TestWithAuthMiddleware(t *testing.T) {
	var opts serverOptions
	opt := WithAuthMiddleware(nil)
	opt(&opts)
	if opts.authMW != nil {
		t.Error("expected nil authMW")
	}
}

func TestWithDatabaseManager(t *testing.T) {
	var opts serverOptions
	opt := WithDatabaseManager(nil)
	opt(&opts)
	if opts.dbManager != nil {
		t.Error("expected nil dbManager")
	}
}

func TestWithDatabaseManager_NonNil(t *testing.T) {
	dataDir := t.TempDir()
	mgr := database.NewManager(nil, dataDir)
	defer func() { _ = mgr.Close() }()

	var opts serverOptions
	opt := WithDatabaseManager(mgr)
	opt(&opts)
	if opts.dbManager != mgr {
		t.Error("expected dbManager to be set")
	}
}

// ---------------------------------------------------------------------------
// Start with all optional dependencies (exercises all if-branches in start*)
// ---------------------------------------------------------------------------

// stubUserService is a minimal user.UserServicer for option propagation tests.
type stubUserService struct{}

func (s *stubUserService) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}
func (s *stubUserService) GetUser(_ context.Context, _ uint64) (*user.User, error) { return nil, nil }
func (s *stubUserService) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return nil, nil
}
func (s *stubUserService) GetUserByEmail(_ context.Context, _, _ string) (*user.User, error) {
	return nil, nil
}
func (s *stubUserService) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	return nil, nil
}
func (s *stubUserService) DeleteUser(_ context.Context, _ uint64) error { return nil }
func (s *stubUserService) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, nil
}
func (s *stubUserService) EnableUser(_ context.Context, _ uint64) error  { return nil }
func (s *stubUserService) DisableUser(_ context.Context, _ uint64) error { return nil }
func (s *stubUserService) SuspendUser(_ context.Context, _ uint64) error { return nil }
func (s *stubUserService) UnsuspendUser(_ context.Context, _ uint64) error {
	return nil
}
func (s *stubUserService) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (s *stubUserService) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

func (s *stubUserService) RemoveCertBinding(_ context.Context, _ uint64, _ string) (*user.User, error) {
	return nil, nil
}

// stubRoleService is a minimal role.RoleServicer for option propagation tests.
type stubRoleService struct{}

func (s *stubRoleService) CreateRole(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
	return nil, nil
}
func (s *stubRoleService) GetRole(_ context.Context, _ string) (*role.Role, error) { return nil, nil }
func (s *stubRoleService) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	return nil, nil
}
func (s *stubRoleService) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	return nil, nil
}
func (s *stubRoleService) DeleteRole(_ context.Context, _ string) error { return nil }
func (s *stubRoleService) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return nil, nil
}
func (s *stubRoleService) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return nil, nil
}

// stubCertProvider is a minimal cert.CertProvider for option propagation tests.
type stubCertProvider struct{}

func (s *stubCertProvider) IssueClientCert(_ context.Context, _ *cert.IssueRequest) (*cert.IssuedCert, error) {
	return nil, nil
}
func (s *stubCertProvider) ExportPKCS12(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
	return nil, nil
}
func (s *stubCertProvider) RevokeCert(_ context.Context, _ string) (*cert.RevokeResult, error) {
	return &cert.RevokeResult{}, nil
}
func (s *stubCertProvider) IsRevoked(_ context.Context, _ string) (bool, error) { return false, nil }
func (s *stubCertProvider) ListCerts(_ context.Context, _ string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (s *stubCertProvider) CACertPEM() []byte { return nil }

func TestStart_AllProtocols_WithAllOptions(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "opts.sock")
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		unix: true,
		mcp:  true,
	}, socketPath)

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	gatherer := prometheus.NewRegistry()
	caPool := x509.NewCertPool()
	resolver := &mockLeaderAdminResolver{}
	host := &mockHost{}
	userSvc := &stubUserService{}
	roleSvc := &stubRoleService{}
	certProv := &stubCertProvider{}
	authMW := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
		WithGatherer(gatherer),
		WithCACertPool(caPool),
		WithLeaderForwarding(resolver),
		WithHost(host),
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithCertProvider(certProv),
		WithAuthMiddleware(authMW),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Fatal("expected non-empty RESTAddr")
	}
	if srv.GRPCAddr() == "" {
		t.Fatal("expected non-empty GRPCAddr")
	}
	if srv.UnixAddr() == "" {
		t.Fatal("expected non-empty UnixAddr")
	}
	if srv.MCPAddr() == "" {
		t.Fatal("expected non-empty MCPAddr")
	}
}

// ---------------------------------------------------------------------------
// Verify interface compliance at compile time
// ---------------------------------------------------------------------------

var _ rest.LeaderAdminResolver = (*mockLeaderAdminResolver)(nil)
var _ rest.Host = (*mockHost)(nil)
var _ user.UserServicer = (*stubUserService)(nil)
var _ role.RoleServicer = (*stubRoleService)(nil)
var _ cert.CertProvider = (*stubCertProvider)(nil)

// Suppress unused import warning for os package.
var _ = os.Remove

// ---------------------------------------------------------------------------
// TLS helper for QUIC tests
// ---------------------------------------------------------------------------

// selfSignedTLSConfig generates a self-signed ECDSA P-256 TLS config
// valid for 127.0.0.1 and localhost. Used for QUIC tests that require
// a valid TLS configuration.
func selfSignedTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	parsedCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(parsedCert)

	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{certDER},
				PrivateKey:  key,
			},
		},
		RootCAs:    pool,
		MinVersion: tls.VersionTLS13,
	}
}

// ---------------------------------------------------------------------------
// QUICAddr tests
// ---------------------------------------------------------------------------

func TestQUICAddr_BeforeStart(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if addr := srv.QUICAddr(); addr != "" {
		t.Fatalf("expected empty QUICAddr before start, got %q", addr)
	}
}

func TestQUICAddr_NilQuicSrv(t *testing.T) {
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{})
	if addr := srv.QUICAddr(); addr != "" {
		t.Fatalf("expected empty QUICAddr when QUIC not configured, got %q", addr)
	}
}

func TestQUICAddr_AfterStart(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.QUICAddr()
	if addr == "" {
		t.Fatal("expected non-empty QUICAddr after start")
	}
	if addr == "127.0.0.1:0" {
		t.Fatal("QUICAddr returned unresolved :0")
	}
}

// ---------------------------------------------------------------------------
// Start/Stop with QUIC only
// ---------------------------------------------------------------------------

func TestStart_QUICOnly(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	quicAddr := srv.QUICAddr()
	if quicAddr == "" {
		t.Fatal("expected non-empty QUICAddr")
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
	if addr := srv.MCPAddr(); addr != "" {
		t.Fatalf("expected empty MCPAddr, got %q", addr)
	}
}

func TestStart_QUICOnly_NoTLS(t *testing.T) {
	// The QUIC transport is TLS-only. With neither an explicit TLS config nor a
	// CeremonyCA to auto-provision one, Start fails closed with a
	// *TLSUnavailableError rather than binding a plaintext QUIC listener.
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected QUIC start to fail closed without TLS or a CeremonyCA")
	}
	var unavailable *TLSUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected *TLSUnavailableError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// QUIC with all options
// ---------------------------------------------------------------------------

func TestStart_QUICOnly_WithAllOptions(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	userSvc := &stubUserService{}
	roleSvc := &stubRoleService{}
	certProv := &stubCertProvider{}
	authMW := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithCertProvider(certProv),
		WithAuthMiddleware(authMW),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.QUICAddr() == "" {
		t.Fatal("expected non-empty QUICAddr")
	}
}

// ---------------------------------------------------------------------------
// Start error: QUIC fails, REST + GRPC roll back
// ---------------------------------------------------------------------------

func TestStart_QUICFail_RESTAndGRPCRollback(t *testing.T) {
	// REST + GRPC + QUIC all auto-provision TLS from the CeremonyCA. REST and
	// GRPC start fine, but QUIC is forced to fail by an out-of-range UDP port so
	// the rollback of the earlier transports is exercised.
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
	}, "")
	cfg.Server.QUICPort = 70000 // out of the valid 0-65535 range: bind fails
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected QUIC start to fail on an out-of-range port")
	}

	// REST and GRPC should have been created before the QUIC failure.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
}

func TestStart_QUICFail_NoOtherProtocols(t *testing.T) {
	// QUIC only, no TLS and no CeremonyCA -> fails closed, no rollback needed.
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{})

	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected QUIC start to fail closed without TLS or a CeremonyCA")
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
	if srv.mcpSrv != nil {
		t.Fatal("expected mcpSrv to be nil")
	}
}

// ---------------------------------------------------------------------------
// Start error: Unix fails, REST + GRPC + QUIC roll back
// ---------------------------------------------------------------------------

func TestStart_UnixFail_RESTGRPCQUICRollback(t *testing.T) {
	// Create an invalid socket path to force Unix failure.
	tmpDir := t.TempDir()
	blockerFile := filepath.Join(tmpDir, "blocker")
	if err := writeTestFile(blockerFile); err != nil {
		t.Fatalf("failed to create blocker file: %v", err)
	}
	badSocketPath := filepath.Join(blockerFile, "sub", "qrdb.sock")

	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
	}, badSocketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected Unix start to fail with invalid socket path")
	}

	// REST, GRPC, and QUIC should have been created before the Unix failure.
	if srv.restSrv == nil {
		t.Fatal("expected restSrv to have been created before rollback")
	}
	if srv.grpcSrv == nil {
		t.Fatal("expected grpcSrv to have been created before rollback")
	}
	if srv.quicSrv == nil {
		t.Fatal("expected quicSrv to have been created before rollback")
	}
}

// ---------------------------------------------------------------------------
// Start error: MCP fails, REST + GRPC + QUIC + Unix roll back
// ---------------------------------------------------------------------------

func TestStart_MCPFail_AllRollback(t *testing.T) {
	// Occupy a port so MCP cannot bind to it.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create blocker listener: %v", err)
	}
	defer func() { _ = blocker.Close() }()

	_, portStr, _ := net.SplitHostPort(blocker.Addr().String())
	blockedPort, _ := strconv.Atoi(portStr)

	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	cfg.MCP.Port = blockedPort

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))
	startErr := srv.Start()
	if startErr == nil {
		srv.Stop()
		t.Fatal("expected MCP start to fail on occupied port")
	}

	// All four earlier servers should have been created before MCP failed.
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
}

// ---------------------------------------------------------------------------
// Start/Stop with all five protocols
// ---------------------------------------------------------------------------

func TestStart_AllFiveProtocols(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "all5.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

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

	quicAddr := srv.QUICAddr()
	if quicAddr == "" {
		t.Fatal("expected non-empty QUICAddr")
	}

	unixAddr := srv.UnixAddr()
	if unixAddr == "" {
		t.Fatal("expected non-empty UnixAddr")
	}

	mcpAddr := srv.MCPAddr()
	if mcpAddr == "" {
		t.Fatal("expected non-empty MCPAddr")
	}

	// Listeners on one network must be on different ports. QUIC listens on
	// UDP, whose port space is separate from TCP's, so the kernel may give it
	// the number a TCP listener holds.
	addrs := map[string]struct{ network, addr string }{
		"REST": {"tcp", restAddr},
		"gRPC": {"tcp", grpcAddr},
		"QUIC": {"udp", quicAddr},
		"MCP":  {"tcp", mcpAddr},
	}
	ports := make(map[string]string)
	for name, a := range addrs {
		_, port, _ := net.SplitHostPort(a.addr)
		key := a.network + "/" + port
		if existing, ok := ports[key]; ok {
			t.Fatalf("%s and %s share %s port %s", existing, name, a.network, port)
		}
		ports[key] = name
	}
}

// ---------------------------------------------------------------------------
// Stop all five protocols gracefully
// ---------------------------------------------------------------------------

func TestStop_AllFiveProtocols(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "stop5.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify all five are running.
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

	// Stop should complete without panic or hang.
	srv.Stop()
}

// ---------------------------------------------------------------------------
// Start/Stop with all five protocols and all options
// ---------------------------------------------------------------------------

func TestStart_AllFiveProtocols_WithAllOptions(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "all5opts.sock")
	tlsCfg := selfSignedTLSConfig(t)
	caPool := x509.NewCertPool()
	gatherer := prometheus.NewRegistry()
	resolver := &mockLeaderAdminResolver{}
	host := &mockHost{}
	userSvc := &stubUserService{}
	roleSvc := &stubRoleService{}
	certProv := &stubCertProvider{}
	authMW := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))

	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
		WithGatherer(gatherer),
		WithCACertPool(caPool),
		WithLeaderForwarding(resolver),
		WithHost(host),
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithCertProvider(certProv),
		WithAuthMiddleware(authMW),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

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
}

// ---------------------------------------------------------------------------
// stopQUIC when quicSrv is nil (nil guard path)
// ---------------------------------------------------------------------------

func TestStopQUIC_NilQuicSrv(t *testing.T) {
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{})
	// stopQUIC should not panic when quicSrv is nil.
	srv.stopQUIC()
}

// ---------------------------------------------------------------------------
// stopQUIC when quicSrv is non-nil (active server path)
// ---------------------------------------------------------------------------

func TestStopQUIC_ActiveServer(t *testing.T) {
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{quic: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify QUIC server is running.
	if srv.quicSrv == nil {
		t.Fatal("expected quicSrv to be non-nil")
	}

	// Stop should complete without panic or hang.
	srv.stopQUIC()
}

// ---------------------------------------------------------------------------
// Stub service implementations for option propagation tests.
// These satisfy the interface contracts without providing any real behavior.
// Methods are never invoked during server startup, so panicking stubs suffice.
// ---------------------------------------------------------------------------

// stubBatchService satisfies service.BatchServicer.
type stubBatchService struct{ service.BatchServicer }

// stubTimeSeriesService satisfies service.TimeSeriesServicer via the alias
// in pkg/service; the underlying interface lives in pkg/timeseries.
type stubTimeSeriesService struct{ service.TimeSeriesServicer }

// stubIndexService satisfies service.IndexServicer via the alias
// in pkg/service; the underlying interface lives in pkg/index.
type stubIndexService struct{ service.IndexServicer }

// stubVectorService satisfies service.VectorServicer (= service.VectorService).
type stubVectorService struct{ service.VectorServicer }

// stubDatabaseService satisfies service.DatabaseServicer.
type stubDatabaseService struct{ service.DatabaseServicer }

// stubBarrierService satisfies service.BarrierServicer.
type stubBarrierService struct{ service.BarrierServicer }

// stubCeremonyService satisfies service.CeremonyServicer.
type stubCeremonyService struct{ service.CeremonyServicer }

// stubCredentialService satisfies service.CredentialServicer.
type stubCredentialService struct{ service.CredentialServicer }

// stubCeremonyCA satisfies ceremony.CeremonyCA.
type stubCeremonyCA struct{ ceremony.CeremonyCA }

// stubCollectionService satisfies service.CollectionServicer.
type stubCollectionService struct{ service.CollectionServicer }

// stubMVCCService satisfies service.MVCCKVServicer.
type stubMVCCService struct{ service.MVCCKVServicer }

// stubTransactionService satisfies service.TransactionServicer.
type stubTransactionService struct{ service.TransactionServicer }

// stubWatchService satisfies service.WatchServicer.
type stubWatchService struct{ service.WatchServicer }

// stubLeaseService satisfies service.LeaseServicer.
type stubLeaseService struct{ service.LeaseServicer }

// Compile-time interface compliance checks.
var _ service.BatchServicer = (*stubBatchService)(nil)
var _ service.TimeSeriesServicer = (*stubTimeSeriesService)(nil)
var _ service.IndexServicer = (*stubIndexService)(nil)
var _ service.VectorServicer = (*stubVectorService)(nil)
var _ service.DatabaseServicer = (*stubDatabaseService)(nil)
var _ service.BarrierServicer = (*stubBarrierService)(nil)
var _ service.CeremonyServicer = (*stubCeremonyService)(nil)
var _ service.CredentialServicer = (*stubCredentialService)(nil)
var _ ceremony.CeremonyCA = (*stubCeremonyCA)(nil)
var _ service.CollectionServicer = (*stubCollectionService)(nil)
var _ service.MVCCKVServicer = (*stubMVCCService)(nil)
var _ service.TransactionServicer = (*stubTransactionService)(nil)
var _ service.WatchServicer = (*stubWatchService)(nil)
var _ service.LeaseServicer = (*stubLeaseService)(nil)

// ---------------------------------------------------------------------------
// ServerOption unit tests: zero-coverage option functions
// ---------------------------------------------------------------------------

func TestWithCABootstrap_Applied(t *testing.T) {
	certPEM := []byte("cert")
	var opts serverOptions
	opt := WithCABootstrap(certPEM)
	opt(&opts)
	if string(opts.caBootstrapCert) != "cert" {
		t.Errorf("expected caBootstrapCert %q, got %q", "cert", opts.caBootstrapCert)
	}
}

func TestWithBatch_Applied(t *testing.T) {
	svc := &stubBatchService{}
	var opts serverOptions
	opt := WithBatch(svc)
	opt(&opts)
	if opts.batchSvc != svc {
		t.Error("expected batchSvc to be set")
	}
}

func TestWithTimeSeries_Applied(t *testing.T) {
	svc := &stubTimeSeriesService{}
	var opts serverOptions
	opt := WithTimeSeries(svc)
	opt(&opts)
	if opts.tsSvc != svc {
		t.Error("expected tsSvc to be set")
	}
}

func TestWithIndex_Applied(t *testing.T) {
	svc := &stubIndexService{}
	var opts serverOptions
	opt := WithIndex(svc)
	opt(&opts)
	if opts.indexSvc != svc {
		t.Error("expected indexSvc to be set")
	}
}

func TestWithVector_Applied(t *testing.T) {
	svc := &stubVectorService{}
	var opts serverOptions
	opt := WithVector(svc)
	opt(&opts)
	if opts.vectorSvc != svc {
		t.Error("expected vectorSvc to be set")
	}
}

// logServiceStub is a LogService the option tests only compare by identity.
type logServiceStub struct{ service.LogService }

func TestWithLogService_Applied(t *testing.T) {
	svc := &logServiceStub{}
	var opts serverOptions
	WithLogService(svc)(&opts)
	if opts.logSvc != svc {
		t.Error("expected logSvc to be set")
	}
}

func TestWithLogService_Nil(t *testing.T) {
	opts := serverOptions{logSvc: &logServiceStub{}}
	WithLogService(nil)(&opts)
	if opts.logSvc != nil {
		t.Error("expected logSvc to be cleared")
	}
}

func TestWithDatabaseService_Applied(t *testing.T) {
	svc := &stubDatabaseService{}
	var opts serverOptions
	opt := WithDatabaseService(svc)
	opt(&opts)
	if opts.databaseSvc != svc {
		t.Error("expected databaseSvc to be set")
	}
}

func TestWithBarrierService_Applied(t *testing.T) {
	svc := &stubBarrierService{}
	var opts serverOptions
	opt := WithBarrierService(svc)
	opt(&opts)
	if opts.barrierSvc != svc {
		t.Error("expected barrierSvc to be set")
	}
}

func TestWithCeremonyService_Applied(t *testing.T) {
	svc := &stubCeremonyService{}
	var opts serverOptions
	opt := WithCeremonyService(svc)
	opt(&opts)
	if opts.ceremonySvc != svc {
		t.Error("expected ceremonySvc to be set")
	}
}

func TestWithCredentialService_Applied(t *testing.T) {
	svc := &stubCredentialService{}
	var opts serverOptions
	opt := WithCredentialService(svc)
	opt(&opts)
	if opts.credentialSvc != svc {
		t.Error("expected credentialSvc to be set")
	}
}

func TestWithCeremonyCA_Applied(t *testing.T) {
	ca := &stubCeremonyCA{}
	var opts serverOptions
	opt := WithCeremonyCA(ca)
	opt(&opts)
	if opts.ceremonyCA != ca {
		t.Error("expected ceremonyCA to be set")
	}
}

func TestWithCollectionService_Applied(t *testing.T) {
	svc := &stubCollectionService{}
	var opts serverOptions
	opt := WithCollectionService(svc)
	opt(&opts)
	if opts.collectionSvc != svc {
		t.Error("expected collectionSvc to be set")
	}
}

// ---------------------------------------------------------------------------
// Start with all optional service dependencies to cover every if-branch in
// startREST, startGRPC, startQUIC, startUnix, and startMCP.
// ---------------------------------------------------------------------------

func TestStart_AllProtocols_WithAllServiceOptions(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "allsvcs.sock")
	tlsCfg := selfSignedTLSConfig(t)
	caPool := x509.NewCertPool()
	gatherer := prometheus.NewRegistry()
	resolver := &mockLeaderAdminResolver{}
	host := &mockHost{}
	userSvc := &stubUserService{}
	roleSvc := &stubRoleService{}
	certProv := &stubCertProvider{}
	authMW := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))

	batchSvc := &stubBatchService{}
	tsSvc := &stubTimeSeriesService{}
	indexSvc := &stubIndexService{}
	vectorSvc := &stubVectorService{}
	dbSvc := &stubDatabaseService{}
	barrierSvc := &stubBarrierService{}
	ceremonySvc := &stubCeremonyService{}
	credentialSvc := &stubCredentialService{}
	ca := &stubCeremonyCA{}
	collectionSvc := &stubCollectionService{}
	mvccSvc := &stubMVCCService{}
	txSvc := &stubTransactionService{}
	watchSvc := &stubWatchService{}
	leaseSvc := &stubLeaseService{}

	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(tlsCfg),
		WithGatherer(gatherer),
		WithCACertPool(caPool),
		WithLeaderForwarding(resolver),
		WithHost(host),
		WithUserService(userSvc),
		WithRoleService(roleSvc),
		WithCertProvider(certProv),
		WithAuthMiddleware(authMW),
		WithBatch(batchSvc),
		WithTimeSeries(tsSvc),
		WithIndex(indexSvc),
		WithVector(vectorSvc),
		WithDatabaseService(dbSvc),
		WithBarrierService(barrierSvc),
		WithCeremonyService(ceremonySvc),
		WithCredentialService(credentialSvc),
		WithCeremonyCA(ca),
		WithCollectionService(collectionSvc),
		WithMVCC(mvccSvc),
		WithTransaction(txSvc),
		WithWatch(watchSvc),
		WithLease(leaseSvc),
		WithCABootstrap([]byte("cert")),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with all service options failed: %v", err)
	}
	defer srv.Stop()

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

	// Verify all options were stored.
	if srv.opts.batchSvc != batchSvc {
		t.Error("expected batchSvc to be stored")
	}
	if srv.opts.tsSvc != tsSvc {
		t.Error("expected tsSvc to be stored")
	}
	if srv.opts.indexSvc != indexSvc {
		t.Error("expected indexSvc to be stored")
	}
	if srv.opts.vectorSvc != vectorSvc {
		t.Error("expected vectorSvc to be stored")
	}
	// With authentication on, the stored database service is dbSvc behind the
	// provisioning check (TestServer_DatabaseCreateChecksProvisioningPermissions).
	if srv.opts.databaseSvc == nil || srv.opts.databaseSvc == dbSvc {
		t.Error("expected databaseSvc stored behind the provisioning check")
	}
	if srv.opts.barrierSvc != barrierSvc {
		t.Error("expected barrierSvc to be stored")
	}
	if srv.opts.ceremonySvc != ceremonySvc {
		t.Error("expected ceremonySvc to be stored")
	}
	if srv.opts.credentialSvc != credentialSvc {
		t.Error("expected credentialSvc to be stored")
	}
	if srv.opts.ceremonyCA != ca {
		t.Error("expected ceremonyCA to be stored")
	}
	if srv.opts.collectionSvc != collectionSvc {
		t.Error("expected collectionSvc to be stored")
	}
}

// ---------------------------------------------------------------------------
// StartEtcd and StopEtcd on Server: exercises the startEtcdServerFn /
// stopEtcdServerFn dispatch paths.
// ---------------------------------------------------------------------------

// saveEtcdFns saves the package-level etcd function pointers and returns
// a restore function that should be deferred by the caller.
func saveEtcdFns() func() {
	origStart := startEtcdServerFn
	origStop := stopEtcdServerFn
	return func() {
		startEtcdServerFn = origStart
		stopEtcdServerFn = origStop
	}
}

// mockEtcdServer implements EtcdProtocolServer for testing.
type mockEtcdServer struct {
	addr    string
	stopped bool
}

func (m *mockEtcdServer) Start() error { return nil }
func (m *mockEtcdServer) Stop()        { m.stopped = true }
func (m *mockEtcdServer) Addr() string { return m.addr }

// Compile-time interface compliance.
var _ EtcdProtocolServer = (*mockEtcdServer)(nil)

func TestStartEtcd_NilFn(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	startEtcdServerFn = nil

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// StartEtcd refuses with a typed capability error when startEtcdServerFn
	// is nil (etcd support not compiled in), rather than silently accepting
	// a configuration it can never serve.
	err := srv.StartEtcd("testdb", &database.EtcdConfig{})
	var capErr *service.CapabilityUnsupportedError
	if !errors.As(err, &capErr) {
		t.Fatalf("StartEtcd() error = %v, want *service.CapabilityUnsupportedError", err)
	}

	// EtcdAddr should still return empty.
	if addr := srv.EtcdAddr("testdb"); addr != "" {
		t.Fatalf("expected empty EtcdAddr, got %q", addr)
	}
}

func TestServerStartEtcd_Success(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	mockSrv := &mockEtcdServer{addr: "127.0.0.1:2379"}
	startEtcdServerFn = func(s *Server, dbName string, cfg *database.EtcdConfig) (EtcdProtocolServer, error) {
		s.etcdServers.Store(dbName, EtcdProtocolServer(mockSrv))
		return mockSrv, nil
	}
	stopEtcdServerFn = func(srv EtcdProtocolServer) {
		srv.Stop()
	}

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := srv.StartEtcd("testdb", &database.EtcdConfig{}); err != nil {
		t.Fatalf("StartEtcd failed: %v", err)
	}

	addr := srv.EtcdAddr("testdb")
	if addr != "127.0.0.1:2379" {
		t.Fatalf("expected EtcdAddr %q, got %q", "127.0.0.1:2379", addr)
	}
}

func TestStopEtcd_NilFn(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	stopEtcdServerFn = nil

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// StopEtcd should not panic when stopEtcdServerFn is nil.
	srv.StopEtcd("testdb")
}

func TestStopEtcd_NotRunning(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	stopped := false
	stopEtcdServerFn = func(srv EtcdProtocolServer) {
		stopped = true
		srv.Stop()
	}

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// StopEtcd for a non-existent database should not call stopEtcdServerFn.
	srv.StopEtcd("nonexistent")

	if stopped {
		t.Fatal("expected stopEtcdServerFn not to be called for non-existent database")
	}
}

func TestStopEtcd_Running(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	mockSrv := &mockEtcdServer{addr: "127.0.0.1:2379"}

	stopEtcdServerFn = func(srv EtcdProtocolServer) {
		srv.Stop()
	}

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// Manually inject a running etcd server into the map.
	srv.etcdServers.Store("testdb", EtcdProtocolServer(mockSrv))

	srv.StopEtcd("testdb")

	if !mockSrv.stopped {
		t.Fatal("expected etcd mock server to be stopped")
	}

	// Verify the entry was removed from the map.
	if addr := srv.EtcdAddr("testdb"); addr != "" {
		t.Fatalf("expected EtcdAddr to be empty after StopEtcd, got %q", addr)
	}
}

// ---------------------------------------------------------------------------
// stopAllEtcd: exercises the Range branch when etcd servers are present.
// ---------------------------------------------------------------------------

func TestStopAllEtcd_MultipleServers(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	stoppedDBs := make(map[string]bool)
	mock1 := &mockEtcdServer{addr: "127.0.0.1:2379"}
	mock2 := &mockEtcdServer{addr: "127.0.0.1:2380"}

	stopEtcdServerFn = func(srv EtcdProtocolServer) {
		srv.Stop()
		switch srv.Addr() {
		case "127.0.0.1:2379":
			stoppedDBs["db1"] = true
		case "127.0.0.1:2380":
			stoppedDBs["db2"] = true
		}
	}

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// Inject two etcd servers directly into the sync.Map.
	srv.etcdServers.Store("db1", EtcdProtocolServer(mock1))
	srv.etcdServers.Store("db2", EtcdProtocolServer(mock2))

	// Stop() calls stopAllEtcd internally.
	srv.Stop()

	if !mock1.stopped {
		t.Error("expected db1 etcd server to be stopped")
	}
	if !mock2.stopped {
		t.Error("expected db2 etcd server to be stopped")
	}

	// Both entries should have been removed from the map.
	if addr := srv.EtcdAddr("db1"); addr != "" {
		t.Fatalf("expected EtcdAddr for db1 to be empty after Stop, got %q", addr)
	}
	if addr := srv.EtcdAddr("db2"); addr != "" {
		t.Fatalf("expected EtcdAddr for db2 to be empty after Stop, got %q", addr)
	}
}

func TestStopAllEtcd_NilFn(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	startEtcdServerFn = nil
	stopEtcdServerFn = nil

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// stopAllEtcd should not panic when stopEtcdServerFn is nil.
	srv.stopAllEtcd()
}

func TestStopAllEtcd_EmptyMap(t *testing.T) {
	restore := saveEtcdFns()
	defer restore()

	called := false
	stopEtcdServerFn = func(srv EtcdProtocolServer) {
		called = true
		srv.Stop()
	}

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	// No etcd servers in the map; Range body should never execute.
	srv.stopAllEtcd()

	if called {
		t.Fatal("expected stopEtcdServerFn not to be called when map is empty")
	}
}

// CompareAndSwap mirrors CompareAndDelete's guard on the edit side: it
// replaces the value only when the stored one still equals expected.
//
// It is implemented rather than stubbed. A stub returning (false, nil)
// satisfies the runtime type assertion that discovers AtomicKVStore while
// exercising none of the behaviour, so every caller's guarded-swap path reads
// as "refused" and its success branch is never taken — worse than no double at
// all, because the assertion passes (go-qrdb#56).
func (m *mockKVService) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		// A swap does not create.
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	m.store[key] = next
	return true, nil
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (s *stubRoleService) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// TenantForSerial holds no records, so every serial is the deny-by-serial case.
func (s *stubCertProvider) TenantForSerial(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}
