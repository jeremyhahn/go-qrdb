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

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// Apply-path benchmarks
//
// The apply path is the hot consensus path: every committed entry passes
// through Update. These benchmarks measure it at several batch sizes, because
// batch size is what the apply loop varies and what determines how much a
// read-through-batch costs relative to the write it accompanies.
//
// Runs with noSync so the measurement is the state machine's own cost rather
// than fsync latency.
// ---------------------------------------------------------------------------

// benchBatchSizes are the apply-batch sizes exercised by every apply benchmark.
var benchBatchSizes = []int{1, 8, 64, 256}

// newBenchMVCCStateMachine opens an MVCCStateMachine for benchmarking.
func newBenchMVCCStateMachine(b *testing.B) *MVCCStateMachine {
	b.Helper()
	s := NewMVCCStateMachine(nil)
	s.noSync = true
	ctx := context.Background()
	if _, err := s.Open(ctx, b.TempDir(), make(chan struct{})); err != nil {
		b.Fatalf("open mvcc sm: %v", err)
	}
	b.Cleanup(func() { _ = s.Close(ctx) })
	return s
}

// newBenchPebbleStateMachine opens a PebbleStateMachine for benchmarking.
func newBenchPebbleStateMachine(b *testing.B) *PebbleStateMachine {
	b.Helper()
	s := NewPebbleStateMachine(&pebble.Options{})
	ctx := context.Background()
	if _, err := s.Open(ctx, b.TempDir(), make(chan struct{})); err != nil {
		b.Fatalf("open pebble sm: %v", err)
	}
	b.Cleanup(func() { _ = s.Close(ctx) })
	return s
}

// runApplyBenchmark drives update over batches of size entries built by build,
// reporting nanoseconds per applied ENTRY rather than per batch.
func runApplyBenchmark(b *testing.B, size int, build func(index uint64) sm.Entry,
	update func(entries []sm.Entry, results []sm.Result) error) {

	entries := make([]sm.Entry, size)
	results := make([]sm.Result, size)
	var index uint64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < size; j++ {
			index++
			entries[j] = build(index)
		}
		if err := update(entries, results); err != nil {
			b.Fatalf("Update: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/entry")
}

// BenchmarkMVCCApply_Put measures the plain-put apply path, which reads the
// previous value through the batch and writes data, metadata, and a revision
// index entry.
func BenchmarkMVCCApply_Put(b *testing.B) {
	value := make([]byte, 128)
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchMVCCStateMachine(b)
			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					return mvccPutEntry(index, []byte(fmt.Sprintf("key-%08d", index)), value, 0)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkMVCCApply_PutHotKey rewrites a single key, so every entry's
// metadata read hits a value written earlier in the same batch. This is the
// case that exercises read-your-own-writes hardest.
func BenchmarkMVCCApply_PutHotKey(b *testing.B) {
	value := make([]byte, 128)
	key := []byte("hot-key")
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchMVCCStateMachine(b)
			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry { return mvccPutEntry(index, key, value, 0) },
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkMVCCApply_Transaction measures the compare-and-swap apply path: a
// mod-revision precondition read followed by a put.
//
// The precondition is deliberately one that holds regardless of how the log is
// grouped — each entry claims its own fresh key, whose mod revision is 0 —
// so the benchmark performs identical work whether or not preconditions read
// through the apply batch. A contended precondition would not: reading stale
// committed state makes most compares fail, most transactions write nothing,
// and the resulting "speedup" would measure the defect rather than the cost of
// fixing it.
func BenchmarkMVCCApply_Transaction(b *testing.B) {
	value := make([]byte, 128)
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchMVCCStateMachine(b)
			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					key := []byte(fmt.Sprintf("txn-%08d", index))
					compare := protocol.EncodeCompare(key,
						byte(service.CompareModRevision), byte(service.CompareEqual),
						nil, 0, 0, 0)
					op := protocol.EncodeOp(protocol.OpMVCCPut, key, value, nil, 0)
					return sm.Entry{
						Index: index,
						Cmd:   protocol.EncodeTransaction([][]byte{compare}, [][]byte{op}, nil),
					}
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkPebbleApply_Put measures the non-MVCC apply path, which is a single
// batch Set per entry plus the last-applied marker.
func BenchmarkPebbleApply_Put(b *testing.B) {
	value := make([]byte, 128)
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchPebbleStateMachine(b)
			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					return mvccPutEntry(index, []byte(fmt.Sprintf("key-%08d", index)), value, 0)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}

// BenchmarkPebbleApply_PutIfAbsent measures the conditional-put apply path,
// whose existence guard reads through the batch.
func BenchmarkPebbleApply_PutIfAbsent(b *testing.B) {
	value := make([]byte, 128)
	for _, size := range benchBatchSizes {
		b.Run(fmt.Sprintf("batch=%d", size), func(b *testing.B) {
			s := newBenchPebbleStateMachine(b)
			runApplyBenchmark(b, size,
				func(index uint64) sm.Entry {
					return mvccPutIfAbsentEntry(index, []byte(fmt.Sprintf("key-%08d", index)), value)
				},
				func(entries []sm.Entry, results []sm.Result) error {
					return s.Update(context.Background(), entries, results)
				})
		})
	}
}
