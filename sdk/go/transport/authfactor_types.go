// Copyright 2026 Jeremy Hahn
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

package transport

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Auth factor SDK transport types
// ---------------------------------------------------------------------------

// EnrollFactorRequest is the request payload for enrolling an auth factor.
type EnrollFactorRequest struct {
	// FactorName identifies the auth factor type (e.g., "pkcs11", "piv", "fido2").
	FactorName string `json:"factor_name"`

	// Subject is the user or custodian being enrolled.
	Subject string `json:"subject"`

	// TenantID does NOT select which tenant the enrollment lands in — the
	// call's authenticated context does that, and every transport routes on
	// it alone. This field is checked against that context tenant: leave it
	// empty and the server fills it in; set it to the context tenant and it
	// passes; set it to anything else and the server refuses the request
	// with a tenant-mismatch error rather than honoring it or silently
	// correcting it. It exists so a caller can assert which tenant it
	// believes it is enrolling into and be told when that belief is wrong.
	TenantID string `json:"tenant_id,omitempty"`

	// MethodParams are factor-specific enrollment parameters.
	MethodParams map[string]any `json:"method_params,omitempty"`
}

// EnrollFactorResponse is returned after successful factor enrollment.
type EnrollFactorResponse struct {
	// EnrollmentID is the opaque identifier for this enrollment record.
	EnrollmentID string `json:"enrollment_id"`

	// FactorName is the enrolled factor type.
	FactorName string `json:"factor_name"`

	// UserID is the subject that was enrolled.
	UserID string `json:"user_id"`

	// TenantID is the tenant scope of this enrollment (may be empty).
	TenantID string `json:"tenant_id,omitempty"`

	// Metadata contains factor-specific enrollment metadata
	// (e.g., credential ID for FIDO2, slot info for PIV/PKCS#11).
	Metadata map[string]any `json:"metadata,omitempty"`

	// EnrolledAt is when the enrollment was recorded.
	EnrolledAt time.Time `json:"enrolled_at"`
}

// ChallengeFactorRequest is the request payload for issuing an auth challenge.
type ChallengeFactorRequest struct {
	// EnrollmentID identifies the enrollment to challenge.
	EnrollmentID string `json:"enrollment_id"`
}

// ChallengeFactorResponse is returned after a challenge is successfully issued.
type ChallengeFactorResponse struct {
	// ChallengeID is the opaque challenge identifier.
	ChallengeID string `json:"challenge_id"`

	// EnrollmentID is the enrollment this challenge is bound to.
	EnrollmentID string `json:"enrollment_id"`

	// Nonce is the random bytes the custodian must sign.
	Nonce []byte `json:"nonce"`

	// ExpiresAt is the challenge expiry time.
	ExpiresAt time.Time `json:"expires_at"`

	// MethodData contains factor-specific challenge data
	// (e.g., WebAuthn PublicKeyCredentialRequestOptions for FIDO2).
	MethodData map[string]any `json:"method_data,omitempty"`
}

// VerifyFactorRequest is the request payload for verifying an auth factor.
type VerifyFactorRequest struct {
	// FactorName identifies the auth factor type (optional; used for routing).
	FactorName string `json:"factor_name,omitempty"`

	// ChallengeID is the challenge to verify against.
	ChallengeID string `json:"challenge_id"`

	// Value is the signed response or proof (e.g., DER-encoded ECDSA signature).
	Value []byte `json:"value"`

	// Metadata contains factor-specific response metadata
	// (e.g., "public_key_pem" for PIV, authenticator assertion for FIDO2).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ListFactorsRequest is the request payload for listing enrolled factors.
type ListFactorsRequest struct {
	// Subject filters enrollments by user or custodian (optional).
	Subject string `json:"subject,omitempty"`
}

// EnrollmentEntry is a single enrollment record in a list response.
type EnrollmentEntry struct {
	// EnrollmentID is the opaque enrollment identifier.
	EnrollmentID string `json:"enrollment_id"`

	// FactorName is the auth factor type.
	FactorName string `json:"factor_name"`

	// UserID is the enrolled subject.
	UserID string `json:"user_id"`

	// TenantID is the tenant scope (may be empty).
	TenantID string `json:"tenant_id,omitempty"`

	// Metadata contains factor-specific enrollment metadata.
	Metadata map[string]any `json:"metadata,omitempty"`

	// EnrolledAt is when the enrollment was recorded.
	EnrolledAt time.Time `json:"enrolled_at"`

	// RevokedAt is set when the enrollment has been revoked.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// ListFactorsResponse is the response for a factor list query.
type ListFactorsResponse struct {
	// Enrollments is the list of matching enrollment records.
	Enrollments []EnrollmentEntry `json:"enrollments"`

	// Total is the total number of enrollments returned.
	Total int `json:"total"`
}

// RevokeFactorRequest is the request payload for revoking an enrollment.
type RevokeFactorRequest struct {
	// EnrollmentID identifies the enrollment to revoke.
	EnrollmentID string `json:"enrollment_id"`
}

// ---------------------------------------------------------------------------
// AuthFactorClient interface
// ---------------------------------------------------------------------------

// AuthFactorClient provides operations for custodian auth factor enrollment,
// challenge issuance, verification, listing, and revocation across all QRDB
// transports. It supports PKCS#11, PIV, and FIDO2 factor types.
type AuthFactorClient interface {
	// EnrollFactor enrolls a custodian in the given auth factor type.
	// The factor_name must match one of the factors registered on the server
	// (e.g., "pkcs11", "piv", "fido2"). Returns an enrollment record
	// containing the enrollment ID and any factor-specific metadata.
	EnrollFactor(ctx context.Context, req *EnrollFactorRequest) (*EnrollFactorResponse, error)

	// ChallengeFactor issues a challenge for a previously enrolled factor.
	// The caller must present the returned nonce (and factor-specific method
	// data) to the hardware token or authenticator to produce a response.
	ChallengeFactor(ctx context.Context, req *ChallengeFactorRequest) (*ChallengeFactorResponse, error)

	// VerifyFactor verifies the custodian's response to an outstanding challenge.
	// A successful call consumes the challenge (preventing replay). Returns
	// nil on success.
	VerifyFactor(ctx context.Context, req *VerifyFactorRequest) error

	// ListFactors lists enrolled auth factors, optionally filtered by subject.
	ListFactors(ctx context.Context, req *ListFactorsRequest) (*ListFactorsResponse, error)

	// RevokeFactor permanently revokes an enrollment. Revoked enrollments
	// cannot be used for subsequent challenges or verifications.
	RevokeFactor(ctx context.Context, req *RevokeFactorRequest) error
}
