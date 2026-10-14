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

package database

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// engineStores returns a fresh store on every storage engine, closed on cleanup.
func engineStores(t *testing.T) map[string]storage.Storage {
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

// raceSwaps races racers swaps from "v0" on key and returns how many won.
func raceSwaps(t *testing.T, b AtomicBackend, key []byte) int64 {
	t.Helper()
	ctx := context.Background()
	var swapped atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ok, err := b.CompareAndSwap(ctx, key, []byte("v0"), []byte("next"+strconv.Itoa(i)))
			if err != nil {
				t.Errorf("CompareAndSwap: unexpected error %v", err)
				return
			}
			if ok {
				swapped.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	return swapped.Load()
}

func TestStandaloneBackend_EveryEngine_CompareAndSwapOneWinner(t *testing.T) {
	for name, store := range engineStores(t) {
		t.Run(name, func(t *testing.T) {
			b := NewStandaloneBackend(store)
			key := []byte("swap")
			if err := b.Put(context.Background(), key, []byte("v0")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if got := raceSwaps(t, b, key); got != 1 {
				t.Fatalf("%d callers swapped, want exactly 1", got)
			}
		})
	}
}

func TestBarrierBackend_EveryEngine_CompareAndSwapOneWinner(t *testing.T) {
	for name, store := range engineStores(t) {
		t.Run(name, func(t *testing.T) {
			b := NewBarrierBackend(NewStandaloneBackend(store), &xorBarrier{mask: 0x5A})
			key := []byte("swap")
			if err := b.Put(context.Background(), key, []byte("v0")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if got := raceSwaps(t, b, key); got != 1 {
				t.Fatalf("%d callers swapped, want exactly 1", got)
			}
		})
	}
}

// TestBarrierBackend_EveryEngine_SealedCompareAndSwapFailsClosed pins that a
// swap inside the sealed window is an error, never a quiet false.
func TestBarrierBackend_EveryEngine_SealedCompareAndSwapFailsClosed(t *testing.T) {
	for name, store := range engineStores(t) {
		t.Run(name, func(t *testing.T) {
			barrier := &xorBarrier{mask: 0x5A}
			b := NewBarrierBackend(NewStandaloneBackend(store), barrier)
			ctx := context.Background()
			if err := b.Put(ctx, []byte("k"), []byte("v0")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			barrier.sealed = true
			ok, err := b.CompareAndSwap(ctx, []byte("k"), []byte("v0"), []byte("v1"))
			var sealed *SealedError
			if ok || !errors.As(err, &sealed) {
				t.Fatalf("sealed swap = (%v, %v), want (false, *SealedError)", ok, err)
			}
			barrier.sealed = false
			got, err := b.Get(ctx, []byte("k"))
			if err != nil || string(got) != "v0" {
				t.Fatalf("value after refused swap = %q, %v; want v0", got, err)
			}
		})
	}
}
