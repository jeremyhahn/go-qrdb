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
	"testing"
)

func TestDefaultGPUConfig(t *testing.T) {
	cfg := DefaultGPUConfig()

	if cfg == nil {
		t.Fatal("DefaultGPUConfig returned nil")
	}
	if cfg.DeviceID != 0 {
		t.Errorf("expected DeviceID 0, got %d", cfg.DeviceID)
	}
	if cfg.MemoryLimitBytes != 0 {
		t.Errorf("expected MemoryLimitBytes 0 (unlimited), got %d", cfg.MemoryLimitBytes)
	}
	if cfg.BuildBatchSize != 100000 {
		t.Errorf("expected BuildBatchSize 100000, got %d", cfg.BuildBatchSize)
	}
	if cfg.Enabled {
		t.Error("expected Enabled to be false by default")
	}
}

func TestDefaultGPUConfig_IndependentInstances(t *testing.T) {
	cfg1 := DefaultGPUConfig()
	cfg2 := DefaultGPUConfig()

	cfg1.DeviceID = 5
	if cfg2.DeviceID == 5 {
		t.Error("modifying one config should not affect another")
	}
}

func TestGPUIndexType_String(t *testing.T) {
	tests := []struct {
		name     string
		indexTyp GPUIndexType
		want     string
	}{
		{
			name:     "CAGRA",
			indexTyp: GPUIndexCAGRA,
			want:     "gpu_cagra",
		},
		{
			name:     "IVFFlat",
			indexTyp: GPUIndexIVFFlat,
			want:     "gpu_ivf_flat",
		},
		{
			name:     "IVFPQ",
			indexTyp: GPUIndexIVFPQ,
			want:     "gpu_ivf_pq",
		},
		{
			name:     "BruteForce",
			indexTyp: GPUIndexBruteForce,
			want:     "gpu_brute_force",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.indexTyp.String()
			if got != tt.want {
				t.Errorf("GPUIndexType(%d).String() = %q, want %q", tt.indexTyp, got, tt.want)
			}
		})
	}
}

func TestGPUIndexType_String_Unknown(t *testing.T) {
	unknown := GPUIndexType(999)
	got := unknown.String()
	if got != "unknown" {
		t.Errorf("unknown GPUIndexType.String() = %q, want %q", got, "unknown")
	}
}
