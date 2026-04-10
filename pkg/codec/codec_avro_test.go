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

//go:build codec_avro

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvroCodec_Type(t *testing.T) {
	c := &AvroCodec{}
	assert.Equal(t, TypeAvro, c.Type())
}

func TestAvroCodec_RoundTrip(t *testing.T) {
	c := &AvroCodec{}

	original := map[string]any{
		"name":    "test-key",
		"value":   float64(42),
		"enabled": true,
	}

	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded map[string]any
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original["name"], decoded["name"])
	assert.Equal(t, original["value"], decoded["value"])
	assert.Equal(t, original["enabled"], decoded["enabled"])
}

func TestAvroCodec_UnmarshalNilTarget(t *testing.T) {
	c := &AvroCodec{}
	// Encode something valid first to get non-empty Avro bytes.
	data, err := c.Marshal(map[string]any{"k": "v"})
	require.NoError(t, err)

	err = c.Unmarshal(data, nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, TypeAvro, nilErr.Codec)
}

func TestAvroCodec_UnmarshalEmptyData(t *testing.T) {
	c := &AvroCodec{}
	var out map[string]any
	err := c.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
	assert.Equal(t, TypeAvro, emptyErr.Codec)
}

func TestAvroCodec_UnmarshalInvalidData(t *testing.T) {
	c := &AvroCodec{}
	var out map[string]any
	// Random bytes that are not valid Avro-encoded bytes schema.
	err := c.Unmarshal([]byte{0xFF, 0xFE, 0xFD, 0xFC}, &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
	assert.Equal(t, TypeAvro, decErr.Codec)
}

func TestAvroCodec_MarshalError(t *testing.T) {
	c := &AvroCodec{}
	// Channels cannot be JSON-marshaled, so Marshal must return an EncodeError.
	_, err := c.Marshal(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeAvro, encErr.Codec)
}

func TestAvroCodec_StringRoundTrip(t *testing.T) {
	c := &AvroCodec{}

	original := "hello avro"
	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded string
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}
