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

package mcp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockKVService implements service.KVServicer for testing.
type mockKVService struct {
	mu    sync.RWMutex
	store map[string][]byte

	putFn    func(ctx context.Context, key string, value []byte) error
	getFn    func(ctx context.Context, key string) ([]byte, error)
	deleteFn func(ctx context.Context, key string) error
	listFn   func(ctx context.Context, prefix string) ([]string, error)
}

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = value
	return nil
}

// PutIfAbsent stores only when the key is absent, matching the real
// engines. The map lookup is the same check they perform.
//
// The lock is held across the check AND the write, and on a GUARDED verb that
// is not merely the map-safety every sibling here needs. Check-then-act under
// no lock is the exact shape these verbs exist to replace, so a mock that
// wrote it could report two winners for one key while the handler under test
// was blameless — a test double cannot stand in for an atomic service by
// being non-atomic.
func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected, under
// one lock for the reason given on PutIfAbsent.
func (m *mockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *mockKVService) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.store))
	for k := range m.store {
		if prefix == "" || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			result[k] = v
		}
	}
	return result, nil
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

func (m *mockKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = value
	return nil
}

func (m *mockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	return nil
}

func (m *mockKVService) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVService) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVService) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

func (m *mockKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	v, err := m.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return v != nil, nil
}

// mockAdminService implements service.AdminServicer for testing.
type mockAdminService struct {
	healthFn          func(ctx context.Context) (*service.HealthStatus, error)
	statusFn          func(ctx context.Context) (*service.StatusInfo, error)
	peersFn           func(ctx context.Context) (*service.PeersInfo, error)
	addMemberFn       func(ctx context.Context, shardID, replicaID uint64, raftAddr string) error
	removeMemberFn    func(ctx context.Context, shardID, replicaID uint64) error
	requestSnapshotFn func(ctx context.Context, shardID uint64) error
	drainFn           func(ctx context.Context) error
	capabilitiesFn    func(ctx context.Context) (*capabilities.Capabilities, error)
	startShardFn      func(ctx context.Context, req *service.StartShardRequest) error
	stopShardFn       func(ctx context.Context, shardID uint64) error
	getShardStateFn   func(ctx context.Context, shardID uint64) (*service.ShardStateResponse, error)
	getTopologyFn     func(ctx context.Context) (*service.TopologyResponse, error)
}

func newMockAdminService() *mockAdminService {
	return &mockAdminService{}
}

func (m *mockAdminService) Health(ctx context.Context) (*service.HealthStatus, error) {
	if m.healthFn != nil {
		return m.healthFn(ctx)
	}
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(ctx context.Context) (*service.StatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.StatusInfo{
		NodeID:      "node-1",
		RaftAddress: "127.0.0.1:63001",
		IsLeader:    true,
		LeaderID:    1,
		ShardCount:  1,
	}, nil
}

func (m *mockAdminService) Peers(ctx context.Context) (*service.PeersInfo, error) {
	if m.peersFn != nil {
		return m.peersFn(ctx)
	}
	return &service.PeersInfo{
		Shards: []service.ShardPeers{
			{
				ShardID:  0,
				LeaderID: 1,
				Peers: []service.PeerInfo{
					{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
				},
			},
		},
	}, nil
}

func (m *mockAdminService) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr, _ string) error {
	if m.addMemberFn != nil {
		return m.addMemberFn(ctx, shardID, replicaID, raftAddr)
	}
	return nil
}

func (m *mockAdminService) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	if m.removeMemberFn != nil {
		return m.removeMemberFn(ctx, shardID, replicaID)
	}
	return nil
}

func (m *mockAdminService) RequestSnapshot(ctx context.Context, shardID uint64) error {
	if m.requestSnapshotFn != nil {
		return m.requestSnapshotFn(ctx, shardID)
	}
	return nil
}

func (m *mockAdminService) Drain(ctx context.Context) error {
	if m.drainFn != nil {
		return m.drainFn(ctx)
	}
	return nil
}

func (m *mockAdminService) Capabilities(ctx context.Context) (*capabilities.Capabilities, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx)
	}
	return &capabilities.Capabilities{
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Index:       true,
		TimeSeries:  false,
		Vector:      false,
	}, nil
}

func (m *mockAdminService) StartShard(ctx context.Context, req *service.StartShardRequest) error {
	if m.startShardFn != nil {
		return m.startShardFn(ctx, req)
	}
	return nil
}

func (m *mockAdminService) StopShard(ctx context.Context, shardID uint64) error {
	if m.stopShardFn != nil {
		return m.stopShardFn(ctx, shardID)
	}
	return nil
}

func (m *mockAdminService) GetShardState(ctx context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if m.getShardStateFn != nil {
		return m.getShardStateFn(ctx, shardID)
	}
	return &service.ShardStateResponse{}, nil
}

func (m *mockAdminService) GetTopology(ctx context.Context) (*service.TopologyResponse, error) {
	if m.getTopologyFn != nil {
		return m.getTopologyFn(ctx)
	}
	return &service.TopologyResponse{}, nil
}

func (m *mockAdminService) NodeCompaction(_ context.Context) (*compaction.Policy, error) {
	return nil, nil
}

func (m *mockAdminService) SetNodeCompaction(_ context.Context, _ *compaction.Policy) error {
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildCallToolRequest constructs a CallToolRequest from an arguments map.
func buildCallToolRequest(t *testing.T, toolName string, args map[string]any) mcp.CallToolRequest {
	t.Helper()
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	}
}

// resultText extracts the text string from the first TextContent item in a
// CallToolResult. It fails the test if the content slice is empty or the
// first item is not TextContent.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("result has no content items")
	}
	tc, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		t.Fatalf("first content item is not TextContent: %T", result.Content[0])
	}
	return tc.Text
}

// testTLSConfig generates a self-signed TLS config for testing.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

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
// Server construction tests
// ---------------------------------------------------------------------------

func TestNewServer(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")

	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.kvSvc != kvSvc {
		t.Error("kvSvc not set")
	}
	if s.adminSvc != adminSvc {
		t.Error("adminSvc not set")
	}
	if s.mcpSrv == nil {
		t.Error("mcpSrv not initialized")
	}
}

func TestNewServer_WithTLS(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tlsCfg := testTLSConfig(t)

	s := NewServer(kvSvc, adminSvc, ":0", WithTLS(tlsCfg))

	// WithTLS clones the config and pins the TLS 1.3 floor, so the stored
	// config is a distinct pointer with MinVersion >= TLS 1.3.
	if s.tlsConfig == nil {
		t.Fatal("TLS config not applied")
	}
	if s.tlsConfig.MinVersion < tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want >= TLS 1.3 (%x)", s.tlsConfig.MinVersion, tls.VersionTLS13)
	}
}

// TestNewServer_WithTLS_MinVersionFloorSurvivesDynamicTrustPoolHook is the
// TLS 1.2 downgrade proof (go-qrdb#560 review, B2-3): tlsCfg already carries
// a GetConfigForClient hook -- standing in for pkg/clienttrust.Pool.Wire,
// shared across every client-facing listener to trust every tenant's Key B
// CA -- whose returned config was cloned from a TLS-1.2-floor snapshot
// taken before this listener's TLS 1.3 floor was ever applied. A bare
// clone-and-mutate (the pre-fix WithTLS) loses the floor on exactly the
// connections that take this dynamic path; WithTLS must re-assert it.
func TestNewServer_WithTLS_MinVersionFloorSurvivesDynamicTrustPoolHook(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tlsCfg := testTLSConfig(t)
	keyBPool := &tls.Config{MinVersion: tls.VersionTLS12}
	tlsCfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return keyBPool, nil
	}

	s := NewServer(kvSvc, adminSvc, ":0", WithTLS(tlsCfg))
	if s.tlsConfig == nil || s.tlsConfig.GetConfigForClient == nil {
		t.Fatal("expected the dynamic hook to be preserved, not dropped")
	}
	got, err := s.tlsConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("the config returned by the dynamic hook was NOT reasserted: MinVersion = %#x, want TLS 1.3", got.MinVersion)
	}
}

// ---------------------------------------------------------------------------
// Server lifecycle tests
// ---------------------------------------------------------------------------

func TestServer_StartStop(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0", WithTLS(testTLSConfig(t)))

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	addr := s.Addr()
	if addr == "" || addr == ":0" {
		t.Error("Addr should resolve to an ephemeral port")
	}

	// Verify the listener is accepting connections.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
}

func TestServer_StartStop_TLS(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tlsCfg := testTLSConfig(t)

	s := NewServer(kvSvc, adminSvc, ":0", WithTLS(tlsCfg))

	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	addr := s.Addr()
	if addr == "" || addr == ":0" {
		t.Error("Addr should resolve to an ephemeral port")
	}

	// Verify TLS handshake succeeds. Use ServerName to match the cert's
	// DNS SAN regardless of whether the listener binds to IPv4 or IPv6.
	clientCfg := &tls.Config{
		RootCAs:    tlsCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 2 * time.Second},
		"tcp", addr, clientCfg,
	)
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	_ = conn.Close()
}

// TestServer_Start_NoTLS_FailsClosed verifies the MCP transport is TLS-only:
// Start without a WithTLS config returns *TLSNotConfiguredError rather than
// binding a plaintext listener.
func TestServer_Start_NoTLS_FailsClosed(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0")
	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("Start() with no TLS returned nil; want *TLSNotConfiguredError")
	}
	var tlsErr *TLSNotConfiguredError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("Start() error type = %T, want *TLSNotConfiguredError", err)
	}
}

// TestServer_Start_WeakTLS_Rejected verifies the MCP transport enforces the
// TLS 1.3 floor: an explicitly weaker MinVersion is rejected at Start.
func TestServer_Start_WeakTLS_Rejected(t *testing.T) {
	cfg := testTLSConfig(t)
	cfg.MinVersion = tls.VersionTLS12
	s := NewServer(newMockKVService(), newMockAdminService(), ":0", WithTLS(cfg))
	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("Start() with TLS 1.2 MinVersion returned nil; want *WeakTLSVersionError")
	}
	var weak *WeakTLSVersionError
	if !errors.As(err, &weak) {
		t.Fatalf("Start() error type = %T, want *WeakTLSVersionError", err)
	}
}

func TestServer_Start_BadAddress(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, "invalid-addr-no-port", WithTLS(testTLSConfig(t)))

	err := s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("expected error for invalid address")
	}

	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Errorf("expected *StartError, got %T", err)
	}
}

func TestServer_Stop_NilHTTPServer(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	// Stop without Start should not panic.
	s.Stop()
}

func TestServer_Addr_BeforeStart(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":9999")

	if got := s.Addr(); got != ":9999" {
		t.Errorf("Addr before Start: got %q, want %q", got, ":9999")
	}
}

// ---------------------------------------------------------------------------
// Tool registration tests
// ---------------------------------------------------------------------------

func TestServer_ToolRegistration(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")

	expectedTools := []string{
		"qrdb_put",
		"qrdb_get",
		"qrdb_delete",
		"qrdb_list",
		"qrdb_scan",
		// The guarded write verbs register with the KV set (go-qrdb#127), so
		// a server with a KV service has them whether or not it has anything
		// else.
		"qrdb_put_if_absent",
		"qrdb_compare_and_delete",
		"qrdb_compare_and_swap",
		"qrdb_health",
		"qrdb_status",
		"qrdb_peers",
		"qrdb_add_member",
		"qrdb_remove_member",
		"qrdb_snapshot",
		"qrdb_drain",
		"qrdb_capabilities",
		"qrdb_start_shard",
		"qrdb_stop_shard",
		"qrdb_shard_state",
		"qrdb_topology",
		"qrdb_node_compaction",
		"qrdb_update_node_compaction",
		"qrdb_mint_join_token",
		// Registered unconditionally, like the kv/admin tools above, not
		// gated behind a barrier service or migrator (go-qrdb#558): the
		// handler itself refuses with a typed error when no
		// EncryptionMigrator is configured, rather than the tool being
		// absent.
		"qrdb_barrier_enable_encryption",
	}
	// The raft-only families are registered unconditionally and refuse at
	// call time when the node has no such service.
	for _, group := range [][]serverTool{mvccTools(nil), watchTools(nil), leaseTools(nil), lockTools(nil)} {
		for _, tl := range group {
			expectedTools = append(expectedTools, tl.Tool.Name)
		}
	}

	registeredTools := s.MCPServer().ListTools()

	for _, name := range expectedTools {
		if _, ok := registeredTools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}

	if len(registeredTools) != len(expectedTools) {
		t.Errorf("expected %d tools, got %d", len(expectedTools), len(registeredTools))
	}
}

// ---------------------------------------------------------------------------
// KV tool handler tests
// ---------------------------------------------------------------------------

func TestHandlePut_Success(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handlePut(kvSvc)

	req := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"key":   "mykey",
		"value": "bXl2YWx1ZQ==", // base64("myvalue")
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handlePut: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}

	// Verify the decoded value was stored.
	kvSvc.mu.RLock()
	stored := string(kvSvc.store["mykey"])
	kvSvc.mu.RUnlock()
	if stored != "myvalue" {
		t.Errorf("stored value: got %q, want %q", stored, "myvalue")
	}
}

func TestHandlePut_MissingKey(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handlePut(kvSvc)

	req := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"value": "myvalue",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandlePut_MissingValue(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handlePut(kvSvc)

	req := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"key": "mykey",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing value")
	}
}

func TestHandlePut_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.putFn = func(_ context.Context, _ string, _ []byte) error {
		return &service.ProposeError{Err: errors.New("raft timeout")}
	}
	handler := handlePut(kvSvc)

	req := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"key":   "mykey",
		"value": "bXl2YWx1ZQ==", // base64("myvalue")
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_put" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_put")
	}
}

func TestHandleGet_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["hello"] = []byte("world")
	handler := handleGet(kvSvc)

	req := buildCallToolRequest(t, "qrdb_get", map[string]any{
		"key": "hello",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGet: %v", err)
	}

	// The result is base64-encoded (RFC 4648) so arbitrary binary values
	// round-trip byte-for-byte.
	text := resultText(t, result)
	if text != "d29ybGQ=" {
		t.Errorf("expected base64(%q), got %q", "world", text)
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if string(decoded) != "world" {
		t.Errorf("decoded value: got %q, want %q", decoded, "world")
	}
}

func TestHandleGet_MissingKey(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handleGet(kvSvc)

	req := buildCallToolRequest(t, "qrdb_get", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandleGet_KeyNotFound(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handleGet(kvSvc)

	req := buildCallToolRequest(t, "qrdb_get", map[string]any{
		"key": "nonexistent",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleDelete_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["goodbye"] = []byte("value")
	handler := handleDelete(kvSvc)

	req := buildCallToolRequest(t, "qrdb_delete", map[string]any{
		"key": "goodbye",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDelete: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}

	kvSvc.mu.RLock()
	_, exists := kvSvc.store["goodbye"]
	kvSvc.mu.RUnlock()
	if exists {
		t.Error("key should have been deleted")
	}
}

func TestHandleDelete_MissingKey(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handleDelete(kvSvc)

	req := buildCallToolRequest(t, "qrdb_delete", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing key")
	}
}

func TestHandleDelete_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.deleteFn = func(_ context.Context, _ string) error {
		return &service.ProposeError{Err: errors.New("raft timeout")}
	}
	handler := handleDelete(kvSvc)

	req := buildCallToolRequest(t, "qrdb_delete", map[string]any{
		"key": "mykey",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleList_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["app/a"] = []byte("1")
	kvSvc.store["app/b"] = []byte("2")
	kvSvc.store["other"] = []byte("3")
	handler := handleList(kvSvc)

	req := buildCallToolRequest(t, "qrdb_list", map[string]any{
		"prefix": "app/",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleList: %v", err)
	}

	text := resultText(t, result)
	var keys []string
	if jsonErr := json.Unmarshal([]byte(text), &keys); jsonErr != nil {
		t.Fatalf("unmarshal keys: %v", jsonErr)
	}

	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d: %v", len(keys), keys)
	}
}

func TestHandleList_EmptyPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["a"] = []byte("1")
	kvSvc.store["b"] = []byte("2")
	handler := handleList(kvSvc)

	req := buildCallToolRequest(t, "qrdb_list", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleList: %v", err)
	}

	text := resultText(t, result)
	var keys []string
	if jsonErr := json.Unmarshal([]byte(text), &keys); jsonErr != nil {
		t.Fatalf("unmarshal keys: %v", jsonErr)
	}

	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d: %v", len(keys), keys)
	}
}

func TestHandleList_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.listFn = func(_ context.Context, _ string) ([]string, error) {
		return nil, &service.ReadError{Err: errors.New("read timeout")}
	}
	handler := handleList(kvSvc)

	req := buildCallToolRequest(t, "qrdb_list", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Admin tool handler tests
// ---------------------------------------------------------------------------

func TestHandleHealth_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleHealth(adminSvc)

	req := buildCallToolRequest(t, "qrdb_health", nil)

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleHealth: %v", err)
	}

	text := resultText(t, result)
	var hs service.HealthStatus
	if jsonErr := json.Unmarshal([]byte(text), &hs); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if hs.Status != "ok" {
		t.Errorf("status: got %q, want %q", hs.Status, "ok")
	}
	if !hs.Leader {
		t.Error("expected leader=true")
	}
}

func TestHandleHealth_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthFn = func(_ context.Context) (*service.HealthStatus, error) {
		return nil, &service.DrainError{Err: errors.New("host draining")}
	}
	handler := handleHealth(adminSvc)

	req := buildCallToolRequest(t, "qrdb_health", nil)

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleStatus_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleStatus(adminSvc)

	req := buildCallToolRequest(t, "qrdb_status", nil)

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleStatus: %v", err)
	}

	text := resultText(t, result)
	var si service.StatusInfo
	if jsonErr := json.Unmarshal([]byte(text), &si); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if si.NodeID != "node-1" {
		t.Errorf("node_id: got %q, want %q", si.NodeID, "node-1")
	}
	if si.ShardCount != 1 {
		t.Errorf("shard_count: got %d, want %d", si.ShardCount, 1)
	}
}

func TestHandleStatus_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.statusFn = func(_ context.Context) (*service.StatusInfo, error) {
		return nil, errors.New("internal error")
	}
	handler := handleStatus(adminSvc)

	req := buildCallToolRequest(t, "qrdb_status", nil)

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandlePeers_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handlePeers(adminSvc)

	req := buildCallToolRequest(t, "qrdb_peers", nil)

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handlePeers: %v", err)
	}

	text := resultText(t, result)
	var pi service.PeersInfo
	if jsonErr := json.Unmarshal([]byte(text), &pi); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(pi.Shards) != 1 {
		t.Errorf("shards: got %d, want %d", len(pi.Shards), 1)
	}
	if len(pi.Shards[0].Peers) != 1 {
		t.Errorf("peers: got %d, want %d", len(pi.Shards[0].Peers), 1)
	}
}

func TestHandlePeers_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.peersFn = func(_ context.Context) (*service.PeersInfo, error) {
		return nil, errors.New("internal error")
	}
	handler := handlePeers(adminSvc)

	req := buildCallToolRequest(t, "qrdb_peers", nil)

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleAddMember_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleAddMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_add_member", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(2),
		"raft_addr":  "127.0.0.1:63002",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleAddMember: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleAddMember_MissingShardID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleAddMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_add_member", map[string]any{
		"replica_id": float64(2),
		"raft_addr":  "127.0.0.1:63002",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing shard_id")
	}
}

func TestHandleAddMember_MissingReplicaID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleAddMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_add_member", map[string]any{
		"shard_id":  float64(1),
		"raft_addr": "127.0.0.1:63002",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing replica_id")
	}
}

func TestHandleAddMember_MissingRaftAddr(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleAddMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_add_member", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing raft_addr")
	}
}

func TestHandleAddMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.addMemberFn = func(_ context.Context, _, _ uint64, _ string) error {
		return &service.MembershipError{Op: "add", Err: errors.New("timeout")}
	}
	handler := handleAddMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_add_member", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(2),
		"raft_addr":  "127.0.0.1:63002",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleRemoveMember_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleRemoveMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_remove_member", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRemoveMember: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleRemoveMember_MissingShardID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleRemoveMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_remove_member", map[string]any{
		"replica_id": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing shard_id")
	}
}

func TestHandleRemoveMember_MissingReplicaID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleRemoveMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_remove_member", map[string]any{
		"shard_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing replica_id")
	}
}

func TestHandleRemoveMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.removeMemberFn = func(_ context.Context, _, _ uint64) error {
		return &service.MembershipError{Op: "remove", Err: errors.New("timeout")}
	}
	handler := handleRemoveMember(adminSvc)

	req := buildCallToolRequest(t, "qrdb_remove_member", map[string]any{
		"shard_id":   float64(1),
		"replica_id": float64(2),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleSnapshot_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleSnapshot(adminSvc)

	req := buildCallToolRequest(t, "qrdb_snapshot", map[string]any{
		"shard_id": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSnapshot: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleSnapshot_MissingShardID(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleSnapshot(adminSvc)

	req := buildCallToolRequest(t, "qrdb_snapshot", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing shard_id")
	}
}

func TestHandleSnapshot_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.requestSnapshotFn = func(_ context.Context, _ uint64) error {
		return &service.SnapshotRequestError{Err: errors.New("busy")}
	}
	handler := handleSnapshot(adminSvc)

	req := buildCallToolRequest(t, "qrdb_snapshot", map[string]any{
		"shard_id": float64(1),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleDrain_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleDrain(adminSvc)

	req := buildCallToolRequest(t, "qrdb_drain", nil)

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDrain: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleDrain_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.drainFn = func(_ context.Context) error {
		return &service.DrainError{Err: errors.New("already draining")}
	}
	handler := handleDrain(adminSvc)

	req := buildCallToolRequest(t, "qrdb_drain", nil)

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleCapabilities_Success(t *testing.T) {
	adminSvc := newMockAdminService()
	handler := handleCapabilities(adminSvc)

	req := buildCallToolRequest(t, "qrdb_capabilities", nil)

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCapabilities: %v", err)
	}

	text := resultText(t, result)
	var caps capabilities.Capabilities
	if jsonErr := json.Unmarshal([]byte(text), &caps); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if !caps.KV {
		t.Error("expected kv=true")
	}
	if !caps.MVCC {
		t.Error("expected mvcc=true")
	}
	if !caps.Transaction {
		t.Error("expected transaction=true")
	}
	if !caps.Batch {
		t.Error("expected batch=true")
	}
	if !caps.Watch {
		t.Error("expected watch=true")
	}
	if !caps.Lease {
		t.Error("expected lease=true")
	}
	if !caps.Index {
		t.Error("expected index=true")
	}
	if caps.TimeSeries {
		t.Error("expected timeSeries=false")
	}
	if caps.Vector {
		t.Error("expected vector=false")
	}
}

func TestHandleCapabilities_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.capabilitiesFn = func(_ context.Context) (*capabilities.Capabilities, error) {
		return nil, errors.New("capabilities unavailable")
	}
	handler := handleCapabilities(adminSvc)

	req := buildCallToolRequest(t, "qrdb_capabilities", nil)

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_capabilities" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_capabilities")
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestStartError(t *testing.T) {
	inner := errors.New("address in use")
	err := &StartError{Addr: ":8080", Err: inner}

	if got := err.Error(); got != "mcp: server failed to start on :8080: address in use" {
		t.Errorf("error message: %q", got)
	}

	if err.Unwrap() != inner {
		t.Error("Unwrap did not return inner error")
	}
}

func TestToolError(t *testing.T) {
	inner := errors.New("timeout")
	err := &ToolError{Tool: "qrdb_get", Err: inner}

	if got := err.Error(); got != "mcp: tool qrdb_get failed: timeout" {
		t.Errorf("error message: %q", got)
	}

	if err.Unwrap() != inner {
		t.Error("Unwrap did not return inner error")
	}
}

func TestToolError_ErrorsAs(t *testing.T) {
	inner := &service.KeyNotFoundError{Key: "missing"}
	err := &ToolError{Tool: "qrdb_get", Err: inner}

	var knf *service.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Error("errors.As should find KeyNotFoundError through ToolError")
	}
	if knf.Key != "missing" {
		t.Errorf("key: got %q, want %q", knf.Key, "missing")
	}
}

// ---------------------------------------------------------------------------
// MCPServer accessor test
// ---------------------------------------------------------------------------

func TestServer_MCPServer(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()

	s := NewServer(kvSvc, adminSvc, ":0")
	if s.MCPServer() == nil {
		t.Error("MCPServer() returned nil")
	}
	if s.MCPServer() != s.mcpSrv {
		t.Error("MCPServer() returned different instance")
	}
}

// CompareAndSwap is the edit half of the guarded set (go-qrdb#56), and it
// GUARDS rather than returning a constant.
//
// It was a `return false, nil` stub while no test drove the verb through a
// tool handler. A stub that always declines is safe only until something
// asserts the true branch — from go-qrdb#127 the qrdb_compare_and_swap handler
// does, and against a stub "the swap was refused" would be indistinguishable
// from "the handler never reached the service". An absent key is left absent,
// as it is in the real engines: a swap is not a create.
func (m *mockKVService) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.store[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	m.store[key] = next
	return true, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
