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

// The scopes the certificate-authority surface resolves to.
//
// Each certs permission's scope is a statement about which tenants it reaches,
// and that statement is invisible at the call site: it lives in a registry
// consulted at authorization time. These tests are where it is legible.

package permission

import "testing"

// TestCertsRevokeCross_IsCrossScope is the load-bearing registration. If this
// permission ever resolves to ScopeSelf, deny-by-serial becomes reachable by
// every tenant again. When this test was written an unregistered permission
// got ScopeSelf by default rather than failing, so the widening would have
// been silent; LookupScope now refuses an undeclared permission, and this
// test remains the statement of which scope the declaration must carry.
func TestCertsRevokeCross_IsCrossScope(t *testing.T) {
	if got := mustScope(t, CertsRevokeCross); got != ScopeCross {
		t.Fatalf("LookupScope(%s) = %v, want ScopeCross", CertsRevokeCross, got)
	}
	if !IsCrossScope(CertsRevokeCross) {
		t.Error("IsCrossScope = false for the CA-operator revocation permission")
	}
}

// TestCertsRevokeCross_IsRegisteredAndGranted keeps the permission reachable:
// a cross-scope permission no role grants is a permission nobody can exercise,
// which would take deny-by-serial away from the operator it was reserved for.
func TestCertsRevokeCross_IsRegisteredAndGranted(t *testing.T) {
	found := false
	for _, p := range All() {
		if p == CertsRevokeCross {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s is not in the permission registry", CertsRevokeCross)
	}

	if !HasRoleGrant(RoleAdmin, CertsRevokeCross) {
		t.Errorf("%s is granted to no built-in role, so no identity can exercise it", CertsRevokeCross)
	}
}

// TestCertsPermissions_AreSelfStrict pins the other half of the design. The
// ordinary certs verbs are ScopeSelfStrict: every tenant acts on its own
// certificates, and the system-tenant rule does NOT lift them, because a
// certificate minted for another tenant's user authenticates as that user.
// Promoting one to ScopeCross would take it away from every tenant admin;
// relaxing one to ScopeSelf would hand the system tenant every tenant's
// identities again.
//
// certs:read is included even though no handler audited here consumes it, so a
// future reader of that permission inherits a scope that was stated rather than
// defaulted into.
func TestCertsPermissions_AreSelfStrict(t *testing.T) {
	for _, p := range []Permission{
		CertsIssue, CertsEnroll, CertsRevoke, CertsList, CertsRead,
		TrustAnchorsAdd, TrustAnchorsList, TrustAnchorsRemove,
	} {
		if got := mustScope(t, p); got != ScopeSelfStrict {
			t.Errorf("LookupScope(%s) = %v, want ScopeSelfStrict", p, got)
		}
	}
}

// TestCertsDelegate_IsTenantOnly: a delegation lets a service act inside the
// tenant whose CA signs it, so only that tenant may give one. ScopeSelf would
// let the system tenant lift it into every tenant; ScopeTenantOnly denies the
// platform outright.
func TestCertsDelegate_IsTenantOnly(t *testing.T) {
	if got := mustScope(t, CertsDelegate); got != ScopeTenantOnly {
		t.Fatalf("LookupScope(%s) = %v, want ScopeTenantOnly", CertsDelegate, got)
	}
}

// TestCertsDelegate_GrantedToTheTenantsIdentityAuthorities: the Security
// Officer and the tenant admin issue identities, so they issue delegations;
// no other built-in role does, and the duty is governance.
func TestCertsDelegate_GrantedToTheTenantsIdentityAuthorities(t *testing.T) {
	for role, want := range map[string]bool{
		RoleSO: true, RoleAdmin: true, RoleOperator: false, RoleUser: false, RoleAuditor: false,
	} {
		if got := HasRoleGrant(role, CertsDelegate); got != want {
			t.Errorf("HasRoleGrant(%s, %s) = %v, want %v", role, CertsDelegate, got, want)
		}
	}
	if got := LookupDuty(CertsDelegate); got != DutyGovernance {
		t.Errorf("LookupDuty(%s) = %v, want DutyGovernance", CertsDelegate, got)
	}
}

// TestIsBuiltinRole names exactly the five built-in roles; a principal named
// for one would hold that whole role.
func TestIsBuiltinRole(t *testing.T) {
	for _, r := range []string{RoleSO, RoleAdmin, RoleOperator, RoleUser, RoleAuditor} {
		if !IsBuiltinRole(r) {
			t.Errorf("IsBuiltinRole(%q) = false", r)
		}
	}
	for _, r := range []string{"", "billing", "Admin", "admin "} {
		if IsBuiltinRole(r) {
			t.Errorf("IsBuiltinRole(%q) = true", r)
		}
	}
}

func TestCertsRevokeCross_SplitsIntoTheCertsResource(t *testing.T) {
	if got := Resource(CertsRevokeCross); got != "certs" {
		t.Errorf("Resource = %q, want %q", got, "certs")
	}
	if got := Action(CertsRevokeCross); got != "revoke:cross" {
		t.Errorf("Action = %q, want %q", got, "revoke:cross")
	}
	if rebuilt := Permission(Resource(CertsRevokeCross) + ":" + Action(CertsRevokeCross)); rebuilt != CertsRevokeCross {
		t.Errorf("resource+action round-trip = %q, want %q", rebuilt, CertsRevokeCross)
	}
}
