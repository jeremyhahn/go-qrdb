// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// The bootstrap path builds its index over a directly-confined store before
// any resolver exists; the routed services build theirs over the resolver.
// If those two composed different keys, a user created through one would be
// unroutable through the other — the same write-path/read-path divergence
// that IdentityNamespace exists to close, one layer up. This test writes with
// each and reads with the other.
func TestIdentityIndex_ResolvedAndDirectAgreeOnTheKey(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	confined, err := kvstore.NewNamespacedKVStore(shared, IdentityNamespace(SystemTenantID))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	direct, err := NewIdentityIndex(confined)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	routed, err := NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}

	if _, err := direct.Record(ctx, IdentityUser, "42", "acme"); err != nil {
		t.Fatalf("direct Record: %v", err)
	}
	gotClaim, ok, err := routed.Lookup(ctx, IdentityUser, "42")
	if err != nil {
		t.Fatalf("routed Lookup: %v", err)
	}
	if !ok || gotClaim.Tenant != "acme" {
		t.Fatalf("routed Lookup = (%q, %v); an id recorded before the resolver "+
			"existed is unroutable through it, so the two sides key the index "+
			"differently", gotClaim.Tenant, ok)
	}

	if _, err := routed.Record(ctx, IdentityRole, "role-1", "globex"); err != nil {
		t.Fatalf("routed Record: %v", err)
	}
	gotClaim2, ok, err := direct.Lookup(ctx, IdentityRole, "role-1")
	if err != nil {
		t.Fatalf("direct Lookup: %v", err)
	}
	if !ok || gotClaim2.Tenant != "globex" {
		t.Fatalf("direct Lookup = (%q, %v), want (globex, true)", gotClaim2.Tenant, ok)
	}
}

// The entry must be reachable at exactly one physical key, inside the system
// tenant's identity namespace and outside pkg/placement's keyspace.
func TestIdentityIndex_PhysicalKeyAndPayload(t *testing.T) {
	shared, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()

	index, err := NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := index.Record(ctx, IdentityUser, "77", "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	const want = "system/__sys:identity_idx/tenant/user/77"
	raw, err := shared.Get(ctx, want)
	if err != nil {
		t.Fatalf("Get(%q): %v", want, err)
	}
	if raw == nil {
		t.Fatalf("no entry at %q; the index is not in the system tenant's identity namespace", want)
	}
	// The payload is the tenant plus an opaque generation token, and nothing
	// else. The token is what lets a release tell one claim of an id from the
	// next; the tenant prefix is what lets Lookup answer routing questions
	// from the stored bytes alone.
	payload := string(raw)
	if !strings.HasPrefix(payload, "acme\x00") {
		t.Fatalf("entry payload = %q, want the tenant id %q followed by a token",
			payload, "acme")
	}
	if tenantFromClaimToken(payload) != "acme" {
		t.Fatalf("payload %q does not name tenant %q", payload, "acme")
	}
	// Still carries nothing ELSE: the entry lives outside every barrier.
	if strings.Contains(payload, "/") || strings.Count(payload, "\x00") != 1 {
		t.Fatalf("payload %q carries more than the tenant and its token", payload)
	}
	if old, oerr := shared.Get(ctx, "system/__sys:placement/user/77"); oerr == nil && old != nil {
		t.Fatal("the identity index wrote under __sys:placement/, which belongs to pkg/placement")
	}
}

// A user id and a role id that happen to be equal must not resolve each
// other: the kind is a key segment, not a comment.
func TestIdentityIndex_KindsAreDisjoint(t *testing.T) {
	_, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()
	index, err := NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}

	if _, err := index.Record(ctx, IdentityUser, "shared-id", "acme"); err != nil {
		t.Fatalf("Record(user): %v", err)
	}
	if _, err := index.Record(ctx, IdentityRole, "shared-id", "globex"); err != nil {
		t.Fatalf("Record(role): %v", err)
	}

	gotUserClaim, _, err := index.Lookup(ctx, IdentityUser, "shared-id")
	if err != nil {
		t.Fatalf("Lookup(user): %v", err)
	}
	gotRoleClaim, _, err := index.Lookup(ctx, IdentityRole, "shared-id")
	if err != nil {
		t.Fatalf("Lookup(role): %v", err)
	}
	if gotUserClaim.Tenant != "acme" || gotRoleClaim.Tenant != "globex" {
		t.Fatalf("user=%q role=%q; the two families overwrote each other", gotUserClaim.Tenant, gotRoleClaim.Tenant)
	}
}

// "" and SystemTenantID are the same tenant. Storing them as two different
// values would route the same id two different ways depending on which call
// wrote it — the split brain #41 already had to fix once in the namespace.
func TestIdentityIndex_EmptyTenantCanonicalizes(t *testing.T) {
	_, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()
	index, err := NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}

	if _, err := index.Record(ctx, IdentityUser, "1", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	gotClaim, ok, err := index.Lookup(ctx, IdentityUser, "1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok || gotClaim.Tenant != SystemTenantID {
		t.Fatalf("Lookup = (%q, %v), want (%q, true)", gotClaim.Tenant, ok, SystemTenantID)
	}

	// The discriminating assertion: a claim taken as "" is STORED under
	// SystemTenantID, so Lookup reports the canonical tenant and the token it
	// returns releases it. Without canonicalization the stored tenant would be
	// "" and the two spellings would be different holders.
	if gotClaim.Tenant != SystemTenantID {
		t.Fatalf("claim taken as \"\" names tenant %q, want %q",
			gotClaim.Tenant, SystemTenantID)
	}
	if err := index.Forget(ctx, IdentityUser, "1", gotClaim.Token); err != nil {
		t.Fatalf("Forget with the claim's own token: %v", err)
	}
	if _, ok, lerr := index.Lookup(ctx, IdentityUser, "1"); lerr != nil || ok {
		t.Fatalf("claim after release = (ok=%v, err=%v), want released", ok, lerr)
	}

	// And the mirror image: taken as SystemTenantID, it is the same holder.
	tok, rerr := index.Record(ctx, IdentityUser, "2", SystemTenantID)
	if rerr != nil {
		t.Fatalf("Record(%q): %v", SystemTenantID, rerr)
	}
	claim2, ok2, lerr2 := index.Lookup(ctx, IdentityUser, "2")
	if lerr2 != nil || !ok2 || claim2.Tenant != SystemTenantID {
		t.Fatalf("claim = (%q, %v, %v), want the canonical system tenant",
			claim2.Tenant, ok2, lerr2)
	}
	if err := index.Forget(ctx, IdentityUser, "2", tok); err != nil {
		t.Fatalf("Forget(%q): %v", SystemTenantID, err)
	}
}

// Forget releases only a claim the named tenant holds. Otherwise one tenant's
// delete path could free an id another tenant holds and put it up for capture.
//
// The routers cannot currently reach this path — a delete loads the record
// from the caller's own store first, so a non-holder never gets as far as
// Forget — which is exactly why the guard is asserted HERE, at the index,
// rather than through a router test that would pass with the guard removed.
func TestIdentityIndex_ForgetRefusesANonHolder(t *testing.T) {
	_, _, _, resolver := newResolverFixture(t)
	ctx := context.Background()
	index, err := NewResolvedIdentityIndex(resolver)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	token, rerr := index.Record(ctx, IdentityRole, "held", "acme")
	if rerr != nil {
		t.Fatalf("Record: %v", rerr)
	}

	// A release naming ANY token but the stored one is refused. That covers a
	// foreign tenant's release and a stale generation of this tenant's own
	// alike: what matters is that the token does not identify the claim that
	// is actually there.
	var stale *IdentityClaimStaleError
	if err := index.Forget(ctx, IdentityRole, "held", token+"-not-this-one"); !errors.As(err, &stale) {
		t.Fatalf("release with a wrong token = %v, want *IdentityClaimStaleError", err)
	}
	claim, held, lerr := index.Lookup(ctx, IdentityRole, "held")
	if lerr != nil || !held || claim.Tenant != "acme" {
		t.Fatalf("after the refused release, claim = (%q, %v, %v); it was freed",
			claim.Tenant, held, lerr)
	}

	// The holder's own token still releases it.
	if err := index.Forget(ctx, IdentityRole, "held", token); err != nil {
		t.Fatalf("release with the claim's own token: %v", err)
	}
	if _, still, serr := index.Lookup(ctx, IdentityRole, "held"); serr != nil || still {
		t.Fatalf("claim after its holder released it = (ok=%v, err=%v)", still, serr)
	}
}

// The daemon's system store resolves PutIfAbsent inside the Raft state
// machine. Assert the index actually USES that path when it is available,
// rather than a read-then-write that two nodes could both pass.
func TestIdentityIndex_ClaimUsesCompareAndSetWhenAvailable(t *testing.T) {
	ctx := context.Background()
	// kvstore.MemoryKVStore offers compare-and-set, as every production
	// system store does (cmd/qrdbd's kvServiceAdapter and lifecycle's
	// kvStoreAdapter both resolve PutIfAbsent inside the state machine).
	backing, err := kvstore.NewNamespacedKVStore(
		kvstore.NewMemoryKVStore(), IdentityNamespace(SystemTenantID))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	atomicBacking, ok := backing.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("a namespaced store over an atomic inner store must itself be atomic")
	}

	counting := &countingAtomicStore{KVStore: backing, atomic: atomicBacking}
	index, err := NewIdentityIndex(counting)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	if _, err := index.Record(ctx, IdentityUser, "cas", "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if counting.putIfAbsent == 0 {
		t.Fatal("Record did not use PutIfAbsent on a store that offers it; the " +
			"claim is a read-then-write two nodes could both win")
	}
	if counting.put != 0 {
		t.Fatalf("Record used an unconditional Put %d times on an atomic store", counting.put)
	}
}

// countingAtomicStore counts which write primitive the index reaches for.
type countingAtomicStore struct {
	kvstore.KVStore
	atomic      kvstore.AtomicKVStore
	put         int
	putIfAbsent int
}

func (c *countingAtomicStore) Put(ctx context.Context, key string, value []byte) error {
	c.put++
	return c.KVStore.Put(ctx, key, value)
}

func (c *countingAtomicStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	c.putIfAbsent++
	return c.atomic.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (c *countingAtomicStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (c *countingAtomicStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// The conflict path has TWO implementations — the compare-and-set branch and
// the serial branch for stores without it — and the tests above exercise only
// the serial one, because every fixture store lacks CAS. The production
// system store HAS CAS, so this pins the branch that actually runs in the
// daemon.
func TestIdentityIndex_ClaimRefusesAForeignHolderOnAnAtomicStore(t *testing.T) {
	ctx := context.Background()
	backing, err := kvstore.NewNamespacedKVStore(
		kvstore.NewMemoryKVStore(), IdentityNamespace(SystemTenantID))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	if _, ok := backing.(kvstore.AtomicKVStore); !ok {
		t.Fatal("this test must run against an atomic store or it pins the wrong branch")
	}
	index, err := NewIdentityIndex(backing)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}

	const contested = "3f7c1a90-contested-role-uuid"
	if _, err := index.Record(ctx, IdentityRole, contested, "acme"); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	_, err = index.Record(ctx, IdentityRole, contested, "globex")
	var conflict *IdentityClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("foreign claim on an atomic store = %v, want *IdentityClaimConflictError", err)
	}
	// The holder's own re-claim is refused on this branch too: a claim
	// succeeds only for the call that created the entry.
	if _, err := index.Record(ctx, IdentityRole, contested, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("re-claim by the holder on an atomic store = %v, want a conflict", err)
	}
	holderClaim, ok, lerr := index.Lookup(ctx, IdentityRole, contested)
	if lerr != nil || !ok || holderClaim.Tenant != "acme" {
		t.Fatalf("holderClaim.Tenant = (%q, %v, %v), want (acme, true, nil)", holderClaim.Tenant, ok, lerr)
	}
}

// Forget's holder guard likewise has to be pinned on the atomic store, since
// the release itself is unconditional there too.
func TestIdentityIndex_ForgetRefusesANonHolderOnAnAtomicStore(t *testing.T) {
	ctx := context.Background()
	backing, err := kvstore.NewNamespacedKVStore(
		kvstore.NewMemoryKVStore(), IdentityNamespace(SystemTenantID))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	index, err := NewIdentityIndex(backing)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	token, rerr := index.Record(ctx, IdentityRole, "held", "acme")
	if rerr != nil {
		t.Fatalf("Record: %v", rerr)
	}

	// A release naming ANY token but the stored one is refused. That covers a
	// foreign tenant's release and a stale generation of this tenant's own
	// alike: what matters is that the token does not identify the claim that
	// is actually there.
	var stale *IdentityClaimStaleError
	if err := index.Forget(ctx, IdentityRole, "held", token+"-not-this-one"); !errors.As(err, &stale) {
		t.Fatalf("release with a wrong token = %v, want *IdentityClaimStaleError", err)
	}
	claim, held, lerr := index.Lookup(ctx, IdentityRole, "held")
	if lerr != nil || !held || claim.Tenant != "acme" {
		t.Fatalf("after the refused release, claim = (%q, %v, %v); it was freed",
			claim.Tenant, held, lerr)
	}

	// The holder's own token still releases it.
	if err := index.Forget(ctx, IdentityRole, "held", token); err != nil {
		t.Fatalf("release with the claim's own token: %v", err)
	}
	if _, still, serr := index.Lookup(ctx, IdentityRole, "held"); serr != nil || still {
		t.Fatalf("claim after its holder released it = (ok=%v, err=%v)", still, serr)
	}
}

// interleavingStore runs a hook between a CompareAndDelete's guard and the
// underlying store's, so a competing claim can be landed in the exact window
// a read-compare-delete release would leave open.
type interleavingStore struct {
	kvstore.AtomicKVStore
	beforeCompareAndDelete func()
}

func (s *interleavingStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if s.beforeCompareAndDelete != nil {
		hook := s.beforeCompareAndDelete
		s.beforeCompareAndDelete = nil
		hook()
	}
	return s.AtomicKVStore.CompareAndDelete(ctx, key, expected)
}

func (s *interleavingStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// THE ACCEPTANCE CASE for generation tokens: an ABA race the tenant alone
// cannot see.
//
// Two concurrent releases for tenant A both read the claim. The first
// succeeds. A concurrent create RE-USES the same caller-supplied id and
// writes a NEW claim — whose tenant is also A. A release guarded on the
// tenant would match that replacement and delete it, leaving a live record
// no by-id operation can reach. Guarded on the TOKEN, the stale releaser
// cannot match, and says so.
func TestIdentityIndex_AStaleReleaseCannotDeleteARecreatedClaim(t *testing.T) {
	ctx := context.Background()
	backing, err := kvstore.NewNamespacedKVStore(
		kvstore.NewMemoryKVStore(), IdentityNamespace(SystemTenantID))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	index, err := NewIdentityIndex(backing)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}

	const contested = "caller-supplied-role-id"

	// The first generation. Both releasers hold THIS token.
	firstToken, err := index.Record(ctx, IdentityRole, contested, "acme")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Releaser #1 completes.
	if ferr := index.Forget(ctx, IdentityRole, contested, firstToken); ferr != nil {
		t.Fatalf("first release: %v", ferr)
	}

	// The same tenant re-creates the same id: a NEW generation, same tenant.
	secondToken, err := index.Record(ctx, IdentityRole, contested, "acme")
	if err != nil {
		t.Fatalf("re-create after release: %v", err)
	}
	if secondToken == firstToken {
		t.Fatal("the re-created claim re-used the first token; generations are " +
			"indistinguishable and the ABA race is still open")
	}

	// Releaser #2 finally runs, still holding the FIRST token.
	ferr := index.Forget(ctx, IdentityRole, contested, firstToken)
	var stale *IdentityClaimStaleError
	if !errors.As(ferr, &stale) {
		t.Fatalf("stale release = %v, want *IdentityClaimStaleError", ferr)
	}
	if stale.ID != contested || stale.Kind != IdentityRole {
		t.Fatalf("stale error = %+v, want it to name the %s claim on %q",
			stale, IdentityRole, contested)
	}

	// THE POINT: the new claim survives.
	claim, held, lerr := index.Lookup(ctx, IdentityRole, contested)
	if lerr != nil {
		t.Fatalf("Lookup: %v", lerr)
	}
	if !held {
		t.Fatal("the re-created claim was DELETED by a release that had read " +
			"the previous one — the record it belongs to is now unroutable by id")
	}
	if claim.Token != secondToken {
		t.Fatalf("surviving token = %q, want the re-created claim's %q",
			claim.Token, secondToken)
	}
}

// The read-fault rule applies to a delete guard exactly as it does to a
// conditional put's: a failed compare must refuse, never delete.
func TestIdentityIndex_AFailedGuardReadDoesNotRelease(t *testing.T) {
	ctx := context.Background()
	backing, err := kvstore.NewNamespacedKVStore(
		kvstore.NewMemoryKVStore(), IdentityNamespace(SystemTenantID))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	atomicBacking := backing.(kvstore.AtomicKVStore) //nolint:errcheck // proved above in the sibling test

	faulting := &guardFaultStore{AtomicKVStore: atomicBacking}
	index, err := NewIdentityIndex(faulting)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	if _, rerr := index.Record(ctx, IdentityUser, "1", "acme"); rerr != nil {
		t.Fatalf("Record: %v", rerr)
	}

	faulting.failGuard = true
	err = index.Forget(ctx, IdentityUser, "1", "acme")
	faulting.failGuard = false
	if !errors.Is(err, errGuardFault) {
		t.Fatalf("Forget with a failing guard read = %v, want the fault propagated", err)
	}

	if holderClaim, held, lerr := index.Lookup(ctx, IdentityUser, "1"); lerr != nil || !held || holderClaim.Tenant != "acme" {
		t.Fatalf("claim after a FAILED guard read = (%q, %v, %v); the release "+
			"acted on a comparison it could not make", holderClaim.Tenant, held, lerr)
	}
}

var errGuardFault = errors.New("injected guard read fault")

// guardFaultStore fails the guarded delete's own read.
type guardFaultStore struct {
	kvstore.AtomicKVStore
	failGuard bool
}

func (s *guardFaultStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if s.failGuard {
		return false, errGuardFault
	}
	return s.AtomicKVStore.CompareAndDelete(ctx, key, expected)
}

func (s *guardFaultStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
