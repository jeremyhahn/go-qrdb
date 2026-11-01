//go:build qrdb_vector && !gpu

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

func TestCAGRAIndex_NewReturnsStubError(t *testing.T) {
	idx, err := NewCAGRAIndex(DefaultGPUConfig(), 128, "l2")
	if idx != nil {
		t.Error("expected nil CAGRAIndex from stub")
	}
	if !errors.Is(err, ErrGPUNotCompiled) {
		t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
	}
}

func TestCAGRAIndex_AllMethodsReturnStubError(t *testing.T) {
	idx := &CAGRAIndex{}

	t.Run("Insert", func(t *testing.T) {
		err := idx.Insert(1, []float32{1.0, 2.0})
		if !errors.Is(err, ErrGPUNotCompiled) {
			t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		err := idx.Delete(1)
		if !errors.Is(err, ErrGPUNotCompiled) {
			t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
		}
	})

	t.Run("Search", func(t *testing.T) {
		results, err := idx.Search([]float32{1.0, 2.0}, 10)
		if results != nil {
			t.Error("expected nil results from stub")
		}
		if !errors.Is(err, ErrGPUNotCompiled) {
			t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
		}
	})

	t.Run("Build", func(t *testing.T) {
		err := idx.Build(map[uint64][]float32{1: {1.0, 2.0}})
		if !errors.Is(err, ErrGPUNotCompiled) {
			t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
		}
	})

	t.Run("Count", func(t *testing.T) {
		count := idx.Count()
		if count != 0 {
			t.Errorf("expected count 0 from stub, got: %d", count)
		}
	})

	t.Run("Close", func(t *testing.T) {
		err := idx.Close()
		if !errors.Is(err, ErrGPUNotCompiled) {
			t.Errorf("expected ErrGPUNotCompiled, got: %v", err)
		}
	})
}

// TestGPUIndexFactory_CreateIndex_DimensionReachesCAGRA proves that a config
// carrying a dimension passes the factory's check and reaches the CAGRA
// constructor, which in a !gpu build is the stub.
func TestGPUIndexFactory_CreateIndex_DimensionReachesCAGRA(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.Enabled = true
	factory := &GPUIndexFactory{config: cfg, gpuAvailable: true, fallback: &mockHNSWFactory{}}

	hnswCfg := interfaces.DefaultHNSWConfig()
	hnswCfg.Dimensions = 64
	if _, err := factory.CreateIndex(hnswCfg); !errors.Is(err, ErrGPUNotCompiled) {
		t.Fatalf("CreateIndex(Dimensions=64) error = %v, want ErrGPUNotCompiled from the CAGRA stub", err)
	}
}
