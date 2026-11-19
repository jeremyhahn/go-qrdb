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

//go:build codec_msgpack

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMsgPackCodec_RoundTrip(t *testing.T) {
	c := &MsgPackCodec{}
	assert.Equal(t, TypeMsgPack, c.Type())

	original := samplePayload()
	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestMsgPackCodec_MarshalError(t *testing.T) {
	c := &MsgPackCodec{}
	_, err := c.Marshal(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeMsgPack, encErr.Codec)
}

func TestMsgPackCodec_UnmarshalNilTarget(t *testing.T) {
	c := &MsgPackCodec{}
	err := c.Unmarshal([]byte{0x80}, nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
}

func TestMsgPackCodec_UnmarshalEmptyData(t *testing.T) {
	c := &MsgPackCodec{}
	var out testPayload
	err := c.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
}

func TestMsgPackCodec_UnmarshalInvalidData(t *testing.T) {
	c := &MsgPackCodec{}
	var out testPayload
	err := c.Unmarshal([]byte{0xff, 0xfe, 0xfd}, &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}
