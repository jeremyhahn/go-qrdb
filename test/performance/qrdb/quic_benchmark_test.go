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
	"crypto/tls"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	qtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
)

// createQUICTransport creates a new independent QUIC transport connected
// to the leader node. Used for per-goroutine connections in concurrent
// benchmarks to avoid stream contention on a single QUIC connection.
func createQUICTransport(b *testing.B, cluster *QRDBCluster) *qtransport.Transport {
	b.Helper()
	leaderQUICPort := cluster.quicPorts[0]
	for i, port := range cluster.adminPorts {
		if port == cluster.leaderPort {
			leaderQUICPort = cluster.quicPorts[i]
			break
		}
	}
	addr := fmt.Sprintf("127.0.0.1:%d", leaderQUICPort)
	qt, err := qtransport.New(
		transport.WithAddress(addr),
		transport.WithTLSConfig(&tls.Config{
			RootCAs:    cluster.caPool,
			MinVersion: tls.VersionTLS13,
			ServerName: "localhost",
		}),
	)
	if err != nil {
		b.Fatalf("createQUICTransport: new: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := qt.Connect(ctx); err != nil {
		b.Fatalf("createQUICTransport: connect %s: %v", addr, err)
	}
	return qt
}

// quicSeedKeys writes seedPoolSize keys via the native QUIC transport using
// 8 concurrent goroutines. Key format is "<prefix><index>" where index is
// 0..seedPoolSize-1.
func quicSeedKeys(b *testing.B, qt *qtransport.Transport, prefix string, value []byte) {
	b.Helper()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= seedPoolSize {
					return
				}
				key := fmt.Sprintf("%s%d", prefix, i)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := qt.Put(ctx, key, value)
				cancel()
				if err != nil {
					b.Errorf("quicSeedKeys %s: %v", key, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Native QUIC single-node (1N) benchmarks
// ---------------------------------------------------------------------------

// BenchmarkQRDB_QUIC_KVPut_SEQ benchmarks sequential PUT throughput
// via native QUIC transport against a single QRDB node.
func BenchmarkQRDB_QUIC_KVPut_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	qt := cluster.quicTransport
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-quic-put-%d", i)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		err := qt.Put(ctx, key, payload)
		cancel()
		if err != nil {
			b.Fatalf("quic put: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_quic_put_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_QUIC_KVPut_Conc8 benchmarks concurrent PUT throughput
// with 8 goroutines via native QUIC transport against a single QRDB node.
// Each goroutine uses its own QUIC connection to avoid stream contention.
func BenchmarkQRDB_QUIC_KVPut_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)

	transports := make([]*qtransport.Transport, concurrency)
	for j := range concurrency {
		qt := createQUICTransport(b, cluster)
		b.Cleanup(func() { qt.Close() })
		transports[j] = qt
	}

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	start := time.Now()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for j := range concurrency {
		wg.Add(1)
		go func(qt *qtransport.Transport) {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-quic-conc-%d", i)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				err := qt.Put(ctx, key, payload)
				cancel()
				if err != nil {
					b.Errorf("quic put: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}(transports[j])
	}
	wg.Wait()
	_ = time.Since(start)
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_quic_put_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_QUIC_KVGet_SEQ benchmarks sequential GET throughput
// via native QUIC transport against a single QRDB node.
func BenchmarkQRDB_QUIC_KVGet_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	qt := cluster.quicTransport

	// Seed a fixed pool of keys via QUIC before benchmarking reads.
	quicSeedKeys(b, qt, "bench-quic-get-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-quic-get-%d", i%seedPoolSize)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		_, err := qt.Get(ctx, key)
		cancel()
		if err != nil {
			b.Fatalf("quic get: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_quic_get_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_QUIC_KVGet_Conc8 benchmarks concurrent GET throughput
// with 8 goroutines via native QUIC transport against a single QRDB node.
// Each goroutine uses its own QUIC connection to avoid stream contention
// on a single connection, which is the intended deployment pattern for
// concurrent QUIC clients.
func BenchmarkQRDB_QUIC_KVGet_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)

	// Seed a fixed pool of keys via the shared transport.
	quicSeedKeys(b, cluster.quicTransport, "bench-quic-getc-", payload)

	// Create a dedicated QUIC transport per goroutine.
	transports := make([]*qtransport.Transport, concurrency)
	for j := range concurrency {
		qt := createQUICTransport(b, cluster)
		b.Cleanup(func() { qt.Close() })
		transports[j] = qt
	}

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	start := time.Now()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for j := range concurrency {
		wg.Add(1)
		go func(qt *qtransport.Transport) {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-quic-getc-%d", i%seedPoolSize)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				_, err := qt.Get(ctx, key)
				cancel()
				if err != nil {
					b.Errorf("quic get: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}(transports[j])
	}
	wg.Wait()
	_ = time.Since(start)
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_quic_get_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_QUIC_KVDelete_SEQ benchmarks sequential DELETE throughput
// via native QUIC transport against a single QRDB node.
func BenchmarkQRDB_QUIC_KVDelete_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	qt := cluster.quicTransport

	// Seed a fixed pool of keys via QUIC before benchmarking deletes.
	quicSeedKeys(b, qt, "bench-quic-del-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-quic-del-%d", i%seedPoolSize)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		err := qt.Delete(ctx, key)
		cancel()
		if err != nil {
			b.Fatalf("quic delete: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_quic_delete_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_QUIC_KVDelete_Conc8 benchmarks concurrent DELETE throughput
// with 8 goroutines via native QUIC transport against a single QRDB node.
// Each goroutine uses its own QUIC connection to avoid stream contention.
func BenchmarkQRDB_QUIC_KVDelete_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)

	// Seed via the shared transport.
	quicSeedKeys(b, cluster.quicTransport, "bench-quic-delc-", payload)

	transports := make([]*qtransport.Transport, concurrency)
	for j := range concurrency {
		qt := createQUICTransport(b, cluster)
		b.Cleanup(func() { qt.Close() })
		transports[j] = qt
	}

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	var counter atomic.Int64
	var wg sync.WaitGroup
	for j := range concurrency {
		wg.Add(1)
		go func(qt *qtransport.Transport) {
			defer wg.Done()
			for {
				i := counter.Add(1) - 1
				if i >= int64(b.N) {
					return
				}
				key := fmt.Sprintf("bench-quic-delc-%d", i%seedPoolSize)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				err := qt.Delete(ctx, key)
				cancel()
				if err != nil {
					b.Errorf("quic delete: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}(transports[j])
	}
	wg.Wait()
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_quic_delete_conc8", rec.Count(), b.Elapsed(), rec)
}
