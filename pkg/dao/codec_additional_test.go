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

// TestSupportedCodecs validates the SupportedCodecs function.
func TestSupportedCodecs(t *testing.T) {
	t.Run("ReturnsSupportedCodecs", func(t *testing.T) {
		codecs := SupportedCodecs()
		require.NotNil(t, codecs)
		// At minimum, JSON should be available since we're running with codec_json tag
		found := false
		for _, c := range codecs {
			if c == CodecJSON {
				found = true
				break
			}
		}
		assert.True(t, found, "JSON codec should be in supported codecs list")
	})

	t.Run("AllReturnedCodecsAreValid", func(t *testing.T) {
		codecs := SupportedCodecs()
		for _, c := range codecs {
			assert.True(t, IsCodecSupported(c), "codec %s should be supported", c)
		}
	})
}

// TestIsCodecSupportedVariants tests various codec types.
func TestIsCodecSupportedVariants(t *testing.T) {
	t.Run("JSONSupported", func(t *testing.T) {
		assert.True(t, IsCodecSupported(CodecJSON))
	})

	t.Run("UnsupportedCodec", func(t *testing.T) {
		assert.False(t, IsCodecSupported(CodecType("fake_codec_xyz")))
	})

	t.Run("EmptyCodecType", func(t *testing.T) {
		assert.False(t, IsCodecSupported(CodecType("")))
	})
}

// TestNewCodecAllSupportedTypes validates NewCodec for various types.
func TestNewCodecAllSupportedTypes(t *testing.T) {
	testCases := []struct {
		name      string
		codecType CodecType
	}{
		{"JSON", CodecJSON},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !IsCodecSupported(tc.codecType) {
				t.Skipf("codec %s not supported in this build", tc.codecType)
			}

			codec, err := NewCodec[*TestCodecEntity](tc.codecType)
			require.NoError(t, err)
			require.NotNil(t, codec)
			assert.Equal(t, tc.codecType, codec.Type())

			// Test basic round-trip
			entity := &TestCodecEntity{ID: 1, Name: "Test", Value: 1.0}
			data, err := codec.Marshal(entity)
			require.NoError(t, err)

			decoded := &TestCodecEntity{}
			err = codec.Unmarshal(data, &decoded)
			require.NoError(t, err)
			assert.Equal(t, entity.ID, decoded.ID)
		})
	}
}

// TestCodecWrapperMethods tests codecWrapper methods directly.
func TestCodecWrapperMethods(t *testing.T) {
	codec, err := NewCodec[*TestCodecEntity](CodecJSON)
	require.NoError(t, err)

	t.Run("Marshal", func(t *testing.T) {
		entity := &TestCodecEntity{ID: 123, Name: "Test", Value: 45.67}
		data, err := codec.Marshal(entity)
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("Unmarshal", func(t *testing.T) {
		entity := &TestCodecEntity{ID: 123, Name: "Test", Value: 45.67}
		data, err := codec.Marshal(entity)
		require.NoError(t, err)

		decoded := &TestCodecEntity{}
		err = codec.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, entity.ID, decoded.ID)
	})

	t.Run("Type", func(t *testing.T) {
		assert.Equal(t, CodecJSON, codec.Type())
	})
}
