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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTransportImplementsDatabaseScoper asserts the MCP transport satisfies
// the DatabaseScoper interface so Client.DatabaseScoped dispatches to it.
func TestTransportImplementsDatabaseScoper(t *testing.T) {
	var tr any = &Transport{}
	if _, ok := tr.(transport.DatabaseScoper); !ok {
		t.Fatal("MCP Transport does not implement transport.DatabaseScoper")
	}
}

// TestWithDatabaseSetsScope verifies WithDatabase records the target database
// and that scoping to the default "system" database (or empty string) yields
// an unscoped clone.
func TestWithDatabaseSetsScope(t *testing.T) {
	base := &Transport{config: transport.DefaultConfig()}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"named database", "db2", "db2"},
		{"system is unscoped", "system", ""},
		{"empty is unscoped", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scoped, ok := base.WithDatabase(tc.in).(*Transport)
			if !ok {
				t.Fatal("WithDatabase did not return a *Transport")
			}
			if scoped.database != tc.want {
				t.Errorf("database = %q, want %q", scoped.database, tc.want)
			}
			// The clone must share the parent's connection state, not the
			// parent's database scope source.
			if scoped.config != base.config {
				t.Error("clone did not share the parent config")
			}
		})
	}
}

// TestIsDataPlaneTool verifies that only data-plane tools are classified as
// carrying the database scope argument; management/admin/barrier tools are not.
func TestIsDataPlaneTool(t *testing.T) {
	dataPlane := []string{
		"qrdb_put", "qrdb_get", "qrdb_delete", "qrdb_list", "qrdb_scan",
		"qrdb_watch",
		"qrdb_mvcc_put_with_lease", "qrdb_mvcc_range",
		"qrdb_tx_execute", "qrdb_batch_execute",
		"qrdb_lease_grant", "qrdb_index_create", "qrdb_index_list",
		"qrdb_ts_write", "qrdb_ts_get_retention_policy",
		"qrdb_vector_upsert", "qrdb_vector_search_similar",
	}
	for _, tool := range dataPlane {
		if !isDataPlaneTool(tool) {
			t.Errorf("isDataPlaneTool(%q) = false, want true", tool)
		}
	}

	management := []string{
		"qrdb_health", "qrdb_status",
		"qrdb_database_create", "qrdb_tenant_create",
		"qrdb_user_create", "qrdb_role_create",
		"qrdb_barrier_status", "qrdb_backup_create",
	}
	for _, tool := range management {
		if isDataPlaneTool(tool) {
			t.Errorf("isDataPlaneTool(%q) = true, want false", tool)
		}
	}
}
