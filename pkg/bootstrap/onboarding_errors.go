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

package bootstrap

import (
	"strconv"
	"strings"
)

// MissingAdminCredentialError is returned by ProvisionTenant in cert mode when
// neither an admin CSR nor an admin certificate is supplied: a tenant is
// onboarded with its first admin's login credential, or not at all.
type MissingAdminCredentialError struct{}

// Error implements error.
func (e *MissingAdminCredentialError) Error() string {
	return "bootstrap: tenant onboarding needs the first admin's CSR or certificate"
}

// ConflictingAdminCredentialError is returned when both an admin CSR and an
// admin certificate are supplied: exactly one names the admin's key.
type ConflictingAdminCredentialError struct{}

// Error implements error.
func (e *ConflictingAdminCredentialError) Error() string {
	return "bootstrap: supply the first admin's CSR or certificate, not both"
}

// AdminCredentialRejectedError is returned when the admin's CSR or
// certificate is refused before anything is written: the CSR does not verify,
// or the certificate does not chain to the supplied trust anchors or does not
// name the admin.
type AdminCredentialRejectedError struct {
	Cause error
}

// Error implements error.
func (e *AdminCredentialRejectedError) Error() string {
	return "bootstrap: the first admin's credential was refused: " + e.Cause.Error()
}

// Unwrap exposes the cause.
func (e *AdminCredentialRejectedError) Unwrap() error { return e.Cause }

// InvalidTrustAnchorsError is returned when the supplied trust anchors are
// refused before anything is written.
type InvalidTrustAnchorsError struct {
	Cause error
}

// Error implements error.
func (e *InvalidTrustAnchorsError) Error() string {
	return "bootstrap: the tenant's trust anchors were refused: " + e.Cause.Error()
}

// Unwrap exposes the cause.
func (e *InvalidTrustAnchorsError) Unwrap() error { return e.Cause }

// MissingProvisioningSeamError is returned when ProvisionTenant is asked for
// something its Dependencies cannot do: Seam names the missing field or
// capability.
type MissingProvisioningSeamError struct {
	Seam string
}

// Error implements error.
func (e *MissingProvisioningSeamError) Error() string {
	return "bootstrap: tenant provisioning needs " + e.Seam
}

// TrustAnchorRegistrationError wraps a failure to register the tenant's
// trust anchors.
type TrustAnchorRegistrationError struct {
	Cause error
}

// Error implements error.
func (e *TrustAnchorRegistrationError) Error() string {
	return "bootstrap: registering the tenant's trust anchors failed: " + e.Cause.Error()
}

// Unwrap exposes the cause.
func (e *TrustAnchorRegistrationError) Unwrap() error { return e.Cause }

// PendingTenantAdminError wraps a failure to record, read or complete a
// pending tenant admin.
type PendingTenantAdminError struct {
	Op    string
	Cause error
}

// Error implements error.
func (e *PendingTenantAdminError) Error() string {
	return "bootstrap: pending tenant admin " + e.Op + " failed: " + e.Cause.Error()
}

// Unwrap exposes the cause.
func (e *PendingTenantAdminError) Unwrap() error { return e.Cause }

// AdminCompletionDeferredError is returned by CompletePendingTenantAdmin on
// a node that does not lead the tenant's home group while the tenant still
// has a pending admin: the leader completes it, and this node wrote nothing.
type AdminCompletionDeferredError struct {
	TenantID string
}

// Error implements error.
func (e *AdminCompletionDeferredError) Error() string {
	return "bootstrap: tenant " + e.TenantID + " admin completion deferred: this node does not lead the tenant's home group"
}

// ProvisioningUndoneError is returned by an exclusive ProvisionTenant whose
// failure was undone. Cause is the failure; UndoFailures lists any undo step
// that itself failed, which an operator must finish by hand.
type ProvisioningUndoneError struct {
	TenantID     string
	Cause        error
	UndoFailures []error
}

// Error implements error.
func (e *ProvisioningUndoneError) Error() string {
	msg := "bootstrap: provisioning tenant " + e.TenantID + " failed and was undone: " + e.Cause.Error()
	if len(e.UndoFailures) > 0 {
		parts := make([]string, len(e.UndoFailures))
		for i, f := range e.UndoFailures {
			parts[i] = f.Error()
		}
		msg += "; undo steps that failed: " + strings.Join(parts, "; ")
	}
	return msg
}

// Unwrap exposes the failure that triggered the undo.
func (e *ProvisioningUndoneError) Unwrap() error { return e.Cause }

// InvalidAdminUsernameError is returned when a pending tenant admin's
// username cannot safely be signed into a certificate's Subject CN: the
// pending-admin path has no later user-store validation to catch it before
// the certificate is issued and recorded, unlike the non-pending path's
// immediate CreateUser.
type InvalidAdminUsernameError struct {
	Username string
}

// Error implements error.
func (e *InvalidAdminUsernameError) Error() string {
	return "bootstrap: admin username " + strconv.Quote(e.Username) + " is not valid: 1-64 characters from [A-Za-z0-9._@-]"
}

// PendingAdminAccountConflictError is returned when a pending tenant admin is
// completed and an account of the admin's name already exists in the tenant
// without the tenant's Admin role: it is not the account a previous attempt
// created, so it is never adopted. An operator resolves the conflict (rename
// or remove that account) and the completion is retried on the next unseal.
type PendingAdminAccountConflictError struct {
	TenantID string
	Username string
}

// Error names the tenant and the account.
func (e *PendingAdminAccountConflictError) Error() string {
	return "bootstrap: tenant " + e.TenantID + " already has an account named " + e.Username +
		" that does not hold the tenant's Admin role; it is not adopted as the onboarded admin"
}
