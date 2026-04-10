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

func TestEncodeError_Error(t *testing.T) {
	inner := errors.New("marshal failed")
	err := &EncodeError{Codec: "json", Err: inner}
	assert.Equal(t, "codec: encode failed (json): marshal failed", err.Error())
}

func TestEncodeError_Unwrap(t *testing.T) {
	inner := errors.New("marshal failed")
	err := &EncodeError{Codec: "json", Err: inner}
	require.True(t, errors.Is(err, inner))
	assert.Equal(t, inner, err.Unwrap())
}

func TestDecodeError_Error(t *testing.T) {
	inner := errors.New("unmarshal failed")
	err := &DecodeError{Codec: "msgpack", Err: inner}
	assert.Equal(t, "codec: decode failed (msgpack): unmarshal failed", err.Error())
}

func TestDecodeError_Unwrap(t *testing.T) {
	inner := errors.New("unmarshal failed")
	err := &DecodeError{Codec: "msgpack", Err: inner}
	require.True(t, errors.Is(err, inner))
	assert.Equal(t, inner, err.Unwrap())
}

func TestTranscodeError_Error(t *testing.T) {
	inner := errors.New("conversion failed")
	err := &TranscodeError{FromCodec: "msgpack", ToCodec: "json", Err: inner}
	assert.Equal(t, "codec: transcode failed (msgpack -> json): conversion failed", err.Error())
}

func TestTranscodeError_Unwrap(t *testing.T) {
	inner := errors.New("conversion failed")
	err := &TranscodeError{FromCodec: "msgpack", ToCodec: "json", Err: inner}
	require.True(t, errors.Is(err, inner))
	assert.Equal(t, inner, err.Unwrap())
}

func TestUnsupportedCodecError_Error(t *testing.T) {
	err := &UnsupportedCodecError{Name: "avro"}
	assert.Equal(t, "codec: unsupported codec type: avro", err.Error())
}

func TestNilInputError_Error(t *testing.T) {
	err := &NilInputError{Codec: "json"}
	assert.Equal(t, "codec: nil target passed to unmarshal (json)", err.Error())
}

func TestEmptyDataError_Error(t *testing.T) {
	err := &EmptyDataError{Codec: "msgpack"}
	assert.Equal(t, "codec: empty data passed to unmarshal (msgpack)", err.Error())
}

func TestConfigValidationError_Error(t *testing.T) {
	err := &ConfigValidationError{Field: "storage_codec", Message: "must not be empty"}
	assert.Equal(t, "codec: config validation failed: storage_codec: must not be empty", err.Error())
}

func TestEncodeError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	err := &EncodeError{Codec: "json", Err: inner}

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, "json", encErr.Codec)
}

func TestDecodeError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	err := &DecodeError{Codec: "yaml", Err: inner}

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
	assert.Equal(t, "yaml", decErr.Codec)
}

func TestTranscodeError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	err := &TranscodeError{FromCodec: "msgpack", ToCodec: "json", Err: inner}

	var tcErr *TranscodeError
	require.True(t, errors.As(err, &tcErr))
	assert.Equal(t, "msgpack", tcErr.FromCodec)
	assert.Equal(t, "json", tcErr.ToCodec)
}

func TestUnsupportedCodecError_ErrorsAs(t *testing.T) {
	err := &UnsupportedCodecError{Name: "bson"}

	var ucErr *UnsupportedCodecError
	require.True(t, errors.As(err, &ucErr))
	assert.Equal(t, "bson", ucErr.Name)
}

func TestYamlPanicError_Error(t *testing.T) {
	err := &yamlPanicError{msg: "cannot marshal type: func()"}
	assert.Equal(t, "cannot marshal type: func()", err.Error())
}

func TestNilInputError_ErrorsAs(t *testing.T) {
	err := &NilInputError{Codec: "json"}

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, "json", nilErr.Codec)
}

func TestEmptyDataError_ErrorsAs(t *testing.T) {
	err := &EmptyDataError{Codec: "msgpack"}

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
	assert.Equal(t, "msgpack", emptyErr.Codec)
}

func TestConfigValidationError_ErrorsAs(t *testing.T) {
	err := &ConfigValidationError{Field: "wire_codec", Message: "unsupported"}

	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr))
	assert.Equal(t, "wire_codec", cvErr.Field)
}
