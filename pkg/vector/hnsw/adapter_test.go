//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

func TestIndexAdapter_ImplementsInterface(t *testing.T) {
	// This test verifies at runtime that IndexAdapter implements interfaces.HNSWIndex.
	// The compile-time check is in adapter.go via: var _ interfaces.HNSWIndex = (*IndexAdapter)(nil)
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create HNSW index: %v", err)
	}

	var _ interfaces.HNSWIndex = NewIndexAdapter(idx)
}

func TestIndexAdapter_Insert(t *testing.T) {
	tests := []struct {
		name    string
		id      uint64
		vector  []float32
		wantErr bool
	}{
		{
			name:    "valid insert",
			id:      1,
			vector:  []float32{1.0, 2.0, 3.0},
			wantErr: false,
		},
		{
			name:    "invalid ID",
			id:      0,
			vector:  []float32{1.0, 2.0, 3.0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := NewHNSWIndex(DefaultConfig())
			if err != nil {
				t.Fatalf("failed to create HNSW index: %v", err)
			}
			adapter := NewIndexAdapter(idx)

			err = adapter.Insert(tt.id, tt.vector)
			if (err != nil) != tt.wantErr {
				t.Errorf("Insert() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIndexAdapter_Delete(t *testing.T) {
	tests := []struct {
		name       string
		setupID    uint64
		deleteID   uint64
		wantErr    bool
		setupFirst bool
	}{
		{
			name:       "delete existing vector",
			setupID:    1,
			deleteID:   1,
			wantErr:    false,
			setupFirst: true,
		},
		{
			name:       "delete non-existent vector",
			setupID:    0,
			deleteID:   999,
			wantErr:    true,
			setupFirst: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := NewHNSWIndex(DefaultConfig())
			if err != nil {
				t.Fatalf("failed to create HNSW index: %v", err)
			}
			adapter := NewIndexAdapter(idx)

			if tt.setupFirst {
				if err := adapter.Insert(tt.setupID, []float32{1.0, 2.0, 3.0}); err != nil {
					t.Fatalf("failed to insert vector for setup: %v", err)
				}
			}

			err = adapter.Delete(tt.deleteID)
			if (err != nil) != tt.wantErr {
				t.Errorf("Delete() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIndexAdapter_Search(t *testing.T) {
	tests := []struct {
		name        string
		setupCount  int
		query       []float32
		k           int
		wantResults int
		wantErr     bool
	}{
		{
			name:        "search with results",
			setupCount:  5,
			query:       []float32{1.0, 2.0, 3.0},
			k:           3,
			wantResults: 3,
			wantErr:     false,
		},
		{
			name:        "search k larger than index",
			setupCount:  2,
			query:       []float32{1.0, 2.0, 3.0},
			k:           10,
			wantResults: 2,
			wantErr:     false,
		},
		{
			name:        "search empty index",
			setupCount:  0,
			query:       []float32{1.0, 2.0, 3.0},
			k:           3,
			wantResults: 0,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := NewHNSWIndex(DefaultConfig())
			if err != nil {
				t.Fatalf("failed to create HNSW index: %v", err)
			}
			adapter := NewIndexAdapter(idx)

			// Setup vectors
			for i := 1; i <= tt.setupCount; i++ {
				vec := []float32{float32(i), float32(i * 2), float32(i * 3)}
				if err := adapter.Insert(uint64(i), vec); err != nil {
					t.Fatalf("failed to insert vector %d: %v", i, err)
				}
			}

			results, err := adapter.Search(tt.query, tt.k)
			if (err != nil) != tt.wantErr {
				t.Errorf("Search() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && len(results) != tt.wantResults {
				t.Errorf("Search() returned %d results, want %d", len(results), tt.wantResults)
			}

			// Verify result type is interface type
			for _, r := range results {
				if r == nil {
					t.Error("Search() returned nil result")
				}
			}
		})
	}
}

func TestIndexAdapter_Count(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create HNSW index: %v", err)
	}
	adapter := NewIndexAdapter(idx)

	// Empty index
	if got := adapter.Count(); got != 0 {
		t.Errorf("Count() = %d, want 0", got)
	}

	// Insert some vectors
	for i := 1; i <= 5; i++ {
		vec := []float32{float32(i), float32(i * 2), float32(i * 3)}
		if err := adapter.Insert(uint64(i), vec); err != nil {
			t.Fatalf("failed to insert vector %d: %v", i, err)
		}
	}

	if got := adapter.Count(); got != 5 {
		t.Errorf("Count() = %d, want 5", got)
	}
}

func TestIndexAdapter_GetStats(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create HNSW index: %v", err)
	}
	adapter := NewIndexAdapter(idx)

	// Insert some vectors to have meaningful stats
	for i := 1; i <= 10; i++ {
		vec := []float32{float32(i), float32(i * 2), float32(i * 3)}
		if err := adapter.Insert(uint64(i), vec); err != nil {
			t.Fatalf("failed to insert vector %d: %v", i, err)
		}
	}

	stats := adapter.GetStats()
	if stats == nil {
		t.Fatal("GetStats() returned nil")
	}

	if stats.NodeCount != 10 {
		t.Errorf("GetStats().NodeCount = %d, want 10", stats.NodeCount)
	}

	if stats.Dimensions != 3 {
		t.Errorf("GetStats().Dimensions = %d, want 3", stats.Dimensions)
	}
}

func TestIndexAdapter_Unwrap(t *testing.T) {
	idx, err := NewHNSWIndex(DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create HNSW index: %v", err)
	}
	adapter := NewIndexAdapter(idx)

	unwrapped := adapter.Unwrap()
	if unwrapped != idx {
		t.Error("Unwrap() did not return the original index")
	}
}

func TestIndexFactory_CreateIndex(t *testing.T) {
	factory := NewIndexFactory()

	tests := []struct {
		name    string
		config  *interfaces.HNSWConfig
		wantErr bool
	}{
		{
			name:    "default config",
			config:  interfaces.DefaultHNSWConfig(),
			wantErr: false,
		},
		{
			name: "custom config with L2",
			config: &interfaces.HNSWConfig{
				M:              32,
				MMax:           64,
				EfConstruction: 400,
				EfSearch:       100,
				DistanceMetric: "l2",
			},
			wantErr: false,
		},
		{
			name: "custom config with cosine",
			config: &interfaces.HNSWConfig{
				M:              16,
				MMax:           32,
				EfConstruction: 200,
				EfSearch:       50,
				DistanceMetric: "cosine",
			},
			wantErr: false,
		},
		{
			name: "custom config with dot product",
			config: &interfaces.HNSWConfig{
				M:              16,
				MMax:           32,
				EfConstruction: 200,
				EfSearch:       50,
				DistanceMetric: "dot_product",
			},
			wantErr: false,
		},
		{
			name: "invalid config - zero M",
			config: &interfaces.HNSWConfig{
				M:              0,
				MMax:           32,
				EfConstruction: 200,
				EfSearch:       50,
				DistanceMetric: "l2",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := factory.CreateIndex(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIndex() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && idx == nil {
				t.Error("CreateIndex() returned nil index without error")
			}
		})
	}
}

func TestIndexFactory_ImplementsInterface(t *testing.T) {
	// Verify IndexFactory implements the interfaces.HNSWIndexFactory interface
	factory := NewIndexFactory()
	var _ interfaces.HNSWIndexFactory = factory
}
