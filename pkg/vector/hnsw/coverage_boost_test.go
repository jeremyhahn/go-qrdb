//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"bytes"
	"errors"
	"testing"
)

// TestHNSWIndex_GetConfig_NilConfig tests GetConfig when internal config is nil.
func TestHNSWIndex_GetConfig_NilConfig(t *testing.T) {
	// Create an index normally
	idx, err := NewHNSWIndex(nil)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Manually set config to nil to test edge case
	idx.config = nil

	// GetConfig should return default config when internal is nil
	config := idx.GetConfig()
	if config == nil {
		t.Fatal("expected non-nil config from GetConfig")
	}

	// Verify it returns default config
	defaultCfg := DefaultConfig()
	if config.M != defaultCfg.M {
		t.Errorf("expected M=%d, got %d", defaultCfg.M, config.M)
	}
}

// TestIndexAdapter_GetStats_WithNilUnderlyingStats tests GetStats when underlying returns nil.
func TestIndexAdapter_GetStats_WithNilUnderlyingStats(t *testing.T) {
	// Create empty index
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	adapter := NewIndexAdapter(idx)

	// Empty index should still return stats (not nil)
	stats := adapter.GetStats()
	if stats == nil {
		t.Error("expected non-nil stats even for empty index")
	}
}

// TestHNSWIndex_Get_TypeAssertionFailure tests type assertion failure in Get.
func TestHNSWIndex_Get_TypeAssertionFailure(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Store something that's not *HNSWNode
	idx.nodes.Store(uint64(999), "not a node")

	// Get should fail with ErrInternal
	_, err = idx.Get(999)
	if err == nil {
		t.Fatal("expected error for type assertion failure")
	}
	if !errors.Is(err, ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
}

// TestHNSWIndex_Delete_TypeAssertionFailure tests type assertion failure in Delete.
func TestHNSWIndex_Delete_TypeAssertionFailure(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Store something that's not *HNSWNode
	idx.nodes.Store(uint64(999), "not a node")

	// Delete should fail with ErrInternal
	err = idx.Delete(999)
	if err == nil {
		t.Fatal("expected error for type assertion failure")
	}
	if !errors.Is(err, ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
}

// TestHNSWIndex_Search_EntryPointTypeAssertionFail tests Search when entry point is not *HNSWNode.
func TestHNSWIndex_Search_EntryPointTypeAssertionFail(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert a valid node first
	if err := idx.Insert(1, []float32{1.0, 2.0, 3.0}); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Replace the entry point node with invalid type
	idx.nodes.Store(uint64(1), "not a node")

	// Search should fail with ErrInternal
	_, err = idx.Search([]float32{1.0, 2.0, 3.0}, 1)
	if err == nil {
		t.Fatal("expected error for type assertion failure")
	}
	if !errors.Is(err, ErrInternal) {
		t.Errorf("expected ErrInternal, got %v", err)
	}
}

// TestHNSWIndex_Search_EntryPointNotFound tests Search when entry point is not found.
func TestHNSWIndex_Search_EntryPointNotFound(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert a valid node first
	if err := idx.Insert(1, []float32{1.0, 2.0, 3.0}); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Delete the entry point node without going through Delete method
	idx.nodes.Delete(uint64(1))

	// Search should fail with ErrNotFound
	_, err = idx.Search([]float32{1.0, 2.0, 3.0}, 1)
	if err == nil {
		t.Fatal("expected error for missing entry point")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestHNSWIndex_UpdateEntryPoint_NoNodesLeft tests updateEntryPoint when all nodes are deleted.
func TestHNSWIndex_UpdateEntryPoint_NoNodesLeft(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert and delete a node
	if err := idx.Insert(1, []float32{1.0, 2.0, 3.0}); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	if err := idx.Delete(1); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// maxLayer should be reset to -1
	if idx.maxLayer.Load() != -1 {
		t.Errorf("expected maxLayer=-1, got %d", idx.maxLayer.Load())
	}
}

// TestHNSWIndex_UpdateEntryPoint_TypeAssertionSkip tests updateEntryPoint skips invalid nodes.
func TestHNSWIndex_UpdateEntryPoint_TypeAssertionSkip(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert valid nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Add an invalid node
	idx.nodes.Store(uint64(999), "not a node")

	// Delete the entry point to trigger updateEntryPoint
	entryPointID := idx.entryPoint.Load()
	if err := idx.Delete(entryPointID); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Should still work, just skipping the invalid node
	if idx.nodeCount.Load() != 2 {
		t.Errorf("expected nodeCount=2, got %d", idx.nodeCount.Load())
	}
}

// TestHNSWIndex_UpdateMaxLayer_TypeAssertionSkip tests updateMaxLayer skips invalid nodes.
func TestHNSWIndex_UpdateMaxLayer_TypeAssertionSkip(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert valid nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Add an invalid node
	idx.nodes.Store(uint64(999), "not a node")

	// Force updateMaxLayer
	idx.updateMaxLayer()

	// Should complete without panic
	if idx.maxLayer.Load() < 0 {
		t.Errorf("expected maxLayer >= 0, got %d", idx.maxLayer.Load())
	}
}

// TestHNSWIndex_Delete_UpdatesEntryPointWhenDeleted tests deleting the entry point.
func TestHNSWIndex_Delete_UpdatesEntryPointWhenDeleted(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert multiple nodes
	for i := uint64(1); i <= 5; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	oldEntryPoint := idx.entryPoint.Load()

	// Delete the entry point
	if err := idx.Delete(oldEntryPoint); err != nil {
		t.Fatalf("failed to delete entry point: %v", err)
	}

	// Entry point should have changed
	newEntryPoint := idx.entryPoint.Load()
	if newEntryPoint == oldEntryPoint {
		t.Error("expected entry point to change after deletion")
	}
}

// TestHNSWIndex_Delete_UpdatesMaxLayerWhenHighLayerDeleted tests deleting node at max layer.
func TestHNSWIndex_Delete_UpdatesMaxLayerWhenHighLayerDeleted(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert many nodes to increase chance of getting high layer nodes
	for i := uint64(1); i <= 50; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Find and delete the entry point (which should be at max layer)
	entryPointID := idx.entryPoint.Load()
	if err := idx.Delete(entryPointID); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Should still function correctly
	if idx.Count() != 49 {
		t.Errorf("expected count=49, got %d", idx.Count())
	}
}

// TestGetNeighbors_NodeNotFound tests getNeighbors when node doesn't exist.
func TestGetNeighbors_NodeNotFound(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Get neighbors for non-existent node
	neighbors := idx.getNeighbors(999, 0)
	if neighbors != nil {
		t.Error("expected nil for non-existent node")
	}
}

// TestGetNeighbors_TypeAssertionFailure tests getNeighbors with invalid node type.
func TestGetNeighbors_TypeAssertionFailure(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Store invalid node
	idx.nodes.Store(uint64(999), "not a node")

	// Get neighbors should return nil
	neighbors := idx.getNeighbors(999, 0)
	if neighbors != nil {
		t.Error("expected nil for invalid node type")
	}
}

// TestSelectNeighborsHeuristic_NodeLoadFailures tests heuristic with missing nodes.
func TestSelectNeighborsHeuristic_NodeLoadFailures(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert some nodes
	for i := uint64(1); i <= 5; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Create candidates including a non-existent node
	candidates := []*candidate{
		{id: 1, distance: 0.1},
		{id: 999, distance: 0.2}, // Non-existent
		{id: 2, distance: 0.3},
	}

	// Should still work, just skipping missing nodes
	query := []float32{1.0, 2.0, 3.0}
	result := idx.selectNeighborsHeuristic(query, candidates, 3)
	if len(result) == 0 {
		t.Error("expected some results even with missing nodes")
	}
}

// TestSelectNeighborsHeuristic_WithTypeAssertionFailures tests heuristic with invalid node types.
func TestSelectNeighborsHeuristic_WithTypeAssertionFailures(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert some valid nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Add an invalid node
	idx.nodes.Store(uint64(999), "not a node")

	candidates := []*candidate{
		{id: 1, distance: 0.1},
		{id: 999, distance: 0.15}, // Invalid type
		{id: 2, distance: 0.2},
	}

	query := []float32{1.0, 2.0, 3.0}
	result := idx.selectNeighborsHeuristic(query, candidates, 3)

	// Should still return results
	if len(result) == 0 {
		t.Error("expected results")
	}
}

// TestPruneConnections_WithMissingNodes tests pruneConnections when some connected nodes are missing.
func TestPruneConnections_WithMissingNodes(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert nodes
	for i := uint64(1); i <= 5; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	node, err := idx.Get(1)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	// Add a connection to a non-existent node
	node.AddConnection(0, 999)

	// Prune should still work, ignoring missing nodes
	idx.pruneConnections(node, 0, 2)

	// Should not panic and node should still be usable
	conns := node.GetConnections(0)
	if len(conns) > 2 {
		t.Errorf("expected at most 2 connections, got %d", len(conns))
	}
}

// TestPruneConnections_WithInvalidNodeTypes tests pruneConnections with invalid node types.
func TestPruneConnections_WithInvalidNodeTypes(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert nodes
	for i := uint64(1); i <= 5; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	node, err := idx.Get(1)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	// Store invalid node type
	idx.nodes.Store(uint64(888), "not a node")

	// Add a connection to the invalid node
	node.AddConnection(0, 888)

	// Prune should still work
	idx.pruneConnections(node, 0, 2)

	// Should not panic
	conns := node.GetConnections(0)
	if len(conns) > 2 {
		t.Errorf("expected at most 2 connections, got %d", len(conns))
	}
}

// TestL2SIMD_DifferentLengths tests L2SIMD with different length vectors.
func TestL2SIMD_DifferentLengths(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}

	dist := L2SIMD(a, b)
	if dist < 1e30 {
		t.Errorf("expected max distance for different lengths, got %f", dist)
	}
}

// TestL2SIMD_RemainingElements tests L2SIMD with length not divisible by 4.
func TestL2SIMD_RemainingElements(t *testing.T) {
	// 5 elements - 4 in SIMD loop, 1 remaining
	a := []float32{1, 2, 3, 4, 5}
	b := []float32{1, 2, 3, 4, 5}

	dist := L2SIMD(a, b)
	if dist != 0 {
		t.Errorf("expected 0 distance for identical vectors, got %f", dist)
	}
}

// TestCosineSIMD_DifferentLengths tests CosineSIMD with different length vectors.
func TestCosineSIMD_DifferentLengths(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}

	dist := CosineSIMD(a, b)
	if dist < 1e30 {
		t.Errorf("expected max distance for different lengths, got %f", dist)
	}
}

// TestCosineSIMD_RemainingElements tests CosineSIMD with length not divisible by 4.
func TestCosineSIMD_RemainingElements(t *testing.T) {
	// 5 elements - 4 in SIMD loop, 1 remaining
	a := []float32{1, 2, 3, 4, 5}
	b := []float32{1, 2, 3, 4, 5}

	dist := CosineSIMD(a, b)
	if dist > 0.01 {
		t.Errorf("expected ~0 distance for identical vectors, got %f", dist)
	}
}

// TestCosineSIMD_ZeroVectors tests CosineSIMD with zero vectors.
func TestCosineSIMD_ZeroVectors(t *testing.T) {
	a := []float32{0, 0, 0, 0, 0}
	b := []float32{1, 2, 3, 4, 5}

	dist := CosineSIMD(a, b)
	if dist != 1.0 {
		t.Errorf("expected 1.0 for zero vector, got %f", dist)
	}
}

// TestSnapshot_RestoreInvalidData tests Restore with corrupted data.
func TestSnapshot_RestoreInvalidData(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Try to restore from invalid data
	invalidData := bytes.NewBuffer([]byte("not valid gob data"))
	err = idx.Restore(invalidData)
	if err == nil {
		t.Error("expected error for invalid restore data")
	}
}

// TestInsert_EntryPointNotFound tests insert when entry point is missing.
func TestInsert_EntryPointNotFound(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert first node
	if err := idx.Insert(1, []float32{1, 2, 3}); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Delete entry point without going through Delete method
	idx.nodes.Delete(idx.entryPoint.Load())

	// Create new node manually
	node := NewNode(2, []float32{4, 5, 6}, 0)

	// insert should handle missing entry point gracefully
	idx.insert(node)

	// Should not panic - the function returns early if entry point not found
}

// TestInsert_EntryPointTypeAssertionFail tests insert when entry point has wrong type.
func TestInsert_EntryPointTypeAssertionFail(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert first node
	if err := idx.Insert(1, []float32{1, 2, 3}); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Replace entry point with invalid type
	idx.nodes.Store(idx.entryPoint.Load(), "not a node")

	// Create new node manually
	node := NewNode(2, []float32{4, 5, 6}, 0)

	// insert should handle type assertion failure gracefully
	idx.insert(node)

	// Should not panic - the function returns early on type assertion failure
}

// TestDelete_RangeWithInvalidTypes tests Delete handles invalid types in Range.
func TestDelete_RangeWithInvalidTypes(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Add invalid node type
	idx.nodes.Store(uint64(999), "not a node")

	// Delete should still work, skipping invalid nodes
	if err := idx.Delete(1); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Verify deletion
	if idx.Count() != 2 {
		t.Errorf("expected count=2, got %d", idx.Count())
	}
}

// TestTransactionLogError_ErrorFormat tests TransactionLogError formatting.
func TestTransactionLogError_ErrorFormat(t *testing.T) {
	t.Run("with txn ID", func(t *testing.T) {
		err := &TransactionLogError{
			Op:    "write",
			TxnID: "txn-123",
			Err:   errors.New("io error"),
		}

		msg := err.Error()
		if msg != "txn_log.write [txn=txn-123]: io error" {
			t.Errorf("unexpected error message: %s", msg)
		}
	})

	t.Run("without txn ID", func(t *testing.T) {
		err := &TransactionLogError{
			Op:  "init",
			Err: errors.New("io error"),
		}

		msg := err.Error()
		if msg != "txn_log.init: io error" {
			t.Errorf("unexpected error message: %s", msg)
		}
	})

	t.Run("unwrap", func(t *testing.T) {
		innerErr := errors.New("inner error")
		err := &TransactionLogError{
			Op:    "test",
			TxnID: "123",
			Err:   innerErr,
		}

		if err.Unwrap() != innerErr {
			t.Error("Unwrap did not return inner error")
		}
	})
}

// TestRollbackError_ErrorFormat tests RollbackError formatting.
func TestRollbackError_ErrorFormat(t *testing.T) {
	t.Run("with vector ID", func(t *testing.T) {
		err := &RollbackError{
			Op:       "delete",
			TxnID:    "txn-123",
			VectorID: 456,
			Err:      errors.New("not found"),
		}

		msg := err.Error()
		if msg != "rollback.delete [txn=txn-123, vector=456]: not found" {
			t.Errorf("unexpected error message: %s", msg)
		}
	})

	t.Run("without vector ID", func(t *testing.T) {
		err := &RollbackError{
			Op:    "start",
			TxnID: "txn-123",
			Err:   errors.New("failed"),
		}

		msg := err.Error()
		if msg != "rollback.start [txn=txn-123]: failed" {
			t.Errorf("unexpected error message: %s", msg)
		}
	})

	t.Run("unwrap", func(t *testing.T) {
		innerErr := errors.New("inner error")
		err := &RollbackError{
			Op:    "test",
			TxnID: "123",
			Err:   innerErr,
		}

		if err.Unwrap() != innerErr {
			t.Error("Unwrap did not return inner error")
		}
	})
}

// TestBuildCandidateHeap_LessThanMaxSize tests buildCandidateHeap with fewer candidates.
func TestBuildCandidateHeap_LessThanMaxSize(t *testing.T) {
	candidates := []*candidate{
		{id: 1, distance: 0.5},
		{id: 2, distance: 0.3},
	}

	heap := buildCandidateHeap(candidates, 5)
	if heap.Len() != 2 {
		t.Errorf("expected heap length 2, got %d", heap.Len())
	}
}

// TestBuildCandidateHeap_ExactMaxSize tests buildCandidateHeap with exact max size.
func TestBuildCandidateHeap_ExactMaxSize(t *testing.T) {
	candidates := []*candidate{
		{id: 1, distance: 0.5},
		{id: 2, distance: 0.3},
		{id: 3, distance: 0.7},
	}

	heap := buildCandidateHeap(candidates, 3)
	if heap.Len() != 3 {
		t.Errorf("expected heap length 3, got %d", heap.Len())
	}
}

// TestBuildCandidateHeap_MoreThanMaxSize tests buildCandidateHeap with more candidates.
func TestBuildCandidateHeap_MoreThanMaxSize(t *testing.T) {
	candidates := []*candidate{
		{id: 1, distance: 0.5},
		{id: 2, distance: 0.3},
		{id: 3, distance: 0.7},
		{id: 4, distance: 0.1},
		{id: 5, distance: 0.9},
	}

	heap := buildCandidateHeap(candidates, 3)
	if heap.Len() != 3 {
		t.Errorf("expected heap length 3, got %d", heap.Len())
	}
}

// TestSelectNeighborsHeuristic_LessThanM tests heuristic with fewer candidates than M.
func TestSelectNeighborsHeuristic_LessThanM(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert nodes
	for i := uint64(1); i <= 2; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	candidates := []*candidate{
		{id: 1, distance: 0.1},
		{id: 2, distance: 0.2},
	}

	query := []float32{1.0, 2.0, 3.0}
	result := idx.selectNeighborsHeuristic(query, candidates, 5)

	// Should return all candidates since we have fewer than M
	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}
}

// TestSelectNeighborsSimple_FewerThanM tests simple selection with fewer candidates.
func TestSelectNeighborsSimple_FewerThanM(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	candidates := []*candidate{
		{id: 1, distance: 0.5},
		{id: 2, distance: 0.3},
	}

	result := idx.selectNeighborsSimple(candidates, 5)
	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}
}

// TestSelectNeighborsSimple_MoreThanM tests simple selection with more candidates.
func TestSelectNeighborsSimple_MoreThanM(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	candidates := []*candidate{
		{id: 1, distance: 0.5},
		{id: 2, distance: 0.3},
		{id: 3, distance: 0.7},
		{id: 4, distance: 0.1},
	}

	result := idx.selectNeighborsSimple(candidates, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}

	// Should have the two closest
	if result[0].distance != 0.1 || result[1].distance != 0.3 {
		t.Error("expected closest neighbors to be selected")
	}
}

// TestPruneConnections_NoNeedToPrune tests pruneConnections when no pruning needed.
func TestPruneConnections_NoNeedToPrune(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	node, err := idx.Get(1)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}

	initialConns := len(node.GetConnections(0))

	// Prune with max higher than current connections
	idx.pruneConnections(node, 0, 100)

	// Should be unchanged
	if len(node.GetConnections(0)) != initialConns {
		t.Error("connections should be unchanged when no pruning needed")
	}
}

// TestHNSWIndex_GetStats_WithInvalidNodeTypes tests GetStats skips invalid node types.
func TestHNSWIndex_GetStats_WithInvalidNodeTypes(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert some valid nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Add an invalid node type
	idx.nodes.Store(uint64(999), "not a node")

	// GetStats should still work
	stats := idx.GetStats()
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	// The invalid node doesn't affect the count as it's in sync.Map but not counted
	if stats.NodeCount != 3 {
		t.Errorf("expected NodeCount=3, got %d", stats.NodeCount)
	}
}

// TestSnapshot_WithInvalidNodeTypes tests Snapshot skips invalid node types.
func TestSnapshot_WithInvalidNodeTypes(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert valid nodes
	for i := uint64(1); i <= 3; i++ {
		if err := idx.Insert(i, []float32{float32(i), float32(i) * 2, float32(i) * 3}); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Add invalid node type
	idx.nodes.Store(uint64(999), "not a node")

	// Snapshot should still work
	var buf bytes.Buffer
	err = idx.Snapshot(&buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify we can restore
	idx2, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	err = idx2.Restore(&buf)
	if err != nil {
		t.Fatalf("failed to restore: %v", err)
	}

	// Should have 3 valid nodes (not the invalid one)
	if idx2.Count() != 3 {
		t.Errorf("expected 3 nodes after restore, got %d", idx2.Count())
	}
}

// TestSelectNeighborsHeuristic_CandidateRejection tests the rejection logic in heuristic.
func TestSelectNeighborsHeuristic_CandidateRejection(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert nodes with specific vectors to test heuristic rejection
	vectors := [][]float32{
		{1.0, 0.0, 0.0},  // id=1
		{1.0, 0.01, 0.0}, // id=2 - very close to id=1
		{0.0, 1.0, 0.0},  // id=3 - different direction
		{0.0, 0.0, 1.0},  // id=4 - different direction
	}

	for i, v := range vectors {
		if err := idx.Insert(uint64(i+1), v); err != nil {
			t.Fatalf("failed to insert %d: %v", i+1, err)
		}
	}

	// Create candidates where id=2 should be rejected due to being too close to id=1
	candidates := []*candidate{
		{id: 1, distance: 0.1},
		{id: 2, distance: 0.11}, // Close to query but very close to id=1
		{id: 3, distance: 0.5},
		{id: 4, distance: 0.6},
	}

	query := []float32{1.0, 0.0, 0.0}
	result := idx.selectNeighborsHeuristic(query, candidates, 3)

	// Should have results
	if len(result) == 0 {
		t.Error("expected results from heuristic")
	}
}
