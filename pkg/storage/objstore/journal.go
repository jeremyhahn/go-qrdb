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

package objstore

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// The commit journal gives the objstore backend atomic (all-or-nothing after
// recovery) batch commits, matching the file backend's contract: go-objstore
// providers offer no native multi-object transaction, so without a journal a
// mid-commit failure or crash could leave an arbitrary subset of a batch's
// operations applied. A batch commit first publishes every buffered
// operation as a single journal object (one atomic Put), then applies the
// operations to their per-key objects, and finally deletes the journal
// object. A crash before the journal Put is observed as durable leaves only
// an absent object (the batch never happened); a crash after leaves a
// present journal object that Open-time recovery replays in full (operations
// are absolute Set/Delete, so replay is idempotent).
//
// journalName lies outside both the "k/" (direct) and "h/" (hashed) key
// prefixes, exactly like formatMarkerName, so no key's encoded name can ever
// collide with it.

const (
	// journalName is the committed journal object.
	journalName = "qrdb-objstore-journal"

	// journalMagic identifies journal format version 1. Distinct from the
	// file backend's magic so the two formats are never confused if a
	// journal image is ever inspected out of context.
	journalMagic = "QRDBOJ1\x00"

	// journalOpSet and journalOpDelete tag each journal record.
	journalOpSet    = byte(0)
	journalOpDelete = byte(1)
)

// castagnoli is the CRC-32C table used to detect a torn or corrupt journal
// object.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// encodeJournal renders the buffered operations as a self-verifying journal
// image: magic, records, and a trailing CRC-32C over everything before it.
func encodeJournal(ops []storage.BufferedOp) []byte {
	var b bytes.Buffer
	b.WriteString(journalMagic)
	var n [4]byte
	for _, op := range ops {
		if op.Deleted {
			b.WriteByte(journalOpDelete)
		} else {
			b.WriteByte(journalOpSet)
		}
		binary.BigEndian.PutUint32(n[:], uint32(len(op.Key)))
		b.Write(n[:])
		b.Write(op.Key)
		if op.Deleted {
			continue
		}
		binary.BigEndian.PutUint32(n[:], uint32(len(op.Value)))
		b.Write(n[:])
		b.Write(op.Value)
	}
	binary.BigEndian.PutUint32(n[:], crc32.Checksum(b.Bytes(), castagnoli))
	b.Write(n[:])
	return b.Bytes()
}

// decodeJournal parses and verifies a journal image. A short, unmagiced, or
// checksum-failing image returns JournalCorruptError.
func decodeJournal(data []byte) ([]storage.BufferedOp, error) {
	if len(data) < len(journalMagic)+4 || string(data[:len(journalMagic)]) != journalMagic {
		return nil, &JournalCorruptError{Reason: "missing or unknown journal header"}
	}
	body, sum := data[:len(data)-4], binary.BigEndian.Uint32(data[len(data)-4:])
	if crc32.Checksum(body, castagnoli) != sum {
		return nil, &JournalCorruptError{Reason: "checksum mismatch"}
	}

	ops := []storage.BufferedOp{}
	rest := body[len(journalMagic):]
	readN := func(n int) ([]byte, bool) {
		if len(rest) < n {
			return nil, false
		}
		out := rest[:n]
		rest = rest[n:]
		return out, true
	}
	for len(rest) > 0 {
		tag, ok := readN(1)
		if !ok {
			return nil, &JournalCorruptError{Reason: "truncated record tag"}
		}
		klenB, ok := readN(4)
		if !ok {
			return nil, &JournalCorruptError{Reason: "truncated key length"}
		}
		key, ok := readN(int(binary.BigEndian.Uint32(klenB)))
		if !ok {
			return nil, &JournalCorruptError{Reason: "truncated key"}
		}
		op := storage.BufferedOp{Key: append([]byte{}, key...)}
		switch tag[0] {
		case journalOpDelete:
			op.Deleted = true
		case journalOpSet:
			vlenB, vok := readN(4)
			if !vok {
				return nil, &JournalCorruptError{Reason: "truncated value length"}
			}
			val, vok2 := readN(int(binary.BigEndian.Uint32(vlenB)))
			if !vok2 {
				return nil, &JournalCorruptError{Reason: "truncated value"}
			}
			op.Value = append([]byte{}, val...)
		default:
			return nil, &JournalCorruptError{Reason: "unknown record tag"}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// writeJournal durably publishes the journal image as a single object Put.
// After it returns nil the batch WILL be applied -- by the caller now, or by
// recovery after a crash.
func (e *Engine) writeJournal(image []byte) error {
	if err := e.store.Put(journalName, bytes.NewReader(image)); err != nil {
		return &JournalWriteError{Err: err}
	}
	return nil
}

// clearJournal removes the committed journal object after its operations
// have been fully applied, making the commit final.
func (e *Engine) clearJournal() error {
	if err := e.store.Delete(journalName); err != nil && !notFound(err) {
		return &JournalWriteError{Err: err}
	}
	return nil
}

// recoverJournal completes an interrupted batch commit at Open time: a
// present journal object is decoded, fully re-applied (idempotent absolute
// operations), and removed. A corrupt journal fails the open -- the store's
// last batch is in an unknown state and silently serving it would break the
// state machine determinism contract.
func (e *Engine) recoverJournal() error {
	rc, err := e.store.Get(journalName)
	if err != nil {
		if notFound(err) {
			return nil
		}
		return err
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return err
	}
	ops, err := decodeJournal(data)
	if err != nil {
		return err
	}
	if err := e.applyBufferedOps(ops); err != nil {
		return err
	}
	return e.clearJournal()
}
