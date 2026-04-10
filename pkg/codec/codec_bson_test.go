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

//go:build codec_bson

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBSONCodec_Type(t *testing.T) {
	c := &BSONCodec{}
	assert.Equal(t, TypeBSON, c.Type())
}

func TestBSONCodec_RoundTrip(t *testing.T) {
	c := &BSONCodec{}

	type doc struct {
		Name  string `bson:"name"`
		Value int64  `bson:"value"`
	}
	original := doc{Name: "test-key", Value: 42}

	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded doc
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestBSONCodec_UnmarshalNilTarget(t *testing.T) {
	c := &BSONCodec{}
	// Provide minimal valid BSON (5-byte empty document).
	err := c.Unmarshal([]byte{0x05, 0x00, 0x00, 0x00, 0x00}, nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, TypeBSON, nilErr.Codec)
}

func TestBSONCodec_UnmarshalEmptyData(t *testing.T) {
	c := &BSONCodec{}
	var out map[string]any
	err := c.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
	assert.Equal(t, TypeBSON, emptyErr.Codec)
}

func TestBSONCodec_UnmarshalInvalidData(t *testing.T) {
	c := &BSONCodec{}
	var out map[string]any
	// Corrupt BSON: claims length of 100 but has far fewer bytes.
	err := c.Unmarshal([]byte{0x64, 0x00, 0x00, 0x00, 0xFF}, &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
	assert.Equal(t, TypeBSON, decErr.Codec)
}

func TestBSONCodec_MapRoundTrip(t *testing.T) {
	c := &BSONCodec{}
	original := map[string]string{"foo": "bar", "baz": "qux"}

	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded map[string]string
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}
