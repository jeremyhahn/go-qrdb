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
// Cross-tenant permissions
//
// All permissions declared in this file are tagged ScopeCross at init time.
// They are reserved for identities whose TenantID equals SystemTenantID;
// the RBAC authorizer rejects them when held by a tenant-local identity.
// Existing self-scope tenant permissions (TenantsRead/Update/Delete/List)
// remain in constants.go and continue to apply to the identity's own tenant.
// ---------------------------------------------------------------------------

const (
	// TenantsListCross grants access to list every tenant on the platform.
	// Reserved for the system tenant; tenant-local Admin uses TenantsList
	// (self-scope) which is constrained to their own tenant.
	TenantsListCross Permission = "tenants:list:cross"

	// TenantsCreateCross grants access to create a new tenant outside the
	// caller's own scope (for example, an admin in the system tenant
	// provisioning a new customer tenant). Reserved for the system tenant.
	TenantsCreateCross Permission = "tenants:create:cross"

	// TenantsApprove grants access to approve a pending tenant
	// registration. Reserved for the system tenant — tenant-local admins
	// cannot approve other tenants' registrations.
	TenantsApprove Permission = "tenants:approve"

	// AuditReadCross grants access to read audit events across every
	// tenant on the platform. Reserved for the system tenant; the
	// per-tenant AuditRead permission constrains tenant-local auditors to
	// their own tenant's audit log.
	AuditReadCross Permission = "audit:read:cross"
)

// ---------------------------------------------------------------------------
// VolumePool permissions
//
// Volume pools are platform-level storage placement primitives. They
// allocate per-tenant barrier backends from a shared pool of paths or
// cloud locations. Pool management is therefore a system-tenant
// responsibility — no tenant-local Admin should be able to create or
// modify the pool that places another tenant's data.
// ---------------------------------------------------------------------------

const (
	// VolumePoolCreate grants access to register a new volume pool.
	VolumePoolCreate Permission = "volumepool:create"

	// VolumePoolList grants access to list configured volume pools.
	VolumePoolList Permission = "volumepool:list"

	// VolumePoolRead grants access to read the configuration and
	// utilization metrics of a single volume pool.
	VolumePoolRead Permission = "volumepool:read"

	// VolumePoolUpdate grants access to mutate a volume pool's backend set
	// or strategy.
	VolumePoolUpdate Permission = "volumepool:update"

	// VolumePoolDelete grants access to remove a volume pool. Tenants
	// referencing the pool must be re-homed first; the operation rejects
	// when references remain.
	VolumePoolDelete Permission = "volumepool:delete"
)

// ---------------------------------------------------------------------------
// Registration configuration permissions
//
// The self-service registration policy (enabled flag, default role,
// approval requirement, allowed isolation modes) is platform-wide
// configuration. Only the system tenant may read or mutate it.
// ---------------------------------------------------------------------------

const (
	// RegistrationConfigRead grants access to read the platform-wide
	// self-service registration policy.
	RegistrationConfigRead Permission = "registration_config:read"

	// RegistrationConfigUpdate grants access to mutate the platform-wide
	// self-service registration policy.
	RegistrationConfigUpdate Permission = "registration_config:update"
)

// crossScopePermissions returns every permission declared in this file as
// a single slice. The init() function below registers each permission in
// the global registry and tags it ScopeCross.
func crossScopePermissions() []Permission {
	return []Permission{
		TenantsListCross,
		TenantsCreateCross,
		TenantsApprove,
		AuditReadCross,
		VolumePoolCreate,
		VolumePoolList,
		VolumePoolRead,
		VolumePoolUpdate,
		VolumePoolDelete,
		RegistrationConfigRead,
		RegistrationConfigUpdate,
	}
}

func init() {
	perms := crossScopePermissions()
	Register(perms...)
	RegisterScope(ScopeCross, perms...)
}
