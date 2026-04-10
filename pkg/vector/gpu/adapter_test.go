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

package gpu

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
)

// mockGPUIndex is a test double implementing GPUIndex.
type mockGPUIndex struct {
	insertErr  error
	deleteErr  error
	searchRes  []SearchResult
	searchErr  error
	countVal   int64
	closeErr   error
	insertedID uint64
	deletedID  uint64
}

func (m *mockGPUIndex) Insert(id uint64, _ []float32) error {
	m.insertedID = id
	return m.insertErr
}

func (m *mockGPUIndex) Delete(id uint64) error {
	m.deletedID = id
	return m.deleteErr
}

func (m *mockGPUIndex) Search(_ []float32, _ int) ([]SearchResult, error) {
	return m.searchRes, m.searchErr
}

func (m *mockGPUIndex) Count() int64 {
	return m.countVal
}

func (m *mockGPUIndex) Close() error {
	return m.closeErr
}

func TestGPUIndexAdapter_Insert(t *testing.T) {
	mock := &mockGPUIndex{}
	adapter := NewGPUIndexAdapter(mock, 128)

	err := adapter.Insert(42, []float32{1.0, 2.0})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if mock.insertedID != 42 {
		t.Errorf("expected insertedID 42, got %d", mock.insertedID)
	}
}

func TestGPUIndexAdapter_Insert_PropagatesError(t *testing.T) {
	sentinel := errors.New("insert failed")
	mock := &mockGPUIndex{insertErr: sentinel}
	adapter := NewGPUIndexAdapter(mock, 128)

	err := adapter.Insert(1, []float32{1.0})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

func TestGPUIndexAdapter_Delete(t *testing.T) {
	mock := &mockGPUIndex{}
	adapter := NewGPUIndexAdapter(mock, 128)

	err := adapter.Delete(99)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if mock.deletedID != 99 {
		t.Errorf("expected deletedID 99, got %d", mock.deletedID)
	}
}

func TestGPUIndexAdapter_Delete_PropagatesError(t *testing.T) {
	sentinel := errors.New("delete failed")
	mock := &mockGPUIndex{deleteErr: sentinel}
	adapter := NewGPUIndexAdapter(mock, 128)

	err := adapter.Delete(1)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

func TestGPUIndexAdapter_Search_ConvertsResults(t *testing.T) {
	gpuResults := []SearchResult{
		{ID: 10, Distance: 0.5},
		{ID: 20, Distance: 1.2},
	}
	mock := &mockGPUIndex{searchRes: gpuResults}
	adapter := NewGPUIndexAdapter(mock, 128)

	results, err := adapter.Search([]float32{1.0, 2.0}, 2)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != 10 || results[0].Distance != 0.5 {
		t.Errorf("result[0] mismatch: got {%d, %f}", results[0].ID, results[0].Distance)
	}
	if results[1].ID != 20 || results[1].Distance != 1.2 {
		t.Errorf("result[1] mismatch: got {%d, %f}", results[1].ID, results[1].Distance)
	}
}

func TestGPUIndexAdapter_Search_EmptyResults(t *testing.T) {
	mock := &mockGPUIndex{searchRes: []SearchResult{}}
	adapter := NewGPUIndexAdapter(mock, 128)

	results, err := adapter.Search([]float32{1.0}, 10)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestGPUIndexAdapter_Search_PropagatesError(t *testing.T) {
	sentinel := errors.New("search failed")
	mock := &mockGPUIndex{searchErr: sentinel}
	adapter := NewGPUIndexAdapter(mock, 128)

	results, err := adapter.Search([]float32{1.0}, 10)
	if results != nil {
		t.Error("expected nil results on error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

func TestGPUIndexAdapter_Count(t *testing.T) {
	mock := &mockGPUIndex{countVal: 42}
	adapter := NewGPUIndexAdapter(mock, 128)

	count := adapter.Count()
	if count != 42 {
		t.Errorf("expected count 42, got %d", count)
	}
}

func TestGPUIndexAdapter_Count_Zero(t *testing.T) {
	mock := &mockGPUIndex{countVal: 0}
	adapter := NewGPUIndexAdapter(mock, 128)

	count := adapter.Count()
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

func TestGPUIndexAdapter_GetStats(t *testing.T) {
	mock := &mockGPUIndex{countVal: 100}
	adapter := NewGPUIndexAdapter(mock, 256)

	stats := adapter.GetStats()
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.NodeCount != 100 {
		t.Errorf("expected NodeCount 100, got %d", stats.NodeCount)
	}
	if stats.Dimensions != 256 {
		t.Errorf("expected Dimensions 256, got %d", stats.Dimensions)
	}
}

func TestGPUIndexAdapter_GetStats_ReflectsCount(t *testing.T) {
	mock := &mockGPUIndex{countVal: 7}
	adapter := NewGPUIndexAdapter(mock, 64)

	stats := adapter.GetStats()
	if stats.NodeCount != 7 {
		t.Errorf("expected NodeCount 7, got %d", stats.NodeCount)
	}
}

func TestGPUIndexAdapter_ImplementsHNSWIndex(t *testing.T) {
	mock := &mockGPUIndex{}
	adapter := NewGPUIndexAdapter(mock, 128)

	var _ interfaces.HNSWIndex = adapter
}
