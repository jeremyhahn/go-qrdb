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

package rbac

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// orchestrator:health / orchestrator:status — node state, not tenant material
//
// These two describe the DAEMON'S OWN orchestrator. They were bulk-registered
// ScopeTenantOnly with the rest of the broker data-plane family, which denied
// them to the system tenant STRUCTURALLY: the scope check runs after the grant
// check, so no role — not admin, not owner — could satisfy it, and the
// operator persona was the single persona that could not see whether the node
// it runs was alive (go-qrdb#183, owner-ruled 2026-09-02).
//
// The permission-package tests pin the SCOPE. These pin the AUTHORIZATION
// OUTCOME, which is what was actually measured as broken: a 403 on
// GET /api/v1/orchestrator/health for the built-in admin over mTLS in tenant
// `system` with roles admin+owner.
// ---------------------------------------------------------------------------

// nodeStateVerbs is the pair under test, as (resource, action).
var nodeStateVerbs = []struct {
	perm     permission.Permission
	resource string
	action   string
}{
	{permission.OrchestratorHealth, "orchestrator", "health"},
}

// The platform operator can now read the node's own orchestrator state. This
// is the exact call that returned 403 in the measurement on go-qrdb#183.
func TestAuthorize_NodeState_SystemAdmin_CanReadOrchestratorState(t *testing.T) {
	for _, v := range nodeStateVerbs {
		t.Run(string(v.perm), func(t *testing.T) {
			svc := newMockRoleService(map[string][]permission.Permission{
				"admin-role": {v.perm},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "platform-admin",
				TenantID: tenantid.System,
				Roles:    []string{"admin-role"},
			}

			if err := authz.Authorize(ctxFor(tenantid.System), identity, v.resource, v.action); err != nil {
				t.Fatalf("Admin@system must be able to read the node's own "+
					"orchestrator %s, got: %v", v.action, err)
			}
		})
	}
}

// ScopeSelf, not ScopeCross: the system-tenant rule lifts these across
// tenants for a SYSTEM member, which is the property that unblocks the
// operator without widening anything for a tenant-local principal.
func TestAuthorize_NodeState_SystemAdmin_LiftsAcrossTenants(t *testing.T) {
	for _, v := range nodeStateVerbs {
		t.Run(string(v.perm), func(t *testing.T) {
			svc := newMockRoleService(map[string][]permission.Permission{
				"admin-role": {v.perm},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "platform-admin",
				TenantID: tenantid.System,
				Roles:    []string{"admin-role"},
			}

			// Superseded (owner ruling, 2026-10-01): "the inSystem || arm ...
			// is deleted under every policy" leaves no exception for
			// orchestrator:health. #183's fix survives unaffected — it reads
			// through on the operator's OWN tenant context
			// (TestAuthorize_NodeState_SystemAdmin_CanReadOrchestratorState),
			// which is the call pattern #183 actually measured. A request
			// whose ambient context names a DIFFERENT tenant than the
			// caller's own is refused like any other ScopeSelf permission.
			for _, target := range []string{"acme", "bob-corp"} {
				if err := authz.Authorize(ctxFor(target), identity, v.resource, v.action); err == nil {
					t.Errorf("target %q: Admin@system should NOT be admitted on a different tenant's context (no system lift)", target)
				}
			}
		})
	}
}

// NOTHING WIDENS FOR A TENANT PRINCIPAL. A tenant-local holder still reaches
// only its own tenant — this is what distinguishes ScopeSelf from ScopeCross
// and is the half a scope relaxation most easily gets wrong.
func TestAuthorize_NodeState_TenantAdmin_ConfinedToOwnTenant(t *testing.T) {
	for _, v := range nodeStateVerbs {
		t.Run(string(v.perm), func(t *testing.T) {
			svc := newMockRoleService(map[string][]permission.Permission{
				"admin-role": {v.perm},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "acme-admin",
				TenantID: "acme",
				Roles:    []string{"admin-role"},
			}

			if err := authz.Authorize(ctxFor("acme"), identity, v.resource, v.action); err != nil {
				t.Fatalf("Admin@acme must reach its OWN tenant, got: %v", err)
			}

			err := authz.Authorize(ctxFor("bob-corp"), identity, v.resource, v.action)
			if err == nil {
				t.Fatal("Admin@acme must NOT reach bob-corp: ScopeSelf confines a " +
					"tenant-local identity to its own tenant")
			}
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
			}
		})
	}
}

// The grant check still runs. Relaxing the SCOPE must not make these verbs
// reachable without the permission — a scope is not a grant.
func TestAuthorize_NodeState_WithoutTheGrant_IsStillDenied(t *testing.T) {
	for _, v := range nodeStateVerbs {
		t.Run(string(v.perm), func(t *testing.T) {
			svc := newMockRoleService(map[string][]permission.Permission{
				"reader-role": {permission.KVGet},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "platform-reader",
				TenantID: tenantid.System,
				Roles:    []string{"reader-role"},
			}

			if err := authz.Authorize(ctxFor(tenantid.System), identity, v.resource, v.action); err == nil {
				t.Fatal("a principal without the permission must still be denied: " +
					"the scope change must not bypass the grant check")
			}
		})
	}
}

// The REST of the family is untouched. go-qrdb#183 made no claim about the
// verbs that act on tenant-owned sessions, jobs, tasks and artifacts, and a
// wider re-scoping was offered to the owner and declined — so the system
// tenant must STILL be denied those.
func TestAuthorize_NodeState_RestOfFamilyStillDeniedToSystemTenant(t *testing.T) {
	neighbours := []struct {
		perm     permission.Permission
		resource string
		action   string
	}{
		// orchestrator:status is in this list, not the exception list: it is
		// documented as SESSION status and stays tenant-only. See
		// nodeStateScopePermissions.
		{permission.OrchestratorStatus, "orchestrator", "status"},
		{permission.OrchestratorSpawn, "orchestrator", "spawn"},
		{permission.OrchestratorTaskRead, "orchestrator", "task_read"},
		{permission.OrchestratorArtifactRead, "orchestrator", "artifact_read"},
	}
	for _, v := range neighbours {
		t.Run(string(v.perm), func(t *testing.T) {
			svc := newMockRoleService(map[string][]permission.Permission{
				"admin-role": {v.perm},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "platform-admin",
				TenantID: tenantid.System,
				Roles:    []string{"admin-role"},
			}

			err := authz.Authorize(ctxFor(tenantid.System), identity, v.resource, v.action)
			if err == nil {
				t.Fatalf("%q must remain denied to the system tenant: the #183 fix "+
					"is scoped to health and status only", v.perm)
			}
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
			}
		})
	}
}
