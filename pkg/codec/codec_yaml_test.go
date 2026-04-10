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

//go:build codec_yaml

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestYAMLCodec_RoundTrip(t *testing.T) {
	c := &YAMLCodec{}
	assert.Equal(t, TypeYAML, c.Type())

	original := samplePayload()
	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestYAMLCodec_MarshalError(t *testing.T) {
	c := &YAMLCodec{}
	// Functions cannot be YAML-marshaled.
	_, err := c.Marshal(func() {})
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeYAML, encErr.Codec)
}

func TestYAMLCodec_UnmarshalNilTarget(t *testing.T) {
	c := &YAMLCodec{}
	err := c.Unmarshal([]byte("name: test"), nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
}

func TestYAMLCodec_UnmarshalEmptyData(t *testing.T) {
	c := &YAMLCodec{}
	var out testPayload
	err := c.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
}

func TestYAMLCodec_UnmarshalInvalidData(t *testing.T) {
	c := &YAMLCodec{}
	// Invalid YAML: tab characters in indentation are not permitted.
	invalidYAML := []byte(":\n\t- :\n\t\t-")
	var out map[string]any
	err := c.Unmarshal(invalidYAML, &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}
