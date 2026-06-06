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
