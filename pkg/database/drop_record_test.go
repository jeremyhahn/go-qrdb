// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestPutDropRecord_DelayedDropCannotOverwriteASuccessor replays the
// drop/recreate/retry interleaving: a drop of the predecessor pauses before
// its drop record, another drop of it completes, the name is recreated, the
// successor's drop records its id, removes its catalog record and fails in
// teardown. The resumed predecessor drop must not overwrite the successor's
// recovery record, and a retry of the successor's drop still resolves it.
func TestPutDropRecord_DelayedDropCannotOverwriteASuccessor(t *testing.T) {
	ctx := context.Background()
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	const tenantID, name = "acme", "orders"
	removeCatalog := func() {
		t.Helper()
		if err := cat.system.Delete(ctx, CatalogKey(tenantID, name)); err != nil {
			t.Fatalf("remove catalog record: %v", err)
		}
	}
	if err := cat.Create(ctx, DatabaseMeta{TenantID: tenantID, Name: name, Backend: BackendMemory, ID: "db-pred"}); err != nil {
		t.Fatalf("Create predecessor: %v", err)
	}
	// The completing drop of the predecessor.
	if err := cat.PutDropRecord(ctx, tenantID, name, "db-pred"); err != nil {
		t.Fatalf("PutDropRecord(pred): %v", err)
	}
	removeCatalog()
	if err := cat.DeleteDropRecord(ctx, tenantID, name, "db-pred"); err != nil {
		t.Fatalf("DeleteDropRecord(pred): %v", err)
	}
	// The successor, its drop failing after it removed the catalog record.
	if err := cat.Create(ctx, DatabaseMeta{TenantID: tenantID, Name: name, Backend: BackendMemory, ID: "db-succ"}); err != nil {
		t.Fatalf("Create successor: %v", err)
	}
	if err := cat.PutDropRecord(ctx, tenantID, name, "db-succ"); err != nil {
		t.Fatalf("PutDropRecord(succ): %v", err)
	}
	removeCatalog()

	// The delayed predecessor drop resumes.
	err = cat.PutDropRecord(ctx, tenantID, name, "db-pred")
	var superseded *DropRecordSupersededError
	if !errors.As(err, &superseded) {
		t.Fatalf("delayed PutDropRecord(pred) = %v, want *DropRecordSupersededError", err)
	}
	if got, err := cat.DropRecord(ctx, tenantID, name); err != nil || got != "db-succ" {
		t.Fatalf("drop record = (%q, %v), want the successor's id kept", got, err)
	}
	// The successor's retried drop still records (idempotently) and resolves.
	if err := cat.PutDropRecord(ctx, tenantID, name, "db-succ"); err != nil {
		t.Fatalf("retried PutDropRecord(succ): %v", err)
	}
}

// TestPutDropRecord_RefusesAnIDTheCatalogDoesNotCarry pins the fence's other
// half: a drop record is written only for the incarnation the catalog names.
func TestPutDropRecord_RefusesAnIDTheCatalogDoesNotCarry(t *testing.T) {
	ctx := context.Background()
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	if err := cat.Create(ctx, DatabaseMeta{TenantID: "acme", Name: "orders", Backend: BackendMemory, ID: "db-live"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var superseded *DropRecordSupersededError
	if err := cat.PutDropRecord(ctx, "acme", "orders", "db-gone"); !errors.As(err, &superseded) {
		t.Fatalf("PutDropRecord of a retired id = %v, want *DropRecordSupersededError", err)
	}
	if got, _ := cat.DropRecord(ctx, "acme", "orders"); got != "" {
		t.Fatalf("drop record = %q, want none", got)
	}
	if err := cat.PutDropRecord(ctx, "acme", "orders", "db-live"); err != nil {
		t.Fatalf("PutDropRecord of the live id: %v", err)
	}
}
