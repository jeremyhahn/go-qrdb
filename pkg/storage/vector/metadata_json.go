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
// stored form keeps one string per key, and the record says how to read
// them: VectorValue.MetadataFormat, replicated on the Raft command as
// VectorCommand.MetadataFormat, never a marker inside the string.
//
//   - MetadataFormatLegacy (0, the zero value of every record and command
//     written before metadata was typed): each value is a plain string,
//     stored and read back verbatim whatever it looks like ("5", "true",
//     "\"q\"" and "\x01true" all stay strings).
//   - MetadataFormatJSON: each value is its canonical JSON encoding (the
//     string "a" is stored as "\"a\"", the number 5 as "5"). Metadata
//     filters encode their comparison value the same way, so equality on
//     the stored strings is equality of the JSON values.
//
// A record never mixes the two: writing JSON metadata into a legacy record
// first upgrades every existing value to the JSON string it reads as
// (UpgradeMetadata), so no migration pass is needed and no legacy value can
// be misread.

// Metadata formats of a stored vector record and of a replicated command.
const (
	// MetadataFormatLegacy stores each metadata value as a verbatim string.
	MetadataFormatLegacy uint8 = 0
	// MetadataFormatJSON stores each metadata value as canonical JSON text.
	MetadataFormatJSON uint8 = 1
)

// EncodeMetadataValue returns the JSON-format stored form of one metadata
// value. A value json cannot encode is stored as the JSON string of its %v
// rendering, so the stored form always decodes.
func EncodeMetadataValue(val interface{}) string {
	b, err := json.Marshal(val)
	if err != nil {
		b, _ = json.Marshal(fmt.Sprintf("%v", val)) //nolint:errchkjson // a string always marshals
	}
	return string(b)
}

// DecodeMetadataValue returns the value of one stored metadata string of a
// record in the given format. A legacy-format value is returned verbatim.
func DecodeMetadataValue(format uint8, stored string) interface{} {
	if format != MetadataFormatJSON {
		return stored
	}
	var out interface{}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return stored
	}
	return out
}

// EncodeMetadata returns the JSON-format stored form of a metadata map (nil
// for nil).
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

// DecodeMetadata returns the values of a stored metadata map of a record in
// the given format (nil for nil).
func DecodeMetadata(format uint8, m map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, stored := range m {
		out[k] = DecodeMetadataValue(format, stored)
	}
	return out
}

// UpgradeMetadata returns the JSON-format form of a stored metadata map of a
// record in the given format: a legacy value becomes the JSON string it
// reads as, a JSON-format map is copied unchanged (nil for nil).
func UpgradeMetadata(format uint8, m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, stored := range m {
		if format == MetadataFormatJSON {
			out[k] = stored
			continue
		}
		out[k] = EncodeMetadataValue(stored)
	}
	return out
}
