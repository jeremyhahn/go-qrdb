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

package vector

import (
	"encoding/json"
	"fmt"
)

// Vector metadata is JSON: a client stores numbers, booleans, arrays and
// objects as well as strings, and reads each back with its JSON type. The
// stored form keeps one string per key: the version byte
// MetadataEncodingJSON followed by the value's canonical JSON encoding (the
// string "a" is stored as "\x01\"a\"", the number 5 as "\x015"). Metadata
// filters encode their comparison value the same way, so equality on the
// stored strings is equality of the JSON values.
//
// A stored value without the version byte was written before metadata was
// typed: it is a plain string, stored verbatim, and it reads back as that
// string whatever it looks like ("5", "true" and "\"q\"" stay strings). The
// version byte is what tells the two apart, never whether the text parses
// as JSON, so reading legacy values lazily is exact and no migration pass
// is needed: a legacy value is rewritten in the versioned form the next
// time its vector's metadata is written. The one value the scheme cannot
// carry is a legacy string that itself begins with the byte 0x01, a
// control character no client metadata path ever produced.

// MetadataEncodingJSON is the version byte that starts every metadata value
// stored since metadata became typed JSON.
const MetadataEncodingJSON byte = 0x01

// IsEncodedMetadataValue reports whether a stored metadata value carries the
// versioned JSON encoding; false means a legacy verbatim string.
func IsEncodedMetadataValue(stored string) bool {
	return len(stored) > 0 && stored[0] == MetadataEncodingJSON
}

// EncodeMetadataValue returns the stored form of one metadata value: the
// version byte and its JSON encoding. A value json cannot encode is stored as
// the JSON string of its %v rendering, so the stored form always decodes.
func EncodeMetadataValue(val interface{}) string {
	b, err := json.Marshal(val)
	if err != nil {
		b, _ = json.Marshal(fmt.Sprintf("%v", val)) //nolint:errchkjson // a string always marshals
	}
	return string(MetadataEncodingJSON) + string(b)
}

// DecodeMetadataValue returns the JSON value of one stored metadata string.
// A stored value without the version byte is a legacy string and is
// returned verbatim.
func DecodeMetadataValue(stored string) interface{} {
	if !IsEncodedMetadataValue(stored) {
		return stored
	}
	var out interface{}
	if err := json.Unmarshal([]byte(stored[1:]), &out); err != nil {
		return stored[1:]
	}
	return out
}

// EncodeMetadata returns the stored form of a metadata map (nil for nil).
func EncodeMetadata(m map[string]interface{}) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = EncodeMetadataValue(val)
	}
	return out
}

// DecodeMetadata returns the JSON values of a stored metadata map (nil for
// nil).
func DecodeMetadata(m map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, stored := range m {
		out[k] = DecodeMetadataValue(stored)
	}
	return out
}
