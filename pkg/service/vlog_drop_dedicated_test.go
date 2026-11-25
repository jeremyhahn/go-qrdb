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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestDatabaseService_DropOfADedicatedTenantNeedsNoLogWork pins that a
// volume- or raft-isolated tenant, which has no verifiable logs (the log
// resolver refuses it with DedicatedTenantAuditNotSupportedError), drops its
// database through the wired log service without touching any shard, and
// placement teardown runs to completion (DropCommitted, then removal).
func TestDatabaseService_DropOfADedicatedTenantNeedsNoLogWork(t *testing.T) {
	for _, info := range []*tenant.TenantInfo{
		{ID: "vt", IsolationType: tenant.IsolationVolume},
		{ID: "rt", IsolationType: tenant.IsolationRaft},
	} {
		t.Run(string(info.IsolationType), func(t *testing.T) {
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
			dbs, store := newPlacementDatabaseService(t, info, f, healthyNodes("n1"))
			svc := dbs.(*DatabaseService)
			shard, calls := untouchedSystemShard()
			lookup := &fakeIsolationLookup{tenants: map[string]*tenant.TenantInfo{info.ID: info}}
			resolver := NewSystemShardAuditResolver(shard, 1, lookup)
			logs, err := NewLogService(LogServiceConfig{Resolver: resolver, AuditResolver: resolver, Databases: noDatabases(t)})
			if err != nil {
				t.Fatalf("NewLogService: %v", err)
			}
			svc.SetLogReclaimer(logs.(LogReclaimer))
			ctx := tenant.WithTenantID(context.Background(), info.ID)
			cfg := testDBConfig("orders")
			cfg.Replicas = 1
			if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
				t.Fatalf("CreateForTenant: %v", err)
			}
			if err := svc.DropForTenant(ctx, "orders"); err != nil {
				t.Fatalf("DropForTenant = %v, want success: the tenant has no logs to reclaim", err)
			}
			if _, err := svc.Get("orders"); err == nil {
				t.Fatal("the database survived its drop")
			}
			if _, err := store.Get(ctx, info.ID, "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
				t.Fatalf("placement after the drop = %v, want ErrPlacementNotFound (teardown committed)", err)
			}
			if *calls != 0 {
				t.Fatalf("the system shard saw %d calls, want none", *calls)
			}
		})
	}
}
