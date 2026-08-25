// Copyright (c) 2026 Jeremy Hahn
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

package custodian_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// hookKV interposes on Put and PutIfAbsent so a test can interleave two
// registrations at exact points. A hook runs before the write and outside any
// store lock, so it may block; returning an error fails the write instead of
// performing it.
type hookKV struct {
	*memKV
	onPut         atomic.Pointer[func(key string, value []byte) error]
	onPutIfAbsent atomic.Pointer[func(key string, value []byte) error]
	onGet         atomic.Pointer[func(key string) error]
	onDelete      atomic.Pointer[func(key string) error]
	onCAS         atomic.Pointer[func(key string, expected, next []byte) error]
}

func newHookKV() *hookKV { return &hookKV{memKV: newMemKV()} }

func (h *hookKV) hook(fn func(key string, value []byte) error) {
	h.onPut.Store(&fn)
}

func (h *hookKV) hookClaim(fn func(key string, value []byte) error) {
	h.onPutIfAbsent.Store(&fn)
}

func (h *hookKV) Put(ctx context.Context, key string, value []byte) error {
	if fn := h.onPut.Load(); fn != nil {
		if err := (*fn)(key, value); err != nil {
			return err
		}
	}
	return h.memKV.Put(ctx, key, value)
}

func (h *hookKV) hookGet(fn func(key string) error) {
	h.onGet.Store(&fn)
}

// hookDelete observes every Delete. It exists so a test can assert an
// operation NEVER deletes a key -- an absence that is otherwise invisible,
// because a delete followed by a create leaves the store looking identical to
// a compare-and-swap once both have run (go-qrdb#21).
func (h *hookKV) hookDelete(fn func(key string) error) {
	h.onDelete.Store(&fn)
}

func (h *hookKV) Delete(ctx context.Context, key string) error {
	if fn := h.onDelete.Load(); fn != nil {
		if err := (*fn)(key); err != nil {
			return err
		}
	}
	return h.memKV.Delete(ctx, key)
}

// PutAllIfUnchanged fires the Put hook for each write BEFORE applying any of
// them, then delegates to the atomic implementation.
//
// The hook has to run outside the store's lock. Its whole purpose is to let a
// test park one caller mid-enrolment while another proceeds, and a hook invoked
// while holding the lock would block the second caller on the mutex instead of
// on the claim — turning a test about claim ownership into a test about lock
// contention, and deadlocking the ones that park indefinitely.
//
// Firing per write keeps the seam the tests were written against: enrolment
// still writes the same three keys, it just commits them together now
// (go-qrdb#21).
func (h *hookKV) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	if fn := h.onPut.Load(); fn != nil {
		for _, w := range writes {
			if err := (*fn)(w.Key, w.Value); err != nil {
				return false, err
			}
		}
	}
	return h.memKV.PutAllIfUnchanged(ctx, guardKey, guardValue, writes)
}

func (h *hookKV) Get(ctx context.Context, key string) ([]byte, error) {
	if fn := h.onGet.Load(); fn != nil {
		if err := (*fn)(key); err != nil {
			return nil, err
		}
	}
	return h.memKV.Get(ctx, key)
}

func (h *hookKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if fn := h.onPutIfAbsent.Load(); fn != nil {
		if err := (*fn)(key, value); err != nil {
			return false, err
		}
	}
	return h.memKV.PutIfAbsent(ctx, key, value)
}

// The guarded writes DELEGATE to the embedded memKV rather than returning a
// fixed (false, nil).
//
// They were stubs, added only to satisfy kvstore.AtomicKVStore. A double that
// refuses what production performs is worse than no double: the compile-time
// assertion passes while the behaviour is absent, so the first caller to
// depend on it -- the expired-claim take-over of go-qrdb#21 -- saw every swap
// fail, in a way that read as a bug in the take-over rather than in the fake.
// This type overrides the operations it exists to interfere with; these are
// not among them.
func (h *hookKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return h.memKV.CompareAndDelete(ctx, key, expected)
}

// hookCAS interposes on CompareAndSwap. It exists because the registration's
// COMMIT became one when claims gained an expiry (go-qrdb#21): a test that
// interposed only on Put would no longer reach the commit at all.
func (h *hookKV) hookCAS(fn func(key string, expected, next []byte) error) {
	h.onCAS.Store(&fn)
}

func (h *hookKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if fn := h.onCAS.Load(); fn != nil {
		if err := (*fn)(key, expected, next); err != nil {
			return false, err
		}
	}
	return h.memKV.CompareAndSwap(ctx, key, expected, next)
}

func newHookService(t *testing.T, store *hookKV) custodian.Servicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

type registerResult struct {
	resp *custodian.RegisterResponse
	err  error
}

// TestRegisterCustodian_ConcurrentSameUsername_ExactlyOneSucceeds drives the
// interleaving the duplicate check cannot settle on its own: both callers pass
// findExistingID before either has written anything. They derive the SAME
// identifier, so without a claim they would both go on to write the same three
// keys, both report success, and the later write would decide which enrolment
// survives — leaving the other share holder with an envelope for material the
// store no longer describes.
//
// The schedule is deterministic, driven by channels rather than timing:
//
//	B: duplicate check passes, then parks at its username claim
//	A: duplicate check passes (B holds nothing yet), claims, and completes
//	B: resumes — its claim must fail, and with it the registration
func TestRegisterCustodian_ConcurrentSameUsername_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var (
		bParked   = make(chan struct{})
		bMayClaim = make(chan struct{})
		bIsFirst  atomic.Bool
	)

	// Park the first attempt to take the username index, whichever store
	// operation takes it. Hooking both is what makes this test a check on the
	// guarantee rather than on the implementation: an emulated claim — Get
	// then Put — parks here too, and then fails.
	park := func(key string, _ []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/username/") && !bIsFirst.Swap(true) {
			close(bParked)
			<-bMayClaim
		}
		return nil
	}
	store.hook(park)
	store.hookClaim(park)

	bResult := make(chan registerResult, 1)
	go func() {
		req, _ := testRegisterRequest(t)
		req.EnrollmentID = "enroll-B"
		resp, err := svc.RegisterCustodian(ctx, req)
		bResult <- registerResult{resp, err}
	}()

	<-bParked

	req, _ := testRegisterRequest(t)
	req.EnrollmentID = "enroll-A"
	a, aErr := svc.RegisterCustodian(ctx, req)
	if aErr != nil {
		t.Fatalf("A registered while B held nothing: %v", aErr)
	}

	close(bMayClaim)
	b := <-bResult

	if b.err == nil {
		t.Fatal("both registrations succeeded; the username claim did not settle the race")
	}
	if !errors.Is(b.err, custodian.ErrCustodianAlreadyExists) {
		t.Errorf("B's error = %v, want ErrCustodianAlreadyExists", b.err)
	}
	if b.resp != nil {
		t.Error("B was refused but still received a sealed envelope")
	}

	// The winner's enrolment is the one in the store, whole and addressable.
	got := enrolmentOfKV(t, store.memKV, a.Custodian.ID)
	if got.EnrollmentID != "enroll-A" {
		t.Errorf("EnrollmentID = %q, want enroll-A", got.EnrollmentID)
	}
	assertEnrolmentIsWhole(ctx, t, store, got.TenantID, got.Username, got.ID)
	if _, err := svc.GetCustodian(ctx, a.Custodian.ID); err != nil {
		t.Errorf("GetCustodian after the race: %v", err)
	}
}

// TestRegisterCustodian_ManyConcurrentSameUsername_ExactlyOneSucceeds is the
// same guarantee without a scripted schedule: whatever order the store sees,
// one enrolment exists and one caller holds its envelope.
func TestRegisterCustodian_ManyConcurrentSameUsername_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	store := newMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	const racers = 16
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		results = make([]registerResult, racers)
	)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := testRegisterRequest(t)
			req.EnrollmentID = "enroll-" + string(rune('A'+i))
			<-start
			resp, err := svc.RegisterCustodian(ctx, req)
			results[i] = registerResult{resp, err}
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	var winner *custodian.RegisterResponse
	for _, r := range results {
		if r.err != nil {
			continue
		}
		winners++
		winner = r.resp
	}
	if winners != 1 {
		t.Fatalf("%d of %d registrations succeeded, want exactly 1", winners, racers)
	}

	records, err := svc.ListCustodians(ctx, winner.Custodian.TenantID)
	if err != nil {
		t.Fatalf("ListCustodians: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("%d custodian records persisted, want 1", len(records))
	}
	if records[0].EnrollmentID != winner.Custodian.EnrollmentID {
		t.Errorf("persisted EnrollmentID = %q, want the winner's %q — a loser overwrote it",
			records[0].EnrollmentID, winner.Custodian.EnrollmentID)
	}
	assertEnrolmentIsWhole(ctx, t, store, records[0].TenantID, records[0].Username, records[0].ID)
}

// TestRegisterCustodian_UncommittedClaim_IsRefusedAsIncomplete covers the other
// interleaving: the second caller reaches the duplicate check while the first
// holds the username but has not written its record. It must refuse — that is
// the duplicate the claim exists to prevent — and it must say what it found
// rather than report a custodian that does not exist, because the remedy is a
// retry rather than an operator deleting an enrolment.
func TestRegisterCustodian_UncommittedClaim_IsRefusedAsIncomplete(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var (
		aParked    = make(chan struct{})
		aMayWrite  = make(chan struct{})
		aAtPrimary atomic.Bool
	)

	store.hook(func(key string, _ []byte) error {
		if strings.HasPrefix(key, "__sys:custodian/") && !aAtPrimary.Swap(true) {
			close(aParked)
			<-aMayWrite
		}
		return nil
	})

	aResult := make(chan registerResult, 1)
	go func() {
		req, _ := testRegisterRequest(t)
		req.EnrollmentID = "enroll-A"
		resp, err := svc.RegisterCustodian(ctx, req)
		aResult <- registerResult{resp, err}
	}()

	<-aParked

	req, _ := testRegisterRequest(t)
	req.EnrollmentID = "enroll-B"
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("B enrolled against a username another registration held")
	} else {
		var incomplete *custodian.IncompleteEnrolmentError
		if !errors.As(err, &incomplete) {
			t.Fatalf("B's error = %v, want IncompleteEnrolmentError", err)
		}
		if !strings.Contains(incomplete.Error(), incomplete.Key) {
			t.Errorf("error %q does not name the key an operator must clear", incomplete.Error())
		}
	}

	close(aMayWrite)
	a := <-aResult
	if a.err != nil {
		t.Fatalf("A's registration failed: %v", a.err)
	}

	// Once A commits, the same username reads as an existing custodian rather
	// than an incomplete one.
	if _, err := svc.RegisterCustodian(ctx, req); !errors.Is(err, custodian.ErrCustodianAlreadyExists) {
		t.Errorf("after A committed, B's error = %v, want ErrCustodianAlreadyExists", err)
	}
}

// assertEnrolmentIsWhole checks that a committed enrolment occupies all three
// of its keys and that the username index names the custodian rather than a
// leftover claim token.
func assertEnrolmentIsWhole(ctx context.Context, t *testing.T, store interface {
	Get(context.Context, string) ([]byte, error)
}, tenantID, username, id string) {
	t.Helper()

	for _, key := range []string{
		"__sys:custodian/" + id,
		"__sys:custodian-idx/tenant/" + tenantID + "/" + id,
	} {
		v, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
		if len(v) == 0 {
			t.Errorf("%s is missing from a committed enrolment", key)
		}
	}

	usernameKey := "__sys:custodian-idx/username/" + tenantID + "/" + username
	v, err := store.Get(ctx, usernameKey)
	if err != nil {
		t.Fatalf("Get(%s): %v", usernameKey, err)
	}
	if string(v) != id {
		t.Errorf("%s = %q, want the custodian ID %q — the claim was never committed",
			usernameKey, v, id)
	}
}

// custodianIDOf returns the single custodian identifier a tenant's primary
// records hold, so a test can address a record written by another goroutine
// without re-deriving the identifier.
func custodianIDOf(t *testing.T, store *memKV, tenantID string) string {
	t.Helper()
	keys, err := store.List(context.Background(), "__sys:custodian/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("%d primary records for tenant %s, want 1", len(keys), tenantID)
	}
	return strings.TrimPrefix(keys[0], "__sys:custodian/")
}

// TestRegisterCustodian_SealFailure_ReleasesTheClaim covers the window between
// the claim and the record. The claim is taken before the share is sealed, so
// a sealing failure must give the username back — otherwise a bad public key
// would lock a custodian out of enrolling for good.
func TestRegisterCustodian_SealFailure_ReleasesTheClaim(t *testing.T) {
	ctx := context.Background()
	store := newMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	bad, _ := testRegisterRequest(t)
	bad.PublicKeyPEM = "-----BEGIN PUBLIC KEY-----\nnot-a-key\n-----END PUBLIC KEY-----\n"
	if _, err := svc.RegisterCustodian(ctx, bad); err == nil {
		t.Fatal("expected sealing to fail for an unparseable public key")
	}

	claimKey := "__sys:custodian-idx/username/" + bad.TenantID + "/" + bad.Username
	held, err := store.Get(ctx, claimKey)
	if err != nil {
		t.Fatalf("Get(%s): %v", claimKey, err)
	}
	if held != nil {
		t.Errorf("%s = %q after a sealing failure, want the claim released", claimKey, held)
	}

	// And the custodian enrols normally once the key is right.
	good, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, good); err != nil {
		t.Fatalf("retry with a valid public key: %v", err)
	}
}

// unreadableClaimKV fails the tenant-index write, and then every read of the
// username index, so a rollback cannot establish whether the claim is still
// its own. Reads only start failing once the claim has been taken, leaving the
// duplicate check that precedes it working normally.
type unreadableClaimKV struct {
	*memKV
	armed atomic.Bool
}

func (u *unreadableClaimKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := u.memKV.PutIfAbsent(ctx, key, value)
	if stored {
		u.armed.Store(true)
	}
	return stored, err
}

// The guarded writes DELEGATE to the embedded memKV rather than returning a
// fixed (false, nil).
//
// They were stubs, added only to satisfy kvstore.AtomicKVStore. A double that
// refuses what production performs is worse than no double: the compile-time
// assertion passes while the behaviour is absent, so the first caller to
// depend on it -- the expired-claim take-over of go-qrdb#21 -- saw every swap
// fail, in a way that read as a bug in the take-over rather than in the fake.
// This type overrides the operations it exists to interfere with; these are
// not among them.
func (u *unreadableClaimKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return u.memKV.CompareAndDelete(ctx, key, expected)
}

func (u *unreadableClaimKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return u.memKV.CompareAndSwap(ctx, key, expected, next)
}

func (u *unreadableClaimKV) Get(ctx context.Context, key string) ([]byte, error) {
	if u.armed.Load() && strings.HasPrefix(key, "__sys:custodian-idx/username/") {
		return nil, errors.New("simulated claim read failure")
	}
	return u.memKV.Get(ctx, key)
}

func (u *unreadableClaimKV) Put(ctx context.Context, key string, value []byte) error {
	if u.armed.Load() && strings.HasPrefix(key, "__sys:custodian-idx/tenant/") {
		return errors.New("simulated tenant-index write failure")
	}
	return u.memKV.Put(ctx, key, value)
}

// claimReleaseKV lets a test decide what the username index reports, and
// whether it can be deleted, from the moment a claim is taken — so the
// fail-safe branches of releasing a claim can be driven directly.
type claimReleaseKV struct {
	*memKV
	armed          atomic.Bool
	casDeleteErr   atomic.Bool
	overwriteClaim atomic.Pointer[[]byte]
}

// CompareAndDelete is where claim release now happens, so it is where these
// branches are injected.
func (c *claimReleaseKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if c.casDeleteErr.Load() {
		return false, errTestStore
	}
	if v := c.overwriteClaim.Load(); v != nil && c.armed.Load() {
		// Stand in for a successor having taken the claim over: the stored
		// value is no longer the one this registration compares against, so the
		// guarded delete declines and the successor's claim survives.
		if err := c.memKV.Put(ctx, key, *v); err != nil {
			return false, err
		}
	}
	return c.memKV.CompareAndDelete(ctx, key, expected)
}

func (c *claimReleaseKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := c.memKV.PutIfAbsent(ctx, key, value)
	if stored {
		c.armed.Store(true)
	}
	return stored, err
}

// The guarded writes DELEGATE to the embedded memKV rather than returning a
// fixed (false, nil).
//
// They were stubs, added only to satisfy kvstore.AtomicKVStore. A double that
// refuses what production performs is worse than no double: the compile-time
// assertion passes while the behaviour is absent, so the first caller to
// depend on it -- the expired-claim take-over of go-qrdb#21 -- saw every swap
// fail, in a way that read as a bug in the take-over rather than in the fake.
// This type overrides the operations it exists to interfere with; these are
// not among them.
func (c *claimReleaseKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return c.memKV.CompareAndSwap(ctx, key, expected, next)
}

func (c *claimReleaseKV) isClaim(key string) bool {
	return c.armed.Load() && strings.HasPrefix(key, "__sys:custodian-idx/username/")
}

// TestRegisterCustodian_ClaimRelease_FailSafeBranches drives releasing a claim
// when the store will not cooperate. None of these may delete a key on a
// guess, and none may turn the caller's sealing failure into a different
// error: the release is best-effort cleanup, not part of the answer.
//
// The release became a single CompareAndDelete on the exact claim token
// (go-qrdb#21): with claims expiring, a read followed by an unconditional
// delete could remove a SUCCESSOR's claim in the window between the two. So
// these branches are driven through that verb now rather than through Get and
// Delete.
//
// One case disappeared with the read, and its absence is the improvement: there
// is no longer a state in which ownership "cannot be established" separately
// from the delete, because the guard and the delete are one operation. What is
// left is the store failing, and the claim belonging to someone else — and the
// claim must survive both.
func TestRegisterCustodian_ClaimRelease_FailSafeBranches(t *testing.T) {
	badPEM := "-----BEGIN PUBLIC KEY-----\nnot-a-key\n-----END PUBLIC KEY-----\n"
	foreign := []byte("claim:another-registrations-token")

	cases := []struct {
		name  string
		setup func(*claimReleaseKV)
		// survives is what the username index must still hold afterwards.
		survives func(*claimReleaseKV) bool
	}{
		{
			name:  "the guarded delete fails",
			setup: func(kv *claimReleaseKV) { kv.casDeleteErr.Store(true) },
			survives: func(kv *claimReleaseKV) bool {
				kv.casDeleteErr.Store(false)
				return true
			},
		},
		{
			name: "the claim is another registration's",
			setup: func(kv *claimReleaseKV) {
				// A foreign value under the claim key: the compare fails, so
				// the delete does not happen and the successor keeps its claim.
				kv.overwriteClaim.Store(&foreign)
			},
			survives: func(kv *claimReleaseKV) bool {
				kv.overwriteClaim.Store(nil)
				return true
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := &claimReleaseKV{memKV: newMemKV()}
			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
			svc, err := custodian.NewService(store, &noopAudit{}, logger)
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			tc.setup(store)

			req, _ := testRegisterRequest(t)
			req.PublicKeyPEM = badPEM
			if _, err := svc.RegisterCustodian(ctx, req); err == nil {
				t.Fatal("expected sealing to fail for an unparseable public key")
			}

			want := tc.survives(store)
			claimKey := "__sys:custodian-idx/username/" + req.TenantID + "/" + req.Username
			held, err := store.memKV.Get(ctx, claimKey)
			if err != nil {
				t.Fatalf("Get(%s): %v", claimKey, err)
			}
			if want != (held != nil) {
				t.Errorf("claim present = %v, want %v", held != nil, want)
			}

			// Whatever happened to the claim, nothing else was written.
			keys, err := store.memKV.List(ctx, "__sys:custodian/")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(keys) != 0 {
				t.Errorf("primary records %v exist after a sealing failure", keys)
			}
		})
	}
}
