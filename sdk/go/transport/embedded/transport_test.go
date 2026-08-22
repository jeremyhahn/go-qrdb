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

package embedded

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock services
// ---------------------------------------------------------------------------

// mockKVService is an in-memory KV service for testing.
type mockKVService struct {
	mu    sync.RWMutex
	store map[string][]byte
}

func newMockKVService() *mockKVService {
	return &mockKVService{store: make(map[string][]byte)}
}

func (m *mockKVService) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = append([]byte(nil), value...)
	return nil
}

func (m *mockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = append([]byte(nil), value...)
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

func (m *mockKVService) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKVService) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	return nil
}

func (m *mockKVService) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *mockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
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

// mockAdminService is a mock admin service for testing.
type mockAdminService struct {
	drainCalled    bool
	snapshotCalled bool
	addCalled      bool
	removeCalled   bool
}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{
		NodeID:      "mock-node",
		RaftAddress: "127.0.0.1:63001",
		IsLeader:    true,
		LeaderID:    1,
		ShardCount:  1,
	}, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
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

func (m *mockAdminService) AddMember(_ context.Context, _, _ uint64, _ string) error {
	m.addCalled = true
	return nil
}

func (m *mockAdminService) RemoveMember(_ context.Context, _, _ uint64) error {
	m.removeCalled = true
	return nil
}

func (m *mockAdminService) RequestSnapshot(_ context.Context, _ uint64) error {
	m.snapshotCalled = true
	return nil
}

func (m *mockAdminService) Drain(_ context.Context) error {
	m.drainCalled = true
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
		ConfigChangeIndex: 1,
		Term:              2,
		LeaderID:          1,
		HasLeader:         true,
	}, nil
}

func (m *mockAdminService) GetTopology(_ context.Context) (*service.TopologyResponse, error) {
	return &service.TopologyResponse{
		NodeID:      "test-node",
		RaftAddress: "127.0.0.1:63001",
		ShardCount:  1,
	}, nil
}

// ---------------------------------------------------------------------------
// Construction tests
// ---------------------------------------------------------------------------

func TestNew_Success(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}

	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestNew_NilKVService(t *testing.T) {
	_, err := New(nil, &mockAdminService{})
	if err == nil {
		t.Fatal("expected error for nil KVServicer")
	}
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T", err)
	}
	if nilErr.Service != "KVServicer" {
		t.Errorf("expected service name 'KVServicer', got %q", nilErr.Service)
	}
}

func TestNew_NilAdminService(t *testing.T) {
	_, err := New(newMockKVService(), nil)
	if err == nil {
		t.Fatal("expected error for nil AdminServicer")
	}
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilServiceError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle tests
// ---------------------------------------------------------------------------

func TestConnect_Noop(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect should be a no-op, got: %v", err)
	}
}

func TestConnect_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error connecting after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestHealthy_Open(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	if !tr.Healthy(context.Background()) {
		t.Error("expected healthy when open")
	}
}

func TestHealthy_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	if tr.Healthy(context.Background()) {
		t.Error("expected unhealthy after close")
	}
}

// ---------------------------------------------------------------------------
// KV operation tests
// ---------------------------------------------------------------------------

func TestPut_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	err := tr.Put(context.Background(), "key", []byte("value"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
}

func TestPut_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.Put(context.Background(), "key", []byte("value"))
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestGet_Success(t *testing.T) {
	kvSvc := newMockKVService()
	tr, _ := New(kvSvc, &mockAdminService{})

	ctx := context.Background()
	_ = tr.Put(ctx, "hello", []byte("world"))

	val, err := tr.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("expected 'world', got %q", string(val))
	}
}

func TestGet_KeyNotFound(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	_, err := tr.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected KeyNotFoundError")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected transport.KeyNotFoundError, got %T", err)
	}
	if knf.Key != "nonexistent" {
		t.Errorf("expected key 'nonexistent', got %q", knf.Key)
	}
}

func TestGet_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	_, err := tr.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestDelete_Success(t *testing.T) {
	kvSvc := newMockKVService()
	tr, _ := New(kvSvc, &mockAdminService{})

	ctx := context.Background()
	_ = tr.Put(ctx, "to-delete", []byte("data"))

	err := tr.Delete(ctx, "to-delete")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = tr.Get(ctx, "to-delete")
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError after delete, got %T", err)
	}
}

func TestDelete_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestList_Empty(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	keys, err := tr.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty list, got %v", keys)
	}
}

func TestList_WithPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	tr, _ := New(kvSvc, &mockAdminService{})

	ctx := context.Background()
	_ = tr.Put(ctx, "app/a", []byte("1"))
	_ = tr.Put(ctx, "app/b", []byte("2"))
	_ = tr.Put(ctx, "other/c", []byte("3"))

	keys, err := tr.List(ctx, "app/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d: %v", len(keys), keys)
	}
}

func TestList_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	_, err := tr.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error after close")
	}
}

// ---------------------------------------------------------------------------
// Admin operation tests
// ---------------------------------------------------------------------------

func TestHealth_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	resp, err := tr.Health(context.Background())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
	if !resp.IsLeader {
		t.Error("expected IsLeader true")
	}
}

func TestHealth_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	_, err := tr.Health(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestStatus_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	resp, err := tr.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if resp.NodeID != "mock-node" {
		t.Errorf("expected node_id 'mock-node', got %q", resp.NodeID)
	}
	if resp.ShardCount != 1 {
		t.Errorf("expected shard_count 1, got %d", resp.ShardCount)
	}
}

func TestStatus_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	_, err := tr.Status(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestPeers_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	resp, err := tr.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers failed: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	if len(resp.Shards[0].Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(resp.Shards[0].Peers))
	}
	if !resp.Shards[0].Peers[0].IsLeader {
		t.Error("expected peer to be leader")
	}
}

func TestPeers_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	_, err := tr.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestAddMember_Success(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.AddMember(context.Background(), 0, 2, "10.0.0.2:63001")
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
	if !adminSvc.addCalled {
		t.Error("expected AddMember to be called on service")
	}
}

func TestAddMember_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.AddMember(context.Background(), 0, 2, "addr")
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestRemoveMember_Success(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.RemoveMember(context.Background(), 0, 2)
	if err != nil {
		t.Fatalf("RemoveMember failed: %v", err)
	}
	if !adminSvc.removeCalled {
		t.Error("expected RemoveMember to be called on service")
	}
}

func TestRemoveMember_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.RemoveMember(context.Background(), 0, 2)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestRequestSnapshot_Success(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.RequestSnapshot(context.Background(), 0)
	if err != nil {
		t.Fatalf("RequestSnapshot failed: %v", err)
	}
	if !adminSvc.snapshotCalled {
		t.Error("expected RequestSnapshot to be called on service")
	}
}

func TestRequestSnapshot_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.RequestSnapshot(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestDrain_Success(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
	if !adminSvc.drainCalled {
		t.Error("expected Drain to be called on service")
	}
}

func TestDrain_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
}

// ---------------------------------------------------------------------------
// Generic Request dispatch tests
// ---------------------------------------------------------------------------

func TestRequest_UnknownMethod(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "UnknownOp", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
	var ume *UnknownMethodError
	if !errors.As(err, &ume) {
		t.Errorf("expected UnknownMethodError, got %T", err)
	}
}

func TestRequest_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	err := tr.Request(context.Background(), "Health", nil, nil)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestRequest_Drain(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.Request(context.Background(), "Drain", nil, nil)
	if err != nil {
		t.Fatalf("Request Drain failed: %v", err)
	}
	if !adminSvc.drainCalled {
		t.Error("expected Drain to be called")
	}
}

func TestRequest_InvalidRequestType(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "Put", "not-a-put-request", nil)
	if err == nil {
		t.Fatal("expected InvalidRequestError")
	}
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T", err)
	}
}

func TestRequest_Put(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "Put", &putRequest{Key: "rk", Value: []byte("rv")}, nil)
	if err != nil {
		t.Fatalf("Request Put failed: %v", err)
	}

	val, err := tr.Get(context.Background(), "rk")
	if err != nil {
		t.Fatalf("Get after Request Put failed: %v", err)
	}
	if string(val) != "rv" {
		t.Errorf("expected 'rv', got %q", string(val))
	}
}

func TestRequest_Get(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	ctx := context.Background()

	_ = tr.Put(ctx, "rg-key", []byte("rg-val"))

	var result []byte
	err := tr.Request(ctx, "Get", &getRequest{Key: "rg-key"}, &result)
	if err != nil {
		t.Fatalf("Request Get failed: %v", err)
	}
	if string(result) != "rg-val" {
		t.Errorf("expected 'rg-val', got %q", string(result))
	}
}

func TestRequest_Get_InvalidRequest(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "Get", "bad-type", nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T", err)
	}
}

func TestRequest_Delete(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	ctx := context.Background()

	_ = tr.Put(ctx, "rd-key", []byte("rd-val"))

	err := tr.Request(ctx, "Delete", &deleteRequest{Key: "rd-key"}, nil)
	if err != nil {
		t.Fatalf("Request Delete failed: %v", err)
	}

	_, err = tr.Get(ctx, "rd-key")
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError after Request Delete, got %T", err)
	}
}

func TestRequest_Delete_InvalidRequest(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "Delete", 42, nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
	var ire *InvalidRequestError
	if !errors.As(err, &ire) {
		t.Errorf("expected InvalidRequestError, got %T", err)
	}
}

func TestRequest_List(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	ctx := context.Background()

	_ = tr.Put(ctx, "rl/a", []byte("1"))
	_ = tr.Put(ctx, "rl/b", []byte("2"))

	var keys []string
	err := tr.Request(ctx, "List", &listRequest{Prefix: "rl/"}, &keys)
	if err != nil {
		t.Fatalf("Request List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestRequest_List_InvalidRequest(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "List", true, nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
}

func TestRequest_Health(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	var resp *transport.HealthResponse
	err := tr.Request(context.Background(), "Health", nil, &resp)
	if err != nil {
		t.Fatalf("Request Health failed: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Status)
	}
}

func TestRequest_Status(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	var resp *transport.StatusResponse
	err := tr.Request(context.Background(), "Status", nil, &resp)
	if err != nil {
		t.Fatalf("Request Status failed: %v", err)
	}
	if resp.NodeID != "mock-node" {
		t.Errorf("expected 'mock-node', got %q", resp.NodeID)
	}
}

func TestRequest_Peers(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	var resp *transport.PeersResponse
	err := tr.Request(context.Background(), "Peers", nil, &resp)
	if err != nil {
		t.Fatalf("Request Peers failed: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Errorf("expected 1 shard, got %d", len(resp.Shards))
	}
}

func TestRequest_AddMember(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.Request(context.Background(), "AddMember", &transport.AddMemberRequest{
		ShardID:   0,
		ReplicaID: 3,
		Address:   "10.0.0.3:63001",
	}, nil)
	if err != nil {
		t.Fatalf("Request AddMember failed: %v", err)
	}
	if !adminSvc.addCalled {
		t.Error("expected AddMember to be called")
	}
}

func TestRequest_AddMember_InvalidRequest(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "AddMember", "bad", nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
}

func TestRequest_RemoveMember(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.Request(context.Background(), "RemoveMember", &transport.RemoveMemberRequest{
		ShardID:   0,
		ReplicaID: 2,
	}, nil)
	if err != nil {
		t.Fatalf("Request RemoveMember failed: %v", err)
	}
	if !adminSvc.removeCalled {
		t.Error("expected RemoveMember to be called")
	}
}

func TestRequest_RemoveMember_InvalidRequest(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "RemoveMember", "bad", nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
}

func TestRequest_RequestSnapshot(t *testing.T) {
	adminSvc := &mockAdminService{}
	tr, _ := New(newMockKVService(), adminSvc)

	err := tr.Request(context.Background(), "RequestSnapshot", &transport.SnapshotRequest{
		ShardID: 0,
	}, nil)
	if err != nil {
		t.Fatalf("Request RequestSnapshot failed: %v", err)
	}
	if !adminSvc.snapshotCalled {
		t.Error("expected RequestSnapshot to be called")
	}
}

func TestRequest_RequestSnapshot_InvalidRequest(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	err := tr.Request(context.Background(), "RequestSnapshot", "bad", nil)
	if err == nil {
		t.Fatal("expected error for invalid request type")
	}
}

// ---------------------------------------------------------------------------
// Error translation tests
// ---------------------------------------------------------------------------

func TestTranslateError_Nil(t *testing.T) {
	if err := translateError(nil); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestTranslateError_KeyNotFound(t *testing.T) {
	svcErr := &service.KeyNotFoundError{Key: "mykey"}
	err := translateError(svcErr)

	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected transport.KeyNotFoundError, got %T", err)
	}
	if knf.Key != "mykey" {
		t.Errorf("expected key 'mykey', got %q", knf.Key)
	}
}

func TestTranslateError_NotLeader(t *testing.T) {
	svcErr := &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.1:63001"}
	err := translateError(svcErr)

	var nle *transport.NotLeaderError
	if !errors.As(err, &nle) {
		t.Errorf("expected transport.NotLeaderError, got %T", err)
	}
	if nle.ShardID != 1 {
		t.Errorf("expected shard 1, got %d", nle.ShardID)
	}
	if nle.LeaderAddress != "10.0.0.1:63001" {
		t.Errorf("expected leader address '10.0.0.1:63001', got %q", nle.LeaderAddress)
	}
}

func TestTranslateError_Generic(t *testing.T) {
	err := translateError(errors.New("something went wrong"))
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Error type message tests
// ---------------------------------------------------------------------------

func TestNilServiceError_Message(t *testing.T) {
	err := &NilServiceError{Service: "KVServicer"}
	expected := "embedded: KVServicer must not be nil"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestUnknownMethodError_Message(t *testing.T) {
	err := &UnknownMethodError{Method: "Foo"}
	expected := "embedded: unknown method: Foo"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestInvalidRequestError_Message(t *testing.T) {
	err := &InvalidRequestError{Method: "Put"}
	expected := "embedded: invalid request type for method: Put"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

// ---------------------------------------------------------------------------
// KV roundtrip integration test
// ---------------------------------------------------------------------------

func TestKV_PutGetDeleteRoundtrip(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})

	ctx := context.Background()

	// Put multiple keys.
	keys := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}
	for k, v := range keys {
		if err := tr.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put(%s) failed: %v", k, err)
		}
	}

	// Get and verify all keys.
	for k, expected := range keys {
		val, err := tr.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get(%s) failed: %v", k, err)
		}
		if string(val) != expected {
			t.Errorf("Get(%s) = %q, expected %q", k, string(val), expected)
		}
	}

	// List all keys.
	allKeys, err := tr.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(allKeys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(allKeys))
	}

	// Delete one key.
	if err := tr.Delete(ctx, "key2"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted.
	_, err = tr.Get(ctx, "key2")
	if err == nil {
		t.Fatal("expected KeyNotFoundError after delete")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T", err)
	}

	// List remaining keys.
	remaining, err := tr.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("expected 2 keys after delete, got %d", len(remaining))
	}
}

func TestExists_KeyPresent(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	ctx := context.Background()

	if err := tr.Put(ctx, "exists-key", []byte("value")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	exists, err := tr.Exists(ctx, "exists-key")
	if err != nil {
		t.Fatalf("Exists returned unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected Exists to return true for present key")
	}
}

func TestExists_KeyMissing(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	ctx := context.Background()

	exists, err := tr.Exists(ctx, "no-such-key")
	if err != nil {
		t.Fatalf("Exists returned unexpected error for missing key: %v", err)
	}
	if exists {
		t.Error("expected Exists to return false for missing key")
	}
}

func TestExists_ClosedTransport(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()

	_, err := tr.Exists(context.Background(), "any-key")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closed *transport.ClosedError
	if !errors.As(err, &closed) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestExists_AfterDelete(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	ctx := context.Background()

	if err := tr.Put(ctx, "del-key", []byte("value")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	exists, err := tr.Exists(ctx, "del-key")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Fatal("expected key to exist before delete")
	}

	if err := tr.Delete(ctx, "del-key"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	exists, err = tr.Exists(ctx, "del-key")
	if err != nil {
		t.Fatalf("Exists returned unexpected error after delete: %v", err)
	}
	if exists {
		t.Error("expected Exists to return false after delete")
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
