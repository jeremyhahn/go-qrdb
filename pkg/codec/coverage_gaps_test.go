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

//go:build codec_yaml && codec_protobuf

package codec

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// YAMLCodec.Marshal - panic recovery branches
// ---------------------------------------------------------------------------

// yamlPanicErrorProducer is a type that makes yaml.Marshal panic with a
// string value (not an error), exercising the string-panic recovery branch.
type yamlPanicErrorProducer struct{}

func (y *yamlPanicErrorProducer) MarshalYAML() (interface{}, error) {
	panic("yaml marshal panicked with a string")
}

// yamlDefaultPanicProducer panics with a non-string, non-error value,
// exercising the default panic recovery branch.
type yamlDefaultPanicProducer struct{}

func (y *yamlDefaultPanicProducer) MarshalYAML() (interface{}, error) {
	panic(42) // int panic, not error or string
}

// TestYAMLCodec_Marshal_PanicStringRecovery verifies that a Marshal panic
// producing a string value is converted to a typed EncodeError.
func TestYAMLCodec_Marshal_PanicStringRecovery(t *testing.T) {
	c := &YAMLCodec{}
	_, err := c.Marshal(&yamlPanicErrorProducer{})
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeYAML, encErr.Codec)
}

// TestYAMLCodec_Marshal_PanicDefaultRecovery verifies that a Marshal panic
// producing a non-string, non-error value is converted to a typed EncodeError.
func TestYAMLCodec_Marshal_PanicDefaultRecovery(t *testing.T) {
	c := &YAMLCodec{}
	_, err := c.Marshal(&yamlDefaultPanicProducer{})
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr))
	assert.Equal(t, TypeYAML, encErr.Codec)
}

// ---------------------------------------------------------------------------
// YAMLCodec.Marshal - yaml.Marshal error path (non-panic)
// ---------------------------------------------------------------------------

// yamlMarshalErrorProducer returns an error (not panic) from its MarshalYAML
// method, exercising the `if err != nil` branch in YAMLCodec.Marshal.
type yamlMarshalErrorProducer struct{}

func (y *yamlMarshalErrorProducer) MarshalYAML() (interface{}, error) {
	return nil, &EncodeError{Codec: TypeYAML, Err: errors.New("yaml marshal error")}
}

// yamlErrorPanicker panics with an error value (not a string), exercising
// the `case error` branch of the YAML panic recovery handler.
type yamlErrorPanicker struct{}

func (y *yamlErrorPanicker) MarshalYAML() (interface{}, error) {
	panic(errors.New("yaml panicked with an error"))
}

// TestYAMLCodec_Marshal_ErrorReturn verifies that a MarshalYAML error (not
// panic) is wrapped in a typed EncodeError.
func TestYAMLCodec_Marshal_ErrorReturn(t *testing.T) {
	c := &YAMLCodec{}
	_, err := c.Marshal(&yamlMarshalErrorProducer{})
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr), "expected EncodeError, got %T", err)
	assert.Equal(t, TypeYAML, encErr.Codec)
}

// TestYAMLCodec_Marshal_PanicErrorRecovery verifies that a panic with an
// error value (not a string) is recovered and converted to a typed EncodeError.
func TestYAMLCodec_Marshal_PanicErrorRecovery(t *testing.T) {
	c := &YAMLCodec{}
	_, err := c.Marshal(&yamlErrorPanicker{})
	require.Error(t, err)

	var encErr *EncodeError
	require.True(t, errors.As(err, &encErr), "expected EncodeError, got %T", err)
	assert.Equal(t, TypeYAML, encErr.Codec)
}

// ---------------------------------------------------------------------------
// NewManagerFromConfig - wire codec resolve error
// ---------------------------------------------------------------------------

// TestNewManagerFromConfig_InvalidWireCodecResolve exercises the branch where
// the storage codec resolves correctly but the wire codec fails to resolve.
// Since Config.Validate() also checks the codec names, the error is a
// ConfigValidationError returned from Validate, not UnsupportedCodecError.
func TestNewManagerFromConfig_InvalidWireCodecResolve(t *testing.T) {
	cfg := &Config{
		StorageCodec: TypeMsgPack,
		WireCodec:    "unknown_codec_xyz", // not registered
	}
	_, err := NewManagerFromConfig(cfg)
	require.Error(t, err)

	var cvErr *ConfigValidationError
	require.True(t, errors.As(err, &cvErr), "expected ConfigValidationError, got %T", err)
	assert.Equal(t, "wire_codec", cvErr.Field)
}
