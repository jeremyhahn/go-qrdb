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

package grpc

import (
	"context"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// fakeDatabaseServicer
// ===========================================================================

type fakeDatabaseServicer struct{}

func (f *fakeDatabaseServicer) Create(_ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb", Engine: "mvcc", Backend: "pebble"}, nil
}
func (f *fakeDatabaseServicer) Get(_ string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) List() ([]database.DatabaseInfo, error) {
	return []database.DatabaseInfo{{Name: "testdb"}}, nil
}
func (f *fakeDatabaseServicer) Drop(_ string) error { return nil }
func (f *fakeDatabaseServicer) UpdateProtocol(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateRaft(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateBarrier(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateConnection(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) SealDatabase(_ context.Context, _ string) error { return nil }
func (f *fakeDatabaseServicer) UnsealDatabase(_ context.Context, _ string, _ seal.Credentials) error {
	return nil
}
func (f *fakeDatabaseServicer) DatabaseBarrierStatus(_ context.Context, _ string) (*seal.BarrierStatus, error) {
	return nil, nil
}
func (f *fakeDatabaseServicer) CreateForTenant(_ context.Context, _ database.DatabaseConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) ListForTenant(_ context.Context) ([]database.DatabaseInfo, error) {
	return []database.DatabaseInfo{{Name: "testdb"}}, nil
}
func (f *fakeDatabaseServicer) DropForTenant(_ context.Context, _ string) error { return nil }
func (f *fakeDatabaseServicer) UpdateProtocolForTenant(_ context.Context, _ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateEtcdForTenant(_ context.Context, _ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateRaftForTenant(_ context.Context, _ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateBarrierForTenant(_ context.Context, _ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) UpdateConnectionForTenant(_ context.Context, _ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: "testdb"}, nil
}
func (f *fakeDatabaseServicer) ScaleForTenant(_ context.Context, name string, targetReplicas int) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{Name: name, Replicas: targetReplicas}, nil
}

// ===========================================================================
// Server setup
// ===========================================================================

func startDatabaseServer(t *testing.T) (string, func()) {
	t.Helper()
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterDatabaseService(&fakeDatabaseServicer{}))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func connectDatabaseTransport(t *testing.T, addr string) (*Transport, func()) {
	t.Helper()
	conn, err := googlegrpc.NewClient(addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	tr := &Transport{
		config: &transport.Config{Address: addr},
		conn:   conn,
		client: pb.NewQRDBServiceClient(conn),
	}
	return tr, func() { _ = conn.Close() }
}

func TestGRPCDatabase_AllMethods_Success(t *testing.T) {
	addr, stop := startDatabaseServer(t)
	defer stop()
	tr, cleanup := connectDatabaseTransport(t, addr)
	defer cleanup()
	ctx := context.Background()

	t.Run("CreateDatabase", func(t *testing.T) {
		if _, err := tr.CreateDatabase(ctx, transport.CreateDatabaseRequest{Name: "testdb"}); err != nil {
			t.Fatalf("CreateDatabase: %v", err)
		}
	})
	t.Run("GetDatabase", func(t *testing.T) {
		if _, err := tr.GetDatabase(ctx, "testdb"); err != nil {
			t.Fatalf("GetDatabase: %v", err)
		}
	})
	t.Run("ListDatabases", func(t *testing.T) {
		if _, err := tr.ListDatabases(ctx); err != nil {
			t.Fatalf("ListDatabases: %v", err)
		}
	})
	t.Run("DropDatabase", func(t *testing.T) {
		if err := tr.DropDatabase(ctx, "testdb"); err != nil {
			t.Fatalf("DropDatabase: %v", err)
		}
	})
	t.Run("UpdateDatabaseProtocol", func(t *testing.T) {
		if _, err := tr.UpdateDatabaseProtocol(ctx, transport.UpdateDatabaseProtocolRequest{Name: "testdb"}); err != nil {
			t.Fatalf("UpdateDatabaseProtocol: %v", err)
		}
	})
	t.Run("UpdateDatabaseEtcd", func(t *testing.T) {
		if _, err := tr.UpdateDatabaseEtcd(ctx, transport.UpdateDatabaseEtcdRequest{Name: "testdb"}); err != nil {
			t.Fatalf("UpdateDatabaseEtcd: %v", err)
		}
	})
	t.Run("UpdateDatabaseRaft", func(t *testing.T) {
		if _, err := tr.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{Name: "testdb"}); err != nil {
			t.Fatalf("UpdateDatabaseRaft: %v", err)
		}
	})
	t.Run("UpdateDatabaseBarrier", func(t *testing.T) {
		if _, err := tr.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{Name: "testdb"}); err != nil {
			t.Fatalf("UpdateDatabaseBarrier: %v", err)
		}
	})
	t.Run("UpdateDatabaseConnection", func(t *testing.T) {
		if _, err := tr.UpdateDatabaseConnection(ctx, transport.UpdateDatabaseConnectionRequest{Name: "testdb"}); err != nil {
			t.Fatalf("UpdateDatabaseConnection: %v", err)
		}
	})
}
