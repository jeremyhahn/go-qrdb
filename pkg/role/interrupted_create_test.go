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

package role

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// go-qrdb#555: CreateRole's identity claim and its three writes (primary
// record, tenant index, name index) used to be four separate proposals. A
// Raft leadership change between any two of them left whichever had already
// committed in place and abandoned the rest, and nothing could repair that
// afterward — completing or rolling back the remainder needs the very
// leadership that was just lost. Every retry, including one from a freshly
// elected leader, found the same partial state and refused identically,
// forever.
//
// The fix (owner ruling) is not a repair path: it is closing the window so
// the interrupted state can never be produced at all. WithAtomicIdentity
// commits the claim and all three writes as ONE guarded batch — see
// createRoleAtomic in store.go — so a failure anywhere in the sequence
// leaves either everything or nothing.
//
// newAtomicTestStore builds a role store over a guarded, atomic memKVStore
// wired with a real tenant.IdentityIndex over the SAME store — the
// precondition WithAtomicIdentity documents and this suite exercises.
func newAtomicTestStore(t *testing.T, kv *memKVStore) RoleServicer {
	t.Helper()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	svc, err := NewStore(kv, WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return svc
}

func assertRoleFullyIndexed(t *testing.T, svc RoleServicer, r *Role) {
	t.Helper()
	ctx := context.Background()

	byID, err := svc.GetRole(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRole(%q): %v", r.ID, err)
	}
	if byID.Name != r.Name {
		t.Errorf("GetRole name: got %q, want %q", byID.Name, r.Name)
	}

	byName, err := svc.GetRoleByName(ctx, r.TenantID, r.Name)
	if err != nil {
		t.Fatalf("GetRoleByName(%q, %q): %v", r.TenantID, r.Name, err)
	}
	if byName.ID != r.ID {
		t.Errorf("GetRoleByName id: got %q, want %q", byName.ID, r.ID)
	}

	listed, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: r.TenantID})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	found := false
	for _, role := range listed.Roles {
		if role.ID == r.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListRoles(%q): id %q not present", r.TenantID, r.ID)
	}
}

// assertNothingWritten checks that none of a built-in role's three
// store-local keys (primary, tenant index, name index) exist — the "commits
// entirely or leaves nothing" half of the atomic guarantee. The identity
// claim itself is checked separately by the caller via idx.Lookup, since it
// lives behind the IdentityIndex abstraction rather than a key this test
// composes by hand.
func assertNothingWritten(t *testing.T, kv *memKVStore, want *Role) {
	t.Helper()
	ctx := context.Background()
	if v, _ := kv.Get(ctx, primaryKey(want.ID)); v != nil {
		t.Errorf("primary key %q exists after a failed atomic create", primaryKey(want.ID))
	}
	if v, _ := kv.Get(ctx, tenantIndexKey(want.TenantID, want.ID)); v != nil {
		t.Errorf("tenant index key exists after a failed atomic create")
	}
	if v, _ := kv.Get(ctx, nameIndexKey(want.TenantID, want.Name)); v != nil {
		t.Errorf("name index key exists after a failed atomic create")
	}
}

// TestCreateRole_Atomic_Success exercises the ordinary atomic path: claim
// and all three writes land together, and the role is fully reachable by
// every lookup path afterward.
func TestCreateRole_Atomic_Success(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()

	want := BuiltInRoles(tenantid.System)[0]
	got, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	assertRoleFullyIndexed(t, svc, want)
}

// TestCreateRole_Atomic_ConcurrentCreatesConverge is the deterministic
// version of go-qrdb#555's actual failure: every node's first-boot attempt
// races CreateRole for the SAME built-in role concurrently. Exactly one must
// win; every other call must observe a clean RoleIDExistsError rather than a
// half-written record, and the winner's record must be fully indexed.
//
// This is the "rollback-vs-retry race... can no longer happen" case Codex's
// review asked for: under the OLD design, a loser's "repair" could race a
// winner's OWN deferred rollback. Under the atomic design there is no
// rollback and no repair to race — only ONE PutAllIfUnchanged call ever
// applies, full stop.
func TestCreateRole_Atomic_ConcurrentCreatesConverge(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()
	want := BuiltInRoles(tenantid.System)[0]

	const attempts = 20
	var wg sync.WaitGroup
	results := make([]error, attempts)
	roles := make([]*Role, attempts)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			roles[i], results[i] = svc.CreateRole(ctx, &CreateRoleRequest{
				ID:          want.ID,
				Name:        want.Name,
				TenantID:    want.TenantID,
				Permissions: want.Permissions,
			})
		}(i)
	}
	wg.Wait()

	wins, losses := 0, 0
	for i := 0; i < attempts; i++ {
		switch {
		case results[i] == nil:
			wins++
			if roles[i].ID != want.ID {
				t.Errorf("attempt %d: winning ID %q, want %q", i, roles[i].ID, want.ID)
			}
		default:
			// A loser can be refused two ways, both pre-existing, unchanged
			// error types: the early name-uniqueness Get (non-atomic, so it
			// usually — but not deterministically — wins the race) or the
			// atomic batch's own guard when the Get raced ahead of every
			// commit. Either is a clean, typed refusal; neither is the
			// corrupted or crashing state go-qrdb#555 evidenced.
			var byName *RoleNameExistsError
			var byID *RoleIDExistsError
			if !errors.As(results[i], &byName) && !errors.As(results[i], &byID) {
				t.Errorf("attempt %d: got %T, want *RoleNameExistsError or *RoleIDExistsError: %v", i, results[i], results[i])
			}
			losses++
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1 (losses = %d)", wins, losses)
	}
	assertRoleFullyIndexed(t, svc, want)
}

// TestCreateRole_Atomic_DifferentIDsSameNameRefused reproduces the finding
// go-qrdb#555's round-3 review raised: guarding on the identity claim key
// ALONE lets two concurrent creates with DIFFERENT ids but the SAME
// tenant/name each satisfy their own distinct claim guard and both commit,
// so the last one silently overwrites the name index while both primary
// records, tenant indexes, and claims remain — orphaned, unreachable by
// name, but still occupying storage forever. The batch must guard the name
// key too, so exactly one of these concurrent creates wins and every other
// one is refused, cleanly, with RoleNameExistsError.
func TestCreateRole_Atomic_DifferentIDsSameNameRefused(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()
	const name = "contended-custom-role"

	const attempts = 20
	var wg sync.WaitGroup
	results := make([]error, attempts)
	roles := make([]*Role, attempts)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			// ID deliberately left empty: CreateRole mints a fresh random
			// UUID per call, so every attempt races with a DIFFERENT id and
			// the SAME name — the exact shape a claim-only guard cannot see.
			roles[i], results[i] = svc.CreateRole(ctx, &CreateRoleRequest{
				Name:     name,
				TenantID: tenantid.System,
			})
		}(i)
	}
	wg.Wait()

	wins, losses := 0, 0
	var winnerID string
	for i := 0; i < attempts; i++ {
		switch {
		case results[i] == nil:
			wins++
			winnerID = roles[i].ID
		default:
			var byName *RoleNameExistsError
			if !errors.As(results[i], &byName) {
				t.Errorf("attempt %d: got %T, want *RoleNameExistsError: %v", i, results[i], results[i])
			}
			losses++
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (losses = %d) — a name-index race let more than one create commit", wins, losses)
	}

	// The name index must resolve to the WINNER's id, and the winner's own
	// primary/tenant-index/claim records must all be present — no orphan
	// left by a loser that raced past the (now-closed) claim-only guard.
	byName, err := svc.GetRoleByName(ctx, tenantid.System, name)
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if byName.ID != winnerID {
		t.Errorf("GetRoleByName id = %q, want the winner's id %q", byName.ID, winnerID)
	}
	if _, err := svc.GetRole(ctx, winnerID); err != nil {
		t.Errorf("GetRole(winner): %v", err)
	}
}

// TestCreateRole_Atomic_GuardFailureRefusesCleanly is the sequential version
// of the concurrency test: a second create for the same built-in role, after
// the first has already committed, is refused cleanly with a pre-existing,
// UNCHANGED error type (Codex review finding: keep RoleIDExistsError's
// contract unchanged) — the early name-uniqueness Get catches a sequential
// repeat deterministically, so this is RoleNameExistsError here, exactly as
// it was before go-qrdb#555. It does not attempt to interpret or repair the
// existing record itself: that determination belongs to firstboot, not to
// CreateRole; see pkg/firstboot's registerBuiltInRoles.
func TestCreateRole_Atomic_GuardFailureRefusesCleanly(t *testing.T) {
	kv := newMemKVStore()
	svc := newAtomicTestStore(t, kv)
	ctx := context.Background()
	want := BuiltInRoles(tenantid.System)[1]

	req := &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	}
	if _, err := svc.CreateRole(ctx, req); err != nil {
		t.Fatalf("CreateRole(first): %v", err)
	}

	_, err := svc.CreateRole(ctx, req)
	var target *RoleNameExistsError
	if !errors.As(err, &target) {
		t.Fatalf("CreateRole(second): got %T, want *RoleNameExistsError: %v", err, err)
	}
	if target.Name != want.Name {
		t.Errorf("Name: got %q, want %q", target.Name, want.Name)
	}
}

// faultyGuardedStore wraps a memKVStore and models two DIFFERENT ways
// PutAllIfUnchanged can return an error, once each:
//
//   - failNext: the error comes back and NOTHING applied — a proposal that
//     never committed.
//   - ambiguousNext: the writes apply FIRST and the error comes back
//     anyway — a proposal that committed but whose response was lost, the
//     case reconcileAmbiguousCreate exists to recognise as success rather
//     than report as a failure that would otherwise burn the id.
//
// Every other method delegates unchanged, except Get, which — unlike
// memKVStore's own, context-blind Get — checks ctx.Err() first, the way a
// real Raft-backed KVServicer.Get would when its RPC/proposal layer sees an
// already-done context. That is load-bearing for
// TestCreateRole_Atomic_ReconcilesLostResponse_ContextAlreadyDone: the whole
// point of that test is that reconcileAmbiguousCreate's read-back must
// SURVIVE a canceled/expired ctx, and a Get that silently ignores
// cancellation would make the test pass whether or not the fix is present.
//
// cancelOnAmbiguous, when set, is called the instant the ambiguousNext write
// commits — modelling the realistic timing go-qrdb#555's round-3 review
// found untested: the caller's context expires AT ABOUT THE SAME MOMENT the
// response is lost (a deadline is usually WHY it was lost), not before
// CreateRole was ever called. The pre-checks and the PutAllIfAllUnchanged
// call itself still run against a live ctx; only reconcileAmbiguousCreate's
// OWN read-back afterward sees it done.
// staleReadsBeforeVisible, when > 0, makes Get lie — returning (nil, nil) as
// though the key were absent — that many times BEFORE ever consulting the
// real store, decrementing on every call regardless of key. It models
// cmd/qrdbd's boot-time StaleRead: a proposal already committed to the Raft
// log is not necessarily visible to a local, non-linearizable read the
// instant the proposal returns, and this is what lets a test put
// reconcileAmbiguousCreate's read-back through that exact visibility lag
// instead of a write that is synchronously visible the moment it lands.
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
	// An empty writes set is a capability PROBE (routingRoleService/
	// routingUserService's atomicCapable), not a real create: honour it
	// truthfully against the real store so a fault meant for the create
	// itself does not get consumed by the probe that precedes it.
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

// TestCreateRole_Atomic_FaultLeavesNeitherClaimNorRecord is the "fault
// injected between claim and record is impossible, or leaves neither" case
// Codex's review asked for. Because the claim and every write are a SINGLE
// PutAllIfUnchanged call, a fault in that call has nothing to interrupt
// BETWEEN claim and record — it can only fail the whole proposal, which
// this double models directly. Nothing this call would have written exists
// afterward: not the claim, not the primary, not either index.
func TestCreateRole_Atomic_FaultLeavesNeitherClaimNorRecord(t *testing.T) {
	kv := newMemKVStore()
	fkv := &faultyGuardedStore{memKVStore: kv, failNext: true, err: errors.New("injected: leadership lost mid-proposal")}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	svc, err := NewStore(fkv, WithIdentityIndex(idx), WithAtomicIdentity(),
		// This create's write never lands (failNext), so reconciliation's
		// retry loop runs for its full window before giving up. A tiny
		// window keeps the test fast without changing what it proves.
		WithReconcilePolling(time.Millisecond, 20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	want := BuiltInRoles(tenantid.System)[2]

	_, err = svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	})
	if err == nil {
		t.Fatal("expected the injected fault to surface as an error")
	}

	assertNothingWritten(t, kv, want)
	claim, held, lerr := idx.Lookup(ctx, tenant.IdentityRole, want.ID)
	if lerr != nil {
		t.Fatalf("Lookup: %v", lerr)
	}
	if held {
		t.Errorf("identity claim held after a failed atomic create: %+v", claim)
	}

	// The id is not burned: a retry after the fault succeeds cleanly,
	// because nothing was left behind to conflict with it.
	got, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	})
	if err != nil {
		t.Fatalf("CreateRole(retry): %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	assertRoleFullyIndexed(t, svc, want)
}

// TestCreateRole_Atomic_ReconcilesLostResponse covers the other half of
// reconcileAmbiguousCreate: PutAllIfUnchanged's writes commit and ONLY the
// response is lost. Every write this call would have made is confirmed
// present with the exact bytes this call would have written, so the create
// is reported as the success it actually was — not as a failure that would
// otherwise report a real role as never having been created.
func TestCreateRole_Atomic_ReconcilesLostResponse(t *testing.T) {
	kv := newMemKVStore()
	fkv := &faultyGuardedStore{memKVStore: kv, ambiguousNext: true, err: errors.New("injected: response lost after commit")}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	svc, err := NewStore(fkv, WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	want := BuiltInRoles(tenantid.System)[3]

	got, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	})
	if err != nil {
		t.Fatalf("CreateRole: expected the lost response to reconcile to success, got error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	assertRoleFullyIndexed(t, svc, want)
}

// TestCreateRole_Atomic_ReconcilesLostResponse_ContextAlreadyDone is the
// go-qrdb#555 round-3 review's exact non-vacuousness complaint: a lost
// response is very often lost BECAUSE the caller's context ran out — a
// timeout or a cancellation is what "the response never arrived" usually
// looks like from the caller's side, not an active background context as
// TestCreateRole_Atomic_ReconcilesLostResponse models. Reusing ctx for the
// read-back would then fail every read immediately (this store's Get checks
// ctx.Err()) and never emit the audit event reconcileAmbiguousCreate exists
// to guarantee. CreateRole must still succeed, and the audit event must
// still be emitted, using a context independently bounded from the one that
// was already done.
func TestCreateRole_Atomic_ReconcilesLostResponse_ContextAlreadyDone(t *testing.T) {
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
	svc, err := NewStore(fkv, WithIdentityIndex(idx), WithAtomicIdentity(), WithAuditLogger(al))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	want := BuiltInRoles(tenantid.System)[4]

	got, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	})
	if err != nil {
		t.Fatalf("CreateRole: expected the lost response to reconcile to success despite a canceled ctx, got error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	assertRoleFullyIndexed(t, svc, want)

	if _, ok := al.findByAction("create"); !ok {
		t.Error("expected a create audit event despite the caller's context already being done")
	}
}

// TestCreateRole_Atomic_ReconcilesLostResponse_StaleReadCatchesUp is the
// go-qrdb#555 round-4 review's finding #1 regression: cmd/qrdbd's boot-time
// stores read via StaleRead, so a proposal that committed but lost its
// response may not be LOCALLY visible for a brief window while apply
// catches up. A single immediate read-back would see stale/absent values
// and wrongly report failure — and skip the audit event — for a create that
// actually committed. The read-back must retry within its window rather
// than give up on the first attempt.
func TestCreateRole_Atomic_ReconcilesLostResponse_StaleReadCatchesUp(t *testing.T) {
	kv := newMemKVStore()
	fkv := &faultyGuardedStore{
		memKVStore:    kv,
		ambiguousNext: true,
		err:           errors.New("injected: response lost after commit"),
		// The write commits synchronously (ambiguousNext applies it via the
		// real store before returning the injected error), but the first
		// few Get calls lie as though it were still invisible — modelling
		// StaleRead's local-apply lag. Reconciliation must survive this.
		staleReadsBeforeVisible: 4,
	}
	idx, err := tenant.NewIdentityIndex(fkv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	al := &fakeAuditLogger{}
	svc, err := NewStore(fkv, WithIdentityIndex(idx), WithAtomicIdentity(), WithAuditLogger(al),
		WithReconcilePolling(time.Millisecond, time.Second))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	want := BuiltInRoles(tenantid.System)[5]

	got, err := svc.CreateRole(context.Background(), &CreateRoleRequest{
		ID:          want.ID,
		Name:        want.Name,
		TenantID:    want.TenantID,
		Permissions: want.Permissions,
	})
	if err != nil {
		t.Fatalf("CreateRole: expected reconciliation to retry past the stale-read window and succeed, got error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	assertRoleFullyIndexed(t, svc, want)

	if _, ok := al.findByAction("create"); !ok {
		t.Error("expected a create audit event once reconciliation caught up with the stale-read lag")
	}
}

// TestCreateRoleAtomic_UnguardedStoreIsDefensive constructs a kvRoleStore
// directly (bypassing NewStore's own construction-time refusal) to reach
// createRoleAtomic's defensive check on a store that does not implement
// kvstore.GuardedBatchKVStore. NewStore makes this unreachable through the
// public API; this white-box test is what lets that unreachability be
// asserted rather than assumed.
func TestCreateRoleAtomic_UnguardedStoreIsDefensive(t *testing.T) {
	kv := newMemKVStore()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	s := &kvRoleStore{
		store:          guardlessKVStore{KVStore: kv},
		identity:       idx,
		atomicIdentity: true,
	}
	_, err = s.createRoleAtomic(context.Background(), &Role{ID: "x", Name: "manager", TenantID: "acme"}, "acme", nameIndexKey("acme", "manager"))
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestNewStore_AtomicIdentityRequiresIdentityIndex covers NewStore's fail-
// closed construction check: WithAtomicIdentity without WithIdentityIndex
// has no claim to make atomic.
func TestNewStore_AtomicIdentityRequiresIdentityIndex(t *testing.T) {
	_, err := NewStore(newMemKVStore(), WithAtomicIdentity())
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if target.Op != "new" {
		t.Errorf("Op: got %q, want %q", target.Op, "new")
	}
}

// guardlessKVStore hides a KVStore's own GuardedBatchKVStore capability (if
// any) behind the plain kvstore.KVStore interface, so wrapping memKVStore —
// which implements PutAllIfUnchanged — in one yields a store the
// GuardedBatchKVStore type assertion answers "no" for, exactly what
// NewStore's other atomic-identity check needs to be exercised in isolation
// from the identity-index check above. Embedding the INTERFACE value rather
// than the concrete *memKVStore is what suppresses the promotion: only the
// methods kvstore.KVStore itself declares are promoted.
type guardlessKVStore struct {
	kvstore.KVStore
}

// TestNewStore_AtomicIdentityRequiresGuardedBatchStore covers NewStore's
// other fail-closed check: WithAtomicIdentity over a store that does not
// implement kvstore.MultiGuardedBatchKVStore has nothing to commit the claim
// atomically WITH.
func TestNewStore_AtomicIdentityRequiresGuardedBatchStore(t *testing.T) {
	kv := newMemKVStore()
	plain := guardlessKVStore{KVStore: kv}
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	_, err = NewStore(plain, WithIdentityIndex(idx), WithAtomicIdentity())
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
	if target.Op != "new" {
		t.Errorf("Op: got %q, want %q", target.Op, "new")
	}
}

// TestCreateRole_IDClashDifferentNameStillRefused is the control for the
// NON-atomic path (the default, used by routingRoleService): an id
// collision that does NOT denote the same role is a genuine conflict and
// must still be refused. Unaffected by the atomic-identity change, since
// this store is never given WithAtomicIdentity.
func TestCreateRole_IDClashDifferentNameStillRefused(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	const id = "shared-id"
	if _, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          id,
		Name:        "manager",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	}); err != nil {
		t.Fatalf("CreateRole(first): %v", err)
	}

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          id,
		Name:        "viewer",
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	var target *RoleIDExistsError
	if !errors.As(err, &target) {
		t.Fatalf("expected *RoleIDExistsError, got %T: %v", err, err)
	}
}

// TestCreateRole_Atomic_DefiniteRefusalSkipsReconciliation pins that a
// refusal PROVING nothing was written — kvstore.ErrNotApplied: a
// propose-time NotLeader, an unsupported capability — returns at once with
// its cause, instead of polling the full reconciliation window for a write
// that cannot appear. The window is left at its 5 s production default, so a
// regression shows up as a slow, failing test rather than a silent one.
func TestCreateRole_Atomic_DefiniteRefusalSkipsReconciliation(t *testing.T) {
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
			svc, err := NewStore(fkv, WithIdentityIndex(idx), WithAtomicIdentity())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			start := time.Now()
			_, err = svc.CreateRole(context.Background(), &CreateRoleRequest{Name: "refused", TenantID: tenantid.System})
			if !errors.Is(err, tc.err) {
				t.Fatalf("CreateRole err = %v, want it to carry the refusal", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("CreateRole took %v: a definite refusal must not wait out the reconciliation window", elapsed)
			}
		})
	}
}

// TestCreateRole_Atomic_GuardFailureDiagnosisSurvivesStaleRead is the losing
// first-boot node's view: another node's identical built-in role is
// COMMITTED — the state machine refuses this node's guard on it — but this
// node's local reads (cmd/qrdbd's boot-time StaleRead) have not applied it
// yet, so the pre-checks and the first diagnosis reads all see nothing. The
// caller must still get the typed RoleIDExistsError firstboot resolves
// idempotently, not a generic guard failure that aborts first boot.
func TestCreateRole_Atomic_GuardFailureDiagnosisSurvivesStaleRead(t *testing.T) {
	kv := newMemKVStore()
	idx, err := tenant.NewIdentityIndex(kv)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	winner, err := NewStore(kv, WithIdentityIndex(idx), WithAtomicIdentity())
	if err != nil {
		t.Fatalf("NewStore (winner): %v", err)
	}
	want := BuiltInRoles(tenantid.System)[0]
	req := &CreateRoleRequest{ID: want.ID, Name: want.Name, TenantID: want.TenantID, Permissions: want.Permissions}
	if _, err := winner.CreateRole(context.Background(), req); err != nil {
		t.Fatalf("winner CreateRole: %v", err)
	}

	// The loser shares the committed state but reads it stale: every Get
	// answers "absent" until the budget is spent.
	stale := &faultyGuardedStore{memKVStore: kv, staleReadsBeforeVisible: 8}
	staleIdx, err := tenant.NewIdentityIndex(stale)
	if err != nil {
		t.Fatalf("tenant.NewIdentityIndex: %v", err)
	}
	loser, err := NewStore(stale, WithIdentityIndex(staleIdx), WithAtomicIdentity(),
		WithReconcilePolling(time.Millisecond, time.Second))
	if err != nil {
		t.Fatalf("NewStore (loser): %v", err)
	}
	_, err = loser.CreateRole(context.Background(), req)
	var idExists *RoleIDExistsError
	if !errors.As(err, &idExists) {
		t.Fatalf("CreateRole err = %v (%T), want *RoleIDExistsError once the stale read catches up", err, err)
	}
	if stale.staleReadsBeforeVisible != 0 {
		t.Fatalf("stale-read budget left = %d: the test never reached the diagnosis through a stale read", stale.staleReadsBeforeVisible)
	}
}
