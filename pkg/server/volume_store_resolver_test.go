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

// TestVolumeStoreResolver_RoutesAVolumeTenantToItsOwnHost is the branch that
// exists at all: a volume-isolated tenant's databases share a dedicated Host,
// so that is where their keys are.
func TestVolumeStoreResolver_RoutesAVolumeTenantToItsOwnHost(t *testing.T) {
	kv := newMockKVService()
	r := &volumeStoreResolver{
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

// TestVolumeStoreResolver_UsesTheSystemStoreEverywhereElse covers the other
// isolation modes and the short-circuits, each of which must report
// dedicated=false rather than an error.
func TestVolumeStoreResolver_UsesTheSystemStoreEverywhereElse(t *testing.T) {
	hosts := &stubTenantServices{svcs: &DatabaseServices{KV: newMockKVService()}}

	tests := []struct {
		name     string
		resolver *volumeStoreResolver
		tenantID string
	}{
		{
			name: "prefix isolation",
			resolver: &volumeStoreResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationPrefix},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "raft isolation — the shard is the database, not a shared prefix",
			resolver: &volumeStoreResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationRaft},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "the system tenant is never volume isolated",
			resolver: &volumeStoreResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: tenant.SystemTenantID,
		},
		{
			name: "an unknown tenant — a database outlives its tenant record exactly during a teardown",
			resolver: &volumeStoreResolver{
				hosts:        hosts,
				tenantSvc:    &isolatedTenantServicer{err: tenant.ErrTenantNotFound},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "no host registry configured",
			resolver: &volumeStoreResolver{
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "a typed-nil host registry",
			resolver: &volumeStoreResolver{
				hosts:        (*TenantHostRegistry)(nil),
				tenantSvc:    &isolatedTenantServicer{isolation: tenant.IsolationVolume},
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "no tenant service configured",
			resolver: &volumeStoreResolver{
				hosts:        hosts,
				systemTenant: tenant.SystemTenantID,
			},
			tenantID: "acme",
		},
		{
			name: "an empty tenant is the system tenant",
			resolver: &volumeStoreResolver{
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

// TestVolumeStoreResolver_AnUnreachableHostFailsTheDrop is the fail-closed
// case, and it is the one that must NOT degrade to the system store: the
// tenant IS volume isolated, so the system store is the wrong place to look
// and finding nothing there would report a drop over untouched data.
func TestVolumeStoreResolver_AnUnreachableHostFailsTheDrop(t *testing.T) {
	boom := errors.New("volume sealed")
	r := &volumeStoreResolver{
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
