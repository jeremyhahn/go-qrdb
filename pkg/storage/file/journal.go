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
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// The commit journal gives the file backend atomic (all-or-nothing after
// recovery) batch commits, which the filesystem itself cannot provide for
// multi-file mutations. A batch commit first writes every buffered
// operation into a single journal file (temp file, fsync, atomic rename,
// directory fsync), then applies the operations to their per-key files,
// and finally removes the journal. A crash before the rename leaves only
// an ignored temp file (the batch never happened); a crash after it
// leaves a complete journal that Open-time recovery re-applies in full
// (operations are absolute Set/Delete, so replay is idempotent).
//
// The journal and its temp files live in the engine root with a '.'
// prefix. encodeKeyToPath escapes a leading '.' in every key segment, so
// no data file can collide with them; the List/iterator walks skip
// dot-prefixed names for the same reason.

const (
	// journalFileName is the committed (renamed-into-place) journal.
	journalFileName = ".commit-journal"

	// journalTmpPattern names in-flight journal writes; leftovers are
	// removed at Open.
	journalTmpPattern = ".tmp-journal-*"

	// journalMagic identifies journal format version 1.
	journalMagic = "QRDBFJ1\x00"

	// journalOpSet and journalOpDelete tag each journal record.
	journalOpSet    = byte(0)
	journalOpDelete = byte(1)
)

// castagnoli is the CRC-32C table used to detect torn journal writes on
// filesystems whose rename atomicity does not extend to file contents.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// encodeJournal renders the buffered operations as a self-verifying
// journal image: magic, records, and a trailing CRC-32C over everything
// before it.
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

// decodeJournal parses and verifies a journal image. A short, unmagiced,
// or checksum-failing image returns JournalCorruptError.
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

// writeJournal durably publishes the journal image: temp file in the
// engine root, fsync, atomic rename into journalFileName, directory
// fsync. After it returns nil the batch WILL be applied — by the caller
// now, or by recovery after a crash.
func (e *Engine) writeJournal(image []byte) error {
	tmp, err := os.CreateTemp(e.dir, journalTmpPattern)
	if err != nil {
		return &JournalWriteError{Err: err}
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(image); err != nil {
		_ = tmp.Close()
		e.discardTemp(tmpName)
		return &JournalWriteError{Err: err}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		e.discardTemp(tmpName)
		return &JournalWriteError{Err: err}
	}
	if err := tmp.Close(); err != nil {
		e.discardTemp(tmpName)
		return &JournalWriteError{Err: err}
	}
	if err := fsync.Rename(tmpName, filepath.Join(e.dir, journalFileName)); err != nil {
		e.discardTemp(tmpName)
		return &JournalWriteError{Err: err}
	}
	return nil
}

// clearJournal removes the committed journal after its operations have
// been fully applied, making the commit final.
func (e *Engine) clearJournal() error {
	if err := os.Remove(filepath.Join(e.dir, journalFileName)); err != nil && !os.IsNotExist(err) {
		return &JournalWriteError{Err: err}
	}
	return fsync.Dir(e.dir)
}

// recoverJournal completes an interrupted batch commit at Open time: a
// present journal is decoded, fully re-applied (idempotent absolute
// operations), and removed. Stray temp files from never-published
// journals are deleted. A corrupt journal fails the open — the store's
// last batch is in an unknown state and silently serving it would break
// the state machine determinism contract.
func (e *Engine) recoverJournal() error {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasPrefix(ent.Name(), ".tmp-journal-") {
			_ = os.Remove(filepath.Join(e.dir, ent.Name()))
		}
	}

	return e.replayJournal()
}

// replayJournal completes a commit whose journal was left behind, then removes
// the journal. The caller holds the write lock. It is the cheap half of
// recoverJournal (one failed open when nothing is pending), run every time the
// lock is acquired: another process may have died between writing a journal and
// applying it, and the next writer must finish that commit before it journals
// or writes anything of its own.
func (e *Engine) replayJournal() error {
	data, err := os.ReadFile(filepath.Join(e.dir, journalFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
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
