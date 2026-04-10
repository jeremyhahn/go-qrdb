//go:build codec_yaml

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

package dao

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewCodecYAML validates YAML codec creation and operations.
func TestNewCodecYAML(t *testing.T) {
	t.Run("CreateYAMLCodec", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecYAML)
		require.NoError(t, err)
		require.NotNil(t, codec)
		assert.Equal(t, CodecYAML, codec.Type())
	})

	t.Run("YAMLMarshalUnmarshal", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecYAML)
		require.NoError(t, err)

		original := &TestCodecEntity{
			ID:    67890,
			Name:  "YAMLTest",
			Value: 123.45,
		}

		// Marshal
		data, err := codec.Marshal(original)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		// Unmarshal
		decoded := &TestCodecEntity{}
		err = codec.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, original.ID, decoded.ID)
		assert.Equal(t, original.Name, decoded.Name)
		assert.Equal(t, original.Value, decoded.Value)
	})
}

// TestCodecUnmarshalInvalidDataYAML validates error handling for invalid data.
func TestCodecUnmarshalInvalidDataYAML(t *testing.T) {
	t.Run("InvalidYAMLData", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecYAML)
		require.NoError(t, err)

		invalidData := []byte("invalid: yaml: data:\n\t- broken")
		decoded := &TestCodecEntity{}
		err = codec.Unmarshal(invalidData, &decoded)
		require.Error(t, err)
	})
}

// TestCodecTypeYAML validates the Type() method for YAML codec.
func TestCodecTypeYAML(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecYAML)
	require.NoError(t, err)
	assert.Equal(t, CodecYAML, codec.Type())
}

// TestCodecRoundTripYAML validates round-trip encoding/decoding for YAML.
func TestCodecRoundTripYAML(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecYAML)
	require.NoError(t, err)

	original := &TestCodecEntity{
		ID:    99999,
		Name:  "RoundTrip",
		Value: 888.88,
	}

	// Marshal
	data, err := codec.Marshal(original)
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Unmarshal
	decoded := &TestCodecEntity{}
	err = codec.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify all fields
	assert.Equal(t, original.ID, decoded.ID)
	assert.Equal(t, original.Name, decoded.Name)
	assert.Equal(t, original.Value, decoded.Value)
}
