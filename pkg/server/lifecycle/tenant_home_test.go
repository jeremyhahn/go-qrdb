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

package lifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeHomeServices is a TenantHomeServices returning one bundle, or an error.
// joining holds the shard IDs whose local replica is still catching up.
type fakeHomeServices struct {
	svcs    *server.DatabaseServices
	err     error
	calls   atomic.Int32
	dbs     []string
	joining map[uint64]bool
	asked   []uint64
}

var _ TenantHomeServices = (*fakeHomeServices)(nil)

func (f *fakeHomeServices) Services(_ context.Context, _, db string) (*server.DatabaseServices, error) {
	f.calls.Add(1)
	f.dbs = append(f.dbs, db)
	if f.err != nil {
		return nil, f.err
	}
	return f.svcs, nil
}

func (f *fakeHomeServices) IsShardJoining(shardID uint64) bool {
	f.asked = append(f.asked, shardID)
	return f.joining[shardID]
}

// fakeHomeProvisioner is a TenantHomeProvisioner recording what it placed.
type fakeHomeProvisioner struct {
	err    error
	placed []string
}

func (f *fakeHomeProvisioner) ProvisionTenantHome(_ context.Context, tenantID string) (*placement.Placement, error) {
	f.placed = append(f.placed, tenantID)
	if f.err != nil {
		return nil, f.err
	}
	return &placement.Placement{TenantID: tenantID, Database: database.TenantHomeName}, nil
}

// homeBundle returns a services bundle whose KV answers Get with getErr.
func homeBundle(getErr func() error) *server.DatabaseServices {
	return &server.DatabaseServices{
		KV: &mockKVServicer{getFn: func(context.Context, string) ([]byte, error) {
			return nil, getErr()
		}},
		Transaction: newEvaluatingTxService(),
	}
}

func newTestTenantHomes(svcs TenantHomeServices, prov TenantHomeProvisioner) *TenantHomes {
	h := NewTenantHomes(svcs, prov)
	h.readyTimeout = 200 * time.Millisecond
	h.readyInterval = 5 * time.Millisecond
	return h
}

func TestTenantHomes_HomeStore_CachesPerBundle(t *testing.T) {
	ctx := context.Background()
	svcs := &fakeHomeServices{svcs: homeBundle(func() error { return nil })}
	h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})

	first, err := h.HomeStore(ctx, "vault")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	second, err := h.HomeStore(ctx, "vault")
	if err != nil {
		t.Fatalf("HomeStore (again): %v", err)
	}
	if first != second {
		t.Fatal("the adapter must be reused while the services bundle is unchanged")
	}
	if svcs.dbs[0] != database.TenantHomeName {
		t.Fatalf("Services asked for %q, want the home group %q", svcs.dbs[0], database.TenantHomeName)
	}

	// A retired shard comes back as a new bundle: the adapter is rebuilt.
	svcs.svcs = homeBundle(func() error { return nil })
	third, err := h.HomeStore(ctx, "vault")
	if err != nil {
		t.Fatalf("HomeStore (new bundle): %v", err)
	}
	if third == first {
		t.Fatal("the adapter must be rebuilt over a replaced services bundle")
	}
}

func TestTenantHomes_HomeStore_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("services fail", func(t *testing.T) {
		h := newTestTenantHomes(&fakeHomeServices{err: seal.ErrSealed}, &fakeHomeProvisioner{})
		if _, err := h.HomeStore(ctx, "vault"); !errors.Is(err, seal.ErrSealed) {
			t.Fatalf("err = %v, want seal.ErrSealed", err)
		}
	})

	t.Run("bundle without a transaction service", func(t *testing.T) {
		svcs := &fakeHomeServices{svcs: &server.DatabaseServices{KV: &mockKVServicer{}}}
		h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})
		if _, err := h.HomeStore(ctx, "vault"); !errors.Is(err, ErrTransactionServiceRequired) {
			t.Fatalf("err = %v, want ErrTransactionServiceRequired", err)
		}
	})
}

func TestTenantHomes_HomeStore_CatchingUpReplicaIsNotServed(t *testing.T) {
	ctx := context.Background()
	bundle := homeBundle(func() error { return nil })
	bundle.ShardID = 42
	svcs := &fakeHomeServices{svcs: bundle, joining: map[uint64]bool{42: true}}
	h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})

	_, err := h.HomeStore(ctx, "vault")
	var catchingUp *TenantHomeCatchingUpError
	if !errors.As(err, &catchingUp) || catchingUp.TenantID != "vault" {
		t.Fatalf("err = %v, want *TenantHomeCatchingUpError for vault", err)
	}
	if len(svcs.asked) != 1 || svcs.asked[0] != 42 {
		t.Fatalf("join state asked for shards %v, want [42], the home group's", svcs.asked)
	}

	// Once the replica has caught up the same node serves.
	svcs.joining[42] = false
	if _, err := h.HomeStore(ctx, "vault"); err != nil {
		t.Fatalf("HomeStore after catching up: %v", err)
	}
}

func TestTenantHomes_HomeStore_CachedStoreIsNotServedWhileCatchingUp(t *testing.T) {
	ctx := context.Background()
	bundle := homeBundle(func() error { return nil })
	bundle.ShardID = 7
	svcs := &fakeHomeServices{svcs: bundle, joining: map[uint64]bool{}}
	h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})

	if _, err := h.HomeStore(ctx, "vault"); err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	// A replica re-entering a join window (a node replaced and re-added) is
	// refused even though a store was built over the bundle before.
	svcs.joining[7] = true
	var catchingUp *TenantHomeCatchingUpError
	if _, err := h.HomeStore(ctx, "vault"); !errors.As(err, &catchingUp) {
		t.Fatalf("err = %v, want *TenantHomeCatchingUpError", err)
	}
}

func TestTenantHomeCatchingUpError_Message(t *testing.T) {
	err := &TenantHomeCatchingUpError{TenantID: "vault"}
	if got, want := err.Error(), "lifecycle: tenant vault home replica on this node is catching up"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestTenantHomes_EnsureHome_WaitsForARead(t *testing.T) {
	var reads atomic.Int32
	notYet := errors.New("no leader")
	svcs := &fakeHomeServices{svcs: homeBundle(func() error {
		if reads.Add(1) < 3 {
			return notYet
		}
		return nil
	})}
	prov := &fakeHomeProvisioner{}
	h := newTestTenantHomes(svcs, prov)

	if err := h.EnsureHome(context.Background(), "vault"); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	if len(prov.placed) != 1 || prov.placed[0] != "vault" {
		t.Fatalf("placed = %v, want [vault]", prov.placed)
	}
	if reads.Load() < 3 {
		t.Fatalf("EnsureHome returned after %d reads, before the group served one", reads.Load())
	}
}

func TestTenantHomes_EnsureHome_SealedIsDeferred(t *testing.T) {
	prov := &fakeHomeProvisioner{}
	h := newTestTenantHomes(&fakeHomeServices{err: &server.RaftBackendError{Op: "start", Err: seal.ErrSealed}}, prov)

	if err := h.EnsureHome(context.Background(), "vault"); err != nil {
		t.Fatalf("EnsureHome with a sealed barrier = %v, want nil (replicas start at unseal)", err)
	}
	if len(prov.placed) != 1 {
		t.Fatal("a sealed tenant's home group must still be placed")
	}
}

func TestTenantHomes_EnsureHome_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("provisioning fails", func(t *testing.T) {
		placeErr := errors.New("no nodes")
		svcs := &fakeHomeServices{svcs: homeBundle(func() error { return nil })}
		h := newTestTenantHomes(svcs, &fakeHomeProvisioner{err: placeErr})
		if err := h.EnsureHome(ctx, "vault"); !errors.Is(err, placeErr) {
			t.Fatalf("err = %v, want the provisioning error", err)
		}
		if svcs.calls.Load() != 0 {
			t.Fatal("no replica may start for a home group that was not placed")
		}
	})

	t.Run("not a replica", func(t *testing.T) {
		startErr := errors.New("not a replica")
		h := newTestTenantHomes(&fakeHomeServices{err: startErr}, &fakeHomeProvisioner{})
		if err := h.EnsureHome(ctx, "vault"); !errors.Is(err, startErr) {
			t.Fatalf("err = %v, want the start error", err)
		}
	})

	t.Run("replica catching up", func(t *testing.T) {
		bundle := homeBundle(func() error { return nil })
		bundle.ShardID = 9
		h := newTestTenantHomes(&fakeHomeServices{svcs: bundle, joining: map[uint64]bool{9: true}}, &fakeHomeProvisioner{})
		var catchingUp *TenantHomeCatchingUpError
		if err := h.EnsureHome(ctx, "vault"); !errors.As(err, &catchingUp) {
			t.Fatalf("err = %v, want *TenantHomeCatchingUpError", err)
		}
	})

	t.Run("never serves", func(t *testing.T) {
		noLeader := errors.New("no leader")
		h := newTestTenantHomes(&fakeHomeServices{svcs: homeBundle(func() error { return noLeader })}, &fakeHomeProvisioner{})
		err := h.EnsureHome(ctx, "vault")
		var notReady *TenantHomeNotReadyError
		if !errors.As(err, &notReady) || !errors.Is(err, noLeader) {
			t.Fatalf("err = %v, want *TenantHomeNotReadyError wrapping the last probe error", err)
		}
		if got, want := notReady.Error(), "lifecycle: tenant vault home group not ready: no leader"; got != want {
			t.Fatalf("Error() = %q, want %q", got, want)
		}
	})
}

func TestTenantHomeNotReadyError_WithoutCause(t *testing.T) {
	err := &TenantHomeNotReadyError{TenantID: "vault"}
	if got, want := err.Error(), "lifecycle: tenant vault home group not ready"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap() must be nil without a cause")
	}
}

// homeRouterResolver is a tenant.StoreResolver that is also a HomeRouter.
type homeRouterResolver struct {
	src tenant.HomeStoreSource
}

func (r *homeRouterResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nil, nil
}

func (r *homeRouterResolver) SetHomeStoreSource(src tenant.HomeStoreSource) { r.src = src }

func (r *homeRouterResolver) EnsureHome(context.Context, string) error { return nil }

// plainResolver is a tenant.StoreResolver that cannot route homes.
type plainResolver struct{}

func (plainResolver) Resolve(context.Context, string) (kvstore.KVStore, error) { return nil, nil }

// provisioningDatabase is a DatabaseServicer that can place home groups.
type provisioningDatabase struct {
	service.DatabaseServicer
	fakeHomeProvisioner
}

func TestAttachTenantHomes_Attaches(t *testing.T) {
	router := &homeRouterResolver{}
	n := &Node{StoreResolver: router, Database: &provisioningDatabase{}}
	if !AttachTenantHomes(n, &fakeHomeServices{}) {
		t.Fatal("AttachTenantHomes = false, want true")
	}
	if _, ok := router.src.(*TenantHomes); !ok {
		t.Fatalf("attached source = %T, want *TenantHomes", router.src)
	}
}

func TestAttachTenantHomes_AttachesTheSameSourceToServedResolvers(t *testing.T) {
	router := &homeRouterResolver{}
	served := &homeRouterResolver{}
	n := &Node{StoreResolver: router, Database: &provisioningDatabase{}}
	// A served resolver that cannot route homes, and an absent one, are
	// skipped rather than refused.
	if !AttachTenantHomes(n, &fakeHomeServices{}, served, plainResolver{}, nil) {
		t.Fatal("AttachTenantHomes = false, want true")
	}
	if served.src == nil || served.src != router.src {
		t.Fatalf("served resolver's source = %v, want the node resolver's %v", served.src, router.src)
	}
}

func TestAttachTenantHomes_RefusalLeavesServedResolversUnattached(t *testing.T) {
	served := &homeRouterResolver{}
	n := &Node{StoreResolver: plainResolver{}, Database: &provisioningDatabase{}}
	if AttachTenantHomes(n, &fakeHomeServices{}, served) {
		t.Fatal("AttachTenantHomes = true, want false")
	}
	if served.src != nil {
		t.Fatal("a refused attach must attach nothing to a served resolver")
	}
}

func TestAttachTenantHomes_Refuses(t *testing.T) {
	tests := []struct {
		name     string
		node     *Node
		services TenantHomeServices
	}{
		{"no routing resolver", &Node{StoreResolver: plainResolver{}, Database: &provisioningDatabase{}}, &fakeHomeServices{}},
		{"no services", &Node{StoreResolver: &homeRouterResolver{}, Database: &provisioningDatabase{}}, nil},
		{"database cannot place", &Node{StoreResolver: &homeRouterResolver{}}, &fakeHomeServices{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if AttachTenantHomes(tc.node, tc.services) {
				t.Fatal("AttachTenantHomes = true, want false")
			}
			if r, ok := tc.node.StoreResolver.(*homeRouterResolver); ok && r.src != nil {
				t.Fatal("a refused attach must attach nothing")
			}
		})
	}
}

func TestAttachLocalTenantHomes_AttachesToEveryServedResolver(t *testing.T) {
	reg := newHomeRegistry(t)
	router := &homeRouterResolver{}
	served := &homeRouterResolver{}
	n := &Node{StoreResolver: router}
	if !AttachLocalTenantHomes(n, reg, nil, served, plainResolver{}, nil) {
		t.Fatal("AttachLocalTenantHomes = false, want true")
	}
	if _, ok := router.src.(*tenant.LocalHomes); !ok {
		t.Fatalf("attached source = %T, want *tenant.LocalHomes", router.src)
	}
	if served.src != router.src {
		t.Fatalf("served resolver's source = %v, want the node resolver's %v", served.src, router.src)
	}
}

// TestAttachLocalTenantHomes_CredentialStoreServesConditionalWrites proves
// a standalone node's tenant home store is a credential store of record:
// credentials.NewKVStore accepts it (no ErrStoreNotAtomic), and the
// PutIfAbsent and CompareAndSwap a rotation claim needs are decided once,
// a conflicting write reporting false.
func TestAttachLocalTenantHomes_CredentialStoreServesConditionalWrites(t *testing.T) {
	ctx := context.Background()
	reg := newHomeRegistry(t)
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("acme-passphrase"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	tb, err := reg.RegisterTenant(ctx, "acme", seal.NewMemoryBackend(), seal.BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := tb.Barrier().Initialize(ctx, seal.MustCredentials(ctx, []byte("acme-passphrase"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	router := &homeRouterResolver{}
	if !AttachLocalTenantHomes(&Node{StoreResolver: router}, reg, nil) {
		t.Fatal("AttachLocalTenantHomes = false, want true")
	}
	homes, ok := router.src.(*tenant.LocalHomes)
	if !ok {
		t.Fatalf("attached source = %T, want *tenant.LocalHomes", router.src)
	}
	kv, err := homes.HomeStore(ctx, "acme")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	store, err := credentials.NewKVStore(kv)
	if err != nil {
		t.Fatalf("credentials.NewKVStore(standalone home store): %v", err)
	}

	const key = "rotation/claim"
	if put, err := store.PutIfAbsent(ctx, key, []byte("v1")); err != nil || !put {
		t.Fatalf("PutIfAbsent(absent) = %v, %v; want true, nil", put, err)
	}
	if put, err := store.PutIfAbsent(ctx, key, []byte("other")); err != nil || put {
		t.Fatalf("PutIfAbsent(present) = %v, %v; want false, nil", put, err)
	}
	if swapped, err := store.CompareAndSwap(ctx, key, []byte("stale"), []byte("v2")); err != nil || swapped {
		t.Fatalf("CompareAndSwap(conflicting) = %v, %v; want false, nil", swapped, err)
	}
	if swapped, err := store.CompareAndSwap(ctx, key, []byte("v1"), []byte("v2")); err != nil || !swapped {
		t.Fatalf("CompareAndSwap(matching) = %v, %v; want true, nil", swapped, err)
	}
	got, err := store.Get(ctx, key)
	if err != nil || string(got) != "v2" {
		t.Fatalf("Get = %q, %v; want \"v2\", nil", got, err)
	}
}

func TestAttachLocalTenantHomes_Refuses(t *testing.T) {
	reg := newHomeRegistry(t)
	tests := []struct {
		name     string
		node     *Node
		registry *seal.BarrierRegistry
	}{
		{"no routing resolver", &Node{StoreResolver: plainResolver{}}, reg},
		{"no registry", &Node{StoreResolver: &homeRouterResolver{}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			served := &homeRouterResolver{}
			if AttachLocalTenantHomes(tc.node, tc.registry, nil, served) {
				t.Fatal("AttachLocalTenantHomes = true, want false")
			}
			if r, ok := tc.node.StoreResolver.(*homeRouterResolver); ok && r.src != nil {
				t.Fatal("a refused attach must attach nothing")
			}
			if served.src != nil {
				t.Fatal("a refused attach must attach nothing to a served resolver")
			}
		})
	}
}

// recordingCredentials is a credential service that records the home
// source it is given; every verb is the embedded (nil) servicer's.
type recordingCredentials struct {
	service.CredentialServicer
	src   tenant.HomeStoreSource
	calls int
}

func (r *recordingCredentials) SetTenantHomes(src tenant.HomeStoreSource) {
	r.src = src
	r.calls++
}

// homeSourceStub is a home source distinguishable by identity.
type homeSourceStub struct{ id int }

func (*homeSourceStub) HomeStore(context.Context, string) (kvstore.KVStore, error) {
	return nil, errors.New("not served")
}

func (*homeSourceStub) EnsureHome(context.Context, string) error { return nil }

func TestAttachHomeSource_AttachesResolversAndCredentials(t *testing.T) {
	router := &homeRouterResolver{}
	served := &homeRouterResolver{}
	creds := &recordingCredentials{}
	n := &Node{StoreResolver: router, Credential: creds}
	src := &homeSourceStub{id: 1}

	// A served resolver that cannot route homes, and an absent one, are
	// skipped rather than refused.
	if !attachHomeSource(n, src, []tenant.StoreResolver{served, plainResolver{}, nil}) {
		t.Fatal("attachHomeSource = false, want true")
	}
	if router.src != src || served.src != src {
		t.Fatalf("resolver sources = %v, %v, want %v", router.src, served.src, src)
	}
	// A tenant's credentials are kept in its home store, so the credential
	// service is bound to the same source as the resolvers.
	if creds.src != src || creds.calls != 1 {
		t.Fatalf("credential source = %v after %d calls, want %v after 1", creds.src, creds.calls, src)
	}

	// A restart attaches the new source in place of the old one everywhere.
	next := &homeSourceStub{id: 2}
	if !attachHomeSource(n, next, []tenant.StoreResolver{served}) {
		t.Fatal("re-attach = false, want true")
	}
	if router.src != next || served.src != next || creds.src != next {
		t.Fatalf("after re-attach sources = %v, %v, %v, want %v", router.src, served.src, creds.src, next)
	}
}

func TestAttachHomeSource_CredentialServiceWithoutHomesIsSkipped(t *testing.T) {
	router := &homeRouterResolver{}
	src := &homeSourceStub{id: 1}
	for _, creds := range []service.CredentialServicer{nil, struct{ service.CredentialServicer }{}} {
		n := &Node{StoreResolver: router, Credential: creds}
		if !attachHomeSource(n, src, nil) {
			t.Fatalf("attachHomeSource with credential %T = false, want true", creds)
		}
		if router.src != src {
			t.Fatalf("resolver source = %v, want %v", router.src, src)
		}
	}
}

func TestAttachHomeSource_RefusesWithoutARoutingResolver(t *testing.T) {
	for _, resolver := range []tenant.StoreResolver{plainResolver{}, nil} {
		served := &homeRouterResolver{}
		creds := &recordingCredentials{}
		n := &Node{StoreResolver: resolver, Credential: creds}
		if attachHomeSource(n, &homeSourceStub{id: 1}, []tenant.StoreResolver{served}) {
			t.Fatalf("attachHomeSource with resolver %T = true, want false", resolver)
		}
		// A refused attach binds nothing: neither a served resolver nor the
		// credential service, which would otherwise serve tenant stores the
		// node's resolver cannot route.
		if served.src != nil || creds.calls != 0 {
			t.Fatalf("refused attach bound served=%v credentials=%d calls", served.src, creds.calls)
		}
	}
}
