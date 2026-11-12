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
// stored form keeps one string per key, and that string is the value's
// canonical JSON encoding (the string "a" is stored as "\"a\"", the number 5
// as "5"). Metadata filters encode their comparison value the same way, so
// equality on the stored strings is equality of the JSON values.

// EncodeMetadataValue returns the stored form of one metadata value: its JSON
// encoding. A value json cannot encode is stored as the JSON string of its
// %v rendering, so the stored form always decodes.
func EncodeMetadataValue(val interface{}) string {
	if b, err := json.Marshal(val); err == nil {
		return string(b)
	}
	b, _ := json.Marshal(fmt.Sprintf("%v", val)) //nolint:errchkjson // a string always marshals
	return string(b)
}

// DecodeMetadataValue returns the JSON value of one stored metadata string.
// A stored string that is not JSON (written before metadata was typed, or by
// a path that stores raw text) is returned as that string.
func DecodeMetadataValue(stored string) interface{} {
	var out interface{}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return stored
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
