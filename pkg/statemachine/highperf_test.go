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

package statemachine

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// HighPerfBackend unit tests
// ---------------------------------------------------------------------------

func TestHighPerfBackend_DefaultShardCount(t *testing.T) {
	b := NewHighPerfBackend(nil)
	assert.Equal(t, 256, b.ShardCount())
}

func TestHighPerfBackend_ConfigurableShardCount(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		expected int
	}{
		{name: "16 shards", count: 16, expected: 16},
		{name: "256 shards", count: 256, expected: 256},
		{name: "1024 shards", count: 1024, expected: 1024},
		{name: "non power of 2 falls back", count: 17, expected: 256},
		{name: "zero falls back", count: 0, expected: 256},
		{name: "negative falls back", count: -1, expected: 256},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewHighPerfBackend(&HighPerfConfig{ShardCount: tt.count})
			assert.Equal(t, tt.expected, b.ShardCount())
		})
	}
}

func TestHighPerfBackend_PutGet(t *testing.T) {
	b := NewHighPerfBackend(nil)

	err := b.Put([]byte("key1"), []byte("value1"))
	require.NoError(t, err)

	val, err := b.Get([]byte("key1"))
	require.NoError(t, err)
	assert.Equal(t, []byte("value1"), val)
}

func TestHighPerfBackend_GetMissing(t *testing.T) {
	b := NewHighPerfBackend(nil)

	val, err := b.Get([]byte("nonexistent"))
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestHighPerfBackend_Delete(t *testing.T) {
	b := NewHighPerfBackend(nil)

	err := b.Put([]byte("key1"), []byte("value1"))
	require.NoError(t, err)

	err = b.Delete([]byte("key1"))
	require.NoError(t, err)

	val, err := b.Get([]byte("key1"))
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestHighPerfBackend_DeleteNonexistent(t *testing.T) {
	b := NewHighPerfBackend(nil)

	err := b.Delete([]byte("nonexistent"))
	require.NoError(t, err)
}

func TestHighPerfBackend_List(t *testing.T) {
	b := NewHighPerfBackend(nil)

	require.NoError(t, b.Put([]byte("prefix/a"), []byte("1")))
	require.NoError(t, b.Put([]byte("prefix/b"), []byte("2")))
	require.NoError(t, b.Put([]byte("other/c"), []byte("3")))

	keys, err := b.List([]byte("prefix/"))
	require.NoError(t, err)
	assert.Len(t, keys, 2)

	// All keys returned.
	allKeys, err := b.List(nil)
	require.NoError(t, err)
	assert.Len(t, allKeys, 3)
}

func TestHighPerfBackend_Scan(t *testing.T) {
	b := NewHighPerfBackend(nil)

	require.NoError(t, b.Put([]byte("scan/a"), []byte("1")))
	require.NoError(t, b.Put([]byte("scan/b"), []byte("2")))
	require.NoError(t, b.Put([]byte("scan/c"), []byte("3")))
	require.NoError(t, b.Put([]byte("other/d"), []byte("4")))

	result, err := b.Scan([]byte("scan/"), 0)
	require.NoError(t, err)
	assert.Len(t, result, 3)

	// Test with limit.
	limited, err := b.Scan([]byte("scan/"), 2)
	require.NoError(t, err)
	assert.Len(t, limited, 2)
}

func TestHighPerfBackend_GetReturnsCopy(t *testing.T) {
	b := NewHighPerfBackend(nil)

	require.NoError(t, b.Put([]byte("key"), []byte("original")))

	val, err := b.Get([]byte("key"))
	require.NoError(t, err)

	// Mutate the returned value.
	val[0] = 'X'

	// Internal state should be unchanged.
	val2, err := b.Get([]byte("key"))
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), val2)
}

func TestHighPerfBackend_SnapshotRoundTrip(t *testing.T) {
	b := NewHighPerfBackend(&HighPerfConfig{ShardCount: 16})

	// Populate data.
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key-%03d", i)
		val := fmt.Sprintf("value-%03d", i)
		require.NoError(t, b.Put([]byte(key), []byte(val)))
	}

	// Save snapshot.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	err := b.SaveSnapshot(&buf, stopper)
	require.NoError(t, err)

	// Recover into a new backend.
	b2 := NewHighPerfBackend(&HighPerfConfig{ShardCount: 16})
	err = b2.RecoverSnapshot(&buf, stopper)
	require.NoError(t, err)

	// Verify all data restored.
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key-%03d", i)
		expected := fmt.Sprintf("value-%03d", i)
		val, err := b2.Get([]byte(key))
		require.NoError(t, err)
		assert.Equal(t, []byte(expected), val)
	}
}

func TestHighPerfBackend_SnapshotCancellation(t *testing.T) {
	b := NewHighPerfBackend(nil)

	for i := 0; i < 1000; i++ {
		require.NoError(t, b.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")))
	}

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err := b.SaveSnapshot(&buf, stopper)
	// Either completes or returns cancellation error.
	if err != nil {
		var snapErr *SnapshotError
		assert.ErrorAs(t, err, &snapErr)
	}
}

func TestHighPerfBackend_GetHash(t *testing.T) {
	b1 := NewHighPerfBackend(&HighPerfConfig{ShardCount: 16})
	b2 := NewHighPerfBackend(&HighPerfConfig{ShardCount: 16})

	// Same data should produce same hash.
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key-%d", i)
		val := fmt.Sprintf("val-%d", i)
		require.NoError(t, b1.Put([]byte(key), []byte(val)))
		require.NoError(t, b2.Put([]byte(key), []byte(val)))
	}

	h1, err := b1.GetHash()
	require.NoError(t, err)
	h2, err := b2.GetHash()
	require.NoError(t, err)
	assert.Equal(t, h1, h2)

	// Different data should produce different hash.
	require.NoError(t, b2.Put([]byte("extra"), []byte("data")))
	h3, err := b2.GetHash()
	require.NoError(t, err)
	assert.NotEqual(t, h1, h3)
}

func TestHighPerfBackend_Close(t *testing.T) {
	b := NewHighPerfBackend(nil)
	require.NoError(t, b.Put([]byte("key"), []byte("val")))
	require.NoError(t, b.Close())

	// After close, shard data is nil -- reading from nil map returns
	// zero value (nil) without error in Go.
	val, err := b.Get([]byte("key"))
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestHighPerfBackend_BytesLookup(t *testing.T) {
	b := NewHighPerfBackend(nil)
	require.NoError(t, b.Put([]byte("key"), []byte("value")))

	val, err := b.BytesLookup([]byte("key"))
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)

	// Missing key.
	val, err = b.BytesLookup([]byte("missing"))
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestHighPerfBackend_BytesLookupInto(t *testing.T) {
	b := NewHighPerfBackend(nil)
	require.NoError(t, b.Put([]byte("key"), []byte("value")))

	// Buffer large enough.
	dst := make([]byte, 0, 64)
	val, err := b.BytesLookupInto([]byte("key"), dst)
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)

	// Buffer too small, should allocate.
	tinyDst := make([]byte, 0, 1)
	val, err = b.BytesLookupInto([]byte("key"), tinyDst)
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)

	// Missing key.
	val, err = b.BytesLookupInto([]byte("missing"), dst)
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestHighPerfBackend_ConcurrentReadWrite(t *testing.T) {
	b := NewHighPerfBackend(&HighPerfConfig{ShardCount: 256})
	const numOps = 10000

	var wg sync.WaitGroup

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			key := fmt.Sprintf("key-%d", i)
			val := fmt.Sprintf("val-%d", i)
			_ = b.Put([]byte(key), []byte(val))
		}
	}()

	// Reader goroutines.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				key := fmt.Sprintf("key-%d", i%1000)
				_, _ = b.Get([]byte(key))
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// HighPerfAdapter (via KVStateMachine) integration tests
// ---------------------------------------------------------------------------

func TestHighPerfAdapter_UpdateAndLookup(t *testing.T) {
	backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 64})
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	// Build a put command: [opPut][keyLen:2 LE][key][value]
	key := []byte("test-key")
	value := []byte("test-value")
	cmd := buildPutCmd(key, value)

	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := adapter.Update(ctx, entries, results)
	require.NoError(t, err)
	assert.NotZero(t, results[0].Value)

	// Lookup via adapter.
	val, err := adapter.Lookup(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, value, val)
}

func TestHighPerfAdapter_DeleteAndLookup(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	// Insert.
	putCmd := buildPutCmd([]byte("del-key"), []byte("del-value"))
	entries := []sm.Entry{{Cmd: putCmd}}
	results := make([]sm.Result, 1)
	require.NoError(t, adapter.Update(ctx, entries, results))

	// Delete.
	delCmd := buildDeleteCmd([]byte("del-key"))
	entries = []sm.Entry{{Cmd: delCmd}}
	results = make([]sm.Result, 1)
	require.NoError(t, adapter.Update(ctx, entries, results))

	// Verify deleted.
	val, err := adapter.Lookup(ctx, []byte("del-key"))
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestHighPerfAdapter_PrepareSnapshot(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	snapCtx, err := adapter.PrepareSnapshot()
	require.NoError(t, err)
	assert.Equal(t, "highperf-snapshot", snapCtx)
}

func TestHighPerfAdapter_SnapshotRoundTrip(t *testing.T) {
	backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 32})
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	// Insert data via Update.
	for i := 0; i < 50; i++ {
		key := []byte(fmt.Sprintf("snap-key-%d", i))
		value := []byte(fmt.Sprintf("snap-val-%d", i))
		cmd := buildPutCmd(key, value)
		entries := []sm.Entry{{Cmd: cmd}}
		results := make([]sm.Result, 1)
		require.NoError(t, adapter.Update(ctx, entries, results))
	}

	// Save snapshot.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	snapCtx, err := adapter.PrepareSnapshot()
	require.NoError(t, err)
	err = adapter.SaveSnapshot(ctx, snapCtx, &buf, stopper)
	require.NoError(t, err)

	// Recover into new adapter.
	backend2 := NewHighPerfBackend(&HighPerfConfig{ShardCount: 32})
	kvsm2 := NewKVStateMachine(backend2)
	adapter2 := &HighPerfAdapter{KVStateMachine: kvsm2, backend: backend2}

	err = adapter2.RecoverFromSnapshot(ctx, &buf, stopper)
	require.NoError(t, err)

	// Verify all data.
	for i := 0; i < 50; i++ {
		key := []byte(fmt.Sprintf("snap-key-%d", i))
		expected := []byte(fmt.Sprintf("snap-val-%d", i))
		val, err := adapter2.Lookup(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, expected, val)
	}
}

func TestHighPerfAdapter_NALookup(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	key := []byte("na-key")
	value := []byte("na-value")
	cmd := buildPutCmd(key, value)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	require.NoError(t, adapter.Update(ctx, entries, results))

	// BytesLookup (NALookup interface).
	val, err := adapter.BytesLookup(key)
	require.NoError(t, err)
	assert.Equal(t, value, val)

	// BytesLookupInto (NALookupInto interface).
	dst := make([]byte, 0, 64)
	val, err = adapter.BytesLookupInto(key, dst)
	require.NoError(t, err)
	assert.Equal(t, value, val)
}

func TestHighPerfAdapter_InvalidOpcode(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	// Unknown opcode 0xFF.
	entries := []sm.Entry{{Cmd: []byte{0xFF, 0x01, 0x02}}}
	results := make([]sm.Result, 1)
	assertOpcodeRefused(t, adapter.Update(ctx, entries, results), results[0])
	assertRefusalNamesOpcode(t, results[0], 0xFF)
}

func TestHighPerfAdapter_EmptyEntry(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	entries := []sm.Entry{{Cmd: nil}}
	results := make([]sm.Result, 1)
	err := adapter.Update(ctx, entries, results)
	require.NoError(t, err)
	assert.Equal(t, sm.Result{}, results[0])
}

func TestHighPerfAdapter_Close(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	err := adapter.Close(context.Background())
	require.NoError(t, err)
}

func TestHighPerfAdapter_GetHash(t *testing.T) {
	backend := NewHighPerfBackend(nil)
	kvsm := NewKVStateMachine(backend)
	adapter := &HighPerfAdapter{KVStateMachine: kvsm, backend: backend}

	ctx := context.Background()

	cmd := buildPutCmd([]byte("hash-key"), []byte("hash-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	require.NoError(t, adapter.Update(ctx, entries, results))

	h, err := adapter.GetHash()
	require.NoError(t, err)
	assert.NotZero(t, h)
}

// ---------------------------------------------------------------------------
// Registry integration test
// ---------------------------------------------------------------------------

func TestHighPerfRegistry(t *testing.T) {
	assert.True(t, Has("highperf"))

	desc, err := Describe("highperf")
	require.NoError(t, err)
	assert.Equal(t, "highperf", desc.Name)
	assert.Equal(t, PersistenceMemory, desc.Persistence)
	assert.Equal(t, SMKindConcurrent, desc.Kind)
	assert.Contains(t, desc.Capabilities, CapabilityKV)
	assert.Contains(t, desc.Capabilities, CapabilityBatch)
}

func TestHighPerfRegistry_Resolve(t *testing.T) {
	createFunc, err := Resolve("highperf")
	require.NoError(t, err)
	assert.NotNil(t, createFunc)
}

// ---------------------------------------------------------------------------
// isPowerOfTwo tests
// ---------------------------------------------------------------------------

func TestIsPowerOfTwo(t *testing.T) {
	tests := []struct {
		n      int
		expect bool
	}{
		{0, false},
		{1, true},
		{2, true},
		{3, false},
		{4, true},
		{7, false},
		{8, true},
		{16, true},
		{17, false},
		{256, true},
		{1024, true},
		{-1, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.n), func(t *testing.T) {
			assert.Equal(t, tt.expect, isPowerOfTwo(tt.n))
		})
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkHighPerfLookup(b *testing.B) {
	backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 256})

	// Pre-populate 10K keys.
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("bench-key-%06d", i)
		val := fmt.Sprintf("bench-val-%06d", i)
		_ = backend.Put([]byte(key), []byte(val))
	}

	key := []byte("bench-key-005000")
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = backend.Get(key)
	}
}

func BenchmarkHighPerfBytesLookup(b *testing.B) {
	backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 256})

	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("bench-key-%06d", i)
		val := fmt.Sprintf("bench-val-%06d", i)
		_ = backend.Put([]byte(key), []byte(val))
	}

	key := []byte("bench-key-005000")
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = backend.BytesLookup(key)
	}
}

func BenchmarkHighPerfInsert(b *testing.B) {
	backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 256})

	keys := make([][]byte, b.N)
	vals := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = []byte(fmt.Sprintf("bench-key-%08d", i))
		vals[i] = []byte(fmt.Sprintf("bench-val-%08d", i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = backend.Put(keys[i], vals[i])
	}
}

func BenchmarkHighPerfConcurrentReadWrite(b *testing.B) {
	backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 256})

	// Pre-populate.
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("bench-key-%06d", i)
		val := fmt.Sprintf("bench-val-%06d", i)
		_ = backend.Put([]byte(key), []byte(val))
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%4 == 0 {
				// 25% writes.
				key := fmt.Sprintf("bench-key-%06d", i%10000)
				val := fmt.Sprintf("bench-val-%06d", i)
				_ = backend.Put([]byte(key), []byte(val))
			} else {
				// 75% reads.
				key := fmt.Sprintf("bench-key-%06d", i%10000)
				_, _ = backend.Get([]byte(key))
			}
			i++
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildPutCmd builds a Raft log entry command for a KV Put operation.
// Format: [opPut][keyLen:2 LE][key][value]
func buildPutCmd(key, value []byte) []byte {
	cmd := make([]byte, 1+2+len(key)+len(value))
	cmd[0] = kv.OpPut
	binary.LittleEndian.PutUint16(cmd[1:3], uint16(len(key)))
	copy(cmd[3:3+len(key)], key)
	copy(cmd[3+len(key):], value)
	return cmd
}

// buildDeleteCmd builds a Raft log entry command for a KV Delete operation.
// Format: [opDelete][keyLen:2 LE][key]
func buildDeleteCmd(key []byte) []byte {
	cmd := make([]byte, 1+2+len(key))
	cmd[0] = kv.OpDelete
	binary.LittleEndian.PutUint16(cmd[1:3], uint16(len(key)))
	copy(cmd[3:], key)
	return cmd
}
