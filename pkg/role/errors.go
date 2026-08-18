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

// SystemRoleImmutableError is returned when trying to delete a system role.
type SystemRoleImmutableError struct {
	ID string
}

// Error returns a human-readable description of the immutability constraint.
func (e *SystemRoleImmutableError) Error() string {
	return "role: system role cannot be deleted: " + e.ID
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
