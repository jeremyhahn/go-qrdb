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
	"reflect"
	"testing"

	gocodecmsgpack "github.com/jeremyhahn/go-codec/pkg/msgpack"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// TestMetadataJSONRoundTripKeepsTypes proves a number, a bool, an array, an
// object and a string come back from the stored form with their JSON types.
func TestMetadataJSONRoundTripKeepsTypes(t *testing.T) {
	in := map[string]interface{}{
		"n":   float64(42.5),
		"b":   true,
		"arr": []interface{}{"x", float64(1), false},
		"obj": map[string]interface{}{"k": "v"},
		"s":   "hello",
		"num": "5",
		"ctl": "\x01true",
	}
	stored := EncodeMetadata(in)
	if stored["s"] != `"hello"` || stored["n"] != "42.5" || stored["b"] != "true" {
		t.Fatalf("stored form is not plain JSON text: %v", stored)
	}
	out := DecodeMetadata(MetadataFormatJSON, stored)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %#v, want %#v", out, in)
	}
	if _, ok := out["num"].(string); !ok {
		t.Fatalf("string %q decoded as %T, want string", "5", out["num"])
	}
}

// TestMetadataJSONNilAndRawText covers nil maps and a stored value that is not
// JSON, which decodes as its own text.
func TestMetadataJSONNilAndRawText(t *testing.T) {
	if EncodeMetadata(nil) != nil || DecodeMetadata(MetadataFormatJSON, nil) != nil ||
		UpgradeMetadata(MetadataFormatLegacy, nil) != nil {
		t.Fatal("nil map must stay nil")
	}
	if got := DecodeMetadataValue(MetadataFormatJSON, "not json"); got != "not json" {
		t.Fatalf("undecodable JSON-format text decoded as %#v", got)
	}
	if got := EncodeMetadataValue(func() {}); DecodeMetadataValue(MetadataFormatJSON, got) == nil {
		t.Fatalf("unencodable value stored as %q, want a JSON string", got)
	}
}

// TestMetadataJSONFilterEquality proves a filter value encoded the same way
// matches the stored number but not the string of the same digits.
func TestMetadataJSONFilterEquality(t *testing.T) {
	vec := &VectorValue{ID: 1, Metadata: EncodeMetadata(map[string]interface{}{"n": float64(5)})}
	if !matchesFilter(vec, "n", EncodeMetadataValue(float64(5)), FilterEqual) {
		t.Fatal("number filter did not match stored number")
	}
	if matchesFilter(vec, "n", EncodeMetadataValue("5"), FilterEqual) {
		t.Fatal("string filter matched stored number")
	}
}

// TestMetadataJSONLegacyStringsStayStrings proves a value stored verbatim
// before metadata was typed reads back as that string even when its text is
// valid JSON ("5", "true", a quoted string), and that a typed value of the
// same text is a different stored value.
func TestMetadataJSONLegacyStringsStayStrings(t *testing.T) {
	for _, legacy := range []string{"5", "true", `"q"`, "null", "[1]", "plain", "\x01true", "\x01", "\x01\"a\""} {
		got := DecodeMetadataValue(MetadataFormatLegacy, legacy)
		if s, ok := got.(string); !ok || s != legacy {
			t.Fatalf("legacy %q decoded as %#v, want the string itself", legacy, got)
		}
		vec := &VectorValue{ID: 1, Metadata: map[string]string{"k": legacy}}
		if !matchesFilter(vec, "k", legacy, FilterEqual) {
			t.Fatalf("legacy %q does not match its verbatim string filter", legacy)
		}
		// Upgrading a legacy record to the JSON format keeps every value the
		// string it read as, the marker-like control byte included.
		up := UpgradeMetadata(MetadataFormatLegacy, vec.Metadata)
		if s, ok := DecodeMetadataValue(MetadataFormatJSON, up["k"]).(string); !ok || s != legacy {
			t.Fatalf("upgraded legacy %q reads back as %#v", legacy, DecodeMetadataValue(MetadataFormatJSON, up["k"]))
		}
		if UpgradeMetadata(MetadataFormatJSON, up)["k"] != up["k"] {
			t.Fatalf("upgrading a JSON-format map changed %q", up["k"])
		}
	}
}

// TestMetadataFormatSurvivesRecordEncoding: the record's format version is
// part of the stored record, and a record written before it existed decodes
// as the legacy format.
func TestMetadataFormatSurvivesRecordEncoding(t *testing.T) {
	for _, format := range []uint8{MetadataFormatLegacy, MetadataFormatJSON} {
		in := &VectorValue{ID: 7, Embedding: []float32{1}, Metadata: map[string]string{"k": "\x01true"}, Version: 1, MetadataFormat: format}
		data, err := schema.EncodeVectorValue(toSchemaVectorValue(in))
		if err != nil {
			t.Fatal(err)
		}
		dec, err := schema.DecodeVectorValue(data)
		if err != nil {
			t.Fatal(err)
		}
		out := fromSchemaVectorValue(dec)
		if out.MetadataFormat != format || out.Metadata["k"] != "\x01true" {
			t.Fatalf("format %d decoded as %d with %q", format, out.MetadataFormat, out.Metadata["k"])
		}
	}
	// preFormatRecord mirrors a vector record written before meta_fmt existed.
	type preFormatRecord struct {
		ID   uint64            `msgpack:"id"`
		Meta map[string]string `msgpack:"meta"`
		Ver  uint64            `msgpack:"ver"`
	}
	old, err := gocodecmsgpack.New[*preFormatRecord]().Marshal(&preFormatRecord{ID: 7, Meta: map[string]string{"k": "\x01true"}, Ver: 1})
	if err != nil {
		t.Fatal(err)
	}
	dec, err := schema.DecodeVectorValue(old)
	if err != nil {
		t.Fatal(err)
	}
	if dec.MetadataFormat != MetadataFormatLegacy {
		t.Fatalf("pre-format record decoded as format %d", dec.MetadataFormat)
	}
	if got := DecodeMetadata(dec.MetadataFormat, dec.Metadata)["k"]; got != "\x01true" {
		t.Fatalf("pre-format record value read as %#v", got)
	}
}
