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

package file

import (
	"errors"
	"strings"
	"testing"
)

// This file exercises every typed error's Error() string directly. Several
// of these types are already produced by other tests via errors.As, but
// none of those tests called Error() itself, so the method bodies ran
// uncovered (go-qrdb#592 coverage).

func TestJournalCorruptError_Error(t *testing.T) {
	err := &JournalCorruptError{Reason: "bad magic"}
	if !strings.Contains(err.Error(), "bad magic") {
		t.Errorf("Error() = %q, want it to contain the reason", err.Error())
	}
	if !strings.Contains(err.Error(), "commit journal corrupt") {
		t.Errorf("Error() = %q, want it to name the journal", err.Error())
	}
}

func TestJournalWriteError_Error(t *testing.T) {
	inner := errors.New("disk full")
	err := &JournalWriteError{Err: inner}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("Error() = %q, want it to contain the inner error", err.Error())
	}
	if got := err.Unwrap(); got != inner {
		t.Errorf("Unwrap() = %v, want %v", got, inner)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is did not see through Unwrap()")
	}
}

func TestFormatMarkerMissingError_Error(t *testing.T) {
	err := &FormatMarkerMissingError{Dir: "/data/qrdb"}
	msg := err.Error()
	if !strings.Contains(msg, "/data/qrdb") {
		t.Errorf("Error() = %q, want it to name the directory", msg)
	}
	if !strings.Contains(msg, formatMarkerFileName) {
		t.Errorf("Error() = %q, want it to name the marker file", msg)
	}
	if !strings.Contains(msg, "no migration") {
		t.Errorf("Error() = %q, want it to say there is no migration", msg)
	}
}

func TestFormatVersionMismatchError_Error(t *testing.T) {
	err := &FormatVersionMismatchError{Found: "1", Want: "2"}
	msg := err.Error()
	if !strings.Contains(msg, "1") || !strings.Contains(msg, "2") {
		t.Errorf("Error() = %q, want it to name both versions", msg)
	}
	if !strings.Contains(msg, "no migration") {
		t.Errorf("Error() = %q, want it to say there is no migration", msg)
	}
}

func TestAtRestMismatchError_Error(t *testing.T) {
	cases := map[string]AtRestMismatchError{
		"plaintext-opened-encrypted": {Dir: "/data/a", Found: "plaintext", Want: "encrypted"},
		"encrypted-opened-plaintext": {Dir: "/data/b", Found: "encrypted", Want: "plaintext"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := tc.Error()
			for _, part := range []string{tc.Dir, tc.Found, tc.Want} {
				if !strings.Contains(msg, part) {
					t.Errorf("Error() = %q, want it to name %q", msg, part)
				}
			}
			if strings.Index(msg, tc.Found) > strings.Index(msg, tc.Want) {
				t.Errorf("Error() = %q, want the found posture before the wanted one", msg)
			}
		})
	}
}

func TestRecordCorruptError_Error(t *testing.T) {
	err := &RecordCorruptError{Path: "/data/ab/cd/deadbeef", Reason: "shorter than the key-length header"}
	msg := err.Error()
	if !strings.Contains(msg, "/data/ab/cd/deadbeef") {
		t.Errorf("Error() = %q, want it to name the path", msg)
	}
	if !strings.Contains(msg, "shorter than the key-length header") {
		t.Errorf("Error() = %q, want it to contain the reason", msg)
	}
}

func TestKeyMismatchError_Error(t *testing.T) {
	err := &KeyMismatchError{Path: "/data/ab/cd/deadbeef"}
	msg := err.Error()
	if !strings.Contains(msg, "/data/ab/cd/deadbeef") {
		t.Errorf("Error() = %q, want it to name the path", msg)
	}
	if !strings.Contains(msg, "does not match") {
		t.Errorf("Error() = %q, want it to describe a mismatch", msg)
	}
}
