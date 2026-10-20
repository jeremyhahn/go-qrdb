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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

var errResolveFault = errors.New("injected resolve fault")

// faultingResolver fails every Resolve, as a sealed or unreachable system
// store does.
type faultingResolver struct{}

func (faultingResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nil, errResolveFault
}

// plainKVStore hides every capability of the store it wraps but KVStore
// itself, so the index sees a store without compare-and-set.
type plainKVStore struct {
	kvstore.KVStore
}

func newAtomicIdentityIndex(t *testing.T) IdentityIndex {
	t.Helper()
	backing, err := kvstore.NewNamespacedKVStore(
		kvstore.NewMemoryKVStore(), IdentityNamespace(tenantid.System))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	index, err := NewIdentityIndex(backing)
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	return index
}

// IdentityID is the decimal form the index keys a numeric id by.
func TestIdentityID_IsDecimal(t *testing.T) {
	for id, want := range map[uint64]string{0: "0", 42: "42", 18446744073709551615: "18446744073709551615"} {
		if got := IdentityID(id); got != want {
			t.Errorf("IdentityID(%d) = %q, want %q", id, got, want)
		}
	}
}

// Both constructors refuse a nil source rather than fail on first use.
func TestIdentityIndex_ConstructorsRefuseNil(t *testing.T) {
	if _, err := NewIdentityIndex(nil); !errors.Is(err, ErrNilIdentityIndexStore) {
		t.Errorf("NewIdentityIndex(nil) err = %v, want ErrNilIdentityIndexStore", err)
	}
	if _, err := NewResolvedIdentityIndex(nil); !errors.Is(err, ErrNilIdentityIndexResolver) {
		t.Errorf("NewResolvedIdentityIndex(nil) err = %v, want ErrNilIdentityIndexResolver", err)
	}
}

// Every operation refuses an empty id, and a release refuses an empty token,
// before it touches the store.
func TestIdentityIndex_RefusesEmptyIDAndToken(t *testing.T) {
	ctx := context.Background()
	index := newAtomicIdentityIndex(t)

	if _, err := index.Record(ctx, IdentityUser, "", "acme"); !errors.Is(err, ErrEmptyIdentityID) {
		t.Errorf("Record(\"\") err = %v, want ErrEmptyIdentityID", err)
	}
	if err := index.Forget(ctx, IdentityUser, "", "tok"); !errors.Is(err, ErrEmptyIdentityID) {
		t.Errorf("Forget(\"\") err = %v, want ErrEmptyIdentityID", err)
	}
	if err := index.Forget(ctx, IdentityUser, "1", ""); !errors.Is(err, ErrEmptyClaimToken) {
		t.Errorf("Forget(token \"\") err = %v, want ErrEmptyClaimToken", err)
	}
	_, _, err := index.Lookup(ctx, IdentityUser, "")
	var ie *IdentityIndexError
	if !errors.As(err, &ie) || !errors.Is(err, ErrEmptyIdentityID) {
		t.Fatalf("Lookup(\"\") err = %v, want *IdentityIndexError wrapping ErrEmptyIdentityID", err)
	}
	if ie.Op != "lookup" || !strings.Contains(err.Error(), "identity index lookup failed for user") {
		t.Errorf("Lookup(\"\") err = %q (op %q), want it to name the lookup of a user", err, ie.Op)
	}
}

// A store the resolver cannot hand out fails every operation with the
// resolver's own cause, never a silent absence.
func TestIdentityIndex_ResolveFailurePropagates(t *testing.T) {
	ctx := context.Background()
	index, err := NewResolvedIdentityIndex(faultingResolver{})
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := index.Record(ctx, IdentityRole, "r", "acme"); !errors.Is(err, errResolveFault) {
		t.Errorf("Record err = %v, want the resolve fault", err)
	}
	if err := index.Forget(ctx, IdentityRole, "r", "tok"); !errors.Is(err, errResolveFault) {
		t.Errorf("Forget err = %v, want the resolve fault", err)
	}
	if _, held, err := index.Lookup(ctx, IdentityRole, "r"); held || !errors.Is(err, errResolveFault) {
		t.Errorf("Lookup = (held=%v, err=%v), want the resolve fault", held, err)
	}
}

// Without compare-and-set neither a claim nor a release can be made safely,
// so both refuse.
func TestIdentityIndex_RefusesANonAtomicStore(t *testing.T) {
	ctx := context.Background()
	index, err := NewIdentityIndex(plainKVStore{KVStore: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	if _, err := index.Record(ctx, IdentityUser, "1", "acme"); !errors.Is(err, ErrIdentityStoreNotAtomic) {
		t.Errorf("Record err = %v, want ErrIdentityStoreNotAtomic", err)
	}
	if err := index.Forget(ctx, IdentityUser, "1", "tok"); !errors.Is(err, ErrIdentityStoreNotAtomic) {
		t.Errorf("Forget err = %v, want ErrIdentityStoreNotAtomic", err)
	}
}

// A release of an id nobody holds has nothing to do and is not an error.
func TestIdentityIndex_ForgetOfAnAbsentClaimIsANoop(t *testing.T) {
	if err := newAtomicIdentityIndex(t).Forget(context.Background(), IdentityUser, "never", "tok"); err != nil {
		t.Fatalf("Forget of an absent claim = %v, want nil", err)
	}
}

// ReconcileDeletedIdentity releases only the owner's own surviving claim.
func TestReconcileDeletedIdentity_ReleasesOnlyTheOwnersClaim(t *testing.T) {
	ctx := context.Background()
	index := newAtomicIdentityIndex(t)
	if _, err := index.Record(ctx, IdentityUser, "own", "acme"); err != nil {
		t.Fatalf("Record(own): %v", err)
	}
	if _, err := index.Record(ctx, IdentityUser, "foreign", "globex"); err != nil {
		t.Fatalf("Record(foreign): %v", err)
	}
	if _, err := index.Record(ctx, IdentityUser, "sys", tenantid.System); err != nil {
		t.Fatalf("Record(sys): %v", err)
	}

	for _, tc := range []struct {
		name  string
		index IdentityIndex
		id    string
		owner string
		want  bool
	}{
		{name: "nil index", index: nil, id: "own", owner: "acme"},
		{name: "empty id", index: index, id: "", owner: "acme"},
		{name: "absent", index: index, id: "never", owner: "acme"},
		{name: "foreign holder", index: index, id: "foreign", owner: "acme"},
		{name: "own claim", index: index, id: "own", owner: "acme", want: true},
		{name: "own claim again", index: index, id: "own", owner: "acme"},
		{name: "system spelled empty", index: index, id: "sys", owner: "", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReconcileDeletedIdentity(ctx, tc.index, IdentityUser, tc.id, tc.owner)
			if err != nil || got != tc.want {
				t.Fatalf("ReconcileDeletedIdentity = (%v, %v), want (%v, nil)", got, err, tc.want)
			}
		})
	}
	if claim, held, err := index.Lookup(ctx, IdentityUser, "foreign"); err != nil || !held || claim.Tenant != "globex" {
		t.Fatalf("foreign claim after reconcile = (%q, %v, %v); another tenant's claim was released",
			claim.Tenant, held, err)
	}
}

// A lookup failure is reported, not read as "nothing to release".
func TestReconcileDeletedIdentity_LookupFailurePropagates(t *testing.T) {
	index, err := NewResolvedIdentityIndex(faultingResolver{})
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	released, err := ReconcileDeletedIdentity(context.Background(), index, IdentityRole, "r", "acme")
	if released || !errors.Is(err, errResolveFault) {
		t.Fatalf("ReconcileDeletedIdentity = (%v, %v), want (false, the resolve fault)", released, err)
	}
}

// OwningTenant routes to the holder only when the caller may reach it, and
// otherwise to the caller's own tenant, so it is no cross-tenant oracle.
func TestOwningTenant_RoutesToTheClaimOrTheCaller(t *testing.T) {
	index := newAtomicIdentityIndex(t)
	if _, err := index.Record(context.Background(), IdentityRole, "acme-role", "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	acme := WithTenantID(context.Background(), "acme")
	globex := WithTenantID(context.Background(), "globex")

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		index IdentityIndex
		id    string
		want  string
	}{
		{name: "nil index", ctx: acme, index: nil, id: "acme-role", want: "acme"},
		{name: "empty id", ctx: globex, index: index, id: "", want: "globex"},
		{name: "unclaimed", ctx: globex, index: index, id: "never", want: "globex"},
		{name: "own claim", ctx: acme, index: index, id: "acme-role", want: "acme"},
		{name: "foreign claim", ctx: globex, index: index, id: "acme-role", want: "globex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OwningTenant(tc.ctx, tc.index, IdentityRole, tc.id, "get role")
			if err != nil || got != tc.want {
				t.Fatalf("OwningTenant = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

// A lookup failure fails the routing closed rather than falling back to the
// caller's tenant.
func TestOwningTenant_LookupFailurePropagates(t *testing.T) {
	index, err := NewResolvedIdentityIndex(faultingResolver{})
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	got, err := OwningTenant(WithTenantID(context.Background(), "acme"), index, IdentityUser, "7", "get user")
	if got != "" || !errors.Is(err, errResolveFault) {
		t.Fatalf("OwningTenant = (%q, %v), want (\"\", the resolve fault)", got, err)
	}
}

// Each typed error names what it is about, and those that wrap expose the
// cause.
func TestTenantErrors_Messages(t *testing.T) {
	cause := errors.New("underlying")
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&PlatformOperatorRequiredError{IdentityTenantID: "acme", Op: "create a tenant"}, "tenant: caller in tenant acme is not the platform operator and cannot create a tenant"},
		{&VolumePoolReserveError{TenantID: "acme", Pool: "fast", Err: cause}, "tenant: could not reserve volume pool fast for tenant acme: underlying"},
		{&RosterRequiresShamirError{Strategy: "passphrase"}, "tenant: an inline officer roster requires --strategy shamir, got passphrase"},
		{&InvalidFactorPolicyError{Policy: "bogus"}, "tenant: invalid factor policy: bogus"},
		{&TenantDeletedError{TenantID: "acme"}, "tenant: id acme was deleted and cannot be reused"},
		{&TenantDeletedError{TenantID: "acme", DeletedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, "tenant: id acme was deleted at 2026-01-02T03:04:05Z and cannot be reused"},
		{&TenantUndoPendingError{TenantID: "acme"}, "tenant: id acme was undone and is not yet released; retry"},
		{&TenantUndoPendingError{TenantID: "acme", Pending: []string{"n1", "n2"}}, "tenant: id acme was undone and awaits cleanup by n1, n2; retry"},
		{&KeyspaceReclaimError{TenantID: "acme", Err: cause}, "tenant: keyspace reclaim for acme: underlying"},
		{&DatabaseListError{TenantID: "acme", Err: cause}, "tenant: list databases for acme: underlying"},
		{&StrictRequiresDedicatedStorageError{IsolationType: IsolationPrefix}, "tenant: strict isolation requires dedicated storage; isolation type " + string(IsolationPrefix) + " has none of its own"},
		{&IsolationPolicyChangeError{From: "strict", To: "standard"}, "tenant: isolation policy cannot move from strict to standard (tighten only)"},
		{&InvalidIsolationPolicyError{Value: "loose"}, `tenant: invalid isolation policy "loose"`},
		{&IdentityClaimConflictError{Kind: IdentityUser, ID: "9"}, "tenant: user id 9 is already claimed"},
		{&IdentityClaimStaleError{Kind: IdentityRole, ID: "r"}, "tenant: the role claim on r is no longer the one this release names"},
		{&IdentityIndexError{Op: "record", Kind: IdentityUser, ID: "9", Err: cause}, "tenant: identity index record failed for user 9: underlying"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, got, tc.want)
		}
	}
	for _, err := range []error{
		&VolumePoolReserveError{Err: cause},
		&KeyspaceReclaimError{Err: cause},
		&DatabaseListError{Err: cause},
		&IdentityIndexError{Err: cause},
	} {
		if !errors.Is(err, cause) {
			t.Errorf("errors.Is(%T, cause) = false; the cause is not unwrapped", err)
		}
	}
}
