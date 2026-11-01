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

package database

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func newCompactionManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m := newCatalogManager(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	t.Cleanup(func() { _ = m.Close() })
	for _, cfg := range []DatabaseConfig{
		{Name: "acme-db", TenantID: "acme", Backend: BackendMemory},
		{Name: "beta-db", TenantID: "beta", Backend: BackendMemory},
		{Name: "sys-db", Backend: BackendMemory},
	} {
		if _, err := m.Create(cfg); err != nil {
			t.Fatalf("Create(%s): %v", cfg.Name, err)
		}
	}
	return m, dir
}

// Every tenant, the system tenant included, may set policies on its own
// databases only; another tenant's database answers as missing. The answer
// is the owner the policy is written for.
func TestManager_CompactionOwnerForTenant_OwnDatabasesOnly(t *testing.T) {
	m, _ := newCompactionManager(t)
	tests := []struct {
		caller, db, owner string
	}{
		{"acme", "acme-db", "acme"},
		{"acme", "beta-db", ""},
		{"acme", "sys-db", ""},
		{tenantid.System, "sys-db", tenantid.System},
		{tenantid.System, "acme-db", ""},
		{"", "beta-db", ""},
		{"acme", "nope", ""},
	}
	for _, tt := range tests {
		owner, err := m.CompactionOwnerForTenant(tenant.WithTenantID(context.Background(), tt.caller), tt.db)
		if tt.owner != "" {
			if err != nil || owner != tt.owner {
				t.Errorf("%q on %s = %q, %v; want %q", tt.caller, tt.db, owner, err, tt.owner)
			}
			continue
		}
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Errorf("%q on %s = %q, %v; want NotFoundError", tt.caller, tt.db, owner, err)
		}
	}
}

// The owner of a database being dropped is told so; anyone else is told the
// database does not exist.
func TestManager_CompactionOwnerForTenant_DroppingDatabase(t *testing.T) {
	m, _ := newCompactionManager(t)
	db, _ := m.Get("acme-db")
	if err := m.claimDrop(context.Background(), db, "test-claim"); err != nil {
		t.Fatalf("fence: %v", err)
	}

	var dropping *DroppingError
	if _, err := m.CompactionOwnerForTenant(tenant.WithTenantID(context.Background(), "acme"), "acme-db"); !errors.As(err, &dropping) {
		t.Fatalf("owner on a dropping database = %v", err)
	}
	var nf *NotFoundError
	if _, err := m.CompactionOwnerForTenant(tenant.WithTenantID(context.Background(), "beta"), "acme-db"); !errors.As(err, &nf) {
		t.Fatalf("stranger on a dropping database = %v, want NotFoundError", err)
	}
}

// TestManager_CatalogHoldsNoCompactionPolicy: the catalog record carries no
// compaction policy. The policy is state in the database's own store
// (pkg/compaction), never in the catalog.
func TestManager_CatalogHoldsNoCompactionPolicy(t *testing.T) {
	_, dir := newCompactionManager(t)
	records, err := testCatalogFor(dir).List(context.Background())
	if err != nil || len(records) == 0 {
		t.Fatalf("catalog List: %d records, %v", len(records), err)
	}
	for _, meta := range records {
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if strings.Contains(string(data), "compaction") {
			t.Errorf("record of %q carries a compaction field: %s", meta.Name, data)
		}
	}
}
