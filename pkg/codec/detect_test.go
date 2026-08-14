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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// FormatDetectionError
// ---------------------------------------------------------------------------

func TestFormatDetectionError_Error(t *testing.T) {
	err := &FormatDetectionError{Filename: "data.bin"}
	want := "codec: unable to detect format for data.bin"
	if err.Error() != want {
		t.Fatalf("expected %q, got %q", want, err.Error())
	}
}

func TestFormatDetectionError_EmptyFilename(t *testing.T) {
	err := &FormatDetectionError{Filename: ""}
	want := "codec: unable to detect format for "
	if err.Error() != want {
		t.Fatalf("expected %q, got %q", want, err.Error())
	}
}

// ---------------------------------------------------------------------------
// DetectFormat — extension detection
// ---------------------------------------------------------------------------

func TestDetectFormat_Extensions(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"data.json", "json"},
		{"data.csv", "csv"},
		{"data.yaml", "yaml"},
		{"data.yml", "yaml"},
		{"data.msgpack", "msgpack"},
		{"data.mp", "msgpack"},
		{"data.bson", "bson"},
		{"data.avro", "avro"},
		{"data.avsc", "avro"},
		{"data.zip", "zip"},
		{"data.tar", "tar"},
		{"data.tgz", "tar.gz"},
		{"data.ndjson", "ndjson"},
		{"data.jsonl", "ndjson"},
		{"data.pb", "protobuf"},
		{"data.proto", "protobuf"},
		{"data.protobuf", "protobuf"},
	}
	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			got, err := DetectFormat(tc.filename, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestDetectFormat_ExtensionCaseInsensitive(t *testing.T) {
	got, err := DetectFormat("DATA.JSON", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "json" {
		t.Fatalf("expected json, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// DetectFormat — compound extension .tar.gz
// ---------------------------------------------------------------------------

func TestDetectFormat_TarGz_CompoundExtension(t *testing.T) {
	got, err := DetectFormat("backup.tar.gz", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tar.gz" {
		t.Fatalf("expected tar.gz, got %q", got)
	}
}

func TestDetectFormat_TarGz_TakesPreferenceOverSingleExtension(t *testing.T) {
	// Filename ends with .tar.gz — must NOT resolve to the .gz extension.
	got, err := DetectFormat("archive.tar.gz", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tar.gz" {
		t.Fatalf("expected tar.gz, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// DetectFormat — magic byte detection
// ---------------------------------------------------------------------------

func TestDetectFormat_MagicBytes_ZIP(t *testing.T) {
	// Build a minimal valid ZIP archive in memory.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("zip.NewWriter close: %v", err)
	}

	got, err := DetectFormat("", buf.Bytes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "zip" {
		t.Fatalf("expected zip, got %q", got)
	}
}

func TestDetectFormat_MagicBytes_GZIP(t *testing.T) {
	// Build a minimal gzip stream in memory.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte("hello")); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	got, err := DetectFormat("", buf.Bytes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tar.gz" {
		t.Fatalf("expected tar.gz, got %q", got)
	}
}

func TestDetectFormat_MagicBytes_TAR(t *testing.T) {
	// Build a minimal valid tar archive in memory.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: "test.txt",
		Mode: 0644,
		Size: int64(len("data")),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar write header: %v", err)
	}
	if _, err := tw.Write([]byte("data")); err != nil {
		t.Fatalf("tar write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	got, err := DetectFormat("", buf.Bytes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tar" {
		t.Fatalf("expected tar, got %q", got)
	}
}

func TestDetectFormat_MagicBytes_BSON(t *testing.T) {
	// Minimal BSON document: { } — int32(5) + 0x00 terminator.
	doc := make([]byte, 5)
	binary.LittleEndian.PutUint32(doc[:4], 5)
	doc[4] = 0x00

	got, err := DetectFormat("", doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "bson" {
		t.Fatalf("expected bson, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// DetectFormat — JSON probing (extensionless)
// ---------------------------------------------------------------------------

func TestDetectFormat_JSONProbing_EmptyFilenameWithJSONData(t *testing.T) {
	got, err := DetectFormat("", []byte(`{"key":"value"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "json" {
		t.Fatalf("expected json, got %q", got)
	}
}

func TestDetectFormat_JSONProbing_ArrayData(t *testing.T) {
	got, err := DetectFormat("", []byte(`[{"a":1},{"b":2}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "json" {
		t.Fatalf("expected json, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// DetectFormat — unknown format returns FormatDetectionError
// ---------------------------------------------------------------------------

func TestDetectFormat_UnknownExtension_ReturnsError(t *testing.T) {
	_, err := DetectFormat("data.xyz", []byte{0x00, 0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var fde *FormatDetectionError
	if !errors.As(err, &fde) {
		t.Fatalf("expected *FormatDetectionError, got %T: %v", err, err)
	}
	if fde.Filename != "data.xyz" {
		t.Fatalf("expected filename 'data.xyz', got %q", fde.Filename)
	}
}

func TestDetectFormat_EmptyFilenameUnknownBinary_ReturnsError(t *testing.T) {
	// Random binary that is not a known magic byte signature, not valid JSON,
	// and not decodable by any probed codec.
	data := []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB, 0xFA, 0xF9, 0xF8}

	_, err := DetectFormat("", data)
	if err == nil {
		t.Fatal("expected FormatDetectionError for unrecognised binary, got nil")
	}
	var fde *FormatDetectionError
	if !errors.As(err, &fde) {
		t.Fatalf("expected *FormatDetectionError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// detectMagicBytes — direct unit tests
// ---------------------------------------------------------------------------

func TestDetectMagicBytes_TooShort(t *testing.T) {
	_, ok := detectMagicBytes([]byte{0x50})
	if ok {
		t.Fatal("expected false for 1-byte input")
	}
}

func TestDetectMagicBytes_Empty(t *testing.T) {
	_, ok := detectMagicBytes(nil)
	if ok {
		t.Fatal("expected false for nil input")
	}
}

func TestDetectMagicBytes_BSONTooShortForDocLen(t *testing.T) {
	// 3 bytes — not enough for BSON length field.
	_, ok := detectMagicBytes([]byte{0x03, 0x00, 0x00})
	if ok {
		t.Fatal("expected false for 3-byte input")
	}
}

func TestDetectMagicBytes_BSONLengthMismatch(t *testing.T) {
	// Claim length=10 but actual data is 5 bytes — must not detect as BSON.
	doc := make([]byte, 5)
	binary.LittleEndian.PutUint32(doc[:4], 10)
	doc[4] = 0x00

	format, ok := detectMagicBytes(doc)
	if ok && format == "bson" {
		t.Fatal("length mismatch: must not detect as BSON")
	}
}
