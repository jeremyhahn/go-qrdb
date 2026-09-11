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
	"errors"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockDatabaseSvc implements service.DatabaseServicer for transport tests.
// ---------------------------------------------------------------------------

type mockDatabaseSvc struct {
	mu sync.Mutex

	createErr   error
	getErr      error
	listErr     error
	dropErr     error
	updProtoErr error
	updEtcdErr  error
	updRaftErr  error
	updBarrErr  error
	updConnErr  error

	lastCreate        database.DatabaseConfig
	lastName          string
	lastUpdateBarrier *database.DatabaseBarrierConfig

	infoResp *database.DatabaseInfo
}

var _ service.DatabaseServicer = (*mockDatabaseSvc)(nil)

func newMockDatabaseSvc() *mockDatabaseSvc {
	return &mockDatabaseSvc{
		infoResp: &database.DatabaseInfo{
			Name:    "db1",
			Engine:  "kv",
			Backend: "pebble",
			Status:  "open",
			DataDir: "/tmp",
		},
	}
}

func (m *mockDatabaseSvc) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCreate = cfg
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) Get(name string) (*database.DatabaseInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastName = name
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) List() ([]database.DatabaseInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return []database.DatabaseInfo{*m.infoResp}, nil
}

func (m *mockDatabaseSvc) Drop(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastName = name
	return m.dropErr
}

func (m *mockDatabaseSvc) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updProtoErr != nil {
		return nil, m.updProtoErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updEtcdErr != nil {
		return nil, m.updEtcdErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updRaftErr != nil {
		return nil, m.updRaftErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) UpdateBarrier(_ string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	m.mu.Lock()
	m.lastUpdateBarrier = barrier
	m.mu.Unlock()
	if m.updBarrErr != nil {
		return nil, m.updBarrErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updConnErr != nil {
		return nil, m.updConnErr
	}
	return m.infoResp, nil
}

func (m *mockDatabaseSvc) SealDatabase(_ context.Context, _ string) error { return nil }

func (m *mockDatabaseSvc) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

func (m *mockDatabaseSvc) DatabaseBarrierStatus(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{}, nil
}

func (m *mockDatabaseSvc) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.Create(cfg)
}

func (m *mockDatabaseSvc) GetForTenant(_ context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}

func (m *mockDatabaseSvc) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}

func (m *mockDatabaseSvc) DropForTenant(_ context.Context, name string) error {
	return m.Drop(context.Background(), name)
}

func (m *mockDatabaseSvc) UpdateProtocolForTenant(_ context.Context, name string, p database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, p)
}

func (m *mockDatabaseSvc) UpdateEtcdForTenant(_ context.Context, name string, e *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, e)
}

func (m *mockDatabaseSvc) UpdateRaftForTenant(_ context.Context, name string, r *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, r)
}

func (m *mockDatabaseSvc) UpdateBarrierForTenant(_ context.Context, name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, b)
}

func (m *mockDatabaseSvc) UpdateConnectionForTenant(_ context.Context, name string, c *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, c)
}

func (m *mockDatabaseSvc) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

func connectDatabaseInProcess(t *testing.T, svc service.DatabaseServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithDatabaseService(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// CreateDatabase
// ---------------------------------------------------------------------------

func TestCreateDatabase_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "db1",
		Engine:  "kv",
		Backend: "pebble",
		DataDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if info.Name != "db1" {
		t.Errorf("name: %q", info.Name)
	}
}

func TestCreateDatabase_ServiceError(t *testing.T) {
	svc := newMockDatabaseSvc()
	svc.createErr = errors.New("already exists")
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name: "x", Engine: "kv", Backend: "pebble",
	}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetDatabase
// ---------------------------------------------------------------------------

func TestGetDatabase_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.GetDatabase(context.Background(), "db1")
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if info.Name != "db1" {
		t.Errorf("name: %q", info.Name)
	}
}

func TestGetDatabase_ServiceError(t *testing.T) {
	svc := newMockDatabaseSvc()
	svc.getErr = errors.New("not found")
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.GetDatabase(context.Background(), "missing"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListDatabases
// ---------------------------------------------------------------------------

func TestListDatabases_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	infos, err := tr.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("count: %d", len(infos))
	}
}

func TestListDatabases_ServiceError(t *testing.T) {
	svc := newMockDatabaseSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ListDatabases(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// DropDatabase
// ---------------------------------------------------------------------------

func TestDropDatabase_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	if err := tr.DropDatabase(context.Background(), "db1"); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
}

func TestDropDatabase_ServiceError(t *testing.T) {
	svc := newMockDatabaseSvc()
	svc.dropErr = errors.New("drop failed")
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	if err := tr.DropDatabase(context.Background(), "db1"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseProtocol
// ---------------------------------------------------------------------------

func TestUpdateDatabaseProtocol_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{
		Name:       "db1",
		MySQL:      true,
		PostgreSQL: false,
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseProtocol: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseEtcd
// ---------------------------------------------------------------------------

func TestUpdateDatabaseEtcd_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{
		Name:       "db1",
		Enabled:    true,
		ListenAddr: "127.0.0.1:2379",
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseEtcd: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseRaft
// ---------------------------------------------------------------------------

func TestUpdateDatabaseRaft_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: "db1",
		Raft: &transport.DatabaseRaftConfig{
			ShardID: 1,
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseBarrier
// ---------------------------------------------------------------------------

func TestUpdateDatabaseBarrier_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{
		Name: "db1",
		Barrier: &transport.DatabaseBarrierConfig{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseBarrier: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
}

// TestUpdateDatabaseBarrier_MetadataFields is the MEDIUM #9 proof for the
// MCP transport: Mode/SharesRequired/SharesTotal must survive the SDK ->
// MCP tool call -> service round trip alongside Enabled.
func TestUpdateDatabaseBarrier_MetadataFields(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	_, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{
		Name: "db1",
		Barrier: &transport.DatabaseBarrierConfig{
			Enabled:        true,
			Mode:           "passphrase",
			SharesRequired: 2,
			SharesTotal:    3,
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseBarrier: %v", err)
	}

	svc.mu.Lock()
	got := svc.lastUpdateBarrier
	svc.mu.Unlock()

	if got == nil {
		t.Fatal("service was not invoked with a barrier config")
	}
	if got.Mode != "passphrase" {
		t.Errorf("Mode = %q, want %q", got.Mode, "passphrase")
	}
	if got.SharesRequired != 2 {
		t.Errorf("SharesRequired = %d, want 2", got.SharesRequired)
	}
	if got.SharesTotal != 3 {
		t.Errorf("SharesTotal = %d, want 3", got.SharesTotal)
	}
}

// ---------------------------------------------------------------------------
// UpdateDatabaseConnection
// ---------------------------------------------------------------------------

func TestUpdateDatabaseConnection_Success(t *testing.T) {
	svc := newMockDatabaseSvc()
	tr, cleanup := connectDatabaseInProcess(t, svc)
	defer cleanup()

	info, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{
		Name: "db1",
		Connection: &transport.DatabaseConnectionConfig{
			Transport: "quic",
			Address:   "localhost:5000",
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseConnection: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
}
