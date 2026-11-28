package user

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

var _ PendingActivator = (*kvUserStore)(nil)

// ActivatePending implements PendingActivator: it moves a PENDING account to
// active in one commit conditional on the record it read (go-qrdb#1765). Any
// other lifecycle is refused with *AccountNotPendingError and nothing is
// written; a lost commit re-reads, so a transition committed between the read
// and the write is refused on the re-read rather than overwritten.
func (s *kvUserStore) ActivatePending(ctx context.Context, userID uint64) (*User, error) {
	if _, ok := s.store.(kvstore.AtomicKVStore); !ok {
		return nil, &StoreError{Op: "activate_pending", Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}
	if userID == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	var lastConflict error
	for attempt := 0; attempt < certBindingMutationAttempts; attempt++ {
		base, err := s.store.Get(ctx, primaryKey(userID))
		if err != nil {
			return nil, &StoreError{Op: "activate_pending", Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return nil, &UserNotFoundError{ID: userID}
		}
		existing := &User{}
		if uerr := json.Unmarshal(base, existing); uerr != nil {
			return nil, &StoreError{Op: "activate_pending", Detail: "unmarshal", Err: uerr}
		}
		if existing.Lifecycle != LifecyclePending {
			return nil, &AccountNotPendingError{ID: existing.ID, Lifecycle: string(existing.Lifecycle)}
		}

		updated, uerr := s.updateUserAt(ctx, &UpdateUserRequest{
			ID:        userID,
			Lifecycle: LifecycleActive,
		}, base)
		if uerr == nil {
			s.emitAudit(ctx, "activate_pending", userID, map[string]any{
				"username": updated.Username,
				"tenantID": updated.TenantID,
			})
			return updated, nil
		}
		var lost *ConcurrentUpdateError
		if !errors.As(uerr, &lost) {
			return nil, uerr
		}
		lastConflict = uerr
	}

	// Exhausted the bound: report the conflict, never a false activation.
	return nil, lastConflict
}

// ActivatePendingOn runs svc's ActivatePending: the entry every transport's
// authenticated activation handler calls (go-qrdb#1765), so each answers the
// same way for a user service that cannot activate. A svc that is not a
// PendingActivator is refused with ErrActivationUnsupported and nothing is
// written.
func ActivatePendingOn(ctx context.Context, svc UserServicer, userID uint64) (*User, error) {
	activator, ok := svc.(PendingActivator)
	if !ok {
		return nil, &StoreError{Op: "activate_pending", Detail: "user service", Err: ErrActivationUnsupported}
	}
	return activator.ActivatePending(ctx, userID)
}
