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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: DatabaseServicer
// ---------------------------------------------------------------------------

// mockDatabaseService implements service.DatabaseServicer for REST handler
// testing.
type mockDatabaseService struct {
	createFn         func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn            func(name string) (*database.DatabaseInfo, error)
	listFn           func() ([]database.DatabaseInfo, error)
	dropFn           func(name string) error
	updateProtocolFn func(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error)
}

// Compile-time interface check.
var _ service.DatabaseServicer = (*mockDatabaseService)(nil)

func newMockDatabaseService() *mockDatabaseService {
	return &mockDatabaseService{}
}

func (m *mockDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createFn != nil {
		return m.createFn(cfg)
	}
	return &database.DatabaseInfo{
		Name:     cfg.Name,
		Engine:   cfg.Engine,
		Backend:  cfg.Backend,
		Status:   database.StatusOpen,
		Protocol: cfg.Protocol,
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

func (m *mockDatabaseService) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *mockDatabaseService) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *mockDatabaseService) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *mockDatabaseService) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return nil, nil
}

func (m *mockDatabaseService) SealDatabase(_ context.Context, _ string) error {
	return nil
}

func (m *mockDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

func (m *mockDatabaseService) DatabaseBarrierStatus(_ string) (*seal.BarrierStatus, error) {
	return nil, nil
}

// The *ForTenant methods delegate to the plain methods, mirroring the real
// DatabaseService's scope routing for the system tenant (the test requests
// carry no tenant), so handlers calling the ctx-aware path behave identically.
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

func (m *mockDatabaseService) UpdateProtocolForTenant(_ context.Context, name string, cfg database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, cfg)
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
// handleCreateDatabase tests
// ---------------------------------------------------------------------------

func TestHandleCreateDatabase_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"mydb","engine":"kv","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "mydb" {
		t.Fatalf("name = %q, want %q", resp.Name, "mydb")
	}
	if resp.Engine != "kv" {
		t.Fatalf("engine = %q, want %q", resp.Engine, "kv")
	}
	if resp.Backend != "pebble" {
		t.Fatalf("backend = %q, want %q", resp.Backend, "pebble")
	}
	if resp.Status != "open" {
		t.Fatalf("status = %q, want %q", resp.Status, "open")
	}
}

func TestHandleCreateDatabase_WithSQL(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"sqldb","engine":"kv","backend":"pebble","protocol":{"mysql":true,"postgresql":false}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "sqldb" {
		t.Fatalf("name = %q, want %q", resp.Name, "sqldb")
	}
	if !resp.Protocol.MySQL {
		t.Fatal("protocol.mysql = false, want true")
	}
	if resp.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = true, want false")
	}
}

func TestHandleCreateDatabase_InvalidJSON(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateDatabase_ServiceError(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return nil, &database.AlreadyExistsError{Name: cfg.Name}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"existing","engine":"kv","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleCreateDatabase_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"name":"mydb","engine":"kv","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// The route is not registered when databaseSvc is nil, so dispatch
	// returns 404. We also directly test the guard inside the handler.
	srv.handleCreateDatabase(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleGetDatabase tests
// ---------------------------------------------------------------------------

func TestHandleGetDatabase_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/testdb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "testdb" {
		t.Fatalf("name = %q, want %q", resp.Name, "testdb")
	}
}

func TestHandleGetDatabase_IncludesSQL(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = func(name string) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:    name,
			Engine:  database.EngineKV,
			Backend: database.BackendPebble,
			Status:  database.StatusOpen,
			Protocol: database.ProtocolConfig{
				MySQL:      true,
				PostgreSQL: true,
			},
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/sqldb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "sqldb" {
		t.Fatalf("name = %q, want %q", resp.Name, "sqldb")
	}
	if !resp.Protocol.MySQL {
		t.Fatal("protocol.mysql = false, want true")
	}
	if !resp.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = false, want true")
	}
}

func TestHandleGetDatabase_NotFound(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.getFn = func(name string) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetDatabase_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/testdb", nil)
	rec := httptest.NewRecorder()
	// dispatch will match the /v1/databases/ prefix and call handleDatabaseByName
	// which checks for nil databaseSvc.
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleListDatabases tests
// ---------------------------------------------------------------------------

func TestHandleListDatabases_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodGet, "/v1/databases/list", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp listDatabasesResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Databases) != 1 {
		t.Fatalf("databases length = %d, want 1", len(resp.Databases))
	}
	if resp.Databases[0].Name != "testdb" {
		t.Fatalf("databases[0].name = %q, want %q", resp.Databases[0].Name, "testdb")
	}
}

func TestHandleListDatabases_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	// Route is not registered; call the handler directly to test the guard.
	req := httptest.NewRequest(http.MethodGet, "/v1/databases/list", nil)
	rec := httptest.NewRecorder()
	srv.handleListDatabases(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// handleDropDatabase tests
// ---------------------------------------------------------------------------

func TestHandleDropDatabase_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/databases/testdb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleDropDatabase_NotFound(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.dropFn = func(name string) error {
		return &database.NotFoundError{Name: name}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodDelete, "/v1/databases/missing", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleUpdateDatabaseProtocol tests
// ---------------------------------------------------------------------------

func TestHandleUpdateDatabaseProtocol_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"mysql":true,"postgresql":true}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/testdb/protocol", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "testdb" {
		t.Fatalf("name = %q, want %q", resp.Name, "testdb")
	}
	if !resp.Protocol.MySQL {
		t.Fatal("protocol.mysql = false, want true")
	}
	if !resp.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = false, want true")
	}
}

func TestHandleUpdateDatabaseProtocol_InvalidJSON(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/testdb/protocol", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUpdateDatabaseProtocol_NotFound(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateProtocolFn = func(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
		return nil, &database.NotFoundError{Name: name}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"mysql":true,"postgresql":false}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/missing/protocol", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)

	var resp errorResponse
	decodeJSON(t, rec, &resp)
	if !strings.Contains(resp.Error, "missing") {
		t.Fatalf("error message = %q, want mention of 'missing'", resp.Error)
	}
}

func TestHandleUpdateDatabaseProtocol_DisableBoth(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"mysql":false,"postgresql":false}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/testdb/protocol", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Protocol.MySQL {
		t.Fatal("protocol.mysql = true, want false")
	}
	if resp.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = true, want false")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseByName routing tests
// ---------------------------------------------------------------------------

func TestHandleDatabaseByName_MethodNotAllowed(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/databases/testdb", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleDatabaseByName_EmptyName(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	// Path /v1/databases/ with trailing slash but no name --
	// dispatch checks len > 15 for "/v1/databases/", so "/v1/databases/"
	// has exactly 15 chars and won't match the prefix handler. The exact
	// route "/v1/databases" is POST only (create). This tests the edge
	// case at the boundary.
	req := httptest.NewRequest(http.MethodGet, "/v1/databases/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// The path "/v1/databases/" is 15 chars but the check is > 15, so it
	// won't enter handleDatabaseByName. It falls through to 404.
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 400 or 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// databaseInfoToResponse tests
// ---------------------------------------------------------------------------

func TestDatabaseInfoToResponse(t *testing.T) {
	info := &database.DatabaseInfo{
		Name:    "mydb",
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
		Status:  database.StatusOpen,
		Protocol: database.ProtocolConfig{
			MySQL:      true,
			PostgreSQL: false,
		},
		DataDir: "/data/mydb",
		ShardID: 42,
	}

	resp := databaseInfoToResponse(info)

	if resp.Name != "mydb" {
		t.Fatalf("name = %q, want %q", resp.Name, "mydb")
	}
	if resp.Engine != "kv" {
		t.Fatalf("engine = %q, want %q", resp.Engine, "kv")
	}
	if resp.Backend != "pebble" {
		t.Fatalf("backend = %q, want %q", resp.Backend, "pebble")
	}
	if resp.Status != "open" {
		t.Fatalf("status = %q, want %q", resp.Status, "open")
	}
	if !resp.Protocol.MySQL {
		t.Fatal("protocol.mysql = false, want true")
	}
	if resp.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = true, want false")
	}
	if resp.DataDir != "/data/mydb" {
		t.Fatalf("data_dir = %q, want %q", resp.DataDir, "/data/mydb")
	}
	if resp.ShardID != 42 {
		t.Fatalf("shard_id = %d, want 42", resp.ShardID)
	}
}

// ---------------------------------------------------------------------------
// Request/response type serialization tests
// ---------------------------------------------------------------------------

func TestCreateDatabaseRequest_JSON(t *testing.T) {
	input := `{"name":"testdb","engine":"kv","backend":"memory","protocol":{"mysql":true,"postgresql":false}}`
	var req createDatabaseRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Name != "testdb" {
		t.Fatalf("name = %q, want %q", req.Name, "testdb")
	}
	if req.Engine != "kv" {
		t.Fatalf("engine = %q, want %q", req.Engine, "kv")
	}
	if req.Backend != "memory" {
		t.Fatalf("backend = %q, want %q", req.Backend, "memory")
	}
	if !req.Protocol.MySQL {
		t.Fatal("protocol.mysql = false, want true")
	}
	if req.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = true, want false")
	}
}

func TestUpdateDatabaseProtocolRequest_JSON(t *testing.T) {
	input := `{"mysql":true,"postgresql":true}`
	var req updateDatabaseProtocolRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.MySQL {
		t.Fatal("mysql = false, want true")
	}
	if !req.PostgreSQL {
		t.Fatal("postgresql = false, want true")
	}
}

func TestDatabaseInfoResponse_JSON(t *testing.T) {
	resp := databaseInfoResponse{
		Name:    "mydb",
		Engine:  "kv",
		Backend: "pebble",
		Status:  "open",
		Protocol: protocolConfigResponse{
			MySQL:      true,
			PostgreSQL: false,
		},
		DataDir: "/data/mydb",
		ShardID: 1,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded databaseInfoResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != "mydb" {
		t.Fatalf("name = %q, want %q", decoded.Name, "mydb")
	}
	if !decoded.Protocol.MySQL {
		t.Fatal("protocol.mysql = false, want true")
	}
	if decoded.Protocol.PostgreSQL {
		t.Fatal("protocol.postgresql = true, want false")
	}
}

// ---------------------------------------------------------------------------
// handleCreateDatabase with Raft discovery tests
// ---------------------------------------------------------------------------

func TestHandleCreateDatabase_WithRaftStatic(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:     cfg.Name,
			Engine:   cfg.Engine,
			Backend:  cfg.Backend,
			Status:   database.StatusOpen,
			Protocol: cfg.Protocol,
			Mode:     database.ModeEmbedded,
			Raft:     cfg.Raft,
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{
		"name":"staticdb","engine":"kv","backend":"raft",
		"raft":{
			"clusterMode":"distributed",
			"shardId":1,"replicaId":1,
			"raftAddress":"10.0.0.1:63001",
			"discovery":"static",
			"static":{"peers":["10.0.0.2:63001","10.0.0.3:63001"],"sharedSecret":"s3cret"}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Name != "staticdb" {
		t.Fatalf("name = %q, want %q", resp.Name, "staticdb")
	}
	if resp.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if resp.Raft.ClusterMode != "distributed" {
		t.Fatalf("raft.clusterMode = %q, want %q", resp.Raft.ClusterMode, "distributed")
	}
	if resp.Raft.Discovery != "static" {
		t.Fatalf("raft.discovery = %q, want %q", resp.Raft.Discovery, "static")
	}
	if resp.Raft.Static == nil {
		t.Fatal("raft.static should not be nil")
	}
	if len(resp.Raft.Static.Peers) != 2 {
		t.Fatalf("raft.static.peers length = %d, want 2", len(resp.Raft.Static.Peers))
	}
	if resp.Raft.Static.SharedSecret != "s3cret" {
		t.Fatalf("raft.static.sharedSecret = %q, want %q", resp.Raft.Static.SharedSecret, "s3cret")
	}
	if resp.Raft.RaftAddress != "10.0.0.1:63001" {
		t.Fatalf("raft.raftAddress = %q, want %q", resp.Raft.RaftAddress, "10.0.0.1:63001")
	}
}

func TestHandleCreateDatabase_WithRaftMulticast(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:     cfg.Name,
			Engine:   cfg.Engine,
			Backend:  cfg.Backend,
			Status:   database.StatusOpen,
			Protocol: cfg.Protocol,
			Mode:     database.ModeEmbedded,
			Raft:     cfg.Raft,
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{
		"name":"mcastdb","engine":"kv","backend":"raft",
		"raft":{
			"clusterMode":"distributed",
			"discovery":"multicast",
			"multicast":{
				"sharedSecret":"hmac-secret",
				"multicastGroup":"239.1.1.1:9999",
				"interface":"eth0",
				"minPeers":3,
				"announceInterval":"1s",
				"discoverTimeout":"30s"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if resp.Raft.Discovery != "multicast" {
		t.Fatalf("raft.discovery = %q, want %q", resp.Raft.Discovery, "multicast")
	}
	if resp.Raft.Multicast == nil {
		t.Fatal("raft.multicast should not be nil")
	}
	if resp.Raft.Multicast.SharedSecret != "hmac-secret" {
		t.Fatalf("raft.multicast.sharedSecret = %q, want %q", resp.Raft.Multicast.SharedSecret, "hmac-secret")
	}
	if resp.Raft.Multicast.MulticastGroup != "239.1.1.1:9999" {
		t.Fatalf("raft.multicast.multicastGroup = %q, want %q", resp.Raft.Multicast.MulticastGroup, "239.1.1.1:9999")
	}
	if resp.Raft.Multicast.Interface != "eth0" {
		t.Fatalf("raft.multicast.interface = %q, want %q", resp.Raft.Multicast.Interface, "eth0")
	}
	if resp.Raft.Multicast.MinPeers != 3 {
		t.Fatalf("raft.multicast.minPeers = %d, want 3", resp.Raft.Multicast.MinPeers)
	}
	if resp.Raft.Multicast.AnnounceInterval != "1s" {
		t.Fatalf("raft.multicast.announceInterval = %q, want %q", resp.Raft.Multicast.AnnounceInterval, "1s")
	}
	if resp.Raft.Multicast.DiscoverTimeout != "30s" {
		t.Fatalf("raft.multicast.discoverTimeout = %q, want %q", resp.Raft.Multicast.DiscoverTimeout, "30s")
	}
}

func TestHandleCreateDatabase_WithRaftDNS(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:     cfg.Name,
			Engine:   cfg.Engine,
			Backend:  cfg.Backend,
			Status:   database.StatusOpen,
			Protocol: cfg.Protocol,
			Mode:     database.ModeEmbedded,
			Raft:     cfg.Raft,
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{
		"name":"dnsdb","engine":"kv","backend":"raft",
		"raft":{
			"clusterMode":"distributed",
			"discovery":"dns",
			"dns":{
				"service":"_raft","proto":"_tcp",
				"domain":"cluster.local","lookupTimeout":"5s"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if resp.Raft.Discovery != "dns" {
		t.Fatalf("raft.discovery = %q, want %q", resp.Raft.Discovery, "dns")
	}
	if resp.Raft.DNS == nil {
		t.Fatal("raft.dns should not be nil")
	}
	if resp.Raft.DNS.Service != "_raft" {
		t.Fatalf("raft.dns.service = %q, want %q", resp.Raft.DNS.Service, "_raft")
	}
	if resp.Raft.DNS.Proto != "_tcp" {
		t.Fatalf("raft.dns.proto = %q, want %q", resp.Raft.DNS.Proto, "_tcp")
	}
	if resp.Raft.DNS.Domain != "cluster.local" {
		t.Fatalf("raft.dns.domain = %q, want %q", resp.Raft.DNS.Domain, "cluster.local")
	}
	if resp.Raft.DNS.LookupTimeout != "5s" {
		t.Fatalf("raft.dns.lookupTimeout = %q, want %q", resp.Raft.DNS.LookupTimeout, "5s")
	}
}

func TestHandleCreateDatabase_WithRaftToken(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:     cfg.Name,
			Engine:   cfg.Engine,
			Backend:  cfg.Backend,
			Status:   database.StatusOpen,
			Protocol: cfg.Protocol,
			Mode:     database.ModeEmbedded,
			Raft:     cfg.Raft,
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{
		"name":"tokendb","engine":"kv","backend":"raft",
		"raft":{
			"clusterMode":"distributed",
			"discovery":"token",
			"token":{
				"endpoint":"https://discovery.example.com/join",
				"token":"bootstrap-token-abc123",
				"clusterSize":3,
				"timeout":"10s"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp databaseInfoResponse
	decodeJSON(t, rec, &resp)
	if resp.Raft == nil {
		t.Fatal("raft config should not be nil")
	}
	if resp.Raft.Discovery != "token" {
		t.Fatalf("raft.discovery = %q, want %q", resp.Raft.Discovery, "token")
	}
	if resp.Raft.Token == nil {
		t.Fatal("raft.token should not be nil")
	}
	if resp.Raft.Token.Endpoint != "https://discovery.example.com/join" {
		t.Fatalf("raft.token.endpoint = %q, want %q", resp.Raft.Token.Endpoint, "https://discovery.example.com/join")
	}
	if resp.Raft.Token.Token != "bootstrap-token-abc123" {
		t.Fatalf("raft.token.token = %q, want %q", resp.Raft.Token.Token, "bootstrap-token-abc123")
	}
	if resp.Raft.Token.ClusterSize != 3 {
		t.Fatalf("raft.token.clusterSize = %d, want 3", resp.Raft.Token.ClusterSize)
	}
	if resp.Raft.Token.Timeout != "10s" {
		t.Fatalf("raft.token.timeout = %q, want %q", resp.Raft.Token.Timeout, "10s")
	}
}

// ---------------------------------------------------------------------------
// raftConfigFromRequest tests
// ---------------------------------------------------------------------------

func TestRaftConfigFromRequest(t *testing.T) {
	req := &raftConfigRequest{
		ClusterMode:    "distributed",
		ShardID:        5,
		ReplicaID:      2,
		RaftAddress:    "10.0.0.1:63001",
		Peers:          []string{"10.0.0.2:63001"},
		LocalNodeCount: 3,
		Discovery:      "static",
		Static: &staticDiscoveryConfigRequest{
			Peers:        []string{"10.0.0.2:63001", "10.0.0.3:63001"},
			SharedSecret: "secret",
		},
		Multicast: &multicastDiscoveryConfigRequest{
			SharedSecret:     "mcast-secret",
			MulticastGroup:   "239.1.1.1:9999",
			Interface:        "eth0",
			MinPeers:         3,
			AnnounceInterval: "2s",
			DiscoverTimeout:  "30s",
		},
		DNS: &dnsDiscoveryConfigRequest{
			Service:       "_raft",
			Proto:         "_tcp",
			Domain:        "cluster.local",
			LookupTimeout: "5s",
		},
		Token: &tokenDiscoveryConfigRequest{
			Endpoint:    "https://disco.example.com/join",
			Token:       "tok-abc",
			ClusterSize: 5,
			Timeout:     "10s",
		},
	}

	raft := raftConfigFromRequest(req)

	if raft.ClusterMode != database.RaftDistributed {
		t.Fatalf("ClusterMode = %q, want %q", raft.ClusterMode, database.RaftDistributed)
	}
	if raft.ShardID != 5 {
		t.Fatalf("ShardID = %d, want 5", raft.ShardID)
	}
	if raft.ReplicaID != 2 {
		t.Fatalf("ReplicaID = %d, want 2", raft.ReplicaID)
	}
	if raft.RaftAddress != "10.0.0.1:63001" {
		t.Fatalf("RaftAddress = %q, want %q", raft.RaftAddress, "10.0.0.1:63001")
	}
	if len(raft.Peers) != 1 {
		t.Fatalf("Peers length = %d, want 1", len(raft.Peers))
	}
	if raft.LocalNodeCount != 3 {
		t.Fatalf("LocalNodeCount = %d, want 3", raft.LocalNodeCount)
	}
	if raft.Discovery != database.DiscoveryStatic {
		t.Fatalf("Discovery = %q, want %q", raft.Discovery, database.DiscoveryStatic)
	}

	// Static
	if raft.Static == nil {
		t.Fatal("Static should not be nil")
	}
	if len(raft.Static.Peers) != 2 {
		t.Fatalf("Static.Peers length = %d, want 2", len(raft.Static.Peers))
	}
	if raft.Static.SharedSecret != "secret" {
		t.Fatalf("Static.SharedSecret = %q, want %q", raft.Static.SharedSecret, "secret")
	}

	// Multicast
	if raft.Multicast == nil {
		t.Fatal("Multicast should not be nil")
	}
	if raft.Multicast.SharedSecret != "mcast-secret" {
		t.Fatalf("Multicast.SharedSecret = %q, want %q", raft.Multicast.SharedSecret, "mcast-secret")
	}
	if raft.Multicast.MulticastGroup != "239.1.1.1:9999" {
		t.Fatalf("Multicast.MulticastGroup = %q, want %q", raft.Multicast.MulticastGroup, "239.1.1.1:9999")
	}
	if raft.Multicast.Interface != "eth0" {
		t.Fatalf("Multicast.Interface = %q, want %q", raft.Multicast.Interface, "eth0")
	}
	if raft.Multicast.MinPeers != 3 {
		t.Fatalf("Multicast.MinPeers = %d, want 3", raft.Multicast.MinPeers)
	}
	if raft.Multicast.AnnounceInterval != "2s" {
		t.Fatalf("Multicast.AnnounceInterval = %q, want %q", raft.Multicast.AnnounceInterval, "2s")
	}
	if raft.Multicast.DiscoverTimeout != "30s" {
		t.Fatalf("Multicast.DiscoverTimeout = %q, want %q", raft.Multicast.DiscoverTimeout, "30s")
	}

	// DNS
	if raft.DNS == nil {
		t.Fatal("DNS should not be nil")
	}
	if raft.DNS.Service != "_raft" {
		t.Fatalf("DNS.Service = %q, want %q", raft.DNS.Service, "_raft")
	}
	if raft.DNS.Proto != "_tcp" {
		t.Fatalf("DNS.Proto = %q, want %q", raft.DNS.Proto, "_tcp")
	}
	if raft.DNS.Domain != "cluster.local" {
		t.Fatalf("DNS.Domain = %q, want %q", raft.DNS.Domain, "cluster.local")
	}
	if raft.DNS.LookupTimeout != "5s" {
		t.Fatalf("DNS.LookupTimeout = %q, want %q", raft.DNS.LookupTimeout, "5s")
	}

	// Token
	if raft.Token == nil {
		t.Fatal("Token should not be nil")
	}
	if raft.Token.Endpoint != "https://disco.example.com/join" {
		t.Fatalf("Token.Endpoint = %q, want %q", raft.Token.Endpoint, "https://disco.example.com/join")
	}
	if raft.Token.Token != "tok-abc" {
		t.Fatalf("Token.Token = %q, want %q", raft.Token.Token, "tok-abc")
	}
	if raft.Token.ClusterSize != 5 {
		t.Fatalf("Token.ClusterSize = %d, want 5", raft.Token.ClusterSize)
	}
	if raft.Token.Timeout != "10s" {
		t.Fatalf("Token.Timeout = %q, want %q", raft.Token.Timeout, "10s")
	}
}

func TestRaftConfigFromRequest_NilSubConfigs(t *testing.T) {
	req := &raftConfigRequest{
		ClusterMode: "local",
		Discovery:   "static",
	}

	raft := raftConfigFromRequest(req)

	if raft.ClusterMode != database.RaftLocal {
		t.Fatalf("ClusterMode = %q, want %q", raft.ClusterMode, database.RaftLocal)
	}
	if raft.Static != nil {
		t.Fatal("Static should be nil when not set in request")
	}
	if raft.Multicast != nil {
		t.Fatal("Multicast should be nil when not set in request")
	}
	if raft.DNS != nil {
		t.Fatal("DNS should be nil when not set in request")
	}
	if raft.Token != nil {
		t.Fatal("Token should be nil when not set in request")
	}
}

// ---------------------------------------------------------------------------
// raftConfigToResponse tests
// ---------------------------------------------------------------------------

func TestRaftConfigToResponse(t *testing.T) {
	raft := &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftDistributed,
		ShardID:        7,
		ReplicaID:      3,
		RaftAddress:    "10.0.0.5:63001",
		Peers:          []string{"10.0.0.6:63001"},
		LocalNodeCount: 1,
		Discovery:      database.DiscoveryMulticast,
		Static: &database.StaticDiscoveryConfig{
			Peers:        []string{"10.0.0.6:63001"},
			SharedSecret: "static-secret",
		},
		Multicast: &database.MulticastDiscoveryConfig{
			SharedSecret:     "mcast-key",
			MulticastGroup:   "239.0.0.1:8888",
			Interface:        "wlan0",
			MinPeers:         2,
			AnnounceInterval: "500ms",
			DiscoverTimeout:  "15s",
		},
		DNS: &database.DNSDiscoveryConfig{
			Service:       "_qrdb",
			Proto:         "_udp",
			Domain:        "prod.internal",
			LookupTimeout: "3s",
		},
		Token: &database.TokenDiscoveryConfig{
			Endpoint:    "https://join.example.com",
			Token:       "join-token",
			ClusterSize: 7,
			Timeout:     "20s",
		},
	}

	resp := raftConfigToResponse(raft)

	if resp.ClusterMode != "distributed" {
		t.Fatalf("ClusterMode = %q, want %q", resp.ClusterMode, "distributed")
	}
	if resp.ShardID != 7 {
		t.Fatalf("ShardID = %d, want 7", resp.ShardID)
	}
	if resp.ReplicaID != 3 {
		t.Fatalf("ReplicaID = %d, want 3", resp.ReplicaID)
	}
	if resp.RaftAddress != "10.0.0.5:63001" {
		t.Fatalf("RaftAddress = %q, want %q", resp.RaftAddress, "10.0.0.5:63001")
	}
	if len(resp.Peers) != 1 {
		t.Fatalf("Peers length = %d, want 1", len(resp.Peers))
	}
	if resp.LocalNodeCount != 1 {
		t.Fatalf("LocalNodeCount = %d, want 1", resp.LocalNodeCount)
	}
	if resp.Discovery != "multicast" {
		t.Fatalf("Discovery = %q, want %q", resp.Discovery, "multicast")
	}

	// Static
	if resp.Static == nil {
		t.Fatal("Static should not be nil")
	}
	if resp.Static.SharedSecret != "static-secret" {
		t.Fatalf("Static.SharedSecret = %q, want %q", resp.Static.SharedSecret, "static-secret")
	}

	// Multicast
	if resp.Multicast == nil {
		t.Fatal("Multicast should not be nil")
	}
	if resp.Multicast.SharedSecret != "mcast-key" {
		t.Fatalf("Multicast.SharedSecret = %q, want %q", resp.Multicast.SharedSecret, "mcast-key")
	}
	if resp.Multicast.MulticastGroup != "239.0.0.1:8888" {
		t.Fatalf("Multicast.MulticastGroup = %q, want %q", resp.Multicast.MulticastGroup, "239.0.0.1:8888")
	}
	if resp.Multicast.Interface != "wlan0" {
		t.Fatalf("Multicast.Interface = %q, want %q", resp.Multicast.Interface, "wlan0")
	}
	if resp.Multicast.MinPeers != 2 {
		t.Fatalf("Multicast.MinPeers = %d, want 2", resp.Multicast.MinPeers)
	}
	if resp.Multicast.AnnounceInterval != "500ms" {
		t.Fatalf("Multicast.AnnounceInterval = %q, want %q", resp.Multicast.AnnounceInterval, "500ms")
	}
	if resp.Multicast.DiscoverTimeout != "15s" {
		t.Fatalf("Multicast.DiscoverTimeout = %q, want %q", resp.Multicast.DiscoverTimeout, "15s")
	}

	// DNS
	if resp.DNS == nil {
		t.Fatal("DNS should not be nil")
	}
	if resp.DNS.Service != "_qrdb" {
		t.Fatalf("DNS.Service = %q, want %q", resp.DNS.Service, "_qrdb")
	}
	if resp.DNS.Proto != "_udp" {
		t.Fatalf("DNS.Proto = %q, want %q", resp.DNS.Proto, "_udp")
	}
	if resp.DNS.Domain != "prod.internal" {
		t.Fatalf("DNS.Domain = %q, want %q", resp.DNS.Domain, "prod.internal")
	}
	if resp.DNS.LookupTimeout != "3s" {
		t.Fatalf("DNS.LookupTimeout = %q, want %q", resp.DNS.LookupTimeout, "3s")
	}

	// Token
	if resp.Token == nil {
		t.Fatal("Token should not be nil")
	}
	if resp.Token.Endpoint != "https://join.example.com" {
		t.Fatalf("Token.Endpoint = %q, want %q", resp.Token.Endpoint, "https://join.example.com")
	}
	if resp.Token.Token != "join-token" {
		t.Fatalf("Token.Token = %q, want %q", resp.Token.Token, "join-token")
	}
	if resp.Token.ClusterSize != 7 {
		t.Fatalf("Token.ClusterSize = %d, want 7", resp.Token.ClusterSize)
	}
	if resp.Token.Timeout != "20s" {
		t.Fatalf("Token.Timeout = %q, want %q", resp.Token.Timeout, "20s")
	}
}

func TestRaftConfigToResponse_NilSubConfigs(t *testing.T) {
	raft := &database.DatabaseRaftConfig{
		ClusterMode: database.RaftLocal,
		Discovery:   database.DiscoveryStatic,
	}

	resp := raftConfigToResponse(raft)

	if resp.ClusterMode != "local" {
		t.Fatalf("ClusterMode = %q, want %q", resp.ClusterMode, "local")
	}
	if resp.Static != nil {
		t.Fatal("Static should be nil")
	}
	if resp.Multicast != nil {
		t.Fatal("Multicast should be nil")
	}
	if resp.DNS != nil {
		t.Fatal("DNS should be nil")
	}
	if resp.Token != nil {
		t.Fatal("Token should be nil")
	}
}

// ---------------------------------------------------------------------------
// databaseInfoToResponse with Raft tests
// ---------------------------------------------------------------------------

func TestDatabaseInfoToResponse_WithRaft(t *testing.T) {
	info := &database.DatabaseInfo{
		Name:    "raftdb",
		Engine:  database.EngineKV,
		Backend: database.BackendRaft,
		Status:  database.StatusOpen,
		Mode:    database.ModeEmbedded,
		Raft: &database.DatabaseRaftConfig{
			ClusterMode: database.RaftDistributed,
			ShardID:     10,
			ReplicaID:   1,
			Discovery:   database.DiscoveryDNS,
			DNS: &database.DNSDiscoveryConfig{
				Domain: "test.local",
			},
		},
	}

	resp := databaseInfoToResponse(info)

	if resp.Name != "raftdb" {
		t.Fatalf("name = %q, want %q", resp.Name, "raftdb")
	}
	if resp.Mode != "embedded" {
		t.Fatalf("mode = %q, want %q", resp.Mode, "embedded")
	}
	if resp.Raft == nil {
		t.Fatal("raft should not be nil")
	}
	if resp.Raft.ClusterMode != "distributed" {
		t.Fatalf("raft.clusterMode = %q, want %q", resp.Raft.ClusterMode, "distributed")
	}
	if resp.Raft.ShardID != 10 {
		t.Fatalf("raft.shardId = %d, want 10", resp.Raft.ShardID)
	}
	if resp.Raft.Discovery != "dns" {
		t.Fatalf("raft.discovery = %q, want %q", resp.Raft.Discovery, "dns")
	}
	if resp.Raft.DNS == nil {
		t.Fatal("raft.dns should not be nil")
	}
	if resp.Raft.DNS.Domain != "test.local" {
		t.Fatalf("raft.dns.domain = %q, want %q", resp.Raft.DNS.Domain, "test.local")
	}
}

func TestDatabaseInfoToResponse_WithoutRaft(t *testing.T) {
	info := &database.DatabaseInfo{
		Name:    "simple",
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
		Status:  database.StatusOpen,
	}

	resp := databaseInfoToResponse(info)

	if resp.Name != "simple" {
		t.Fatalf("name = %q, want %q", resp.Name, "simple")
	}
	if resp.Raft != nil {
		t.Fatal("raft should be nil when not set in info")
	}
}

// ---------------------------------------------------------------------------
// Raft config JSON serialization tests
// ---------------------------------------------------------------------------

func TestRaftConfigRequest_JSON(t *testing.T) {
	input := `{
		"clusterMode":"distributed",
		"shardId":1,"replicaId":2,
		"raftAddress":"10.0.0.1:63001",
		"peers":["10.0.0.2:63001"],
		"localNodeCount":3,
		"discovery":"multicast",
		"static":{"peers":["a","b"],"sharedSecret":"ss"},
		"multicast":{"sharedSecret":"ms","multicastGroup":"239.1.1.1:9999","interface":"eth0","minPeers":2,"announceInterval":"1s","discoverTimeout":"10s"},
		"dns":{"service":"_raft","proto":"_tcp","domain":"d.local","lookupTimeout":"5s"},
		"token":{"endpoint":"https://e.com","token":"t","clusterSize":3,"timeout":"8s"}
	}`

	var req raftConfigRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify round-trip.
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip raftConfigRequest
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}

	if roundTrip.ClusterMode != "distributed" {
		t.Fatalf("ClusterMode = %q, want %q", roundTrip.ClusterMode, "distributed")
	}
	if roundTrip.ShardID != 1 {
		t.Fatalf("ShardID = %d, want 1", roundTrip.ShardID)
	}
	if roundTrip.ReplicaID != 2 {
		t.Fatalf("ReplicaID = %d, want 2", roundTrip.ReplicaID)
	}
	if roundTrip.Discovery != "multicast" {
		t.Fatalf("Discovery = %q, want %q", roundTrip.Discovery, "multicast")
	}
	if roundTrip.Static == nil {
		t.Fatal("Static should not be nil")
	}
	if roundTrip.Static.SharedSecret != "ss" {
		t.Fatalf("Static.SharedSecret = %q, want %q", roundTrip.Static.SharedSecret, "ss")
	}
	if roundTrip.Multicast == nil {
		t.Fatal("Multicast should not be nil")
	}
	if roundTrip.Multicast.SharedSecret != "ms" {
		t.Fatalf("Multicast.SharedSecret = %q, want %q", roundTrip.Multicast.SharedSecret, "ms")
	}
	if roundTrip.DNS == nil {
		t.Fatal("DNS should not be nil")
	}
	if roundTrip.DNS.Domain != "d.local" {
		t.Fatalf("DNS.Domain = %q, want %q", roundTrip.DNS.Domain, "d.local")
	}
	if roundTrip.Token == nil {
		t.Fatal("Token should not be nil")
	}
	if roundTrip.Token.Endpoint != "https://e.com" {
		t.Fatalf("Token.Endpoint = %q, want %q", roundTrip.Token.Endpoint, "https://e.com")
	}
	if roundTrip.Token.ClusterSize != 3 {
		t.Fatalf("Token.ClusterSize = %d, want 3", roundTrip.Token.ClusterSize)
	}
}

func TestRaftConfigResponse_JSON(t *testing.T) {
	resp := raftConfigResponse{
		ClusterMode:    "distributed",
		ShardID:        4,
		ReplicaID:      1,
		RaftAddress:    "10.0.0.1:63001",
		Peers:          []string{"10.0.0.2:63001"},
		LocalNodeCount: 3,
		Discovery:      "token",
		Static: &staticDiscoveryConfigResponse{
			Peers:        []string{"a"},
			SharedSecret: "ss",
		},
		Multicast: &multicastDiscoveryConfigResponse{
			SharedSecret:     "ms",
			MulticastGroup:   "239.1.1.1:9999",
			Interface:        "eth0",
			MinPeers:         2,
			AnnounceInterval: "1s",
			DiscoverTimeout:  "10s",
		},
		DNS: &dnsDiscoveryConfigResponse{
			Service:       "_raft",
			Proto:         "_tcp",
			Domain:        "d.local",
			LookupTimeout: "5s",
		},
		Token: &tokenDiscoveryConfigResponse{
			Endpoint:    "https://e.com",
			Token:       "t",
			ClusterSize: 3,
			Timeout:     "8s",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var roundTrip raftConfigResponse
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}

	if roundTrip.ClusterMode != "distributed" {
		t.Fatalf("ClusterMode = %q, want %q", roundTrip.ClusterMode, "distributed")
	}
	if roundTrip.ShardID != 4 {
		t.Fatalf("ShardID = %d, want 4", roundTrip.ShardID)
	}
	if roundTrip.Discovery != "token" {
		t.Fatalf("Discovery = %q, want %q", roundTrip.Discovery, "token")
	}
	if roundTrip.Static == nil {
		t.Fatal("Static should not be nil")
	}
	if roundTrip.Static.SharedSecret != "ss" {
		t.Fatalf("Static.SharedSecret = %q, want %q", roundTrip.Static.SharedSecret, "ss")
	}
	if roundTrip.Multicast == nil {
		t.Fatal("Multicast should not be nil")
	}
	if roundTrip.Multicast.MinPeers != 2 {
		t.Fatalf("Multicast.MinPeers = %d, want 2", roundTrip.Multicast.MinPeers)
	}
	if roundTrip.DNS == nil {
		t.Fatal("DNS should not be nil")
	}
	if roundTrip.DNS.Domain != "d.local" {
		t.Fatalf("DNS.Domain = %q, want %q", roundTrip.DNS.Domain, "d.local")
	}
	if roundTrip.Token == nil {
		t.Fatal("Token should not be nil")
	}
	if roundTrip.Token.Token != "t" {
		t.Fatalf("Token.Token = %q, want %q", roundTrip.Token.Token, "t")
	}
	if roundTrip.Token.ClusterSize != 3 {
		t.Fatalf("Token.ClusterSize = %d, want 3", roundTrip.Token.ClusterSize)
	}
	if roundTrip.Token.Timeout != "8s" {
		t.Fatalf("Token.Timeout = %q, want %q", roundTrip.Token.Timeout, "8s")
	}
}

func TestWithDatabaseService_Option(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))
	if srv.databaseSvc == nil {
		t.Fatal("databaseSvc should not be nil after WithDatabaseService")
	}
}
