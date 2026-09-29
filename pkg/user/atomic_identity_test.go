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
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// go-qrdb#555's user-side sibling: CreateUser used to claim the identity
// index and the username handle SEPARATELY from writing the primary record
// and tenant index, across up to four proposals. A leadership change
// anywhere in that sequence could leave a claimed username with no user
// record behind it, and cleanup itself needs the same leadership that was
// just lost. WithAtomicIdentity closes the window by committing the
// identity claim, the username index entry, the primary record, and the
// tenant index as ONE guarded batch — see createUserAtomic in store.go.

// newAtomicTestStore builds a user store over a guarded memKVStore wired
// with a real tenant.IdentityIndex over the SAME store — the precondition
// WithAtomicIdentity documents.
func newAtomicTestStore(t *testing.T, kv *memKVStore) UserServicer {
	t.Helper()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	svc, err := NewStore(kv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc
}

func assertUserFullyIndexed(t *testing.T, svc UserServicer, tenantID, username string, wantID uint64) {
	t.Helper()
	ctx := context.Background()

	byID, err := svc.GetUser(ctx, wantID)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", wantID, err)
	}
	if byID.Username != username {
		t.Errorf("GetUser username: got %q, want %q", byID.Username, username)
	}

	byName, err := svc.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		t.Fatalf("GetUserByUsername(%q, %q): %v", tenantID, username, err)
	}
	if byName.ID != wantID {
		t.Errorf("GetUserByUsername id: got %d, want %d", byName.ID, wantID)
	}
}

// TestCreateUser_Atomic_Success exercises the ordinary atomic path: the
// identity claim and all three writes land together, and the user is fully
// reachable by every lookup path afterward.
func TestCreateUser_Atomic_Success(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()

	got, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got.Username != "admin" {
		t.Errorf("Username: got %q, want %q", got.Username, "admin")
	}
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", got.ID)
}

// TestCreateUser_Atomic_ConcurrentCreatesConverge reproduces go-qrdb#555's
// actual failure shape for users: every node's first-boot attempt races
// CreateUser for the SAME fixed username ("admin") concurrently, each with
// its OWN freshly generated id. Exactly one must win; every other call must
// observe a clean UsernameExistsError rather than a half-written record.
func TestCreateUser_Atomic_ConcurrentCreatesConverge(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()

	const attempts = 20
	var wg sync.WaitGroup
	results := make([]error, attempts)
	users := make([]*User, attempts)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			users[i], results[i] = svc.CreateUser(ctx, &CreateUserRequest{
				Username:             "admin",
				TenantID:             tenantid.System,
				WithoutEmailLoginKey: true,
			})
		}(i)
	}
	wg.Wait()

	wins, losses := 0, 0
	var winID uint64
	for i := 0; i < attempts; i++ {
		switch {
		case results[i] == nil:
			wins++
			winID = users[i].ID
		default:
			var target *UsernameExistsError
			if !errors.As(results[i], &target) {
				t.Errorf("attempt %d: got %T, want *UsernameExistsError: %v", i, results[i], results[i])
			}
			losses++
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (losses = %d)", wins, losses)
	}
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", winID)
}

// TestCreateUser_Atomic_GuardFailureRefusesCleanly is the sequential
// version: a second create for the same username, after the first has
// already committed, is refused with the SAME UsernameExistsError CreateUser
// has always returned for a taken username.
func TestCreateUser_Atomic_GuardFailureRefusesCleanly(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()

	req := &CreateUserRequest{Username: "admin", TenantID: tenantid.System, WithoutEmailLoginKey: true}
	if _, err := svc.CreateUser(ctx, req); err != nil {
		t.Fatalf("CreateUser(first): %v", err)
	}

	_, err := svc.CreateUser(ctx, req)
	var target *UsernameExistsError
	if !errors.As(err, &target) {
		t.Fatalf("CreateUser(second): got %T, want *UsernameExistsError: %v", err, err)
	}
	if target.Username != "admin" {
		t.Errorf("Username: got %q, want %q", target.Username, "admin")
	}
}

// faultyGuardedStore wraps a memKVStore and models the same two fault
// shapes pkg/role's identically-named double does: a proposal that never
// committed (failNext) and one that committed but whose response was lost
// (ambiguousNext).
// cancelOnAmbiguous, when set, is called the instant the ambiguousNext write
// commits — modelling the realistic timing go-qrdb#555's round-3 review
// found untested: the caller's context expires AT ABOUT THE SAME MOMENT the
// response is lost (a deadline is usually WHY it was lost), not before
// CreateUser was ever called. Get checks ctx.Err() first, unlike
// memKVStore's own context-blind Get, so a context that goes done between
// the write and reconcileAmbiguousCreate's own read-back is actually
// observed by that read-back — see pkg/role's identical double for the full
// reasoning.
// staleReadsBeforeVisible, when > 0, makes Get lie — returning (nil, nil) as
// though the key were absent — that many times BEFORE ever consulting the
// real store, decrementing on every call regardless of key. See pkg/role's
// identical double for the full reasoning: it models cmd/qrdbd's boot-time
// StaleRead visibility lag.
type faultyGuardedStore struct {
	*memKVStore
	failNext                bool
	ambiguousNext           bool
	err                     error
	cancelOnAmbiguous       context.CancelFunc
	staleReadsBeforeVisible int
}

func (f *faultyGuardedStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.staleReadsBeforeVisible > 0 {
		f.staleReadsBeforeVisible--
		return nil, nil
	}
	return f.memKVStore.Get(ctx, key)
}

func (f *faultyGuardedStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	// An empty writes set is a capability PROBE (routingUserService's
	// atomicCapable), not a real create: honour it truthfully against
	// the real store so a fault meant for the create itself does not get
	// consumed by the probe that precedes it.
	if len(writes) == 0 {
		return f.memKVStore.PutAllIfAllUnchanged(ctx, guards, writes)
	}
	if f.failNext {
		f.failNext = false
		return false, f.err
	}
	if f.ambiguousNext {
		f.ambiguousNext = false
		applied, err := f.memKVStore.PutAllIfAllUnchanged(ctx, guards, writes)
		if f.cancelOnAmbiguous != nil {
			f.cancelOnAmbiguous()
		}
		if err != nil {
			return applied, err
		}
		return applied, f.err
	}
	return f.memKVStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

// TestCreateUser_Atomic_FaultLeavesNeitherClaimNorRecord is the "fault
// injected between claim and record is impossible, or leaves neither" case:
// because the claim and every write are a SINGLE PutAllIfUnchanged call, a
// fault in that call can only fail the whole proposal.
func TestCreateUser_Atomic_FaultLeavesNeitherClaimNorRecord(t *testing.T) {
	kv := newMemKVStore()
	fkv := &faultyGuardedStore{memKVStore: kv, failNext: true, err: errors.New("injected: leadership lost mid-proposal")}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	svc, err := NewStore(fkv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity(),
		// This create's write never lands (failNext), so reconciliation's
		// retry loop runs for its full window before giving up. A tiny
		// window keeps the test fast without changing what it proves.
		WithReconcilePolling(time.Millisecond, 20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err == nil {
		t.Fatal("expected the injected fault to surface as an error")
	}

	if v, _ := kv.Get(ctx, usernameIndexKey(tenantid.System, "admin")); v != nil {
		t.Error("username index key exists after a failed atomic create")
	}

	// The retry succeeds cleanly: nothing was left behind to conflict with.
	got, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(retry): %v", err)
	}
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", got.ID)
}

// TestCreateUser_Atomic_ReconcilesLostResponse covers the other half of
// reconcileAmbiguousCreate: PutAllIfUnchanged's writes commit and ONLY the
// response is lost.
func TestCreateUser_Atomic_ReconcilesLostResponse(t *testing.T) {
	kv := newMemKVStore()
	fkv := &faultyGuardedStore{memKVStore: kv, ambiguousNext: true, err: errors.New("injected: response lost after commit")}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	svc, err := NewStore(fkv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	got, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: expected the lost response to reconcile to success, got error: %v", err)
	}
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", got.ID)
}

// TestCreateUser_Atomic_ReconcilesLostResponse_ContextAlreadyDone is the
// go-qrdb#555 round-3 review's exact non-vacuousness complaint: a lost
// response is very often lost BECAUSE the caller's context ran out — a
// timeout or a cancellation is what "the response never arrived" usually
// looks like, not an active background context as
// TestCreateUser_Atomic_ReconcilesLostResponse models. Reusing ctx for the
// read-back would then fail every read immediately (this store's Get checks
// ctx.Err()) and never emit the audit event reconcileAmbiguousCreate exists
// to guarantee. CreateUser must still succeed, and the audit event must
// still be emitted, using a context independently bounded from the one that
// was already done.
func TestCreateUser_Atomic_ReconcilesLostResponse_ContextAlreadyDone(t *testing.T) {
	kv := newMemKVStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fkv := &faultyGuardedStore{
		memKVStore:        kv,
		ambiguousNext:     true,
		err:               errors.New("injected: response lost after commit"),
		cancelOnAmbiguous: cancel,
	}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	al := &fakeAuditLogger{}
	svc, err := NewStore(fkv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity(), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: expected the lost response to reconcile to success despite a canceled ctx, got error: %v", err)
	}
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", got.ID)

	if _, ok := al.findByAction("create"); !ok {
		t.Error("expected a create audit event despite the caller's context already being done")
	}
}

// TestCreateUser_Atomic_ReconcilesLostResponse_StaleReadCatchesUp is the
// go-qrdb#555 round-4 review's finding #1 regression: see pkg/role's
// identical test for the full reasoning. cmd/qrdbd's boot-time stores read
// via StaleRead, so a proposal that committed but lost its response may not
// be LOCALLY visible for a brief window while apply catches up. The
// read-back must retry within its window rather than give up on the first
// attempt.
func TestCreateUser_Atomic_ReconcilesLostResponse_StaleReadCatchesUp(t *testing.T) {
	kv := newMemKVStore()
	fkv := &faultyGuardedStore{
		memKVStore:    kv,
		ambiguousNext: true,
		err:           errors.New("injected: response lost after commit"),
		// The write commits synchronously (ambiguousNext applies it via the
		// real store before returning the injected error), but the first
		// few Get calls lie as though it were still invisible — modelling
		// StaleRead's local-apply lag. CreateUser's atomic path (unlike
		// role's) has no pre-check read before reaching reconciliation, so
		// this budget is spent entirely inside reconcileAmbiguousCreate's
		// own retries.
		staleReadsBeforeVisible: 2,
	}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	al := &fakeAuditLogger{}
	svc, err := NewStore(fkv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity(), WithAuditLogger(al),
		WithReconcilePolling(time.Millisecond, time.Second))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got, err := svc.CreateUser(context.Background(), &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: expected reconciliation to retry past the stale-read window and succeed, got error: %v", err)
	}
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", got.ID)

	if _, ok := al.findByAction("create"); !ok {
		t.Error("expected a create audit event once reconciliation caught up with the stale-read lag")
	}
}

// TestCreateUser_Atomic_EmailIsAlsoAtomic confirms an email-bearing create
// commits through the SAME guarded batch as a no-email one: the username
// index, the email index, the primary record, and the tenant index all land
// in one proposal, fenced by a MultiGuardedBatchKVStore guarding the identity
// claim, username, AND email keys together (go-qrdb#555 review: an earlier
// version of this option fell back to a non-atomic sequential path here,
// leaving exactly the leadership-change window this whole design exists to
// close, just for the email-bearing case instead of the plain one).
func TestCreateUser_Atomic_EmailIsAlsoAtomic(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()

	got, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		Email:    "alice@example.com",
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	assertUserFullyIndexed(t, svc, "acme", "alice", got.ID)
	byEmail, err := svc.GetUserByEmail(ctx, "acme", "alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if byEmail.ID != got.ID {
		t.Errorf("GetUserByEmail id: got %d, want %d", byEmail.ID, got.ID)
	}
}

// TestCreateUser_Atomic_EmailGuardFailureReturnsEmailExistsError covers the
// email index guard specifically: a taken email, with a fresh username and a
// fresh id, must still be refused — the fresh keys alone must not let the
// batch through.
func TestCreateUser_Atomic_EmailGuardFailureReturnsEmailExistsError(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "alice",
		Email:    "shared@example.com",
		TenantID: "acme",
	}); err != nil {
		t.Fatalf("CreateUser(first): %v", err)
	}

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "bob",
		Email:    "shared@example.com",
		TenantID: "acme",
	})
	var target *EmailExistsError
	if !errors.As(err, &target) {
		t.Fatalf("CreateUser(second): got %T, want *EmailExistsError: %v", err, err)
	}
	if target.Email != "shared@example.com" {
		t.Errorf("Email: got %q, want %q", target.Email, "shared@example.com")
	}
}

// TestCreateUser_Atomic_IDCollisionOverwriteRefused reproduces the finding
// go-qrdb#555's review raised: guarding on the username key ALONE let a
// repeated generated id, under a fresh unused username, overwrite an
// existing primary record and identity claim. The atomic batch must guard
// the identity claim key too, so a repeated id is refused even though the
// username and email keys it is paired with are both fresh.
func TestCreateUser_Atomic_IDCollisionOverwriteRefused(t *testing.T) {
	kv := newMemKVStore()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	gen := &fixedIDGen{id: 42}
	svc, err := NewStore(kv, gen, WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	first, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "alice",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(first): %v", err)
	}
	if first.ID != 42 {
		t.Fatalf("first.ID = %d, want 42", first.ID)
	}

	// Same generated id (42), a DIFFERENT, unused username: the username key
	// is fresh, so a guard that checked ONLY the username would let this
	// through and overwrite the first user's primary record and claim.
	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "bob",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	var target *UserIDExistsError
	if !errors.As(err, &target) {
		t.Fatalf("CreateUser(id collision): got %T, want *UserIDExistsError: %v", err, err)
	}
	if target.ID != 42 {
		t.Errorf("ID: got %d, want 42", target.ID)
	}

	// The original record must be intact — untouched by the refused create.
	assertUserFullyIndexed(t, svc, tenantid.System, "alice", 42)
	if _, err := svc.GetUserByUsername(ctx, tenantid.System, "bob"); err == nil {
		t.Error("GetUserByUsername(bob) succeeded; want not-found since the create was refused")
	}
}

// TestCreateUser_Atomic_IDCollisionSameUsernameReportsUserIDExists is the
// bootstrap-realistic sibling of TestCreateUser_Atomic_IDCollisionOverwriteRefused:
// two independently seeded nodes racing first-boot for the SAME fixed
// username generate DIFFERENT random ids that happen to collide (the
// scenario go-qrdb#555 exists for), so BOTH guards this batch holds are
// occupied at once. Codex's review found the diagnosis order untested for
// exactly this case: diagnoseAtomicGuardFailure checks the id guard first,
// so the loser sees UserIDExistsError even though its username ALSO
// collided — and firstboot's createAdminUser must treat that the same as an
// ordinary username collision (see pkg/firstboot's
// TestInitialize_AdminIDCollisionIsIdempotent).
func TestCreateUser_Atomic_IDCollisionSameUsernameReportsUserIDExists(t *testing.T) {
	kv := newMemKVStore()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	gen := &fixedIDGen{id: 42}
	svc, err := NewStore(kv, gen, WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()

	first, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(first): %v", err)
	}
	if first.ID != 42 {
		t.Fatalf("first.ID = %d, want 42", first.ID)
	}

	// SAME generated id (a repeated Snowflake, or a second node racing the
	// same deterministic seed) AND the SAME fixed username: both guards are
	// occupied by the SAME winning record.
	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
	})
	var target *UserIDExistsError
	if !errors.As(err, &target) {
		t.Fatalf("CreateUser(id+username collision): got %T, want *UserIDExistsError: %v", err, err)
	}
	if target.ID != 42 {
		t.Errorf("ID: got %d, want 42", target.ID)
	}

	// The original record must be intact.
	assertUserFullyIndexed(t, svc, tenantid.System, "admin", 42)
}

// TestNewStore_AtomicIdentityRequiresIdentityIndex covers NewStore's
// fail-closed construction check: WithAtomicIdentity without
// WithIdentityIndex has no claim to make atomic.
func TestNewStore_AtomicIdentityRequiresIdentityIndex(t *testing.T) {
	_, err := NewStore(newMemKVStore(), newTestIDGen(t), WithAtomicIdentity())
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if target.Op != "new" {
		t.Errorf("Op: got %q, want %q", target.Op, "new")
	}
}

// guardlessKVStore hides a KVStore's own GuardedBatchKVStore capability (if
// any) behind the plain kvstore.KVStore interface. See pkg/role's identical
// double for why embedding the INTERFACE value, not the concrete type, is
// what suppresses the promotion.
type guardlessKVStore struct {
	kvstore.KVStore
}

// TestNewStore_AtomicIdentityRequiresGuardedBatchStore covers NewStore's
// other fail-closed check: WithAtomicIdentity over a store that does not
// implement kvstore.GuardedBatchKVStore has nothing to commit the claim
// atomically WITH.
func TestNewStore_AtomicIdentityRequiresGuardedBatchStore(t *testing.T) {
	kv := newMemKVStore()
	plain := guardlessKVStore{KVStore: kv}
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	_, err = NewStore(plain, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity())
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if target.Op != "new" {
		t.Errorf("Op: got %q, want %q", target.Op, "new")
	}
}

// TestCreateUserAtomic_UnguardedStoreIsDefensive constructs a kvUserStore
// directly (bypassing NewStore's own construction-time refusal) to reach
// createUserAtomic's defensive check on a store that does not implement
// kvstore.GuardedBatchKVStore.
func TestCreateUserAtomic_UnguardedStoreIsDefensive(t *testing.T) {
	kv := newMemKVStore()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	s := &kvUserStore{
		store:          guardlessKVStore{KVStore: kv},
		idgen:          newTestIDGen(t),
		identity:       idx,
		atomicIdentity: true,
	}
	_, err = s.createUserAtomic(context.Background(), &CreateUserRequest{Username: "admin"}, tenantid.System, 1, encodeID(1), usernameIndexKey(tenantid.System, "admin"), "", "", LifecycleActive, nil)
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestCreateUser_Atomic_DefiniteRefusalSkipsReconciliation is pkg/role's
// identical test for users.
func TestCreateUser_Atomic_DefiniteRefusalSkipsReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "not applied (no leader)", err: &kvstore.NotAppliedError{Err: errors.New("service: not leader")}},
		{name: "unsupported capability", err: &kvstore.GuardedBatchUnsupportedError{Err: errors.New("mvcc not configured")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fkv := &faultyGuardedStore{memKVStore: newMemKVStore(), failNext: true, err: tc.err}
			idx, err := tenant.NewIdentityIndex(fkv)
			if err != nil {
				t.Fatalf("tenant.NewIdentityIndex: %v", err)
			}
			svc, err := NewStore(fkv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			start := time.Now()
			_, err = svc.CreateUser(context.Background(), &CreateUserRequest{Username: "refused", TenantID: tenantid.System, WithoutEmailLoginKey: true})
			if !errors.Is(err, tc.err) {
				t.Fatalf("CreateUser err = %v, want it to carry the refusal", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("CreateUser took %v: a definite refusal must not wait out the reconciliation window", elapsed)
			}
		})
	}
}

// TestCreateUser_Atomic_GuardFailureDiagnosisSurvivesStaleRead is pkg/role's
// identical test for users: the admin is committed by another node but not
// yet applied locally, and the losing create must still report a typed
// already-exists error firstboot verifies, not a generic failure.
func TestCreateUser_Atomic_GuardFailureDiagnosisSurvivesStaleRead(t *testing.T) {
	kv := newMemKVStore()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	winner, err := NewStore(kv, newTestIDGen(t), WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore (winner): %v", err)
	}
	req := &CreateUserRequest{Username: "admin", TenantID: tenantid.System, WithoutEmailLoginKey: true}
	if _, err := winner.CreateUser(context.Background(), req); err != nil {
		t.Fatalf("winner CreateUser: %v", err)
	}

	stale := &faultyGuardedStore{memKVStore: kv, staleReadsBeforeVisible: 8}
	staleIdx, err := tenant.NewIdentityIndex(stale)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	loser, err := NewStore(stale, newTestIDGen(t), WithIdentityIndex(staleIdx), WithAtomicIdentity(),
		WithReconcilePolling(time.Millisecond, time.Second))
	if err != nil {
		t.Fatalf("NewStore (loser): %v", err)
	}
	_, err = loser.CreateUser(context.Background(), req)
	// Both stores draw ids from identically seeded generators — go-qrdb#555's
	// own failure shape — so the id claim collides too and is diagnosed
	// first. Either typed conflict is what firstboot verifies; the generic
	// guard failure a single stale read produced is not.
	var usernameExists *UsernameExistsError
	var idExists *UserIDExistsError
	if !errors.As(err, &usernameExists) && !errors.As(err, &idExists) {
		t.Fatalf("CreateUser err = %v (%T), want a typed already-exists error once the stale read catches up", err, err)
	}
	if stale.staleReadsBeforeVisible != 0 {
		t.Fatalf("stale-read budget left = %d: the test never reached the diagnosis through a stale read", stale.staleReadsBeforeVisible)
	}
}
