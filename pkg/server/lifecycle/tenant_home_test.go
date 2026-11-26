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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
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
	host    service.Host
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

// HomeGroup mirrors the raft factory's: the home database's shard on host.
func (f *fakeHomeServices) HomeGroup(ctx context.Context, tenantID string) (service.Host, uint64, error) {
	svcs, err := f.Services(ctx, tenantID, database.TenantHomeName)
	if err != nil {
		return nil, 0, err
	}
	return f.host, svcs.ShardID, nil
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

// stubHost is a service.Host identified by pointer; HomeGroup only names it.
type stubHost struct{ service.Host }

func TestTenantHomes_HomeGroup(t *testing.T) {
	ctx := context.Background()

	t.Run("names the home database's shard", func(t *testing.T) {
		bundle := homeBundle(func() error { return nil })
		bundle.ShardID = 42
		host := &stubHost{}
		svcs := &fakeHomeServices{svcs: bundle, host: host}
		h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})

		got, shardID, err := h.HomeGroup(ctx, "vault")
		if err != nil {
			t.Fatalf("HomeGroup: %v", err)
		}
		if got != host || shardID != 42 {
			t.Fatalf("HomeGroup = (%v, %d), want (the services' host, 42)", got, shardID)
		}
		if len(svcs.dbs) != 1 || svcs.dbs[0] != database.TenantHomeName {
			t.Fatalf("services asked for %v, want [%s]", svcs.dbs, database.TenantHomeName)
		}
	})

	t.Run("catching up is refused", func(t *testing.T) {
		bundle := homeBundle(func() error { return nil })
		bundle.ShardID = 42
		svcs := &fakeHomeServices{svcs: bundle, host: &stubHost{}, joining: map[uint64]bool{42: true}}
		h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})

		_, _, err := h.HomeGroup(ctx, "vault")
		var catchingUp *TenantHomeCatchingUpError
		if !errors.As(err, &catchingUp) || catchingUp.TenantID != "vault" {
			t.Fatalf("err = %v, want *TenantHomeCatchingUpError for vault", err)
		}
	})

	t.Run("sealed is returned as is", func(t *testing.T) {
		h := newTestTenantHomes(&fakeHomeServices{err: seal.ErrSealed}, &fakeHomeProvisioner{})
		if _, _, err := h.HomeGroup(ctx, "vault"); !errors.Is(err, seal.ErrSealed) {
			t.Fatalf("err = %v, want seal.ErrSealed", err)
		}
	})
}

// TestTenantHomes_HomeGroup_RefusalsClassifyUnavailable pins what a transport
// answers when the home group cannot be named: a retryable unavailable, as the
// HomeStore path answers for the same causes, not an internal error.
func TestTenantHomes_HomeGroup_RefusalsClassifyUnavailable(t *testing.T) {
	ctx := context.Background()

	t.Run("catching up", func(t *testing.T) {
		bundle := homeBundle(func() error { return nil })
		bundle.ShardID = 42
		svcs := &fakeHomeServices{svcs: bundle, host: &stubHost{}, joining: map[uint64]bool{42: true}}
		h := newTestTenantHomes(svcs, &fakeHomeProvisioner{})

		_, _, err := h.HomeGroup(ctx, "vault")
		if got := errclass.Classify(err); got != errclass.Unavailable {
			t.Fatalf("Classify(%v) = %v, want Unavailable", err, got)
		}
		var catchingUp *TenantHomeCatchingUpError
		if !errors.As(err, &catchingUp) || catchingUp.TenantID != "vault" {
			t.Fatalf("err = %v, want *TenantHomeCatchingUpError for vault", err)
		}
	})

	t.Run("factory error", func(t *testing.T) {
		factoryErr := errors.New("home services not built")
		h := newTestTenantHomes(&fakeHomeServices{err: factoryErr}, &fakeHomeProvisioner{})

		_, _, err := h.HomeGroup(ctx, "vault")
		if got := errclass.Classify(err); got != errclass.Unavailable {
			t.Fatalf("Classify(%v) = %v, want Unavailable", err, got)
		}
		if !errors.Is(err, factoryErr) {
			t.Fatalf("err = %v, want it to wrap the factory's error", err)
		}
	})
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

// quietLogger returns a logger that discards every record.
func quietLogger(t *testing.T) interfaces.Logger {
	t.Helper()
	l, err := logger.NewWithHandler(slog.DiscardHandler, logger.LogConfig{})
	if err != nil {
		t.Fatalf("NewWithHandler: %v", err)
	}
	return logger.NewAdapter(l)
}

// isolatedVectorNode returns a node whose StoreResolver keeps acme in a home of the given isolation and beta prefix-scoped.
// isolatingTenants reports id with the given isolation, as a seal-enabled tenant service records it.
type isolatingTenants struct {
	tenant.TenantServicer
	id        string
	isolation tenant.IsolationType
}

func (s isolatingTenants) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	info, err := s.TenantServicer.GetTenant(ctx, tenantID)
	if err != nil || tenantID != s.id {
		return info, err
	}
	cp := *info
	cp.IsolationType = s.isolation
	return &cp, nil
}

func isolatedVectorNode(t *testing.T, isolation tenant.IsolationType) (*Node, *seal.BarrierRegistry, *vector.MemoryVectorStorage) {
	t.Helper()
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
	shared := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(shared, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	for _, id := range []string{"acme", "beta"} {
		if _, err := tenants.CreateTenant(ctx, id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	resolver, err := tenant.NewStoreResolver(shared, reg, isolatingTenants{TenantServicer: tenants, id: "acme", isolation: isolation})
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	nodeWide := vector.NewMemoryVectorStorage()
	vec, err := service.NewVectorService(nodeWide, quietLogger(t))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	return &Node{StoreResolver: resolver, Vector: vec, Database: &provisioningDatabase{}}, reg, nodeWide
}

// requireVectorsInHome checks acme's vectors land in home's vector keyspace and beta's in nodeWide.
func requireVectorsInHome(t *testing.T, n *Node, home kvstore.KVStore, nodeWide *vector.MemoryVectorStorage) {
	t.Helper()
	for _, id := range []string{"acme", "beta"} {
		ctx := tenant.WithTenantID(context.Background(), id)
		if _, err := n.Vector.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "docs", Dimension: 2, DistanceMetric: "cosine"}); err != nil {
			t.Fatalf("CreateNamespace(%s): %v", id, err)
		}
		if _, err := n.Vector.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "docs", ID: 1, Vector: []float32{1, 0}}); err != nil {
			t.Fatalf("UpsertVector(%s): %v", id, err)
		}
		got, err := n.Vector.GetVector(ctx, &service.GetVectorRequest{Namespace: "docs", ID: 1})
		if err != nil || !got.Found {
			t.Fatalf("GetVector(%s) = %+v, %v; want found", id, got, err)
		}
	}
	keys, err := home.List(context.Background(), "__vector/")
	if err != nil {
		t.Fatalf("List(home): %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("acme's home store holds no vector key")
	}
	spaces, err := nodeWide.ListNamespaces(context.Background())
	if err != nil {
		t.Fatalf("ListNamespaces(node-wide): %v", err)
	}
	if len(spaces) != 1 || !strings.HasPrefix(spaces[0].Name, "beta") {
		t.Fatalf("node-wide namespaces = %+v, want beta's only", spaces)
	}
}

func TestAttachLocalTenantHomes_VectorsInTenantBarrier(t *testing.T) {
	n, reg, nodeWide := isolatedVectorNode(t, tenant.IsolationBarrier)
	if !AttachLocalTenantHomes(n, reg, nil) {
		t.Fatal("AttachLocalTenantHomes = false, want true")
	}
	if err := attachVectorTenantHomes(n); err != nil {
		t.Fatalf("attachVectorTenantHomes: %v", err)
	}
	home, err := n.StoreResolver.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Resolve(acme): %v", err)
	}
	requireVectorsInHome(t, n, home, nodeWide)

	if err := reg.SealTenant("acme"); err != nil {
		t.Fatalf("SealTenant(acme): %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err = n.Vector.GetVector(ctx, &service.GetVectorRequest{Namespace: "docs", ID: 1})
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetVector on a sealed home = %v, want ErrTenantSealed", err)
	}
}

func TestAttachTenantHomes_VectorsInHomeGroup(t *testing.T) {
	n, _, nodeWide := isolatedVectorNode(t, tenant.IsolationRaft)
	group := newMapKVServicer()
	if !AttachTenantHomes(n, &fakeHomeServices{svcs: &server.DatabaseServices{KV: group, Transaction: newEvaluatingTxService()}}) {
		t.Fatal("AttachTenantHomes = false, want true")
	}
	if err := attachVectorTenantHomes(n); err != nil {
		t.Fatalf("attachVectorTenantHomes: %v", err)
	}
	home, err := n.StoreResolver.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Resolve(acme): %v", err)
	}
	requireVectorsInHome(t, n, home, nodeWide)
	if keys := group.keysWithPrefix("__vector/"); len(keys) == 0 {
		t.Fatal("the home group's store holds no vector key")
	}
}

func TestAttachVectorTenantHomes_RefusesAResolverWithoutHomes(t *testing.T) {
	vec, err := service.NewVectorService(vector.NewMemoryVectorStorage(), quietLogger(t))
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	err = attachVectorTenantHomes(&Node{StoreResolver: plainResolver{}, Vector: vec})
	if !errors.Is(err, ErrStoreResolverLocatesNoHomes) {
		t.Fatalf("attachVectorTenantHomes = %v, want ErrStoreResolverLocatesNoHomes", err)
	}
}

// mapKVServicer is a map-backed KV servicer standing in for a home group's store.
type mapKVServicer struct {
	mockKVServicer
	mu   sync.Mutex
	data map[string][]byte
}

func newMapKVServicer() *mapKVServicer {
	m := &mapKVServicer{data: map[string][]byte{}}
	m.putFn = func(_ context.Context, key string, value []byte) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.data[key] = append([]byte(nil), value...)
		return nil
	}
	m.putIfAbsentFn = func(_ context.Context, key string, value []byte) (bool, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.data[key]; ok {
			return false, nil
		}
		m.data[key] = append([]byte(nil), value...)
		return true, nil
	}
	m.getFn = func(_ context.Context, key string) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		v, ok := m.data[key]
		if !ok {
			return nil, &service.KeyNotFoundError{Key: key}
		}
		return append([]byte(nil), v...), nil
	}
	m.deleteFn = func(_ context.Context, key string) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.data, key)
		return nil
	}
	m.scanFn = func(_ context.Context, prefix string) (map[string][]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := map[string][]byte{}
		for k, v := range m.data {
			if strings.HasPrefix(k, prefix) {
				out[k] = append([]byte(nil), v...)
			}
		}
		return out, nil
	}
	m.listFn = func(_ context.Context, prefix string) ([]string, error) {
		return m.keysWithPrefix(prefix), nil
	}
	m.compareAndSwapFn = func(_ context.Context, key string, expected, next []byte) (bool, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		cur, ok := m.data[key]
		if !ok || !bytes.Equal(cur, expected) {
			return false, nil
		}
		m.data[key] = append([]byte(nil), next...)
		return true, nil
	}
	return m
}

func (m *mapKVServicer) keysWithPrefix(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
