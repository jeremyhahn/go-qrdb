// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// The tenant-routed store is a kvstore.AtomicKVStore on every engine.
var _ kvstore.AtomicKVStore = (*tenantBarrierKVStore)(nil)

func tenantEngineStores(t *testing.T) map[string]storage.Storage {
	t.Helper()
	pdb, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	fdb, err := file.New(t.TempDir())
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	odb, err := objstore.New("local", map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	stores := map[string]storage.Storage{
		"memory":   memory.NewStorage(),
		"pebble":   pdb,
		"file":     fdb,
		"objstore": odb,
	}
	t.Cleanup(func() {
		for _, s := range stores {
			_ = s.Close()
		}
	})
	return stores
}

// newEngineTenantKV returns the tenant-routed KV over a tenant barrier whose
// backend is the given engine.
func newEngineTenantKV(t *testing.T, store storage.Storage) (kvstore.AtomicKVStore, *TenantBarrier) {
	t.Helper()
	tb, err := NewTenantBarrier("tkv", testLogger(), NewStorageAdapter(store), BarrierConfig{}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}
	if err := tb.Initialize(context.Background(), MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	kv, err := NewTenantBarrierKVStore(tb)
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}
	return kv.(kvstore.AtomicKVStore), tb
}

func TestTenantKV_EveryEngine_CompareAndSwapOneWinner(t *testing.T) {
	ctx := context.Background()
	for name, store := range tenantEngineStores(t) {
		t.Run(name, func(t *testing.T) {
			kv, _ := newEngineTenantKV(t, store)
			if err := kv.Put(ctx, "k", []byte("v0")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			var swapped atomic.Int64
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					ok, err := kv.CompareAndSwap(ctx, "k", []byte("v0"), []byte("next-"+strconv.Itoa(i)))
					if err != nil {
						t.Errorf("CompareAndSwap: %v", err)
						return
					}
					if ok {
						swapped.Add(1)
					}
				}(i)
			}
			close(start)
			wg.Wait()
			if got := swapped.Load(); got != 1 {
				t.Fatalf("%d callers swapped, want exactly 1", got)
			}
		})
	}
}

func TestTenantKV_EveryEngine_LosingSwapIsFalseNotError(t *testing.T) {
	ctx := context.Background()
	for name, store := range tenantEngineStores(t) {
		t.Run(name, func(t *testing.T) {
			kv, _ := newEngineTenantKV(t, store)
			ok, err := kv.CompareAndSwap(ctx, "absent", []byte("a"), []byte("b"))
			if err != nil || ok {
				t.Fatalf("absent key: (%v, %v), want (false, nil)", ok, err)
			}
			if err := kv.Put(ctx, "k", []byte("a")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			ok, err = kv.CompareAndSwap(ctx, "k", []byte("x"), []byte("b"))
			if err != nil || ok {
				t.Fatalf("mismatch: (%v, %v), want (false, nil)", ok, err)
			}
			got, err := kv.Get(ctx, "k")
			if err != nil || string(got) != "a" {
				t.Fatalf("value after losing swap = %q, %v; want a", got, err)
			}
		})
	}
}
