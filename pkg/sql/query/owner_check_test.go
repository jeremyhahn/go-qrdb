//go:build qrdb_sql

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package query

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ownerCatalog is a catalog of three KV databases: acme's, globex's, and the
// shared default SQL database (recorded with no owner, so the system
// tenant's), each with its own SQL settings.
func ownerCatalog() *mockDB {
	owners := map[string]string{"acmedb": "acme", "globexdb": "globex", "shared": ""}
	return &mockDB{getFn: func(name string) (*database.DatabaseInfo, error) {
		owner, ok := owners[name]
		if !ok {
			return nil, &database.NotFoundError{Name: name}
		}
		info, _ := kvDB(name)
		info.TenantID = owner
		info.SQL = &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic}
		return info, nil
	}}
}

// TestExecute_AnotherTenantsCatalogEntryIsMissing: the engine lookup is
// owner-checked, so a tenant naming another tenant's database is answered
// exactly as for a name nobody holds -- even with no registry in front.
func TestExecute_AnotherTenantsCatalogEntryIsMissing(t *testing.T) {
	svc := NewService(newMockKV(), ownerCatalog(), WithSharedDefaultDatabase("shared"))
	ctx := authedContext("acme")

	var notFound *database.NotFoundError
	if _, err := svc.Execute(ctx, "globexdb", "SELECT 1"); !errors.As(err, &notFound) {
		t.Fatalf("acme on globex's database = %v, want *database.NotFoundError", err)
	}
	if _, err := svc.Execute(ctx, "nosuch", "SELECT 1"); !errors.As(err, &notFound) {
		t.Fatalf("acme on a missing database = %v, want *database.NotFoundError", err)
	}
	if _, err := svc.Execute(ctx, "acmedb", "SELECT 1"); err != nil {
		t.Fatalf("acme on its own database: %v", err)
	}
	if _, err := svc.Execute(ctx, "shared", "SELECT 1"); err != nil {
		t.Fatalf("acme on the shared default SQL database: %v", err)
	}
}

// TestDatabaseSQLConfig_IsOwnerChecked: another tenant's SQL settings are
// never read for a session -- it gets the defaults -- while the owner's, and
// the shared default database's for every tenant, are.
func TestDatabaseSQLConfig_IsOwnerChecked(t *testing.T) {
	svc := NewService(newMockKV(), ownerCatalog(), WithSharedDefaultDatabase("shared")).(*sqlService)
	if cfg := svc.databaseSQLConfig("acme", "globexdb"); cfg != nil {
		t.Fatalf("acme read globex's SQL settings: %+v", cfg)
	}
	if cfg := svc.databaseSQLConfig("acme", "acmedb"); cfg == nil {
		t.Fatal("acme did not get its own SQL settings")
	}
	if cfg := svc.databaseSQLConfig("globex", "shared"); cfg == nil {
		t.Fatal("globex did not get the shared default database's SQL settings")
	}
}

// TestExecute_WithoutSharedDefault_EveryNameIsOwnerChecked: without the
// option, the shared name is an ordinary system-owned database, missing to a
// tenant.
func TestExecute_WithoutSharedDefault_EveryNameIsOwnerChecked(t *testing.T) {
	svc := NewService(newMockKV(), ownerCatalog())
	var notFound *database.NotFoundError
	if _, err := svc.Execute(authedContext("acme"), "shared", "SELECT 1"); !errors.As(err, &notFound) {
		t.Fatalf("acme on a system database = %v, want *database.NotFoundError", err)
	}
}

// pairCatalog is a catalog keyed by (tenant, name), as the real one is: the
// owner-checked read resolves the pair, and the name-only read answers
// *database.AmbiguousNameError once two tenants hold a name, as
// database.Manager.Get does.
type pairCatalog struct {
	service.DatabaseServicer
	sql map[string]map[string]*database.SQLConfig // owner -> name -> settings
}

func (c *pairCatalog) Get(name string) (*database.DatabaseInfo, error) {
	var found *database.DatabaseInfo
	for owner, dbs := range c.sql {
		if _, ok := dbs[name]; !ok {
			continue
		}
		if found != nil {
			return nil, &database.AmbiguousNameError{Name: name}
		}
		found = c.info(owner, name)
	}
	if found == nil {
		return nil, &database.NotFoundError{Name: name}
	}
	return found, nil
}

func (c *pairCatalog) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	owner := tenantid.Normalize(tenant.FromContext(ctx))
	if _, ok := c.sql[owner][name]; !ok {
		return nil, &database.NotFoundError{Name: name}
	}
	return c.info(owner, name), nil
}

func (c *pairCatalog) info(owner, name string) *database.DatabaseInfo {
	info, _ := kvDB(name)
	info.TenantID = owner
	info.SQL = c.sql[owner][name]
	return info
}

var _ service.DatabaseServicer = (*pairCatalog)(nil)

// TestDatabaseSQLConfig_TwoTenantsSharingANameEachResolveTheirOwn: a name two
// tenants hold resolves to the caller's own database, the shared default
// included -- a tenant that owns a database of the shared default's name
// reaches its own, and one that does not reaches the system tenant's.
func TestDatabaseSQLConfig_TwoTenantsSharingANameEachResolveTheirOwn(t *testing.T) {
	pessimistic := &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic}
	optimistic := &database.SQLConfig{Concurrency: database.ConcurrencyOptimistic}
	catalog := &pairCatalog{sql: map[string]map[string]*database.SQLConfig{
		"acme":          {"orders": pessimistic, "shared": optimistic},
		"globex":        {"orders": optimistic},
		tenantid.System: {"shared": pessimistic},
	}}
	svc := NewService(newMockKV(), catalog, WithSharedDefaultDatabase("shared")).(*sqlService)

	tests := []struct {
		tenant, name string
		want         *database.SQLConfig
	}{
		{"acme", "orders", pessimistic},
		{"globex", "orders", optimistic},
		{"acme", "shared", optimistic},
		{"globex", "shared", pessimistic},
		{"initech", "orders", nil},
	}
	for _, tt := range tests {
		t.Run(tt.tenant+"/"+tt.name, func(t *testing.T) {
			if got := svc.databaseSQLConfig(tt.tenant, tt.name); got != tt.want {
				t.Fatalf("databaseSQLConfig(%q, %q) = %+v, want %+v", tt.tenant, tt.name, got, tt.want)
			}
		})
	}
	if _, err := svc.Execute(authedContext("acme"), "orders", "SELECT 1"); err != nil {
		t.Fatalf("acme's own orders: %v", err)
	}
	if _, err := svc.Execute(authedContext("globex"), "shared", "SELECT 1"); err != nil {
		t.Fatalf("the shared default through the system tenant: %v", err)
	}
}
