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

import (
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// entityType is the DAO namespace invitations are stored under.
const entityType = "invites"

// redemptionEntityType is the namespace for redemption markers. See DAOStore.Redeem.
const redemptionEntityType = "invite_redemptions"

// Store is the invitation lifecycle surface.
//
// It is an interface so a deployment may keep invitations elsewhere and so
// tests need no storage. DAOStore is the canonical implementation and is what
// every daemon in the stack uses through the SDK.
type Store interface {
	// Create issues an invitation and returns the plaintext token.
	//
	// The token is returned exactly once, here. Only its digest is stored, so
	// it cannot be recovered afterwards: a caller that loses it must issue a
	// new invitation.
	Create(ctx context.Context, userID, tenantID string, ttl time.Duration, opts ...CreateOption) (token string, entity *Entity, err error)

	// Lookup resolves a plaintext token without consuming it, so the invited
	// user can be shown what they are accepting.
	//
	// It returns ErrExpired or ErrAlreadyRedeemed for an invitation that exists
	// but is unusable. A handler serving unauthenticated callers MUST collapse
	// those into one opaque response; see ErrNotFound.
	Lookup(ctx context.Context, token string) (*Entity, error)

	// Redeem consumes an invitation exactly once and returns it. Concurrent
	// redemptions of one token resolve to exactly one winner; the losers get
	// ErrAlreadyRedeemed.
	Redeem(ctx context.Context, token string) (*Entity, error)

	// Reissue supersedes every unredeemed invitation userID holds and issues
	// a new one, returning its plaintext token exactly as Create does. The
	// supersession and the new invitation take effect in one guarded write,
	// so no earlier token stays redeemable beside the new one, and of two
	// racing reissues exactly one succeeds; the other gets
	// ErrReissueConflict, as does a reissue a concurrent Redeem beat. It
	// returns ErrUserNotFound for a user that was never invited and
	// ErrUserActive for one recorded active by Activate. A user who redeemed
	// an invitation but was never activated can still be reissued: that is
	// how a redemption whose activation failed recovers.
	Reissue(ctx context.Context, userID, tenantID string, ttl time.Duration, opts ...CreateOption) (token string, entity *Entity, err error)

	// Activate records that the account userID's invitation enrols is
	// active, after which Reissue refuses it with ErrUserActive. The caller
	// calls it once its own activation, which follows Redeem, has succeeded.
	// It is idempotent, and returns ErrUserNotFound for a user that was never
	// invited.
	Activate(ctx context.Context, userID, tenantID string, at time.Time) error

	// GC removes invitations that are redeemed, expired or superseded, and
	// returns how many were removed. An invitation a Reissue has written but
	// not yet made live is not superseded: it stays until it expires.
	GC(ctx context.Context, now time.Time) (removed int, err error)
}

// redemption is the marker written to claim an invitation.
//
// It is a separate entity rather than a flag on Entity because claiming must be
// atomic, and the only atomic primitive available is create-if-absent. Setting a
// field on Entity would require read-modify-write, which two callers can
// interleave; creating a marker cannot be interleaved, because exactly one
// create of a given key succeeds.
type redemption struct {
	ID uint64 `json:"id"`

	// TokenHash ties the marker to the invitation it claims, and is what the
	// ID is derived from so the marker's key is a pure function of the token.
	TokenHash string `json:"token_hash" index:"unique"`

	// RedeemedAt records when the claim happened, for audit.
	RedeemedAt time.Time `json:"redeemed_at"`
}

// EntityID returns the marker's unique identifier.
func (r *redemption) EntityID() uint64 { return r.ID }

// SetEntityID sets the marker's unique identifier.
func (r *redemption) SetEntityID(id uint64) { r.ID = id }

// DAOStore is the qrdb-backed Store.
//
// Invitations go through the generic DAO over a KVStore, which puts them under
// the same barrier encryption, replication, tenant namespacing and quota
// accounting as every other entity — rather than a bespoke key layout that
// would have to re-earn each of those properties.
type DAOStore struct {
	invites     dao.GenericDAO[*Entity]
	redemptions dao.GenericDAO[*redemption]
	subjects    dao.GenericDAO[*subject]
	inviteIDs   *dao.FieldHashGenerator
	claimIDs    *dao.FieldHashGenerator
	subjectIDs  *dao.FieldHashGenerator
}

// NewDAOStore builds a DAOStore over the given KV store.
//
// The store must support atomic conditional writes; without them redemption
// cannot be made single-use, and an invitation that can be redeemed twice
// enrolls two authenticators against one identity. Refusing at construction
// surfaces that at startup rather than at the first redemption.
func NewDAOStore(store kvstore.KVStore) (*DAOStore, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if _, ok := store.(kvstore.AtomicKVStore); !ok {
		// Single-use redemption needs an atomic conditional write; refuse at
		// construction with the one atomic-refusal marker rather than let an
		// invitation be redeemed twice.
		return nil, kvstore.ErrAtomicWriteUnsupported
	}

	inviteIDs := dao.NewFieldHashGenerator("TokenHash")
	invites, err := dao.New(
		store, entityType,
		func() *Entity { return &Entity{} },
		dao.WithIDGenerator(inviteIDs),
	)
	if err != nil {
		return nil, &StoreError{Op: "create invite dao", Cause: err}
	}

	claimIDs := dao.NewFieldHashGenerator("TokenHash")
	redemptions, err := dao.New(
		store, redemptionEntityType,
		func() *redemption { return &redemption{} },
		dao.WithIDGenerator(claimIDs),
	)
	if err != nil {
		return nil, &StoreError{Op: "create redemption dao", Cause: err}
	}

	subjectIDs := dao.NewFieldHashGenerator("Key")
	subjects, err := dao.New(
		store, subjectEntityType,
		func() *subject { return &subject{} },
		dao.WithIDGenerator(subjectIDs),
	)
	if err != nil {
		return nil, &StoreError{Op: "create invite subject dao", Cause: err}
	}

	return &DAOStore{
		invites:     invites,
		redemptions: redemptions,
		subjects:    subjects,
		inviteIDs:   inviteIDs,
		claimIDs:    claimIDs,
		subjectIDs:  subjectIDs,
	}, nil
}

// Create implements Store.
func (s *DAOStore) Create(ctx context.Context, userID, tenantID string, ttl time.Duration, opts ...CreateOption) (string, *Entity, error) {
	if userID == "" {
		return "", nil, ErrMissingUserID
	}
	if ttl <= 0 {
		return "", nil, ErrNonPositiveTTL
	}

	current, err := s.ensureSubject(ctx, tenantID, userID)
	if err != nil {
		return "", nil, err
	}
	token, entity, err := s.mint(ctx, userID, tenantID, ttl, current.Generation, opts)
	if err != nil {
		return "", nil, err
	}

	// A reissue that advanced the generation while this record was being
	// written has already superseded it. Report that rather than hand out a
	// token that is dead on arrival.
	if err := s.checkLive(ctx, entity); err != nil {
		s.discard(ctx, entity)
		if errors.Is(err, ErrSuperseded) {
			return "", nil, ErrReissueConflict
		}
		return "", nil, err
	}
	return token, entity, nil
}

// mint writes a new invitation for userID at the given generation and returns
// its plaintext token.
func (s *DAOStore) mint(ctx context.Context, userID, tenantID string, ttl time.Duration, generation uint64, opts []CreateOption) (string, *Entity, error) {
	token, err := GenerateToken()
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	entity := &Entity{
		TokenHash:  TokenDigest(token),
		UserID:     userID,
		TenantID:   tenantID,
		ExpiresAt:  now.Add(ttl),
		CreatedAt:  now,
		Generation: generation,
	}
	for _, opt := range opts {
		opt(entity)
	}

	// SaveIfAbsent, not Save: a digest collision would otherwise overwrite a
	// live invitation belonging to someone else. With 256 bits of randomness
	// this should never fire, which is exactly why it must not silently clobber
	// if it somehow does.
	stored, err := s.invites.SaveIfAbsent(ctx, entity)
	if err != nil {
		return "", nil, &StoreError{Op: "save invite", Cause: err}
	}
	if !stored {
		return "", nil, ErrTokenCollision
	}

	return token, entity, nil
}

// Lookup implements Store.
func (s *DAOStore) Lookup(ctx context.Context, token string) (*Entity, error) {
	entity, err := s.find(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := s.checkUsable(ctx, entity, time.Now().UTC()); err != nil {
		return nil, err
	}
	return entity, nil
}

// Redeem implements Store.
//
// Claiming writes a redemption marker with SaveIfAbsent. Exactly one create of
// a given key succeeds, so exactly one caller can claim a token however many
// race — the guarantee comes from the storage engine's conditional write, which
// for a replicated deployment is evaluated inside the Raft state machine.
//
// The marker is written BEFORE the invitation is stamped. If the process dies
// between the two, the invitation is already unredeemable, which is the safe
// direction: the alternative loses the marker and lets the token be redeemed
// again.
func (s *DAOStore) Redeem(ctx context.Context, token string) (*Entity, error) {
	entity, err := s.find(ctx, token)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.checkUsable(ctx, entity, now); err != nil {
		return nil, err
	}

	marker := &redemption{TokenHash: entity.TokenHash, RedeemedAt: now}
	claimed, err := s.redemptions.SaveIfAbsent(ctx, marker)
	if err != nil {
		return nil, &StoreError{Op: "claim invite", Cause: err}
	}
	if !claimed {
		return nil, ErrAlreadyRedeemed
	}
	if err := s.claimGeneration(ctx, entity); err != nil {
		return nil, err
	}

	// Stamp the invitation for audit and for GC. A failure here does not undo
	// the claim — the marker is authoritative — so it is reported without
	// pretending the invitation is still available.
	entity.UsedAt = now
	if err := s.invites.Save(ctx, entity); err != nil {
		return nil, &StoreError{Op: "stamp redeemed invite", Cause: err}
	}

	return entity, nil
}

// GC implements Store.
func (s *DAOStore) GC(ctx context.Context, now time.Time) (int, error) {
	removed := 0
	err := s.invites.ForEachPage(ctx, dao.PageQuery{Page: 1, PageSize: gcPageSize},
		func(page dao.PageResult[*Entity]) error {
			for _, entity := range page.Entities {
				if entity.Usable(now) {
					collect, liveErr := s.collectible(ctx, entity)
					if liveErr != nil {
						return liveErr
					}
					if !collect {
						continue
					}
				}
				if delErr := s.invites.Delete(ctx, entity); delErr != nil {
					return delErr
				}
				removed++

				// Remove the marker too, so a redeemed invitation leaves
				// nothing behind. Its absence cannot resurrect the token: the
				// invitation itself is gone.
				marker := &redemption{TokenHash: entity.TokenHash}
				marker.SetEntityID(s.claimIDs.NextID(marker))
				if delErr := s.redemptions.Delete(ctx, marker); delErr != nil && !dao.IsNotFound(delErr) {
					return delErr
				}
			}
			return nil
		})
	if err != nil {
		return removed, &StoreError{Op: "gc invites", Cause: err}
	}
	return removed, nil
}

// gcPageSize bounds how many invitations GC holds in memory at once.
const gcPageSize = 256

// checkUsable reports why an invitation cannot be redeemed, if it cannot.
//
// Redemption is consulted from the marker store rather than from Entity.UsedAt,
// because the marker is what the claim actually writes: a crash between the
// claim and the stamp leaves UsedAt zero on an invitation that IS spent, and
// trusting the field would hand the token out a second time.
func (s *DAOStore) checkUsable(ctx context.Context, entity *Entity, now time.Time) error {
	marker := &redemption{TokenHash: entity.TokenHash}
	_, err := s.redemptions.Get(ctx, s.claimIDs.NextID(marker))
	switch {
	case err == nil:
		return ErrAlreadyRedeemed
	case !dao.IsNotFound(err):
		// The marker's absence could not be confirmed, so this is NOT "not
		// yet redeemed": treating a storage fault as absence here would let
		// an outage make a spent token look usable again. Fail closed and
		// report the fault itself.
		return &StoreError{Op: "check invite redemption", Cause: err}
	}
	if entity.Redeemed() {
		return ErrAlreadyRedeemed
	}
	if entity.Expired(now) {
		return ErrExpired
	}
	return s.checkLive(ctx, entity)
}

// find resolves a plaintext token to its stored invitation.
func (s *DAOStore) find(ctx context.Context, token string) (*Entity, error) {
	if token == "" {
		return nil, ErrEmptyToken
	}

	probe := &Entity{TokenHash: TokenDigest(token)}
	entity, err := s.invites.Get(ctx, s.inviteIDs.NextID(probe))
	if err != nil {
		// dao.Get already distinguishes a genuine miss (dao.NotFoundError,
		// an empty read) from every other failure (go-qrdb#58/#65): only the
		// former means "no such invitation". Reporting anything else as
		// ErrNotFound would tell a caller presenting a VALID token during a
		// storage outage that it was forged, and tell an operator reading
		// the logs the same thing. Everything else is the backing store
		// failing to answer, so it is returned as its own typed error
		// instead of folded into the absence it is not.
		if dao.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, &StoreError{Op: "lookup invite", Cause: err}
	}

	// The entity ID is a 64-bit hash of the digest, so a collision is possible
	// in principle. Compare the full digest before treating the record as this
	// token's, or a collision would hand one user's invitation to another.
	if entity.TokenHash != probe.TokenHash {
		return nil, ErrNotFound
	}

	return entity, nil
}
