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
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestCreateUser_SystemRequestPersisted asserts CreateUser honors
// CreateUserRequest.System, seeding the record through the REAL create path
// rather than by mutating a record after the fact (go-qrdb#106) — the test
// shape #17 showed cannot be trusted to catch a regression.
func TestCreateUser_SystemRequestPersisted(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	created, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		WithoutEmailLoginKey: true,
		System:               true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if !created.System {
		t.Fatal("expected System=true on the created record")
	}

	fetched, err := svc.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !fetched.System {
		t.Error("expected the persisted record to read back System=true")
	}
}

// TestCreateUser_SystemDefaultsFalse asserts an ordinary CreateUserRequest —
// the shape every transport handler builds — never produces a system user.
func TestCreateUser_SystemDefaultsFalse(t *testing.T) {
	svc := newTestStore(t)
	created := createTestUser(t, svc, "ordinary", "acme")
	if created.System {
		t.Error("expected System=false for a request that never set it")
	}
}

// TestDeleteUser_RefusesRealSystemUser seeds a system user through the real
// CreateUserRequest path (go-qrdb#106's own prescribed verification), then
// asserts DeleteUser refuses it with SystemUserImmutableError and that the
// record stays readable afterward — the refusal must not leave the account
// half-deleted.
func TestDeleteUser_RefusesRealSystemUser(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	admin, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		WithoutEmailLoginKey: true,
		System:               true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	err = svc.DeleteUser(ctx, admin.ID)
	if err == nil {
		t.Fatal("expected DeleteUser to refuse a system user")
	}
	var target *SystemUserImmutableError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T, want *SystemUserImmutableError", err)
	}

	// MUTATION CHECK: this assertion is what would fail if the guard at
	// store.go's DeleteUser ("if existing.System { return ... }") were ever
	// removed, or if System were derived instead of read from the stored
	// record — the two ways this guard has failed to protect anything before
	// (go-qrdb#106, go-qrdb#17).
	still, gerr := svc.GetUser(ctx, admin.ID)
	if gerr != nil {
		t.Fatalf("GetUser after refused delete: %v", gerr)
	}
	if still == nil || still.ID != admin.ID {
		t.Fatal("expected the system user's record to remain readable after the refused delete")
	}
	if !still.System {
		t.Error("expected the surviving record to still report System=true")
	}
}

// TestDeleteUser_NonSystemUserStillDeletable is the mutation-check's other
// half: an ordinary user, created the identical way except for System, must
// still be deletable — the guard must not overreach.
func TestDeleteUser_NonSystemUserStillDeletable(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	ordinary := createTestUser(t, svc, "regular", "acme")

	if err := svc.DeleteUser(ctx, ordinary.ID); err != nil {
		t.Fatalf("DeleteUser(non-system user): %v", err)
	}

	var notFound *UserNotFoundError
	if _, err := svc.GetUser(ctx, ordinary.ID); !errors.As(err, &notFound) {
		t.Fatalf("GetUser after delete: err = %v, want *UserNotFoundError", err)
	}
}

// ---------------------------------------------------------------------------
// System invariants enforced independent of caller identity (go-qrdb#106
// review): a raw user.UserServicer is reachable in-process through sdk/go's
// embedded surface and Node.UserService(), so "no wire request can set
// System" is not sufficient on its own. CreateUser and PromoteToSystemUser
// both enforce the system-tenant scope and store-wide uniqueness regardless
// of who calls them.
// ---------------------------------------------------------------------------

// TestCreateUser_SystemRequiresSystemTenant asserts a System request outside
// the system tenant is refused before anything is written, whoever the
// caller is.
func TestCreateUser_SystemRequiresSystemTenant(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "not-the-bootstrap-admin",
		WithoutEmailLoginKey: true,
		TenantID:             "acme",
		System:               true,
	})
	var target *SystemTenantRequiredError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T (%v), want *SystemTenantRequiredError", err, err)
	}

	// Nothing must have been written -- not even the username claim.
	if _, gerr := svc.GetUserByUsername(ctx, "acme", "not-the-bootstrap-admin"); gerr == nil {
		t.Fatal("refused create left a readable record behind")
	}
}

// TestCreateUser_SystemRefusesSecondUser asserts a second System: true
// request is refused once one exists in the store, closing the "arbitrary
// undeletable users" gap: nothing about permission or wire access is checked
// here, this is enforced against the store state itself.
func TestCreateUser_SystemRefusesSecondUser(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	first, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "admin",
		WithoutEmailLoginKey: true,
		System:               true,
	})
	if err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}

	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		Username:             "another-admin",
		WithoutEmailLoginKey: true,
		System:               true,
	})
	var target *SystemUserAlreadyExistsError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T (%v), want *SystemUserAlreadyExistsError", err, err)
	}
	if target.ExistingID != first.ID {
		t.Errorf("ExistingID = %d, want %d", target.ExistingID, first.ID)
	}

	// The refused attempt's username must be genuinely unclaimed, not
	// left behind by a partial commit.
	if _, gerr := svc.GetUserByUsername(ctx, tenantid.System, "another-admin"); gerr == nil {
		t.Fatal("refused second system-user create left a readable record behind")
	}

	// The first system user must be entirely unaffected.
	still, gerr := svc.GetUser(ctx, first.ID)
	if gerr != nil {
		t.Fatalf("GetUser(first): %v", gerr)
	}
	if !still.System {
		t.Error("the original system user lost its System flag")
	}
}

// TestPromoteToSystemUser_Idempotent asserts promoting an already-System
// record is a no-op success.
func TestPromoteToSystemUser_Idempotent(t *testing.T) {
	svc := newTestStore(t)
	store := svc.(*kvUserStore)
	ctx := context.Background()

	admin, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "admin", WithoutEmailLoginKey: true, System: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := store.PromoteToSystemUser(ctx, admin.ID)
	if err != nil {
		t.Fatalf("PromoteToSystemUser: %v", err)
	}
	if !got.System || got.ID != admin.ID {
		t.Errorf("PromoteToSystemUser(already-System) = %+v, want the same record unchanged", got)
	}
}

// TestPromoteToSystemUser_PromotesAPreExistingAdmin is the core go-qrdb#106
// reconciliation path: an ordinary record created before the flag existed
// (or created without WithoutEmailLoginKey's sibling System bit for any
// other reason) can be healed in place.
func TestPromoteToSystemUser_PromotesAPreExistingAdmin(t *testing.T) {
	svc := newTestStore(t)
	store := svc.(*kvUserStore)
	ctx := context.Background()

	legacy, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "admin", WithoutEmailLoginKey: true, // no System: simulates a pre-#106 record
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if legacy.System {
		t.Fatal("test setup: the seeded record must not already be System")
	}

	promoted, err := store.PromoteToSystemUser(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("PromoteToSystemUser: %v", err)
	}
	if !promoted.System || promoted.ID != legacy.ID {
		t.Fatalf("PromoteToSystemUser = %+v, want System=true on id %d", promoted, legacy.ID)
	}

	// DeleteUser must now actually refuse it.
	var immutable *SystemUserImmutableError
	if derr := svc.DeleteUser(ctx, legacy.ID); !errors.As(derr, &immutable) {
		t.Fatalf("DeleteUser after promotion: err = %v, want *SystemUserImmutableError", derr)
	}
}

// TestPromoteToSystemUser_RefusesWrongTenant asserts a record outside the
// system tenant cannot be promoted.
func TestPromoteToSystemUser_RefusesWrongTenant(t *testing.T) {
	svc := newTestStore(t)
	store := svc.(*kvUserStore)
	ordinary := createTestUser(t, svc, "not-the-admin", "acme")

	_, err := store.PromoteToSystemUser(context.Background(), ordinary.ID)
	var target *SystemTenantRequiredError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T (%v), want *SystemTenantRequiredError", err, err)
	}
}

// TestPromoteToSystemUser_RefusesWhenAnotherSystemUserExists asserts the
// uniqueness invariant applies to promotion exactly as it does to create.
func TestPromoteToSystemUser_RefusesWhenAnotherSystemUserExists(t *testing.T) {
	svc := newTestStore(t)
	store := svc.(*kvUserStore)
	ctx := context.Background()

	admin, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "admin", WithoutEmailLoginKey: true, System: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(system): %v", err)
	}
	second, err := svc.CreateUser(ctx, &CreateUserRequest{
		Username: "second", WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}

	_, err = store.PromoteToSystemUser(ctx, second.ID)
	var target *SystemUserAlreadyExistsError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T (%v), want *SystemUserAlreadyExistsError", err, err)
	}
	if target.ExistingID != admin.ID {
		t.Errorf("ExistingID = %d, want %d", target.ExistingID, admin.ID)
	}

	still, gerr := svc.GetUser(ctx, second.ID)
	if gerr != nil {
		t.Fatalf("GetUser(second): %v", gerr)
	}
	if still.System {
		t.Error("the refused promotion target ended up System anyway")
	}
}

// TestPromoteToSystemUser_UnknownID asserts the ordinary not-found mapping.
func TestPromoteToSystemUser_UnknownID(t *testing.T) {
	svc := newTestStore(t)
	store := svc.(*kvUserStore)

	_, err := store.PromoteToSystemUser(context.Background(), 999999)
	var notFound *UserNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %T (%v), want *UserNotFoundError", err, err)
	}
}

// TestPromoteToSystemUser_ZeroID asserts the same validation CreateUser and
// DeleteUser apply to a zero id.
func TestPromoteToSystemUser_ZeroID(t *testing.T) {
	svc := newTestStore(t)
	store := svc.(*kvUserStore)

	_, err := store.PromoteToSystemUser(context.Background(), 0)
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T (%v), want *ValidationError", err, err)
	}
}

// newPromotionStore returns a kvUserStore over a guarded-batch-capable memory
// double, with an ordinary (non-System) admin already created in the system
// tenant — the state a cluster upgraded across go-qrdb#106 is in.
func newPromotionStore(t *testing.T) (*kvUserStore, *memKVStore, *User) {
	t.Helper()
	kv := newMemKVStore()
	svcAny, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store := svcAny.(*kvUserStore)
	legacy, err := store.CreateUser(context.Background(), &CreateUserRequest{
		Username: "admin", WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return store, kv, legacy
}

// renameRecord rewrites the record's Name directly in the store, standing in
// for a concurrent update that lands between a read and a guarded write.
func renameRecord(t *testing.T, kv *memKVStore, id uint64, name string) {
	t.Helper()
	ctx := context.Background()
	raw, err := kv.Get(ctx, primaryKey(id))
	if err != nil || raw == nil {
		t.Fatalf("read record: %v", err)
	}
	var u User
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	u.Name = name
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(id), data); err != nil {
		t.Fatalf("put: %v", err)
	}
}

// TestPromoteToSystemUser_RetriesOnConflict lands an unrelated update between
// the promotion's read and its guarded batch, through the store's hook rather
// than racing goroutines. The record guard must fail that batch, and the
// retry must re-read, keep the concurrent update, and still promote.
func TestPromoteToSystemUser_RetriesOnConflict(t *testing.T) {
	store, kv, legacy := newPromotionStore(t)
	injected := false
	kv.beforeBatch = func() {
		if !injected {
			injected = true
			renameRecord(t, kv, legacy.ID, "concurrent-writer-won-the-race")
		}
	}

	promoted, err := store.PromoteToSystemUser(context.Background(), legacy.ID)
	if err != nil {
		t.Fatalf("PromoteToSystemUser: %v", err)
	}
	if !injected {
		t.Fatal("test did not exercise the conflict: beforeBatch never fired")
	}
	if !promoted.System {
		t.Error("promotion was lost across the retry")
	}
	if promoted.Name != "concurrent-writer-won-the-race" {
		t.Errorf("Name = %q, want the concurrent write to have survived the retry", promoted.Name)
	}
}

// TestPromoteToSystemUser_ExhaustsRetriesUnderContinuousConflict asserts the
// retry bound, and that a batch that never applied left nothing behind: no
// sentinel, no flag, and the account still promotable once contention stops.
func TestPromoteToSystemUser_ExhaustsRetriesUnderContinuousConflict(t *testing.T) {
	store, kv, legacy := newPromotionStore(t)
	ctx := context.Background()
	attempts := 0
	kv.beforeBatch = func() {
		attempts++
		renameRecord(t, kv, legacy.ID, "rewritten-"+strconv.Itoa(attempts))
	}

	_, err := store.PromoteToSystemUser(ctx, legacy.ID)
	var conflict *ConcurrentUpdateError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %T (%v), want *ConcurrentUpdateError", err, err)
	}
	if attempts != certBindingMutationAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, certBindingMutationAttempts)
	}
	if owner := store.existingSystemUserID(ctx); owner != 0 {
		t.Fatalf("sentinel names %d after every batch failed its guard, want it absent", owner)
	}

	kv.beforeBatch = nil
	promoted, err := store.PromoteToSystemUser(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("PromoteToSystemUser after exhaustion: %v", err)
	}
	if !promoted.System {
		t.Error("expected the promotion to succeed once contention stopped")
	}
}

// TestPromoteToSystemUser_RefusesADifferentSystemUser asserts a sentinel held
// by another record refuses the promotion, naming that record.
func TestPromoteToSystemUser_RefusesADifferentSystemUser(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	store := svc.(*kvUserStore)

	existing, err := store.CreateUser(ctx, &CreateUserRequest{
		Username: "admin", WithoutEmailLoginKey: true, System: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(System): %v", err)
	}
	other, err := store.CreateUser(ctx, &CreateUserRequest{
		Username: "second", WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}

	_, err = store.PromoteToSystemUser(ctx, other.ID)
	var already *SystemUserAlreadyExistsError
	if !errors.As(err, &already) {
		t.Fatalf("err = %T (%v), want *SystemUserAlreadyExistsError", err, err)
	}
	if already.ExistingID != existing.ID {
		t.Errorf("ExistingID = %d, want %d", already.ExistingID, existing.ID)
	}
}

// TestPromoteToSystemUser_SentinelClaimedMidPromotionRefuses lands a
// different record's sentinel claim between the promotion's read and its
// batch. The sentinel guard must fail the batch, and the retry must refuse
// rather than write a second System user.
func TestPromoteToSystemUser_SentinelClaimedMidPromotionRefuses(t *testing.T) {
	store, kv, legacy := newPromotionStore(t)
	ctx := context.Background()
	const rival = uint64(424242)
	kv.beforeBatch = func() {
		kv.beforeBatch = nil
		if err := kv.Put(ctx, keySystemSentinel, encodeID(rival)); err != nil {
			t.Fatalf("claim sentinel: %v", err)
		}
	}

	_, err := store.PromoteToSystemUser(ctx, legacy.ID)
	var already *SystemUserAlreadyExistsError
	if !errors.As(err, &already) || already.ExistingID != rival {
		t.Fatalf("err = %v, want *SystemUserAlreadyExistsError naming %d", err, rival)
	}
	got, err := store.GetUser(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.System {
		t.Fatal("the record was promoted although another record holds the sentinel")
	}
}

// TestPromoteToSystemUser_AmbiguousBatchIsReportedAndRetryable asserts a batch
// error is reported as an unknown outcome, never as a failure the caller could
// act on by giving up, and that a retry answers from what actually committed.
func TestPromoteToSystemUser_AmbiguousBatchIsReportedAndRetryable(t *testing.T) {
	store, kv, legacy := newPromotionStore(t)
	ctx := context.Background()
	kv.batchErr = func() error { return errors.New("simulated lost response") }

	_, err := store.PromoteToSystemUser(ctx, legacy.ID)
	var unknown *CommitOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.ID != legacy.ID {
		t.Fatalf("err = %v, want *CommitOutcomeUnknownError for %d", err, legacy.ID)
	}

	kv.batchErr = nil
	promoted, err := store.PromoteToSystemUser(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !promoted.System || store.existingSystemUserID(ctx) != legacy.ID {
		t.Fatalf("retry left System=%v sentinel=%d, want both naming %d", promoted.System, store.existingSystemUserID(ctx), legacy.ID)
	}
}

// TestPromoteToSystemUser_RequiresGuardedBatch asserts a store that cannot
// write the sentinel and the record in one guarded batch is refused, rather
// than promoted in two steps a crash could split.
func TestPromoteToSystemUser_RequiresGuardedBatch(t *testing.T) {
	svcAny, err := NewStore(newFakeKVStore(), newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store := svcAny.(*kvUserStore)
	ctx := context.Background()
	legacy, err := store.CreateUser(ctx, &CreateUserRequest{Username: "admin", WithoutEmailLoginKey: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	_, err = store.PromoteToSystemUser(ctx, legacy.ID)
	if !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("err = %v, want ErrStoreNotAtomic", err)
	}
	if owner := store.existingSystemUserID(ctx); owner != 0 {
		t.Fatalf("sentinel names %d, want nothing written", owner)
	}
}

// TestPromoteToSystemUser_AlreadySystemWithForeignSentinelRefuses asserts a
// record already flagged System while the sentinel names a different record
// is reported, not silently accepted as a second System user.
func TestPromoteToSystemUser_AlreadySystemWithForeignSentinelRefuses(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	store := svc.(*kvUserStore)
	existing, err := store.CreateUser(ctx, &CreateUserRequest{Username: "admin", WithoutEmailLoginKey: true, System: true})
	if err != nil {
		t.Fatalf("CreateUser(System): %v", err)
	}
	if err := store.store.Put(ctx, keySystemSentinel, encodeID(existing.ID+1)); err != nil {
		t.Fatalf("overwrite sentinel: %v", err)
	}

	_, err = store.PromoteToSystemUser(ctx, existing.ID)
	var already *SystemUserAlreadyExistsError
	if !errors.As(err, &already) || already.ExistingID != existing.ID+1 {
		t.Fatalf("err = %v, want *SystemUserAlreadyExistsError naming %d", err, existing.ID+1)
	}
}

// TestPromoteToSystemUser_AlreadySystemRepairsMissingSentinel is the
// round-2 regression test: a record that is already System: true must
// still (best-effort) repair the sentinel if it does not already name this
// id, so a LATER caller cannot claim it for a different record and produce
// a second System user.
func TestPromoteToSystemUser_AlreadySystemRepairsMissingSentinel(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	store := svc.(*kvUserStore)

	existing, err := store.CreateUser(ctx, &CreateUserRequest{
		Username: "admin", WithoutEmailLoginKey: true, System: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(System): %v", err)
	}

	// Simulate a corrupted/missing sentinel: release it directly, as if it
	// had never been written or had been manually deleted.
	atomicStore, ok := store.store.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("setup: store does not implement kvstore.AtomicKVStore")
	}
	if _, err := atomicStore.CompareAndDelete(ctx, keySystemSentinel, encodeID(existing.ID)); err != nil {
		t.Fatalf("setup: release sentinel: %v", err)
	}
	if owner := store.existingSystemUserID(ctx); owner != 0 {
		t.Fatalf("setup: sentinel still resolves to %d, want it cleared", owner)
	}

	got, err := store.PromoteToSystemUser(ctx, existing.ID)
	if err != nil {
		t.Fatalf("PromoteToSystemUser(already System) = %v, want nil", err)
	}
	if !got.System {
		t.Fatal("expected the already-System record back unchanged")
	}

	// The sentinel must now be repaired to name this record again.
	if owner := store.existingSystemUserID(ctx); owner != existing.ID {
		t.Errorf("sentinel owner = %d, want %d (repaired)", owner, existing.ID)
	}

	// And the repair must not have disturbed a DIFFERENT record's own,
	// legitimate hold: promoting a second id while this one is System must
	// still be refused, naming the repaired holder.
	other, err := store.CreateUser(ctx, &CreateUserRequest{
		Username: "second", WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(second): %v", err)
	}
	_, err = store.PromoteToSystemUser(ctx, other.ID)
	var already *SystemUserAlreadyExistsError
	if !errors.As(err, &already) {
		t.Fatalf("err = %T (%v), want *SystemUserAlreadyExistsError", err, err)
	}
	if already.ExistingID != existing.ID {
		t.Errorf("ExistingID = %d, want %d", already.ExistingID, existing.ID)
	}
}

// TestCreateUser_AtomicSystemClaimsSentinelInTheSameBatch covers the System
// flag on the atomic create path (the one the system tenant's store uses in a
// cluster): the record and the sentinel commit together, a second System
// create is refused by the sentinel guard, and a repeated create of the same
// username still reports UsernameExistsError first.
func TestCreateUser_AtomicSystemClaimsSentinelInTheSameBatch(t *testing.T) {
	kv := newMemKVStore()
	store := newAtomicTestStore(t, kv).(*kvUserStore)
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, &CreateUserRequest{Username: "admin", WithoutEmailLoginKey: true, System: true})
	if err != nil {
		t.Fatalf("CreateUser(System): %v", err)
	}
	if !admin.System || store.existingSystemUserID(ctx) != admin.ID {
		t.Fatalf("System=%v sentinel=%d, want both naming %d", admin.System, store.existingSystemUserID(ctx), admin.ID)
	}

	tests := []struct {
		name  string
		req   *CreateUserRequest
		check func(t *testing.T, err error)
	}{
		{name: "second System user", req: &CreateUserRequest{Username: "other", WithoutEmailLoginKey: true, System: true}, check: func(t *testing.T, err error) {
			var already *SystemUserAlreadyExistsError
			if !errors.As(err, &already) || already.ExistingID != admin.ID {
				t.Fatalf("err = %v, want *SystemUserAlreadyExistsError naming %d", err, admin.ID)
			}
		}},
		{name: "same username again", req: &CreateUserRequest{Username: "admin", WithoutEmailLoginKey: true, System: true}, check: func(t *testing.T, err error) {
			var exists *UsernameExistsError
			if !errors.As(err, &exists) {
				t.Fatalf("err = %v, want *UsernameExistsError", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.CreateUser(ctx, tt.req)
			tt.check(t, err)
		})
	}
	if _, err := store.GetUserByUsername(ctx, tenantid.System, "other"); err == nil {
		t.Fatal("the refused System create left a record behind")
	}
}
