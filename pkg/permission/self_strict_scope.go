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
		// certs
		//
		// A certificate this CA signs authenticates as the user and tenant
		// its Subject names, so minting one for another tenant's user IS
		// logging in as that user. Each tenant generates, lists, revokes and
		// exports its own certificates; the system tenant does the same for
		// its own and for no one else's. These were ScopeSelf -- the
		// system-tenant rule let a system Admin mint a certificate for any
		// tenant's user. CertsRevokeCross, the deny-by-serial revocation of
		// a serial no tenant owns, stays ScopeCross (cross_scope.go).
		CertsExport,
		CertsGenerate,
		CertsList,
		CertsRead,
		CertsRevoke,

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

		// mvcc
		//
		// Compaction discards a database's revision history. The store keeps
		// a compaction floor per database namespace, so a compaction is the
		// owning tenant's act on its own history -- the system tenant's on
		// its own databases included -- and a lift onto another tenant's
		// would destroy history that tenant owns. It was ScopeCross while a
		// compaction discarded the whole store's history, which made the
		// platform operator the one identity able to destroy every tenant's.
		MVCCCompact,
		// databases (barrier)
		//
		// A database barrier is sealed, unsealed and inspected by the tenant
		// that owns the database, the system tenant for its own databases
		// included. These were ScopeSelf -- the system tenant passed the
		// authorizer for any tenant's database barrier.
		DatabasesSeal,
		DatabasesUnseal,
		DatabasesBarrierStatus,
	}
}

func init() {
	perms := selfStrictPermissions()
	Register(perms...)
	RegisterScope(ScopeSelfStrict, perms...)
}
