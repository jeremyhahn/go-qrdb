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
	"encoding/binary"
	"io"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// writeSnapshotEntry writes one length-framed key/value pair to a snapshot
// stream, refusing anything a four-byte length field cannot describe.
//
// Both state machines framed these fields with a bare uint32(len(x)) cast and
// then wrote the full value regardless, which is worse here than in the command
// encoders that share the defect. A mis-framed command corrupts one record; a
// mis-framed SNAPSHOT desynchronises the stream from that point on. A 4 GiB + 1
// byte value writes a length of 1 followed by 4 GiB + 1 bytes of payload, so
// RecoverFromSnapshot reads a one-byte value and then treats the remaining
// ~4 GiB as the next key length, the next key, the next value — and the replica
// comes up structurally valid, silently diverged from the leader it copied,
// on the recovery path where nothing is left to cross-check it (go-qrdb#54).
//
// The bound is protocol.CheckUint32Len rather than a local comparison. Two
// definitions of one bound is the drift this family of defects is made of, and
// the exported predicate is the same one the command encoders write through.
//
// Both lengths are validated BEFORE either is written, so a refusal never emits
// a truncated frame — a half-written entry would desynchronise the stream just
// as effectively as a narrowed length.
func writeSnapshotEntry(w io.Writer, key, value []byte) error {
	if err := checkSnapshotEntryLengths(len(key), len(value)); err != nil {
		return err
	}

	if err := binary.Write(w, binary.LittleEndian, uint32(len(key))); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	if _, err := w.Write(key); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(value))); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	if _, err := w.Write(value); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	return nil
}

// checkSnapshotEntryLengths refuses an entry whose key or value a four-byte
// length field cannot describe.
//
// It takes LENGTHS rather than the slices so the refusal is reachable in a
// test: driving it through writeSnapshotEntry would need a 4 GiB fixture, and
// a guard nobody can execute is a guard nobody can verify. Both fields are
// checked BEFORE either is written, so a refusal never emits a truncated frame
// — a half-written entry desynchronises the stream just as effectively as a
// narrowed length (go-qrdb#54).
func checkSnapshotEntryLengths(keyLen, valueLen int) error {
	if err := protocol.CheckUint32Len(keyLen, "snapshot key"); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	if err := protocol.CheckUint32Len(valueLen, "snapshot value"); err != nil {
		return &SnapshotError{Op: "save", Err: err}
	}
	return nil
}

// SnapshotEntryCountError reports that a snapshot holds as many entries as its
// four-byte count field can describe, and one more would wrap it to zero.
//
// A dedicated type rather than protocol.FieldTooLongError, whose Length is an
// int and whose meaning is "this value is too long". Neither fits: the count
// at the limit is not itself too long — it is the NEXT entry that cannot be
// counted — and narrowing 4,294,967,295 through an int yields -1 on a 32-bit
// target. The message would have read "4294967295 exceeds 4294967295", which
// is both false and useless to whoever finds it in a log (go-qrdb#54).
type SnapshotEntryCountError struct {
	// Count is the number of entries already counted, at the limit.
	Count uint64
	// Max is the largest count the four-byte field can describe.
	Max uint64
}

func (e *SnapshotEntryCountError) Error() string {
	return "store: snapshot already holds " + strconv.FormatUint(e.Count, 10) +
		" entries, the most a four-byte count field can describe (" +
		strconv.FormatUint(e.Max, 10) + "); counting another would wrap it to zero " +
		"and recovery would restore an empty database"
}

// snapshotEntryCounter counts snapshot entries into the four-byte field the
// format reserves for the count, refusing to wrap rather than narrowing.
//
// It is the same defect as the key/value lengths above, one level up, and its
// consequence is worse. Both state machines counted into a bare uint32 and
// incremented without a guard, while the SECOND pass wrote every entry
// regardless. At 4,294,967,296 non-metadata entries the count wraps to zero,
// so RecoverFromSnapshot reads "zero entries", stops, and reports SUCCESS —
// restoring an empty database and silently discarding the entire trailing
// snapshot. A mis-framed value corrupts a record; a wrapped count loses the
// whole replica while every layer above reports a healthy recovery
// (go-qrdb#54).
//
// A separate type rather than an inline check because the two state machines
// carried identical copies of this loop, which is how the bound drifts in the
// first place — and because a counter with an add method can be driven to its
// limit in a unit test, which billions of records cannot.
type snapshotEntryCounter struct {
	n uint32
}

// add counts one entry, returning a *SnapshotError at the limit instead of
// wrapping to zero.
func (c *snapshotEntryCounter) add() error {
	if uint64(c.n) >= protocol.MaxUint32Field {
		return &SnapshotError{
			Op: "save",
			Err: &SnapshotEntryCountError{
				Count: uint64(c.n),
				Max:   protocol.MaxUint32Field,
			},
		}
	}
	c.n++
	return nil
}

// count returns the tally to write into the frame.
func (c *snapshotEntryCounter) count() uint32 { return c.n }
