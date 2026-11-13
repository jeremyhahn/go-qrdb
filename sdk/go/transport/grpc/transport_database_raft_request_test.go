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
	"log/slog"
	"net"
	"os"
	"testing"

	googlegrpc "google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Every field of the SDK's Raft request -- discovery blocks and their secrets
// included -- reaches the database over gRPC.
func TestGRPCDatabase_RaftRequestReachesTheService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	dbwiretest.ClaimShards(t, mgr)
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)

	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterDatabaseService(svc))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	tr, closeConn := connectDatabaseTransport(t, lis.Addr().String())
	defer closeConn()

	if _, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: dbwiretest.Name, Raft: dbwiretest.FullRaftRequest(),
	}); err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertRaftConfig(t, "gRPC", stored.Raft)
}
