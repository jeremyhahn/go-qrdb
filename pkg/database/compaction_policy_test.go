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
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func newCompactionManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	t.Cleanup(func() { _ = m.Close() })
	for _, cfg := range []DatabaseConfig{
		{Name: "acme-db", TenantID: "acme", Engine: EngineKV, Backend: BackendMemory},
		{Name: "beta-db", TenantID: "beta", Engine: EngineKV, Backend: BackendMemory},
		{Name: "sys-db", Engine: EngineKV, Backend: BackendMemory},
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
		{tenant.SystemTenantID, "sys-db", tenant.SystemTenantID},
		{tenant.SystemTenantID, "acme-db", ""},
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
	db.SetDropping(true)

	var dropping *DroppingError
	if _, err := m.CompactionOwnerForTenant(tenant.WithTenantID(context.Background(), "acme"), "acme-db"); !errors.As(err, &dropping) {
		t.Fatalf("owner on a dropping database = %v", err)
	}
	var nf *NotFoundError
	if _, err := m.CompactionOwnerForTenant(tenant.WithTenantID(context.Background(), "beta"), "acme-db"); !errors.As(err, &nf) {
		t.Fatalf("stranger on a dropping database = %v, want NotFoundError", err)
	}
}

// TestManager_CatalogHoldsNoCompactionPolicy: the catalog record -- each
// database's node-local meta.json -- carries no compaction policy. The policy
// is replicated state in the database's own store (pkg/compaction), never
// node-local.
func TestManager_CatalogHoldsNoCompactionPolicy(t *testing.T) {
	_, dir := newCompactionManager(t)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	found := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != metaFile {
			return err
		}
		found++
		data, rerr := root.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "compaction") {
			t.Errorf("%s carries a compaction field: %s", path, data)
		}
		return nil
	})
	if err != nil || found == 0 {
		t.Fatalf("walked the catalog: %d meta files, %v", found, err)
	}
}
