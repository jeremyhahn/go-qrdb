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
// not adopted: unparseable, outside its validity window, or not chaining to
// AdminCertPool.
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
