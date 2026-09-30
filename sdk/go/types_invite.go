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

package sdk

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// Invitation lifecycle — one flow for the whole stack.
//
// An invitation solves a bootstrap problem specific to passwordless systems: a
// brand-new user holds no credential, so there is nothing for them to
// authenticate with in order to enroll their first one. An administrator
// creates the user, the user follows a single-use link, and redemption enrolls
// their first factor through the ordinary Factor.Enroll seam — which is where
// go-xkms plugs in FIDO2 and PIV.
//
// This is NOT admin bootstrap. Platform administrator and security officer
// enrollment is certificate-based and has no invitation step; see
// FirstBootDependencies (Paths A/B/C) and the ceremony types. The two answer
// different questions, and invitations were deliberately removed from the
// admin path.
//
// It lives in qrdb because an invitation is a pending-user record and qrdb owns
// the user model. go-xkms, go-quicbaas and go-trusted-platform all consume this
// one implementation rather than each keeping their own.

// InviteEntity is a persisted invitation. The plaintext token is never stored —
// only its digest, so a database disclosure yields no redeemable invitations.
type InviteEntity = invite.Entity

// InviteStore is the invitation lifecycle surface.
type InviteStore = invite.Store

// InviteDAOStore is the qrdb-backed InviteStore, putting invitations under the
// same barrier encryption, replication, tenant namespacing and quota accounting
// as every other entity.
type InviteDAOStore = invite.DAOStore

// NewInviteStore builds the canonical invitation store over a KV store.
//
// The store must support atomic conditional writes; redemption is single-use
// and cannot be guaranteed without them. Construction fails with
// ErrInviteAtomicWriteUnsupported rather than deferring the discovery to the
// first redemption, by which point an invitation could already have enrolled
// two authenticators against one identity.
func NewInviteStore(store kvstore.KVStore) (*InviteDAOStore, error) {
	return invite.NewDAOStore(store)
}

// TenantInviteStore issues and redeems invitations in each tenant's own store,
// routed through a StoreResolver.
//
// This is the constructor a multi-tenant deployment should use. Isolation
// becomes a property of the storage rather than a field the caller remembers to
// check: a token issued for one tenant is not present in another tenant's store
// at all, so a redemption against the wrong tenant returns ErrInviteNotFound —
// the same answer a fabricated token gets, giving no cross-tenant signal.
//
// A barrier-isolated tenant's invitations are encrypted in that tenant's own
// barrier; a sealed tenant fails closed.
type TenantInviteStore = invite.TenantStore

// NewTenantInviteStore constructs the per-tenant invitation store.
func NewTenantInviteStore(resolver TenantStoreResolver) (*TenantInviteStore, error) {
	return invite.NewTenantStore(resolver)
}

// GenerateInviteToken returns a fresh invitation token. Callers normally use
// InviteStore.Create, which generates and stores in one step; this is exposed
// for consumers minting tokens for a store of their own.
func GenerateInviteToken() (string, error) { return invite.GenerateToken() }

// InviteTokenDigest returns the hex SHA-256 of a plaintext token — the only
// representation that ever reaches storage.
func InviteTokenDigest(token string) string { return invite.TokenDigest(token) }

// InviteTokenBytes is the length of the random material behind a token.
const InviteTokenBytes = invite.TokenBytes

// Invitation errors.
//
// A handler serving unauthenticated callers MUST collapse ErrInviteNotFound,
// ErrInviteExpired and ErrInviteAlreadyRedeemed into one opaque response.
// Telling a stranger that a token exists but is spent confirms a valid user was
// invited, which is the enumeration an opaque token exists to prevent.
var (
	// ErrInviteNotFound indicates no invitation matches the presented token.
	ErrInviteNotFound = invite.ErrNotFound
	// ErrInviteExpired indicates the invitation is past its expiry.
	ErrInviteExpired = invite.ErrExpired
	// ErrInviteAlreadyRedeemed indicates the invitation was already consumed.
	ErrInviteAlreadyRedeemed = invite.ErrAlreadyRedeemed
	// ErrInviteEmptyToken indicates an empty token was presented.
	ErrInviteEmptyToken = invite.ErrEmptyToken
	// ErrInviteMissingUserID indicates an invitation was created with no subject.
	ErrInviteMissingUserID = invite.ErrMissingUserID
	// ErrInviteNonPositiveTTL indicates a non-positive lifetime was requested.
	// An invitation that never expires is a permanent bearer credential.
	ErrInviteNonPositiveTTL = invite.ErrNonPositiveTTL
	// ErrInviteAtomicWriteUnsupported indicates the store cannot guarantee
	// single-use redemption. It IS ErrKVAtomicWriteUnsupported, the one
	// atomic-refusal marker; the name is kept for callers already using it.
	ErrInviteAtomicWriteUnsupported = kvstore.ErrAtomicWriteUnsupported
	// ErrInviteNilStoreResolver indicates NewTenantInviteStore was called with
	// no resolver, which would place every tenant's invitations together.
	ErrInviteNilStoreResolver = invite.ErrNilStoreResolver
)

// DefaultInviteTTL is the lifetime a daemon should use when an operator does
// not choose one. It is long enough for a person to act on an emailed link and
// short enough that a forgotten invitation stops being a live credential.
const DefaultInviteTTL = 24 * time.Hour
