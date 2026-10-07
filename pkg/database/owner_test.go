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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestOwnedBy is the one ownership rule, both sides normalized: an untagged
// database is the system tenant's, and no tenant -- the system tenant
// included -- owns another's.
func TestOwnedBy(t *testing.T) {
	cases := []struct {
		owner, tenantID string
		want            bool
	}{
		{"acme", "acme", true},
		{"acme", "globex", false},
		{"", tenantid.System, true},
		{tenantid.System, "", true},
		{"", "", true},
		{"acme", tenantid.System, false},
		{"", "acme", false},
		{tenantid.System, "acme", false},
	}
	for _, c := range cases {
		if got := OwnedBy(c.owner, c.tenantID); got != c.want {
			t.Errorf("OwnedBy(%q, %q) = %v, want %v", c.owner, c.tenantID, got, c.want)
		}
	}
}

// newOwnedFixture is a manager holding one untagged (system) database and one
// owned by acme.
func newOwnedFixture(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)
	for _, cfg := range []DatabaseConfig{
		{Name: "boot", Backend: BackendMemory},
		{Name: "acmedb", TenantID: "acme", Backend: BackendMemory},
	} {
		if _, err := m.Create(cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	return m
}

// TestGetOwned_AnswersAnotherTenantsDatabaseAsMissing: the owner gets the
// database; anyone else, the system tenant included, gets NotFoundError --
// the same answer a name nobody holds gets.
func TestGetOwned_AnswersAnotherTenantsDatabaseAsMissing(t *testing.T) {
	m := newOwnedFixture(t)
	if db, err := m.GetOwned("acmedb", "acme"); err != nil || db.Name() != "acmedb" {
		t.Fatalf("owner: %v, %v", db, err)
	}
	if _, err := m.GetOwned("boot", tenantid.System); err != nil {
		t.Fatalf("system on its untagged database: %v", err)
	}
	for _, c := range []struct{ name, tenantID string }{
		{"acmedb", tenantid.System}, {"acmedb", "globex"}, {"boot", "acme"}, {"absent", "acme"},
	} {
		var nf *NotFoundError
		if _, err := m.GetOwned(c.name, c.tenantID); !errors.As(err, &nf) {
			t.Errorf("GetOwned(%q, %q) = %v, want *NotFoundError", c.name, c.tenantID, err)
		}
	}
}

// TestForTenant_SystemTenantIsConfinedNotRefused: the manager's *ForTenant
// operations hold the system tenant to the ownership rule instead of refusing
// it -- it sees, lists and updates its own databases and never acme's.
func TestForTenant_SystemTenantIsConfinedNotRefused(t *testing.T) {
	m := newOwnedFixture(t)
	sys := tenant.WithTenantID(context.Background(), tenantid.System)

	if _, err := m.GetForTenant(sys, "boot"); err != nil {
		t.Fatalf("GetForTenant(system, boot): %v", err)
	}
	var nf *NotFoundError
	if _, err := m.GetForTenant(sys, "acmedb"); !errors.As(err, &nf) {
		t.Fatalf("GetForTenant(system, acmedb) = %v, want *NotFoundError", err)
	}
	infos, err := m.ListForTenant(sys)
	if err != nil || len(infos) != 1 || infos[0].Name != "boot" {
		t.Fatalf("ListForTenant(system) = %v, %v; want [boot]", infos, err)
	}
	if err := m.UpdateSQLForTenant(sys, "acmedb", &SQLConfig{}); !errors.As(err, &nf) {
		t.Fatalf("UpdateSQLForTenant(system, acmedb) = %v, want *NotFoundError", err)
	}
	if err := m.DropForTenant(sys, "acmedb"); !errors.As(err, &nf) {
		t.Fatalf("DropForTenant(system, acmedb) = %v, want *NotFoundError", err)
	}
	if _, err := m.Get("acmedb"); err != nil {
		t.Fatalf("acme's database was touched: %v", err)
	}
}
