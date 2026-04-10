// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, TypeMsgPack, cfg.StorageCodec)
	assert.Equal(t, TypeJSON, cfg.WireCodec)
	assert.True(t, cfg.UseOptimized)
}

func TestConfig_Validate_Success(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "json_wire",
			cfg:  Config{StorageCodec: TypeJSON, WireCodec: TypeJSON},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			require.NoError(t, err)
		})
	}
}

func TestConfig_Validate_Error(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantField string
	}{
		{
			name:      "empty_storage_codec",
			cfg:       Config{StorageCodec: "", WireCodec: TypeJSON},
			wantField: "storage_codec",
		},
		{
			name:      "unsupported_storage_codec",
			cfg:       Config{StorageCodec: "unknown_codec_xyz", WireCodec: TypeJSON},
			wantField: "storage_codec",
		},
		{
			name:      "empty_wire_codec",
			cfg:       Config{StorageCodec: TypeJSON, WireCodec: ""},
			wantField: "wire_codec",
		},
		{
			name:      "unsupported_wire_codec",
			cfg:       Config{StorageCodec: TypeJSON, WireCodec: "unknown_codec_xyz"},
			wantField: "wire_codec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			require.Error(t, err)

			var cvErr *ConfigValidationError
			require.True(t, errors.As(err, &cvErr))
			assert.Equal(t, tt.wantField, cvErr.Field)
		})
	}
}

func TestResolveCodec_JSON(t *testing.T) {
	c, err := ResolveCodec(TypeJSON)
	require.NoError(t, err)
	assert.Equal(t, TypeJSON, c.Type())

	_, ok := c.(*JSONCodec)
	assert.True(t, ok, "expected *JSONCodec")
}

func TestResolveCodec_Error(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
	}{
		{"empty", ""},
		{"unsupported", "unknown_codec_xyz"},
		{"typo", "jsn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveCodec(tt.typeName)
			require.Error(t, err)

			var ucErr *UnsupportedCodecError
			require.True(t, errors.As(err, &ucErr))
			assert.Equal(t, tt.typeName, ucErr.Name)
		})
	}
}

func TestAvailableCodecs_ContainsJSON(t *testing.T) {
	codecs := AvailableCodecs()
	require.NotEmpty(t, codecs, "registry must have at least one codec (JSON)")

	found := false
	for _, name := range codecs {
		if name == TypeJSON {
			found = true
			break
		}
	}
	assert.True(t, found, "JSON codec must always be in registry")
}

func TestAvailableCodecs_Sorted(t *testing.T) {
	codecs := AvailableCodecs()
	for i := 1; i < len(codecs); i++ {
		assert.LessOrEqual(t, codecs[i-1], codecs[i], "AvailableCodecs must return sorted names")
	}
}

func TestRegisterCodec_CustomCodec(t *testing.T) {
	// Register a temporary test codec and verify it can be resolved.
	const testType = "test_codec_register_xyz"
	RegisterCodec(testType, func() Codec { return &JSONCodec{} })

	c, err := ResolveCodec(testType)
	require.NoError(t, err)
	assert.NotNil(t, c)

	// Clean up: re-registering with nil factory removes it from active use
	// but we cannot delete from the map here; just confirm the lookup works.
	delete(codecRegistry, testType)

	_, err = ResolveCodec(testType)
	require.Error(t, err)
	var ucErr *UnsupportedCodecError
	require.True(t, errors.As(err, &ucErr))
}
