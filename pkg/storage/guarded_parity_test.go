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

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// guardedBackends returns every parity backend as a GuardedStorage, failing the
// test for a backend that does not implement it.
func guardedBackends(t *testing.T) map[string]storage.GuardedStorage {
	t.Helper()
	out := make(map[string]storage.GuardedStorage)
	for _, b := range newParityBackends(t) {
		g, ok := b.s.(storage.GuardedStorage)
		if !ok {
			t.Fatalf("%s does not implement storage.GuardedStorage", b.name)
		}
		out[b.name] = g
	}
	return out
}

func TestGuardedStorage_Semantics(t *testing.T) {
	ctx := context.Background()
	for name, g := range guardedBackends(t) {
		t.Run(name, func(t *testing.T) {
			s := g.(storage.Storage)
			key := []byte("k")

			swapped, err := g.CompareAndSwap(ctx, key, []byte("a"), []byte("b"))
			if err != nil || swapped {
				t.Fatalf("absent key: swapped=%v err=%v, want false nil", swapped, err)
			}
			if v, _ := s.Get(key); v != nil {
				t.Fatalf("absent key was created: %q", v)
			}

			if err := s.Set(key, []byte("a")); err != nil {
				t.Fatal(err)
			}
			swapped, err = g.CompareAndSwap(ctx, key, []byte("x"), []byte("b"))
			if err != nil || swapped {
				t.Fatalf("mismatch: swapped=%v err=%v, want false nil", swapped, err)
			}
			if v, _ := s.Get(key); !bytes.Equal(v, []byte("a")) {
				t.Fatalf("mismatch changed value to %q", v)
			}

			swapped, err = g.CompareAndSwap(ctx, key, []byte("a"), []byte("b"))
			if err != nil || !swapped {
				t.Fatalf("match: swapped=%v err=%v, want true nil", swapped, err)
			}
			if v, _ := s.Get(key); !bytes.Equal(v, []byte("b")) {
				t.Fatalf("value after swap = %q, want b", v)
			}
		})
	}
}

func TestGuardedStorage_Errors(t *testing.T) {
	for name, g := range guardedBackends(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := g.CompareAndSwap(context.Background(), nil, nil, nil); err == nil {
				t.Fatal("empty key: want error")
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := g.CompareAndSwap(cancelled, []byte("k"), nil, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled ctx: err=%v, want context.Canceled", err)
			}
			if err := g.(storage.Storage).Close(); err != nil {
				t.Fatal(err)
			}
			_, err := g.CompareAndSwap(context.Background(), []byte("k"), nil, nil)
			if !storage.IsClosed(err) {
				t.Fatalf("closed: err=%v, want closed error", err)
			}
		})
	}
}

// TestGuardedStorage_ConcurrentSwaps races many swaps from the same expected
// value on one key: exactly one wins.
func TestGuardedStorage_ConcurrentSwaps(t *testing.T) {
	ctx := context.Background()
	for name, g := range guardedBackends(t) {
		t.Run(name, func(t *testing.T) {
			s := g.(storage.Storage)
			key := []byte("race")
			if err := s.Set(key, []byte("start")); err != nil {
				t.Fatal(err)
			}
			const contenders = 16
			var (
				wg   sync.WaitGroup
				mu   sync.Mutex
				wins int
			)
			for i := 0; i < contenders; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					swapped, err := g.CompareAndSwap(ctx, key, []byte("start"), []byte{byte('A' + i)})
					if err != nil {
						t.Errorf("swap: %v", err)
						return
					}
					if swapped {
						mu.Lock()
						wins++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if wins != 1 {
				t.Fatalf("winners = %d, want exactly 1", wins)
			}
		})
	}
}
