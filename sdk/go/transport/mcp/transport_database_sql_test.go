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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestMCP_Database_UpdateDatabaseSQL(t *testing.T) {
	tr, cleanup := connectDatabaseBarrierInProcess(t, newMockDatabaseSvc())
	defer cleanup()

	info, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{
		Name: "mydb", SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL: %v", err)
	}
	if info.SQL == nil || info.SQL.Concurrency != "pessimistic" {
		t.Fatalf("SQL = %+v, want pessimistic", info.SQL)
	}

	// A nil SQL restores the defaults: the settings are cleared, as on every
	// other transport, not pinned to an explicit "optimistic".
	info, err = tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{Name: "mydb"})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL(default): %v", err)
	}
	if info.SQL != nil {
		t.Fatalf("SQL = %+v, want cleared", info.SQL)
	}

	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name: "mydb2", Engine: "kv", Backend: "memory",
		SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	}); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
}

func TestMCP_Database_UpdateDatabaseSQL_Closed(t *testing.T) {
	tr, cleanup := connectDatabaseBarrierInProcess(t, newMockDatabaseSvc())
	defer cleanup()
	tr.closed.Store(true)
	if _, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{Name: "mydb"}); err == nil {
		t.Fatal("expected a closed-transport error")
	}
}
