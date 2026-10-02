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
	"strings"
	"testing"
)

func TestCompactRefusal_RoundTrip(t *testing.T) {
	for _, reason := range []CompactRefusalReason{CompactRefusalInvalid, CompactRefusalFuture, CompactRefusalCompacted} {
		data := EncodeCompactRefusal(reason, -3, 1<<40)
		gotReason, requested, bound, ok := DecodeCompactRefusal(data)
		if !ok || gotReason != reason || requested != -3 || bound != 1<<40 {
			t.Fatalf("round trip of %d = %d, %d, %d, %v", reason, gotReason, requested, bound, ok)
		}
		// A generic reader still sees an application error -- never a result.
		msg, kind, isErr := DecodeAppErrorKind(data)
		if !isErr || kind != AppErrorInvalid || !strings.HasPrefix(msg, "mvcc compact: revision -3") {
			t.Fatalf("generic decode of %d = %q, %d, %v", reason, msg, kind, isErr)
		}
	}
}

func TestDecodeCompactRefusal_RejectsOtherPayloads(t *testing.T) {
	for name, data := range map[string][]byte{
		"ordinary result":     {1},
		"another app error":   EncodeAppError(AppErrorInvalid, "x"),
		"guard refusal":       EncodeGuardRefusal([]byte("k")),
		"truncated body":      EncodeCompactRefusal(CompactRefusalFuture, 1, 2)[:20],
		"trailing bytes":      append(EncodeCompactRefusal(CompactRefusalFuture, 1, 2), 0),
		"nil":                 nil,
		"prefix without body": appErrorPrefix,
		"marker without body": append(append([]byte{}, appErrorPrefix...), compactRefusalMarker...),
	} {
		if _, _, _, ok := DecodeCompactRefusal(data); ok {
			t.Errorf("%s decoded as a compaction refusal", name)
		}
	}
}

func TestCompactRefusalMessage_EveryReason(t *testing.T) {
	tests := map[CompactRefusalReason]string{
		CompactRefusalInvalid:      "is invalid",
		CompactRefusalFuture:       "future revision (current 9)",
		CompactRefusalCompacted:    "has been compacted (floor 9)",
		CompactRefusalReason(0xEE): "unknown reason 238",
	}
	for reason, want := range tests {
		if got := compactRefusalMessage(reason, 4, 9); !strings.Contains(got, want) {
			t.Errorf("compactRefusalMessage(%d) = %q, want it to contain %q", reason, got, want)
		}
	}
}
