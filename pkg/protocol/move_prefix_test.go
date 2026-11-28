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

func moveOps(t *testing.T) [][]byte {
	t.Helper()
	put, err := EncodeOp(OpMVCCPut, []byte("db/meta"), []byte("m2"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	del, err := EncodeOp(OpMVCCDelete, []byte("db/p/9"), nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{put, del}
}

func TestMVCCMovePrefixRoundtrip(t *testing.T) {
	cmd, err := EncodeMVCCMovePrefix([]byte("db/s/1/"), []byte("db/p/"), 3, moveOps(t), moveCompares(t))
	if err != nil {
		t.Fatal(err)
	}
	if cmd[0] != OpMVCCMovePrefix {
		t.Fatalf("opcode = %d, want %d", cmd[0], OpMVCCMovePrefix)
	}
	src, dst, expected, ops, cmps, err := DecodeMVCCMovePrefix(cmd[1:])
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != "db/s/1/" || string(dst) != "db/p/" || expected != 3 {
		t.Fatalf("decoded %q %q %d", src, dst, expected)
	}
	if len(ops) != 2 || ops[0].Type != OpMVCCPut || string(ops[0].Key) != "db/meta" ||
		string(ops[0].Value) != "m2" || ops[1].Type != OpMVCCDelete || string(ops[1].Key) != "db/p/9" {
		t.Fatalf("decoded ops %+v", ops)
	}
	if len(cmps) != 2 ||
		string(cmps[0].Key) != "db/p/1" || cmps[0].Target != CompareTargetModRevision ||
		cmps[0].Result != CompareResultEqual || cmps[0].ModRevision != 0 ||
		string(cmps[1].Key) != "db/p/2" || cmps[1].Target != CompareTargetModRevision ||
		cmps[1].Result != CompareResultLess || cmps[1].ModRevision != 7 {
		t.Fatalf("decoded compares %+v", cmps)
	}

	// No extra ops and no compares is a valid move.
	cmd, err = EncodeMVCCMovePrefix([]byte("a/"), []byte("b/"), 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, ops, cmps, err = DecodeMVCCMovePrefix(cmd[1:]); err != nil || len(ops) != 0 || len(cmps) != 0 {
		t.Fatalf("ops %v compares %v err %v", ops, cmps, err)
	}
}

// moveCompares is a read set of two compares: one key never written
// (ModRevision equal 0) and one read at a revision below 7.
func moveCompares(t *testing.T) [][]byte {
	t.Helper()
	absent, err := EncodeCompare([]byte("db/p/1"), CompareTargetModRevision, CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	older, err := EncodeCompare([]byte("db/p/2"), CompareTargetModRevision, CompareResultLess, nil, 0, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{absent, older}
}

func TestEncodeMVCCMovePrefix_Refusals(t *testing.T) {
	long := bytes.Repeat([]byte("k"), MaxClientKeyLength+1)
	cases := []struct {
		name     string
		src, dst []byte
		expected int64
	}{
		{"empty src", nil, []byte("b/"), 1},
		{"empty dst", []byte("a/"), nil, 1},
		{"zero expected", []byte("a/"), []byte("b/"), 0},
		{"negative expected", []byte("a/"), []byte("b/"), -1},
	}
	for _, tc := range cases {
		_, err := EncodeMVCCMovePrefix(tc.src, tc.dst, tc.expected, nil, nil)
		var inv *InvalidCommandError
		if !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want *InvalidCommandError", tc.name, err)
		}
	}
	for _, pair := range [][2][]byte{{long, []byte("b/")}, {[]byte("a/"), long}} {
		_, err := EncodeMVCCMovePrefix(pair[0], pair[1], 1, nil, nil)
		var tooLong *KeyTooLongError
		if !errors.As(err, &tooLong) {
			t.Errorf("oversize prefix: err = %v, want *KeyTooLongError", err)
		}
	}
}

func TestDecodeMVCCMovePrefix_Refusals(t *testing.T) {
	cmd, err := EncodeMVCCMovePrefix([]byte("a/"), []byte("b/"), 2, moveOps(t), moveCompares(t))
	if err != nil {
		t.Fatal(err)
	}
	payload := cmd[1:]
	bad := map[string][]byte{
		"empty":    {},
		"short":    payload[:len(payload)-1],
		"trailing": append(append([]byte(nil), payload...), 0),
		// [srcLen=0][dstLen=2]b/...
		"zero src": append([]byte{0, 0}, payload[4:]...),
		"zero dst": append(append([]byte(nil), payload[:4]...), append([]byte{0, 0}, payload[8:]...)...),
		"no count": payload[:8+8],
	}
	zeroExpected := append([]byte(nil), payload...)
	for i := 8; i < 16; i++ {
		zeroExpected[i] = 0
	}
	bad["zero expected"] = zeroExpected

	// A move with no compares ends in [cmpCount=0]; rewrite that tail.
	cmd, err = EncodeMVCCMovePrefix([]byte("a/"), []byte("b/"), 2, moveOps(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	noCmp := cmd[1:]
	head := noCmp[:len(noCmp)-4]
	bad["no compare count"] = append([]byte(nil), head...)
	// [cmpCount=5] with no compare frames after it.
	bad["compare count past payload"] = append(append([]byte(nil), head...), 5, 0, 0, 0)
	// [cmpCount=1][cmpLen=100] and two bytes of compare.
	bad["truncated compare frame"] = append(append([]byte(nil), head...), 1, 0, 0, 0, 100, 0, 0, 0, 1, 2)
	// [cmpCount=1] and half a length frame.
	bad["truncated compare length"] = append(append([]byte(nil), head...), 1, 0, 0, 0, 4, 0)

	for name, p := range bad {
		_, _, _, _, _, err := DecodeMVCCMovePrefix(p)
		var inv *InvalidCommandError
		if !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want *InvalidCommandError", name, err)
		}
	}
}
