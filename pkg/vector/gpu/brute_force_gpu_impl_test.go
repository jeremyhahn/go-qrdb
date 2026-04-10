//go:build qrdb_vector && gpu

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

package gpu

import (
	"testing"
)

func TestBruteForceGPU_NewAndClose(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestBruteForceGPU_InsertAndCount(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	if err := idx.Insert(10, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := idx.Insert(20, []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if got := idx.Count(); got != 2 {
		t.Errorf("Count() = %d, want 2", got)
	}
}

func TestBruteForceGPU_Insert_DuplicateOverwrites(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	if err := idx.Insert(1, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if err := idx.Insert(1, []float32{0, 0, 1, 0}); err != nil {
		t.Fatalf("duplicate Insert: %v", err)
	}
	if got := idx.Count(); got != 1 {
		t.Errorf("Count() = %d, want 1 after duplicate insert", got)
	}
}

func TestBruteForceGPU_Delete(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	if err := idx.Insert(5, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := idx.Delete(5); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := idx.Count(); got != 0 {
		t.Errorf("Count() = %d, want 0 after delete", got)
	}
}

func TestBruteForceGPU_Search_EmptyIndex(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	results, err := idx.Search([]float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search on empty index: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty index, got %d", len(results))
	}
}

func TestBruteForceGPU_Build_AndSearch(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	vectors := map[uint64][]float32{
		1: {1, 0, 0, 0},
		2: {0, 1, 0, 0},
		3: {0, 0, 1, 0},
	}
	if err := idx.Build(vectors); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.Search([]float32{1, 0, 0, 0}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].ID != 1 {
		t.Errorf("nearest neighbor ID = %d, want 1", results[0].ID)
	}
}

func TestBruteForceGPU_Build_EmptyMap(t *testing.T) {
	cfg := DefaultGPUConfig()
	idx, err := NewBruteForceGPU(cfg, 4, "l2")
	if err != nil {
		t.Fatalf("NewBruteForceGPU: %v", err)
	}
	defer idx.Close()

	if err := idx.Build(map[uint64][]float32{}); err != nil {
		t.Errorf("Build with empty map: %v", err)
	}
}
