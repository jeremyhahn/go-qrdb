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
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// OpIndexCreate owner extension (go-qrdb#426)
// ---------------------------------------------------------------------------

func TestIndexCreateOpts_OwnerRoundTrip(t *testing.T) {
	want := IndexCreateOptions{Unique: true, KeyPrefix: "__col:db:c/", Owner: "collection:db/c#ab12"}
	cmd, err := EncodeIndexCreateOpts("db.c.f", "f", want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	name, field, got, err := DecodeIndexCreateOpts(cmd[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if name != "db.c.f" || field != "f" || got != want {
		t.Fatalf("round trip: got (%q, %q, %+v); want (%q, %q, %+v)", name, field, got, "db.c.f", "f", want)
	}
}

// TestIndexCreateOpts_PrefixOnlyShapeDecodesToExplicitOwner pins log replay:
// a command written after the prefix extension but before the owner one ends
// at the prefix, and means an index owned by the explicit API.
func TestIndexCreateOpts_PrefixOnlyShapeDecodesToExplicitOwner(t *testing.T) {
	cmd, err := EncodeIndexCreateOpts("n", "f", IndexCreateOptions{KeyPrefix: "p/"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Strip the two-byte empty owner length the current encoder appends.
	legacy := cmd[1 : len(cmd)-2]
	_, _, opts, err := DecodeIndexCreateOpts(legacy)
	if err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if opts.Owner != "" || opts.KeyPrefix != "p/" {
		t.Fatalf("legacy decode: %+v", opts)
	}
}

func TestIndexCreateOpts_OwnerTruncation(t *testing.T) {
	cmd, err := EncodeIndexCreateOpts("n", "f", IndexCreateOptions{Owner: "owner"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload := cmd[1:]
	ownerStart := len(payload) - len("owner") - 2
	for name, bad := range map[string][]byte{
		"truncated length": payload[:ownerStart+1],
		"short owner":      payload[:len(payload)-1],
	} {
		var ice *InvalidCommandError
		if _, _, _, err := DecodeIndexCreateOpts(bad); !errors.As(err, &ice) {
			t.Errorf("%s: expected InvalidCommandError, got %v", name, err)
		}
	}
}

func TestIndexCreateOpts_OwnerTooLong(t *testing.T) {
	if _, err := EncodeIndexCreateOpts("n", "f", IndexCreateOptions{Owner: string(make([]byte, 70000))}); err == nil {
		t.Fatal("an owner longer than a uint16 length must be refused")
	}
}

// ---------------------------------------------------------------------------
// OpIndexDrop options
// ---------------------------------------------------------------------------

func TestIndexDropOpts_ZeroIsTheUnconditionalShape(t *testing.T) {
	plain, err := EncodeIndexDrop("n")
	if err != nil {
		t.Fatal(err)
	}
	zero, err := EncodeIndexDropOpts("n", IndexDropOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, zero) {
		t.Fatal("zero drop options must encode the existing unconditional shape")
	}
	name, opts, err := DecodeIndexDropOpts(plain[1:])
	if err != nil || name != "n" || opts != (IndexDropOptions{}) {
		t.Fatalf("decode unconditional: %q %+v %v", name, opts, err)
	}
}

func TestIndexDropOpts_RoundTrip(t *testing.T) {
	want := IndexDropOptions{CheckOwner: true, Owner: "collection:db/c#1"}
	cmd, err := EncodeIndexDropOpts("db.c.f", want)
	if err != nil {
		t.Fatal(err)
	}
	if cmd[0] != OpIndexDrop {
		t.Fatalf("op byte %d", cmd[0])
	}
	name, got, err := DecodeIndexDropOpts(cmd[1:])
	if err != nil || name != "db.c.f" || got != want {
		t.Fatalf("round trip: %q %+v %v", name, got, err)
	}
	// The name-only decoder still reads the name.
	if n, err := DecodeIndexDrop(cmd[1:]); err != nil || n != "db.c.f" {
		t.Fatalf("DecodeIndexDrop: %q %v", n, err)
	}
}

func TestIndexDropOpts_Malformed(t *testing.T) {
	cmd, err := EncodeIndexDropOpts("n", IndexDropOptions{CheckOwner: true, Owner: "own"})
	if err != nil {
		t.Fatal(err)
	}
	payload := cmd[1:]
	for name, bad := range map[string][]byte{
		"too short":        {0x01},
		"name overrun":     {0x09, 0x00, 'n'},
		"truncated length": payload[:4],
		"short owner":      payload[:len(payload)-1],
	} {
		var ice *InvalidCommandError
		if _, _, err := DecodeIndexDropOpts(bad); !errors.As(err, &ice) {
			t.Errorf("%s: expected InvalidCommandError, got %v", name, err)
		}
	}
	if _, err := EncodeIndexDropOpts("n", IndexDropOptions{CheckOwner: true, Owner: string(make([]byte, 70000))}); err == nil {
		t.Fatal("an owner longer than a uint16 length must be refused")
	}
	if _, err := EncodeIndexDropOpts(string(make([]byte, 70000)), IndexDropOptions{CheckOwner: true}); err == nil {
		t.Fatal("a name longer than a uint16 length must be refused")
	}
}

// ---------------------------------------------------------------------------
// Ownership application error
// ---------------------------------------------------------------------------

func TestIndexOwnershipError_RoundTrip(t *testing.T) {
	data := EncodeIndexOwnershipError("db.c.f", "collection:db/c#1")
	name, owner, ok := DecodeIndexOwnershipError(data)
	if !ok || name != "db.c.f" || owner != "collection:db/c#1" {
		t.Fatalf("decode: %q %q %v", name, owner, ok)
	}
	msg, kind, ok := DecodeAppErrorKind(data)
	if !ok || kind != AppErrorConflict {
		t.Fatalf("an ownership refusal is a conflict application error, not a capability or corrupt one: %v %v", ok, kind)
	}
	if msg != "index db.c.f is owned by collection:db/c#1" {
		t.Fatalf("message %q", msg)
	}
	if msg, _ := DecodeAppError(EncodeIndexOwnershipError("x", "")); msg != "index x is owned by the explicit index API" {
		t.Fatalf("explicit-owner message %q", msg)
	}
}

func TestIndexOwnershipError_OtherPayloads(t *testing.T) {
	for name, data := range map[string][]byte{
		"plain result":       {0x01},
		"conflict app error": EncodeAppError(AppErrorConflict, "boom"),
		"capability error":   EncodeCapabilityError("nope"),
	} {
		if _, _, ok := DecodeIndexOwnershipError(data); ok {
			t.Errorf("%s decoded as an ownership refusal", name)
		}
	}
	// A body with no separator still decodes, as a name with no owner.
	body := append(append(append([]byte{}, appErrorPrefix...), ownershipMarker...), "lonely"...)
	if name, owner, ok := DecodeIndexOwnershipError(body); !ok || name != "lonely" || owner != "" {
		t.Fatalf("separator-less body: %q %q %v", name, owner, ok)
	}
}
