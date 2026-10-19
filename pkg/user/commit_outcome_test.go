package user

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The tests in this file drive the read-back that settles an AMBIGUOUS
// conditional commit — a CompareAndSwap that replied with an error, so the
// caller cannot know whether it landed — through the two conditional writers
// that have one: DeleteUser's deletion mark (markDeleting) and the
// LastAuth-only merge (mergeLastAuth). Each read-back has four answers: the
// read itself failed, the write landed, nothing moved, or someone else
// committed. They also cover the claim release a failed create performs
// (releaseClaimAfterFailedCreate) when the identity index refuses it.

// renameUnder commits a display-name change to the user record at key,
// directly on kv: the interfering writer whose commit a conditional write
// under test then loses to.
func renameUnder(t *testing.T, kv *memKVStore, key, name string) {
	t.Helper()
	ctx := context.Background()
	raw, err := kv.Get(ctx, key)
	if err != nil {
		t.Errorf("interfering write: get: %v", err)
		return
	}
	var other User
	if uerr := json.Unmarshal(raw, &other); uerr != nil {
		t.Errorf("interfering write: unmarshal: %v", uerr)
		return
	}
	other.Name = name
	data, merr := json.Marshal(&other)
	if merr != nil {
		t.Errorf("interfering write: marshal: %v", merr)
		return
	}
	if perr := kv.Put(ctx, key, data); perr != nil {
		t.Errorf("interfering write: put: %v", perr)
	}
}

// ambiguousSwapCase arms an atSwapKV on the seeded user's primary record.
type ambiguousSwapCase struct {
	name string
	// getErr fails the read-back as well as the swap.
	getErr error
	// interfere lands another writer's commit just before the failed swap.
	interfere bool
	check     func(t *testing.T, err error, svc UserServicer, u *User)
}

// newAmbiguousSwapStore seeds one user over a fresh memKVStore and returns a
// store over an atSwapKV armed on that user's primary record with swapErr.
func newAmbiguousSwapStore(t *testing.T, tc ambiguousSwapCase, swapErr error) (UserServicer, *User) {
	t.Helper()
	ctx := context.Background()
	kv := newMemKVStore()
	seed, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, err := seed.CreateUser(ctx, &CreateUserRequest{Username: "amb", Email: "amb@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	akv := &atSwapKV{memKVStore: kv, primary: primaryKey(u.ID), swapErr: swapErr, getErr: tc.getErr}
	if tc.interfere {
		akv.onSwap = func() { renameUnder(t, kv, primaryKey(u.ID), "Concurrent") }
	}
	svc, err := NewStore(akv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc, u
}

func TestDeleteUser_AmbiguousDeletionMarkIsReadBack(t *testing.T) {
	swapErr := errors.New("reply lost")
	readErr := errors.New("read-back failed")

	// stillLive asserts the delete removed nothing: the mark did not land, so
	// the cascade must never have started.
	stillLive := func(t *testing.T, svc UserServicer, u *User) {
		t.Helper()
		got, err := svc.GetUser(context.Background(), u.ID)
		if err != nil {
			t.Fatalf("GetUser after a refused delete: %v", err)
		}
		if got.Lifecycle == LifecycleDeleting {
			t.Fatal("a deletion mark that never landed is on the record")
		}
	}

	cases := []ambiguousSwapCase{
		{
			name:   "read-back fails: outcome unknown",
			getErr: readErr,
			check: func(t *testing.T, err error, _ UserServicer, u *User) {
				var unknown *CommitOutcomeUnknownError
				if !errors.As(err, &unknown) {
					t.Fatalf("want *CommitOutcomeUnknownError, got %T: %v", err, err)
				}
				if unknown.ID != u.ID || !errors.Is(err, swapErr) {
					t.Fatalf("outcome-unknown error names id %d / cause %v, want %d / %v", unknown.ID, unknown.Err, u.ID, swapErr)
				}
			},
		},
		{
			name: "nothing moved: the swap error is reported",
			check: func(t *testing.T, err error, svc UserServicer, u *User) {
				var se *StoreError
				if !errors.As(err, &se) || !errors.Is(err, swapErr) {
					t.Fatalf("want *StoreError wrapping the swap error, got %T: %v", err, err)
				}
				stillLive(t, svc, u)
			},
		},
		{
			name:      "another writer committed: concurrent update",
			interfere: true,
			check: func(t *testing.T, err error, svc UserServicer, u *User) {
				var cu *ConcurrentUpdateError
				if !errors.As(err, &cu) || cu.ID != u.ID {
					t.Fatalf("want *ConcurrentUpdateError for %d, got %T: %v", u.ID, err, err)
				}
				stillLive(t, svc, u)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, u := newAmbiguousSwapStore(t, tc, swapErr)
			err := svc.DeleteUser(context.Background(), u.ID)
			if err == nil {
				t.Fatal("DeleteUser: want an error from an ambiguous mark that did not land")
			}
			tc.check(t, err, svc, u)
		})
	}
}

// A deletion mark that landed before its reply was lost is the delete's own
// claim: the read-back must recognise it and finish the delete.
func TestDeleteUser_DeletionMarkThatLandedFinishesTheDelete(t *testing.T) {
	ctx := context.Background()
	kv := &committedThenFailedKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "gone", Email: "gone@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	kv.armKeyPrefix = keyPrefixPrimary

	if dErr := svc.DeleteUser(ctx, u.ID); dErr != nil {
		t.Fatalf("DeleteUser: a mark that landed must finish the delete, got: %v", dErr)
	}
	if !kv.fired {
		t.Fatal("the lost-reply swap was never exercised")
	}
	var nf *UserNotFoundError
	if _, gErr := svc.GetUser(ctx, u.ID); !errors.As(gErr, &nf) {
		t.Fatalf("GetUser after delete: want *UserNotFoundError, got %T: %v", gErr, gErr)
	}
}

func TestUpdateUser_LastAuthOnlyAmbiguousCommitIsReadBack(t *testing.T) {
	swapErr := errors.New("reply lost")
	readErr := errors.New("read-back failed")
	at := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)

	cases := []ambiguousSwapCase{
		{
			name:   "read-back fails: outcome unknown",
			getErr: readErr,
			check: func(t *testing.T, err error, _ UserServicer, u *User) {
				var unknown *CommitOutcomeUnknownError
				if !errors.As(err, &unknown) || unknown.ID != u.ID || !errors.Is(err, swapErr) {
					t.Fatalf("want *CommitOutcomeUnknownError for %d wrapping the swap error, got %T: %v", u.ID, err, err)
				}
			},
		},
		{
			name: "nothing moved: the swap error is reported",
			check: func(t *testing.T, err error, svc UserServicer, u *User) {
				var se *StoreError
				if !errors.As(err, &se) || !errors.Is(err, swapErr) {
					t.Fatalf("want *StoreError wrapping the swap error, got %T: %v", err, err)
				}
				got, gErr := svc.GetUser(context.Background(), u.ID)
				if gErr != nil {
					t.Fatalf("GetUser: %v", gErr)
				}
				if got.LastAuth != nil {
					t.Fatalf("LastAuth moved on a commit that did not land: %v", got.LastAuth)
				}
			},
		},
		{
			name:      "another writer committed: the merge retries on top of it",
			interfere: true,
			check: func(t *testing.T, err error, svc UserServicer, u *User) {
				if err != nil {
					t.Fatalf("a LastAuth-only merge must retry past a lost race, got: %v", err)
				}
				got, gErr := svc.GetUser(context.Background(), u.ID)
				if gErr != nil {
					t.Fatalf("GetUser: %v", gErr)
				}
				if got.LastAuth == nil || !got.LastAuth.Equal(at) {
					t.Fatalf("LastAuth: got %v, want %v", got.LastAuth, at)
				}
				if got.Name != "Concurrent" {
					t.Fatalf("the concurrent writer's change was reverted: Name=%q", got.Name)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, u := newAmbiguousSwapStore(t, tc, swapErr)
			_, err := svc.UpdateUser(context.Background(), &UpdateUserRequest{ID: u.ID, LastAuth: &at})
			tc.check(t, err, svc, u)
		})
	}
}

// A LastAuth merge whose reply was lost after it landed reports success: the
// read-back finds exactly the bytes it wrote.
func TestUpdateUser_LastAuthOnlyCommitThatLandedSucceeds(t *testing.T) {
	ctx := context.Background()
	kv := &committedThenFailedKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "landed", Email: "landed@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	kv.armKeyPrefix = keyPrefixPrimary

	at := time.Date(2026, 9, 3, 9, 30, 0, 0, time.UTC)
	got, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &at})
	if uErr != nil {
		t.Fatalf("UpdateUser: a merge that landed must succeed, got: %v", uErr)
	}
	if !kv.fired {
		t.Fatal("the lost-reply swap was never exercised")
	}
	if got.LastAuth == nil || !got.LastAuth.Equal(at) {
		t.Fatalf("LastAuth: got %v, want %v", got.LastAuth, at)
	}
}

// Losing every attempt exhausts the merge's bound, and the last conflict is
// what the caller is told rather than a success that raised nothing.
func TestUpdateUser_LastAuthOnlyGivesUpAfterTheBound(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "loser", Email: "loser@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	attempts := 0
	kv.beforeCAS = func(key string) {
		if key != primaryKey(u.ID) {
			return
		}
		attempts++
		// A distinct name per attempt: the same bytes again would match the
		// merge's base and let it win.
		renameUnder(t, kv, key, fmt.Sprintf("winner-%d", attempts))
	}

	at := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, LastAuth: &at})
	var cu *ConcurrentUpdateError
	if !errors.As(uErr, &cu) || cu.ID != u.ID {
		t.Fatalf("want *ConcurrentUpdateError for %d, got %T: %v", u.ID, uErr, uErr)
	}
	if attempts != lastAuthMergeAttempts {
		t.Fatalf("merge attempts: got %d, want %d", attempts, lastAuthMergeAttempts)
	}
}

func TestUpdateUser_LastAuthOnlyRefusals(t *testing.T) {
	at := time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		setup func(t *testing.T, kv *memKVStore, u *User) uint64
		check func(t *testing.T, err error, id uint64)
	}{
		{
			name:  "unknown id",
			setup: func(_ *testing.T, _ *memKVStore, u *User) uint64 { return u.ID + 1 },
			check: func(t *testing.T, err error, id uint64) {
				var nf *UserNotFoundError
				if !errors.As(err, &nf) || nf.ID != id {
					t.Fatalf("want *UserNotFoundError for %d, got %T: %v", id, err, err)
				}
			},
		},
		{
			name: "deletion has begun",
			setup: func(t *testing.T, kv *memKVStore, u *User) uint64 {
				ctx := context.Background()
				raw, err := kv.Get(ctx, primaryKey(u.ID))
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				var rec User
				if uerr := json.Unmarshal(raw, &rec); uerr != nil {
					t.Fatalf("unmarshal: %v", uerr)
				}
				rec.Lifecycle = LifecycleDeleting
				data, merr := json.Marshal(&rec)
				if merr != nil {
					t.Fatalf("marshal: %v", merr)
				}
				if perr := kv.Put(ctx, primaryKey(u.ID), data); perr != nil {
					t.Fatalf("Put: %v", perr)
				}
				return u.ID
			},
			check: func(t *testing.T, err error, id uint64) {
				var lt *LifecycleTransitionError
				if !errors.As(err, &lt) || lt.ID != id || lt.From != LifecycleDeleting {
					t.Fatalf("want *LifecycleTransitionError from deleting for %d, got %T: %v", id, err, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			kv := newMemKVStore()
			svc, err := NewStore(kv, newTestIDGen(t))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			u, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "refused", Email: "refused@example.com"})
			if err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			id := tc.setup(t, kv, u)
			_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: id, LastAuth: &at})
			tc.check(t, uErr, id)
		})
	}
}

// forgetFaultIndex is a real tenant.IdentityIndex whose Forget fails with
// forgetErr, so a failed create's claim release can be refused.
type forgetFaultIndex struct {
	tenant.IdentityIndex
	forgetErr error
	forgets   int
}

var _ tenant.IdentityIndex = (*forgetFaultIndex)(nil)

func (f *forgetFaultIndex) Forget(_ context.Context, _ tenant.IdentityKind, _, _ string) error {
	f.forgets++
	return f.forgetErr
}

func TestCreateUser_RefusedClaimReleaseIsAudited(t *testing.T) {
	putErr := errors.New("primary put failed")
	cases := []struct {
		name      string
		forgetErr error
		// wantLeak is whether the release refusal is a leaked claim, audited
		// as such. A stale claim is not one: it was already replaced.
		wantLeak bool
	}{
		{name: "index refuses the release", forgetErr: errors.New("index unavailable"), wantLeak: true},
		{name: "claim already replaced", forgetErr: &tenant.IdentityClaimStaleError{Kind: tenant.IdentityUser, ID: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kv := &faultKV{memKVStore: newMemKVStore(), failPutPrefix: keyPrefixPrimary, err: putErr}
			inner, err := tenant.NewIdentityIndex(kv.memKVStore)
			if err != nil {
				t.Fatalf("tenant.NewIdentityIndex: %v", err)
			}
			idx := &forgetFaultIndex{IdentityIndex: inner, forgetErr: tc.forgetErr}
			al := &fakeAuditLogger{}
			svc, err := NewStore(kv, newTestIDGen(t), WithIdentityIndex(idx), WithAuditLogger(al))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			_, cErr := svc.CreateUser(context.Background(), &CreateUserRequest{Username: "leak", Email: "leak@example.com"})
			if !errors.Is(cErr, putErr) {
				t.Fatalf("CreateUser: want the primary put failure, got %T: %v", cErr, cErr)
			}
			if idx.forgets != 1 {
				t.Fatalf("Forget calls: got %d, want 1", idx.forgets)
			}
			if _, leaked := al.findByAction("create_claim_leaked"); leaked != tc.wantLeak {
				t.Fatalf("create_claim_leaked audited: got %v, want %v", leaked, tc.wantLeak)
			}
		})
	}
}
