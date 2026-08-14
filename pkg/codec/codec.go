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

// Package codec provides a dual-codec system for storage and wire encoding.
// Storage codecs (default: msgpack) are optimized for compact, fast
// serialization. Wire codecs (default: json) are optimized for
// human-readable interchange. State machines remain []byte-only; the
// codec layer lives in the service tier.
//
// Codec implementations are compiled conditionally via build tags:
//
//	codec_msgpack  — MessagePack (github.com/vmihailenco/msgpack/v5)
//	codec_yaml     — YAML (gopkg.in/yaml.v3)
//	codec_bson     — BSON (go.mongodb.org/mongo-driver/bson)
//	codec_protobuf — Protocol Buffers (google.golang.org/protobuf/proto)
//
// JSON is always compiled in. All other codecs are opt-in via build tags.
// The Makefile enables all non-JSON codecs by default; set WITH_CODEC_X=0
// to disable a specific codec at build time.
package codec

import "encoding/json"

// Codec defines the interface for encoding/decoding values.
type Codec interface {
	// Marshal encodes v into a byte slice.
	Marshal(v any) ([]byte, error)

	// Unmarshal decodes data into the value pointed to by v.
	Unmarshal(data []byte, v any) error

	// Type returns the codec type identifier (e.g., "json", "msgpack").
	Type() string
}

// Type constants identify the built-in codec implementations.
// These constants are always declared regardless of build tags so
// that configuration code can reference them at compile time.
const (
	TypeJSON     = "json"
	TypeMsgPack  = "msgpack"
	TypeYAML     = "yaml"
	TypeProtoBuf = "protobuf"
	TypeBSON     = "bson"
)

// JSONCodec implements Codec using encoding/json.
type JSONCodec struct{}

// Marshal encodes v as JSON.
func (c *JSONCodec) Marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &EncodeError{Codec: TypeJSON, Err: err}
	}
	return data, nil
}

// Unmarshal decodes JSON data into v.
func (c *JSONCodec) Unmarshal(data []byte, v any) error {
	if v == nil {
		return &NilInputError{Codec: TypeJSON}
	}
	if len(data) == 0 {
		return &EmptyDataError{Codec: TypeJSON}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return &DecodeError{Codec: TypeJSON, Err: err}
	}
	return nil
}

// Type returns "json".
func (c *JSONCodec) Type() string {
	return TypeJSON
}

func init() {
	RegisterCodec(TypeJSON, func() Codec { return &JSONCodec{} })
}
