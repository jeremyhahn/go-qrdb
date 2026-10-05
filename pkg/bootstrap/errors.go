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

// MissingLoggerError is returned when an orchestrator entry point is called
// without a logger in Dependencies.
type MissingLoggerError struct{}

// Error implements error.
func (e *MissingLoggerError) Error() string {
	return "bootstrap: missing logger"
}

// MissingBarriersError is returned when an entry point that initializes a
// barrier is called without a *seal.BarrierRegistry in Dependencies.
type MissingBarriersError struct{}

// Error implements error.
func (e *MissingBarriersError) Error() string {
	return "bootstrap: missing barrier registry"
}

// MissingPassphraseError is returned when the passphrase sealing strategy is
// selected but no passphrase is supplied. There is no derived default: the
// passphrase strategy always requires an explicit passphrase.
type MissingPassphraseError struct{}

// Error implements error.
func (e *MissingPassphraseError) Error() string {
	return "bootstrap: passphrase strategy requires a non-empty passphrase"
}

// UnsupportedStrategyError is returned when an unknown or unsupported sealing
// strategy id is requested. Only none, passphrase (software), and shamir are
// supported by the bootstrap orchestrator; hardware strategies are injected
// into the barrier registry by xkms at composition time, not selected here.
type UnsupportedStrategyError struct {
	Strategy string
}

// Error implements error.
func (e *UnsupportedStrategyError) Error() string {
	return "bootstrap: unsupported sealing strategy: " + e.Strategy
}

// EmptyTenantIDError is returned when a tenant-scoped entry point is called
// with an empty tenant id.
type EmptyTenantIDError struct{}

// Error implements error.
func (e *EmptyTenantIDError) Error() string {
	return "bootstrap: empty tenant id"
}

// MissingShamirConfigError is returned when the shamir strategy is selected
// but the threshold/total configuration is missing or invalid.
type MissingShamirConfigError struct{}

// Error implements error.
func (e *MissingShamirConfigError) Error() string {
	return "bootstrap: shamir strategy requires threshold and total (2 <= threshold <= total)"
}

// SystemBarrierInitError wraps errors that occur while initializing the system
// barrier during BootstrapSystem.
type SystemBarrierInitError struct {
	Cause error
}

// Error implements error.
func (e *SystemBarrierInitError) Error() string {
	return "bootstrap: system barrier initialization failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *SystemBarrierInitError) Unwrap() error { return e.Cause }

// FirstBootError wraps errors returned by firstboot.Initialize during
// BootstrapSystem.
type FirstBootError struct {
	Cause error
}

// Error implements error.
func (e *FirstBootError) Error() string {
	return "bootstrap: first-boot failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *FirstBootError) Unwrap() error { return e.Cause }

// InvalidAdminEnrollmentModeError reports a TenantProvisionConfig
// AdminEnrollment value that names no declared enrollment mode.
type InvalidAdminEnrollmentModeError struct {
	Mode string
}

// Error implements error.
func (e *InvalidAdminEnrollmentModeError) Error() string {
	return "bootstrap: invalid tenant-admin enrollment mode: " + e.Mode
}

// ConflictingAdminEnrollmentError reports an AdminCSRPEM supplied alongside
// AdminEnrollDeferred: signing the CSR would contradict the requested
// deferral, and silently discarding it would strand the admin's keypair.
type ConflictingAdminEnrollmentError struct{}

// Error implements error.
func (e *ConflictingAdminEnrollmentError) Error() string {
	return "bootstrap: admin CSR conflicts with deferred tenant-admin enrollment"
}

// SystemTenantDeferredEnrollmentError reports AdminEnrollDeferred requested
// for the system tenant. System-level enrollment is cert-only, always: an
// invitation-activated identity is not an acceptable bootstrap anchor for
// the tenant that guards barrier material.
type SystemTenantDeferredEnrollmentError struct{}

// Error implements error.
func (e *SystemTenantDeferredEnrollmentError) Error() string {
	return "bootstrap: deferred enrollment is not permitted for the system tenant (system-level enrollment is cert-only)"
}

// MissingAdminFactorsError reports deferred enrollment requested without the
// AdminFactors seam. The canonical auth-factor store must be consultable to
// prove the admin is credential-less before an invitation target is handed
// back, so deferred mode refuses to run without it.
type MissingAdminFactorsError struct{}

// Error implements error.
func (e *MissingAdminFactorsError) Error() string {
	return "bootstrap: deferred enrollment requires the AdminFactors seam (canonical auth-factor store)"
}

// PendingAdminUnresolvableError reports that deferred provisioning could not
// obtain a truly pending tenant-admin record for the invitation to attach
// to — whether the username was already taken by an unresolvable or
// non-pending record, or the creating adapter returned one that fails the
// pending-admin validation.
type PendingAdminUnresolvableError struct {
	TenantID string
	Username string
	Reason   string
}

// Error implements error.
func (e *PendingAdminUnresolvableError) Error() string {
	return "bootstrap: cannot resolve pending tenant admin " + e.Username + "@" + e.TenantID + ": " + e.Reason
}

// TenantRecordCreationError wraps errors that occur while creating the tenant
// record during ProvisionTenant.
type TenantRecordCreationError struct {
	Cause error
}

// Error implements error.
func (e *TenantRecordCreationError) Error() string {
	return "bootstrap: tenant record creation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TenantRecordCreationError) Unwrap() error { return e.Cause }

// TenantAdminRoleCreationError wraps errors that occur while seeding the
// tenant-admin role during ProvisionTenant.
type TenantAdminRoleCreationError struct {
	Cause error
}

// Error implements error.
func (e *TenantAdminRoleCreationError) Error() string {
	return "bootstrap: tenant-admin role creation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TenantAdminRoleCreationError) Unwrap() error { return e.Cause }

// TenantAdminUserCreationError wraps errors that occur while creating the
// tenant-admin user during ProvisionTenant.
type TenantAdminUserCreationError struct {
	Cause error
}

// Error implements error.
func (e *TenantAdminUserCreationError) Error() string {
	return "bootstrap: tenant-admin user creation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TenantAdminUserCreationError) Unwrap() error { return e.Cause }

// TenantAdminCertError wraps errors that occur while enrolling (issuing,
// thumbprinting, or binding) the tenant-admin certificate during
// ProvisionTenant.
type TenantAdminCertError struct {
	Cause error
}

// Error implements error.
func (e *TenantAdminCertError) Error() string {
	return "bootstrap: tenant-admin cert enrollment failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TenantAdminCertError) Unwrap() error { return e.Cause }

// TenantBarrierRegistrationError wraps errors that occur while registering a
// tenant barrier with the BarrierRegistry during InitializeTenantBarrier.
type TenantBarrierRegistrationError struct {
	Cause error
}

// Error implements error.
func (e *TenantBarrierRegistrationError) Error() string {
	return "bootstrap: tenant barrier registration failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TenantBarrierRegistrationError) Unwrap() error { return e.Cause }

// TenantBarrierInitError wraps errors that occur while initializing a tenant
// barrier during InitializeTenantBarrier.
type TenantBarrierInitError struct {
	Cause error
}

// Error implements error.
func (e *TenantBarrierInitError) Error() string {
	return "bootstrap: tenant barrier initialization failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TenantBarrierInitError) Unwrap() error { return e.Cause }
