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
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, "unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("failed to dial unix socket %s: %v", socketPath, err)
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

	srv := NewServer(kvSvc, adminSvc, socketPath)
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

	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test.sock", opt)
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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)
	if addr := srv.Addr(); addr != socketPath {
		t.Fatalf("expected Addr() = %q before start, got %q", socketPath, addr)
	}
}

func TestAddr_AfterStart(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

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
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-never.sock")
	// Stop before Start should not panic.
	srv.Stop()
}

func TestStop_CalledTwice(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

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
		srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)
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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

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

	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)
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
	srv := NewServer(kvSvc, &mockAdminService{}, socketPath)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnix(t, socketPath)
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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnix(t, socketPath)
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
	srv := NewServer(kvSvc, &mockAdminService{}, socketPath)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnix(t, socketPath)
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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnix(t, socketPath)
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
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn := dialUnix(t, socketPath)
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
// Interface compliance
// ---------------------------------------------------------------------------

var _ service.KVServicer = (*mockKVService)(nil)
var _ service.AdminServicer = (*mockAdminService)(nil)
