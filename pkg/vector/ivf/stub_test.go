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

//go:build !qrdb_vector

package ivf

import (
	"errors"
	"testing"
)

// TestStubNewIndex verifies the stub constructor reports that vector
// support is not compiled in.
func TestStubNewIndex(t *testing.T) {
	idx, err := NewIndex(DefaultConfig(8))
	if !errors.Is(err, ErrNotCompiled) {
		t.Errorf("NewIndex error = %v, want ErrNotCompiled", err)
	}
	if idx != nil {
		t.Errorf("NewIndex index = %v, want nil", idx)
	}
}

// TestStubConfig verifies the stub configuration helpers keep the real
// package's defaults and string formatting.
func TestStubConfig(t *testing.T) {
	cfg := DefaultConfig(16)
	if cfg.Dimensions != 16 {
		t.Errorf("Dimensions = %d, want 16", cfg.Dimensions)
	}
	if cfg.NumCentroids != 256 {
		t.Errorf("NumCentroids = %d, want 256", cfg.NumCentroids)
	}
	if cfg.DistanceMetric != L2Distance {
		t.Errorf("DistanceMetric = %v, want L2Distance", cfg.DistanceMetric)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
	want := "IVF(centroids=256, dims=16, nprobe=10, metric=L2)"
	if cfg.String() != want {
		t.Errorf("String() = %q, want %q", cfg.String(), want)
	}
}

// TestStubDistanceMetricString verifies the stub distance metric names
// match the real package.
func TestStubDistanceMetricString(t *testing.T) {
	tests := []struct {
		metric DistanceMetric
		want   string
	}{
		{L2Distance, "L2"},
		{CosineDistance, "Cosine"},
		{DotProductDistance, "DotProduct"},
		{ManhattanDistance, "Manhattan"},
		{DistanceMetric(99), "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.metric.String(); got != tt.want {
			t.Errorf("DistanceMetric(%d).String() = %q, want %q", tt.metric, got, tt.want)
		}
	}
}
