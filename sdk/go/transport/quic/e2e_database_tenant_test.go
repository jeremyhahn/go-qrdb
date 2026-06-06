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

package quic

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Database (JSON wire)
// ---------------------------------------------------------------------------

func TestE2E_CreateDatabase_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Create", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db", Engine: "pebble", Backend: "local", Status: "open"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{Name: "db"})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if resp.Name != "db" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetDatabase_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Get", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db", Status: "open"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.GetDatabase(context.Background(), "db")
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if resp.Name != "db" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ListDatabases_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.List", func(_ []byte) (uint16, []byte) {
		infos := []transport.DatabaseInfo{
			{Name: "a"}, {Name: "b"},
		}
		b, _ := json.Marshal(infos)
		return wire.StatusOK, b
	})
	resp, err := tr.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(resp) != 2 {
		t.Fatalf("resp = %v", resp)
	}
}

func TestE2E_DropDatabase_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Drop", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DropDatabase(context.Background(), "db"); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}
}

func TestE2E_UpdateDatabaseProtocol_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateProtocol", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseProtocol(context.Background(), transport.UpdateDatabaseProtocolRequest{
		Name: "db", MySQL: true,
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseProtocol: %v", err)
	}
	if resp.Name != "db" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpdateDatabaseEtcd_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateEtcd", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseEtcd(context.Background(), transport.UpdateDatabaseEtcdRequest{
		Name: "db", Enabled: true, ListenAddr: ":2379",
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseEtcd: %v", err)
	}
	if resp == nil {
		t.Fatalf("nil resp")
	}
}

func TestE2E_UpdateDatabaseRaft_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateRaft", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: "db", Raft: &transport.DatabaseRaftConfig{ClusterMode: "single"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	if resp == nil {
		t.Fatalf("nil resp")
	}
}

func TestE2E_UpdateDatabaseBarrier_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateBarrier", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseBarrier(context.Background(), transport.UpdateDatabaseBarrierRequest{
		Name: "db", Barrier: &transport.DatabaseBarrierConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseBarrier: %v", err)
	}
	if resp == nil {
		t.Fatalf("nil resp")
	}
}

func TestE2E_UpdateDatabaseConnection_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.UpdateConnection", func(_ []byte) (uint16, []byte) {
		info := transport.DatabaseInfo{Name: "db"}
		b, _ := json.Marshal(info)
		return wire.StatusOK, b
	})
	resp, err := tr.UpdateDatabaseConnection(context.Background(), transport.UpdateDatabaseConnectionRequest{
		Name: "db", Connection: &transport.DatabaseConnectionConfig{Transport: "quic", Address: "host:1"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseConnection: %v", err)
	}
	if resp == nil {
		t.Fatalf("nil resp")
	}
}

// ---------------------------------------------------------------------------
// Tenant (proto + JSON for barrier methods)
// ---------------------------------------------------------------------------

func TestE2E_CreateTenant_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TenantResponse{
			Id: "t1", Name: "tenant-1", IsolationType: "isolated",
		})
	})
	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID: "t1", Name: "tenant-1", IsolationType: transport.TenantIsolationBarrier,
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if resp.Tenant.ID != "t1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetTenant_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TenantResponse{Id: "t1", Name: "tenant-1"})
	})
	resp, err := tr.GetTenant(context.Background(), "t1")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if resp.Tenant.ID != "t1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_ListTenants_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TenantListResponse{
			Tenants: []*pb.TenantResponse{{Id: "t1"}, {Id: "t2"}},
		})
	})
	resp, err := tr.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(resp.Tenants) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DeleteTenant_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Delete", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DeleteTenant(context.Background(), "t1"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
}

func TestE2E_UpdateTenant_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.Update", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TenantResponse{Id: "t1", Name: "renamed"})
	})
	resp, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID: "t1", Name: "renamed",
	})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if resp.Tenant.Name != "renamed" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpdateTenantQuotas_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.UpdateQuotas", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TenantResponse{
			Id: "t1", Quotas: &pb.TenantQuotas{MaxDatabases: 10},
		})
	})
	resp, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{
		TenantID: "t1", Quotas: transport.TenantQuotas{MaxDatabases: 10},
	})
	if err != nil {
		t.Fatalf("UpdateTenantQuotas: %v", err)
	}
	if resp.Tenant.Quotas == nil || resp.Tenant.Quotas.MaxDatabases != 10 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_TenantBarrierInit_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.BarrierInit", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID: "t1", Threshold: 2, Shares: 3,
	}); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
}

func TestE2E_TenantBarrierUnseal_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.BarrierUnseal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "t1", Share: []byte("s"),
	}); err != nil {
		t.Fatalf("TenantBarrierUnseal: %v", err)
	}
}

func TestE2E_TenantBarrierSeal_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.BarrierSeal", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "t1"}); err != nil {
		t.Fatalf("TenantBarrierSeal: %v", err)
	}
}

func TestE2E_TenantBarrierStatus_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.BarrierStatus", func(_ []byte) (uint16, []byte) {
		resp := quicTenantBarrierStatusResponse{
			TenantID: "t1", Sealed: false, Initialized: true, Strategy: "shamir", HardwareBacked: false,
		}
		b, _ := json.Marshal(resp)
		return wire.StatusOK, b
	})
	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if !resp.Initialized || resp.Strategy != "shamir" {
		t.Fatalf("resp = %+v", resp)
	}
}
