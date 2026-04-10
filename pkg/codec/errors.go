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

// EncodeError is returned when a codec fails to marshal a value.
type EncodeError struct {
	Codec string
	Err   error
}

// Error returns a human-readable description of the encoding failure.
func (e *EncodeError) Error() string {
	return "codec: encode failed (" + e.Codec + "): " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *EncodeError) Unwrap() error {
	return e.Err
}

// DecodeError is returned when a codec fails to unmarshal data.
type DecodeError struct {
	Codec string
	Err   error
}

// Error returns a human-readable description of the decoding failure.
func (e *DecodeError) Error() string {
	return "codec: decode failed (" + e.Codec + "): " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

// TranscodeError is returned when transcoding between storage and wire
// formats fails. It carries both the decode and encode codec names.
type TranscodeError struct {
	FromCodec string
	ToCodec   string
	Err       error
}

// Error returns a human-readable description of the transcode failure.
func (e *TranscodeError) Error() string {
	return "codec: transcode failed (" + e.FromCodec + " -> " + e.ToCodec + "): " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *TranscodeError) Unwrap() error {
	return e.Err
}

// UnsupportedCodecError is returned when a codec type name is not recognized
// by the resolver.
type UnsupportedCodecError struct {
	Name string
}

// Error returns a human-readable description of the unsupported codec.
func (e *UnsupportedCodecError) Error() string {
	return "codec: unsupported codec type: " + e.Name
}

// NilInputError is returned when a nil pointer is passed to Unmarshal.
type NilInputError struct {
	Codec string
}

// Error returns a human-readable description of the nil input error.
func (e *NilInputError) Error() string {
	return "codec: nil target passed to unmarshal (" + e.Codec + ")"
}

// EmptyDataError is returned when empty data is passed to Unmarshal.
type EmptyDataError struct {
	Codec string
}

// Error returns a human-readable description of the empty data error.
func (e *EmptyDataError) Error() string {
	return "codec: empty data passed to unmarshal (" + e.Codec + ")"
}

// ConfigValidationError is returned when codec configuration is invalid.
type ConfigValidationError struct {
	Field   string
	Message string
}

// Error returns a human-readable description of the validation failure.
func (e *ConfigValidationError) Error() string {
	return "codec: config validation failed: " + e.Field + ": " + e.Message
}
