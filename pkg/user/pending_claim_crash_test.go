package user

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errStopped = errors.New("process stopped")

// stopKV stops the store, as a process stop would, right after the step a
// claim takes on its reap marker once it has lost the compare-and-swap of the
// primary record: that compare-and-delete, if the claim makes one, lands, and
// every later read and write fails until the test clears the stop.
type stopKV struct {
	*overlapKV
	id    uint64
	armed bool
	down  bool
}

// step reports whether the store is stopped, stopping it first once the loss
// has been seen.
func (s *stopKV) step() bool {
	if s.armed {
		s.down = true
	}
	return s.down
}

func (s *stopKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.step() {
		return false, errStopped
	}
	ok, err := s.overlapKV.CompareAndSwap(ctx, key, expected, next)
	if err == nil && !ok && key == primaryKey(s.id) {
		s.armed = true
	}
	return ok, err
}

func (s *stopKV) Get(ctx context.Context, key string) ([]byte, error) {
	if s.step() {
		return nil, errStopped
	}
	return s.overlapKV.Get(ctx, key)
}

func (s *stopKV) Put(ctx context.Context, key string, value []byte) error {
	if s.step() {
		return errStopped
	}
	return s.overlapKV.Put(ctx, key, value)
}

func (s *stopKV) Delete(ctx context.Context, key string) error {
	if s.step() {
		return errStopped
	}
	return s.overlapKV.Delete(ctx, key)
}

func (s *stopKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if s.down {
		return false, errStopped
	}
	if s.armed {
		s.down = true
	}
	return s.overlapKV.CompareAndDelete(ctx, key, expected)
}

// A claim that loses the record to an overlapping claim must leave the
// winner's recovery record in place by itself, with no compensating write:
// the process may stop right after the loser's marker step. The winner then
// stops after deleting the record and before releasing the identity; a fresh
// reaper must still find the account from its marker, release the identity
// and emit pending_reaped.
func TestClaimExpiredPending_LoserStoppingKeepsTheWinnersRecovery(t *testing.T) {
	for name, beforeWrite := range map[string]bool{
		"loser marker lands after the winner":  true,
		"loser marker lands before the winner": false,
	} {
		t.Run(name, func(t *testing.T) {
			kv := &stopKV{overlapKV: &overlapKV{memKVStore: newMemKVStore(), beforeWrite: beforeWrite}}
			al := &fakeAuditLogger{}
			svc, err := NewStore(kv, newTestIDGen(t), WithAuditLogger(al))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			s := svc.(*kvUserStore)
			ctx := context.Background()
			now := time.Now().UTC().Add(time.Hour)
			u := createPending(t, svc, "contested", "acme", time.Minute)
			kv.id = u.ID

			var winErr error
			kv.other = func() { _, winErr = s.ClaimExpiredPending(ctx, u.ID, now) }
			if _, err := s.ClaimExpiredPending(ctx, u.ID, now); err == nil {
				t.Fatal("the overlapped claim won the record too")
			}
			if winErr != nil {
				t.Fatalf("overlapping claim: %v", winErr)
			}
			if !kv.armed {
				t.Fatal("the losing claim never saw its compare-and-swap fail")
			}

			// Restart. The winner stopped after deleting the record and
			// before releasing the identity.
			kv.armed, kv.down = false, false
			if err := kv.memKVStore.Delete(ctx, primaryKey(u.ID)); err != nil {
				t.Fatalf("delete primary: %v", err)
			}

			ids, err := s.ListExpiryClaims(ctx, "acme")
			if err != nil || len(ids) != 1 || ids[0] != u.ID {
				t.Fatalf("ListExpiryClaims after the stop = %v, %v; want [%d]", ids, err, u.ID)
			}
			if _, err := s.DeleteExpiredPending(ctx, u.ID, now); err != nil {
				t.Fatalf("DeleteExpiredPending: %v", err)
			}
			if got := countAudits(al, "pending_reaped"); got != 1 {
				t.Fatalf("pending_reaped audits = %d, want 1", got)
			}
			if ids, err := s.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
				t.Fatalf("reap marker left after the recovery: %v, %v", ids, err)
			}
		})
	}
}
