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

package firstboot

import "strconv"

// MissingTenantsError is returned when Initialize is called without a
// TenantServicer in Dependencies.
type MissingTenantsError struct{}

// Error implements error.
func (e *MissingTenantsError) Error() string {
	return "firstboot: missing tenant servicer"
}

// MissingUsersError is returned when Initialize is called without a
// UserServicer in Dependencies.
type MissingUsersError struct{}

// Error implements error.
func (e *MissingUsersError) Error() string {
	return "firstboot: missing user servicer"
}

// MissingRolesError is returned when Initialize is called without a
// RoleServicer in Dependencies.
type MissingRolesError struct{}

// Error implements error.
func (e *MissingRolesError) Error() string {
	return "firstboot: missing role servicer"
}

// MissingLoggerError is returned when Initialize is called without a
// logger in Dependencies.
type MissingLoggerError struct{}

// Error implements error.
func (e *MissingLoggerError) Error() string {
	return "firstboot: missing logger"
}

// SystemTenantCreationError wraps errors that occur while creating the
// system tenant during first-boot. Causes that indicate
// already-exists-state are not surfaced through this error.
type SystemTenantCreationError struct {
	Cause error
}

// Error implements error.
func (e *SystemTenantCreationError) Error() string {
	return "firstboot: system tenant creation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *SystemTenantCreationError) Unwrap() error {
	return e.Cause
}

// RolesRegistrationError wraps errors that occur while registering the
// built-in roles during first-boot.
type RolesRegistrationError struct {
	Role  string
	Cause error
}

// Error implements error.
func (e *RolesRegistrationError) Error() string {
	return "firstboot: register role " + e.Role + " failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *RolesRegistrationError) Unwrap() error {
	return e.Cause
}

// AdminUserCreationError wraps errors that occur while creating the
// admin user during first-boot.
type AdminUserCreationError struct {
	Cause error
}

// Error implements error.
func (e *AdminUserCreationError) Error() string {
	return "firstboot: admin user creation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminUserCreationError) Unwrap() error {
	return e.Cause
}

// AdminCertGenerationError wraps errors that occur while generating the
// platform admin certificate during first-boot.
type AdminCertGenerationError struct {
	Cause error
}

// Error implements error.
func (e *AdminCertGenerationError) Error() string {
	return "firstboot: admin cert generation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminCertGenerationError) Unwrap() error {
	return e.Cause
}

// AdminCertBindError wraps errors that occur while binding the admin
// certificate's SPKI thumbprint to the admin user during first-boot.
type AdminCertBindError struct {
	Cause error
}

// Error implements error.
func (e *AdminCertBindError) Error() string {
	return "firstboot: admin cert binding failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminCertBindError) Unwrap() error {
	return e.Cause
}

// ConflictingAdminCertInputsError is returned when Dependencies carries both
// AdminCSRPEM and AdminCertPEM.
//
// The two select different enrollment paths for the same identity — Path B
// signs a CSR the admin generated, Path C adopts a certificate the admin
// already holds — and there is no defensible way to pick one on the operator's
// behalf. Signing the CSR would leave the presented certificate unbound and
// unusable for login; adopting the certificate would silently discard the CSR.
type ConflictingAdminCertInputsError struct{}

// Error implements error.
func (e *ConflictingAdminCertInputsError) Error() string {
	return "firstboot: AdminCSRPEM and AdminCertPEM are mutually exclusive: " +
		"supply a CSR to have the CA issue a certificate, or a certificate to adopt one you already hold"
}

// MissingAdminCertPoolError is returned when AdminCertPEM is supplied without
// AdminCertPool.
//
// A presented certificate is only an identity claim until it chains to
// something the deployment trusts. Adopting one against an empty pool would
// bind whatever the caller handed over, so the pool is required rather than
// defaulted — the same rule the ceremony applies to an externally enrolled
// officer.
type MissingAdminCertPoolError struct{}

// Error implements error.
func (e *MissingAdminCertPoolError) Error() string {
	return "firstboot: AdminCertPEM requires AdminCertPool: a presented certificate " +
		"must chain to a trust anchor the deployment already accepts"
}

// AdminCertRejectedError wraps the reason a presented admin certificate was
// not adopted: unparseable, outside its validity window, not chaining to
// AdminCertPool, refused by the mTLS authenticator's tenant rule (Cause is
// then the *auth.CertificateTenantError auth.TenantFromClientCert returned),
// or naming a tenant other than the admin's (*AdminCertTenantError).
type AdminCertRejectedError struct {
	Cause error
}

// Error implements error.
func (e *AdminCertRejectedError) Error() string {
	return "firstboot: presented admin certificate rejected: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminCertRejectedError) Unwrap() error {
	return e.Cause
}

// AdminCertTenantError is the cause of an *AdminCertRejectedError when a
// presented certificate passes the authenticator's tenant rule but names a
// tenant other than the one the admin is bound in: every login with it would
// resolve to CertTenant, where this admin does not exist.
type AdminCertTenantError struct {
	// CertTenant is the tenant the certificate's agreeing OU and SPIFFE ID name.
	CertTenant string
	// AdminTenant is the tenant the bootstrap admin is bound in.
	AdminTenant string
}

// Error implements error.
func (e *AdminCertTenantError) Error() string {
	return "firstboot: certificate names tenant " + strconv.Quote(e.CertTenant) +
		", not the admin's tenant " + strconv.Quote(e.AdminTenant)
}

// AdminCertBindingReason says why the bootstrap admin could not log in under
// the "required" mTLS binding posture. Each value is a complete clause an
// operator can act on.
type AdminCertBindingReason string

const (
	// AdminBindingNoBinder: first-boot would create the admin with no
	// CertBinder wired, so the admin would hold no binding.
	AdminBindingNoBinder AdminCertBindingReason = "first boot has no CertBinder wired, so the admin it creates would hold no certificate binding"

	// AdminBindingNone: the admin exists and has no certificate binding.
	AdminBindingNone AdminCertBindingReason = "the admin account has no certificate binding"

	// AdminBindingAllExpired: the admin's every binding has passed its
	// NotAfter.
	AdminBindingAllExpired AdminCertBindingReason = "every certificate binding on the admin account has expired"

	// AdminBindingMissing: the admin account does not exist.
	AdminBindingMissing AdminCertBindingReason = "the admin account does not exist"

	// AdminBindingCannotAuthenticate: the admin's lifecycle state does not
	// permit a login (suspended, locked, disabled, pending, deleting).
	AdminBindingCannotAuthenticate AdminCertBindingReason = "the admin account's lifecycle state does not permit a login"

	// AdminBindingMTLSNotPermitted: the admin's permitted-method policy
	// excludes mTLS, so no certificate can log in as it.
	AdminBindingMTLSNotPermitted AdminCertBindingReason = "the admin account's permitted authentication methods exclude mtls"

	// AdminBindingUnverifiable: the user surface cannot look the admin up.
	AdminBindingUnverifiable AdminCertBindingReason = "the user surface cannot look the admin up, so its certificate binding cannot be verified"
)

// AdminCertBindingRequiredError is the boot refusal under
// auth.mtls_binding_policy "required" (go-qrdb#517): the bootstrap admin could
// not log in by certificate, so a node that started would be locked out of
// itself. It names the account, the reason, and the way out.
type AdminCertBindingRequiredError struct {
	TenantID string
	Username string
	Reason   AdminCertBindingReason
}

// Error implements error.
func (e *AdminCertBindingRequiredError) Error() string {
	return `firstboot: auth.mtls_binding_policy is "required" but the bootstrap admin ` +
		e.Username + " in tenant " + e.TenantID + " could not log in under it: " + string(e.Reason) +
		`; start with the policy "enrolled", enrol the admin's certificate (users:add_cert_binding), then set "required"`
}

// AdminLookupError wraps a failure to read the bootstrap admin while checking
// the "required" binding posture. It refuses the boot: the posture cannot be
// shown to hold, and starting anyway is the lockout the check exists to
// prevent.
type AdminLookupError struct {
	Cause error
}

// Error implements error.
func (e *AdminLookupError) Error() string {
	return "firstboot: looking up the bootstrap admin to verify its certificate binding: " + e.Cause.Error()
}

// Unwrap returns the underlying cause.
func (e *AdminLookupError) Unwrap() error {
	return e.Cause
}
