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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// ---------------------------------------------------------------------------
// grpcCoalescer batches concurrent gRPC operations into Batch RPCs.
// ---------------------------------------------------------------------------

// grpcBatchItem is a single queued gRPC operation waiting for its batch.
type grpcBatchItem struct {
	op       *pb.Op
	ctx      context.Context
	resultCh chan grpcBatchResult
}

// grpcBatchResult carries the per-operation response from a flushed batch.
type grpcBatchResult struct {
	resp *pb.OpResponse
	err  error
}

// grpcCoalescer accumulates concurrent Put/Get/Delete calls and dispatches
// them as a single Batch RPC. This mirrors TiKV's BatchCommands pattern.
type grpcCoalescer struct {
	client   pb.QRDBServiceClient
	batchCh  chan grpcBatchItem
	maxBatch int
	window   time.Duration
	done     chan struct{}
	wg       sync.WaitGroup
}

// newGRPCCoalescer creates a coalescer that batches operations via the
// given gRPC client. maxBatch is the maximum operations per batch; window
// is the maximum wait before flushing a partial batch.
func newGRPCCoalescer(client pb.QRDBServiceClient, maxBatch int, window time.Duration) *grpcCoalescer {
	c := &grpcCoalescer{
		client:   client,
		batchCh:  make(chan grpcBatchItem, maxBatch*2),
		maxBatch: maxBatch,
		window:   window,
		done:     make(chan struct{}),
	}
	c.wg.Add(1)
	go c.drainLoop()
	return c
}

func (c *grpcCoalescer) drainLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.window)
	defer ticker.Stop()
	pending := make([]grpcBatchItem, 0, c.maxBatch)
	for {
		select {
		case item := <-c.batchCh:
			pending = append(pending, item)
			if len(pending) >= c.maxBatch {
				c.flush(pending)
				pending = pending[:0]
			}
		case <-ticker.C:
			if len(pending) > 0 {
				c.flush(pending)
				pending = pending[:0]
			}
		case <-c.done:
			for {
				select {
				case item := <-c.batchCh:
					pending = append(pending, item)
				default:
					if len(pending) > 0 {
						c.flush(pending)
					}
					return
				}
			}
		}
	}
}

func (c *grpcCoalescer) flush(items []grpcBatchItem) {
	ops := make([]*pb.Op, len(items))
	for i := range items {
		ops[i] = items[i].op
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.client.Batch(ctx, &pb.BatchRequest{Ops: ops})
	for i := range items {
		if err != nil {
			items[i].resultCh <- grpcBatchResult{err: err}
			continue
		}
		if i < len(resp.GetResponses()) {
			items[i].resultCh <- grpcBatchResult{resp: resp.GetResponses()[i]}
		} else {
			items[i].resultCh <- grpcBatchResult{err: fmt.Errorf("batch response missing index %d", i)}
		}
	}
}

func (c *grpcCoalescer) submit(ctx context.Context, op *pb.Op) (*pb.OpResponse, error) {
	resultCh := make(chan grpcBatchResult, 1)
	select {
	case c.batchCh <- grpcBatchItem{op: op, ctx: ctx, resultCh: resultCh}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-resultCh:
		return r.resp, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *grpcCoalescer) close() {
	close(c.done)
	c.wg.Wait()
}

// grpcSeedKeys writes seedPoolSize keys via gRPC Put using 8 concurrent
// goroutines. Key format is "<prefix><index>" where index is 0..seedPoolSize-1.
func grpcSeedKeys(b *testing.B, client pb.QRDBServiceClient, prefix string, value []byte) {
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
				_, err := client.Put(ctx, &pb.PutRequest{Key: key, Value: value})
				cancel()
				if err != nil {
					b.Errorf("grpcSeedKeys %s: %v", key, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// gRPC single-node (1N) benchmarks
// ---------------------------------------------------------------------------

// BenchmarkQRDB_GRPC_KVPut_SEQ benchmarks sequential PUT throughput
// via gRPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVPut_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient
	rec := perfresult.NewLatencyRecorder(b.N)

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-grpc-put-%d", i)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		_, err := client.Put(ctx, &pb.PutRequest{Key: key, Value: payload})
		cancel()
		if err != nil {
			b.Fatalf("grpc put: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_put_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVPut_Conc8 benchmarks concurrent PUT throughput
// with 8 goroutines via gRPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVPut_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient
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
				key := fmt.Sprintf("bench-grpc-conc-%d", i)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				_, err := client.Put(ctx, &pb.PutRequest{Key: key, Value: payload})
				cancel()
				if err != nil {
					b.Errorf("grpc put: %v", err)
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
	suite.AddResult("kv_grpc_put_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVGet_SEQ benchmarks sequential GET throughput
// via gRPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVGet_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient

	// Seed a fixed pool of keys via gRPC before benchmarking reads.
	grpcSeedKeys(b, client, "bench-grpc-get-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-grpc-get-%d", i%seedPoolSize)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		_, err := client.Get(ctx, &pb.GetRequest{Key: key})
		cancel()
		if err != nil {
			b.Fatalf("grpc get: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_get_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVGet_Conc8 benchmarks concurrent GET throughput
// with 8 goroutines via gRPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVGet_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient

	// Seed a fixed pool of keys via gRPC before benchmarking reads.
	grpcSeedKeys(b, client, "bench-grpc-getc-", payload)

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
				key := fmt.Sprintf("bench-grpc-getc-%d", i%seedPoolSize)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				_, err := client.Get(ctx, &pb.GetRequest{Key: key})
				cancel()
				if err != nil {
					b.Errorf("grpc get: %v", err)
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
	suite.AddResult("kv_grpc_get_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVDelete_SEQ benchmarks sequential DELETE throughput
// via gRPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVDelete_SEQ(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient

	// Seed a fixed pool of keys via gRPC before benchmarking deletes.
	grpcSeedKeys(b, client, "bench-grpc-del-", payload)

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-grpc-del-%d", i%seedPoolSize)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		_, err := client.Delete(ctx, &pb.DeleteRequest{Key: key})
		cancel()
		if err != nil {
			b.Fatalf("grpc delete: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_delete_seq", b.N, b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVDelete_Conc8 benchmarks concurrent DELETE throughput
// with 8 goroutines via gRPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVDelete_Conc8(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient

	// Seed a fixed pool of keys via gRPC before benchmarking deletes.
	grpcSeedKeys(b, client, "bench-grpc-delc-", payload)

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
				key := fmt.Sprintf("bench-grpc-delc-%d", i%seedPoolSize)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				_, err := client.Delete(ctx, &pb.DeleteRequest{Key: key})
				cancel()
				if err != nil {
					b.Errorf("grpc delete: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_delete_conc8", rec.Count(), b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVBatchPut benchmarks bulk PUT throughput by
// submitting a single atomic Batch RPC with 10K operations per iteration.
func BenchmarkQRDB_GRPC_KVBatchPut(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient
	totalOps := b.N * batchSize
	rec := perfresult.NewLatencyRecorder(totalOps)

	// Pre-build the ops slice once; keys differ per iteration by prefix.
	ops := make([]*pb.Op, batchSize)
	for j := range ops {
		ops[j] = &pb.Op{
			Type:  pb.OpType_OP_PUT,
			Key:   []byte(fmt.Sprintf("bench-grpc-batch-0-%d", j)),
			Value: payload,
		}
	}

	b.ResetTimer()
	for i := range b.N {
		// Update keys for this iteration so each write is unique.
		for j := range ops {
			ops[j].Key = []byte(fmt.Sprintf("bench-grpc-batch-%d-%d", i, j))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		t0 := time.Now()
		_, err := client.Batch(ctx, &pb.BatchRequest{Ops: ops})
		cancel()
		if err != nil {
			b.Fatalf("grpc batch: %v", err)
		}
		elapsed := time.Since(t0)
		// Record one latency sample per op within the batch.
		perOp := elapsed / time.Duration(batchSize)
		for range batchSize {
			rec.Record(perOp)
		}
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_batch_put_10k", totalOps, b.Elapsed(), rec)
}

// BenchmarkQRDB_GRPC_KVScan benchmarks prefix scan throughput over
// 10K pre-seeded keys via gRPC Scan RPC against a single QRDB node.
func BenchmarkQRDB_GRPC_KVScan(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	client := cluster.grpcClient

	// Seed 10K keys with a common prefix via gRPC.
	for i := range scanSize {
		key := fmt.Sprintf("grpc-scan/%05d", i)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := client.Put(ctx, &pb.PutRequest{Key: key, Value: payload})
		cancel()
		if err != nil {
			b.Fatalf("seed grpc scan: %v", err)
		}
	}

	rec := perfresult.NewLatencyRecorder(b.N)
	b.ResetTimer()
	for range b.N {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t0 := time.Now()
		_, err := client.Scan(ctx, &pb.ScanRequest{Prefix: "grpc-scan/"})
		cancel()
		if err != nil {
			b.Fatalf("grpc scan: %v", err)
		}
		rec.Record(time.Since(t0))
	}
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_scan_10k", b.N, b.Elapsed(), rec)
}

// ---------------------------------------------------------------------------
// Coalesced gRPC benchmarks — concurrent ops batched via Batch RPC
// ---------------------------------------------------------------------------

// BenchmarkQRDB_GRPC_KVPut_Coalesced benchmarks concurrent PUT throughput
// with 8 goroutines using client-side batch coalescing (TiKV BatchCommands
// pattern). Individual Put calls are automatically accumulated and flushed
// as single Batch RPCs by the coalescer.
func BenchmarkQRDB_GRPC_KVPut_Coalesced(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	coal := newGRPCCoalescer(cluster.grpcClient, 256, time.Millisecond)
	defer coal.close()
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
				key := fmt.Sprintf("bench-coal-put-%d", i)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				_, err := coal.submit(ctx, &pb.Op{
					Type:  pb.OpType_OP_PUT,
					Key:   []byte(key),
					Value: payload,
				})
				cancel()
				if err != nil {
					b.Errorf("coalesced put: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_put_coalesced", rec.Count(), b.Elapsed(), rec)
}

// NOTE: Coalesced GET is not benchmarked because the server's Batch RPC
// routes GET operations through MVCCGetQuery which is incompatible with
// the raw PebbleDB state machine used in benchmarks. This is a server-side
// issue in pkg/service/batch_service.go that should use the KV read path
// for non-MVCC state machines.

// BenchmarkQRDB_GRPC_KVDelete_Coalesced benchmarks concurrent DELETE
// throughput with 8 goroutines using client-side batch coalescing.
func BenchmarkQRDB_GRPC_KVDelete_Coalesced(b *testing.B) {
	cluster := SetupQRDBCluster(b, 1)
	grpcSeedKeys(b, cluster.grpcClient, "bench-coal-del-", payload)
	coal := newGRPCCoalescer(cluster.grpcClient, 256, time.Millisecond)
	defer coal.close()
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
				key := fmt.Sprintf("bench-coal-del-%d", i%seedPoolSize)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				t0 := time.Now()
				_, err := coal.submit(ctx, &pb.Op{
					Type: pb.OpType_OP_DELETE,
					Key:  []byte(key),
				})
				cancel()
				if err != nil {
					b.Errorf("coalesced delete: %v", err)
					return
				}
				rec.Record(time.Since(t0))
			}
		}()
	}
	wg.Wait()
	b.StopTimer()

	suite.Config.TLS = true
	suite.AddResult("kv_grpc_delete_coalesced", rec.Count(), b.Elapsed(), rec)
}
