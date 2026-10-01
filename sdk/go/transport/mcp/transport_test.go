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
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
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
	mu    sync.RWMutex
	store map[string][]byte

	putErr  error
	getErr  error
	delErr  error
	listErr error
	scanErr error
}

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if m.putErr != nil {
		return false, m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
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

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = value
	return nil
}

func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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

// buildMCPServer creates a real MCP server (in-process) with all tools
// registered, suitable for use with NewInProcess.
func buildMCPServer(kvSvc service.KVServicer, adminSvc service.AdminServicer) *mcpserver.MCPServer {
	srv := mcpapi.NewServer(kvSvc, adminSvc, ":0")
	return srv.MCPServer()
}

// connectInProcess creates a new in-process MCP SDK Transport, connects
// it, and returns it along with a cleanup function.
func connectInProcess(t *testing.T, kvSvc service.KVServicer, adminSvc service.AdminServicer) (*Transport, func()) {
	t.Helper()

	mcpSrv := buildMCPServer(kvSvc, adminSvc)

	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}

	cleanup := func() {
		_ = tr.Close()
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
	tr, err := New(transport.WithAddress("http://localhost:63004/mcp"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestNewInProcess_NilServer(t *testing.T) {
	_, err := NewInProcess(nil)
	if err == nil {
		t.Fatal("expected error for nil MCPServer")
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *transport.ConfigError, got %T: %v", err, err)
	}
}

func TestNewInProcess_ValidServer(t *testing.T) {
	mcpSrv := buildMCPServer(newMockKVService(), newMockAdminService())
	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------------------

func TestConnect_InProcess(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if !tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy to return true after Connect")
	}
}

func TestConnect_ClosedTransport(t *testing.T) {
	mcpSrv := buildMCPServer(newMockKVService(), newMockAdminService())

	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
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

func TestClose_Idempotent(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	if tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy=false after Close")
	}
}

// ---------------------------------------------------------------------------
// KV operations tests
// ---------------------------------------------------------------------------

func TestPutGetDelete(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
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

func TestPut_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
}

func TestAddMember_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.RemoveMember(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

func TestRemoveMember_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.RequestSnapshot(context.Background(), 1)
	if err != nil {
		t.Fatalf("RequestSnapshot: %v", err)
	}
}

func TestRequestSnapshot_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func TestDrain_OnClosedTransport(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_ = tr.Close()
	err := tr.Request(context.Background(), "qrdb_health", nil, nil)
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

func TestInitializeError_Format(t *testing.T) {
	inner := errors.New("handshake failed")
	err := &initializeError{Err: inner}
	if !strings.Contains(err.Error(), "initialization failed") {
		t.Fatalf("expected 'initialization failed' in message, got %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestToolCallError_Format(t *testing.T) {
	inner := errors.New("timeout")
	err := &toolCallError{Tool: "qrdb_put", Err: inner}
	if !strings.Contains(err.Error(), "qrdb_put") {
		t.Fatalf("expected tool name in message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected 'timeout' in message, got %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestToolResultError_Format(t *testing.T) {
	err := &toolResultError{Tool: "qrdb_get", Message: "key not found"}
	if !strings.Contains(err.Error(), "qrdb_get") {
		t.Fatalf("expected tool name in message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "key not found") {
		t.Fatalf("expected message content, got %q", err.Error())
	}
}

func TestJsonDecodeError_Format(t *testing.T) {
	inner := errors.New("unexpected end of JSON")
	err := &jsonDecodeError{Err: inner}
	if !strings.Contains(err.Error(), "JSON decode failed") {
		t.Fatalf("expected 'JSON decode failed' in message, got %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestNoContentError_Format(t *testing.T) {
	err := &noContentError{Tool: "qrdb_health"}
	if !strings.Contains(err.Error(), "qrdb_health") {
		t.Fatalf("expected tool name in message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "no content") {
		t.Fatalf("expected 'no content' in message, got %q", err.Error())
	}
}

func TestUnexpectedContentTypeError_Format(t *testing.T) {
	err := &unexpectedContentTypeError{Tool: "qrdb_status"}
	if !strings.Contains(err.Error(), "qrdb_status") {
		t.Fatalf("expected tool name in message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "unexpected content type") {
		t.Fatalf("expected 'unexpected content type' in message, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// isToolNotFound helper tests
//
// These replace the isNotFoundError tests go-qrdb#438 deleted. The one that
// matters is TestIsToolNotFound_ProseIsNotAClassification: the predicate this
// transport used to run answered TRUE for the second case below, which is
// exactly the fail-open the code vocabulary exists to remove.
// ---------------------------------------------------------------------------

func TestIsToolNotFound_Nil(t *testing.T) {
	if isToolNotFound(nil) {
		t.Fatal("expected false for nil error")
	}
}

func TestIsToolNotFound_TypedNotFound(t *testing.T) {
	if !isToolNotFound(&transport.KeyNotFoundError{Key: "mykey"}) {
		t.Fatal("expected true for the typed not-found the code mapper builds")
	}
}

func TestIsToolNotFound_WrappedTypedNotFound(t *testing.T) {
	wrapped := fmt.Errorf("call failed: %w", &transport.KeyNotFoundError{Key: "mykey"})
	if !isToolNotFound(wrapped) {
		t.Fatal("expected true for a wrapped typed not-found; the predicate must " +
			"match through the error tree, not only at the top")
	}
}

func TestIsToolNotFound_ProseIsNotAClassification(t *testing.T) {
	// The exact string the deleted isNotFoundError accepted. A transport
	// failure whose text happens to carry the phrase is NOT a determination
	// that the key is absent, and reading it as one is the fail-open
	// go-qrdb#289 and go-qrdb#438 exist to delete.
	err := errors.New("key not found: mykey")
	if isToolNotFound(err) {
		t.Fatal("prose containing \"not found\" was classified as absence; the " +
			"substring match is back and the code vocabulary is being bypassed")
	}
}

func TestIsToolNotFound_NonMatchingError(t *testing.T) {
	err := errors.New("internal server error")
	if isToolNotFound(err) {
		t.Fatal("expected false for non-matching error")
	}
}

// ---------------------------------------------------------------------------
// Service error path tests (cover callTool error propagation)
// ---------------------------------------------------------------------------

func TestPut_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.putErr = errors.New("raft timeout")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	err := tr.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestGet_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.getErr = errors.New("internal error")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := tr.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from service")
	}
	// Internal error should NOT be a KeyNotFoundError.
	var knfErr *transport.KeyNotFoundError
	if errors.As(err, &knfErr) {
		t.Fatal("expected non-KeyNotFoundError for internal error")
	}
}

func TestDelete_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.delErr = errors.New("raft timeout")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	err := tr.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestList_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.listErr = errors.New("raft timeout")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := tr.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestHealth_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthErr = errors.New("health check failed")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.Health(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestStatus_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.statusErr = errors.New("status unavailable")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.Status(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestPeers_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.peersErr = errors.New("peers unavailable")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	_, err := tr.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestAddMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.addMemberErr = errors.New("not leader")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.AddMember(context.Background(), 1, 3, "127.0.0.3:63001", "")
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestRemoveMember_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.removeMemberErr = errors.New("not leader")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.RemoveMember(context.Background(), 1, 2)
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestRequestSnapshot_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.snapshotErr = errors.New("snapshot failed")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.RequestSnapshot(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestDrain_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.drainErr = errors.New("drain failed")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	err := tr.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Request method success and edge cases
// ---------------------------------------------------------------------------

func TestRequest_Success(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	// Use a real tool name so the MCP server can route it.
	err := tr.Request(context.Background(), "qrdb_health", nil, nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
}

func TestRequest_WithMapArgs(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	// Put via Request with map args. Request passes tool arguments through
	// verbatim (no automatic encoding), so the value is base64-encoded
	// (RFC 4648) here to match the server's binary-safe wire contract.
	err := tr.Request(context.Background(), "qrdb_put", map[string]any{
		"key":   "rkey",
		"value": "cnZhbA==", // base64("rval")
	}, nil)
	if err != nil {
		t.Fatalf("Request put: %v", err)
	}

	// Verify through KV interface.
	val, getErr := tr.Get(context.Background(), "rkey")
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if string(val) != "rval" {
		t.Fatalf("expected %q, got %q", "rval", string(val))
	}
}

func TestRequest_UnknownTool(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.Request(context.Background(), "nonexistent_tool", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// ---------------------------------------------------------------------------
// Healthy edge cases
// ---------------------------------------------------------------------------

func TestHealthy_NilClient(t *testing.T) {
	mcpSrv := buildMCPServer(newMockKVService(), newMockAdminService())
	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	// Don't call Connect — client remains nil.
	if tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy=false when client is nil")
	}
}

func TestHealthy_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.healthErr = errors.New("unavailable")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
	defer cleanup()

	// Health tool returns error → Healthy returns false.
	if tr.Healthy(context.Background()) {
		t.Fatal("expected Healthy=false when health tool returns error")
	}
}

// ---------------------------------------------------------------------------
// extractTextContent edge cases
// ---------------------------------------------------------------------------

func TestExtractTextContent_NilResult(t *testing.T) {
	text := extractTextContent(nil)
	if text != "" {
		t.Fatalf("expected empty string, got %q", text)
	}
}

func TestExtractTextContent_EmptyContent(t *testing.T) {
	result := &mcp.CallToolResult{}
	text := extractTextContent(result)
	if text != "" {
		t.Fatalf("expected empty string, got %q", text)
	}
}

// ---------------------------------------------------------------------------
// Client composite interface test (compile-time check validated at line 38)
// ---------------------------------------------------------------------------

func TestTransport_ImplementsClientInterface(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	// Verify the transport satisfies the composite Client interface at runtime.
	var client transport.Client = tr
	ctx := context.Background()

	// Use via Client interface — KV Put then Get.
	if err := client.Put(ctx, "iface-key", []byte("iface-val")); err != nil {
		t.Fatalf("Put via Client: %v", err)
	}
	val, err := client.Get(ctx, "iface-key")
	if err != nil {
		t.Fatalf("Get via Client: %v", err)
	}
	if string(val) != "iface-val" {
		t.Fatalf("expected %q, got %q", "iface-val", string(val))
	}

	// Admin Health via Client interface.
	resp, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health via Client: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status %q, got %q", "ok", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// Shard management tests
// ---------------------------------------------------------------------------

func TestStartShard(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.StopShard(context.Background(), 1)
	if err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestStopShard_ServiceError(t *testing.T) {
	adminSvc := newMockAdminService()
	adminSvc.stopShardErr = errors.New("shard stop failed")
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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
	tr, cleanup := connectInProcess(t, newMockKVService(), adminSvc)
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
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
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

// ---------------------------------------------------------------------------
// Network Connect path tests
// ---------------------------------------------------------------------------

func TestConnect_NetworkInvalidAddress(t *testing.T) {
	tr, err := New(transport.WithAddress("http://127.0.0.1:1/mcp"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Attempt connect with very short timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = tr.Connect(ctx)
	if err == nil {
		t.Fatal("expected connection error")
	}
	_ = tr.Close()
}

// CompareAndSwap is the edit half of the guarded set (go-qrdb#56), and it
// GUARDS rather than returning a constant.
//
// It was a `return false, nil` stub while no test drove the verb over this
// transport. A stub that always declines is safe only for as long as nothing
// asserts the true branch — the moment go-qrdb#127 gave MCP the verb, a stub
// would have made "the swap was refused" indistinguishable from "the handler
// never reached the service", which is the one distinction the guarded tests
// exist to make. An absent key is left absent here, as it is in the real
// store: a swap is not a create.
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
func (m *mockKVService) ListAll(a0 context.Context, prefix string) ([]string, error) {
	return m.List(a0, prefix)
}

// TestAddMember_CarriesIdentity pins that the operator-supplied member
// identity reaches the server's admin service over this transport.
func TestAddMember_CarriesIdentity(t *testing.T) {
	admin := newMockAdminService()
	tr, cleanup := connectInProcess(t, newMockKVService(), admin)
	defer cleanup()

	identity := "spki:" + strings.Repeat("ab", 32)
	if err := tr.AddMember(context.Background(), 1, 4, "127.0.0.4:63001", identity); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if got, _ := admin.addIdentity.Load().(string); got != identity {
		t.Fatalf("server received identity %q, want %q", got, identity)
	}
}
