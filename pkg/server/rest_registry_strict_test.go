// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// floorTenantSvc is a tenant service that also reads the cluster isolation
// floor (tenant.ClusterIsolationPolicyReader), as the production one does.
type floorTenantSvc struct {
	fakeRaftTenantSvc
	floor    tenant.ClusterIsolationPolicy
	floorErr error
}

func (s *floorTenantSvc) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return s.floor, s.floorErr
}

// newStrictSharedRegistry is a registry whose only store is the shared one,
// for barrier tenant "bt" with the given policy and cluster floor.
func newStrictSharedRegistry(t *testing.T, policy tenant.TenantIsolationPolicy, floor tenant.ClusterIsolationPolicy, floorErr error) (*restServiceRegistry, *mockKVService) {
	t.Helper()
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "bt")
	shared, sharedSvcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", sharedSvcs)
	info := &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier, IsolationPolicy: policy}
	return &restServiceRegistry{
		dbReg:        dbReg,
		barrierReg:   reg,
		tenantSvc:    &floorTenantSvc{fakeRaftTenantSvc: fakeRaftTenantSvc{info: info}, floor: floor, floorErr: floorErr},
		catalog:      raftCatalogStub{tenantID: "bt", db: "app"},
		systemTenant: "system",
	}, shared
}

// TestResolve_StrictTenantSharedDefaultStoreIsRefused: a strict barrier
// tenant -- by its own policy, or inheriting a strict floor -- that names no
// database, or the default one, is refused with
// *rest.StrictTenantSharedStoreError instead of being served from the
// shared node-keyed store, and nothing lands there.
func TestResolve_StrictTenantSharedDefaultStoreIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy tenant.TenantIsolationPolicy
		floor  tenant.ClusterIsolationPolicy
	}{
		{"own policy strict", tenant.TenantIsolationPolicyStrict, ""},
		{"inherits a strict floor", "", tenant.ClusterIsolationPolicyStrict},
	} {
		for _, db := range []string{"", "system"} {
			t.Run(tc.name+"/"+db, func(t *testing.T) {
				rr, shared := newStrictSharedRegistry(t, tc.policy, tc.floor, nil)
				_, err := rr.KVService("bt", db)
				var refused *rest.StrictTenantSharedStoreError
				if !errors.As(err, &refused) || refused.TenantID != "bt" || refused.Database != db {
					t.Fatalf("KVService(bt, %q) = %v, want *rest.StrictTenantSharedStoreError for bt", db, err)
				}
				if keys := storeKeys(shared); len(keys) != 0 {
					t.Fatalf("shared store keys = %v, want none", keys)
				}
			})
		}
	}
}

// TestResolve_StrictFloorReadErrorFailsClosed: a floor that cannot be read
// refuses the shared store rather than serving it.
func TestResolve_StrictFloorReadErrorFailsClosed(t *testing.T) {
	boom := errors.New("floor unreadable")
	rr, _ := newStrictSharedRegistry(t, "", "", boom)
	if _, err := rr.KVService("bt", ""); !errors.Is(err, boom) {
		t.Fatalf("KVService = %v, want the floor read error", err)
	}
}

// TestResolve_OperatorAccessTenantKeepsTheSharedStore: the same barrier
// tenant under an operator-access policy still resolves the shared store.
func TestResolve_OperatorAccessTenantKeepsTheSharedStore(t *testing.T) {
	rr, shared := newStrictSharedRegistry(t, tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess, nil)
	kv, err := rr.KVService("bt", "system")
	if err != nil {
		t.Fatalf("KVService(operator-access): %v", err)
	}
	if err := kv.Put(context.Background(), "routed", []byte("yes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := string(shared.store["bt/system/routed"]); got != "yes" {
		t.Fatalf("shared store bt/system/routed = %q, want yes; keys = %v", got, storeKeys(shared))
	}
}

// TestResolve_StrictTenantOwnBarrierDatabaseStillResolves: the shared-store
// refusal is only for the shared store. A strict tenant's database whose own
// barrier wraps its own data path is served from that store, never the
// system shard, so it still resolves, under its own policy or a strict floor.
func TestResolve_StrictTenantOwnBarrierDatabaseStillResolves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy tenant.TenantIsolationPolicy
		floor  tenant.ClusterIsolationPolicy
	}{
		{"own policy strict", tenant.TenantIsolationPolicyStrict, ""},
		{"inherits a strict floor", tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyStrict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr, shared := newStrictSharedRegistry(t, tc.policy, tc.floor, nil)
			mgr := databasetest.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
			t.Cleanup(func() { _ = mgr.Close() })
			rr.dbManager = mgr
			rr.dedicatedStore = newDedicatedStoreServiceCache()
			ctx := context.Background()
			if _, err := mgr.CreateWithCredentials(ctx, database.DatabaseConfig{
				TenantID: "bt", Name: "vault", Backend: database.BackendMemory, DataDir: t.TempDir(),
				Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase},
			}, seal.MustCredentials(ctx, []byte("db-pass"))); err != nil {
				t.Fatalf("create the barrier database: %v", err)
			}
			kv, err := rr.KVService("bt", "vault")
			if err != nil {
				t.Fatalf("KVService(bt, vault) = %v, want its own barrier store", err)
			}
			if err := kv.Put(ctx, "k", []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if keys := storeKeys(shared); len(keys) != 0 {
				t.Fatalf("shared store keys = %v, want none: the database's own store serves it", keys)
			}
		})
	}
}
