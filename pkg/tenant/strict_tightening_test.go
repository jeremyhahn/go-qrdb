// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// markerSeeingProbe lists dbs for every tenant and records whether the
// tightening's marker was visible to a create while the probe ran.
type markerSeeingProbe struct {
	svc        IsolationTighteningReader
	dbs        []string
	sawMarker  map[string]bool
	probedWith []string
}

func (p *markerSeeingProbe) NodeKeyLogDatabases(ctx context.Context, tenantID string) ([]string, error) {
	seen, err := p.svc.IsolationTightening(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	p.sawMarker[tenantID] = seen
	p.probedWith = append(p.probedWith, tenantID)
	return p.dbs, nil
}

func newRaftTenantForTightening(t *testing.T, id string) *TenantService {
	t.Helper()
	svc, _ := newPlatformService(t, &bookDouble{}, nil)
	if _, err := svc.CreateTenant(context.Background(), id, id, IsolationRaft); err != nil {
		t.Fatalf("CreateTenant(%s): %v", id, err)
	}
	return svc
}

func TestTightenToStrict_RefusedWhileDatabasesAreOnTheNodeKeyedWAL(t *testing.T) {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "acme-admin", TenantID: "acme"})
	svc := newRaftTenantForTightening(t, "acme")
	probe := &markerSeeingProbe{svc: svc, dbs: []string{"orders"}, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)
	svc.SetAuditRouteProbe(noSystemAuditRoute{})

	_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "acme" || len(nk.Databases) != 1 || nk.Databases[0] != "orders" {
		t.Fatalf("UpdateTenant(strict) = %v, want *NodeKeyLogsError naming orders", err)
	}
	if !probe.sawMarker["acme"] {
		t.Fatal("a create racing the tightening could not see its marker while the databases were listed")
	}
	if info, _ := svc.GetTenant(ctx, "acme"); info == nil || info.IsolationPolicy == TenantIsolationPolicyStrict {
		t.Fatalf("refused tightening committed the policy: %+v", info)
	}
	if on, err := svc.IsolationTightening(ctx, "acme"); err != nil || on {
		t.Fatalf("IsolationTightening after the refusal = (%v, %v), want the marker removed", on, err)
	}
}

// A platform operator tightening another tenant (tenants:update:cross) holds
// no access to that tenant's catalog: its refusal carries the count only, as
// the cluster-floor refusal does. So does a caller with no identity.
func TestTightenToStrict_CrossTenantRefusalDisclosesNoDatabaseNames(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"system operator": auth.WithIdentity(context.Background(), &auth.Identity{
			Subject: "platform-admin", TenantID: tenantid.System, ScopePermissions: []string{"tenants:update:cross"},
		}),
		"other tenant": auth.WithIdentity(context.Background(), &auth.Identity{Subject: "g", TenantID: "globex"}),
		"no identity":  context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			svc := newRaftTenantForTightening(t, "acme")
			svc.SetNodeKeyLogProbe(&fixedProbe{dbs: map[string][]string{"acme": {"orders", "ledger"}}})
			svc.SetAuditRouteProbe(noSystemAuditRoute{})
			_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
			var nk *NodeKeyLogsError
			if !errors.As(err, &nk) || nk.TenantID != "acme" {
				t.Fatalf("UpdateTenant(strict) = %v, want *NodeKeyLogsError", err)
			}
			if len(nk.Databases) != 0 || nk.Count != 2 {
				t.Fatalf("cross-tenant refusal = databases %v count %d, want no names and count 2", nk.Databases, nk.Count)
			}
			for _, db := range []string{"orders", "ledger"} {
				if strings.Contains(err.Error(), db) {
					t.Fatalf("refusal text %q names database %s", err.Error(), db)
				}
			}
		})
	}
}

func TestTightenToStrict_CommitsWhenNoDatabaseIsOnTheNodeKeyedWAL(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	probe := &markerSeeingProbe{svc: svc, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)
	svc.SetAuditRouteProbe(noSystemAuditRoute{})

	if _, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}); err != nil {
		t.Fatalf("UpdateTenant(strict): %v", err)
	}
	if info, _ := svc.GetTenant(ctx, "acme"); info == nil || info.IsolationPolicy != TenantIsolationPolicyStrict {
		t.Fatalf("tenant after the tightening = %+v, want strict", info)
	}
	if on, err := svc.IsolationTightening(ctx, "acme"); err != nil || on {
		t.Fatalf("IsolationTightening after the commit = (%v, %v), want the marker removed", on, err)
	}
}

func TestTightenToStrict_NoProbeFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	if !errors.Is(err, ErrNodeKeyLogsUnverifiable) {
		t.Fatalf("UpdateTenant(strict) with no probe = %v, want ErrNodeKeyLogsUnverifiable", err)
	}
}

// noSystemAuditRoute is an AuditRouteProbe reporting every tenant's audit
// chain on its home group.
type noSystemAuditRoute struct{}

func (noSystemAuditRoute) SystemAuditRoute(context.Context, string) (bool, error) { return false, nil }

// systemAuditRoute is an AuditRouteProbe reporting the named tenants' audit
// chains on the system shard.
type systemAuditRoute map[string]bool

func (s systemAuditRoute) SystemAuditRoute(_ context.Context, id string) (bool, error) {
	return s[id], nil
}

// TestTightenToStrict_NoAuditRouteProbeFailsClosed: with the node-key probe
// wired but no audit-route probe, a tightening fails closed.
func TestTightenToStrict_NoAuditRouteProbeFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	svc.SetNodeKeyLogProbe(&fixedProbe{})
	_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	if !errors.Is(err, ErrAuditRouteUnverifiable) {
		t.Fatalf("UpdateTenant(strict) with no audit-route probe = %v, want ErrAuditRouteUnverifiable", err)
	}
}

// TestTightenToStrict_RefusedWhileTheAuditChainIsOnTheSystemShard: a tenant
// with no node-keyed databases whose audit chain is on the system shard (under
// the node key) is refused strict, on the tenant's own policy update and on
// the cluster floor, and stays non-strict.
func TestTightenToStrict_RefusedWhileTheAuditChainIsOnTheSystemShard(t *testing.T) {
	ctx := context.Background()
	for name, tighten := range map[string]func(*TenantService) error{
		"tenant policy": func(svc *TenantService) error {
			_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
			return err
		},
		"cluster floor": func(svc *TenantService) error {
			return svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict)
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newPlatformService(t, &bookDouble{}, nil)
			if _, err := svc.CreateTenant(ctx, "acme", "acme", IsolationBarrier); err != nil {
				t.Fatalf("CreateTenant(acme): %v", err)
			}
			svc.SetNodeKeyLogProbe(&fixedProbe{})
			svc.SetAuditRouteProbe(systemAuditRoute{"acme": true})
			err := tighten(svc)
			var sa *SystemAuditRouteError
			if !errors.As(err, &sa) || sa.TenantID != "acme" {
				t.Fatalf("tightening = %v, want *SystemAuditRouteError for acme", err)
			}
			info, err := svc.GetTenant(ctx, "acme")
			if err != nil || info.IsolationPolicy == TenantIsolationPolicyStrict {
				t.Fatalf("tenant after the refusal = (%+v, %v); want not strict", info, err)
			}
			if floor, err := svc.ClusterIsolationPolicy(ctx); err != nil || floor == ClusterIsolationPolicyStrict {
				t.Fatalf("floor after the refusal = (%v, %v); want not strict", floor, err)
			}
		})
	}
}

func TestTightenFloorToStrict_RefusedWhileARaftTenantHasNodeKeyedLogs(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	probe := &markerSeeingProbe{svc: svc, dbs: []string{"orders"}, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)
	svc.SetAuditRouteProbe(noSystemAuditRoute{})

	err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict)
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "acme" {
		t.Fatalf("SetClusterIsolationPolicy(strict) = %v, want *NodeKeyLogsError for acme", err)
	}
	if !probe.sawMarker["acme"] {
		t.Fatal("a create racing the floor's tightening could not see its marker")
	}
	if got, _ := svc.ClusterIsolationPolicy(ctx); got == ClusterIsolationPolicyStrict {
		t.Fatal("refused floor tightening committed strict")
	}

	probe.dbs = nil
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy(strict) once the logs are gone: %v", err)
	}
	if on, err := svc.IsolationTightening(ctx, "acme"); err != nil || on {
		t.Fatalf("IsolationTightening after the floor commit = (%v, %v), want the marker removed", on, err)
	}
}

// TestTightenFloorToStrict_RefusalDisclosesNoDatabaseNames pins that the
// cluster-floor refusal, returned to a platform caller with no cross-tenant
// database-read right, carries the tenant ID and a count, never the tenant's
// database names.
func TestTightenFloorToStrict_RefusalDisclosesNoDatabaseNames(t *testing.T) {
	ctx := context.Background()
	svc := newRaftTenantForTightening(t, "acme")
	svc.SetNodeKeyLogProbe(&markerSeeingProbe{svc: svc, dbs: []string{"orders", "ledger"}, sawMarker: map[string]bool{}})
	svc.SetAuditRouteProbe(noSystemAuditRoute{})

	err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict)
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "acme" {
		t.Fatalf("SetClusterIsolationPolicy(strict) = %v, want *NodeKeyLogsError for acme", err)
	}
	if len(nk.Databases) != 0 || nk.Count != 2 {
		t.Fatalf("floor refusal = databases %v count %d, want no names and count 2", nk.Databases, nk.Count)
	}
	for _, name := range []string{"orders", "ledger"} {
		if strings.Contains(err.Error(), name) {
			t.Fatalf("floor refusal %q discloses database name %q", err.Error(), name)
		}
	}
}

// memMarkerStore is an in-memory markerStore: List returns the full keys
// under a prefix, Get of an absent key is (nil, nil), and Delete fails with
// deleteErr when armed.
type memMarkerStore struct {
	mu        sync.Mutex
	kv        map[string][]byte
	deleteErr error
}

func newMemMarkerStore() *memMarkerStore { return &memMarkerStore{kv: map[string][]byte{}} }

func (s *memMarkerStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[key] = append([]byte(nil), value...)
	return nil
}

func (s *memMarkerStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kv[key], nil
}

func (s *memMarkerStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.kv, key)
	return nil
}

func (s *memMarkerStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.kv {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// fixedProbe lists dbs for every tenant and records the tenants probed.
type fixedProbe struct {
	dbs    map[string][]string
	probed []string
	during func()
}

func (p *fixedProbe) NodeKeyLogDatabases(_ context.Context, tenantID string) ([]string, error) {
	p.probed = append(p.probed, tenantID)
	if p.during != nil {
		p.during()
	}
	return p.dbs[tenantID], nil
}

// setTighteningClock pins the marker-lease clock for the test.
func setTighteningClock(t *testing.T, now *time.Time) {
	t.Helper()
	prev := tighteningNow
	tighteningNow = func() time.Time { return *now }
	t.Cleanup(func() { tighteningNow = prev })
}

// TestTightenToStrict_RefusedWhileABarrierTenantHasNodeKeyedLogs: a barrier
// tenant served from the shared system store can have raft databases on the
// node-keyed WAL, so its own tightening and the floor's are refused while
// they exist.
func TestTightenToStrict_RefusedWhileABarrierTenantHasNodeKeyedLogs(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(ctx, "secure", "Secure", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	probe := &markerSeeingProbe{svc: svc, dbs: []string{"orders"}, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)
	svc.SetAuditRouteProbe(noSystemAuditRoute{})

	_, err := svc.UpdateTenant(ctx, "secure", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "secure" {
		t.Fatalf("UpdateTenant(strict) of a barrier tenant with shared-WAL databases = %v, want *NodeKeyLogsError", err)
	}
	if !probe.sawMarker["secure"] {
		t.Fatal("a create racing the barrier tenant's tightening could not see its marker")
	}
	err = svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict)
	if !errors.As(err, &nk) || nk.TenantID != "secure" {
		t.Fatalf("SetClusterIsolationPolicy(strict) over a barrier tenant with shared-WAL databases = %v, want *NodeKeyLogsError", err)
	}
	if got, _ := svc.ClusterIsolationPolicy(ctx); got == ClusterIsolationPolicyStrict {
		t.Fatal("refused floor tightening committed strict")
	}
}

// TestTightenToStrict_GuardJudgesTheRecordTheSwapCommits pins G1 under the
// interleaving where a strict update reads an exempt volume record, the tenant
// is then changed to barrier and gains a shared-WAL database, and the update
// resumes: the guard must judge (and fence) the record the swap commits over,
// not the stale one it was handed.
func TestTightenToStrict_GuardJudgesTheRecordTheSwapCommits(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(ctx, "secure", "Secure", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	probe := &markerSeeingProbe{svc: svc, dbs: []string{"orders"}, sawMarker: map[string]bool{}}
	svc.SetNodeKeyLogProbe(probe)
	svc.SetAuditRouteProbe(noSystemAuditRoute{})

	// The update's initial read saw the tenant while it was still volume.
	stale := &TenantInfo{ID: "secure", IsolationType: IsolationVolume}
	patch := TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}
	swapper, _ := svc.store.(tenantRecordSwapper)
	err := svc.guardedUpdate(ctx, svc.store, stale, patch, func(ctx context.Context) error {
		_, uerr := updateTenantRecord(ctx, svc, swapper, "secure", patch)
		return uerr
	})
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "secure" {
		t.Fatalf("strict update over a now-barrier tenant with shared-WAL databases = %v, want *NodeKeyLogsError", err)
	}
	if !probe.sawMarker["secure"] {
		t.Fatal("the recheck tightened without its marker standing")
	}
	got, err := svc.GetTenant(ctx, "secure")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.IsolationPolicy == TenantIsolationPolicyStrict {
		t.Fatal("strict committed over a barrier tenant with node-keyed data")
	}
}

// TestGuardedFloor_MarksBeforeListingTenants: the cluster marker is in place
// before the floor lists tenants -- even when none exist -- so a tenant
// created while the list is taken has its creates refused.
func TestGuardedFloor_MarksBeforeListingTenants(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		tenants []TenantInfo
	}{
		{name: "no tenants", tenants: nil},
		{name: "one tenant", tenants: []TenantInfo{{ID: "old", IsolationType: IsolationRaft}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var g isolationChangeGuard
			g.SetNodeKeyLogProbe(&fixedProbe{})
			g.SetAuditRouteProbe(noSystemAuditRoute{})
			store := newMemMarkerStore()
			var sawMarker, committed bool
			list := func(ctx context.Context) ([]TenantInfo, error) {
				on, err := isolationTightening(ctx, store, "newcomer")
				if err != nil {
					return nil, err
				}
				sawMarker = on
				return tc.tenants, nil
			}
			if err := g.guardedFloor(ctx, store, ClusterIsolationPolicyStrict, list, func(context.Context) error {
				committed = true
				return nil
			}); err != nil {
				t.Fatalf("guardedFloor: %v", err)
			}
			if !sawMarker {
				t.Fatal("a tenant created while the floor listed tenants could not see the cluster marker")
			}
			if !committed {
				t.Fatal("guardedFloor did not commit")
			}
			if on, err := isolationTightening(ctx, store, "newcomer"); err != nil || on {
				t.Fatalf("IsolationTightening after the floor commit = (%v, %v), want the marker removed", on, err)
			}
		})
	}
}

// TestGuardedFloor_ProbesATenantCreatedDuringTheList: a tenant the list
// returns because it was created after the marker is probed.
func TestGuardedFloor_ProbesATenantCreatedDuringTheList(t *testing.T) {
	var g isolationChangeGuard
	probe := &fixedProbe{dbs: map[string][]string{"newcomer": {"orders"}}}
	g.SetNodeKeyLogProbe(probe)
	g.SetAuditRouteProbe(noSystemAuditRoute{})
	list := func(context.Context) ([]TenantInfo, error) {
		return []TenantInfo{{ID: "newcomer", IsolationType: IsolationBarrier}}, nil
	}
	err := g.guardedFloor(context.Background(), newMemMarkerStore(), ClusterIsolationPolicyStrict, list, func(context.Context) error {
		t.Fatal("floor committed over a tenant with shared-WAL databases")
		return nil
	})
	var nk *NodeKeyLogsError
	if !errors.As(err, &nk) || nk.TenantID != "newcomer" {
		t.Fatalf("guardedFloor = %v, want *NodeKeyLogsError for newcomer", err)
	}
}

// TestIsolationTightening_StaleMarkersExpire: a marker left by a crash (past
// its lease), by an earlier format, or by a failed removal no longer governs
// creates once its lease is past, and the reader removes it.
func TestIsolationTightening_StaleMarkersExpire(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	setTighteningClock(t, &now)

	t.Run("crash leaves a marker past its lease", func(t *testing.T) {
		store := newMemMarkerStore()
		key := clusterTighteningPrefix + "deadbeef"
		_ = store.Put(ctx, key, markerValue(now.Add(-time.Second)))
		if on, err := isolationTightening(ctx, store, "acme"); err != nil || on {
			t.Fatalf("IsolationTightening over an expired marker = (%v, %v), want false", on, err)
		}
		if keys, _ := store.List(ctx, tighteningPrefix); len(keys) != 0 {
			t.Fatalf("expired marker not reconciled: %v", keys)
		}
	})
	t.Run("marker of an earlier format", func(t *testing.T) {
		store := newMemMarkerStore()
		_ = store.Put(ctx, tighteningPrefix+"acme/deadbeef", []byte{1})
		if on, err := isolationTightening(ctx, store, "acme"); err != nil || on {
			t.Fatalf("IsolationTightening over a legacy marker = (%v, %v), want false", on, err)
		}
	})
	t.Run("live marker governs", func(t *testing.T) {
		store := newMemMarkerStore()
		_ = store.Put(ctx, tighteningPrefix+"acme/deadbeef", markerValue(now.Add(time.Minute)))
		if on, err := isolationTightening(ctx, store, "acme"); err != nil || !on {
			t.Fatalf("IsolationTightening over a live marker = (%v, %v), want true", on, err)
		}
		if on, _ := isolationTightening(ctx, store, "other"); on {
			t.Fatal("a tenant's own marker governed another tenant")
		}
	})
	t.Run("failed removal after a refusal", func(t *testing.T) {
		store := newMemMarkerStore()
		store.deleteErr = errors.New("delete boom")
		var g isolationChangeGuard
		g.SetNodeKeyLogProbe(&fixedProbe{dbs: map[string][]string{"acme": {"orders"}}})
		g.SetAuditRouteProbe(noSystemAuditRoute{})
		err := g.guardedUpdate(ctx, store, &TenantInfo{ID: "acme", IsolationType: IsolationRaft},
			TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}, func(context.Context) error { return nil })
		var nk *NodeKeyLogsError
		if !errors.As(err, &nk) {
			t.Fatalf("guardedUpdate = %v, want *NodeKeyLogsError", err)
		}
		if on, _ := isolationTightening(ctx, store, "acme"); !on {
			t.Fatal("the marker a failed removal left is not live within its lease")
		}
		now = now.Add(tighteningLease + time.Second)
		store.deleteErr = nil
		if on, err := isolationTightening(ctx, store, "acme"); err != nil || on {
			t.Fatalf("IsolationTightening past the lease = (%v, %v), want false", on, err)
		}
		if keys, _ := store.List(ctx, tighteningPrefix); len(keys) != 0 {
			t.Fatalf("stale marker not reconciled: %v", keys)
		}
	})
}

// TestTightenToStrict_RefusesACommitPastItsLease: a tightening whose checks
// outlast its marker's lease does not commit, since a create may already
// have ignored the expired marker.
func TestTightenToStrict_RefusesACommitPastItsLease(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	setTighteningClock(t, &now)
	var g isolationChangeGuard
	g.SetNodeKeyLogProbe(&fixedProbe{during: func() { now = now.Add(tighteningLease) }})
	g.SetAuditRouteProbe(noSystemAuditRoute{})
	err := g.guardedUpdate(context.Background(), newMemMarkerStore(), &TenantInfo{ID: "acme", IsolationType: IsolationRaft},
		TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}, func(context.Context) error {
			t.Fatal("tightening committed past its lease")
			return nil
		})
	if !errors.Is(err, ErrTighteningLeaseExpired) {
		t.Fatalf("guardedUpdate past the lease = %v, want ErrTighteningLeaseExpired", err)
	}
}

// TestTightenToStrict_WritesIsolationEpochBeforeProbe: a tightening to
// strict writes a fresh value to the epoch key of its scope before its
// node-key log probe runs, so a shared-store write guarded on the epoch it
// read before then is refused at apply time.
func TestTightenToStrict_WritesIsolationEpochBeforeProbe(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, prefix, key string
	}{
		{"tenant", tighteningPrefix + "acme/", isolationEpochPrefix + "acme"},
		{"floor", clusterTighteningPrefix, clusterEpochKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemMarkerStore()
			before := []byte("earlier-epoch")
			_ = store.Put(ctx, tc.key, before)
			var seen []byte
			probe := &fixedProbe{}
			probe.during = func() {
				store.mu.Lock()
				seen = append([]byte(nil), store.kv[tc.key]...)
				store.mu.Unlock()
			}
			var g isolationChangeGuard
			g.SetNodeKeyLogProbe(probe)
			g.SetAuditRouteProbe(noSystemAuditRoute{})
			committed := false
			err := g.tightenToStrict(ctx, store, tc.prefix, true,
				func(context.Context) ([]string, error) { return []string{"acme"}, nil },
				func(context.Context) error { committed = true; return nil })
			if err != nil || !committed {
				t.Fatalf("tightenToStrict = %v (committed %v), want a commit", err, committed)
			}
			if len(seen) == 0 || string(seen) == string(before) {
				t.Fatalf("the probe read epoch %q, want a fresh one (before %q)", seen, before)
			}
			after, err := store.Get(ctx, tc.key)
			if err != nil || string(after) != string(seen) {
				t.Fatalf("epoch after the tightening = (%q, %v), want %q kept", after, err, seen)
			}
			if !slices.Contains(IsolationEpochKeys("acme"), tc.key) {
				t.Fatalf("IsolationEpochKeys(acme) = %v, missing %q", IsolationEpochKeys("acme"), tc.key)
			}
		})
	}
}

// TestTightenToStrict_NewEpochImpliesLiveMarker: the shared-store write
// guard refuses on a live marker (ErrIsolationTighteningInFlight) after it
// reads the epochs. That closes the race only if every point at which the
// new epoch is readable but the strict policy is not committed has the
// marker up: paused after its probe, inside the commit, the new epoch is
// readable and isolationTightening reports the tightening; after it returns
// the marker is gone.
func TestTightenToStrict_NewEpochImpliesLiveMarker(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, prefix, key string
	}{
		{"tenant", tighteningPrefix + "acme/", isolationEpochPrefix + "acme"},
		{"floor", clusterTighteningPrefix, clusterEpochKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemMarkerStore()
			before := []byte("earlier-epoch")
			_ = store.Put(ctx, tc.key, before)
			var g isolationChangeGuard
			g.SetNodeKeyLogProbe(&fixedProbe{})
			g.SetAuditRouteProbe(noSystemAuditRoute{})
			paused := false
			err := g.tightenToStrict(ctx, store, tc.prefix, true,
				func(context.Context) ([]string, error) { return []string{"acme"}, nil },
				func(context.Context) error {
					paused = true
					epochs, err := isolationEpochs(ctx, store, "acme")
					if err != nil || string(epochs[tc.key]) == string(before) {
						t.Fatalf("paused tightening: epochs = (%q, %v), want a fresh %s", epochs, err, tc.key)
					}
					live, err := isolationTightening(ctx, store, "acme")
					if err != nil || !live {
						t.Fatalf("paused tightening with the new epoch: isolationTightening = (%v, %v), want true", live, err)
					}
					return nil
				})
			if err != nil || !paused {
				t.Fatalf("tightenToStrict = %v (paused %v), want a commit", err, paused)
			}
			if live, err := isolationTightening(ctx, store, "acme"); err != nil || live {
				t.Fatalf("after the tightening: isolationTightening = (%v, %v), want false", live, err)
			}
		})
	}
}

// TestIsolationEpochs_AbsentKeysAreOmitted: a key no tightening wrote is
// absent from the map IsolationEpochs returns; a written one is present.
func TestIsolationEpochs_AbsentKeysAreOmitted(t *testing.T) {
	ctx := context.Background()
	store := newMemMarkerStore()
	got, err := isolationEpochs(ctx, store, "acme")
	if err != nil || len(got) != 0 {
		t.Fatalf("isolationEpochs over an empty store = (%v, %v), want an empty map", got, err)
	}
	_ = store.Put(ctx, clusterEpochKey, []byte("floor"))
	got, err = isolationEpochs(ctx, store, "acme")
	if err != nil || len(got) != 1 || string(got[clusterEpochKey]) != "floor" {
		t.Fatalf("isolationEpochs = (%v, %v), want only the floor epoch", got, err)
	}
	if _, ok := got[isolationEpochPrefix+"acme"]; ok {
		t.Fatal("an unwritten tenant epoch is present in the map")
	}
}

// TestTightenToStrict_PolicyWriteFencedOnItsMarker: the policy write is one
// batch guarded on the tightening's marker, so a create that reaps the
// expired marker after the lease check and before the write (the window a
// separate deadline check leaves) makes the write refuse rather than land
// after the create's policy read.
func TestTightenToStrict_PolicyWriteFencedOnItsMarker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	setTighteningClock(t, &now)
	var g isolationChangeGuard
	g.SetNodeKeyLogProbe(&fixedProbe{})
	g.SetAuditRouteProbe(noSystemAuditRoute{})
	store := newMemKVStore()
	const policyKey = "policy/acme"
	store.data[policyKey] = []byte("inherit")
	commit := func(ctx context.Context) error {
		// The commit is delayed past the lease; a create sees the marker
		// expired and reaps it before its policy read.
		now = now.Add(tighteningLease)
		reaped, err := isolationTightening(context.Background(), store, "acme")
		if err != nil || reaped {
			t.Fatalf("create's marker read = (%v, %v), want the expired marker reaped", reaped, err)
		}
		handled, swapped, err := fencedSwap(ctx, store, policyKey, []byte("inherit"), []byte("strict"))
		if !handled || swapped {
			t.Fatalf("fencedSwap = (handled %v, swapped %v), want a handled refusal", handled, swapped)
		}
		return err
	}
	err := g.guardedUpdate(context.Background(), store, &TenantInfo{ID: "acme", IsolationType: IsolationRaft},
		TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}, commit)
	if !errors.Is(err, ErrTighteningLeaseExpired) {
		t.Fatalf("fenced write after the marker was reaped = %v, want ErrTighteningLeaseExpired", err)
	}
	if got := string(store.data[policyKey]); got != "inherit" {
		t.Fatalf("policy = %q after a refused fenced write, want inherit", got)
	}

	// With the marker standing the same write lands.
	now = time.Unix(1_700_000_000, 0)
	err = g.guardedUpdate(context.Background(), store, &TenantInfo{ID: "acme", IsolationType: IsolationRaft},
		TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict}, func(ctx context.Context) error {
			_, swapped, err := fencedSwap(ctx, store, policyKey, []byte("inherit"), []byte("strict"))
			if err == nil && !swapped {
				t.Fatal("fenced write under a standing marker did not land")
			}
			return err
		})
	if err != nil {
		t.Fatalf("fenced write under a standing marker = %v", err)
	}
	if got := string(store.data[policyKey]); got != "strict" {
		t.Fatalf("policy = %q, want strict", got)
	}
}
