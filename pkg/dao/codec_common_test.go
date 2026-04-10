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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodecEntity is a test entity for codec testing.
type TestCodecEntity struct {
	ID    uint64  `json:"id" yaml:"id" toml:"id" bson:"id" msgpack:"id"`
	Name  string  `json:"name" yaml:"name" toml:"name" bson:"name" msgpack:"name"`
	Value float64 `json:"value" yaml:"value" toml:"value" bson:"value" msgpack:"value"`
}

func (e *TestCodecEntity) EntityID() uint64 {
	return e.ID
}

func (e *TestCodecEntity) SetEntityID(id uint64) {
	e.ID = id
}

// TestNewCodecUnsupported validates error handling for unsupported codecs.
func TestNewCodecUnsupported(t *testing.T) {
	t.Run("UnsupportedCodecType", func(t *testing.T) {
		// Test with a codec type that will never exist, ensuring this test
		// always exercises the unsupported codec error path regardless of build tags
		codec, err := NewCodec[*TestCodecEntity](CodecType("nonexistent_test_codec"))
		require.Error(t, err)
		assert.Nil(t, codec)

		var codecErr CodecCreationError
		require.True(t, errors.As(err, &codecErr))
		assert.Equal(t, "nonexistent_test_codec", codecErr.CodecType)
		// Error message comes from go-codec's ErrCodecNotSupported
		assert.Contains(t, codecErr.Error(), "nonexistent_test_codec")
		assert.Contains(t, codecErr.Error(), "not supported")
	})

	t.Run("ProtoBufRequiresSpecialConstructor", func(t *testing.T) {
		// ProtoBuf always returns an error from NewCodec because it requires
		// types to implement proto.Message. Users must use NewProtoBufCodec[E]() instead.
		// This tests both scenarios:
		// - When protobuf is NOT supported: returns "not supported" error
		// - When protobuf IS supported: returns "use NewProtoBufCodec" guidance
		codec, err := NewCodec[*TestCodecEntity](CodecProtoBuf)
		require.Error(t, err)
		assert.Nil(t, codec)

		var codecErr CodecCreationError
		require.True(t, errors.As(err, &codecErr))
		assert.Equal(t, "protobuf", codecErr.CodecType)

		// Error message depends on whether protobuf is supported
		if IsCodecSupported(CodecProtoBuf) {
			// When protobuf is supported, NewCodec tells user to use NewProtoBufCodec
			assert.Contains(t, codecErr.Error(), "NewProtoBufCodec")
		} else {
			// When protobuf is not supported
			assert.Contains(t, codecErr.Error(), "protobuf")
			assert.Contains(t, codecErr.Error(), "not supported")
		}
	})

	t.Run("EmptyCodecType", func(t *testing.T) {
		// Test with empty string codec type
		codec, err := NewCodec[*TestCodecEntity](CodecType(""))
		require.Error(t, err)
		assert.Nil(t, codec)

		var codecErr CodecCreationError
		require.True(t, errors.As(err, &codecErr))
		assert.Equal(t, "", codecErr.CodecType)
	})
}
