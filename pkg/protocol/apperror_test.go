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

package protocol

import "testing"

// A capability refusal and a constraint violation both travel as encoded
// application errors, and until go-qrdb#141 nothing distinguished them: the
// service layer produced one type for both, so a caller could not tell "this
// node was not built to do that" from "your request was understood and
// refused". These pin the discrimination and, just as importantly, that adding
// it did not disturb the existing encoding.
func TestEncodeCapabilityError_IsDistinguishableFromAGenericAppError(t *testing.T) {
	const msg = "statemachine: unknown opcode: 67"

	capData := EncodeCapabilityError(msg)
	gotMsg, capability, ok := DecodeAppErrorKind(capData)
	if !ok {
		t.Fatal("a capability error did not decode as an application error; checkResultError " +
			"recognises one ONLY by the shared prefix, so an unprefixed payload is read as " +
			"an ordinary result and the refusal disappears")
	}
	if !capability {
		t.Error("capability flag = false for EncodeCapabilityError")
	}
	if gotMsg != msg {
		t.Errorf("message = %q, want %q — the marker must not leak into the text", gotMsg, msg)
	}

	genMsg, capability, ok := DecodeAppErrorKind(EncodeAppError(msg))
	if !ok {
		t.Fatal("a generic application error did not decode")
	}
	if capability {
		t.Error("capability flag = true for EncodeAppError; a unique-index violation would " +
			"be reported to the caller as an absent capability")
	}
	if genMsg != msg {
		t.Errorf("generic message = %q, want %q", genMsg, msg)
	}
}

// DecodeAppError is the pre-existing entry point and has many callers. It must
// keep returning the same text for both kinds, or adding the marker would
// change every message that flows through the capability path.
func TestDecodeAppError_StripsTheCapabilityMarker(t *testing.T) {
	const msg = "statemachine: mvcc not configured"
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"generic", EncodeAppError(msg)},
		{"capability", EncodeCapabilityError(msg)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DecodeAppError(tc.data)
			if !ok {
				t.Fatal("did not decode as an application error")
			}
			if got != msg {
				t.Errorf("message = %q, want %q", got, msg)
			}
		})
	}
}

// The control: ordinary result payloads must still be invisible to both
// decoders. The transaction success flag is a bare 0x00/0x01, and reading one
// as an error would report every successful transaction as a failure.
func TestDecodeAppErrorKind_IgnoresOrdinaryResultPayloads(t *testing.T) {
	for _, data := range [][]byte{nil, {}, {0x00}, {0x01}, []byte("plain result")} {
		if _, _, ok := DecodeAppErrorKind(data); ok {
			t.Errorf("payload %v decoded as an application error", data)
		}
	}
}
