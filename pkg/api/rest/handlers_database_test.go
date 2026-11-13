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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
	scaleFn          func(name string, targetReplicas int) (*database.DatabaseInfo, error)
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
			Backend: database.BackendPebble,
			Status:  database.StatusOpen,
		},
	}, nil
}

func (m *mockDatabaseService) Drop(_ context.Context, name string) error {
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

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *mockDatabaseService) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *mockDatabaseService) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
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
	return m.Drop(context.Background(), name)
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

func (m *mockDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *mockDatabaseService) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}

func (m *mockDatabaseService) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	if m.scaleFn != nil {
		return m.scaleFn(name, targetReplicas)
	}
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

// ---------------------------------------------------------------------------
// handleCreateDatabase tests
// ---------------------------------------------------------------------------

func TestHandleCreateDatabase_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"mydb","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp transport.DatabaseInfo
	decodeJSON(t, rec, &resp)
	if resp.Name != "mydb" {
		t.Fatalf("name = %q, want %q", resp.Name, "mydb")
	}
	if resp.Backend != "pebble" {
		t.Fatalf("backend = %q, want %q", resp.Backend, "pebble")
	}
}

// TestHandleCreateDatabase_StaleEngineFieldRefused pins OD-13: a create
// request carrying the removed "engine" field is refused as an unknown
// field (400), not silently ignored -- the same generic strict-decoding rule
// decodeGuardedRequest applies to every state-changing endpoint.
func TestHandleCreateDatabase_StaleEngineFieldRefused(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"mydb","engine":"kv","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Mock: service.DatabaseCredentialCreator
// ---------------------------------------------------------------------------

// credentialedMockDatabaseService additionally implements
// service.DatabaseCredentialCreator so handleCreateDatabase's type-assertion
// succeeds and a request's barrier secret can be observed.
type credentialedMockDatabaseService struct {
	*mockDatabaseService

	gotCfg     database.DatabaseConfig
	gotSecret  []byte // snapshot taken during the call; the handler destroys the credential after
	calledWith string
	err        error
}

func newCredentialedMockDatabaseService() *credentialedMockDatabaseService {
	return &credentialedMockDatabaseService{mockDatabaseService: newMockDatabaseService()}
}

// Compile-time interface check.
var _ service.DatabaseCredentialCreator = (*credentialedMockDatabaseService)(nil)

func (m *credentialedMockDatabaseService) CreateWithCredentials(_ context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	m.gotCfg, m.gotSecret, m.calledWith = cfg, sealtest.Snapshot(creds), "createWithCredentials"
	if m.err != nil {
		return nil, m.err
	}
	return &database.DatabaseInfo{Name: cfg.Name, Backend: cfg.Backend, Status: database.StatusOpen, Barrier: cfg.Barrier}, nil
}

func (m *credentialedMockDatabaseService) CreateForTenantWithCredentials(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	return m.CreateWithCredentials(ctx, cfg, creds)
}

// TestHandleCreateDatabase_BarrierSecretReachesCredentialedCreator verifies
// that a POST /v1/databases body carrying a barrier.secret is decoded and
// forwarded as seal.Credentials.Secret to
// service.DatabaseCredentialCreator.CreateForTenantWithCredentials when the
// resolved DatabaseServicer implements it, and that the secret never leaks
// into the JSON response.
func TestHandleCreateDatabase_BarrierSecretReachesCredentialedCreator(t *testing.T) {
	dbSvc := newCredentialedMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"secure-db","backend":"pebble","dataDir":"/data/secure-db",` +
		`"barrier":{"enabled":true,"mode":"passphrase","secret":"correct-horse-battery-staple"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	if dbSvc.calledWith != "createWithCredentials" {
		t.Fatalf("calledWith = %q, want %q", dbSvc.calledWith, "createWithCredentials")
	}
	if dbSvc.gotCfg.DataDir != "/data/secure-db" {
		t.Errorf("DataDir = %q, want %q", dbSvc.gotCfg.DataDir, "/data/secure-db")
	}
	if dbSvc.gotCfg.Barrier == nil || !dbSvc.gotCfg.Barrier.Enabled {
		t.Fatal("Barrier.Enabled = false, want true")
	}
	if string(dbSvc.gotSecret) != "correct-horse-battery-staple" {
		t.Errorf("Credentials.Secret = %q, want %q", dbSvc.gotSecret, "correct-horse-battery-staple")
	}

	// The response body must never echo the secret back to the caller.
	if strings.Contains(rec.Body.String(), "correct-horse-battery-staple") {
		t.Error("response body leaks the barrier secret")
	}
}

// TestHandleCreateDatabase_NoBarrier_EmptyCredentials verifies that a request
// with no barrier block still routes through CreateForTenantWithCredentials
// (when the resolved service implements DatabaseCredentialCreator), carrying
// empty seal.Credentials and no Barrier config -- confirming the credentialed
// path is a strict superset of the plain path, not a behavior change for
// non-barrier creates.
func TestHandleCreateDatabase_NoBarrier_EmptyCredentials(t *testing.T) {
	dbSvc := newCredentialedMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"plain-db","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
	if dbSvc.calledWith != "createWithCredentials" {
		t.Fatalf("calledWith = %q, want %q", dbSvc.calledWith, "createWithCredentials")
	}
	if dbSvc.gotCfg.Barrier != nil {
		t.Errorf("Barrier = %+v, want nil", dbSvc.gotCfg.Barrier)
	}
	if len(dbSvc.gotSecret) != 0 {
		t.Errorf("Credentials.Secret = %q, want empty", dbSvc.gotSecret)
	}
}

// TestHandleCreateDatabase_NoBarrier_PlainServiceFallback verifies that a
// DatabaseServicer which does NOT implement DatabaseCredentialCreator (the
// pre-existing mockDatabaseService) is still usable for database creation --
// the type-assertion fallback in createDatabaseForTenant is exercised.
func TestHandleCreateDatabase_NoBarrier_PlainServiceFallback(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"legacy-mock-db","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)
}

// TestHandleCreateDatabase_BarrierEnabled_SharedStoreRejected verifies a
// SharedStoreBarrierUnsupportedError from the service surfaces as an HTTP
// error response, not a 201.
func TestHandleCreateDatabase_BarrierEnabled_SharedStoreRejected(t *testing.T) {
	dbSvc := newCredentialedMockDatabaseService()
	dbSvc.err = &database.SharedStoreBarrierUnsupportedError{Name: "shared-db", Backend: database.BackendPebble}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"shared-db","backend":"pebble","barrier":{"enabled":true,"secret":"whatever"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusCreated {
		t.Fatalf("expected a non-201 status for a shared-store barrier rejection, got %d", rec.Code)
	}
}

func TestHandleCreateDatabase_WithSQL(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"sqldb","backend":"pebble","protocol":{"mysql":true,"postgresql":false}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp transport.DatabaseInfo
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

// TestHandleCreateDatabase_WithEtcd pins the parity fix for a review-found
// bug: createDatabaseRequest.Protocol carried MySQL and PostgreSQL but no
// Etcd field at all, so "database create" with an etcd block silently
// dropped it on every build -- gRPC and QUIC already carried it. See
// sdk/go/conformance's TestDatabaseCreate_WithEtcd_EveryTransport for the
// cross-transport version of this same check.
func TestHandleCreateDatabase_WithEtcd(t *testing.T) {
	dbSvc := newMockDatabaseService()
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"etcddb","backend":"pebble","protocol":{"etcd":{"enabled":true,"listenAddr":"127.0.0.1:2379"}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusCreated)

	var resp transport.DatabaseInfo
	decodeJSON(t, rec, &resp)
	if resp.Name != "etcddb" {
		t.Fatalf("name = %q, want %q", resp.Name, "etcddb")
	}
	if resp.Protocol.Etcd == nil {
		t.Fatal("protocol.etcd = nil, want an enabled etcd config")
	}
	if !resp.Protocol.Etcd.Enabled {
		t.Fatal("protocol.etcd.enabled = false, want true")
	}
	if resp.Protocol.Etcd.ListenAddr != "127.0.0.1:2379" {
		t.Fatalf("protocol.etcd.listenAddr = %q, want %q", resp.Protocol.Etcd.ListenAddr, "127.0.0.1:2379")
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

	body := `{"name":"existing","backend":"pebble"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

// TestHandleCreateDatabase_InvalidRaftStateStorage pins that a raft create
// naming a state storage outside pebble, file and memory is a client error:
// the service's validation refusal maps to 400, not 500.
func TestHandleCreateDatabase_InvalidRaftStateStorage(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		t.Fatalf("Validate() accepted raft.stateStorage %q", cfg.Raft.StateStorage)
		return nil, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"name":"rocksdb","backend":"raft","raft":{"clusterMode":"local","stateStorage":"rocks"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCreateDatabase_NilService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	body := `{"name":"mydb","backend":"pebble"}`
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

	var resp transport.DatabaseInfo
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

	var resp transport.DatabaseInfo
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

	var resp transport.DatabaseInfo
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

	var resp transport.DatabaseInfo
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
// Request/response type serialization tests
// ---------------------------------------------------------------------------

func TestCreateDatabaseRequest_JSON(t *testing.T) {
	input := `{"name":"testdb","backend":"memory","protocol":{"mysql":true,"postgresql":false}}`
	var req createDatabaseRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Name != "testdb" {
		t.Fatalf("name = %q, want %q", req.Name, "testdb")
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

// ---------------------------------------------------------------------------
// handleCreateDatabase with Raft discovery tests
// ---------------------------------------------------------------------------

func TestHandleCreateDatabase_WithRaftStatic(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createFn = func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{
			Name:     cfg.Name,
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
		"name":"staticdb","backend":"raft",
		"raft":{
			"clusterMode":"distributed",
			"replicaId":1,
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

	var resp transport.DatabaseInfo
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
	// The secret is write-only: the response reports it set, never its value.
	if !resp.Raft.Static.SharedSecretSet || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("raft.static: sharedSecretSet = %v, body %s; want set and no secret", resp.Raft.Static.SharedSecretSet, rec.Body.String())
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
		"name":"mcastdb","backend":"raft",
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

	var resp transport.DatabaseInfo
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
	if !resp.Raft.Multicast.SharedSecretSet || strings.Contains(rec.Body.String(), "hmac-secret") {
		t.Fatalf("raft.multicast: sharedSecretSet = %v, body %s; want set and no secret", resp.Raft.Multicast.SharedSecretSet, rec.Body.String())
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
		"name":"dnsdb","backend":"raft",
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

	var resp transport.DatabaseInfo
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
		"name":"tokendb","backend":"raft",
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

	var resp transport.DatabaseInfo
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
	if !resp.Raft.Token.TokenSet || strings.Contains(rec.Body.String(), "bootstrap-token-abc123") {
		t.Fatalf("raft.token: tokenSet = %v, body %s; want set and no token", resp.Raft.Token.TokenSet, rec.Body.String())
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
	// The request carries no shard ID: the cluster issues it at create.
	if raft.ShardID != 0 {
		t.Fatalf("ShardID = %d, want 0", raft.ShardID)
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
// Raft config JSON serialization tests
// ---------------------------------------------------------------------------

func TestRaftConfigRequest_JSON(t *testing.T) {
	input := `{
		"clusterMode":"distributed",
		"replicaId":2,
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
	// A request naming a shard ID is not honoured: the field does not exist,
	// so the name is dropped on decode and never reaches the server.
	if strings.Contains(string(data), "shardId") {
		t.Fatalf("a decoded raft request still carries a shard ID: %s", data)
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

// TestHandleDatabaseByName_UpdateBadBody proves every PATCH sub-route of
// /v1/databases/{name} refuses an undecodable body with 400.
func TestHandleDatabaseByName_UpdateBadBody(t *testing.T) {
	for _, sub := range []string{"etcd", "raft", "barrier", "connection"} {
		t.Run(sub, func(t *testing.T) {
			s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
				WithDatabaseService(newMockDatabaseService()))
			req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/"+sub, strings.NewReader(`bad`))
			rec := httptest.NewRecorder()
			s.dispatch(rec, req)
			assertStatus(t, rec, http.StatusBadRequest)
		})
	}
}

// TestHandleUpdateDatabaseEtcd_WithCallback proves WithOnEtcdUpdated's
// callback runs after an etcd update, naming the database and its tenant:
// another tenant may hold a database named mydb too.
func TestHandleUpdateDatabaseEtcd_WithCallback(t *testing.T) {
	called := false
	dbSvc := &mockDatabaseServiceWithEtcd{
		updateEtcdResult: &database.DatabaseInfo{TenantID: "acme", Name: "mydb", Backend: database.BackendPebble},
	}
	fn := func(tenantID, dbName string, _ *database.EtcdConfig) error {
		called = true
		if tenantID != "acme" || dbName != "mydb" {
			t.Errorf("onEtcdUpdated(%q, %q), want (acme, mydb)", tenantID, dbName)
		}
		return nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc),
		WithOnEtcdUpdated(fn))
	body := `{"enabled":true,"listen_addr":"localhost:2379"}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/etcd", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUpdateDatabaseEtcd(rec, req, "mydb")
	if !called {
		t.Error("onEtcdUpdated callback not invoked")
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

// ---------------------------------------------------------------------------
// handleScaleDatabase tests
// ---------------------------------------------------------------------------

// TestHandleScaleDatabase_Success verifies a successful scale request
// returns the updated DatabaseInfo.
func TestHandleScaleDatabase_Success(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.scaleFn = func(name string, targetReplicas int) (*database.DatabaseInfo, error) {
		return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"target_replicas":3}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/scale", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp transport.DatabaseInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Replicas != 3 {
		t.Errorf("resp.Replicas = %d, want 3", resp.Replicas)
	}
}

// TestHandleScaleDatabase_ForwardToLeader_Success verifies that when
// ScaleForTenant returns *service.NotLeaderError with a known leader
// address, handleScaleDatabase forwards the original PATCH request (method,
// path, and body intact) to the leader's REST server, mirroring the KV
// PUT/DELETE leader-forwarding path (see TestHandler_KVPut_ForwardToLeader_Success).
func TestHandleScaleDatabase_ForwardToLeader_Success(t *testing.T) {
	leaderSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("leader received method %s, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/databases/mydb/scale" {
			t.Fatalf("leader received path %s, want /v1/databases/mydb/scale", r.URL.Path)
		}
		fwd := r.Header.Get(headerForwardedBy)
		if fwd == "" {
			t.Fatal("leader did not receive X-Quicraft-Forwarded-By header")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("leader read body: %v", err)
		}
		var req scaleDatabaseRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("leader decode body: %v", err)
		}
		if req.TargetReplicas != 3 {
			t.Fatalf("leader received target_replicas=%d, want 3", req.TargetReplicas)
		}
		writeJSON(w, transport.DatabaseInfo{Name: "mydb", Replicas: 3})
	}))
	defer leaderSrv.Close()

	dbSvc := newMockDatabaseService()
	dbSvc.scaleFn = func(_ string, _ int) (*database.DatabaseInfo, error) {
		return nil, &service.NotLeaderError{
			ShardID:       7,
			LeaderID:      2,
			LeaderAddress: "10.0.0.2:63001",
		}
	}

	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leaderSrv.URL)

	host := &mockHost{addr: "10.0.0.1:63001"}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc), WithLeaderForwarding(reg), WithHost(host))

	body := `{"target_replicas":3}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/scale", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp transport.DatabaseInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal forwarded response: %v", err)
	}
	if resp.Replicas != 3 {
		t.Errorf("resp.Replicas = %d, want 3", resp.Replicas)
	}
}

// TestHandleScaleDatabase_NotLeader_NoResolver verifies that when
// ScaleForTenant returns *service.NotLeaderError but no leader-forwarding
// resolver is configured, handleScaleDatabase falls back to mapHTTPError's
// 503 Service Unavailable rather than forwarding.
func TestHandleScaleDatabase_NotLeader_NoResolver(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.scaleFn = func(_ string, _ int) (*database.DatabaseInfo, error) {
		return nil, &service.NotLeaderError{ShardID: 7, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithDatabaseService(dbSvc))

	body := `{"target_replicas":3}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/scale", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

// ---------------------------------------------------------------------------
// mockDatabaseServiceWithEtcd overrides UpdateEtcd to return a non-nil
// DatabaseInfo, avoiding nil dereference in databaseInfoToResponse.
// ---------------------------------------------------------------------------

type mockDatabaseServiceWithEtcd struct {
	mockDatabaseService
	updateEtcdResult *database.DatabaseInfo
}

func (m *mockDatabaseServiceWithEtcd) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.updateEtcdResult, nil
}

// UpdateEtcdForTenant overrides the embedded mock's promoted method so it
// routes to this type's UpdateEtcd (Go embedding is not virtual dispatch).
func (m *mockDatabaseServiceWithEtcd) UpdateEtcdForTenant(_ context.Context, name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, cfg)
}
