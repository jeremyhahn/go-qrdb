//go:build qrdb_sql

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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// The configured default SQL database (sql.default_database) exists for every
// tenant, exactly as the service registry resolves it: each tenant sees its own
// "<tenant>/<db>/" namespace of it. Scoping lookups to the owning tenant hid it
// from every tenant session, so a tenant client whose connection names it --
// the default -- could not connect at all. Every other system-owned database
// stays hidden from tenants.
func TestProvider_SharedDefaultDatabaseVisibleToEveryTenant(t *testing.T) {
	mgr := newTestManager(t)
	for _, name := range []string{"qrdb", "ops"} {
		if _, err := mgr.Create(database.DatabaseConfig{Name: name, Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}
	p := NewManagerProvider(mgr, &mockKVService{}, WithSharedDefaultDatabase("qrdb"))
	tenantCtx := tenantSession("alice", "acme")

	if !p.HasDatabase(tenantCtx, "qrdb") {
		t.Error("the shared default database is hidden from a tenant session")
	}
	if _, err := p.Database(tenantCtx, "qrdb"); err != nil {
		t.Errorf("Database(qrdb) for a tenant: %v", err)
	}
	listed := false
	for _, db := range p.AllDatabases(tenantCtx) {
		if db.Name() == "qrdb" {
			listed = true
		}
		if db.Name() == "ops" {
			t.Error("another system-owned database is listed for a tenant")
		}
	}
	if !listed {
		t.Error("the shared default database is not listed for a tenant")
	}
	if p.HasDatabase(tenantCtx, "ops") {
		t.Error("another system-owned database is visible to a tenant")
	}
}

// Without the option no system-owned database is shared.
func TestProvider_NoSharedDefaultDatabase(t *testing.T) {
	mgr := newTestManager(t)
	if _, err := mgr.Create(database.DatabaseConfig{Name: "qrdb", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	p := NewManagerProvider(mgr, &mockKVService{})
	if p.HasDatabase(tenantSession("alice", "acme"), "qrdb") {
		t.Fatal("a system-owned database is visible to a tenant without being shared")
	}
}
