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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// retiredPermission is a permission string that is deliberately NOT registered
// in the global permission registry, standing in for a verb an older build
// defined and this one has retired — the go-quicbaas#573 shape, where a
// tenant's stored `admin` kept sdlc:register after the build renamed it.
const retiredPermission permission.Permission = "sdlc:register"

// ---------------------------------------------------------------------------
// countingKVStore: a memKVStore that counts writes
// ---------------------------------------------------------------------------

// countingKVStore wraps memKVStore and counts Put and Delete calls, so a test
// can assert that a reconcile pass wrote NOTHING rather than merely that it
// reported no changes. The two are different claims: a store that rewrote every
// built-in with an identical value would still report an empty diff.
type countingKVStore struct {
	*memKVStore
	puts    atomic.Int64
	deletes atomic.Int64
}

func newCountingKVStore() *countingKVStore {
	return &countingKVStore{memKVStore: newMemKVStore()}
}

func (c *countingKVStore) Put(ctx context.Context, key string, value []byte) error {
	c.puts.Add(1)
	return c.memKVStore.Put(ctx, key, value)
}

func (c *countingKVStore) Delete(ctx context.Context, key string) error {
	c.deletes.Add(1)
	return c.memKVStore.Delete(ctx, key)
}

func (c *countingKVStore) writes() int64 { return c.puts.Load() + c.deletes.Load() }

// Compile-time check: the counting wrapper is still a KVStore.
var _ kvstore.KVStore = (*countingKVStore)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// seedStaleBuiltIn writes a built-in role record and its indexes DIRECTLY,
// exactly as an older build's tenant provisioning left them: the canonical id
// and name, System true, and whatever permission set that build placed on the
// role. It bypasses CreateRole deliberately — CreateRole refuses a reserved
// name carrying anything but the CURRENT canonical permission set, which is
// precisely why the drift this repairs cannot be reproduced through it.
func seedStaleBuiltIn(t *testing.T, kv kvstore.KVStore, tenantID, name string, perms []permission.Permission) *Role {
	t.Helper()
	ctx := context.Background()

	seeded := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &Role{
		ID:          SystemRoleID(tenantID, name),
		Name:        name,
		TenantID:    tenantID,
		Permissions: perms,
		System:      true,
		CreatedAt:   seeded,
		UpdatedAt:   seeded,
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal seed role: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(r.ID), data); err != nil {
		t.Fatalf("put primary: %v", err)
	}
	if err := kv.Put(ctx, tenantIndexKey(tenantID, r.ID), []byte(r.ID)); err != nil {
		t.Fatalf("put tenant index: %v", err)
	}
	if err := kv.Put(ctx, nameIndexKey(tenantID, name), []byte(r.ID)); err != nil {
		t.Fatalf("put name index: %v", err)
	}
	return r
}

// canonicalPermissions returns the build's permission set for a built-in role
// in a tenant.
func canonicalPermissions(t *testing.T, tenantID, name string) []permission.Permission {
	t.Helper()
	r := canonicalSystemRole(tenantID, name)
	if r == nil {
		t.Fatalf("no canonical role named %q", name)
	}
	return append([]permission.Permission(nil), r.Permissions...)
}

// resultFor returns the report entry for a named role, or nil.
func resultFor(report *ReconcileReport, name string) *RoleReconcileResult {
	for i := range report.Roles {
		if report.Roles[i].Name == name {
			return &report.Roles[i]
		}
	}
	return nil
}

func containsPermission(perms []permission.Permission, want permission.Permission) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Convergence
// ---------------------------------------------------------------------------

// TestReconcileBuiltInRoles_ConvergesStaleTenant is the defect in go-qrdb#354:
// a tenant seeded from an older definition keeps a verb the build retired and
// never gains one the build added. Reconciliation must report and perform both
// halves.
func TestReconcileBuiltInRoles_ConvergesStaleTenant(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const tenantID = "acme"

	current := canonicalPermissions(t, tenantID, RoleNameAdmin)
	if len(current) < 2 {
		t.Fatalf("admin carries %d permissions; the fixture needs at least 2", len(current))
	}

	// The build grants `dropped`; the stale tenant does not carry it. The stale
	// tenant carries `retiredPermission`; the build does not define it at all.
	dropped := current[0]
	stale := append([]permission.Permission{retiredPermission}, current[1:]...)
	seedStaleBuiltIn(t, kv, tenantID, RoleNameAdmin, stale)

	report, err := svc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if report.TenantID != tenantID {
		t.Errorf("report tenant = %q, want %q", report.TenantID, tenantID)
	}
	if !report.Changed() {
		t.Fatal("report reports no change; the tenant was stale")
	}

	adminResult := resultFor(report, RoleNameAdmin)
	if adminResult == nil {
		t.Fatal("no report entry for admin")
	}
	if adminResult.Created {
		t.Error("admin reported as created; it already existed")
	}
	if len(adminResult.Added) != 1 || adminResult.Added[0] != dropped {
		t.Errorf("added = %v, want exactly [%s]", adminResult.Added, dropped)
	}
	if len(adminResult.Removed) != 1 || adminResult.Removed[0] != retiredPermission {
		t.Errorf("removed = %v, want exactly [%s]", adminResult.Removed, retiredPermission)
	}

	// The STORE, not just the report, must now hold the build's set.
	stored, err := svc.GetRoleByName(ctx, tenantID, RoleNameAdmin)
	if err != nil {
		t.Fatalf("GetRoleByName after reconcile: %v", err)
	}
	if !permissionSetsEqual(stored.Permissions, current) {
		t.Error("stored admin permissions do not match the build's definition")
	}
	if containsPermission(stored.Permissions, retiredPermission) {
		t.Errorf("stored admin still carries the retired permission %s", retiredPermission)
	}
	if !containsPermission(stored.Permissions, dropped) {
		t.Errorf("stored admin did not gain %s", dropped)
	}
	if len(stored.RetiredPermissions) != 0 {
		t.Errorf("stored admin still reports drift: %v", stored.RetiredPermissions)
	}
}

// TestReconcileBuiltInRoles_SeedsMissingBuiltIns covers a tenant that holds no
// built-in roles at all: every canonical role is created with its whole
// permission set.
func TestReconcileBuiltInRoles_SeedsMissingBuiltIns(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	const tenantID = "fresh"

	report, err := svc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}

	builtIn := BuiltInRoles(tenantID)
	if len(report.Roles) != len(builtIn) {
		t.Fatalf("report covers %d roles, want %d", len(report.Roles), len(builtIn))
	}

	for _, want := range builtIn {
		got := resultFor(report, want.Name)
		if got == nil {
			t.Fatalf("no report entry for %s", want.Name)
		}
		if !got.Created {
			t.Errorf("%s: Created = false, want true", want.Name)
		}
		if len(got.Added) != len(want.Permissions) {
			t.Errorf("%s: added %d permissions, want %d", want.Name, len(got.Added), len(want.Permissions))
		}
		if len(got.Removed) != 0 {
			t.Errorf("%s: removed = %v, want none", want.Name, got.Removed)
		}

		stored, err := svc.GetRoleByName(ctx, tenantID, want.Name)
		if err != nil {
			t.Fatalf("GetRoleByName(%s): %v", want.Name, err)
		}
		if !stored.System {
			t.Errorf("%s: seeded role is not marked System", want.Name)
		}
		if !permissionSetsEqual(stored.Permissions, want.Permissions) {
			t.Errorf("%s: seeded permissions do not match the canonical set", want.Name)
		}
	}
}

// TestReconcileBuiltInRoles_LeavesCustomRolesUntouched proves the blast radius:
// a role whose name is not a built-in name is neither read nor written, so an
// operator's workaround role survives the repair that supersedes it.
func TestReconcileBuiltInRoles_LeavesCustomRolesUntouched(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	const tenantID = "acme"

	custom, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "sdlc-operator",
		TenantID:    tenantID,
		Permissions: []permission.Permission{permission.KVGet, permission.KVPut},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	if _, err := svc.ReconcileBuiltInRoles(ctx, tenantID); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}

	after, err := svc.GetRole(ctx, custom.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if !permissionSetsEqual(after.Permissions, custom.Permissions) {
		t.Errorf("custom role permissions changed: %v", after.Permissions)
	}
	if !after.UpdatedAt.Equal(custom.UpdatedAt) {
		t.Error("custom role UpdatedAt moved; reconciliation wrote to it")
	}
	if after.System {
		t.Error("custom role became a system role")
	}
}

// TestReconcileBuiltInRoles_SecondCallWritesNothing is the idempotency claim,
// asserted on the STORE rather than only on the report: a converged tenant must
// cost zero writes, because this runs on every boot of every node and a no-op
// Put would still bump UpdatedAt, emit audit, and replicate a Raft entry.
func TestReconcileBuiltInRoles_SecondCallWritesNothing(t *testing.T) {
	ctx := context.Background()
	kv := newCountingKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const tenantID = "acme"

	current := canonicalPermissions(t, tenantID, RoleNameOperator)
	seedStaleBuiltIn(t, kv, tenantID, RoleNameOperator, current[1:])

	first, err := svc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		t.Fatalf("first ReconcileBuiltInRoles: %v", err)
	}
	if !first.Changed() {
		t.Fatal("first pass reported no change; the tenant was stale")
	}

	before := kv.writes()
	second, err := svc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		t.Fatalf("second ReconcileBuiltInRoles: %v", err)
	}
	if second.Changed() {
		t.Errorf("second pass reported changes: %+v", second.Roles)
	}
	if len(second.Roles) != 0 {
		t.Errorf("second pass report is not empty: %+v", second.Roles)
	}
	if got := kv.writes() - before; got != 0 {
		t.Errorf("second pass performed %d store writes, want 0", got)
	}
}

// TestReconcileBuiltInRoles_DefaultsToSystemTenant covers the empty tenant ID.
func TestReconcileBuiltInRoles_DefaultsToSystemTenant(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	report, err := svc.ReconcileBuiltInRoles(ctx, "")
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if report.TenantID != tenant.SystemTenantID {
		t.Errorf("tenant = %q, want %q", report.TenantID, tenant.SystemTenantID)
	}
	if _, err := svc.GetRoleByName(ctx, tenant.SystemTenantID, RoleNameAdmin); err != nil {
		t.Fatalf("system-tenant admin was not seeded: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Owner
// ---------------------------------------------------------------------------

// TestReconcileBuiltInRoles_NeverCreatesOwner: Owner is posture-conditional, so
// seeding it into a tenant that does not hold it would hand a strict
// separation-of-duties deployment a superuser reconciliation was never asked to
// create.
func TestReconcileBuiltInRoles_NeverCreatesOwner(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	const tenantID = "strict"

	report, err := svc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if got := resultFor(report, RoleNameOwner); got != nil {
		t.Errorf("owner reported: %+v", *got)
	}

	_, err = svc.GetRoleByName(ctx, tenantID, RoleNameOwner)
	var notFound *RoleNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("GetRoleByName(owner) = %v, want RoleNotFoundError", err)
	}
}

// TestReconcileBuiltInRoles_RepairsExistingOwner: an Owner the tenant DOES hold
// drifts for the same reason as the six — its grant is permission.All() — and
// UpdateRole refuses to repair it, so reconciliation must.
func TestReconcileBuiltInRoles_RepairsExistingOwner(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const tenantID = "standard"

	seedStaleBuiltIn(t, kv, tenantID, RoleNameOwner, []permission.Permission{retiredPermission})

	report, err := svc.ReconcileBuiltInRoles(ctx, tenantID)
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}

	owner := resultFor(report, RoleNameOwner)
	if owner == nil {
		t.Fatal("no report entry for owner")
	}
	if owner.Created {
		t.Error("owner reported as created; it already existed")
	}
	if len(owner.Removed) != 1 || owner.Removed[0] != retiredPermission {
		t.Errorf("removed = %v, want exactly [%s]", owner.Removed, retiredPermission)
	}

	stored, err := svc.GetRoleByName(ctx, tenantID, RoleNameOwner)
	if err != nil {
		t.Fatalf("GetRoleByName(owner): %v", err)
	}
	if !permissionSetsEqual(stored.Permissions, permission.All()) {
		t.Error("owner was not converged onto permission.All()")
	}
}

// ---------------------------------------------------------------------------
// The guard is unchanged
// ---------------------------------------------------------------------------

// TestReconcileBuiltInRoles_DoesNotWeakenTheGuard: reconciliation is the ONE
// sanctioned writer. Every other caller must still be refused, before and after
// a reconcile pass — otherwise this change traded a stranded tenant for an
// operator who can widen `auditor` by hand.
func TestReconcileBuiltInRoles_DoesNotWeakenTheGuard(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	const tenantID = "acme"

	if _, err := svc.ReconcileBuiltInRoles(ctx, tenantID); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}

	auditor, err := svc.GetRoleByName(ctx, tenantID, RoleNameAuditor)
	if err != nil {
		t.Fatalf("GetRoleByName(auditor): %v", err)
	}

	_, err = svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          auditor.ID,
		Permissions: []permission.Permission{permission.KVPut},
	})
	var immutable *SystemRoleImmutableError
	if !errors.As(err, &immutable) {
		t.Fatalf("UpdateRole(auditor, permissions) = %v, want SystemRoleImmutableError", err)
	}
	if immutable.Op != "re-permissioned" {
		t.Errorf("Op = %q, want %q", immutable.Op, "re-permissioned")
	}

	if err := svc.DeleteRole(ctx, auditor.ID); !errors.As(err, &immutable) {
		t.Fatalf("DeleteRole(auditor) = %v, want SystemRoleImmutableError", err)
	}

	after, err := svc.GetRole(ctx, auditor.ID)
	if err != nil {
		t.Fatalf("GetRole(auditor): %v", err)
	}
	if !permissionSetsEqual(after.Permissions, auditor.Permissions) {
		t.Error("auditor permissions changed despite the refusal")
	}
}

// ---------------------------------------------------------------------------
// Fault propagation
// ---------------------------------------------------------------------------

// TestReconcileBuiltInRoles_StoreFailurePropagates checks the error path: a
// store that cannot be read must surface a typed StoreError, never a partial
// report that reads as success.
func TestReconcileBuiltInRoles_StoreFailurePropagates(t *testing.T) {
	svc, err := NewStore(&errKVStore{getErr: errors.New("backend down")})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	report, err := svc.ReconcileBuiltInRoles(context.Background(), "acme")
	if err == nil {
		t.Fatal("ReconcileBuiltInRoles succeeded against a failing store")
	}
	if report != nil {
		t.Errorf("report = %+v, want nil on failure", report)
	}
	var storeErr *StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("err = %v, want StoreError", err)
	}
}

// TestReconcileBuiltInRoles_WriteFailurePropagates covers a store that reads a
// stale role but cannot write the repair.
func TestReconcileBuiltInRoles_WriteFailurePropagates(t *testing.T) {
	kv := &putFailKVStore{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const tenantID = "acme"
	seedStaleBuiltIn(t, kv, tenantID, RoleNameAdmin, []permission.Permission{retiredPermission})

	kv.fail.Store(true)
	if _, err := svc.ReconcileBuiltInRoles(context.Background(), tenantID); err == nil {
		t.Fatal("ReconcileBuiltInRoles succeeded despite a failing write")
	} else {
		var storeErr *StoreError
		if !errors.As(err, &storeErr) {
			t.Fatalf("err = %v, want StoreError", err)
		}
	}
}

// putFailKVStore fails Put once armed, so a reconcile can be interrupted after
// its reads succeed.
type putFailKVStore struct {
	*memKVStore
	fail atomic.Bool
}

func (p *putFailKVStore) Put(ctx context.Context, key string, value []byte) error {
	if p.fail.Load() {
		return errors.New("write refused")
	}
	return p.memKVStore.Put(ctx, key, value)
}

// ---------------------------------------------------------------------------
// Retired-permission drift on read
// ---------------------------------------------------------------------------

// TestRetiredPermissions_ReportedOnRead: an operator must be able to SEE the
// drift on an ordinary listing before running the repair.
func TestRetiredPermissions_ReportedOnRead(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const tenantID = "acme"
	stale := append([]permission.Permission{retiredPermission}, permission.KVGet)
	seeded := seedStaleBuiltIn(t, kv, tenantID, RoleNameAdmin, stale)

	got, err := svc.GetRole(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if len(got.RetiredPermissions) != 1 || got.RetiredPermissions[0] != retiredPermission {
		t.Fatalf("RetiredPermissions = %v, want [%s]", got.RetiredPermissions, retiredPermission)
	}

	// The listing carries it too — that is the surface the issue asks for.
	list, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: tenantID})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(list.Roles) != 1 {
		t.Fatalf("listed %d roles, want 1", len(list.Roles))
	}
	if len(list.Roles[0].RetiredPermissions) != 1 {
		t.Errorf("listed role reports %v, want the retired permission", list.Roles[0].RetiredPermissions)
	}

	// And the typed form.
	err = got.RetiredPermissionError()
	var retired *RetiredPermissionError
	if !errors.As(err, &retired) {
		t.Fatalf("RetiredPermissionError() = %v, want *RetiredPermissionError", err)
	}
	if retired.Name != RoleNameAdmin || retired.TenantID != tenantID || retired.RoleID != seeded.ID {
		t.Errorf("error identifies %s/%s/%s, want %s/%s/%s",
			retired.TenantID, retired.Name, retired.RoleID, tenantID, RoleNameAdmin, seeded.ID)
	}
	if len(retired.Permissions) != 1 || retired.Permissions[0] != retiredPermission {
		t.Errorf("Permissions = %v, want [%s]", retired.Permissions, retiredPermission)
	}

	// After the repair the signal clears.
	if _, err := svc.ReconcileBuiltInRoles(ctx, tenantID); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	repaired, err := svc.GetRole(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("GetRole after reconcile: %v", err)
	}
	if repaired.RetiredPermissionError() != nil {
		t.Errorf("drift still reported after reconcile: %v", repaired.RetiredPermissions)
	}
}

// TestRetiredPermissions_CleanRoleReportsNothing is the negative case: a role
// whose permissions are all registered must produce no signal and no error.
func TestRetiredPermissions_CleanRoleReportsNothing(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "clean",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet, "keys:*"},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	got, err := svc.GetRole(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if len(got.RetiredPermissions) != 0 {
		t.Errorf("RetiredPermissions = %v, want none", got.RetiredPermissions)
	}
	if err := got.RetiredPermissionError(); err != nil {
		t.Errorf("RetiredPermissionError() = %v, want nil", err)
	}
}

// TestRetiredPermissions_IgnoresWildcards: a whole-segment wildcard has no
// registry entry to lose, so it must never be read as drift — otherwise every
// role holding "keys:*" would be reported as broken and reconciliation would
// look required on a healthy cluster.
func TestRetiredPermissions_IgnoresWildcards(t *testing.T) {
	got := RetiredPermissions([]permission.Permission{"keys:*", permission.KVGet, retiredPermission})
	if len(got) != 1 || got[0] != retiredPermission {
		t.Errorf("RetiredPermissions = %v, want [%s]", got, retiredPermission)
	}
}

// TestRetiredPermissionError_NilRole covers the zero-value guard.
func TestRetiredPermissionError_NilRole(t *testing.T) {
	var r *Role
	if err := r.RetiredPermissionError(); err != nil {
		t.Errorf("nil role reported %v, want nil", err)
	}
}

// TestRetiredPermissionError_Message checks the message names the role, the
// tenant and every retired verb — an operator reading a log line must not have
// to cross-reference an id to learn what drifted.
func TestRetiredPermissionError_Message(t *testing.T) {
	err := &RetiredPermissionError{
		RoleID:      "id-1",
		Name:        RoleNameAdmin,
		TenantID:    "acme",
		Permissions: []permission.Permission{"sdlc:register", "sdlc:update"},
	}
	msg := err.Error()
	for _, want := range []string{RoleNameAdmin, "acme", "sdlc:register", "sdlc:update", "2"} {
		if !bytes.Contains([]byte(msg), []byte(want)) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// TestMarshalRole_NeverPersistsDerivedDrift is the structural guarantee behind
// the derived field. GetRole populates RetiredPermissions and UpdateRole writes
// back the very value GetRole returned, so without the single marshal seam a
// snapshot of one build's registry would be persisted into the record and the
// next build would read it back as if it were stored grant data.
func TestMarshalRole_NeverPersistsDerivedDrift(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	created, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        "custom",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	// Force drift onto the in-memory value, then take the ordinary update path.
	loaded, err := svc.GetRole(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	loaded.RetiredPermissions = []permission.Permission{retiredPermission}
	if err := (&kvRoleStore{store: kv}).putRole(ctx, loaded, "test"); err != nil {
		t.Fatalf("putRole: %v", err)
	}

	raw, err := kv.Get(ctx, primaryKey(created.ID))
	if err != nil {
		t.Fatalf("raw Get: %v", err)
	}
	if bytes.Contains(raw, []byte("retiredPermissions")) {
		t.Errorf("stored record carries the derived field: %s", raw)
	}

	// And an ordinary rename, which marshals the value GetRole returned.
	if _, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: created.ID, Name: "custom2"}); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	raw, err = kv.Get(ctx, primaryKey(created.ID))
	if err != nil {
		t.Fatalf("raw Get after update: %v", err)
	}
	if bytes.Contains(raw, []byte("retiredPermissions")) {
		t.Errorf("update persisted the derived field: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// Report helpers
// ---------------------------------------------------------------------------

// TestReconcileReport_Changed covers the report predicates, including the nil
// receiver a caller gets from a failed call.
func TestReconcileReport_Changed(t *testing.T) {
	var nilReport *ReconcileReport
	if nilReport.Changed() {
		t.Error("nil report reports a change")
	}
	if (&ReconcileReport{TenantID: "acme"}).Changed() {
		t.Error("empty report reports a change")
	}
	changed := &ReconcileReport{Roles: []RoleReconcileResult{{Name: RoleNameAdmin, Created: true}}}
	if !changed.Changed() {
		t.Error("populated report reports no change")
	}
}

// TestRoleReconcileResult_Changed covers each of the three ways a result can
// record a change, and the empty case.
func TestRoleReconcileResult_Changed(t *testing.T) {
	cases := map[string]struct {
		result RoleReconcileResult
		want   bool
	}{
		"empty":   {RoleReconcileResult{Name: RoleNameAdmin}, false},
		"created": {RoleReconcileResult{Created: true}, true},
		"added":   {RoleReconcileResult{Added: []permission.Permission{permission.KVGet}}, true},
		"removed": {RoleReconcileResult{Removed: []permission.Permission{permission.KVGet}}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.result.Changed(); got != tc.want {
				t.Errorf("Changed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPermissionSetDiff covers the set semantics: order is meaningless in a
// grant and a repeated permission grants nothing extra.
func TestPermissionSetDiff(t *testing.T) {
	have := []permission.Permission{permission.KVGet, permission.KVPut, permission.KVGet}
	want := []permission.Permission{permission.KVPut, permission.KVDelete}

	added, removed := permissionSetDiff(have, want)
	if len(added) != 1 || added[0] != permission.KVDelete {
		t.Errorf("added = %v, want [%s]", added, permission.KVDelete)
	}
	if len(removed) != 1 || removed[0] != permission.KVGet {
		t.Errorf("removed = %v, want [%s]", removed, permission.KVGet)
	}

	added, removed = permissionSetDiff(have, have)
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("identical sets diffed as +%v -%v", added, removed)
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

// TestRoutingReconcileBuiltInRoles_RoutesToTenantStore: a barrier-isolated
// tenant's built-ins must be written to that tenant's OWN store, never the
// shared one — the reconcile write is a role record like any other.
func TestRoutingReconcileBuiltInRoles_RoutesToTenantStore(t *testing.T) {
	shared := newFakeKVStore()
	tenantStore := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, tenants: map[string]kvstore.KVStore{"secure": tenantStore}}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)
	report, err := svc.ReconcileBuiltInRoles(ctx, "secure")
	if err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	if report.TenantID != "secure" {
		t.Errorf("TenantID = %q, want secure", report.TenantID)
	}
	if len(report.Roles) != len(BuiltInRoles("secure")) {
		t.Fatalf("report covers %d roles, want %d", len(report.Roles), len(BuiltInRoles("secure")))
	}
	if len(tenantStore.m) == 0 {
		t.Fatal("reconciliation did not write to the tenant's own store")
	}

	// The shared store may hold only identity-index entries, never a role
	// record: the same containment the create path is held to.
	const identityIndexPrefix = "__sys:identity_idx/tenant/"
	for k := range shared.m {
		if !strings.HasPrefix(k, identityIndexPrefix) {
			t.Fatalf("shared store holds non-index key %q (leak)", k)
		}
	}
}

// TestRoutingReconcileBuiltInRoles_CrossTenantDenied: a normal tenant caller
// may converge only its own tenant. Reconciliation writes system roles, so a
// caller who could aim it at another tenant could rewrite that tenant's grants.
func TestRoutingReconcileBuiltInRoles_CrossTenantDenied(t *testing.T) {
	svc, _, storeB := crossTenantSetup(t)
	actx := tenant.WithTenantID(context.Background(), "a")

	report, err := svc.ReconcileBuiltInRoles(actx, "b")
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("ReconcileBuiltInRoles(b) = (%+v, %v), want CrossTenantDeniedError", report, err)
	}
	if denied.IdentityTenantID != "a" || denied.RequestedTenantID != "b" {
		t.Errorf("error tenants = (%q,%q), want (a,b)", denied.IdentityTenantID, denied.RequestedTenantID)
	}
	if n := len(storeB.m); n != 0 {
		t.Fatalf("tenant B store holds %d records after a denied reconcile", n)
	}
}

// TestRoutingReconcileBuiltInRoles_SealedTenantFailsClosed: a sealed barrier
// tenant must not fall back to the shared store.
func TestRoutingReconcileBuiltInRoles_SealedTenantFailsClosed(t *testing.T) {
	shared := newFakeKVStore()
	resolver := &fakeResolver{shared: shared, sealed: map[string]bool{"sealed": true}}
	svc, err := NewRoutingService(resolver)
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)
	if _, err := svc.ReconcileBuiltInRoles(ctx, "sealed"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ReconcileBuiltInRoles(sealed) = %v, want ErrTenantSealed", err)
	}
	if len(shared.m) != 0 {
		t.Fatalf("sealed reconcile wrote %d records to the shared store", len(shared.m))
	}
}
