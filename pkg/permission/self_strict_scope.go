// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package permission

// ---------------------------------------------------------------------------
// Strictly self-scoped permissions
//
// Every permission registered here is tagged ScopeSelfStrict: admitted only
// when the identity's tenant is the request's target tenant, for EVERY
// identity. The system-tenant rule does not lift it, and unlike
// ScopeTenantOnly the system tenant is not denied either -- it is confined to
// its own tenant, exactly as every other tenant is.
//
// Reach for this when a permission acts on material each tenant owns for
// itself AND the system tenant legitimately owns some of that material too.
// ScopeTenantOnly is the right answer when the platform operator must never
// hold the material at all (per-tenant barrier key custody); ScopeSelfStrict
// is the right answer when it holds its own.
//
// PIN EACH MEMBER, NOT THE FAMILY -- the same rule the sibling scope files
// carry. A family helper that later gains a member would tag that member
// silently, and the gate would stay green while a permission nobody scoped
// slipped in on someone else's decision.
// ---------------------------------------------------------------------------

// selfStrictPermissions returns every strictly self-scoped permission as a
// single slice. The init() function below registers each permission in the
// global registry and tags it ScopeSelfStrict.
func selfStrictPermissions() []Permission {
	return []Permission{
		// escrow
		//
		// Escrow hands wrapped key material to a custodian so it can be
		// reconstructed outside the module that created it. Each tenant
		// escrows and recovers its own keys; the system tenant does the same
		// for its own, and reaching another tenant's escrowed material would
		// be a cross-tenant escalation wearing an admin role. These were
		// ScopeSelf -- the system tenant could recover any tenant's keys.
		EscrowCreate,
		EscrowList,
		EscrowRecover,
		EscrowRevoke,
	}
}

func init() {
	perms := selfStrictPermissions()
	Register(perms...)
	RegisterScope(ScopeSelfStrict, perms...)
}
