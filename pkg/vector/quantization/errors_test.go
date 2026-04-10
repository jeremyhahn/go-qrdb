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

package quantization

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuantizationError_Error(t *testing.T) {
	err := &QuantizationError{
		Op:      "TestOp",
		Message: "something failed",
		Err:     ErrNotCalibrated,
	}
	assert.Equal(t, "TestOp: something failed: quantizer not calibrated", err.Error())
	assert.True(t, errors.Is(err, ErrNotCalibrated))
}

func TestQuantizationError_ErrorWithoutCause(t *testing.T) {
	err := &QuantizationError{
		Op:      "TestOp",
		Message: "something failed",
	}
	assert.Equal(t, "TestOp: something failed", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestDimensionError_Error(t *testing.T) {
	err := &DimensionError{Got: 64, Expected: 128}
	assert.Equal(t, "vector has 64 dimensions, expected 128", err.Error())
	assert.True(t, errors.Is(err, ErrDimensionMismatch))
}

func TestCodebookNotTrainedError_Error(t *testing.T) {
	err := &CodebookNotTrainedError{SubQuantizer: 3}
	assert.Equal(t, "codebook not trained for sub-quantizer 3", err.Error())
	assert.True(t, errors.Is(err, ErrNotTrained))
}

func TestPrecisionMismatchError_Error(t *testing.T) {
	err := &PrecisionMismatchError{Expected: QuantSQ8, Got: QuantFloat16}
	assert.Equal(t, "precision mismatch: expected sq8, got float16", err.Error())
}

func TestEmptyVectorSetError_Error(t *testing.T) {
	err := &EmptyVectorSetError{Op: "Train"}
	assert.Equal(t, "Train: empty vector set", err.Error())
}

func TestInvalidConfigError_Error(t *testing.T) {
	err := &InvalidConfigError{Message: "m must be positive"}
	assert.Equal(t, "invalid quantization config: m must be positive", err.Error())
}

func TestKMeansConvergenceError_Error(t *testing.T) {
	err := &KMeansConvergenceError{MaxIterations: 100}
	assert.Equal(t, "k-means did not converge within 100 iterations", err.Error())
}

func TestInsufficientVectorsError_Error(t *testing.T) {
	err := &InsufficientVectorsError{Required: 256, Got: 50}
	assert.Equal(t, "insufficient vectors: need at least 256, got 50", err.Error())
}

func TestDimensionError_IsNotDimensionMismatch(t *testing.T) {
	err := &DimensionError{Got: 64, Expected: 128}
	assert.False(t, errors.Is(err, ErrNotCalibrated))
}

func TestCodebookNotTrainedError_IsNotCalibrated(t *testing.T) {
	err := &CodebookNotTrainedError{SubQuantizer: 0}
	assert.False(t, errors.Is(err, ErrNotCalibrated))
}
