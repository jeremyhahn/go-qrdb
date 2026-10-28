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
	"testing"
)

func TestLogRefusal_RoundTrips(t *testing.T) {
	cases := []struct {
		reason LogRefusalReason
		size   uint64
		msg    string
	}{
		{LogRefusalNotFound, 0, "vlog: log acme/system/audit not found"},
		{LogRefusalExists, 0, "vlog: log acme/system/audit already exists"},
		{LogRefusalCheckpointExists, 1<<63 + 5, "vlog: a checkpoint for tree size 5 already exists"},
		{LogRefusalExpireNotAllowed, 0, ""},
	}
	for _, c := range cases {
		reason, size, msg, ok := DecodeLogRefusal(EncodeLogRefusal(c.reason, c.size, c.msg))
		if !ok || reason != c.reason || size != c.size || msg != c.msg {
			t.Fatalf("decode(encode(%d, %d, %q)) = %d, %d, %q, %v", c.reason, c.size, c.msg, reason, size, msg, ok)
		}
	}
}

// TestLogRefusal_GenericDecodeIsTheRefusalKind checks a reader that does not
// know the log refusal gets its message and the kind the state machine's
// refusal table gives the vlog error it encodes.
func TestLogRefusal_GenericDecodeIsTheRefusalKind(t *testing.T) {
	for reason, want := range map[LogRefusalReason]AppErrorKind{
		LogRefusalNotFound:         AppErrorInvalid,
		LogRefusalExists:           AppErrorConflict,
		LogRefusalCheckpointExists: AppErrorConflict,
		LogRefusalExpireNotAllowed: AppErrorInvalid,
	} {
		msg, kind, ok := DecodeAppErrorKind(EncodeLogRefusal(reason, 3, "vlog: refused"))
		if !ok || kind != want || msg != "vlog: refused" {
			t.Fatalf("generic decode of reason %d = %q, %v, %v; want kind %v with the message", reason, msg, kind, ok, want)
		}
	}
}

func TestLogRefusal_RejectsOtherPayloads(t *testing.T) {
	short := append(append(append([]byte{}, appErrorPrefix...), logRefusalMarker...), byte(LogRefusalExists), 0, 0)
	for name, data := range map[string][]byte{
		"plain result":      {1},
		"other refusal":     EncodeAppError(AppErrorInvalid, "vlog: log a/b/c not found"),
		"lease not found":   EncodeLeaseNotFoundError(7),
		"marker, no prefix": logRefusalMarker,
		"truncated size":    short,
		"unknown reason":    EncodeLogRefusal(0, 0, "x"),
		"reason past last":  EncodeLogRefusal(LogRefusalExpireNotAllowed+1, 0, "x"),
		"empty":             nil,
	} {
		if reason, _, msg, ok := DecodeLogRefusal(data); ok {
			t.Errorf("%s decoded as log refusal %d: %q", name, reason, msg)
		}
	}
}
