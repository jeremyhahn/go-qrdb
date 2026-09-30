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

package role

// RoleNotFoundError is returned when a role ID does not exist.
type RoleNotFoundError struct {
	ID string
}

// Error returns a human-readable description of the not-found condition.
func (e *RoleNotFoundError) Error() string {
	return "role: not found: " + e.ID
}

// RoleNameExistsError is returned when a role name is already taken
// within the tenant.
type RoleNameExistsError struct {
	Name     string
	TenantID string
}

// Error returns a human-readable description of the conflict.
func (e *RoleNameExistsError) Error() string {
	return "role: name already exists: " + e.Name + " in tenant " + e.TenantID
}

// RoleIDExistsError is returned when a caller-supplied role ID is already in
// use. CreateRoleRequest.ID travels from the REST and gRPC surfaces straight
// into the store, so a duplicate must be refused rather than overwriting the
// record it collides with — or, across tenants, taking over that id's entry
// in the global identity index and redirecting every by-id operation an
// administrator performs on it.
//
// TenantID is the CALLER's tenant, never the tenant already holding the id: a
// caller that supplied only an id must not learn where that id lives.
type RoleIDExistsError struct {
	ID       string
	TenantID string
}

// Error returns a human-readable description of the ID conflict.
func (e *RoleIDExistsError) Error() string {
	return "role: id already exists: " + e.ID + " in tenant " + e.TenantID
}

// SystemRoleImmutableError is returned when an operation would delete or
// rename a system role. Op names the refused operation in past tense
// ("deleted", "renamed") and defaults to the general "modified" so a zero
// value still reads correctly.
type SystemRoleImmutableError struct {
	ID string
	Op string
}

// Error returns a human-readable description of the immutability constraint.
func (e *SystemRoleImmutableError) Error() string {
	op := e.Op
	if op == "" {
		op = "modified"
	}
	return "role: system role cannot be " + op + ": " + e.ID
}

// ReservedRoleNameError is returned when a create would take a system role's
// name without being the canonical role that name denotes — a wrong
// identifier, or a permission set other than the constructor's.
//
// It is distinct from RoleNameExistsError: that one reports a collision with a
// role that is present, while this one refuses a name whether or not anything
// holds it yet. The distinction is load-bearing, because the case worth
// refusing is precisely the one where the built-in has NOT been seeded and the
// name is therefore still free.
type ReservedRoleNameError struct {
	Name     string
	TenantID string
	Detail   string
}

// Error returns a human-readable description of the reserved-name refusal.
func (e *ReservedRoleNameError) Error() string {
	msg := "role: reserved system role name: " + e.Name + " in tenant " + e.TenantID
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// StoreError is returned for store-level operation failures.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description of the store failure.
func (e *StoreError) Error() string {
	msg := "role: store " + e.Op + " failed"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StoreError) Unwrap() error { return e.Err }

// ValidationError is returned when request validation fails.
type ValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "role: validation failed: " + e.Field + ": " + e.Reason
}

// InvalidPermissionError is returned when a permission string is not
// registered in the global permission registry.
type InvalidPermissionError struct {
	Permission string
}

// Error returns a human-readable description of the invalid permission.
func (e *InvalidPermissionError) Error() string {
	return "role: invalid permission: " + e.Permission
}

// InvalidResolverResultError is returned when a NameResolver breaks its
// contract by reporting neither a role nor an error.
//
// It is refused rather than read as "no such role". ResolveSystemRoleID and
// SystemRoleIDs answer the question "which identifier denotes this system
// role", and their callers include controls that exist to VOID a grant — the
// SO deny-list and the strict-separation-of-duties Owner strip. Reading a
// contract violation as absence would drop the persisted identifier from the
// answer, and on a cluster seeded before the UUIDv5 migration that is exactly
// the identifier those controls must recognise. The grant would survive the
// control that exists to void it. Callers fail closed on this error instead.
type InvalidResolverResultError struct {
	// Name is the role name that was being resolved.
	Name string
	// TenantID is the tenant the lookup was scoped to.
	TenantID string
}

// Error returns a human-readable description of the contract violation.
func (e *InvalidResolverResultError) Error() string {
	return "role: name resolver returned no role and no error for " + e.Name +
		" in tenant " + e.TenantID
}

// ResolverRequiredError is returned by HoldsSystemRole when it is given no
// NameResolver. Deciding that a caller holds a role needs the store's answer,
// so a missing store is reported rather than read as "does not hold it" --
// which would look like an ordinary refusal and hide the misconfiguration.
type ResolverRequiredError struct {
	// Name is the role name that was being resolved.
	Name string
}

// Error returns a human-readable description of the missing resolver.
func (e *ResolverRequiredError) Error() string {
	return "role: no name resolver to decide whether a caller holds " + e.Name
}
