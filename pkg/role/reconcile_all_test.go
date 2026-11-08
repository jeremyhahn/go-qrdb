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

package role

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// staleRetiredPermission stands in for a verb a rename retired: it is not in
// the registry, exactly like certs:generate after the v0.51 cutover.
const staleRetiredPermission permission.Permission = "certs:generate"

// listedTenants is a TenantLister over a fixed list.
type listedTenants struct {
	infos []tenant.TenantInfo
	err   error
}

func (l listedTenants) ListTenants(context.Context) ([]tenant.TenantInfo, error) {
	return l.infos, l.err
}

// failingReconciler is a RoleServicer whose per-tenant reconcile fails for
// one tenant and succeeds, unchanged, for every other.
type failingReconciler struct {
	RoleServicer
	failFor string
	err     error
}

func (f failingReconciler) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*ReconcileReport, error) {
	if tenantID == f.failFor {
		return nil, f.err
	}
	if tenantID == "nilreport" {
		return nil, nil
	}
	return &ReconcileReport{TenantID: tenantID}, nil
}

// strandBuiltIn seeds tenantID's built-ins in kv, then rewrites the stored
// role name to carry only the retired verb: the record an upgrade strands.
func strandBuiltIn(t *testing.T, kv kvstore.KVStore, tenantID, name string) {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ks := svc.(*kvRoleStore)
	if _, err := ks.ReconcileBuiltInRoles(ctx, tenantID); err != nil {
		t.Fatalf("seed %s: %v", tenantID, err)
	}
	stored, err := ks.GetRoleByName(ctx, tenantID, name)
	if err != nil {
		t.Fatalf("GetRoleByName(%s, %s): %v", tenantID, name, err)
	}
	stored.Permissions = []permission.Permission{staleRetiredPermission}
	if err := ks.putRole(ctx, stored, "test_strand"); err != nil {
		t.Fatalf("putRole: %v", err)
	}
}

func TestReconcileAllTenants_ConvergesEveryTenantsStrandedBuiltIns(t *testing.T) {
	shared := newFakeKVStore()
	vault := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{"vault": vault}}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	strandBuiltIn(t, shared, "acme", RoleNameAdmin)
	strandBuiltIn(t, shared, "acme", RoleNameOperator)
	strandBuiltIn(t, vault, "vault", RoleNameAdmin)

	// A custom role in acme must survive untouched.
	acmeCtx := tenant.WithTenantID(context.Background(), "acme")
	custom, err := svc.CreateRole(acmeCtx, &CreateRoleRequest{
		Name: "legacy-workaround", TenantID: "acme", Permissions: []permission.Permission{permission.RolesRead},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	lister := listedTenants{infos: []tenant.TenantInfo{{ID: "vault"}, {ID: "acme"}, {ID: "acme"}, {ID: ""}}}
	report, err := ReconcileAllTenants(context.Background(), svc, lister)
	if err != nil {
		t.Fatalf("ReconcileAllTenants: %v", err)
	}
	if len(report.Failed) != 0 || len(report.Skipped) != 0 {
		t.Fatalf("failed=%v skipped=%v, want none", report.Failed, report.Skipped)
	}
	var order []string
	for _, tr := range report.Tenants {
		order = append(order, tr.TenantID)
	}
	if len(order) != 3 || order[0] != tenantid.System || order[1] != "acme" || order[2] != "vault" {
		t.Fatalf("tenant order = %v, want [system acme vault]", order)
	}
	if !report.Changed() {
		t.Fatal("Changed() = false after converging stranded roles")
	}

	for _, c := range []struct{ tenantID, name string }{
		{"acme", RoleNameAdmin}, {"acme", RoleNameOperator}, {"vault", RoleNameAdmin},
	} {
		ctx := tenant.WithTenantID(context.Background(), c.tenantID)
		got, err := svc.GetRoleByName(ctx, c.tenantID, c.name)
		if err != nil {
			t.Fatalf("GetRoleByName(%s, %s): %v", c.tenantID, c.name, err)
		}
		added, removed := permissionSetDiff(got.Permissions, canonicalPermissions(t, c.tenantID, c.name))
		if len(added) != 0 || len(removed) != 0 {
			t.Fatalf("%s/%s not converged: missing %v, extra %v", c.tenantID, c.name, added, removed)
		}
	}

	kept, err := svc.GetRole(acmeCtx, custom.ID)
	if err != nil {
		t.Fatalf("GetRole(custom): %v", err)
	}
	if len(kept.Permissions) != 1 || kept.Permissions[0] != permission.RolesRead {
		t.Fatalf("custom role permissions = %v, want [roles:read]", kept.Permissions)
	}

	// Idempotent: a second pass writes nothing.
	again, err := ReconcileAllTenants(context.Background(), svc, lister)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again.Changed() {
		t.Fatalf("second pass changed %v, want nothing", again.Tenants)
	}
}

func TestReconcileAllTenants_RefusesANonSystemCaller(t *testing.T) {
	svc, err := NewRoutingService(&fakeResolver{shared: newFakeKVStore(), tenants: map[string]kvstore.KVStore{}})
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	lister := listedTenants{infos: []tenant.TenantInfo{{ID: "acme"}, {ID: "other"}}}
	for _, caller := range []string{"acme", "other"} {
		_, err := ReconcileAllTenants(tenant.WithTenantID(context.Background(), caller), svc, lister)
		var denied *tenant.PlatformOperatorRequiredError
		if !errors.As(err, &denied) || denied.IdentityTenantID != caller {
			t.Fatalf("caller %s: err = %v, want *tenant.PlatformOperatorRequiredError", caller, err)
		}
	}
}

func TestReconcileAllTenants_SkipsSealedAndRefusedTenants(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{},
		sealed:  map[string]bool{"vault": true},
	}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	lister := listedTenants{infos: []tenant.TenantInfo{
		{ID: "vault"},
		{ID: "remote", Refusal: errors.New("volume pool fast does not resolve")},
	}}
	report, err := ReconcileAllTenants(context.Background(), svc, lister)
	if err != nil {
		t.Fatalf("ReconcileAllTenants: %v", err)
	}
	if len(report.Skipped) != 2 {
		t.Fatalf("skipped = %v, want remote and vault", report.Skipped)
	}
	reasons := map[string]string{}
	for _, s := range report.Skipped {
		reasons[s.TenantID] = s.Reason
	}
	if reasons["remote"] != SkipReasonRefused || reasons["vault"] != SkipReasonSealed {
		t.Fatalf("reasons = %v", reasons)
	}
	if len(report.Tenants) != 1 || report.Tenants[0].TenantID != tenantid.System {
		t.Fatalf("tenants = %v, want the system tenant only", report.Tenants)
	}
}

func TestReconcileAllTenants_OneTenantsFailureDoesNotStopThePass(t *testing.T) {
	boom := errors.New("store unavailable")
	svc := failingReconciler{failFor: "bad", err: boom}
	lister := listedTenants{infos: []tenant.TenantInfo{{ID: "bad"}, {ID: "good"}, {ID: "nilreport"}}}
	report, err := ReconcileAllTenants(context.Background(), svc, lister)
	if err != nil {
		t.Fatalf("ReconcileAllTenants: %v", err)
	}
	if len(report.Failed) != 1 || report.Failed[0].TenantID != "bad" || report.Failed[0].Error != boom.Error() {
		t.Fatalf("failed = %v, want bad: %v", report.Failed, boom)
	}
	if len(report.Tenants) != 3 || report.Changed() {
		t.Fatalf("tenants = %v, want system, good and nilreport unchanged", report.Tenants)
	}
}

func TestReconcileAllTenants_ArgumentAndListErrors(t *testing.T) {
	svc := failingReconciler{}
	if _, err := ReconcileAllTenants(context.Background(), nil, listedTenants{}); err == nil {
		t.Fatal("nil role service: want error")
	}
	if _, err := ReconcileAllTenants(context.Background(), svc, nil); err == nil {
		t.Fatal("nil tenant lister: want error")
	}
	boom := errors.New("list failed")
	_, err := ReconcileAllTenants(context.Background(), svc, listedTenants{err: boom})
	var se *StoreError
	if !errors.As(err, &se) || !errors.Is(err, boom) {
		t.Fatalf("list failure: err = %v, want *StoreError wrapping it", err)
	}
	var nilReport *AllTenantsReconcileReport
	if nilReport.Changed() {
		t.Fatal("nil report Changed() = true")
	}
}
