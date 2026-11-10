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

package grpc

import (
	"errors"
	"testing"
)

func TestGRPCVector_ErrorTypes(t *testing.T) {
	inner := errors.New("inner")

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"vectorIDParseError", &vectorIDParseError{ID: "abc", Err: inner},
			"grpc transport: failed to parse vector ID abc: inner"},
		{"metadataJSONMarshalError", &metadataJSONMarshalError{Err: inner},
			"grpc transport: failed to marshal metadata JSON: inner"},
		{"metadataJSONUnmarshalError", &metadataJSONUnmarshalError{Err: inner},
			"grpc transport: failed to unmarshal metadata JSON: inner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.Error(); got != c.want {
				t.Fatalf("Error() = %q, want %q", got, c.want)
			}
			if !errors.Is(c.err, inner) {
				t.Fatal("Unwrap does not return the wrapped error")
			}
		})
	}
}

func TestGRPCVector_IDFormatting(t *testing.T) {
	for _, id := range []uint64{0, 42, ^uint64(0)} {
		s := fmtVectorID(id)
		got, err := parseVecID(s)
		if err != nil || got != id {
			t.Fatalf("parseVecID(fmtVectorID(%d)) = %d, %v", id, got, err)
		}
	}
	if got := fmtVectorID(42); got != "42" {
		t.Fatalf("fmtVectorID(42) = %q", got)
	}
	if got, err := parseVecID(""); got != 0 || err != nil {
		t.Fatalf("parseVecID(\"\") = %d, %v", got, err)
	}
	_, err := parseVecID("not-a-number")
	var pe *vectorIDParseError
	if !errors.As(err, &pe) || pe.ID != "not-a-number" {
		t.Fatalf("parseVecID(invalid) error = %T %v", err, err)
	}
}

func TestGRPCVector_MarshalHelpers(t *testing.T) {
	t.Run("StringMapJSON", func(t *testing.T) {
		if s, err := marshalStringMapJSON(nil); err != nil || s != "" {
			t.Fatalf("empty: got %q %v", s, err)
		}
		if s, err := marshalStringMapJSON(map[string]string{"k": "v"}); err != nil || s != `{"k":"v"}` {
			t.Fatalf("non-empty: got %q %v", s, err)
		}
	})
	t.Run("InterfaceMapJSON", func(t *testing.T) {
		if s, err := marshalInterfaceMapJSON(nil); err != nil || s != "" {
			t.Fatalf("empty: got %q %v", s, err)
		}
		if s, err := marshalInterfaceMapJSON(map[string]interface{}{"k": "v"}); err != nil || s != `{"k":"v"}` {
			t.Fatalf("non-empty: got %q %v", s, err)
		}
		_, err := marshalInterfaceMapJSON(map[string]interface{}{"k": make(chan int)})
		var me *metadataJSONMarshalError
		if !errors.As(err, &me) {
			t.Fatalf("unmarshallable value: error = %T %v", err, err)
		}
	})
	t.Run("UnmarshalInterfaceMapJSON", func(t *testing.T) {
		if m, err := unmarshalInterfaceMapJSON(""); err != nil || m != nil {
			t.Fatalf("empty: got %v %v", m, err)
		}
		if m, err := unmarshalInterfaceMapJSON(`{"k":"v"}`); err != nil || m["k"] != "v" {
			t.Fatalf("valid: got %v %v", m, err)
		}
		_, err := unmarshalInterfaceMapJSON("not-json")
		var ue *metadataJSONUnmarshalError
		if !errors.As(err, &ue) {
			t.Fatalf("invalid: error = %T %v", err, err)
		}
	})
}
