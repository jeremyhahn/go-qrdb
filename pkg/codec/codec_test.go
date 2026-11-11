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
