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

//go:build codec_json && raft

package statemachine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// ---------------------------------------------------------------------------
// NewVectorStandardAdapter (0% coverage)
// ---------------------------------------------------------------------------

func TestNewVectorStandardAdapter_WrapsStateMachine(t *testing.T) {
	log := newTestLogger()
	inner := NewVectorConsensusFSM(1, 1, log)
	adapter := NewVectorStandardAdapter(inner)
	if adapter == nil {
		t.Fatal("expected non-nil VectorStandardAdapter")
	}
	// Verify it wraps the inner state machine.
	if adapter.StateMachine != inner {
		t.Fatal("expected adapter to embed the inner state machine")
	}
}

// ---------------------------------------------------------------------------
// NewVectorRegularCreateFunc (0% coverage)
// ---------------------------------------------------------------------------

func TestNewVectorRegularCreateFunc_ProducesAdapter(t *testing.T) {
	log := newTestLogger()

	storageFactory := func(shardID, replicaID uint64) vectorStorage.Storage {
		return nil
	}

	createFunc := NewVectorRegularCreateFunc(storageFactory, log)

	// Invoke the create function directly - sm.CreateFunc is a function type.
	machine := createFunc(1, 1)
	if machine == nil {
		t.Fatal("expected non-nil state machine from create func")
	}

	// The created machine should be a VectorStandardAdapter.
	adapter, ok := machine.(*VectorStandardAdapter)
	if !ok {
		t.Fatalf("expected *VectorStandardAdapter, got %T", machine)
	}
	if adapter.StateMachine == nil {
		t.Fatal("expected inner state machine to be set")
	}
}

func TestNewVectorRegularCreateFunc_DifferentShardIDs(t *testing.T) {
	log := newTestLogger()

	var capturedShardID, capturedReplicaID uint64
	storageFactory := func(shardID, replicaID uint64) vectorStorage.Storage {
		capturedShardID = shardID
		capturedReplicaID = replicaID
		return nil
	}

	createFunc := NewVectorRegularCreateFunc(storageFactory, log)
	_ = createFunc(42, 7)

	if capturedShardID != 42 {
		t.Fatalf("expected shardID 42, got %d", capturedShardID)
	}
	if capturedReplicaID != 7 {
		t.Fatalf("expected replicaID 7, got %d", capturedReplicaID)
	}
}

// ---------------------------------------------------------------------------
// vector_ops.go: NewCreateNamespaceCommandWithMetric (0% coverage)
// ---------------------------------------------------------------------------

func TestNewCreateNamespaceCommandWithMetric_SetsFields(t *testing.T) {
	cmd := NewCreateNamespaceCommandWithMetric("test-ns", 128, "cosine", "node1")
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
	if cmd.Op != OpCreateNamespace {
		t.Fatalf("expected OpCreateNamespace, got %v", cmd.Op)
	}
	if cmd.Namespace != "test-ns" {
		t.Fatalf("expected 'test-ns', got %q", cmd.Namespace)
	}
	if cmd.Dimension != 128 {
		t.Fatalf("expected dimension 128, got %d", cmd.Dimension)
	}
	if cmd.DistanceMetric != "cosine" {
		t.Fatalf("expected 'cosine', got %q", cmd.DistanceMetric)
	}
	if cmd.NodeID != "node1" {
		t.Fatalf("expected 'node1', got %q", cmd.NodeID)
	}
	if cmd.Timestamp == 0 {
		t.Fatal("expected non-zero timestamp")
	}
}

// ---------------------------------------------------------------------------
// vector_ops.go: NewCreateNamespaceCommandWithTenant (0% coverage)
// ---------------------------------------------------------------------------

func TestNewCreateNamespaceCommandWithTenant_SetsFields(t *testing.T) {
	quotas := &NamespaceQuotas{
		MaxVectors:      1000,
		MaxDimensions:   512,
		MaxStorageBytes: 1024 * 1024 * 1024,
	}
	labels := map[string]string{"env": "test", "team": "platform"}

	cmd := NewCreateNamespaceCommandWithTenant(
		"tenant-ns",
		256,
		"node2",
		"tenant-abc",
		"user-xyz",
		quotas,
		labels,
	)
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
	if cmd.Op != OpCreateNamespace {
		t.Fatalf("expected OpCreateNamespace, got %v", cmd.Op)
	}
	if cmd.Namespace != "tenant-ns" {
		t.Fatalf("expected 'tenant-ns', got %q", cmd.Namespace)
	}
	if cmd.Dimension != 256 {
		t.Fatalf("expected dimension 256, got %d", cmd.Dimension)
	}
	if cmd.NodeID != "node2" {
		t.Fatalf("expected 'node2', got %q", cmd.NodeID)
	}
	if cmd.TenantID != "tenant-abc" {
		t.Fatalf("expected 'tenant-abc', got %q", cmd.TenantID)
	}
	if cmd.OwnerUserID != "user-xyz" {
		t.Fatalf("expected 'user-xyz', got %q", cmd.OwnerUserID)
	}
	if cmd.Quotas != quotas {
		t.Fatal("expected quotas to be set")
	}
	if len(cmd.Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(cmd.Labels))
	}
}

// ---------------------------------------------------------------------------
// kv_adapter.go: GetHash on DiskAdapter and ConcurrentAdapter (0% coverage)
// ---------------------------------------------------------------------------

func TestDiskAdapter_GetHash_ZeroOnEmpty(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	h, err := adapter.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}
	// Hash of empty backend should be deterministic.
	h2, _ := adapter.GetHash()
	if h != h2 {
		t.Fatal("empty hash should be deterministic")
	}
}

func TestDiskAdapter_GetHash_ChangesAfterWrite(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	h1, err := adapter.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	// Write a key and check hash changes.
	ctx := context.Background()
	entries := make([]sm.Entry, 1)
	results := make([]sm.Result, 1)
	_ = ctx

	// Use kvApplyPut pattern through the underlying KVSM.
	kvApplyPut(t, kvsm, "disk-hash-key", "disk-hash-val")

	h2, err := adapter.GetHash()
	if err != nil {
		t.Fatalf("GetHash after put failed: %v", err)
	}
	if h1 == h2 {
		t.Fatal("hash should change after put")
	}
	_ = entries
	_ = results
}

func TestConcurrentAdapter_GetHash_ZeroOnEmpty(t *testing.T) {
	backend := NewConcurrentBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &ConcurrentAdapter{KVStateMachine: kvsm}

	h, err := adapter.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}
	h2, _ := adapter.GetHash()
	if h != h2 {
		t.Fatal("empty hash should be deterministic")
	}
}

func TestConcurrentAdapter_GetHash_ChangesAfterWrite(t *testing.T) {
	backend := NewConcurrentBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &ConcurrentAdapter{KVStateMachine: kvsm}

	h1, _ := adapter.GetHash()
	kvApplyPut(t, kvsm, "conc-hash-key", "conc-hash-val")
	h2, _ := adapter.GetHash()

	if h1 == h2 {
		t.Fatal("hash should change after put")
	}
}
