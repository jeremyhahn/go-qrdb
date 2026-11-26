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
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
)

var errRevision = errors.New("backend revision read failed")

// revisionFailBackend fails Revision while fail is set, the way a backend
// whose read path is down does: every new snapshot fails to begin.
type revisionFailBackend struct {
	*sqlitevfs.MVCCBackend
	fail atomic.Bool
}

func (b *revisionFailBackend) Revision(ctx context.Context) (int64, error) {
	if b.fail.Load() {
		return 0, errRevision
	}
	return b.MVCCBackend.Revision(ctx)
}

func newFile(t *testing.T, b sqlitevfs.Backend, name string, opts ...sqlitevfs.Option) *dbFile {
	t.Helper()
	s, err := sqlitevfs.NewStore(b, name, opts...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	f := &dbFile{store: s}
	t.Cleanup(func() { f.close() })
	return f
}

// cstr returns a NUL-terminated copy of s in C memory, freed at cleanup:
// libc's strlen reads whole words, which checkptr refuses on Go memory.
func cstr(t *testing.T, s string) uintptr {
	t.Helper()
	p, err := libc.CString(s)
	if err != nil {
		t.Fatalf("CString: %v", err)
	}
	t.Cleanup(func() {
		tls := libc.NewTLS()
		libc.Xfree(tls, p)
		tls.Close()
	})
	return p
}

func TestDBFile_BackendReadFailureMapsToIOErr(t *testing.T) {
	b := &revisionFailBackend{MVCCBackend: newBackend(t)}
	f := newFile(t, b, "revfail.db")
	b.fail.Store(true)

	p := []byte{1, 2, 3, 4}
	if rc := f.read(p, 0); rc != sqlite3.SQLITE_IOERR_READ {
		t.Fatalf("read rc = %d, want SQLITE_IOERR_READ", rc)
	}
	if p[0] != 0 || p[3] != 0 {
		t.Fatalf("read left %v in the buffer, want it cleared", p)
	}
	if _, rc := f.size(); rc != sqlite3.SQLITE_IOERR_FSTAT {
		t.Fatalf("size rc = %d, want SQLITE_IOERR_FSTAT", rc)
	}
	if rc := f.lock(sqlite3.SQLITE_LOCK_SHARED); rc != sqlite3.SQLITE_IOERR_LOCK {
		t.Fatalf("lock rc = %d, want SQLITE_IOERR_LOCK", rc)
	}
	if rc := f.write([]byte{1}, 200); rc != sqlite3.SQLITE_IOERR_WRITE {
		t.Fatalf("write rc = %d, want SQLITE_IOERR_WRITE", rc)
	}
	if rc := f.truncate(0); rc != sqlite3.SQLITE_IOERR_TRUNCATE {
		t.Fatalf("truncate rc = %d, want SQLITE_IOERR_TRUNCATE", rc)
	}
	b.fail.Store(false)
	if _, rc := f.size(); rc != sqlite3.SQLITE_OK {
		t.Fatalf("size after recovery rc = %d, want SQLITE_OK", rc)
	}
}

func TestDBFile_ReserveContentionIsBusy(t *testing.T) {
	b := newBackend(t)
	const name = "reserve.db"
	l := newMemLocker()
	writer := func(id string) sqlitevfs.Option {
		w, err := sqlitevfs.NewLockerWriter(l, id, time.Minute)
		if err != nil {
			t.Fatalf("NewLockerWriter: %v", err)
		}
		return sqlitevfs.WithWriterLock(w)
	}
	first := newFile(t, b, name, writer("first"))
	second := newFile(t, b, name, writer("second"))
	if rc := first.lock(sqlite3.SQLITE_LOCK_RESERVED); rc != sqlite3.SQLITE_OK {
		t.Fatalf("first lock rc = %d", rc)
	}
	if rc := second.lock(sqlite3.SQLITE_LOCK_RESERVED); rc != sqlite3.SQLITE_BUSY {
		t.Fatalf("second RESERVED rc = %d, want SQLITE_BUSY", rc)
	}
	if rc := second.lock(sqlite3.SQLITE_LOCK_SHARED); rc != sqlite3.SQLITE_OK {
		t.Fatalf("second SHARED rc = %d", rc)
	}
	if rc := second.write(make([]byte, 8), 4096); rc != sqlite3.SQLITE_BUSY {
		t.Fatalf("second write rc = %d, want SQLITE_BUSY", rc)
	}
	if rc := second.truncate(0); rc != sqlite3.SQLITE_BUSY {
		t.Fatalf("second truncate rc = %d, want SQLITE_BUSY", rc)
	}
}

func TestDBFile_ShortAndFailedReads(t *testing.T) {
	f := newFile(t, newBackend(t), "short.db")
	if rc := f.lock(sqlite3.SQLITE_LOCK_SHARED); rc != sqlite3.SQLITE_OK {
		t.Fatalf("lock rc = %d", rc)
	}
	p := []byte{9, 9, 9}
	if rc := f.read(p, 1<<20); rc != sqlite3.SQLITE_IOERR_SHORT_READ {
		t.Fatalf("read past the end rc = %d, want SQLITE_IOERR_SHORT_READ", rc)
	}
	if p[0] != 0 {
		t.Fatalf("short read left %v, want zeros", p)
	}
	if rc := f.read(make([]byte, 4), -1); rc != sqlite3.SQLITE_IOERR_READ {
		t.Fatalf("read at a negative offset rc = %d, want SQLITE_IOERR_READ", rc)
	}
}

func TestDBFile_WriteTruncateSyncPaths(t *testing.T) {
	f := newFile(t, newBackend(t), "wts.db")
	if rc := f.lock(sqlite3.SQLITE_LOCK_RESERVED); rc != sqlite3.SQLITE_OK {
		t.Fatalf("lock rc = %d", rc)
	}
	if rc := f.write([]byte{1}, -1); rc != sqlite3.SQLITE_IOERR_WRITE {
		t.Fatalf("write at a negative offset rc = %d, want SQLITE_IOERR_WRITE", rc)
	}
	if rc := f.truncate(-1); rc != sqlite3.SQLITE_IOERR_TRUNCATE {
		t.Fatalf("truncate to a negative size rc = %d, want SQLITE_IOERR_TRUNCATE", rc)
	}
	for _, off := range []int64{0, 4096} {
		if rc := f.write(make([]byte, 4096), off); rc != sqlite3.SQLITE_OK {
			t.Fatalf("write at %d rc = %d", off, rc)
		}
	}
	f.dirty = false
	if rc := f.truncate(8192); rc != sqlite3.SQLITE_OK || f.dirty {
		t.Fatalf("truncate to the same size rc = %d dirty = %v, want OK and clean", rc, f.dirty)
	}
	if rc := f.truncate(4096); rc != sqlite3.SQLITE_OK || !f.dirty {
		t.Fatalf("truncate shrinking rc = %d dirty = %v, want OK and dirty", rc, f.dirty)
	}
	if rc := f.sync(); rc != sqlite3.SQLITE_OK {
		t.Fatalf("sync rc = %d", rc)
	}
	if got, _ := f.size(); got != 4096 {
		t.Fatalf("size after sync = %d, want 4096", got)
	}

	f.failed = sqlite3.SQLITE_FULL
	if rc := f.write([]byte{1}, 8192); rc != sqlite3.SQLITE_OK {
		t.Fatalf("write after a failed commit rc = %d, want SQLITE_OK", rc)
	}
	if rc := f.truncate(0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("truncate after a failed commit rc = %d, want SQLITE_OK", rc)
	}
	if rc := f.sync(); rc != sqlite3.SQLITE_FULL {
		t.Fatalf("sync after a failed commit rc = %d, want SQLITE_FULL", rc)
	}
	if rc := f.unlock(sqlite3.SQLITE_LOCK_NONE); rc != sqlite3.SQLITE_OK || f.failed != sqlite3.SQLITE_OK {
		t.Fatalf("unlock rc = %d failed = %d, want OK and cleared", rc, f.failed)
	}
}

func TestDBFile_ControlOps(t *testing.T) {
	f := newFile(t, newBackend(t), "control.db")
	if rc := f.control(sqlite3.SQLITE_FCNTL_PRAGMA, 0); rc != sqlite3.SQLITE_NOTFOUND {
		t.Fatalf("pragma with no arg rc = %d, want SQLITE_NOTFOUND", rc)
	}
	name := cstr(t, "synchronous")
	arg := [3]uintptr{0, name, 0}
	if rc := f.control(sqlite3.SQLITE_FCNTL_PRAGMA, uintptr(unsafe.Pointer(&arg))); rc != sqlite3.SQLITE_NOTFOUND {
		t.Fatalf("pragma synchronous with no value rc = %d, want SQLITE_NOTFOUND", rc)
	}
	if rc := f.control(sqlite3.SQLITE_FCNTL_BEGIN_ATOMIC_WRITE, 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("begin atomic write rc = %d", rc)
	}
	if rc := f.control(sqlite3.SQLITE_FCNTL_COMMIT_ATOMIC_WRITE, 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("commit atomic write with nothing written rc = %d", rc)
	}
	if rc := f.lock(sqlite3.SQLITE_LOCK_RESERVED); rc != sqlite3.SQLITE_OK {
		t.Fatalf("lock rc = %d", rc)
	}
	if rc := f.write(make([]byte, 4096), 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("write rc = %d", rc)
	}
	if rc := f.control(sqlite3.SQLITE_FCNTL_COMMIT_ATOMIC_WRITE, 0); rc != sqlite3.SQLITE_OK || f.dirty {
		t.Fatalf("commit atomic write rc = %d dirty = %v", rc, f.dirty)
	}
	if rc := f.control(sqlite3.SQLITE_FCNTL_ROLLBACK_ATOMIC_WRITE, 0); rc != sqlite3.SQLITE_OK || f.txn != nil {
		t.Fatalf("rollback atomic write rc = %d txn = %v", rc, f.txn)
	}
	if rc := f.control(-1, 0); rc != sqlite3.SQLITE_NOTFOUND {
		t.Fatalf("unknown op rc = %d, want SQLITE_NOTFOUND", rc)
	}
}

func TestMemFile_Operations(t *testing.T) {
	m := &memFile{}
	if rc := m.write([]byte("abcdef"), 2); rc != sqlite3.SQLITE_OK {
		t.Fatalf("write rc = %d", rc)
	}
	if rc := m.truncate(100); rc != sqlite3.SQLITE_OK {
		t.Fatalf("growing truncate rc = %d", rc)
	}
	if n, _ := m.size(); n != 8 {
		t.Fatalf("size after a growing truncate = %d, want 8 (a journal never grows on truncate)", n)
	}
	if rc := m.truncate(4); rc != sqlite3.SQLITE_OK {
		t.Fatalf("truncate rc = %d", rc)
	}
	p := make([]byte, 4)
	if rc := m.read(p, 0); rc != sqlite3.SQLITE_OK || string(p[2:]) != "ab" {
		t.Fatalf("read rc = %d p = %q", rc, p)
	}
	if rc := m.read(p, 2); rc != sqlite3.SQLITE_IOERR_SHORT_READ {
		t.Fatalf("short read rc = %d", rc)
	}
	if rc := m.lock(sqlite3.SQLITE_LOCK_EXCLUSIVE); rc != sqlite3.SQLITE_OK {
		t.Fatalf("lock rc = %d", rc)
	}
	if rc := m.unlock(sqlite3.SQLITE_LOCK_NONE); rc != sqlite3.SQLITE_OK {
		t.Fatalf("unlock rc = %d", rc)
	}
	if rc := m.control(sqlite3.SQLITE_FCNTL_PRAGMA, 0); rc != sqlite3.SQLITE_NOTFOUND {
		t.Fatalf("control rc = %d", rc)
	}
}

func TestResultCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int32
	}{
		{nil, sqlite3.SQLITE_OK},
		{fmt.Errorf("x: %w", sqlitevfs.ErrBusy), sqlite3.SQLITE_BUSY},
		{fmt.Errorf("x: %w", sqlitevfs.ErrSnapshotChanged), sqlite3.SQLITE_BUSY_SNAPSHOT},
		{fmt.Errorf("x: %w", sqlitevfs.ErrFull), sqlite3.SQLITE_FULL},
		{errors.New("other"), sqlite3.SQLITE_IOERR},
	} {
		if got := resultCode(tc.err, sqlite3.SQLITE_IOERR); got != tc.want {
			t.Errorf("resultCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
	if err := resultError("call", 5); err == nil || err.Error() != "moderncvfs: call: SQLite result code 5" {
		t.Fatalf("resultError = %v", err)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		flags     int32
		anonymous bool
		want      fileKind
	}{
		{sqlite3.SQLITE_OPEN_WAL, false, kindRefused},
		{sqlite3.SQLITE_OPEN_SUPER_JOURNAL, false, kindRefused},
		{sqlite3.SQLITE_OPEN_MAIN_DB, true, kindTemp},
		{sqlite3.SQLITE_OPEN_MAIN_DB, false, kindMain},
		{sqlite3.SQLITE_OPEN_MAIN_JOURNAL, false, kindJournal},
		{sqlite3.SQLITE_OPEN_TEMP_JOURNAL, false, kindTemp},
	} {
		if got := classify(tc.flags, tc.anonymous); got != tc.want {
			t.Errorf("classify(%#x, %v) = %d, want %d", tc.flags, tc.anonymous, got, tc.want)
		}
	}
}

func TestABI_UnknownHandleAndRefusedOpen(t *testing.T) {
	file := [2]uintptr{0, ^uintptr(0)}
	pFile := uintptr(unsafe.Pointer(&file[0]))
	buf := make([]byte, 4)
	pBuf := uintptr(unsafe.Pointer(&buf[0]))
	if rc := xRead(nil, pFile, pBuf, 4, 0); rc != sqlite3.SQLITE_IOERR_READ {
		t.Errorf("xRead rc = %d", rc)
	}
	if rc := xWrite(nil, pFile, pBuf, 4, 0); rc != sqlite3.SQLITE_IOERR_WRITE {
		t.Errorf("xWrite rc = %d", rc)
	}
	if rc := xTruncate(nil, pFile, 0); rc != sqlite3.SQLITE_IOERR_TRUNCATE {
		t.Errorf("xTruncate rc = %d", rc)
	}
	if rc := xSync(nil, pFile, 0); rc != sqlite3.SQLITE_IOERR_FSYNC {
		t.Errorf("xSync rc = %d", rc)
	}
	if rc := xFileSize(nil, pFile, 0); rc != sqlite3.SQLITE_IOERR_FSTAT {
		t.Errorf("xFileSize rc = %d", rc)
	}
	if rc := xLock(nil, pFile, sqlite3.SQLITE_LOCK_SHARED); rc != sqlite3.SQLITE_IOERR_LOCK {
		t.Errorf("xLock rc = %d", rc)
	}
	if rc := xUnlock(nil, pFile, sqlite3.SQLITE_LOCK_NONE); rc != sqlite3.SQLITE_IOERR_UNLOCK {
		t.Errorf("xUnlock rc = %d", rc)
	}
	if rc := xFileControl(nil, pFile, sqlite3.SQLITE_FCNTL_PRAGMA, 0); rc != sqlite3.SQLITE_NOTFOUND {
		t.Errorf("xFileControl rc = %d", rc)
	}
	if rc := xClose(nil, pFile); rc != sqlite3.SQLITE_OK {
		t.Errorf("xClose rc = %d", rc)
	}
	var reserved int32 = 7
	if rc := xCheckReservedLock(nil, pFile, uintptr(unsafe.Pointer(&reserved))); rc != sqlite3.SQLITE_OK || reserved != 0 {
		t.Errorf("xCheckReservedLock rc = %d reserved = %d", rc, reserved)
	}
	zName := cstr(t, "long-name.db")
	if rc := xFullPathname(nil, 0, zName, 4, pBuf); rc != sqlite3.SQLITE_CANTOPEN {
		t.Errorf("xFullPathname into a short buffer rc = %d, want SQLITE_CANTOPEN", rc)
	}
	if rc := xOpen(nil, 0, zName, pFile, sqlite3.SQLITE_OPEN_WAL, 0); rc != sqlite3.SQLITE_CANTOPEN {
		t.Errorf("xOpen of a WAL rc = %d, want SQLITE_CANTOPEN", rc)
	}
	if got := cbytes(0, 4); got != nil {
		t.Errorf("cbytes(0) = %v, want nil", got)
	}
}

func TestABI_JournalHandleRoundTrip(t *testing.T) {
	var file [2]uintptr
	pFile := uintptr(unsafe.Pointer(&file[0]))
	zName := cstr(t, "j.db-journal")
	var out int32
	if rc := xOpen(nil, 0, zName, pFile, sqlite3.SQLITE_OPEN_MAIN_JOURNAL, uintptr(unsafe.Pointer(&out))); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xOpen journal rc = %d", rc)
	}
	if out != sqlite3.SQLITE_OPEN_MAIN_JOURNAL {
		t.Fatalf("out flags = %#x", out)
	}
	data := []byte("journal!")
	if rc := xWrite(nil, pFile, uintptr(unsafe.Pointer(&data[0])), int32(len(data)), 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xWrite rc = %d", rc)
	}
	var size int64
	if rc := xFileSize(nil, pFile, uintptr(unsafe.Pointer(&size))); rc != sqlite3.SQLITE_OK || size != int64(len(data)) {
		t.Fatalf("xFileSize rc = %d size = %d", rc, size)
	}
	if rc := xFileSize(nil, pFile, 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xFileSize with no out pointer rc = %d", rc)
	}
	if rc := xTruncate(nil, pFile, 4); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xTruncate rc = %d", rc)
	}
	if rc := xSync(nil, pFile, 0); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xSync rc = %d", rc)
	}
	if rc := xLock(nil, pFile, sqlite3.SQLITE_LOCK_SHARED); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xLock rc = %d", rc)
	}
	if rc := xUnlock(nil, pFile, sqlite3.SQLITE_LOCK_NONE); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xUnlock rc = %d", rc)
	}
	if rc := xFileControl(nil, pFile, -1, 0); rc != sqlite3.SQLITE_NOTFOUND {
		t.Fatalf("xFileControl rc = %d", rc)
	}
	if rc := xClose(nil, pFile); rc != sqlite3.SQLITE_OK {
		t.Fatalf("xClose rc = %d", rc)
	}
	if getHandle(file[1]) != nil {
		t.Fatal("handle still registered after xClose")
	}
}

func TestVFS_UnknownDatabaseCannotOpen(t *testing.T) {
	setup(t, "known.db", plainOpener(newBackend(t), "known.db"))
	db := openDB(t, "unknown.db", "")
	if _, err := db.Exec("create table t(a)"); code(err) != sqlite3.SQLITE_CANTOPEN {
		t.Fatalf("open of a database the opener refuses err = %v (code %d), want SQLITE_CANTOPEN", err, code(err))
	}
}

func TestVFS_TempFilesUseTheOSVFS(t *testing.T) {
	const name = "tempfile.db"
	setup(t, name, plainOpener(newBackend(t), name))
	db := openDB(t, name, "")
	mustExec(t, db, "pragma temp_store=file")
	mustExec(t, db, "pragma cache_size=2")
	mustExec(t, db, "create temp table scratch(a integer primary key, b blob)")
	mustExec(t, db, "with recursive s(i) as (select 1 union all select i + 1 from s where i < 200) "+
		"insert into scratch(b) select randomblob(2000) from s")
	if n := count(t, db, "temp.scratch"); n != 200 {
		t.Fatalf("temp rows = %d, want 200", n)
	}
}

func TestRegister_NilAndUnregisterCycle(t *testing.T) {
	setup(t, "cycle.db", plainOpener(newBackend(t), "cycle.db"))
	if err := Register(nil); !errors.Is(err, errNilOpener) {
		t.Fatalf("Register(nil) = %v, want errNilOpener", err)
	}
	reg.mu.Lock()
	saved := reg.open
	reg.mu.Unlock()
	if err := Unregister(); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	t.Cleanup(func() {
		if err := Register(saved); err != nil && !errors.Is(err, ErrRegistered) {
			t.Errorf("re-Register: %v", err)
		}
	})
	if err := Unregister(); err != nil {
		t.Fatalf("second Unregister: %v", err)
	}
	if f, rc := openMain("cycle.db", url.Values{}); f != nil || rc != sqlite3.SQLITE_CANTOPEN {
		t.Fatalf("openMain with no opener = %v, %d, want nil, SQLITE_CANTOPEN", f, rc)
	}
	if err := Register(saved); err != nil {
		t.Fatalf("re-Register: %v", err)
	}
}
