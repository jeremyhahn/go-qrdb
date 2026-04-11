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

package quantization

import (
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// SQ8Quantizer provides 4x memory reduction by quantizing float32 vectors to uint8.
// Each dimension is independently quantized to [0, 255] using per-dimension min/max
// calibration values. This preserves relative distances well while dramatically
// reducing memory footprint.
type SQ8Quantizer struct {
	dimensions int
	mins       []float32 // per-dimension minimum values
	maxs       []float32 // per-dimension maximum values
	scales     []float32 // per-dimension scale = 255 / (max - min)
	calibrated atomic.Bool
	mu         sync.RWMutex // protects calibration state only
}

// NewSQ8Quantizer creates a new SQ8Quantizer for vectors of the given dimensionality.
func NewSQ8Quantizer(dimensions int) *SQ8Quantizer {
	return &SQ8Quantizer{
		dimensions: dimensions,
		mins:       make([]float32, dimensions),
		maxs:       make([]float32, dimensions),
		scales:     make([]float32, dimensions),
	}
}

// Calibrate computes per-dimension min/max ranges from the provided training vectors.
// This must be called before Encode or Decode. Calling it again re-calibrates.
func (q *SQ8Quantizer) Calibrate(vectors [][]float32) error {
	if len(vectors) == 0 {
		return &EmptyVectorSetError{Op: "SQ8Quantizer.Calibrate"}
	}

	for i, v := range vectors {
		if len(v) != q.dimensions {
			return &DimensionError{Got: len(v), Expected: q.dimensions}
		}
		if i == 0 {
			// Initialize min/max from the first vector.
			for d := 0; d < q.dimensions; d++ {
				q.mins[d] = v[d]
				q.maxs[d] = v[d]
			}
			continue
		}
		for d := 0; d < q.dimensions; d++ {
			if v[d] < q.mins[d] {
				q.mins[d] = v[d]
			}
			if v[d] > q.maxs[d] {
				q.maxs[d] = v[d]
			}
		}
	}

	// Compute scales. For constant dimensions (max == min), use scale=0
	// which maps everything to 0.
	for d := 0; d < q.dimensions; d++ {
		r := q.maxs[d] - q.mins[d]
		if r > 0 {
			q.scales[d] = 255.0 / r
		} else {
			q.scales[d] = 0
		}
	}

	q.calibrated.Store(true)
	return nil
}

// IsCalibrated returns whether the quantizer has been calibrated.
func (q *SQ8Quantizer) IsCalibrated() bool {
	return q.calibrated.Load()
}

// Encode quantizes a float32 vector to uint8 values. Each dimension is linearly
// mapped from [min, max] to [0, 255].
func (q *SQ8Quantizer) Encode(vector []float32) ([]uint8, error) {
	if !q.calibrated.Load() {
		return nil, &QuantizationError{
			Op:      "SQ8Quantizer.Encode",
			Message: "quantizer not calibrated",
			Err:     ErrNotCalibrated,
		}
	}

	if len(vector) != q.dimensions {
		return nil, &DimensionError{Got: len(vector), Expected: q.dimensions}
	}

	encoded := make([]uint8, q.dimensions)
	for d := 0; d < q.dimensions; d++ {
		if q.scales[d] == 0 {
			encoded[d] = 0
			continue
		}
		val := (vector[d] - q.mins[d]) * q.scales[d]
		// Clamp to [0, 255]
		if val < 0 {
			val = 0
		} else if val > 255 {
			val = 255
		}
		encoded[d] = uint8(val + 0.5) // Round to nearest
	}
	return encoded, nil
}

// Decode reconstructs a float32 vector from uint8 quantized values. This is a
// lossy reconstruction: the recovered values are the midpoints of quantization bins.
func (q *SQ8Quantizer) Decode(encoded []uint8) ([]float32, error) {
	if !q.calibrated.Load() {
		return nil, &QuantizationError{
			Op:      "SQ8Quantizer.Decode",
			Message: "quantizer not calibrated",
			Err:     ErrNotCalibrated,
		}
	}

	if len(encoded) != q.dimensions {
		return nil, &DimensionError{Got: len(encoded), Expected: q.dimensions}
	}

	decoded := make([]float32, q.dimensions)
	for d := 0; d < q.dimensions; d++ {
		if q.scales[d] == 0 {
			decoded[d] = q.mins[d]
			continue
		}
		decoded[d] = float32(encoded[d])/q.scales[d] + q.mins[d]
	}
	return decoded, nil
}

// AsymmetricDistance computes the distance between a full-precision query vector
// and a quantized database vector. The query stays at full precision while the
// database vector is decoded from uint8, providing better accuracy than symmetric
// (decoded-vs-decoded) comparison.
func (q *SQ8Quantizer) AsymmetricDistance(query []float32, encoded []uint8, metric hnsw.DistanceMetric) (float32, error) {
	if !q.calibrated.Load() {
		return 0, &QuantizationError{
			Op:      "SQ8Quantizer.AsymmetricDistance",
			Message: "quantizer not calibrated",
			Err:     ErrNotCalibrated,
		}
	}

	if len(query) != q.dimensions {
		return 0, &DimensionError{Got: len(query), Expected: q.dimensions}
	}

	if len(encoded) != q.dimensions {
		return 0, &DimensionError{Got: len(encoded), Expected: q.dimensions}
	}

	// Decode the quantized vector and compute distance with full-precision query.
	decoded, err := q.Decode(encoded)
	if err != nil {
		return 0, &QuantizationError{
			Op:      "SQ8Quantizer.AsymmetricDistance",
			Message: "failed to decode vector",
			Err:     err,
		}
	}

	distFn := hnsw.GetDistanceFunc(metric)
	return distFn(query, decoded), nil
}

// MemoryReduction returns the memory reduction factor compared to float32.
// SQ8 uses 1 byte per dimension vs 4 bytes for float32.
func (q *SQ8Quantizer) MemoryReduction() float64 {
	return 4.0
}

// MaxQuantizationError returns the maximum quantization error per dimension.
// This is half the bin width: (max - min) / (2 * 255).
func (q *SQ8Quantizer) MaxQuantizationError() []float32 {
	if !q.calibrated.Load() {
		return nil
	}
	errs := make([]float32, q.dimensions)
	for d := 0; d < q.dimensions; d++ {
		r := q.maxs[d] - q.mins[d]
		if r > 0 {
			errs[d] = r / (2 * 255)
		}
	}
	return errs
}

// EncodedSize returns the size in bytes of an encoded vector.
func (q *SQ8Quantizer) EncodedSize() int {
	return q.dimensions
}

// Dimensions returns the dimensionality of vectors this quantizer handles.
func (q *SQ8Quantizer) Dimensions() int {
	return q.dimensions
}
