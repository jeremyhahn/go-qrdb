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
	"sync/atomic"

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
// Meta.ChangeCounter (in optimistic mode a per-transaction value, see
// nextServed), so they never move page 1's key revision.
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
// back. The file refuses PRAGMA synchronous=OFF (any spelling) and
// locking_mode=EXCLUSIVE, so every write
// transaction reaches a sync before it reports success. After a
// failed commit the file ignores writes until the unlock, so SQLite's
// journal playback is a no-op, and every later sync returns the failure
// code, so a retried commit never reports writes it dropped.
type dbFile struct {
	store    *sqlitevfs.Store
	level    int32
	txn      *sqlitevfs.Txn
	reserved bool
	dirty    bool
	// failed is the result code of the failed commit, or SQLITE_OK.
	failed int32
	// served is the change counter page 1 shows in this transaction in
	// optimistic mode; 0 serves the snapshot's change counter.
	served uint32
	// published is set from a commit until the unlock that ends it: txn
	// is then the snapshot pinned at this connection's own publication.
	published bool
	// pinRev is the revision of this connection's own commit until the
	// unlock below SHARED: the snapshot reads are pinned at, retried there
	// if the pin after the commit failed.
	pinRev int64
}

// servedSeq numbers the counters served in optimistic mode.
var servedSeq atomic.Uint32

// nextServed returns a change counter no earlier transaction was served.
// Served values are odd and SQLite writes the value it read plus one, so
// a served value never equals a counter SQLite remembers from its own
// commit: SQLite drops its page cache at every transaction start, since
// an optimistic commit merges other writers' pages it never read.
func nextServed() uint32 {
	return servedSeq.Add(1)<<1 | 1
}

// counter is the change counter page 1 shows for reads from txn.
func (f *dbFile) counter(txn *sqlitevfs.Txn) uint64 {
	if f.store.Optimistic() {
		if f.txn == txn && f.served != 0 {
			return uint64(f.served)
		}
		return uint64(nextServed())
	}
	return txn.ChangeCounter()
}

// reader returns the Txn reads use and a func to call after the read.
// Without a lock the read runs on a snapshot of its own.
func (f *dbFile) reader() (*sqlitevfs.Txn, func(), error) {
	if f.txn != nil {
		return f.txn, func() {}, nil
	}
	txn, err := f.begin()
	if err != nil {
		return nil, nil, err
	}
	if f.level >= sqlite3.SQLITE_LOCK_SHARED {
		f.txn = txn
		f.served = nextServed()
		return txn, func() {}, nil
	}
	return txn, func() { _ = txn.Rollback(context.Background()) }, nil
}

// writer returns the reserved Txn writes go to.
func (f *dbFile) writer() (*sqlitevfs.Txn, error) {
	if f.txn == nil {
		txn, err := f.begin()
		if err != nil {
			return nil, err
		}
		f.txn = txn
		f.served = nextServed()
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
	f.txn, f.reserved, f.dirty, f.published, f.pinRev = nil, false, false, false, 0
}

// begin opens the snapshot this connection reads: the revision of its own
// commit while the lock that made it is held (pinRev), else the newest.
// A failed pin is retried at pinRev only: the page cache SQLite keeps
// after its commit belongs to that revision, so serving a newer one would
// mix two database states; the read fails until the unlock ends the pin.
func (f *dbFile) begin() (*sqlitevfs.Txn, error) {
	if f.pinRev != 0 {
		return f.store.BeginAt(context.Background(), f.pinRev)
	}
	return f.store.Begin(context.Background())
}

// commit commits the Txn as one revision. On failure the file enters
// the failed state and the mapped code, with fallback for errors
// outside the table, is returned. On success the file keeps a read Txn
// pinned at the commit's revision: SQLite may stay at SHARED after the
// commit (an open read statement) and reads on with the page cache of
// its own commit, so those reads must come from that snapshot and not
// from whatever committed since. If the pinned Begin fails, the commit
// stands and pinRev keeps the revision: the next read retries the pin
// there (never the newest revision) and fails until the unlock.
func (f *dbFile) commit(fallback int32) int32 {
	txn := f.txn
	f.txn, f.reserved, f.dirty = nil, false, false
	if err := trimToHeader(txn); err != nil {
		_ = txn.Rollback(context.Background())
		f.failed = resultCode(err, fallback)
		return f.failed
	}
	rev, err := txn.Commit(context.Background())
	if err != nil {
		_ = txn.Rollback(context.Background())
		f.failed = resultCode(err, fallback)
		return f.failed
	}
	f.pinRev = rev
	if pinned, err := f.store.BeginAt(context.Background(), rev); err == nil {
		f.txn = pinned
		f.published = true
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
	overlayCounter(p[:n], off, f.counter(txn))
	if n < len(p) {
		clear(p[n:])
		if err != nil && !errors.Is(err, io.EOF) {
			return resultCode(err, sqlite3.SQLITE_IOERR_READ)
		}
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	return sqlite3.SQLITE_OK
}

// trimToHeader shrinks txn to the page count page 1's header records.
// SQLite truncates a shrinking rollback-journal commit only after the
// xSync that publishes it, so without this the truncate would be a
// second publication under a second writer reservation, and another
// writer committing in between fails it with a stale snapshot. A file
// with no header yet, or a header that does not match the stored page
// size, is left as it is.
func trimToHeader(txn *sqlitevfs.Txn) error {
	var hdr [32]byte
	if n, err := txn.ReadAt(hdr[:], 0); n != len(hdr) || (err != nil && !errors.Is(err, io.EOF)) {
		return nil
	}
	pageSize := int64(binary.BigEndian.Uint16(hdr[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	pages := int64(binary.BigEndian.Uint32(hdr[28:32]))
	size := txn.Size()
	if pages == 0 || pageSize == 0 || size%pageSize != 0 {
		return nil
	}
	if want := pages * pageSize; want < size {
		return txn.Truncate(want)
	}
	return nil
}

func (f *dbFile) write(p []byte, off int64) int32 {
	if f.failed != sqlite3.SQLITE_OK {
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
	if f.failed != sqlite3.SQLITE_OK {
		return sqlite3.SQLITE_OK
	}
	// A shrinking commit's truncate comes after its publication, which
	// already trimmed the image to the header's page count: it is a no-op
	// and takes no new writer reservation.
	if f.published && !f.reserved && f.txn != nil && f.txn.Size() == size {
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
	if f.failed != sqlite3.SQLITE_OK {
		return f.failed
	}
	if !f.dirty || f.txn == nil {
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
		f.served = nextServed()
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
	case level >= sqlite3.SQLITE_LOCK_SHARED && f.dirty && f.failed == sqlite3.SQLITE_OK && f.txn != nil:
		rc = f.commit(sqlite3.SQLITE_IOERR_UNLOCK)
	case level >= sqlite3.SQLITE_LOCK_SHARED && !f.reserved:
		// A reader keeps its snapshot.
	default:
		f.release()
	}
	f.failed = sqlite3.SQLITE_OK
	f.published = false
	f.level = level
	return rc
}

// control answers the batch-atomic file controls and SQLITE_NOTFOUND
// for every other op. This modernc.org/sqlite build is compiled without
// SQLITE_ENABLE_BATCH_ATOMIC_WRITE, so SQLite never sends them; they
// are answered as the design states. SQLITE_FCNTL_PRAGMA refuses
// every synchronous setting but NORMAL, FULL and EXTRA (synchronousSupported),
// and locking_mode=EXCLUSIVE in every concurrency mode (SQLite would keep
// its locks and page cache across transactions and never drop back to the
// lock levels this file commits and pins snapshots at), and
// leaves every other pragma to SQLite.
func (f *dbFile) control(op int32, arg uintptr) int32 {
	switch op {
	case sqlite3.SQLITE_FCNTL_PRAGMA:
		name, value, ok := pragmaArg(arg)
		if ok && strings.EqualFold(name, "synchronous") && !synchronousSupported(value) {
			return sqlite3.SQLITE_ERROR
		}
		if ok && strings.EqualFold(name, "locking_mode") &&
			strings.EqualFold(strings.TrimSpace(value), "exclusive") {
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

// synchronousSupported reports whether a PRAGMA synchronous value is one
// the file allows: NORMAL, FULL or EXTRA by name, by SQLite's boolean
// alias for NORMAL (on, yes, true), or by number 1, 2 or 3 as SQLite parses
// it. Everything else is refused, OFF and its aliases above all: an
// unknown name SQLite would read as NORMAL is refused too, so no spelling
// the file did not vet reaches SQLite.
func synchronousSupported(value string) bool {
	if value != "" && value[0] >= '0' && value[0] <= '9' {
		// SQLite reads a value starting with a digit as a number
		// (sqlite3Atoi: the leading digits, the rest ignored), so
		// "00", "0x" and "8" (which wraps to OFF) are numbers too.
		digits := strings.TrimLeft(value, "0")
		end := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
		if end >= 0 {
			digits = digits[:end]
		}
		return digits == "1" || digits == "2" || digits == "3"
	}
	switch strings.ToLower(value) {
	case "normal", "full", "extra", "on", "yes", "true":
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
