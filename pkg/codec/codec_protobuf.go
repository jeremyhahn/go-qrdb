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

import "google.golang.org/protobuf/proto"

// ProtoBufCodec implements Codec using google.golang.org/protobuf/proto.
// Values passed to Marshal must implement proto.Message. Values passed
// to Unmarshal must be a pointer to a proto.Message implementation.
type ProtoBufCodec struct{}

// Marshal encodes v as Protocol Buffers binary wire format. The value
// must implement proto.Message; otherwise an EncodeError is returned.
func (c *ProtoBufCodec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, &EncodeError{
			Codec: TypeProtoBuf,
			Err:   &protoBufTypeError{value: v},
		}
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, &EncodeError{Codec: TypeProtoBuf, Err: err}
	}
	return data, nil
}

// Unmarshal decodes Protocol Buffers binary data into v. The target
// must implement proto.Message; otherwise a DecodeError is returned.
func (c *ProtoBufCodec) Unmarshal(data []byte, v any) error {
	if v == nil {
		return &NilInputError{Codec: TypeProtoBuf}
	}
	if len(data) == 0 {
		return &EmptyDataError{Codec: TypeProtoBuf}
	}
	msg, ok := v.(proto.Message)
	if !ok {
		return &DecodeError{
			Codec: TypeProtoBuf,
			Err:   &protoBufTypeError{value: v},
		}
	}
	if err := proto.Unmarshal(data, msg); err != nil {
		return &DecodeError{Codec: TypeProtoBuf, Err: err}
	}
	return nil
}

// Type returns "protobuf".
func (c *ProtoBufCodec) Type() string {
	return TypeProtoBuf
}

// protoBufTypeError is an internal error indicating the value does not
// implement proto.Message.
type protoBufTypeError struct {
	value any
}

// Error returns a description of the type mismatch.
func (e *protoBufTypeError) Error() string {
	return "value does not implement proto.Message"
}

func init() {
	RegisterCodec(TypeProtoBuf, func() Codec { return &ProtoBufCodec{} })
}
