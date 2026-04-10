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

//go:build codec_avro

package codec

import (
	"encoding/json"

	"github.com/hamba/avro/v2"
)

// avroSchema is the Avro schema used for generic document encoding.
// Values are JSON-marshaled and stored as Avro bytes, giving full type
// flexibility while producing valid Avro binary wire format. The Avro
// framing (varint length prefix) ensures compatibility with any Avro reader
// that understands a bytes schema.
var avroSchema = avro.MustParse(`"bytes"`)

// AvroCodec implements Codec using the Apache Avro binary format via
// github.com/hamba/avro/v2. Because Avro requires a fixed schema per
// value type, this codec encodes any value by first JSON-marshaling it
// into a byte slice, then wrapping that slice in Avro bytes framing.
// This approach is self-describing and works for arbitrary Go types.
type AvroCodec struct{}

// Marshal encodes v as Avro bytes. The value is first JSON-encoded, then
// the resulting bytes are stored in Avro binary format using a bytes schema.
func (c *AvroCodec) Marshal(v any) ([]byte, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, &EncodeError{Codec: TypeAvro, Err: err}
	}
	data, err := avro.Marshal(avroSchema, jsonBytes)
	if err != nil {
		return nil, &EncodeError{Codec: TypeAvro, Err: err}
	}
	return data, nil
}

// Unmarshal decodes Avro bytes data into v. The Avro bytes payload is first
// extracted, then JSON-decoded into the target value.
func (c *AvroCodec) Unmarshal(data []byte, v any) error {
	if v == nil {
		return &NilInputError{Codec: TypeAvro}
	}
	if len(data) == 0 {
		return &EmptyDataError{Codec: TypeAvro}
	}
	var jsonBytes []byte
	if err := avro.Unmarshal(avroSchema, data, &jsonBytes); err != nil {
		return &DecodeError{Codec: TypeAvro, Err: err}
	}
	if err := json.Unmarshal(jsonBytes, v); err != nil {
		return &DecodeError{Codec: TypeAvro, Err: err}
	}
	return nil
}

// Type returns "avro".
func (c *AvroCodec) Type() string {
	return TypeAvro
}

func init() {
	RegisterCodec(TypeAvro, func() Codec { return &AvroCodec{} })
}
