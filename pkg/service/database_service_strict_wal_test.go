package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// strictRaftTenant is a raft-isolated tenant whose own isolation policy is
// strict (docs/multi-tenancy/engineering.md phase 5, G1).
func strictRaftTenant() *tenant.TenantInfo {
	return &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationRaft, IsolationPolicy: tenant.TenantIsolationPolicyStrict}
}

// TestCreateForTenant_StrictTenant_SharedWALRefused: a strict tenant's raft
// database requested without a dedicated WAL would put its log on the shared
// WAL under a node key; the create is refused with a typed error before any
// quota is reserved.
func TestCreateForTenant_StrictTenant_SharedWALRefused(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant err = %v, want *database.StrictTenantDedicatedWALRequiredError", err)
	}
	if refused.Name != "app" || refused.Tenant != "rt" {
		t.Errorf("refusal = %+v, want Name app, Tenant rt", refused)
	}
	if f.incWAL != 0 {
		t.Errorf("dedicated-WAL increment = %d, want 0", f.incWAL)
	}
}

// TestCreateForTenant_StrictTenant_DedicatedWALCreated: with the dedicated WAL
// requested, the strict tenant's raft database is created on it.
func TestCreateForTenant_StrictTenant_DedicatedWALCreated(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if !got.DedicatedWAL {
		t.Errorf("persisted DedicatedWAL = false, want true")
	}
	if f.incWAL != 1 {
		t.Errorf("dedicated-WAL increment = %d, want 1", f.incWAL)
	}
}

// TestCreateDatabase_StrictTenantRefusedUntilStrictIsolationActive: until
// the cluster activates strict-isolation (a mixed-version cluster), a strict
// tenant's database create is refused with the gate's *feature.NotActiveError
// and nothing is persisted or reserved; once the gate passes, today's
// behaviour (the dedicated-WAL create) holds.
func TestCreateDatabase_StrictTenantRefusedUntilStrictIsolationActive(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "rt")
	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true

	t.Run("inactive", func(t *testing.T) {
		f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
		svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
		WithStrictIsolationGate(func() error {
			return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
		})(svc.(*DatabaseService))

		_, err := svc.CreateForTenant(ctx, cfg)
		var notActive *feature.NotActiveError
		if !errors.As(err, &notActive) || notActive.Feature != featuregate.StrictIsolation {
			t.Fatalf("CreateForTenant before activation = %v, want NotActiveError(%s)", err, featuregate.StrictIsolation)
		}
		if f.incWAL != 0 {
			t.Errorf("dedicated-WAL increment = %d, want 0", f.incWAL)
		}
		if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
			t.Errorf("refused create persisted %+v", got)
		}
	})

	t.Run("active", func(t *testing.T) {
		f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
		svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
		WithStrictIsolationGate(func() error { return nil })(svc.(*DatabaseService))

		got, err := svc.CreateForTenant(ctx, cfg)
		if err != nil {
			t.Fatalf("CreateForTenant after activation: %v", err)
		}
		if !got.DedicatedWAL || f.incWAL != 1 {
			t.Errorf("created DedicatedWAL=%v incWAL=%d, want true, 1", got.DedicatedWAL, f.incWAL)
		}
	})
}

// TestCreateForTenant_StrictTenant_QuotaStillApplies: the strict policy does
// not bypass the MaxDedicatedWALGroups quota.
func TestCreateForTenant_StrictTenant_QuotaStillApplies(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 1, CurrentDedicatedWALGroups: 1}}
	svc := newReplicaDatabaseService(t, strictRaftTenant(), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true
	_, err := svc.CreateForTenant(ctx, cfg)
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) || exceeded.Resource != quota.ResourceDedicatedWALGroups {
		t.Fatalf("CreateForTenant err = %v, want the dedicated-WAL quota refusal", err)
	}
}

// fixedDatabaseFloor is a tenant.ClusterIsolationPolicyReader returning a
// fixed cluster floor.
type fixedDatabaseFloor struct{ policy tenant.ClusterIsolationPolicy }

func (r fixedDatabaseFloor) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return r.policy, nil
}

// TestCreateForTenant_StrictFloor_SharedWALRefused: under a strict cluster
// floor every isolated tenant is strict, whatever its own policy.
func TestCreateForTenant_StrictFloor_SharedWALRefused(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	WithDatabaseIsolationPolicyReader(fixedDatabaseFloor{policy: tenant.ClusterIsolationPolicyStrict})(svc.(*DatabaseService))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant err = %v, want *database.StrictTenantDedicatedWALRequiredError", err)
	}
}

// TestCreateForTenant_NonStrictTenant_SharedWALAllowed: an isolated tenant
// under operator-access keeps today's behaviour (shared WAL allowed).
func TestCreateForTenant_NonStrictTenant_SharedWALAllowed(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	WithDatabaseIsolationPolicyReader(fixedDatabaseFloor{policy: tenant.ClusterIsolationPolicyOperatorAccess})(svc.(*DatabaseService))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	got, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.DedicatedWAL {
		t.Errorf("persisted DedicatedWAL = true, want false")
	}
}
