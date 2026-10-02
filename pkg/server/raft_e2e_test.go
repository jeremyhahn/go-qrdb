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

package server

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// alwaysAbsentCatalog is a DatabaseCatalog that reports every database as
// absent, simulating a node whose local catalog (meta.json) never recorded a
// dedicated-raft database created on another node.
type alwaysAbsentCatalog struct{}

func (alwaysAbsentCatalog) DatabaseExists(string, string) bool { return false }

// TestRaftE2E_ResolveIgnoresLocalCatalog proves that an IsolationRaft database
// resolves through the replicated placement-backed RaftBackendFactory even when
// the node-local catalog has no record of it — the post-restart follower / leader
// change case where only the creating node holds a local meta.json record. A
// genuinely unknown database for the raft tenant still surfaces as a 404.
func TestRaftE2E_ResolveIgnoresLocalCatalog(t *testing.T) {
	stack := newRaftE2EStack(t)
	ctx := context.Background()
	stack.createRaftTenantDB(t, "acme", "orders")

	// Simulate a node whose local catalog never recorded the database.
	stack.resolver.catalog = alwaysAbsentCatalog{}

	// The dedicated-raft database still resolves and serves data via the raft
	// backend, despite the local catalog reporting it absent.
	kv, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolve with absent local catalog: %v", err)
	}
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, gerr := kv.Get(ctx, "k"); gerr != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want v", got, gerr)
	}

	// A genuinely unknown database for the raft tenant is a 404.
	_, err = stack.resolver.KVService("acme", "does-not-exist")
	var dnf *rest.DatabaseNotFoundError
	if !errors.As(err, &dnf) {
		t.Fatalf("unknown db: got %T %v, want *rest.DatabaseNotFoundError", err, err)
	}
}

// raftE2EStack bundles the live components the daemon wires for IsolationRaft, so
// the end-to-end test exercises the real create→resolve→data-plane vertical
// through the in-process service seam (no transport/mTLS).
type raftE2EStack struct {
	host     *quicraft.Host
	reg      *seal.BarrierRegistry
	tenants  *tenant.TenantService
	manager  *database.Manager
	dbSvc    service.DatabaseServicer
	raftReg  *RaftBackendFactory
	resolver *restServiceRegistry
}

// newRaftE2EStack assembles the full IsolationRaft stack against real
// subsystems: a volume-pooled barrier registry (system barrier initialized so
// system-managed tenants auto-unseal), a tenant service entitled to raft +
// system-managed barrier, a database manager with a shard allocator, the
// quota-enforcing DatabaseService, the shared-host RaftBackendFactory, and the
// transport-neutral resolver every protocol uses.
func newRaftE2EStack(t *testing.T) *raftE2EStack {
	t.Helper()
	ctx := context.Background()

	reg := newVolumePooledRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	tenants, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, volTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:        true,
			AllowSystemManagedBarrier: true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	mgr := database.NewManager(volTestLogger(), t.TempDir(), database.WithShardIDClaimer(testShardClaimer(t)))
	t.Cleanup(func() { _ = mgr.Close() })

	// Numeric quota plane (raft-group counter reads the persisted usage record).
	quotaSvc, err := quota.NewService(tenants, volTestLogger())
	if err != nil {
		t.Fatalf("quota.NewService: %v", err)
	}
	quotaSvc.RegisterCounter(quota.ResourceRaftGroups, func(ctx context.Context, id string) (int64, error) {
		return tenants.GetQuotaUsage(ctx, id, quota.ResourceRaftGroups)
	})
	quotaSvc.RegisterCounter(quota.ResourceDatabases, func(ctx context.Context, id string) (int64, error) {
		return tenants.GetQuotaUsage(ctx, id, quota.ResourceDatabases)
	})

	dbSvc := service.NewDatabaseService(mgr,
		service.WithEntitlementChecker(tenants),
		service.WithTenantInfoLookup(tenants),
		service.WithQuotaChecker(quotaSvc),
		service.WithQuotaUsageRecorder(tenants),
	)

	host, _ := newSharedTestHost(t)
	raftReg := NewRaftBackendFactoryFromManager(
		host, sharedHostReplicaID, sharedHostRaftAddr, reg, mgr, tenant.SystemTenantID, volTestLogger())
	t.Cleanup(func() { _ = raftReg.CloseAll() })

	resolver := &restServiceRegistry{
		dbReg:        NewDatabaseServiceRegistry("system"),
		barrierReg:   reg,
		tenantSvc:    tenants,
		catalog:      newManagerCatalog(mgr, tenant.SystemTenantID),
		raftBackend:  raftReg,
		systemTenant: tenant.SystemTenantID,
	}

	return &raftE2EStack{
		host:     host,
		reg:      reg,
		tenants:  tenants,
		manager:  mgr,
		dbSvc:    dbSvc,
		raftReg:  raftReg,
		resolver: resolver,
	}
}

// createRaftTenantDB creates a system-managed IsolationRaft tenant and a database
// for it through the real DatabaseService.CreateForTenant path, returning the
// tenant DataRoot and the database's runtime info.
func (s *raftE2EStack) createRaftTenantDB(t *testing.T, tenantID, db string) (string, *database.DatabaseInfo) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.tenants.CreateTenant(ctx, tenantID, tenantID, tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(%s): %v", tenantID, err)
	}
	info, err := s.dbSvc.CreateForTenant(tenant.WithTenantID(ctx, tenantID),
		database.DatabaseConfig{Name: db, Engine: database.EngineKV})
	if err != nil {
		t.Fatalf("CreateForTenant(%s/%s): %v", tenantID, db, err)
	}
	ti, err := s.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", tenantID, err)
	}
	return ti.DataRoot, info
}
