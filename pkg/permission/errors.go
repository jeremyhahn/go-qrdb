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

import "errors"

// ErrNilStoreResolver is returned by NewTenantScopeStore when the supplied
// StoreResolver is nil.
var ErrNilStoreResolver = errors.New("permission: nil store resolver")

// InvalidPermissionError is returned when a permission string does not
// conform to the required "resource:action" format.
type InvalidPermissionError struct {
	Value string
}

// Error returns a human-readable description of the malformed permission.
func (e *InvalidPermissionError) Error() string {
	return "permission: invalid format: " + e.Value + " (expected resource:action)"
}

// UnregisteredPermissionError is returned when an operation references a
// permission that has not been registered in the global registry.
type UnregisteredPermissionError struct {
	Permission Permission
}

// Error returns a human-readable description of the unregistered permission.
func (e *UnregisteredPermissionError) Error() string {
	return "permission: not registered: " + string(e.Permission)
}

// InvalidScopeError is returned by ParseScope when a stored scope label is
// not one of the recognized scope constants. It signals a corrupt or
// forward-incompatible persisted value; callers fail closed to ScopeSelf.
type InvalidScopeError struct {
	Value string
}

// Error returns a human-readable description of the invalid scope label.
func (e *InvalidScopeError) Error() string {
	return "permission: invalid scope label: " + e.Value
}

// UndeclaredScopeError is returned by LookupScope when a permission has no
// entry in the built-in scope registry. It exists so that an omission is a
// refusal rather than an admission: before it, LookupScope answered ScopeSelf
// for any untagged permission, which made "nobody has decided this
// permission's tenancy reach" indistinguishable from "confined to the caller's
// own tenant" — and the undecided one is the one that admitted (go-qrdb#474,
// where certs:revoke reached every tenant through exactly that default).
//
// The RBAC authorizer denies on any scope-resolution error, so a permission
// that reaches production undeclared is refused at every call, loudly, with
// its name in the reason — and TestEveryPermissionDeclaresItsScope fails the
// build before it can get there.
type UndeclaredScopeError struct {
	Permission Permission
}

// Error returns a human-readable description naming the undeclared permission.
func (e *UndeclaredScopeError) Error() string {
	return "permission: no scope declared for " + string(e.Permission) + " (register it in self_scope.go, tenant_scope.go or cross_scope.go)"
}

// EmptyPermissionError is returned by TenantScopeStore operations when an
// empty permission name is supplied. A per-tenant scope definition must
// name a non-empty permission.
type EmptyPermissionError struct{}

// Error returns a human-readable description of the empty-permission error.
func (e *EmptyPermissionError) Error() string {
	return "permission: empty permission name"
}

// ScopeDecodeError wraps a failure to decode a persisted per-tenant scope
// definition. It carries the offending permission and the underlying
// cause. Resolution fails closed when this occurs.
type ScopeDecodeError struct {
	Permission Permission
	Err        error
}

// Error returns a human-readable description of the decode failure.
func (e *ScopeDecodeError) Error() string {
	return "permission: decode scope for " + string(e.Permission) + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause for errors.Is / errors.As.
func (e *ScopeDecodeError) Unwrap() error {
	return e.Err
}

// SoDViolationError is raised by RegisterRoleGrant when a caller attempts to
// grant a permission that performs a cryptographic operation with key material
// (see IsCryptoUse) to the Security Officer role, violating the FIPS 140-3
// AS10.03 separation-of-duties invariant. It is panicked rather than returned:
// the violation can only originate from a package init() or an explicit
// registration call made before role seeding, both of which are programmer
// errors with no reasonable recovery path.
type SoDViolationError struct {
	Role       string
	Permission Permission
}

// Error returns a human-readable description of the separation-of-duties
// violation.
func (e *SoDViolationError) Error() string {
	return "permission: FIPS 140-3 AS10.03 violation: role " + e.Role +
		" cannot be granted cryptographic permission " + string(e.Permission)
}

// UndeclaredDutyError is raised by RegisterRoleGrant when a caller attempts to
// grant the Security Officer role a permission whose separated duty its author
// never declared. It is the fail-safe half of the FIPS 140-3 AS10.03 guard: a
// permission does not become Security-Officer-grantable by nobody having
// classified it, so the omission surfaces as a registration-time panic naming
// the declaration the author must add.
type UndeclaredDutyError struct {
	Role       string
	Permission Permission
}

// Error returns a human-readable description of the missing declaration,
// including the call that resolves it.
func (e *UndeclaredDutyError) Error() string {
	return "permission: FIPS 140-3 AS10.03: role " + e.Role +
		" cannot be granted " + string(e.Permission) +
		": its duty is undeclared - declare RegisterDuty(DutyGovernance, " +
		string(e.Permission) + ") at the permission's declaration site if the " +
		"Security Officer is meant to hold it, or declare " +
		"RegisterDuty(DutyCryptoUse, ...) and drop the grant if it performs a " +
		"cryptographic operation"
}

// WildcardGrantError is raised by RegisterRoleGrant when a caller attempts to
// grant the Security Officer role a whole-segment wildcard permission (e.g.
// "keys:*"). A wildcard spans verbs of different duties by construction, so it
// cannot be classified, and granting one would sweep cryptographic operations
// onto the Security Officer through the RBAC authorizer's wildcard expansion.
// Other built-in roles may hold wildcards; the Security Officer must name every
// permission it holds.
type WildcardGrantError struct {
	Role       string
	Permission Permission
}

// Error returns a human-readable description of the rejected wildcard grant.
func (e *WildcardGrantError) Error() string {
	return "permission: FIPS 140-3 AS10.03: role " + e.Role +
		" cannot be granted wildcard permission " + string(e.Permission) +
		": a wildcard cannot be classified as governance or cryptographic use - " +
		"grant the concrete permissions instead"
}

// InvalidDutyError is raised by RegisterDuty when the supplied Duty is not one
// a caller may declare. DutyUndeclared is invalid on purpose: a permission is
// undeclared by omitting the RegisterDuty call, never by declaring it.
type InvalidDutyError struct {
	Duty Duty
}

// Error returns a human-readable description of the invalid duty.
func (e *InvalidDutyError) Error() string {
	return "permission: invalid duty: " + e.Duty.String() +
		" (declare DutyGovernance or DutyCryptoUse; omit the call for undeclared)"
}

// DutyConflictError is raised by RegisterDuty when a permission that already
// has a declared duty is re-declared with a different one. Duty declarations
// are immutable: re-declaration would otherwise let a later caller relabel a
// cryptographic permission as governance and hand it to the Security Officer.
type DutyConflictError struct {
	Permission Permission
	Existing   Duty
	Requested  Duty
}

// Error returns a human-readable description of the conflicting declaration.
func (e *DutyConflictError) Error() string {
	return "permission: duty for " + string(e.Permission) + " is already declared as " +
		e.Existing.String() + " and cannot be re-declared as " + e.Requested.String()
}
