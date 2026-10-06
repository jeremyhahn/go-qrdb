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
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// go-qrdb#413 -- create, populate, delete, RE-create, read
//
// The issue was read from the code rather than measured, and it is explicit
// that the answer differs by isolation type. These tests run the sequence for
// each type against real components: a real KVEngine over a real storage
// backend behind a real LocalBackend, the real service.KVService and
// IndexService on top of it, the real restServiceRegistry applying the real
// ComposeNamespace, and a real TenantService over a real BarrierRegistry with
// a real volume pool.
//
// What each type did before the fix, measured:
//
//	prefix  the tenant's keys survived DeleteTenant, the id could be
//	        re-created, and the second tenant READ the first tenant's data.
//	        This is the case with no cryptographic backstop at all, and it is
//	        the one these tests lead with.
//	volume  the DataRoot is derived from the tenant id, so re-creating the id
//	        landed on the SAME directory and re-opened the SAME barrier store.
//	        A system-managed tenant failed with "barrier already initialized"
//	        -- a refusal, but an accidental one with an opaque message. A
//	        SELF-managed tenant was created successfully and was then
//	        permanently unusable: its barrier could not be initialized
//	        (already is) and could not be unsealed (nobody holds the first
//	        tenant's credential). The data stayed unreadable throughout, so
//	        the exposure was a bricked id and a leaked directory, not a
//	        disclosure.
//	raft    identical to volume for everything DeleteTenant touches: both take
//	        RegisterVolumeTenant, both get a tenant-id-derived DataRoot and a
//	        per-tenant barrier. The part that is raft's alone is its placement
//	        records; see TestTenantDelete_RaftPlacementRecordsAreReclaimed.
// ---------------------------------------------------------------------------

// tenantFixture is the shared-store half of the sequence: a real state
// machine, the services the transports resolve through, and a tenant service
// with the teardown wired the way lifecycle.BuildServices wires it.
type tenantFixture struct {
	root     service.KVServicer
	index    service.IndexServicer
	tenants  *tenant.TenantService
	registry restKVResolver
}

// restKVResolver is the slice of rest.ServiceRegistry these tests use.
type restKVResolver interface {
	KVService(tenantID, db string) (service.KVServicer, error)
}

// newTenantFixture builds the fixture. wireTeardown selects whether the
// tenant service gets its reclaimer, which is what separates "the id is
// retired" from "the bytes are gone" when a test wants to check only one.
func newTenantFixture(t *testing.T, wireTeardown bool) *tenantFixture {
	t.Helper()

	stg := memory.NewStorage()
	host := backend.NewLocalBackend(engine.NewKVEngine(stg))
	t.Cleanup(func() { _ = host.Close() })

	root := service.NewKVService(host, shard.ShardKV)
	idx := service.NewIndexService(host, shard.ShardKV)

	svc, _ := newAllIsolationTenantService(t)
	if wireTeardown {
		svc.SetKeyspaceReclaimer(service.NewKeyspaceReclaimService(
			service.ReclaimTarget{KV: root, Index: idx}))
	}

	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", &DatabaseServices{Name: "system", KV: root, Index: idx})
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System, WithTenantServicer(svc))

	return &tenantFixture{root: root, index: idx, tenants: svc, registry: reg}
}

// newAllIsolationTenantService builds a TenantService over a pooled barrier
// registry whose default entitlements permit EVERY isolation type, so one
// fixture serves the prefix, volume and raft legs of the sequence.
func newAllIsolationTenantService(t *testing.T) (*tenant.TenantService, *seal.BarrierRegistry) {
	t.Helper()

	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("tenant-delete-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	reg, _, allocator := sealtest.PooledRegistry(t, system)

	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes: []string{
				quota.IsolationModePrefix,
				quota.IsolationModeVolume,
				quota.IsolationModeRaft,
			},
			AllowVolumeStorage:        true,
			AllowDedicatedRaft:        true,
			AllowSystemManagedBarrier: true,
			AllowedVolumePools:        []string{"default"},
		}),
		tenant.WithVolumeAllocator(allocator),
	)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// populate writes one key of every shape a prefix tenant owns that a
// tenant-scoped reclamation has to reach, and returns the keys it wrote.
//
// The time-series shapes are written through the raw store rather than the
// TimeSeriesServicer because the point is WHERE the bytes land, not how they
// got there: their namespace sits INSIDE the series prefix, which is the
// property that makes a single "<tenant>/" scan insufficient and is the one
// thing a test here must not take on faith.
func (f *tenantFixture) populate(ctx context.Context, t *testing.T, tenantID string) []string {
	t.Helper()

	kv, err := f.registry.KVService(tenantID, "system")
	if err != nil {
		t.Fatalf("KVService(%s): %v", tenantID, err)
	}
	if err := kv.Put(ctx, "payroll", []byte("top secret")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ns := tenant.ComposeNamespace(tenantID, "system")
	raw := []string{
		tenant.IdentityNamespace(tenantID) + "user/alice",
		timeseries.KeyPrefixPoints + ns + "cpu:1:0",
		timeseries.KeyPrefixMetricIndex + ns + "cpu",
		timeseries.KeyPrefixRetention + ns + "cpu",
	}
	for _, k := range raw {
		if err := f.root.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	return append([]string{ns + "payroll"}, raw...)
}

// survivors returns the keys still in the store that belong to tenantID.
//
// It enumerates through ListAll, never List: an assertion about residue has
// to see the internal keyspace, or it silently stops checking the thing it
// exists to check (go-qrdb#412).
func (f *tenantFixture) survivors(ctx context.Context, t *testing.T, tenantID string) []string {
	t.Helper()
	all, err := f.root.ListAll(ctx, "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	prefixes := service.TenantKeyPrefixes(tenantID)
	var owned []string
	for _, k := range all {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				owned = append(owned, k)
				break
			}
		}
	}
	return owned
}

// assertDataRootGone fails when a deleted tenant's dedicated directory is
// still on disk.
func assertDataRootGone(t *testing.T, dataRoot string) {
	t.Helper()
	if dataRoot == "" {
		return
	}
	if _, err := os.Stat(dataRoot); !os.IsNotExist(err) {
		t.Errorf("the deleted tenant's DataRoot %s is still on disk (stat err %v)", dataRoot, err)
	}
}

// TestTenantDelete_PrefixIdIsRetired runs the issue's sequence for prefix
// isolation, which is the case whose data stayed READABLE. The id must be
// refused on re-creation and the keyspace must be empty.
func TestTenantDelete_PrefixIdIsRetired(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	if _, err := f.tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	written := f.populate(ctx, t, "acme")
	if got := f.survivors(ctx, t, "acme"); len(got) != len(written) {
		t.Fatalf("fixture wrote %d keys but %d are visible (%v); "+
			"the rest of this test would pass vacuously", len(written), len(got), got)
	}

	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	if got := f.survivors(ctx, t, "acme"); len(got) != 0 {
		t.Errorf("%d keys of the deleted tenant survived: %v", len(got), got)
	}

	var deleted *tenant.TenantDeletedError
	_, err := f.tenants.CreateTenant(ctx, "acme", "Acme Two", tenant.IsolationPrefix)
	if !errors.As(err, &deleted) {
		t.Fatalf("re-creating a deleted tenant id must be refused, got %v", err)
	}
	if deleted.TenantID != "acme" {
		t.Errorf("the refusal names tenant %q, want acme", deleted.TenantID)
	}
	if deleted.DeletedAt.IsZero() {
		t.Error("the refusal must report when the id was retired")
	}
}

// TestTenantDelete_UnreclaimedKeyspaceIsStillUnreachable is the same sequence
// with NO reclaimer wired, which is the shape of a composition that has no
// shared store behind it -- and, before this change, of every composition.
// The bytes are still there; the id is not available, so nothing can name
// them.
func TestTenantDelete_UnreclaimedKeyspaceIsStillUnreachable(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, false)

	if _, err := f.tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	f.populate(ctx, t, "acme")
	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	if got := f.survivors(ctx, t, "acme"); len(got) == 0 {
		t.Fatal("with no reclaimer wired the keys should still be present; " +
			"this test is measuring the tombstone, not the reclamation")
	}
	var deleted *tenant.TenantDeletedError
	if _, err := f.tenants.CreateTenant(ctx, "acme", "Acme Two", tenant.IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("the id must be refused even when the bytes remain, got %v", err)
	}
}

// TestTenantDelete_ReclamationIsScopedToTheTenant asserts the reclamation
// reaches all four shapes a prefix tenant owns -- including the three
// time-series ranges, whose namespace sits inside the series prefix and which
// a single "<tenant>/" scan does not reach -- and touches no other tenant.
func TestTenantDelete_ReclamationIsScopedToTheTenant(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	var globexKeys int
	for _, id := range []string{"acme", "globex"} {
		if _, err := f.tenants.CreateTenant(ctx, id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
		globexKeys = len(f.populate(ctx, t, id))
	}

	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	if got := f.survivors(ctx, t, "acme"); len(got) != 0 {
		t.Errorf("the deleted tenant kept %d keys: %v", len(got), got)
	}
	if got := f.survivors(ctx, t, "globex"); len(got) != globexKeys {
		t.Errorf("the other tenant has %d keys after the delete, want %d: %v",
			len(got), globexKeys, got)
	}
}

// TestTenantDelete_SystemTenantKeyspaceIsRefused pins the guard that keeps a
// tenant reclamation from emptying the store. "system/" holds the bootstrap
// database and every system identity record, so a reclamation that accepted
// the system tenant would delete them.
func TestTenantDelete_SystemTenantKeyspaceIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	bootKey := tenant.ComposeNamespace(tenantid.System, "system") + "boot"
	if err := f.root.Put(ctx, bootKey, []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reclaimer := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: f.root, Index: f.index})
	for _, id := range []string{tenantid.System, ""} {
		var scope *service.ReclaimScopeError
		if err := reclaimer.ReclaimTenantKeyspace(ctx, id); !errors.As(err, &scope) {
			t.Errorf("ReclaimTenantKeyspace(%q) must be refused, got %v", id, err)
		}
	}

	if _, err := f.root.Get(ctx, bootKey); err != nil {
		t.Errorf("the system tenant's key was deleted: %v", err)
	}
}

// TestTenantDelete_IsIdempotent re-issues a delete whose reclamation already
// ran. The metadata is gone by then, so the second call would report
// not-found if the tombstone were not consulted -- and an interrupted delete
// could never be finished by retrying it.
func TestTenantDelete_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	if _, err := f.tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	f.populate(ctx, t, "acme")

	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("re-issued DeleteTenant: %v", err)
	}
	if got := f.survivors(ctx, t, "acme"); len(got) != 0 {
		t.Errorf("keys survived: %v", got)
	}
}

// TestTenantDelete_UnknownTenantIsStillNotFound guards the other side of the
// idempotence: an id that was never used is not-found, not a silent success.
func TestTenantDelete_UnknownTenantIsStillNotFound(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	if err := f.tenants.DeleteTenant(ctx, "never-existed"); !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Errorf("DeleteTenant on an unknown id = %v, want ErrTenantNotFound", err)
	}
}

// TestTenantDelete_DeletedIdDoesNotResolveToAKeyspace covers the identity
// half. Resolve deliberately falls through to the shared store for an UNKNOWN
// tenant, which keeps lookups working for callers that never registered
// metadata -- and which, before the tombstone existed, handed a DELETED
// tenant's id its old identity keyspace back.
func TestTenantDelete_DeletedIdDoesNotResolveToAKeyspace(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	resolver, err := tenant.NewStoreResolver(kvstore.NewMemoryKVStore(), nil, f.tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}

	// An id that was never registered still resolves: that fall-through is
	// deliberate and must survive this change.
	if _, err := resolver.Resolve(ctx, "never-registered"); err != nil {
		t.Fatalf("an unknown tenant must still resolve to the shared store: %v", err)
	}

	if _, err := f.tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := resolver.Resolve(ctx, "acme"); err != nil {
		t.Fatalf("Resolve before delete: %v", err)
	}
	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	var deleted *tenant.TenantDeletedError
	if _, err := resolver.Resolve(ctx, "acme"); !errors.As(err, &deleted) {
		t.Errorf("a deleted tenant id must not resolve to a keyspace, got %v", err)
	}
}

// TestTenantDelete_VolumeIdIsRetiredAndTheDataRootIsGone runs the sequence
// for volume isolation. The DataRoot is derived from the tenant id, so before
// the fix the id re-created onto the same directory over the same barrier
// store; now the id is refused and the directory is gone.
func TestTenantDelete_VolumeIdIsRetiredAndTheDataRootIsGone(t *testing.T) {
	ctx := context.Background()
	svc, breg := newAllIsolationTenantService(t)

	hostReg := NewTenantHostRegistryFromLookup(breg, svc, reattachTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", tenant.IsolationVolume,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	dataRoot, err := breg.LocalTenantDataRoot(ctx, "acme")
	if err != nil || dataRoot == "" {
		t.Fatalf("a volume tenant must be given a DataRoot: %v", err)
	}
	svcs, err := hostReg.Services(ctx, "acme")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "payroll", []byte("top secret")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The per-tenant Host is a server-level object the tenant service does
	// not know about; a caller closes it, exactly as the daemon does.
	if err := hostReg.Close("acme"); err != nil {
		t.Fatalf("Close host: %v", err)
	}

	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	assertDataRootGone(t, dataRoot)

	var deleted *tenant.TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme Two", tenant.IsolationVolume,
		tenant.WithBarrierAutoUnseal(true)); !errors.As(err, &deleted) {
		t.Fatalf("re-creating a deleted volume tenant id must be refused, got %v", err)
	}
}

// TestTenantDelete_SelfManagedVolumeIdIsRetired is the same sequence for a
// SELF-managed volume tenant, which is the shape that used to succeed: its
// barrier is registered sealed and left uninitialized, so nothing objected
// until the second tenant's admin found a barrier they could neither
// initialize (already is) nor unseal (nobody holds the first credential).
func TestTenantDelete_SelfManagedVolumeIdIsRetired(t *testing.T) {
	ctx := context.Background()
	svc, breg := newAllIsolationTenantService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", tenant.IsolationVolume); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	dataRoot, err := breg.LocalTenantDataRoot(ctx, "acme")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}
	if _, err := svc.TenantBarrierInit(ctx, "acme", []byte("first-tenant-passphrase"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	assertDataRootGone(t, dataRoot)

	var deleted *tenant.TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme Two", tenant.IsolationVolume); !errors.As(err, &deleted) {
		t.Fatalf("re-creating a deleted self-managed volume tenant id must be refused, got %v", err)
	}
}

// TestTenantDelete_RaftIdIsRetiredAndTheDataRootIsGone runs the sequence for
// raft isolation. Everything DeleteTenant touches is shared with volume --
// both take RegisterVolumeTenant and get a tenant-id-derived DataRoot and a
// per-tenant barrier -- and this asserts that rather than assuming it.
func TestTenantDelete_RaftIdIsRetiredAndTheDataRootIsGone(t *testing.T) {
	ctx := context.Background()
	svc, breg := newAllIsolationTenantService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	dataRoot, err := breg.LocalTenantDataRoot(ctx, "acme")
	if err != nil || dataRoot == "" {
		t.Fatalf("a raft tenant must be given a DataRoot: %v", err)
	}

	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	assertDataRootGone(t, dataRoot)

	var deleted *tenant.TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme Two", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(true)); !errors.As(err, &deleted) {
		t.Fatalf("re-creating a deleted raft tenant id must be refused, got %v", err)
	}
}

// TestTenantDelete_DeletedVolumeTenantDoesNotComeBackAtBoot is the finding
// that made DestroyTenant necessary rather than merely tidy.
//
// UnregisterTenant, which DeleteTenant used to call, seals and forgets the
// in-memory barrier and leaves the DataRoot record under
// "__registry:tenant_root/" in place. RestoreVolumeTenants reads exactly that
// record at boot -- so the deleted tenant's barrier was re-registered on the
// next restart, at the same directory, over the same store.
func TestTenantDelete_DeletedVolumeTenantDoesNotComeBackAtBoot(t *testing.T) {
	ctx := context.Background()
	svc, breg := newAllIsolationTenantService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", tenant.IsolationVolume,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	// Boot: restore every volume tenant whose DataRoot record survived.
	if err := breg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if _, err := breg.GetTenant("acme"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Errorf("a deleted tenant's barrier was restored at boot: %v", err)
	}
	for _, id := range breg.ListTenants() {
		if id == "acme" {
			t.Error("a deleted tenant is still registered after a boot restore")
		}
	}
}

// TestTenantDelete_RaftPlacementRecordsAreReclaimed covers the one thing that
// is raft isolation's alone.
//
// A dedicated-raft database's replica assignment lives at
// "__sys:placement/{tenant}/{db}", OUTSIDE the tenant's namespace and keyed by
// tenant, and the per-node reconciler watches that range: removing an entry is
// what stops the shard and purges its data dir on every node holding a
// replica. DeleteTenant never touched those records, so a deleted raft
// tenant's shards kept running cluster-wide -- which the local node's DataRoot
// removal cannot reach, because the other replicas' directories are on other
// nodes.
//
// The key is learned from the REAL placement.Store rather than written by
// hand, so a change to placementKey's formula fails here instead of silently
// making the reclamation a no-op.
func TestTenantDelete_RaftPlacementRecordsAreReclaimed(t *testing.T) {
	ctx := context.Background()

	probe := kvstore.NewMemoryKVStore()
	ps, err := placement.NewStore(probe)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := ps.Put(ctx, &placement.Placement{
		TenantID: "acme", Database: "ledger", ShardID: 1000, RF: 1,
		Replicas: []placement.Replica{{NodeID: "n1", ReplicaID: 1}},
	}); err != nil {
		t.Fatalf("placement Put: %v", err)
	}
	placementKeys, err := probe.List(ctx, "")
	if err != nil {
		t.Fatalf("probe List: %v", err)
	}
	if len(placementKeys) != 1 {
		t.Fatalf("the placement store wrote %d keys, want 1: %v", len(placementKeys), placementKeys)
	}

	covered := false
	for _, p := range service.TenantKeyPrefixes("acme") {
		if strings.HasPrefix(placementKeys[0], p) {
			covered = true
			break
		}
	}
	if !covered {
		t.Fatalf("no tenant prefix covers the placement key %q; the reclamation "+
			"would leave a deleted raft tenant's shards running", placementKeys[0])
	}

	// End to end: the same key, in the store the reclamation runs against.
	f := newTenantFixture(t, true)
	if _, err := f.tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := f.root.Put(ctx, placementKeys[0], []byte("{}")); err != nil {
		t.Fatalf("Put placement: %v", err)
	}
	if err := f.tenants.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if _, err := f.root.Get(ctx, placementKeys[0]); err == nil {
		t.Errorf("the deleted tenant's placement record %q survived", placementKeys[0])
	}
}

// TestTenantDelete_SystemTenantIsNotDeletable is the review finding on
// go-qrdb#413: the reserved-id rule was applied only on CREATE, so the id
// this system declares un-creatable was freely DELETABLE. DELETE
// /v1/tenants/system ran the irreversible teardown -- shred the DEK, write
// the tombstone, remove the metadata record -- and reported success.
//
// The reclamation already refused the system tenant, so no data was lost; the
// damage was to the control plane, and it was permanent. This drives the
// fully-wired composition and asserts the system tenant's keyspace, its
// metadata and its creatability all survive.
func TestTenantDelete_SystemTenantIsNotDeletable(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	// The system tenant's own records: an identity record and a key in the
	// bootstrap database. Both live under "system/", which is precisely the
	// range a tenant reclamation would have walked.
	bootKey := tenant.ComposeNamespace(tenantid.System, "system") + "boot"
	identityKey := tenant.IdentityNamespace(tenantid.System) + "user/root"
	for _, k := range []string{bootKey, identityKey} {
		if err := f.root.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}

	var reserved *tenant.ReservedTenantIDError
	if err := f.tenants.DeleteTenant(ctx, tenantid.System); !errors.As(err, &reserved) {
		t.Fatalf("DeleteTenant(system) = %v, want a ReservedTenantIDError", err)
	}

	for _, k := range []string{bootKey, identityKey} {
		if _, err := f.root.Get(ctx, k); err != nil {
			t.Errorf("the system tenant's key %q was reclaimed: %v", k, err)
		}
	}
	// And the system tenant is still SERVABLE. That is the operator-visible
	// consequence of retiring its id, and it is the half only this layer can
	// see; that no tombstone was written is asserted in pkg/tenant, where the
	// key formula lives and cannot be re-derived by hand.
	kv, err := f.registry.KVService(tenantid.System, "system")
	if err != nil {
		t.Fatalf("the system tenant stopped resolving after a refused delete: %v", err)
	}
	if _, err := kv.Get(ctx, "boot"); err != nil {
		t.Errorf("the system tenant's bootstrap database stopped serving: %v", err)
	}
}

// TestTenantDelete_ReservedIdsAreNotDeletable covers the rest of the reserved
// set end to end, with the reclaimer and the database catalog both wired --
// the configuration where the bottom-up refusal catches "system" only by
// accident, because it happens to own the default database.
func TestTenantDelete_ReservedIdsAreNotDeletable(t *testing.T) {
	ctx := context.Background()
	f := newTenantFixture(t, true)

	for _, id := range []string{tenantid.System, "default", "_internal", "acme:eu"} {
		t.Run(id, func(t *testing.T) {
			var reserved *tenant.ReservedTenantIDError
			if err := f.tenants.DeleteTenant(ctx, id); !errors.As(err, &reserved) {
				t.Errorf("DeleteTenant(%q) = %v, want a ReservedTenantIDError", id, err)
			}
		})
	}
}
