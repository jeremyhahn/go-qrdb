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

//go:build codec_bson

package codec

import "go.mongodb.org/mongo-driver/bson"

// BSONCodec implements Codec using the BSON binary serialization format
// from go.mongodb.org/mongo-driver/bson. BSON is a binary JSON superset
// optimized for storage and network transfer in document-oriented systems.
type BSONCodec struct{}

// Marshal encodes v as BSON.
func (c *BSONCodec) Marshal(v any) ([]byte, error) {
	data, err := bson.Marshal(v)
	if err != nil {
		return nil, &EncodeError{Codec: TypeBSON, Err: err}
	}
	return data, nil
}

// Unmarshal decodes BSON data into v.
func (c *BSONCodec) Unmarshal(data []byte, v any) error {
	if v == nil {
		return &NilInputError{Codec: TypeBSON}
	}
	if len(data) == 0 {
		return &EmptyDataError{Codec: TypeBSON}
	}
	if err := bson.Unmarshal(data, v); err != nil {
		return &DecodeError{Codec: TypeBSON, Err: err}
	}
	return nil
}

// Type returns "bson".
func (c *BSONCodec) Type() string {
	return TypeBSON
}

func init() {
	RegisterCodec(TypeBSON, func() Codec { return &BSONCodec{} })
}
