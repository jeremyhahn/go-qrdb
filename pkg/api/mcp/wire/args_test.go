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

package wire

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// argsRequest mirrors a holder request: scalars, bytes, and structured
// fields carried as JSON strings.
type argsRequest struct {
	Username         string            `json:"username"`
	Threshold        int               `json:"threshold"`
	Share            []byte            `json:"share"`
	ResponseMetadata map[string]string `json:"response_metadata,omitempty"`
	Factors          []struct {
		Name   string         `json:"name"`
		Params map[string]any `json:"params,omitempty"`
	} `json:"factors,omitempty"`
}

// TestArgs_RoundTrip pins that DecodeArgs inverts EncodeArgs, and that the
// structured fields travel as JSON strings.
func TestArgs_RoundTrip(t *testing.T) {
	in := argsRequest{Username: "carol", Threshold: 3, Share: []byte{0, 1, 2, 255}, ResponseMetadata: map[string]string{"k": "v"}}
	in.Factors = append(in.Factors, struct {
		Name   string         `json:"name"`
		Params map[string]any `json:"params,omitempty"`
	}{Name: "fido2", Params: map[string]any{"public_key": "abc"}})

	args, err := EncodeArgs(in, HolderJSONFields...)
	if err != nil {
		t.Fatalf("EncodeArgs: %v", err)
	}
	if _, ok := args["factors"].(string); !ok {
		t.Errorf("factors must travel as a JSON string, got %T", args["factors"])
	}
	if _, ok := args["response_metadata"].(string); !ok {
		t.Errorf("response_metadata must travel as a JSON string, got %T", args["response_metadata"])
	}
	var out argsRequest
	if err := DecodeArgs(args, &out, HolderJSONFields...); err != nil {
		t.Fatalf("DecodeArgs: %v", err)
	}
	if !bytes.Equal(out.Share, in.Share) || out.Username != in.Username || out.Threshold != in.Threshold ||
		!reflect.DeepEqual(out.ResponseMetadata, in.ResponseMetadata) || len(out.Factors) != 1 || out.Factors[0].Name != "fido2" {
		t.Errorf("round trip mismatch: %+v", out)
	}
}

// TestArgs_Refusals covers the error paths.
func TestArgs_Refusals(t *testing.T) {
	var ae *ArgsError
	if _, err := EncodeArgs(func() {}); !errors.As(err, &ae) {
		t.Errorf("an unencodable request must be refused: %v", err)
	}
	if _, err := EncodeArgs("scalar"); !errors.As(err, &ae) {
		t.Errorf("a non-object request must be refused: %v", err)
	}
	var out argsRequest
	if err := DecodeArgs(map[string]any{"factors": "{not json"}, &out, "factors"); !errors.As(err, &ae) || ae.Field != "factors" {
		t.Errorf("malformed JSON field: %v", err)
	}
	if err := DecodeArgs(map[string]any{"threshold": "three"}, &out); !errors.As(err, &ae) {
		t.Errorf("a mistyped field must be refused: %v", err)
	}
	if err := DecodeArgs(map[string]any{"bad": func() {}}, &out); !errors.As(err, &ae) {
		t.Errorf("an unencodable argument must be refused: %v", err)
	}
	if err := DecodeArgs(map[string]any{"factors": ""}, &out, "factors"); err != nil {
		t.Errorf("an empty structured field is absent: %v", err)
	}
	if (&ArgsError{Err: errors.New("x")}).Error() == "" || (&ArgsError{Field: "f", Err: errors.New("x")}).Unwrap() == nil {
		t.Error("ArgsError must describe and unwrap")
	}
}

// TestArgs_ErrorNamesTheField pins that a field-level error names the field.
func TestArgs_ErrorNamesTheField(t *testing.T) {
	msg := (&ArgsError{Field: "factors", Err: errors.New("bad")}).Error()
	if msg != "mcp: tool argument factors: bad" {
		t.Errorf("Error() = %q", msg)
	}
}

// TestArgs_StructuredFieldAlreadyDecoded proves a structured field that
// arrives as a JSON value rather than a string is taken as it is.
func TestArgs_StructuredFieldAlreadyDecoded(t *testing.T) {
	var out argsRequest
	args := map[string]any{"response_metadata": map[string]any{"k": "v"}}
	if err := DecodeArgs(args, &out, HolderJSONFields...); err != nil {
		t.Fatalf("DecodeArgs: %v", err)
	}
	if out.ResponseMetadata["k"] != "v" {
		t.Errorf("response_metadata = %v", out.ResponseMetadata)
	}
}

// TestArgs_CeremonyFields pins that the ceremony's holders and SANs travel as
// JSON strings and round-trip.
func TestArgs_CeremonyFields(t *testing.T) {
	type initRequest struct {
		Threshold int `json:"threshold"`
		Officers  []struct {
			Username string `json:"username"`
		} `json:"officers,omitempty"`
		SANs []string `json:"sans,omitempty"`
	}
	in := initRequest{Threshold: 2, SANs: []string{"db.example.com"}}
	in.Officers = append(in.Officers, struct {
		Username string `json:"username"`
	}{Username: "alice"})
	args, err := EncodeArgs(in, CeremonyJSONFields...)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range CeremonyJSONFields {
		if _, ok := args[f].(string); !ok {
			t.Errorf("%s must travel as a JSON string, got %T", f, args[f])
		}
	}
	var out initRequest
	if err := DecodeArgs(args, &out, CeremonyJSONFields...); err != nil {
		t.Fatal(err)
	}
	if out.Threshold != 2 || len(out.Officers) != 1 || out.Officers[0].Username != "alice" || len(out.SANs) != 1 {
		t.Errorf("round trip = %+v", out)
	}
}
