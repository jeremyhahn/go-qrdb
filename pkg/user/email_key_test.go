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

package user

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The email login key (#190). Each test names the property it protects and is
// mutation-checked: deleting the guard it describes makes it fail. Where that
// is not reachable, the test says so rather than implying coverage.

func createWithEmail(t *testing.T, svc UserServicer, username, email, tenantID string) *User {
	t.Helper()
	u, err := svc.CreateUser(context.Background(), &CreateUserRequest{
		Username: username,
		Email:    email,
		TenantID: tenantID,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s,%s): %v", username, email, err)
	}
	return u
}

// The login lookup resolves the record the ceremony must join to. Asserting on
// the ID rather than on "no error" is what makes this fail if the index ever
// resolves to a DIFFERENT user — the failure mode that matters for a login key.
func TestGetUserByEmail_ResolvesTheOwningRecord(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	want := createWithEmail(t, svc, "op", "op@example.com", "acme")
	createWithEmail(t, svc, "other", "other@example.com", "acme")

	got, err := svc.GetUserByEmail(ctx, "acme", "op@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if got.ID != want.ID {
		t.Fatalf("resolved wrong record: got id %d, want %d", got.ID, want.ID)
	}
}

// Case-insensitivity is a security property, not a convenience: without
// NormalizeEmail the two spellings occupy two index slots, so the same address
// becomes two accounts and the second registrant gets one of them.
func TestGetUserByEmail_IsCaseAndSpaceInsensitive(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	want := createWithEmail(t, svc, "op", "Op@Example.COM", "acme")

	if want.Email != "op@example.com" {
		t.Fatalf("stored email not normalized: %q", want.Email)
	}

	for _, spelling := range []string{"op@example.com", "OP@EXAMPLE.COM", "  Op@Example.Com  "} {
		got, err := svc.GetUserByEmail(ctx, "acme", spelling)
		if err != nil {
			t.Fatalf("GetUserByEmail(%q): %v", spelling, err)
		}
		if got.ID != want.ID {
			t.Fatalf("GetUserByEmail(%q): got id %d, want %d", spelling, got.ID, want.ID)
		}
	}
}

// Uniqueness within the tenant, including across spellings — the create-side
// half of the property above.
func TestCreateUser_RefusesDuplicateEmailAcrossSpellings(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createWithEmail(t, svc, "first", "op@example.com", "acme")

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "second",
		Email:    "OP@Example.com",
		TenantID: "acme",
	})
	var exists *EmailExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("want EmailExistsError, got %T: %v", err, err)
	}
}

// Tenant scoping is deliberate: the same person may hold an account in two
// tenants. This is the test that would fail if someone "fixed" the index by
// making it global.
func TestCreateUser_SameEmailInTwoTenantsIsAllowed(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	a := createWithEmail(t, svc, "op", "op@example.com", "acme")
	b := createWithEmail(t, svc, "op", "op@example.com", "globex")

	if a.ID == b.ID {
		t.Fatal("two tenants resolved to one record")
	}

	got, err := svc.GetUserByEmail(ctx, "globex", "op@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail(globex): %v", err)
	}
	if got.ID != b.ID {
		t.Fatalf("tenant scoping leaked: got id %d, want %d", got.ID, b.ID)
	}
}

// A lookup must not answer with a record from a tenant the caller named
// differently. Distinct from the test above: that one proves both records
// exist, this one proves neither tenant can see the other's.
func TestGetUserByEmail_DoesNotCrossTenants(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createWithEmail(t, svc, "op", "op@example.com", "acme")

	_, err := svc.GetUserByEmail(ctx, "globex", "op@example.com")
	var notFound *UserNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("want UserNotFoundError, got %T: %v", err, err)
	}
}

// Changing the login key must move the index, not merely rewrite the record:
// leaving the old key behind keeps a retired address able to resolve the
// account, and failing to write the new one locks the user out.
func TestUpdateUser_MovesTheEmailIndex(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	u := createWithEmail(t, svc, "op", "old@example.com", "acme")

	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	got, err := svc.GetUserByEmail(ctx, "acme", "new@example.com")
	if err != nil {
		t.Fatalf("new key does not resolve: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("new key resolved wrong record: %d != %d", got.ID, u.ID)
	}

	if _, err := svc.GetUserByEmail(ctx, "acme", "old@example.com"); err == nil {
		t.Fatal("retired email still resolves — old index entry was not removed")
	}
}

// The takeover this guard exists to stop: without the uniqueness re-check on
// update, this repoints the index that logins resolve through onto the second
// user's record.
func TestUpdateUser_RefusesEmailAlreadyTaken(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	victim := createWithEmail(t, svc, "victim", "victim@example.com", "acme")
	attacker := createWithEmail(t, svc, "attacker", "attacker@example.com", "acme")

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: attacker.ID, Email: "victim@example.com"})
	var exists *EmailExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("want EmailExistsError, got %T: %v", err, err)
	}

	// And the victim's key still resolves to the victim.
	got, gerr := svc.GetUserByEmail(ctx, "acme", "victim@example.com")
	if gerr != nil {
		t.Fatalf("victim lookup: %v", gerr)
	}
	if got.ID != victim.ID {
		t.Fatalf("index was repointed: got id %d, want %d", got.ID, victim.ID)
	}
}

// Deleting a user must free the address. A leaked index entry resolves to a
// primary record that no longer exists, which is a lookup failure at best and
// an id-reuse hazard at worst.
func TestDeleteUser_RemovesTheEmailIndex(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	if err := svc.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if _, err := svc.GetUserByEmail(ctx, "acme", "op@example.com"); err == nil {
		t.Fatal("email still resolves after delete — index entry leaked")
	}

	// The address is reusable, which is the observable consequence.
	if _, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "successor", Email: "op@example.com", TenantID: "acme",
	}); err != nil {
		t.Fatalf("address not reusable after delete: %v", err)
	}
}

// A user with no email login key must stay creatable — a bootstrap admin, an
// mTLS-only identity — but the omission must be DECLARED, never defaulted.
// Renamed from TestCreateUser_EmailIsOptional, which asserted only the first
// half and would have passed against the code that let an ordinary user be
// created with no entry in the login index (#190).
func TestCreateUser_EmailOmissionMustBeDeclared(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	// Undeclared: refused, including whitespace-only, which normalizes to empty.
	for _, bad := range []string{"", "   "} {
		_, err := svc.CreateUser(ctx, &CreateUserRequest{
			Username: "undeclared", Email: bad, TenantID: "acme",
		})
		var v *ValidationError
		if !errors.As(err, &v) {
			t.Fatalf("CreateUser(email=%q): want ValidationError, got %T: %v", bad, err, err)
		}
	}

	// Declared: allowed, and resolvable by username.
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "admin", TenantID: "acme"})
	if err != nil {
		t.Fatalf("CreateUser without email: %v", err)
	}
	if u.Email != "" {
		t.Fatalf("unexpected email %q", u.Email)
	}

	got, err := svc.GetUserByUsername(ctx, "acme", "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("got id %d, want %d", got.ID, u.ID)
	}
}

// An empty lookup is a validation failure, not a scan that might answer with
// whichever record happens to carry no email.
func TestGetUserByEmail_RejectsEmpty(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	createWithEmail(t, svc, "op", "op@example.com", "acme")

	for _, empty := range []string{"", "   "} {
		_, err := svc.GetUserByEmail(ctx, "acme", empty)
		var v *ValidationError
		if !errors.As(err, &v) {
			t.Fatalf("GetUserByEmail(%q): want ValidationError, got %T: %v", empty, err, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Fault injection: the login index must not be left inconsistent when a write
// after it fails (go-qrdb #190, Codex review on PR #191).
// ---------------------------------------------------------------------------

// faultKV fails the Nth Put whose key matches a prefix, so a test can put the
// failure exactly where it matters — after the index has already moved.
type faultKV struct {
	*memKVStore
	failPutPrefix string
	// failClaimKey fails PutIfAbsent for ONE exact key. The claim and the
	// rollback's restore are both PutIfAbsent, so a prefix would hit the claim
	// too and the update would fail before compensation was ever reached —
	// testing nothing about compensation.
	failClaimKey string
	// failReleaseKey fails CompareAndDelete for ONE exact key, so a create's
	// rollback cannot give that claim back. Releasing is a THIRD verb — Put,
	// PutIfAbsent and CompareAndSwap are all already injected here and none of
	// them reaches it — and without its own injection point the create
	// compensation path had no way to fail in a test at all.
	failReleaseKey string
	err            error
}

func (f *faultKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.failReleaseKey != "" && key == f.failReleaseKey {
		return false, f.err
	}
	return f.memKVStore.CompareAndDelete(ctx, key, expected)
}

func (f *faultKV) Put(ctx context.Context, key string, value []byte) error {
	if f.failPutPrefix != "" && strings.HasPrefix(key, f.failPutPrefix) {
		return f.err
	}
	return f.memKVStore.Put(ctx, key, value)
}

// PutIfAbsent honours the same injection. The rollback restores a released
// handle with PutIfAbsent, not Put, so a fault targeting only Put would let
// compensation quietly succeed and this double would stop testing the path it
// was written for.
// CompareAndSwap honours failPutPrefix as well. UpdateUser commits the primary
// record with CompareAndSwap now, not Put, and a fault targeting only Put would
// silently stop reaching the commit — the fourth time in this PR that changing
// which primitive a path uses moved an injection off the path it was written
// for. An injection point follows the VERB.
func (f *faultKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.failPutPrefix != "" && strings.HasPrefix(key, f.failPutPrefix) {
		return false, f.err
	}
	return f.memKVStore.CompareAndSwap(ctx, key, expected, next)
}

func (f *faultKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.failClaimKey != "" && key == f.failClaimKey {
		return false, f.err
	}
	return f.memKVStore.PutIfAbsent(ctx, key, value)
}

func newEmailFaultStore(t *testing.T, failPutPrefix string, err error) (UserServicer, *faultKV) {
	t.Helper()
	kv := &faultKV{memKVStore: newMemKVStore(), failPutPrefix: failPutPrefix, err: err}
	svc, sErr := NewStore(kv, newTestIDGen(t))
	if sErr != nil {
		t.Fatalf("NewStore: %v", sErr)
	}
	return svc, kv
}

var errPrimaryWriteFault = errors.New("primary write fault")

// The finding, exactly: the email index moves, then the primary record write
// fails. Without compensation the retired address stops resolving, the new one
// resolves to a record whose Email field still holds the OLD value, and the new
// address stays locked against a future create — an account made unreachable by
// a failed update.
func TestUpdateUser_PrimaryWriteFailureLeavesTheEmailIndexIntact(t *testing.T) {
	ctx := context.Background()

	// Seed through a healthy store, then swap in the fault.
	svc, kv := newEmailFaultStore(t, "", nil)
	u := createWithEmail(t, svc, "op", "old@example.com", "acme")

	kv.failPutPrefix = keyPrefixPrimary
	kv.err = errPrimaryWriteFault

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"})
	if !errors.Is(err, errPrimaryWriteFault) {
		t.Fatalf("want the injected primary fault, got %T: %v", err, err)
	}

	kv.failPutPrefix = "" // stop injecting; observe the resulting state

	// The retired address must still resolve to its owner.
	got, gErr := svc.GetUserByEmail(ctx, "acme", "old@example.com")
	if gErr != nil {
		t.Fatalf("old email no longer resolves after a failed update: %v", gErr)
	}
	if got.ID != u.ID {
		t.Fatalf("old email resolves to the wrong record: %d != %d", got.ID, u.ID)
	}
	if got.Email != "old@example.com" {
		t.Fatalf("record and index disagree: record says %q", got.Email)
	}

	// The address that was never adopted must not resolve, and must not be
	// locked against a future create.
	if _, e := svc.GetUserByEmail(ctx, "acme", "new@example.com"); e == nil {
		t.Fatal("uncommitted email still resolves — the new index entry leaked")
	}
	if _, e := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "someone-else", Email: "new@example.com", TenantID: "acme",
	}); e != nil {
		t.Fatalf("uncommitted email left locked against a later create: %v", e)
	}
}

// The username index has the identical hazard and the same compensation. Codex
// flagged only the email path because that one was PR-introduced; fixing one
// and leaving its twin would have left a known defect behind.
func TestUpdateUser_PrimaryWriteFailureLeavesTheUsernameIndexIntact(t *testing.T) {
	ctx := context.Background()

	svc, kv := newEmailFaultStore(t, "", nil)
	u := createWithEmail(t, svc, "old-name", "op@example.com", "acme")

	kv.failPutPrefix = keyPrefixPrimary
	kv.err = errPrimaryWriteFault

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Username: "new-name"})
	if !errors.Is(err, errPrimaryWriteFault) {
		t.Fatalf("want the injected primary fault, got %T: %v", err, err)
	}

	kv.failPutPrefix = ""

	got, gErr := svc.GetUserByUsername(ctx, "acme", "old-name")
	if gErr != nil {
		t.Fatalf("old username no longer resolves after a failed update: %v", gErr)
	}
	if got.ID != u.ID {
		t.Fatalf("old username resolves to the wrong record: %d != %d", got.ID, u.ID)
	}

	if _, e := svc.GetUserByUsername(ctx, "acme", "new-name"); e == nil {
		t.Fatal("uncommitted username still resolves — the new index entry leaked")
	}
}

// cancelKV fails the primary Put by CANCELING the request context, and honours
// cancellation on every operation thereafter — the way a real store does.
//
// This is the case a one-shot storage fault cannot reach, and the one that
// matters most: the usual reason a write fails is that the caller's context
// ended, and compensation issued on that same context fails immediately. A
// rollback that only works when the context is healthy is a rollback that does
// not run when it is needed (Codex review, PR #191).
type cancelKV struct {
	*memKVStore
	cancel context.CancelFunc
	armed  bool
}

func (c *cancelKV) Put(ctx context.Context, key string, value []byte) error {
	if c.armed && strings.HasPrefix(key, keyPrefixPrimary) {
		c.armed = false
		c.cancel() // the request context dies mid-write, as a timeout would
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.memKVStore.Put(ctx, key, value)
}

// The primary commit is a CompareAndSwap, so the cancellation must be armed
// there too — otherwise this double stops reproducing the case it exists for.
func (c *cancelKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if c.armed && strings.HasPrefix(key, keyPrefixPrimary) {
		c.armed = false
		c.cancel()
		return false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return c.memKVStore.CompareAndSwap(ctx, key, expected, next)
}

func (c *cancelKV) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.memKVStore.Delete(ctx, key)
}

func TestUpdateUser_CompensationSurvivesACanceledRequestContext(t *testing.T) {
	kv := &cancelKV{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	seedCtx := context.Background()
	u, cErr := svc.CreateUser(seedCtx, &CreateUserRequest{
		Username: "op", Email: "old@example.com", TenantID: "acme",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv.cancel = cancel
	kv.armed = true

	_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"})
	if uErr == nil {
		t.Fatal("expected the update to fail")
	}
	// The rollback must have SUCCEEDED, so the error stays the plain failure
	// rather than escalating to IndexCompensationError.
	var comp *IndexCompensationError
	if errors.As(uErr, &comp) {
		t.Fatalf("compensation did not run on a canceled context: %v", uErr)
	}

	// Observe the resulting state on a fresh, live context.
	obs := context.Background()

	got, gErr := svc.GetUserByEmail(obs, "acme", "old@example.com")
	if gErr != nil {
		t.Fatalf("old email no longer resolves after a canceled update: %v", gErr)
	}
	if got.ID != u.ID || got.Email != "old@example.com" {
		t.Fatalf("record and index disagree: id=%d email=%q", got.ID, got.Email)
	}

	if _, e := svc.GetUserByEmail(obs, "acme", "new@example.com"); e == nil {
		t.Fatal("uncommitted email still resolves — the new index entry leaked")
	}
	if _, e := svc.CreateUser(obs, &CreateUserRequest{
		Username: "someone-else", Email: "new@example.com", TenantID: "acme",
	}); e != nil {
		t.Fatalf("uncommitted email left locked against a later create: %v", e)
	}
}

// When compensation itself cannot complete, the caller must be TOLD. Silently
// returning the original error would report a recoverable failed write while
// the canonical login key is left inconsistent.
func TestUpdateUser_IncompleteCompensationIsReported(t *testing.T) {
	ctx := context.Background()

	svc, kv := newEmailFaultStore(t, "", nil)
	u := createWithEmail(t, svc, "op", "old@example.com", "acme")

	// Fail the primary write (triggering compensation) AND the rollback's
	// restore of the OLD email key — but not the claim of the new one, which
	// must succeed for there to be anything to compensate.
	kv.failPutPrefix = keyPrefixPrimary
	kv.failClaimKey = emailIndexKey("acme", "old@example.com")
	kv.err = errPrimaryWriteFault

	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"})

	var comp *IndexCompensationError
	if !errors.As(err, &comp) {
		t.Fatalf("want IndexCompensationError, got %T: %v", err, err)
	}
	if len(comp.Failures) == 0 {
		t.Fatal("IndexCompensationError carries no cleanup failures")
	}
	// The original cause must still be matchable through it.
	if !errors.Is(err, errPrimaryWriteFault) {
		t.Fatalf("original cause not reachable through the wrapper: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency: the login key must be claimed, not checked-then-written
// (go-qrdb #190, Codex review on PR #191).
// ---------------------------------------------------------------------------

// gateKV forces the interleaving instead of hoping for it.
//
// A first attempt at these tests raced two goroutines and let the scheduler
// decide. Run against the UNFIXED Get-then-Put it passed 200/200 — the window
// between the read and the write is a few instructions, so the test detected
// nothing while looking careful. A test that cannot fail against the code it
// exists to reject is evidence of nothing.
//
// This gate makes the window deterministic: any caller reading the contested
// key blocks until a second caller reaches the same point, so both observe the
// key absent before either writes — precisely the racy sequence. Against the
// FIXED code nothing reads that key at all (the claim is a single PutIfAbsent),
// so the gate is never entered and the short deadline keeps a one-caller test
// from hanging.
type gateKV struct {
	*memKVStore
	gateKey  string
	mu       sync.Mutex
	armed    bool
	arrived  int
	released chan struct{}
}

func newGateKV(gateKey string) *gateKV {
	return &gateKV{memKVStore: newMemKVStore(), gateKey: gateKey, armed: true, released: make(chan struct{})}
}

// disarm stops gating, so the test's own verification reads afterwards are not
// delayed by a barrier that will never see a second arrival.
// setKey arms the gate on a key that is only known after construction (a
// record's primary key depends on its generated id).
func (g *gateKV) setKey(key string) {
	g.mu.Lock()
	g.gateKey = key
	g.mu.Unlock()
}

func (g *gateKV) disarm() {
	g.mu.Lock()
	g.armed = false
	g.mu.Unlock()
}

func (g *gateKV) Get(ctx context.Context, key string) ([]byte, error) {
	g.mu.Lock()
	gated := g.armed && g.gateKey != "" && key == g.gateKey
	g.mu.Unlock()

	// Read FIRST, then block. Blocking before the read would release both
	// callers to race their reads, letting one finish read-and-write before the
	// other read at all — which is how the first version of this gate failed to
	// reproduce anything. Holding them here means both have already observed the
	// key absent before either is allowed to write, which is the racy sequence
	// exactly rather than probably.
	v, err := g.memKVStore.Get(ctx, key)
	if !gated {
		return v, err
	}

	g.mu.Lock()
	g.arrived++
	if g.arrived == 2 {
		close(g.released)
	}
	g.mu.Unlock()
	select {
	case <-g.released:
	case <-time.After(250 * time.Millisecond):
	}
	return v, err
}

func newGatedStore(t *testing.T, gateKey string) (UserServicer, *gateKV) {
	t.Helper()
	kv := newGateKV(gateKey)
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc, kv
}

// Two concurrent creates of the same email must produce exactly one user.
//
// Under the previous Get-then-Put both callers could observe the index absent,
// both persist an enabled record carrying the same normalized email, and
// whichever wrote the index last would win — leaving the loser as a live account
// whose canonical login key resolves to somebody ELSE's record. That is identity
// corruption, not a lost update.
//
// The goroutines are released by a closed channel and spin rather than yield:
// yielding hands the other goroutine exactly the slot it needs to finish, which
// is how a race test comes to detect nothing while looking careful.
func TestCreateUser_ConcurrentSameEmailYieldsExactlyOneUser(t *testing.T) {
	const attempts = 20

	for i := 0; i < attempts; i++ {
		svc, gate := newGatedStore(t, emailIndexKey("acme", "contested@example.com"))
		ctx := context.Background()

		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]*User, 2)
		errs := make([]error, 2)

		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				results[g], errs[g] = svc.CreateUser(ctx, &CreateUserRequest{
					Username: fmt.Sprintf("user-%d", g),
					Email:    "contested@example.com",
					TenantID: "acme",
				})
			}(g)
		}
		close(start)
		wg.Wait()
		gate.disarm()

		var winners []*User
		for g := 0; g < 2; g++ {
			if errs[g] == nil {
				winners = append(winners, results[g])
				continue
			}
			var exists *EmailExistsError
			if !errors.As(errs[g], &exists) {
				t.Fatalf("attempt %d: unexpected error: %T %v", i, errs[g], errs[g])
			}
		}
		if len(winners) != 1 {
			t.Fatalf("attempt %d: %d creates succeeded for one email, want exactly 1", i, len(winners))
		}

		// The surviving record must be the one the login key resolves to.
		got, err := svc.GetUserByEmail(ctx, "acme", "contested@example.com")
		if err != nil {
			t.Fatalf("attempt %d: login key does not resolve: %v", i, err)
		}
		if got.ID != winners[0].ID {
			t.Fatalf("attempt %d: login key resolves to id %d, but the surviving user is %d",
				i, got.ID, winners[0].ID)
		}
	}
}

// The same race on the update path: two concurrent moves onto one address.
func TestUpdateUser_ConcurrentSameEmailYieldsExactlyOneWinner(t *testing.T) {
	const attempts = 20

	for i := 0; i < attempts; i++ {
		svc, gate := newGatedStore(t, emailIndexKey("acme", "contested@example.com"))
		ctx := context.Background()

		a := createWithEmail(t, svc, "a", "a@example.com", "acme")
		b := createWithEmail(t, svc, "b", "b@example.com", "acme")

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		ids := []uint64{a.ID, b.ID}

		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				_, errs[g] = svc.UpdateUser(ctx, &UpdateUserRequest{
					ID: ids[g], Email: "contested@example.com",
				})
			}(g)
		}
		close(start)
		wg.Wait()
		gate.disarm()

		winners := 0
		var winner uint64
		for g := 0; g < 2; g++ {
			if errs[g] == nil {
				winners++
				winner = ids[g]
			}
		}
		if winners != 1 {
			t.Fatalf("attempt %d: %d updates succeeded for one email, want exactly 1", i, winners)
		}

		got, err := svc.GetUserByEmail(ctx, "acme", "contested@example.com")
		if err != nil {
			t.Fatalf("attempt %d: contested key does not resolve: %v", i, err)
		}
		if got.ID != winner {
			t.Fatalf("attempt %d: contested key resolves to %d, winner was %d", i, got.ID, winner)
		}
	}
}

// reclaimKV simulates the interleaving Codex identified: the moment UpdateUser
// releases the old handle, a DIFFERENT caller legitimately claims it — the key
// really is free at that instant — and the primary write then fails, so the
// rollback tries to put the old key back.
//
// Forced at the seam rather than raced, so it reproduces every run.
type reclaimKV struct {
	*memKVStore
	releaseKey string // the key whose release triggers the reclaim
	rivalID    []byte // the id the rival claims it for
	failPrefix string // Put prefix that fails, to trigger the rollback
	err        error
}

func (r *reclaimKV) Delete(ctx context.Context, key string) error {
	if err := r.memKVStore.Delete(ctx, key); err != nil {
		return err
	}
	r.handOver(ctx, key)
	return nil
}

// CompareAndDelete is the verb the guarded release actually uses now. Hooking
// only Delete left this double silently inert once handle releases became
// guarded — the fifth time on this PR that changing a primitive moved an
// injection off the path it was written for.
func (r *reclaimKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	deleted, err := r.memKVStore.CompareAndDelete(ctx, key, expected)
	if err != nil || !deleted {
		return deleted, err
	}
	r.handOver(ctx, key)
	return true, nil
}

// handOver gives the just-freed key to a rival, exactly as a concurrent create
// would the instant the key became available.
func (r *reclaimKV) handOver(ctx context.Context, key string) {
	if key == r.releaseKey {
		_, _ = r.memKVStore.PutIfAbsent(ctx, key, r.rivalID)
	}
}

func (r *reclaimKV) Put(ctx context.Context, key string, value []byte) error {
	if r.failPrefix != "" && strings.HasPrefix(key, r.failPrefix) {
		return r.err
	}
	return r.memKVStore.Put(ctx, key, value)
}

func (r *reclaimKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if r.failPrefix != "" && strings.HasPrefix(key, r.failPrefix) {
		return false, r.err
	}
	return r.memKVStore.CompareAndSwap(ctx, key, expected, next)
}

// A rollback must not evict the winner of the released key. Restoring over it
// would leave the rival's live record unreachable by its own unique handle —
// trading our failed update for their outage.
func TestUpdateUser_RollbackDoesNotEvictAConcurrentClaimant(t *testing.T) {
	ctx := context.Background()
	rival := encodeID(999999)

	kv := &reclaimKV{memKVStore: newMemKVStore(), rivalID: rival}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "old@example.com", TenantID: "acme",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}

	// Arm: releasing the old email key hands it to the rival, and the primary
	// write then fails so compensation runs.
	kv.releaseKey = emailIndexKey("acme", "old@example.com")
	kv.failPrefix = keyPrefixPrimary
	kv.err = errPrimaryWriteFault

	_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"})
	if uErr == nil {
		t.Fatal("expected the update to fail")
	}

	// The rollback could not restore the old key, and must SAY so rather than
	// reporting a plain failed write.
	var comp *IndexCompensationError
	if !errors.As(uErr, &comp) {
		t.Fatalf("want IndexCompensationError, got %T: %v", uErr, uErr)
	}
	var reclaimed *HandleReclaimedError
	if !errors.As(uErr, &reclaimed) && !containsReclaimed(comp) {
		t.Fatalf("compensation failure does not name the reclaimed handle: %v", uErr)
	}

	// The decisive assertion: the rival still holds the key.
	kv.failPrefix = ""
	got, gErr := kv.memKVStore.Get(ctx, kv.releaseKey)
	if gErr != nil {
		t.Fatalf("read back: %v", gErr)
	}
	if !bytes.Equal(got, rival) {
		t.Fatalf("rollback evicted the concurrent claimant: key holds %v, want the rival's id %v", got, rival)
	}
}

func containsReclaimed(comp *IndexCompensationError) bool {
	for _, f := range comp.Failures {
		var r *HandleReclaimedError
		if errors.As(f, &r) {
			return true
		}
	}
	return false
}

// Two updates racing on ONE user, each moving it to a different email.
//
// Distinct from the contention tests above, which race two USERS for one handle.
// Here both callers read the same record, both legitimately claim their own new
// key, and both delete the shared old one. Before the commit was made
// conditional, both then wrote the primary: the winner set User.Email, and the
// LOSER's key survived as a live alias resolving to a user whose canonical email
// is something else — and permanently unavailable to any other account
// (Codex, PR #191).
//
// Forced at the seam: the double holds the first commit until the second caller
// has also read and claimed, so both are committing against the same version.
func TestUpdateUser_ConcurrentUpdatesOfOneUserLeaveOneHandle(t *testing.T) {
	ctx := context.Background()

	// Both callers must commit against the SAME version, or they simply run in
	// sequence and both succeed legitimately — which is what the first version
	// of this test measured, and it measured nothing. The gate holds each
	// caller at its read of the primary record until the other has read it too.
	seed := newTestStore(t)
	_ = seed
	svc, gate := newGatedStore(t, "")
	u := createWithEmail(t, svc, "op", "old@example.com", "acme")
	gate.setKey(primaryKey(u.ID))

	start := make(chan struct{})
	var wg sync.WaitGroup
	targets := []string{"first@example.com", "second@example.com"}
	errs := make([]error, 2)

	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			_, errs[g] = svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: targets[g]})
		}(g)
	}
	close(start)
	wg.Wait()
	gate.disarm()

	winners := 0
	winner := ""
	for g := 0; g < 2; g++ {
		if errs[g] == nil {
			winners++
			winner = targets[g]
		}
	}
	if winners != 1 {
		t.Fatalf("%d updates succeeded on one user, want exactly 1 (errs: %v)", winners, errs)
	}

	// Exactly one email may resolve, and it must be the committed one.
	resolved := 0
	for _, e := range targets {
		got, err := svc.GetUserByEmail(ctx, "acme", e)
		if err != nil {
			continue
		}
		resolved++
		if e != winner {
			t.Fatalf("loser's email %q still resolves (to id %d) — a live alias for a record whose canonical email is %q",
				e, got.ID, winner)
		}
	}
	if resolved != 1 {
		t.Fatalf("%d of the two emails resolve, want exactly 1", resolved)
	}

	// And the retired address is free again.
	if _, err := svc.GetUserByEmail(ctx, "acme", "old@example.com"); err == nil {
		t.Fatal("the retired email still resolves")
	}
}

// ---------------------------------------------------------------------------
// The same concurrency class in the paths Codex had not reached: delete and
// enable/disable. Found by auditing for the shape rather than waiting for
// round six (#190).
// ---------------------------------------------------------------------------

// A delete holding a STALE view must not evict a handle another account has
// since legitimately claimed.
//
// The sequence is reachable without any race in the test: A moves off the
// address, B takes it, and only then does a delete of A run — which is exactly
// what a delete that read A before the move would do.
func TestDeleteUser_DoesNotEvictAHandleAnotherAccountNowOwns(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	a := createWithEmail(t, svc, "a", "shared@example.com", "acme")

	// A moves off the address, freeing it.
	if _, err := svc.UpdateUser(ctx, &UpdateUserRequest{ID: a.ID, Email: "moved@example.com"}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	// B legitimately takes it.
	b := createWithEmail(t, svc, "b", "shared@example.com", "acme")

	// A stale delete of A believes it still owns "shared@example.com". Force
	// exactly that by restoring A's pre-move record before deleting.
	stale := *a
	stale.Email = "shared@example.com"
	raw, mErr := json.Marshal(&stale)
	if mErr != nil {
		t.Fatalf("marshal: %v", mErr)
	}
	if err := svc.(*kvUserStore).store.Put(ctx, primaryKey(a.ID), raw); err != nil {
		t.Fatalf("seed stale record: %v", err)
	}

	if err := svc.DeleteUser(ctx, a.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// B must still own the address.
	got, err := svc.GetUserByEmail(ctx, "acme", "shared@example.com")
	if err != nil {
		t.Fatalf("B's login key no longer resolves after A was deleted: %v", err)
	}
	if got.ID != b.ID {
		t.Fatalf("address resolves to %d, want B (%d)", got.ID, b.ID)
	}
}

// staleReadKV serves ONE stale answer for a key, modelling a read that happened
// before a concurrent commit. Without it EnableUser re-reads fresh and never
// holds the stale view the defect requires — the first version of this test
// passed against an unconditional Put, i.e. measured nothing.
type staleReadKV struct {
	*memKVStore
	key   string
	stale []byte
	once  bool
}

func (s *staleReadKV) Get(ctx context.Context, key string) ([]byte, error) {
	if key == s.key && !s.once {
		s.once = true
		return s.stale, nil
	}
	return s.memKVStore.Get(ctx, key)
}

// EnableUser writes the WHOLE record back, so an unconditional commit reverts a
// concurrent email change while its index entry has already moved — leaving the
// login key resolving to a record that no longer carries it.
func TestEnableUser_DoesNotRevertAConcurrentEmailChange(t *testing.T) {
	ctx := context.Background()
	kv := &staleReadKV{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "old@example.com", "acme")

	// Capture the record as it stood, then let an update commit underneath.
	base, gErr := kv.memKVStore.Get(ctx, primaryKey(u.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}
	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"}); uErr != nil {
		t.Fatalf("UpdateUser: %v", uErr)
	}

	// Arm: EnableUser's read returns the pre-update record.
	kv.key = primaryKey(u.ID)
	kv.stale = base

	eErr := svc.EnableUser(ctx, u.ID)
	// Losing the race is the CORRECT outcome; silently winning it is the defect.
	var lost *ConcurrentUpdateError
	if eErr != nil && !errors.As(eErr, &lost) {
		t.Fatalf("EnableUser: unexpected error %T: %v", eErr, eErr)
	}

	got, rErr := svc.GetUser(ctx, u.ID)
	if rErr != nil {
		t.Fatalf("GetUser: %v", rErr)
	}
	if got.Email != "new@example.com" {
		t.Fatalf("enable reverted the committed email: record says %q while the index says new@example.com", got.Email)
	}
	if _, e := svc.GetUserByEmail(ctx, "acme", "new@example.com"); e != nil {
		t.Fatalf("login key no longer resolves to its record: %v", e)
	}
}

// committedThenFailedKV commits the swap and THEN reports an error, modelling a
// store that lost the reply. This is the case CompareAndSwap's contract exists
// for: a non-nil error means the outcome is unknown, not that nothing happened.
type committedThenFailedKV struct {
	*memKVStore
	armKeyPrefix string
	err          error
	fired        bool
}

func (c *committedThenFailedKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if !c.fired && c.armKeyPrefix != "" && strings.HasPrefix(key, c.armKeyPrefix) {
		c.fired = true
		// The write LANDS, then the reply is lost.
		_, _ = c.memKVStore.CompareAndSwap(ctx, key, expected, next)
		return false, c.err
	}
	return c.memKVStore.CompareAndSwap(ctx, key, expected, next)
}

// A commit that landed must not be rolled back. Blind compensation here would
// leave the record carrying the new email while the indexes resolve the old one
// — corrupting the canonical login key on a write that actually succeeded.
func TestUpdateUser_ACommitThatLandedIsNotRolledBack(t *testing.T) {
	ctx := context.Background()
	kv := &committedThenFailedKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "old@example.com", TenantID: "acme",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}

	kv.armKeyPrefix = keyPrefixPrimary

	// The store commits and then reports failure. The record and its indexes
	// must agree afterwards, whichever answer the caller is given.
	_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"})
	if uErr != nil {
		var unknown *CommitOutcomeUnknownError
		if !errors.As(uErr, &unknown) {
			t.Fatalf("unexpected error %T: %v", uErr, uErr)
		}
	}

	got, gErr := svc.GetUser(ctx, u.ID)
	if gErr != nil {
		t.Fatalf("GetUser: %v", gErr)
	}

	// Whatever the record says, the login index must agree with it.
	resolved, rErr := svc.GetUserByEmail(ctx, "acme", got.Email)
	if rErr != nil {
		t.Fatalf("record carries %q but the login key does not resolve: %v", got.Email, rErr)
	}
	if resolved.ID != u.ID {
		t.Fatalf("login key for %q resolves to %d, want %d", got.Email, resolved.ID, u.ID)
	}

	// And the other address must NOT also resolve to this user.
	other := "old@example.com"
	if got.Email == other {
		other = "new@example.com"
	}
	if alias, aErr := svc.GetUserByEmail(ctx, "acme", other); aErr == nil && alias.ID == u.ID {
		t.Fatalf("both %q and %q resolve to user %d — the commit and its indexes disagree",
			got.Email, other, u.ID)
	}
}

// A LOST update must not leave a cert-thumbprint index entry behind.
//
// The cert path mutates its index before the conditional commit, exactly as the
// handle paths do, and was the one index in this function left uncompensated —
// missed because the earlier audit enumerated the two HANDLES rather than the
// writes (Codex, PR #191). The consequence is worse than a stale read:
// UserByCertThumbprint rejects the entry as stale, while AddCertBinding sees the
// thumbprint as owned and refuses to let anyone else bind it. A live thumbprint,
// reserved against a binding that does not exist.
func TestUpdateUser_ALostUpdateLeavesNoCertIndexEntry(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	store := svc.(*kvUserStore)

	// A well-formed base64url SHA-256 SPKI thumbprint: AddCertBinding validates
	// the shape, so a placeholder would fail for the wrong reason.
	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	// Capture the record as the losing caller read it, then let another update
	// commit underneath so the CAS will fail.
	base, err := store.store.Get(ctx, primaryKey(u.ID))
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "winner"}); uErr != nil {
		t.Fatalf("winning update: %v", uErr)
	}

	// Replay the stale read so the cert update commits against a superseded
	// version and loses.
	kv := &staleReadKV{memKVStore: store.store.(*memKVStore), key: primaryKey(u.ID), stale: base}
	losing, nErr := NewStore(kv, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}

	_, lErr := losing.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		CertBindings: []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}},
	})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	got, gErr := svc.GetUser(ctx, u.ID)
	if gErr != nil {
		t.Fatalf("GetUser: %v", gErr)
	}
	for _, b := range got.CertBindings {
		if b.Thumbprint == thumb {
			t.Fatal("the record adopted a binding from a lost update")
		}
	}

	// THE DECISIVE ASSERTION. Checking UserByCertThumbprint does not
	// discriminate: it rejects a leaked entry as stale either way, so the first
	// version of this test passed against the uncompensated code. The observable
	// consequence is that the thumbprint stays RESERVED — AddCertBinding sees it
	// as owned and refuses to let anybody else bind it.
	other := createWithEmail(t, svc, "other", "other@example.com", "acme")
	if _, bErr := svc.AddCertBinding(ctx, other.ID, thumb, 0); bErr != nil {
		t.Fatalf("thumbprint left reserved by a lost update — another user cannot bind it: %v", bErr)
	}
}

// A mutation that COMMITTED must still be audited, even when the store reported
// the commit as failed.
//
// The readback branch that recognises "it landed" was returning success
// immediately, before emitAudit — so exactly during a backend communication
// failure, an email or role change, or an account being enabled or disabled,
// completed with no audit record. A security-relevant activity gap created by
// the branch that made the ambiguous case succeed (Codex, PR #191).
func TestLandedCommitsAreStillAudited(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action func(UserServicer, context.Context, uint64) error
		want   string
	}{
		{"update", func(s UserServicer, ctx context.Context, id uint64) error {
			_, err := s.UpdateUser(ctx, &UpdateUserRequest{ID: id, Email: "new@example.com"})
			return err
		}, "update"},
		{"enable", func(s UserServicer, ctx context.Context, id uint64) error {
			return s.EnableUser(ctx, id)
		}, "enable"},
		{"disable", func(s UserServicer, ctx context.Context, id uint64) error {
			return s.DisableUser(ctx, id)
		}, "disable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			al := &fakeAuditLogger{}
			kv := &committedThenFailedKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
			svc, err := NewStore(kv, newTestIDGen(t), WithAuditLogger(al))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
				Username: "op", Email: "old@example.com", TenantID: "acme",
			})
			if cErr != nil {
				t.Fatalf("seed: %v", cErr)
			}

			before := len(al.snapshot())
			kv.armKeyPrefix = keyPrefixPrimary

			// The store commits and then reports failure.
			if aErr := tc.action(svc, ctx, u.ID); aErr != nil {
				var unknown *CommitOutcomeUnknownError
				if !errors.As(aErr, &unknown) {
					t.Fatalf("unexpected error %T: %v", aErr, aErr)
				}
			}

			// If the write landed, it must have been audited.
			var landed bool
			got, gErr := svc.GetUser(ctx, u.ID)
			if gErr != nil {
				t.Fatalf("GetUser: %v", gErr)
			}
			switch tc.want {
			case "update":
				landed = got.Email == "new@example.com"
			case "enable":
				landed = got.CanAuthenticate()
			case "disable":
				landed = !got.CanAuthenticate()
			}
			// A hard precondition, not a Skip: committedThenFailedKV applies the
			// write before reporting the error, so "did not land" means the
			// double stopped modelling the case and this test would otherwise
			// pass by not running.
			if !landed {
				t.Fatalf("precondition: the commit should have landed, so there is something to audit")
			}

			after := al.snapshot()
			if len(after) == before {
				t.Fatalf("%s committed but emitted no audit event — a security-relevant gap", tc.want)
			}
		})
	}
}

// A stale updater must not evict the handle a third account now legitimately
// owns — even though that updater is about to lose its own commit anyway, so
// the eviction is pure collateral damage.
//
// Sequence, forced without any race: A owns X; A moves off X and commits; B
// claims the now-free X; a STALE view of A (still believing it owns X) runs an
// update, releases "its" handle, and loses the CAS.
func TestUpdateUser_AStaleUpdaterDoesNotEvictTheNewOwner(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	a := createWithEmail(t, svc, "a", "shared@example.com", "acme")
	base, gErr := kv.Get(ctx, primaryKey(a.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}

	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: a.ID, Email: "moved@example.com"}); uErr != nil {
		t.Fatalf("A moves off: %v", uErr)
	}
	b := createWithEmail(t, svc, "b", "shared@example.com", "acme")

	// The stale updater: reads A as it was, so it thinks it still owns the key.
	stale := &staleReadKV{memKVStore: kv, key: primaryKey(a.ID), stale: base}
	stalesvc, sErr := NewStore(stale, newTestIDGen(t))
	if sErr != nil {
		t.Fatalf("NewStore: %v", sErr)
	}
	_, lErr := stalesvc.UpdateUser(ctx, &UpdateUserRequest{ID: a.ID, Email: "another@example.com"})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	// B must still own the contested address.
	got, rErr := svc.GetUserByEmail(ctx, "acme", "shared@example.com")
	if rErr != nil {
		t.Fatalf("B's login key was evicted by a stale updater: %v", rErr)
	}
	if got.ID != b.ID {
		t.Fatalf("contested key resolves to %d, want B (%d)", got.ID, b.ID)
	}
}

// A certificate thumbprint is an authentication credential, so two users must
// not be able to bind the same one. AddCertBinding's conflict check is a Get,
// which is not atomic; the index write must claim.
func TestUpdateUser_RefusesACertThumbprintAnotherUserOwns(t *testing.T) {
	ctx := context.Background()
	svc := newTestStore(t)

	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	// The system tenant, because UserByCertThumbprint resolves the tenant from
	// the context and a background context names the system tenant — using
	// "acme" here would fail the final lookup for a reason unrelated to the
	// property under test.
	owner := createWithEmail(t, svc, "owner", "owner@example.com", "system")
	if _, err := svc.AddCertBinding(ctx, owner.ID, thumb, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	// A direct UpdateUser must not be able to take it.
	other := createWithEmail(t, svc, "other", "other@example.com", "system")
	_, err := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:           other.ID,
		CertBindings: []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}},
	})
	var conflict *CertBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want CertBindingConflictError, got %T: %v", err, err)
	}

	// And the thumbprint still resolves to its owner.
	got, rErr := svc.UserByCertThumbprint(ctx, thumb)
	if rErr != nil {
		t.Fatalf("owner lost the binding: %v", rErr)
	}
	if got.ID != owner.ID {
		t.Fatalf("thumbprint resolves to %d, want the owner (%d)", got.ID, owner.ID)
	}
}

// A FAILED cert update must not remove a binding it was not even changing.
//
// Releasing every old key and re-claiming every requested key looks symmetrical
// and is not: a RETAINED binding has the same key on both sides, so it was
// released and then claimed, and the rollback then restored-and-deleted it. The
// result was that a failed no-op or purely additive cert update silently
// removed a still-valid mTLS credential from the index while the record still
// listed it (Codex, PR #191).
func TestUpdateUser_AFailedCertUpdateKeepsRetainedBindings(t *testing.T) {
	ctx := context.Background()
	kv := &faultKV{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const kept = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	const added = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8"

	u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "op@example.com", TenantID: "system",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}
	if _, aErr := svc.AddCertBinding(ctx, u.ID, kept, 0); aErr != nil {
		t.Fatalf("AddCertBinding: %v", aErr)
	}

	// An ADDITIVE update that fails at the commit: the kept binding is not
	// being changed at all.
	kv.failPutPrefix = keyPrefixPrimary
	kv.err = errPrimaryWriteFault

	_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID: u.ID,
		CertBindings: []CertBinding{
			{Thumbprint: kept, Kind: CertBindingSPKIThumbprint},
			{Thumbprint: added, Kind: CertBindingSPKIThumbprint},
		},
	})
	if uErr == nil {
		t.Fatal("expected the update to fail")
	}

	kv.failPutPrefix = ""

	// Assert on the SCOPED index entry directly.
	//
	// UserByCertThumbprint falls back to the legacy un-scoped key, and the
	// rollback restores that one — so the lookup still answers even when the
	// scoped entry has been wrongly deleted. Checking through the lookup made
	// this test pass against the very code it exists to reject; the fallback was
	// masking the defect.
	scoped := certThumbprintIndexKey("system", kept)
	raw, sErr := kv.memKVStore.Get(ctx, scoped)
	if sErr != nil {
		t.Fatalf("read scoped index: %v", sErr)
	}
	if raw == nil {
		t.Fatal("a failed update deleted the scoped index entry for an untouched mTLS credential")
	}
	boundID, dErr := decodeID(raw)
	if dErr != nil {
		t.Fatalf("decode scoped index: %v", dErr)
	}
	if boundID != u.ID {
		t.Fatalf("scoped entry names %d, want %d", boundID, u.ID)
	}

	// And the one that was never committed must not be left claimed.
	other, oErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "other", Email: "other@example.com", TenantID: "system",
	})
	if oErr != nil {
		t.Fatalf("create other: %v", oErr)
	}
	if _, bErr := svc.AddCertBinding(ctx, other.ID, added, 0); bErr != nil {
		t.Fatalf("uncommitted thumbprint left reserved: %v", bErr)
	}
}

// A LOSING update must not delete a WINNING one's committed certificate index.
//
// Two updates adding the same binding to the same user both end up holding the
// key: the second is told "taken, and it is yours". Registering a release for it
// meant the loser's compensation CompareAndDeleted successfully — both use the
// same id bytes — leaving the winner's record carrying a binding with no index
// entry, so a freshly committed mTLS credential could not authenticate
// (Codex, PR #191).
//
// Compensation may only undo what its own invocation actually did.
func TestUpdateUser_ALoserDoesNotDeleteTheWinnersCertIndex(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "op@example.com", TenantID: "system",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}

	// Both callers read the same version.
	base, gErr := kv.Get(ctx, primaryKey(u.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}

	binding := []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}}

	// The winner commits first.
	if _, wErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, CertBindings: binding}); wErr != nil {
		t.Fatalf("winning update: %v", wErr)
	}

	// The loser replays the stale version and adds the SAME binding, so its
	// claim finds the key already present and already its own.
	stale := &staleReadKV{memKVStore: kv, key: primaryKey(u.ID), stale: base}
	loser, nErr := NewStore(stale, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	_, lErr := loser.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, CertBindings: binding})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	// The winner's credential must still be resolvable — assert on the scoped
	// entry, since UserByCertThumbprint has a legacy fallback that can answer
	// for it.
	scoped := certThumbprintIndexKey("system", thumb)
	raw, sErr := kv.Get(ctx, scoped)
	if sErr != nil {
		t.Fatalf("read scoped index: %v", sErr)
	}
	if raw == nil {
		t.Fatal("the loser's compensation deleted the winner's committed cert index entry")
	}
	boundID, dErr := decodeID(raw)
	if dErr != nil {
		t.Fatalf("decode: %v", dErr)
	}
	if boundID != u.ID {
		t.Fatalf("scoped entry names %d, want %d", boundID, u.ID)
	}
}

// A losing update must not delete a handle the WINNER still relies on.
//
// "We lost the race" does not imply "the winner retired the same handles we
// did". A changes the email and releases the old one; B changes only the display
// name and wins. B's committed record still carries the OLD email — so skipping
// the restore leaves an enabled account that GetUserByEmail cannot resolve. A
// login-availability defect inside ordinary concurrent use (Codex, PR #191).
func TestUpdateUser_ALostHandleChangeRestoresWhatTheWinnerKept(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "keep@example.com", "acme")

	// Both callers read this version.
	base, gErr := kv.Get(ctx, primaryKey(u.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}

	// B wins, changing ONLY the display name — so it keeps the old email.
	if _, wErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "winner"}); wErr != nil {
		t.Fatalf("winning update: %v", wErr)
	}

	// A replays the stale read, moves the email (releasing the old key), and
	// loses the commit.
	stale := &staleReadKV{memKVStore: kv, key: primaryKey(u.ID), stale: base}
	loser, nErr := NewStore(stale, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	_, lErr := loser.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "moved@example.com"})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	// The winner's record still says keep@example.com, so that must resolve.
	got, rErr := svc.GetUserByEmail(ctx, "acme", "keep@example.com")
	if rErr != nil {
		t.Fatalf("a losing update stranded the winner's login key: %v", rErr)
	}
	if got.ID != u.ID {
		t.Fatalf("login key resolves to %d, want %d", got.ID, u.ID)
	}
	if got.Name != "winner" {
		t.Fatalf("winner's change was lost: name is %q", got.Name)
	}

	// And the address the loser never committed must not be left claimed.
	if _, e := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "someone", Email: "moved@example.com", TenantID: "acme",
	}); e != nil {
		t.Fatalf("uncommitted email left reserved by a lost update: %v", e)
	}
}

// A delete must not leave behind an index entry a concurrent update claimed
// after the delete read its snapshot.
//
// Sequence: DeleteUser reads user U (email = old). UpdateUser then commits
// email = new, claiming that entry. The delete, working from its stale
// snapshot, removes only the OLD handle and then the record — leaving
// new@example.com resolving to a user that no longer exists, and reserved until
// somebody repairs it by hand (Codex, PR #191).
func TestDeleteUser_DoesNotStrandAHandleClaimedAfterItsRead(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "old@example.com", "acme")

	// The version the delete will believe in.
	base, gErr := kv.Get(ctx, primaryKey(u.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}

	// A concurrent update commits a NEW email and claims its index entry.
	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "new@example.com"}); uErr != nil {
		t.Fatalf("update: %v", uErr)
	}

	// The delete runs from the stale snapshot.
	stale := &staleReadKV{memKVStore: kv, key: primaryKey(u.ID), stale: base}
	staleSvc, nErr := NewStore(stale, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	dErr := staleSvc.DeleteUser(ctx, u.ID)

	var lost *ConcurrentUpdateError
	if !errors.As(dErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", dErr, dErr)
	}

	// The record must still exist, and its committed login key must resolve.
	got, rErr := svc.GetUserByEmail(ctx, "acme", "new@example.com")
	if rErr != nil {
		t.Fatalf("committed login key no longer resolves: %v", rErr)
	}
	if got.ID != u.ID {
		t.Fatalf("resolves to %d, want %d", got.ID, u.ID)
	}
	if _, e := svc.GetUser(ctx, u.ID); e != nil {
		t.Fatalf("a losing delete removed the record: %v", e)
	}
}

// A lost delete must leave the surviving record fully reachable — including by
// tenant listing, which is not a unique handle and so was not tracked as one.
func TestDeleteUser_ALostDeleteLeavesTheRecordListable(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	base, gErr := kv.Get(ctx, primaryKey(u.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}

	// A concurrent update wins.
	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "winner"}); uErr != nil {
		t.Fatalf("update: %v", uErr)
	}

	stale := &staleReadKV{memKVStore: kv, key: primaryKey(u.ID), stale: base}
	staleSvc, nErr := NewStore(stale, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	var lost *ConcurrentUpdateError
	if dErr := staleSvc.DeleteUser(ctx, u.ID); !errors.As(dErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", dErr, dErr)
	}

	// Reachable by listing...
	page, lErr := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if lErr != nil {
		t.Fatalf("ListUsers: %v", lErr)
	}
	found := false
	for _, x := range page.Users {
		if x.ID == u.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("a lost delete dropped a surviving record from its tenant index")
	}

	// ...and by its login key.
	if _, e := svc.GetUserByEmail(ctx, "acme", "op@example.com"); e != nil {
		t.Fatalf("a lost delete stranded the login key: %v", e)
	}
}

// deleteCommittedThenFailedKV removes the record and THEN reports an error, so
// the delete's outcome is genuinely ambiguous.
type deleteCommittedThenFailedKV struct {
	*memKVStore
	armPrefix string
	err       error
	fired     bool
}

func (d *deleteCommittedThenFailedKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if !d.fired && d.armPrefix != "" && strings.HasPrefix(key, d.armPrefix) {
		d.fired = true
		_, _ = d.memKVStore.CompareAndDelete(ctx, key, expected)
		return false, d.err
	}
	return d.memKVStore.CompareAndDelete(ctx, key, expected)
}

// An ambiguous delete that actually LANDED must not be reported as a failure
// that restores indexes for a record which no longer exists.
func TestDeleteUser_AnAmbiguousDeleteThatLandedIsNotUndone(t *testing.T) {
	ctx := context.Background()
	kv := &deleteCommittedThenFailedKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	kv.armPrefix = keyPrefixPrimary

	if dErr := svc.DeleteUser(ctx, u.ID); dErr != nil {
		var unknown *CommitOutcomeUnknownError
		if !errors.As(dErr, &unknown) {
			t.Fatalf("unexpected error %T: %v", dErr, dErr)
		}
	}

	// The record is gone, so its handles must be free rather than restored.
	if _, e := svc.GetUser(ctx, u.ID); e == nil {
		t.Fatal("record still present after a delete that landed")
	}
	if _, e := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "successor", Email: "op@example.com", TenantID: "acme",
	}); e != nil {
		t.Fatalf("handles restored for a record that no longer exists: %v", e)
	}
}

// failingDeleteKV reports an error from CompareAndDelete WITHOUT deleting, so
// the outcome is ambiguous and the record in fact survives.
type failingDeleteKV struct {
	*memKVStore
	armPrefix string
	err       error
	fired     bool
}

func (f *failingDeleteKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if !f.fired && f.armPrefix != "" && strings.HasPrefix(key, f.armPrefix) {
		f.fired = true
		return false, f.err // the record is NOT removed
	}
	return f.memKVStore.CompareAndDelete(ctx, key, expected)
}

// An ambiguous delete that did NOT land must leave the surviving record
// reachable.
//
// This is the discriminating half. The companion test above — where the delete
// DID land — behaves identically with or without the read-back, so it documents
// intent rather than guarding it; only this case fails when the read-back is
// removed. By the time CompareAndDelete is reached every index is already gone,
// so returning "unknown" without looking leaves a live record with no tenant,
// username, email or cert entry: present, and reachable by nothing.
func TestDeleteUser_AnAmbiguousDeleteThatDidNotLandRestoresTheIndexes(t *testing.T) {
	ctx := context.Background()
	kv := &failingDeleteKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	kv.armPrefix = keyPrefixPrimary

	if dErr := svc.DeleteUser(ctx, u.ID); dErr == nil {
		t.Fatal("expected the delete to report a failure")
	}

	// The record survives, so every way of reaching it must survive too.
	if _, e := svc.GetUser(ctx, u.ID); e != nil {
		t.Fatalf("record vanished though the delete did not land: %v", e)
	}
	if _, e := svc.GetUserByEmail(ctx, "acme", "op@example.com"); e != nil {
		t.Fatalf("a failed delete stranded the login key of a live record: %v", e)
	}
	page, lErr := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
	if lErr != nil {
		t.Fatalf("ListUsers: %v", lErr)
	}
	found := false
	for _, x := range page.Users {
		if x.ID == u.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("a failed delete dropped a live record from its tenant index")
	}
}

// failReleaseKV fails the guarded release of ONE exact key, modelling an
// ordinary backend error partway through a delete.
type failReleaseKV struct {
	*memKVStore
	failKey string
	err     error
}

func (f *failReleaseKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.failKey != "" && key == f.failKey {
		return false, f.err
	}
	return f.memKVStore.CompareAndDelete(ctx, key, expected)
}

// A delete that fails PARTWAY THROUGH must leave the surviving record reachable
// by every index it still has.
//
// Compensation used to run only after the conditional primary delete, so an
// error on the username, email or certificate release returned immediately with
// the tenant index already gone — a live record, unlistable (Codex, PR #191).
func TestDeleteUser_APartialFailureLeavesTheRecordReachable(t *testing.T) {
	errRelease := errors.New("release fault")

	for _, tc := range []struct {
		name string
		key  func(id uint64) string
	}{
		{"username release", func(uint64) string { return usernameIndexKey("acme", "op") }},
		{"email release", func(uint64) string { return emailIndexKey("acme", "op@example.com") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			kv := &failReleaseKV{memKVStore: newMemKVStore(), err: errRelease}
			svc, err := NewStore(kv, newTestIDGen(t))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			u := createWithEmail(t, svc, "op", "op@example.com", "acme")
			kv.failKey = tc.key(u.ID)

			if dErr := svc.DeleteUser(ctx, u.ID); dErr == nil {
				t.Fatal("expected the delete to fail")
			}
			kv.failKey = ""

			// The record survives, so every retained index must too.
			if _, e := svc.GetUser(ctx, u.ID); e != nil {
				t.Fatalf("record vanished on a failed delete: %v", e)
			}
			page, lErr := svc.ListUsers(ctx, &ListUsersRequest{TenantID: "acme"})
			if lErr != nil {
				t.Fatalf("ListUsers: %v", lErr)
			}
			found := false
			for _, x := range page.Users {
				if x.ID == u.ID {
					found = true
				}
			}
			if !found {
				t.Fatal("a partially failed delete dropped a live record from its tenant index")
			}
		})
	}
}

// An update that loses to a concurrent DELETE must not restore login indexes
// for the record that was deleted.
//
// "Could not read the winner" was previously treated as "restore everything",
// on the reasoning that a stale index entry is more visible than a missing one.
// That holds when the record survives and is false when it does not — and
// not-found is exactly the reply a concurrent delete produces. The restored
// entries then resolve to a primary record that no longer exists
// (Codex, PR #191).
func TestUpdateUser_LosingToADeleteRestoresNothing(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	base, gErr := kv.Get(ctx, primaryKey(u.ID))
	if gErr != nil {
		t.Fatalf("read base: %v", gErr)
	}

	// The winner deletes the user outright.
	if dErr := svc.DeleteUser(ctx, u.ID); dErr != nil {
		t.Fatalf("delete: %v", dErr)
	}

	// The loser replays the stale read and tries to move the email.
	stale := &staleReadKV{memKVStore: kv, key: primaryKey(u.ID), stale: base}
	loser, nErr := NewStore(stale, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	if _, lErr := loser.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "moved@example.com"}); lErr == nil {
		t.Fatal("expected the update to fail")
	}

	// No index may resolve to the deleted record.
	if _, e := svc.GetUserByEmail(ctx, "acme", "op@example.com"); e == nil {
		t.Fatal("a lost update restored the login key of a DELETED user")
	}
	if _, e := svc.GetUserByUsername(ctx, "acme", "op"); e == nil {
		t.Fatal("a lost update restored the username of a DELETED user")
	}
	// And the address is free for somebody else.
	if _, e := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "successor", Email: "op@example.com", TenantID: "acme",
	}); e != nil {
		t.Fatalf("address left reserved by a deleted user: %v", e)
	}
}

// unknowableDeleteKV commits the delete, reports an error, and then makes the
// read-back fail too — so the outcome is genuinely undecidable rather than
// merely reported as failed.
type unknowableDeleteKV struct {
	*memKVStore
	armPrefix string
	err       error
	fired     bool
}

func (u *unknowableDeleteKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if !u.fired && u.armPrefix != "" && strings.HasPrefix(key, u.armPrefix) {
		u.fired = true
		_, _ = u.memKVStore.CompareAndDelete(ctx, key, expected) // it LANDS
		return false, u.err
	}
	return u.memKVStore.CompareAndDelete(ctx, key, expected)
}

func (u *unknowableDeleteKV) Get(ctx context.Context, key string) ([]byte, error) {
	if u.fired && strings.HasPrefix(key, keyPrefixPrimary) {
		return nil, u.err // the read-back is inconclusive too
	}
	return u.memKVStore.Get(ctx, key)
}

// A delete whose outcome is UNKNOWN must not touch the indexes: if it in fact
// committed, restoring them leaves entries resolving to a record that is gone.
func TestDeleteUser_AnUnknownOutcomeLeavesIndexesAlone(t *testing.T) {
	ctx := context.Background()
	kv := &unknowableDeleteKV{memKVStore: newMemKVStore(), err: errors.New("reply lost")}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "op@example.com", "acme")
	kv.armPrefix = keyPrefixPrimary

	dErr := svc.DeleteUser(ctx, u.ID)
	var unknown *CommitOutcomeUnknownError
	if !errors.As(dErr, &unknown) {
		t.Fatalf("want CommitOutcomeUnknownError, got %T: %v", dErr, dErr)
	}

	// The delete actually landed, so the handles must NOT have been restored.
	if raw, _ := kv.memKVStore.Get(ctx, emailIndexKey("acme", "op@example.com")); raw != nil {
		t.Fatal("an unknown-outcome delete restored the login key of a record that was in fact removed")
	}
	if raw, _ := kv.memKVStore.Get(ctx, usernameIndexKey("acme", "op")); raw != nil {
		t.Fatal("an unknown-outcome delete restored the username of a record that was in fact removed")
	}
}

// A STALE index entry must not authenticate a retired address.
//
// The index is a hint; the record is the authority. An entry left behind by a
// partial mutation, an unresolved compensation or a repair gap would otherwise
// let a retired email resolve to its former owner — for the canonical login key,
// that is the whole game. The certificate lookup has always confirmed its
// binding after resolving the index; this is the same defence (Codex, PR #191).
func TestGetUserByEmail_RefusesAStaleIndexEntry(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "op", "current@example.com", "acme")

	// Forge the state a partial mutation would leave: an index entry for an
	// address the record does not carry.
	if pErr := kv.Put(ctx, emailIndexKey("acme", "retired@example.com"), encodeID(u.ID)); pErr != nil {
		t.Fatalf("seed stale entry: %v", pErr)
	}

	if got, e := svc.GetUserByEmail(ctx, "acme", "retired@example.com"); e == nil {
		t.Fatalf("a retired address resolved to user %d — a stale index entry authenticated", got.ID)
	}

	// The address the record actually carries still works.
	got, gErr := svc.GetUserByEmail(ctx, "acme", "current@example.com")
	if gErr != nil {
		t.Fatalf("the live login key stopped resolving: %v", gErr)
	}
	if got.ID != u.ID {
		t.Fatalf("resolved to %d, want %d", got.ID, u.ID)
	}
}

// The username path carries the same confirmation, for the same reason.
func TestGetUserByUsername_RefusesAStaleIndexEntry(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	u := createWithEmail(t, svc, "current-name", "op@example.com", "acme")

	if pErr := kv.Put(ctx, usernameIndexKey("acme", "retired-name"), encodeID(u.ID)); pErr != nil {
		t.Fatalf("seed stale entry: %v", pErr)
	}

	if got, e := svc.GetUserByUsername(ctx, "acme", "retired-name"); e == nil {
		t.Fatalf("a retired username resolved to user %d", got.ID)
	}
	if _, e := svc.GetUserByUsername(ctx, "acme", "current-name"); e != nil {
		t.Fatalf("the live username stopped resolving: %v", e)
	}
}

// Two updates racing the SAME user to the SAME new handle must not report a
// duplicate. The loser finds the key already held by its own id — that is not
// "somebody else owns this", it is a concurrent self-update, and the caller
// would otherwise get a permanent duplicate error for a value its own account
// holds. The cert path in the same function already worked this way
// (Codex, PR #191).
func TestUpdateUser_ASelfOwnedHandleIsNotADuplicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  func(id uint64) *UpdateUserRequest
	}{
		{"email", func(id uint64) *UpdateUserRequest {
			return &UpdateUserRequest{ID: id, Email: "contested@example.com"}
		}},
		{"username", func(id uint64) *UpdateUserRequest {
			return &UpdateUserRequest{ID: id, Username: "contested"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			kv := newMemKVStore()
			svc, err := NewStore(kv, newTestIDGen(t))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}

			u := createWithEmail(t, svc, "op", "op@example.com", "acme")
			base, gErr := kv.Get(ctx, primaryKey(u.ID))
			if gErr != nil {
				t.Fatalf("read base: %v", gErr)
			}

			// The winner takes the new handle.
			if _, wErr := svc.UpdateUser(ctx, tc.req(u.ID)); wErr != nil {
				t.Fatalf("winning update: %v", wErr)
			}

			// The loser replays the stale read and asks for the SAME handle.
			stale := &staleReadKV{memKVStore: kv, key: primaryKey(u.ID), stale: base}
			loser, nErr := NewStore(stale, newTestIDGen(t))
			if nErr != nil {
				t.Fatalf("NewStore: %v", nErr)
			}
			_, lErr := loser.UpdateUser(ctx, tc.req(u.ID))

			// It must be a lost race, never a duplicate-handle error.
			var dupEmail *EmailExistsError
			var dupName *UsernameExistsError
			if errors.As(lErr, &dupEmail) || errors.As(lErr, &dupName) {
				t.Fatalf("a self-owned handle was reported as a duplicate: %v", lErr)
			}
			var lost *ConcurrentUpdateError
			if !errors.As(lErr, &lost) {
				t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
			}

			// And the winner's handle still resolves to the user.
			if tc.name == "email" {
				got, e := svc.GetUserByEmail(ctx, "acme", "contested@example.com")
				if e != nil || got.ID != u.ID {
					t.Fatalf("winner's email lost: got %v err %v", got, e)
				}
			} else {
				got, e := svc.GetUserByUsername(ctx, "acme", "contested")
				if e != nil || got.ID != u.ID {
					t.Fatalf("winner's username lost: got %v err %v", got, e)
				}
			}
		})
	}
}

// failReadKV fails Get for one exact key, modelling a transient backend read
// failure while resolving who owns a contended handle.
type failReadKV struct {
	*memKVStore
	failKey string
	err     error
}

func (f *failReadKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failKey != "" && key == f.failKey {
		return nil, f.err
	}
	return f.memKVStore.Get(ctx, key)
}

// A store failure while reading a contended handle's owner must be reported as
// a store failure, not as a duplicate.
//
// The ownership read discarded its errors, so a transient read fault or a
// malformed value became UsernameExistsError / EmailExistsError — telling the
// caller to choose another name when the truth was that the store could not
// answer (Codex, PR #191).
func TestUpdateUser_AFailedOwnerReadIsNotADuplicate(t *testing.T) {
	ctx := context.Background()
	errRead := errors.New("read fault")

	kv := &failReadKV{memKVStore: newMemKVStore(), err: errRead}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Somebody else already holds the address.
	other := createWithEmail(t, svc, "other", "contested@example.com", "acme")
	_ = other
	u := createWithEmail(t, svc, "op", "op@example.com", "acme")

	// The ownership read for the contested key now fails.
	kv.failKey = emailIndexKey("acme", "contested@example.com")

	_, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Email: "contested@example.com"})
	if uErr == nil {
		t.Fatal("expected the update to fail")
	}

	var dup *EmailExistsError
	if errors.As(uErr, &dup) {
		t.Fatalf("a store read failure was reported as a duplicate: %v", uErr)
	}
	var se *StoreError
	if !errors.As(uErr, &se) {
		t.Fatalf("want StoreError, got %T: %v", uErr, uErr)
	}
	if !errors.Is(uErr, errRead) {
		t.Fatalf("the underlying read fault is not reachable: %v", uErr)
	}
}

// A binding reachable ONLY through the legacy un-scoped key must survive an
// update that keeps it.
//
// The update clears the legacy key for every existing binding, and the set-diff
// added in an earlier round made retained bindings skip the scoped claim — so a
// legacy-only user lost its single lookup entry on an update that changed
// something else entirely, locking it out of mTLS while the record still listed
// the certificate (Codex, PR #191).
func TestUpdateUser_ALegacyOnlyBindingSurvivesARetainingUpdate(t *testing.T) {
	ctx := context.Background()
	kv := newMemKVStore()
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "op@example.com", TenantID: "system",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}
	if _, aErr := svc.AddCertBinding(ctx, u.ID, thumb, 0); aErr != nil {
		t.Fatalf("AddCertBinding: %v", aErr)
	}

	// Forge the pre-migration shape: the binding is reachable ONLY through the
	// legacy un-scoped key.
	if dErr := kv.Delete(ctx, certThumbprintIndexKey("system", thumb)); dErr != nil {
		t.Fatalf("drop scoped key: %v", dErr)
	}
	if pErr := kv.Put(ctx, legacyCertThumbprintIndexKey(thumb), encodeID(u.ID)); pErr != nil {
		t.Fatalf("seed legacy key: %v", pErr)
	}
	if _, e := svc.UserByCertThumbprint(ctx, thumb); e != nil {
		t.Fatalf("precondition: the legacy-only binding should resolve: %v", e)
	}

	// An update that KEEPS the binding and changes something else.
	current, gErr := svc.GetUser(ctx, u.ID)
	if gErr != nil {
		t.Fatalf("GetUser: %v", gErr)
	}
	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID: u.ID, Name: "renamed", CertBindings: current.CertBindings,
	}); uErr != nil {
		t.Fatalf("UpdateUser: %v", uErr)
	}

	// The certificate must still authenticate.
	got, rErr := svc.UserByCertThumbprint(ctx, thumb)
	if rErr != nil {
		t.Fatalf("a retaining update orphaned a legacy-only cert binding: %v", rErr)
	}
	if got.ID != u.ID {
		t.Fatalf("thumbprint resolves to %d, want %d", got.ID, u.ID)
	}
	// And it should now be reachable through the SCOPED key: the update
	// migrated it rather than merely leaving the legacy entry in place.
	if raw, _ := kv.Get(ctx, certThumbprintIndexKey("system", thumb)); raw == nil {
		t.Fatal("the binding was not migrated to the tenant-scoped key")
	}
}

// A FAILED update must not resurrect a legacy index entry that did not exist.
//
// After migration the legacy un-scoped key is normally absent. Recording it as
// "released" regardless of whether anything was deleted meant a failed update's
// rollback CREATED one — reversing the migration and leaving persistent lookup
// state the operation never touched (Codex, PR #191).
func TestUpdateUser_AFailedUpdateDoesNotResurrectALegacyIndex(t *testing.T) {
	ctx := context.Background()
	kv := &faultKV{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	u, cErr := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "op@example.com", TenantID: "system",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}
	if _, aErr := svc.AddCertBinding(ctx, u.ID, thumb, 0); aErr != nil {
		t.Fatalf("AddCertBinding: %v", aErr)
	}

	// The migrated shape: scoped entry present, legacy entry absent.
	legacy := legacyCertThumbprintIndexKey(thumb)
	if raw, _ := kv.memKVStore.Get(ctx, legacy); raw != nil {
		t.Fatalf("precondition: the legacy key should be absent, got %v", raw)
	}

	// An update that keeps the binding and then fails at the commit.
	current, gErr := svc.GetUser(ctx, u.ID)
	if gErr != nil {
		t.Fatalf("GetUser: %v", gErr)
	}
	kv.failPutPrefix = keyPrefixPrimary
	kv.err = errPrimaryWriteFault

	if _, uErr := svc.UpdateUser(ctx, &UpdateUserRequest{
		ID: u.ID, Name: "renamed", CertBindings: current.CertBindings,
	}); uErr == nil {
		t.Fatal("expected the update to fail")
	}
	kv.failPutPrefix = ""

	// The rollback must not have invented a legacy entry.
	if raw, _ := kv.memKVStore.Get(ctx, legacy); raw != nil {
		t.Fatal("a failed update resurrected a legacy cert index entry that never existed")
	}
	// And the scoped entry, which the call did not remove, is still there.
	if raw, _ := kv.memKVStore.Get(ctx, certThumbprintIndexKey("system", thumb)); raw == nil {
		t.Fatal("the scoped cert index entry was lost")
	}
}

var errUnreleasableClaim = errors.New("release fault")

// A create that cannot give back a handle it claimed must say so.
//
// The finding, exactly: CreateUser claims the username and email keys with
// PutIfAbsent before the primary record is written, and the deferred rollback
// discarded every CompareAndDelete result. So a create that failed after
// claiming, and whose release also failed, returned a plain "put primary
// failed" — while the email index kept pointing at a user id that was never
// written. The address is then permanently unusable: a later create sees the
// key taken and reports a duplicate, and every lookup through it resolves to
// nothing. Nothing reconciles that, and the caller was never told.
//
// UpdateUser and DeleteUser already wrapped this state in an
// IndexCompensationError. CreateUser is the third path with the same failure
// mode and did not get the treatment.
func TestCreateUser_AClaimThatCouldNotBeReleasedIsReported(t *testing.T) {
	ctx := context.Background()
	kv := &faultKV{
		memKVStore:    newMemKVStore(),
		failPutPrefix: keyPrefixPrimary,
		err:           errPrimaryWriteFault,
	}
	svc, sErr := NewStore(kv, newTestIDGen(t))
	if sErr != nil {
		t.Fatalf("NewStore: %v", sErr)
	}

	const addr = "stranded@example.com"
	emailKey := emailIndexKey(tenantid.System, addr)
	kv.failReleaseKey = emailKey
	kv.err = errUnreleasableClaim

	_, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "stranded", Email: addr})
	if err == nil {
		t.Fatal("create succeeded although the primary write was faulted")
	}

	var comp *IndexCompensationError
	if !errors.As(err, &comp) {
		t.Fatalf("a create that stranded a login-handle claim reported an ordinary "+
			"failure, so no caller can tell the address is now unusable: %v", err)
	}
	if len(comp.Failures) == 0 {
		t.Fatal("IndexCompensationError carried no cleanup failures")
	}

	// The original cause must still match: callers keyed on the underlying
	// store failure keep working (IndexCompensationError.Unwrap).
	if !errors.Is(err, errUnreleasableClaim) {
		t.Fatalf("the underlying create failure no longer matches: %v", err)
	}

	// The state the error is reporting is real, not hypothetical.
	if raw, gErr := kv.Get(ctx, emailKey); gErr != nil || len(raw) == 0 {
		t.Fatal("expected the stranded email claim to still be present; " +
			"if it is gone this test is asserting on a state that did not occur")
	}
}

// The companion: when the release SUCCEEDS, an ordinary failure must stay
// ordinary. Without this, wrapping every failed create in an
// IndexCompensationError would pass the test above while telling every caller
// that a cleanly rolled-back create had corrupted the index.
func TestCreateUser_ACleanRollbackDoesNotReportCompensationFailure(t *testing.T) {
	ctx := context.Background()
	kv := &faultKV{
		memKVStore:    newMemKVStore(),
		failPutPrefix: keyPrefixPrimary,
		err:           errPrimaryWriteFault,
	}
	svc, sErr := NewStore(kv, newTestIDGen(t))
	if sErr != nil {
		t.Fatalf("NewStore: %v", sErr)
	}

	const addr = "clean@example.com"
	_, err := svc.CreateUser(ctx, &CreateUserRequest{Username: "clean", Email: addr})
	if err == nil {
		t.Fatal("create succeeded although the primary write was faulted")
	}

	var comp *IndexCompensationError
	if errors.As(err, &comp) {
		t.Fatalf("a create whose rollback fully succeeded was reported as index "+
			"corruption: %v", err)
	}

	// And the handle really is back: the address must be usable again.
	emailKey := emailIndexKey(tenantid.System, addr)
	if raw, gErr := kv.Get(ctx, emailKey); gErr != nil || len(raw) != 0 {
		t.Fatal("a cleanly rolled-back create left its email claim behind")
	}
}

// atSwapKV commits "the winner" at the instant the loser attempts its
// conditional commit, then lets the real CompareAndSwap run — which now fails
// because the value moved. That is a genuine lost update at a chosen point in
// the loser's execution, which staleReadKV cannot express: it serialises the
// winner BEFORE the loser starts, so the loser's releases all happen after the
// winner is done. The interleaving that matters here is the other one — the
// loser retires the legacy key first, and the winner commits before the loser's
// compensation runs.
type atSwapKV struct {
	*memKVStore
	primary string
	onSwap  func()
	fired   bool
}

func (a *atSwapKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if !a.fired && key == a.primary {
		a.fired = true
		if a.onSwap != nil {
			a.onSwap()
		}
	}
	return a.memKVStore.CompareAndSwap(ctx, key, expected, next)
}

// seedCertUser creates a user carrying one cert binding and returns it.
func seedCertUser(t *testing.T, svc UserServicer, kv *memKVStore, thumb string) *User {
	t.Helper()
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "op@example.com", TenantID: "system",
	})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	if _, err = svc.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		CertBindings: []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}},
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	return u
}

// A lost update must not put back a legacy cert index the winner migrated off.
//
// The legacy un-scoped key is read only as a FALLBACK, when the scoped key
// resolves to nothing. handlesTheWinnerStillNeeds marked it needed for every
// binding on the winner's record, so a loser that had retired the legacy entry
// recreated it — reinstating the global thumbprint entry the migration exists to
// remove, and restoring the un-scoped lookup path for that credential.
//
// winner.CertBindings cannot answer this: it says a binding exists, not which
// key serves it. Only the committed index state can.
func TestUpdateUser_ALostUpdateDoesNotResurrectAMigratedLegacyCertIndex(t *testing.T) {
	ctx := context.Background()
	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	kv := newMemKVStore()
	seedSvc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u := seedCertUser(t, seedSvc, kv, thumb)

	// A deployment mid-migration: the scoped entry exists AND the legacy global
	// entry is still there.
	legacy := legacyCertThumbprintIndexKey(thumb)
	if pErr := kv.Put(ctx, legacy, encodeID(u.ID)); pErr != nil {
		t.Fatalf("seed legacy: %v", pErr)
	}

	loserKV := &atSwapKV{memKVStore: kv, primary: primaryKey(u.ID)}
	loser, nErr := NewStore(loserKV, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	// The winner commits an update that does NOT touch cert bindings, so it
	// leaves the scoped entry in place and never re-adds the legacy one.
	loserKV.onSwap = func() {
		if _, wErr := seedSvc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "winner"}); wErr != nil {
			t.Errorf("winning update: %v", wErr)
		}
	}

	_, lErr := loser.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		CertBindings: []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}},
	})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	// The scoped entry still covers the binding, so the legacy entry is dead
	// weight and must stay retired.
	if raw, gErr := kv.Get(ctx, legacy); gErr != nil || len(raw) != 0 {
		t.Fatal("a lost update recreated the legacy global cert index entry, " +
			"undoing the migration the winning state had completed")
	}
	scoped := certThumbprintIndexKey("system", thumb)
	if raw, gErr := kv.Get(ctx, scoped); gErr != nil || len(raw) == 0 {
		t.Fatal("the scoped cert index entry is gone; this test is asserting on " +
			"a state where the legacy entry was the only lookup path")
	}
}

// The companion, and what makes excluding the legacy key safe: a binding the
// winner still carries always keeps a scoped entry, so the credential resolves
// without the legacy fallback.
//
// Here the loser INSERTED that scoped entry while migrating a pre-migration
// record, and the winner — an update that never touched cert bindings — commits
// on top. The loser's claim is the only scoped entry in existence, so releasing
// it would leave the winner's committed record carrying an mTLS credential that
// resolves to no user at all. It is kept because the winner needs it.
func TestUpdateUser_ALostUpdateKeepsAScopedCertIndexTheWinnerRelieson(t *testing.T) {
	ctx := context.Background()
	const thumb = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	kv := newMemKVStore()
	seedSvc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u := seedCertUser(t, seedSvc, kv, thumb)

	// A pre-migration record: the binding is reachable ONLY through the legacy
	// global key.
	scoped := certThumbprintIndexKey("system", thumb)
	if dErr := kv.Delete(ctx, scoped); dErr != nil {
		t.Fatalf("seed: drop scoped: %v", dErr)
	}
	legacy := legacyCertThumbprintIndexKey(thumb)
	if pErr := kv.Put(ctx, legacy, encodeID(u.ID)); pErr != nil {
		t.Fatalf("seed legacy: %v", pErr)
	}

	loserKV := &atSwapKV{memKVStore: kv, primary: primaryKey(u.ID)}
	loser, nErr := NewStore(loserKV, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	loserKV.onSwap = func() {
		if _, wErr := seedSvc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Name: "winner"}); wErr != nil {
			t.Errorf("winning update: %v", wErr)
		}
	}

	_, lErr := loser.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		CertBindings: []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}},
	})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	// The credential must still resolve, through the scoped entry the loser
	// claimed and the winner adopted.
	got, rErr := seedSvc.UserByCertThumbprint(ctx, thumb)
	if rErr != nil {
		t.Fatalf("the winner's mTLS credential no longer resolves to any user "+
			"after a lost update retired its only index entry: %v", rErr)
	}
	if got.ID != u.ID {
		t.Fatalf("resolved to user %d, want %d", got.ID, u.ID)
	}
	if raw, gErr := kv.Get(ctx, scoped); gErr != nil || len(raw) == 0 {
		t.Fatal("the scoped entry is gone; the credential is resolving through " +
			"the legacy fallback, which is not what this test is asserting")
	}
}

// The same defect without any legacy key involved: two concurrent updates add
// the SAME cert binding to the SAME user.
//
// Both write identical id bytes, so the winner's PutIfAbsent finds the loser's
// entry already present and already its own, adopts it, and registers no claim.
// "Give back every handle THIS call reserved" then deleted an index entry the
// winner's committed record depends on — a live mTLS credential resolving to no
// user. Reproduced before the fix; it is the case that showed the two undo kinds
// are not asymmetric after all.
func TestUpdateUser_ALoserDoesNotReleaseAClaimTheWinnerAdopted(t *testing.T) {
	ctx := context.Background()
	const thumb = "BBECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

	kv := newMemKVStore()
	seedSvc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, cErr := seedSvc.CreateUser(ctx, &CreateUserRequest{
		Username: "op", Email: "op@example.com", TenantID: "system",
	})
	if cErr != nil {
		t.Fatalf("seed: %v", cErr)
	}

	binding := []CertBinding{{Thumbprint: thumb, Kind: CertBindingSPKIThumbprint}}

	loserKV := &atSwapKV{memKVStore: kv, primary: primaryKey(u.ID)}
	loser, nErr := NewStore(loserKV, newTestIDGen(t))
	if nErr != nil {
		t.Fatalf("NewStore: %v", nErr)
	}
	loserKV.onSwap = func() {
		if _, wErr := seedSvc.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, CertBindings: binding}); wErr != nil {
			t.Errorf("winning update: %v", wErr)
		}
	}

	_, lErr := loser.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, CertBindings: binding})
	var lost *ConcurrentUpdateError
	if !errors.As(lErr, &lost) {
		t.Fatalf("want ConcurrentUpdateError, got %T: %v", lErr, lErr)
	}

	got, rErr := seedSvc.UserByCertThumbprint(ctx, thumb)
	if rErr != nil {
		t.Fatalf("the winner committed this binding but its credential resolves "+
			"to no user: the loser released a claim the winner had adopted: %v", rErr)
	}
	if got.ID != u.ID {
		t.Fatalf("resolved to user %d, want %d", got.ID, u.ID)
	}
}
