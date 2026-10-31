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
	"strings"
	"sync"
	"testing"
	"time"
)

// hookKV runs before, once, ahead of the first guarded create under prefix.
// It is how a test lands a second writer exactly inside the window between
// another operation's read and its guarded write, with no sleeps.
type hookKV struct {
	*memKV
	mu        sync.Mutex
	prefix    string
	before    func()
	casPrefix string
	beforeCAS func()
	casErr    error
	casLose   bool
}

func (h *hookKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	h.mu.Lock()
	fire := h.before
	if fire != nil && strings.HasPrefix(key, h.prefix) {
		h.before = nil
	} else {
		fire = nil
	}
	h.mu.Unlock()
	if fire != nil {
		fire()
	}
	return h.memKV.PutIfAbsent(ctx, key, value)
}

// CompareAndSwap fails with casErr, or reports a lost swap while casLose is
// set, so a test can break the subject's guarded write alone.
func (h *hookKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	h.mu.Lock()
	casErr, casLose := h.casErr, h.casLose
	fire := h.beforeCAS
	if fire != nil && strings.HasPrefix(key, h.casPrefix) {
		h.beforeCAS = nil
	} else {
		fire = nil
	}
	h.mu.Unlock()
	if fire != nil {
		fire()
	}
	if casErr != nil {
		return false, casErr
	}
	if casLose {
		return false, nil
	}
	return h.memKV.CompareAndSwap(ctx, key, expected, next)
}

// breakCAS makes every later CompareAndSwap fail with err, or lose when err
// is nil.
func (h *hookKV) breakCAS(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.casErr, h.casLose = err, err == nil
}

// arm sets the hook to fire on the next guarded create under prefix.
func (h *hookKV) arm(prefix string, before func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prefix, h.before = prefix, before
}

// armCAS runs before once, just before the next CompareAndSwap of a key under
// prefix reaches the store.
func (h *hookKV) armCAS(prefix string, before func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.casPrefix, h.beforeCAS = prefix, before
}

func newHookStore(t *testing.T) (*DAOStore, *hookKV) {
	t.Helper()
	kv := &hookKV{memKV: newMemKV()}
	store, err := NewDAOStore(kv)
	if err != nil {
		t.Fatalf("NewDAOStore: %v", err)
	}
	return store, kv
}

func TestReissueSupersedesTheOldToken(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	old, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, entity, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour, WithKeyPin("pin"))
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	if fresh == old || entity.UserID != "user-1" || entity.KeyPin != "pin" {
		t.Fatalf("Reissue = %q, %+v; want a new token for user-1 pinned to pin", fresh, entity)
	}

	if _, err := store.Lookup(ctx, old); !errors.Is(err, ErrSuperseded) {
		t.Errorf("Lookup(old) = %v, want ErrSuperseded", err)
	}
	if _, err := store.Redeem(ctx, old); !errors.Is(err, ErrSuperseded) {
		t.Errorf("Redeem(old) = %v, want ErrSuperseded", err)
	}
	if _, err := store.Redeem(ctx, fresh); err != nil {
		t.Fatalf("Redeem(new): %v", err)
	}
}

// TestCreateSupersedesAPreSubjectInvitation is the upgraded-database case: an
// invitation persisted before subject records existed sits at generation 0
// with no subject. Reissue finds no subject, so the caller falls back to
// Create, and the invitation Create mints must be the only one that redeems.
func TestCreateSupersedesAPreSubjectInvitation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	legacy, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	now := time.Now().UTC()
	stored, err := store.invites.SaveIfAbsent(ctx, &Entity{
		TokenHash: TokenDigest(legacy),
		UserID:    "user-1",
		TenantID:  "tenant-a",
		ExpiresAt: now.Add(time.Hour),
		CreatedAt: now,
	})
	if err != nil || !stored {
		t.Fatalf("SaveIfAbsent(legacy) = %v, %v; want stored", stored, err)
	}

	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Reissue with no subject = %v, want ErrUserNotFound", err)
	}
	fresh, entity, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entity.Generation != firstGeneration {
		t.Errorf("Create generation = %d, want %d", entity.Generation, firstGeneration)
	}

	if _, err := store.Lookup(ctx, legacy); !errors.Is(err, ErrSuperseded) {
		t.Errorf("Lookup(legacy) = %v, want ErrSuperseded", err)
	}
	if _, err := store.Redeem(ctx, legacy); !errors.Is(err, ErrSuperseded) {
		t.Errorf("Redeem(legacy) = %v, want ErrSuperseded", err)
	}
	if _, err := store.Redeem(ctx, fresh); err != nil {
		t.Fatalf("Redeem(new): %v", err)
	}
}

func TestReissueSupersedesEveryEarlierInvitation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	var earlier []string
	for range 3 {
		token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		earlier = append(earlier, token)
	}
	other, _, err := store.Create(ctx, "user-2", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	for _, token := range earlier {
		if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSuperseded) {
			t.Errorf("Lookup(earlier) = %v, want ErrSuperseded", err)
		}
	}
	if _, err := store.Lookup(ctx, other); err != nil {
		t.Errorf("another user's invitation was disturbed: %v", err)
	}
}

func TestConcurrentReissuesLeaveExactlyOneLiveInvite(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	first, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const racers = 16
	tokens := make([]string, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			<-start
			tokens[i], _, errs[i] = store.Reissue(ctx, "user-1", "tenant-a", time.Hour)
		})
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrReissueConflict):
			t.Fatalf("racer %d: %v, want nil or ErrReissueConflict", i, err)
		}
	}
	if won == 0 {
		t.Fatal("no reissue succeeded")
	}

	live := 0
	for _, token := range append(tokens, first) {
		if token == "" {
			continue
		}
		if _, err := store.Lookup(ctx, token); err == nil {
			live++
		} else if !errors.Is(err, ErrSuperseded) {
			t.Errorf("Lookup: %v, want nil or ErrSuperseded", err)
		}
	}
	if live != 1 {
		t.Fatalf("%d live invitations after %d racing reissues, want exactly 1", live, racers)
	}
}

func TestReissueLosesToAConcurrentReissue(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var winner string
	kv.arm(entityType+"/", func() {
		var err error
		if winner, _, err = store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err != nil {
			t.Errorf("inner Reissue: %v", err)
		}
	})
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrReissueConflict) {
		t.Fatalf("Reissue = %v, want ErrReissueConflict", err)
	}
	if _, err := store.Lookup(ctx, winner); err != nil {
		t.Fatalf("the winning reissue's token is not live: %v", err)
	}
}

func TestReissueForAnActiveUserIsRefused(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Redeem(ctx, token); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	for range 2 {
		if err := store.Activate(ctx, "user-1", "tenant-a", time.Now()); err != nil {
			t.Fatalf("Activate: %v; it is idempotent", err)
		}
	}
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrUserActive) {
		t.Fatalf("Reissue = %v, want ErrUserActive", err)
	}
}

func TestActivateRefusesAnUninvitedUserAndBadArguments(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Activate(ctx, "user-1", "tenant-a", time.Now()); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("Activate(uninvited) = %v, want ErrUserNotFound", err)
	}
	if err := store.Activate(ctx, "", "tenant-a", time.Now()); !errors.Is(err, ErrMissingUserID) {
		t.Fatalf("Activate(\"\") = %v, want ErrMissingUserID", err)
	}
}

func TestActivateReportsStorageFaults(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var storeErr *StoreError
	kv.breakCAS(errors.New("disk on fire"))
	if err := store.Activate(ctx, "user-1", "tenant-a", time.Now()); !errors.As(err, &storeErr) {
		t.Fatalf("Activate(failing swap) = %v, want *StoreError", err)
	}
	kv.breakCAS(nil)
	err := store.Activate(ctx, "user-1", "tenant-a", time.Now())
	if !errors.As(err, &storeErr) || !errors.Is(err, ErrReissueConflict) {
		t.Fatalf("Activate(always-losing swap) = %v, want *StoreError wrapping ErrReissueConflict", err)
	}
}

func TestReissueRecoversARedemptionThatWasNeverActivated(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	old, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Redeem(ctx, old); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	// The caller's activation failed, so Activate was never called: the
	// account is still pending and must be re-invitable.
	fresh, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Reissue after an unactivated redemption: %v", err)
	}
	if _, err := store.Redeem(ctx, fresh); err != nil {
		t.Fatalf("Redeem(new): %v", err)
	}
	if _, err := store.Redeem(ctx, old); !errors.Is(err, ErrAlreadyRedeemed) {
		t.Fatalf("Redeem(old) = %v, want ErrAlreadyRedeemed", err)
	}
}

func TestReissueLosesToAConcurrentActivation(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}
	kv.arm(entityType+"/", func() {
		if err := store.Activate(ctx, "user-1", "tenant-a", time.Now()); err != nil {
			t.Errorf("inner Activate: %v", err)
		}
	})
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrUserActive) {
		t.Fatalf("Reissue = %v, want ErrUserActive", err)
	}
}

func TestReissueLosesToAConcurrentRedemption(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.arm(entityType+"/", func() {
		if _, err := store.Redeem(ctx, token); err != nil {
			t.Errorf("inner Redeem: %v", err)
		}
	})
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrReissueConflict) {
		t.Fatalf("Reissue = %v, want ErrReissueConflict", err)
	}
	// The redemption landed first and the user is not active, so a retry
	// succeeds.
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("retried Reissue: %v", err)
	}
}

func TestRedeemLosesToAConcurrentReissue(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var fresh string
	kv.arm(redemptionEntityType+"/", func() {
		if fresh, _, err = store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err != nil {
			t.Errorf("inner Reissue: %v", err)
		}
	})
	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("Redeem = %v, want ErrSuperseded", err)
	}
	if _, err := store.Redeem(ctx, fresh); err != nil {
		t.Fatalf("the reissued token is not redeemable: %v", err)
	}
}

func TestCreateRacingAReissueIsReportedNotDeadOnArrival(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	kv.arm(entityType+"/", func() {
		if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err != nil {
			t.Errorf("inner Reissue: %v", err)
		}
	})
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrReissueConflict) {
		t.Fatalf("Create = %v, want ErrReissueConflict", err)
	}
}

func TestCreateRacingAFirstCreateSharesTheSubject(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)

	var inner string
	kv.arm(subjectEntityType+"/", func() {
		var err error
		if inner, _, err = store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
			t.Errorf("inner Create: %v", err)
		}
	})
	outer, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, token := range []string{inner, outer} {
		if _, err := store.Lookup(ctx, token); err != nil {
			t.Errorf("Lookup: %v", err)
		}
	}
}

func TestReissueRefusesAnUninvitedUserAndBadArguments(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cases := []struct {
		name, user, tenant string
		ttl                time.Duration
		want               error
	}{
		{"never invited", "user-2", "tenant-a", time.Hour, ErrUserNotFound},
		{"other tenant", "user-1", "tenant-b", time.Hour, ErrUserNotFound},
		{"no user", "", "tenant-a", time.Hour, ErrMissingUserID},
		{"no ttl", "user-1", "tenant-a", 0, ErrNonPositiveTTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := store.Reissue(ctx, tc.user, tc.tenant, tc.ttl); !errors.Is(err, tc.want) {
				t.Fatalf("Reissue = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReissueReportsStorageFaults(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var storeErr *StoreError
	kv.failReads = true
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.As(err, &storeErr) {
		t.Errorf("Reissue on a failed read = %v, want *StoreError", err)
	}
	kv.failReads = false
	kv.failWrites = true
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.As(err, &storeErr) {
		t.Errorf("Reissue on a failed write = %v, want *StoreError", err)
	}
}

func TestLookupFailsClosedWhenTheSubjectCannotBeRead(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	kv.failReads = true
	kv.failReadsPrefix = subjectEntityType + "/"
	var storeErr *StoreError
	if _, err := store.Lookup(ctx, token); !errors.As(err, &storeErr) {
		t.Errorf("Lookup = %v, want *StoreError", err)
	}
	if _, err := store.GC(ctx, time.Now()); !errors.As(err, &storeErr) {
		t.Errorf("GC = %v, want *StoreError", err)
	}
}

func TestRedeemReportsAFailedActivation(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("storage unavailable")
	for _, tc := range []struct {
		name  string
		err   error
		cause error
	}{
		{"swap fails", boom, boom},
		{"swap never lands", nil, ErrReissueConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, kv := newHookStore(t)
			token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			kv.breakCAS(tc.err)
			var storeErr *StoreError
			if _, err := store.Redeem(ctx, token); !errors.As(err, &storeErr) || !errors.Is(err, tc.cause) {
				t.Fatalf("Redeem = %v, want *StoreError wrapping %v", err, tc.cause)
			}
		})
	}
}

func TestCreateReportsAFailedSubject(t *testing.T) {
	ctx := context.Background()
	var storeErr *StoreError

	kv := newErrKV()
	store := newErrStore(t, kv)
	kv.failWrites = true
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); !errors.As(err, &storeErr) {
		t.Errorf("Create on a failed subject write = %v, want *StoreError", err)
	}
	kv.failWrites = false
	kv.failReads = true
	kv.failReadsPrefix = subjectEntityType + "/"
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); !errors.As(err, &storeErr) {
		t.Errorf("Create on a failed subject read = %v, want *StoreError", err)
	}
}

func TestAVanishedSubjectFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	drop := func() {
		current, err := store.loadSubject(ctx, "tenant-a", "user-1")
		if err != nil {
			t.Errorf("loadSubject: %v", err)
			return
		}
		if err := store.subjects.Delete(ctx, current); err != nil {
			t.Errorf("Delete: %v", err)
		}
	}

	// Removed while Reissue writes its invitation: the swap finds no subject.
	kv.arm(entityType+"/", drop)
	var storeErr *StoreError
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.As(err, &storeErr) {
		t.Fatalf("Reissue = %v, want *StoreError", err)
	}

	// Removed while Redeem claims the token: the claim cannot activate.
	token, _, err = store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	kv.arm(redemptionEntityType+"/", drop)
	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("Redeem = %v, want ErrSuperseded", err)
	}
}

func TestCreateReportsAFailedInvitationWrite(t *testing.T) {
	ctx := context.Background()
	kv := newErrKV()
	store := newErrStore(t, kv)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}
	kv.failWrites = true
	var storeErr *StoreError
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); !errors.As(err, &storeErr) {
		t.Errorf("Create on a failed invitation write = %v, want *StoreError", err)
	}
}

func TestASubjectHashCollisionIsAMiss(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Overwrite the subject's record with another pair's, under the same ID:
	// what a 64-bit hash collision would leave there.
	current, err := store.loadSubject(ctx, "tenant-a", "user-1")
	if err != nil {
		t.Fatalf("loadSubject: %v", err)
	}
	other := *current
	other.Key = subjectKey("tenant-z", "user-9")
	if err := store.subjects.Save(ctx, &other); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSuperseded) {
		t.Errorf("Lookup = %v, want ErrSuperseded", err)
	}
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Reissue = %v, want ErrUserNotFound", err)
	}
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); !errors.Is(err, ErrTokenCollision) {
		t.Errorf("Create = %v, want ErrTokenCollision", err)
	}
}

func TestGCRemovesSupersededInvitations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}
	live, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	removed, err := store.GC(ctx, time.Now())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 {
		t.Errorf("GC removed %d, want 1 (the superseded invitation)", removed)
	}
	if _, err := store.Lookup(ctx, live); err != nil {
		t.Errorf("GC removed the live invitation: %v", err)
	}
}

// GC that runs between Reissue's mint and its subject swap must keep the
// staged invitation: the swap is about to make it the user's one live token.
func TestGCKeepsAReissueStagedBeforeItsSwap(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fired := false
	kv.armCAS(subjectEntityType+"/", func() {
		fired = true
		removed, err := store.GC(ctx, time.Now())
		if err != nil {
			t.Errorf("GC between mint and swap: %v", err)
		}
		if removed != 0 {
			t.Errorf("GC between mint and swap removed %d, want 0 (one live, one staged)", removed)
		}
	})
	token, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	if !fired {
		t.Fatal("the subject swap hook never fired")
	}
	if _, err := store.Lookup(ctx, token); err != nil {
		t.Fatalf("the reissued token is not live after an interleaved GC: %v", err)
	}
	removed, err := store.GC(ctx, time.Now())
	if err != nil || removed != 1 {
		t.Fatalf("GC after the swap = %d, %v; want 1 (the superseded invitation)", removed, err)
	}
	if _, err := store.Lookup(ctx, token); err != nil {
		t.Fatalf("GC removed the live invitation: %v", err)
	}
}

// A staged invitation whose swap failed with an unknown outcome stays until
// it expires, and is collected then.
func TestGCCollectsAnUnresolvedStagedReissueOnceExpired(t *testing.T) {
	ctx := context.Background()
	store, kv := newHookStore(t)
	first, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	kv.breakCAS(errors.New("raft: proposal outcome unknown"))
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err == nil {
		t.Fatal("Reissue with a failed swap succeeded")
	}

	removed, err := store.GC(ctx, time.Now())
	if err != nil || removed != 0 {
		t.Fatalf("GC before expiry = %d, %v; want 0 (one live, one staged)", removed, err)
	}
	if _, err := store.Lookup(ctx, first); err != nil {
		t.Fatalf("the first invitation is no longer live: %v", err)
	}
	removed, err = store.GC(ctx, time.Now().Add(2*time.Hour))
	if err != nil || removed != 2 {
		t.Fatalf("GC after expiry = %d, %v; want 2", removed, err)
	}
}

func TestCollectibleClassifiesByGeneration(t *testing.T) {
	ctx := context.Background()
	store, _ := newHookStore(t)
	_, live, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := store.Reissue(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	// The subject is now at generation 2.
	cases := []struct {
		name   string
		entity Entity
		want   bool
	}{
		{"pre-subject", Entity{UserID: "user-1", TenantID: "tenant-a", Generation: 0}, true},
		{"superseded", Entity{UserID: "user-1", TenantID: "tenant-a", Generation: 1}, true},
		{"live", Entity{UserID: "user-1", TenantID: "tenant-a", Generation: 2}, false},
		{"staged", Entity{UserID: "user-1", TenantID: "tenant-a", Generation: 3}, false},
		{"no subject", Entity{UserID: "user-2", TenantID: "tenant-a", Generation: 5}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.collectible(ctx, &tc.entity)
			if err != nil || got != tc.want {
				t.Fatalf("collectible = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if live.Generation != firstGeneration {
		t.Fatalf("the created invitation's generation = %d, want %d", live.Generation, firstGeneration)
	}
}

func TestTenantStoreReissue(t *testing.T) {
	ctx := context.Background()
	resolver := newStubResolver()
	store, err := NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}
	old, _, err := store.Create(ctx, "user-1", "acme", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _, err := store.Reissue(ctx, "user-1", "acme", time.Hour)
	if err != nil {
		t.Fatalf("Reissue: %v", err)
	}
	if _, err := store.Redeem(ctx, "acme", old); !errors.Is(err, ErrSuperseded) {
		t.Errorf("Redeem(old) = %v, want ErrSuperseded", err)
	}
	if _, err := store.Redeem(ctx, "acme", fresh); err != nil {
		t.Errorf("Redeem(new): %v", err)
	}

	boom := errors.New("tenant sealed")
	resolver.fail["acme"] = boom
	if _, _, err := store.Reissue(ctx, "user-1", "acme", time.Hour); !errors.Is(err, boom) {
		t.Errorf("Reissue on an unresolvable tenant = %v, want %v", err, boom)
	}
}
