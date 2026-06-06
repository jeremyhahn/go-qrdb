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
// User-facing FactorClient (Phase 3)
// ---------------------------------------------------------------------------
//
// The Phase 3 FactorClient is distinct from both the ceremony-time
// AuthFactorClient (in authfactor_types.go) and the legacy 1-to-many user
// surface UserAuthFactorClient (in auth_factor_types.go). Phase 3 ships a
// concise enroll / list / remove / verify surface that wraps:
//
//   - pkg/auth/factors.go::AuthFactor (record model + AuthFactorType)
//   - pkg/service.UserAuthFactorServicer (Add / List / Remove)
//   - pkg/authfactors (Factor implementations: piv / fido2 / pkcs11)
//
// Verification is performed by the qrdb-side authfactors.Factor adapter
// for the requested type. The hardware-specific challenge / signature
// dance lives in the adapter; this SDK surface just provides the typed
// request / response carriers and routes by factor type.

// FactorType identifies the kind of authentication factor a user has
// registered. The three first-class types match auth.AuthFactorType so
// the wire format and the in-process model are byte-identical.
type FactorType string

const (
	// FactorTypeMTLS designates a client certificate / mTLS factor.
	FactorTypeMTLS FactorType = "mtls"

	// FactorTypeFIDO2 designates a WebAuthn / FIDO2 authenticator.
	FactorTypeFIDO2 FactorType = "fido2"

	// FactorTypePIV designates a PIV smart card.
	FactorTypePIV FactorType = "piv"
)

// IsValid reports whether the receiver is one of the supported factor
// types. The set is intentionally closed — adding a new type requires
// touching the authfactors package and the auth.AuthFactor model in
// lockstep.
func (t FactorType) IsValid() bool {
	switch t {
	case FactorTypeMTLS, FactorTypeFIDO2, FactorTypePIV:
		return true
	default:
		return false
	}
}

// String returns the canonical string form of the factor type.
func (t FactorType) String() string { return string(t) }

// Factor is the SDK projection of an enrolled authentication factor.
// It is the wire-format twin of auth.AuthFactor. The Credential blob
// carries the canonical lookup key for the factor type:
//
//	mtls : SHA-256(cert)        (32 bytes)
//	fido2: WebAuthn credentialID (variable length)
//	piv  : SHA-256(slot cert)   (32 bytes)
//
// Hardware-specific verification metadata (cert PEM, COSE public key,
// AAGUID, slot id, card serial number) belongs in Metadata.
type Factor struct {
	// ID is the stable factor identifier.
	ID string `json:"id"`

	// UserID is the owning user.
	UserID uint64 `json:"userID"`

	// Type is one of the FactorType constants.
	Type FactorType `json:"type"`

	// Label is a user-supplied display name.
	Label string `json:"label,omitempty"`

	// Credential is the canonical lookup-key bytes for the factor.
	Credential []byte `json:"credential"`

	// Metadata is type-specific data (vendor / AAGUID / cert serial / slot).
	Metadata map[string]string `json:"metadata,omitempty"`

	// RegisteredAt is the UTC time the factor was first added.
	RegisteredAt time.Time `json:"registeredAt"`

	// LastUsedAt is the UTC time of the most recent successful login.
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`

	// RevokedAt is the UTC time the factor was soft-deleted.
	RevokedAt *time.Time `json:"revokedAt,omitempty"`

	// CreatedBy is the provenance string ("self", "admin", "ceremony").
	CreatedBy string `json:"createdBy,omitempty"`
}

// IsActive reports whether the factor is currently usable for login.
func (f *Factor) IsActive() bool {
	return f != nil && f.RevokedAt == nil
}

// ---------------------------------------------------------------------------
// Verification carriers
// ---------------------------------------------------------------------------

// VerifyChallenge is the input to FactorClient.Verify. It carries the
// type-specific signed response or possession proof produced by the
// hardware token. The factor type is required so the transport layer
// can route to the correct authfactors.Factor implementation; the
// FactorID identifies which enrolled credential the proof is for.
type VerifyChallenge struct {
	// FactorID identifies the enrolled factor whose possession is being
	// proven. May be empty when the type-specific verification protocol
	// includes the credential identifier in MethodMetadata (FIDO2's
	// credentialID, for example) — implementations look it up there.
	FactorID string `json:"factorID,omitempty"`

	// Type is the factor type whose verifier handles this challenge.
	Type FactorType `json:"type"`

	// Nonce is the random bytes that the hardware signed. The hardware
	// signature must cover at least these bytes; implementations may
	// require a wider AAD per protocol.
	Nonce []byte `json:"nonce"`

	// Signature is the type-specific signed response.
	//
	//   mtls : DER-encoded ECDSA / RSA-PSS signature
	//   fido2: WebAuthn assertion JSON (base64url)
	//   piv  : DER-encoded ECDSA signature over Nonce
	Signature []byte `json:"signature"`

	// MethodMetadata carries factor-specific extras (FIDO2 client data,
	// authenticator data; PIV slot id; PKCS#11 mechanism). The verifier
	// reads only the fields it understands.
	MethodMetadata map[string]string `json:"methodMetadata,omitempty"`
}

// FactorVerifyResult is the outcome of a successful FactorClient.Verify. It
// echoes back the resolved factor and the user id so callers don't
// need a second round-trip to determine who just logged in.
type FactorVerifyResult struct {
	// UserID is the resolved user.
	UserID uint64 `json:"userID"`

	// FactorID is the verified factor's id.
	FactorID string `json:"factorID"`

	// Type is the verified factor's type.
	Type FactorType `json:"type"`

	// VerifiedAt is the UTC timestamp of the successful verification.
	VerifiedAt time.Time `json:"verifiedAt"`
}

// ---------------------------------------------------------------------------
// FactorClient interface
// ---------------------------------------------------------------------------

// FactorClient is the user-facing auth-factor surface added in Phase 3.
// It composes the persistence service with the hardware-specific
// verifiers so callers can enroll a credential, list a user's factors,
// remove one, and verify a signature.
//
// Hardware verification is delegated to the qrdb-side authfactors.Factor
// implementation registered for the requested FactorType. The transport
// is responsible for routing by type; the SDK consumer calls a single
// Verify entry point.
type FactorClient interface {
	// Enroll persists a new factor for the given user. The factor's ID
	// is generated when empty and RegisteredAt is set when zero. Returns
	// FactorAlreadyEnrolledError when the (Type, Credential) tuple
	// collides with an existing record.
	Enroll(ctx context.Context, userID uint64, factor Factor) error

	// List returns every factor belonging to the user — active and
	// revoked — in registration order.
	List(ctx context.Context, userID uint64) ([]Factor, error)

	// Remove soft-deletes the named factor. Implementations refuse to
	// remove the user's last active factor unless the underlying service
	// honours an admin override.
	Remove(ctx context.Context, userID uint64, factorID string) error

	// Verify exercises the registered authfactors.Factor for the given
	// type to assert that the challenge proves possession of the
	// enrolled credential. Returns nil on success and a typed error
	// otherwise.
	Verify(ctx context.Context, userID uint64, challenge VerifyChallenge) (*FactorVerifyResult, error)
}
