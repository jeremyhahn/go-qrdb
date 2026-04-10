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

//go:build codec_msgpack && codec_yaml

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})
	require.NotNil(t, m)
	assert.Equal(t, TypeMsgPack, m.StorageCodec().Type())
	assert.Equal(t, TypeJSON, m.WireCodec().Type())
}

func TestManager_EncodeDecodeStorage(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})
	original := samplePayload()

	data, err := m.EncodeForStorage(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = m.DecodeFromStorage(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestManager_EncodeDecodeWire(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})
	original := samplePayload()

	data, err := m.EncodeForWire(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	err = m.DecodeFromWire(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestManager_Transcode(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})
	original := samplePayload()

	// Encode to storage format (msgpack).
	storageData, err := m.EncodeForStorage(original)
	require.NoError(t, err)

	// Transcode from storage to wire (msgpack -> json).
	var intermediate testPayload
	wireData, err := m.Transcode(storageData, &intermediate)
	require.NoError(t, err)
	require.NotEmpty(t, wireData)

	// Verify the wire output is valid JSON.
	var decoded testPayload
	err = m.DecodeFromWire(wireData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)
}

func TestManager_Transcode_DecodeError(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})

	var intermediate testPayload
	_, err := m.Transcode([]byte{0xff, 0xfe}, &intermediate)
	require.Error(t, err)

	var tcErr *TranscodeError
	require.True(t, errors.As(err, &tcErr))
	assert.Equal(t, TypeMsgPack, tcErr.FromCodec)
	assert.Equal(t, TypeJSON, tcErr.ToCodec)
}

func TestManager_Transcode_EncodeError(t *testing.T) {
	// Use a codec that succeeds on decode but fails on encode for the
	// wire side by using a custom failing codec.
	storage := &MsgPackCodec{}
	wire := &failMarshalCodec{}

	m := NewManager(storage, wire)
	original := samplePayload()

	storageData, err := storage.Marshal(original)
	require.NoError(t, err)

	var intermediate testPayload
	_, err = m.Transcode(storageData, &intermediate)
	require.Error(t, err)

	var tcErr *TranscodeError
	require.True(t, errors.As(err, &tcErr))
	assert.Equal(t, TypeMsgPack, tcErr.FromCodec)
	assert.Equal(t, "fail_marshal", tcErr.ToCodec)
}

func TestManager_StorageEncodeError(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	_, err := m.EncodeForStorage(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
}

func TestManager_WireDecodeError(t *testing.T) {
	m := NewManager(&MsgPackCodec{}, &JSONCodec{})
	var out testPayload
	err := m.DecodeFromWire([]byte("not json"), &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}

// ---------------------------------------------------------------------------
// NewManagerFromConfig
// ---------------------------------------------------------------------------

func TestNewManagerFromConfig_Default(t *testing.T) {
	cfg := DefaultConfig()
	m, err := NewManagerFromConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, m)

	// With UseOptimized=true, the codecs should be wrapped.
	_, ok := m.StorageCodec().(*OptimizedCodec)
	assert.True(t, ok, "expected storage codec to be *OptimizedCodec")

	_, ok = m.WireCodec().(*OptimizedCodec)
	assert.True(t, ok, "expected wire codec to be *OptimizedCodec")
}

func TestNewManagerFromConfig_NotOptimized(t *testing.T) {
	cfg := &Config{
		StorageCodec: TypeJSON,
		WireCodec:    TypeYAML,
		UseOptimized: false,
	}
	m, err := NewManagerFromConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, m)

	_, ok := m.StorageCodec().(*JSONCodec)
	assert.True(t, ok, "expected storage codec to be *JSONCodec")

	_, ok = m.WireCodec().(*YAMLCodec)
	assert.True(t, ok, "expected wire codec to be *YAMLCodec")
}

func TestNewManagerFromConfig_ValidationError(t *testing.T) {
	cfg := &Config{
		StorageCodec: "",
		WireCodec:    TypeJSON,
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)

	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr))
}

func TestNewManagerFromConfig_UnsupportedStorageCodec(t *testing.T) {
	cfg := &Config{
		StorageCodec: "unknown_codec_xyz",
		WireCodec:    TypeJSON,
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)
}

func TestNewManagerFromConfig_UnsupportedWireCodec(t *testing.T) {
	cfg := &Config{
		StorageCodec: TypeMsgPack,
		WireCodec:    "unknown_codec_xyz",
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)
}

func TestNewManagerFromConfig_RoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	m, err := NewManagerFromConfig(cfg)
	require.NoError(t, err)

	original := samplePayload()

	// Storage round-trip.
	data, err := m.EncodeForStorage(original)
	require.NoError(t, err)

	var decoded testPayload
	err = m.DecodeFromStorage(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded)

	// Transcode.
	var intermediate testPayload
	wireData, err := m.Transcode(data, &intermediate)
	require.NoError(t, err)

	var final testPayload
	err = m.DecodeFromWire(wireData, &final)
	require.NoError(t, err)
	assert.Equal(t, original, final)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// failMarshalCodec always fails on Marshal.
type failMarshalCodec struct{}

func (c *failMarshalCodec) Marshal(_ any) ([]byte, error) {
	return nil, &EncodeError{Codec: "fail_marshal", Err: errors.New("intentional failure")}
}

func (c *failMarshalCodec) Unmarshal(data []byte, v any) error {
	return (&JSONCodec{}).Unmarshal(data, v)
}

func (c *failMarshalCodec) Type() string {
	return "fail_marshal"
}
