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

// QuantizationType represents the type of vector quantization.
type QuantizationType int

const (
	// QuantNone indicates no quantization (full float32).
	QuantNone QuantizationType = iota
	// QuantSQ8 represents Scalar Quantization 8-bit (4x memory reduction).
	QuantSQ8
	// QuantPQ represents Product Quantization (high compression via codebook encoding).
	QuantPQ
	// QuantFloat16 represents IEEE 754 half precision (2x memory reduction).
	QuantFloat16
	// QuantBFloat16 represents Brain floating point (2x memory reduction, wider range).
	QuantBFloat16
)

// quantTypeNames maps QuantizationType values to their string representations.
var quantTypeNames = map[QuantizationType]string{
	QuantNone:     "none",
	QuantSQ8:      "sq8",
	QuantPQ:       "pq",
	QuantFloat16:  "float16",
	QuantBFloat16: "bfloat16",
}

// String returns the string representation of the quantization type.
func (qt QuantizationType) String() string {
	if name, ok := quantTypeNames[qt]; ok {
		return name
	}
	return "unknown"
}

// ParseQuantizationType parses a string into a QuantizationType.
var parseQuantTypeMap = map[string]QuantizationType{
	"none":     QuantNone,
	"sq8":      QuantSQ8,
	"pq":       QuantPQ,
	"float16":  QuantFloat16,
	"bfloat16": QuantBFloat16,
}

// ParseQuantizationType converts a string representation to a QuantizationType.
// Returns QuantNone and false if the string is not recognized.
func ParseQuantizationType(s string) (QuantizationType, bool) {
	qt, ok := parseQuantTypeMap[s]
	return qt, ok
}

// SQ8Config configures Scalar Quantization 8-bit.
type SQ8Config struct {
	// CalibrationSize is the number of vectors used to compute per-dimension min/max.
	// If zero, calibration uses all provided vectors.
	CalibrationSize int
}

// PQConfig configures Product Quantization.
type PQConfig struct {
	// M is the number of sub-quantizers (dimensions divided into M sub-spaces).
	// Dimensions must be evenly divisible by M.
	M int
	// Nbits is the bits per sub-quantizer code (typically 8, giving 256 centroids).
	Nbits int
	// TrainingSize is the number of vectors used for codebook training.
	// If zero, all provided training vectors are used.
	TrainingSize int
}

// Float16Config configures Float16/BFloat16 storage.
type Float16Config struct {
	// UseBFloat16 selects BFloat16 instead of IEEE 754 Float16.
	UseBFloat16 bool
}
