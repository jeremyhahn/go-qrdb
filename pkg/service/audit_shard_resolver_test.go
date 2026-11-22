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

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fakeIsolationLookup answers GetTenant from a fixed map, keyed by tenant ID.
type fakeIsolationLookup struct {
	tenants map[string]*tenant.TenantInfo
}

func (f *fakeIsolationLookup) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	info, ok := f.tenants[tenantID]
	if !ok {
		return nil, tenant.ErrTenantNotFound
	}
	return info, nil
}

// IsolationTightening reports no tightening in flight, so the tests that
// predate the marker keep their assertions (floorIsolationLookup inherits it).
func (f *fakeIsolationLookup) IsolationTightening(context.Context, string) (bool, error) {
	return false, nil
}

type failingIsolationLookup struct{ err error }

func (f *failingIsolationLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return nil, f.err
}

func TestSystemShardAuditResolver_RoutesSystemTenantToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	r := NewSystemShardAuditResolver(host, 1, nil)
	gotHost, shardID, err := r.ResolveAuditShard(context.Background(), tenantid.System)
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if gotHost != host || shardID != 1 {
		t.Fatalf("resolved (%v, %d), want (%v, 1)", gotHost, shardID, host)
	}
}

func TestSystemShardAuditResolver_NilIsolationRoutesEveryTenantToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	r := NewSystemShardAuditResolver(host, 1, nil)
	_, shardID, err := r.ResolveAuditShard(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if shardID != 1 {
		t.Fatalf("shardID = %d, want 1", shardID)
	}
}

func TestSystemShardAuditResolver_RoutesUnregisteredTenantToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	r := NewSystemShardAuditResolver(host, 1, &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{}})
	_, shardID, err := r.ResolveAuditShard(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if shardID != 1 {
		t.Fatalf("shardID = %d, want 1", shardID)
	}
}

func TestSystemShardAuditResolver_RoutesPrefixAndBarrierTenantsToTheSystemShardWithoutAHomeGroup(t *testing.T) {
	host := &mockHost{}
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme":    {ID: "acme", IsolationType: tenant.IsolationPrefix},
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
	}}
	r := NewSystemShardAuditResolver(host, 1, lookup)
	for _, id := range []string{"acme", "widgets"} {
		_, shardID, err := r.ResolveAuditShard(context.Background(), id)
		if err != nil {
			t.Fatalf("ResolveAuditShard(%q): %v", id, err)
		}
		if shardID != 1 {
			t.Fatalf("tenant %q resolved shard %d, want 1", id, shardID)
		}
	}
}

// TestSystemShardLogResolver_RefusesBarrierTenantsWithoutAHomeGroup: the log registry sits
// under the system barrier, so the log resolver refuses a barrier tenant.
func TestSystemShardLogResolver_RefusesBarrierTenantsWithoutAHomeGroup(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
	}}
	r := NewSystemShardLogResolver(&mockHost{}, 1, lookup)
	_, _, err := r.ResolveAuditShard(context.Background(), "widgets")
	var refused *BarrierTenantLogNotSupportedError
	if !errors.As(err, &refused) || refused.TenantID != "widgets" {
		t.Fatalf("ResolveAuditShard = %v, want *BarrierTenantLogNotSupportedError for widgets", err)
	}
}

// TestSystemShardLogResolver_RoutesOtherTenantsAsTheAuditResolverDoes: only
// a barrier tenant is refused beyond what the audit resolver refuses.
func TestSystemShardLogResolver_RoutesOtherTenantsAsTheAuditResolverDoes(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme":  {ID: "acme", IsolationType: tenant.IsolationPrefix},
		"vault": {ID: "vault", IsolationType: tenant.IsolationVolume},
	}}
	r := NewSystemShardLogResolver(&mockHost{}, 1, lookup)
	for _, id := range []string{tenantid.System, "acme", "ghost"} {
		if _, shardID, err := r.ResolveAuditShard(context.Background(), id); err != nil || shardID != 1 {
			t.Fatalf("ResolveAuditShard(%q) = %d, %v; want shard 1", id, shardID, err)
		}
	}
	var dedicated *DedicatedTenantAuditNotSupportedError
	if _, _, err := r.ResolveAuditShard(context.Background(), "vault"); !errors.As(err, &dedicated) {
		t.Fatalf("ResolveAuditShard(vault) = %v, want *DedicatedTenantAuditNotSupportedError", err)
	}
}

// TestSystemShardAuditResolver_RefusesDedicatedIsolationTenantsWithoutAHomeGroup is the
// explicit-refusal property: a volume or raft tenant must never silently
// get the system shard's chain under its own segment.
func TestSystemShardAuditResolver_RefusesDedicatedIsolationTenantsWithoutAHomeGroup(t *testing.T) {
	host := &mockHost{}
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"vault":  {ID: "vault", IsolationType: tenant.IsolationVolume},
		"shard1": {ID: "shard1", IsolationType: tenant.IsolationRaft},
	}}
	r := NewSystemShardAuditResolver(host, 1, lookup)
	for _, id := range []string{"vault", "shard1"} {
		_, _, err := r.ResolveAuditShard(context.Background(), id)
		var refused *DedicatedTenantAuditNotSupportedError
		if !errors.As(err, &refused) {
			t.Fatalf("tenant %q: err = %v, want *DedicatedTenantAuditNotSupportedError", id, err)
		}
		if refused.TenantID != id {
			t.Fatalf("refused.TenantID = %q, want %q", refused.TenantID, id)
		}
	}
}

func TestSystemShardAuditResolver_PropagatesLookupError(t *testing.T) {
	host := &mockHost{}
	lookupErr := errors.New("store unavailable")
	r := NewSystemShardAuditResolver(host, 1, &failingIsolationLookup{err: lookupErr})
	_, _, err := r.ResolveAuditShard(context.Background(), "acme")
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want %v", err, lookupErr)
	}
}

func TestDedicatedTenantAuditNotSupportedError_Error(t *testing.T) {
	err := &DedicatedTenantAuditNotSupportedError{TenantID: "vault", IsolationType: "volume"}
	if err.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
}

func TestAuditFeatureNotActiveError_Error(t *testing.T) {
	err := &AuditFeatureNotActiveError{TenantID: "acme", ShardID: 1}
	if err.Error() == "" {
		t.Fatal("Error() returned empty string")
	}
}

// fakeHomeGroups names one home group per tenant, or fails with err.
type fakeHomeGroups struct {
	host  Host
	err   error
	asked []string
}

func (f *fakeHomeGroups) HomeGroup(_ context.Context, tenantID string) (Host, uint64, error) {
	f.asked = append(f.asked, tenantID)
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.host, 100 + uint64(len(f.asked)), nil
}

func isolatedTenantsLookup() *fakeIsolationLookup {
	return &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme":    {ID: "acme", IsolationType: tenant.IsolationPrefix},
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
		"vault":   {ID: "vault", IsolationType: tenant.IsolationVolume},
		"shard1":  {ID: "shard1", IsolationType: tenant.IsolationRaft},
	}}
}

// TestSystemShardAuditResolver_RoutesIsolatedTenantsToTheirHomeGroup: with a
// home group attached, barrier, volume and raft tenants resolve to it, on
// both the audit and the log resolver.
func TestSystemShardAuditResolver_RoutesIsolatedTenantsToTheirHomeGroup(t *testing.T) {
	for name, build := range map[string]func(Host, uint64, AuditIsolationLookup) *SystemShardAuditResolver{
		"audit": NewSystemShardAuditResolver,
		"log":   NewSystemShardLogResolver,
	} {
		t.Run(name, func(t *testing.T) {
			homeHost := &mockHost{}
			homes := &fakeHomeGroups{host: homeHost}
			r := build(newChainShard(0, auditActive()).host, 1, isolatedTenantsLookup())
			r.SetHomeGroupSource(homes, newMemRouteStore())
			for i, id := range []string{"widgets", "vault", "shard1"} {
				gotHost, shardID, err := r.ResolveAuditShard(context.Background(), id)
				if err != nil {
					t.Fatalf("ResolveAuditShard(%q): %v", id, err)
				}
				if gotHost != homeHost || shardID != uint64(101+i) {
					t.Fatalf("ResolveAuditShard(%q) = (%v, %d), want the home group (%v, %d)", id, gotHost, shardID, homeHost, 101+i)
				}
			}
			if len(homes.asked) != 3 || homes.asked[0] != "widgets" {
				t.Fatalf("home groups asked for %v, want [widgets vault shard1]", homes.asked)
			}
		})
	}
}

// TestSystemShardAuditResolver_HomeGroupKeepsSharedTenantsOnTheSystemShard:
// a home group never takes the system, a prefix or an unregistered tenant.
func TestSystemShardAuditResolver_HomeGroupKeepsSharedTenantsOnTheSystemShard(t *testing.T) {
	host := &mockHost{}
	homes := &fakeHomeGroups{host: &mockHost{}}
	r := NewSystemShardAuditResolver(host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, newMemRouteStore())
	for _, id := range []string{tenantid.System, "acme", "ghost"} {
		gotHost, shardID, err := r.ResolveAuditShard(context.Background(), id)
		if err != nil || gotHost != host || shardID != 1 {
			t.Fatalf("ResolveAuditShard(%q) = (%v, %d, %v), want the system shard", id, gotHost, shardID, err)
		}
	}
	if len(homes.asked) != 0 {
		t.Fatalf("home groups asked for %v, want none", homes.asked)
	}
}

// TestSystemShardAuditResolver_HomeGroupErrorPropagates: a sealed or
// catching-up home is returned as is, never answered by the system shard.
func TestSystemShardAuditResolver_HomeGroupErrorPropagates(t *testing.T) {
	homeErr := errors.New("home sealed")
	r := NewSystemShardAuditResolver(newChainShard(0, auditActive()).host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(&fakeHomeGroups{err: homeErr}, newMemRouteStore())
	if gotHost, _, err := r.ResolveAuditShard(context.Background(), "widgets"); !errors.Is(err, homeErr) || gotHost != nil {
		t.Fatalf("ResolveAuditShard = (%v, %v), want (nil, %v)", gotHost, err, homeErr)
	}
}

// TestSystemShardAuditResolver_DetachedHomeGroupRestoresTheRefusals: a nil
// source detaches, and the no-home-group refusals apply again.
func TestSystemShardAuditResolver_DetachedHomeGroupRestoresTheRefusals(t *testing.T) {
	r := NewSystemShardLogResolver(&mockHost{}, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(&fakeHomeGroups{host: &mockHost{}}, newMemRouteStore())
	r.SetHomeGroupSource(nil, nil)
	var refused *BarrierTenantLogNotSupportedError
	if _, _, err := r.ResolveAuditShard(context.Background(), "widgets"); !errors.As(err, &refused) {
		t.Fatalf("ResolveAuditShard(widgets) = %v, want *BarrierTenantLogNotSupportedError", err)
	}
}

// floorIsolationLookup adds a cluster isolation floor to fakeIsolationLookup.
type floorIsolationLookup struct {
	*fakeIsolationLookup
	floor tenant.ClusterIsolationPolicy
}

func (f *floorIsolationLookup) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return f.floor, nil
}

// barrierTenantLookup is a lookup holding one barrier tenant, widgets, with
// the given policy, under the given cluster floor.
func barrierTenantLookup(policy tenant.TenantIsolationPolicy, floor tenant.ClusterIsolationPolicy) *floorIsolationLookup {
	return &floorIsolationLookup{
		fakeIsolationLookup: &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
			"acme":    {ID: "acme", IsolationType: tenant.IsolationPrefix},
			"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier, IsolationPolicy: policy},
		}},
		floor: floor,
	}
}

// TestSystemShardAuditResolver_StrictTenantAppendOnASystemRouteIsRefused: a
// barrier tenant whose audit route is the system shard (under the node key)
// gets no append there once its effective policy is strict -- its own policy
// or the cluster floor -- and its history stays readable.
func TestSystemShardAuditResolver_StrictTenantAppendOnASystemRouteIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, lookup := range map[string]*floorIsolationLookup{
		"tenant policy": barrierTenantLookup(tenant.TenantIsolationPolicyStrict, ""),
		"cluster floor": barrierTenantLookup("", tenant.ClusterIsolationPolicyStrict),
	} {
		t.Run(name, func(t *testing.T) {
			system := newChainShard(3, auditActive())
			homes, homeCmds := homeShard()
			r := NewSystemShardAuditResolver(system.host, 1, lookup)
			routes := newMemRouteStore()
			if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
				t.Fatalf("PutIfAbsent: %v", err)
			}
			r.SetHomeGroupSource(homes, routes)
			logger := NewReplicatedLogger(r)

			err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"})
			var sa *tenant.SystemAuditRouteError
			if !errors.As(err, &sa) || sa.TenantID != "widgets" {
				t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
			}
			if len(*system.cmds) != 0 || len(*homeCmds) != 0 {
				t.Fatalf("proposals: system %d, home %d; want none", len(*system.cmds), len(*homeCmds))
			}
			if report, err := logger.VerifyTenantChain(ctx, "widgets"); err != nil || report.Head != 3 {
				t.Fatalf("VerifyTenantChain = %+v, %v; want the system shard's chain readable", report, err)
			}
		})
	}
}

// losingRouteStore is a route store on which this writer loses the
// first-writer-wins record to another writer's system route: its first Get
// finds nothing, and PutIfAbsent records "system" as the other writer's
// winner and reports won=false.
type losingRouteStore struct {
	*memRouteStore
	gets int
}

func (s *losingRouteStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets++
	if s.gets == 1 {
		return nil, nil
	}
	return s.memRouteStore.Get(ctx, key)
}

func (s *losingRouteStore) PutIfAbsent(ctx context.Context, key string, _ []byte) (bool, error) {
	if _, err := s.memRouteStore.PutIfAbsent(ctx, key, []byte(AuditRouteSystem)); err != nil {
		return false, err
	}
	return false, nil
}

// TestSystemShardAuditResolver_StrictAppendLosingToARecordedSystemRouteIsRefused:
// a strict tenant whose own derived route is home but which loses the record
// to another writer's system route gets no append on the system shard: the
// check runs on the route that won.
func TestSystemShardAuditResolver_StrictAppendLosingToARecordedSystemRouteIsRefused(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(0, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, barrierTenantLookup(tenant.TenantIsolationPolicyStrict, ""))
	routes := &losingRouteStore{memRouteStore: newMemRouteStore()}
	r.SetHomeGroupSource(homes, routes)

	err := NewReplicatedLogger(r).Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"})
	var sa *tenant.SystemAuditRouteError
	if !errors.As(err, &sa) || sa.TenantID != "widgets" {
		t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
	}
	if got := routes.route("widgets"); got != AuditRouteSystem {
		t.Fatalf("recorded route = %q, want the other writer's %q", got, AuditRouteSystem)
	}
	if len(*system.cmds) != 0 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want none", len(*system.cmds), len(*homeCmds))
	}
}

// tighteningIsolationLookup reports a tightening marker (or a marker read
// error) for every tenant.
type tighteningIsolationLookup struct {
	*floorIsolationLookup
	tightening bool
	err        error
}

func (l *tighteningIsolationLookup) IsolationTightening(context.Context, string) (bool, error) {
	return l.tightening, l.err
}

// TestSystemShardAuditResolver_SystemAppendDuringTighteningIsRefused: a
// tenant that is not strict yet but has a tightening in flight gets no
// append on its system route; a marker read error fails closed; with no
// tightening the append lands.
func TestSystemShardAuditResolver_SystemAppendDuringTighteningIsRefused(t *testing.T) {
	ctx := context.Background()
	markerErr := errors.New("marker read failed")
	for name, tc := range map[string]struct {
		tightening bool
		err        error
		wantCmds   int
	}{
		"tightening":    {tightening: true},
		"marker error":  {err: markerErr},
		"no tightening": {wantCmds: 1},
	} {
		t.Run(name, func(t *testing.T) {
			system := newChainShard(3, auditActive())
			homes, homeCmds := homeShard()
			lookup := &tighteningIsolationLookup{
				floorIsolationLookup: barrierTenantLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess),
				tightening:           tc.tightening,
				err:                  tc.err,
			}
			r := NewSystemShardAuditResolver(system.host, 1, lookup)
			routes := newMemRouteStore()
			if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
				t.Fatalf("PutIfAbsent: %v", err)
			}
			r.SetHomeGroupSource(homes, routes)

			err := NewReplicatedLogger(r).Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"})
			switch {
			case tc.tightening:
				var sa *tenant.SystemAuditRouteError
				if !errors.As(err, &sa) || sa.TenantID != "widgets" {
					t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
				}
			case tc.err != nil:
				if !errors.Is(err, markerErr) {
					t.Fatalf("Emit = %v, want the marker read error", err)
				}
			default:
				if err != nil {
					t.Fatalf("Emit: %v", err)
				}
			}
			if len(*system.cmds) != tc.wantCmds || len(*homeCmds) != 0 {
				t.Fatalf("proposals: system %d, home %d; want system %d, home 0", len(*system.cmds), len(*homeCmds), tc.wantCmds)
			}
		})
	}
}

// TestSystemShardAuditResolver_StrictTenantAppendOnAHomeRouteSucceeds: a
// strict barrier tenant with no chain on the system shard appends to its
// home group.
func TestSystemShardAuditResolver_StrictTenantAppendOnAHomeRouteSucceeds(t *testing.T) {
	system := newChainShard(0, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, barrierTenantLookup(tenant.TenantIsolationPolicyStrict, ""))
	routes := newMemRouteStore()
	r.SetHomeGroupSource(homes, routes)
	if err := NewReplicatedLogger(r).Emit(context.Background(), audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 0 || len(*homeCmds) != 1 {
		t.Fatalf("proposals: system %d, home %d; want the event on the home group", len(*system.cmds), len(*homeCmds))
	}
	if got := routes.route("widgets"); got != AuditRouteHome {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteHome)
	}
}

// TestSystemShardAuditResolver_NonStrictTenantAppendOnASystemRouteSucceeds:
// a barrier tenant that is not strict keeps appending to its system-shard
// chain.
func TestSystemShardAuditResolver_NonStrictTenantAppendOnASystemRouteSucceeds(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(3, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, barrierTenantLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess))
	routes := newMemRouteStore()
	if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	r.SetHomeGroupSource(homes, routes)
	if err := NewReplicatedLogger(r).Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 1 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want the event on the system shard's chain", len(*system.cmds), len(*homeCmds))
	}
}

// Without a home group (standalone) a barrier tenant's appends can only go
// to the system shard, under the node key: a strict one gets none there
// (decision S9), whether or not its chain has records yet; reads stay
// allowed.
func TestSystemShardAuditResolver_StrictBarrierAppendWithoutAHomeGroupIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, lookup := range map[string]func() *floorIsolationLookup{
		"tenant policy": func() *floorIsolationLookup { return barrierTenantLookup(tenant.TenantIsolationPolicyStrict, "") },
		"cluster floor": func() *floorIsolationLookup { return barrierTenantLookup("", tenant.ClusterIsolationPolicyStrict) },
	} {
		for _, head := range []uint64{0, 3} {
			t.Run(fmt.Sprintf("%s/head %d", name, head), func(t *testing.T) {
				system := newChainShard(head, auditActive())
				r := NewSystemShardAuditResolver(system.host, 1, lookup())
				err := NewReplicatedLogger(r).Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"})
				var sa *tenant.SystemAuditRouteError
				if !errors.As(err, &sa) || sa.TenantID != "widgets" {
					t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
				}
				if len(*system.cmds) != 0 {
					t.Fatalf("system shard proposals = %d, want none", len(*system.cmds))
				}
				if _, shard, err := r.ResolveAuditShard(ctx, "widgets"); err != nil || shard != 1 {
					t.Fatalf("ResolveAuditShard(widgets) = %d, %v; want shard 1", shard, err)
				}
			})
		}
	}
	t.Run("not strict", func(t *testing.T) {
		system := newChainShard(3, auditActive())
		r := NewSystemShardAuditResolver(system.host, 1, barrierTenantLookup("", ""))
		if err := NewReplicatedLogger(r).Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if len(*system.cmds) != 1 {
			t.Fatalf("system shard proposals = %d, want the event on shard 1", len(*system.cmds))
		}
	})
}

// TestSystemShardAuditResolver_SystemAuditRoute: the probe reports a
// recorded system route, and with none recorded a chain already on the
// system shard; it records no route, and a tenant that is not a barrier
// tenant is never on a system route.
func TestSystemShardAuditResolver_SystemAuditRoute(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		tenantID string
		head     uint64
		recorded string
		homes    bool
		want     bool
	}{
		{"recorded system route", "widgets", 0, AuditRouteSystem, true, true},
		{"recorded home route", "widgets", 3, AuditRouteHome, true, false},
		{"no route, chain on the system shard", "widgets", 3, "", true, true},
		{"no route, no chain", "widgets", 0, "", true, false},
		{"prefix tenant", "acme", 3, "", true, false},
		{"unregistered tenant", "nobody", 3, "", true, false},
		{"no home group, no chain", "widgets", 0, "", false, true},
		{"no home group, chain on the system shard", "widgets", 3, "", false, true},
		{"no home group, prefix tenant", "acme", 3, "", false, false},
		{"no home group, unregistered tenant", "nobody", 3, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			system := newChainShard(tc.head, auditActive())
			homes, _ := homeShard()
			r := NewSystemShardAuditResolver(system.host, 1, barrierTenantLookup("", ""))
			routes := newMemRouteStore()
			if tc.recorded != "" {
				if _, err := routes.PutIfAbsent(ctx, AuditRouteKey(tc.tenantID), []byte(tc.recorded)); err != nil {
					t.Fatalf("PutIfAbsent: %v", err)
				}
			}
			if tc.homes {
				r.SetHomeGroupSource(homes, routes)
			}
			got, err := r.SystemAuditRoute(ctx, tc.tenantID)
			if err != nil || got != tc.want {
				t.Fatalf("SystemAuditRoute(%s) = %v, %v; want %v", tc.tenantID, got, err, tc.want)
			}
			if tc.recorded == "" && routes.route(tc.tenantID) != "" {
				t.Fatalf("SystemAuditRoute recorded route %q, want none", routes.route(tc.tenantID))
			}
		})
	}
}
