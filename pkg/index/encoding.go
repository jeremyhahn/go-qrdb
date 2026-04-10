// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package index

// Order-preserving encoding for compound index keys.
//
// Compound indexes concatenate multiple field values into a single byte
// sequence that preserves lexicographic ordering across all fields. This
// enables efficient prefix scans: a query on (A=?) can use a compound
// index (A, B) by scanning the prefix formed by encoding A alone.
//
// Encoding scheme per field type:
//   - String: escaped with byte-stuffing so that \x00 never appears in
//     field data. Each \x00 byte in the source is replaced with \x00\xFF,
//     and the field is terminated with \x00\x00. This preserves
//     lexicographic order and allows unambiguous field boundary detection.
//   - Numeric: 8-byte big-endian with sign-bit flip (existing
//     EncodeNumericValue). Fixed width, no terminator needed.
//   - Boolean: single byte \x01 (true) or \x00\x00 (false, using the
//     string terminator convention for consistency).

// EncodeCompoundValue encodes multiple field values into a single
// order-preserving byte sequence suitable for use as a compound index key.
// Each value is encoded according to its corresponding type in types.
// The types slice must have the same length as values.
func EncodeCompoundValue(values [][]byte, types []IndexValueType) []byte {
	// Pre-estimate capacity: each string field adds at most 2x+2 bytes,
	// each numeric field adds exactly 8 bytes.
	capacity := 0
	for i, v := range values {
		if i < len(types) && types[i] == IndexValueTypeNumeric {
			capacity += 8
		} else {
			capacity += len(v)*2 + 2 // worst case: every byte is \x00
		}
	}

	buf := make([]byte, 0, capacity)

	for i, v := range values {
		vtype := IndexValueTypeString
		if i < len(types) {
			vtype = types[i]
		}

		switch vtype {
		case IndexValueTypeNumeric:
			// Numeric values are already encoded as fixed 8-byte
			// big-endian by EncodeNumericValue. Pass through directly.
			buf = append(buf, v...)
		default:
			// String encoding: escape \x00 as \x00\xFF, terminate with \x00\x00.
			buf = encodeStringField(buf, v)
		}
	}

	return buf
}

// DecodeCompoundValue splits a compound-encoded byte sequence back into
// individual field values. The types slice specifies the encoding used
// for each field and must match what was passed to EncodeCompoundValue.
func DecodeCompoundValue(data []byte, types []IndexValueType) [][]byte {
	var result [][]byte
	offset := 0

	for i := 0; offset < len(data) && i < len(types); i++ {
		switch types[i] {
		case IndexValueTypeNumeric:
			// Fixed 8-byte field.
			if offset+8 > len(data) {
				return result
			}
			field := make([]byte, 8)
			copy(field, data[offset:offset+8])
			result = append(result, field)
			offset += 8
		default:
			// String field: scan for \x00\x00 terminator.
			field, newOffset := decodeStringField(data, offset)
			result = append(result, field)
			offset = newOffset
		}
	}

	return result
}

// EncodeCompoundPrefix encodes a prefix of compound field values.
// This is used for prefix-scan queries where only the first N fields
// of a compound index are specified.
func EncodeCompoundPrefix(values [][]byte, types []IndexValueType) []byte {
	return EncodeCompoundValue(values, types)
}

// encodeStringField appends a byte-stuffed, null-terminated encoding of
// src to dst and returns the extended slice.
func encodeStringField(dst, src []byte) []byte {
	for _, b := range src {
		if b == 0x00 {
			dst = append(dst, 0x00, 0xFF)
		} else {
			dst = append(dst, b)
		}
	}
	// Terminator: \x00\x00
	dst = append(dst, 0x00, 0x00)
	return dst
}

// decodeStringField reads a byte-stuffed string field starting at offset
// in data. Returns the decoded field and the offset past the terminator.
// An empty string field encodes as \x00\x00 and decodes to []byte{}, not nil.
func decodeStringField(data []byte, offset int) ([]byte, int) {
	field := []byte{}
	i := offset
	for i < len(data) {
		if data[i] == 0x00 {
			if i+1 >= len(data) {
				// Truncated; treat as terminator.
				i++
				break
			}
			if data[i+1] == 0x00 {
				// Terminator found.
				i += 2
				break
			}
			if data[i+1] == 0xFF {
				// Escaped \x00.
				field = append(field, 0x00)
				i += 2
				continue
			}
			// Unexpected byte after \x00; treat as terminator.
			i += 2
			break
		}
		field = append(field, data[i])
		i++
	}
	return field, i
}
