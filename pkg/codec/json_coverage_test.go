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

// json_coverage_test.go covers errors.go, manager.go, and optimized.go using
// only the JSON codec, which is always compiled regardless of build tags.
// Tests in errors_test.go, manager_test.go, and optimized_test.go are gated
// behind codec_yaml / codec_msgpack build tags and do not compile under the
// minimal codec_json+raft build.
package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// errors.go — EncodeError
// ---------------------------------------------------------------------------

func TestEncodeError_Error_JSON(t *testing.T) {
	inner := errors.New("marshal failed")
	err := &EncodeError{Codec: TypeJSON, Err: inner}
	assert.Equal(t, "codec: encode failed (json): marshal failed", err.Error())
}

func TestEncodeError_Unwrap_JSON(t *testing.T) {
	inner := errors.New("marshal failed")
	err := &EncodeError{Codec: TypeJSON, Err: inner}
	assert.Equal(t, inner, err.Unwrap())
	require.True(t, errors.Is(err, inner))
}

// ---------------------------------------------------------------------------
// errors.go — DecodeError
// ---------------------------------------------------------------------------

func TestDecodeError_Error_JSON(t *testing.T) {
	inner := errors.New("unmarshal failed")
	err := &DecodeError{Codec: TypeJSON, Err: inner}
	assert.Equal(t, "codec: decode failed (json): unmarshal failed", err.Error())
}

func TestDecodeError_Unwrap_JSON(t *testing.T) {
	inner := errors.New("unmarshal failed")
	err := &DecodeError{Codec: TypeJSON, Err: inner}
	assert.Equal(t, inner, err.Unwrap())
	require.True(t, errors.Is(err, inner))
}

// ---------------------------------------------------------------------------
// errors.go — TranscodeError
// ---------------------------------------------------------------------------

func TestTranscodeError_Error_JSON(t *testing.T) {
	inner := errors.New("conversion failed")
	err := &TranscodeError{FromCodec: TypeJSON, ToCodec: TypeJSON, Err: inner}
	assert.Equal(t, "codec: transcode failed (json -> json): conversion failed", err.Error())
}

func TestTranscodeError_Unwrap_JSON(t *testing.T) {
	inner := errors.New("conversion failed")
	err := &TranscodeError{FromCodec: TypeJSON, ToCodec: TypeJSON, Err: inner}
	assert.Equal(t, inner, err.Unwrap())
	require.True(t, errors.Is(err, inner))
}

// ---------------------------------------------------------------------------
// errors.go — UnsupportedCodecError
// ---------------------------------------------------------------------------

func TestUnsupportedCodecError_Error_JSON(t *testing.T) {
	err := &UnsupportedCodecError{Name: "msgpack"}
	assert.Equal(t, "codec: unsupported codec type: msgpack", err.Error())
}

func TestUnsupportedCodecError_ErrorsAs_JSON(t *testing.T) {
	err := &UnsupportedCodecError{Name: "yaml"}
	var ucErr *UnsupportedCodecError
	require.True(t, errors.As(err, &ucErr))
	assert.Equal(t, "yaml", ucErr.Name)
}

// ---------------------------------------------------------------------------
// errors.go — NilInputError
// ---------------------------------------------------------------------------

func TestNilInputError_Error_JSON(t *testing.T) {
	err := &NilInputError{Codec: TypeJSON}
	assert.Equal(t, "codec: nil target passed to unmarshal (json)", err.Error())
}

func TestNilInputError_ErrorsAs_JSON(t *testing.T) {
	err := &NilInputError{Codec: TypeJSON}
	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
	assert.Equal(t, TypeJSON, nilErr.Codec)
}

// ---------------------------------------------------------------------------
// errors.go — EmptyDataError
// ---------------------------------------------------------------------------

func TestEmptyDataError_Error_JSON(t *testing.T) {
	err := &EmptyDataError{Codec: TypeJSON}
	assert.Equal(t, "codec: empty data passed to unmarshal (json)", err.Error())
}

func TestEmptyDataError_ErrorsAs_JSON(t *testing.T) {
	err := &EmptyDataError{Codec: TypeJSON}
	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
	assert.Equal(t, TypeJSON, emptyErr.Codec)
}

// ---------------------------------------------------------------------------
// errors.go — ConfigValidationError
// ---------------------------------------------------------------------------

func TestConfigValidationError_Error_JSON(t *testing.T) {
	err := &ConfigValidationError{Field: "storage_codec", Message: "must not be empty"}
	assert.Equal(t, "codec: config validation failed: storage_codec: must not be empty", err.Error())
}

func TestConfigValidationError_ErrorsAs_JSON(t *testing.T) {
	err := &ConfigValidationError{Field: "wire_codec", Message: "unsupported"}
	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr))
	assert.Equal(t, "wire_codec", cvErr.Field)
}

// ---------------------------------------------------------------------------
// manager.go — NewManager / accessors
// ---------------------------------------------------------------------------

func TestNewManager_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	require.NotNil(t, m)
	assert.Equal(t, TypeJSON, m.StorageCodec().Type())
	assert.Equal(t, TypeJSON, m.WireCodec().Type())
}

func TestManager_StorageCodec_JSON(t *testing.T) {
	storage := &JSONCodec{}
	m := NewManager(storage, &JSONCodec{})
	assert.Equal(t, storage, m.StorageCodec())
}

func TestManager_WireCodec_JSON(t *testing.T) {
	wire := &JSONCodec{}
	m := NewManager(&JSONCodec{}, wire)
	assert.Equal(t, wire, m.WireCodec())
}

// ---------------------------------------------------------------------------
// manager.go — EncodeForStorage / DecodeFromStorage
// ---------------------------------------------------------------------------

func TestManager_EncodeDecodeStorage_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	original := samplePayload()

	data, err := m.EncodeForStorage(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	require.NoError(t, m.DecodeFromStorage(data, &decoded))
	assert.Equal(t, original, decoded)
}

func TestManager_EncodeForStorage_Error_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	_, err := m.EncodeForStorage(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeJSON, encErr.Codec)
}

func TestManager_DecodeFromStorage_Error_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	var out testPayload
	err := m.DecodeFromStorage([]byte("not json"), &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
	assert.Equal(t, TypeJSON, decErr.Codec)
}

// ---------------------------------------------------------------------------
// manager.go — EncodeForWire / DecodeFromWire
// ---------------------------------------------------------------------------

func TestManager_EncodeDecodeWire_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	original := samplePayload()

	data, err := m.EncodeForWire(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	require.NoError(t, m.DecodeFromWire(data, &decoded))
	assert.Equal(t, original, decoded)
}

func TestManager_EncodeForWire_Error_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	_, err := m.EncodeForWire(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
}

func TestManager_DecodeFromWire_Error_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	var out testPayload
	err := m.DecodeFromWire([]byte("not json"), &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}

// ---------------------------------------------------------------------------
// manager.go — Transcode
// ---------------------------------------------------------------------------

func TestManager_Transcode_JSON(t *testing.T) {
	// JSON-to-JSON transcode: encode as storage JSON, transcode to wire JSON.
	m := NewManager(&JSONCodec{}, &JSONCodec{})
	original := samplePayload()

	storageData, err := m.EncodeForStorage(original)
	require.NoError(t, err)

	var intermediate testPayload
	wireData, err := m.Transcode(storageData, &intermediate)
	require.NoError(t, err)
	require.NotEmpty(t, wireData)

	var decoded testPayload
	require.NoError(t, m.DecodeFromWire(wireData, &decoded))
	assert.Equal(t, original, decoded)
}

func TestManager_Transcode_DecodeError_JSON(t *testing.T) {
	m := NewManager(&JSONCodec{}, &JSONCodec{})

	var intermediate testPayload
	_, err := m.Transcode([]byte("not json"), &intermediate)
	require.Error(t, err)

	var tcErr *TranscodeError
	require.True(t, errors.As(err, &tcErr))
	assert.Equal(t, TypeJSON, tcErr.FromCodec)
	assert.Equal(t, TypeJSON, tcErr.ToCodec)
}

func TestManager_Transcode_EncodeError_JSON(t *testing.T) {
	// Use a storage codec that succeeds on decode but a wire codec that always
	// fails on encode.
	storage := &JSONCodec{}
	wire := &alwaysFailMarshalCodec{}
	m := NewManager(storage, wire)

	original := samplePayload()
	storageData, err := storage.Marshal(original)
	require.NoError(t, err)

	var intermediate testPayload
	_, err = m.Transcode(storageData, &intermediate)
	require.Error(t, err)

	var tcErr *TranscodeError
	require.True(t, errors.As(err, &tcErr))
	assert.Equal(t, TypeJSON, tcErr.FromCodec)
	assert.Equal(t, "fail_always", tcErr.ToCodec)
}

// ---------------------------------------------------------------------------
// manager.go — NewManagerFromConfig (JSON-only paths)
// ---------------------------------------------------------------------------

func TestNewManagerFromConfig_JSONOnly_Default(t *testing.T) {
	cfg := DefaultConfig()
	m, err := NewManagerFromConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, m)

	// UseOptimized=true wraps both codecs in OptimizedCodec.
	_, ok := m.StorageCodec().(*OptimizedCodec)
	assert.True(t, ok, "expected storage codec to be *OptimizedCodec")

	_, ok = m.WireCodec().(*OptimizedCodec)
	assert.True(t, ok, "expected wire codec to be *OptimizedCodec")
}

func TestNewManagerFromConfig_JSONOnly_NotOptimized(t *testing.T) {
	cfg := &Config{
		StorageCodec: TypeJSON,
		WireCodec:    TypeJSON,
		UseOptimized: false,
	}
	m, err := NewManagerFromConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, m)

	_, ok := m.StorageCodec().(*JSONCodec)
	assert.True(t, ok, "expected storage codec to be *JSONCodec")

	_, ok = m.WireCodec().(*JSONCodec)
	assert.True(t, ok, "expected wire codec to be *JSONCodec")
}

func TestNewManagerFromConfig_ValidationError_JSON(t *testing.T) {
	cfg := &Config{
		StorageCodec: "",
		WireCodec:    TypeJSON,
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)

	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr))
	assert.Equal(t, "storage_codec", cvErr.Field)
}

func TestNewManagerFromConfig_UnsupportedStorageCodec_JSON(t *testing.T) {
	cfg := &Config{
		StorageCodec: "unknown_codec_xyz",
		WireCodec:    TypeJSON,
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)

	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr))
	assert.Equal(t, "storage_codec", cvErr.Field)
}

func TestNewManagerFromConfig_UnsupportedWireCodec_JSON(t *testing.T) {
	cfg := &Config{
		StorageCodec: TypeJSON,
		WireCodec:    "unknown_codec_xyz",
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)

	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr))
	assert.Equal(t, "wire_codec", cvErr.Field)
}

func TestNewManagerFromConfig_RoundTrip_JSON(t *testing.T) {
	cfg := DefaultConfig()
	m, err := NewManagerFromConfig(cfg)
	require.NoError(t, err)

	original := samplePayload()

	data, err := m.EncodeForStorage(original)
	require.NoError(t, err)

	var decoded testPayload
	require.NoError(t, m.DecodeFromStorage(data, &decoded))
	assert.Equal(t, original, decoded)

	var intermediate testPayload
	wireData, err := m.Transcode(data, &intermediate)
	require.NoError(t, err)

	var final testPayload
	require.NoError(t, m.DecodeFromWire(wireData, &final))
	assert.Equal(t, original, final)
}

// ---------------------------------------------------------------------------
// optimized.go — NewOptimizedCodec / Type / Inner
// ---------------------------------------------------------------------------

func TestOptimizedCodec_NewAndType_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	require.NotNil(t, oc)
	assert.Equal(t, TypeJSON, oc.Type())
}

func TestOptimizedCodec_Inner_JSON(t *testing.T) {
	inner := &JSONCodec{}
	oc := NewOptimizedCodec(inner)
	assert.Equal(t, inner, oc.Inner())
}

// ---------------------------------------------------------------------------
// optimized.go — Marshal / Unmarshal round-trip
// ---------------------------------------------------------------------------

func TestOptimizedCodec_RoundTrip_JSONOnly(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	original := samplePayload()

	data, err := oc.Marshal(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded testPayload
	require.NoError(t, oc.Unmarshal(data, &decoded))
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_Marshal_Error_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	_, err := oc.Marshal(make(chan int))
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeJSON, encErr.Codec)
}

func TestOptimizedCodec_Unmarshal_Error_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	var out testPayload
	err := oc.Unmarshal([]byte("bad json"), &out)
	require.Error(t, err)

	var decErr *DecodeError
	require.True(t, errors.As(err, &decErr))
}

func TestOptimizedCodec_Unmarshal_NilTarget_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	err := oc.Unmarshal([]byte(`{}`), nil)
	require.Error(t, err)

	var nilErr *NilInputError
	require.True(t, errors.As(err, &nilErr))
}

func TestOptimizedCodec_Unmarshal_EmptyData_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	var out testPayload
	err := oc.Unmarshal([]byte{}, &out)
	require.Error(t, err)

	var emptyErr *EmptyDataError
	require.True(t, errors.As(err, &emptyErr))
}

// ---------------------------------------------------------------------------
// optimized.go — MarshalTo
// ---------------------------------------------------------------------------

func TestOptimizedCodec_MarshalTo_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	original := samplePayload()

	buf := make([]byte, 0, 1024)
	result, err := oc.MarshalTo(original, buf)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	var decoded testPayload
	require.NoError(t, oc.Unmarshal(result, &decoded))
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_MarshalTo_GrowsBuffer_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	original := samplePayload()

	// Start with a tiny buffer that must be grown.
	buf := make([]byte, 0, 1)
	result, err := oc.MarshalTo(original, buf)
	require.NoError(t, err)
	require.NotEmpty(t, result)

	var decoded testPayload
	require.NoError(t, oc.Unmarshal(result, &decoded))
	assert.Equal(t, original, decoded)
}

func TestOptimizedCodec_MarshalTo_Error_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	buf := make([]byte, 0, 64)
	result, err := oc.MarshalTo(make(chan int), buf)
	require.Error(t, err)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// optimized.go — pool reuse (exercises the cap(buf) >= len(data) branch)
// ---------------------------------------------------------------------------

func TestOptimizedCodec_PoolReuse_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})
	original := samplePayload()

	// Marshal multiple times to exercise pool put/get cycles.
	for i := range 50 {
		_ = i
		data, err := oc.Marshal(original)
		require.NoError(t, err)

		var decoded testPayload
		require.NoError(t, oc.Unmarshal(data, &decoded))
		assert.Equal(t, original, decoded)
	}
}

func TestOptimizedCodec_LargePayload_JSON(t *testing.T) {
	oc := NewOptimizedCodec(&JSONCodec{})

	// Payload larger than the default pool buffer (512 bytes) exercises the
	// cap(buf) < len(data) branch in Marshal.
	tags := make([]string, 200)
	for i := range tags {
		tags[i] = "long-tag-name-for-testing-pool-buffer-growth-in-optimized-codec"
	}
	original := testPayload{
		Name:  "large-json-optimized",
		Value: 123456789,
		Tags:  tags,
	}

	data, err := oc.Marshal(original)
	require.NoError(t, err)

	var decoded testPayload
	require.NoError(t, oc.Unmarshal(data, &decoded))
	assert.Equal(t, original.Name, decoded.Name)
	assert.Len(t, decoded.Tags, 200)
}

// ---------------------------------------------------------------------------
// detect.go — probeCodecs (currently only JSON codec is registered under
// codec_json+raft; probeableCodecs only includes protobuf so
// probeCodecs always returns ("", false) in this build.  The test
// verifies the function path still executes without panic and that
// DetectFormat falls through to FormatDetectionError for unknown binary
// data when neither magic bytes nor JSON probing succeeds.
// ---------------------------------------------------------------------------

func TestProbeCodecs_NoProbableCodecsRegistered(t *testing.T) {
	// Under codec_json+raft only the JSON codec is registered, and JSON is NOT
	// in probeableCodecs, so probeCodecs must always return ("", false).
	format, ok := probeCodecs([]byte(`{"x":1}`))
	assert.False(t, ok, "probeCodecs must not match JSON under codec_json build")
	assert.Empty(t, format)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// alwaysFailMarshalCodec is a Codec whose Marshal always returns an error.
// It is used to exercise the Transcode encode-error branch without requiring
// the msgpack build tag.
type alwaysFailMarshalCodec struct{}

func (c *alwaysFailMarshalCodec) Marshal(_ any) ([]byte, error) {
	return nil, &EncodeError{Codec: "fail_always", Err: errors.New("intentional encode failure")}
}

func (c *alwaysFailMarshalCodec) Unmarshal(data []byte, v any) error {
	return (&JSONCodec{}).Unmarshal(data, v)
}

func (c *alwaysFailMarshalCodec) Type() string {
	return "fail_always"
}
