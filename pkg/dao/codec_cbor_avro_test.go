//go:build codec_cbor && codec_avro

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

// TestNewCodecCBOR validates CBOR codec creation and operations.
func TestNewCodecCBOR(t *testing.T) {
	t.Run("CreateCBORCodec", func(t *testing.T) {
		if !IsCodecSupported(CodecCBOR) {
			t.Skip("CBOR codec not supported in this build")
		}

		codec, err := NewCodec[*TestCodecEntity](CodecCBOR)
		require.NoError(t, err)
		require.NotNil(t, codec)
		assert.Equal(t, CodecCBOR, codec.Type())
	})

	t.Run("CBORMarshalUnmarshal", func(t *testing.T) {
		if !IsCodecSupported(CodecCBOR) {
			t.Skip("CBOR codec not supported in this build")
		}

		codec, err := NewCodec[*TestCodecEntity](CodecCBOR)
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
}

// TestNewCodecAvro validates Avro codec creation.
func TestNewCodecAvro(t *testing.T) {
	t.Run("CreateAvroCodec", func(t *testing.T) {
		if !IsCodecSupported(CodecAvro) {
			t.Skip("Avro codec not supported in this build")
		}

		codec, err := NewCodec[*TestCodecEntity](CodecAvro)
		require.NoError(t, err)
		require.NotNil(t, codec)
		assert.Equal(t, CodecAvro, codec.Type())
	})

	// Note: Avro has specific type restrictions (e.g., no uint64)
	// so we don't test marshal/unmarshal with TestCodecEntity here.
	// The DAO package wraps go-codec which handles Avro-compatible types.
}

// TestNewCBORCodecFunction tests the newCBORCodec function directly.
func TestNewCBORCodecFunction(t *testing.T) {
	codec := newCBORCodec[*TestCodecEntity]()
	require.NotNil(t, codec)
}

// TestNewAvroCodecFunction tests the newAvroCodec function directly.
func TestNewAvroCodecFunction(t *testing.T) {
	codec := newAvroCodec[*TestCodecEntity]()
	require.NotNil(t, codec)
}
