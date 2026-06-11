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

package embedded

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MissingUserAuthFactorServiceError is returned by FactorClient when the
// embedded transport was constructed without WithUserAuthFactorService.
type MissingUserAuthFactorServiceError struct{}

// Error returns a human-readable description.
func (e *MissingUserAuthFactorServiceError) Error() string {
	return "embedded: user auth-factor service is not configured"
}

// FactorVerifier is the per-type hardware verification adapter consumed
// by FactorClient.Verify. The embedded transport routes the incoming
// challenge to the verifier registered for the requested FactorType;
// the verifier validates the signature against the stored credential
// and returns nil on success.
//
// Implementations live in pkg/authfactors/{piv,fido2,pkcs11} (Phase 4
// adapters wrap them). Phase 3 ships the registry plumbing so consumers
// can wire production verifiers via WithFactorVerifier and tests can
// inject fakes.
type FactorVerifier interface {
	// Type returns the factor type this verifier handles.
	Type() transport.FactorType

	// Verify asserts that the challenge signature proves possession of
	// the enrolled credential. Implementations consult challenge.Nonce,
	// challenge.Signature, and challenge.MethodMetadata. The factor
	// argument is the persisted record; verifiers MUST reject revoked
	// factors (RevokedAt != nil) before performing any cryptographic
	// work.
	Verify(ctx context.Context, factor *auth.AuthFactor, challenge transport.VerifyChallenge) error
}

// FactorChallenger is an OPTIONAL capability a FactorVerifier may implement
// to issue a per-method, tenant-scoped login challenge during BeginLogin —
// e.g. a WebAuthn relying-party-bound PublicKeyCredentialRequestOptions, or a
// PIV nonce. Verifiers that do not implement it fall back to the generic
// random server nonce. The returned metadata is echoed to the client in
// BeginLoginResponse.ChallengeMetadata and persisted in the login session so
// it can be threaded back into Verify at FinishLogin time.
//
// This is the migration seam toward a single tenant-aware FactorAuthenticator:
// once every registered verifier implements both FactorChallenger and
// TenantFactorVerifier, the three interfaces collapse into one (no lasting
// shim — see Phase 2 of the unification plan).
type FactorChallenger interface {
	BeginChallenge(ctx context.Context, tenantID string, factorType transport.FactorType, hint string) (challenge []byte, metadata map[string]string, expiresAt time.Time, err error)
}

// TenantFactorVerifier is an OPTIONAL tenant-aware verification capability.
// When a registered verifier implements it, the login ceremony calls
// VerifyTenant (passing the tenant resolved from the authenticating
// principal) instead of Verify, so the verifier can apply per-tenant
// configuration (e.g. the WebAuthn RP id/origin) during proof verification.
type TenantFactorVerifier interface {
	VerifyTenant(ctx context.Context, tenantID string, factor *auth.AuthFactor, challenge transport.VerifyChallenge) error
}

// verifyFactor dispatches to the tenant-aware verifier when the registered
// verifier implements TenantFactorVerifier, otherwise the base Verify.
// Centralises the tenant-vs-legacy choice for the login + factor paths.
func verifyFactor(ctx context.Context, tenantID string, v FactorVerifier, factor *auth.AuthFactor, challenge transport.VerifyChallenge) error {
	if tv, ok := v.(TenantFactorVerifier); ok {
		return tv.VerifyTenant(ctx, tenantID, factor, challenge)
	}
	return v.Verify(ctx, factor, challenge)
}

// factorClient is the embedded implementation of transport.FactorClient.
// It wraps the user-side service (UserAuthFactorServicer) for
// persistence and a per-type verifier registry for hardware checks.
type factorClient struct {
	transport *Transport
}

// Compile-time interface assertion.
var _ transport.FactorClient = (*factorClient)(nil)

// factorTypeToDomain converts the SDK FactorType into the domain
// auth.AuthFactorType.
func factorTypeToDomain(t transport.FactorType) auth.AuthFactorType {
	switch t {
	case transport.FactorTypeMTLS:
		return auth.AuthFactorMTLS
	case transport.FactorTypeFIDO2:
		return auth.AuthFactorFIDO2
	case transport.FactorTypePIV:
		return auth.AuthFactorPIV
	case transport.FactorTypePKCS11:
		return auth.AuthFactorPKCS11
	default:
		return auth.AuthFactorType(string(t))
	}
}

// factorTypeFromDomain converts the domain auth.AuthFactorType back to
// the SDK FactorType.
func factorTypeFromDomain(t auth.AuthFactorType) transport.FactorType {
	switch t {
	case auth.AuthFactorMTLS:
		return transport.FactorTypeMTLS
	case auth.AuthFactorFIDO2:
		return transport.FactorTypeFIDO2
	case auth.AuthFactorPIV:
		return transport.FactorTypePIV
	case auth.AuthFactorPKCS11:
		return transport.FactorTypePKCS11
	default:
		return transport.FactorType(string(t))
	}
}

// factorToTransport projects a domain AuthFactor into the SDK Factor.
func factorToTransport(f *auth.AuthFactor) transport.Factor {
	if f == nil {
		return transport.Factor{}
	}
	out := transport.Factor{
		ID:           f.ID,
		UserID:       f.UserID,
		Type:         factorTypeFromDomain(f.Type),
		Label:        f.Label,
		Credential:   append([]byte(nil), f.Credential...),
		RegisteredAt: f.RegisteredAt,
		LastUsedAt:   f.LastUsedAt,
		RevokedAt:    f.RevokedAt,
		CreatedBy:    f.CreatedBy,
	}
	if len(f.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(f.Metadata))
		for k, v := range f.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// factorToDomain projects an SDK Factor into the domain auth.AuthFactor.
func factorToDomain(userID uint64, f transport.Factor) *auth.AuthFactor {
	out := &auth.AuthFactor{
		ID:           f.ID,
		UserID:       userID,
		Type:         factorTypeToDomain(f.Type),
		Label:        f.Label,
		Credential:   append([]byte(nil), f.Credential...),
		RegisteredAt: f.RegisteredAt,
		LastUsedAt:   f.LastUsedAt,
		RevokedAt:    f.RevokedAt,
		CreatedBy:    f.CreatedBy,
	}
	if len(f.Metadata) > 0 {
		out.Metadata = make(map[string]string, len(f.Metadata))
		for k, v := range f.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// Enroll persists a new factor for the given user.
func (c *factorClient) Enroll(ctx context.Context, userID uint64, factor transport.Factor) error {
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if c.transport.userAuthFactorSvc == nil {
		return &MissingUserAuthFactorServiceError{}
	}
	if !factor.Type.IsValid() {
		return &transport.FactorTypeUnsupportedError{Type: string(factor.Type)}
	}
	if userID == 0 {
		return &transport.FactorNotFoundError{}
	}
	domain := factorToDomain(userID, factor)
	if err := c.transport.userAuthFactorSvc.Add(ctx, userID, domain); err != nil {
		return mapFactorError(err)
	}
	return nil
}

// List returns every factor belonging to the user.
func (c *factorClient) List(ctx context.Context, userID uint64) ([]transport.Factor, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if userID == 0 {
		return nil, &transport.FactorNotFoundError{}
	}
	factors, err := c.transport.userAuthFactorSvc.List(ctx, userID)
	if err != nil {
		return nil, mapFactorError(err)
	}
	out := make([]transport.Factor, len(factors))
	for i, f := range factors {
		out[i] = factorToTransport(f)
	}
	return out, nil
}

// Remove soft-deletes the named factor. The userID argument is
// validated for non-zero shape but is not used to look up the factor —
// the user-side service identifies factors by id alone. Callers passing
// a mismatched userID still see RBAC-level isolation upstream.
func (c *factorClient) Remove(ctx context.Context, userID uint64, factorID string) error {
	if c.transport.closed.Load() {
		return &transport.ClosedError{}
	}
	if c.transport.userAuthFactorSvc == nil {
		return &MissingUserAuthFactorServiceError{}
	}
	if userID == 0 {
		return &transport.FactorNotFoundError{}
	}
	if factorID == "" {
		return &transport.FactorNotFoundError{FactorID: factorID}
	}
	if err := c.transport.userAuthFactorSvc.Remove(ctx, factorID, false); err != nil {
		return mapFactorError(err)
	}
	return nil
}

// Verify exercises the configured FactorVerifier for the requested
// factor type.
func (c *factorClient) Verify(ctx context.Context, userID uint64, challenge transport.VerifyChallenge) (*transport.FactorVerifyResult, error) {
	if c.transport.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if c.transport.userAuthFactorSvc == nil {
		return nil, &MissingUserAuthFactorServiceError{}
	}
	if !challenge.Type.IsValid() {
		return nil, &transport.FactorTypeUnsupportedError{Type: string(challenge.Type)}
	}
	if userID == 0 {
		return nil, &transport.FactorNotFoundError{}
	}
	if challenge.FactorID == "" {
		return nil, &transport.FactorNotFoundError{FactorID: challenge.FactorID}
	}

	factor, err := c.transport.userAuthFactorSvc.Get(ctx, challenge.FactorID)
	if err != nil {
		return nil, mapFactorError(err)
	}
	if factor.UserID != userID {
		// Treat cross-user lookup as not-found to avoid leaking factor
		// existence to a different user.
		return nil, &transport.FactorNotFoundError{FactorID: challenge.FactorID}
	}
	if factor.RevokedAt != nil {
		return nil, &transport.FactorVerificationError{FactorID: factor.ID, Reason: "factor revoked"}
	}

	verifier := c.transport.factorVerifier(challenge.Type)
	if verifier == nil {
		return nil, &transport.FactorTypeUnsupportedError{Type: string(challenge.Type)}
	}

	// Standalone factor verification carries no resolved principal, so no
	// tenant context is threaded here ("" = default); a tenant-aware
	// verifier must tolerate the empty tenant on this direct path. The
	// tenant-scoped path is the login ceremony (FinishLogin), which passes
	// the principal's tenant.
	if err := verifyFactor(ctx, "", verifier, factor, challenge); err != nil {
		return nil, &transport.FactorVerificationError{FactorID: factor.ID, Reason: err.Error()}
	}

	now := time.Now().UTC()
	// Best-effort last-used update; failures are not fatal.
	_ = c.transport.userAuthFactorSvc.MarkUsed(ctx, factor.ID, now)

	return &transport.FactorVerifyResult{
		UserID:     factor.UserID,
		FactorID:   factor.ID,
		Type:       factorTypeFromDomain(factor.Type),
		VerifiedAt: now,
	}, nil
}

// mapFactorError translates qrdb auth-package errors into the SDK
// transport-package equivalents. Errors that have no SDK counterpart
// pass through unchanged so callers can still inspect them via
// errors.As.
func mapFactorError(err error) error {
	if err == nil {
		return nil
	}
	switch e := err.(type) {
	case *auth.FactorNotFoundError:
		return &transport.FactorNotFoundError{FactorID: e.FactorID}
	case *auth.FactorTypeUnsupportedError:
		return &transport.FactorTypeUnsupportedError{Type: e.Type}
	case *auth.FactorAlreadyExistsError:
		return &transport.FactorAlreadyEnrolledError{
			Type:     factorTypeFromDomain(e.Type),
			FactorID: e.FactorID,
		}
	default:
		return err
	}
}

// FactorClient returns the embedded factor client projection.
func (t *Transport) FactorClient() transport.FactorClient {
	return &factorClient{transport: t}
}

// WithUserAuthFactorService injects the qrdb UserAuthFactorServicer
// into the embedded transport. The service is consumed by FactorClient.
func WithUserAuthFactorService(svc service.UserAuthFactorServicer) Option {
	return func(t *Transport) {
		t.userAuthFactorSvc = svc
	}
}

// WithFactorVerifier registers a FactorVerifier for one factor type.
// Calling WithFactorVerifier multiple times for the same type
// overwrites the previous entry — last-write-wins, idempotent.
func WithFactorVerifier(v FactorVerifier) Option {
	return func(t *Transport) {
		if v == nil {
			return
		}
		t.factorVerifierMu.Lock()
		defer t.factorVerifierMu.Unlock()
		if t.factorVerifiers == nil {
			t.factorVerifiers = make(map[transport.FactorType]FactorVerifier)
		}
		t.factorVerifiers[v.Type()] = v
	}
}

// factorVerifier returns the verifier registered for the given factor
// type, or nil when none is registered.
func (t *Transport) factorVerifier(ft transport.FactorType) FactorVerifier {
	t.factorVerifierMu.RLock()
	defer t.factorVerifierMu.RUnlock()
	return t.factorVerifiers[ft]
}
