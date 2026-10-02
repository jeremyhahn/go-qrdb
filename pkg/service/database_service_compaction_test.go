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

package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// UpdateCompactionForTenant confines every caller -- the system tenant
// included -- to its own databases, and returns the updated information.
func TestDatabaseService_UpdateCompactionForTenant(t *testing.T) {
	mgr := database.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, cfg := range []database.DatabaseConfig{
		{Name: "acme-db", TenantID: "acme", Engine: database.EngineKV, Backend: database.BackendMemory},
		{Name: "sys-db", Engine: database.EngineKV, Backend: database.BackendMemory},
	} {
		if _, err := mgr.Create(cfg); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewDatabaseService(mgr)
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 2}
	acme := tenant.WithTenantID(context.Background(), "acme")
	system := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	info, err := svc.UpdateCompactionForTenant(acme, "acme-db", policy)
	if err != nil || info.Compaction == nil || info.Compaction.RetainRevisions != 2 {
		t.Fatalf("acme on its own database = %+v, %v", info, err)
	}
	if info, err = svc.UpdateCompactionForTenant(system, "sys-db", policy); err != nil || info.Compaction == nil {
		t.Fatalf("system on its own database = %+v, %v", info, err)
	}

	var nf *database.NotFoundError
	if _, err := svc.UpdateCompactionForTenant(system, "acme-db", nil); !errors.As(err, &nf) {
		t.Fatalf("system on acme's database = %v, want NotFoundError", err)
	}
	if _, err := svc.UpdateCompactionForTenant(acme, "sys-db", nil); !errors.As(err, &nf) {
		t.Fatalf("acme on the system tenant's database = %v, want NotFoundError", err)
	}
	if db, _ := mgr.Get("acme-db"); db.CompactionConfig() == nil {
		t.Fatal("a refused caller cleared acme's policy")
	}
}
