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

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestCreateForTenant_DedicatedWAL_Persisted verifies the per-tenant-KEY WAL
// opt-in is honored for a dedicated-raft database when both the entitlement and
// the MaxDedicatedWALGroups quota permit it: the flag is persisted and the
// counter is reserved.
func TestCreateForTenant_DedicatedWAL_Persisted(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
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

// TestCreateForTenant_DedicatedWAL_OverQuota rejects an opt-in that would exceed
// MaxDedicatedWALGroups and reserves nothing (the gate fires before the raft
// reservation block).
func TestCreateForTenant_DedicatedWAL_OverQuota(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{
		MaxTotalReplicas:          10,
		MaxDedicatedWALGroups:     1,
		CurrentDedicatedWALGroups: 1,
	}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true
	_, err := svc.CreateForTenant(ctx, cfg)
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("CreateForTenant error = %v, want *quota.ExceededError", err)
	}
	if exceeded.Resource != quota.ResourceDedicatedWALGroups {
		t.Errorf("ExceededError.Resource = %v, want ResourceDedicatedWALGroups", exceeded.Resource)
	}
	if f.incWAL != 0 || f.incRaft != 0 {
		t.Errorf("an over-quota opt-in must reserve nothing; incWAL=%d incRaft=%d", f.incWAL, f.incRaft)
	}
}

// TestCreateForTenant_DedicatedWAL_EntitlementRejected rejects an opt-in when the
// tenant's plan permits dedicated raft but not the dedicated_wal feature.
func TestCreateForTenant_DedicatedWAL_EntitlementRejected(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, dataDir)
	t.Cleanup(func() { _ = mgr.Close() })

	entChecker := &mockEntitlementChecker{
		featureFn: func(_ context.Context, tenantID, feature string) error {
			if feature == quota.FeatureDedicatedWAL {
				return &quota.EntitlementError{TenantID: tenantID, Feature: feature}
			}
			return nil
		},
	}
	svc := NewDatabaseService(mgr, WithEntitlementChecker(entChecker))
	ctx := tenant.WithTenantID(context.Background(), "acme")

	cfg := raftDBConfig("dedicated")
	cfg.DedicatedWAL = true
	_, err := svc.CreateForTenant(ctx, cfg)
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("CreateForTenant error = %v, want *quota.EntitlementError", err)
	}
	if entErr.Feature != quota.FeatureDedicatedWAL {
		t.Errorf("EntitlementError.Feature = %q, want %q", entErr.Feature, quota.FeatureDedicatedWAL)
	}
}

// TestCreateForTenant_DedicatedWAL_NonRaftCleared verifies that a dedicated-WAL
// opt-in on a non-raft backend is silently cleared (never persisted or metered).
func TestCreateForTenant_DedicatedWAL_NonRaftCleared(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxDedicatedWALGroups: 5}}
	// A prefix tenant so the IsolationRaft backend-forcing path does not run.
	info := &tenant.TenantInfo{ID: "rt", IsolationType: tenant.IsolationPrefix, DataRoot: t.TempDir()}
	svc := newReplicaDatabaseService(t, info, f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app") // memory/file backend, not raft
	cfg.DedicatedWAL = true
	got, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.DedicatedWAL {
		t.Errorf("non-raft DedicatedWAL must be cleared, got true")
	}
	if f.incWAL != 0 {
		t.Errorf("non-raft opt-in must not reserve the dedicated-WAL counter; incWAL=%d", f.incWAL)
	}
}

// TestCreateForTenant_DedicatedWAL_CounterRoundTrip verifies the dedicated-WAL
// counter is reserved on create and released on drop.
func TestCreateForTenant_DedicatedWAL_CounterRoundTrip(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, raftTenant(t), f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("app")
	cfg.DedicatedWAL = true
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if err := svc.DropForTenant(ctx, "app"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if f.incWAL != 1 || f.decWAL != 1 {
		t.Errorf("dedicated-WAL counter round-trip: inc=%d dec=%d, want 1/1", f.incWAL, f.decWAL)
	}
}
