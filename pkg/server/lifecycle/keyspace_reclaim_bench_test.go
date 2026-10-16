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

package lifecycle

import (
	"context"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// perKeyReclaimKV is a KVService without DeleteRange: the reclaim deletes one
// key per proposal over it, as it does on a shard whose replicas have not all
// activated the range-delete gate.
type perKeyReclaimKV struct {
	service.ReclaimKV
}

// BenchmarkReclaimDatabaseKeyspace measures dropping a database's keyspace
// through the wiring a node uses -- storage, engine, LocalBackend, KVService
// -- once with one delete per key ("per-key") and once with the chunked range
// deletes the reclaim proposes when it can ("range"), for both engines over
// the in-memory, file and pebble backends. Each iteration seeds n keys under
// the database outside the timer and reclaims them inside it. The file backend
// syncs every write, so it runs the smaller database only.
func BenchmarkReclaimDatabaseKeyspace(b *testing.B) {
	backends := []struct {
		name  string
		sizes []int
		new   func(b *testing.B) storage.Storage
	}{
		{"memory", []int{10_000, 100_000}, func(*testing.B) storage.Storage { return memory.NewStorage() }},
		// The file backend makes every key a file and fsyncs it and its
		// directory, so the per-key mode costs one fsync pair per key; 1,000
		// keys keeps a run in seconds rather than hours.
		{"file", []int{1_000}, func(b *testing.B) storage.Storage {
			eng, err := file.New(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			return eng
		}},
		{"pebble", []int{10_000, 100_000}, func(b *testing.B) storage.Storage {
			pdb, err := pebble.New(&pebble.Config{DataDir: b.TempDir()})
			if err != nil {
				b.Fatal(err)
			}
			return pdb
		}},
	}
	engines := []struct {
		name string
		new  func(b *testing.B, stg storage.Storage) engine.Engine
		seed func(b *testing.B, stg storage.Storage, host service.Host, ns string, n int)
	}{
		{"kv", func(_ *testing.B, stg storage.Storage) engine.Engine { return engine.NewKVEngine(stg) }, seedThroughStorage},
		{"mvcc", func(b *testing.B, stg storage.Storage) engine.Engine {
			eng, err := engine.NewMVCCEngine(stg)
			if err != nil {
				b.Fatal(err)
			}
			return eng
		}, seedThroughProposals},
	}
	modes := []struct {
		name string
		kv   func(service.KVServicer) service.ReclaimKV
	}{
		{"per-key", func(kv service.KVServicer) service.ReclaimKV { return perKeyReclaimKV{kv} }},
		{"range", func(kv service.KVServicer) service.ReclaimKV { return kv }},
	}
	ns := tenant.ComposeNamespace("acme", "reports")
	ctx := context.Background()

	for _, st := range backends {
		for _, en := range engines {
			for _, n := range st.sizes {
				for _, mode := range modes {
					b.Run(st.name+"/"+en.name+"/"+strconv.Itoa(n)+"/"+mode.name, func(b *testing.B) {
						for i := 0; i < b.N; i++ {
							b.StopTimer()
							stg := st.new(b)
							host := backend.NewLocalBackend(en.new(b, stg))
							kv := service.NewKVService(host, 0)
							en.seed(b, stg, host, ns, n)
							svc := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: mode.kv(kv)})
							b.StartTimer()

							if err := svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", ""); err != nil {
								b.Fatal(err)
							}

							b.StopTimer()
							if left, err := kv.ListAll(ctx, ns); err != nil || len(left) != 0 {
								b.Fatalf("after the reclaim: %d keys left, err %v", len(left), err)
							}
							_ = host.Close()
							_ = stg.Close()
						}
					})
				}
			}
		}
	}
}

// seedReclaimBatch is how many keys seedReclaimKeyspace writes per proposal.
const seedReclaimBatch = 1_024

// seedReclaimKeyspace writes n keys under ns in batched proposals. Seeding
// one proposal per key made the setup, not the reclaim being measured,
// dominate the run: on a durable backend each proposal is its own fsync.
// seedReclaimKey is the k-th key a benchmark seeds under ns.
func seedReclaimKey(ns string, k int) []byte {
	return []byte(ns + "bulk/" + strconv.Itoa(k))
}

// seedThroughStorage writes n keys under ns straight to stg in batched
// commits. KVEngine stores a key as itself, so with no index registered
// this is byte-for-byte what n Puts would leave, and KVEngine has no
// multi-key proposal to seed through instead. Seeding one proposal per key
// made the setup, not the reclaim being measured, dominate the run: on a
// durable backend every proposal is its own fsync.
func seedThroughStorage(b *testing.B, stg storage.Storage, _ service.Host, ns string, n int) {
	b.Helper()
	for k := 0; k < n; {
		batch := stg.Batch()
		for end := min(k+seedReclaimBatch, n); k < end; k++ {
			if err := batch.Set(seedReclaimKey(ns, k), []byte("value")); err != nil {
				b.Fatal(err)
			}
		}
		if err := batch.Commit(); err != nil {
			b.Fatal(err)
		}
		_ = batch.Close()
	}
}

// seedThroughProposals writes n keys under ns as batched proposals, so
// MVCCEngine assigns their revisions exactly as it does for client writes.
func seedThroughProposals(b *testing.B, _ storage.Storage, host service.Host, ns string, n int) {
	b.Helper()
	svc := service.NewBatchService(host, 0)
	ops := make([]service.Op, 0, seedReclaimBatch)
	for k := 0; k < n; {
		ops = ops[:0]
		for end := min(k+seedReclaimBatch, n); k < end; k++ {
			ops = append(ops, service.Op{Type: service.OpPut, Key: seedReclaimKey(ns, k), Value: []byte("value")})
		}
		if _, err := svc.Execute(context.Background(), ops); err != nil {
			b.Fatal(err)
		}
	}
}
