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

// Cross-tenant isolation of DISTRIBUTED LOCKS (go-qrdb#455).
//
// docs/distributed-locks/architecture.md states, under Trust boundaries and
// with status NORMATIVE: "Locks are scoped per (tenant, database)." A lock
// record is keyed "__sys:lock/{key}" with the key supplied by the caller and
// no tenant segment of its own, and every transport used to reach it through
// ONE process-wide LockServicer built over the raw, pre-namespace engines. So
// under prefix isolation -- where tenants share a state machine and are
// separated only by tenant.ComposeNamespace's "<tenant>/<database>/" -- two
// tenants locking the same name took the same lock, in all three of the
// directions the issue enumerates:
//
//  1. Denial. Tenant A holding "deploy" blocked tenant B's "deploy".
//  2. Disclosure. List enumerated the other tenant's lock keys, Status
//     returned a foreign holder record, and LockHeldError carried the foreign
//     HolderID -- a leak that survives the caller being correctly refused.
//  3. The fencing token guarded release, but a token is an int64 obtained
//     from a successful acquire: it bounds the exposure, it does not close it.
//
// These tests drive the REAL data plane -- an MVCC engine over in-memory
// storage, with a real lessor and watch hub, and the real lease, watch,
// transaction and MVCC servicers over it -- through the registry accessor, and
// assert the three consequences are gone under EVERY isolation type. volume
// and raft are expected to have been sound already; they are here so the fix
// is shown not to have narrowed to the mode that was broken.

package server

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

const (
	lockTTL      = 60 * time.Second
	lockTestDB   = "appdb"
	lockTenantA  = "tenant-a"
	lockTenantB  = "tenant-b"
	lockHolderA  = "tenant-a-worker-7"
	lockHolderB  = "tenant-b-worker-3"
	contendedKey = "deploy"
)

// ---------------------------------------------------------------------------
// Real data plane
// ---------------------------------------------------------------------------

// newLockDataPlane builds the full data-plane service set a lock needs --
// transaction, lease, watch and MVCC -- over ONE real MVCC engine backed by
// in-memory storage, with a real lessor and a real watch hub. It is the same
// binding lifecycle.BuildServices and buildDataPlaneServices perform, so a
// lock acquired through it takes the production path rather than a double's
// approximation of it. The storage is returned so a test can read the RAW
// keys the engine wrote and see the namespace for itself.
func newLockDataPlane(t *testing.T, name string) (storage.Storage, *DatabaseServices) {
	t.Helper()
	stg := memory.NewStorage()
	hub := watch.NewHub()
	eng, err := engine.NewMVCCEngine(stg,
		engine.WithMVCCLessor(lease.NewLessor()),
		engine.WithMVCCWatchHub(hub),
	)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })

	kv := service.NewKVService(host, shard.ShardKV)
	mvcc := service.NewMVCCKVService(host, shard.ShardKV)
	idx := service.NewIndexService(host, shard.ShardKV)
	txn := service.NewTransactionService(host, shard.ShardKV)
	return stg, &DatabaseServices{
		Name:        name,
		ShardID:     shard.ShardKV,
		KV:          kv,
		MVCC:        mvcc,
		Batch:       service.NewBatchService(host, shard.ShardKV),
		Transaction: txn,
		Lease:       service.NewLeaseService(host, shard.ShardKV),
		TimeSeries:  service.NewTimeSeriesService(host, shard.ShardKV),
		Index:       idx,
		Watch:       service.NewWatchService(hub),
		Collection: service.NewCollectionServiceWithIndex(kv, idx).
			WithSequenceService(service.NewSequenceService(mvcc, txn)),
	}
}

// ---------------------------------------------------------------------------
// One registry per isolation type
// ---------------------------------------------------------------------------

// lockScene is one isolation type's fixture: the registry two tenants resolve
// through, and a function returning every raw store key written under it. The
// key list is what makes "two tenants, one shared store" checkable rather than
// merely asserted -- under prefix both tenants' locks are in ONE list, under
// volume and raft they are in separate stores.
type lockScene struct {
	registry service.Registry
	rawKeys  func(t *testing.T) []string
}

// rawKeysOf returns every key in the given stores, sorted, for stable
// assertions.
func rawKeysOf(t *testing.T, stores ...storage.Storage) []string {
	t.Helper()
	var out []string
	for _, s := range stores {
		keys, err := s.List(nil)
		if err != nil {
			t.Fatalf("storage List: %v", err)
		}
		for _, k := range keys {
			out = append(out, string(k))
		}
	}
	sort.Strings(out)
	return out
}

// prefixLockScene is the exposed mode and the default: both tenants share one
// state machine and are separated only by the composite namespace.
func prefixLockScene(t *testing.T) *lockScene {
	t.Helper()
	stg, svcs := newLockDataPlane(t, lockTestDB)
	dbReg := NewDatabaseServiceRegistry(lockTestDB)
	dbReg.Register(lockTestDB, svcs)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationPrefix}),
	)
	return &lockScene{
		registry: reg,
		rawKeys:  func(t *testing.T) []string { return rawKeysOf(t, stg) },
	}
}

// volumeLockScene gives each tenant its own Host on its own store, which is
// what IsolationVolume buys. The per-tenant hosts are seeded directly because
// the property under test is the RESOLUTION -- that LockService routes to the
// tenant's own engines and then namespaces on top of them -- not the on-disk
// Host construction, which tenant_host_test.go covers.
func volumeLockScene(t *testing.T) *lockScene {
	t.Helper()
	stgA, svcsA := newLockDataPlane(t, lockTestDB)
	stgB, svcsB := newLockDataPlane(t, lockTestDB)

	hosts := NewTenantHostRegistry(&seal.BarrierRegistry{}, staticDataRoot{}, volTestLogger())
	hosts.hosts[lockTenantA] = &tenantHost{tenantID: lockTenantA, services: svcsA}
	hosts.hosts[lockTenantB] = &tenantHost{tenantID: lockTenantB, services: svcsB}

	dbReg := NewDatabaseServiceRegistry(lockTestDB)
	dbReg.Register(lockTestDB, svcsA)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationVolume}),
		withTenantHostRegistry(hosts),
	)
	return &lockScene{
		registry: reg,
		rawKeys:  func(t *testing.T) []string { return rawKeysOf(t, stgA, stgB) },
	}
}

// staticDataRoot satisfies tenantInfoSource for a registry whose per-tenant
// Hosts are pre-built, which is the state a running node is in once boot has
// re-attached them. It is never consulted, because the cache hit in Services
// returns first.
type staticDataRoot struct{}

func (staticDataRoot) dataRootOf(context.Context, string) (string, error) { return "", nil }

// staticRaftInfo satisfies raftDatabaseInfoSource for a factory whose shards
// are pre-attached, which is the state a running node is in once boot has
// re-attached them. Every method reports "no such database" because the cache
// hit in Services returns before any of them is consulted.
type staticRaftInfo struct{}

func (staticRaftInfo) databaseInfo(string, string, bool) (uint64, string, bool) { return 0, "", false }
func (staticRaftInfo) placementInfo(string, string) (*placement.Placement, bool, error) {
	return nil, false, nil
}
func (staticRaftInfo) dedicatedWAL(string, string) bool { return false }
func (staticRaftInfo) recordedIdentity(context.Context, string, string) (string, string, bool, error) {
	return "", "", false, nil
}

// raftLockScene gives each (tenant, database) its own dedicated shard, which
// is what IsolationRaft buys. No composite prefix is applied on this path --
// the shard IS the keyspace -- so the raw keys carry no tenant segment and
// isolation rests entirely on which store the servicer reaches.
func raftLockScene(t *testing.T) *lockScene {
	t.Helper()
	stgA, svcsA := newLockDataPlane(t, lockTestDB)
	stgB, svcsB := newLockDataPlane(t, lockTestDB)

	f := &RaftBackendFactory{
		host:       &quicraft.Host{},
		barrierReg: &seal.BarrierRegistry{},
		dbInfo:     staticRaftInfo{},
		logger:     volTestLogger(),
		shards:     make(map[raftShardKey]*raftShard),
	}
	f.shards[raftShardKey{tenantID: lockTenantA, database: lockTestDB}] = &raftShard{services: svcsA}
	f.shards[raftShardKey{tenantID: lockTenantB, database: lockTestDB}] = &raftShard{services: svcsB}

	dbReg := NewDatabaseServiceRegistry(lockTestDB)
	dbReg.Register(lockTestDB, svcsA)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationRaft}),
		withRaftBackendFactory(f),
	)
	return &lockScene{
		registry: reg,
		rawKeys:  func(t *testing.T) []string { return rawKeysOf(t, stgA, stgB) },
	}
}

// lockScenes is the isolation matrix every consequence below is asserted over.
var lockScenes = map[string]func(*testing.T) *lockScene{
	"prefix": prefixLockScene,
	"volume": volumeLockScene,
	"raft":   raftLockScene,
}

// lockSvcFor resolves a tenant's LockServicer or fails the test.
func lockSvcFor(t *testing.T, reg service.Registry, tenantID string) service.LockServicer {
	t.Helper()
	svc, err := reg.LockService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("LockService(%s): %v", tenantID, err)
	}
	if svc == nil {
		t.Fatalf("LockService(%s) returned a nil servicer", tenantID)
	}
	return svc
}

// ---------------------------------------------------------------------------
// Consequence 1 -- denial
// ---------------------------------------------------------------------------

// TestLockIsolation_HoldingANameDoesNotDenyAnotherTenant is the first
// consequence: tenant A holding "deploy" must not stop tenant B taking
// "deploy". Before the fix, B's TryLock returned lock.LockHeldError naming A's
// holder, and B's blocking Lock waited until A's TTL expired.
func TestLockIsolation_HoldingANameDoesNotDenyAnotherTenant(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()

			a := lockSvcFor(t, scene.registry, lockTenantA)
			b := lockSvcFor(t, scene.registry, lockTenantB)

			held, err := a.TryLock(ctx, contendedKey, lockHolderA, lockTTL, false)
			if err != nil {
				t.Fatalf("tenant-a TryLock: %v", err)
			}
			if held.HolderID != lockHolderA {
				t.Fatalf("tenant-a HolderID = %q, want %q", held.HolderID, lockHolderA)
			}

			// The whole claim: the same name is free for the other tenant.
			mine, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false)
			if err != nil {
				t.Fatalf("CROSS-TENANT DENIAL: tenant-b could not take %q while tenant-a held it: %v", contendedKey, err)
			}
			if mine.HolderID != lockHolderB {
				t.Fatalf("tenant-b HolderID = %q, want %q", mine.HolderID, lockHolderB)
			}

			// And a blocking acquire does not block either -- it is the same
			// CAS, so a free key returns on the first attempt rather than
			// waiting on a watch.
			blockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			other, err := b.Lock(blockCtx, "migrate", lockHolderB, lockTTL, false)
			if err != nil {
				t.Fatalf("tenant-b Lock(migrate): %v", err)
			}
			if other.Key != "migrate" {
				t.Fatalf("Key = %q, want %q", other.Key, "migrate")
			}
		})
	}
}

// TestLockIsolation_ContentionWithinATenantStillDenies is the other half of
// the same property, and the one that keeps the fix honest: isolation must not
// have been bought by making the lock stop locking. Two holders inside ONE
// tenant still contend.
func TestLockIsolation_ContentionWithinATenantStillDenies(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()
			b := lockSvcFor(t, scene.registry, lockTenantB)

			if _, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false); err != nil {
				t.Fatalf("first TryLock: %v", err)
			}
			_, err := b.TryLock(ctx, contendedKey, "tenant-b-worker-9", lockTTL, false)
			var held *lock.LockHeldError
			if !errors.As(err, &held) {
				t.Fatalf("second TryLock err = %v, want *lock.LockHeldError", err)
			}
			if held.HolderID != lockHolderB {
				t.Fatalf("HolderID = %q, want the tenant's own holder %q", held.HolderID, lockHolderB)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consequence 2 -- disclosure
// ---------------------------------------------------------------------------

// TestLockIsolation_ListDoesNotEnumerateForeignLocks asserts the List half of
// the disclosure: a prefix scan over the lock keyspace must return only the
// caller's own locks, whatever prefix is asked for -- including the empty one,
// which is "every lock".
func TestLockIsolation_ListDoesNotEnumerateForeignLocks(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()

			a := lockSvcFor(t, scene.registry, lockTenantA)
			b := lockSvcFor(t, scene.registry, lockTenantB)

			for _, key := range []string{"jobs/nightly-report", "jobs/acquisition-closing", "leader"} {
				if _, err := a.TryLock(ctx, key, lockHolderA, lockTTL, false); err != nil {
					t.Fatalf("tenant-a TryLock(%s): %v", key, err)
				}
			}
			if _, err := b.TryLock(ctx, "jobs/own-report", lockHolderB, lockTTL, false); err != nil {
				t.Fatalf("tenant-b TryLock: %v", err)
			}

			for _, prefix := range []string{"", "jobs/", "leader"} {
				locks, err := b.List(ctx, prefix)
				if err != nil {
					t.Fatalf("tenant-b List(%q): %v", prefix, err)
				}
				for _, l := range locks {
					if l.HolderID == lockHolderA {
						t.Fatalf("DISCLOSURE: List(%q) returned tenant-a's holder %q on key %q", prefix, l.HolderID, l.Key)
					}
					if l.Key == "leader" || l.Key == "jobs/nightly-report" || l.Key == "jobs/acquisition-closing" {
						t.Fatalf("DISCLOSURE: List(%q) enumerated tenant-a's lock name %q", prefix, l.Key)
					}
				}
			}

			// And the caller's own locks are still enumerable -- an empty list
			// would pass every assertion above while breaking the feature.
			own, err := b.List(ctx, "")
			if err != nil {
				t.Fatalf("tenant-b List: %v", err)
			}
			if len(own) != 1 || own[0].Key != "jobs/own-report" {
				t.Fatalf("tenant-b List = %v, want exactly its own jobs/own-report", own)
			}
		})
	}
}

// TestLockIsolation_StatusDoesNotReturnForeignHolder asserts the Status half:
// asking after a key another tenant holds must read as "not locked", not as
// the other tenant's holder record.
func TestLockIsolation_StatusDoesNotReturnForeignHolder(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()

			a := lockSvcFor(t, scene.registry, lockTenantA)
			b := lockSvcFor(t, scene.registry, lockTenantB)

			if _, err := a.TryLock(ctx, contendedKey, lockHolderA, lockTTL, false); err != nil {
				t.Fatalf("tenant-a TryLock: %v", err)
			}

			got, err := b.Status(ctx, contendedKey)
			if err != nil {
				t.Fatalf("tenant-b Status: %v", err)
			}
			if got != nil {
				t.Fatalf("DISCLOSURE: tenant-b Status(%q) returned a foreign record %+v, want nil", contendedKey, got)
			}

			// The caller's own Status still answers.
			if _, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false); err != nil {
				t.Fatalf("tenant-b TryLock: %v", err)
			}
			mine, err := b.Status(ctx, contendedKey)
			if err != nil {
				t.Fatalf("tenant-b Status: %v", err)
			}
			if mine == nil || mine.HolderID != lockHolderB {
				t.Fatalf("tenant-b Status = %+v, want its own holder %q", mine, lockHolderB)
			}
		})
	}
}

// TestLockIsolation_LockHeldErrorCarriesNoForeignHolder is the disclosure that
// survives a correct refusal, which is why it is asserted on the ERROR's
// contents rather than on the fact of refusal. Before the fix, tenant B's
// TryLock on a name tenant A held was refused -- correctly, from B's point of
// view -- with lock.LockHeldError{HolderID: <tenant A's holder>}. The refusal
// looked right and leaked anyway.
func TestLockIsolation_LockHeldErrorCarriesNoForeignHolder(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()

			a := lockSvcFor(t, scene.registry, lockTenantA)
			b := lockSvcFor(t, scene.registry, lockTenantB)

			if _, err := a.TryLock(ctx, contendedKey, lockHolderA, lockTTL, false); err != nil {
				t.Fatalf("tenant-a TryLock: %v", err)
			}

			// B takes the name for itself; there is nothing to be refused by.
			if _, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false); err != nil {
				t.Fatalf("tenant-b TryLock: %v", err)
			}

			// A second B holder IS refused -- and the error must name B's own
			// holder, never A's.
			_, err := b.TryLock(ctx, contendedKey, "tenant-b-worker-9", lockTTL, false)
			var held *lock.LockHeldError
			if !errors.As(err, &held) {
				t.Fatalf("err = %v, want *lock.LockHeldError", err)
			}
			if held.HolderID == lockHolderA {
				t.Fatalf("DISCLOSURE: LockHeldError carried tenant-a's HolderID %q", held.HolderID)
			}
			if held.HolderID != lockHolderB {
				t.Fatalf("HolderID = %q, want %q", held.HolderID, lockHolderB)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consequence 3 -- the fencing token
// ---------------------------------------------------------------------------

// TestLockIsolation_FencingTokenSemanticsUnchanged pins the token behaviour
// the issue says must not change: a stale or foreign token is refused with
// NotLockHolderError, the matching one releases, and successive acquisitions
// of the same lock produce strictly increasing tokens.
func TestLockIsolation_FencingTokenSemanticsUnchanged(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()
			b := lockSvcFor(t, scene.registry, lockTenantB)

			first, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false)
			if err != nil {
				t.Fatalf("TryLock: %v", err)
			}

			var notHolder *lock.NotLockHolderError
			if err := b.Unlock(ctx, contendedKey, first.FencingToken+1); !errors.As(err, &notHolder) {
				t.Fatalf("stale-token Unlock err = %v, want *lock.NotLockHolderError", err)
			}
			if _, err := b.Extend(ctx, contendedKey, first.FencingToken+1); !errors.As(err, &notHolder) {
				t.Fatalf("stale-token Extend err = %v, want *lock.NotLockHolderError", err)
			}
			if err := b.Unlock(ctx, contendedKey, first.FencingToken); err != nil {
				t.Fatalf("Unlock with the matching token: %v", err)
			}

			second, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false)
			if err != nil {
				t.Fatalf("re-acquire: %v", err)
			}
			if second.FencingToken <= first.FencingToken {
				t.Fatalf("FencingToken = %d, want strictly greater than %d", second.FencingToken, first.FencingToken)
			}
		})
	}
}

// TestLockIsolation_AForeignFencingTokenCannotReachAnotherTenantsLock states
// what the token is NOT: the thing isolation rests on. A fencing token is the
// MVCC revision of an acquisition IN ITS OWN KEYSPACE, so two tenants that
// acquire the same name in two namespaces routinely hold the SAME int64 -- and
// that is fine precisely because the keyspace, not the number, is what keeps
// them apart. What must hold is that a call made with another tenant's token
// cannot reach that tenant's lock at all.
func TestLockIsolation_AForeignFencingTokenCannotReachAnotherTenantsLock(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			ctx := context.Background()

			a := lockSvcFor(t, scene.registry, lockTenantA)
			b := lockSvcFor(t, scene.registry, lockTenantB)

			if _, err := a.TryLock(ctx, contendedKey, lockHolderA, lockTTL, false); err != nil {
				t.Fatalf("tenant-a TryLock: %v", err)
			}
			bLock, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false)
			if err != nil {
				t.Fatalf("tenant-b TryLock: %v", err)
			}

			// Tenant A releases and extends using tenant B's token. Whatever
			// this does to A's own lock, it must not touch B's.
			_ = a.Unlock(ctx, contendedKey, bLock.FencingToken)
			_, _ = a.Extend(ctx, contendedKey, bLock.FencingToken)

			still, err := b.Status(ctx, contendedKey)
			if err != nil {
				t.Fatalf("tenant-b Status: %v", err)
			}
			if still == nil {
				t.Fatal("CROSS-TENANT RELEASE: tenant-a's call released tenant-b's lock")
			}
			if still.HolderID != lockHolderB {
				t.Fatalf("tenant-b lock holder = %q, want %q", still.HolderID, lockHolderB)
			}
			if err := b.Unlock(ctx, contendedKey, bLock.FencingToken); err != nil {
				t.Fatalf("tenant-b Unlock with its own token: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The mechanism, read off the store
// ---------------------------------------------------------------------------

// TestLockPrefixIsolation_LockKeysCarryTheTenantNamespace is the direct
// evidence for the prefix mode: with both tenants on ONE store, the raw keys
// the engine wrote must carry each tenant's composite namespace. Before the
// fix there was exactly one "__sys:lock/deploy" in this store and both tenants
// addressed it.
func TestLockPrefixIsolation_LockKeysCarryTheTenantNamespace(t *testing.T) {
	scene := prefixLockScene(t)
	ctx := context.Background()

	a := lockSvcFor(t, scene.registry, lockTenantA)
	b := lockSvcFor(t, scene.registry, lockTenantB)
	if _, err := a.TryLock(ctx, contendedKey, lockHolderA, lockTTL, false); err != nil {
		t.Fatalf("tenant-a TryLock: %v", err)
	}
	if _, err := b.TryLock(ctx, contendedKey, lockHolderB, lockTTL, false); err != nil {
		t.Fatalf("tenant-b TryLock: %v", err)
	}

	wantA := tenant.ComposeNamespace(lockTenantA, lockTestDB) + "__sys:lock/" + contendedKey
	wantB := tenant.ComposeNamespace(lockTenantB, lockTestDB) + "__sys:lock/" + contendedKey
	var sawA, sawB, sawBare bool
	for _, k := range scene.rawKeys(t) {
		raw := []byte(k)
		switch {
		case bytes.Contains(raw, []byte(wantA)):
			sawA = true
		case bytes.Contains(raw, []byte(wantB)):
			sawB = true
		case bytes.Contains(raw, []byte("__sys:lock/")):
			sawBare = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("namespaced lock keys missing: sawA=%v sawB=%v", sawA, sawB)
	}
	if sawBare {
		t.Fatal("a lock key was written OUTSIDE any tenant namespace -- that is the shared __sys:lock/ space #455 is about")
	}
}

// TestLockRaftIsolation_KeysAreSeparatedByStoreNotByPrefix records the
// difference between the modes, so a later reader does not conclude the
// namespace is the only mechanism. A dedicated-raft database's shard IS the
// keyspace, so its lock keys carry no tenant segment and must not: adding one
// there would be the duplicate-encoding the multi-tenancy design rejects.
func TestLockRaftIsolation_KeysAreSeparatedByStoreNotByPrefix(t *testing.T) {
	scene := raftLockScene(t)
	ctx := context.Background()

	a := lockSvcFor(t, scene.registry, lockTenantA)
	if _, err := a.TryLock(ctx, contendedKey, lockHolderA, lockTTL, false); err != nil {
		t.Fatalf("tenant-a TryLock: %v", err)
	}

	var sawBare bool
	for _, k := range scene.rawKeys(t) {
		if bytes.Contains([]byte(k), []byte("__sys:lock/"+contendedKey)) {
			sawBare = true
			if bytes.Contains([]byte(k), []byte(lockTenantA+"/")) {
				t.Fatalf("dedicated-raft lock key carries a redundant tenant segment: %q", k)
			}
		}
	}
	if !sawBare {
		t.Fatal("no lock key was written to the tenant's dedicated shard store")
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// TestLockService_RefusesWhenTheScopeHasNoLockEngines asserts the accessor
// fails closed on a database whose store cannot serve a lock. A dedicated
// non-raft store is a plain Backend with no lease, watch or MVCC engine, so a
// lock over it would have no expiry and no blocking acquisition. Refusing is
// the only correct answer.
func TestLockService_RefusesWhenTheScopeHasNoLockEngines(t *testing.T) {
	dbReg := NewDatabaseServiceRegistry(lockTestDB)
	dbReg.Register(lockTestDB, &DatabaseServices{Name: lockTestDB, KV: newMockKVService()})
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationPrefix}),
	)

	svc, err := reg.LockService(lockTenantA, lockTestDB)
	if svc != nil {
		t.Fatal("a scope with no lease/watch/MVCC engines must not resolve a lock servicer")
	}
	var cue *service.CapabilityUnsupportedError
	if !errors.As(err, &cue) {
		t.Fatalf("err = %v, want *service.CapabilityUnsupportedError", err)
	}
	if !strings.HasPrefix(cue.Message, "lock: ") {
		t.Fatalf("message %q must name the refused service lock", cue.Message)
	}
}

// TestLockService_PropagatesTenantAndDatabaseResolutionErrors asserts the
// accessor refuses for the same reasons its thirteen siblings do rather than
// resolving a lock for a tenant or database that does not exist.
func TestLockService_PropagatesTenantAndDatabaseResolutionErrors(t *testing.T) {
	_, svcs := newLockDataPlane(t, lockTestDB)
	dbReg := NewDatabaseServiceRegistry(lockTestDB)
	dbReg.Register(lockTestDB, svcs)

	t.Run("unknown_database", func(t *testing.T) {
		reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
			WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationPrefix}),
		)
		if _, err := reg.LockService(lockTenantA, "nope"); err == nil {
			t.Fatal("LockService resolved an unknown database")
		} else {
			var dnf *rest.DatabaseNotFoundError
			if !errors.As(err, &dnf) {
				t.Fatalf("err = %v, want *rest.DatabaseNotFoundError", err)
			}
		}
	})

	t.Run("isolation_backend_unwired", func(t *testing.T) {
		reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
			WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationVolume}),
		)
		_, err := reg.LockService(lockTenantA, lockTestDB)
		var ibu *rest.IsolationBackendUnavailableError
		if !errors.As(err, &ibu) {
			t.Fatalf("err = %v, want *rest.IsolationBackendUnavailableError", err)
		}
	})
}
