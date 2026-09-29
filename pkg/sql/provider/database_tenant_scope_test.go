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

package provider

import (
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestProvider_DatabaseLookupScopedToSessionTenant pins SEC-8(b) on the SQL
// wire: HasDatabase and Database answered for every tenant's databases, so a
// session could tell which names other tenants use and USE them. Another
// tenant's database must look exactly like a missing one.
func TestProvider_DatabaseLookupScopedToSessionTenant(t *testing.T) {
	mgr := newTestManager(t)
	for owner, name := range map[string]string{"acme": "acme_ledger", "": "ops"} {
		if _, err := mgr.Create(database.DatabaseConfig{TenantID: owner, Name: name, Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}
	p := NewManagerProvider(mgr, &mockKVService{})

	cases := []struct {
		ctx      *sqle.Context
		name     string
		visible  bool
		scenario string
	}{
		{tenantSession("alice", "acme"), "acme_ledger", true, "owner"},
		{tenantSession("mallory", "globex"), "acme_ledger", false, "other tenant"},
		{tenantSession("mallory", "globex"), "ops", false, "system-owned, tenant session"},
		{tenantSession("root", tenant.SystemTenantID), "ops", true, "system-owned, system session"},
		{tenantSession("root", tenant.SystemTenantID), "acme_ledger", false, "tenant-owned, system session"},
		{tenantSession("mallory", "globex"), "nobody_has_this", false, "missing"},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			if got := p.HasDatabase(c.ctx, c.name); got != c.visible {
				t.Errorf("HasDatabase(%s) = %v, want %v", c.name, got, c.visible)
			}
			_, err := p.Database(c.ctx, c.name)
			var nf *DatabaseNotFoundError
			if c.visible && err != nil {
				t.Errorf("Database(%s) = %v, want the database", c.name, err)
			}
			if !c.visible && !errors.As(err, &nf) {
				t.Errorf("Database(%s) = %v (%T), want *DatabaseNotFoundError", c.name, err, err)
			}
		})
	}
}
