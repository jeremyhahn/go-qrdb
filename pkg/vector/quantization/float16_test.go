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
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFloat16_EncodeDecode(t *testing.T) {
	c := NewFloat16Converter()

	input := []float32{0.0, 1.0, -1.0, 0.5, 0.123, 100.0, -42.5}
	encoded := c.Encode(input)
	require.Len(t, encoded, len(input)*2)

	decoded := c.Decode(encoded)
	require.Len(t, decoded, len(input))

	// Float16 has ~3 decimal digits of precision.
	for i, v := range input {
		if v == 0 {
			assert.Equal(t, float32(0), decoded[i])
		} else {
			relErr := math.Abs(float64(decoded[i]-v)) / math.Abs(float64(v))
			assert.Less(t, relErr, 0.01, "Float16 relative error for %.4f should be < 1%%, got %.6f", v, relErr)
		}
	}
}

func TestFloat16_SpecialValues(t *testing.T) {
	c := NewFloat16Converter()

	tests := []struct {
		name  string
		value float32
	}{
		{"positive zero", 0.0},
		{"negative zero", float32(math.Copysign(0, -1))},
		{"positive infinity", float32(math.Inf(1))},
		{"negative infinity", float32(math.Inf(-1))},
		{"NaN", float32(math.NaN())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := c.Encode([]float32{tt.value})
			decoded := c.Decode(encoded)
			require.Len(t, decoded, 1)

			if math.IsNaN(float64(tt.value)) {
				assert.True(t, math.IsNaN(float64(decoded[0])), "NaN should round-trip")
			} else if math.IsInf(float64(tt.value), 1) {
				assert.True(t, math.IsInf(float64(decoded[0]), 1), "positive infinity should round-trip")
			} else if math.IsInf(float64(tt.value), -1) {
				assert.True(t, math.IsInf(float64(decoded[0]), -1), "negative infinity should round-trip")
			} else {
				assert.Equal(t, tt.value, decoded[0])
			}
		})
	}
}

func TestFloat16_Range(t *testing.T) {
	c := NewFloat16Converter()

	// Float16 max is ~65504, min positive is ~5.96e-8.
	tests := []struct {
		name  string
		value float32
		delta float64
	}{
		{"near max", 65000.0, 100.0},
		{"small positive", 0.001, 0.0001},
		{"near min normal", 6.1e-5, 1e-5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := c.Encode([]float32{tt.value})
			decoded := c.Decode(encoded)
			assert.InDelta(t, tt.value, decoded[0], tt.delta)
		})
	}
}

func TestFloat16_EmptyVector(t *testing.T) {
	c := NewFloat16Converter()
	encoded := c.Encode([]float32{})
	assert.Empty(t, encoded)
	decoded := c.Decode(encoded)
	assert.Empty(t, decoded)
}

func TestFloat16_EncodedSize(t *testing.T) {
	c := NewFloat16Converter()
	assert.Equal(t, 256, c.EncodedSize(128))
	assert.Equal(t, 1536, c.EncodedSize(768))
}

func TestBFloat16_EncodeDecode(t *testing.T) {
	c := NewBFloat16Converter()

	input := []float32{0.0, 1.0, -1.0, 0.5, 3.14159, 100.0, -42.5, 1e10, -1e-10}
	encoded := c.Encode(input)
	require.Len(t, encoded, len(input)*2)

	decoded := c.Decode(encoded)
	require.Len(t, decoded, len(input))

	// BFloat16 has ~2 decimal digits of precision but full float32 range.
	for i, v := range input {
		if v == 0 {
			// Zero should round-trip exactly.
			assert.Equal(t, float32(0), decoded[i])
		} else {
			relErr := math.Abs(float64(decoded[i]-v)) / math.Abs(float64(v))
			assert.Less(t, relErr, 0.02, "BFloat16 relative error for %g should be < 2%%, got %.6f", v, relErr)
		}
	}
}

func TestBFloat16_PreservesRange(t *testing.T) {
	c := NewBFloat16Converter()

	// BFloat16 preserves the exponent range of float32, so very large and very small
	// values should survive round-trip (with precision loss).
	tests := []struct {
		name   string
		value  float32
		maxRel float64
	}{
		{"very large", 1e38, 0.02},
		{"very small", 1e-38, 0.02},
		{"negative large", -1e30, 0.02},
		{"one", 1.0, 0},
		{"negative one", -1.0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := c.Encode([]float32{tt.value})
			decoded := c.Decode(encoded)
			require.Len(t, decoded, 1)

			if tt.maxRel == 0 {
				assert.Equal(t, tt.value, decoded[0])
			} else {
				relErr := math.Abs(float64(decoded[0]-tt.value)) / math.Abs(float64(tt.value))
				assert.Less(t, relErr, tt.maxRel)
			}
		})
	}
}

func TestBFloat16_SpecialValues(t *testing.T) {
	c := NewBFloat16Converter()

	tests := []struct {
		name  string
		value float32
	}{
		{"positive zero", 0.0},
		{"negative zero", float32(math.Copysign(0, -1))},
		{"positive infinity", float32(math.Inf(1))},
		{"negative infinity", float32(math.Inf(-1))},
		{"NaN", float32(math.NaN())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := c.Encode([]float32{tt.value})
			decoded := c.Decode(encoded)
			require.Len(t, decoded, 1)

			if math.IsNaN(float64(tt.value)) {
				assert.True(t, math.IsNaN(float64(decoded[0])), "NaN should round-trip")
			} else if math.IsInf(float64(tt.value), 1) {
				assert.True(t, math.IsInf(float64(decoded[0]), 1))
			} else if math.IsInf(float64(tt.value), -1) {
				assert.True(t, math.IsInf(float64(decoded[0]), -1))
			} else {
				assert.Equal(t, tt.value, decoded[0])
			}
		})
	}
}

func TestBFloat16_EmptyVector(t *testing.T) {
	c := NewBFloat16Converter()
	encoded := c.Encode([]float32{})
	assert.Empty(t, encoded)
	decoded := c.Decode(encoded)
	assert.Empty(t, decoded)
}

func TestBFloat16_EncodedSize(t *testing.T) {
	c := NewBFloat16Converter()
	assert.Equal(t, 256, c.EncodedSize(128))
}

func BenchmarkFloat16_Encode_128(b *testing.B) {
	benchmarkFloat16Encode(b, 128)
}

func BenchmarkFloat16_Encode_768(b *testing.B) {
	benchmarkFloat16Encode(b, 768)
}

func BenchmarkBFloat16_Encode_128(b *testing.B) {
	benchmarkBFloat16Encode(b, 128)
}

func BenchmarkBFloat16_Encode_768(b *testing.B) {
	benchmarkBFloat16Encode(b, 768)
}

func BenchmarkFloat16_Decode_128(b *testing.B) {
	c := NewFloat16Converter()
	vec := make([]float32, 128)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}
	encoded := c.Encode(vec)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = c.Decode(encoded)
	}
}

func BenchmarkBFloat16_Decode_128(b *testing.B) {
	c := NewBFloat16Converter()
	vec := make([]float32, 128)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}
	encoded := c.Encode(vec)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = c.Decode(encoded)
	}
}

func benchmarkFloat16Encode(b *testing.B, dim int) {
	c := NewFloat16Converter()
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = c.Encode(vec)
	}
}

func benchmarkBFloat16Encode(b *testing.B, dim int) {
	c := NewBFloat16Converter()
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = c.Encode(vec)
	}
}
