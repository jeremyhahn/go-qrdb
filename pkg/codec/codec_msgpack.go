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

import "github.com/vmihailenco/msgpack/v5"

// MsgPackCodec implements Codec using github.com/vmihailenco/msgpack/v5.
type MsgPackCodec struct{}

// Marshal encodes v as MessagePack.
func (c *MsgPackCodec) Marshal(v any) ([]byte, error) {
	data, err := msgpack.Marshal(v)
	if err != nil {
		return nil, &EncodeError{Codec: TypeMsgPack, Err: err}
	}
	return data, nil
}

// Unmarshal decodes MessagePack data into v.
func (c *MsgPackCodec) Unmarshal(data []byte, v any) error {
	if v == nil {
		return &NilInputError{Codec: TypeMsgPack}
	}
	if len(data) == 0 {
		return &EmptyDataError{Codec: TypeMsgPack}
	}
	if err := msgpack.Unmarshal(data, v); err != nil {
		return &DecodeError{Codec: TypeMsgPack, Err: err}
	}
	return nil
}

// Type returns "msgpack".
func (c *MsgPackCodec) Type() string {
	return TypeMsgPack
}

func init() {
	RegisterCodec(TypeMsgPack, func() Codec { return &MsgPackCodec{} })
}
