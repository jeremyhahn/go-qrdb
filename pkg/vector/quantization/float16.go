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
	"encoding/binary"
	"math"

	"github.com/x448/float16"
)

// Float16Converter provides IEEE 754 half-precision (float16) conversion for 2x
// memory reduction. Float16 has 1 sign bit, 5 exponent bits, and 10 mantissa bits,
// giving approximately 3 decimal digits of precision and a range of ~5.96e-8 to 65504.
type Float16Converter struct{}

// NewFloat16Converter creates a new Float16Converter.
func NewFloat16Converter() *Float16Converter {
	return &Float16Converter{}
}

// Encode converts a float32 vector to float16 bytes (2 bytes per element, little-endian).
func (c *Float16Converter) Encode(vector []float32) []byte {
	data := make([]byte, len(vector)*2)
	for i, v := range vector {
		f16 := float16.Fromfloat32(v)
		binary.LittleEndian.PutUint16(data[i*2:], f16.Bits())
	}
	return data
}

// Decode converts float16 bytes back to a float32 vector.
func (c *Float16Converter) Decode(data []byte) []float32 {
	n := len(data) / 2
	vector := make([]float32, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint16(data[i*2:])
		f16 := float16.Frombits(bits)
		vector[i] = f16.Float32()
	}
	return vector
}

// EncodedSize returns the encoded size in bytes for a vector of the given dimensionality.
func (c *Float16Converter) EncodedSize(dimensions int) int {
	return dimensions * 2
}

// BFloat16Converter provides Brain floating-point (bfloat16) conversion for 2x
// memory reduction. BFloat16 has 1 sign bit, 8 exponent bits, and 7 mantissa bits.
// It preserves the full exponent range of float32 but with reduced precision (~2
// decimal digits). This makes it ideal for ML embeddings trained with bfloat16.
type BFloat16Converter struct{}

// NewBFloat16Converter creates a new BFloat16Converter.
func NewBFloat16Converter() *BFloat16Converter {
	return &BFloat16Converter{}
}

// Encode converts a float32 vector to bfloat16 bytes (2 bytes per element, little-endian).
// BFloat16 encoding simply truncates the lower 16 bits of the float32 representation.
func (c *BFloat16Converter) Encode(vector []float32) []byte {
	data := make([]byte, len(vector)*2)
	for i, v := range vector {
		bf16 := uint16(math.Float32bits(v) >> 16)
		binary.LittleEndian.PutUint16(data[i*2:], bf16)
	}
	return data
}

// Decode converts bfloat16 bytes back to a float32 vector.
// BFloat16 decoding simply shifts the 16-bit value to the upper half of a float32.
func (c *BFloat16Converter) Decode(data []byte) []float32 {
	n := len(data) / 2
	vector := make([]float32, n)
	for i := 0; i < n; i++ {
		bf16 := binary.LittleEndian.Uint16(data[i*2:])
		vector[i] = math.Float32frombits(uint32(bf16) << 16)
	}
	return vector
}

// EncodedSize returns the encoded size in bytes for a vector of the given dimensionality.
func (c *BFloat16Converter) EncodedSize(dimensions int) int {
	return dimensions * 2
}
