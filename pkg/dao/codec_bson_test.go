//go:build codec_bson

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

// TestNewCodecBSON validates BSON codec creation and operations.
func TestNewCodecBSON(t *testing.T) {
	t.Run("CreateBSONCodec", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecBSON)
		require.NoError(t, err)
		require.NotNil(t, codec)
		assert.Equal(t, CodecBSON, codec.Type())
	})

	t.Run("BSONMarshalUnmarshal", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecBSON)
		require.NoError(t, err)

		original := &TestCodecEntity{
			ID:    33333,
			Name:  "BSONTest",
			Value: 234.56,
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

// TestCodecTypeBSON validates the Type() method for BSON codec.
func TestCodecTypeBSON(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecBSON)
	require.NoError(t, err)
	assert.Equal(t, CodecBSON, codec.Type())
}

// TestCodecRoundTripBSON validates round-trip encoding/decoding for BSON.
func TestCodecRoundTripBSON(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecBSON)
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
