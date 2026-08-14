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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sqlServiceMock is a minimal service.SQLServicer test double. Deliberately
// self-contained (not defined in a build-tag-gated mocks file) so these
// tests compile and run under both plain and full tag sets.
type sqlServiceMock struct {
	result          *service.SQLQueryResult
	err             error
	gotDB, gotQuery string
}

func (m *sqlServiceMock) Execute(_ context.Context, database, query string) (*service.SQLQueryResult, error) {
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

func TestHandleSQLQuery_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.SQLQueryRequest{Query: "SELECT 1", Database: "db"})

	_, err := srv.handleSQLQuery(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleSQLQuery_Success(t *testing.T) {
	mock := &sqlServiceMock{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSQLService(mock))

	payload, err := proto.Marshal(&pb.SQLQueryRequest{Query: "SELECT 1 AS val", Database: "testdb"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	respBytes, err := srv.handleSQLQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp pb.SQLQueryResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.GetColumns()) != 1 || resp.GetColumns()[0] != "val" {
		t.Fatalf("unexpected columns: %v", resp.GetColumns())
	}
	if resp.GetRowCount() != 1 {
		t.Fatalf("rowCount = %d, want 1", resp.GetRowCount())
	}
	if mock.gotDB != "testdb" || mock.gotQuery != "SELECT 1 AS val" {
		t.Fatalf("service not invoked with expected args: db=%q query=%q", mock.gotDB, mock.gotQuery)
	}
}

func TestHandleSQLQuery_EmptyQuery(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSQLService(&sqlServiceMock{}))

	payload, _ := proto.Marshal(&pb.SQLQueryRequest{Query: "", Database: "db"})
	_, err := srv.handleSQLQuery(context.Background(), payload)

	var emptyErr *service.SQLEmptyQueryError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("expected *service.SQLEmptyQueryError, got %T: %v", err, err)
	}
}

func TestHandleSQLQuery_EmptyDatabase(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSQLService(&sqlServiceMock{}))

	payload, _ := proto.Marshal(&pb.SQLQueryRequest{Query: "SELECT 1", Database: ""})
	_, err := srv.handleSQLQuery(context.Background(), payload)

	var emptyErr *service.SQLEmptyDatabaseError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("expected *service.SQLEmptyDatabaseError, got %T: %v", err, err)
	}
}

func TestHandleSQLQuery_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSQLService(&sqlServiceMock{}))

	_, err := srv.handleSQLQuery(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleSQLQuery_DispatchRegistered(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSQLService(&sqlServiceMock{}))
	if _, ok := srv.handlers["sql.Query"]; !ok {
		t.Fatal("expected sql.Query to be registered in the dispatch table")
	}
}
