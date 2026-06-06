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

// Package custodian provides custodian lifecycle management for the Shamir
// unseal ceremony. Each custodian holds one encrypted Shamir share delivered
// in an ECDH+AES-256-GCM envelope; no share material is ever persisted
// server-side.
package custodian

import "time"

// FactorMethod identifies the authentication mechanism bound to a custodian.
type FactorMethod string

const (
	// FactorMethodPKCS11 is a PKCS#11 hardware token.
	FactorMethodPKCS11 FactorMethod = "pkcs11"

	// FactorMethodPIV is a PIV smart card per NIST SP 800-73.
	FactorMethodPIV FactorMethod = "piv"

	// FactorMethodFIDO2 is a FIDO2 authenticator.
	FactorMethodFIDO2 FactorMethod = "fido2"

	// FactorMethodSoftware is a software-backed key (tests/non-hardware deployments).
	FactorMethodSoftware FactorMethod = "software"
)

// Custodian is the persistent public metadata record for an enrolled
// custodian. It is stored under __sys:custodian/<id> and Raft-replicated.
// No share material is ever present in this struct.
type Custodian struct {
	// ID is the unique identifier for this custodian (UUID v5, deterministic
	// from tenantID+username).
	ID string `json:"id"`

	// Username is the human-readable identifier for the custodian.
	Username string `json:"username"`

	// TenantID scopes the custodian record.
	TenantID string `json:"tenantID"`

	// Method is the authentication factor type bound to this custodian.
	Method FactorMethod `json:"method"`

	// EnrollmentID references the authfactors.Enrollment record created
	// during factor enrollment.
	EnrollmentID string `json:"enrollmentID"`

	// PublicKeyPEM is the PEM-encoded public key from the enrollment.
	// Duplicated here for offline verification convenience; canonical
	// source is the enrollment record.
	PublicKeyPEM string `json:"publicKeyPEM"`

	// ShareIndex is the 1-based position in the Shamir scheme (1..N).
	ShareIndex int `json:"shareIndex"`

	// EnrolledAt is the UTC timestamp of enrollment.
	EnrolledAt time.Time `json:"enrolledAt"`

	// RevokedAt is non-nil when the custodian has been revoked.
	RevokedAt *time.Time `json:"revokedAt,omitempty"`

	// ShareClaimed tracks whether the sealed share envelope was delivered
	// to this custodian during the enrollment ceremony.
	ShareClaimed bool `json:"shareClaimed"`

	// ShareClaimedAt is the UTC timestamp of share delivery.
	ShareClaimedAt *time.Time `json:"shareClaimedAt,omitempty"`
}

// Active reports whether the custodian is enrolled and not revoked.
func (c *Custodian) Active() bool {
	return c.RevokedAt == nil
}

// RegisterRequest carries the inputs for enrolling a new custodian.
type RegisterRequest struct {
	// Username is the human-readable custodian identifier. Must be unique
	// within the tenant.
	Username string

	// TenantID identifies the owning tenant.
	TenantID string

	// Method is the authentication factor type.
	Method FactorMethod

	// EnrollmentID references an existing authfactors.Enrollment.
	EnrollmentID string

	// PublicKeyPEM is the PEM-encoded public key for ECDH share encryption.
	// Must be a P-256 (or compatible) public key.
	PublicKeyPEM string

	// ShareIndex is the 1-based Shamir share index assigned to this custodian.
	ShareIndex int

	// ShareBytes is the raw plaintext Shamir share to be sealed into an
	// envelope for this custodian. This field is consumed and zeroed by
	// RegisterCustodian; it must never be stored or logged.
	ShareBytes []byte
}

// RegisterResponse carries the output of a successful RegisterCustodian call.
// The SealedEnvelope must be delivered to the custodian out-of-band; it is
// single-use and not re-retrievable from the server after this call.
type RegisterResponse struct {
	// Custodian is the persisted metadata record.
	Custodian *Custodian

	// SealedEnvelope is the ECDH+AES-256-GCM-encrypted Shamir share.
	// This value is transient: it is returned once and never stored server-side.
	SealedEnvelope *SealedEnvelope
}
