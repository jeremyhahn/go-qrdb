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

// TestRegisterCustodian_CompensationSparesAnotherRegistrationsEnrolment is the
// ownership rule under the one condition that can hand this identity to someone
// else mid-flight: the claim is removed out from under a registration, and a
// second one then takes it and completes.
//
// Both registrations derive the SAME identifier and write the SAME keys, so a
// rollback that trusted the keys alone would delete a completed enrolment. The
// claim token is per-attempt, so the loser can tell that what is in the store is
// no longer its own.
func TestRegisterCustodian_CompensationSparesAnotherRegistrationsEnrolment(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var (
		aAtTenantIndex = make(chan struct{})
		aMayFail       = make(chan struct{})
		aAtFailedWrite atomic.Bool
	)

	store.hook(func(key string, _ []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/tenant/") && !aAtFailedWrite.Swap(true) {
			close(aAtTenantIndex)
			<-aMayFail
			return errors.New("simulated tenant-index write failure")
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

	<-aAtTenantIndex

	// An operator clears what looks like stuck debris — the claim and the
	// record it names — so the identity is free while A is still holding a
	// half-written registration.
	reqB, _ := testRegisterRequest(t)
	for _, key := range []string{
		"__sys:custodian-idx/username/" + reqB.TenantID + "/" + reqB.Username,
		"__sys:custodian/" + custodianIDOf(t, store.memKV, reqB.TenantID),
	} {
		if err := store.Delete(ctx, key); err != nil {
			t.Fatalf("Delete(%s): %v", key, err)
		}
	}

	reqB.EnrollmentID = "enroll-B"
	b, err := svc.RegisterCustodian(ctx, reqB)
	if err != nil {
		t.Fatalf("B could not enrol once the claim was cleared: %v", err)
	}

	close(aMayFail)
	a := <-aResult
	if a.err == nil {
		t.Fatal("A's failed write did not fail the registration")
	}

	// B reported success, so B's enrolment must be intact and addressable.
	got := enrolmentOfKV(t, store.memKV, b.Custodian.ID)
	if got.EnrollmentID != "enroll-B" {
		t.Errorf("EnrollmentID = %q, want enroll-B — A's rollback destroyed the winner", got.EnrollmentID)
	}
	assertEnrolmentIsWhole(ctx, t, store, got.TenantID, got.Username, got.ID)
	if _, err := svc.GetCustodian(ctx, b.Custodian.ID); err != nil {
		t.Errorf("GetCustodian after the race: %v", err)
	}
}

// TestRegisterCustodian_CompensationRunsWhenTheClaimIsStillOurs is the other
// half of the ownership rule: with no competitor, the failed registration must
// clean up after itself — record, indexes and the claim — rather than becoming
// conservative and stranding the username.
func TestRegisterCustodian_CompensationRunsWhenTheClaimIsStillOurs(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var failWrites atomic.Bool
	failWrites.Store(true)
	store.hook(func(key string, _ []byte) error {
		if failWrites.Load() && strings.HasPrefix(key, "__sys:custodian-idx/tenant/") {
			return errors.New("simulated tenant-index write failure")
		}
		return nil
	})

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("expected the failing write to fail registration")
	}

	for _, prefix := range []string{
		"__sys:custodian/",
		"__sys:custodian-idx/tenant/",
		"__sys:custodian-idx/username/",
	} {
		keys, err := store.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List(%s): %v", prefix, err)
		}
		if len(keys) != 0 {
			t.Errorf("%v survived under %s, want the registration compensated", keys, prefix)
		}
	}

	// The username is free again, so the custodian can enrol on a retry.
	failWrites.Store(false)
	retry, _ := testRegisterRequest(t)
	resp, err := svc.RegisterCustodian(ctx, retry)
	if err != nil {
		t.Fatalf("retry after a compensated registration: %v", err)
	}
	if resp.SealedEnvelope == nil {
		t.Error("retry produced no sealed envelope")
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

// TestRegisterCustodian_AmbiguousCommit_IsRolledBackWhenTheRecordIsStillOurs
// covers the one write whose failure does not mean it was not applied. The
// commit replaces the claim token with the custodian identifier, so a store
// that applies it and still reports an error leaves the claim reading as a
// value any registration for this identity would write. Ownership is then
// established from the primary record, which carries this call's bytes and no
// one else's — and the registration must still clean up after itself.
func TestRegisterCustodian_AmbiguousCommit_IsRolledBackWhenTheRecordIsStillOurs(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var applied atomic.Bool
	store.hook(func(key string, value []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/username/") && !applied.Swap(true) {
			// Applied by the store, reported as a failure to the caller.
			if err := store.memKV.Put(ctx, key, value); err != nil {
				return err
			}
			return errors.New("simulated ambiguous commit")
		}
		return nil
	})

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("an ambiguous commit did not fail the registration")
	}

	for _, prefix := range []string{
		"__sys:custodian/",
		"__sys:custodian-idx/tenant/",
		"__sys:custodian-idx/username/",
	} {
		keys, err := store.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List(%s): %v", prefix, err)
		}
		if len(keys) != 0 {
			t.Errorf("%v survived under %s; the applied commit was not rolled back", keys, prefix)
		}
	}

	// And the identity is free, so the custodian can enrol on a retry.
	retry, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, retry); err != nil {
		t.Fatalf("retry after an ambiguous commit: %v", err)
	}
}

// TestRegisterCustodian_AmbiguousCommit_SparesARecordThatIsNoLongerOurs is the
// refusal half of the same rule: when the commit may have applied AND the
// primary record no longer carries this call's bytes, the claim's value proves
// nothing — every registration for this identity commits the same one — so
// nothing is deleted.
func TestRegisterCustodian_AmbiguousCommit_SparesARecordThatIsNoLongerOurs(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	req, _ := testRegisterRequest(t)
	foreign := []byte(`{"id":"someone-elses","username":"` + req.Username + `"}`)

	var applied atomic.Bool
	store.hook(func(key string, value []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/username/") && !applied.Swap(true) {
			if err := store.memKV.Put(ctx, key, value); err != nil {
				return err
			}
			// Another enrolment now holds the primary record.
			primary := "__sys:custodian/" + string(value)
			if err := store.memKV.Put(ctx, primary, foreign); err != nil {
				return err
			}
			return errors.New("simulated ambiguous commit")
		}
		return nil
	})

	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("an ambiguous commit did not fail the registration")
	}

	keys, err := store.List(ctx, "__sys:custodian/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("%d primary records survived, want the other enrolment's 1", len(keys))
	}
	got, err := store.Get(ctx, keys[0])
	if err != nil {
		t.Fatalf("Get(%s): %v", keys[0], err)
	}
	if string(got) != string(foreign) {
		t.Errorf("%s = %q, want the other enrolment's record — rollback deleted or replaced it", keys[0], got)
	}
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

// TestRegisterCustodian_UnreadableClaim_ReportsEverythingAsStranded pins the
// conservative answer when ownership cannot be established at all: nothing is
// deleted on a guess, and every key the registration wrote — the claim
// included — is named so an operator knows what to look at.
func TestRegisterCustodian_UnreadableClaim_ReportsEverythingAsStranded(t *testing.T) {
	ctx := context.Background()
	store := &unreadableClaimKV{memKV: newMemKV()}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := custodian.NewService(store, &noopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	req, _ := testRegisterRequest(t)
	_, err = svc.RegisterCustodian(ctx, req)

	var orphaned *custodian.OrphanedEnrolmentError
	if !errors.As(err, &orphaned) {
		t.Fatalf("err = %v (%T), want *custodian.OrphanedEnrolmentError", err, err)
	}
	claimKey := "__sys:custodian-idx/username/" + req.TenantID + "/" + req.Username
	var namesClaim, namesPrimary bool
	for _, key := range orphaned.Keys {
		switch {
		case key == claimKey:
			namesClaim = true
		case strings.HasPrefix(key, "__sys:custodian/"):
			namesPrimary = true
		}
	}
	if !namesClaim {
		t.Errorf("stranded keys %v do not name the claim an operator must clear", orphaned.Keys)
	}
	if !namesPrimary {
		t.Errorf("stranded keys %v do not name the primary record", orphaned.Keys)
	}

	// Nothing was deleted on a guess: what the registration wrote is still there.
	store.armed.Store(false)
	for _, key := range orphaned.Keys {
		v, getErr := store.Get(ctx, key)
		if getErr != nil {
			t.Fatalf("Get(%s): %v", key, getErr)
		}
		if v == nil {
			t.Errorf("%s was reported stranded but is gone", key)
		}
	}
}

// claimReleaseKV lets a test decide what the username index reports, and
// whether it can be deleted, from the moment a claim is taken — so the
// fail-safe branches of releasing a claim can be driven directly.
type claimReleaseKV struct {
	*memKV
	armed     atomic.Bool
	getErr    atomic.Bool
	getValue  atomic.Pointer[[]byte]
	deleteErr atomic.Bool
}

func (c *claimReleaseKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := c.memKV.PutIfAbsent(ctx, key, value)
	if stored {
		c.armed.Store(true)
	}
	return stored, err
}

func (c *claimReleaseKV) isClaim(key string) bool {
	return c.armed.Load() && strings.HasPrefix(key, "__sys:custodian-idx/username/")
}

func (c *claimReleaseKV) Get(ctx context.Context, key string) ([]byte, error) {
	if c.isClaim(key) {
		if c.getErr.Load() {
			return nil, errors.New("simulated claim read failure")
		}
		if v := c.getValue.Load(); v != nil {
			return *v, nil
		}
	}
	return c.memKV.Get(ctx, key)
}

func (c *claimReleaseKV) Delete(ctx context.Context, key string) error {
	if c.isClaim(key) && c.deleteErr.Load() {
		return errors.New("simulated claim delete failure")
	}
	return c.memKV.Delete(ctx, key)
}

// TestRegisterCustodian_ClaimRelease_FailSafeBranches drives releasing a claim
// when the store will not cooperate. None of these may delete a key on a
// guess, and none may turn the caller's sealing failure into a different
// error: the release is best-effort cleanup, not part of the answer.
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
			name:  "ownership unreadable",
			setup: func(kv *claimReleaseKV) { kv.getErr.Store(true) },
			survives: func(kv *claimReleaseKV) bool {
				kv.getErr.Store(false)
				return true
			},
		},
		{
			name:  "claim is another registration's",
			setup: func(kv *claimReleaseKV) { kv.getValue.Store(&foreign) },
			survives: func(kv *claimReleaseKV) bool {
				kv.getValue.Store(nil)
				return true
			},
		},
		{
			name:     "release fails",
			setup:    func(kv *claimReleaseKV) { kv.deleteErr.Store(true) },
			survives: func(*claimReleaseKV) bool { return true },
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

// TestRegisterCustodian_AmbiguousCommit_WithheldWhenTheRecordCannotBeRead is
// the unreadable case of the same rule. A store that cannot say whether the
// primary record is still this call's gives no proof, so the claim reads as
// unowned and nothing is deleted — the answer that cannot destroy an enrolment.
func TestRegisterCustodian_AmbiguousCommit_WithheldWhenTheRecordCannotBeRead(t *testing.T) {
	ctx := context.Background()
	store := newHookKV()
	svc := newHookService(t, store)

	var applied atomic.Bool
	store.hook(func(key string, value []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/username/") && !applied.Swap(true) {
			if err := store.memKV.Put(ctx, key, value); err != nil {
				return err
			}
			return errors.New("simulated ambiguous commit")
		}
		return nil
	})
	store.hookGet(func(key string) error {
		if applied.Load() && strings.HasPrefix(key, "__sys:custodian/") {
			return errors.New("simulated record read failure")
		}
		return nil
	})

	req, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, req); err == nil {
		t.Fatal("an ambiguous commit did not fail the registration")
	}

	// Nothing was removed on a guess.
	for _, prefix := range []string{"__sys:custodian/", "__sys:custodian-idx/"} {
		keys, err := store.memKV.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List(%s): %v", prefix, err)
		}
		if len(keys) == 0 {
			t.Errorf("nothing survives under %s; rollback deleted without establishing ownership", prefix)
		}
	}
}
