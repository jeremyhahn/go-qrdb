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

//go:build codec_yaml

package codec

import "gopkg.in/yaml.v3"

// YAMLCodec implements Codec using gopkg.in/yaml.v3.
type YAMLCodec struct{}

// Marshal encodes v as YAML. The underlying yaml.v3 library panics on
// unsupported types, so this method recovers from panics and returns
// them as typed EncodeError values.
func (c *YAMLCodec) Marshal(v any) (data []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch e := r.(type) {
			case error:
				err = &EncodeError{Codec: TypeYAML, Err: e}
			case string:
				err = &EncodeError{Codec: TypeYAML, Err: &yamlPanicError{msg: e}}
			default:
				err = &EncodeError{Codec: TypeYAML, Err: &yamlPanicError{msg: "unknown panic during YAML marshal"}}
			}
		}
	}()
	data, err = yaml.Marshal(v)
	if err != nil {
		return nil, &EncodeError{Codec: TypeYAML, Err: err}
	}
	return data, nil
}

// Unmarshal decodes YAML data into v.
func (c *YAMLCodec) Unmarshal(data []byte, v any) error {
	if v == nil {
		return &NilInputError{Codec: TypeYAML}
	}
	if len(data) == 0 {
		return &EmptyDataError{Codec: TypeYAML}
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return &DecodeError{Codec: TypeYAML, Err: err}
	}
	return nil
}

// Type returns "yaml".
func (c *YAMLCodec) Type() string {
	return TypeYAML
}

// yamlPanicError wraps a panic message from the yaml.v3 library, which
// panics on unsupported types instead of returning an error.
type yamlPanicError struct {
	msg string
}

// Error returns the panic message.
func (e *yamlPanicError) Error() string {
	return e.msg
}

func init() {
	RegisterCodec(TypeYAML, func() Codec { return &YAMLCodec{} })
}
