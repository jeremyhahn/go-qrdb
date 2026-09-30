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
	"encoding/binary"
	"errors"
	"testing"
)

// absentCompare encodes "key is absent": version == 0.
func absentCompare(t *testing.T, key string) []byte {
	t.Helper()
	c, err := EncodeCompare([]byte(key), CompareTargetVersion, CompareResultEqual, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	return c
}

// innerPut is an ordinary MVCC put command, standing in for any write.
func innerPut(t *testing.T) []byte {
	t.Helper()
	cmd, err := EncodeMVCCPut([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatalf("EncodeMVCCPut: %v", err)
	}
	return cmd
}

// mapReader is a GuardReader over a map; a missing key reads as absent.
func mapReader(m map[string][]byte) GuardReader {
	return func(key []byte) ([]byte, error) {
		return m[string(key)], nil
	}
}

func TestEncodeGuarded_RoundTripsCompareAndInnerCommand(t *testing.T) {
	inner := innerPut(t)
	cmd, err := EncodeGuarded([][]byte{absentCompare(t, "fence")}, inner)
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}
	if cmd[0] != OpGuarded {
		t.Fatalf("opcode = %d, want OpGuarded (%d)", cmd[0], OpGuarded)
	}

	compares, gotInner, err := DecodeGuarded(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeGuarded: %v", err)
	}
	if len(compares) != 1 || string(compares[0].Key) != "fence" || compares[0].Target != CompareTargetVersion {
		t.Fatalf("compares = %+v, want one version compare on %q", compares, "fence")
	}
	if !bytes.Equal(gotInner, inner) {
		t.Fatalf("inner = %x, want the command byte-for-byte (%x)", gotInner, inner)
	}
}

func TestEncodeGuarded_RefusesWhatAGuardCannotCarry(t *testing.T) {
	createRev, err := EncodeCompare([]byte("k"), CompareTargetCreateRevision, CompareResultEqual, nil, 0, 1, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	nested, err := EncodeGuarded([][]byte{absentCompare(t, "f")}, innerPut(t))
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}
	tests := []struct {
		name     string
		compares [][]byte
		inner    []byte
	}{
		{"no compares", nil, innerPut(t)},
		{"empty inner command", [][]byte{absentCompare(t, "f")}, nil},
		{"nested guard", [][]byte{absentCompare(t, "f")}, nested},
		{"revision target", [][]byte{createRev}, innerPut(t)},
		{"trailing bytes after a compare", [][]byte{append(absentCompare(t, "f"), 0)}, innerPut(t)},
		{"undecodable compare", [][]byte{{1, 2}}, innerPut(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var invalid *InvalidCommandError
			if _, err := EncodeGuarded(tt.compares, tt.inner); !errors.As(err, &invalid) {
				t.Fatalf("EncodeGuarded error = %v, want *InvalidCommandError", err)
			}
		})
	}
}

func TestDecodeGuarded_RefusesMalformedPayloads(t *testing.T) {
	good, err := EncodeGuarded([][]byte{absentCompare(t, "f")}, innerPut(t))
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}
	payload := good[1:]
	compareLen := int(binary.LittleEndian.Uint32(payload[FrameSize:]))
	afterCompares := 2*FrameSize + compareLen

	zeroCount := make([]byte, FrameSize)
	nested := append(append([]byte(nil), payload[:afterCompares]...), OpGuarded, 0)
	trailing := append([]byte(nil), payload...)
	binary.LittleEndian.PutUint32(trailing[FrameSize:], uint32(compareLen+1))

	tests := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"zero compares", zeroCount},
		{"truncated compare length", payload[:FrameSize+2]},
		{"compare longer than the payload", payload[:FrameSize+FrameSize+3]},
		{"missing inner command", payload[:afterCompares]},
		{"nested guard", nested},
		{"compare frame with trailing bytes", trailing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := DecodeGuarded(tt.payload); err == nil {
				t.Fatal("DecodeGuarded accepted a malformed payload")
			}
		})
	}
}

func TestEvaluateGuard_HoldsFailsAndReportsTheRefusedKey(t *testing.T) {
	c1, _, _ := DecodeCompare(absentCompare(t, "a"))
	c2, _, _ := DecodeCompare(absentCompare(t, "b"))

	if key, ok, err := EvaluateGuard([]DecodedCompare{c1, c2}, mapReader(nil)); err != nil || !ok || key != nil {
		t.Fatalf("absent keys: (%q, %v, %v), want the guard to hold", key, ok, err)
	}
	key, ok, err := EvaluateGuard([]DecodedCompare{c1, c2}, mapReader(map[string][]byte{"b": []byte("x")}))
	if err != nil || ok {
		t.Fatalf("present key: (%v, %v), want the guard to fail", ok, err)
	}
	if string(key) != "b" {
		t.Fatalf("refused key = %q, want the compare that failed (%q)", key, "b")
	}
}

func TestEvaluateGuard_ReadFaultIsAnErrorNotARefusal(t *testing.T) {
	c, _, _ := DecodeCompare(absentCompare(t, "a"))
	boom := errors.New("disk")
	_, ok, err := EvaluateGuard([]DecodedCompare{c}, func([]byte) ([]byte, error) { return nil, boom })
	var readErr *GuardReadError
	if !errors.As(err, &readErr) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, want *GuardReadError wrapping the fault", err)
	}
	if ok {
		t.Fatal("a read fault must not be reported as a guard that held")
	}
	if readErr.Error() == "" || string(readErr.Key) != "a" {
		t.Fatalf("GuardReadError = %+v", readErr)
	}
}

func TestOpenGuarded_ThreeOutcomes(t *testing.T) {
	inner := innerPut(t)
	cmd, err := EncodeGuarded([][]byte{absentCompare(t, "fence")}, inner)
	if err != nil {
		t.Fatalf("EncodeGuarded: %v", err)
	}

	got, refusal, err := OpenGuarded(cmd[1:], mapReader(nil))
	if err != nil || refusal != nil || !bytes.Equal(got, inner) {
		t.Fatalf("guard holds: (%x, %x, %v), want the inner command", got, refusal, err)
	}

	got, refusal, err = OpenGuarded(cmd[1:], mapReader(map[string][]byte{"fence": []byte("dropping")}))
	if err != nil || got != nil {
		t.Fatalf("guard fails: (%x, %v), want no inner command and no error", got, err)
	}
	if key, ok := DecodeGuardRefusal(refusal); !ok || string(key) != "fence" {
		t.Fatalf("refusal = %x, want an encoded guard refusal naming %q", refusal, "fence")
	}

	if _, _, err := OpenGuarded(nil, mapReader(nil)); err == nil {
		t.Fatal("a malformed payload must be an error")
	}
	if _, _, err := OpenGuarded(cmd[1:], func([]byte) ([]byte, error) { return nil, errors.New("disk") }); err == nil {
		t.Fatal("a read fault must be an error")
	}
}

func TestCompareHolds_VersionAndValueTargets(t *testing.T) {
	tests := []struct {
		name    string
		c       DecodedCompare
		current []byte
		want    bool
	}{
		{"version 0 on absent", DecodedCompare{Target: CompareTargetVersion, Version: 0}, nil, true},
		{"version 0 on present", DecodedCompare{Target: CompareTargetVersion, Version: 0}, []byte{}, false},
		{"version 1 on present", DecodedCompare{Target: CompareTargetVersion, Version: 1}, []byte("v"), true},
		{"version greater", DecodedCompare{Target: CompareTargetVersion, Result: CompareResultGreater, Version: 0}, []byte("v"), true},
		{"value equal", DecodedCompare{Target: CompareTargetValue, Value: []byte("v")}, []byte("v"), true},
		{"value not equal", DecodedCompare{Target: CompareTargetValue, Result: CompareResultNotEqual, Value: []byte("v")}, []byte("w"), true},
		{"empty value on absent", DecodedCompare{Target: CompareTargetValue}, nil, true},
		{"non-empty value on absent", DecodedCompare{Target: CompareTargetValue, Value: []byte("v")}, nil, false},
		{"create revision fails closed", DecodedCompare{Target: CompareTargetCreateRevision}, []byte("v"), false},
		{"mod revision fails closed", DecodedCompare{Target: CompareTargetModRevision}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareHolds(tt.c, tt.current); got != tt.want {
				t.Fatalf("CompareHolds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCompareBytes_AllOperators(t *testing.T) {
	tests := []struct {
		result byte
		cmp    int
		want   bool
	}{
		{CompareResultEqual, 0, true}, {CompareResultEqual, 1, false},
		{CompareResultGreater, 1, true}, {CompareResultGreater, 0, false}, {CompareResultGreater, -1, false},
		{CompareResultLess, -1, true}, {CompareResultLess, 0, false}, {CompareResultLess, 1, false},
		{CompareResultNotEqual, 1, true}, {CompareResultNotEqual, -1, true}, {CompareResultNotEqual, 0, false},
		{99, 0, false}, {255, 1, false},
	}
	for _, tt := range tests {
		if got := CompareBytes(tt.result, tt.cmp); got != tt.want {
			t.Errorf("CompareBytes(%d, %d) = %v, want %v", tt.result, tt.cmp, got, tt.want)
		}
	}
}

func TestCompareInt_AllOperators(t *testing.T) {
	tests := []struct {
		result           byte
		actual, expected int64
		want             bool
	}{
		{CompareResultEqual, 5, 5, true}, {CompareResultEqual, 5, 6, false},
		{CompareResultGreater, 10, 5, true}, {CompareResultGreater, 5, 10, false}, {CompareResultGreater, 5, 5, false},
		{CompareResultLess, 3, 5, true}, {CompareResultLess, 5, 3, false}, {CompareResultLess, 5, 5, false},
		{CompareResultNotEqual, 5, 6, true}, {CompareResultNotEqual, 5, 5, false},
		{99, 5, 5, false},
	}
	for _, tt := range tests {
		if got := CompareInt(tt.result, tt.actual, tt.expected); got != tt.want {
			t.Errorf("CompareInt(%d, %d, %d) = %v, want %v", tt.result, tt.actual, tt.expected, got, tt.want)
		}
	}
}

func TestValidateGuard_AcceptsVersionAndValueTargets(t *testing.T) {
	value, err := EncodeCompare([]byte("k"), CompareTargetValue, CompareResultEqual, []byte("v"), 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	if err := ValidateGuard([][]byte{absentCompare(t, "a"), value}); err != nil {
		t.Fatalf("ValidateGuard: %v", err)
	}
	modRev, err := EncodeCompare([]byte("k"), CompareTargetModRevision, CompareResultEqual, nil, 0, 0, 1)
	if err != nil {
		t.Fatalf("EncodeCompare: %v", err)
	}
	if err := ValidateGuard([][]byte{modRev}); err == nil {
		t.Fatal("ValidateGuard accepted a revision target")
	}
}

// A guard refusal is an application error to every reader, so a reader
// that predates the marker still does not mistake it for a result -- and a
// reader that knows the marker gets the key back.
func TestGuardRefusal_EncodingIsAnAppErrorCarryingTheKey(t *testing.T) {
	data := EncodeGuardRefusal([]byte("__sys:dropfence/acme/orders/"))

	key, ok := DecodeGuardRefusal(data)
	if !ok || string(key) != "__sys:dropfence/acme/orders/" {
		t.Fatalf("DecodeGuardRefusal = (%q, %v)", key, ok)
	}
	msg, kind, isApp := DecodeAppErrorKind(data)
	if !isApp || kind != AppErrorGuardRefused {
		t.Fatalf("DecodeAppErrorKind = (%q, %v, %v), want AppErrorGuardRefused", msg, kind, isApp)
	}
	if !bytes.Contains([]byte(msg), []byte("__sys:dropfence/acme/orders/")) {
		t.Fatalf("message %q does not name the refused key", msg)
	}

	if _, ok := DecodeGuardRefusal(EncodeAppError(AppErrorConflict, "unique violation")); ok {
		t.Fatal("an ordinary application error decoded as a guard refusal")
	}
	if _, ok := DecodeGuardRefusal([]byte{1}); ok {
		t.Fatal("an ordinary result payload decoded as a guard refusal")
	}
}
