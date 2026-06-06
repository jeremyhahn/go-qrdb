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
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// Mock: DatabaseServicer
// ---------------------------------------------------------------------------

// mockDatabaseService implements service.DatabaseServicer for testing.
type mockDatabaseService struct {
	createFn           func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn              func(name string) (*database.DatabaseInfo, error)
	listFn             func() ([]database.DatabaseInfo, error)
	dropFn             func(name string) error
	updateProtocolFn   func(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcdFn       func(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error)
	updateRaftFn       func(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn    func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnectionFn func(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
}

func newMockDatabaseService() *mockDatabaseService {
	return &mockDatabaseService{}
}

func (m *mockDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createFn != nil {
		return m.createFn(cfg)
	}
	return &database.DatabaseInfo{
		Name:    cfg.Name,
		Engine:  cfg.Engine,
		Backend: cfg.Backend,
		Status:  database.StatusOpen,
	}, nil
}

func (m *mockDatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	if m.getFn != nil {
		return m.getFn(name)
	}
	return &database.DatabaseInfo{
		Name:    name,
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
		Status:  database.StatusOpen,
	}, nil
}

func (m *mockDatabaseService) List() ([]database.DatabaseInfo, error) {
	if m.listFn != nil {
		return m.listFn()
	}
	return []database.DatabaseInfo{
		{
			Name:    "testdb",
			Engine:  database.EngineKV,
			Backend: database.BackendPebble,
			Status:  database.StatusOpen,
		},
	}, nil
}

func (m *mockDatabaseService) Drop(name string) error {
	if m.dropFn != nil {
		return m.dropFn(name)
	}
	return nil
}

func (m *mockDatabaseService) UpdateProtocol(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolFn != nil {
		return m.updateProtocolFn(name, proto)
	}
	return &database.DatabaseInfo{
		Name:     name,
		Engine:   database.EngineKV,
		Backend:  database.BackendPebble,
		Status:   database.StatusOpen,
		Protocol: proto,
	}, nil
}

func (m *mockDatabaseService) UpdateEtcd(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdFn != nil {
		return m.updateEtcdFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name, Status: database.StatusOpen}, nil
}

func (m *mockDatabaseService) UpdateRaft(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name, Status: database.StatusOpen}, nil
}

func (m *mockDatabaseService) UpdateBarrier(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name, Status: database.StatusOpen}, nil
}

func (m *mockDatabaseService) UpdateConnection(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnectionFn != nil {
		return m.updateConnectionFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name, Status: database.StatusOpen}, nil
}

func (m *mockDatabaseService) SealDatabase(_ context.Context, _ string) error {
	return nil
}

func (m *mockDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

func (m *mockDatabaseService) DatabaseBarrierStatus(_ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{}, nil
}

// The *ForTenant methods delegate to the plain methods, mirroring the real
// DatabaseService's scope routing for the system tenant (no tenant in the test
// context), so handlers calling the ctx-aware path exercise the same behavior.
func (m *mockDatabaseService) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.Create(cfg)
}

func (m *mockDatabaseService) GetForTenant(_ context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}

func (m *mockDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}

func (m *mockDatabaseService) DropForTenant(_ context.Context, name string) error {
	return m.Drop(name)
}

func (m *mockDatabaseService) UpdateProtocolForTenant(_ context.Context, name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, proto)
}

func (m *mockDatabaseService) UpdateEtcdForTenant(_ context.Context, name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, cfg)
}

func (m *mockDatabaseService) UpdateRaftForTenant(_ context.Context, name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, cfg)
}

func (m *mockDatabaseService) UpdateBarrierForTenant(_ context.Context, name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, cfg)
}

func (m *mockDatabaseService) UpdateConnectionForTenant(_ context.Context, name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, cfg)
}

// ---------------------------------------------------------------------------
// Tool registration
// ---------------------------------------------------------------------------

func TestDatabaseTools_Registration(t *testing.T) {
	dbSvc := newMockDatabaseService()
	tools := databaseTools(dbSvc)

	expectedNames := []string{
		"qrdb_database_create",
		"qrdb_database_list",
		"qrdb_database_get",
		"qrdb_database_drop",
		"qrdb_database_update_protocol",
		"qrdb_database_update_etcd",
		"qrdb_database_update_raft",
		"qrdb_database_update_barrier",
		"qrdb_database_update_connection",
		"qrdb_database_seal",
		"qrdb_database_unseal",
		"qrdb_database_barrier_status",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	for i, name := range expectedNames {
		if tools[i].Tool.Name != name {
			t.Errorf("tool[%d]: got %q, want %q", i, tools[i].Tool.Name, name)
		}
	}
}

func TestServer_WithDatabaseService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	dbSvc := newMockDatabaseService()

	s := NewServer(kvSvc, adminSvc, ":0", WithDatabaseService(dbSvc))

	if s.dbSvc != dbSvc {
		t.Error("database service not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	dbToolNames := []string{
		"qrdb_database_create",
		"qrdb_database_list",
		"qrdb_database_get",
		"qrdb_database_drop",
		"qrdb_database_update_protocol",
	}
	for _, name := range dbToolNames {
		if _, ok := registeredTools[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// database_create
// ---------------------------------------------------------------------------

func TestHandleDatabaseCreate_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":    "mydb",
		"engine":  "kv",
		"backend": "pebble",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Name != "mydb" {
		t.Errorf("name: got %q, want %q", info.Name, "mydb")
	}
	if info.Engine != database.EngineKV {
		t.Errorf("engine: got %q, want %q", info.Engine, database.EngineKV)
	}
	if info.Backend != database.BackendPebble {
		t.Errorf("backend: got %q, want %q", info.Backend, database.BackendPebble)
	}
	if info.Status != database.StatusOpen {
		t.Errorf("status: got %q, want %q", info.Status, database.StatusOpen)
	}
}

func TestHandleDatabaseCreate_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"engine":  "kv",
		"backend": "pebble",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseCreate_MissingEngine(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":    "mydb",
		"backend": "pebble",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing engine")
	}
}

func TestHandleDatabaseCreate_MissingBackend(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":   "mydb",
		"engine": "kv",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing backend")
	}
}

func TestHandleDatabaseCreate_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return nil, &database.AlreadyExistsError{Name: "mydb"}
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":    "mydb",
		"engine":  "kv",
		"backend": "pebble",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_create" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_database_create")
	}

	var existsErr *database.AlreadyExistsError
	if !errors.As(err, &existsErr) {
		t.Error("expected AlreadyExistsError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// database_list
// ---------------------------------------------------------------------------

func TestHandleDatabaseList_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseList(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_list", nil)

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseList: %v", err)
	}

	text := resultText(t, result)
	var infos []database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &infos); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(infos) != 1 {
		t.Fatalf("expected 1 database, got %d", len(infos))
	}
	if infos[0].Name != "testdb" {
		t.Errorf("name: got %q, want %q", infos[0].Name, "testdb")
	}
}

func TestHandleDatabaseList_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.listFn = func() ([]database.DatabaseInfo, error) {
		return nil, errors.New("internal error")
	}
	handler := handleDatabaseList(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_list", nil)

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_list" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_database_list")
	}
}

// ---------------------------------------------------------------------------
// database_get
// ---------------------------------------------------------------------------

func TestHandleDatabaseGet_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseGet(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_get", map[string]any{
		"name": "testdb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseGet: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Name != "testdb" {
		t.Errorf("name: got %q, want %q", info.Name, "testdb")
	}
	if info.Engine != database.EngineKV {
		t.Errorf("engine: got %q, want %q", info.Engine, database.EngineKV)
	}
	if info.Status != database.StatusOpen {
		t.Errorf("status: got %q, want %q", info.Status, database.StatusOpen)
	}
}

func TestHandleDatabaseGet_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseGet(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_get", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseGet_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = func(name string) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}
	handler := handleDatabaseGet(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_get", map[string]any{
		"name": "nonexistent",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_get" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_database_get")
	}

	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected NotFoundError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// database_drop
// ---------------------------------------------------------------------------

func TestHandleDatabaseDrop_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseDrop(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_drop", map[string]any{
		"name": "testdb",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseDrop: %v", err)
	}

	text := resultText(t, result)
	if text != "ok" {
		t.Errorf("expected 'ok', got %q", text)
	}
}

func TestHandleDatabaseDrop_MissingName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	handler := handleDatabaseDrop(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_drop", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleDatabaseDrop_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.dropFn = func(name string) error {
		return &database.NotFoundError{Name: name}
	}
	handler := handleDatabaseDrop(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_drop", map[string]any{
		"name": "nonexistent",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_database_drop" {
		t.Errorf("tool name: got %q, want %q", toolErr.Tool, "qrdb_database_drop")
	}

	var notFoundErr *database.NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Error("expected NotFoundError through ToolError chain")
	}
}

// ---------------------------------------------------------------------------
// database_create: raft and discovery configuration
// ---------------------------------------------------------------------------

func TestHandleDatabaseCreate_WithRaftDistributed(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    cfg.Name,
			Engine:  cfg.Engine,
			Backend: cfg.Backend,
			Status:  database.StatusOpen,
			Raft:    cfg.Raft,
		}, nil
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":                 "raft-db",
		"engine":               "kv",
		"backend":              "raft",
		"raft_cluster_mode":    "distributed",
		"raft_shard_id":        float64(1),
		"raft_replica_id":      float64(1),
		"raft_address":         "localhost:63001",
		"discovery_method":     "static",
		"static_shared_secret": "my-secret",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if info.Raft.ClusterMode != database.RaftDistributed {
		t.Errorf("cluster mode: got %q, want %q", info.Raft.ClusterMode, database.RaftDistributed)
	}
	if info.Raft.ShardID != 1 {
		t.Errorf("shard id: got %d, want 1", info.Raft.ShardID)
	}
	if info.Raft.ReplicaID != 1 {
		t.Errorf("replica id: got %d, want 1", info.Raft.ReplicaID)
	}
	if info.Raft.RaftAddress != "localhost:63001" {
		t.Errorf("raft address: got %q, want %q", info.Raft.RaftAddress, "localhost:63001")
	}
	if info.Raft.Discovery != database.DiscoveryStatic {
		t.Errorf("discovery: got %q, want %q", info.Raft.Discovery, database.DiscoveryStatic)
	}
	if info.Raft.Static == nil {
		t.Fatal("static config should not be nil")
	}
	if info.Raft.Static.SharedSecret != "my-secret" {
		t.Errorf("shared secret: got %q, want %q", info.Raft.Static.SharedSecret, "my-secret")
	}
}

func TestHandleDatabaseCreate_WithMulticastDiscovery(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    cfg.Name,
			Engine:  cfg.Engine,
			Backend: cfg.Backend,
			Status:  database.StatusOpen,
			Raft:    cfg.Raft,
		}, nil
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":                    "multicast-db",
		"engine":                  "kv",
		"backend":                 "raft",
		"raft_cluster_mode":       "distributed",
		"raft_shard_id":           float64(2),
		"raft_replica_id":         float64(1),
		"raft_address":            "localhost:63002",
		"discovery_method":        "multicast",
		"multicast_shared_secret": "hmac-secret",
		"multicast_group":         "239.255.77.77:63777",
		"multicast_interface":     "eth0",
		"multicast_min_peers":     float64(3),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if info.Raft.Discovery != database.DiscoveryMulticast {
		t.Errorf("discovery: got %q, want %q", info.Raft.Discovery, database.DiscoveryMulticast)
	}
	if info.Raft.Multicast == nil {
		t.Fatal("multicast config should not be nil")
	}
	if info.Raft.Multicast.SharedSecret != "hmac-secret" {
		t.Errorf("shared secret: got %q, want %q", info.Raft.Multicast.SharedSecret, "hmac-secret")
	}
	if info.Raft.Multicast.MulticastGroup != "239.255.77.77:63777" {
		t.Errorf("multicast group: got %q, want %q", info.Raft.Multicast.MulticastGroup, "239.255.77.77:63777")
	}
	if info.Raft.Multicast.Interface != "eth0" {
		t.Errorf("interface: got %q, want %q", info.Raft.Multicast.Interface, "eth0")
	}
	if info.Raft.Multicast.MinPeers != 3 {
		t.Errorf("min peers: got %d, want 3", info.Raft.Multicast.MinPeers)
	}
}

func TestHandleDatabaseCreate_WithDNSDiscovery(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    cfg.Name,
			Engine:  cfg.Engine,
			Backend: cfg.Backend,
			Status:  database.StatusOpen,
			Raft:    cfg.Raft,
		}, nil
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":              "dns-db",
		"engine":            "kv",
		"backend":           "raft",
		"raft_cluster_mode": "distributed",
		"raft_shard_id":     float64(3),
		"raft_replica_id":   float64(1),
		"raft_address":      "localhost:63003",
		"discovery_method":  "dns",
		"dns_domain":        "qrdb.example.com",
		"dns_service":       "qrdb",
		"dns_proto":         "udp",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if info.Raft.Discovery != database.DiscoveryDNS {
		t.Errorf("discovery: got %q, want %q", info.Raft.Discovery, database.DiscoveryDNS)
	}
	if info.Raft.DNS == nil {
		t.Fatal("dns config should not be nil")
	}
	if info.Raft.DNS.Domain != "qrdb.example.com" {
		t.Errorf("domain: got %q, want %q", info.Raft.DNS.Domain, "qrdb.example.com")
	}
	if info.Raft.DNS.Service != "qrdb" {
		t.Errorf("service: got %q, want %q", info.Raft.DNS.Service, "qrdb")
	}
	if info.Raft.DNS.Proto != "udp" {
		t.Errorf("proto: got %q, want %q", info.Raft.DNS.Proto, "udp")
	}
}

func TestHandleDatabaseCreate_WithTokenDiscovery(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    cfg.Name,
			Engine:  cfg.Engine,
			Backend: cfg.Backend,
			Status:  database.StatusOpen,
			Raft:    cfg.Raft,
		}, nil
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":               "token-db",
		"engine":             "kv",
		"backend":            "raft",
		"raft_cluster_mode":  "distributed",
		"raft_shard_id":      float64(4),
		"raft_replica_id":    float64(1),
		"raft_address":       "localhost:63004",
		"discovery_method":   "token",
		"token_endpoint":     "https://discovery.example.com/v1/cluster",
		"token_value":        "bootstrap-token-abc123",
		"token_cluster_size": float64(5),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if info.Raft.Discovery != database.DiscoveryToken {
		t.Errorf("discovery: got %q, want %q", info.Raft.Discovery, database.DiscoveryToken)
	}
	if info.Raft.Token == nil {
		t.Fatal("token config should not be nil")
	}
	if info.Raft.Token.Endpoint != "https://discovery.example.com/v1/cluster" {
		t.Errorf("endpoint: got %q, want %q", info.Raft.Token.Endpoint, "https://discovery.example.com/v1/cluster")
	}
	if info.Raft.Token.Token != "bootstrap-token-abc123" {
		t.Errorf("token: got %q, want %q", info.Raft.Token.Token, "bootstrap-token-abc123")
	}
	if info.Raft.Token.ClusterSize != 5 {
		t.Errorf("cluster size: got %d, want 5", info.Raft.Token.ClusterSize)
	}
}

func TestHandleDatabaseCreate_NoRaftConfig(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    cfg.Name,
			Engine:  cfg.Engine,
			Backend: cfg.Backend,
			Status:  database.StatusOpen,
			Raft:    cfg.Raft,
		}, nil
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":    "simple-db",
		"engine":  "kv",
		"backend": "pebble",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Raft != nil {
		t.Errorf("raft config should be nil for non-raft create, got %+v", info.Raft)
	}
	if info.Name != "simple-db" {
		t.Errorf("name: got %q, want %q", info.Name, "simple-db")
	}
}

func TestHandleDatabaseCreate_RaftWithoutDiscovery(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    cfg.Name,
			Engine:  cfg.Engine,
			Backend: cfg.Backend,
			Status:  database.StatusOpen,
			Raft:    cfg.Raft,
		}, nil
	}
	handler := handleDatabaseCreate(dbSvc)

	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name":              "raft-no-discovery",
		"engine":            "kv",
		"backend":           "raft",
		"raft_cluster_mode": "distributed",
		"raft_shard_id":     float64(1),
		"raft_replica_id":   float64(1),
		"raft_address":      "localhost:63001",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDatabaseCreate: %v", err)
	}

	text := resultText(t, result)
	var info database.DatabaseInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if info.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if info.Raft.ClusterMode != database.RaftDistributed {
		t.Errorf("cluster mode: got %q, want %q", info.Raft.ClusterMode, database.RaftDistributed)
	}
	// Discovery should be empty when not specified; DatabaseRaftConfig.Validate
	// defaults to static, but the MCP handler does not call Validate.
	if info.Raft.Discovery != "" {
		t.Errorf("discovery: got %q, want empty string", info.Raft.Discovery)
	}
	if info.Raft.Static != nil {
		t.Errorf("static config should be nil when discovery not specified, got %+v", info.Raft.Static)
	}
}
