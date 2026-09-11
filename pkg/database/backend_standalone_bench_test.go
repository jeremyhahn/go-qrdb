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

package database

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// These benchmarks exist to keep a claim honest rather than to chase a number.
// StandaloneBackend serializes every write so its guarded verbs can be atomic,
// and "the write path pays for that" is a statement someone will want to check
// against the engines it actually runs on. Run them with -cpu at or above the
// host's core count: the cost of a write mutex is invisible single-threaded and
// is the entire question under parallelism.
//
//	make bench-database
//
// The comparison that chose the single mutex over per-key lock striping is not
// reproducible from this file -- the striped variant was measured and then not
// shipped. What is reproducible here is the shipped path's cost, against an
// unlocked Set on the same engine.

// benchKeys hands out distinct keys so a parallel benchmark measures the lock
// and the engine rather than one key's cache line.
var benchKeys atomic.Uint64

func benchStores(b *testing.B) map[string]func(*testing.B) storage.Storage {
	b.Helper()
	return map[string]func(*testing.B) storage.Storage{
		"memory": func(b *testing.B) storage.Storage {
			s := memory.NewStorage()
			b.Cleanup(func() { _ = s.Close() })
			return s
		},
		"pebble": func(b *testing.B) storage.Storage {
			s, err := pebble.New(&pebble.Config{DataDir: b.TempDir()})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close() })
			return s
		},
	}
}

// BenchmarkStandaloneBackend_Put measures the write path that now takes the
// shared mutex, against the same engine's unlocked Set. The gap is what the
// atomicity guarantee costs an ordinary write.
func BenchmarkStandaloneBackend_Put(b *testing.B) {
	value := make([]byte, 128)
	for engine, newStore := range benchStores(b) {
		b.Run(engine+"/unlocked-set", func(b *testing.B) {
			store := newStore(b)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := store.Set(benchKey(), value); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
		b.Run(engine+"/backend-put", func(b *testing.B) {
			backend := NewStandaloneBackend(newStore(b))
			ctx := context.Background()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := backend.Put(ctx, benchKey(), value); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkStandaloneBackend_PutIfAbsent measures the guarded write itself:
// one read and one write inside the same critical section, on a key that is
// always free so the write half is always taken.
func BenchmarkStandaloneBackend_PutIfAbsent(b *testing.B) {
	value := make([]byte, 128)
	for engine, newStore := range benchStores(b) {
		b.Run(engine, func(b *testing.B) {
			backend := NewStandaloneBackend(newStore(b))
			ctx := context.Background()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := backend.PutIfAbsent(ctx, benchKey(), value); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkStandaloneBackend_ContendedPutIfAbsent is the losing case: every
// caller claims the SAME key, so all but the first read, decline and return
// without writing. It is the shape a claim under contention actually has.
func BenchmarkStandaloneBackend_ContendedPutIfAbsent(b *testing.B) {
	value := make([]byte, 128)
	key := []byte("contended")
	for engine, newStore := range benchStores(b) {
		b.Run(engine, func(b *testing.B) {
			backend := NewStandaloneBackend(newStore(b))
			ctx := context.Background()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := backend.PutIfAbsent(ctx, key, value); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func benchKey() []byte {
	return []byte("bench/" + strconv.FormatUint(benchKeys.Add(1), 10))
}
