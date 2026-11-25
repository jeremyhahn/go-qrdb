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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
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

// writeCheckingKV is the shared store's KV double run the way the real KV
// service runs a write: the context's write checks are run before the
// command is proposed (service.CheckWrite, from the propose path), so a
// refusal installed by the resolved view reaches the caller as the real
// service returns it.
type writeCheckingKV struct {
	*mockKVService
}

func (k writeCheckingKV) Put(ctx context.Context, key string, value []byte) error {
	if err := service.CheckWrite(ctx); err != nil {
		return err
	}
	return k.mockKVService.Put(ctx, key, value)
}

// TestRestRegistry_ResolvedHandleWriteRefusedAfterStrictTightening: a
// barrier tenant resolves its implicit default database under
// operator-access, the floor then tightens to strict, and a write through
// the already-resolved handle is refused with the strict refusal resolve
// returns, never reaching the node-keyed store. A floor that cannot be read
// at write time fails closed; a tenant that stays non-strict still writes.
func TestRestRegistry_ResolvedHandleWriteRefusedAfterStrictTightening(t *testing.T) {
	rr, shared := newStrictSharedRegistry(t, tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess, nil)
	rr.dbReg.Register("system", &DatabaseServices{Name: "system", KV: writeCheckingKV{shared}})
	kv, err := rr.KVService("bt", "system")
	if err != nil {
		t.Fatalf("KVService(operator-access): %v", err)
	}
	if err := kv.Put(context.Background(), "before", []byte("yes")); err != nil {
		t.Fatalf("Put before tightening: %v", err)
	}

	svc := rr.tenantSvc.(*floorTenantSvc)
	svc.floor = tenant.ClusterIsolationPolicyStrict
	err = kv.Put(context.Background(), "after", []byte("no"))
	var strictErr *rest.StrictTenantSharedStoreError
	if !errors.As(err, &strictErr) {
		t.Fatalf("Put after strict tightening: err = %v, want *rest.StrictTenantSharedStoreError", err)
	}
	if _, ok := shared.store["bt/system/after"]; ok {
		t.Fatal("the refused write reached the shared node-keyed store")
	}

	svc.floor = tenant.ClusterIsolationPolicyOperatorAccess
	svc.floorErr = errors.New("floor unavailable")
	if err := kv.Put(context.Background(), "unreadable", []byte("no")); err == nil {
		t.Fatal("a floor that cannot be read at write time must fail the write closed")
	}

	svc.floorErr = nil
	if err := kv.Put(context.Background(), "still", []byte("yes")); err != nil {
		t.Fatalf("Put by a non-strict tenant: %v", err)
	}
	if got := string(shared.store["bt/system/still"]); got != "yes" {
		t.Fatalf("shared store bt/system/still = %q, want yes", got)
	}
}

// holdFirstProposalHost is a LocalBackend that, when armed, holds the next
// proposal -- guarded or not -- after it was built and checked, until the
// test releases it.
type holdFirstProposalHost struct {
	*backend.LocalBackend

	mu   sync.Mutex
	gate *proposalGate
}

func (h *holdFirstProposalHost) holdNext() *proposalGate {
	g := &proposalGate{proposed: make(chan []byte, 1), release: make(chan struct{})}
	h.mu.Lock()
	h.gate = g
	h.mu.Unlock()
	return g
}

func (h *holdFirstProposalHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	h.mu.Lock()
	g := h.gate
	h.gate = nil
	h.mu.Unlock()
	if g != nil {
		g.proposed <- cmd
		<-g.release
	}
	return h.LocalBackend.SyncPropose(ctx, shardID, cmd)
}

func (h *holdFirstProposalHost) ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, _ writemode.Mode) (sm.Result, error) {
	return h.SyncPropose(ctx, shardID, cmd)
}

// epochTenantSvc is floorTenantSvc reading the isolation epochs from the
// system KV the shared store's writes go to, as the tenant service's system
// store does.
type epochTenantSvc struct {
	*floorTenantSvc
	system service.KVServicer
	// beforeRead, when set, runs once before the next epoch read.
	beforeRead func()
}

func (s *epochTenantSvc) IsolationEpochs(ctx context.Context, tenantID string) (map[string][]byte, error) {
	if hook := s.beforeRead; hook != nil {
		s.beforeRead = nil
		hook()
	}
	out := map[string][]byte{}
	for _, key := range tenant.IsolationEpochKeys(tenantID) {
		value, err := s.system.Get(ctx, key)
		var missing *service.KeyNotFoundError
		if errors.As(err, &missing) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

// TestRestRegistry_WriteInFlightAcrossStrictTighteningIsRefused: a
// shared-store write that passed the propose-time recheck and is in flight
// when a tightening to strict writes its isolation epoch and commits is
// refused at apply time, as the strict refusal, and stores nothing. A write
// in flight across an epoch write with no tightening to strict is refused
// by its guard alone and succeeds on its retry.
func TestRestRegistry_WriteInFlightAcrossStrictTighteningIsRefused(t *testing.T) {
	ctx := context.Background()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	lb := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = lb.Close() })
	host := &holdFirstProposalHost{LocalBackend: lb}
	raw := rawServices(host)

	rr, _ := newStrictSharedRegistry(t, tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess, nil)
	rr.dbReg.Register("system", raw)
	floor := rr.tenantSvc.(*floorTenantSvc)
	rr.tenantSvc = &epochTenantSvc{floorTenantSvc: floor, system: raw.KV}
	kv, err := rr.KVService("bt", "system")
	if err != nil {
		t.Fatalf("KVService(operator-access): %v", err)
	}
	floorEpoch := tenant.IsolationEpochKeys("bt")[1]

	inFlight := func(key string, during func()) error {
		gate := host.holdNext()
		errc := make(chan error, 1)
		go func() { errc <- kv.Put(ctx, key, []byte("v")) }()
		select {
		case <-gate.proposed:
		case err := <-errc:
			t.Fatalf("Put %s returned %v before it was proposed", key, err)
		}
		during()
		close(gate.release)
		return <-errc
	}

	err = inFlight("inflight", func() {
		if err := raw.KV.Put(ctx, floorEpoch, []byte("epoch-1")); err != nil {
			t.Fatalf("epoch write: %v", err)
		}
		floor.floor = tenant.ClusterIsolationPolicyStrict
	})
	var strictErr *rest.StrictTenantSharedStoreError
	if !errors.As(err, &strictErr) {
		t.Fatalf("write in flight across the tightening: err = %v, want *rest.StrictTenantSharedStoreError", err)
	}
	if _, err := raw.KV.Get(ctx, "bt/system/inflight"); err == nil {
		t.Fatal("the write in flight across the tightening reached the shared node-keyed store")
	}

	floor.floor = tenant.ClusterIsolationPolicyOperatorAccess
	err = inFlight("twin", func() {
		if err := raw.KV.Put(ctx, floorEpoch, []byte("epoch-2")); err != nil {
			t.Fatalf("epoch write: %v", err)
		}
	})
	var refused *service.WriteGuardRefusedError
	if !errors.As(err, &refused) || errors.As(err, &strictErr) {
		t.Fatalf("write in flight across a non-strict epoch write: err = %v, want the guard refusal", err)
	}
	if err := kv.Put(ctx, "twin", []byte("v")); err != nil {
		t.Fatalf("retry after the epoch write: %v", err)
	}
	if got, err := raw.KV.Get(ctx, "bt/system/twin"); err != nil || string(got) != "v" {
		t.Fatalf("bt/system/twin = (%q, %v), want v", got, err)
	}
}

// TestRestRegistry_WriteGuardRefusesTighteningBeforeEpochRead: a strict
// tightening that commits after the write's policy check but before its
// guard reads the isolation epoch refuses the write, and the shared store
// stays untouched.
func TestRestRegistry_WriteGuardRefusesTighteningBeforeEpochRead(t *testing.T) {
	ctx := context.Background()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	lb := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = lb.Close() })
	raw := rawServices(lb)

	rr, _ := newStrictSharedRegistry(t, tenant.TenantIsolationPolicyInherit, tenant.ClusterIsolationPolicyOperatorAccess, nil)
	rr.dbReg.Register("system", raw)
	floor := rr.tenantSvc.(*floorTenantSvc)
	epochs := &epochTenantSvc{floorTenantSvc: floor, system: raw.KV}
	rr.tenantSvc = epochs
	kv, err := rr.KVService("bt", "system")
	if err != nil {
		t.Fatalf("KVService(operator-access): %v", err)
	}
	floorEpoch := tenant.IsolationEpochKeys("bt")[1]

	epochs.beforeRead = func() {
		if err := raw.KV.Put(ctx, floorEpoch, []byte("epoch-1")); err != nil {
			t.Fatalf("epoch write: %v", err)
		}
		floor.floor = tenant.ClusterIsolationPolicyStrict
	}
	err = kv.Put(ctx, "late", []byte("v"))
	var strictErr *rest.StrictTenantSharedStoreError
	if !errors.As(err, &strictErr) {
		t.Fatalf("write across a tightening before the epoch read: err = %v, want *rest.StrictTenantSharedStoreError", err)
	}
	if _, err := raw.KV.Get(ctx, "bt/system/late"); err == nil {
		t.Fatal("the write across the tightening reached the shared node-keyed store")
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
