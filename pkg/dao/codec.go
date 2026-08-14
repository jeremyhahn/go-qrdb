// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"github.com/jeremyhahn/go-codec"
	"github.com/jeremyhahn/go-codec/pkg/bson"
	"github.com/jeremyhahn/go-codec/pkg/json"
	"github.com/jeremyhahn/go-codec/pkg/msgpack"
	"github.com/jeremyhahn/go-codec/pkg/toml"
	"github.com/jeremyhahn/go-codec/pkg/yaml"
)

// Codec wraps go-codec's Codec interface for DAO operations.
type Codec[E any] interface {
	// Marshal serializes an entity to bytes.
	Marshal(entity E) ([]byte, error)

	// Unmarshal deserializes bytes into an entity pointer.
	Unmarshal(data []byte, entity *E) error

	// Type returns the codec type.
	Type() CodecType
}

// codecWrapper wraps a go-codec Codec to implement our Codec interface.
type codecWrapper[E any] struct {
	codec     codec.Codec[E]
	codecType CodecType
}

func (w *codecWrapper[E]) Marshal(entity E) ([]byte, error) {
	return w.codec.Marshal(entity)
}

func (w *codecWrapper[E]) Unmarshal(data []byte, entity *E) error {
	return w.codec.Unmarshal(data, entity)
}

func (w *codecWrapper[E]) Type() CodecType {
	return w.codecType
}

// SupportedCodecs returns a list of all codecs available in the current build.
// This uses go-codec's dynamic registration system to discover available codecs.
func SupportedCodecs() []CodecType {
	return codec.SupportedCodecs()
}

// IsCodecSupported returns true if the specified codec is available in the current build.
func IsCodecSupported(ct CodecType) bool {
	return codec.IsSupported(ct)
}

// NewCodec creates a codec for the specified type.
// Returns an error if the codec type is not supported in the current build.
//
// Always available codecs:
//   - CodecJSON: Standard JSON encoding (default, human-readable)
//   - CodecYAML: YAML encoding (human-readable, good for config)
//   - CodecTOML: TOML encoding (human-readable, good for config)
//   - CodecMsgPack: MessagePack binary encoding (compact, fast)
//   - CodecBSON: BSON binary encoding (MongoDB compatible)
//
// Build-tag codecs (require -tags flag):
//   - CodecCBOR: CBOR binary encoding (build tag: codec_cbor)
//   - CodecProtoBuf: Protocol Buffers (build tag: codec_protobuf, use NewProtoBufCodec[E]())
//
// Use SupportedCodecs() to discover available codecs at runtime.
func NewCodec[E any](ct CodecType) (Codec[E], error) {
	// Check if codec is supported using go-codec's dynamic discovery
	if !codec.IsSupported(ct) {
		return nil, CodecCreationError{
			CodecType: string(ct),
			Cause:     codec.ErrCodecNotSupported{CodecType: ct},
		}
	}

	var c codec.Codec[E]

	switch ct {
	case CodecJSON:
		c = json.New[E]()
	case CodecYAML:
		c = yaml.New[E]()
	case CodecTOML:
		c = toml.New[E]()
	case CodecMsgPack:
		c = msgpack.New[E]()
	case CodecBSON:
		c = bson.New[E]()
	case CodecCBOR:
		c = newCBORCodec[E]()
	case CodecProtoBuf:
		// ProtoBuf requires special handling - entities must implement proto.Message
		// Use NewProtoBufCodec[E]() directly for protobuf types
		return nil, CodecCreationError{
			CodecType: string(ct),
			Cause:     InvalidConfigError{Field: "codec", Reason: "use NewProtoBufCodec[E]() for protobuf types"},
		}
	default:
		return nil, CodecCreationError{
			CodecType: string(ct),
			Cause:     codec.ErrCodecNotSupported{CodecType: ct},
		}
	}

	return &codecWrapper[E]{
		codec:     c,
		codecType: ct,
	}, nil
}
