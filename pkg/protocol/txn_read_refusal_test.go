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

func TestTxnReadRefusal_RoundTrip(t *testing.T) {
	data := EncodeTxnReadRefusal(4<<20, 4<<20+7)
	limit, read, ok := DecodeTxnReadRefusal(data)
	if !ok || limit != 4<<20 || read != 4<<20+7 {
		t.Fatalf("DecodeTxnReadRefusal = (%d, %d, %v), want (%d, %d, true)", limit, read, ok, 4<<20, 4<<20+7)
	}
	// A generic reader still sees an application error -- never a result.
	msg, kind, isErr := DecodeAppErrorKind(data)
	if !isErr || kind != AppErrorInvalid || msg != txnReadRefusalMessage(4<<20, 4<<20+7) {
		t.Fatalf("DecodeAppErrorKind = (%q, %d, %v), want the invalid-kind refusal message", msg, kind, isErr)
	}
}

func TestTxnReadRefusal_RejectsOtherPayloads(t *testing.T) {
	short := EncodeTxnReadRefusal(1, 2)
	for name, data := range map[string][]byte{
		"nil":             nil,
		"plain result":    []byte("plain result"),
		"other refusal":   EncodeAppError(AppErrorInvalid, "x"),
		"compact refusal": EncodeCompactRefusal(CompactRefusalInvalid, 1, 2),
		"truncated body":  short[:len(short)-1],
	} {
		if _, _, ok := DecodeTxnReadRefusal(data); ok {
			t.Errorf("%s decoded as a transaction-read refusal", name)
		}
	}
}
