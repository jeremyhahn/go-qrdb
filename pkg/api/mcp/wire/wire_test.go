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

package wire

import (
	"encoding/json"
	"testing"
)

// TestCodeNumbering_IsPinned writes every number out BY HAND.
//
// Deriving the expectations from the constants — or from codeNames — would
// produce a test that agrees with any renumbering, which is the one thing this
// test exists to refuse. These numbers are a wire contract: a client built
// against one of them and a server emitting another do not fail loudly, they
// disagree silently about what happened.
func TestCodeNumbering_IsPinned(t *testing.T) {
	for _, tc := range []struct {
		code Code
		want int
		name string
	}{
		{CodeOK, 0, "OK"},
		{CodeUnauthenticated, -32001, "UNAUTHENTICATED"},
		{CodePermissionDenied, -32003, "PERMISSION_DENIED"},
		{CodeNotFound, -32004, "NOT_FOUND"},
		{CodeUnavailable, -32006, "UNAVAILABLE"},
		{CodeDeadlineExceeded, -32007, "DEADLINE_EXCEEDED"},
		{CodeResourceExhausted, -32008, "RESOURCE_EXHAUSTED"},
		{CodeFailedPrecondition, -32009, "FAILED_PRECONDITION"},
		{CodeCanceled, -32011, "CANCELED"},
		{CodeInvalidArgument, -32012, "INVALID_ARGUMENT"},
		{CodeAlreadyExists, -32013, "ALREADY_EXISTS"},
		{CodeUnimplemented, -32014, "UNIMPLEMENTED"},
		{CodeAborted, -32015, "ABORTED"},
		{CodeOutOfRange, -32016, "OUT_OF_RANGE"},
		{CodeDataLoss, -32017, "DATA_LOSS"},
		{CodeUnknown, -32018, "UNKNOWN"},
		{CodeInternal, -32603, "INTERNAL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if int(tc.code) != tc.want {
				t.Errorf("number = %d, want %d. Renumbering a declared code is a "+
					"BREAKING wire change, not a tidy-up", int(tc.code), tc.want)
			}
			if got := tc.code.String(); got != tc.name {
				t.Errorf("String() = %q, want %q", got, tc.name)
			}
		})
	}
}

// TestCodes_CountIsSeventeen pins the size of the vocabulary against the
// reason it has that size: one code per gRPC status code.
func TestCodes_CountIsSeventeen(t *testing.T) {
	if got := len(Codes()); got != 17 {
		t.Fatalf("declared %d codes, want 17 — one per gRPC status code. Adding an "+
			"eighteenth means this transport can express a condition no other one "+
			"can, which is the divergence the vocabulary was chosen to prevent", got)
	}
}

// TestCodes_AreUniqueAndInRange asserts no two codes share a number and every
// one sits where JSON-RPC 2.0 allows an implementation-defined server error.
func TestCodes_AreUniqueAndInRange(t *testing.T) {
	seen := map[Code]bool{}
	for _, c := range Codes() {
		if seen[c] {
			t.Errorf("%d is declared twice", int(c))
		}
		seen[c] = true

		if c == CodeOK {
			continue // never serialized; see its doc comment.
		}
		n := int(c)
		inServerBand := n <= -32000 && n >= -32099
		if !inServerBand && n != -32603 {
			t.Errorf("%s (%d) is outside the -32000..-32099 implementation-defined "+
				"server-error band and is not JSON-RPC's own INTERNAL_ERROR", c, n)
		}
	}
}

// TestCodes_ReservedNumbersAreUnassigned pins the three numbers this
// vocabulary deliberately does not use.
//
// conventions/errors.md §5 records fourteen application codes at
// -32001..-32014. Three of those names — InvalidToken (-32002),
// ResourceConflict (-32005) and OperationNotAllowed (-32010) — have no gRPC
// sibling, and go-qrdb emits nothing that means them. Reusing the numbers for
// something else would make one integer mean two different conditions
// depending on which server answered, which is a worse failure than a gap
// because nothing surfaces it.
func TestCodes_ReservedNumbersAreUnassigned(t *testing.T) {
	for _, reserved := range []int{-32002, -32005, -32010} {
		if Code(reserved).Declared() {
			t.Errorf("%d is declared as %s. It is reserved for a name this "+
				"vocabulary has no sibling for; take the next free number instead",
				reserved, Code(reserved))
		}
	}
}

// TestCode_String_Unrecognized covers the version-skew answer: a code from a
// newer server names itself as unrecognised rather than as something else.
func TestCode_String_Unrecognized(t *testing.T) {
	future := Code(-32099)
	if future.Declared() {
		t.Fatal("-32099 is declared; pick an undeclared number for this test")
	}
	if got := future.String(); got != "UNRECOGNIZED" {
		t.Errorf("String() = %q, want UNRECOGNIZED", got)
	}
}

func TestNewErrorEnvelope_FillsEveryField(t *testing.T) {
	env := NewErrorEnvelope("qrdb_user_get", CodeNotFound, "user not found: 7")
	if env.Error == nil {
		t.Fatal("envelope carries no payload")
	}
	if env.Error.Code != CodeNotFound {
		t.Errorf("Code = %d, want %d", env.Error.Code, CodeNotFound)
	}
	if env.Error.Status != "NOT_FOUND" {
		t.Errorf("Status = %q, want NOT_FOUND", env.Error.Status)
	}
	if env.Error.Message != "user not found: 7" {
		t.Errorf("Message = %q", env.Error.Message)
	}
	if env.Error.Tool != "qrdb_user_get" {
		t.Errorf("Tool = %q, want qrdb_user_get", env.Error.Tool)
	}
}

// TestErrorPayload_CarriesNoSubject is the shape assertion behind a decision.
//
// The payload names the CLASSIFICATION and not the subject: the SDK
// reconstitutes which user or which key was missing from the request IT made,
// exactly as the gRPC and QUIC transports do. A subject field here would give
// the SDK a second source for the same fact, and two sources can disagree.
// This test fails the moment one is added, so adding one is a decision taken
// on purpose rather than a field that slipped in.
func TestErrorPayload_CarriesNoSubject(t *testing.T) {
	raw, err := json.Marshal(NewErrorEnvelope("qrdb_get", CodeNotFound, "key not found"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]bool{"code": true, "status": true, "message": true, "tool": true}
	for k := range decoded.Error {
		if !want[k] {
			t.Errorf("the payload carries a %q field. If the server is now naming the "+
				"subject, read this test's comment before keeping it: the SDK "+
				"reconstitutes the subject from the caller's own request", k)
		}
	}
}

// roundTrip marshals and unmarshals through JSON the way a real call does, so
// the decoder is fed a map rather than the struct the encoder produced.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestDecodeErrorPayload_RoundTrip(t *testing.T) {
	structured := roundTrip(t, NewErrorEnvelope("qrdb_get", CodeUnavailable, "barrier sealed"))

	payload, ok := DecodeErrorPayload(structured)
	if !ok {
		t.Fatal("a well-formed envelope did not decode")
	}
	if payload.Code != CodeUnavailable {
		t.Errorf("Code = %d, want %d", payload.Code, CodeUnavailable)
	}
	if payload.Message != "barrier sealed" {
		t.Errorf("Message = %q", payload.Message)
	}
	if payload.Tool != "qrdb_get" {
		t.Errorf("Tool = %q", payload.Tool)
	}
}

// TestDecodeErrorPayload_UnknownCodePassesThrough is the version-skew case
// from the client's side: a newer server's code is returned AS IT ARRIVED so
// the caller degrades to a generic failure, rather than being rejected (which
// would discard the message too) or coerced (which would misclassify).
func TestDecodeErrorPayload_UnknownCodePassesThrough(t *testing.T) {
	structured := roundTrip(t, &ErrorEnvelope{Error: &ErrorPayload{
		Code:    Code(-32098),
		Status:  "SOMETHING_NEWER",
		Message: "from a later server",
	}})

	payload, ok := DecodeErrorPayload(structured)
	if !ok {
		t.Fatal("an unrecognised code was rejected; it must degrade, not vanish")
	}
	if payload.Code.Declared() {
		t.Error("a code this build does not declare reported itself as declared")
	}
	if payload.Message != "from a later server" {
		t.Errorf("Message = %q", payload.Message)
	}
}

func TestDecodeErrorPayload_Rejections(t *testing.T) {
	for _, tc := range []struct {
		name       string
		structured any
	}{
		{"nil", nil},
		{"not an envelope", roundTrip(t, map[string]any{"rows": 3})},
		{"envelope with no error", roundTrip(t, &ErrorEnvelope{})},
		{"payload with no code", roundTrip(t, map[string]any{
			"error": map[string]any{"message": "something"},
		})},
		{"unmarshalable", make(chan int)},
		{"wrongly typed error member", roundTrip(t, map[string]any{"error": "a string"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := DecodeErrorPayload(tc.structured); ok {
				t.Error("decoded as a classification when it carries none")
			}
		})
	}
}
