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

package schema

import (
	"testing"
)

func TestEncodeDecodeVectorValue(t *testing.T) {
	tests := []struct {
		name string
		vec  *VectorValue
	}{
		{
			name: "simple_vector",
			vec: &VectorValue{
				ID:        1,
				Embedding: []float32{0.1, 0.2, 0.3},
				Metadata: map[string]string{
					"category": "tech",
					"author":   "alice",
				},
				CreatedAt: 1234567890,
				UpdatedAt: 1234567891,
				Version:   1,
			},
		},
		{
			name: "empty_embedding",
			vec: &VectorValue{
				ID:        2,
				Embedding: []float32{},
				Metadata:  map[string]string{"tag": "empty"},
				CreatedAt: 1234567890,
				UpdatedAt: 1234567891,
				Version:   1,
			},
		},
		{
			name: "nil_metadata",
			vec: &VectorValue{
				ID:        3,
				Embedding: []float32{1.0, 2.0},
				Metadata:  nil,
				CreatedAt: 1234567890,
				UpdatedAt: 1234567891,
				Version:   1,
			},
		},
		{
			name: "empty_metadata",
			vec: &VectorValue{
				ID:        4,
				Embedding: []float32{1.0},
				Metadata:  map[string]string{},
				CreatedAt: 1234567890,
				UpdatedAt: 1234567891,
				Version:   1,
			},
		},
		{
			name: "large_embedding",
			vec: &VectorValue{
				ID:        5,
				Embedding: make([]float32, 1536), // OpenAI ada-002 size
				Metadata: map[string]string{
					"model": "text-embedding-ada-002",
				},
				CreatedAt: 1234567890,
				UpdatedAt: 1234567891,
				Version:   2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode
			encoded, err := EncodeVectorValue(tt.vec)
			if err != nil {
				t.Fatalf("EncodeVectorValue failed: %v", err)
			}

			if len(encoded) == 0 {
				t.Error("encoded data should not be empty")
			}

			// Decode
			decoded, err := DecodeVectorValue(encoded)
			if err != nil {
				t.Fatalf("DecodeVectorValue failed: %v", err)
			}

			// Verify all fields
			if decoded.ID != tt.vec.ID {
				t.Errorf("ID mismatch: got %d, want %d", decoded.ID, tt.vec.ID)
			}

			if decoded.Version != tt.vec.Version {
				t.Errorf("Version mismatch: got %d, want %d", decoded.Version, tt.vec.Version)
			}

			// Verify embedding
			if len(decoded.Embedding) != len(tt.vec.Embedding) {
				t.Errorf("Embedding length mismatch: got %d, want %d", len(decoded.Embedding), len(tt.vec.Embedding))
			}
		})
	}
}

func TestDecodeVectorValueErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"invalid_msgpack", []byte{0xFF, 0xFF, 0xFF}},
		{"truncated", []byte{0x81, 0xA2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeVectorValue(tt.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestEncodeDecodeNamespaceInfo(t *testing.T) {
	tests := []struct {
		name string
		info *NamespaceInfo
	}{
		{
			name: "simple",
			info: &NamespaceInfo{
				Name:        "default",
				VectorCount: 100,
				Dimension:   768,
				CreatedAt:   1234567890,
				UpdatedAt:   1234567891,
			},
		},
		{
			name: "empty_namespace",
			info: &NamespaceInfo{
				Name:        "",
				VectorCount: 0,
				Dimension:   0,
				CreatedAt:   0,
				UpdatedAt:   0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode
			encoded, err := EncodeNamespaceInfo(tt.info)
			if err != nil {
				t.Fatalf("EncodeNamespaceInfo failed: %v", err)
			}

			// Decode
			decoded, err := DecodeNamespaceInfo(encoded)
			if err != nil {
				t.Fatalf("DecodeNamespaceInfo failed: %v", err)
			}

			// Verify all fields
			if decoded.Name != tt.info.Name {
				t.Errorf("Name mismatch: got %q, want %q", decoded.Name, tt.info.Name)
			}

			if decoded.VectorCount != tt.info.VectorCount {
				t.Errorf("VectorCount mismatch: got %d, want %d", decoded.VectorCount, tt.info.VectorCount)
			}
		})
	}
}

func TestDecodeNamespaceInfoErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"invalid_msgpack", []byte{0xFF, 0xFF, 0xFF}},
		{"truncated", []byte{0x81, 0xA4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeNamespaceInfo(tt.data)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestEstimateVectorSize(t *testing.T) {
	tests := []struct {
		name string
		vec  *VectorValue
	}{
		{
			name: "small_vector",
			vec: &VectorValue{
				ID:        1,
				Embedding: []float32{0.1, 0.2, 0.3},
				Metadata: map[string]string{
					"tag": "test",
				},
				Version: 1,
			},
		},
		{
			name: "large_embedding",
			vec: &VectorValue{
				ID:        3,
				Embedding: make([]float32, 1536),
				Metadata: map[string]string{
					"model": "ada-002",
				},
				Version: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := EstimateVectorSize(tt.vec)

			// Size should be positive
			if size <= 0 {
				t.Errorf("size should be positive, got %d", size)
			}

			// Verify estimate is reasonable compared to actual encoding
			encoded, err := EncodeVectorValue(tt.vec)
			if err != nil {
				t.Fatalf("EncodeVectorValue failed: %v", err)
			}

			actualSize := len(encoded)
			// Estimate should be within reasonable bounds (0.3x to 3x of actual)
			// The estimate is intentionally conservative and may be higher due to overhead estimation
			if size > actualSize*3 {
				t.Errorf("estimate too high: got %d, actual %d (%.1fx)", size, actualSize, float64(size)/float64(actualSize))
			}
			if size < actualSize/3 {
				t.Errorf("estimate too low: got %d, actual %d", size, actualSize)
			}
		})
	}
}

func BenchmarkEncodeVectorValue(b *testing.B) {
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 768),
		Metadata: map[string]string{
			"category": "tech",
			"author":   "alice",
		},
		CreatedAt: 1234567890,
		UpdatedAt: 1234567891,
		Version:   1,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = EncodeVectorValue(vec)
	}
}

func BenchmarkDecodeVectorValue(b *testing.B) {
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 768),
		Metadata: map[string]string{
			"category": "tech",
			"author":   "alice",
		},
		CreatedAt: 1234567890,
		UpdatedAt: 1234567891,
		Version:   1,
	}

	encoded, _ := EncodeVectorValue(vec)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = DecodeVectorValue(encoded)
	}
}

func BenchmarkEstimateVectorSize(b *testing.B) {
	vec := &VectorValue{
		ID:        1,
		Embedding: make([]float32, 768),
		Metadata: map[string]string{
			"category": "tech",
			"author":   "alice",
			"version":  "1.0",
		},
		CreatedAt: 1234567890,
		UpdatedAt: 1234567891,
		Version:   1,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateVectorSize(vec)
	}
}
