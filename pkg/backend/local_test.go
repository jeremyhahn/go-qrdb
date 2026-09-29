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

package backend

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestLocalBackend() *LocalBackend {
	ssm := NewSimpleStateMachine(memory.NewStorage(), nil)
	return NewLocalBackend(ssm)
}

// ---------------------------------------------------------------------------
// ID tests
// ---------------------------------------------------------------------------

func TestLocalBackend_ID(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if lb.ID() != "local" {
		t.Fatalf("expected ID 'local', got %q", lb.ID())
	}
}

func TestLocalBackend_ID_Constant(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if lb.ID() == "" {
		t.Fatal("ID should return a non-empty string")
	}
}

// ---------------------------------------------------------------------------
// RaftAddress tests
// ---------------------------------------------------------------------------

func TestLocalBackend_RaftAddress(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if lb.RaftAddress() != "local://embedded" {
		t.Fatalf("expected 'local://embedded', got %q", lb.RaftAddress())
	}
}

func TestLocalBackend_RaftAddress_Constant(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if lb.RaftAddress() == "" {
		t.Fatal("RaftAddress should return a non-empty string")
	}
}

// ---------------------------------------------------------------------------
// ShardCount tests
// ---------------------------------------------------------------------------

func TestLocalBackend_ShardCount(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if lb.ShardCount() != 1 {
		t.Fatalf("expected ShardCount 1, got %d", lb.ShardCount())
	}
}

func TestLocalBackend_ShardCount_AlwaysOne(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	for range 3 {
		if lb.ShardCount() != 1 {
			t.Fatal("ShardCount should always be 1")
		}
	}
}

// ---------------------------------------------------------------------------
// GetNodeHostInfo tests
// ---------------------------------------------------------------------------

func TestLocalBackend_GetNodeHostInfo(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	info := lb.GetNodeHostInfo(quicraft.NodeHostInfoOption{})
	if info == nil {
		t.Fatal("expected non-nil NodeHostInfo")
	}
}

func TestLocalBackend_GetNodeHostInfo_SkipLogInfo(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	info := lb.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	if info == nil {
		t.Fatal("expected non-nil NodeHostInfo with SkipLogInfo")
	}
}

// ---------------------------------------------------------------------------
// HealthCheck tests
// ---------------------------------------------------------------------------

func TestLocalBackend_HealthCheck(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	health := lb.HealthCheck()
	if health == nil {
		t.Fatal("expected non-nil HealthStatus")
	}
	if health.Status != "ok" {
		t.Fatalf("expected Status 'ok', got %q", health.Status)
	}
}

func TestLocalBackend_HealthCheck_DiskOK(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	health := lb.HealthCheck()
	if !health.DiskOK {
		t.Fatal("expected DiskOK true")
	}
}

// ---------------------------------------------------------------------------
// Drain tests
// ---------------------------------------------------------------------------

func TestLocalBackend_Drain(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if err := lb.Drain(); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func TestLocalBackend_Drain_Idempotent(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	for range 3 {
		if err := lb.Drain(); err != nil {
			t.Fatalf("Drain: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// IsDraining tests
// ---------------------------------------------------------------------------

func TestLocalBackend_IsDraining(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	if lb.IsDraining() {
		t.Fatal("expected IsDraining false")
	}
}

func TestLocalBackend_IsDraining_AfterDrain(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	_ = lb.Drain()
	if lb.IsDraining() {
		t.Fatal("expected IsDraining false even after Drain (no-op)")
	}
}

// ---------------------------------------------------------------------------
// SyncPropose tests
// ---------------------------------------------------------------------------

func TestLocalBackend_SyncPropose(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	result, err := lb.SyncPropose(ctx, 1, cmd)
	if err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}
	if result.Value != uint64(len("v1")) {
		t.Fatalf("expected result.Value=%d, got %d", len("v1"), result.Value)
	}
}

func TestLocalBackend_SyncPropose_ReadBack(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	val, err := lb.SyncRead(ctx, 1, "k1")
	if err != nil {
		t.Fatalf("SyncRead: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v1")) {
		t.Fatalf("expected 'v1', got %q", val)
	}
}

func TestLocalBackend_SyncPropose_AfterClose(t *testing.T) {
	lb := newTestLocalBackend()
	_ = lb.Close()

	cmd, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	_, err = lb.SyncPropose(context.Background(), 1, cmd)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var bce *BackendClosedError
	if !errors.As(err, &bce) {
		t.Fatalf("expected BackendClosedError, got %T: %v", err, err)
	}
}

func TestLocalBackend_SyncPropose_MonotonicIndex(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	for i := range 5 {
		cmd, err := kv.EncodePut([]byte("k"), []byte{byte(i)})
		if err != nil {
			t.Fatalf("EncodePut #%d: %v", i, err)
		}
		if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
			t.Fatalf("SyncPropose #%d: %v", i, err)
		}
	}

	idx := lb.index.Load()
	if idx != 5 {
		t.Fatalf("expected index 5, got %d", idx)
	}
}

// ---------------------------------------------------------------------------
// SyncRead tests
// ---------------------------------------------------------------------------

func TestLocalBackend_SyncRead(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	val, err := lb.SyncRead(ctx, 1, "k1")
	if err != nil {
		t.Fatalf("SyncRead: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v1")) {
		t.Fatalf("expected 'v1', got %q", val)
	}
}

func TestLocalBackend_SyncRead_AfterClose(t *testing.T) {
	lb := newTestLocalBackend()
	_ = lb.Close()

	_, err := lb.SyncRead(context.Background(), 1, "k1")
	if err == nil {
		t.Fatal("expected error after Close")
	}
	var bce *BackendClosedError
	if !errors.As(err, &bce) {
		t.Fatalf("expected BackendClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Cluster operation tests (should all return ClusterOperationError)
// ---------------------------------------------------------------------------

func TestLocalBackend_RequestAddNode(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	if lb.NodeIdentity() != "" {
		t.Fatal("a local backend has no transport and so no node identity")
	}
	if _, err := lb.SyncGetShardMembership(context.Background(), 1); err == nil {
		t.Fatal("a local backend has no Raft membership to read")
	}
	_, err := lb.RequestAddNodeWithIdentity(context.Background(), 1, 2, "addr", "", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	var coe *ClusterOperationError
	if !errors.As(err, &coe) {
		t.Fatalf("expected ClusterOperationError, got %T: %v", err, err)
	}
}

func TestLocalBackend_RequestAddNode_ReturnsNilState(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	rs, _ := lb.RequestAddNodeWithIdentity(context.Background(), 1, 2, "addr", "", 0)
	if rs != nil {
		t.Fatal("expected nil RequestState")
	}
}

func TestLocalBackend_RequestRemoveNode(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	_, err := lb.RequestRemoveNode(context.Background(), 1, 2, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	var coe *ClusterOperationError
	if !errors.As(err, &coe) {
		t.Fatalf("expected ClusterOperationError, got %T: %v", err, err)
	}
}

func TestLocalBackend_RequestRemoveNode_ReturnsNilState(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	rs, _ := lb.RequestRemoveNode(context.Background(), 1, 2, 0)
	if rs != nil {
		t.Fatal("expected nil RequestState")
	}
}

func TestLocalBackend_RequestSnapshot(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	_, err := lb.RequestSnapshot(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error")
	}
	var coe *ClusterOperationError
	if !errors.As(err, &coe) {
		t.Fatalf("expected ClusterOperationError, got %T: %v", err, err)
	}
}

func TestLocalBackend_RequestSnapshot_ReturnsNilState(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	rs, _ := lb.RequestSnapshot(context.Background(), 1)
	if rs != nil {
		t.Fatal("expected nil RequestState")
	}
}

// ---------------------------------------------------------------------------
// ProposeWithMode tests
// ---------------------------------------------------------------------------

func TestLocalBackend_ProposeWithMode_Sync(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	result, err := lb.ProposeWithMode(ctx, 1, cmd, writemode.Sync)
	if err != nil {
		t.Fatalf("ProposeWithMode(Sync): %v", err)
	}
	if result.Value != uint64(len("v1")) {
		t.Fatalf("expected result.Value=%d, got %d", len("v1"), result.Value)
	}
}

func TestLocalBackend_ProposeWithMode_Batched(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	_, err = lb.ProposeWithMode(ctx, 1, cmd, writemode.Batched)
	if err != nil {
		t.Fatalf("ProposeWithMode(Batched): %v", err)
	}

	val, err := lb.SyncRead(ctx, 1, "k1")
	if err != nil {
		t.Fatalf("SyncRead after Batched: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v1")) {
		t.Fatalf("expected 'v1', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// StaleRead tests
// ---------------------------------------------------------------------------

func TestLocalBackend_StaleRead(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	val, err := lb.StaleRead(ctx, 1, "k1")
	if err != nil {
		t.Fatalf("StaleRead: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v1")) {
		t.Fatalf("expected 'v1', got %q", val)
	}
}

func TestLocalBackend_StaleRead_SameAsSyncRead(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	syncVal, _ := lb.SyncRead(ctx, 1, "k1")
	staleVal, _ := lb.StaleRead(ctx, 1, "k1")
	if !bytes.Equal(syncVal.([]byte), staleVal.([]byte)) {
		t.Fatal("StaleRead and SyncRead should return identical results")
	}
}

// ---------------------------------------------------------------------------
// ReadWithConsistency tests
// ---------------------------------------------------------------------------

func TestLocalBackend_ReadWithConsistency_Strong(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	val, err := lb.ReadWithConsistency(ctx, 1, "k1", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("ReadWithConsistency: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v1")) {
		t.Fatalf("expected 'v1', got %q", val)
	}
}

func TestLocalBackend_ReadWithConsistency_AllowStale(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	val, err := lb.ReadWithConsistency(ctx, 1, "k1", consistency.ReadOptions{AllowStale: true})
	if err != nil {
		t.Fatalf("ReadWithConsistency(AllowStale): %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v1")) {
		t.Fatalf("expected 'v1', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// GetLeaderID tests
// ---------------------------------------------------------------------------

func TestLocalBackend_GetLeaderID(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	id, ok, err := lb.GetLeaderID(1)
	if err != nil {
		t.Fatalf("GetLeaderID: %v", err)
	}
	if !ok {
		t.Fatal("expected leader known")
	}
	if id != 1 {
		t.Fatalf("expected leader ID 1, got %d", id)
	}
}

func TestLocalBackend_GetLeaderID_AlwaysLeader(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	// Any shard ID returns the same result.
	for _, shardID := range []uint64{1, 42, 999} {
		id, ok, err := lb.GetLeaderID(shardID)
		if err != nil || !ok || id != 1 {
			t.Fatalf("GetLeaderID(%d): id=%d ok=%v err=%v", shardID, id, ok, err)
		}
	}
}

// ---------------------------------------------------------------------------
// GetShardRaftState tests
// ---------------------------------------------------------------------------

func TestLocalBackend_GetShardRaftState(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	ccIdx, term, err := lb.GetShardRaftState(1)
	if err != nil {
		t.Fatalf("GetShardRaftState: %v", err)
	}
	if ccIdx != 0 || term != 0 {
		t.Fatalf("expected zeros, got ccIdx=%d term=%d", ccIdx, term)
	}
}

func TestLocalBackend_GetShardRaftState_AnyShard(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	ccIdx, term, err := lb.GetShardRaftState(42)
	if err != nil {
		t.Fatalf("GetShardRaftState(42): %v", err)
	}
	if ccIdx != 0 || term != 0 {
		t.Fatalf("expected zeros for any shard")
	}
}

// ---------------------------------------------------------------------------
// StartShard tests
// ---------------------------------------------------------------------------

func TestLocalBackend_StartShard(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	err := lb.StartShard(nil, false, nil, config.Config{})
	if err == nil {
		t.Fatal("expected error")
	}
	var coe *ClusterOperationError
	if !errors.As(err, &coe) {
		t.Fatalf("expected ClusterOperationError, got %T: %v", err, err)
	}
}

func TestLocalBackend_StartShard_OpName(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	err := lb.StartShard(nil, false, nil, config.Config{})
	var coe *ClusterOperationError
	if errors.As(err, &coe) && coe.Op != "StartShard" {
		t.Fatalf("expected op 'StartShard', got %q", coe.Op)
	}
}

// ---------------------------------------------------------------------------
// StopShard tests
// ---------------------------------------------------------------------------

func TestLocalBackend_StopShard(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	if err := lb.StopShard(1); err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestLocalBackend_StopShard_Idempotent(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()

	for range 3 {
		if err := lb.StopShard(1); err != nil {
			t.Fatalf("StopShard: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestLocalBackend_Close(t *testing.T) {
	lb := newTestLocalBackend()
	if err := lb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestLocalBackend_Close_DoubleCloseReturnsError(t *testing.T) {
	lb := newTestLocalBackend()
	if err := lb.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	err := lb.Close()
	if err == nil {
		t.Fatal("expected error on double close")
	}
	var bce *BackendClosedError
	if !errors.As(err, &bce) {
		t.Fatalf("expected BackendClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency tests
// ---------------------------------------------------------------------------

func TestLocalBackend_ConcurrentReads(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	// Seed data.
	cmd, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
		t.Fatalf("SyncPropose: %v", err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := lb.SyncRead(ctx, 1, "k1")
			if err != nil {
				t.Errorf("concurrent SyncRead: %v", err)
				return
			}
			if !bytes.Equal(val.([]byte), []byte("v1")) {
				t.Errorf("expected 'v1', got %q", val)
			}
		}()
	}
	wg.Wait()
}

func TestLocalBackend_ConcurrentWritesDuringReads(t *testing.T) {
	lb := newTestLocalBackend()
	defer func() { _ = lb.Close() }()
	ctx := context.Background()

	var wg sync.WaitGroup

	// Writer goroutines.
	for i := range 5 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cmd, err := kv.EncodePut([]byte("k1"), []byte{byte(idx)})
			if err != nil {
				t.Errorf("EncodePut: %v", err)
				return
			}
			if _, err := lb.SyncPropose(ctx, 1, cmd); err != nil {
				t.Errorf("concurrent SyncPropose: %v", err)
			}
		}(i)
	}

	// Reader goroutines.
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Read may see any value (or key-not-found before first write).
			_, _ = lb.SyncRead(ctx, 1, "k1")
		}()
	}

	wg.Wait()
}
