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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ---------------------------------------------------------------------------
// User-side AuthFactor SDK transport types (M14)
// ---------------------------------------------------------------------------
//
// These types model the user-facing 1-to-many AuthFactor relationship per
// docs/auth-factors/architecture.md. They are deliberately distinct
// from the ceremony-style EnrollFactorRequest/Response defined in
// authfactor_types.go which model the challenge / verify enrolment flow used
// during custodian unseal ceremonies. The user-side API exposes Add, List,
// Get, LookupByCredentialKey, Remove, Rotate, Relabel, MarkUsed, and Login.
//
// The Credential field carries the canonical lookup key for the factor type
// (SHA-256 of the cert for mtls/piv, the WebAuthn credentialID for fido2).
// Rich associated data (cert PEM, COSE key, sign counter, AAGUID, slot ID,
// card serial) belongs in Metadata.

// AuthFactorRecord is the wire shape of an AuthFactor across all transports.
type AuthFactorRecord struct {
	// ID is the stable factor identifier.
	ID string `json:"id"`

	// UserID is the owning user's numeric identifier.
	UserID uint64 `json:"user_id"`

	// Type is one of "mtls", "fido2", "piv".
	Type string `json:"type"`

	// Label is a user-supplied display name.
	Label string `json:"label,omitempty"`

	// Credential is the canonical lookup-key bytes for this factor.
	Credential []byte `json:"credential"`

	// SPKICredential is the optional renewal-stable secondary lookup key:
	// SHA-256 of the certificate's SubjectPublicKeyInfo for mtls / piv
	// factors. Empty for fido2 and pre-SPKI enrolments.
	SPKICredential []byte `json:"spki_credential,omitempty"`

	// Metadata is type-specific data (vendor / AAGUID / cert serial / slot).
	Metadata map[string]string `json:"metadata,omitempty"`

	// RegisteredAt is the UTC time the factor was first added.
	RegisteredAt time.Time `json:"registered_at"`

	// LastUsedAt is the UTC time of the most recent successful login.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	// RevokedAt is the UTC time the factor was soft-deleted.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// CreatedBy is the provenance string ("self", "admin", "ceremony").
	CreatedBy string `json:"created_by,omitempty"`
}

// AuthFactorAddRequest is the request payload for adding a user auth factor.
type AuthFactorAddRequest struct {
	// UserID is the owning user.
	UserID uint64 `json:"user_id"`

	// Type is one of "mtls", "fido2", "piv".
	Type string `json:"type"`

	// Label is the user-supplied display name.
	Label string `json:"label,omitempty"`

	// Credential is the canonical lookup-key bytes for the factor.
	Credential []byte `json:"credential"`

	// SPKICredential is the optional renewal-stable secondary lookup key:
	// SHA-256 of the certificate's SubjectPublicKeyInfo for mtls / piv
	// factors. Empty for fido2.
	SPKICredential []byte `json:"spki_credential,omitempty"`

	// Metadata is type-specific data.
	Metadata map[string]string `json:"metadata,omitempty"`

	// CreatedBy is the provenance string ("self", "admin", "ceremony").
	CreatedBy string `json:"created_by,omitempty"`

	// IfNoActiveType makes the add conditional: it is refused with
	// ErrFactorConditionFailed when the user already holds an active factor
	// of Type. Of N callers racing the same claim, exactly one wins.
	IfNoActiveType bool `json:"if_no_active_type,omitempty"`

	// ExpectedFactorSetVersion makes the add conditional: it is refused with
	// ErrFactorConditionFailed unless the user's factor-set version -- the
	// one AuthFactorListResponse reports -- still equals it.
	ExpectedFactorSetVersion *uint64 `json:"expected_factor_set_version,omitempty"`
}

// Condition returns the condition the add names, or nil for an
// unconditional add.
func (r *AuthFactorAddRequest) Condition() *auth.FactorAddCondition {
	return auth.FactorAddConditionOf(r.IfNoActiveType, r.ExpectedFactorSetVersion)
}

// AuthFactorAddResponse is returned after a successful Add.
type AuthFactorAddResponse struct {
	// Factor is the persisted record (with ID and RegisteredAt populated).
	Factor AuthFactorRecord `json:"factor"`

	// FactorSetVersion is the factor-set version a conditional add committed.
	// It is zero for an unconditional add.
	FactorSetVersion uint64 `json:"factor_set_version,omitempty"`
}

// AuthFactorListRequest is the request payload for listing a user's factors.
type AuthFactorListRequest struct {
	// UserID is the owner whose factors to list.
	UserID uint64 `json:"user_id"`
}

// AuthFactorListResponse is the list of factors for a user.
type AuthFactorListResponse struct {
	// Factors is every record (active and revoked) belonging to the user.
	Factors []AuthFactorRecord `json:"factors"`

	// Total is len(Factors) for caller convenience.
	Total int `json:"total"`

	// FactorSetVersion is the user's factor-set version, read before the
	// factors were listed: a conditional add guarded on it fails rather than
	// succeeds if the set changed in between.
	FactorSetVersion uint64 `json:"factor_set_version"`
}

// AuthFactorGetRequest is the request payload for retrieving a single factor.
type AuthFactorGetRequest struct {
	// FactorID is the stable factor identifier.
	FactorID string `json:"factor_id"`
}

// AuthFactorGetResponse carries the requested factor.
type AuthFactorGetResponse struct {
	// Factor is the requested record.
	Factor AuthFactorRecord `json:"factor"`
}

// AuthFactorRemoveRequest is the request payload for soft-deleting a factor.
type AuthFactorRemoveRequest struct {
	// FactorID is the factor to revoke.
	FactorID string `json:"factor_id"`

	// Force bypasses the last-active-factor guard. When true and the factor
	// is the user's last active factor the user is moved to inactive.
	Force bool `json:"force,omitempty"`
}

// AuthFactorRemoveResponse is the empty-OK response for Remove.
type AuthFactorRemoveResponse struct {
	// Status is "revoked" on success.
	Status string `json:"status"`
}

// AuthFactorDeleteRequest is the request payload for hard-deleting a factor:
// the record and its credential, SPKI and user index entries go together, so
// a rolled-back add can bind the same credential again. A revoked factor is
// refused.
type AuthFactorDeleteRequest struct {
	// FactorID is the factor to delete.
	FactorID string `json:"factor_id"`

	// Force bypasses the last-active-factor guard. When true and the factor
	// is the user's last active factor the user is moved to inactive.
	Force bool `json:"force,omitempty"`
}

// AuthFactorDeleteResponse is the empty-OK response for Delete.
type AuthFactorDeleteResponse struct {
	// Status is "deleted" on success.
	Status string `json:"status"`
}

// AuthFactorRotateRequest atomically replaces an old factor with a new one.
type AuthFactorRotateRequest struct {
	// OldFactorID is the factor to revoke.
	OldFactorID string `json:"old_factor_id"`

	// NewFactor is the replacement record. UserID is inherited from the
	// old factor when zero.
	NewFactor AuthFactorAddRequest `json:"new_factor"`
}

// Validate refuses a rotation whose NewFactor names an add condition. A
// rotation is not conditional; a guard it dropped would let the caller
// believe the rotation had been decided by one.
func (r *AuthFactorRotateRequest) Validate() error {
	return auth.RefuseRotationCondition(r.NewFactor.Condition())
}

// AuthFactorRotateResponse carries the new factor record.
type AuthFactorRotateResponse struct {
	// NewFactor is the persisted replacement.
	NewFactor AuthFactorRecord `json:"new_factor"`
}

// AuthFactorRelabelRequest renames a factor.
type AuthFactorRelabelRequest struct {
	// FactorID is the factor to relabel.
	FactorID string `json:"factor_id"`

	// Label is the new label.
	Label string `json:"label"`
}

// AuthFactorRelabelResponse confirms the relabel.
type AuthFactorRelabelResponse struct {
	// Factor is the updated record.
	Factor AuthFactorRecord `json:"factor"`
}

// AuthFactorLoginRequest performs login resolution server-side. The transport
// layer (xkms) is responsible for hardware verification BEFORE sending this
// request — Login here only resolves the (Type, Credential) tuple to a User.
type AuthFactorLoginRequest struct {
	// Type is one of "mtls", "fido2", "piv".
	Type string `json:"type"`

	// LookupKey is the canonical key bytes:
	//   mtls : SHA-256(certificate)
	//   fido2: WebAuthn credentialID bytes
	//   piv  : SHA-256(slot certificate)
	LookupKey []byte `json:"lookup_key"`
}

// AuthFactorLoginResponse carries the resolved principal and factor.
type AuthFactorLoginResponse struct {
	// UserID is the resolved user identifier.
	UserID uint64 `json:"user_id"`

	// Username is the human-readable login name.
	Username string `json:"username"`

	// Email is the contact email when present.
	Email string `json:"email,omitempty"`

	// TenantID is the tenant scope.
	TenantID string `json:"tenant_id,omitempty"`

	// Roles is the role set assigned to the user.
	Roles []string `json:"roles,omitempty"`

	// Active reports whether the user account is enabled.
	Active bool `json:"active"`

	// Factor is the factor that authorised the login.
	Factor AuthFactorRecord `json:"factor"`
}

// ---------------------------------------------------------------------------
// UserAuthFactorClient interface
// ---------------------------------------------------------------------------

// UserAuthFactorClient is the user-side auth-factor surface exposed by every
// QRDB transport. It mirrors the server-side service.UserAuthFactorServicer
// interface and is the single SDK entry point for the operations called out
// in docs/auth-factors/architecture.md.
type UserAuthFactorClient interface {
	// AddAuthFactor persists a new factor for a user.
	AddAuthFactor(ctx context.Context, req *AuthFactorAddRequest) (*AuthFactorAddResponse, error)

	// ListAuthFactors returns every factor (active and revoked) owned by
	// the user.
	ListAuthFactors(ctx context.Context, req *AuthFactorListRequest) (*AuthFactorListResponse, error)

	// GetAuthFactor returns the factor with the given ID.
	GetAuthFactor(ctx context.Context, req *AuthFactorGetRequest) (*AuthFactorGetResponse, error)

	// RemoveAuthFactor soft-deletes a factor. ErrLastFactor is returned
	// when force=false and the factor is the user's last active one.
	RemoveAuthFactor(ctx context.Context, req *AuthFactorRemoveRequest) (*AuthFactorRemoveResponse, error)

	// DeleteAuthFactor hard-deletes a factor and frees its credential and
	// SPKI indexes, so a rolled-back add can bind the same credential again.
	// A revoked factor is refused with ErrFactorRevoked; force works as for
	// RemoveAuthFactor.
	DeleteAuthFactor(ctx context.Context, req *AuthFactorDeleteRequest) (*AuthFactorDeleteResponse, error)

	// RotateAuthFactor atomically adds the new factor and revokes the old.
	RotateAuthFactor(ctx context.Context, req *AuthFactorRotateRequest) (*AuthFactorRotateResponse, error)

	// RelabelAuthFactor updates the label on a factor.
	RelabelAuthFactor(ctx context.Context, req *AuthFactorRelabelRequest) (*AuthFactorRelabelResponse, error)

	// LoginAuthFactor resolves an inbound (Type, LookupKey) to a Principal
	// and AuthFactor. Hardware verification MUST be performed by the
	// caller; this RPC only performs the database mapping.
	LoginAuthFactor(ctx context.Context, req *AuthFactorLoginRequest) (*AuthFactorLoginResponse, error)
}
