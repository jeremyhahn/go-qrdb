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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// These cover the routing half of go-qrdb#412's reclamation: which store a
// tenant's composite-namespaced keys are in. Getting it wrong is silent --
// scanning the system store for a tenant whose keys are on a dedicated
// per-tenant Host finds nothing and reports a successful drop.

// isolatedTenantServicer reports one isolation type for every tenant.
type isolatedTenantServicer struct {
	mockTenantServicer
	isolation tenant.IsolationType
	err       error
}

func (s *isolatedTenantServicer) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &tenant.TenantInfo{ID: tenantID, Name: tenantID, IsolationType: s.isolation}, nil
}

// stubTenantServices returns a fixed service set, or an error.
type stubTenantServices struct {
	svcs *DatabaseServices
	err  error
}

func (s *stubTenantServices) Services(context.Context, string) (*DatabaseServices, error) {
	return s.svcs, s.err
}

// TestStoreResolver_RoutesAVolumeTenantToItsOwnHost is the branch that
// exists at all: a volume-isolated tenant's databases share a dedicated Host,
// so that is where their keys are.
func TestStoreResolver_RoutesAVolumeTenantToItsOwnHost(t *testing.T) {
	kv := newMockKVService()
	r := &storeResolver{
		hosts:        &stubTenantServices{svcs: &DatabaseServices{KV: kv}},
		tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
		systemTenant: tenant.SystemTenantID,
	}

	target, dedicated, err := r.ReclaimTarget(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReclaimTarget: %v", err)
	}
	if !dedicated {
		t.Fatal("a volume-isolated tenant's keys are not in the system store")
	}
	if target.KV == nil {
		t.Fatal("the tenant's own KV engine must be returned")
	}
}

// TestStoreResolver_UsesTheSystemStoreEverywhereElse covers the other
// isolation modes and the short-circuits, each of which must report
// dedicated=false rather than an error.
func TestStoreResolver_UsesTheSystemStoreEverywhereElse(t *testing.T) {
	hosts := &stubTenantServices{svcs: &DatabaseServices{KV: newMockKVService()}}

	tests := []struct {
		name     string
		resolver *storeResolver
		tenantID string
	}{
		{
			name: "prefix isolation",
			resolver: &storeResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationPrefix},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "raft isolation — the shard is the database, not a shared prefix",
			resolver: &storeResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationRaft},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "the system tenant is never volume isolated",
			resolver: &storeResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: tenant.SystemTenantID,
		},
		{
			name: "an unknown tenant — a database outlives its tenant record exactly during a teardown",
			resolver: &storeResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{err: tenant.ErrTenantNotFound},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "no host registry configured",
			resolver: &storeResolver{
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "a typed-nil host registry",
			resolver: &storeResolver{
				hosts:        (*TenantHostRegistry)(nil),
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "no tenant service configured",
			resolver: &storeResolver{
				hosts:        hosts,
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "an empty tenant is the system tenant",
			resolver: &storeResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, dedicated, err := tc.resolver.ReclaimTarget(context.Background(), tc.tenantID)
			if err != nil {
				t.Fatalf("ReclaimTarget: %v", err)
			}
			if dedicated {
				t.Error("these tenants' namespaced keys are in the system store")
			}
		})
	}
}

// TestStoreResolver_AnUnreachableHostFailsTheDrop is the fail-closed
// case, and it is the one that must NOT degrade to the system store: the
// tenant IS volume isolated, so the system store is the wrong place to look
// and finding nothing there would report a drop over untouched data.
func TestStoreResolver_AnUnreachableHostFailsTheDrop(t *testing.T) {
	boom := errors.New("volume sealed")
	r := &storeResolver{
		hosts:        &stubTenantServices{err: boom},
		tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
		systemTenant: tenant.SystemTenantID,
	}

	_, dedicated, err := r.ReclaimTarget(context.Background(), "acme")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the host failure surfaced", err)
	}
	if !dedicated {
		t.Error("the tenant is volume isolated; the caller must not be told to use the system store")
	}
}

// TestServer_WiresTheVolumeStoreResolver covers the wiring itself: without
// it, a volume-isolated tenant's drop scans the system store and reclaims
// nothing.
func TestServer_WiresTheVolumeStoreResolver(t *testing.T) {
	reclaim := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: newMockKVService()})

	srv := New(&config.Config{}, nil, nil,
		WithKeyspaceReclaimService(reclaim),
		WithTenantHostRegistry(NewTenantHostRegistry(nil, nil, nil)),
		WithTenantService(&isolatedTenantServicer{isolation: tenant.IsolationVolume}),
	)
	if srv == nil {
		t.Fatal("New returned nil")
	}

	// The resolver is installed, so a volume-isolated tenant now routes to the
	// (here unavailable) Host rather than silently to the system store.
	err := reclaim.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports")
	var want *service.ReclaimStoreUnavailableError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want the resolver to have been wired and to fail closed", err, err)
	}
}

// TestServer_WithoutATenantHostRegistryLeavesTheResolverUnset is the control:
// a server serving no volume-isolated tenants must not install a resolver,
// because every drop would then pay a tenant lookup it cannot use.
func TestServer_WithoutATenantHostRegistryLeavesTheResolverUnset(t *testing.T) {
	reclaim := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: newMockKVService()})

	_ = New(&config.Config{}, nil, nil, WithKeyspaceReclaimService(reclaim))

	if err := reclaim.ReclaimDatabaseKeyspace(context.Background(), "acme", "reports"); err != nil {
		t.Fatalf("with no resolver the system store is the answer: %v", err)
	}
}

// TestServer_WiringIsANoOpWithoutAReclaimService covers the other guard on
// the wiring: a host registry with nothing to attach the resolver to.
func TestServer_WiringIsANoOpWithoutAReclaimService(t *testing.T) {
	srv := New(&config.Config{}, nil, nil, WithTenantHostRegistry(NewTenantHostRegistry(nil, nil, nil)))
	if srv == nil {
		t.Fatal("New returned nil")
	}
}

// TestStoreResolver_FenceTargetRoutesEachIsolationMode pins where a
// database's drop fence is written: the Raft group its writes are proposed
// to, which is the only place their guard reads.
func TestStoreResolver_FenceTargetRoutesEachIsolationMode(t *testing.T) {
	volumeKV := newMockKVService()
	raftKV := newMockKVService()
	hosts := &stubTenantServices{svcs: &DatabaseServices{KV: volumeKV}}
	raft := &stubRaftFence{svcs: &DatabaseServices{KV: raftKV}}

	tests := []struct {
		name            string
		resolver        *storeResolver
		tenantID        string
		wantKV          service.ReclaimKV
		wantDedicated   bool
		wantPerDatabase bool
	}{
		{
			name:          "volume isolation: the tenant's own Host",
			resolver:      &storeResolver{hosts: hosts, raft: raft, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationVolume}, systemTenant: tenant.SystemTenantID},
			tenantID:      "acme",
			wantKV:        volumeKV,
			wantDedicated: true,
		},
		{
			name:            "raft isolation: the database's own shard, which is its incarnation",
			resolver:        &storeResolver{hosts: hosts, raft: raft, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationRaft}, systemTenant: tenant.SystemTenantID},
			tenantID:        "acme",
			wantKV:          raftKV,
			wantDedicated:   true,
			wantPerDatabase: true,
		},
		{
			name:     "prefix isolation: the system store",
			resolver: &storeResolver{hosts: hosts, raft: raft, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationPrefix}, systemTenant: tenant.SystemTenantID},
			tenantID: "acme",
		},
		{
			name:     "the system tenant",
			resolver: &storeResolver{hosts: hosts, raft: raft, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationRaft}, systemTenant: tenant.SystemTenantID},
			tenantID: tenant.SystemTenantID,
		},
		{
			name:     "a tenant that does not exist",
			resolver: &storeResolver{hosts: hosts, raft: raft, tenantSvc: &isolatedTenantServicer{err: tenant.ErrTenantNotFound}, systemTenant: tenant.SystemTenantID},
			tenantID: "acme",
		},
		{
			name:     "no tenant service",
			resolver: &storeResolver{hosts: hosts, raft: raft, systemTenant: tenant.SystemTenantID},
			tenantID: "acme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, dedicated, err := tt.resolver.FenceTarget(context.Background(), tt.tenantID, "orders")
			if err != nil {
				t.Fatalf("FenceTarget: %v", err)
			}
			if dedicated != tt.wantDedicated || store.PerDatabase != tt.wantPerDatabase {
				t.Fatalf("(dedicated, perDatabase) = (%v, %v), want (%v, %v)", dedicated, store.PerDatabase, tt.wantDedicated, tt.wantPerDatabase)
			}
			if tt.wantKV != nil && store.KV != tt.wantKV {
				t.Fatal("FenceTarget returned the wrong store")
			}
		})
	}
}

// TestStoreResolver_FenceTargetNeverFallsBackOnFailure covers every way the
// database's store can be unreachable. None of them may be answered with the
// system store: a fence written -- or a name marked live -- where the
// database's writes are not guarded does nothing, silently.
func TestStoreResolver_FenceTargetNeverFallsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("host down")
	lookupFailed := errors.New("tenant lookup timed out")
	unavailable := func(err error) bool {
		var e *rest.IsolationBackendUnavailableError
		return errors.As(err, &e)
	}

	tests := []struct {
		name     string
		resolver *storeResolver
		check    func(error) bool
	}{
		{
			name:     "a tenant lookup that fails for any reason but not-found",
			resolver: &storeResolver{hosts: &stubTenantServices{}, raft: &stubRaftFence{}, tenantSvc: &isolatedTenantServicer{err: lookupFailed}, systemTenant: tenant.SystemTenantID},
			check:    func(err error) bool { return errors.Is(err, lookupFailed) },
		},
		{
			name:     "a volume tenant whose Host cannot be resolved",
			resolver: &storeResolver{hosts: &stubTenantServices{err: boom}, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationVolume}, systemTenant: tenant.SystemTenantID},
			check:    func(err error) bool { return errors.Is(err, boom) },
		},
		{
			name:     "a volume tenant with no Host registry",
			resolver: &storeResolver{tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationVolume}, systemTenant: tenant.SystemTenantID},
			check:    unavailable,
		},
		{
			name:     "a raft tenant whose shard cannot be resolved",
			resolver: &storeResolver{raft: &stubRaftFence{err: boom}, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationRaft}, systemTenant: tenant.SystemTenantID},
			check:    func(err error) bool { return errors.Is(err, boom) },
		},
		{
			name:     "a raft tenant with no raft backend",
			resolver: &storeResolver{hosts: &stubTenantServices{}, tenantSvc: &isolatedTenantServicer{isolation: tenant.IsolationRaft}, systemTenant: tenant.SystemTenantID},
			check:    unavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, dedicated, err := tt.resolver.FenceTarget(ctx, "acme", "orders")
			if err == nil || !tt.check(err) {
				t.Fatalf("FenceTarget error = %v (%T), want the failure reported", err, err)
			}
			if !dedicated {
				t.Fatal("a failure must not be reported as the system store")
			}
		})
	}
}

// TestStoreResolver_FenceTargetMissingShard: a raft database whose shard is
// unknown is a dedicated store that does not exist -- nothing to mark.
func TestStoreResolver_FenceTargetMissingShard(t *testing.T) {
	unknown := &storeResolver{
		raft:         &stubRaftFence{err: &RaftBackendError{TenantID: "acme", Database: "orders", Op: "db-info", Err: ErrRaftDatabaseUnknown}},
		tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationRaft},
		systemTenant: tenant.SystemTenantID,
	}
	store, dedicated, err := unknown.FenceTarget(context.Background(), "acme", "orders")
	if err != nil || !dedicated || store.KV != nil || !store.PerDatabase {
		t.Fatalf("unknown shard: (%+v, dedicated=%v, %v), want a per-database store that does not exist", store, dedicated, err)
	}
}

// TestServer_WiresTheResolverForARaftBackendAlone: a raft backend is reason
// enough to install the resolver, because a raft database's drop fence
// belongs in its own shard.
func TestServer_WiresTheResolverForARaftBackendAlone(t *testing.T) {
	reclaim := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: newMockKVService()})
	_ = New(&config.Config{}, nil, nil,
		WithKeyspaceReclaimService(reclaim),
		WithRaftBackendFactory(&RaftBackendFactory{}),
		WithTenantService(&isolatedTenantServicer{isolation: tenant.IsolationRaft}),
	)

	// A factory with no host cannot resolve the shard, and the resolver must
	// say so rather than fence the system store.
	err := reclaim.FenceDatabaseKeyspace(context.Background(), "acme", "orders")
	var unavailable *service.ReclaimStoreUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v (%T), want the resolver to be wired and to fail closed", err, err)
	}
}

// ReclaimTarget answers a tenant lookup that fails for any reason but
// not-found with the failure, never with the system store: scanning the
// system store for a tenant whose keys are elsewhere would report a drop
// over data that is still there.
func TestStoreResolver_ReclaimTargetNeverFallsBackOnALookupFailure(t *testing.T) {
	lookupFailed := errors.New("tenant lookup timed out")
	r := &storeResolver{
		hosts:        &stubTenantServices{svcs: &DatabaseServices{KV: newMockKVService()}},
		tenantSvc:    &isolatedTenantServicer{err: lookupFailed},
		systemTenant: tenant.SystemTenantID,
	}
	_, dedicated, err := r.ReclaimTarget(context.Background(), "acme")
	if !errors.Is(err, lookupFailed) || !dedicated {
		t.Fatalf("ReclaimTarget = (dedicated=%v, %v), want the lookup failure", dedicated, err)
	}

	notFound := &storeResolver{
		hosts:        &stubTenantServices{svcs: &DatabaseServices{KV: newMockKVService()}},
		tenantSvc:    &isolatedTenantServicer{err: tenant.ErrTenantNotFound},
		systemTenant: tenant.SystemTenantID,
	}
	if _, dedicated, err := notFound.ReclaimTarget(context.Background(), "acme"); err != nil || dedicated {
		t.Fatalf("an unknown tenant: (dedicated=%v, %v), want the system store", dedicated, err)
	}
}
