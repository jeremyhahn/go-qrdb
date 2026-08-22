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

package seal

import (
	"context"
	"strconv"
	"sync"
	"testing"
)

// guardedMutator is the shape every canonical seal backend must offer once
// StorageBarrier requires it. Naming it here is what makes the table below a
// contract rather than a handful of unrelated tests.
type guardedMutator interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
}

// Every canonical backend honours the same guarded-mutation contract.
//
// They are tested together because the defect this covers was a MISSING
// implementation, not a wrong one: StorageBarrier requires guardedSwapBackend,
// and a backend that does not offer it makes CompareAndSwap return
// ErrGuardedSwapUnsupported through an otherwise ordinary composition, while
// still advertising AtomicKVStore. A per-backend test that only ever ran
// against MemoryBackend would not have caught that (go-qrdb#56).
func TestCanonicalBackends_GuardedMutationContract(t *testing.T) {
	backends := map[string]func() guardedMutator{
		"MemoryBackend": func() guardedMutator { return NewMemoryBackend() },
		"StorageAdapter": func() guardedMutator {
			return NewStorageAdapter(newMemLegacyStorage()).(guardedMutator)
		},
	}

	for name, build := range backends {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			t.Run("swaps on a match", func(t *testing.T) {
				b := build()
				if err := b.Put(ctx, "k", []byte("old")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				swapped, err := b.CompareAndSwap(ctx, "k", []byte("old"), []byte("new"))
				if err != nil || !swapped {
					t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
				}
				got, gerr := b.Get(ctx, "k")
				if gerr != nil || string(got) != "new" {
					t.Fatalf("stored = (%q, %v), want %q", got, gerr, "new")
				}
			})

			t.Run("refuses a swap on a mismatch and changes nothing", func(t *testing.T) {
				b := build()
				if err := b.Put(ctx, "k", []byte("old")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				swapped, err := b.CompareAndSwap(ctx, "k", []byte("stale"), []byte("new"))
				if err != nil || swapped {
					t.Fatalf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
				}
				got, _ := b.Get(ctx, "k")
				if string(got) != "old" {
					t.Fatalf("stored = %q, want the holder's %q", got, "old")
				}
			})

			t.Run("deletes on a match", func(t *testing.T) {
				b := build()
				if err := b.Put(ctx, "k", []byte("old")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				deleted, err := b.CompareAndDelete(ctx, "k", []byte("old"))
				if err != nil || !deleted {
					t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
				}
			})

			t.Run("refuses a delete on a mismatch and changes nothing", func(t *testing.T) {
				b := build()
				if err := b.Put(ctx, "k", []byte("old")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				deleted, err := b.CompareAndDelete(ctx, "k", []byte("stale"))
				if err != nil || deleted {
					t.Fatalf("CompareAndDelete = (%v, %v), want (false, nil)", deleted, err)
				}
				got, _ := b.Get(ctx, "k")
				if string(got) != "old" {
					t.Fatalf("stored = %q, want the holder's %q", got, "old")
				}
			})

			t.Run("does not create an absent key", func(t *testing.T) {
				b := build()
				swapped, err := b.CompareAndSwap(ctx, "never", nil, []byte("new"))
				if err != nil || swapped {
					t.Fatalf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
				}
				if got, _ := b.Get(ctx, "never"); got != nil {
					t.Fatalf("a guarded swap CREATED an absent key: %q", got)
				}
			})

			t.Run("admits exactly one of N contenders", func(t *testing.T) {
				b := build()
				if err := b.Put(ctx, "k", []byte("start")); err != nil {
					t.Fatalf("Put: %v", err)
				}

				const contenders = 16
				var (
					wg      sync.WaitGroup
					mu      sync.Mutex
					winners []string
				)
				release := make(chan struct{})
				for i := 0; i < contenders; i++ {
					wg.Add(1)
					go func(id int) {
						defer wg.Done()
						mine := "w" + strconv.Itoa(id)
						<-release
						ok, err := b.CompareAndSwap(ctx, "k", []byte("start"), []byte(mine))
						if err != nil {
							t.Errorf("contender %d: %v", id, err)
							return
						}
						if ok {
							mu.Lock()
							winners = append(winners, mine)
							mu.Unlock()
						}
					}(i)
				}
				close(release)
				wg.Wait()

				if len(winners) != 1 {
					t.Fatalf("%d contenders swapped from one expected value, want exactly 1: %v",
						len(winners), winners)
				}
				got, _ := b.Get(ctx, "k")
				if string(got) != winners[0] {
					t.Errorf("stored = %q, want the winner's %q", got, winners[0])
				}
			})
		})
	}
}

// An absent key is (false, nil) for EVERY guarded verb on StorageBarrier.
//
// The two verbs read absence from different places and both used to get it
// wrong in the same way. StorageBackend reports a missing key by returning
// ErrSecretNotFound from Get — MemoryBackend and the lifecycle kvSealBackend
// both do — so a barrier that propagates that error turns "nothing was there"
// into a failure. A caller cannot then tell an absent key from a broken
// backend, and the natural retry loop around a guarded verb spins on an error
// that will never clear.
//
// Table-driven over the verbs rather than two tests, because the requirement
// is that they AGREE: a fix applied to one and not the other is the defect
// (go-qrdb#56).
func TestStorageBarrier_GuardedVerbs_AbsentKeyIsNotAnError(t *testing.T) {
	verbs := map[string]func(ctx context.Context, b *StorageBarrier, key string) (bool, error){
		"CompareAndSwap": func(ctx context.Context, b *StorageBarrier, key string) (bool, error) {
			return b.CompareAndSwap(ctx, key, []byte("expected"), []byte("next"))
		},
		"CompareAndDelete": func(ctx context.Context, b *StorageBarrier, key string) (bool, error) {
			return b.CompareAndDelete(ctx, key, []byte("expected"))
		},
	}

	for name, verb := range verbs {
		t.Run(name, func(t *testing.T) {
			b := newUnsealedStorageBarrier(t)
			ok, err := verb(context.Background(), b, "no/such/key")
			if err != nil {
				t.Fatalf("%s on an absent key = (%v, %v), want (false, nil) — "+
					"absence is an outcome, not a failure", name, ok, err)
			}
			if ok {
				t.Fatalf("%s reported success on an absent key", name)
			}
		})
	}
}

// memLegacyStorage is the minimal LegacyStorage StorageAdapter wraps.
type memLegacyStorage struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemLegacyStorage() *memLegacyStorage {
	return &memLegacyStorage{data: make(map[string][]byte)}
}

func (m *memLegacyStorage) Get(key []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (m *memLegacyStorage) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (m *memLegacyStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *memLegacyStorage) Close() error { return nil }

func (m *memLegacyStorage) Exists(key []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *memLegacyStorage) List(prefix []byte) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out [][]byte
	for k := range m.data {
		if len(prefix) == 0 || (len(k) >= len(prefix) && k[:len(prefix)] == string(prefix)) {
			out = append(out, []byte(k))
		}
	}
	return out, nil
}
