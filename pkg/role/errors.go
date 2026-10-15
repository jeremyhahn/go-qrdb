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

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// RoleNotFoundError is returned when a role ID does not exist.
type RoleNotFoundError struct {
	ID string
}

// Error returns a human-readable description of the not-found condition.
func (e *RoleNotFoundError) Error() string {
	return "role: not found: " + e.ID
}

// NotAssignableError is returned when a role referenced by an assignment
// (a user's RoleIDs, or a RoleBinding) cannot be granted within the named
// tenant: the role does not exist, or it exists but belongs to neither that
// tenant nor the system tenant's global built-in role set (go-qrdb#192).
type NotAssignableError struct {
	RoleID   string
	TenantID string
}

// Error returns a human-readable description of the refusal.
func (e *NotAssignableError) Error() string {
	return "role: " + e.RoleID + " is not assignable within tenant " + e.TenantID
}

// ReferenceError is returned when a role-assignment write names a role that
// does not exist. It wraps the lookup's own *RoleNotFoundError, so errors.As
// still finds it, but it classifies as a bad field in the WRITE rather than as
// a missing resource: the request that carried the reference names a user,
// and reporting "not found" for it would name the wrong subject.
type ReferenceError struct {
	RoleID string
	Err    error
}

// Error returns a human-readable description of the unresolved reference.
func (e *ReferenceError) Error() string {
	msg := "role: reference to unknown role " + e.RoleID
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the lookup's own error.
func (e *ReferenceError) Unwrap() error { return e.Err }

// ServiceRequiredError is returned when a write names one or more role
// references (RoleIDs, or a RoleBinding) but no RoleServicer was available
// to validate them. Whenever role references are present, validating them
// is mandatory, not best-effort: a composition that cannot resolve a role
// service must refuse the write rather than accept an unvalidated
// reference (go-qrdb#192 round 2).
type ServiceRequiredError struct{}

// Error returns a human-readable description of the refusal.
func (e *ServiceRequiredError) Error() string {
	return "role: a role service is required to validate the referenced role(s), and none was configured"
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
//
// transient is set at the construction site (see newStoreError), by the code
// that actually saw the underlying failure -- not inferred later from Op or
// Detail strings, which name WHERE the failure happened, not WHAT KIND it is.
// A caller deciding whether to retry reads it through the ReadTransient
// method rather than matching on Op/Detail: those are free text for a log
// message, and a renamed one must not silently change what is retryable.
type StoreError struct {
	Op     string
	Detail string
	Err    error

	transient bool
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

// ReadTransient reports whether retrying the operation that produced this
// error might succeed: the store read itself failed (ReadIndex not ready,
// leader lost), as opposed to a definitive answer -- corrupt stored data, or
// a cryptographic refusal (decrypt, authentication) from a
// sealed-barrier-backed store -- that no retry changes. See newStoreError.
//
// Named ReadTransient rather than the shorter Transient to avoid a
// structural collision with *service.FactorMigrationRefusedError's own
// Transient() bool, which answers an unrelated question ("does a later
// migration run still have work to do") and would otherwise satisfy the
// same single-method interface a classifier type-switches on.
func (e *StoreError) ReadTransient() bool { return e.transient }

// newStoreError builds a *StoreError and decides transience from cause ITSELF
// (the underlying store's own failure), not from op or detail: those are
// free-text labels a future refactor can rename without anyone noticing the
// retry semantics moved with them. A cryptographic refusal -- the one failure
// shape a successful read can still produce, via a sealed-barrier-backed
// store's Decrypt -- is never transient, because no retry decrypts a
// tampered ciphertext or authenticates with the wrong key; isTransientCause
// is the one place that distinction is made.
func newStoreError(op, detail string, cause error) *StoreError {
	return &StoreError{Op: op, Detail: detail, Err: cause, transient: isTransientCause(cause)}
}

// isTransientCause reports whether cause, the error a store's own Get/List/
// Scan returned, describes a failure worth retrying. A cryptographic refusal
// from a sealed-barrier-backed store's Decrypt -- a tampered ciphertext, or
// the wrong key -- is excluded explicitly: it is a successful READ of
// definitively bad data, not a failure to read, and retrying never decrypts
// it. Everything else reaching here is presumed to be the read itself
// failing (ReadIndex not ready, leader lost, timeout), which retrying can fix.
func isTransientCause(cause error) bool {
	return !errors.Is(cause, seal.ErrDecryptionFailed)
}

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
// It is refused rather than read as "no such role". HoldsSystemRole answers
// the question "does this set of role identifiers grant the named system
// role", and its callers include controls that exist to VOID a grant — the
// SO deny-list and the strict-separation-of-duties Owner strip. Reading a
// contract violation as absence would drop the persisted identifier from the
// answer, and that is exactly the identifier those controls must recognise.
// The grant would survive the control that exists to void it. Callers fail
// closed on this error instead.
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

// RoleDeletingError is returned by ValidateAssignable and UpdateRole for a
// role whose DeleteRole has begun (Role.Deleting). Assigning it would hand an
// account a role the delete is in the middle of stripping from every holder,
// and editing it would change a role that is on its way out, so both writes
// are refused.
type RoleDeletingError struct {
	ID string
	// Op is the refused operation: "assigned" or "updated".
	Op string
}

// Error returns a human-readable description of the refusal.
func (e *RoleDeletingError) Error() string {
	return "role: " + e.ID + " is being deleted and cannot be " + e.Op
}

// HolderRevokerRequiredError is returned by DeleteRole when the store was
// built without WithHolderRevoker. Deleting a role without stripping it from
// the accounts that hold it would leave every holder carrying a dead role
// identifier, which an access audit cannot tell from a live grant, so a
// composition that cannot reach the user store refuses the delete instead.
type HolderRevokerRequiredError struct {
	ID string
}

// Error returns a human-readable description of the refusal.
func (e *HolderRevokerRequiredError) Error() string {
	return "role: deleting " + e.ID + " needs a holder revoker to strip it from its holders, and none was configured"
}

// HolderRevocationError is returned by DeleteRole when stripping the role from
// its holders failed. The role record is still present, marked Deleting, so a
// retry of the same DeleteRole resumes the strip; Err is the revoker's own
// error and decides how the failure is classified.
type HolderRevocationError struct {
	ID       string
	TenantID string
	Err      error
}

// Error returns a human-readable description of the failed strip.
func (e *HolderRevocationError) Error() string {
	msg := "role: stripping " + e.ID + " from its holders in tenant " + e.TenantID + " failed; retry the delete"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the revoker's own error.
func (e *HolderRevocationError) Unwrap() error { return e.Err }
