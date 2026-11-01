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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// subjectEntityType is the namespace for per-user invitation state. See subject.
const subjectEntityType = "invite_subjects"

// subjectCASAttempts bounds how often Redeem and Activate retry a swap of a
// subject other writers are changing. Every lost swap is decided by a writer
// that either superseded the invitation (Redeem then refuses) or already
// recorded what the retry would (it is then done), so a second attempt
// settles it; the bound only stops a misbehaving store from spinning forever.
const subjectCASAttempts = 8

// firstGeneration is the generation a user's subject record starts at. It
// is one, not zero, because an invitation persisted before subject records
// existed carries generation zero and has no subject: creating the subject
// at zero would make every such invitation live again beside the one minted
// now, so a reissue that falls back to Create would not revoke the tokens it
// replaces. Starting above zero supersedes every one of them. Subjects are
// never deleted, so no invitation minted under a subject predates its
// subject's record.
const firstGeneration uint64 = 1

// subject is one user's invitation state within a tenant: which generation
// of invitation is live, and whether one has been redeemed.
//
// It is what makes Reissue atomic. Every invitation records the generation
// it was minted at, and an invitation is redeemable only while its generation
// is the subject's. Reissue advances the generation with ONE
// CompareAndSwap, so every earlier invitation is superseded in the same
// guarded write that makes the new one live; there is no window in which an
// old and a new token are both redeemable, and of two racing reissues
// exactly one swap lands.
type subject struct {
	ID uint64 `json:"id"`

	// Key identifies the user within the tenant and is what the ID is derived
	// from, so the subject's key is a pure function of (tenant, user).
	Key string `json:"key" index:"unique"`

	// TenantID and UserID are the two halves of Key, kept for audit.
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`

	// Generation is the generation of the live invitations. Reissue advances
	// it; an invitation minted at any other generation is superseded.
	Generation uint64 `json:"generation"`

	// Redeemed records that an invitation of the live generation was
	// redeemed. It is what serialises Redeem against Reissue: both swap this
	// record, so whichever lands second sees the first and loses. It does
	// NOT make the user active -- a redemption whose activation then failed
	// leaves the account pending, and Reissue is how it recovers -- and it
	// is cleared when Reissue advances the generation.
	Redeemed bool `json:"redeemed"`

	// ActivatedAt records the caller's Activate: the account the invitation
	// enrols is active, so Reissue refuses it. Zero means not yet.
	ActivatedAt time.Time `json:"activated_at"`
}

// EntityID returns the subject's unique identifier.
func (s *subject) EntityID() uint64 { return s.ID }

// SetEntityID sets the subject's unique identifier.
func (s *subject) SetEntityID(id uint64) { s.ID = id }

// activated reports whether the caller recorded the user's account active.
func (s *subject) activated() bool { return !s.ActivatedAt.IsZero() }

// subjectKey joins tenant and user with a NUL, which neither carries, so two
// distinct pairs never share a key.
func subjectKey(tenantID, userID string) string {
	return tenantID + "\x00" + userID
}

// Reissue implements Store.
//
// The new invitation is written first, at the next generation, where it is
// not yet redeemable; the subject's generation is then advanced with one
// CompareAndSwap. That swap is the operation's single commit point: it
// supersedes every earlier invitation and makes the new one live together.
// A lost swap means another writer moved the subject first, so the new record
// is removed and the caller is told why.
func (s *DAOStore) Reissue(ctx context.Context, userID, tenantID string, ttl time.Duration, opts ...CreateOption) (string, *Entity, error) {
	if userID == "" {
		return "", nil, ErrMissingUserID
	}
	if ttl <= 0 {
		return "", nil, ErrNonPositiveTTL
	}

	current, err := s.loadSubject(ctx, tenantID, userID)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", nil, ErrUserNotFound
		}
		return "", nil, err
	}
	if current.activated() {
		return "", nil, ErrUserActive
	}

	next := *current
	next.Generation++
	next.Redeemed = false
	token, entity, err := s.mint(ctx, userID, tenantID, ttl, next.Generation, opts)
	if err != nil {
		return "", nil, err
	}

	swapped, err := s.subjects.CompareAndSwap(ctx, current, &next)
	if err != nil {
		// The outcome is unknown: the swap may have landed, and with it the
		// new invitation. Its token is not returned, so it is unreachable and
		// expires; removing it here could delete the one live invitation.
		return "", nil, &StoreError{Op: "advance invite subject", Cause: err}
	}
	if !swapped {
		s.discard(ctx, entity)
		return "", nil, s.lostReissue(ctx, tenantID, userID)
	}
	return token, entity, nil
}

// lostReissue explains a lost subject swap: the user activated in between,
// or another reissue landed first.
func (s *DAOStore) lostReissue(ctx context.Context, tenantID, userID string) error {
	current, err := s.loadSubject(ctx, tenantID, userID)
	if err != nil {
		return &StoreError{Op: "reload invite subject", Cause: err}
	}
	if current.activated() {
		return ErrUserActive
	}
	return ErrReissueConflict
}

// ensureSubject returns the user's subject, creating it at generation zero if
// the user has none yet. Of two racing creators exactly one write lands, and
// the other reads the winner's.
func (s *DAOStore) ensureSubject(ctx context.Context, tenantID, userID string) (*subject, error) {
	current, err := s.loadSubject(ctx, tenantID, userID)
	if err == nil {
		return current, nil
	}
	if !dao.IsNotFound(err) {
		return nil, err
	}
	fresh := &subject{Key: subjectKey(tenantID, userID), TenantID: tenantID, UserID: userID, Generation: firstGeneration}
	stored, err := s.subjects.SaveIfAbsent(ctx, fresh)
	if err != nil {
		return nil, &StoreError{Op: "create invite subject", Cause: err}
	}
	if stored {
		return fresh, nil
	}
	current, err = s.loadSubject(ctx, tenantID, userID)
	if err != nil {
		if dao.IsNotFound(err) {
			// The key is taken but holds another pair's subject: a 64-bit
			// hash collision, which must not merge two users' state.
			return nil, ErrTokenCollision
		}
		return nil, err
	}
	return current, nil
}

// loadSubject reads the user's subject. A genuine miss is returned as a
// dao.NotFoundError, so callers can tell it from a storage fault, which is
// returned as a *StoreError.
func (s *DAOStore) loadSubject(ctx context.Context, tenantID, userID string) (*subject, error) {
	probe := &subject{Key: subjectKey(tenantID, userID)}
	current, err := s.subjects.Get(ctx, s.subjectIDs.NextID(probe))
	if err != nil {
		if dao.IsNotFound(err) {
			return nil, err
		}
		return nil, &StoreError{Op: "lookup invite subject", Cause: err}
	}
	// The ID is a 64-bit hash of the key; compare the full key so a collision
	// is a miss rather than another user's state.
	if current.Key != probe.Key {
		return nil, &dao.NotFoundError{EntityType: subjectEntityType, ID: current.ID}
	}
	return current, nil
}

// checkLive reports ErrSuperseded unless entity is of its user's live
// generation.
func (s *DAOStore) checkLive(ctx context.Context, entity *Entity) error {
	current, err := s.loadSubject(ctx, entity.TenantID, entity.UserID)
	if err != nil {
		if dao.IsNotFound(err) {
			// Every invitation is minted under a subject, so one without
			// a subject is not live: fail closed.
			return ErrSuperseded
		}
		return err
	}
	if current.Generation != entity.Generation {
		return ErrSuperseded
	}
	return nil
}

// collectible reports whether GC may remove entity, an invitation that is
// neither redeemed nor expired, telling a storage fault (an error) from an
// answer. Only a superseded invitation is collectible: one minted below its
// user's live generation, or under no subject. The generation only ever
// advances, so it can never become live again.
//
// An invitation minted ABOVE the live generation is staged: Reissue writes it
// before the subject swap that is its commit point, so while that swap is in
// flight it is about to become the user's one live invitation, and after a
// swap that failed with an unknown outcome GC cannot tell whether it did. It
// is not collectible until it expires, when its outcome no longer matters;
// the record of a swap known to be lost Reissue removes itself.
func (s *DAOStore) collectible(ctx context.Context, entity *Entity) (bool, error) {
	current, err := s.loadSubject(ctx, entity.TenantID, entity.UserID)
	if err != nil {
		if dao.IsNotFound(err) {
			// Every invitation is minted under a subject, so one without a
			// subject can never become live.
			return true, nil
		}
		return false, err
	}
	return entity.Generation < current.Generation, nil
}

// claimGeneration records that entity, already claimed, was redeemed. It is
// the redemption's commit point against Reissue: whichever of the two swaps
// the subject first wins, so a reissue that lands first supersedes the claim
// and one that lands second loses its swap and is told to retry.
func (s *DAOStore) claimGeneration(ctx context.Context, entity *Entity) error {
	return s.swapSubject(ctx, entity.TenantID, entity.UserID, ErrSuperseded, func(current *subject) (bool, error) {
		if current.Generation != entity.Generation {
			return false, ErrSuperseded
		}
		if current.Redeemed {
			// Another invitation of this generation was redeemed first.
			return false, nil
		}
		current.Redeemed = true
		return true, nil
	})
}

// Activate implements Store.
func (s *DAOStore) Activate(ctx context.Context, userID, tenantID string, at time.Time) error {
	if userID == "" {
		return ErrMissingUserID
	}
	return s.swapSubject(ctx, tenantID, userID, ErrUserNotFound, func(current *subject) (bool, error) {
		if current.activated() {
			return false, nil
		}
		current.ActivatedAt = at
		return true, nil
	})
}

// swapSubject applies change to the user's subject with a CompareAndSwap,
// retrying while other writers move it, and returns missing when the user has
// no subject. change edits its argument and reports whether there is anything
// to write; an error from it is returned as is.
func (s *DAOStore) swapSubject(ctx context.Context, tenantID, userID string, missing error, change func(*subject) (bool, error)) error {
	for range subjectCASAttempts {
		current, err := s.loadSubject(ctx, tenantID, userID)
		if err != nil {
			if dao.IsNotFound(err) {
				return missing
			}
			return err
		}
		next := *current
		write, err := change(&next)
		if err != nil || !write {
			return err
		}
		swapped, err := s.subjects.CompareAndSwap(ctx, current, &next)
		if err != nil {
			return &StoreError{Op: "update invite subject", Cause: err}
		}
		if swapped {
			return nil
		}
	}
	return &StoreError{Op: "update invite subject", Cause: ErrReissueConflict}
}

// discard removes an invitation that never became live. It is best effort:
// a record left behind was never handed out, and GC removes it once it is
// superseded or expired.
func (s *DAOStore) discard(ctx context.Context, entity *Entity) {
	_ = s.invites.Delete(context.WithoutCancel(ctx), entity)
}
