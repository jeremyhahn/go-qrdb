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

// Package seal — internal coverage tests for pool-resolution helpers and
// ExportSealedBlob / loadTenantRoot edge branches not reachable through the
// public API alone.
package seal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// poolInternalKV is a concurrency-safe in-memory KV that implements
// qrdbpool.ReplicatedKV. It serves as a lightweight cursor store for the
// round-robin strategy used in these internal pool tests.
type poolInternalKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newPoolInternalKV() *poolInternalKV {
	return &poolInternalKV{data: make(map[string][]byte)}
}

func (k *poolInternalKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.data[key]; ok {
		cp := make([]byte, len(v))
		copy(cp, v)
		return cp, nil
	}
	return nil, nil
}

func (k *poolInternalKV) Put(_ context.Context, key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	k.data[key] = cp
	return nil
}

// buildPoolRegistry creates a BarrierRegistry wired with a 2-candidate local
// pool ("default") and, when slotStore is non-nil, attaches it as the
// replicated slot-assignment store. Returns the registry and the two
// local-backend root paths (index 0 = first candidate, index 1 = second).
func buildPoolRegistry(t *testing.T, slotStore StorageBackend) (*BarrierRegistry, []string) {
	t.Helper()

	dir1, dir2 := t.TempDir(), t.TempDir()
	c1, c2 := local.New(), local.New()
	if err := c1.Configure(map[string]string{"path": dir1}); err != nil {
		t.Fatalf("configure c1: %v", err)
	}
	if err := c2.Configure(map[string]string{"path": dir2}); err != nil {
		t.Fatalf("configure c2: %v", err)
	}

	kv := newPoolInternalKV()
	state, err := qrdbpool.NewBoundReplicatedStateStore(kv)
	if err != nil {
		t.Fatalf("NewBoundReplicatedStateStore: %v", err)
	}
	rr := &objpool.RoundRobinStrategy{State: state}
	mgr := objpool.NewManager()
	if err := mgr.Register("default", []common.Storage{c1, c2}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	sys, err := NewStorageBarrier(testLogger(), NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "sys/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	opts := []Option{
		WithVolumePool(mgr, state, "default"),
		WithPerTenantRegistry(perTenantReg),
	}
	if slotStore != nil {
		opts = append(opts, WithReplicatedSlotStore(slotStore))
	}
	reg, err := NewBarrierRegistry(testLogger(), sys, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, []string{dir1, dir2}
}

// TestSlotForBase_WrongPoolName exercises the pool-name-mismatch branch inside
// slotForBase: when the pool manager has a "default" pool but the caller
// searches for a pool whose name does not match, the loop body's continue is
// taken and slotForBase returns (0, false).
//
// Covers: barrier_registry.go line 463 (continue on name mismatch).
func TestSlotForBase_WrongPoolName(t *testing.T) {
	t.Parallel()
	reg, roots := buildPoolRegistry(t, nil)

	slot, ok := reg.slotForBase(context.Background(), "nonexistent-pool", roots[0])
	if ok {
		t.Fatalf("slotForBase with wrong pool name: expected ok=false, got slot=%d", slot)
	}
}

// TestLocalBaseForSlot_WrongPoolName exercises two complementary branches:
//
//  1. The continue inside the range loop when the current pool's name does not
//     match the requested pool name (line 488).
//  2. The ErrPoolSlotUnresolved sentinel returned at the end of the function
//     when no matching pool name was found in the entire list (line 500).
func TestLocalBaseForSlot_WrongPoolName(t *testing.T) {
	t.Parallel()
	reg, _ := buildPoolRegistry(t, nil)

	_, err := reg.localBaseForSlot(context.Background(), "nonexistent-pool", 0)
	if !errors.Is(err, ErrPoolSlotUnresolved) {
		t.Fatalf("localBaseForSlot wrong pool: want ErrPoolSlotUnresolved, got %v", err)
	}
}

// TestRecordSlotAssignment_NoSlotStore calls recordSlotAssignment directly on
// a registry that has a pool but NO replicated slot store. The function must
// return the picked base unchanged via the early-return guard at the top.
//
// Covers: barrier_registry.go line 389-391 (nil slotStore early return body).
func TestRecordSlotAssignment_NoSlotStore(t *testing.T) {
	t.Parallel()
	// Build a registry with pool but without WithReplicatedSlotStore.
	reg, roots := buildPoolRegistry(t, nil)

	const tenantID = "no-slot-store-tenant"
	result := reg.recordSlotAssignment(context.Background(), tenantID, "default", roots[0])
	if result != roots[0] {
		t.Fatalf("recordSlotAssignment (no slotStore): want %q, got %q", roots[0], result)
	}
}

// TestRecordSlotAssignment_RaceLoseUnresolvablePool simulates the race-loser
// path in recordSlotAssignment: a concurrent writer already recorded a slot
// assignment for the tenant (PutIfAbsent reports stored=false), but the
// recorded assignment references a pool that does not exist on this node. The
// function must fall back to the picked base rather than returning an error.
//
// Covers: barrier_registry.go line 430 (return pickedBase when localBaseForSlot
// fails after losing the assignment race).
func TestRecordSlotAssignment_RaceLoseUnresolvablePool(t *testing.T) {
	t.Parallel()
	slotStore := NewMemoryBackend()
	reg, roots := buildPoolRegistry(t, slotStore)
	ctx := context.Background()

	// Pre-seed the slot store with an assignment referencing a pool that does
	// not exist on this node. When recordSlotAssignment loses the PutIfAbsent
	// race it will load this record, call localBaseForSlot("ghost-pool", 0),
	// receive ErrPoolSlotUnresolved, and fall back to the picked base.
	ghostAssignment, err := json.Marshal(slotAssignment{Pool: "ghost-pool", Slot: 0})
	if err != nil {
		t.Fatalf("json.Marshal ghost assignment: %v", err)
	}
	if err := slotStore.Put(ctx, slotAssignmentPrefix+"race-tenant", ghostAssignment); err != nil {
		t.Fatalf("pre-seed slot store: %v", err)
	}

	// recordSlotAssignment picks roots[0] (the validBase). It finds a match in
	// the pool for that path (slot 0), tries PutIfAbsent → stored=false (already
	// written), reads back the ghost-pool record, fails to resolve it locally,
	// and returns pickedBase (roots[0]).
	got := reg.recordSlotAssignment(ctx, "race-tenant", "default", roots[0])
	if got != roots[0] {
		t.Fatalf("recordSlotAssignment race-loser: want %q, got %q", roots[0], got)
	}
}

// TestExportSealedBlob_GetErrorPassthrough verifies that ExportSealedBlob
// propagates a storage-layer Get error that is NOT ErrSecretNotFound. When
// the underlying backend fails with an unexpected error the function must
// return (nil, err) immediately.
//
// Covers: import_key.go line 222 (return nil, err for non-ErrSecretNotFound).
func TestExportSealedBlob_GetErrorPassthrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Use a backend whose Get returns a non-ErrSecretNotFound error. ErrSealed
	// is a sentinel from this package and is guaranteed != ErrSecretNotFound.
	backend := &errorBackend{getErr: ErrSealed}
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{RootKeyPath: "root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	_, gotErr := b.ExportSealedBlob(ctx)
	if gotErr == nil {
		t.Fatal("ExportSealedBlob: expected error, got nil")
	}
	if errors.Is(gotErr, ErrNotInitialized) {
		t.Fatalf("ExportSealedBlob: got ErrNotInitialized, want the passthrough storage error")
	}
}

// TestExportSealedBlob_NilBlobReturnsNotInitialized exercises the defensive nil
// guard after a successful Get: when the backend returns (nil, nil) — a valid
// "found but empty" response from some implementations — ExportSealedBlob must
// treat the barrier as uninitialized and return ErrNotInitialized.
//
// Covers: import_key.go lines 224-226 (if blob == nil branch).
func TestExportSealedBlob_NilBlobReturnsNotInitialized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// errorBackend with no getErr returns (nil, nil) from Get.
	backend := &errorBackend{}
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{RootKeyPath: "root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	_, gotErr := b.ExportSealedBlob(ctx)
	if !errors.Is(gotErr, ErrNotInitialized) {
		t.Fatalf("ExportSealedBlob nil blob: want ErrNotInitialized, got %v", gotErr)
	}
}

// TestLoadTenantRoot_CorruptJSON verifies that loadTenantRoot handles a
// corrupt (unparseable) JSON payload in the system barrier gracefully: it
// treats the entry as not-found rather than returning an error, allowing a
// fresh registration to overwrite the corrupt record.
//
// Covers: barrier_registry.go lines 623-626 (json.Unmarshal failure → nil, false, nil).
func TestLoadTenantRoot_CorruptJSON(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := NewMemoryBackend()
	sys, err := NewStorageBarrier(testLogger(), store,
		BarrierConfig{RootKeyPath: "sys/root"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	// Initialize so the system barrier is unsealed (loadTenantRoot bails out
	// with (nil, false, nil) when the barrier is still sealed).
	if err := sys.Initialize(ctx, MustCredentials([]byte("test-password"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Write a corrupt JSON payload under the tenant root key. StorageBarrier.Put
	// encrypts it; loadTenantRoot decrypts and then tries json.Unmarshal → fail.
	if err := sys.Put(ctx, tenantRootStorePrefix+"corrupt-t", []byte(`}{not valid json`)); err != nil {
		t.Fatalf("sys.Put corrupt record: %v", err)
	}

	reg, err := NewBarrierRegistry(testLogger(), sys)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	rec, found, loadErr := reg.loadTenantRoot(ctx, "corrupt-t")
	if loadErr != nil {
		t.Fatalf("loadTenantRoot corrupt JSON: expected nil error, got %v", loadErr)
	}
	if found {
		t.Fatalf("loadTenantRoot corrupt JSON: expected found=false, got true")
	}
	if rec != nil {
		t.Fatalf("loadTenantRoot corrupt JSON: expected nil record, got %v", rec)
	}
}
