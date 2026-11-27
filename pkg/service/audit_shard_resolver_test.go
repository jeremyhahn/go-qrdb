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
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
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
	r := NewSystemShardLogResolver(host, 1, nil, nil)
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
	r := NewSystemShardLogResolver(host, 1, nil, nil)
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
	r := NewSystemShardLogResolver(host, 1, &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{}}, nil)
	_, shardID, err := r.ResolveAuditShard(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("ResolveAuditShard: %v", err)
	}
	if shardID != 1 {
		t.Fatalf("shardID = %d, want 1", shardID)
	}
}

func TestSystemShardAuditResolver_RoutesPrefixTenantsToTheSystemShard(t *testing.T) {
	host := &mockHost{}
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme": {ID: "acme", IsolationType: tenant.IsolationPrefix},
	}}
	r := NewSystemShardLogResolver(host, 1, lookup, &fakeLogGroups{})
	gotHost, shardID, err := r.ResolveAuditShard(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ResolveAuditShard(acme): %v", err)
	}
	if gotHost != host || shardID != 1 {
		t.Fatalf("tenant acme resolved (%v, %d), want the system shard 1", gotHost, shardID)
	}
}

// TestSystemShardAuditResolver_RoutesBarrierTenantsToTheirHomeGroup: a barrier
// tenant's audit instance and its other logs live in its home group, under the
// tenant's key, for the audit trail and the log service alike.
// TestSystemShardAuditResolver_RoutesBarrierTenantsToTheSystemShard pins
// main's audit routing: the audit logger writes a barrier tenant's events
// to the system shard, even when a group source is wired, and a sealed
// barrier tenant is not consulted.
func TestSystemShardAuditResolver_RoutesBarrierTenantsToTheSystemShard(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
		"vault":   {ID: "vault", IsolationType: tenant.IsolationBarrier},
	}}
	system := &mockHost{}
	r := NewSystemShardAuditResolver(system, 1, lookup)
	for _, id := range []string{"widgets", "vault"} {
		host, shardID, err := r.ResolveAuditShard(context.Background(), id)
		if err != nil || shardID != 1 || host != system {
			t.Fatalf("ResolveAuditShard(%q) = %v, %d, %v; want the system shard 1", id, host, shardID, err)
		}
	}
}

// TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup pins the
// log resolver: a barrier tenant's own logs live in its home group.
func TestSystemShardLogResolver_RoutesBarrierTenantsToTheirHomeGroup(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"widgets": {ID: "widgets", IsolationType: tenant.IsolationBarrier},
		"vault":   {ID: "vault", IsolationType: tenant.IsolationBarrier},
	}}
	home := &mockHost{}
	groups := &fakeLogGroups{host: home, homes: map[string]uint64{"widgets": 9}, sealed: map[string]bool{"vault": true}}
	r := NewSystemShardLogResolver(&mockHost{}, 1, lookup, groups)

	host, shardID, err := r.ResolveAuditShard(context.Background(), "widgets")
	if err != nil || shardID != 9 || host != home {
		t.Fatalf("ResolveAuditShard(widgets) = %v, %d, %v; want the home group 9", host, shardID, err)
	}
	if _, _, err := r.ResolveAuditShard(context.Background(), "vault"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ResolveAuditShard(vault) = %v, want tenant.ErrTenantSealed", err)
	}

	// Without a group source a barrier tenant fails closed.
	bare := NewSystemShardLogResolver(&mockHost{}, 1, lookup, nil)
	var unavailable *BarrierStoreUnavailableError
	if _, _, err := bare.ResolveAuditShard(context.Background(), "widgets"); !errors.As(err, &unavailable) || unavailable.TenantID != "widgets" {
		t.Fatalf("no groups: ResolveAuditShard(widgets) = %v, want *BarrierStoreUnavailableError", err)
	}
}

// fakeLogGroups is a LogGroupSource: homes maps a tenant to its home group,
// a sealed tenant is refused with tenant.ErrTenantSealed, and every database
// group is shard 11.
type fakeLogGroups struct {
	host   Host
	homes  map[string]uint64
	sealed map[string]bool
}

func (g *fakeLogGroups) TenantHomeGroup(_ context.Context, tenantID string) (Host, uint64, error) {
	if g.sealed[tenantID] {
		return nil, 0, tenant.ErrTenantSealed
	}
	return g.host, g.homes[tenantID], nil
}

// TestSystemShardAuditResolver_RoutesNonBarrierTenantsAsWithoutGroups: only
// a barrier tenant uses the group source; every other tenant routes as it
// does with none.
func TestSystemShardAuditResolver_RoutesNonBarrierTenantsAsWithoutGroups(t *testing.T) {
	lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{
		"acme":  {ID: "acme", IsolationType: tenant.IsolationPrefix},
		"vault": {ID: "vault", IsolationType: tenant.IsolationVolume},
	}}
	r := NewSystemShardLogResolver(&mockHost{}, 1, lookup, &fakeLogGroups{})
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
	r := NewSystemShardLogResolver(host, 1, lookup, nil)
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
	r := NewSystemShardLogResolver(host, 1, &failingIsolationLookup{err: lookupErr}, nil)
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
		"log": func(host Host, shardID uint64, isolation AuditIsolationLookup) *SystemShardAuditResolver {
			return NewSystemShardLogResolver(host, shardID, isolation, nil)
		},
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

// TestSystemShardAuditResolver_DetachedHomeGroupFailsClosed: a nil source
// detaches, and a barrier tenant with no log group fails closed.
func TestSystemShardAuditResolver_DetachedHomeGroupFailsClosed(t *testing.T) {
	r := NewSystemShardLogResolver(&mockHost{}, 1, isolatedTenantsLookup(), nil)
	r.SetHomeGroupSource(&fakeHomeGroups{host: &mockHost{}}, newMemRouteStore())
	r.SetHomeGroupSource(nil, nil)
	var unavailable *BarrierStoreUnavailableError
	if _, _, err := r.ResolveAuditShard(context.Background(), "widgets"); !errors.As(err, &unavailable) {
		t.Fatalf("ResolveAuditShard(widgets) = %v, want *BarrierStoreUnavailableError", err)
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
	// The other writer's system route carries no chain, so the refusal
	// releases it: the tenant is never stranded on it.
	if got := routes.route("widgets"); got != "" {
		t.Fatalf("recorded route = %q, want the unchained system route released", got)
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

// TestSystemShardAuditResolver_TwoResolversSharingRouteStoreSeeReleasedSystemRoute:
// two resolvers (two nodes) share one route store. One resolves widgets by
// its recorded system route; the other, refusing an append during a
// tightening, releases that chainless route, and a later append records
// home. The first resolver then resolves home: it never cached the system
// route.
func TestSystemShardAuditResolver_TwoResolversSharingRouteStoreSeeReleasedSystemRoute(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(0, auditActive())
	homes, _ := homeShard()
	routes := newMemRouteStore()
	if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	policy := func() *tighteningIsolationLookup {
		return &tighteningIsolationLookup{
			floorIsolationLookup: barrierTenantLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess),
		}
	}
	firstLookup, secondLookup := policy(), policy()
	first := NewSystemShardAuditResolver(system.host, 1, firstLookup)
	first.SetHomeGroupSource(homes, routes)
	second := NewSystemShardAuditResolver(system.host, 1, secondLookup)
	second.SetHomeGroupSource(homes, routes)

	if host, _, err := first.ResolveAuditAppend(ctx, "widgets"); err != nil || host != Host(system.host) {
		t.Fatalf("first ResolveAuditAppend = %v, %v; want the system shard", host, err)
	}

	secondLookup.tightening = true
	var refused *tenant.SystemAuditRouteError
	if _, _, err := second.ResolveAuditAppend(ctx, "widgets"); !errors.As(err, &refused) {
		t.Fatalf("second ResolveAuditAppend during the tightening = %v, want *tenant.SystemAuditRouteError", err)
	}
	if recorded, _ := routes.Get(ctx, AuditRouteKey("widgets")); recorded != nil {
		t.Fatalf("route after the refusal = %q, want it released", recorded)
	}

	secondLookup.tightening = false
	if host, _, err := second.ResolveAuditAppend(ctx, "widgets"); err != nil || host != Host(homes.host) {
		t.Fatalf("second ResolveAuditAppend after the release = %v, %v; want the home group", host, err)
	}
	if recorded, _ := routes.Get(ctx, AuditRouteKey("widgets")); string(recorded) != AuditRouteHome {
		t.Fatalf("route = %q, want %q", recorded, AuditRouteHome)
	}
	for name, resolve := range map[string]func(context.Context, string) (Host, uint64, error){
		"append": first.ResolveAuditAppend,
		"read":   first.ResolveAuditShard,
	} {
		if host, _, err := resolve(ctx, "widgets"); err != nil || host != Host(homes.host) {
			t.Fatalf("first %s after another resolver recorded home = %v, %v; want the home group", name, host, err)
		}
	}
}

// TestSystemShardAuditResolver_ReleaseWaitsForInFlightAppend: an append
// resolved by widgets' chainless system route pauses before its proposal
// returns. A tightening begins; an append it refuses does not retire the
// route, and the tightening's probe still reports it. Once the paused
// append returns, the next refusal retires the route.
func TestSystemShardAuditResolver_ReleaseWaitsForInFlightAppend(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(0, auditActive())
	proposing, proceed := make(chan struct{}), make(chan struct{})
	var paused sync.Once
	hold := func(cmd []byte) {
		paused.Do(func() {
			close(proposing)
			<-proceed
		})
		*system.cmds = append(*system.cmds, append([]byte(nil), cmd...))
	}
	system.host.syncProposeFn = func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		hold(cmd)
		return sm.Result{Value: 1}, nil
	}
	system.host.proposeWithModeFn = func(_ context.Context, _ uint64, cmd []byte, _ writemode.Mode) (sm.Result, error) {
		hold(cmd)
		return sm.Result{Value: 1}, nil
	}
	homes, homeCmds := homeShard()
	lookup := &tighteningIsolationLookup{
		floorIsolationLookup: barrierTenantLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess),
	}
	r := NewSystemShardAuditResolver(system.host, 1, lookup)
	routes := newMemRouteStore()
	if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)

	done := make(chan error, 1)
	go func() { done <- logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}) }()
	<-proposing

	lookup.tightening = true
	var refused *tenant.SystemAuditRouteError
	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); !errors.As(err, &refused) {
		t.Fatalf("Emit during the tightening = %v, want *tenant.SystemAuditRouteError", err)
	}
	if recorded, _ := routes.Get(ctx, AuditRouteKey("widgets")); string(recorded) != AuditRouteSystem {
		t.Fatalf("route while an append is in flight = %q, want %q", recorded, AuditRouteSystem)
	}
	if onSystem, err := r.SystemAuditRoute(ctx, "widgets"); err != nil || !onSystem {
		t.Fatalf("SystemAuditRoute while an append is in flight = %v, %v; want true", onSystem, err)
	}

	close(proceed)
	if err := <-done; err != nil {
		t.Fatalf("in-flight Emit: %v", err)
	}
	if len(*system.cmds) != 1 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want system 1, home 0", len(*system.cmds), len(*homeCmds))
	}

	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); !errors.As(err, &refused) {
		t.Fatalf("Emit after the in-flight append returned = %v, want *tenant.SystemAuditRouteError", err)
	}
	if recorded, _ := routes.Get(ctx, AuditRouteKey("widgets")); recorded != nil {
		t.Fatalf("route after the in-flight append returned = %q, want it released", recorded)
	}
}

// epochIsolationLookup is a tighteningIsolationLookup that also reads the
// tenant's and the floor's isolation epochs (tenant.IsolationEpochReader)
// from a settable map, logging every read it serves in order.
type epochIsolationLookup struct {
	*tighteningIsolationLookup
	mu       sync.Mutex
	epochs   map[string][]byte
	epochErr error
	calls    []string
}

func newEpochIsolationLookup(policy tenant.TenantIsolationPolicy, floor tenant.ClusterIsolationPolicy, epochs map[string][]byte) *epochIsolationLookup {
	return &epochIsolationLookup{
		tighteningIsolationLookup: &tighteningIsolationLookup{floorIsolationLookup: barrierTenantLookup(policy, floor)},
		epochs:                    epochs,
	}
}

func (l *epochIsolationLookup) record(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *epochIsolationLookup) log() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

func (l *epochIsolationLookup) IsolationEpochs(context.Context, string) (map[string][]byte, error) {
	l.record("IsolationEpochs")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.epochErr != nil {
		return nil, l.epochErr
	}
	return maps.Clone(l.epochs), nil
}

func (l *epochIsolationLookup) setEpoch(key string, value []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.epochs == nil {
		l.epochs = make(map[string][]byte)
	}
	l.epochs[key] = value
}

func (l *epochIsolationLookup) IsolationTightening(ctx context.Context, tenantID string) (bool, error) {
	l.record("IsolationTightening")
	return l.tighteningIsolationLookup.IsolationTightening(ctx, tenantID)
}

func (l *epochIsolationLookup) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	l.record("GetTenant")
	return l.tighteningIsolationLookup.GetTenant(ctx, tenantID)
}

// routedResolver is a resolver over system with home groups attached and
// widgets' audit route recorded as route ("" records none).
func routedResolver(t *testing.T, system *chainShard, lookup AuditIsolationLookup, route string) *SystemShardAuditResolver {
	t.Helper()
	homes, _ := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, lookup)
	routes := newMemRouteStore()
	if route != "" {
		if _, err := routes.PutIfAbsent(context.Background(), AuditRouteKey("widgets"), []byte(route)); err != nil {
			t.Fatalf("PutIfAbsent: %v", err)
		}
	}
	r.SetHomeGroupSource(homes, routes)
	return r
}

// heldAppends reports how many appends r counts on tenantID's system route.
func heldAppends(r *SystemShardAuditResolver, tenantID string) int {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	return r.inflight[tenantID]
}

// TestSystemShardAuditResolver_HoldAuditAppendGuardsSystemRouteOnIsolationEpochs:
// an append held on a recorded system route carries one value-equal compare
// per isolation epoch key of the tenant and the floor, holding the values
// read (an absent key compared as absent); a home route, the system tenant
// and a lookup that cannot read epochs get the zero guard; a failed epoch
// read fails the append and leaves no hold behind.
func TestSystemShardAuditResolver_HoldAuditAppendGuardsSystemRouteOnIsolationEpochs(t *testing.T) {
	ctx := context.Background()
	keys := tenant.IsolationEpochKeys("widgets")
	newLookup := func() *epochIsolationLookup {
		return newEpochIsolationLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess,
			map[string][]byte{keys[0]: []byte("epoch-1")})
	}

	t.Run("recorded system route", func(t *testing.T) {
		lookup := newLookup()
		r := routedResolver(t, newChainShard(3, auditActive()), lookup, AuditRouteSystem)
		_, shardID, release, guard, err := r.HoldAuditAppend(ctx, "widgets")
		if err != nil || shardID != 1 {
			t.Fatalf("HoldAuditAppend = shard %d, %v; want the system shard", shardID, err)
		}
		defer release()
		if heldAppends(r, "widgets") != 1 {
			t.Fatalf("held = %d, want 1", heldAppends(r, "widgets"))
		}
		if len(guard.compares) != len(keys) {
			t.Fatalf("guard has %d compares, want %d", len(guard.compares), len(keys))
		}
		for i, frame := range guard.compares {
			c, _, err := protocol.DecodeCompare(frame)
			if err != nil {
				t.Fatalf("DecodeCompare: %v", err)
			}
			if string(c.Key) != keys[i] || c.Target != byte(CompareValue) || c.Result != byte(CompareEqual) {
				t.Fatalf("compare %d = key %q target %d result %d; want %q value-equal", i, c.Key, c.Target, c.Result, keys[i])
			}
			if want := lookup.epochs[keys[i]]; !bytes.Equal(c.Value, want) {
				t.Fatalf("compare %d value = %q, want %q", i, c.Value, want)
			}
		}
		if c, _, _ := protocol.DecodeCompare(guard.compares[1]); len(c.Value) != 0 {
			t.Fatalf("absent floor epoch compared as %q, want absent", c.Value)
		}
	})

	t.Run("home route", func(t *testing.T) {
		r := routedResolver(t, newChainShard(3, auditActive()), newLookup(), AuditRouteHome)
		_, _, release, guard, err := r.HoldAuditAppend(ctx, "widgets")
		if err != nil {
			t.Fatalf("HoldAuditAppend: %v", err)
		}
		release()
		if !guard.IsZero() {
			t.Fatal("home route carries a guard, want the zero guard")
		}
	})

	t.Run("system tenant", func(t *testing.T) {
		lookup := newLookup()
		r := routedResolver(t, newChainShard(3, auditActive()), lookup, "")
		_, _, release, guard, err := r.HoldAuditAppend(ctx, tenantid.System)
		if err != nil {
			t.Fatalf("HoldAuditAppend: %v", err)
		}
		release()
		if !guard.IsZero() {
			t.Fatal("system tenant carries a guard, want the zero guard")
		}
		if slices.Contains(lookup.log(), "IsolationEpochs") {
			t.Fatalf("calls = %v, want no IsolationEpochs read", lookup.log())
		}
	})

	t.Run("lookup without the epoch reader", func(t *testing.T) {
		lookup := newLookup().tighteningIsolationLookup
		r := routedResolver(t, newChainShard(3, auditActive()), lookup, AuditRouteSystem)
		_, _, release, guard, err := r.HoldAuditAppend(ctx, "widgets")
		if err != nil {
			t.Fatalf("HoldAuditAppend: %v", err)
		}
		release()
		if !guard.IsZero() {
			t.Fatal("lookup without IsolationEpochs yields a guard, want the zero guard")
		}
	})

	t.Run("epoch read error", func(t *testing.T) {
		lookup := newLookup()
		readErr := errors.New("epoch read failed")
		lookup.epochErr = readErr
		r := routedResolver(t, newChainShard(0, auditActive()), lookup, AuditRouteSystem)
		_, _, release, guard, err := r.HoldAuditAppend(ctx, "widgets")
		if !errors.Is(err, readErr) || release != nil || !guard.IsZero() {
			t.Fatalf("HoldAuditAppend = release %v, guard zero %v, %v; want %v and nothing held", release != nil, guard.IsZero(), err, readErr)
		}
		if heldAppends(r, "widgets") != 0 {
			t.Fatalf("held = %d after a failed epoch read, want 0", heldAppends(r, "widgets"))
		}
	})
}

// TestSystemShardAuditResolver_HoldReadsEpochsBeforeStrictCheck: the
// epochs the guard compares are read before the strict check (the marker
// and the tenant), so a tightening that wrote its epoch after the read is
// refused at apply time and one that wrote it before is seen by the check.
func TestSystemShardAuditResolver_HoldReadsEpochsBeforeStrictCheck(t *testing.T) {
	ctx := context.Background()
	for name, policy := range map[string]tenant.TenantIsolationPolicy{
		"appended": tenant.TenantIsolationPolicyInherit,
		"refused":  tenant.TenantIsolationPolicyStrict,
	} {
		t.Run(name, func(t *testing.T) {
			lookup := newEpochIsolationLookup(policy, tenant.ClusterIsolationPolicyOperatorAccess, nil)
			r := routedResolver(t, newChainShard(0, auditActive()), lookup, AuditRouteSystem)
			_, _, release, _, err := r.HoldAuditAppend(ctx, "widgets")
			if release != nil {
				release()
			}
			var refused *tenant.SystemAuditRouteError
			if (policy == tenant.TenantIsolationPolicyStrict) != errors.As(err, &refused) {
				t.Fatalf("HoldAuditAppend: %v", err)
			}
			calls := lookup.log()
			if slices.Index(calls, "IsolationEpochs") < 0 || slices.Index(calls, "IsolationTightening") < 0 {
				t.Fatalf("calls = %v, want IsolationEpochs and IsolationTightening", calls)
			}
			// Each attempt reads the epochs first: every strict-check read
			// follows an epoch read.
			if calls[0] != "IsolationEpochs" {
				t.Fatalf("calls = %v, want IsolationEpochs first", calls)
			}
		})
	}
}

// TestSystemShardAuditResolver_AuditGuardRefusalMapsOnlyEpochKeys: a
// refused guard is reported as the strict refusal only when it is on one of
// the tenant's isolation epoch keys and the tenant is now strict or being
// tightened; any other refusal stays the retryable one.
func TestSystemShardAuditResolver_AuditGuardRefusalMapsOnlyEpochKeys(t *testing.T) {
	ctx := context.Background()
	keys := tenant.IsolationEpochKeys("widgets")
	for name, tc := range map[string]struct {
		key        string
		policy     tenant.TenantIsolationPolicy
		floor      tenant.ClusterIsolationPolicy
		tightening bool
		wantStrict bool
	}{
		"not an epoch key":         {key: "widgets/data", policy: tenant.TenantIsolationPolicyStrict},
		"epoch key, not strict":    {key: keys[0], policy: tenant.TenantIsolationPolicyInherit, floor: tenant.ClusterIsolationPolicyOperatorAccess},
		"epoch key, tenant strict": {key: keys[0], policy: tenant.TenantIsolationPolicyStrict, wantStrict: true},
		"floor key, floor strict":  {key: keys[1], floor: tenant.ClusterIsolationPolicyStrict, wantStrict: true},
		"epoch key, tightening":    {key: keys[0], policy: tenant.TenantIsolationPolicyInherit, floor: tenant.ClusterIsolationPolicyOperatorAccess, tightening: true, wantStrict: true},
	} {
		t.Run(name, func(t *testing.T) {
			lookup := newEpochIsolationLookup(tc.policy, tc.floor, nil)
			lookup.tightening = tc.tightening
			r := routedResolver(t, newChainShard(0, auditActive()), lookup, AuditRouteSystem)
			err := r.auditGuardRefusal(ctx, "widgets", &WriteGuardRefusedError{Key: tc.key})
			var refused *tenant.SystemAuditRouteError
			if got := errors.As(err, &refused); got != tc.wantStrict || (!tc.wantStrict && err != nil) {
				t.Fatalf("auditGuardRefusal = %v, want strict refusal %v", err, tc.wantStrict)
			}
			if tc.wantStrict && refused.TenantID != "widgets" {
				t.Fatalf("refused tenant = %q, want widgets", refused.TenantID)
			}
		})
	}
}

// TestSystemShardAuditResolver_NoHomeGroupBarrierAppendIsUnguarded pins
// that a barrier tenant on a node without home groups appends to the system
// shard with the zero guard. That needs no guard: the tightening's probe
// (SystemAuditRoute, read by tenant.isolationChangeGuard.tightenToStrict
// after it writes the epoch) reports a system route for every such tenant,
// so it is never tightened to strict while it appends there.
func TestSystemShardAuditResolver_NoHomeGroupBarrierAppendIsUnguarded(t *testing.T) {
	ctx := context.Background()
	lookup := newEpochIsolationLookup(tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess,
		map[string][]byte{tenant.IsolationEpochKeys("widgets")[0]: []byte("epoch-1")})
	r := NewSystemShardAuditResolver(newChainShard(0, auditActive()).host, 1, lookup)

	_, shardID, release, guard, err := r.HoldAuditAppend(ctx, "widgets")
	if err != nil || shardID != 1 {
		t.Fatalf("HoldAuditAppend = shard %d, %v; want the system shard", shardID, err)
	}
	release()
	if !guard.IsZero() {
		t.Fatal("no-home-group barrier append carries a guard, want the zero guard")
	}
	system, err := r.SystemAuditRoute(ctx, "widgets")
	if err != nil || !system {
		t.Fatalf("SystemAuditRoute = %v, %v; want true: the probe refuses the tightening", system, err)
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

// memRouteStore is an in-memory AuditRouteStore with the replicated store's
// first-writer-wins PutIfAbsent.
type memRouteStore struct {
	mu   sync.Mutex
	data map[string][]byte
	// beforePutIfAbsent, when set, pauses the next PutIfAbsent: it signals
	// reached, then waits for release.
	beforePutIfAbsent *putPause
}

// putPause is a channel pair that holds a writer immediately before its
// route claim.
type putPause struct {
	reached chan struct{}
	release chan struct{}
}

func newMemRouteStore() *memRouteStore { return &memRouteStore{data: map[string][]byte{}} }

func (s *memRouteStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}

func (s *memRouteStore) CompareAndDelete(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, ok := s.data[key]; !ok || string(got) != string(value) {
		return false, nil
	}
	delete(s.data, key)
	return true, nil
}

func (s *memRouteStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	pause := s.beforePutIfAbsent
	s.beforePutIfAbsent = nil
	s.mu.Unlock()
	if pause != nil {
		close(pause.reached)
		<-pause.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.data[key] = append([]byte(nil), value...)
	return true, nil
}

func (s *memRouteStore) route(tenantID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.data[AuditRouteKey(tenantID)])
}

// chainShard is the system shard as the route sees it: a recording host
// whose replicated chain for "widgets" holds head events, gated per feature.
type chainShard struct {
	host  *perFeatureHost
	cmds  *[][]byte
	reads []any
}

func newChainShard(head uint64, on map[feature.Feature]bool) *chainShard {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	c := &chainShard{cmds: cmds}
	inner.syncReadFn = func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
		c.reads = append(c.reads, query)
		switch query.(type) {
		case audit.VerifyRequest:
			return &audit.ChainContinuityReport{Verified: true, Records: int64(head), Head: head, Low: min(head, 1), Genesis: head > 0}, nil
		case audit.QueryRequest:
			return &audit.Page{}, nil
		}
		return nil, nil
	}
	c.host = &perFeatureHost{mockHost: inner, on: on}
	return c
}

func auditActive() map[feature.Feature]bool {
	return map[feature.Feature]bool{
		featuregate.AuditAppend:        true,
		featuregate.VerifiableLog:      true,
		featuregate.VerifiableLogAudit: true,
	}
}

// mixedCapability is a cluster where some replica does not yet advertise
// the audit instance: VerifiableLogAudit is not activated.
func mixedCapability() map[feature.Feature]bool {
	return map[feature.Feature]bool{featuregate.AuditAppend: true, featuregate.VerifiableLog: true}
}

func homeShard() (*fakeHomeGroups, *[][]byte) {
	inner, cmds := recordingHost(sm.Result{Value: 1})
	return &fakeHomeGroups{host: &perFeatureHost{mockHost: inner, on: auditActive()}}, cmds
}

// TestAuditRoute_ChainOnTheSystemShardKeepsItsRoute: a barrier tenant whose
// replicated chain already has records on the system shard keeps that
// route after the home groups attach, for writes, Query and Verify, so its
// history stays readable and its next event extends the same chain.
func TestAuditRoute_ChainOnTheSystemShardKeepsItsRoute(t *testing.T) {
	system := newChainShard(3, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	routes := newMemRouteStore()
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)
	ctx := context.Background()

	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 1 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want the event on the system shard's chain", len(*system.cmds), len(*homeCmds))
	}
	if _, err := logger.Query(ctx, audit.Filter{TenantID: "widgets"}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	report, err := logger.VerifyTenantChain(ctx, "widgets")
	if err != nil || report.Head != 3 {
		t.Fatalf("VerifyTenantChain = %+v, %v; want the system shard's chain (head 3)", report, err)
	}
	if len(homes.asked) != 0 {
		t.Fatalf("home groups asked for %v, want none", homes.asked)
	}
	if got := routes.route("widgets"); got != AuditRouteSystem {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteSystem)
	}
}

// TestAuditRoute_NoPriorChainRoutesToTheHomeGroup: a barrier tenant with
// no chain on the system shard records the home route and writes there.
func TestAuditRoute_NoPriorChainRoutesToTheHomeGroup(t *testing.T) {
	system := newChainShard(0, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	routes := newMemRouteStore()
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)

	if err := logger.Emit(context.Background(), audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 0 || len(*homeCmds) != 1 {
		t.Fatalf("proposals: system %d, home %d; want the event in the home group", len(*system.cmds), len(*homeCmds))
	}
	if got := routes.route("widgets"); got != AuditRouteHome {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteHome)
	}
}

// TestAuditRoute_RecordedRouteWins: the recorded route is the fact every
// node follows; a node never re-derives it once recorded.
func TestAuditRoute_RecordedRouteWins(t *testing.T) {
	system := newChainShard(0, auditActive())
	homes, homeCmds := homeShard()
	routes := newMemRouteStore()
	if _, err := routes.PutIfAbsent(context.Background(), AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
		t.Fatal(err)
	}
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, routes)
	if err := NewReplicatedLogger(r).Emit(context.Background(), audit.AuditEvent{TenantID: "widgets"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(*system.cmds) != 1 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want the recorded system route", len(*system.cmds), len(*homeCmds))
	}
}

// TestAuditRoute_MixedCapabilityNeverWritesToTheHomeGroup: until every
// replica advertises the audit instance, no event reaches a home group: a
// barrier tenant stays on (and records) the system shard, a volume or raft
// tenant is refused, and a tenant already recorded home is refused rather
// than split onto the system shard.
func TestAuditRoute_MixedCapabilityNeverWritesToTheHomeGroup(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(0, mixedCapability())
	homes, homeCmds := homeShard()
	routes := newMemRouteStore()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)

	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets"}); err != nil {
		t.Fatalf("Emit(widgets): %v", err)
	}
	if got := routes.route("widgets"); got != AuditRouteSystem {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteSystem)
	}
	for _, id := range []string{"vault", "shard1"} {
		var notActive *AuditHomeNotActiveError
		if err := logger.Emit(ctx, audit.AuditEvent{TenantID: id}); !errors.As(err, &notActive) {
			t.Fatalf("Emit(%s) = %v, want *AuditHomeNotActiveError", id, err)
		}
	}

	if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("gadgets"), []byte(AuditRouteHome)); err != nil {
		t.Fatal(err)
	}
	lookup := isolatedTenantsLookup()
	lookup.tenants["gadgets"] = &tenant.TenantInfo{ID: "gadgets", IsolationType: tenant.IsolationBarrier}
	r2 := NewSystemShardAuditResolver(system.host, 1, lookup)
	r2.SetHomeGroupSource(homes, routes)
	var notActive *AuditHomeNotActiveError
	if err := NewReplicatedLogger(r2).Emit(ctx, audit.AuditEvent{TenantID: "gadgets"}); !errors.As(err, &notActive) {
		t.Fatalf("Emit(gadgets) = %v, want *AuditHomeNotActiveError", err)
	}

	if len(*homeCmds) != 0 || len(homes.asked) != 0 {
		t.Fatalf("home group: %d proposals, asked for %v; want none", len(*homeCmds), homes.asked)
	}
	if len(*system.cmds) != 1 {
		t.Fatalf("system shard proposals = %d, want widgets' one event", len(*system.cmds))
	}
}

// TestAuditRoute_ReadsNeverRecordARoute: Query and Verify follow the route
// without recording one, so reading never pins a tenant.
func TestAuditRoute_ReadsNeverRecordARoute(t *testing.T) {
	system := newChainShard(0, mixedCapability())
	homes, _ := homeShard()
	routes := newMemRouteStore()
	r := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	r.SetHomeGroupSource(homes, routes)
	if _, err := NewReplicatedLogger(r).VerifyTenantChain(context.Background(), "widgets"); err != nil {
		t.Fatalf("VerifyTenantChain: %v", err)
	}
	if got := routes.route("widgets"); got != "" {
		t.Fatalf("a read recorded route %q", got)
	}
}

// TestAuditRoute_AuditInstanceOnTheSystemShardIsServedFromTheSystemShard: on
// the log surface, a barrier tenant whose audit chain is recorded on the
// system shard has its audit instance served from the system shard, not the
// home group's empty instance; its other logs apply in the home group.
func TestAuditRoute_AuditInstanceOnTheSystemShardIsServedFromTheSystemShard(t *testing.T) {
	system := newChainShard(3, auditActive())
	homes, _ := homeShard()
	routes := newMemRouteStore()
	audits := NewSystemShardAuditResolver(system.host, 1, isolatedTenantsLookup())
	logs := NewSystemShardLogResolver(system.host, 1, isolatedTenantsLookup(), nil)
	audits.SetHomeGroupSource(homes, routes)
	logs.SetHomeGroupSource(homes, routes)
	if err := NewReplicatedLogger(audits).Emit(context.Background(), audit.AuditEvent{TenantID: "widgets"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if host, shardID, err := logs.ResolveAuditInstance(context.Background(), "widgets"); err != nil || host != system.host || shardID != 1 {
		t.Fatalf("ResolveAuditInstance(widgets) = %v, %d, %v; want the system shard", host, shardID, err)
	}
	if host, _, err := logs.ResolveAuditShard(context.Background(), "widgets"); err != nil || host != homes.host {
		t.Fatalf("ResolveAuditShard(widgets) = %v, %v; want the home group", host, err)
	}
}

// TestAuditRoute_TighteningBeforeSystemClaimDoesNotStrandTheTenant: with the
// audit instance inactive a writer derives the system route, passes the
// strict check and pauses before its claim; a tightening commits strict
// while it is paused. The landed claim is checked again and, carrying no
// chain, released, so once the audit instance activates the tenant's next
// append records the home route and succeeds.
func TestAuditRoute_TighteningBeforeSystemClaimDoesNotStrandTheTenant(t *testing.T) {
	ctx := context.Background()
	on := mixedCapability()
	system := newChainShard(0, on)
	homes, homeCmds := homeShard()
	lookup := barrierTenantLookup("", "")
	r := NewSystemShardAuditResolver(system.host, 1, lookup)
	routes := newMemRouteStore()
	pause := &putPause{reached: make(chan struct{}), release: make(chan struct{})}
	routes.beforePutIfAbsent = pause
	r.SetHomeGroupSource(homes, routes)
	logger := NewReplicatedLogger(r)

	done := make(chan error, 1)
	go func() {
		done <- logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"})
	}()
	<-pause.reached
	// The tightening commits strict while the writer is paused.
	lookup.tenants["widgets"].IsolationPolicy = tenant.TenantIsolationPolicyStrict
	close(pause.release)
	err := <-done

	var sa *tenant.SystemAuditRouteError
	if !errors.As(err, &sa) || sa.TenantID != "widgets" {
		t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
	}
	if got := routes.route("widgets"); got != "" {
		t.Fatalf("recorded route = %q, want no system route left recorded", got)
	}
	if len(*system.cmds) != 0 {
		t.Fatalf("system proposals = %d, want none", len(*system.cmds))
	}

	on[featuregate.VerifiableLogAudit] = true
	if err := logger.Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"}); err != nil {
		t.Fatalf("Emit after activation: %v", err)
	}
	if got := routes.route("widgets"); got != AuditRouteHome {
		t.Fatalf("recorded route = %q, want %q", got, AuditRouteHome)
	}
	if len(*system.cmds) != 0 || len(*homeCmds) != 1 {
		t.Fatalf("proposals: system %d, home %d; want the event in the home group", len(*system.cmds), len(*homeCmds))
	}
}

// TestAuditRoute_SystemRouteWithChainIsKept: a strict tenant's recorded
// system route with a chain already on the system shard is refused but kept
// (S9): its history stays where it is.
func TestAuditRoute_SystemRouteWithChainIsKept(t *testing.T) {
	ctx := context.Background()
	system := newChainShard(3, auditActive())
	homes, homeCmds := homeShard()
	r := NewSystemShardAuditResolver(system.host, 1, barrierTenantLookup(tenant.TenantIsolationPolicyStrict, ""))
	routes := newMemRouteStore()
	if _, err := routes.PutIfAbsent(ctx, AuditRouteKey("widgets"), []byte(AuditRouteSystem)); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	r.SetHomeGroupSource(homes, routes)

	err := NewReplicatedLogger(r).Emit(ctx, audit.AuditEvent{TenantID: "widgets", Action: "delete"})
	var sa *tenant.SystemAuditRouteError
	if !errors.As(err, &sa) || sa.TenantID != "widgets" {
		t.Fatalf("Emit = %v, want *tenant.SystemAuditRouteError for widgets", err)
	}
	if got := routes.route("widgets"); got != AuditRouteSystem {
		t.Fatalf("recorded route = %q, want the chained %q kept", got, AuditRouteSystem)
	}
	if len(*system.cmds) != 0 || len(*homeCmds) != 0 {
		t.Fatalf("proposals: system %d, home %d; want none", len(*system.cmds), len(*homeCmds))
	}
}
