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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestCreateForTenant_RaftTopology_NoneRejected verifies that a raft-backed
// database explicitly requesting cluster mode "none" is rejected: a
// dedicated Raft group is incoherent with "no consensus".
func TestCreateForTenant_RaftTopology_NoneRejected(t *testing.T) {
	svc, _ := newRaftTenantDatabaseService(t, raftTenant(t), nil)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := raftDBConfig("app")
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftNone}

	_, err := svc.CreateForTenant(ctx, cfg)
	var irt *InvalidRaftTopologyError
	if !errors.As(err, &irt) {
		t.Fatalf("CreateForTenant(ClusterMode=none) = %v, want *InvalidRaftTopologyError", err)
	}
}

// TestCreateForTenant_RaftTopology_DistributedRequiresPlacement verifies that
// requesting the "distributed" cluster mode fails closed with
// RaftDistributedUnavailableError when this node has no placement subsystem
// (store + scheduler) configured, rather than silently collapsing to a
// single local replica.
func TestCreateForTenant_RaftTopology_DistributedRequiresPlacement(t *testing.T) {
	svc, _ := newRaftTenantDatabaseService(t, raftTenant(t), nil)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := raftDBConfig("app")
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftDistributed}

	_, err := svc.CreateForTenant(ctx, cfg)
	var rdu *RaftDistributedUnavailableError
	if !errors.As(err, &rdu) {
		t.Fatalf("CreateForTenant(ClusterMode=distributed, no placement) = %v, want *RaftDistributedUnavailableError", err)
	}
}

// TestCreateForTenant_RaftTopology_DistributedWithPlacementSucceeds verifies
// that requesting "distributed" succeeds -- and drives the existing
// multi-replica placement pipeline -- once the placement subsystem is wired.
func TestCreateForTenant_RaftTopology_DistributedWithPlacementSucceeds(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2", "n3"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Backend = database.BackendRaft
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftDistributed}
	cfg.Replicas = 3

	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant(ClusterMode=distributed, placement wired): %v", err)
	}
	if info.Replicas != 3 {
		t.Errorf("Replicas = %d, want 3", info.Replicas)
	}

	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement store Get: %v", err)
	}
	if len(p.Replicas) != 3 {
		t.Errorf("persisted placement replicas = %d, want 3", len(p.Replicas))
	}
}

// TestCreateForTenant_RaftTopology_LocalDefaultUnaffected verifies that a
// dedicated-raft database created with no explicit Raft config -- the
// existing IsolationRaft default, which fills in ClusterMode=local -- is
// unaffected by the new topology validation (non-regression).
func TestCreateForTenant_RaftTopology_LocalDefaultUnaffected(t *testing.T) {
	svc, _ := newRaftTenantDatabaseService(t, raftTenant(t), nil)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := raftDBConfig("app")

	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant(no explicit Raft config): %v", err)
	}
	if info.Raft == nil || info.Raft.ClusterMode != database.RaftLocal {
		t.Errorf("Raft.ClusterMode = %+v, want %q (the existing IsolationRaft default)", info.Raft, database.RaftLocal)
	}
}

// TestCreateWithCredentials_RaftTopology_NoneRejected verifies the same
// "none" rejection at the non-tenant (system-scope) Create path.
func TestCreateWithCredentials_RaftTopology_NoneRejected(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := raftDBConfig("sysdb")
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftNone}

	_, err := svc.Create(cfg)
	var irt *InvalidRaftTopologyError
	if !errors.As(err, &irt) {
		t.Fatalf("Create(ClusterMode=none) = %v, want *InvalidRaftTopologyError", err)
	}
}

// TestCreateWithCredentials_RaftTopology_DistributedRequiresPlacement
// verifies the same distributed-requires-placement gate at the non-tenant
// (system-scope) Create path.
func TestCreateWithCredentials_RaftTopology_DistributedRequiresPlacement(t *testing.T) {
	svc := newTestDatabaseService(t)

	cfg := raftDBConfig("sysdb")
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftDistributed}

	_, err := svc.Create(cfg)
	var rdu *RaftDistributedUnavailableError
	if !errors.As(err, &rdu) {
		t.Fatalf("Create(ClusterMode=distributed, no placement) = %v, want *RaftDistributedUnavailableError", err)
	}
}
