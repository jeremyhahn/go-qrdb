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

package sdk_test

import (
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
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock services (duplicated here to avoid circular test imports)
// ---------------------------------------------------------------------------

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

type mockAdminService struct{}

func (m *mockAdminService) Health(_ context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok", Leader: true}, nil
}

func (m *mockAdminService) Status(_ context.Context) (*service.StatusInfo, error) {
	return &service.StatusInfo{
		NodeID:     "test-node",
		ShardCount: 1,
	}, nil
}

func (m *mockAdminService) Peers(_ context.Context) (*service.PeersInfo, error) {
	return &service.PeersInfo{
		Shards: []service.ShardPeers{
			{ShardID: 0, LeaderID: 1, Peers: []service.PeerInfo{
				{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
			}},
		},
	}, nil
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
// NewClient tests
// ---------------------------------------------------------------------------

func TestNewClient_Success(t *testing.T) {
	tr, err := embedded.New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatal(err)
	}

	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClient_NilTransport(t *testing.T) {
	_, err := sdk.NewClient(nil)
	if err == nil {
		t.Fatal("expected error for nil transport")
	}
	var nilErr *sdk.NilTransportError
	if !errors.As(err, &nilErr) {
		t.Errorf("expected NilTransportError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Client lifecycle tests
// ---------------------------------------------------------------------------

func TestClient_Connect(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
}

func TestClient_Connect_AfterClose(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	_ = client.Close()

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error connecting after close")
	}
	var closedErr *sdk.ClientClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClientClosedError, got %T", err)
	}
}

func TestClient_Healthy(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)

	if !client.Healthy(context.Background()) {
		t.Error("expected healthy")
	}
}

func TestClient_Healthy_AfterClose(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	_ = client.Close()

	if client.Healthy(context.Background()) {
		t.Error("expected unhealthy after close")
	}
}

func TestClient_Close_Idempotent(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)

	if err := client.Close(); err != nil {
		t.Errorf("first close failed: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second close failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Client KV operations via embedded transport
// ---------------------------------------------------------------------------

func TestClient_KV_PutGetDeleteRoundtrip(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	ctx := context.Background()
	kv := client.KV()

	// Put.
	if err := kv.Put(ctx, "greeting", []byte("hello")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get.
	val, err := kv.Get(ctx, "greeting")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "hello" {
		t.Errorf("expected 'hello', got %q", string(val))
	}

	// Delete.
	if err := kv.Delete(ctx, "greeting"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted.
	_, err = kv.Get(ctx, "greeting")
	if err == nil {
		t.Fatal("expected error after delete")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T", err)
	}
}

func TestClient_KV_List(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	ctx := context.Background()
	kv := client.KV()

	_ = kv.Put(ctx, "ns/a", []byte("1"))
	_ = kv.Put(ctx, "ns/b", []byte("2"))
	_ = kv.Put(ctx, "other", []byte("3"))

	keys, err := kv.List(ctx, "ns/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

// ---------------------------------------------------------------------------
// Client Admin operations via embedded transport
// ---------------------------------------------------------------------------

func TestClient_Admin_Health(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	resp, err := client.Admin().Health(context.Background())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Status)
	}
}

func TestClient_Admin_Status(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	resp, err := client.Admin().Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if resp.NodeID != "test-node" {
		t.Errorf("expected 'test-node', got %q", resp.NodeID)
	}
}

func TestClient_Admin_Peers(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	resp, err := client.Admin().Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers failed: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
}

func TestClient_Admin_AddMember(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	err := client.Admin().AddMember(context.Background(), 0, 2, "10.0.0.2:63001")
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
}

func TestClient_Admin_RemoveMember(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	err := client.Admin().RemoveMember(context.Background(), 0, 2)
	if err != nil {
		t.Fatalf("RemoveMember failed: %v", err)
	}
}

func TestClient_Admin_RequestSnapshot(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	err := client.Admin().RequestSnapshot(context.Background(), 0)
	if err != nil {
		t.Fatalf("RequestSnapshot failed: %v", err)
	}
}

func TestClient_Admin_Drain(t *testing.T) {
	tr, _ := embedded.New(newMockKVService(), &mockAdminService{})
	client, _ := sdk.NewClient(tr)
	defer client.Close()

	err := client.Admin().Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestClientError_Unwrap(t *testing.T) {
	inner := errors.New("oops")
	err := &sdk.ClientError{Op: "connect", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("expected Unwrap to return inner error")
	}
	expected := "sdk: connect: oops"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestClientClosedError_Message(t *testing.T) {
	err := &sdk.ClientClosedError{}
	expected := "sdk: client is closed"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestNilTransportError_Message(t *testing.T) {
	err := &sdk.NilTransportError{}
	expected := "sdk: transport must not be nil"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}
