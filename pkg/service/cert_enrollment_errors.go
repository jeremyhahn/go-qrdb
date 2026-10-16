// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

// CertEnrollmentNotConfiguredError is returned by a CertEnrollmentService
// built without the collaborator a verb needs.
type CertEnrollmentNotConfiguredError struct {
	Capability string
}

// Error names the missing capability.
func (e *CertEnrollmentNotConfiguredError) Error() string {
	return "service: " + e.Capability + " not configured on this node"
}

// CertEnrollmentFieldError is returned when a certificate enrolment request
// is missing a required field or carries an invalid one.
type CertEnrollmentFieldError struct {
	Field string
}

// Error names the field.
func (e *CertEnrollmentFieldError) Error() string {
	return "service: certificate enrolment request has a missing or invalid " + e.Field
}

// CertBindingFailedError is returned when a certificate could not be bound to
// its account. For a certificate this node signed, Serial names it and it was
// revoked -- unless RevokeErr says the revocation failed too, in which case
// the serial must be revoked by an operator.
type CertBindingFailedError struct {
	Serial    string
	Err       error
	RevokeErr error
}

// Error describes the failure and what became of the certificate.
func (e *CertBindingFailedError) Error() string {
	msg := "service: the certificate could not be bound to its account: " + e.Err.Error()
	switch {
	case e.Serial != "" && e.RevokeErr != nil:
		msg += "; revoking serial " + e.Serial + " failed too and it must be revoked by an operator: " + e.RevokeErr.Error()
	case e.Serial != "":
		msg += "; serial " + e.Serial + " was revoked"
	}
	return msg
}

// Unwrap returns the binding failure.
func (e *CertBindingFailedError) Unwrap() error { return e.Err }

// CertTargetRefusedError is returned by SignCertificate and EnrollCertificate
// when the account they would issue or enrol for holds the tenant's Security
// Officer or Owner role -- certificate custody those roles hold outside RBAC
// reach, never delegated through an Admin's certs:issue/certs:enroll grant.
// See certTargetGuard.
type CertTargetRefusedError struct {
	TenantID string
	Username string
}

// Error names the tenant and account.
func (e *CertTargetRefusedError) Error() string {
	return "service: " + e.Username + " of tenant " + e.TenantID + " holds a role certificate issuance may not target"
}

// DelegationRequestError is returned by IssueDelegation for a grant a tenant
// may not issue: Field names what is wrong with it, and Err, when set, is the
// delegation package's own judgement.
type DelegationRequestError struct {
	Field  string
	Reason string
	Err    error
}

// Error names the field and the reason.
func (e *DelegationRequestError) Error() string {
	msg := "service: delegation " + e.Field + " " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the delegation package's refusal, if any.
func (e *DelegationRequestError) Unwrap() error { return e.Err }

// DelegationDeniedError is returned by IssueDelegation when the caller may not
// delegate at all: the system tenant, or a caller that is itself delegated.
type DelegationDeniedError struct {
	Reason string
}

// Error states the reason.
func (e *DelegationDeniedError) Error() string {
	return "service: delegation denied: " + e.Reason
}

// TenantOnboardingNotConfiguredError is returned by a TenantOnboardingService
// built without one of its required collaborators.
type TenantOnboardingNotConfiguredError struct{}

// Error describes the refusal.
func (e *TenantOnboardingNotConfiguredError) Error() string {
	return "service: tenant onboarding is not configured on this node"
}

// TenantOnboardingFieldError is returned when a tenant onboarding request is
// missing a required field.
type TenantOnboardingFieldError struct {
	Field string
}

// Error names the field.
func (e *TenantOnboardingFieldError) Error() string {
	return "service: tenant onboarding request is missing " + e.Field
}
