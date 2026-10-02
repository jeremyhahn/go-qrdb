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

	// TenantsListDeleted grants access to enumerate retired tenant ids and
	// their deletion records. A retirement is a platform-wide record naming
	// ids no tenant may ever use again, so it is reserved for the system
	// tenant: a tenant-local caller has no claim on another tenant's history.
	TenantsListDeleted Permission = "tenants:list_deleted"

	// TenantsReconcileDeleted grants access to run the deleted-tenant
	// reconcile on a node: remove, from that node's disk, the directories of
	// retired tenant ids. It acts on the node's storage across every tenant,
	// so it is a platform-operator act reserved for the system tenant.
	TenantsReconcileDeleted Permission = "tenants:reconcile_deleted"

	// TenantsApprove grants access to approve a pending tenant
	// registration. Reserved for the system tenant — tenant-local admins
	// cannot approve other tenants' registrations.
	TenantsApprove Permission = "tenants:approve"

	// CertsRevokeCross grants the CA-operator revocation: writing a
	// cluster-wide deny marker for a serial this node holds NO issued
	// record for. Reserved for the system tenant.
	//
	// This is the authority behind deny-by-serial (see
	// docs/certificate-authority/architecture.md). A serial that resolves to
	// no record names no tenant, so no tenant-local caller can show the
	// certificate is theirs — "revoke another tenant's serial" and "revoke a
	// serial issued by a peer" are the same request on the wire, and the
	// marker IsRevoked reads lives in one namespace shared by every tenant.
	// The CA is a platform asset held by a single process-wide provider, so
	// acting on serials it cannot attribute is a platform-operator act.
	//
	// CertsRevoke remains the ordinary, self-scope verb: it revokes a serial
	// whose issued record names the caller's own tenant, which is the case
	// every tenant admin actually has.
	CertsRevokeCross Permission = "certs:revoke:cross"
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
//
// OWNERSHIP (owner ruling 2026-09-01, company doc 08 §"Registration
// model"): the policy record itself lives in the composing host —
// go-quicbaas's `iam.self_service_registration` config section is the one
// canonical holder for the whole stack. qrdb deliberately implements NO
// registration-config store or service; it owns only the permission
// vocabulary. These permissions gate the host's registration-config
// surface, and `tenants:approve` (above) gates the host's pending-tenant
// approval surface — the pending-tenant queue is likewise host state, not
// a qrdb entity.
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
		TenantsListDeleted,
		TenantsReconcileDeleted,
		TenantsApprove,
		CertsRevokeCross,
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
