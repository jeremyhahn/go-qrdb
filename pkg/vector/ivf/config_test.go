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

package ivf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig(128)

	assert.Equal(t, 256, config.NumCentroids)
	assert.Equal(t, 128, config.Dimensions)
	assert.Equal(t, 25, config.MaxIterations)
	assert.Equal(t, float32(0.0001), config.Convergence)
	assert.Equal(t, "kmeans++", config.InitMethod)
	assert.Equal(t, 10, config.DefaultNProbe)
	assert.Equal(t, L2Distance, config.DistanceMetric)
	assert.False(t, config.UseQuantization)
}

func TestAutoConfig(t *testing.T) {
	tests := []struct {
		name             string
		dimensions       int
		datasetSize      int
		wantCentroids    int
		wantNProbe       int
		wantQuantization bool
	}{
		{
			name:             "small dataset",
			dimensions:       128,
			datasetSize:      100,
			wantCentroids:    16, // sqrt(100) = 10, but minimum is 16
			wantNProbe:       1,
			wantQuantization: false,
		},
		{
			name:             "medium dataset",
			dimensions:       256,
			datasetSize:      10000,
			wantCentroids:    100, // sqrt(10000) = 100
			wantNProbe:       5,
			wantQuantization: false,
		},
		{
			name:             "large dataset",
			dimensions:       512,
			datasetSize:      200000,
			wantCentroids:    447, // sqrt(200000) ~ 447
			wantNProbe:       22,
			wantQuantization: true,
		},
		{
			name:             "very large dataset",
			dimensions:       1024,
			datasetSize:      50000000,
			wantCentroids:    4096, // sqrt(50M) > 4096, capped at max
			wantNProbe:       100,
			wantQuantization: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := AutoConfig(tt.dimensions, tt.datasetSize)

			assert.Equal(t, tt.dimensions, config.Dimensions)
			assert.Equal(t, tt.wantCentroids, config.NumCentroids)
			assert.Equal(t, tt.wantNProbe, config.DefaultNProbe)
			assert.Equal(t, tt.wantQuantization, config.UseQuantization)
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid config",
			config:  DefaultConfig(128),
			wantErr: false,
		},
		{
			name: "invalid num_centroids",
			config: &Config{
				NumCentroids:  0,
				Dimensions:    128,
				MaxIterations: 25,
				Convergence:   0.0001,
				InitMethod:    "kmeans++",
				DefaultNProbe: 10,
			},
			wantErr: true,
			errMsg:  "num_centroids must be positive",
		},
		{
			name: "invalid dimensions",
			config: &Config{
				NumCentroids:  256,
				Dimensions:    0,
				MaxIterations: 25,
				Convergence:   0.0001,
				InitMethod:    "kmeans++",
				DefaultNProbe: 10,
			},
			wantErr: true,
			errMsg:  "dimensions must be positive",
		},
		{
			name: "invalid max_iterations",
			config: &Config{
				NumCentroids:  256,
				Dimensions:    128,
				MaxIterations: 0,
				Convergence:   0.0001,
				InitMethod:    "kmeans++",
				DefaultNProbe: 10,
			},
			wantErr: true,
			errMsg:  "max_iterations must be positive",
		},
		{
			name: "invalid convergence",
			config: &Config{
				NumCentroids:  256,
				Dimensions:    128,
				MaxIterations: 25,
				Convergence:   0,
				InitMethod:    "kmeans++",
				DefaultNProbe: 10,
			},
			wantErr: true,
			errMsg:  "convergence must be positive",
		},
		{
			name: "invalid init_method",
			config: &Config{
				NumCentroids:  256,
				Dimensions:    128,
				MaxIterations: 25,
				Convergence:   0.0001,
				InitMethod:    "invalid",
				DefaultNProbe: 10,
			},
			wantErr: true,
			errMsg:  "init_method must be 'kmeans++' or 'random'",
		},
		{
			name: "invalid default_nprobe zero",
			config: &Config{
				NumCentroids:  256,
				Dimensions:    128,
				MaxIterations: 25,
				Convergence:   0.0001,
				InitMethod:    "kmeans++",
				DefaultNProbe: 0,
			},
			wantErr: true,
			errMsg:  "default_nprobe must be positive",
		},
		{
			name: "invalid default_nprobe exceeds centroids",
			config: &Config{
				NumCentroids:  256,
				Dimensions:    128,
				MaxIterations: 25,
				Convergence:   0.0001,
				InitMethod:    "kmeans++",
				DefaultNProbe: 300,
			},
			wantErr: true,
			errMsg:  "default_nprobe (300) cannot exceed num_centroids (256)",
		},
		{
			name: "quantization with invalid num_subvectors",
			config: &Config{
				NumCentroids:    256,
				Dimensions:      128,
				MaxIterations:   25,
				Convergence:     0.0001,
				InitMethod:      "kmeans++",
				DefaultNProbe:   10,
				UseQuantization: true,
				NumSubvectors:   0,
				NumBits:         8,
			},
			wantErr: true,
			errMsg:  "num_subvectors must be positive when quantization is enabled",
		},
		{
			name: "quantization with non-divisible dimensions",
			config: &Config{
				NumCentroids:    256,
				Dimensions:      127,
				MaxIterations:   25,
				Convergence:     0.0001,
				InitMethod:      "kmeans++",
				DefaultNProbe:   10,
				UseQuantization: true,
				NumSubvectors:   8,
				NumBits:         8,
			},
			wantErr: true,
			errMsg:  "dimensions (127) must be divisible by num_subvectors (8)",
		},
		{
			name: "quantization with invalid num_bits",
			config: &Config{
				NumCentroids:    256,
				Dimensions:      128,
				MaxIterations:   25,
				Convergence:     0.0001,
				InitMethod:      "kmeans++",
				DefaultNProbe:   10,
				UseQuantization: true,
				NumSubvectors:   8,
				NumBits:         20,
			},
			wantErr: true,
			errMsg:  "num_bits must be between 1 and 16",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfigString(t *testing.T) {
	config := DefaultConfig(128)
	str := config.String()

	assert.Contains(t, str, "IVF")
	assert.Contains(t, str, "centroids=256")
	assert.Contains(t, str, "dims=128")
	assert.Contains(t, str, "nprobe=10")
	assert.Contains(t, str, "metric=L2")
}

func TestDistanceMetricString(t *testing.T) {
	tests := []struct {
		metric DistanceMetric
		want   string
	}{
		{L2Distance, "L2"},
		{CosineDistance, "Cosine"},
		{DotProductDistance, "DotProduct"},
		{DistanceMetric(999), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.metric.String())
		})
	}
}
