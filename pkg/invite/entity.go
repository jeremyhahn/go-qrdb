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

// Package invite implements one-time enrollment invitations.
//
// An invitation solves a bootstrap problem specific to passwordless systems:
// a brand-new user holds no credential, so there is nothing for them to
// authenticate with in order to enroll their first one. The invitation is the
// out-of-band step that closes that loop — an administrator creates the user,
// the user follows a single-use link, and the redemption enrolls their first
// FIDO2 passkey or PIV credential through the ordinary
// authfactors.Factor.Enroll seam.
//
// # This is NOT admin bootstrap
//
// Platform administrator and security officer enrollment is certificate-based
// and has no invitation step — see pkg/firstboot (Paths A/B/C) and
// pkg/ceremony. Invitations were deliberately removed from that path, and
// nothing here reintroduces them. The two answer different questions: the admin
// already holds, or is issued, a certificate; an ordinary user has nothing yet.
//
// # Why this lives in qrdb
//
// An invitation is a pending-user record, and qrdb owns the user model. Keeping
// it here means every project above — go-xkms, go-quicbaas,
// go-trusted-platform — inherits one flow through the SDK instead of growing
// its own, and the redemption step reaches the same factor registry that
// already carries xkms's FIDO2 and PIV implementations.
package invite

import "time"

// Entity is the persisted invitation record.
//
// The plaintext token is NEVER stored. Only its SHA-256 digest is, so a
// database disclosure yields no redeemable invitations — the same reasoning
// that keeps password hashes rather than passwords. The digest doubles as the
// lookup key, which is why it carries a unique index.
type Entity struct {
	// ID is the DAO-assigned identifier, derived from TokenHash so a lookup
	// needs no secondary index scan.
	ID uint64 `json:"id"`

	// TokenHash is the hex-encoded SHA-256 of the plaintext token. It is the
	// only representation of the token that ever reaches storage.
	TokenHash string `json:"token_hash" index:"unique"`

	// UserID is the user this invitation activates.
	UserID string `json:"user_id" index:"true"`

	// TenantID scopes the invitation.
	TenantID string `json:"tenant_id" index:"true"`

	// ExpiresAt is when the invitation stops being redeemable. An invitation
	// is bounded in time as well as in count: a link that leaks a year later
	// should be worthless even if it was never redeemed.
	ExpiresAt time.Time `json:"expires_at"`

	// UsedAt records redemption. It is a timestamp rather than a boolean so an
	// auditor can answer "when was this consumed", which a bool cannot.
	// Zero means unredeemed.
	UsedAt time.Time `json:"used_at,omitempty"`

	// CreatedAt is when the invitation was issued.
	CreatedAt time.Time `json:"created_at"`
}

// EntityID returns the entity's unique identifier.
func (e *Entity) EntityID() uint64 { return e.ID }

// SetEntityID sets the entity's unique identifier.
func (e *Entity) SetEntityID(id uint64) { e.ID = id }

// Redeemed reports whether the invitation has already been consumed.
func (e *Entity) Redeemed() bool { return !e.UsedAt.IsZero() }

// Expired reports whether the invitation is past its expiry as of now.
func (e *Entity) Expired(now time.Time) bool { return now.After(e.ExpiresAt) }

// Usable reports whether the invitation may still be redeemed.
func (e *Entity) Usable(now time.Time) bool { return !e.Redeemed() && !e.Expired(now) }
