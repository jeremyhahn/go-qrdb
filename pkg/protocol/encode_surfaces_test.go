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

package protocol

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

// TestEncodeMVCCPutIfAbsent_DecodesAsAnMVCCPut pins the documented layout:
// identical to EncodeMVCCPut apart from the opcode.
func TestEncodeMVCCPutIfAbsent_DecodesAsAnMVCCPut(t *testing.T) {
	key, value := []byte("mk"), []byte("mv")
	cmd, err := EncodeMVCCPutIfAbsent(key, value, 77)
	if err != nil {
		t.Fatalf("EncodeMVCCPutIfAbsent: %v", err)
	}
	if cmd[0] != OpMVCCPutIfAbsent {
		t.Fatalf("opcode = %d, want OpMVCCPutIfAbsent", cmd[0])
	}
	put, err := EncodeMVCCPut(key, value, 77)
	if err != nil {
		t.Fatalf("EncodeMVCCPut: %v", err)
	}
	if !bytes.Equal(cmd[1:], put[1:]) {
		t.Fatalf("payload %x differs from EncodeMVCCPut's %x", cmd[1:], put[1:])
	}
	gotKey, gotValue, leaseID, err := DecodeMVCCPut(cmd[1:])
	if err != nil || string(gotKey) != "mk" || string(gotValue) != "mv" || leaseID != 77 {
		t.Fatalf("DecodeMVCCPut = %q, %q, %d, %v", gotKey, gotValue, leaseID, err)
	}

	empty, err := EncodeMVCCPutIfAbsent(key, nil, 0)
	if err != nil {
		t.Fatalf("EncodeMVCCPutIfAbsent with no value: %v", err)
	}
	if _, v, _, err := DecodeMVCCPut(empty[1:]); err != nil || len(v) != 0 {
		t.Fatalf("an empty value decoded as %q, %v", v, err)
	}
}

func TestEncodeMVCCPutIfAbsent_RefusesAnOversizeKey(t *testing.T) {
	cmd, err := EncodeMVCCPutIfAbsent(make([]byte, math.MaxUint16+1), []byte("v"), 0)
	if err == nil || cmd != nil {
		t.Fatalf("EncodeMVCCPutIfAbsent with a 64 KiB key = %x, %v, want a refusal", cmd, err)
	}
}

func TestEncodeDeleteRange_RoundTrip(t *testing.T) {
	for name, tc := range map[string]struct{ start, end []byte }{
		"bounded":   {[]byte("a"), []byte("c")},
		"unbounded": {[]byte("app/"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			cmd, err := EncodeDeleteRange(tc.start, tc.end)
			if err != nil {
				t.Fatalf("EncodeDeleteRange: %v", err)
			}
			op, n, err := DecodeOp(cmd)
			if err != nil {
				t.Fatalf("DecodeOp: %v", err)
			}
			if n != len(cmd) || op.Type != OpDeleteRange || !bytes.Equal(op.Key, tc.start) ||
				!bytes.Equal(op.RangeEnd, tc.end) || len(op.Value) != 0 || op.LeaseID != 0 {
				t.Fatalf("DecodeOp = %+v (%d of %d bytes), want a delete range over [%q, %q)", op, n, len(cmd), tc.start, tc.end)
			}
		})
	}
}

func TestEncodeDeleteRange_HoldsBothBoundsToTheKeyCap(t *testing.T) {
	long := make([]byte, MaxClientKeyLength+1)
	for name, tc := range map[string]struct{ start, end []byte }{
		"start": {long, nil},
		"end":   {[]byte("a"), long},
	} {
		_, err := EncodeDeleteRange(tc.start, tc.end)
		var tooLong *KeyTooLongError
		if !errors.As(err, &tooLong) || tooLong.Length != len(long) {
			t.Errorf("EncodeDeleteRange with an oversize %s = %v, want a KeyTooLongError", name, err)
		}
	}
	if _, err := EncodeDeleteRange(make([]byte, MaxClientKeyLength), make([]byte, MaxClientKeyLength)); err != nil {
		t.Fatalf("EncodeDeleteRange at the cap = %v, want it accepted", err)
	}
}

func TestDecodeCompareAndDelete_RefusesAShortPayload(t *testing.T) {
	for name, payload := range map[string][]byte{
		"empty":                       nil,
		"one byte":                    {0x01},
		"key length past the payload": {0x05, 0x00, 'a', 'b'},
	} {
		key, expected, err := DecodeCompareAndDelete(payload)
		var ice *InvalidCommandError
		if !errors.As(err, &ice) || !strings.HasPrefix(ice.Reason, "compare and delete:") || key != nil || expected != nil {
			t.Errorf("DecodeCompareAndDelete(%s) = %q, %q, %v, want an InvalidCommandError", name, key, expected, err)
		}
	}
	key, expected, err := DecodeCompareAndDelete([]byte{0x00, 0x00})
	if err != nil || len(key) != 0 || len(expected) != 0 {
		t.Fatalf("DecodeCompareAndDelete of an empty key and value = %q, %q, %v", key, expected, err)
	}
}

func TestEncodedSizeOverflowError_NamesBothTerms(t *testing.T) {
	_, err := addSize(math.MaxInt, 1)
	var overflow *EncodedSizeOverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("addSize(MaxInt, 1) = %v, want an EncodedSizeOverflowError", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "protocol: encoded size ") || !strings.Contains(msg, " plus 1 overflows int") {
		t.Fatalf("Error() = %q, want both terms named", msg)
	}
}
