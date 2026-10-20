// Copyright 2026 Jeremy Hahn
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

package sealtest

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// PooledRegistry builds a *seal.BarrierRegistry wired with a single real
// "dir"-provider volume (rooted at a fresh t.TempDir(), pool name
// "default"), a lightweight in-memory volume.Catalog, and a real
// pkg/storage/pebble per-tenant registry -- the standard fixture every
// package's tests use to exercise RegisterBarrierTenant /
// RegisterVolumeTenant / ReattachVolumeTenant over system.
//
// The in-memory Catalog is backed by a non-replicated KV/Tx double: the
// thing under test in a consumer of this fixture is virtually never the
// Catalog's own CAS mechanics (pkg/volume's own tests cover those), so a
// faithful-enough single-node double is the right level of fidelity here.
//
// The returned volume.Allocator lets a caller Reserve a tenant's
// Assignment directly -- required before RegisterVolumeTenant /
// ReattachVolumeTenant, which no longer pick a path themselves (see
// seal.BarrierRegistry's doc comments) -- and the returned root is the
// pool's local directory, for assertions about where a tenant landed.
func PooledRegistry(tb testing.TB, system *seal.StorageBarrier, extra ...seal.Option) (reg *seal.BarrierRegistry, root string, allocator volume.Allocator) {
	tb.Helper()
	allocator, resolver, root := ShareableVolumeComponents(tb)

	perTenantReg := pebblestore.NewPerTenantRegistry()
	tb.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	opts := append([]seal.Option{
		seal.WithVolumeAllocator(allocator),
		seal.WithVolumeResolver(resolver),
		seal.WithPerTenantRegistry(perTenantReg),
	}, extra...)
	reg, err := seal.NewBarrierRegistry(slog.Default(), system, opts...)
	if err != nil {
		tb.Fatalf("sealtest: NewBarrierRegistry: %v", err)
	}
	return reg, root, allocator
}

// ShareableVolumeComponents opens a single real "dir"-provider volume
// (rooted at a fresh t.TempDir(), pool name "default") and builds an
// in-memory volume.Catalog over it, returning the volume.Allocator and
// seal.VolumeResolver so a caller can build MULTIPLE *seal.BarrierRegistry
// instances that share them -- simulating a daemon restart, where a fresh
// per-tenant Pebble registry is opened on each boot but the pool and
// catalog survive:
//
//	allocator, resolver, _ := sealtest.ShareableVolumeComponents(t)
//	opts := []seal.Option{seal.WithVolumeAllocator(allocator), seal.WithVolumeResolver(resolver), ...}
//	reg1, _ := seal.NewBarrierRegistry(logger, system, opts...) // first boot
//	reg2, _ := seal.NewBarrierRegistry(logger, system, opts...) // simulated restart
func ShareableVolumeComponents(tb testing.TB) (allocator volume.Allocator, resolver seal.VolumeResolver, root string) {
	tb.Helper()
	ctx := context.Background()

	registry := volume.NewRegistry()
	provider, err := registry.Lookup("dir")
	if err != nil {
		tb.Fatalf("sealtest: registry.Lookup(dir): %v", err)
	}
	root = tb.TempDir()
	vol, err := provider.Open(ctx, volume.Spec{Pool: "default", NodeID: "test-node", Path: root})
	if err != nil {
		tb.Fatalf("sealtest: provider.Open: %v", err)
	}
	tb.Cleanup(func() { _ = vol.Close(context.Background()) })

	open := volume.NewOpenVolumes("test-node")
	if err := open.Add("default", vol); err != nil {
		tb.Fatalf("sealtest: open.Add: %v", err)
	}

	kv := newFakeCatalogKV()
	tx := newFakeCatalogTx(kv)
	catalog, err := volume.NewCatalog(kv, tx, registry)
	if err != nil {
		tb.Fatalf("sealtest: volume.NewCatalog: %v", err)
	}
	if _, err := catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "default", Provider: "dir"}); err != nil {
		tb.Fatalf("sealtest: catalog.CreatePool: %v", err)
	}
	return catalog, open, root
}

// ReserveVolumeTenant reserves tenantID on allocator's "default" pool,
// with tenantID as both the one-clean-path-component Path and
// reserveBytes left at 0 -- the Assignment RegisterVolumeTenant /
// ReattachVolumeTenant require to already exist.
func ReserveVolumeTenant(tb testing.TB, allocator volume.Allocator, tenantID string) {
	tb.Helper()
	if _, err := allocator.Reserve(context.Background(), tenantID, "default", tenantID, 0); err != nil {
		tb.Fatalf("sealtest: Reserve(%s): %v", tenantID, err)
	}
}

// ---------------------------------------------------------------------------
// fakeCatalogKV / fakeCatalogTx: an in-memory, non-replicated double for
// pkg/volume's KVServicer/TransactionServicer, duplicated from
// pkg/seal's own (unexported, package-internal) test fakes because this
// package is imported across package boundaries and cannot reach them.
// ---------------------------------------------------------------------------

type fakeCatalogKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeCatalogKV() *fakeCatalogKV { return &fakeCatalogKV{data: make(map[string][]byte)} }

func (f *fakeCatalogKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	return bytes.Clone(v), nil
}

func (f *fakeCatalogKV) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = bytes.Clone(value)
	return nil
}

func (f *fakeCatalogKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.data[key]; exists {
		return false, nil
	}
	f.data[key] = bytes.Clone(value)
	return true, nil
}

func (f *fakeCatalogKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	f.data[key] = bytes.Clone(next)
	return true, nil
}

func (f *fakeCatalogKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	delete(f.data, key)
	return true, nil
}

func (f *fakeCatalogKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]byte)
	for k, v := range f.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = bytes.Clone(v)
		}
	}
	return out, nil
}

type fakeCatalogTx struct {
	kv *fakeCatalogKV

	mu       sync.Mutex
	versions map[string]int64
}

func newFakeCatalogTx(kv *fakeCatalogKV) *fakeCatalogTx {
	return &fakeCatalogTx{kv: kv, versions: make(map[string]int64)}
}

func (t *fakeCatalogTx) Execute(ctx context.Context, compares []lock.Compare, successOps, failureOps []lock.Op) (*lock.TransactionResponse, error) {
	for _, c := range compares {
		ok, err := t.evalCompare(ctx, c)
		if err != nil {
			return nil, err
		}
		if !ok {
			if err := t.apply(ctx, failureOps); err != nil {
				return nil, err
			}
			return &lock.TransactionResponse{Succeeded: false}, nil
		}
	}
	if err := t.apply(ctx, successOps); err != nil {
		return nil, err
	}
	return &lock.TransactionResponse{Succeeded: true}, nil
}

func (t *fakeCatalogTx) evalCompare(ctx context.Context, c lock.Compare) (bool, error) {
	key := string(c.Key)
	switch c.Target {
	case lock.CompareVersion:
		t.mu.Lock()
		v := t.versions[key]
		t.mu.Unlock()
		return v == c.Version, nil
	case lock.CompareValue:
		cur, err := t.kv.Get(ctx, key)
		if err != nil {
			return false, err
		}
		return bytes.Equal(cur, c.Value), nil
	default:
		return false, nil
	}
}

func (t *fakeCatalogTx) apply(ctx context.Context, ops []lock.Op) error {
	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case lock.OpPut:
			if err := t.kv.Put(ctx, key, op.Value); err != nil {
				return err
			}
			t.mu.Lock()
			t.versions[key]++
			t.mu.Unlock()
		case lock.OpDelete:
			t.kv.mu.Lock()
			delete(t.kv.data, key)
			t.kv.mu.Unlock()
			t.mu.Lock()
			t.versions[key] = 0
			t.mu.Unlock()
		}
	}
	return nil
}
