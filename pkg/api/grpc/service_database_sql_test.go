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
	"os"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

func newRealDatabaseService(t *testing.T) service.DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return servicetest.NewDatabaseService(t, mgr)
}

func TestGRPC_DatabaseSQL_CreateAndUpdate(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithDatabaseService(newRealDatabaseService(t)))
	defer cleanup()
	ctx := context.Background()

	created, err := client.DatabaseCreate(ctx, &pb.DatabaseCreateRequest{
		Name: "shop", Backend: "memory",
		Sql: &pb.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("DatabaseCreate: %v", err)
	}
	if created.GetSql().GetConcurrency() != "pessimistic" {
		t.Fatalf("created sql = %v, want pessimistic", created.GetSql())
	}

	updated, err := client.DatabaseUpdateSQL(ctx, &pb.DatabaseUpdateSQLRequest{
		Name: "shop", Sql: &pb.DatabaseSQLConfig{Concurrency: "optimistic"},
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateSQL: %v", err)
	}
	if updated.GetSql().GetConcurrency() != "optimistic" {
		t.Fatalf("updated sql = %v, want optimistic", updated.GetSql())
	}

	reset, err := client.DatabaseUpdateSQL(ctx, &pb.DatabaseUpdateSQLRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("DatabaseUpdateSQL(reset): %v", err)
	}
	if reset.GetSql() != nil {
		t.Fatalf("reset sql = %v, want unset", reset.GetSql())
	}
}

func TestGRPC_DatabaseSQL_Refusals(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithDatabaseService(newRealDatabaseService(t)))
	defer cleanup()
	ctx := context.Background()

	if _, err := client.DatabaseCreate(ctx, &pb.DatabaseCreateRequest{Name: "shop", Backend: "memory"}); err != nil {
		t.Fatalf("DatabaseCreate: %v", err)
	}
	cases := []struct {
		req  *pb.DatabaseUpdateSQLRequest
		want codes.Code
	}{
		{&pb.DatabaseUpdateSQLRequest{Name: "shop", Sql: &pb.DatabaseSQLConfig{Concurrency: "eager"}}, codes.InvalidArgument},
		{&pb.DatabaseUpdateSQLRequest{Name: "missing", Sql: &pb.DatabaseSQLConfig{Concurrency: "pessimistic"}}, codes.NotFound},
	}
	for _, c := range cases {
		_, err := client.DatabaseUpdateSQL(ctx, c.req)
		if st, _ := status.FromError(err); st.Code() != c.want {
			t.Errorf("DatabaseUpdateSQL(%v) code = %s, want %s", c.req, st.Code(), c.want)
		}
	}
}

func TestGRPC_DatabaseSQL_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	_, err := client.DatabaseUpdateSQL(context.Background(), &pb.DatabaseUpdateSQLRequest{Name: "db"})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}
