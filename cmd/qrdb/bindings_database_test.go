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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock database service for binding tests
// ---------------------------------------------------------------------------

// bindingDatabaseService implements service.DatabaseServicer with
// configurable function fields for per-test behavior injection.
type bindingDatabaseService struct {
	createFn         func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn            func(name string) (*database.DatabaseInfo, error)
	listFn           func() ([]database.DatabaseInfo, error)
	dropFn           func(name string) error
	updateProtocolFn func(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateRaftFn     func(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrierFn  func(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConnFn     func(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
	updateEtcdFn     func(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error)
	scaleFn          func(name string, targetReplicas int) (*database.DatabaseInfo, error)
}

// Compile-time interface check.
var _ service.DatabaseServicer = (*bindingDatabaseService)(nil)

func (m *bindingDatabaseService) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createFn(cfg)
}

func (m *bindingDatabaseService) Get(name string) (*database.DatabaseInfo, error) {
	return m.getFn(name)
}

func (m *bindingDatabaseService) List() ([]database.DatabaseInfo, error) {
	return m.listFn()
}

func (m *bindingDatabaseService) Drop(_ context.Context, name string) error {
	return m.dropFn(name)
}

func (m *bindingDatabaseService) UpdateProtocol(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProtocolFn != nil {
		return m.updateProtocolFn(name, proto)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *bindingDatabaseService) UpdateEtcd(name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcdFn != nil {
		return m.updateEtcdFn(name, cfg)
	}
	return nil, &database.NotFoundError{Name: name}
}

func (m *bindingDatabaseService) UpdateRaft(name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaftFn != nil {
		return m.updateRaftFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *bindingDatabaseService) UpdateBarrier(name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrierFn != nil {
		return m.updateBarrierFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *bindingDatabaseService) UpdateConnection(name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConnFn != nil {
		return m.updateConnFn(name, cfg)
	}
	return &database.DatabaseInfo{Name: name}, nil
}

func (m *bindingDatabaseService) SealDatabase(_ context.Context, _ string) error {
	return nil
}

func (m *bindingDatabaseService) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (m *bindingDatabaseService) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := m.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (m *bindingDatabaseService) databaseBarrierStatusSeal(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return &seal.BarrierStatus{Sealed: false}, nil
}

// CreateForTenant creates as Create does: the binding runs with no tenant in
// its context, and DatabaseService.CreateForTenant takes the system tenant's
// create path for such a caller.
func (m *bindingDatabaseService) CreateForTenant(_ context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return m.createFn(cfg)
}

// GetForTenant is Get: the service's is owner-checked, and every database
// this double serves is the system tenant's own, which every call here runs
// as, so the check would pass.
func (m *bindingDatabaseService) GetForTenant(_ context.Context, name string) (*database.DatabaseInfo, error) {
	return m.Get(name)
}

func (m *bindingDatabaseService) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return m.List()
}

func (m *bindingDatabaseService) DropForTenant(ctx context.Context, name string) error {
	return m.Drop(ctx, name)
}

func (m *bindingDatabaseService) UpdateProtocolForTenant(_ context.Context, name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return m.UpdateProtocol(name, proto)
}

func (m *bindingDatabaseService) UpdateEtcdForTenant(_ context.Context, name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, cfg)
}

func (m *bindingDatabaseService) UpdateRaftForTenant(_ context.Context, name string, cfg *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return m.UpdateRaft(name, cfg)
}

func (m *bindingDatabaseService) UpdateBarrierForTenant(_ context.Context, name string, cfg *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return m.UpdateBarrier(name, cfg)
}

func (m *bindingDatabaseService) UpdateConnectionForTenant(_ context.Context, name string, cfg *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return m.UpdateConnection(name, cfg)
}

func (m *bindingDatabaseService) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *bindingDatabaseService) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *bindingDatabaseService) UpdateCompactionForTenant(_ context.Context, name string, policy *compaction.Policy) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Compaction: policy}, nil
}

func (m *bindingDatabaseService) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	if m.scaleFn != nil {
		return m.scaleFn(name, targetReplicas)
	}
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

// newDatabaseTestConn creates a ConnectionBinding wired to an embedded
// transport with the given mock database service.
func newDatabaseTestConn(t *testing.T, dbSvc service.DatabaseServicer) *ConnectionBinding {
	t.Helper()
	return newTestConnectionBinding(
		t,
		newMockKVService(),
		newMockAdminService(),
		embedded.WithDatabaseService(dbSvc),
	)
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_Create_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				Status:  "open",
			}, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.Create(&transport.CreateDatabaseRequest{
		Name:    "metrics",
		Engine:  "timeseries",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil DatabaseInfo")
	}
	if info.Name != "metrics" {
		t.Errorf("Name = %q, want %q", info.Name, "metrics")
	}
	if info.Engine != "timeseries" {
		t.Errorf("Engine = %q, want %q", info.Engine, "timeseries")
	}
	if info.Backend != "pebble" {
		t.Errorf("Backend = %q, want %q", info.Backend, "pebble")
	}
	if info.Status != "open" {
		t.Errorf("Status = %q, want %q", info.Status, "open")
	}
}

func TestDatabaseBinding_Create_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.Create(&transport.CreateDatabaseRequest{
		Name:   "testdb",
		Engine: "kv",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_Create_ServiceError(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return nil, &database.AlreadyExistsError{Name: "dup"}
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.Create(&transport.CreateDatabaseRequest{
		Name:   "dup",
		Engine: "kv",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var aee *database.AlreadyExistsError
	if !errors.As(err, &aee) {
		t.Fatalf("expected *AlreadyExistsError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_Get_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    name,
				Engine:  "kv",
				Backend: "pebble",
				Status:  "open",
				DataDir: "/data/default",
				ShardID: 42,
			}, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.Get("default")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil DatabaseInfo")
	}
	if info.Name != "default" {
		t.Errorf("Name = %q, want %q", info.Name, "default")
	}
	if info.Engine != "kv" {
		t.Errorf("Engine = %q, want %q", info.Engine, "kv")
	}
	if info.DataDir != "/data/default" {
		t.Errorf("DataDir = %q, want %q", info.DataDir, "/data/default")
	}
	if info.ShardID != 42 {
		t.Errorf("ShardID = %d, want %d", info.ShardID, 42)
	}
}

func TestDatabaseBinding_Get_NotFound(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return nil, &database.NotFoundError{Name: name}
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nfe *database.NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *NotFoundError, got %T: %v", err, err)
	}
	if nfe.Name != "nonexistent" {
		t.Errorf("NotFoundError.Name = %q, want %q", nfe.Name, "nonexistent")
	}
}

func TestDatabaseBinding_Get_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.Get("testdb")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_List_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return []database.DatabaseInfo{
				{Name: "default", Engine: "kv", Backend: "pebble", Status: "open"},
				{Name: "metrics", Engine: "timeseries", Backend: "pebble", Status: "open"},
			}, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	infos, err := b.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2", len(infos))
	}
	if infos[0].Name != "default" {
		t.Errorf("infos[0].Name = %q, want %q", infos[0].Name, "default")
	}
	if infos[1].Name != "metrics" {
		t.Errorf("infos[1].Name = %q, want %q", infos[1].Name, "metrics")
	}
}

func TestDatabaseBinding_List_Empty(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return nil, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	infos, err := b.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("len(infos) = %d, want 0", len(infos))
	}
}

func TestDatabaseBinding_List_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.List()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_List_ServiceError(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return nil, &database.MetaReadError{Path: "/data/meta", Err: errors.New("disk failure")}
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.List()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var mre *database.MetaReadError
	if !errors.As(err, &mre) {
		t.Fatalf("expected *MetaReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Drop tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_Drop_Success(t *testing.T) {
	var droppedName string
	dbSvc := &bindingDatabaseService{
		dropFn: func(name string) error {
			droppedName = name
			return nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	if err := b.Drop("metrics"); err != nil {
		t.Fatalf("Drop() error: %v", err)
	}
	if droppedName != "metrics" {
		t.Errorf("dropped name = %q, want %q", droppedName, "metrics")
	}
}

func TestDatabaseBinding_Drop_NotFound(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		dropFn: func(name string) error {
			return &database.NotFoundError{Name: name}
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	err := b.Drop("nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nfe *database.NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *NotFoundError, got %T: %v", err, err)
	}
	if nfe.Name != "nonexistent" {
		t.Errorf("NotFoundError.Name = %q, want %q", nfe.Name, "nonexistent")
	}
}

func TestDatabaseBinding_Drop_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	err := b.Drop("testdb")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Constructor test
// ---------------------------------------------------------------------------

func TestNewDatabaseBinding(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewDatabaseBinding(log, conn)
	if b == nil {
		t.Fatal("expected non-nil DatabaseBinding")
	}
	if b.logger != log {
		t.Error("expected logger to be set")
	}
	if b.conn != conn {
		t.Error("expected conn to be set")
	}
}

// ---------------------------------------------------------------------------
// UpdateProtocol tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_UpdateProtocol_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		updateProtocolFn: func(name string, proto database.ProtocolConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:   name,
				Engine: "kv",
				Status: "open",
			}, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.UpdateProtocol("default", &transport.UpdateDatabaseProtocolRequest{
		MySQL:      true,
		PostgreSQL: false,
	})
	if err != nil {
		t.Fatalf("UpdateProtocol() error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil DatabaseInfo")
	}
	if info.Name != "default" {
		t.Errorf("Name = %q, want %q", info.Name, "default")
	}
}

func TestDatabaseBinding_UpdateProtocol_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.UpdateProtocol("db", &transport.UpdateDatabaseProtocolRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_UpdateProtocol_SetsName(t *testing.T) {
	var capturedName string
	dbSvc := &bindingDatabaseService{
		updateProtocolFn: func(name string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
			capturedName = name
			return &database.DatabaseInfo{Name: name}, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	req := &transport.UpdateDatabaseProtocolRequest{}
	if _, err := b.UpdateProtocol("target-db", req); err != nil {
		t.Fatalf("UpdateProtocol() error: %v", err)
	}
	if capturedName != "target-db" {
		t.Errorf("expected name %q, got %q", "target-db", capturedName)
	}
	if req.Name != "target-db" {
		t.Errorf("expected req.Name to be set to %q, got %q", "target-db", req.Name)
	}
}

// ---------------------------------------------------------------------------
// UpdateRaft tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_UpdateRaft_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.UpdateRaft("db", &transport.UpdateDatabaseRaftRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_UpdateRaft_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		updateRaftFn: func(name string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name, Engine: "mvcc"}, nil
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.UpdateRaft("db", &transport.UpdateDatabaseRaftRequest{
		Raft: &transport.DatabaseRaftConfig{ReplicaID: 1},
	})
	if err != nil {
		t.Fatalf("UpdateRaft() unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil DatabaseInfo")
	}
	if info.Name != "db" {
		t.Errorf("Name = %q, want %q", info.Name, "db")
	}
}

func TestDatabaseBinding_UpdateRaft_SetsName(t *testing.T) {
	dbSvc := &bindingDatabaseService{}
	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	req := &transport.UpdateDatabaseRaftRequest{}
	if _, err := b.UpdateRaft("mydb", req); err != nil {
		t.Fatalf("UpdateRaft(): %v", err)
	}
	if req.Name != "mydb" {
		t.Errorf("expected req.Name=%q, got %q", "mydb", req.Name)
	}
}

// ---------------------------------------------------------------------------
// UpdateBarrier tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_UpdateBarrier_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.UpdateBarrier("db", &transport.UpdateDatabaseBarrierRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_UpdateBarrier_SetsName(t *testing.T) {
	dbSvc := &bindingDatabaseService{}
	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	req := &transport.UpdateDatabaseBarrierRequest{}
	if _, err := b.UpdateBarrier("barrierdb", req); err != nil {
		t.Fatalf("UpdateBarrier(): %v", err)
	}
	if req.Name != "barrierdb" {
		t.Errorf("expected req.Name=%q, got %q", "barrierdb", req.Name)
	}
}

func TestDatabaseBinding_UpdateBarrier_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{}
	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.UpdateBarrier("db", &transport.UpdateDatabaseBarrierRequest{})
	if err != nil {
		t.Fatalf("UpdateBarrier() unexpected error: %v", err)
	}
	_ = info
}

// ---------------------------------------------------------------------------
// UpdateEtcd tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_UpdateEtcd_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.UpdateEtcd("db", &transport.UpdateDatabaseEtcdRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_UpdateEtcd_ServiceError(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		updateEtcdFn: func(name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
			return nil, &database.NotFoundError{Name: name}
		},
	}

	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.UpdateEtcd("missing", &transport.UpdateDatabaseEtcdRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nfe *database.NotFoundError
	if !errors.As(err, &nfe) {
		t.Fatalf("expected *NotFoundError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_UpdateEtcd_SetsName(t *testing.T) {
	dbSvc := &bindingDatabaseService{
		updateEtcdFn: func(name string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	req := &transport.UpdateDatabaseEtcdRequest{}
	if _, err := b.UpdateEtcd("etcddb", req); err != nil {
		t.Fatalf("UpdateEtcd(): %v", err)
	}
	if req.Name != "etcddb" {
		t.Errorf("expected req.Name=%q, got %q", "etcddb", req.Name)
	}
}

// ---------------------------------------------------------------------------
// UpdateConnection tests
// ---------------------------------------------------------------------------

func TestDatabaseBinding_UpdateConnection_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewDatabaseBinding(testLogger(t), conn)

	_, err := b.UpdateConnection("db", &transport.UpdateDatabaseConnectionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}

func TestDatabaseBinding_UpdateConnection_SetsName(t *testing.T) {
	dbSvc := &bindingDatabaseService{}
	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	req := &transport.UpdateDatabaseConnectionRequest{}
	if _, err := b.UpdateConnection("conndb", req); err != nil {
		t.Fatalf("UpdateConnection(): %v", err)
	}
	if req.Name != "conndb" {
		t.Errorf("expected req.Name=%q, got %q", "conndb", req.Name)
	}
}

func TestDatabaseBinding_UpdateConnection_Success(t *testing.T) {
	dbSvc := &bindingDatabaseService{}
	conn := newDatabaseTestConn(t, dbSvc)
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.UpdateConnection("db", &transport.UpdateDatabaseConnectionRequest{})
	if err != nil {
		t.Fatalf("UpdateConnection() unexpected error: %v", err)
	}
	_ = info
}
