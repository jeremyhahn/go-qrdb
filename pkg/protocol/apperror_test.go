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

import (
	"bytes"
	"testing"
)

// allKinds is every AppErrorKind, written out: a list derived from the
// marker map under test would assert nothing about a kind missing from it.
var allKinds = []AppErrorKind{
	AppErrorCorrupt, AppErrorCapability, AppErrorInvalid,
	AppErrorConflict, AppErrorUnavailable, AppErrorFault,
}

// Every kind round-trips: the refusal decodes as an application error, keeps
// its kind, and keeps its message with the marker stripped. Until go-qrdb#141
// nothing distinguished a capability refusal from a constraint violation, and
// until go-qrdb#557 nothing distinguished the rest from one another.
func TestAppErrorKind_EveryKindRoundTrips(t *testing.T) {
	const msg = "statemachine: refused"
	for _, kind := range allKinds {
		gotMsg, gotKind, ok := DecodeAppErrorKind(EncodeAppError(kind, msg))
		if !ok {
			t.Fatalf("kind %d did not decode as an application error; checkResultError "+
				"would read the refusal as an ordinary result", kind)
		}
		if gotKind != kind {
			t.Errorf("kind %d decoded as kind %d", kind, gotKind)
		}
		if gotMsg != msg {
			t.Errorf("kind %d: message = %q, want %q — the marker must not leak into the text", kind, gotMsg, msg)
		}
	}
	if len(appErrorMarkers) != len(allKinds) {
		t.Errorf("appErrorMarkers has %d rows for %d kinds", len(appErrorMarkers), len(allKinds))
	}
}

// No marker may be a prefix of another, or one kind would decode as another
// depending on map iteration order.
func TestAppErrorMarkers_NoMarkerPrefixesAnother(t *testing.T) {
	for a, ma := range appErrorMarkers {
		for b, mb := range appErrorMarkers {
			if a != b && bytes.HasPrefix(ma, mb) {
				t.Errorf("marker for kind %d starts with the marker for kind %d", a, b)
			}
		}
	}
}

// EncodeCapabilityError is AppErrorCapability by name.
func TestEncodeCapabilityError_IsTheCapabilityKind(t *testing.T) {
	const msg = "statemachine: unknown opcode: 67"
	if !bytes.Equal(EncodeCapabilityError(msg), EncodeAppError(AppErrorCapability, msg)) {
		t.Error("EncodeCapabilityError and EncodeAppError(AppErrorCapability) disagree")
	}
}

// DecodeAppError keeps returning the bare text, whichever kind wrote it.
func TestDecodeAppError_StripsTheMarker(t *testing.T) {
	const msg = "statemachine: mvcc not configured"
	for _, kind := range allKinds {
		got, ok := DecodeAppError(EncodeAppError(kind, msg))
		if !ok || got != msg {
			t.Errorf("kind %d: DecodeAppError = %q, %v; want %q, true", kind, got, ok, msg)
		}
	}
}

// A payload carrying the prefix and no marker this code writes is still an
// error — reading it as a result would report a refusal as success — and it
// is classified as corrupt, because no writer of this version produced it.
func TestDecodeAppErrorKind_UnmarkedPayloadIsCorrupt(t *testing.T) {
	data := append(append([]byte{}, appErrorPrefix...), "legacy refusal"...)
	msg, kind, ok := DecodeAppErrorKind(data)
	if !ok || kind != AppErrorCorrupt || msg != "legacy refusal" {
		t.Fatalf("DecodeAppErrorKind = %q, %d, %v; want \"legacy refusal\", corrupt, true", msg, kind, ok)
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
