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
