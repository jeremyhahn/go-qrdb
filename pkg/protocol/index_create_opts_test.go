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
	"encoding/binary"
	"testing"
)

// TestIndexCreateOptions_Roundtrip covers go-qrdb#34: every option the command
// can carry must survive Encode -> Decode, including the case-sensitivity flag
// the old positional signature could not express.
func TestIndexCreateOptions_Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		opts IndexCreateOptions
	}{
		{"zero value", IndexCreateOptions{}},
		{"unique", IndexCreateOptions{Unique: true}},
		{"sparse", IndexCreateOptions{Sparse: true}},
		{"numeric", IndexCreateOptions{Numeric: true}},
		{"case-insensitive", IndexCreateOptions{CaseInsensitive: true}},
		{"unique + case-insensitive", IndexCreateOptions{Unique: true, CaseInsensitive: true}},
		{"all", IndexCreateOptions{Unique: true, Sparse: true, Numeric: true, CaseInsensitive: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := EncodeIndexCreateOpts("by_email", "email", tt.opts)
			if err != nil {
				t.Fatalf("EncodeIndexCreateOpts: %v", err)
			}
			cmd, err := DecodeCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCommand: %v", err)
			}

			name, fieldName, got, err := DecodeIndexCreateOpts(cmd.Payload)
			if err != nil {
				t.Fatalf("DecodeIndexCreateOpts: %v", err)
			}
			if name != "by_email" || fieldName != "email" {
				t.Fatalf("name/field = %q/%q; want by_email/email", name, fieldName)
			}
			if got != tt.opts {
				t.Errorf("opts = %+v; want %+v", got, tt.opts)
			}
		})
	}
}

// TestIndexCreateOptions_FlagsAreDistinctBits guards the wire encoding itself.
// Two options sharing a bit would round-trip through the struct above while
// silently turning one property into another on the wire.
func TestIndexCreateOptions_FlagsAreDistinctBits(t *testing.T) {
	seen := map[byte]string{}
	for name, bit := range map[string]byte{
		"unique":           IndexFlagUnique,
		"sparse":           IndexFlagSparse,
		"numeric":          IndexFlagNumeric,
		"case_insensitive": IndexFlagCaseInsensitive,
	} {
		if bit == 0 {
			t.Errorf("%s flag is zero, so it cannot be encoded", name)
		}
		if bit&(bit-1) != 0 {
			t.Errorf("%s flag %08b is not a single bit", name, bit)
		}
		if other, dup := seen[bit]; dup {
			t.Errorf("%s and %s share bit %08b", name, other, bit)
		}
		seen[bit] = name
	}
}

// TestDecodeIndexCreateOpts_LegacyPayloadIsCaseSensitive is the compatibility
// guarantee go-qrdb#34 turns on: a command written before
// IndexFlagCaseInsensitive existed has the bit clear and must decode to a
// case-SENSITIVE index -- what those commands meant. Encoding the flag the
// other way round would reinterpret every replayed Raft log entry.
//
// The payload here is built BY HAND from the documented wire layout rather
// than by calling the encoder under test, so the assertion does not depend on
// the code under test to produce its own fixture.
func TestDecodeIndexCreateOpts_LegacyPayloadIsCaseSensitive(t *testing.T) {
	// [nameLen:2 LE][name][fieldLen:2 LE][field][flags:1]
	const name, field = "by_team", "team"
	payload := make([]byte, 0, 2+len(name)+2+len(field)+1)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(len(name)))
	payload = append(payload, name...)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(len(field)))
	payload = append(payload, field...)
	// A pre-#34 writer could only ever set these three bits.
	payload = append(payload, IndexFlagUnique|IndexFlagSparse|IndexFlagNumeric)

	gotName, gotField, opts, err := DecodeIndexCreateOpts(payload)
	if err != nil {
		t.Fatalf("DecodeIndexCreateOpts: %v", err)
	}
	if gotName != name || gotField != field {
		t.Fatalf("name/field = %q/%q; want %q/%q", gotName, gotField, name, field)
	}
	if opts.CaseInsensitive {
		t.Error("a payload predating IndexFlagCaseInsensitive decoded as case-INSENSITIVE; " +
			"every replayed log entry would silently change meaning")
	}
	if !opts.Unique || !opts.Sparse || !opts.Numeric {
		t.Errorf("legacy flags lost: %+v", opts)
	}
}

// TestEncodeDecodeIndexCreateOpts_RoundTripsCaseInsensitivity confirms the
// case-insensitive bit set by EncodeIndexCreateOpts survives the round trip
// through DecodeIndexCreateOpts.
func TestEncodeDecodeIndexCreateOpts_RoundTripsCaseInsensitivity(t *testing.T) {
	encoded, err := EncodeIndexCreateOpts("by_email", "email", IndexCreateOptions{CaseInsensitive: true})
	if err != nil {
		t.Fatalf("EncodeIndexCreateOpts: %v", err)
	}
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}

	_, _, opts, err := DecodeIndexCreateOpts(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeIndexCreateOpts: %v", err)
	}
	if !opts.CaseInsensitive {
		t.Error("EncodeIndexCreateOpts did not set the case-insensitive bit")
	}
}

// TestEncodeIndexCreateOpts_RoundTripsFlags confirms the unique, sparse and
// numeric flags round-trip through EncodeIndexCreateOpts/DecodeIndexCreateOpts.
func TestEncodeIndexCreateOpts_RoundTripsFlags(t *testing.T) {
	encoded, err := EncodeIndexCreateOpts("by_team", "team", IndexCreateOptions{Unique: true, Sparse: true, Numeric: true})
	if err != nil {
		t.Fatalf("EncodeIndexCreateOpts: %v", err)
	}
	cmd, err := DecodeCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}
	_, _, opts, err := DecodeIndexCreateOpts(cmd.Payload)
	if err != nil {
		t.Fatalf("DecodeIndexCreateOpts: %v", err)
	}
	want := IndexCreateOptions{Unique: true, Sparse: true, Numeric: true}
	if opts != want {
		t.Errorf("opts = %+v; want %+v", opts, want)
	}
}
