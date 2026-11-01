// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package invite

import "errors"

var (
	// ErrNotFound indicates no invitation matches the presented token.
	//
	// Callers MUST NOT distinguish this from ErrExpired or ErrAlreadyRedeemed
	// in anything they return to an unauthenticated caller: telling a stranger
	// that a token exists but is spent confirms a valid user was invited, which
	// is exactly the enumeration the opaque token is meant to prevent.
	ErrNotFound = errors.New("invite: not found")

	// ErrExpired indicates the invitation is past its expiry.
	ErrExpired = errors.New("invite: expired")

	// ErrAlreadyRedeemed indicates the invitation was already consumed.
	ErrAlreadyRedeemed = errors.New("invite: already redeemed")

	// ErrSuperseded indicates a later Reissue replaced the invitation. It is
	// as unusable as a redeemed one, and the same rule applies: never tell it
	// apart from ErrNotFound to an unauthenticated caller.
	ErrSuperseded = errors.New("invite: superseded")

	// ErrUserNotFound indicates Reissue was asked for a user that holds no
	// invitation in the tenant, so there is nothing to supersede.
	ErrUserNotFound = errors.New("invite: user has no invitation")

	// ErrUserActive indicates Reissue was asked for a user recorded active by
	// Activate: the account is active and needs no invitation.
	ErrUserActive = errors.New("invite: user already active")

	// ErrReissueConflict indicates another reissue, or a redemption, for the
	// same user landed first. The winner's state is the live one; the caller
	// may retry.
	ErrReissueConflict = errors.New("invite: concurrent reissue")

	// ErrEmptyToken indicates an empty token was presented.
	ErrEmptyToken = errors.New("invite: empty token")

	// ErrNilStore indicates a nil DAO/KV store was supplied.
	ErrNilStore = errors.New("invite: nil store")

	// ErrNilRecord indicates a nil invitation was supplied to Create.
	ErrNilRecord = errors.New("invite: nil record")

	// ErrMissingUserID indicates an invitation was created without a subject.
	ErrMissingUserID = errors.New("invite: missing user id")

	// ErrNonPositiveTTL indicates a non-positive lifetime was requested. An
	// invitation that never expires is a permanent bearer credential.
	ErrNonPositiveTTL = errors.New("invite: ttl must be positive")
)

// TokenGenerationError wraps a failure of the system random source.
type TokenGenerationError struct {
	Cause error
}

// Error implements error.
func (e *TokenGenerationError) Error() string {
	return "invite: token generation failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *TokenGenerationError) Unwrap() error { return e.Cause }

// StoreError wraps a failure of the underlying DAO.
type StoreError struct {
	Op    string
	Cause error
}

// Error implements error.
func (e *StoreError) Error() string {
	return "invite: " + e.Op + " failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *StoreError) Unwrap() error { return e.Cause }

// ErrTokenCollision indicates a freshly generated token's digest already
// exists. With 256 bits of randomness this should never occur; it is reported
// rather than ignored because the alternative is overwriting a live invitation
// belonging to someone else.
var ErrTokenCollision = errors.New("invite: generated token collides with an existing invitation")

// ErrNilStoreResolver is returned by NewTenantStore when no resolver is
// supplied. Without one there is no way to route an invitation into its
// tenant's store, and defaulting to a shared store would place every tenant's
// invitations together — the isolation this type exists to provide.
var ErrNilStoreResolver = errors.New("invite: nil tenant store resolver")
