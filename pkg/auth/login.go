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

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"time"
)

// FactorStore is the minimum surface the login resolver requires from the
// AuthFactor service to perform User <-> Factor mapping. It is implemented
// by the canonical service in pkg/service and may be implemented by tests
// for unit-test isolation.
type FactorStore interface {
	// LookupByCredentialKey returns the factor whose (Type, lookup key)
	// matches. Returns ErrFactorNotFound if no record exists.
	LookupByCredentialKey(ctx context.Context, t AuthFactorType, key []byte) (*AuthFactor, error)

	// MarkUsed updates the LastUsedAt timestamp of a factor. Implementations
	// MAY perform this asynchronously.
	MarkUsed(ctx context.Context, factorID string, ts time.Time) error
}

// UserLookup resolves a numeric user ID into a Principal. Callers inject
// their own implementation that bridges the auth layer to whatever user
// store they use.
type UserLookup interface {
	// LookupUser returns the principal projection of the user with the given
	// ID. Implementations should return a non-nil error when the user does
	// not exist.
	LookupUser(ctx context.Context, userID uint64) (*Principal, error)
}

// UserLookupFunc adapts a plain function into a UserLookup.
type UserLookupFunc func(ctx context.Context, userID uint64) (*Principal, error)

// LookupUser implements UserLookup.
func (f UserLookupFunc) LookupUser(ctx context.Context, userID uint64) (*Principal, error) {
	return f(ctx, userID)
}

// LoginResolver maps an inbound auth event onto a (Principal, AuthFactor)
// pair. It performs no hardware verification; callers (transport layers in
// go-xkms) MUST verify the signature/possession proof before invoking
// Resolve, OR after Resolve returns, depending on the protocol semantics.
//
// The contract is:
//
//  1. Caller computes the canonical lookup key for the factor type.
//  2. Caller invokes Resolve.
//  3. Resolver reads the factor from the FactorStore.
//  4. Resolver rejects revoked factors with FactorRevokedError.
//  5. Resolver loads the user via UserLookup.
//  6. Resolver returns (principal, factor) and updates LastUsedAt async.
type LoginResolver struct {
	store FactorStore
	users UserLookup
}

// NewLoginResolver constructs a resolver bound to the given FactorStore and
// UserLookup. Both arguments must be non-nil.
func NewLoginResolver(store FactorStore, users UserLookup) *LoginResolver {
	if store == nil {
		panic("auth: NewLoginResolver: store must not be nil")
	}
	if users == nil {
		panic("auth: NewLoginResolver: users must not be nil")
	}
	return &LoginResolver{store: store, users: users}
}

// Resolve maps the (factor type, lookup key) tuple to a (principal, factor)
// pair. Returns:
//
//	ErrFactorNotFound        when no factor matches the lookup key
//	ErrFactorRevoked         when the matching factor has been revoked
//	ErrFactorTypeUnsupported when the supplied type is outside the canonical set
//
// Hardware-specific verification (FIDO2 signature, PIV chain, mTLS chain)
// is the CALLER's responsibility — Resolve only handles the database
// mapping.
//
// LastUsedAt is updated asynchronously via FactorStore.MarkUsed; failures
// of the async update do not surface to the caller because they are not
// required for correctness of the current login.
func (r *LoginResolver) Resolve(ctx context.Context, t AuthFactorType, lookupKey []byte) (*Principal, *AuthFactor, error) {
	if !t.IsValid() {
		return nil, nil, &FactorTypeUnsupportedError{Type: string(t)}
	}
	if len(lookupKey) == 0 {
		return nil, nil, &InvalidFactorError{Reason: "empty lookup key"}
	}

	factor, err := r.store.LookupByCredentialKey(ctx, t, lookupKey)
	if err != nil {
		return nil, nil, err
	}
	if factor == nil {
		return nil, nil, &FactorNotFoundError{}
	}
	if factor.RevokedAt != nil {
		return nil, nil, &FactorRevokedError{FactorID: factor.ID}
	}

	principal, err := r.users.LookupUser(ctx, factor.UserID)
	if err != nil {
		return nil, factor, err
	}
	if principal == nil {
		return nil, factor, &UserHasNoFactorsError{UserID: factor.UserID}
	}

	// Best-effort async last-used update. Failures here are non-fatal.
	now := time.Now().UTC()
	// #nosec G118 -- deliberately detached: the update must not be cancelled
	// when the login response returns; it carries its own timeout.
	go func(id string, t time.Time) {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.store.MarkUsed(bg, id, t)
	}(factor.ID, now)

	return principal, factor, nil
}

// ResolveFirst tries each lookup key in order and returns the first
// successful resolution. A FactorNotFoundError on one key advances to the
// next key; any other error (revoked factor, unsupported type, store
// failure) aborts immediately so security-relevant failures are never
// masked by a fallback. When every key misses, the final
// FactorNotFoundError is returned.
//
// The canonical caller is the embedded login transport, which resolves a
// renewal-stable SPKI key first and falls back to the legacy
// SHA-256(cert.Raw) key for factors enrolled before SPKI indexing existed.
// The fallback must happen inside one resolution call because the login
// session is consumed by the transport before resolution.
func (r *LoginResolver) ResolveFirst(ctx context.Context, t AuthFactorType, lookupKeys ...[]byte) (*Principal, *AuthFactor, error) {
	if len(lookupKeys) == 0 {
		return nil, nil, &InvalidFactorError{Reason: "no lookup keys supplied"}
	}
	var lastErr error
	for _, key := range lookupKeys {
		principal, factor, err := r.Resolve(ctx, t, key)
		if err == nil {
			return principal, factor, nil
		}
		var notFound *FactorNotFoundError
		if !errors.As(err, &notFound) {
			return nil, nil, err
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

// CredentialKey is a small helper that hashes raw certificate / blob bytes
// down to the canonical 32-byte lookup key used by the mtls and piv
// factor types. It is exported so caller code in xkms can produce keys
// consistently with the resolver's expectations.
//
// FIDO2 lookup keys are the raw credentialID bytes — do not pass them to
// CredentialKey.
func CredentialKey(blob []byte) []byte {
	sum := sha256.Sum256(blob)
	return sum[:]
}

// CredentialKeyHex returns the hex-encoded form of CredentialKey for use
// in storage indices.
func CredentialKeyHex(blob []byte) string {
	return hex.EncodeToString(CredentialKey(blob))
}

// SPKICredentialKey derives the renewal-stable secondary lookup key for
// certificate-based factors (mtls, piv): SHA-256 over the certificate's
// RawSubjectPublicKeyInfo. It is the raw-byte counterpart of
// user.ComputeSPKIThumbprint (which base64url-encodes the same digest) —
// THE single SPKI binding algorithm for the whole stack.
//
// Because the digest covers only the public key, a certificate renewed
// with the same keypair yields the same SPKICredentialKey, keeping login
// working across renewals.
func SPKICredentialKey(cert *x509.Certificate) []byte {
	if cert == nil || len(cert.RawSubjectPublicKeyInfo) == 0 {
		return nil
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return sum[:]
}
