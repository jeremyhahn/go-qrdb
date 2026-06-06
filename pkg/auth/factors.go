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

package auth

import "time"

// AuthFactorType identifies the kind of authentication factor a user has
// registered. Three first-class types are supported per
// docs/08-bootstrap-auth-architecture.md §5e.
type AuthFactorType string

const (
	// AuthFactorMTLS designates a client certificate / mTLS factor. The
	// credential lookup key is the SHA-256 fingerprint of the certificate.
	AuthFactorMTLS AuthFactorType = "mtls"

	// AuthFactorFIDO2 designates a WebAuthn / FIDO2 authenticator. The
	// credential lookup key is the WebAuthn credentialID bytes.
	AuthFactorFIDO2 AuthFactorType = "fido2"

	// AuthFactorPIV designates a PIV smart card (NIST SP 800-73-4). The
	// credential lookup key is the SHA-256 fingerprint of the slot certificate.
	AuthFactorPIV AuthFactorType = "piv"
)

// IsValid reports whether the receiver is one of the supported factor types.
func (t AuthFactorType) IsValid() bool {
	switch t {
	case AuthFactorMTLS, AuthFactorFIDO2, AuthFactorPIV:
		return true
	default:
		return false
	}
}

// String returns the canonical string form of the factor type.
func (t AuthFactorType) String() string {
	return string(t)
}

// Provenance identifies who created a factor record. It is a free-form
// string by spec; the canonical values are listed below.
const (
	// FactorCreatedBySelf indicates the user enrolled the factor themselves.
	FactorCreatedBySelf = "self"

	// FactorCreatedByAdmin indicates an administrator (SO PIN holder) enrolled
	// the factor on behalf of the user.
	FactorCreatedByAdmin = "admin"

	// FactorCreatedByCeremony indicates the factor was bound during a
	// custodian / unseal ceremony.
	FactorCreatedByCeremony = "ceremony"
)

// AuthFactor is a single authentication credential bound to a user. A user
// may have many AuthFactor records and authenticate with any one of them.
//
// The Credential blob carries the canonical lookup key for the factor type —
// the same bytes that an inbound auth event reduces to:
//
//	mtls : SHA-256(cert)        (32 bytes)
//	fido2: WebAuthn credentialID (variable length)
//	piv  : SHA-256(slot cert)   (32 bytes)
//
// Rich associated data (cert PEM, COSE public key, sign counter, AAGUID,
// slot ID, card serial number) belongs in Metadata, not in Credential.
// Keeping Credential narrow lets the lookup index be type-agnostic and
// vendor-agnostic.
//
// Hardware-specific verification (WebAuthn signature, PIV chain validation,
// mTLS chain validation) is the responsibility of the transport layers in
// go-xkms; this layer only provides the User <-> Factor mapping.
type AuthFactor struct {
	// ID is the stable factor identifier (UUID). Stable across rotations is
	// not guaranteed — Rotate produces a new ID.
	ID string `json:"id"`

	// UserID is the owning user's numeric identifier.
	UserID uint64 `json:"user_id"`

	// Type is one of the AuthFactor* constants.
	Type AuthFactorType `json:"type"`

	// Label is a user-supplied display name (e.g., "YubiKey 5C - work").
	Label string `json:"label,omitempty"`

	// Credential is the type-specific opaque credential blob.
	Credential []byte `json:"credential"`

	// Metadata holds vendor / AAGUID / cert serial / slot info. Never carries
	// private key material.
	Metadata map[string]string `json:"metadata,omitempty"`

	// RegisteredAt is the UTC time the factor was first added.
	RegisteredAt time.Time `json:"registered_at"`

	// LastUsedAt is the UTC time of the most recent successful login with
	// this factor. Nil before the first successful authentication.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	// RevokedAt is the UTC time the factor was soft-deleted. Nil while the
	// factor is active. A non-nil RevokedAt makes the factor unusable for
	// authentication.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// CreatedBy is the provenance string ("self", "admin", "ceremony").
	CreatedBy string `json:"created_by,omitempty"`
}

// IsActive reports whether the factor is currently usable for login.
func (f *AuthFactor) IsActive() bool {
	return f != nil && f.RevokedAt == nil
}

// Clone returns a deep copy of the factor. Useful when callers need to
// mutate a record without disturbing the canonical store representation.
func (f *AuthFactor) Clone() *AuthFactor {
	if f == nil {
		return nil
	}
	out := *f
	if f.Credential != nil {
		out.Credential = append([]byte(nil), f.Credential...)
	}
	if f.Metadata != nil {
		out.Metadata = make(map[string]string, len(f.Metadata))
		for k, v := range f.Metadata {
			out.Metadata[k] = v
		}
	}
	if f.LastUsedAt != nil {
		t := *f.LastUsedAt
		out.LastUsedAt = &t
	}
	if f.RevokedAt != nil {
		t := *f.RevokedAt
		out.RevokedAt = &t
	}
	return &out
}

// Principal is a transport-agnostic projection of a user used by login
// resolution. It carries only the fields needed to mint a session.
//
// pkg/auth deliberately does not import pkg/user (and vice versa) — callers
// adapt their concrete user types into a Principal via UserLookup.
type Principal struct {
	// ID is the numeric user identifier matching AuthFactor.UserID.
	ID uint64 `json:"id"`

	// Username is the human-readable login name.
	Username string `json:"username"`

	// Email is the contact email when present.
	Email string `json:"email,omitempty"`

	// TenantID identifies the tenant scope of this principal.
	TenantID string `json:"tenant_id,omitempty"`

	// Roles is the list of role names assigned to this principal.
	Roles []string `json:"roles,omitempty"`

	// Active reports whether the user account itself is enabled.
	Active bool `json:"active"`
}
