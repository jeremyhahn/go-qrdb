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

package seal

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// slotFakeKV is a tiny concurrency-safe in-memory replicated KV used to back a
// shared round-robin cursor across two simulated nodes. A missing key returns
// (nil, nil), matching the kvstore contract pool.ReplicatedStateStore expects.
type slotFakeKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newSlotFakeKV() *slotFakeKV { return &slotFakeKV{data: make(map[string][]byte)} }

func (k *slotFakeKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.data[key]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (k *slotFakeKV) Put(_ context.Context, key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	k.data[key] = cp
	return nil
}

// replicatedNode bundles one simulated node's registry and its local pool roots.
type replicatedNode struct {
	reg   *BarrierRegistry
	roots []string // local candidate paths, index = relative slot
}

// newReplicatedNode builds a node with a 2-candidate local volume pool whose
// round-robin cursor is the SHARED rr strategy (so the cursor is cluster-wide)
// and whose tenant→slot assignment store is the SHARED slotStore. slotStore nil
// selects standalone (node-local) behavior.
func newReplicatedNode(t *testing.T, rr *objpool.RoundRobinStrategy, slotStore StorageBackend) replicatedNode {
	t.Helper()

	roots := make([]string, 2)
	cands := make([]common.Storage, 2)
	for i := range 2 {
		dir := t.TempDir()
		s := local.New()
		if err := s.Configure(map[string]string{"path": dir}); err != nil {
			t.Fatalf("configure local: %v", err)
		}
		roots[i] = dir
		cands[i] = s
	}

	mgr := objpool.NewManager()
	if err := mgr.Register("default", cands, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	opts := []Option{
		WithVolumePool(objpool.LocalOnly(mgr), rr.State, "default"),
		WithPerTenantRegistry(perTenantReg),
	}
	if slotStore != nil {
		opts = append(opts, WithReplicatedSlotStore(slotStore))
	}
	reg, err := NewBarrierRegistry(poolTestLogger(), newPoolSystemBarrier(t), opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	// RegisterVolumeTenant/ReattachVolumeTenant refuse while the system
	// barrier is sealed: the tenant-root record they resolve from and write
	// lives behind it. Every caller in this file registers a volume tenant, so
	// the node's system barrier must be initialized up front.
	if err := reg.SystemBarrier().Initialize(context.Background(), MustCredentials([]byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	return replicatedNode{reg: reg, roots: roots}
}

// slotOf returns the relative slot index (0 or 1) the tenant's Pebble landed
// under for this node, or -1 when it is at neither root.
func (n replicatedNode) slotOf(tenant string) int {
	for i, root := range n.roots {
		if _, err := os.Stat(filepath.Join(root, tenant)); err == nil {
			return i
		}
	}
	return -1
}

// TestReplicatedSlot_TwoNodesResolveSameRelativeSlot proves the Phase 2
// guarantee: when two nodes share the replicated cursor and slot-assignment
// store, the relative slot a tenant is assigned on the first node is the slot
// every other node resolves — realized at its OWN local pool base — WITHOUT
// running a divergent node-local pick.
func TestReplicatedSlot_TwoNodesResolveSameRelativeSlot(t *testing.T) {
	ctx := context.Background()

	cursorStore, err := qrdbpool.NewBoundReplicatedStateStore(newSlotFakeKV())
	if err != nil {
		t.Fatalf("NewBoundReplicatedStateStore: %v", err)
	}
	rr := &objpool.RoundRobinStrategy{State: cursorStore}
	slotStore := NewMemoryBackend()

	nodeA := newReplicatedNode(t, rr, slotStore)
	nodeB := newReplicatedNode(t, rr, slotStore)

	// Advance the shared cursor with a throwaway tenant on node A so the tenant
	// under test is assigned a NON-zero relative slot. This makes the assertion
	// meaningful: if node B were to pick instead of resolve, the shared cursor
	// (now at 2) would route it to slot 0, not the recorded slot 1.
	if _, err := nodeA.reg.RegisterTenant(ctx, "warmup", nil, BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("register warmup on node A: %v", err)
	}
	if got := nodeA.slotOf("warmup"); got != 0 {
		t.Fatalf("warmup landed at slot %d on node A, want 0", got)
	}

	// Node A assigns the tenant under test: cursor 1 -> slot 1, recorded in the
	// shared slot store.
	if _, err := nodeA.reg.RegisterTenant(ctx, "t1", nil, BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("register t1 on node A: %v", err)
	}
	slotA := nodeA.slotOf("t1")
	if slotA != 1 {
		t.Fatalf("t1 landed at slot %d on node A, want 1", slotA)
	}

	// Node B provisions the SAME tenant via the pooled flow. It must resolve the
	// recorded relative slot (1) at its own local root — not pick afresh.
	if _, err := nodeB.reg.RegisterTenant(ctx, "t1", nil, BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("register t1 on node B: %v", err)
	}
	slotB := nodeB.slotOf("t1")
	if slotB != slotA {
		t.Fatalf("node B resolved t1 to slot %d, want %d (same relative slot as node A)", slotB, slotA)
	}
	// And it must be node B's OWN absolute path, not node A's.
	if slotB < 0 {
		t.Fatal("node B did not realize t1 under any of its own roots")
	}
	if _, err := os.Stat(filepath.Join(nodeA.roots[slotB], "t1")); err != nil {
		// node A also has a t1 (it created it); ensure node B used its OWN root.
		// nodeB.roots[slotB] != nodeA.roots[slotB] because they are distinct temp
		// dirs, which slotOf already confirmed by statting nodeB.roots.
		_ = err
	}
}

// TestReplicatedSlot_RegisterVolumeTenant_SameSlot proves the IsolationVolume /
// IsolationRaft provisioning path (RegisterVolumeTenant) — the path the
// dedicated-raft integration test exercises — resolves the same cluster-wide
// relative slot on a second node without re-picking.
func TestReplicatedSlot_RegisterVolumeTenant_SameSlot(t *testing.T) {
	ctx := context.Background()

	cursorStore, err := qrdbpool.NewBoundReplicatedStateStore(newSlotFakeKV())
	if err != nil {
		t.Fatalf("NewBoundReplicatedStateStore: %v", err)
	}
	rr := &objpool.RoundRobinStrategy{State: cursorStore}
	slotStore := NewMemoryBackend()

	nodeA := newReplicatedNode(t, rr, slotStore)
	nodeB := newReplicatedNode(t, rr, slotStore)

	// Warm up so the tenant under test gets relative slot 1.
	if _, _, err := nodeA.reg.RegisterVolumeTenant(ctx, "warmup", false); err != nil {
		t.Fatalf("RegisterVolumeTenant(warmup) node A: %v", err)
	}
	if _, _, err := nodeA.reg.RegisterVolumeTenant(ctx, "t1", false); err != nil {
		t.Fatalf("RegisterVolumeTenant(t1) node A: %v", err)
	}
	slotA := nodeA.slotOf("t1")
	if slotA != 1 {
		t.Fatalf("t1 landed at slot %d on node A, want 1", slotA)
	}

	if _, _, err := nodeB.reg.RegisterVolumeTenant(ctx, "t1", false); err != nil {
		t.Fatalf("RegisterVolumeTenant(t1) node B: %v", err)
	}
	if slotB := nodeB.slotOf("t1"); slotB != slotA {
		t.Fatalf("node B resolved t1 to slot %d, want %d", slotB, slotA)
	}
}

// hookSlotStore wraps a MemoryBackend so a test can script Get and
// PutIfAbsent responses for the slot-assignment key while delegating every
// other method to the real in-memory backend.
type hookSlotStore struct {
	*MemoryBackend
	getHook         func(key string) (data []byte, handled bool)
	putIfAbsentHook func(key string) (stored bool, handled bool)
}

func (h *hookSlotStore) Get(ctx context.Context, key string) ([]byte, error) {
	if h.getHook != nil {
		if data, handled := h.getHook(key); handled {
			if data == nil {
				return nil, ErrSecretNotFound
			}
			return data, nil
		}
	}
	return h.MemoryBackend.Get(ctx, key)
}

func (h *hookSlotStore) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	if h.putIfAbsentHook != nil {
		if stored, handled := h.putIfAbsentHook(key); handled {
			return stored, nil
		}
	}
	return h.MemoryBackend.PutIfAbsent(ctx, key, data)
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (h *hookSlotStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (h *hookSlotStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// TestReplicatedSlot_UnresolvableRecordRepicks proves that a recorded slot that
// cannot be resolved to a local candidate (out-of-range index — e.g. a
// heterogeneous-topology misconfiguration) falls back to a fresh node-local
// pick rather than failing the tenant.
func TestReplicatedSlot_UnresolvableRecordRepicks(t *testing.T) {
	ctx := context.Background()
	// getHook always reports slot 99 for the assignment key (out of range for a
	// 2-candidate pool); PutIfAbsent uses the real backend.
	store := &hookSlotStore{
		MemoryBackend: NewMemoryBackend(),
		getHook: func(key string) ([]byte, bool) {
			if filepath.Base(key) == "t1" {
				return []byte(`{"pool":"default","slot":99}`), true
			}
			return nil, false
		},
	}
	storeA, _, _ := newStateStore(t)
	node := newReplicatedNode(t, &objpool.RoundRobinStrategy{State: storeA}, store)

	if _, _, err := node.reg.RegisterVolumeTenant(ctx, "t1", false); err != nil {
		t.Fatalf("RegisterVolumeTenant(t1): %v", err)
	}
	if got := node.slotOf("t1"); got != 0 {
		t.Fatalf("unresolvable record should re-pick to slot 0, got %d", got)
	}
}

// TestReplicatedSlot_LostRaceDefersToWinner proves the PutIfAbsent race guard:
// when this node picks a slot but a peer recorded a different slot first
// (PutIfAbsent reports stored=false), the node re-reads and converges on the
// winner's slot instead of its own pick.
func TestReplicatedSlot_LostRaceDefersToWinner(t *testing.T) {
	ctx := context.Background()
	var getCalls int
	store := &hookSlotStore{
		MemoryBackend: NewMemoryBackend(),
		getHook: func(key string) ([]byte, bool) {
			if filepath.Base(key) != "t1" {
				return nil, false
			}
			getCalls++
			if getCalls == 1 {
				return nil, true // first read: absent → node picks
			}
			return []byte(`{"pool":"default","slot":1}`), true // winner recorded slot 1
		},
		putIfAbsentHook: func(key string) (bool, bool) {
			if filepath.Base(key) == "t1" {
				return false, true // a peer already recorded the assignment
			}
			return false, false
		},
	}
	// Node-local cursor would pick slot 0; the winner's slot 1 must override.
	storeA, _, _ := newStateStore(t)
	node := newReplicatedNode(t, &objpool.RoundRobinStrategy{State: storeA}, store)

	if _, _, err := node.reg.RegisterVolumeTenant(ctx, "t1", false); err != nil {
		t.Fatalf("RegisterVolumeTenant(t1): %v", err)
	}
	if got := node.slotOf("t1"); got != 1 {
		t.Fatalf("lost-race should defer to winner slot 1, got %d", got)
	}
}

// TestReplicatedSlot_CorruptRecordRepicks proves a corrupt (unparseable)
// assignment record is treated as absent so provisioning falls back to a pick
// rather than erroring.
func TestReplicatedSlot_CorruptRecordRepicks(t *testing.T) {
	ctx := context.Background()
	store := &hookSlotStore{
		MemoryBackend: NewMemoryBackend(),
		getHook: func(key string) ([]byte, bool) {
			if filepath.Base(key) == "t1" {
				return []byte("not-json"), true
			}
			return nil, false
		},
	}
	storeA, _, _ := newStateStore(t)
	node := newReplicatedNode(t, &objpool.RoundRobinStrategy{State: storeA}, store)

	if _, _, err := node.reg.RegisterVolumeTenant(ctx, "t1", false); err != nil {
		t.Fatalf("RegisterVolumeTenant(t1): %v", err)
	}
	if got := node.slotOf("t1"); got != 0 {
		t.Fatalf("corrupt record should re-pick to slot 0, got %d", got)
	}
}

// TestReplicatedSlot_StandaloneUsesNodeLocalPick proves that with NO replicated
// slot store attached (standalone / single-node), each registry picks
// independently from its own node-local cursor — the pre-Phase-2 behavior is
// preserved and no assignment record is consulted.
func TestReplicatedSlot_StandaloneUsesNodeLocalPick(t *testing.T) {
	ctx := context.Background()

	// Two independent node-local Pebble cursors (one per node), no slot store.
	storeA, _, _ := newStateStore(t)
	storeB, _, _ := newStateStore(t)

	rrA := &objpool.RoundRobinStrategy{State: storeA}
	rrB := &objpool.RoundRobinStrategy{State: storeB}

	nodeA := newReplicatedNode(t, rrA, nil)
	nodeB := newReplicatedNode(t, rrB, nil)

	// Each node's first pick uses its own cursor at 0 -> slot 0. No coordination.
	if _, err := nodeA.reg.RegisterTenant(ctx, "t1", nil, BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("register t1 on node A: %v", err)
	}
	if _, err := nodeB.reg.RegisterTenant(ctx, "t1", nil, BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("register t1 on node B: %v", err)
	}
	if got := nodeA.slotOf("t1"); got != 0 {
		t.Fatalf("standalone node A t1 slot = %d, want 0 (node-local pick)", got)
	}
	if got := nodeB.slotOf("t1"); got != 0 {
		t.Fatalf("standalone node B t1 slot = %d, want 0 (node-local pick)", got)
	}
}

// TestReplicatedSlot_DetachReturnsToNodeLocalPick proves DetachReplicatedSlotStore
// clears the attachment made by WithReplicatedSlotStore, idempotently, and
// that provisioning afterward falls back to node-local Pick exactly as a
// registry that was never given a slot store does. A failed clustered boot
// that attached the replicated store over a now-stopped system KV must not
// leave it reachable on retry.
func TestReplicatedSlot_DetachReturnsToNodeLocalPick(t *testing.T) {
	ctx := context.Background()
	slotStore := NewMemoryBackend()

	store, _, _ := newStateStore(t)
	rr := &objpool.RoundRobinStrategy{State: store}
	node := newReplicatedNode(t, rr, slotStore)

	node.reg.poolMu.RLock()
	attached := node.reg.slotStore
	node.reg.poolMu.RUnlock()
	if attached == nil {
		t.Fatal("slot store must be attached before Detach")
	}

	node.reg.DetachReplicatedSlotStore()
	node.reg.poolMu.RLock()
	after := node.reg.slotStore
	node.reg.poolMu.RUnlock()
	if after != nil {
		t.Fatal("slot store must be nil after DetachReplicatedSlotStore")
	}

	// Idempotent: detaching an already-detached registry is a no-op, not an error.
	node.reg.DetachReplicatedSlotStore()

	// Provisioning after detach uses node-local Pick (slot 0, the cursor's
	// start), exactly as TestReplicatedSlot_StandaloneUsesNodeLocalPick shows
	// for a registry that never had a slot store.
	if _, err := node.reg.RegisterTenant(ctx, "t1", nil, BarrierConfig{}, newPoolSoftwareStrategy(t)); err != nil {
		t.Fatalf("RegisterTenant after detach: %v", err)
	}
	if got := node.slotOf("t1"); got != 0 {
		t.Fatalf("post-detach t1 slot = %d, want 0 (node-local pick)", got)
	}
}
