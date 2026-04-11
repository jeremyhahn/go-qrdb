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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuantizationType_String(t *testing.T) {
	tests := []struct {
		name     string
		qt       QuantizationType
		expected string
	}{
		{name: "none", qt: QuantNone, expected: "none"},
		{name: "sq8", qt: QuantSQ8, expected: "sq8"},
		{name: "pq", qt: QuantPQ, expected: "pq"},
		{name: "float16", qt: QuantFloat16, expected: "float16"},
		{name: "bfloat16", qt: QuantBFloat16, expected: "bfloat16"},
		{name: "unknown", qt: QuantizationType(99), expected: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.qt.String())
		})
	}
}

func TestParseQuantizationType_ValidTypes(t *testing.T) {
	tests := []struct {
		input    string
		expected QuantizationType
	}{
		{"none", QuantNone},
		{"sq8", QuantSQ8},
		{"pq", QuantPQ},
		{"float16", QuantFloat16},
		{"bfloat16", QuantBFloat16},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			qt, ok := ParseQuantizationType(tt.input)
			assert.True(t, ok)
			assert.Equal(t, tt.expected, qt)
		})
	}
}

func TestParseQuantizationType_InvalidType(t *testing.T) {
	qt, ok := ParseQuantizationType("invalid")
	assert.False(t, ok)
	assert.Equal(t, QuantNone, qt)
}
