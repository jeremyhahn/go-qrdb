package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tighteningLookup is a tenant lookup that is also the
// tenant.IsolationTighteningReader a create re-reads after persisting a
// database on the shared WAL. afterFirst is the tenant record every read but
// the first returns: a tightening that committed while the create ran.
type tighteningLookup struct {
	info       *tenant.TenantInfo
	afterFirst *tenant.TenantInfo
	marker     bool
	reads      int
}

func (l *tighteningLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	l.reads++
	if l.reads > 1 && l.afterFirst != nil {
		return l.afterFirst, nil
	}
	return l.info, nil
}

func (l *tighteningLookup) IsolationTightening(context.Context, string) (bool, error) {
	return l.marker, nil
}

func looseRaftTenant() *tenant.TenantInfo {
	return &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationRaft}
}

func newTighteningRaceService(t *testing.T, l *tighteningLookup) DatabaseServicer {
	t.Helper()
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, l.info, f)
	svc.(*DatabaseService).tenantInfo = l
	return svc
}

// TestCreateForTenant_SharedWALRolledBackUnderATighteningMarker: a create on
// the shared WAL that, after its catalog write, finds a tightening to strict
// in flight rolls itself back, so the tightening -- which may have listed the
// databases before that write -- never commits over a node-keyed log.
func TestCreateForTenant_SharedWALRolledBackUnderATighteningMarker(t *testing.T) {
	l := &tighteningLookup{info: looseRaftTenant(), marker: true}
	svc := newTighteningRaceService(t, l)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) || refused.Name != "app" || refused.Tenant != "rt" {
		t.Fatalf("CreateForTenant under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
	if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
		t.Fatalf("refused create left %+v in the catalog", got)
	}
}

// TestCreateForTenant_SharedWALRolledBackWhenStrictCommittedMeanwhile: a
// tightening that committed strict between the create's first read and its
// catalog write is seen by the re-read, and the create rolls back.
func TestCreateForTenant_SharedWALRolledBackWhenStrictCommittedMeanwhile(t *testing.T) {
	strict := looseRaftTenant()
	strict.IsolationPolicy = tenant.TenantIsolationPolicyStrict
	l := &tighteningLookup{info: looseRaftTenant(), afterFirst: strict}
	svc := newTighteningRaceService(t, l)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant after strict committed = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
	if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
		t.Fatalf("refused create left %+v in the catalog", got)
	}
}

// TestCreateForTenant_SharedWALKeptWithNoTightening: with no tightening in
// flight the create stands, and the probe a tightening consults lists it.
func TestCreateForTenant_SharedWALKeptWithNoTightening(t *testing.T) {
	l := &tighteningLookup{info: looseRaftTenant()}
	svc := newTighteningRaceService(t, l)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("app")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	dbs, err := NewNodeKeyLogProbe(svc.(*DatabaseService).manager).NodeKeyLogDatabases(context.Background(), "rt")
	if err != nil || len(dbs) != 1 || dbs[0] != "app" {
		t.Fatalf("NodeKeyLogDatabases = (%v, %v), want [app]", dbs, err)
	}
}

// windowLookup models a tightening that commits strict and removes its
// marker just before the create reads the markers: the marker read finds
// none, and every tenant read after it returns the committed strict policy.
type windowLookup struct {
	info      *tenant.TenantInfo
	committed bool
}

func (l *windowLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	if l.committed {
		strict := *l.info
		strict.IsolationPolicy = tenant.TenantIsolationPolicyStrict
		return &strict, nil
	}
	return l.info, nil
}

func (l *windowLookup) IsolationTightening(context.Context, string) (bool, error) {
	l.committed = true
	return false, nil
}

// TestCreateForTenant_SharedWALRolledBackWhenTheMarkerIsRemovedAfterCommit is
// Codex round 3 on #1576: the create reads the markers before a fresh policy
// read, so a tightening that committed strict and removed its marker between
// the create's catalog write and its marker read is still seen. Read the
// other way round, the create saw neither and kept a node-keyed log. Both a
// raft tenant and a barrier tenant on the shared store are governed.
func TestCreateForTenant_SharedWALRolledBackWhenTheMarkerIsRemovedAfterCommit(t *testing.T) {
	for _, info := range []*tenant.TenantInfo{
		looseRaftTenant(),
		{ID: "rt", IsolationType: tenant.IsolationBarrier},
	} {
		t.Run(string(info.IsolationType), func(t *testing.T) {
			l := &windowLookup{info: info}
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
			svc := newReplicaDatabaseService(t, info, f)
			svc.(*DatabaseService).tenantInfo = l
			ctx := tenant.WithTenantID(context.Background(), "rt")

			cfg := testDBConfig("app")
			cfg.Backend = database.BackendRaft
			_, err := svc.CreateForTenant(ctx, cfg)
			var refused *database.StrictTenantDedicatedWALRequiredError
			if !errors.As(err, &refused) || refused.Name != "app" || refused.Tenant != "rt" {
				t.Fatalf("CreateForTenant across a tightening's commit = %v, want *StrictTenantDedicatedWALRequiredError", err)
			}
			if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
				t.Fatalf("refused create left %+v in the catalog", got)
			}
		})
	}
}

// TestCreateForTenant_BarrierSharedWALRolledBackUnderATighteningMarker: a
// barrier tenant's raft database on the shared store meets the same
// handshake as a raft tenant's.
func TestCreateForTenant_BarrierSharedWALRolledBackUnderATighteningMarker(t *testing.T) {
	info := &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationBarrier}
	l := &tighteningLookup{info: info, marker: true}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, info, f)
	svc.(*DatabaseService).tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.Backend = database.BackendRaft
	_, err := svc.CreateForTenant(ctx, cfg)
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant(barrier, shared store) under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
}

// publishedProbeLookup is a tighteningLookup that, when the create reads the
// tightening markers, records whether the database is already published --
// resolvable, and so writable, through the manager.
type publishedProbeLookup struct {
	tighteningLookup
	mgr       *database.Manager
	published bool
	probed    bool
}

func (l *publishedProbeLookup) IsolationTightening(ctx context.Context, tenantID string) (bool, error) {
	l.probed = true
	if db, err := l.mgr.GetOwned("app", tenantID); err == nil && db != nil {
		l.published = true
	}
	return l.tighteningLookup.IsolationTightening(ctx, tenantID)
}

// TestCreateForTenant_StrictCheckRunsBeforeTheDatabaseIsWritable is Codex
// round 2 on #1576: the strict-isolation handshake runs after the catalog
// write but before the database is opened, unfenced or published, so a
// create refused under a tightening was never writable -- no concurrent
// request could land data on the node-keyed log before the refusal.
func TestCreateForTenant_StrictCheckRunsBeforeTheDatabaseIsWritable(t *testing.T) {
	for _, info := range []*tenant.TenantInfo{
		looseRaftTenant(),
		{ID: "rt", IsolationType: tenant.IsolationBarrier},
	} {
		t.Run(string(info.IsolationType), func(t *testing.T) {
			l := &publishedProbeLookup{tighteningLookup: tighteningLookup{info: info, marker: true}}
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
			svc := newReplicaDatabaseService(t, info, f)
			svc.(*DatabaseService).tenantInfo = l
			l.mgr = svc.(*DatabaseService).manager
			ctx := tenant.WithTenantID(context.Background(), "rt")

			cfg := testDBConfig("app")
			cfg.Backend = database.BackendRaft
			_, err := svc.CreateForTenant(ctx, cfg)
			var refused *database.StrictTenantDedicatedWALRequiredError
			if !errors.As(err, &refused) {
				t.Fatalf("CreateForTenant under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
			}
			if !l.probed {
				t.Fatal("the create never read the tightening markers")
			}
			if l.published {
				t.Fatal("the database was published, and so writable, before the strict-isolation check refused it")
			}
			if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
				t.Fatalf("refused create left %+v in the catalog", got)
			}
		})
	}
}
