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
	}
	stored := EncodeMetadata(in)
	if stored["s"] != "\x01\"hello\"" || stored["n"] != "\x0142.5" || stored["b"] != "\x01true" {
		t.Fatalf("stored form is not JSON: %v", stored)
	}
	out := DecodeMetadata(stored)
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
	if EncodeMetadata(nil) != nil || DecodeMetadata(nil) != nil {
		t.Fatal("nil map must stay nil")
	}
	if got := DecodeMetadataValue("plain text"); got != "plain text" {
		t.Fatalf("raw text decoded as %#v", got)
	}
	if got := EncodeMetadataValue(func() {}); DecodeMetadataValue(got) == nil {
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
	for _, legacy := range []string{"5", "true", `"q"`, "null", "[1]", "plain"} {
		got := DecodeMetadataValue(legacy)
		if s, ok := got.(string); !ok || s != legacy {
			t.Fatalf("legacy %q decoded as %#v, want the string itself", legacy, got)
		}
		if IsEncodedMetadataValue(legacy) {
			t.Fatalf("legacy %q reported as versioned", legacy)
		}
		vec := &VectorValue{ID: 1, Metadata: map[string]string{"k": legacy}}
		if !matchesFilter(vec, "k", legacy, FilterEqual) {
			t.Fatalf("legacy %q does not match its verbatim string filter", legacy)
		}
		if matchesFilter(vec, "k", EncodeMetadataValue(DecodeMetadataValue("\x01"+legacy)), FilterEqual) {
			t.Fatalf("legacy %q matched the typed filter of the same text", legacy)
		}
	}
}
