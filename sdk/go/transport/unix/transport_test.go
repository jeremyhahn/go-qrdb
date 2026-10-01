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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/connectivity"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

type mockKVService struct {
	store   map[string][]byte
	putErr  error
	getErr  error
	delErr  error
	listErr error
	scanErr error
}

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.store[key] = value
	return nil
}

func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if m.putErr != nil {
		return false, m.putErr
	}
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
	if m.getErr != nil {
		return nil, m.getErr
	}
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) Delete(_ context.Context, key string) error {
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKVService) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	key := string(req.EntityType) + "/" + req.ID
	if err := m.Put(ctx, key, req.Data); err != nil {
		return nil, err
	}
	return &service.PutEntityResponse{Version: 1, Revision: 1}, nil
}

func (m *mockKVService) GetEntity(ctx context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	key := string(req.EntityType) + "/" + req.ID
	data, err := m.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return &service.GetEntityResponse{Data: data, Version: 1, Revision: 1}, nil
}

func (m *mockKVService) DeleteEntity(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	key := string(req.EntityType) + "/" + req.ID
	if err := m.Delete(ctx, key); err != nil {
		return nil, err
	}
	return &service.DeleteEntityResponse{Deleted: true, Revision: 1}, nil
}

func (m *mockKVService) ListEntities(ctx context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	prefix := string(req.EntityType) + "/"
	keys, err := m.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	stripped := make([]string, 0, len(keys))
	for _, k := range keys {
		stripped = append(stripped, strings.TrimPrefix(k, prefix))
	}
	return &service.ListEntitiesResponse{Keys: stripped, TotalCount: int64(len(stripped))}, nil
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
func (m *mockKVService) PutWithMode(ctx context.Context, key string, value []byte, _ writemode.Mode) error {
	return m.Put(ctx, key, value)
}

func (m *mockKVService) DeleteWithMode(ctx context.Context, key string, _ writemode.Mode) error {
	return m.Delete(ctx, key)
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

type mockAdminService struct {
	// addIdentity records the member identity the last AddMember received.
	addIdentity     atomic.Value
	healthResult    *service.HealthStatus
	statusResult    *service.StatusInfo
	peersResult     *service.PeersInfo
	healthErr       error
	statusErr       error
	peersErr        error
	addMemberErr    error
	removeMemberErr error
	snapshotErr     error
	drainErr        error
	startShardErr   error
	stopShardErr    error
	shardStateErr   error
	topologyErr     error
}

func newMockAdminService() *mockAdminService {
	return &mockAdminService{
		healthResult: &service.HealthStatus{Status: "ok", Leader: true},
		statusResult: &service.StatusInfo{
			NodeID:      "node-1",
			RaftAddress: "127.0.0.1:63001",
			IsLeader:    true,
			LeaderID:    1,
			ShardCount:  1,
			IsDraining:  false,
		},
		peersResult: &service.PeersInfo{
			Shards: []service.ShardPeers{
				{
					ShardID:  1,
					LeaderID: 1,
					Peers: []service.PeerInfo{
						{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
						{ReplicaID: 2, Address: "127.0.0.2:63001", IsLeader: false},
					},
				},
			},
		},
	}
}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	if m.healthErr != nil {
		return nil, m.healthErr
	}
	return m.healthResult, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResult, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	if m.peersErr != nil {
		return nil, m.peersErr
	}
	return m.peersResult, nil
}

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _, identity string) error {
	m.addIdentity.Store(identity)
	return m.addMemberErr
}

func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	return m.removeMemberErr
}

func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	return m.snapshotErr
}

func (m *mockAdminService) Drain(_ context.Context) error {
	return m.drainErr
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
	return m.startShardErr
}

func (m *mockAdminService) StopShard(_ context.Context, _ uint64) error {
	return m.stopShardErr
}

func (m *mockAdminService) GetShardState(_ context.Context, shardID uint64) (*service.ShardStateResponse, error) {
	if m.shardStateErr != nil {
		return nil, m.shardStateErr
	}
	return &service.ShardStateResponse{
		ShardID:           shardID,
		ConfigChangeIndex: 1,
		Term:              2,
		LeaderID:          1,
		HasLeader:         true,
	}, nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	if m.topologyErr != nil {
		return nil, m.topologyErr
	}
	return &service.TopologyResponse{
		NodeID:      "test-node",
		RaftAddress: "127.0.0.1:63001",
		ShardCount:  1,
		Shards: []service.TopologyShardInfo{
			{
				ShardID:   1,
				ReplicaID: 1,
				IsLeader:  true,
				LeaderID:  1,
				Nodes:     map[uint64]string{1: "127.0.0.1:63001"},
			},
		},
	}, nil
}

// Compile-time interface assertions for mock services.
var (
	_ service.KVServicer    = (*mockKVService)(nil)
	_ service.AdminServicer = (*mockAdminService)(nil)
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// tempSocketPath returns a unique socket path under a short temp directory.
// It deliberately avoids t.TempDir(), which embeds the (often long) test
// name in the path: a Unix domain socket path must fit within the ~108-byte
// sun_path limit, and a long TMPDIR plus a long test name overflows it
// ("bind: invalid argument"). A short MkdirTemp prefix keeps the socket path
// well under the limit regardless of the test name.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "q")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// startUnixTestServer starts a real Unix gRPC server on a temp socket path
// with the given mock services and returns the socket path and a cleanup
// function.
func startUnixTestServer(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer) (string, func()) {
	t.Helper()

	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(kvSvc, adminSvc, socketPath, unixserver.WithTLS(testUnixServerTLS))
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start Unix server: %v", err)
	}

	return socketPath, func() { srv.Stop() }
}

// connectTransport creates a new Unix SDK Transport, connects it, and
// returns it along with a cleanup function.
func connectTransport(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer) (*Transport, func()) {
	t.Helper()

	socketPath, stopSrv := startUnixTestServer(t, kvSvc, adminSvc)

	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		stopSrv()
		t.Fatalf("New: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		stopSrv()
		t.Fatalf("Connect: %v", connErr)
	}

	cleanup := func() {
		_ = tr.Close()
		stopSrv()
	}

	return tr, cleanup
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNew_MissingAddress(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Fatal("expected error for missing address")
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *transport.ConfigError, got %T: %v", err, err)
	}
}

func TestNew_ValidAddress(t *testing.T) {
	tr, err := New(transport.WithAddress("/tmp/test.sock"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------------------

func TestConnect_HealthyServer(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if !tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy to return true after Connect")
	}
}

func TestConnect_ClosedTransport(t *testing.T) {
	socketPath, stopSrv := startUnixTestServer(t, newMockKVService(), newMockAdminService())
	defer stopSrv()

	tr, err := New(transport.WithAddress(socketPath))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Close before connect.
	_ = tr.Close()

	err = tr.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error on Connect after Close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestConnect_NonExistentSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	tr, err := New(transport.WithAddress(socketPath))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = tr.Connect(context.Background())
	if err == nil {
		_ = tr.Close()
		t.Fatal("expected error connecting to nonexistent socket")
	}
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *transport.ConnectionError, got %T: %v", err, err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if err := tr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should succeed without error.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestHealthy_AfterClose(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	if tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy=false after Close")
	}
}

func TestConnState_BeforeConnect(t *testing.T) {
	tr, err := New(transport.WithAddress("/tmp/test.sock"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if state := tr.ConnState(); state != connectivity.Shutdown {
		t.Fatalf("expected Shutdown before connect, got %s", state)
	}
}

// ---------------------------------------------------------------------------
// KV operations tests
// ---------------------------------------------------------------------------

func TestPutGetDelete(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()

	// Put
	if err := tr.Put(ctx, "hello", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get
	val, err := tr.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "world" {
		t.Fatalf("expected value %q, got %q", "world", string(val))
	}

	// Delete
	if err := tr.Delete(ctx, "hello"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Get after delete returns KeyNotFoundError.
	_, err = tr.Get(ctx, "hello")
	if err == nil {
		t.Fatal("expected error on Get after Delete")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Fatalf("expected *transport.KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestGet_KeyNotFound(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Fatalf("expected *transport.KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestList(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()

	// Insert keys with different prefixes.
	for _, key := range []string{"app:1", "app:2", "other:1"} {
		if err := tr.Put(ctx, key, []byte("v")); err != nil {
			t.Fatalf("Put %q: %v", key, err)
		}
	}

	// List all keys.
	keys, err := tr.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}

	// List with prefix.
	keys, err = tr.List(ctx, "app:")
	if err != nil {
		t.Fatalf("List with prefix: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys with prefix 'app:', got %d", len(keys))
	}
}

func TestList_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.listErr = &service.ReadError{Err: errors.New("read failed")}
	tr, cleanup := connectTransport(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := tr.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestPut_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestGet_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestDelete_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestList_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Admin operations tests
// ---------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", resp.Status)
	}
	if !resp.IsLeader {
		t.Fatal("expected IsLeader=true")
	}
}

func TestHealth_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.Health(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestStatus(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.NodeID != "node-1" {
		t.Fatalf("expected NodeID %q, got %q", "node-1", resp.NodeID)
	}
	if resp.RaftAddress != "127.0.0.1:63001" {
		t.Fatalf("expected RaftAddress %q, got %q", "127.0.0.1:63001", resp.RaftAddress)
	}
	if !resp.IsLeader {
		t.Fatal("expected IsLeader=true")
	}
	if resp.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", resp.LeaderID)
	}
	if resp.ShardCount != 1 {
		t.Fatalf("expected ShardCount=1, got %d", resp.ShardCount)
	}
	if resp.IsDraining {
		t.Fatal("expected IsDraining=false")
	}
}

func TestStatus_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.Status(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestPeers(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	shard := resp.Shards[0]
	if shard.ShardID != 1 {
		t.Fatalf("expected ShardID=1, got %d", shard.ShardID)
	}
	if shard.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", shard.LeaderID)
	}
	if len(shard.Peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(shard.Peers))
	}
}

func TestPeers_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestAddMember(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
}

func TestAddMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.addMemberErr = &service.MembershipError{Op: "add", Err: errors.New("failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestAddMember_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestRemoveMember(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.RemoveMember(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

func TestRemoveMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.removeMemberErr = &service.MembershipError{Op: "remove", Err: errors.New("failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.RemoveMember(context.Background(), 1, 2)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestRemoveMember_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.RemoveMember(context.Background(), 1, 2)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestRequestSnapshot(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.RequestSnapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("RequestSnapshot: %v", err)
	}
}

func TestRequestSnapshot_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.snapshotErr = &service.SnapshotRequestError{Err: errors.New("snapshot failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.RequestSnapshot(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestRequestSnapshot_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.RequestSnapshot(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestDrain(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func TestDrain_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.drainErr = &service.DrainError{Err: errors.New("drain failed")}
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestDrain_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Request method test
// ---------------------------------------------------------------------------

func TestRequest_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.Request(context.Background(), "/test.Method", nil, nil)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestDialError_Format(t *testing.T) {
	inner := errors.New("connection refused")
	err := &dialError{Err: inner}
	if !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("expected 'dial failed' in message, got %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestSocketNotFoundError_Format(t *testing.T) {
	err := &socketNotFoundError{Path: "/tmp/missing.sock"}
	if !strings.Contains(err.Error(), "/tmp/missing.sock") {
		t.Fatalf("expected path in message, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Shard management tests
// ---------------------------------------------------------------------------

func TestStartShard(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
	})
	if err != nil {
		t.Fatalf("StartShard: %v", err)
	}
}

func TestStartShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.startShardErr = errors.New("shard start failed")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestStartShard_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestStopShard(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.StopShard(context.Background(), 1)
	if err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestStopShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.stopShardErr = errors.New("shard stop failed")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.StopShard(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestStopShard_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.StopShard(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestGetShardState(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.GetShardState(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardState: %v", err)
	}
	if resp.ShardID != 1 {
		t.Fatalf("expected ShardID=1, got %d", resp.ShardID)
	}
	if resp.ConfigChangeIndex != 1 {
		t.Fatalf("expected ConfigChangeIndex=1, got %d", resp.ConfigChangeIndex)
	}
	if resp.Term != 2 {
		t.Fatalf("expected Term=2, got %d", resp.Term)
	}
	if resp.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", resp.LeaderID)
	}
	if !resp.HasLeader {
		t.Fatal("expected HasLeader=true")
	}
}

func TestGetShardState_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.shardStateErr = errors.New("shard state unavailable")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.GetShardState(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestGetShardState_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.GetShardState(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestGetTopology(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if resp.NodeID != "test-node" {
		t.Fatalf("expected NodeID %q, got %q", "test-node", resp.NodeID)
	}
	if resp.RaftAddress != "127.0.0.1:63001" {
		t.Fatalf("expected RaftAddress %q, got %q", "127.0.0.1:63001", resp.RaftAddress)
	}
	if resp.ShardCount != 1 {
		t.Fatalf("expected ShardCount=1, got %d", resp.ShardCount)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	if resp.Shards[0].ShardID != 1 {
		t.Fatalf("expected shard ID=1, got %d", resp.Shards[0].ShardID)
	}
	if !resp.Shards[0].IsLeader {
		t.Fatal("expected shard IsLeader=true")
	}
}

func TestGetTopology_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.topologyErr = errors.New("topology unavailable")
	tr, cleanup := connectTransport(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.GetTopology(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestGetTopology_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	_, err := tr.GetTopology(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

// CompareAndSwap mirrors CompareAndDelete's guard on the edit side: it
// replaces the value only when the stored one still equals expected, and
// leaves an absent key absent because a swap is not a create.
//
// It used to be a stub returning (false, nil) unconditionally, which satisfied
// the interface and proved nothing: a transport that dropped the request
// entirely, or sent `next` where `expected` belongs, produced exactly the same
// answer. A guarded verb's test needs a service that actually ENFORCES the
// guard or the round trip is not being measured (go-qrdb#125).
func (m *mockKVService) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	m.store[key] = next
	return true, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVService) ListAll(a0 context.Context, prefix string) ([]string, error) {
	return m.List(a0, prefix)
}

// TestAddMember_CarriesIdentity pins that the operator-supplied member
// identity reaches the server's admin service over this transport.
func TestAddMember_CarriesIdentity(t *testing.T) {
	admin := newMockAdminService()
	tr, cleanup := connectTransport(t, newMockKVService(), admin)
	defer cleanup()

	identity := "spki:" + strings.Repeat("ab", 32)
	if err := tr.AddMember(context.Background(), 1, 4, "127.0.0.4:63001", identity); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if got, _ := admin.addIdentity.Load().(string); got != identity {
		t.Fatalf("server received identity %q, want %q", got, identity)
	}
}
