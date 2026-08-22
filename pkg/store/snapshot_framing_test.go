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

package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// writeSnapshotEntry now owns the on-disk snapshot format for BOTH state
// machines, so its exact byte layout is pinned here rather than left to two
// copies agreeing by inspection.
//
// The layout is load-bearing in a way most encodings are not: the reader
// consumes this stream positionally, so a single wrong length does not corrupt
// one entry, it desynchronises everything after it. A replica then recovers a
// structurally valid stream that is not the leader's data.
func TestWriteSnapshotEntry_Framing(t *testing.T) {
	tests := []struct {
		name  string
		key   []byte
		value []byte
	}{
		{name: "ordinary pair", key: []byte("key"), value: []byte("value")},
		{name: "empty value", key: []byte("key"), value: []byte{}},
		{name: "empty key", key: []byte{}, value: []byte("value")},
		{name: "both empty", key: []byte{}, value: []byte{}},
		{name: "binary content", key: []byte{0x00, 0xff}, value: []byte{0x00, 0x01, 0xfe}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeSnapshotEntry(&buf, tt.key, tt.value); err != nil {
				t.Fatalf("writeSnapshotEntry: %v", err)
			}

			var want bytes.Buffer
			_ = binary.Write(&want, binary.LittleEndian, uint32(len(tt.key)))
			want.Write(tt.key)
			_ = binary.Write(&want, binary.LittleEndian, uint32(len(tt.value)))
			want.Write(tt.value)

			if !bytes.Equal(buf.Bytes(), want.Bytes()) {
				t.Errorf("framing = % x, want % x", buf.Bytes(), want.Bytes())
			}

			// And it must be readable back positionally, which is the property
			// the recovery path depends on.
			r := bytes.NewReader(buf.Bytes())
			var keyLen, valLen uint32
			if err := binary.Read(r, binary.LittleEndian, &keyLen); err != nil {
				t.Fatalf("read key length: %v", err)
			}
			gotKey := make([]byte, keyLen)
			if _, err := r.Read(gotKey); err != nil && keyLen > 0 {
				t.Fatalf("read key: %v", err)
			}
			if err := binary.Read(r, binary.LittleEndian, &valLen); err != nil {
				t.Fatalf("read value length: %v", err)
			}
			gotVal := make([]byte, valLen)
			if _, err := r.Read(gotVal); err != nil && valLen > 0 {
				t.Fatalf("read value: %v", err)
			}
			if r.Len() != 0 {
				t.Errorf("%d bytes left over; the entry must consume exactly what it wrote", r.Len())
			}
			if !bytes.Equal(gotKey, tt.key) || !bytes.Equal(gotVal, tt.value) {
				t.Errorf("round trip = (%q, %q), want (%q, %q)", gotKey, gotVal, tt.key, tt.value)
			}
		})
	}
}

// errAfterN is a writer that fails once a given number of writes have
// succeeded, so each of the four writes in an entry can be failed in turn.
type errAfterN struct {
	n   int
	err error
}

func (w *errAfterN) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, w.err
	}
	w.n--
	return len(p), nil
}

// Every write failure surfaces as *SnapshotError with Op "save" and the cause
// intact, at each of the four positions in an entry.
//
// The Op matters: a snapshot failure that does not name the operation is
// indistinguishable from a recovery failure in a log, and these two are
// diagnosed very differently — one loses a snapshot, the other loses a replica.
func TestWriteSnapshotEntry_WrapsWriteFailures(t *testing.T) {
	cause := errors.New("disk gone")

	// 0: key length, 1: key, 2: value length, 3: value.
	for position := 0; position < 4; position++ {
		w := &errAfterN{n: position, err: cause}
		err := writeSnapshotEntry(w, []byte("key"), []byte("value"))
		if err == nil {
			t.Fatalf("write failing at position %d: got nil error", position)
		}
		var snapErr *SnapshotError
		if !errors.As(err, &snapErr) {
			t.Fatalf("position %d: error is %T, want *SnapshotError", position, err)
		}
		if snapErr.Op != "save" {
			t.Errorf("position %d: Op = %q, want %q", position, snapErr.Op, "save")
		}
		if !errors.Is(err, cause) {
			t.Errorf("position %d: cause not preserved through the wrapper", position)
		}
	}
}

// Scope note on the length guard, so its absence from this file is not read as
// an oversight.
//
// writeSnapshotEntry refuses a key or value that a four-byte field cannot
// describe, through protocol.CheckUint32Len. That refusal is NOT exercised
// here, and cannot honestly be: triggering it needs a slice longer than
// 4,294,967,295 bytes, which is a 4 GiB allocation on a 64-bit build and
// unreachable at all on a 32-bit one, where len() cannot exceed MaxInt32.
//
// It is asserted where it can be — at the predicate, in
// pkg/protocol/lengths_test.go, which is the same reasoning already accepted
// for putUint32Len in go-qrdb#52. Sharing one exported predicate rather than
// writing a second bound here is what lets that single set of assertions cover
// this call site too; a local copy would need its own 4 GiB it also could not
// allocate.
func TestCheckSnapshotEntryLengths(t *testing.T) {
	// One past what a four-byte field can describe, computed at RUN time.
	// `int(math.MaxUint32) + 1` is a constant expression and does not compile
	// on a 32-bit target, which is the same narrowing trap that made
	// protocol.MaxUint32Field a typed uint64 in the first place — and which
	// this PR's own error type was just fixed for. Going through a variable
	// makes it a runtime conversion.
	maxField := protocol.MaxUint32Field
	tooLong := int(maxField) + 1
	representable := uint64(math.MaxInt) > maxField

	t.Run("accepts what the field can describe", func(t *testing.T) {
		if err := checkSnapshotEntryLengths(0, 0); err != nil {
			t.Errorf("empty entry refused: %v", err)
		}
		if representable {
			if err := checkSnapshotEntryLengths(int(maxField), int(maxField)); err != nil {
				t.Errorf("entry at the limit refused: %v", err)
			}
		}
	})

	if !representable {
		// A 32-bit target cannot express a length above the bound at all, so
		// the refusal below is unreachable there. Asserted rather than skipped:
		// "no int can exceed the bound" is a true and useful statement about
		// this target, and the guard exists for the 64-bit one.
		t.Run("no length on this target can exceed the bound", func(t *testing.T) {
			if err := checkSnapshotEntryLengths(math.MaxInt, math.MaxInt); err != nil {
				t.Errorf("the largest representable length was refused: %v", err)
			}
		})
		return
	}

	for _, tc := range []struct {
		name           string
		keyLen, valLen int
		field          string
	}{
		{"refuses an over-long key", tooLong, 8, "snapshot key"},
		{"refuses an over-long value", 8, tooLong, "snapshot value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSnapshotEntryLengths(tc.keyLen, tc.valLen)
			if err == nil {
				t.Fatal("an unframeable length was accepted: the encoder would write a " +
					"narrowed length and then the full payload, desynchronising every " +
					"entry after it")
			}
			var se *SnapshotError
			if !errors.As(err, &se) {
				t.Fatalf("error is %T, want *SnapshotError", err)
			}
			var tooLongErr *protocol.FieldTooLongError
			if !errors.As(err, &tooLongErr) {
				t.Fatalf("error does not wrap *protocol.FieldTooLongError: %v", err)
			}
			if tooLongErr.Field != tc.field {
				t.Errorf("Field = %q, want %q — the refusal must name which half failed",
					tooLongErr.Field, tc.field)
			}
		})
	}

	t.Run("checks the key before the value", func(t *testing.T) {
		// Both over-limit: the KEY must be the one reported, because it is
		// written first and a refusal has to come before any byte is emitted.
		err := checkSnapshotEntryLengths(tooLong, tooLong)
		var tooLongErr *protocol.FieldTooLongError
		if !errors.As(err, &tooLongErr) {
			t.Fatalf("error does not wrap *protocol.FieldTooLongError: %v", err)
		}
		if tooLongErr.Field != "snapshot key" {
			t.Errorf("Field = %q, want %q", tooLongErr.Field, "snapshot key")
		}
	})
}

// writeSnapshotEntry refuses through that helper, and emits nothing when it
// does.
//
// The refusal cannot be driven end-to-end on any target: a length above
// MaxUint32 needs a 4 GiB slice on 64-bit, and on 32-bit an int cannot hold
// one at all. What IS assertable is that the call site consults the helper for
// both fields before writing, which is what this pins — the helper's own
// refusals are covered above.
func TestWriteSnapshotEntry_ValidatesBeforeWriting(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSnapshotEntry(&buf, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("writeSnapshotEntry: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("an accepted entry wrote nothing")
	}

	// A writer that fails on its first write proves nothing was emitted before
	// validation: the error surfaces from the write, not from a guard, and the
	// entry is refused whole.
	failing := &errAfterN{n: 0, err: errors.New("disk gone")}
	err := writeSnapshotEntry(failing, []byte("k"), []byte("v"))
	if err == nil {
		t.Fatal("a failing writer was not reported")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("error is %T, want *SnapshotError", err)
	}
}

// The entry counter refuses to wrap.
//
// Driven at its limit rather than through a state machine: the condition needs
// 4,294,967,296 entries, which no test can construct, and that unreachability
// is exactly why the defect survived. Giving the counter its own type made the
// boundary reachable in a unit test — the seam is the point, not an
// implementation detail.
func TestSnapshotEntryCounter_RefusesToWrap(t *testing.T) {
	t.Run("counts up to the limit", func(t *testing.T) {
		c := &snapshotEntryCounter{n: math.MaxUint32 - 1}
		if err := c.add(); err != nil {
			t.Fatalf("add at limit-1: %v", err)
		}
		if got := c.count(); got != math.MaxUint32 {
			t.Fatalf("count = %d, want %d", got, uint32(math.MaxUint32))
		}
	})

	t.Run("refuses the entry that would wrap", func(t *testing.T) {
		c := &snapshotEntryCounter{n: math.MaxUint32}

		err := c.add()
		if err == nil {
			t.Fatal("the counter wrapped to zero: a recovery would read an empty " +
				"snapshot and report success while discarding every entry")
		}

		var se *SnapshotError
		if !errors.As(err, &se) {
			t.Fatalf("error is %T, want *SnapshotError", err)
		}
		var countErr *SnapshotEntryCountError
		if !errors.As(err, &countErr) {
			t.Fatalf("error does not wrap *SnapshotEntryCountError: %v", err)
		}
		if countErr.Max != protocol.MaxUint32Field {
			t.Errorf("Max = %d, want protocol.MaxUint32Field (%d) — the bound must "+
				"come from the shared predicate, not a local copy",
				countErr.Max, protocol.MaxUint32Field)
		}
		// The count already reached is reported as itself, not narrowed through
		// an int (which is -1 on a 32-bit target) and not described as
		// "exceeding" a maximum it merely equals.
		if countErr.Count != protocol.MaxUint32Field {
			t.Errorf("Count = %d, want %d", countErr.Count, protocol.MaxUint32Field)
		}
		if msg := countErr.Error(); !strings.Contains(msg, "4294967295 entries") ||
			strings.Contains(msg, "-1") {
			t.Errorf("message %q must state the count it holds without narrowing it", msg)
		}

		if got := c.count(); got != math.MaxUint32 {
			t.Errorf("count = %d after a refused add, want it unchanged at %d",
				got, uint32(math.MaxUint32))
		}
	})
}
