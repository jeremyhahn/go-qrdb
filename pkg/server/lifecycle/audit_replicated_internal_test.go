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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeTenantServicer answers GetTenant from a fixed map; a zero value
// answers ErrTenantNotFound for everything, matching an unpopulated
// Node.Tenant's absence of metadata.
type fakeTenantServicer struct {
	tenant.TenantServicer
	tenants map[string]*tenant.TenantInfo
}

func (f *fakeTenantServicer) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	info, ok := f.tenants[tenantID]
	if !ok {
		return nil, tenant.ErrTenantNotFound
	}
	return info, nil
}

// TestNodeAuditIsolationLookup_NilTenantReportsNotFound asserts the lookup
// degrades to "no metadata" -- never a panic -- before Node.Tenant exists,
// which is the state BuildServices wires the replicated audit sink in.
func TestNodeAuditIsolationLookup_NilTenantReportsNotFound(t *testing.T) {
	n := &Node{}
	l := &nodeAuditIsolationLookup{node: n}
	_, err := l.GetTenant(context.Background(), "acme")
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("err = %v, want tenant.ErrTenantNotFound", err)
	}
}

// TestNodeAuditIsolationLookup_DelegatesOnceTenantIsSet asserts the lazy
// indirection actually reads n.Tenant's CURRENT value, not a value
// snapshotted at construction time -- the whole point of binding to n
// rather than to n.Tenant directly.
func TestNodeAuditIsolationLookup_DelegatesOnceTenantIsSet(t *testing.T) {
	n := &Node{}
	l := &nodeAuditIsolationLookup{node: n}

	n.Tenant = &fakeTenantServicer{tenants: map[string]*tenant.TenantInfo{
		"acme": {ID: "acme", IsolationType: tenant.IsolationPrefix},
	}}

	info, err := l.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if info.ID != "acme" {
		t.Fatalf("info.ID = %q, want acme", info.ID)
	}
}

// fakeEntitledTenantServicer additionally satisfies service.AuditEntitlementLookup,
// the shape *tenant.TenantService has and other TenantServicer
// implementations may not.
type fakeEntitledTenantServicer struct {
	tenant.TenantServicer
	enabled map[string]bool
}

func (f *fakeEntitledTenantServicer) ReplicatedAuditEnabled(_ context.Context, tenantID string) (bool, error) {
	return f.enabled[tenantID], nil
}

// TestNodeAuditEntitlementLookup_NilTenantIsNotEntitled asserts the
// entitlement lookup degrades to "not entitled" -- the secure default --
// before Node.Tenant exists.
func TestNodeAuditEntitlementLookup_NilTenantIsNotEntitled(t *testing.T) {
	n := &Node{}
	l := &nodeAuditEntitlementLookup{node: n}
	on, err := l.ReplicatedAuditEnabled(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if on {
		t.Fatal("want false with no Tenant servicer wired")
	}
}

// TestNodeAuditEntitlementLookup_TenantServicerWithoutTheMethodIsNotEntitled
// asserts a TenantServicer implementation that does not expose
// ReplicatedAuditEnabled (e.g. the KV-routed service some compositions use)
// degrades to "not entitled" rather than a type-assertion panic.
func TestNodeAuditEntitlementLookup_TenantServicerWithoutTheMethodIsNotEntitled(t *testing.T) {
	n := &Node{Tenant: &fakeTenantServicer{}}
	l := &nodeAuditEntitlementLookup{node: n}
	on, err := l.ReplicatedAuditEnabled(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if on {
		t.Fatal("want false when the servicer does not implement AuditEntitlementLookup")
	}
}

// TestNodeAuditEntitlementLookup_DelegatesWhenSupported asserts the lookup
// delegates to a TenantServicer that DOES implement ReplicatedAuditEnabled.
func TestNodeAuditEntitlementLookup_DelegatesWhenSupported(t *testing.T) {
	n := &Node{Tenant: &fakeEntitledTenantServicer{enabled: map[string]bool{"acme": true}}}
	l := &nodeAuditEntitlementLookup{node: n}
	on, err := l.ReplicatedAuditEnabled(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if !on {
		t.Fatal("want true: the wired servicer reports acme entitled")
	}
}

// fakeTighteningTenantServicer extends fakeTenantServicer with the
// strict-tightening marker read *tenant.TenantService and the KV-routed
// service implement.
type fakeTighteningTenantServicer struct {
	fakeTenantServicer
	tightening map[string]bool
}

func (f *fakeTighteningTenantServicer) IsolationTightening(_ context.Context, tenantID string) (bool, error) {
	return f.tightening[tenantID], nil
}

// ClusterIsolationPolicy reports the operator-access floor: only the
// tenant's own policy or the marker makes it strict.
func (f *fakeTighteningTenantServicer) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

func TestNodeAuditIsolationLookup_ForwardsIsolationTightening(t *testing.T) {
	ctx := context.Background()

	n := &Node{}
	l := &nodeAuditIsolationLookup{node: n}
	got, err := l.IsolationTightening(ctx, "acme")
	if err != nil || got {
		t.Fatalf("nil Tenant: IsolationTightening = (%v, %v), want (false, nil)", got, err)
	}

	n.Tenant = &fakeTighteningTenantServicer{tightening: map[string]bool{"acme": true}}
	got, err = l.IsolationTightening(ctx, "acme")
	if err != nil || !got {
		t.Fatalf("forwarded marker: IsolationTightening = (%v, %v), want (true, nil)", got, err)
	}
	got, err = l.IsolationTightening(ctx, "other")
	if err != nil || got {
		t.Fatalf("no marker: IsolationTightening = (%v, %v), want (false, nil)", got, err)
	}

	n.Tenant = &fakeTenantServicer{}
	if got, err = l.IsolationTightening(ctx, "acme"); err == nil {
		t.Fatalf("servicer without the method: IsolationTightening = (%v, nil), want an error (fail closed)", got)
	}
}

// systemRouteStore answers every recorded audit route as the system route.
type systemRouteStore struct{}

func (systemRouteStore) Get(context.Context, string) ([]byte, error) {
	return []byte(service.AuditRouteSystem), nil
}

func (systemRouteStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, errors.New("systemRouteStore: route already recorded")
}

// unusedHomeGroups is a home-group source no test path reaches: a recorded
// system route never resolves the tenant's home group.
type unusedHomeGroups struct {
	service.AuditHomeGroupSource
}

func TestNodeAuditIsolationLookup_TighteningRefusesSystemAppend(t *testing.T) {
	ctx := context.Background()
	n := &Node{Tenant: &fakeTighteningTenantServicer{
		fakeTenantServicer: fakeTenantServicer{tenants: map[string]*tenant.TenantInfo{
			"acme": {ID: "acme", IsolationType: tenant.IsolationBarrier},
		}},
		tightening: map[string]bool{"acme": true},
	}}
	r := service.NewSystemShardAuditResolver(nil, 1, &nodeAuditIsolationLookup{node: n})
	r.SetHomeGroupSource(unusedHomeGroups{}, systemRouteStore{})

	_, _, err := r.ResolveAuditAppend(ctx, "acme")
	var refused *tenant.SystemAuditRouteError
	if !errors.As(err, &refused) {
		t.Fatalf("ResolveAuditAppend during a strict tightening = %v, want *tenant.SystemAuditRouteError", err)
	}

	// Without the marker the same append is allowed onto the system route.
	n.Tenant.(*fakeTighteningTenantServicer).tightening = nil
	if _, _, err := r.ResolveAuditAppend(ctx, "acme"); err != nil {
		t.Fatalf("ResolveAuditAppend without a tightening = %v, want nil", err)
	}
}
