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

package tenant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// legacyRecords writes the two records a tenant created before tenant
// locations were resolved per node left behind: tenant metadata naming its
// DataRoot and a tenant-root record naming the same path. The path holds a
// file, so a test can tell whether anything touched it.
func legacyRecords(t *testing.T, svc *TenantService, id string) string {
	t.Helper()
	ctx := context.Background()
	recorded := filepath.Join(t.TempDir(), "another-node", id)
	if err := os.MkdirAll(recorded, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recorded, "data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"` + id + `","name":"Legacy","isolation_type":"volume","data_root":"` + recorded + `"}`
	if err := svc.store.Put(ctx, metadataPrefix+id, []byte(meta)); err != nil {
		t.Fatal(err)
	}
	root := `{"pool":"default","slot":0,"data_root":"` + recorded + `","auto_unseal":true}`
	if err := svc.registry.SystemBarrier().Put(ctx, "__registry:tenant_root/"+id, []byte(root)); err != nil {
		t.Fatal(err)
	}
	return recorded
}

// TestListTenants_ARefusedTenantDoesNotHideTheOthers: one tenant whose record
// names a path must not take the listing -- and with it every node's boot --
// down. It is listed with its typed refusal and no DataRoot; the others are
// served as before. A tenant whose own metadata is clean but whose
// tenant-root record names a path is refused the same way.
func TestListTenants_ARefusedTenantDoesNotHideTheOthers(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newReconcileService(t)
	live, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	legacyRecords(t, svc, "legacy")
	sealOnly := filepath.Join(t.TempDir(), "sealonly")
	if err := svc.store.Put(ctx, metadataPrefix+"sealonly",
		[]byte(`{"id":"sealonly","name":"S","isolation_type":"volume"}`)); err != nil {
		t.Fatal(err)
	}
	if err := svc.registry.SystemBarrier().Put(ctx, "__registry:tenant_root/sealonly",
		[]byte(`{"pool":"default","data_root":"`+sealOnly+`"}`)); err != nil {
		t.Fatal(err)
	}

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants = %v; one refused tenant must not fail the listing", err)
	}
	byID := make(map[string]TenantInfo, len(tenants))
	for _, ti := range tenants {
		byID[ti.ID] = ti
	}
	if got := byID["vlive"]; got.Refusal != nil || got.DataRoot != live.DataRoot {
		t.Fatalf("live tenant = %+v; want served at %s with no refusal", got, live.DataRoot)
	}
	var persisted *PersistedDataRootError
	if got, ok := byID["legacy"]; !ok || !errors.As(got.Refusal, &persisted) || got.DataRoot != "" {
		t.Fatalf("legacy tenant = %+v (listed %v); want *PersistedDataRootError and no DataRoot", got, ok)
	}
	var abs *seal.AbsoluteDataRootRecordError
	if got, ok := byID["sealonly"]; !ok || !errors.As(got.Refusal, &abs) || got.DataRoot != "" {
		t.Fatalf("sealonly tenant = %+v (listed %v); want *seal.AbsoluteDataRootRecordError and no DataRoot", got, ok)
	}
	if !strings.Contains(persisted.Error(), "delete the tenant") || !strings.Contains(persisted.Error(), "re-create") {
		t.Fatalf("refusal %q does not tell the operator to delete and re-create", persisted.Error())
	}
}

// TestDeleteTenant_RemovesARefusedRecordWithoutTouchingAnyPath: the delete is
// the way out for a refused tenant. It retires the id and removes every
// record that would restore it, and the directory the record named is left
// exactly as it was.
func TestDeleteTenant_RemovesARefusedRecordWithoutTouchingAnyPath(t *testing.T) {
	ctx := context.Background()
	svc, reg, _ := newReconcileService(t)
	live, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	recorded := legacyRecords(t, svc, "legacy")

	if err := svc.DeleteTenant(ctx, "legacy"); err != nil {
		t.Fatalf("DeleteTenant(refused) = %v; want success", err)
	}
	if _, err := os.Stat(filepath.Join(recorded, "data")); err != nil {
		t.Fatalf("the directory the refused record named was touched: %v", err)
	}
	if _, err := svc.GetTenant(ctx, "legacy"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("GetTenant after delete = %v; want ErrTenantNotFound", err)
	}
	if held, err := reg.HasTenantState(ctx, "legacy"); err != nil || held {
		t.Fatalf("HasTenantState after delete = %v, %v; want no record left to restore it", held, err)
	}
	page, err := svc.ListDeletedTenants(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(page.Tombstones, func(ts Tombstone) bool { return ts.ID == "legacy" }) {
		t.Fatalf("tombstones = %+v; want the id retired", page.Tombstones)
	}
	if !dirExists(t, live.DataRoot) {
		t.Fatal("deleting a refused tenant removed another tenant's directory")
	}
	tenants, err := svc.ListTenants(ctx)
	if err != nil || len(tenants) != 1 || tenants[0].ID != "vlive" {
		t.Fatalf("ListTenants after delete = %+v, %v; want only vlive", tenants, err)
	}
}

// TestDeleteTenant_RefusedRecordWithDatabasesIsRefused: a refused tenant is
// held to the same no-databases rule as any other.
func TestDeleteTenant_RefusedRecordWithDatabasesIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newReconcileService(t)
	legacyRecords(t, svc, "legacy")
	svc.SetDatabaseLister(&listingLister{names: []string{"ledger"}})

	var has *TenantHasDatabasesError
	if err := svc.DeleteTenant(ctx, "legacy"); !errors.As(err, &has) {
		t.Fatalf("DeleteTenant = %v; want *TenantHasDatabasesError", err)
	}
	if _, err := svc.store.Get(ctx, metadataPrefix+"legacy"); err != nil {
		t.Fatalf("the refused record was removed despite the refusal: %v", err)
	}
}

// TestReconcileDeletedTenants_ProtectsARefusedLiveTenant: a live refused
// tenant neither aborts the reconciliation nor loses its directories -- the
// path its records name and its pool-derived directory are live roots.
func TestReconcileDeletedTenants_ProtectsARefusedLiveTenant(t *testing.T) {
	ctx := context.Background()
	svc, reg, _ := newReconcileService(t)
	recorded := legacyRecords(t, svc, "legacy")

	roots, err := svc.liveTenantRootsStrict(ctx)
	if err != nil {
		t.Fatalf("liveTenantRootsStrict = %v; a refused live tenant must not abort it", err)
	}
	bases, err := reg.PoolBasePaths(ctx)
	if err != nil || len(bases) == 0 {
		t.Fatalf("PoolBasePaths = %v, %v", bases, err)
	}
	for _, want := range []string{recorded, filepath.Join(bases[0], "legacy")} {
		if !slices.Contains(roots, want) {
			t.Fatalf("live roots %v do not protect %s", roots, want)
		}
	}
	if _, err := svc.ReconcileDeletedTenants(ctx); err != nil {
		t.Fatalf("ReconcileDeletedTenants = %v", err)
	}
	if _, err := os.Stat(filepath.Join(recorded, "data")); err != nil {
		t.Fatalf("reconciliation touched a refused live tenant's directory: %v", err)
	}
}
