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
	"context"
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
var suite = perfresult.NewBenchmarkSuite(fmt.Sprintf("tikv-%sn", clusterNodes), "v8.5.0")

// payload is the deterministic value used across all write benchmarks.
var payload = generatePayload(defaultPayloadSize)

// ---------------------------------------------------------------------------
// Single-node (1N) benchmarks
// ---------------------------------------------------------------------------

// BenchmarkTiKV_KVPut_SEQ measures sequential Put throughput and latency.
func BenchmarkTiKV_KVPut_SEQ(b *testing.B) {
	cluster := SetupTiKVCluster(b)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := []byte(fmt.Sprintf("bench-put-%d", i))
		t0 := time.Now()
		if err := cluster.client.Put(context.Background(), key, payload); err != nil {
			b.Fatalf("put: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_put_seq"), b.N, b.Elapsed(), rec)
}

// BenchmarkTiKV_KVPut_Conc8 measures concurrent Put throughput with 8 writers.
func BenchmarkTiKV_KVPut_Conc8(b *testing.B) {
	cluster := SetupTiKVCluster(b)
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
				key := []byte(fmt.Sprintf("bench-conc-%d", i))
				t0 := time.Now()
				if err := cluster.client.Put(context.Background(), key, payload); err != nil {
					b.Errorf("put: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_put_conc8"), rec.Count(), b.Elapsed(), rec)
}

// BenchmarkTiKV_KVGet_SEQ measures sequential Get throughput and latency.
func BenchmarkTiKV_KVGet_SEQ(b *testing.B) {
	cluster := SetupTiKVCluster(b)

	// Seed a fixed pool of keys concurrently before benchmarking reads.
	seedKeys(b, cluster.client, "bench-get-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := []byte(fmt.Sprintf("bench-get-%d", i%seedPoolSize))
		t0 := time.Now()
		val, err := cluster.client.Get(context.Background(), key)
		if err != nil {
			b.Fatalf("get: %v", err)
		}
		if val == nil {
			b.Fatalf("get returned nil for key bench-get-%d", i%seedPoolSize)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_get_seq"), b.N, b.Elapsed(), rec)
}

// BenchmarkTiKV_KVGet_Conc8 measures concurrent Get throughput with 8 readers.
func BenchmarkTiKV_KVGet_Conc8(b *testing.B) {
	cluster := SetupTiKVCluster(b)

	// Seed a fixed pool of keys concurrently before benchmarking reads.
	seedKeys(b, cluster.client, "bench-getc-", payload)

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
				key := []byte(fmt.Sprintf("bench-getc-%d", i%seedPoolSize))
				t0 := time.Now()
				val, err := cluster.client.Get(context.Background(), key)
				if err != nil {
					b.Errorf("get: %v", err)
					return
				}
				if val == nil {
					b.Errorf("get returned nil for key bench-getc-%d", i%seedPoolSize)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_get_conc8"), rec.Count(), b.Elapsed(), rec)
}

// BenchmarkTiKV_KVDelete_SEQ measures sequential Delete throughput and latency.
func BenchmarkTiKV_KVDelete_SEQ(b *testing.B) {
	cluster := SetupTiKVCluster(b)

	// Seed a fixed pool of keys concurrently before benchmarking deletes.
	seedKeys(b, cluster.client, "bench-del-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := []byte(fmt.Sprintf("bench-del-%d", i%seedPoolSize))
		t0 := time.Now()
		if err := cluster.client.Delete(context.Background(), key); err != nil {
			b.Fatalf("delete: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_delete_seq"), b.N, b.Elapsed(), rec)
}

// BenchmarkTiKV_KVDelete_Conc8 measures concurrent Delete throughput with 8 goroutines.
func BenchmarkTiKV_KVDelete_Conc8(b *testing.B) {
	cluster := SetupTiKVCluster(b)

	// Seed a fixed pool of keys concurrently before benchmarking deletes.
	seedKeys(b, cluster.client, "bench-delc-", payload)

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
				key := []byte(fmt.Sprintf("bench-delc-%d", i%seedPoolSize))
				t0 := time.Now()
				if err := cluster.client.Delete(context.Background(), key); err != nil {
					b.Errorf("delete: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_delete_conc8"), rec.Count(), b.Elapsed(), rec)
}

// BenchmarkTiKV_KVBatchPut_10K measures batch Put throughput for 10K keys per batch.
func BenchmarkTiKV_KVBatchPut_10K(b *testing.B) {
	cluster := SetupTiKVCluster(b)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		keys := make([][]byte, batchSize)
		values := make([][]byte, batchSize)
		for j := range batchSize {
			keys[j] = []byte(fmt.Sprintf("bench-batch-%d-%d", i, j))
			values[j] = payload
		}
		t0 := time.Now()
		if err := cluster.client.BatchPut(context.Background(), keys, values); err != nil {
			b.Fatalf("batch put: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_batch_put_10k"), b.N, b.Elapsed(), rec)
}

// BenchmarkTiKV_KVScan_10K measures Scan throughput for 10K keys per scan.
func BenchmarkTiKV_KVScan_10K(b *testing.B) {
	cluster := SetupTiKVCluster(b)

	// Seed 10K keys with a common prefix for scan range.
	keys := make([][]byte, scanSize)
	values := make([][]byte, scanSize)
	for i := range scanSize {
		keys[i] = []byte(fmt.Sprintf("scan/%05d", i))
		values[i] = payload
	}
	if err := cluster.client.BatchPut(context.Background(), keys, values); err != nil {
		b.Fatalf("seed scan keys: %v", err)
	}

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for range b.N {
		t0 := time.Now()
		// Scan from "scan/" to "scan0" (lexicographically after "scan/").
		scannedKeys, scannedValues, err := cluster.client.Scan(context.Background(),
			[]byte("scan/"), []byte("scan0"), scanSize)
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(scannedKeys) == 0 || len(scannedValues) == 0 {
			b.Fatalf("scan returned 0 results, expected %d", scanSize)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("kv_scan_10k"), b.N, b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// TestMain
// ---------------------------------------------------------------------------

// TestMain runs all benchmarks and writes the accumulated suite JSON to
// stdout with sentinel markers for the orchestrator to extract.
func TestMain(m *testing.M) {
	suite.Config.TLS = true
	suite.Config.PayloadSize = defaultPayloadSize
	suite.Config.ConcurrentGoroutines = concurrency

	exitCode := m.Run()

	fmt.Println()
	if err := perfresult.WriteSuiteToStdout(suite); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write benchmark suite: %v\n", err)
	}

	os.Exit(exitCode)
}
