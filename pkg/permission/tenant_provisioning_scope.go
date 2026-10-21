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

package permission

// ---------------------------------------------------------------------------
// Tenant provisioning permissions
//
// Creating a tenant and setting its plan -- its resource quotas, its feature
// entitlements and its isolation tier -- are PLATFORM-OPERATOR operations.
// A tenant's Admin manages everything inside its own tenant, but it does not
// get to raise its own plan limits, grant itself entitlements, move itself to
// a different isolation tier, or mint new tenants.
//
// They are tagged ScopeCross, so the RBAC authorizer admits them only for a
// system-tenant identity, whatever tenant the request names, and refuses them
// to every tenant-local identity. The built-in Admin role carries them in
// every tenant, like its other cross-scope grants (cross_scope.go); only
// Admin in the system tenant can exercise them.
//
// Until this file they were ScopeSelf, whose system-tenant lift admitted the
// platform operator and whose own-tenant rule admitted every tenant Admin on
// itself -- so a tenant Admin could, for example, lift its own quotas.
//
// tenants:update stays ScopeSelf: it covers only what a tenant may edit on
// itself (its display name). A request that also changes the isolation tier
// must hold tenants:set_isolation (see AuthMiddleware.AuthorizeTenantUpdate).
//
// The permission constants are declared in constants.go (and registered in
// the global permission registry there); this file only refines their scope.
// ---------------------------------------------------------------------------

// tenantProvisioningPermissions returns the platform-operator tenant
// permissions as a single slice.
func tenantProvisioningPermissions() []Permission {
	return []Permission{
		TenantsCreate,
		TenantsSetQuotas,
		TenantsSetEntitlements,
		TenantsSetIsolation,
		TenantsSetStatus,
	}
}

func init() {
	RegisterScope(ScopeCross, tenantProvisioningPermissions()...)
}
