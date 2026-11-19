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

//go:build codec_protobuf

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestProtoBufCodec_MarshalNonProtoMessage(t *testing.T) {
	c := &ProtoBufCodec{}
	_, err := c.Marshal("not a proto message")
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeProtoBuf, encErr.Codec)
}

func TestProtoBufCodec_UnmarshalNilTarget(t *testing.T) {
	c := &ProtoBufCodec{}
	err := c.Unmarshal([]byte{0x0a}, nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
}

func TestProtoBufCodec_UnmarshalEmptyData(t *testing.T) {
	c := &ProtoBufCodec{}
	err := c.Unmarshal([]byte{}, &testPayload{})
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
}

func TestProtoBufCodec_UnmarshalNonProtoMessage(t *testing.T) {
	c := &ProtoBufCodec{}
	var out testPayload
	err := c.Unmarshal([]byte{0x0a, 0x01, 0x42}, &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}

func TestProtoBufTypeError_Error(t *testing.T) {
	err := &protoBufTypeError{value: "test"}
	assert.Equal(t, "value does not implement proto.Message", err.Error())
}

func TestProtoBufCodec_RoundTrip(t *testing.T) {
	c := &ProtoBufCodec{}
	assert.Equal(t, TypeProtoBuf, c.Type())

	// Use a StringValue so the marshalled bytes are non-empty.
	original := wrapperspb.String("hello proto")
	data, err := c.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data, "expected non-empty proto wire bytes")

	// Unmarshal back into the same message type.
	var decoded wrapperspb.StringValue
	err = c.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify structural equality.
	assert.True(t, proto.Equal(original, &decoded))
	assert.Equal(t, "hello proto", decoded.GetValue())
}

func TestProtoBufCodec_Unmarshal_InvalidWireBytes(t *testing.T) {
	c := &ProtoBufCodec{}
	var out wrapperspb.StringValue
	// 0x88, 0x01 is a valid field-tag for an unknown field but the varint is
	// truncated, making it an invalid wire encoding.
	err := c.Unmarshal([]byte{0x88, 0x01}, &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr), "expected DecodeError, got %T", err)
	assert.Equal(t, TypeProtoBuf, decErr.Codec)
}
