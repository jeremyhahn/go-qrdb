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

package codec

import (
	"encoding/binary"
	"encoding/json"
	"strings"
)

// extensionFormats maps file extensions to their format identifiers.
// The map is checked after compound extensions (.tar.gz) are handled first.
var extensionFormats = map[string]string{
	".json":     "json",
	".csv":      "csv",
	".yaml":     "yaml",
	".yml":      "yaml",
	".msgpack":  "msgpack",
	".mp":       "msgpack",
	".bson":     "bson",
	".avro":     "avro",
	".avsc":     "avro",
	".zip":      "zip",
	".tar":      "tar",
	".tgz":      "tar.gz",
	".ndjson":   "ndjson",
	".jsonl":    "ndjson",
	".pb":       "protobuf",
	".proto":    "protobuf",
	".protobuf": "protobuf",
}

// FormatDetectionError is returned when DetectFormat cannot determine the
// format from the filename extension, magic bytes, or codec probing.
type FormatDetectionError struct {
	Filename string
}

// Error returns a human-readable description of the detection failure.
func (e *FormatDetectionError) Error() string {
	return "codec: unable to detect format for " + e.Filename
}

// DetectFormat determines the data format from a filename and/or raw data.
// It first checks the file extension, then falls back to magic byte detection,
// then tries JSON validity and each compiled codec's Unmarshal.
func DetectFormat(filename string, data []byte) (string, error) {
	// Check compound extension before single extensions.
	if strings.HasSuffix(filename, ".tar.gz") {
		return "tar.gz", nil
	}

	// Check single extension via map lookup.
	if filename != "" {
		dot := strings.LastIndex(filename, ".")
		if dot >= 0 {
			ext := strings.ToLower(filename[dot:])
			if format, ok := extensionFormats[ext]; ok {
				return format, nil
			}
		}
	}

	// Fall back to magic byte detection when the extension is unknown.
	if format, ok := detectMagicBytes(data); ok {
		return format, nil
	}

	// Try codec probing: JSON first (most common), then registered codecs.
	if len(data) > 0 && json.Valid(data) {
		return "json", nil
	}

	if format, ok := probeCodecs(data); ok {
		return format, nil
	}

	return "", &FormatDetectionError{Filename: filename}
}

// detectMagicBytes inspects the leading bytes of data to identify well-known
// binary formats. Returns the format name and true when recognized.
func detectMagicBytes(data []byte) (string, bool) {
	if len(data) < 2 {
		return "", false
	}

	// ZIP: PK signature followed by a valid record type marker.
	// Local file header: PK\x03\x04
	// End-of-central-directory: PK\x05\x06
	// Data descriptor: PK\x07\x08
	if len(data) >= 4 && data[0] == 0x50 && data[1] == 0x4B {
		marker := [2]byte{data[2], data[3]}
		switch marker {
		case [2]byte{0x03, 0x04}, [2]byte{0x05, 0x06}, [2]byte{0x07, 0x08}:
			return "zip", true
		}
	}

	// GZIP (tar.gz): \x1f\x8b (2 bytes).
	if data[0] == 0x1F && data[1] == 0x8B {
		return "tar.gz", true
	}

	// TAR: "ustar" magic at byte offset 257.
	if len(data) >= 262 {
		magic := data[257:262]
		if string(magic) == "ustar" {
			return "tar", true
		}
	}

	// BSON: first 4 bytes are a little-endian int32 document length.
	// Valid when the encoded length matches the actual data length.
	if len(data) >= 4 {
		docLen := binary.LittleEndian.Uint32(data[:4])
		if int(docLen) == len(data) && docLen >= 5 {
			return "bson", true
		}
	}

	return "", false
}

// probeableCodecs lists the codec types that are safe to use for blind
// probing. Only codecs with strict format validation are included.
// Excluded formats:
//   - msgpack: no magic bytes; decodes almost any byte sequence
//   - bson: detected via length-prefix magic bytes, not by probing
//   - yaml: too permissive; accepts arbitrary bytes as string scalars
var probeableCodecs = map[string]struct{}{
	TypeProtoBuf: {},
	TypeAvro:     {},
}

// probeCodecs tries each registered, probeable codec's Unmarshal on the data
// and returns the first format that successfully decodes the payload. JSON is
// excluded because it is tried before this function is called. Binary formats
// without reliable magic bytes (msgpack, bson) are also excluded to prevent
// false-positive detection.
func probeCodecs(data []byte) (string, bool) {
	for _, name := range AvailableCodecs() {
		if _, probeable := probeableCodecs[name]; !probeable {
			continue
		}
		c, err := ResolveCodec(name)
		if err != nil {
			continue
		}
		var v interface{}
		if err := c.Unmarshal(data, &v); err == nil {
			return name, true
		}
	}
	return "", false
}
