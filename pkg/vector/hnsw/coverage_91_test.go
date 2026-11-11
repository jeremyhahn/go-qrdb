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
