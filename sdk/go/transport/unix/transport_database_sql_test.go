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

package unix

import (
	"context"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestUnixRT_UpdateDatabaseSQL(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	resp, err := tr.UpdateDatabaseSQL(context.Background(), transport.UpdateDatabaseSQLRequest{
		Name: "db1", SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseSQL: %v", err)
	}
	if resp.SQL == nil || resp.SQL.Concurrency != "pessimistic" {
		t.Fatalf("SQL = %+v, want pessimistic", resp.SQL)
	}
}

func TestUnixRT_CreateDatabase_CarriesSQL(t *testing.T) {
	svc := &mockDatabaseSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()

	if _, err := tr.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name: "db1", Engine: "kv", Backend: "memory",
		SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"},
	}); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
}
