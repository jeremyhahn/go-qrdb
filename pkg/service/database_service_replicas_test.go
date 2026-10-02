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
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeReplicaQuota is both a quota.QuotaProvider and a QuotaUsageRecorder. It
// holds a single tenant's Quotas and reflects increments/decrements back into
// the current-usage counters so the real quota.Service observes them on the next
// CheckN. It records per-resource increment/decrement totals for assertions.
type fakeReplicaQuota struct {
	q        *quota.Quotas
	incRaft  int64
	decRaft  int64
	incTotal int64
	decTotal int64
	incWAL   int64
	decWAL   int64
}

func (f *fakeReplicaQuota) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return f.q, nil
}

func (f *fakeReplicaQuota) IncrementQuotaUsage(_ context.Context, _ string, res quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	switch res {
	case quota.ResourceRaftGroups:
		f.incRaft += delta
	case quota.ResourceTotalReplicas:
		f.incTotal += delta
	case quota.ResourceDedicatedWALGroups:
		f.incWAL += delta
	}
	cur, _ := f.q.CurrentUsage(res)
	_ = f.q.SetCurrentUsage(res, cur+delta)
	return nil, nil
}

func (f *fakeReplicaQuota) DecrementQuotaUsage(_ context.Context, _ string, res quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	switch res {
	case quota.ResourceRaftGroups:
		f.decRaft += delta
	case quota.ResourceTotalReplicas:
		f.decTotal += delta
	case quota.ResourceDedicatedWALGroups:
		f.decWAL += delta
	}
	cur, _ := f.q.CurrentUsage(res)
	next := cur - delta
	if next < 0 {
		next = 0
	}
	_ = f.q.SetCurrentUsage(res, next)
	return nil, nil
}

// newReplicaQuotaChecker wires a real quota.Service over the fake provider with
// the production counter registrations: ResourceReplicasPerGroup reports zero
// (per-group cap), the others read the reflected current usage.
func newReplicaQuotaChecker(t *testing.T, f *fakeReplicaQuota) quota.Checker {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	qs, err := quota.NewService(f, logger)
	if err != nil {
		t.Fatalf("quota.NewService: %v", err)
	}
	read := func(res quota.Resource) quota.CountFunc {
		return func(_ context.Context, _ string) (int64, error) {
			return f.q.CurrentUsage(res)
		}
	}
	qs.RegisterCounter(quota.ResourceDatabases, read(quota.ResourceDatabases))
	qs.RegisterCounter(quota.ResourceRaftGroups, read(quota.ResourceRaftGroups))
	qs.RegisterCounter(quota.ResourceTotalReplicas, read(quota.ResourceTotalReplicas))
	qs.RegisterCounter(quota.ResourceDedicatedWALGroups, read(quota.ResourceDedicatedWALGroups))
	qs.RegisterCounter(quota.ResourceReplicasPerGroup, func(_ context.Context, _ string) (int64, error) {
		return 0, nil
	})
	return qs
}

// newReplicaDatabaseService builds a DatabaseService backed by a Manager with a
// shard-ID claimer and wired with the replica-aware quota checker + usage recorder.
func newReplicaDatabaseService(t *testing.T, info *tenant.TenantInfo, f *fakeReplicaQuota) DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir(), database.WithShardIDClaimer(testShardClaimer(t)))
	t.Cleanup(func() { _ = mgr.Close() })
	return NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
	)
}

func raftTenant(t *testing.T) *tenant.TenantInfo {
	return &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationRaft, DataRoot: t.TempDir()}
}

// TestCreateForTenant_Replicas_UnderPerGroupCap accepts a replica count within
// the per-group cap and meters it against the aggregate budget.
func TestCreateForTenant_Replicas_UnderPerGroupCap(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 3, MaxTotalReplicas: 10}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.Replicas = 2
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.Replicas != 2 {
		t.Errorf("persisted Replicas = %d, want 2", got.Replicas)
	}
	if f.incTotal != 2 {
		t.Errorf("total-replica increment = %d, want 2", f.incTotal)
	}
	if f.incRaft != 1 {
		t.Errorf("raft-group increment = %d, want 1", f.incRaft)
	}
}

// TestCreateForTenant_Replicas_OverPerGroupCap rejects a replica count above the
// per-group cap with a quota.ExceededError and reserves nothing.
func TestCreateForTenant_Replicas_OverPerGroupCap(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 3, MaxTotalReplicas: 100}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.Replicas = 5
	_, err := svc.CreateForTenant(ctx, cfg)
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v, want *quota.ExceededError", err)
	}
	if ee.Resource != quota.ResourceReplicasPerGroup {
		t.Errorf("exceeded resource = %v, want ResourceReplicasPerGroup", ee.Resource)
	}
	if f.incTotal != 0 || f.incRaft != 0 {
		t.Errorf("reservation leaked on rejection: incTotal=%d incRaft=%d", f.incTotal, f.incRaft)
	}
}

// TestCreateForTenant_Replicas_TotalBudgetExceeded rejects a create when the
// existing total plus the requested replicas exceeds the aggregate budget.
func TestCreateForTenant_Replicas_TotalBudgetExceeded(t *testing.T) {
	// Per-group unlimited (0); aggregate budget 3 with 2 already used.
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 0, MaxTotalReplicas: 3, CurrentTotalReplicas: 2}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.Replicas = 2 // 2 + 2 = 4 > 3
	_, err := svc.CreateForTenant(ctx, cfg)
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v, want *quota.ExceededError", err)
	}
	if ee.Resource != quota.ResourceTotalReplicas {
		t.Errorf("exceeded resource = %v, want ResourceTotalReplicas", ee.Resource)
	}
}

// TestCreateForTenant_Replicas_CounterRoundTrip verifies the total-replica
// counter is incremented by the replica count on create and released by the
// same amount on drop.
func TestCreateForTenant_Replicas_CounterRoundTrip(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("rr")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if f.incTotal != 3 || f.incRaft != 1 {
		t.Fatalf("after create: incTotal=%d incRaft=%d, want 3,1", f.incTotal, f.incRaft)
	}
	if err := svc.DropForTenant(ctx, "rr"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if f.decTotal != 3 {
		t.Errorf("total-replica decrement after drop = %d, want 3", f.decTotal)
	}
	if f.decRaft != 1 {
		t.Errorf("raft-group decrement after drop = %d, want 1", f.decRaft)
	}
}

// TestCreateForTenant_Replicas_UnlimitedPassthrough accepts any replica count
// when both caps are unlimited (<= 0).
func TestCreateForTenant_Replicas_UnlimitedPassthrough(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 0, MaxTotalReplicas: -1}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("big")
	cfg.Replicas = 10
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant with unlimited caps: %v", err)
	}
	if got.Replicas != 10 {
		t.Errorf("persisted Replicas = %d, want 10", got.Replicas)
	}
	if f.incTotal != 10 {
		t.Errorf("total-replica increment = %d, want 10", f.incTotal)
	}
}

// TestCreateForTenant_Replicas_DefaultsToOne defaults an unset replica count to
// a single replica for a raft database.
func TestCreateForTenant_Replicas_DefaultsToOne(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 3, MaxTotalReplicas: 10}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("def") // Replicas unset (0)
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.Replicas != 1 {
		t.Errorf("persisted Replicas = %d, want 1 (default)", got.Replicas)
	}
	if f.incTotal != 1 {
		t.Errorf("total-replica increment = %d, want 1", f.incTotal)
	}
}

// TestCreateForTenant_Replicas_SystemTenantExempt verifies the system tenant
// bypasses replica quota enforcement and metering entirely.
func TestCreateForTenant_Replicas_SystemTenantExempt(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 1, MaxTotalReplicas: 1}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	cfg := testDBConfig("sys")
	cfg.Replicas = 999 // far above the caps; must be ignored for the system tenant
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("system-tenant CreateForTenant: %v", err)
	}
	if f.incTotal != 0 || f.incRaft != 0 {
		t.Errorf("system tenant must not meter replicas: incTotal=%d incRaft=%d", f.incTotal, f.incRaft)
	}
}

// TestCreateForTenant_Replicas_NonRaftIgnored verifies a non-raft database
// ignores the replica field (reset to zero, not metered).
func TestCreateForTenant_Replicas_NonRaftIgnored(t *testing.T) {
	// A prefix tenant: the isolation switch does not force the raft backend, so
	// the database stays on its (memory) backend.
	info := &tenant.TenantInfo{ID: "pf", IsolationType: tenant.IsolationPrefix}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 1, MaxTotalReplicas: 1}}
	svc := newReplicaDatabaseService(t, info, f)
	ctx := tenant.WithTenantID(context.Background(), "pf")

	cfg := testDBConfig("kv")
	cfg.Replicas = 5 // above caps, but ignored for a non-raft database
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant (non-raft): %v", err)
	}
	if got.Replicas != 0 {
		t.Errorf("non-raft persisted Replicas = %d, want 0", got.Replicas)
	}
	if f.incTotal != 0 || f.incRaft != 0 {
		t.Errorf("non-raft database must not meter replicas: incTotal=%d incRaft=%d", f.incTotal, f.incRaft)
	}
}
