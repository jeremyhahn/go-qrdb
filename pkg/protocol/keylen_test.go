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
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// CheckKeyLen / KeyTooLongError / MaxClientKeyLength (go-qrdb#592 F4)
// ---------------------------------------------------------------------------

func TestCheckKeyLen_AtAndUnderTheCap(t *testing.T) {
	for _, n := range []int{0, 1, MaxClientKeyLength - 1, MaxClientKeyLength} {
		if err := CheckKeyLen(make([]byte, n)); err != nil {
			t.Errorf("n=%d: CheckKeyLen refused a key at or under the cap: %v", n, err)
		}
	}
}

func TestCheckKeyLen_OverTheCap(t *testing.T) {
	for _, n := range []int{MaxClientKeyLength + 1, MaxClientKeyLength + 11, 70000} {
		err := CheckKeyLen(make([]byte, n))
		var ktl *KeyTooLongError
		if !errors.As(err, &ktl) {
			t.Fatalf("n=%d: want *KeyTooLongError, got %T: %v", n, err, err)
		}
		if ktl.Length != n {
			t.Errorf("n=%d: KeyTooLongError.Length = %d, want %d", n, ktl.Length, n)
		}
	}
}

// KeyTooLongError satisfies RefusalError: it is a refusal of the request on
// its merits, never a storage fault, so ResolveApplyError and friends must
// encode it as a result rather than halt Apply.
func TestKeyTooLongError_IsARefusal(t *testing.T) {
	err := &KeyTooLongError{Length: 70000}
	if err.Refusal() == "" {
		t.Fatal("KeyTooLongError.Refusal() is empty")
	}
	if !strings.Contains(err.Error(), "70000") {
		t.Errorf("Error() = %q, want it to name the length", err.Error())
	}
	if !strings.Contains(err.Error(), "65535") {
		t.Errorf("Error() = %q, want it to name MaxClientKeyLength", err.Error())
	}
}

// ---------------------------------------------------------------------------
// DecodeKeyLen (go-qrdb#592 F1/F4): the shared primitive every 2-byte
// key-length decoder in this package, and every caller outside it, must
// use instead of computing "N + keyLen" in the field's own 16-bit width.
// ---------------------------------------------------------------------------

func TestDecodeKeyLen_BoundaryNeverWraps(t *testing.T) {
	for _, n := range []int{0, 1, 65525, 65526, 65533, 65534, 65535} {
		key := make([]byte, n)
		for i := range key {
			key[i] = 'k'
		}
		payload := make([]byte, 0, 2+n+3)
		payload = append(payload, byte(n), byte(n>>8))
		payload = append(payload, key...)
		payload = append(payload, 'V', 'A', 'L')

		got, err := DecodeKeyLen(payload, len(payload)-2, "test")
		if err != nil {
			t.Fatalf("n=%d: DecodeKeyLen: %v", n, err)
		}
		if got != n {
			t.Fatalf("n=%d: DecodeKeyLen = %d, want %d", n, got, n)
		}
		gotKey := payload[2 : 2+got]
		if string(gotKey) != string(key) {
			t.Errorf("n=%d: decoded key does not match (len %d)", n, len(gotKey))
		}
		rest := payload[2+got:]
		if string(rest) != "VAL" {
			t.Errorf("n=%d: FINDING trailing bytes corrupted: %q", n, rest)
		}
	}
}

func TestDecodeKeyLen_Errors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		rem     int
		wantMsg string
	}{
		{"payload too short", []byte{0x01}, -1, "payload too short"},
		{"length exceeds remaining", []byte{0xFF, 0x00}, 0, "key length exceeds payload"},
		{"at-cap length within remaining", append([]byte{0xFF, 0xFF}, make([]byte, 65535)...), 65535, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeKeyLen(tt.payload, tt.rem, "test")
			if tt.wantMsg == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			var ice *InvalidCommandError
			if !errors.As(err, &ice) {
				t.Fatalf("want *InvalidCommandError, got %T", err)
			}
			if !strings.Contains(ice.Reason, tt.wantMsg) {
				t.Errorf("reason = %q, want substring %q", ice.Reason, tt.wantMsg)
			}
		})
	}
}

// FuzzDecodeKeyLen never panics, for any payload and any remaining value:
// the bounds check happens entirely in int, before any slicing (go-qrdb#592
// F1).
func FuzzDecodeKeyLen(f *testing.F) {
	f.Add([]byte{0xFF, 0xFF}, 65535)
	f.Add([]byte{0x00, 0x00}, 0)
	f.Add([]byte{}, 0)
	f.Add([]byte{0x01}, -1)
	f.Fuzz(func(t *testing.T, payload []byte, remaining int) {
		_, _ = DecodeKeyLen(payload, remaining, "fuzz")
	})
}

// ---------------------------------------------------------------------------
// DecodeMVCCPut / DecodeMVCCDelete boundary (go-qrdb#592 F1)
// ---------------------------------------------------------------------------

// TestDecodeMVCCPut_Boundary is the exact shape of the F1 defect: a key
// within a few bytes of 65535 used to make "2 + keyLen + 8" wrap in 16-bit
// arithmetic, silently corrupting the decoded value for keyLen in
// [65526, 65533] and panicking the slice expression for keyLen in
// [65534, 65535].
func TestDecodeMVCCPut_Boundary(t *testing.T) {
	for _, n := range []int{65525, 65526, 65527, 65530, 65533, 65534, 65535} {
		key := make([]byte, n)
		for i := range key {
			key[i] = 'k'
		}
		encoded, err := EncodeMVCCPut(key, []byte("VAL"), 42)
		if err != nil {
			t.Fatalf("n=%d: EncodeMVCCPut: %v", n, err)
		}
		cmd, err := DecodeCommand(encoded)
		if err != nil {
			t.Fatalf("n=%d: DecodeCommand: %v", n, err)
		}
		gotKey, gotVal, leaseID, err := DecodeMVCCPut(cmd.Payload)
		if err != nil {
			t.Fatalf("n=%d: DecodeMVCCPut: %v", n, err)
		}
		if string(gotKey) != string(key) {
			t.Errorf("n=%d: key corrupted (got len %d)", n, len(gotKey))
		}
		if string(gotVal) != "VAL" {
			t.Errorf("n=%d: FINDING value corrupted: %q", n, gotVal)
		}
		if leaseID != 42 {
			t.Errorf("n=%d: leaseID = %d, want 42", n, leaseID)
		}
	}
}

func TestDecodeMVCCDelete_Boundary(t *testing.T) {
	for _, n := range []int{65525, 65526, 65533, 65534, 65535} {
		key := make([]byte, n)
		for i := range key {
			key[i] = 'k'
		}
		encoded, err := EncodeMVCCDelete(key)
		if err != nil {
			t.Fatalf("n=%d: EncodeMVCCDelete: %v", n, err)
		}
		cmd, err := DecodeCommand(encoded)
		if err != nil {
			t.Fatalf("n=%d: DecodeCommand: %v", n, err)
		}
		gotKey, err := DecodeMVCCDelete(cmd.Payload)
		if err != nil {
			t.Fatalf("n=%d: DecodeMVCCDelete: %v", n, err)
		}
		if string(gotKey) != string(key) {
			t.Errorf("n=%d: key corrupted (got len %d)", n, len(gotKey))
		}
	}
}

// FuzzDecodeMVCCPut and FuzzDecodeMVCCDelete never panic on any input.
func FuzzDecodeMVCCPut(f *testing.F) {
	k := make([]byte, 65535)
	enc, _ := EncodeMVCCPut(k, []byte("v"), 1)
	f.Add(enc)
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _, _, _ = DecodeMVCCPut(payload)
	})
}

func FuzzDecodeMVCCDelete(f *testing.F) {
	k := make([]byte, 65535)
	enc, _ := EncodeMVCCDelete(k)
	f.Add(enc)
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = DecodeMVCCDelete(payload)
	})
}

// ---------------------------------------------------------------------------
// EncodeOp / EncodeCompare at the cap, and the OpMVCCRange exemption
// (go-qrdb#592 F4)
// ---------------------------------------------------------------------------

func TestEncodeOp_KeyAtTheCap(t *testing.T) {
	key := make([]byte, MaxClientKeyLength)
	if _, err := EncodeOp(OpMVCCPut, key, []byte("v"), nil, 0); err != nil {
		t.Errorf("a key at MaxClientKeyLength was refused: %v", err)
	}
}

func TestEncodeOp_KeyOverTheCapRefused(t *testing.T) {
	key := make([]byte, MaxClientKeyLength+1)
	_, err := EncodeOp(OpMVCCPut, key, []byte("v"), nil, 0)
	var ktl *KeyTooLongError
	if !errors.As(err, &ktl) {
		t.Fatalf("want *KeyTooLongError, got %T: %v", err, err)
	}
}

// TestEncodeOp_MVCCRangeExemptFromTheKeyCap: for OpMVCCRange, "key" is the
// START of a scan range, not a client-named point key, so the length policy
// about client-named keys does not apply to it (see DecodeKeyLen's sibling
// discussion in EncodeOp's own doc comment).
func TestEncodeOp_MVCCRangeExemptFromTheKeyCap(t *testing.T) {
	start := make([]byte, MaxClientKeyLength+5000)
	if _, err := EncodeOp(OpMVCCRange, start, nil, []byte("end"), 0); err != nil {
		t.Errorf("OpMVCCRange refused a range-start longer than MaxClientKeyLength: %v", err)
	}
}

func TestEncodeOp_DeleteOverTheCapRefused(t *testing.T) {
	key := make([]byte, MaxClientKeyLength+1)
	_, err := EncodeOp(OpMVCCDelete, key, nil, nil, 0)
	var ktl *KeyTooLongError
	if !errors.As(err, &ktl) {
		t.Fatalf("want *KeyTooLongError, got %T: %v", err, err)
	}
}

func TestEncodeCompare_KeyAtTheCap(t *testing.T) {
	key := make([]byte, MaxClientKeyLength)
	if _, err := EncodeCompare(key, 0, 0, nil, 0, 0, 0); err != nil {
		t.Errorf("a key at MaxClientKeyLength was refused: %v", err)
	}
}

func TestEncodeCompare_KeyOverTheCapRefused(t *testing.T) {
	key := make([]byte, MaxClientKeyLength+1)
	_, err := EncodeCompare(key, 0, 0, nil, 0, 0, 0)
	var ktl *KeyTooLongError
	if !errors.As(err, &ktl) {
		t.Fatalf("want *KeyTooLongError, got %T: %v", err, err)
	}
}
