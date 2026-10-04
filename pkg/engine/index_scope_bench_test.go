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

package engine

import (
	"context"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// Index maintenance on the apply path (go-qrdb#410)
//
// Scoping adds a byte-prefix comparison per registered index per write, on
// the Raft apply path. These benchmarks measure that cost against the shapes
// it is paid in: a shard with no indexes, a shard whose single index covers
// the write, and a shard holding several databases' indexes of which one --
// or none -- covers the write. The last shape is the one prefix tenant
// isolation produces, and it is where the scope check lets the maintenance
// helpers skip the JSON parse entirely.
//
// Run with: make bench-engine
// ---------------------------------------------------------------------------

// benchEngine builds an MVCC engine holding indexCount indexes, each named
// for its own (tenant, database) namespace, and returns it with the namespace
// writes should be addressed to.
func benchEngine(b *testing.B, indexCount int) *MVCCEngine {
	b.Helper()
	stg := memory.NewStorage()
	b.Cleanup(func() { _ = stg.Close() })
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		b.Fatalf("NewMVCCEngine: %v", err)
	}
	b.Cleanup(func() { _ = eng.Close(context.Background()) })

	results := make([]sm.Result, 1)
	for i := 0; i < indexCount; i++ {
		name := "tenant-" + strconv.Itoa(i) + "/sales/users.email"
		cmd, err := protocol.EncodeIndexCreateOpts(name, "email", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false})
		if err != nil {
			b.Fatalf("EncodeIndexCreate: %v", err)
		}
		if err := eng.Update(context.Background(), []sm.Entry{{Index: uint64(i + 1), Cmd: cmd}}, results); err != nil {
			b.Fatalf("index create: %v", err)
		}
	}
	return eng
}

// benchApplyPuts drives count MVCC puts under keyPrefix through the apply
// path, which is where index maintenance runs.
func benchApplyPuts(b *testing.B, eng *MVCCEngine, keyPrefix string, value string) {
	b.Helper()
	ctx := context.Background()
	results := make([]sm.Result, 1)
	entries := make([]sm.Entry, 1)
	val := []byte(value)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(keyPrefix + strconv.Itoa(i))
		cmd, err := protocol.EncodeMVCCPut(key, val, 0)
		if err != nil {
			b.Fatalf("EncodeMVCCPut: %v", err)
		}
		entries[0] = sm.Entry{Index: uint64(i + 1000000), Cmd: cmd}
		if err := eng.Update(ctx, entries, results); err != nil {
			b.Fatalf("Update: %v", err)
		}
	}
}

// BenchmarkApplyPut_NoIndexes is the floor: what a write costs when index
// maintenance has nothing to do.
func BenchmarkApplyPut_NoIndexes(b *testing.B) {
	eng := benchEngine(b, 0)
	benchApplyPuts(b, eng, "tenant-0/sales/u/", `{"email":"a@example.com","name":"alice"}`)
}

// BenchmarkApplyPut_OneIndexInScope is the single-database shard: one index,
// and every write belongs to it. The scope check is pure overhead here.
func BenchmarkApplyPut_OneIndexInScope(b *testing.B) {
	eng := benchEngine(b, 1)
	benchApplyPuts(b, eng, "tenant-0/sales/u/", `{"email":"a@example.com","name":"alice"}`)
}

// BenchmarkApplyPut_EightIndexesOneInScope is the prefix-isolation shape:
// several tenants' indexes on one shard, one of which covers the write.
func BenchmarkApplyPut_EightIndexesOneInScope(b *testing.B) {
	eng := benchEngine(b, 8)
	benchApplyPuts(b, eng, "tenant-3/sales/u/", `{"email":"a@example.com","name":"alice"}`)
}

// BenchmarkApplyPut_EightIndexesNoneInScope is the same shard for a write
// that belongs to no index: a tenant with no index of its own, or a key
// outside every indexed keyspace. Before scoping, this write was parsed as
// JSON and offered to all eight indexes.
func BenchmarkApplyPut_EightIndexesNoneInScope(b *testing.B) {
	eng := benchEngine(b, 8)
	benchApplyPuts(b, eng, "tenant-99/sales/u/", `{"email":"a@example.com","name":"alice"}`)
}

// BenchmarkApplyPut_EightIndexesNonJSON pairs with the above for the value
// shape index maintenance rejects after parsing rather than before.
func BenchmarkApplyPut_EightIndexesNonJSON(b *testing.B) {
	eng := benchEngine(b, 8)
	benchApplyPuts(b, eng, "tenant-3/sales/u/", "not json at all, just bytes")
}
