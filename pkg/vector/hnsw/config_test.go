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

package hnsw

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, 16, config.M)
	assert.Equal(t, 32, config.MMax)
	assert.Equal(t, 200, config.EfConstruction)
	assert.Equal(t, 50, config.EfSearch)
	assert.InDelta(t, 1.0/math.Log(16), config.Ml, 1e-9)
	assert.Equal(t, L2Distance, config.DistanceMetric)
}

func TestConfig_Validate(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		config := &Config{
			M:              16,
			MMax:           32,
			EfConstruction: 200,
			EfSearch:       50,
			Ml:             1.0,
			DistanceMetric: L2Distance,
		}

		err := config.Validate()
		assert.NoError(t, err)
	})

	t.Run("invalid M", func(t *testing.T) {
		config := &Config{
			M:              0,
			EfConstruction: 200,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.Error(t, err)
		assert.Equal(t, ErrInvalidConfig, err)
	})

	t.Run("negative M", func(t *testing.T) {
		config := &Config{
			M:              -1,
			EfConstruction: 200,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.Error(t, err)
	})

	t.Run("invalid EfConstruction", func(t *testing.T) {
		config := &Config{
			M:              16,
			EfConstruction: 0,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.Error(t, err)
	})

	t.Run("invalid EfSearch", func(t *testing.T) {
		config := &Config{
			M:              16,
			EfConstruction: 200,
			EfSearch:       0,
		}

		err := config.Validate()
		assert.Error(t, err)
	})

	t.Run("auto-set MMax", func(t *testing.T) {
		config := &Config{
			M:              16,
			MMax:           0, // Will be auto-set
			EfConstruction: 200,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.NoError(t, err)
		assert.Equal(t, 32, config.MMax)
	})

	t.Run("auto-set Ml", func(t *testing.T) {
		config := &Config{
			M:              16,
			EfConstruction: 200,
			EfSearch:       50,
			Ml:             0, // Will be auto-set
		}

		err := config.Validate()
		assert.NoError(t, err)
		assert.InDelta(t, 1.0/math.Log(16), config.Ml, 1e-9)
	})

	t.Run("negative MMax becomes auto-set", func(t *testing.T) {
		config := &Config{
			M:              16,
			MMax:           -1,
			EfConstruction: 200,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.NoError(t, err)
		assert.Equal(t, 32, config.MMax)
	})

	t.Run("negative Ml becomes auto-set", func(t *testing.T) {
		config := &Config{
			M:              16,
			EfConstruction: 200,
			EfSearch:       50,
			Ml:             -1,
		}

		err := config.Validate()
		assert.NoError(t, err)
		assert.Greater(t, config.Ml, 0.0)
	})
}

func TestConfig_CustomMetrics(t *testing.T) {
	metrics := []DistanceMetric{
		L2Distance,
		CosineDistance,
		DotProductDistance,
	}

	for _, metric := range metrics {
		config := &Config{
			M:              8,
			EfConstruction: 100,
			EfSearch:       20,
			DistanceMetric: metric,
		}

		err := config.Validate()
		assert.NoError(t, err)
		assert.Equal(t, metric, config.DistanceMetric)
	}
}

func TestConfig_SmallValues(t *testing.T) {
	t.Run("small M", func(t *testing.T) {
		config := &Config{
			M:              2,
			EfConstruction: 10,
			EfSearch:       5,
		}

		err := config.Validate()
		assert.NoError(t, err)
		assert.Equal(t, 2, config.M)
		assert.Equal(t, 4, config.MMax)
	})

	t.Run("minimal valid config", func(t *testing.T) {
		config := &Config{
			M:              1,
			EfConstruction: 1,
			EfSearch:       1,
		}

		err := config.Validate()
		assert.NoError(t, err)
	})
}

func TestConfig_LargeValues(t *testing.T) {
	config := &Config{
		M:              64,
		MMax:           128,
		EfConstruction: 1000,
		EfSearch:       500,
	}

	err := config.Validate()
	assert.NoError(t, err)
	assert.Equal(t, 64, config.M)
	assert.Equal(t, 128, config.MMax)
}

func TestConfig_MlCalculation(t *testing.T) {
	tests := []struct {
		name string
		m    int
	}{
		{"M=2", 2},
		{"M=4", 4},
		{"M=8", 8},
		{"M=16", 16},
		{"M=32", 32},
		{"M=64", 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				M:              tt.m,
				EfConstruction: 100,
				EfSearch:       50,
			}

			err := config.Validate()
			require.NoError(t, err)

			expectedMl := 1.0 / math.Log(float64(tt.m))
			assert.InDelta(t, expectedMl, config.Ml, 1e-9)
		})
	}
}

func TestConfig_EdgeCases(t *testing.T) {
	t.Run("EfConstruction equals EfSearch", func(t *testing.T) {
		config := &Config{
			M:              16,
			EfConstruction: 50,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.NoError(t, err)
	})

	t.Run("EfConstruction less than EfSearch", func(t *testing.T) {
		config := &Config{
			M:              16,
			EfConstruction: 30,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.NoError(t, err)
	})

	t.Run("MMax equals M", func(t *testing.T) {
		config := &Config{
			M:              16,
			MMax:           16,
			EfConstruction: 200,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.NoError(t, err)
	})

	t.Run("MMax less than M", func(t *testing.T) {
		config := &Config{
			M:              16,
			MMax:           8,
			EfConstruction: 200,
			EfSearch:       50,
		}

		err := config.Validate()
		assert.NoError(t, err)
		// Config is valid, but this is unusual usage
	})
}

func TestConfig_ParameterRanges(t *testing.T) {
	t.Run("typical M range", func(t *testing.T) {
		for m := 8; m <= 32; m += 8 {
			config := &Config{
				M:              m,
				EfConstruction: 200,
				EfSearch:       50,
			}

			err := config.Validate()
			assert.NoError(t, err)
			assert.Equal(t, m*2, config.MMax)
		}
	})

	t.Run("typical EfConstruction range", func(t *testing.T) {
		for ef := 100; ef <= 400; ef += 100 {
			config := &Config{
				M:              16,
				EfConstruction: ef,
				EfSearch:       50,
			}

			err := config.Validate()
			assert.NoError(t, err)
		}
	})

	t.Run("typical EfSearch range", func(t *testing.T) {
		for ef := 10; ef <= 100; ef += 10 {
			config := &Config{
				M:              16,
				EfConstruction: 200,
				EfSearch:       ef,
			}

			err := config.Validate()
			assert.NoError(t, err)
		}
	})
}

func TestConfig_Immutability(t *testing.T) {
	// Test that DefaultConfig returns a new instance each time
	config1 := DefaultConfig()
	config2 := DefaultConfig()

	config1.M = 8
	assert.Equal(t, 16, config2.M, "Modifying one config should not affect another")
}

func TestConfig_MultipleValidations(t *testing.T) {
	config := &Config{
		M:              16,
		MMax:           0,
		EfConstruction: 200,
		EfSearch:       50,
		Ml:             0,
	}

	// First validation
	err := config.Validate()
	assert.NoError(t, err)
	assert.Equal(t, 32, config.MMax)
	assert.Greater(t, config.Ml, 0.0)

	// Second validation should not change values
	originalMMax := config.MMax
	originalMl := config.Ml

	err = config.Validate()
	assert.NoError(t, err)
	assert.Equal(t, originalMMax, config.MMax)
	assert.Equal(t, originalMl, config.Ml)
}
