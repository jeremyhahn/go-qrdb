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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestGRPCDatabase_UpdateDatabaseSQL_RoundTrip(t *testing.T) {
	addr, stop := startDatabaseServer(t)
	defer stop()
	tr, cleanup := connectDatabaseTransport(t, addr)
	defer cleanup()

	info, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{
		Name: "testdb", SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL: %v", err)
	}
	if info.SQL == nil || info.SQL.Concurrency != "pessimistic" {
		t.Fatalf("SQL = %+v, want pessimistic", info.SQL)
	}

	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name: "testdb", Engine: "kv", Backend: "memory",
		SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	}); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
}

func TestGRPCDatabase_UpdateDatabaseSQL_Closed(t *testing.T) {
	addr, stop := startDatabaseServer(t)
	defer stop()
	tr, cleanup := connectDatabaseTransport(t, addr)
	defer cleanup()
	tr.closed.Store(true)
	var closed *transport.ClosedError
	if _, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{Name: "testdb"}); !errors.As(err, &closed) {
		t.Fatalf("closed = %v, want *ClosedError", err)
	}
}
