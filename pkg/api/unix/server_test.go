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

package unix

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
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

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

// PutIfAbsent stores only when the key is absent, matching the real
// engines. The map lookup is the same check they perform.
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

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
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
	m.store[key] = value
	return nil
}

func (m *mockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
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
	_, err := m.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return true, nil
}

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

func (m *mockAdminService) StartShard(_ context.Context, _ *service.StartShardRequest) error {
	return nil
}

func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	return &service.ShardStateResponse{
		ShardID:           shardID,
		ConfigChangeIndex: 0,
		Term:              0,
		LeaderID:          0,
		HasLeader:         false,
	}, nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{
		NodeID:      "test-node",
		RaftAddress: "127.0.0.1:63001",
		ShardCount:  1,
		Shards:      []service.TopologyShardInfo{},
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// tempSocketPath returns a unique socket path under os.TempDir for tests.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "qrdb-test.sock")
}

// dialUnix creates a gRPC client connection to the given Unix socket path.
func dialUnix(t *testing.T, socketPath string) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial unix socket %s: %v", socketPath, err)
	}
	return conn
}

// dialUnixTLS dials the unix socket over TLS, trusting the server certificate
// in serverCfg and verifying it under the "localhost" server name. It is the
// TLS-only counterpart of dialUnix used by the round-trip tests now that the
// Unix transport refuses plaintext connections.
func dialUnixTLS(t *testing.T, socketPath string, serverCfg *tls.Config) *grpc.ClientConn {
	t.Helper()

	clientTLS := &tls.Config{
		RootCAs:    serverCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(grpccredentials.NewTLS(clientTLS)),
	)
	if err != nil {
		t.Fatalf("failed to dial unix socket %s over TLS: %v", socketPath, err)
	}
	return conn
}

// ---------------------------------------------------------------------------
// NewServer tests
// ---------------------------------------------------------------------------

func TestNewServer_CreatesServer(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	socketPath := "/tmp/test-qrdb.sock"

	srv := NewServer(kvSvc, adminSvc, socketPath, WithTLS(selfSignedTLSConfig(t)))
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	if srv.kvSvc == nil {
		t.Fatal("kvSvc not set")
	}
	if srv.adminSvc == nil {
		t.Fatal("adminSvc not set")
	}
	if srv.socketPath != socketPath {
		t.Fatalf("expected socketPath %q, got %q", socketPath, srv.socketPath)
	}
}

func TestNewServer_WithOptions(t *testing.T) {
	called := false
	opt := func(s *Server) {
		called = true
	}

	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock", opt, WithTLS(selfSignedTLSConfig(t)))
	if !called {
		t.Fatal("option function was not called")
	}
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
}

// ---------------------------------------------------------------------------
// Addr tests
// ---------------------------------------------------------------------------

func TestAddr_BeforeStart(t *testing.T) {
	socketPath := "/tmp/test-qrdb-addr.sock"
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))
	if addr := srv.Addr(); addr != socketPath {
		t.Fatalf("expected Addr() = %q before start, got %q", socketPath, addr)
	}
}

func TestAddr_AfterStart(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr()
	if addr != socketPath {
		t.Fatalf("expected Addr() = %q, got %q", socketPath, addr)
	}
}

// ---------------------------------------------------------------------------
// Start/Stop lifecycle tests
// ---------------------------------------------------------------------------

func TestStart_CreatesSocketFile(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Verify socket file exists.
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("socket file not created: %v", err)
	}
	if info.Mode().Type()&os.ModeSocket == 0 {
		t.Fatal("file at socket path is not a Unix socket")
	}
}

func TestStop_RemovesSocketFile(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	srv.Stop()

	// Verify socket file is cleaned up.
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("socket file not removed after Stop")
	}
}

func TestStop_BeforeStart(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-never.sock", WithTLS(selfSignedTLSConfig(t)))
	// Stop before Start should not panic.
	srv.Stop()
}

func TestStop_CalledTwice(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	srv.Stop()
	// Second Stop should not panic.
	srv.Stop()
}

func TestMultipleStartStopCycles(t *testing.T) {
	socketPath := tempSocketPath(t)

	for i := range 3 {
		srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))

		if err := srv.Start(); err != nil {
			t.Fatalf("cycle %d: Start failed: %v", i, err)
		}

		// Verify socket exists.
		if _, err := os.Stat(socketPath); err != nil {
			t.Fatalf("cycle %d: socket not created: %v", i, err)
		}

		srv.Stop()

		// Verify socket cleaned up.
		if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
			t.Fatalf("cycle %d: socket not removed after Stop", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Stale socket cleanup
// ---------------------------------------------------------------------------

func TestStart_CleansUpStaleSocket(t *testing.T) {
	socketPath := tempSocketPath(t)

	// Create a stale socket file by starting and stopping a listener
	// without removing the file.
	staleListener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create stale socket: %v", err)
	}
	_ = staleListener.Close()
	// Socket file still exists after close.

	// Start should succeed despite the stale socket.
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed with stale socket: %v", err)
	}
	defer srv.Stop()
}

// ---------------------------------------------------------------------------
// Start error: invalid path
// ---------------------------------------------------------------------------

func TestStart_InvalidPath_ReturnsError(t *testing.T) {
	// Use a path under /proc which is not writable.
	socketPath := "/proc/nonexistent/deep/nested/qrdb.sock"
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))

	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("expected Start to fail on invalid path")
	}

	var mkdirErr *MkdirError
	if !errors.As(err, &mkdirErr) {
		t.Fatalf("expected MkdirError, got %T: %v", err, err)
	}
}

func TestStart_ParentDirCreated(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub", "deep")
	socketPath := filepath.Join(nested, "qrdb.sock")

	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(selfSignedTLSConfig(t)))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Verify nested directories were created.
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("parent directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("parent path is not a directory")
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestStartError_Format(t *testing.T) {
	inner := errors.New("bind failed")
	err := &StartError{SocketPath: "/tmp/test.sock", Err: inner}

	expected := "unix: server failed to start on /tmp/test.sock: bind failed"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestMkdirError_Format(t *testing.T) {
	inner := errors.New("permission denied")
	err := &MkdirError{Dir: "/proc/bad", Err: inner}

	expected := "unix: failed to create socket directory /proc/bad: permission denied"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// gRPC client integration: KV operations
// ---------------------------------------------------------------------------

func TestGRPC_PutGetDeleteList(t *testing.T) {
	socketPath := tempSocketPath(t)
	kvSvc := newMockKVService()
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(kvSvc, &mockAdminService{}, socketPath, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	ctx := context.Background()

	// Put
	_, err := client.Put(ctx, &pb.PutRequest{Key: "hello", Value: []byte("world")})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	getResp, err := client.Get(ctx, &pb.GetRequest{Key: "hello"})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(getResp.GetValue()) != "world" {
		t.Fatalf("expected value %q, got %q", "world", string(getResp.GetValue()))
	}

	// List
	listResp, err := client.List(ctx, &pb.ListRequest{Prefix: ""})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(listResp.GetKeys()) != 1 || listResp.GetKeys()[0] != "hello" {
		t.Fatalf("expected [hello], got %v", listResp.GetKeys())
	}

	// Delete
	_, err = client.Delete(ctx, &pb.DeleteRequest{Key: "hello"})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get after delete returns NotFound.
	_, err = client.Get(ctx, &pb.GetRequest{Key: "hello"})
	if err == nil {
		t.Fatal("expected error on Get after Delete")
	}
}

func TestGRPC_GetNonExistentKey(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err := client.Get(context.Background(), &pb.GetRequest{Key: "missing"})
	if err == nil {
		t.Fatal("expected error on Get of non-existent key")
	}
}

func TestGRPC_ListWithPrefix(t *testing.T) {
	socketPath := tempSocketPath(t)
	kvSvc := newMockKVService()
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(kvSvc, &mockAdminService{}, socketPath, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	ctx := context.Background()

	// Insert keys with different prefixes.
	for _, kv := range []struct{ k, v string }{
		{"app/config", "cfg1"},
		{"app/data", "data1"},
		{"sys/info", "info1"},
	} {
		if _, err := client.Put(ctx, &pb.PutRequest{Key: kv.k, Value: []byte(kv.v)}); err != nil {
			t.Fatalf("Put %q failed: %v", kv.k, err)
		}
	}

	listResp, err := client.List(ctx, &pb.ListRequest{Prefix: "app/"})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(listResp.GetKeys()) != 2 {
		t.Fatalf("expected 2 keys with prefix app/, got %d: %v", len(listResp.GetKeys()), listResp.GetKeys())
	}
}

// ---------------------------------------------------------------------------
// gRPC client integration: Admin operations
// ---------------------------------------------------------------------------

func TestGRPC_Health(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	resp, err := client.Health(context.Background(), nil)
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if resp.GetStatus() != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", resp.GetStatus())
	}
	if !resp.GetLeader() {
		t.Fatal("expected leader=true")
	}
}

func TestGRPC_Status(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	resp, err := client.Status(context.Background(), nil)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if resp.GetNodeId() != "test-node" {
		t.Fatalf("expected node_id %q, got %q", "test-node", resp.GetNodeId())
	}
	if !resp.GetIsLeader() {
		t.Fatal("expected is_leader=true")
	}
}

// ---------------------------------------------------------------------------
// ServerOption coverage: WithUserService, WithRoleService, WithCertProvider
// ---------------------------------------------------------------------------

func TestWithUserService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithUserService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.userSvc != nil {
		t.Fatal("expected nil userSvc")
	}
}

func TestWithRoleService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithRoleService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.roleSvc != nil {
		t.Fatal("expected nil roleSvc")
	}
}

func TestWithCertProvider_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithCertProvider(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.certProv != nil {
		t.Fatal("expected nil certProv")
	}
}

// ---------------------------------------------------------------------------
// Start with optional services exercises the non-nil branches in Start()
// ---------------------------------------------------------------------------

// stubUserSvc satisfies user.UserServicer for option propagation tests.
type stubUserSvc struct{}

func (s *stubUserSvc) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}
func (s *stubUserSvc) GetUser(_ context.Context, _ uint64) (*user.User, error) { return nil, nil }
func (s *stubUserSvc) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return nil, nil
}
func (s *stubUserSvc) GetUserByEmail(_ context.Context, _, _ string) (*user.User, error) {
	return nil, nil
}
func (s *stubUserSvc) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	return nil, nil
}
func (s *stubUserSvc) DeleteUser(_ context.Context, _ uint64) error { return nil }
func (s *stubUserSvc) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, nil
}
func (s *stubUserSvc) EnableUser(_ context.Context, _ uint64) error  { return nil }
func (s *stubUserSvc) DisableUser(_ context.Context, _ uint64) error { return nil }
func (s *stubUserSvc) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (s *stubUserSvc) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

func (s *stubUserSvc) RemoveCertBinding(_ context.Context, _ uint64, _ string) (*user.User, error) {
	return nil, nil
}

// stubRoleSvc satisfies role.RoleServicer for option propagation tests.
type stubRoleSvc struct{}

func (s *stubRoleSvc) CreateRole(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
	return nil, nil
}
func (s *stubRoleSvc) GetRole(_ context.Context, _ string) (*role.Role, error) { return nil, nil }
func (s *stubRoleSvc) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	return nil, nil
}
func (s *stubRoleSvc) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	return nil, nil
}
func (s *stubRoleSvc) DeleteRole(_ context.Context, _ string) error { return nil }
func (s *stubRoleSvc) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return nil, nil
}
func (s *stubRoleSvc) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return nil, nil
}

// stubCertProv satisfies cert.CertProvider for option propagation tests.
type stubCertProv struct{}

func (s *stubCertProv) GenerateClientCert(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, nil
}
func (s *stubCertProv) ExportPKCS12(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
	return nil, nil
}
func (s *stubCertProv) RevokeCert(_ context.Context, _ string) (*cert.RevokeResult, error) {
	return &cert.RevokeResult{}, nil
}
func (s *stubCertProv) IsRevoked(_ context.Context, _ string) (bool, error) { return false, nil }
func (s *stubCertProv) ListCerts(_ context.Context, _ string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (s *stubCertProv) CACertPEM() []byte { return nil }

func TestStart_WithAllOptionalServices(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithUserService(&stubUserSvc{}),
		WithRoleService(&stubRoleSvc{}),
		WithCertProvider(&stubCertProv{}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() == "" {
		t.Fatal("expected non-empty Addr")
	}
}

// ---------------------------------------------------------------------------
// WithTLS / WithAuthMiddleware: option propagation and TLS enforcement
// ---------------------------------------------------------------------------

// selfSignedTLSConfig generates a self-signed ECDSA P-256 TLS config valid
// for "localhost", mirroring pkg/server's test helper of the same name.
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

// TestWithTLS_Applied verifies the WithTLS option accepts nil as a no-op
// (identical to omitting WithTLS entirely). A nil config leaves s.tls unset;
// Start then fails closed with a *TLSNotConfiguredError (see
// TestStart_NoTLS_FailsClosed) because the Unix transport is TLS-only.
func TestWithTLS_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tls.sock",
		WithTLS(nil),
	)
	if srv.tls != nil {
		t.Fatal("expected nil tls")
	}
}

// TestWithTLS_NonNil verifies the WithTLS option propagates a cloned,
// non-nil *tls.Config through to the Server struct. The clone is a distinct
// pointer from the caller's config (WithTLS never mutates or retains the
// caller's copy), so the fields are compared instead of pointer identity.
func TestWithTLS_NonNil(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tls-nn.sock",
		WithTLS(cfg),
	)
	if srv.tls == nil {
		t.Fatal("expected tls field to be set")
	}
	if srv.tls == cfg {
		t.Fatal("expected WithTLS to clone the config, not retain the caller's pointer")
	}
	if srv.tls.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %#x, want TLS 1.3", srv.tls.MinVersion)
	}
}

// TestWithTLS_DefaultsUnsetMinVersionToTLS13 verifies WithTLS upgrades an
// unset MinVersion to TLS 1.3 rather than leaving the config's default
// (which permits TLS 1.0+) in place.
func TestWithTLS_DefaultsUnsetMinVersionToTLS13(t *testing.T) {
	cfg := &tls.Config{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tls-default.sock",
		WithTLS(cfg),
	)
	if srv.tls == nil || srv.tls.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected MinVersion upgraded to TLS 1.3, got %+v", srv.tls)
	}
}

// TestWithTLS_RejectsWeakExplicitMinVersion verifies WithTLS records a
// *WeakTLSVersionError (surfaced by Start) when the caller explicitly
// configures a MinVersion below TLS 1.3.
func TestWithTLS_RejectsWeakExplicitMinVersion(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tls-weak.sock",
		WithTLS(cfg),
	)
	err := srv.Start()
	var weakErr *WeakTLSVersionError
	if !errors.As(err, &weakErr) {
		t.Fatalf("expected *WeakTLSVersionError, got %T: %v", err, err)
	}
}

// TestStart_NoTLS_FailsClosed proves the Unix transport is TLS-only: when no
// TLS config is supplied via WithTLS, Start fails closed with a
// *TLSNotConfiguredError rather than bind an unauthenticated plaintext
// listener. In production the orchestrator (pkg/server) auto-provisions a
// certificate from the node's CeremonyCA, so a normal deployment never reaches
// this path.
func TestStart_NoTLS_FailsClosed(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)
	err := srv.Start()
	var tlsErr *TLSNotConfiguredError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("expected *TLSNotConfiguredError, got %T: %v", err, err)
	}
}

// TestWithAuthMiddleware_Applied verifies the WithAuthMiddleware option
// accepts nil (no RBAC enforcement, matching embedded/test deployments that
// do not configure auth).
func TestWithAuthMiddleware_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-authmw.sock",
		WithAuthMiddleware(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.authMW != nil {
		t.Fatal("expected nil authMW")
	}
}

// TestStart_WithTLS_InsecureClientRejected proves that once WithTLS is
// configured, a plaintext (insecure-credentials) client can no longer reach
// the unix listener -- the "verified TLS 1.3 only" posture required by
// cmd/qrdb and cmd/qrdbd's CLAUDE.md.
func TestStart_WithTLS_InsecureClientRejected(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithTLS(tlsCfg),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnix(t, socketPath)
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err := client.Health(context.Background(), nil)
	if err == nil {
		t.Fatal("expected Health to fail over a plaintext connection to a TLS-configured unix listener")
	}
}

// TestStart_WithTLS_ClientAccepted proves a client that dials with matching
// TLS credentials (trusting the server's certificate) can complete the
// handshake and reach the KV service over the unix socket.
func TestStart_WithTLS_ClientAccepted(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithTLS(tlsCfg),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	clientTLS := &tls.Config{
		RootCAs:    tlsCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(grpccredentials.NewTLS(clientTLS)),
	)
	if err != nil {
		t.Fatalf("failed to dial unix socket with TLS: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	resp, err := client.Health(context.Background(), nil)
	if err != nil {
		t.Fatalf("Health over TLS unix socket failed: %v", err)
	}
	if resp.GetStatus() != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", resp.GetStatus())
	}
}

// ---------------------------------------------------------------------------
// recoveryInterceptor: panic recovery path
// ---------------------------------------------------------------------------

func TestRecoveryInterceptor_NoPanic(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}

	resp, err := recoveryInterceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected response %q, got %v", "ok", resp)
	}
}

func TestRecoveryInterceptor_PanicRecovered(t *testing.T) {
	handler := func(_ context.Context, _ any) (any, error) {
		panic("test panic")
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Panicker"}

	resp, err := recoveryInterceptor(context.Background(), nil, info, handler)
	if err == nil {
		t.Fatal("expected error from panic recovery")
	}
	if resp != nil {
		t.Fatalf("expected nil response after panic, got %v", resp)
	}
}

// ---------------------------------------------------------------------------
// Stubs for remaining ServerOption interfaces
// ---------------------------------------------------------------------------

// stubBarrierSvc satisfies service.BarrierServicer for option propagation tests.
type stubBarrierSvc struct{}

func (s *stubBarrierSvc) BarrierInitialize(_ context.Context, _ []byte) error { return nil }
func (s *stubBarrierSvc) BarrierUnseal(_ context.Context, _ []byte) error     { return nil }
func (s *stubBarrierSvc) BarrierSeal(_ context.Context) error                 { return nil }
func (s *stubBarrierSvc) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierInitializeShamir(_ context.Context, _ []byte, _, _ int) (*service.ShamirInitInfo, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierUnsealWithShare(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierUnsealWithShares(_ context.Context, _ []string) error { return nil }
func (s *stubBarrierSvc) BarrierRekey(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierGenerateRecoveryKeys(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierRecoverWithKeys(_ context.Context, _ []string) error { return nil }
func (s *stubBarrierSvc) BarrierDeleteRecoveryKeys(_ context.Context) error          { return nil }
func (s *stubBarrierSvc) BarrierHasRecoveryKeys(_ context.Context) (bool, error)     { return false, nil }
func (s *stubBarrierSvc) BarrierGenerateRootToken(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return nil, nil
}
func (s *stubBarrierSvc) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return nil, nil
}

// stubCeremonySvc satisfies service.CeremonyServicer for option propagation tests.
type stubCeremonySvc struct{}

func (s *stubCeremonySvc) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyBeginClaimCert(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyCompleteClaimCert(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyBeginClaimShare(_ context.Context, _, _ string) (*service.CeremonyChallenge, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyCompleteClaimShare(_ context.Context, _, _ string, _ []byte, _ string) (*service.CeremonyShareClaimResult, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	return nil, nil
}
func (s *stubCeremonySvc) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return nil, nil
}

// stubCredentialSvc satisfies service.CredentialServicer for option propagation tests.
type stubCredentialSvc struct{}

func (s *stubCredentialSvc) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (s *stubCredentialSvc) CredentialRetrieve(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}
func (s *stubCredentialSvc) CredentialSubmit(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (s *stubCredentialSvc) CredentialDelete(_ context.Context, _ string) error { return nil }
func (s *stubCredentialSvc) CredentialStrategy() string                         { return "stub" }
func (s *stubCredentialSvc) CredentialList(_ context.Context) ([]string, error) {
	return nil, nil
}
func (s *stubCredentialSvc) CredentialRotate(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
	return &credentials.RotateResult{}, nil
}
func (s *stubCredentialSvc) CredentialRetrieveVersion(_ context.Context, _ string, _ uint64) ([]byte, error) {
	return nil, nil
}
func (s *stubCredentialSvc) CredentialListVersions(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
	return nil, nil
}
func (s *stubCredentialSvc) CredentialGCExpired(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// stubCollectionSvc satisfies service.CollectionServicer for option propagation tests.
type stubCollectionSvc struct{}

func (s *stubCollectionSvc) CreateCollection(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	return nil, nil
}
func (s *stubCollectionSvc) DropCollection(_ context.Context, _ *service.DropCollectionRequest) error {
	return nil
}
func (s *stubCollectionSvc) ListCollections(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	return nil, nil
}
func (s *stubCollectionSvc) GetCollectionInfo(_ context.Context, _, _ string) (*service.CollectionInfo, error) {
	return nil, nil
}
func (s *stubCollectionSvc) InsertDocument(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	return nil, nil
}
func (s *stubCollectionSvc) GetDocument(_ context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	return nil, nil
}
func (s *stubCollectionSvc) UpdateDocument(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	return nil, nil
}
func (s *stubCollectionSvc) DeleteDocument(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	return nil, nil
}
func (s *stubCollectionSvc) ListDocuments(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	return nil, nil
}

// stubTransactionSvc satisfies service.TransactionServicer for option propagation tests.
type stubTransactionSvc struct{}

func (s *stubTransactionSvc) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	return nil, nil
}
func (s *stubTransactionSvc) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return nil, nil
}
func (s *stubTransactionSvc) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, nil
}

// stubBatchSvc satisfies service.BatchServicer for option propagation tests.
type stubBatchSvc struct{}

func (s *stubBatchSvc) Execute(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
	return nil, nil
}
func (s *stubBatchSvc) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	return nil, nil
}
func (s *stubBatchSvc) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return nil, nil
}
func (s *stubBatchSvc) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return nil, nil
}

// stubWatchSvc satisfies service.WatchServicer for option propagation tests.
type stubWatchSvc struct{}

func (s *stubWatchSvc) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return nil, nil
}
func (s *stubWatchSvc) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	return nil, nil
}
func (s *stubWatchSvc) WatchEntity(_ context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}
func (s *stubWatchSvc) WatchEntityType(_ context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, nil
}

// stubLeaseSvc satisfies lease.LeaseServicer for option propagation tests.
type stubLeaseSvc struct{}

func (s *stubLeaseSvc) Grant(_ context.Context, _ int64) (*lease.Lease, error)     { return nil, nil }
func (s *stubLeaseSvc) Revoke(_ context.Context, _ int64) error                    { return nil }
func (s *stubLeaseSvc) KeepAlive(_ context.Context, _ int64) (*lease.Lease, error) { return nil, nil }
func (s *stubLeaseSvc) TimeToLive(_ context.Context, _ int64) (*lease.Lease, error) {
	return nil, nil
}
func (s *stubLeaseSvc) Leases(_ context.Context) ([]*lease.Lease, error) { return nil, nil }

// stubIndexSvc satisfies index.IndexServicer for option propagation tests.
type stubIndexSvc struct{}

func (s *stubIndexSvc) CreateIndex(_ context.Context, _, _ string, _ index.IndexConfig) error {
	return nil
}
func (s *stubIndexSvc) DropIndex(_ context.Context, _ string) error { return nil }
func (s *stubIndexSvc) Query(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (s *stubIndexSvc) Scan(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (s *stubIndexSvc) ListIndexes(_ context.Context) ([]string, error) { return nil, nil }

// stubTimeSeriesSvc satisfies timeseries.TimeSeriesServicer for option propagation tests.
type stubTimeSeriesSvc struct{}

func (s *stubTimeSeriesSvc) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	return nil
}
func (s *stubTimeSeriesSvc) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error {
	return nil
}
func (s *stubTimeSeriesSvc) Query(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return nil, nil
}
func (s *stubTimeSeriesSvc) Aggregate(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	return nil, nil
}
func (s *stubTimeSeriesSvc) WritePoint(_ context.Context, _ *timeseries.DataPoint) error { return nil }
func (s *stubTimeSeriesSvc) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return nil, nil
}
func (s *stubTimeSeriesSvc) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return nil
}
func (s *stubTimeSeriesSvc) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return nil, nil
}
func (s *stubTimeSeriesSvc) SetRetentionPolicy(_ string, _ *timeseries.RetentionPolicy) error {
	return nil
}
func (s *stubTimeSeriesSvc) ListMetrics(_ string, _ int) ([]string, error) { return nil, nil }

func (s *stubTimeSeriesSvc) QueryWithConsistency(ctx context.Context, metric string, start, end time.Time, _ consistency.ReadOptions, opts ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return s.Query(ctx, metric, start, end, opts...)
}

func (s *stubTimeSeriesSvc) AggregateWithConsistency(ctx context.Context, metric string, start, end time.Time, fn timeseries.AggFunc, interval time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return s.Aggregate(ctx, metric, start, end, fn, interval)
}

func (s *stubTimeSeriesSvc) QueryRichWithConsistency(ctx context.Context, req *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return s.QueryRich(ctx, req)
}

func (s *stubTimeSeriesSvc) ListMetricsWithConsistency(_ context.Context, prefix string, limit int, _ consistency.ReadOptions) ([]string, error) {
	return s.ListMetrics(prefix, limit)
}

// stubDatabaseSvc satisfies service.DatabaseServicer for option propagation tests.
type stubDatabaseSvc struct{}

func (s *stubDatabaseSvc) Create(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) Get(_ string) (*database.DatabaseInfo, error) { return nil, nil }
func (s *stubDatabaseSvc) List() ([]database.DatabaseInfo, error)       { return nil, nil }
func (s *stubDatabaseSvc) Drop(_ context.Context, _ string) error       { return nil }
func (s *stubDatabaseSvc) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) SealDatabase(_ context.Context, _ string) error { return nil }
func (s *stubDatabaseSvc) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}
func (s *stubDatabaseSvc) DatabaseBarrierStatus(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) CreateForTenant(_ context.Context, _ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) DropForTenant(_ context.Context, _ string) error { return nil }
func (s *stubDatabaseSvc) UpdateProtocolForTenant(_ context.Context, _ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateEtcdForTenant(_ context.Context, _ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateRaftForTenant(_ context.Context, _ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateBarrierForTenant(_ context.Context, _ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) UpdateConnectionForTenant(_ context.Context, _ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}
func (s *stubDatabaseSvc) ScaleForTenant(_ context.Context, _ string, _ int) (*database.DatabaseInfo, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// ServerOption tests: With* option functions that were previously uncovered
// ---------------------------------------------------------------------------

func TestWithBarrierService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithBarrierService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.barrierSvc != nil {
		t.Fatal("expected nil barrierSvc")
	}
}

func TestWithCeremonyService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithCeremonyService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.ceremonySvc != nil {
		t.Fatal("expected nil ceremonySvc")
	}
}

func TestWithCredentialService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithCredentialService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.credentialSvc != nil {
		t.Fatal("expected nil credentialSvc")
	}
}

func TestWithCollectionService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithCollectionService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.collectionSvc != nil {
		t.Fatal("expected nil collectionSvc")
	}
}

func TestWithMVCC_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithMVCC(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.mvccSvc != nil {
		t.Fatal("expected nil mvccSvc")
	}
}

func TestWithTransaction_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithTransaction(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.txSvc != nil {
		t.Fatal("expected nil txSvc")
	}
}

func TestWithBatch_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithBatch(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.batchSvc != nil {
		t.Fatal("expected nil batchSvc")
	}
}

func TestWithWatch_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithWatch(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.watchSvc != nil {
		t.Fatal("expected nil watchSvc")
	}
}

func TestWithLease_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithLease(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.leaseSvc != nil {
		t.Fatal("expected nil leaseSvc")
	}
}

func TestWithIndex_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithIndex(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.indexSvc != nil {
		t.Fatal("expected nil indexSvc")
	}
}

func TestWithTimeSeries_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithTimeSeries(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.tsSvc != nil {
		t.Fatal("expected nil tsSvc")
	}
}

func TestWithVector_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithVector(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.vectorSvc != nil {
		t.Fatal("expected nil vectorSvc")
	}
}

func TestWithDatabaseService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock",
		WithDatabaseService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.databaseSvc != nil {
		t.Fatal("expected nil databaseSvc")
	}
}

// stubMVCCSvc satisfies service.MVCCKVServicer (extends KVServicer) for option propagation tests.
type stubMVCCSvc struct {
	mockKVService
}

func (s *stubMVCCSvc) PutWithLease(_ context.Context, _ string, _ []byte, _ int64) (*service.KeyValue, error) {
	return nil, nil
}
func (s *stubMVCCSvc) PutWithLeaseAndMode(_ context.Context, _ string, _ []byte, _ int64, _ writemode.Mode) (*service.KeyValue, error) {
	return nil, nil
}
func (s *stubMVCCSvc) GetRevision(_ context.Context, _ string) (*service.KeyValue, error) {
	return nil, nil
}
func (s *stubMVCCSvc) GetRevisionWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (*service.KeyValue, error) {
	return nil, nil
}
func (s *stubMVCCSvc) GetAtRevision(_ context.Context, _ string, _ int64) (*service.KeyValue, error) {
	return nil, nil
}
func (s *stubMVCCSvc) GetAtRevisionWithConsistency(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*service.KeyValue, error) {
	return nil, nil
}
func (s *stubMVCCSvc) Range(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return nil, 0, 0, nil
}
func (s *stubMVCCSvc) RangeWithConsistency(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return nil, 0, 0, nil
}
func (s *stubMVCCSvc) Compact(_ context.Context, _ int64) error         { return nil }
func (s *stubMVCCSvc) CurrentRevision(_ context.Context) (int64, error) { return 0, nil }

// stubVectorSvc satisfies service.VectorServicer for option propagation tests.
type stubVectorSvc struct{}

func (s *stubVectorSvc) CreateNamespace(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) DeleteNamespace(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) ListNamespaces(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) GetNamespaceInfo(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) UpsertVector(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) GetVector(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) DeleteVector(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) BatchUpsert(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) BatchDeleteVectors(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) ListVectors(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) UpdateMetadata(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) SearchSimilar(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) SearchByMetadata(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) HybridSearch(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) SearchByID(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) RebuildIndex(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) GetRebuildProgress(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) GetIndexStats(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) CompactNamespace(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return nil, nil
}
func (s *stubVectorSvc) GetCompactionProgress(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return nil, nil
}

// TestStart_WithNonNilOptionalServices exercises all non-nil conditional
// branches inside Start() that build the grpcpkg.RegisterOption slice.
func TestStart_WithNonNilOptionalServices(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithBarrierService(&stubBarrierSvc{}),
		WithCeremonyService(&stubCeremonySvc{}),
		WithCredentialService(&stubCredentialSvc{}),
		WithCollectionService(&stubCollectionSvc{}),
		WithMVCC(&stubMVCCSvc{mockKVService: mockKVService{store: make(map[string][]byte)}}),
		WithTransaction(&stubTransactionSvc{}),
		WithBatch(&stubBatchSvc{}),
		WithWatch(&stubWatchSvc{}),
		WithLease(&stubLeaseSvc{}),
		WithIndex(&stubIndexSvc{}),
		WithTimeSeries(&stubTimeSeriesSvc{}),
		WithVector(&stubVectorSvc{}),
		WithDatabaseService(&stubDatabaseSvc{}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with full optional services failed: %v", err)
	}
	defer srv.Stop()

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty Addr after Start")
	}
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

var _ service.KVServicer = (*mockKVService)(nil)
var _ service.AdminServicer = (*mockAdminService)(nil)
var _ user.UserServicer = (*stubUserSvc)(nil)
var _ role.RoleServicer = (*stubRoleSvc)(nil)
var _ cert.CertProvider = (*stubCertProv)(nil)
var _ service.BarrierServicer = (*stubBarrierSvc)(nil)
var _ service.CeremonyServicer = (*stubCeremonySvc)(nil)
var _ service.CredentialServicer = (*stubCredentialSvc)(nil)
var _ service.CollectionServicer = (*stubCollectionSvc)(nil)
var _ service.MVCCKVServicer = (*stubMVCCSvc)(nil)
var _ service.TransactionServicer = (*stubTransactionSvc)(nil)
var _ service.BatchServicer = (*stubBatchSvc)(nil)
var _ service.WatchServicer = (*stubWatchSvc)(nil)
var _ lease.LeaseServicer = (*stubLeaseSvc)(nil)
var _ index.IndexServicer = (*stubIndexSvc)(nil)
var _ timeseries.TimeSeriesServicer = (*stubTimeSeriesSvc)(nil)
var _ service.VectorServicer = (*stubVectorSvc)(nil)
var _ service.DatabaseServicer = (*stubDatabaseSvc)(nil)

// RestoreToRevision satisfies service.MVCCKVServicer.
func (s *stubMVCCSvc) RestoreToRevision(context.Context, *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return nil, nil
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (s *stubRoleSvc) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(a0 context.Context, prefix string) ([]string, error) {
	return m.List(a0, prefix)
}

// TenantForSerial holds no records, so every serial is the deny-by-serial case.
func (s *stubCertProv) TenantForSerial(_ context.Context, _ string) (string, bool, error) {
	return "", false, nil
}
