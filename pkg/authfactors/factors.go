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

// Package authfactors defines the common Factor interface and shared types for
// hardware and software authentication devices used in custodian enrollment and
// Shamir share unseal ceremonies. Four sub-packages provide concrete
// implementations: pkcs11, piv, fido2, and mtls (certificate-possession).
package authfactors

import (
	"context"
	"time"
)

// Factor represents a hardware or software authentication device.
// Implementations provide enrollment, challenge issuance, and response
// verification. The four supported factor types are PKCS#11, PIV, FIDO2, and
// mTLS (certificate-key possession proof).
type Factor interface {
	// Name returns the canonical factor type identifier ("pkcs11", "piv", "fido2", "mtls").
	Name() string

	// Enroll produces a public credential from a user-initiated enrollment
	// action and returns an Enrollment containing the public key material and
	// associated metadata. No private key material is returned or stored.
	Enroll(ctx context.Context, params *EnrollParams) (*Enrollment, error)

	// Challenge produces a nonce-based challenge for the user to sign. The
	// returned Challenge contains the nonce, enrollment reference, expiry time,
	// and method-specific parameters needed for signing.
	Challenge(ctx context.Context, enrollment *Enrollment) (*Challenge, error)

	// Verify asserts that the response proves possession of the private key
	// corresponding to the enrolled public credential. Returns nil on success.
	Verify(ctx context.Context, challenge *Challenge, response *Response) error
}

// EnrollParams captures the inputs needed to enroll a user with a factor.
type EnrollParams struct {
	// UserID is the identifier for the enrollment subject (e.g., "custodian_a").
	UserID string

	// MethodParams carries method-specific enrollment inputs (JSON-serializable).
	//
	// PKCS#11: {"slot_id": 0, "token_label": "MyToken", "pin": "1234"}
	// PIV:     {"slot": "9a", "algorithm": "ecdsap256"}
	// FIDO2:   {"attestation_type": "direct", "user_id": "base64..."}
	MethodParams map[string]any

	// TenantID scopes the enrollment in multi-tenant deployments.
	TenantID string
}

// Enrollment is returned from Enroll and persisted by the service layer.
// It contains only public credential material; no private key bytes are
// ever stored in an Enrollment.
type Enrollment struct {
	// EnrollmentID is the unique identifier for this enrollment record.
	EnrollmentID string

	// UserID is the subject of this enrollment.
	UserID string

	// FactorName is the factor type that produced this enrollment ("pkcs11", "piv", "fido2").
	FactorName string

	// TenantID scopes the enrollment.
	TenantID string

	// PublicKey holds the DER-encoded public key (PKCS#11 / PIV) or the
	// serialised WebAuthn credential (FIDO2). Never contains private key material.
	PublicKey []byte

	// Metadata carries method-specific attributes required for challenge and verify.
	//
	// PKCS#11: {"key_id": "...", "algorithm": "CKM_ECDSA_SHA256", "slot": "0"}
	// PIV:     {"slot": "9a", "algorithm": "ecdsap256", "issuer_hash": "..."}
	// FIDO2:   {"credential_id": "base64...", "public_key_hash": "..."}
	Metadata map[string]string

	// EnrolledAt is the UTC time of enrollment.
	EnrolledAt time.Time

	// RevokedAt is the UTC time of revocation; zero if still active.
	RevokedAt time.Time
}

// Challenge is issued by the server during a barrier unseal ceremony.
// The custodian must sign the Nonce with their private key and return a Response.
type Challenge struct {
	// ChallengeID is a unique opaque identifier for this challenge session.
	ChallengeID string

	// EnrollmentID references the enrollment that this challenge was issued for.
	EnrollmentID string

	// Nonce is a cryptographically random byte slice (32 bytes minimum) that
	// the custodian must sign to prove possession of the enrolled private key.
	Nonce []byte

	// ExpiresAt is the UTC deadline for submitting a Response. Challenges not
	// answered within this window are rejected (default five minutes).
	ExpiresAt time.Time

	// MethodData carries method-specific parameters for the signing operation.
	//
	// PKCS#11: {"mechanism": "CKM_ECDSA_SHA256"}
	// PIV:     {"slot": "9a", "algorithm": "ecdsap256"}
	// FIDO2:   {"challenge_base64": "...", "rp_id": "..."}
	//
	// Values MUST be strings. The type is map[string]any, not map[string]string,
	// only because it is built with the same composite-literal shape as
	// EnrollParams.MethodParams and Response.Metadata; it carries no wider
	// contract than they do. The floor is set by the LEAST expressive
	// transport this crosses, not the most: pkg/api/grpc/proto's
	// ChallengeFactorResponse.method_data is `map<string, string>`, fixed
	// wire schema, so a non-string value would arrive over gRPC and Unix as
	// whatever string pkg/api/grpc.authFactorServer stringifies it to (e.g.
	// `true`, not the JSON boolean true) while arriving over REST, QUIC, MCP
	// and embedded as the native type -- a six-transport contract violation
	// the service now refuses at write time (see persistChallenge) rather
	// than let ship silently. This is also the reason it must round-trip
	// through the KV store between ChallengeFactor and VerifyFactor
	// (go-qrdb#464): a factor that needs a non-string value -- bytes, an
	// exact integer -- must encode it itself (e.g. base64, decimal string),
	// exactly as every factor in this repository and in go-xkms already
	// does.
	MethodData map[string]any
}

// Expired reports whether the Challenge deadline has passed.
func (c *Challenge) Expired() bool {
	return time.Now().UTC().After(c.ExpiresAt)
}

// ResponseMetaPeerCertThumbprint is the Response.Metadata key under which the
// SERVER records the lowercase hex SHA-256 thumbprint of the client
// certificate the caller presented on a verified TLS session. A factor that
// binds to a certificate (the "mtls" factor) compares it against the enrolled
// certificate. It is never taken from the client: the server deletes any
// client-supplied value before it sets its own, and leaves it unset when the
// transport carried no verified client certificate.
const ResponseMetaPeerCertThumbprint = "peer_cert_thumbprint"

// Response is submitted by the custodian after signing the Challenge nonce.
type Response struct {
	// Value holds the signature or assertion produced by the factor.
	//
	// PKCS#11: DER-encoded ECDSA or RSA-PSS signature
	// PIV:     DER-encoded ECDSA signature
	// FIDO2:   base64url-encoded WebAuthn assertion JSON
	Value []byte

	// Metadata carries method-specific attributes accompanying the response.
	//
	// PKCS#11: {"algorithm": "CKM_ECDSA_SHA256", "key_id": "..."}
	// PIV:     {"algorithm": "ecdsap256", "slot": "9a"}
	// FIDO2:   {"client_data": "...", "authenticator_data": "..."}
	Metadata map[string]string
}
