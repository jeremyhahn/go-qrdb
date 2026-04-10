//go:build codec_json

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

// TestNewCodecJSON validates JSON codec creation and operations.
func TestNewCodecJSON(t *testing.T) {
	t.Run("CreateJSONCodec", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecJSON)
		require.NoError(t, err)
		require.NotNil(t, codec)
		assert.Equal(t, CodecJSON, codec.Type())
	})

	t.Run("JSONMarshalUnmarshal", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecJSON)
		require.NoError(t, err)

		original := &TestCodecEntity{
			ID:    12345,
			Name:  "TestEntity",
			Value: 99.99,
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

	t.Run("JSONMarshalNil", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecJSON)
		require.NoError(t, err)

		// Marshal nil entity
		var nilEntity *TestCodecEntity
		data, err := codec.Marshal(nilEntity)
		// Should marshal to "null"
		require.NoError(t, err)
		assert.NotNil(t, data)
	})
}

// TestCodecEmptyEntityJSON validates codec behavior with empty entities.
func TestCodecEmptyEntityJSON(t *testing.T) {
	t.Run("EmptyEntityJSON", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecJSON)
		require.NoError(t, err)

		empty := &TestCodecEntity{}

		data, err := codec.Marshal(empty)
		require.NoError(t, err)

		decoded := &TestCodecEntity{}
		err = codec.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, uint64(0), decoded.ID)
		assert.Equal(t, "", decoded.Name)
		assert.Equal(t, 0.0, decoded.Value)
	})
}

// TestCodecUnmarshalInvalidDataJSON validates error handling for invalid data.
func TestCodecUnmarshalInvalidDataJSON(t *testing.T) {
	t.Run("InvalidJSONData", func(t *testing.T) {
		codec, err := NewCodec[*TestCodecEntity](CodecJSON)
		require.NoError(t, err)

		invalidData := []byte("not valid json{")
		decoded := &TestCodecEntity{}
		err = codec.Unmarshal(invalidData, &decoded)
		require.Error(t, err)
	})
}

// TestCodecTypeJSON validates the Type() method for JSON codec.
func TestCodecTypeJSON(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecJSON)
	require.NoError(t, err)
	assert.Equal(t, CodecJSON, codec.Type())
}

// TestCodecRoundTripJSON validates round-trip encoding/decoding for JSON.
func TestCodecRoundTripJSON(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecJSON)
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
