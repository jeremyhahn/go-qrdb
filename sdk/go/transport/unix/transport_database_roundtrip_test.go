// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Round-trip tests for the Database service over the Unix domain socket
// transport. Each test wires a real gRPC-over-UDS server with a stub database
// service and asserts the SDK transport correctly marshals + unmarshals
// configuration objects across the wire.

package unix

import (
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockDatabaseSvc — service.DatabaseServicer stub
// ---------------------------------------------------------------------------

type mockDatabaseSvc struct {
	createFn        func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	getFn           func(name string) (*database.DatabaseInfo, error)
	listFn          func() ([]database.DatabaseInfo, error)
	dropFn          func(name string) error
	updateProto     func(name string, p database.ProtocolConfig) (*database.DatabaseInfo, error)
	updateEtcd      func(name string, e *database.EtcdConfig) (*database.DatabaseInfo, error)
	updateRaft      func(name string, r *database.DatabaseRaftConfig) (*database.DatabaseInfo, error)
	updateBarrier   func(name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error)
	updateConn      func(name string, c *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error)
	sealDBFn        func(ctx context.Context, name string) error
	unsealDBFn      func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

func (m *mockDatabaseSvc) Create(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
	if m.createFn != nil {
		return m.createFn(cfg)
	}
	return &database.DatabaseInfo{
		Name:    cfg.Name,
		Engine:  cfg.Engine,
		Backend: cfg.Backend,
		Status:  "ready",
		DataDir: cfg.DataDir,
		Mode:    cfg.Mode,
	}, nil
}

func (m *mockDatabaseSvc) Get(name string) (*database.DatabaseInfo, error) {
	if m.getFn != nil {
		return m.getFn(name)
	}
	return &database.DatabaseInfo{Name: name, Status: "ready"}, nil
}

func (m *mockDatabaseSvc) List() ([]database.DatabaseInfo, error) {
	if m.listFn != nil {
		return m.listFn()
	}
	return []database.DatabaseInfo{
		{Name: "db1", Status: "ready"},
		{Name: "db2", Status: "ready"},
	}, nil
}

func (m *mockDatabaseSvc) Drop(_ context.Context, name string) error {
	if m.dropFn != nil {
		return m.dropFn(name)
	}
	return nil
}

func (m *mockDatabaseSvc) UpdateProtocol(name string, p database.ProtocolConfig) (*database.DatabaseInfo, error) {
	if m.updateProto != nil {
		return m.updateProto(name, p)
	}
	return &database.DatabaseInfo{Name: name, Protocol: p}, nil
}

func (m *mockDatabaseSvc) UpdateEtcd(name string, e *database.EtcdConfig) (*database.DatabaseInfo, error) {
	if m.updateEtcd != nil {
		return m.updateEtcd(name, e)
	}
	return &database.DatabaseInfo{
		Name:     name,
		Protocol: database.ProtocolConfig{Etcd: e},
	}, nil
}

func (m *mockDatabaseSvc) UpdateRaft(name string, r *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	if m.updateRaft != nil {
		return m.updateRaft(name, r)
	}
	return &database.DatabaseInfo{Name: name, Raft: r}, nil
}

func (m *mockDatabaseSvc) UpdateBarrier(name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	if m.updateBarrier != nil {
		return m.updateBarrier(name, b)
	}
	return &database.DatabaseInfo{Name: name, Barrier: b}, nil
}

func (m *mockDatabaseSvc) UpdateConnection(name string, c *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	if m.updateConn != nil {
		return m.updateConn(name, c)
	}
	return &database.DatabaseInfo{Name: name, Connection: c}, nil
}

func (m *mockDatabaseSvc) SealDatabase(ctx context.Context, name string) error {
	if m.sealDBFn != nil {
		return m.sealDBFn(ctx, name)
	}
	return nil
}

func (m *mockDatabaseSvc) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	if m.unsealDBFn != nil {
		return m.unsealDBFn(ctx, name, creds)
	}
	return nil
}

func (m *mockDatabaseSvc) DatabaseBarrierStatus(_ context.Context, name string) (*seal.BarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(name)
	}
	return &seal.BarrierStatus{Sealed: false}, nil
}

// Tenant-scoped variants — not exercised by the Unix transport at the moment,
// but required to satisfy the DatabaseServicer interface.

func (m *mockDatabaseSvc) CreateForTenant(ctx context.Context, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
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

func (m *mockDatabaseSvc) UpdateSQL(name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, SQL: sqlCfg}, nil
}

func (m *mockDatabaseSvc) UpdateSQLForTenant(_ context.Context, name string, sqlCfg *database.SQLConfig) (*database.DatabaseInfo, error) {
	return m.UpdateSQL(name, sqlCfg)
}

func (m *mockDatabaseSvc) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

// ---------------------------------------------------------------------------
// Database round-trips
// ---------------------------------------------------------------------------

func TestUnixRT_CreateDatabase(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "db1",
		Engine:  "pebble",
		Backend: "raft",
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if resp.Name != "db1" || resp.Status != "ready" {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_GetDatabase(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.GetDatabase(context.Background(), "db1")
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if resp.Name != "db1" {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_ListDatabases(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(resp) != 2 {
		t.Errorf("got %d, want 2", len(resp))
	}
}

func TestUnixRT_DropDatabase(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	if err := tr.DropDatabase(context.Background(), "db1"); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
}

func TestUnixRT_UpdateDatabaseProtocol(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{
		Name:       "db1",
		MySQL:      true,
		PostgreSQL: false,
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseProtocol: %v", err)
	}
	if !resp.Protocol.MySQL {
		t.Errorf("got %+v, want MySQL=true", resp.Protocol)
	}
}

func TestUnixRT_UpdateDatabaseEtcd(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{
		Name:                "db1",
		Enabled:             true,
		ListenAddr:          ":2379",
		AdvertiseClientURLs: []string{"http://localhost:2379"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseEtcd: %v", err)
	}
	if resp.Protocol.Etcd == nil || !resp.Protocol.Etcd.Enabled {
		t.Errorf("got %+v, want Etcd.Enabled=true", resp.Protocol.Etcd)
	}
}

func TestUnixRT_UpdateDatabaseRaft(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: "db1",
		Raft: &transport.DatabaseRaftConfig{
			ClusterMode: "single",
			ShardID:     1,
			ReplicaID:   1,
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	if resp.Raft == nil || resp.Raft.ShardID != 1 {
		t.Errorf("got %+v, want Raft.ShardID=1", resp.Raft)
	}
}

func TestUnixRT_UpdateDatabaseBarrier(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{
		Name:    "db1",
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseBarrier: %v", err)
	}
	if resp.Barrier == nil || !resp.Barrier.Enabled {
		t.Errorf("got %+v, want Barrier.Enabled=true", resp.Barrier)
	}
}

// TestUnixRT_UpdateDatabaseBarrier_MetadataFields is the MEDIUM #9 proof for
// the Unix transport: Mode/SharesRequired/SharesTotal must survive the
// gRPC-over-UDS round trip alongside Enabled, not be silently dropped.
func TestUnixRT_UpdateDatabaseBarrier_MetadataFields(t *testing.T) {
	var got *database.DatabaseBarrierConfig
	svc := &mockDatabaseSvc{
		updateBarrier: func(name string, b *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
			got = b
			return &database.DatabaseInfo{Name: name, Barrier: b}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{
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
	if got == nil {
		t.Fatal("server-side UpdateBarrier was not invoked with a barrier config")
	}
	if got.Mode != "passphrase" || got.SharesRequired != 2 || got.SharesTotal != 3 {
		t.Errorf("server received %+v, want Mode=passphrase SharesRequired=2 SharesTotal=3", got)
	}
	if resp.Barrier == nil || resp.Barrier.Mode != "passphrase" ||
		resp.Barrier.SharesRequired != 2 || resp.Barrier.SharesTotal != 3 {
		t.Errorf("response Barrier = %+v, want Mode=passphrase SharesRequired=2 SharesTotal=3", resp.Barrier)
	}
}

func TestUnixRT_UpdateDatabaseConnection(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{
		Name: "db1",
		Connection: &transport.DatabaseConnectionConfig{
			Transport: "tcp",
			Address:   "10.0.0.1:5000",
		},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseConnection: %v", err)
	}
	if resp.Connection == nil || resp.Connection.Address != "10.0.0.1:5000" {
		t.Errorf("got %+v, want address=10.0.0.1:5000", resp.Connection)
	}
}

func TestUnixRT_CreateDatabase_PropagatesCfg(t *testing.T) {
	svc := &mockDatabaseSvc{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			if cfg.Name != "main" || string(cfg.Engine) != "pebble" || string(cfg.Backend) != "raft" {
				t.Errorf("server got cfg=%+v", cfg)
			}
			return &database.DatabaseInfo{
				Name:    cfg.Name,
				Engine:  cfg.Engine,
				Backend: cfg.Backend,
				Status:  "ready",
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "main",
		Engine:  "pebble",
		Backend: "raft",
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if resp.Name != "main" {
		t.Errorf("got %+v", resp)
	}
}

func TestUnixRT_CreateDatabase_ServiceError(t *testing.T) {
	svc := &mockDatabaseSvc{
		createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return nil, errors.New("create failed")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	_, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{Name: "x"})
	if err == nil {
		t.Fatal("expected error from server")
	}
}
