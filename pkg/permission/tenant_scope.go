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
// Tenant-scoped permissions
//
// The permissions registered here are tagged ScopeTenantOnly at init time.
// Unlike ScopeSelf permissions — which the system-tenant rule lifts across
// all tenants — a ScopeTenantOnly permission is admitted only when the
// identity is NOT in the system tenant AND the identity's tenant matches
// the request's target tenant. They encode "tenant-admin-only,
// system-admin-denied, no cross-tenant" semantics.
//
// The per-tenant barrier operations belong here: the per-tenant barrier
// custodies tenant-owned key material. A platform (system) operator must
// not be able to initialize, unseal, or seal another tenant's barrier, and
// a tenant admin must not be able to act on a tenant other than their own.
// ---------------------------------------------------------------------------

// tenantOnlyPermissions returns every strictly tenant-local permission as a
// single slice. The init() function below registers each permission in the
// global registry and tags it ScopeTenantOnly.
func tenantOnlyPermissions() []Permission {
	return []Permission{
		TenantBarrierInit,
		TenantBarrierUnseal,
		TenantBarrierSeal,
		TenantBarrierStatus,
		TenantBarrierOfficerRegister,
		TenantBarrierOfficerList,
		TenantBarrierRekey,
		TenantBarrierOfficerRevoke,
	}
}

func init() {
	perms := tenantOnlyPermissions()
	Register(perms...)
	RegisterScope(ScopeTenantOnly, perms...)
}
