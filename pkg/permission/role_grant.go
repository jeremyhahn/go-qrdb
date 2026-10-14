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

import (
	"sort"
	"sync"
)

// ---------------------------------------------------------------------------
// Built-in role default-grant registry
//
// Role placement is metadata carried by the permission, not a parallel list
// maintained by the role package. A permission declares, at registration
// time, which of the canonical built-in roles receive it by default; the
// role package's built-in permission sets (pkg/role.BuiltInRoles) are a
// lazy projection of this registry via RolePermissions rather than a
// hand-maintained duplicate. This mirrors the existing Scope mechanism
// (RegisterScope / LookupScope) rather than introducing a third parallel
// registration path.
//
// The five canonical role names below are untyped string constants so that
// pkg/role's RoleName* constants (which are plain strings, e.g. used as
// role.Role.Name and passed to SystemRoleID) can alias them directly with
// no type conversion — there is exactly one literal definition of each
// name, here.
// ---------------------------------------------------------------------------

const (
	// RoleSO is the Security Officer built-in role name. The SO manages
	// module lifecycle and governance and may hold ONLY permissions declared
	// DutyGovernance — see RegisterRoleGrant.
	RoleSO = "so"

	// RoleAdmin is the Admin built-in role name. Admins manage operational
	// key lifecycle and tenant configuration.
	RoleAdmin = "admin"

	// RoleOperator is the Operator built-in role name. Operators perform
	// routine cryptographic operations, maintenance, and are the non-admin
	// humans who use the broker data-plane services day to day.
	RoleOperator = "operator"

	// RoleUser is the User built-in role name. Users perform cryptographic
	// operations using existing keys and consume the data-plane services.
	RoleUser = "user"

	// RoleAuditor is the Auditor built-in role name. Auditors have
	// read-only access to audit logs and system state.
	RoleAuditor = "auditor"
)

var (
	roleGrantMu sync.RWMutex

	// roleGrantRegistry maps a permission to the set of built-in role names
	// that hold it by default. A permission may be granted to more than one
	// role (e.g. a read permission shared by Operator and Auditor), so the
	// value is a set rather than a single role.
	roleGrantRegistry = map[Permission]map[string]struct{}{}
)

// assertSOGrantable panics unless p may be held by the Security Officer role
// under FIPS 140-3 AS10.03. Three things disqualify a permission, each with its
// own diagnosis:
//
//   - A whole-segment wildcard, which cannot be classified at all and would
//     expand onto cryptographic operations at authorization time
//     (*WildcardGrantError).
//   - A cryptographic operation — declared DutyCryptoUse, or carrying an
//     unambiguously cryptographic action verb in any resource family
//     (*SoDViolationError). This is the invariant itself.
//   - Anything else that is not declared DutyGovernance, including a permission
//     nobody has classified (*UndeclaredDutyError). This is the fail-safe:
//     silence is a denial, not a permission.
func assertSOGrantable(p Permission) {
	if IsWildcard(p) {
		panic(&WildcardGrantError{Role: RoleSO, Permission: p})
	}
	if IsCryptoUse(p) {
		panic(&SoDViolationError{Role: RoleSO, Permission: p})
	}
	if LookupDuty(p) != DutyGovernance {
		panic(&UndeclaredDutyError{Role: RoleSO, Permission: p})
	}
}

// RegisterRoleGrant records that each permission in perms is granted by
// default to the named built-in role. It is safe for concurrent use and is
// typically called from init() functions alongside Register and
// RegisterScope, but may also be called at runtime by a higher layer (e.g.
// go-quicbaas, go-xkms) before role seeding to place a contributed
// permission into a built-in role — a permission that is registered but
// never given a role grant is reachable only through a hand-authored
// custom role.
//
// SoD invariant (FIPS 140-3 AS10.03): when role == RoleSO, RegisterRoleGrant
// admits ONLY permissions declared DutyGovernance (see RegisterDuty). It
// panics on a cryptographic permission (*SoDViolationError), on a permission
// whose duty its author never declared (*UndeclaredDutyError), and on a
// whole-segment wildcard (*WildcardGrantError). The invariant is therefore
// structural in both directions: a contributor cannot violate it by
// registering a bad grant, and cannot slip past it by registering an
// unclassified one — from this package or from a higher layer through the
// sdk/go re-export. "Cryptographic" is not limited to the keys:* family: it
// covers every resource whose action names a cryptographic primitive
// (including a higher layer's, e.g. go-quicbaas's attestation:attest) plus
// every permission explicitly declared DutyCryptoUse (keys:verify, piv:use).
//
// The runtime RBAC deny-list in pkg/auth/rbac remains as defense-in-depth for
// any grant that reaches a role by a path other than this function — a
// hand-authored custom role named "so", or a role store written by an earlier
// build — and is an independent mechanism: it classifies the requested
// resource:action itself rather than consulting the duty registry. A test in
// that package proves the two agree.
//
// A panic is deliberate here, not an error return: this is a package-init-
// time or bootstrap-time programmer error with no reasonable recovery — the
// same posture Go's standard library takes for MustCompile-style invariant
// violations discovered before main() runs.
func RegisterRoleGrant(role string, perms ...Permission) {
	if role == RoleSO {
		for _, p := range perms {
			assertSOGrantable(p)
		}
	}

	roleGrantMu.Lock()
	defer roleGrantMu.Unlock()
	for _, p := range perms {
		set, ok := roleGrantRegistry[p]
		if !ok {
			set = map[string]struct{}{}
			roleGrantRegistry[p] = set
		}
		set[role] = struct{}{}
	}
}

// RolePermissions returns every permission registered with a default grant
// for the named built-in role, sorted. It is evaluated against the live
// registry on every call (not memoized at package-init time), so a
// permission registered by a later-loaded package — including one
// registered at runtime by a higher layer before role seeding — is
// reflected the next time RolePermissions is called. Callers that
// materialize built-in roles (pkg/role.BuiltInRoles) must call this at
// role-seed time, not cache its result at package-init time, or they will
// miss late contributions.
func RolePermissions(role string) []Permission {
	roleGrantMu.RLock()
	defer roleGrantMu.RUnlock()
	var out []Permission
	for p, roles := range roleGrantRegistry {
		if _, ok := roles[role]; ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// HasRoleGrant reports whether p is registered with a default grant for the
// named built-in role.
func HasRoleGrant(role string, p Permission) bool {
	roleGrantMu.RLock()
	defer roleGrantMu.RUnlock()
	roles, ok := roleGrantRegistry[p]
	if !ok {
		return false
	}
	_, granted := roles[role]
	return granted
}
