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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// orphanedMetadata writes a volume-isolation tenant's metadata record
// directly, with NO catalog Assignment ever reserved for it -- the state a
// node that missed a peer's CreateTenant (a partial replication, a crash
// between the metadata write and the Reserve a future version might retry)
// is left in. The pre-catalog equivalent of this fixture injected a raw
// tenant-root record naming a path on another node's disk
// (*PersistedDataRootError / *seal.AbsoluteDataRootRecordError); neither the
// record shape nor either error exists any more, since a catalog Assignment
// is the only durable record and Reserve validates it before it is ever
// written (docs/volume-pools D9). An unresolvable tenant now looks like
// exactly what it is here: a metadata record whose id the catalog has never
// heard of, refused with *volume.AssignmentNotFoundError.
func orphanedMetadata(t *testing.T, svc *TenantService, id string) {
	t.Helper()
	ctx := context.Background()
	meta := `{"id":"` + id + `","name":"Orphaned","isolation_type":"volume"}`
	if err := svc.store.Put(ctx, metadataPrefix+id, []byte(meta)); err != nil {
		t.Fatal(err)
	}
}

func TestRefusalOf(t *testing.T) {
	if got := RefusalOf(nil); got != nil {
		t.Fatalf("RefusalOf(nil) = %+v, want nil", got)
	}

	missingProvider := &volume.ProviderNotRegisteredError{Provider: "nvme", Available: []string{"file"}}
	providerRefusal := RefusalOf(missingProvider)
	if providerRefusal == nil {
		t.Fatal("RefusalOf(provider error) = nil, want refusal")
	}
	if providerRefusal.Code != RefusalCodeProviderMissing {
		t.Fatalf("provider refusal code = %q, want %q", providerRefusal.Code, RefusalCodeProviderMissing)
	}
	if providerRefusal.Message != missingProvider.Error() {
		t.Fatalf("provider refusal message = %q, want %q", providerRefusal.Message, missingProvider.Error())
	}

	poolErr := errors.New("pool exhausted")
	poolRefusal := RefusalOf(poolErr)
	if poolRefusal == nil {
		t.Fatal("RefusalOf(pool error) = nil, want refusal")
	}
	if poolRefusal.Code != RefusalCodePoolUnavailable {
		t.Fatalf("pool refusal code = %q, want %q", poolRefusal.Code, RefusalCodePoolUnavailable)
	}
	if poolRefusal.Message != poolErr.Error() {
		t.Fatalf("pool refusal message = %q, want %q", poolRefusal.Message, poolErr.Error())
	}
}

// TestListTenants_ARefusedTenantDoesNotHideTheOthers: one tenant whose
// catalog Assignment does not resolve on this node must not take the
// listing -- and with it every node's boot -- down. It is listed with its
// typed refusal; the others are served as before.
func TestListTenants_ARefusedTenantDoesNotHideTheOthers(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newReconcileService(t)
	if _, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume); err != nil {
		t.Fatal(err)
	}
	orphanedMetadata(t, svc, "orphan")

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants = %v; one refused tenant must not fail the listing", err)
	}
	byID := make(map[string]TenantInfo, len(tenants))
	for _, ti := range tenants {
		byID[ti.ID] = ti
	}
	if got := byID["vlive"]; got.Refusal != nil {
		t.Fatalf("live tenant = %+v; want served with no refusal", got)
	}
	var notFound *volume.AssignmentNotFoundError
	got, ok := byID["orphan"]
	if !ok || !errors.As(got.Refusal, &notFound) {
		t.Fatalf("orphan tenant = %+v (listed %v); want *volume.AssignmentNotFoundError", got, ok)
	}
}

// TestGetTenant_ARefusedTenantCarriesItsRefusal proves a single GetTenant
// reports the same refusal ListTenants already did: a tenant this node
// cannot resolve is not silently returned as if it were served. Before
// this fix, GetTenant skipped checkLocalResolution entirely, so `tenant
// get` (unlike `tenant list`) never showed Refused: at all.
func TestGetTenant_ARefusedTenantCarriesItsRefusal(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newReconcileService(t)
	if _, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume); err != nil {
		t.Fatal(err)
	}
	orphanedMetadata(t, svc, "orphan")

	live, err := svc.GetTenant(ctx, "vlive")
	if err != nil {
		t.Fatalf("GetTenant(vlive) = %v", err)
	}
	if live.Refusal != nil {
		t.Fatalf("live tenant = %+v; want served with no refusal", live)
	}

	orphan, err := svc.GetTenant(ctx, "orphan")
	if err != nil {
		t.Fatalf("GetTenant(orphan) = %v; a refused tenant is still returned, not an error", err)
	}
	var notFound *volume.AssignmentNotFoundError
	if !errors.As(orphan.Refusal, &notFound) {
		t.Fatalf("orphan.Refusal = %v, want *volume.AssignmentNotFoundError", orphan.Refusal)
	}
}

// TestDeleteTenant_RemovesARefusedRecordWithoutTouchingAnyPath: the delete
// is the way out for a refused tenant. It retires the id and removes every
// record that would restore it; there is no Assignment and so no directory
// to leave untouched, which is itself the point -- DestroyTenant's
// "no Assignment" case is "nothing to remove", not an error.
func TestDeleteTenant_RemovesARefusedRecordWithoutTouchingAnyPath(t *testing.T) {
	ctx := context.Background()
	svc, reg, _ := newReconcileService(t)
	if _, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume); err != nil {
		t.Fatal(err)
	}
	liveRoot, err := reg.LocalTenantDataRoot(ctx, "vlive")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vlive): %v", err)
	}
	orphanedMetadata(t, svc, "orphan")

	if err := svc.DeleteTenant(ctx, "orphan"); err != nil {
		t.Fatalf("DeleteTenant(refused) = %v; want success", err)
	}
	if _, err := svc.GetTenant(ctx, "orphan"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("GetTenant after delete = %v; want ErrTenantNotFound", err)
	}
	if held, err := reg.HasTenantState(ctx, "orphan"); err != nil || held {
		t.Fatalf("HasTenantState after delete = %v, %v; want no record left to restore it", held, err)
	}
	page, err := svc.ListDeletedTenants(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(page.Tombstones, func(ts Tombstone) bool { return ts.ID == "orphan" }) {
		t.Fatalf("tombstones = %+v; want the id retired", page.Tombstones)
	}
	if !dirExists(t, liveRoot) {
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
	orphanedMetadata(t, svc, "orphan")
	svc.SetDatabaseLister(&listingLister{names: []string{"ledger"}})

	var has *TenantHasDatabasesError
	if err := svc.DeleteTenant(ctx, "orphan"); !errors.As(err, &has) {
		t.Fatalf("DeleteTenant = %v; want *TenantHasDatabasesError", err)
	}
	if _, err := svc.store.Get(ctx, metadataPrefix+"orphan"); err != nil {
		t.Fatalf("the refused record was removed despite the refusal: %v", err)
	}
}

// Note: TestReconcileDeletedTenants_ProtectsARefusedLiveTenant covered a
// refused LIVE tenant's recorded path and pool-derived directory both
// surviving a concurrent reconcile pass. Under the catalog, an unresolvable
// tenant (no Assignment) has no recorded path and no pool-derived directory
// to protect in the first place -- liveTenantRootsStrict's
// TenantDataRootCandidates call returns no candidates for an id with no
// Assignment (*volume.AssignmentNotFoundError is treated as "nothing live
// here", not an abort) -- so there is nothing left for this test to prove.
