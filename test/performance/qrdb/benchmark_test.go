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

//go:build benchmark

package main

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"
)

// suite is the global benchmark suite populated by benchmarks and
// serialized to stdout by TestMain after all benchmarks complete.
var suite = perfresult.NewBenchmarkSuite(fmt.Sprintf("qrdb-%s-%s", benchSM, benchProtocol), "dev")

// payload is the deterministic value used across all write benchmarks.
var payload = generatePayload(defaultPayloadSize)

// ---------------------------------------------------------------------------
// Single-node (1N) benchmarks
// ---------------------------------------------------------------------------

// BenchmarkQRDB_KVPut_SEQ benchmarks sequential PUT throughput
// against a single QRDB node.
func BenchmarkQRDB_KVPut_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-put-%d", i)
		t0 := time.Now()
		if err := kvPut(client, cluster.leaderPort, key, payload); err != nil {
			b.Fatalf("put: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_put_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_KVPut_Conc8 benchmarks concurrent PUT throughput
// with 8 goroutines against a single QRDB node.
func BenchmarkQRDB_KVPut_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	start := time.Now()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-conc-%d", i)
				t0 := time.Now()
				if err := kvPut(client, cluster.leaderPort, key, payload); err != nil {
					b.Errorf("put: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	_ = time.Since(start)
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_put_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_KVGet_SEQ benchmarks sequential GET throughput
// against a single QRDB node.
func BenchmarkQRDB_KVGet_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed a fixed pool of keys concurrently before benchmarking reads.
	seedKeys(b, client, cluster.leaderPort, "bench-get-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-get-%d", i%seedPoolSize)
		t0 := time.Now()
		if _, err := kvGet(client, cluster.leaderPort, key); err != nil {
			b.Fatalf("get: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_get_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_KVGet_Conc8 benchmarks concurrent GET throughput
// with 8 goroutines against a single QRDB node.
func BenchmarkQRDB_KVGet_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed a fixed pool of keys concurrently before benchmarking reads.
	seedKeys(b, client, cluster.leaderPort, "bench-getc-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	start := time.Now()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-getc-%d", i%seedPoolSize)
				t0 := time.Now()
				if _, err := kvGet(client, cluster.leaderPort, key); err != nil {
					b.Errorf("get: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	_ = time.Since(start)
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_get_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_KVDelete_SEQ benchmarks sequential DELETE throughput
// against a single QRDB node. Deletes are issued against keys that may
// or may not exist (the Raft proposal path is identical either way).
func BenchmarkQRDB_KVDelete_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed a fixed pool of keys concurrently before benchmarking deletes.
	seedKeys(b, client, cluster.leaderPort, "bench-del-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-del-%d", i%seedPoolSize)
		t0 := time.Now()
		if err := kvDelete(client, cluster.leaderPort, key); err != nil {
			b.Fatalf("delete: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_delete_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_KVDelete_Conc8 benchmarks concurrent DELETE throughput
// with 8 goroutines against a single QRDB node.
func BenchmarkQRDB_KVDelete_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed a fixed pool of keys concurrently before benchmarking deletes.
	seedKeys(b, client, cluster.leaderPort, "bench-delc-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-delc-%d", i%seedPoolSize)
				t0 := time.Now()
				if err := kvDelete(client, cluster.leaderPort, key); err != nil {
					b.Errorf("delete: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_delete_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_KVBatchPut_10K benchmarks bulk PUT throughput by
// writing 10K entries per benchmark iteration against a single QRDB node.
func BenchmarkQRDB_KVBatchPut_10K(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)
	totalOps := b.N * batchSize
	rec := perfresult.NewLatencyRecorder(totalOps)

	b.ResetTimer()
	for i := range b.N {
		for j := range batchSize {
			key := fmt.Sprintf("bench-batch-%d-%d", i, j)
			t0 := time.Now()
			if err := kvPut(client, cluster.leaderPort, key, payload); err != nil {
				b.Fatalf("batch put: %v", err)
			}
			rec.Record(time.Since(t0))
		}
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_batch_put_10k", totalOps, b.Elapsed(), rec)
}

// BenchmarkQRDB_KVScan_10K benchmarks prefix scan throughput over
// 10K pre-seeded keys against a single QRDB node.
func BenchmarkQRDB_KVScan_10K(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed 10K keys with a common prefix.
	for i := range scanSize {
		key := fmt.Sprintf("scan/%05d", i)
		if err := kvPut(client, cluster.leaderPort, key, payload); err != nil {
			b.Fatalf("seed scan: %v", err)
		}
	}

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for range b.N {
		t0 := time.Now()
		if _, err := kvList(client, cluster.leaderPort, "scan/"); err != nil {
			b.Fatalf("scan: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_scan_10k", b.N, b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// Multi-node 3N benchmarks
// ---------------------------------------------------------------------------

// BenchmarkQRDB_KVPut_SEQ_3N benchmarks sequential PUT throughput
// against a 3-node QRDB cluster.
func BenchmarkQRDB_KVPut_SEQ_3N(b *testing.B) {
	cluster := SetupQRDBCluster(b, 3)
	client := benchClient(cluster.caPool, 10*time.Second)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-3n-put-%d", i)
		t0 := time.Now()
		if err := kvPut(client, cluster.leaderPort, key, payload); err != nil {
			b.Fatalf("put: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.Config.NodeCount = 3
	suite.AddResult("kv_put_seq_3n", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_KVPut_Conc8_3N benchmarks concurrent PUT throughput
// with 8 goroutines against a 3-node QRDB cluster.
func BenchmarkQRDB_KVPut_Conc8_3N(b *testing.B) {
	cluster := SetupQRDBCluster(b, 3)
	client := benchClient(cluster.caPool, 10*time.Second)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	start := time.Now()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-3n-conc-%d", i)
				t0 := time.Now()
				if err := kvPut(client, cluster.leaderPort, key, payload); err != nil {
					b.Errorf("put: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	_ = time.Since(start)
	b.StopTimer()

	suite.Config.TLS = true
	suite.Config.NodeCount = 3
	suite.AddResult("kv_put_conc8_3n", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_KVGet_SEQ_3N benchmarks sequential GET throughput
// against a 3-node QRDB cluster.
func BenchmarkQRDB_KVGet_SEQ_3N(b *testing.B) {
	cluster := SetupQRDBCluster(b, 3)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed a fixed pool of keys concurrently before benchmarking reads.
	seedKeys(b, client, cluster.leaderPort, "bench-3n-get-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-3n-get-%d", i%seedPoolSize)
		t0 := time.Now()
		if _, err := kvGet(client, cluster.leaderPort, key); err != nil {
			b.Fatalf("get: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.Config.NodeCount = 3
	suite.AddResult("kv_get_seq_3n", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_KVGet_Conc8_3N benchmarks concurrent GET throughput
// with 8 goroutines against a 3-node QRDB cluster.
func BenchmarkQRDB_KVGet_Conc8_3N(b *testing.B) {
	cluster := SetupQRDBCluster(b, 3)
	client := benchClient(cluster.caPool, 10*time.Second)

	// Seed a fixed pool of keys concurrently before benchmarking reads.
	seedKeys(b, client, cluster.leaderPort, "bench-3n-getc-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	start := time.Now()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-3n-getc-%d", i%seedPoolSize)
				t0 := time.Now()
				if _, err := kvGet(client, cluster.leaderPort, key); err != nil {
					b.Errorf("get: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	_ = time.Since(start)
	b.StopTimer()

	suite.Config.TLS = true
	suite.Config.NodeCount = 3
	suite.AddResult("kv_get_conc8_3n", rec.Count(), b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// TestMain
// ---------------------------------------------------------------------------

// TestMain runs all benchmarks and writes the accumulated suite JSON to
// stdout with sentinel markers for the orchestrator to extract.
func TestMain(m *testing.M) {
	suite.Config.PayloadSize = defaultPayloadSize
	suite.Config.ConcurrentGoroutines = concurrency

	exitCode := m.Run()

	fmt.Println()
	if err := perfresult.WriteSuiteToStdout(suite); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write benchmark suite: %v\n", err)
	}

	os.Exit(exitCode)
}
