//go:build qrdb_vector

package hnsw

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// TestRecoveryError_Unwrap tests RecoveryError Unwrap method.
func TestRecoveryError_Unwrap(t *testing.T) {
	innerErr := errors.New("inner error")
	recErr := &RecoveryError{
		Op:  "test_op",
		Err: innerErr,
	}

	unwrapped := recErr.Unwrap()
	if unwrapped != innerErr {
		t.Errorf("expected inner error, got %v", unwrapped)
	}

	if !errors.Is(recErr, innerErr) {
		t.Error("expected errors.Is to return true for inner error")
	}
}

// TestRecoveryError_Error_Format tests RecoveryError Error method format.
func TestRecoveryError_Error_Format(t *testing.T) {
	innerErr := errors.New("inner error")
	recErr := &RecoveryError{
		Op:  "load",
		Err: innerErr,
	}

	errMsg := recErr.Error()
	if errMsg != "recovery.load: inner error" {
		t.Errorf("unexpected error message: %s", errMsg)
	}
}

// TestParseDistanceMetric_AllCases tests parseDistanceMetric with various inputs.
func TestParseDistanceMetric_AllCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected DistanceMetric
	}{
		{"cosine lowercase", "cosine", CosineDistance},
		{"cosine uppercase", "COSINE", CosineDistance},
		{"dot_product lowercase", "dot_product", DotProductDistance},
		{"dot_product uppercase", "DOT_PRODUCT", DotProductDistance},
		{"l2 lowercase", "l2", L2Distance},
		{"l2 uppercase", "L2", L2Distance},
		{"euclidean lowercase", "euclidean", L2Distance},
		{"euclidean uppercase", "EUCLIDEAN", L2Distance},
		{"unknown defaults to l2", "unknown", L2Distance},
		{"empty defaults to l2", "", L2Distance},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseDistanceMetric(tt.input)
			if result != tt.expected {
				t.Errorf("parseDistanceMetric(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestIndexAdapter_GetStats_NonNil tests GetStats returns non-nil for empty index.
func TestIndexAdapter_GetStats_NonNil(t *testing.T) {
	// Create a minimal index
	config := DefaultConfig()
	config.M = 4
	config.MMax = 8
	config.EfConstruction = 16
	index, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	adapter := NewIndexAdapter(index)

	// GetStats on empty index should not panic
	stats := adapter.GetStats()
	if stats == nil {
		t.Error("expected non-nil stats even for empty index")
	}
}

// TestHNSWIndex_GetConfig_ReturnsCopy tests GetConfig returns a copy.
func TestHNSWIndex_GetConfig_ReturnsCopy(t *testing.T) {
	// Create an index with normal config
	config := DefaultConfig()
	config.M = 4
	config.MMax = 8
	idx, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Get config - should return a copy
	retrievedConfig := idx.GetConfig()
	if retrievedConfig == nil {
		t.Fatal("expected non-nil config")
	}

	// Verify it's a copy (modifying it shouldn't affect original)
	retrievedConfig.M = 999
	originalConfig := idx.GetConfig()
	if originalConfig.M == 999 {
		t.Error("GetConfig should return a copy, not the original")
	}
}

// TestHNSWIndex_Get_NotFoundError tests Get for a non-existent ID.
func TestHNSWIndex_Get_NotFoundError(t *testing.T) {
	config := DefaultConfig()
	config.M = 4
	config.MMax = 8
	config.EfConstruction = 16
	idx, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Try to get a vector that doesn't exist
	_, err = idx.Get(999)
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}
}

// TestHNSWIndex_Delete_NotFoundError tests Delete for a non-existent ID.
func TestHNSWIndex_Delete_NotFoundError(t *testing.T) {
	config := DefaultConfig()
	config.M = 4
	config.MMax = 8
	config.EfConstruction = 16
	idx, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Try to delete a vector that doesn't exist
	err = idx.Delete(999)
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}
}

// TestHNSWIndex_InsertAndRetrieve tests the basic insert and get workflow.
func TestHNSWIndex_InsertAndRetrieve(t *testing.T) {
	config := DefaultConfig()
	config.M = 4
	config.MMax = 8
	config.EfConstruction = 16
	idx, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert a vector
	vector := []float32{1.0, 2.0, 3.0, 4.0}
	if err := idx.Insert(1, vector); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Get the node back
	node, err := idx.Get(1)
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}

	if node == nil {
		t.Fatal("expected non-nil node")
	}
	if node.ID != 1 {
		t.Errorf("node ID = %d, want 1", node.ID)
	}
}

// TestHNSWIndex_Delete_SuccessfulRemoval tests successful deletion.
func TestHNSWIndex_Delete_SuccessfulRemoval(t *testing.T) {
	config := DefaultConfig()
	config.M = 4
	config.MMax = 8
	config.EfConstruction = 16
	idx, err := NewHNSWIndex(config)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Insert some vectors
	for i := uint64(1); i <= 5; i++ {
		vector := []float32{float32(i), float32(i) * 2, float32(i) * 3, float32(i) * 4}
		if err := idx.Insert(i, vector); err != nil {
			t.Fatalf("failed to insert %d: %v", i, err)
		}
	}

	// Delete one
	if err := idx.Delete(3); err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Verify it's gone
	_, err = idx.Get(3)
	if err == nil {
		t.Error("expected error when getting deleted vector")
	}

	// Verify others still exist
	_, err = idx.Get(1)
	if err != nil {
		t.Errorf("vector 1 should still exist: %v", err)
	}
}

// TestIndexFactory_CreateIndex_AllMetrics tests IndexFactory with different metrics.
func TestIndexFactory_CreateIndex_AllMetrics(t *testing.T) {
	factory := NewIndexFactory()

	tests := []struct {
		name   string
		metric string
	}{
		{"cosine", "cosine"},
		{"l2", "l2"},
		{"dot_product", "dot_product"},
		{"default", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &interfaces.HNSWConfig{
				M:              8,
				MMax:           16,
				EfConstruction: 100,
				EfSearch:       50,
				DistanceMetric: tt.metric,
			}

			idx, err := factory.CreateIndex(config)
			if err != nil {
				t.Fatalf("failed to create index: %v", err)
			}
			if idx == nil {
				t.Fatal("expected non-nil index")
			}
		})
	}
}
