//go:build codec_toml

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

package dao

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewCodecTOML validates TOML codec creation and operations.
func TestNewCodecTOML(t *testing.T) {
	t.Run("CreateTOMLCodec", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecTOML)
		require.NoError(t, err)
		require.NotNil(t, codec)
		assert.Equal(t, CodecTOML, codec.Type())
	})

	t.Run("TOMLMarshalUnmarshal", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecTOML)
		require.NoError(t, err)

		original := &TestCodecEntity{
			ID:    11111,
			Name:  "TOMLTest",
			Value: 456.78,
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

// TestCodecTypeTOML validates the Type() method for TOML codec.
func TestCodecTypeTOML(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecTOML)
	require.NoError(t, err)
	assert.Equal(t, CodecTOML, codec.Type())
}

// TestCodecRoundTripTOML validates round-trip encoding/decoding for TOML.
func TestCodecRoundTripTOML(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecTOML)
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
