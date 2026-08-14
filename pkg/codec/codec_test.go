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
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPayload is a representative struct used for round-trip tests.
type testPayload struct {
	Name    string   `json:"name" msgpack:"name" yaml:"name" bson:"name"`
	Value   int64    `json:"value" msgpack:"value" yaml:"value" bson:"value"`
	Tags    []string `json:"tags" msgpack:"tags" yaml:"tags" bson:"tags"`
	Nested  *nested  `json:"nested,omitempty" msgpack:"nested,omitempty" yaml:"nested,omitempty" bson:"nested,omitempty"`
	Enabled bool     `json:"enabled" msgpack:"enabled" yaml:"enabled" bson:"enabled"`
}

type nested struct {
	Score float64 `json:"score" msgpack:"score" yaml:"score" bson:"score"`
}

func samplePayload() testPayload {
	return testPayload{
		Name:    "test-key",
		Value:   42,
		Tags:    []string{"alpha", "beta"},
		Nested:  &nested{Score: 3.14},
		Enabled: true,
	}
}

// ---------------------------------------------------------------------------
// JSON codec
// ---------------------------------------------------------------------------

func TestJSONCodec_RoundTrip(t *testing.T) {
	c := &JSONCodec{}
	assert.Equal(t, TypeJSON, c.Type())

	original := samplePayload()
	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestJSONCodec_MarshalError(t *testing.T) {
	c := &JSONCodec{}
	// Channels cannot be JSON-marshaled.
	_, err := c.Marshal(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeJSON, encErr.Codec)
}

func TestJSONCodec_UnmarshalNilTarget(t *testing.T) {
	c := &JSONCodec{}
	err := c.Unmarshal([]byte(`{}`), nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
}

func TestJSONCodec_UnmarshalEmptyData(t *testing.T) {
	c := &JSONCodec{}
	var out testPayload
	err := c.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
}

func TestJSONCodec_UnmarshalInvalidData(t *testing.T) {
	c := &JSONCodec{}
	var out testPayload
	err := c.Unmarshal([]byte("not json"), &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
	assert.Equal(t, TypeJSON, decErr.Codec)
}

// ---------------------------------------------------------------------------
// Edge cases: large payload round-trip
// ---------------------------------------------------------------------------

func TestJSONCodec_LargePayload(t *testing.T) {
	c := &JSONCodec{}
	tags := make([]string, 1000)
	for i := range tags {
		tags[i] = "tag-" + string(rune('A'+i%26))
	}
	original := testPayload{
		Name:  "large",
		Value: math.MaxInt64,
		Tags:  tags,
	}

	data, err := c.Marshal(original)
	require.NoError(t, err)

	var decoded testPayload
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original.Name, decoded.Name)
	assert.Equal(t, original.Value, decoded.Value)
	assert.Len(t, decoded.Tags, 1000)
}

// ---------------------------------------------------------------------------
// Primitive type round-trips
// ---------------------------------------------------------------------------

func TestJSONCodec_PrimitiveTypes(t *testing.T) {
	c := &JSONCodec{}

	t.Run("string", func(t *testing.T) {
		data, err := c.Marshal("hello")
		require.NoError(t, err)
		var out string
		require.NoError(t, c.Unmarshal(data, &out))
		assert.Equal(t, "hello", out)
	})

	t.Run("int", func(t *testing.T) {
		data, err := c.Marshal(42)
		require.NoError(t, err)
		var out int
		require.NoError(t, c.Unmarshal(data, &out))
		assert.Equal(t, 42, out)
	})

	t.Run("bool", func(t *testing.T) {
		data, err := c.Marshal(true)
		require.NoError(t, err)
		var out bool
		require.NoError(t, c.Unmarshal(data, &out))
		assert.True(t, out)
	})

	t.Run("nil_value", func(t *testing.T) {
		data, err := c.Marshal(nil)
		require.NoError(t, err)
		var out any
		require.NoError(t, c.Unmarshal(data, &out))
		assert.Nil(t, out)
	})
}
