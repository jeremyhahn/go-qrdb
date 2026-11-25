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

package moderncvfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
)

// vfile is a file open on VFS qrdb: the database file or its journal.
// Each method returns a SQLite result code.
type vfile interface {
	read(p []byte, off int64) int32
	write(p []byte, off int64) int32
	truncate(size int64) int32
	sync() int32
	size() (int64, int32)
	lock(level int32) int32
	unlock(level int32) int32
	control(op int32, arg uintptr) int32
	close() int32
}

// Header fields served from the meta change counter (open item o6):
// the file change counter at offset 24 and the version-valid-for number
// at offset 92. SQLite writes them on every write transaction; the
// binding stores them as zeros and serves the snapshot's
// Meta.ChangeCounter, so they never move page 1's key revision.
var counterFields = [...]int64{24, 92}

// headerEnd is the end of the database header in page 1.
const headerEnd = 100

// dbFile is a main database file over a Store. SQLite calls one file
// from one connection at a time.
//
// Lock levels map onto the page store: SHARED begins a Txn, RESERVED
// (and anything above it) reserves it. The Txn commits when SQLite
// syncs the file holding RESERVED or above, or, when a dirty Txn is
// left unsynced, when it drops back to SHARED; dropping to NONE rolls
// back. The file refuses PRAGMA synchronous=OFF, so every write
// transaction reaches a sync before it reports success. After a
// failed commit the file ignores writes until the unlock, so SQLite's
// journal playback is a no-op.
type dbFile struct {
	store    *sqlitevfs.Store
	level    int32
	txn      *sqlitevfs.Txn
	reserved bool
	dirty    bool
	failed   bool
}

// reader returns the Txn reads use and a func to call after the read.
// Without a lock the read runs on a snapshot of its own.
func (f *dbFile) reader() (*sqlitevfs.Txn, func(), error) {
	if f.txn != nil {
		return f.txn, func() {}, nil
	}
	txn, err := f.store.Begin(context.Background())
	if err != nil {
		return nil, nil, err
	}
	if f.level >= sqlite3.SQLITE_LOCK_SHARED {
		f.txn = txn
		return txn, func() {}, nil
	}
	return txn, func() { _ = txn.Rollback(context.Background()) }, nil
}

// writer returns the reserved Txn writes go to.
func (f *dbFile) writer() (*sqlitevfs.Txn, error) {
	if f.txn == nil {
		txn, err := f.store.Begin(context.Background())
		if err != nil {
			return nil, err
		}
		f.txn = txn
	}
	if !f.reserved {
		if err := f.txn.Reserve(context.Background()); err != nil {
			return nil, err
		}
		f.reserved = true
	}
	return f.txn, nil
}

// release rolls back the Txn, if any, releasing the writer lock.
func (f *dbFile) release() {
	if f.txn != nil {
		_ = f.txn.Rollback(context.Background())
	}
	f.txn, f.reserved, f.dirty = nil, false, false
}

// commit commits the Txn as one revision. On failure the file enters
// the failed state and the mapped code, with fallback for errors
// outside the table, is returned. On success the file keeps a read Txn
// pinned at the commit's revision: SQLite may stay at SHARED after the
// commit (an open read statement) and reads on with the page cache of
// its own commit, so those reads must come from that snapshot and not
// from whatever committed since. If the pinned Begin fails, the commit
// stands and the next read begins afresh (and reports the error).
func (f *dbFile) commit(fallback int32) int32 {
	txn := f.txn
	f.txn, f.reserved, f.dirty = nil, false, false
	rev, err := txn.Commit(context.Background())
	if err != nil {
		_ = txn.Rollback(context.Background())
		f.failed = true
		return resultCode(err, fallback)
	}
	if pinned, err := f.store.BeginAt(context.Background(), rev); err == nil {
		f.txn = pinned
	}
	return sqlite3.SQLITE_OK
}

func (f *dbFile) read(p []byte, off int64) int32 {
	txn, done, err := f.reader()
	if err != nil {
		clear(p)
		return resultCode(err, sqlite3.SQLITE_IOERR_READ)
	}
	defer done()
	n, err := txn.ReadAt(p, off)
	overlayCounter(p[:n], off, txn.ChangeCounter())
	if n < len(p) {
		clear(p[n:])
		if err != nil && !errors.Is(err, io.EOF) {
			return resultCode(err, sqlite3.SQLITE_IOERR_READ)
		}
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	return sqlite3.SQLITE_OK
}

func (f *dbFile) write(p []byte, off int64) int32 {
	if f.failed {
		return sqlite3.SQLITE_OK
	}
	txn, err := f.writer()
	if err != nil {
		return resultCode(err, sqlite3.SQLITE_IOERR_WRITE)
	}
	if overlapsCounter(off, len(p)) {
		p = maskCounter(p, off)
		cur := make([]byte, len(p))
		if n, err := txn.ReadAt(cur, off); n == len(cur) && (err == nil || errors.Is(err, io.EOF)) && bytes.Equal(cur, p) {
			return sqlite3.SQLITE_OK
		}
	}
	if _, err := txn.WriteAt(p, off); err != nil {
		return resultCode(err, sqlite3.SQLITE_IOERR_WRITE)
	}
	f.dirty = true
	return sqlite3.SQLITE_OK
}

func (f *dbFile) truncate(size int64) int32 {
	if f.failed {
		return sqlite3.SQLITE_OK
	}
	txn, err := f.writer()
	if err != nil {
		return resultCode(err, sqlite3.SQLITE_IOERR_TRUNCATE)
	}
	if txn.Size() == size {
		return sqlite3.SQLITE_OK
	}
	if err := txn.Truncate(size); err != nil {
		return resultCode(err, sqlite3.SQLITE_IOERR_TRUNCATE)
	}
	f.dirty = true
	return sqlite3.SQLITE_OK
}

// sync is the commit point.
func (f *dbFile) sync() int32 {
	if f.failed || !f.dirty || f.txn == nil {
		return sqlite3.SQLITE_OK
	}
	return f.commit(sqlite3.SQLITE_IOERR_FSYNC)
}

func (f *dbFile) size() (int64, int32) {
	txn, done, err := f.reader()
	if err != nil {
		return 0, resultCode(err, sqlite3.SQLITE_IOERR_FSTAT)
	}
	defer done()
	return txn.Size(), sqlite3.SQLITE_OK
}

func (f *dbFile) lock(level int32) int32 {
	if level <= f.level {
		return sqlite3.SQLITE_OK
	}
	if f.txn == nil {
		txn, err := f.store.Begin(context.Background())
		if err != nil {
			return resultCode(err, sqlite3.SQLITE_IOERR_LOCK)
		}
		f.txn = txn
	}
	if level >= sqlite3.SQLITE_LOCK_RESERVED && !f.reserved {
		if err := f.txn.Reserve(context.Background()); err != nil {
			return resultCode(err, sqlite3.SQLITE_IOERR_LOCK)
		}
		f.reserved = true
	}
	f.level = level
	return sqlite3.SQLITE_OK
}

func (f *dbFile) unlock(level int32) int32 {
	if level >= f.level {
		return sqlite3.SQLITE_OK
	}
	rc := int32(sqlite3.SQLITE_OK)
	switch {
	case level >= sqlite3.SQLITE_LOCK_SHARED && f.dirty && !f.failed && f.txn != nil:
		rc = f.commit(sqlite3.SQLITE_IOERR_UNLOCK)
	case level >= sqlite3.SQLITE_LOCK_SHARED && !f.reserved:
		// A reader keeps its snapshot.
	default:
		f.release()
	}
	f.failed = false
	f.level = level
	return rc
}

// control answers the batch-atomic file controls and SQLITE_NOTFOUND
// for every other op. This modernc.org/sqlite build is compiled without
// SQLITE_ENABLE_BATCH_ATOMIC_WRITE, so SQLite never sends them; they
// are answered as the design states. SQLITE_FCNTL_PRAGMA refuses
// synchronous=OFF and leaves every other pragma to SQLite.
func (f *dbFile) control(op int32, arg uintptr) int32 {
	switch op {
	case sqlite3.SQLITE_FCNTL_PRAGMA:
		name, value, ok := pragmaArg(arg)
		if ok && strings.EqualFold(name, "synchronous") && synchronousOff(value) {
			return sqlite3.SQLITE_ERROR
		}
		return sqlite3.SQLITE_NOTFOUND
	case sqlite3.SQLITE_FCNTL_BEGIN_ATOMIC_WRITE:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_FCNTL_COMMIT_ATOMIC_WRITE:
		if !f.dirty || f.txn == nil {
			return sqlite3.SQLITE_OK
		}
		return f.commit(sqlite3.SQLITE_ERROR)
	case sqlite3.SQLITE_FCNTL_ROLLBACK_ATOMIC_WRITE:
		f.release()
		return sqlite3.SQLITE_OK
	default:
		return sqlite3.SQLITE_NOTFOUND
	}
}

// synchronousOff reports whether a PRAGMA synchronous value selects OFF.
func synchronousOff(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

func (f *dbFile) close() int32 {
	f.release()
	return sqlite3.SQLITE_OK
}

func overlapsCounter(off int64, n int) bool {
	return off < headerEnd && off+int64(n) > counterFields[0]
}

// overlayCounter writes the big-endian counter into the counter fields
// p, read at off, covers.
func overlayCounter(p []byte, off int64, counter uint64) {
	if !overlapsCounter(off, len(p)) {
		return
	}
	var v [4]byte
	binary.BigEndian.PutUint32(v[:], uint32(counter))
	for _, at := range counterFields {
		for i := range int64(len(v)) {
			if pos := at + i - off; pos >= 0 && pos < int64(len(p)) {
				p[pos] = v[i]
			}
		}
	}
}

// maskCounter is a copy of p, written at off, with the counter fields
// zeroed.
func maskCounter(p []byte, off int64) []byte {
	q := bytes.Clone(p)
	for _, at := range counterFields {
		for i := range int64(4) {
			if pos := at + i - off; pos >= 0 && pos < int64(len(q)) {
				q[pos] = 0
			}
		}
	}
	return q
}

// memFile is a rollback journal: SQLite needs one for every write
// transaction on this build (see control), and it lives only as long as
// the connection's open of it. xAccess never reports it, so it is never
// hot.
type memFile struct {
	data []byte
}

func (m *memFile) read(p []byte, off int64) int32 {
	n := 0
	if off < int64(len(m.data)) {
		n = copy(p, m.data[off:])
	}
	if n < len(p) {
		clear(p[n:])
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	return sqlite3.SQLITE_OK
}

func (m *memFile) write(p []byte, off int64) int32 {
	if end := off + int64(len(p)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	copy(m.data[off:], p)
	return sqlite3.SQLITE_OK
}

func (m *memFile) truncate(size int64) int32 {
	if size < int64(len(m.data)) {
		m.data = m.data[:size]
	}
	return sqlite3.SQLITE_OK
}

func (m *memFile) sync() int32 { return sqlite3.SQLITE_OK }

func (m *memFile) size() (int64, int32) { return int64(len(m.data)), sqlite3.SQLITE_OK }

func (m *memFile) lock(int32) int32 { return sqlite3.SQLITE_OK }

func (m *memFile) unlock(int32) int32 { return sqlite3.SQLITE_OK }

func (m *memFile) control(int32, uintptr) int32 { return sqlite3.SQLITE_NOTFOUND }

func (m *memFile) close() int32 {
	m.data = nil
	return sqlite3.SQLITE_OK
}
