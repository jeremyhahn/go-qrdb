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

// The wire status vocabulary (go-qrdb#437).
//
// No build constraint: these name only the status constants and the two
// accessors, all unconstrained, so the file is a superset of every caller.

package wire

import (
	"bytes"
	"testing"
)

// statusNumbering is the wire's numbering, written out by hand.
//
// It is a LITERAL table and not a loop over statusNames, because the property
// under test is that these specific numbers never move. A status is a value on
// the wire: renumbering one silently reclassifies every frame every deployed
// peer has in flight, and pre-1.0 permission to renumber is not a reason to.
// Generating this table from the same map the production code uses would make
// the test agree with any renumbering, which is the one thing it exists to
// refuse.
var statusNumbering = map[string]uint16{
	// Codes 0-5 predate the vocabulary. They are the reason the numbering is
	// qrdb's own rather than gRPC's: extending was the only way to add the
	// eleven below without moving these six.
	"StatusOK":               0,
	"StatusError":            1,
	"StatusUnimplemented":    2,
	"StatusUnauthenticated":  3,
	"StatusPermissionDenied": 4,
	"StatusUnavailable":      5,

	// Added by go-qrdb#437, appended at the next free numbers.
	"StatusNotFound":           6,
	"StatusInvalidArgument":    7,
	"StatusAlreadyExists":      8,
	"StatusFailedPrecondition": 9,
	"StatusResourceExhausted":  10,
	"StatusAborted":            11,
	"StatusOutOfRange":         12,
	"StatusCanceled":           13,
	"StatusDataLoss":           14,
	"StatusDeadlineExceeded":   15,
	"StatusUnknown":            16,
}

// declaredStatuses pairs each constant with the name statusNumbering keys on.
var declaredStatuses = map[string]uint16{
	"StatusOK":                 StatusOK,
	"StatusError":              StatusError,
	"StatusUnimplemented":      StatusUnimplemented,
	"StatusUnauthenticated":    StatusUnauthenticated,
	"StatusPermissionDenied":   StatusPermissionDenied,
	"StatusUnavailable":        StatusUnavailable,
	"StatusNotFound":           StatusNotFound,
	"StatusInvalidArgument":    StatusInvalidArgument,
	"StatusAlreadyExists":      StatusAlreadyExists,
	"StatusFailedPrecondition": StatusFailedPrecondition,
	"StatusResourceExhausted":  StatusResourceExhausted,
	"StatusAborted":            StatusAborted,
	"StatusOutOfRange":         StatusOutOfRange,
	"StatusCanceled":           StatusCanceled,
	"StatusDataLoss":           StatusDataLoss,
	"StatusDeadlineExceeded":   StatusDeadlineExceeded,
	"StatusUnknown":            StatusUnknown,
}

// TestStatusNumbering_IsPinned is the wire-compatibility guard: every status
// carries the number it carries today, and the six that predate go-qrdb#437
// carry the numbers they carried before it.
func TestStatusNumbering_IsPinned(t *testing.T) {
	for name, want := range statusNumbering {
		got, ok := declaredStatuses[name]
		if !ok {
			t.Errorf("%s is numbered but no longer declared", name)
			continue
		}
		if got != want {
			t.Errorf("%s = %d, want %d — a status number is a value on the wire "+
				"and moving one reclassifies every frame in flight on every deployed peer",
				name, got, want)
		}
	}
	if len(declaredStatuses) != len(statusNumbering) {
		t.Errorf("declared %d statuses, numbered %d: a new status needs a row in "+
			"statusNumbering naming the number it will carry forever",
			len(declaredStatuses), len(statusNumbering))
	}
}

// TestStatusNumbering_IsDense proves the vocabulary EXTENDED rather than
// renumbered: the seventeen statuses occupy 0..16 with no gap and no
// duplicate, so nothing was moved aside to make room.
func TestStatusNumbering_IsDense(t *testing.T) {
	seen := make(map[uint16]string, len(declaredStatuses))
	for name, code := range declaredStatuses {
		if prev, dup := seen[code]; dup {
			t.Errorf("status %d is declared twice: %s and %s — two conditions "+
				"sharing a number cannot be told apart by a reader", code, prev, name)
		}
		seen[code] = name
	}
	for code := uint16(0); code < uint16(len(declaredStatuses)); code++ {
		if _, ok := seen[code]; !ok {
			t.Errorf("status %d is unused: a hole means a status was removed or "+
				"the numbering skipped, and the next addition would reuse it", code)
		}
	}
}

// TestStatusName_CoversEveryDeclaredStatus: a status with no name renders as a
// number in every operator-facing log this protocol reaches.
func TestStatusName_CoversEveryDeclaredStatus(t *testing.T) {
	for name, code := range declaredStatuses {
		got := StatusName(code)
		if got == "" {
			t.Errorf("%s (%d) renders as the empty string", name, code)
		}
		if len(got) > 8 && got[:8] == "STATUS_" {
			t.Errorf("%s (%d) rendered as the undeclared fallback %q", name, code, got)
		}
		if !StatusDeclared(code) {
			t.Errorf("%s (%d) is declared but StatusDeclared says otherwise", name, code)
		}
	}
}

// TestStatusName_UndeclaredRendersItsNumber: a frame from a newer peer must be
// legible in a log without being reported as something this build recognises.
func TestStatusName_UndeclaredRendersItsNumber(t *testing.T) {
	const future = uint16(4242)
	if StatusDeclared(future) {
		t.Fatalf("status %d must not be declared by this build", future)
	}
	if got, want := StatusName(future), "STATUS_4242"; got != want {
		t.Errorf("StatusName(%d) = %q, want %q", future, got, want)
	}
	// The boundary matters: one past the last declared status is the first
	// number a newer peer will use.
	if got, want := StatusName(uint16(len(declaredStatuses))), "STATUS_17"; got != want {
		t.Errorf("StatusName(%d) = %q, want %q", len(declaredStatuses), got, want)
	}
}

// TestStatusNames_AreDistinct: two statuses sharing a rendering would make a
// log ambiguous about which condition occurred.
func TestStatusNames_AreDistinct(t *testing.T) {
	seen := make(map[string]uint16, len(declaredStatuses))
	for _, code := range declaredStatuses {
		name := StatusName(code)
		if prev, dup := seen[name]; dup {
			t.Errorf("statuses %d and %d both render as %q", prev, code, name)
		}
		seen[name] = code
	}
}

// TestNewStatuses_RoundTripOnTheWire runs every status added by go-qrdb#437
// through the real frame writer and reader.
//
// The status field is a uint16 and the new values are the first to exceed the
// single digits the protocol had carried until now, so this asserts the
// framing itself was never the constraint — the vocabulary was.
func TestNewStatuses_RoundTripOnTheWire(t *testing.T) {
	for name, code := range declaredStatuses {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			payload := []byte("the server's reason")
			if err := WriteResponse(&buf, 7, code, payload); err != nil {
				t.Fatalf("WriteResponse: %v", err)
			}
			gotID, gotStatus, gotPayload, err := ReadResponse(&buf)
			if err != nil {
				t.Fatalf("ReadResponse: %v", err)
			}
			if gotID != 7 {
				t.Errorf("requestID = %d, want 7", gotID)
			}
			if gotStatus != code {
				t.Errorf("status = %d, want %d", gotStatus, code)
			}
			if !bytes.Equal(gotPayload, payload) {
				t.Errorf("payload = %q, want %q", gotPayload, payload)
			}
		})
	}
}
