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

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"
)

// suite is the global benchmark suite populated by benchmarks and
// serialized to stdout by TestMain after all benchmarks complete.
var suite = perfresult.NewBenchmarkSuite(systemName, "dev")

// payload is the deterministic value used across all write benchmarks.
var payload = generatePayload(defaultPayloadSize)

// opTimeout is the per-operation context timeout for all gRPC calls.
// Prevents indefinite hangs if the server stalls on any operation.
const opTimeout = 10 * time.Second

// opCtx returns a context with the per-operation timeout.
func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

// ---------------------------------------------------------------------------
// PUT benchmarks
// ---------------------------------------------------------------------------

// BenchmarkEtcd_KVPut_SEQ measures sequential Put throughput and latency.
func BenchmarkEtcd_KVPut_SEQ(b *testing.B) {
	cluster := SetupEtcdCluster(b)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-put-%d", i)
		ctx, cancel := opCtx()
		t0 := time.Now()
		if _, err := cluster.client.Put(ctx, key, string(payload)); err != nil {
			cancel()
			b.Fatalf("put: %v", err)
		}
		cancel()
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("etcd_put_seq"), b.N, b.Elapsed(), rec)
}

// BenchmarkEtcd_KVPut_Conc8 measures concurrent Put throughput with 8 writers.
func BenchmarkEtcd_KVPut_Conc8(b *testing.B) {
	cluster := SetupEtcdCluster(b)
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
				ctx, cancel := opCtx()
				t0 := time.Now()
				if _, err := cluster.client.Put(ctx, key, string(payload)); err != nil {
					cancel()
					b.Errorf("put: %v", err)
					return
				}
				cancel()
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	b.StopTimer()

	completed := int(counter.Load())
	if completed > b.N {
		completed = b.N
	}
	suite.AddResult(scenarioKey("etcd_put_conc8"), completed, elapsed, rec)
}

// ---------------------------------------------------------------------------
// GET (Range) benchmarks
// ---------------------------------------------------------------------------

// BenchmarkEtcd_KVGet_SEQ measures sequential Get throughput and latency.
func BenchmarkEtcd_KVGet_SEQ(b *testing.B) {
	cluster := SetupEtcdCluster(b)
	seedKeys(b, cluster.client, "bench-get-", payload)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-get-%d", i%seedPoolSize)
		ctx, cancel := opCtx()
		t0 := time.Now()
		if _, err := cluster.client.Get(ctx, key); err != nil {
			cancel()
			b.Fatalf("get: %v", err)
		}
		cancel()
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("etcd_get_seq"), b.N, b.Elapsed(), rec)
}

// BenchmarkEtcd_KVGet_Conc8 measures concurrent Get throughput with 8 readers.
func BenchmarkEtcd_KVGet_Conc8(b *testing.B) {
	cluster := SetupEtcdCluster(b)
	seedKeys(b, cluster.client, "bench-getc-", payload)
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
				ctx, cancel := opCtx()
				t0 := time.Now()
				if _, err := cluster.client.Get(ctx, key); err != nil {
					cancel()
					b.Errorf("get: %v", err)
					return
				}
				cancel()
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	b.StopTimer()

	completed := int(counter.Load())
	if completed > b.N {
		completed = b.N
	}
	suite.AddResult(scenarioKey("etcd_get_conc8"), completed, elapsed, rec)
}

// ---------------------------------------------------------------------------
// Delete benchmarks
// ---------------------------------------------------------------------------

// BenchmarkEtcd_KVDelete_SEQ measures sequential Delete throughput and latency.
func BenchmarkEtcd_KVDelete_SEQ(b *testing.B) {
	cluster := SetupEtcdCluster(b)
	seedKeys(b, cluster.client, "bench-del-", payload)
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-del-%d", i%seedPoolSize)
		ctx, cancel := opCtx()
		t0 := time.Now()
		if _, err := cluster.client.Delete(ctx, key); err != nil {
			cancel()
			b.Fatalf("delete: %v", err)
		}
		cancel()
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("etcd_delete_seq"), b.N, b.Elapsed(), rec)
}

// BenchmarkEtcd_KVDelete_Conc8 measures concurrent Delete throughput with 8 goroutines.
func BenchmarkEtcd_KVDelete_Conc8(b *testing.B) {
	cluster := SetupEtcdCluster(b)
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
				key := fmt.Sprintf("bench-delc-%d", i%seedPoolSize)
				ctx, cancel := opCtx()
				t0 := time.Now()
				if _, err := cluster.client.Delete(ctx, key); err != nil {
					cancel()
					b.Errorf("delete: %v", err)
					return
				}
				cancel()
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.AddResult(scenarioKey("etcd_delete_conc8"), rec.Count(), b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// Transaction benchmarks
// ---------------------------------------------------------------------------

// BenchmarkEtcd_KVTxn_SEQ measures sequential transaction throughput and latency.
// Each transaction checks whether a key exists and puts a new value if so
// (or unconditionally puts on first iteration via empty compare).
func BenchmarkEtcd_KVTxn_SEQ(b *testing.B) {
	cluster := SetupEtcdCluster(b)
	rec := perfresult.NewLatencyRecorder(b.N)

	// Seed the first key so the txn compare succeeds from iteration 1 onward.
	txnKey := "bench-txn-0"
	ctx, cancel := opCtx()
	if _, err := cluster.client.Put(ctx, txnKey, string(payload)); err != nil {
		cancel()
		b.Fatalf("txn seed: %v", err)
	}
	cancel()

	b.ResetTimer()
	for i := range b.N {
		src := fmt.Sprintf("bench-txn-%d", i)
		dst := fmt.Sprintf("bench-txn-%d", i+1)
		ctx, cancel := opCtx()
		t0 := time.Now()
		_, err := cluster.client.Txn(ctx).
			If(clientv3.Compare(clientv3.Version(src), ">", 0)).
			Then(clientv3.OpPut(dst, string(payload))).
			Commit()
		cancel()
		if err != nil {
			b.Fatalf("txn: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("etcd_txn_seq"), b.N, b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// Watch latency benchmark
// ---------------------------------------------------------------------------

// BenchmarkEtcd_WatchLatency measures the end-to-end latency from issuing a
// Put to receiving the corresponding watch event on a prefix watcher.
func BenchmarkEtcd_WatchLatency(b *testing.B) {
	cluster := SetupEtcdCluster(b)
	rec := perfresult.NewLatencyRecorder(b.N)

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	wch := cluster.client.Watch(watchCtx, "bench-watch-", clientv3.WithPrefix())

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-watch-%d", i)
		ctx, cancel := opCtx()
		t0 := time.Now()
		if _, err := cluster.client.Put(ctx, key, string(payload)); err != nil {
			cancel()
			b.Fatalf("put for watch: %v", err)
		}
		cancel()
		// Wait for the watch event corresponding to this put.
		select {
		case resp := <-wch:
			if resp.Err() != nil {
				b.Fatalf("watch: %v", resp.Err())
			}
			rec.Record(time.Since(t0))
		case <-time.After(10 * time.Second):
			b.Fatal("watch event timeout")
		}
	}
	b.StopTimer()

	suite.AddResult(scenarioKey("etcd_watch_latency"), b.N, b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// TestMain
// ---------------------------------------------------------------------------

// TestMain runs all benchmarks and writes the accumulated suite JSON to
// stdout with sentinel markers for the orchestrator to extract.
func TestMain(m *testing.M) {
	suite.Config.PayloadSize = defaultPayloadSize
	suite.Config.ConcurrentGoroutines = concurrency
	suite.Config.TLS = true

	exitCode := m.Run()

	fmt.Println()
	if err := perfresult.WriteSuiteToStdout(suite); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write benchmark suite: %v\n", err)
	}

	os.Exit(exitCode)
}
