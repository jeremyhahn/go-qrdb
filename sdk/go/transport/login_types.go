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
// LoginClient types
// ---------------------------------------------------------------------------
//
// LoginClient wraps qrdb's auth.LoginResolver — the resolver maps an
// inbound (FactorType, lookup key) tuple to a (Principal, Factor) pair
// without performing hardware verification. The transport layer (this
// SDK + xkms in Phase 4) is responsible for completing the hardware
// proof before invoking BeginLogin / FinishLogin.
//
// LoginClient also exposes a session-issuance pair: BeginLogin returns
// a server-issued nonce + an opaque login-session id; FinishLogin
// consumes the proof and mints a bearer. Resolved identities round-trip
// through ResolveIdentity for callers that want to validate an existing
// bearer.

// BeginLoginRequest opens a login attempt for the given factor type.
// The transport produces a server-side nonce and binds it to a short-
// lived login-session id; the caller signs the nonce with the user's
// hardware credential and submits it via FinishLogin.
type BeginLoginRequest struct {
	// Type is the factor type the caller intends to use.
	Type FactorType `json:"type"`

	// Hint is an optional caller-supplied hint that the server uses to
	// resolve the candidate user (e.g. a username or a tenant id). An
	// empty hint is permitted — the caller may identify the user only
	// via the factor's lookup key in FinishLogin.
	Hint string `json:"hint,omitempty"`

	// TenantID scopes the login attempt to a tenant so the registered
	// factor authenticator can apply per-tenant configuration when it
	// issues the challenge (e.g. the WebAuthn relying-party id/origin).
	// Empty selects the default/global configuration.
	TenantID string `json:"tenantID,omitempty"`
}

// BeginLoginResponse carries the server-issued login challenge.
type BeginLoginResponse struct {
	// SessionID is the opaque login-session identifier. Callers must
	// echo it back in FinishLogin.
	SessionID string `json:"sessionID"`

	// Nonce is the bytes the user's hardware must sign. For a generic
	// factor this is a random server nonce; for a method-specific
	// authenticator (e.g. WebAuthn) it is the challenge bytes the
	// authenticator issued.
	Nonce []byte `json:"nonce"`

	// ChallengeMetadata carries method-specific challenge data the client
	// needs to complete the ceremony and must echo back in FinishLogin
	// (e.g. WebAuthn PublicKeyCredentialRequestOptions, PIV slot id).
	// Nil for generic nonce challenges.
	ChallengeMetadata map[string]string `json:"challengeMetadata,omitempty"`

	// ExpiresAt is the deadline by which FinishLogin must be invoked.
	ExpiresAt time.Time `json:"expiresAt"`
}

// FinishLoginRequest carries the result of the hardware-signing step.
// The Type and LookupKey fields locate the enrolled factor; Signature
// proves possession.
type FinishLoginRequest struct {
	// SessionID echoes the value returned by BeginLogin.
	SessionID string `json:"sessionID"`

	// Type is the factor type used to authenticate.
	Type FactorType `json:"type"`

	// LookupKey is the canonical credential lookup key for the factor.
	LookupKey []byte `json:"lookupKey"`

	// Signature is the type-specific proof produced by the hardware.
	Signature []byte `json:"signature"`

	// MethodMetadata carries factor-specific extras (FIDO2 authenticator
	// data, PIV slot id, etc.).
	MethodMetadata map[string]string `json:"methodMetadata,omitempty"`
}

// FinishLoginResponse delivers the issued bearer and the resolved
// identity.
type FinishLoginResponse struct {
	// Bearer is the opaque token the caller stores and presents on
	// subsequent requests via SessionClient.ResolveIdentity.
	Bearer string `json:"bearer"`

	// Identity is the resolved authenticated entity.
	Identity *Identity `json:"identity"`

	// ExpiresAt is the bearer's expiry time.
	ExpiresAt time.Time `json:"expiresAt"`

	// FactorID is the factor that authorised this login.
	FactorID string `json:"factorID,omitempty"`
}

// ---------------------------------------------------------------------------
// LoginClient interface
// ---------------------------------------------------------------------------

// LoginClient is the SDK surface for the qrdb login resolver and
// session issuer. The pair Begin / Finish models a stateful challenge
// exchange so transports can plug the round-trip into their native
// session model (HTTP cookies, gRPC metadata, native QUIC streams).
//
// ResolveIdentity is the bearer-validation entry point — given a token
// previously minted by FinishLogin (or an externally minted bearer that
// the daemon recognises), it returns the live Identity.
type LoginClient interface {
	// ResolveIdentity returns the live Identity bound to the given
	// session bearer. Returns InvalidBearerError when the bearer is
	// unknown or expired.
	ResolveIdentity(ctx context.Context, sessionToken string) (*Identity, error)

	// BeginLogin opens a login attempt and returns the server-issued
	// nonce + opaque session id.
	BeginLogin(ctx context.Context, req BeginLoginRequest) (*BeginLoginResponse, error)

	// FinishLogin consumes the hardware-signed nonce, resolves the
	// (Type, LookupKey) tuple to a user, mints a bearer, and returns
	// both the bearer and the resolved identity.
	FinishLogin(ctx context.Context, req FinishLoginRequest) (*FinishLoginResponse, error)

	// Logout invalidates the bearer so subsequent ResolveIdentity calls
	// return InvalidBearerError.
	Logout(ctx context.Context, sessionToken string) error
}
