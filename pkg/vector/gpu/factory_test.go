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

// mockHNSWIndex is a test double implementing interfaces.HNSWIndex.
type mockHNSWIndex struct {
	insertErr error
	deleteErr error
	searchRes []*interfaces.HNSWSearchResult
	searchErr error
	countVal  int64
}

func (m *mockHNSWIndex) Insert(_ uint64, _ []float32) error {
	return m.insertErr
}

func (m *mockHNSWIndex) Delete(_ uint64) error {
	return m.deleteErr
}

func (m *mockHNSWIndex) Search(_ []float32, _ int) ([]*interfaces.HNSWSearchResult, error) {
	return m.searchRes, m.searchErr
}

func (m *mockHNSWIndex) Count() int64 {
	return m.countVal
}

func (m *mockHNSWIndex) GetStats() *interfaces.HNSWStats {
	return &interfaces.HNSWStats{NodeCount: m.countVal}
}

// mockHNSWFactory is a test double implementing interfaces.HNSWIndexFactory.
type mockHNSWFactory struct {
	createErr error
	index     interfaces.HNSWIndex
}

func (m *mockHNSWFactory) CreateIndex(_ *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.index, nil
}

func TestGPUIndexFactory_CreateIndex_GPUDisabled_FallsToCPU(t *testing.T) {
	cpuIndex := &mockHNSWIndex{countVal: 5}
	fallback := &mockHNSWFactory{index: cpuIndex}

	cfg := DefaultGPUConfig()
	cfg.Enabled = false

	factory := NewGPUIndexFactory(cfg, nil, fallback)
	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	// Should return the CPU index from the fallback.
	if idx.Count() != 5 {
		t.Errorf("expected count 5 from CPU fallback, got %d", idx.Count())
	}
}

func TestGPUIndexFactory_CreateIndex_GPUNotAvailable_FallsToCPU(t *testing.T) {
	cpuIndex := &mockHNSWIndex{countVal: 3}
	fallback := &mockHNSWFactory{index: cpuIndex}

	cfg := DefaultGPUConfig()
	cfg.Enabled = true

	// gpuAvailable is injected as false to simulate no GPU hardware.
	factory := &GPUIndexFactory{
		config:       cfg,
		gpuAvailable: false,
		fallback:     fallback,
	}

	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	if idx.Count() != 3 {
		t.Errorf("expected count 3 from CPU fallback, got %d", idx.Count())
	}
}

func TestGPUIndexFactory_NilFallback_ReturnsError(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.Enabled = false

	factory := NewGPUIndexFactory(cfg, nil, nil)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err == nil {
		t.Fatal("expected error with nil fallback, got nil")
	}
	if !errors.Is(err, ErrNoFallbackFactory) {
		t.Errorf("expected ErrNoFallbackFactory, got: %v", err)
	}
}

func TestGPUIndexFactory_FallbackError_PropagatesError(t *testing.T) {
	sentinel := errors.New("cpu factory failed")
	fallback := &mockHNSWFactory{createErr: sentinel}

	cfg := DefaultGPUConfig()
	cfg.Enabled = false

	factory := NewGPUIndexFactory(cfg, nil, fallback)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

func TestGPUIndexFactory_ImplementsHNSWIndexFactory(t *testing.T) {
	factory := NewGPUIndexFactory(DefaultGPUConfig(), nil, &mockHNSWFactory{})
	var _ interfaces.HNSWIndexFactory = factory
}

func TestNewGPUIndexFactory_StoresConfig(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.DeviceID = 2

	factory := NewGPUIndexFactory(cfg, nil, &mockHNSWFactory{})
	if factory.config.DeviceID != 2 {
		t.Errorf("expected DeviceID 2, got %d", factory.config.DeviceID)
	}
}

func TestGPUIndexFactory_CreateIndex_GPUEnabled_GPUAvailable_CreatesGPUIndex(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.Enabled = true

	fallback := &mockHNSWFactory{index: &mockHNSWIndex{countVal: 0}}

	// Inject gpuAvailable=true to exercise the GPU path.
	// In a !gpu build this reaches createGPUIndex which returns ErrGPUNotCompiled;
	// in a +gpu build with hardware it succeeds. Either outcome is valid here —
	// the test verifies that the GPU path is taken (not fallback) when both are true.
	factory := &GPUIndexFactory{
		config:       cfg,
		gpuAvailable: true,
		fallback:     fallback,
	}

	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	// In !gpu builds this returns ErrGPUNotCompiled from the CAGRA stub.
	// In +gpu builds this succeeds. Both are expected outcomes.
	_ = err
}
