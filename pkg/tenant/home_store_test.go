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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fakeHomeSource is a HomeStoreSource over one in-memory store per tenant.
type fakeHomeSource struct {
	mu        sync.Mutex
	stores    map[string]*memKVStore
	ensured   []string
	ensureErr error
	storeErr  error
	calls     int
}

func newFakeHomeSource() *fakeHomeSource {
	return &fakeHomeSource{stores: map[string]*memKVStore{}}
}

func (f *fakeHomeSource) EnsureHome(_ context.Context, tenantID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, tenantID)
	return f.ensureErr
}

func (f *fakeHomeSource) HomeStore(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.storeErr != nil {
		return nil, f.storeErr
	}
	s, ok := f.stores[tenantID]
	if !ok {
		s = newMemKVStore()
		f.stores[tenantID] = s
	}
	return s, nil
}

var _ HomeStoreSource = (*fakeHomeSource)(nil)

// getTenantFailingService is a TenantService whose registry cannot be read.
type getTenantFailingService struct {
	*TenantService
	err error
}

func (s *getTenantFailingService) GetTenant(context.Context, string) (*TenantInfo, error) {
	return nil, s.err
}

// homeFixture is a tenant service, a resolver over it with NO home source
// attached, the shared store and the barrier registry.
type homeFixture struct {
	shared   *memKVStore
	svc      *TenantService
	reg      *seal.BarrierRegistry
	resolver StoreResolver
	router   HomeRouter
}

func newHomeFixture(t *testing.T) *homeFixture {
	t.Helper()
	shared, svc, reg, resolver := newResolverFixture(t)
	router, ok := resolver.(HomeRouter)
	if !ok {
		t.Fatal("the default StoreResolver must implement HomeRouter")
	}
	router.SetHomeStoreSource(nil)
	return &homeFixture{shared: shared, svc: svc, reg: reg, resolver: resolver, router: router}
}

// createIsolated creates a barrier tenant with the given policy and unseals
// its barrier, so resolution reaches the home source.
func (f *homeFixture) createIsolated(t *testing.T, id string, policy TenantIsolationPolicy) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.svc.CreateTenant(ctx, id, id, IsolationBarrier, WithIsolationPolicy(policy)); err != nil {
		t.Fatalf("CreateTenant(%s): %v", id, err)
	}
	if err := f.reg.InitializeTenant(ctx, id, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(%s): %v", id, err)
	}
}

// putTenant persists a tenant record directly, bypassing CreateTenant's
// barrier registration, for tests that read routing metadata only.
func putTenant(t *testing.T, svc *TenantService, id string, iso IsolationType, policy TenantIsolationPolicy) {
	t.Helper()
	info := &TenantInfo{ID: id, Name: id, IsolationType: iso, IsolationPolicy: policy}
	if err := putTenantMeta(context.Background(), svc, info); err != nil {
		t.Fatalf("persist tenant %s: %v", id, err)
	}
}

// Every isolated tenant resolves to its home store whatever its policy and
// the floor's: an operator-access tenant's records are no more in the
// system shard than a strict one's.
func TestStoreResolver_IsolatedTenantResolvesToHomeStoreUnderEveryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		floor  ClusterIsolationPolicy
		policy TenantIsolationPolicy
	}{
		{"operator-access floor, inherit", ClusterIsolationPolicyOperatorAccess, TenantIsolationPolicyInherit},
		{"operator-access floor, strict", ClusterIsolationPolicyOperatorAccess, TenantIsolationPolicyStrict},
		{"strict floor, inherit", ClusterIsolationPolicyStrict, TenantIsolationPolicyInherit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHomeFixture(t)
			ctx := context.Background()
			if err := f.svc.SetClusterIsolationPolicy(ctx, tc.floor); err != nil {
				t.Fatalf("SetClusterIsolationPolicy: %v", err)
			}
			f.createIsolated(t, "vault", tc.policy)
			src := newFakeHomeSource()
			f.router.SetHomeStoreSource(src)

			got, err := f.resolver.Resolve(ctx, "vault")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if err := got.Put(ctx, "users/alice", []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			// The home store is the tenant's own: the key lands un-namespaced
			// in it, and at no key at all in the shared store.
			if _, ok := src.stores["vault"].data["users/alice"]; !ok {
				t.Fatal("write did not land in the tenant's home store")
			}
			for _, k := range []string{"users/alice", "vault/users/alice"} {
				if _, ok := f.shared.data[k]; ok {
					t.Fatalf("isolated tenant's record found in the shared store at %q", k)
				}
			}
		})
	}
}

func TestStoreResolver_IsolatedTenantFailsClosedWithoutSource(t *testing.T) {
	f := newHomeFixture(t)
	f.createIsolated(t, "vault", TenantIsolationPolicyInherit)

	_, err := f.resolver.Resolve(context.Background(), "vault")
	var unavailable *TenantHomeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Resolve without a home source = %v, want *TenantHomeUnavailableError", err)
	}
	if !errors.Is(err, ErrTenantSealed) || !errors.Is(err, ErrTenantHomeNotAttached) {
		t.Fatalf("err = %v, want it to match ErrTenantSealed and ErrTenantHomeNotAttached", err)
	}
}

func TestStoreResolver_IsolatedTenantFailsClosedOnSourceError(t *testing.T) {
	f := newHomeFixture(t)
	f.createIsolated(t, "vault", TenantIsolationPolicyStrict)
	src := newFakeHomeSource()
	src.storeErr = errors.New("not a replica")
	f.router.SetHomeStoreSource(src)

	_, err := f.resolver.Resolve(context.Background(), "vault")
	if !errors.Is(err, src.storeErr) || !errors.Is(err, ErrTenantSealed) {
		t.Fatalf("err = %v, want the source's error wrapped as ErrTenantSealed", err)
	}
}

func TestStoreResolver_DetachedSourceFailsClosedAgain(t *testing.T) {
	f := newHomeFixture(t)
	f.createIsolated(t, "vault", TenantIsolationPolicyInherit)
	f.router.SetHomeStoreSource(newFakeHomeSource())
	f.router.SetHomeStoreSource(nil)

	if _, err := f.resolver.Resolve(context.Background(), "vault"); !errors.Is(err, ErrTenantHomeNotAttached) {
		t.Fatalf("Resolve after detaching = %v, want ErrTenantHomeNotAttached", err)
	}
}

// A sealed or absent barrier refuses before the home source is consulted.
func TestStoreResolver_SealedTenantNeverReachesTheSource(t *testing.T) {
	f := newHomeFixture(t)
	ctx := context.Background()
	f.createIsolated(t, "vault", TenantIsolationPolicyInherit)
	putTenant(t, f.svc, "unregistered", IsolationBarrier, TenantIsolationPolicyInherit)
	src := newFakeHomeSource()
	f.router.SetHomeStoreSource(src)
	if err := f.reg.SealTenant("vault"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	for _, id := range []string{"vault", "unregistered"} {
		store, err := f.resolver.Resolve(ctx, id)
		if !errors.Is(err, ErrTenantSealed) || store != nil {
			t.Fatalf("Resolve(%s) = (%v, %v), want ErrTenantSealed and no store", id, store, err)
		}
	}
	if src.calls != 0 {
		t.Fatalf("home source consulted %d times for a tenant with no unsealed barrier", src.calls)
	}
}

func TestHomeLocator_InHomeFollowsTheRouting(t *testing.T) {
	f := newHomeFixture(t)
	locator, ok := f.resolver.(HomeLocator)
	if !ok {
		t.Fatal("the default StoreResolver must implement HomeLocator")
	}
	putTenant(t, f.svc, "vault", IsolationRaft, TenantIsolationPolicyStrict)
	putTenant(t, f.svc, "loose", IsolationBarrier, TenantIsolationPolicyInherit)
	putTenant(t, f.svc, "vol", IsolationVolume, TenantIsolationPolicyInherit)
	putTenant(t, f.svc, "prefixed", IsolationPrefix, TenantIsolationPolicyStrict)

	for _, tc := range []struct {
		tenant string
		want   bool
	}{
		{"", false},
		{"system", false},
		{"never-registered", false},
		{"prefixed", false},
		{"loose", true},
		{"vol", true},
		{"vault", true},
	} {
		got, err := locator.InHome(context.Background(), tc.tenant)
		if err != nil {
			t.Fatalf("InHome(%q): %v", tc.tenant, err)
		}
		if got != tc.want {
			t.Errorf("InHome(%q) = %v, want %v", tc.tenant, got, tc.want)
		}
	}
}

func TestHomeLocator_InHomeErrors(t *testing.T) {
	_, svc, reg, _ := newResolverFixture(t)
	lookupErr := errors.New("registry unreadable")
	failing, err := NewStoreResolver(newMemKVStore(), reg, &getTenantFailingService{TenantService: svc, err: lookupErr})
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	if _, err := failing.(HomeLocator).InHome(context.Background(), "loose"); !errors.Is(err, lookupErr) {
		t.Fatalf("InHome with an unreadable registry = %v, want the read error", err)
	}
}

func TestHomeRouter_EnsureHome_ProvisionsEveryIsolatedTenant(t *testing.T) {
	f := newHomeFixture(t)
	putTenant(t, f.svc, "vault", IsolationRaft, TenantIsolationPolicyStrict)
	putTenant(t, f.svc, "loose", IsolationBarrier, TenantIsolationPolicyInherit)
	putTenant(t, f.svc, "vol", IsolationVolume, TenantIsolationPolicyInherit)
	src := newFakeHomeSource()
	f.router.SetHomeStoreSource(src)

	for _, id := range []string{"vault", "loose", "vol"} {
		if err := f.router.EnsureHome(context.Background(), id); err != nil {
			t.Fatalf("EnsureHome(%s): %v", id, err)
		}
	}
	if len(src.ensured) != 3 {
		t.Fatalf("ensured = %v, want every isolated tenant", src.ensured)
	}
}

func TestHomeRouter_EnsureHome_NoOpForSharedTenant(t *testing.T) {
	f := newHomeFixture(t)
	putTenant(t, f.svc, "prefixed", IsolationPrefix, TenantIsolationPolicyInherit)
	src := newFakeHomeSource()
	f.router.SetHomeStoreSource(src)

	if err := f.router.EnsureHome(context.Background(), "prefixed"); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	if len(src.ensured) != 0 {
		t.Fatalf("a shared tenant must not get a home store, ensured = %v", src.ensured)
	}
}

func TestHomeRouter_EnsureHome_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown tenant", func(t *testing.T) {
		f := newHomeFixture(t)
		if err := f.router.EnsureHome(ctx, "ghost"); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("err = %v, want ErrTenantNotFound", err)
		}
	})

	t.Run("no source attached", func(t *testing.T) {
		f := newHomeFixture(t)
		putTenant(t, f.svc, "vol", IsolationVolume, TenantIsolationPolicyInherit)
		if err := f.router.EnsureHome(ctx, "vol"); !errors.Is(err, ErrTenantHomeNotAttached) {
			t.Fatalf("err = %v, want ErrTenantHomeNotAttached", err)
		}
	})

	t.Run("source fails", func(t *testing.T) {
		f := newHomeFixture(t)
		putTenant(t, f.svc, "vault", IsolationRaft, TenantIsolationPolicyStrict)
		src := newFakeHomeSource()
		src.ensureErr = errors.New("no quorum")
		f.router.SetHomeStoreSource(src)
		err := f.router.EnsureHome(ctx, "vault")
		var unavailable *TenantHomeUnavailableError
		if !errors.As(err, &unavailable) || !errors.Is(err, src.ensureErr) {
			t.Fatalf("err = %v, want *TenantHomeUnavailableError wrapping the source error", err)
		}
	})
}

func TestTenantHomeUnavailableError_Message(t *testing.T) {
	err := &TenantHomeUnavailableError{TenantID: "vault", Err: ErrTenantHomeNotAttached}
	if got := err.Error(); got != `tenant: home store of "vault" unavailable: `+ErrTenantHomeNotAttached.Error() {
		t.Fatalf("Error() = %q", got)
	}
}

func TestNewLocalHomes_RefusesANilRegistry(t *testing.T) {
	if _, err := NewLocalHomes(nil); !errors.Is(err, ErrNilRegistry) {
		t.Fatalf("NewLocalHomes(nil) = %v, want ErrNilRegistry", err)
	}
}

// The standalone home store is the tenant barrier's own backend, under the
// HomeName keyspace, through the resolver; nothing reaches the shared store.
func TestLocalHomes_RecordsLiveInTheTenantBarrier(t *testing.T) {
	f := newHomeFixture(t)
	ctx := context.Background()
	f.createIsolated(t, "vault", TenantIsolationPolicyInherit)
	homes, err := NewLocalHomes(f.reg)
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	f.router.SetHomeStoreSource(homes)
	if err := f.router.EnsureHome(ctx, "vault"); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}

	store, err := f.resolver.Resolve(ctx, "vault")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := store.Put(ctx, "users/alice", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := store.(kvstore.AtomicKVStore); !ok {
		t.Fatal("the local home store must keep the barrier's atomic capability")
	}
	tb, err := f.reg.GetTenant("vault")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	got, err := tb.Get(ctx, HomeName+"/users/alice")
	if err != nil || string(got) != "v" {
		t.Fatalf("barrier Get(%s/users/alice) = (%q, %v), want the record", HomeName, got, err)
	}
	if len(f.shared.data) != 0 {
		t.Fatalf("shared store holds %v; want nothing", sharedKeys(f.shared))
	}

	again, err := homes.HomeStore(ctx, "vault")
	if err != nil || again != store {
		t.Fatalf("HomeStore again = (%v, %v), want the cached store", again, err)
	}

	if err := f.reg.SealTenant("vault"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if _, err := again.Get(ctx, "users/alice"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Get on a sealed barrier = %v, want seal.ErrSealed", err)
	}
}

func TestLocalHomes_UnregisteredTenantFails(t *testing.T) {
	_, _, reg, _ := newResolverFixture(t)
	homes, err := NewLocalHomes(reg)
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	ctx := context.Background()
	if err := homes.EnsureHome(ctx, "ghost"); err == nil {
		t.Fatal("EnsureHome for a tenant with no barrier here must fail")
	}
	if _, err := homes.HomeStore(ctx, "ghost"); err == nil {
		t.Fatal("HomeStore for a tenant with no barrier here must fail")
	}
}
