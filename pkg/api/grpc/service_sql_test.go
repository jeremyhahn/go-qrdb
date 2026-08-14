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
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// startSQLTestServer creates a gRPC server on a random port with the given
// options and returns a connected client and a cleanup function. Defined
// locally (rather than reusing grpc_coverage_test.go's
// startTestServerWithAll) because that helper lives in a build-tag-gated
// file and this test must build under plain tags too.
func startSQLTestServer(t *testing.T, opts ...ServerOption) (pb.QRDBServiceClient, func()) {
	t.Helper()

	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", append([]ServerOption{WithTLS(tlsCfg)}, opts...)...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}
	return pb.NewQRDBServiceClient(conn), cleanup
}

// mockSQLService is a minimal service.SQLServicer test double.
type mockSQLService struct {
	result          *service.SQLQueryResult
	err             error
	gotDB, gotQuery string
}

func (m *mockSQLService) Execute(_ context.Context, database, query string) (*service.SQLQueryResult, error) {
	m.gotDB, m.gotQuery = database, query
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		return m.result, nil
	}
	return &service.SQLQueryResult{
		Columns:  []string{"val"},
		Rows:     []map[string]any{{"val": "1"}},
		RowCount: 1,
		Duration: "1ms",
	}, nil
}

func TestSQLQuery_Success(t *testing.T) {
	mock := &mockSQLService{}
	client, cleanup := startSQLTestServer(t, WithSQLService(mock))
	defer cleanup()

	resp, err := client.SQLQuery(context.Background(), &pb.SQLQueryRequest{
		Query:    "SELECT 1 AS val",
		Database: "testdb",
	})
	if err != nil {
		t.Fatalf("SQLQuery failed: %v", err)
	}
	if len(resp.GetColumns()) != 1 || resp.GetColumns()[0] != "val" {
		t.Fatalf("unexpected columns: %v", resp.GetColumns())
	}
	if resp.GetRowCount() != 1 {
		t.Fatalf("rowCount = %d, want 1", resp.GetRowCount())
	}
	if len(resp.GetRows()) != 1 || resp.GetRows()[0].GetValues()["val"] != "1" {
		t.Fatalf("unexpected rows: %v", resp.GetRows())
	}
	if mock.gotDB != "testdb" || mock.gotQuery != "SELECT 1 AS val" {
		t.Fatalf("service not invoked with expected args: db=%q query=%q", mock.gotDB, mock.gotQuery)
	}
}

func TestSQLQuery_NotConfigured(t *testing.T) {
	client, cleanup := startSQLTestServer(t)
	defer cleanup()

	_, err := client.SQLQuery(context.Background(), &pb.SQLQueryRequest{Query: "SELECT 1", Database: "db"})
	if err == nil {
		t.Fatal("expected error when sql service is not configured")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", status.Code(err))
	}
}

func TestSQLQuery_EmptyQuery(t *testing.T) {
	mock := &mockSQLService{}
	client, cleanup := startSQLTestServer(t, WithSQLService(mock))
	defer cleanup()

	_, err := client.SQLQuery(context.Background(), &pb.SQLQueryRequest{Query: "", Database: "db"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument for empty query, got %v", status.Code(err))
	}
}

func TestSQLQuery_EmptyDatabase(t *testing.T) {
	mock := &mockSQLService{}
	client, cleanup := startSQLTestServer(t, WithSQLService(mock))
	defer cleanup()

	_, err := client.SQLQuery(context.Background(), &pb.SQLQueryRequest{Query: "SELECT 1", Database: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument for empty database, got %v", status.Code(err))
	}
}

func TestSQLQuery_ServiceError(t *testing.T) {
	mock := &mockSQLService{err: &service.SQLExecutionError{Query: "bad", Err: context.DeadlineExceeded}}
	client, cleanup := startSQLTestServer(t, WithSQLService(mock))
	defer cleanup()

	_, err := client.SQLQuery(context.Background(), &pb.SQLQueryRequest{Query: "bad", Database: "db"})
	if err == nil {
		t.Fatal("expected error from failing SQL execution")
	}
}
