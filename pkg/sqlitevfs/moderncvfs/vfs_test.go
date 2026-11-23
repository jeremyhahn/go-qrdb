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
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// openFn builds the Store one database open gets, from its URI parameters.
type openFn func(params url.Values) (*sqlitevfs.Store, error)

var (
	registerOnce sync.Once
	errRegister  error
	// openers maps a database name to the openFn of the test that owns it:
	// VFS qrdb is registered once per process, so its one Opener dispatches.
	openers sync.Map
)

// setup registers VFS qrdb once and routes opens of the database name to
// open for the test's lifetime.
func setup(t *testing.T, name string, open openFn) {
	t.Helper()
	registerOnce.Do(func() {
		errRegister = Register(func(_ context.Context, name string, params url.Values) (*sqlitevfs.Store, error) {
			v, ok := openers.Load(name)
			if !ok {
				return nil, fmt.Errorf("no store for %q", name)
			}
			return v.(openFn)(params)
		})
	})
	if errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	openers.Store(name, open)
	t.Cleanup(func() { openers.Delete(name) })
}

func newBackend(t *testing.T) *sqlitevfs.MVCCBackend {
	t.Helper()
	ms, err := mvcc.New(memory.NewStorage())
	if err != nil {
		t.Fatalf("mvcc.New: %v", err)
	}
	b, err := sqlitevfs.NewMVCCBackend(ms)
	if err != nil {
		t.Fatalf("NewMVCCBackend: %v", err)
	}
	return b
}

// plainOpener opens a Store with opts over b for every open.
func plainOpener(b sqlitevfs.Backend, name string, opts ...sqlitevfs.Option) openFn {
	return func(url.Values) (*sqlitevfs.Store, error) {
		return sqlitevfs.NewStore(b, name, opts...)
	}
}

// lockedOpener gives every open its own holder of the writer lock over l.
func lockedOpener(b sqlitevfs.Backend, name string, l lock.Locker) openFn {
	var n atomic.Int64
	return func(url.Values) (*sqlitevfs.Store, error) {
		w, err := sqlitevfs.NewLockerWriter(l, fmt.Sprintf("conn-%d", n.Add(1)), time.Minute)
		if err != nil {
			return nil, err
		}
		return sqlitevfs.NewStore(b, name, sqlitevfs.WithWriterLock(w))
	}
}

// openDB opens name through VFS qrdb as a pool of one connection.
func openDB(t *testing.T, name, extra string) *sql.DB {
	t.Helper()
	db, err := sql.Open(DriverName, "file:"+name+"?vfs="+VFSName+extra)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("select count(*) from " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func revision(t *testing.T, b sqlitevfs.Backend) int64 {
	t.Helper()
	rev, err := b.Revision(context.Background())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	return rev
}

// code is the SQLite result code (extended where SQLite gives one) of err.
func code(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return -1
}

func TestVFS_CreateInsertSelect(t *testing.T) {
	b := newBackend(t)
	setup(t, "create.db", plainOpener(b, "create.db"))
	db := openDB(t, "create.db", "")
	mustExec(t, db, "create table t(a integer primary key, b text)")
	for i := 1; i <= 3; i++ {
		mustExec(t, db, "insert into t(a, b) values(?, ?)", i, fmt.Sprintf("row-%d", i))
	}
	var sum int
	var last string
	if err := db.QueryRow("select sum(a), max(b) from t").Scan(&sum, &last); err != nil {
		t.Fatalf("select: %v", err)
	}
	if sum != 6 || last != "row-3" {
		t.Fatalf("sum, max = %d, %q, want 6, row-3", sum, last)
	}
}

func TestVFS_RollbackLeavesDatabaseUnchanged(t *testing.T) {
	b := newBackend(t)
	setup(t, "rollback.db", plainOpener(b, "rollback.db"))
	db := openDB(t, "rollback.db", "")
	mustExec(t, db, "create table t(a integer primary key, b blob)")
	mustExec(t, db, "insert into t(b) values(randomblob(100))")
	before := revision(t, b)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < 50; i++ {
		mustExec(t, tx, "insert into t(b) values(randomblob(1000))")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := count(t, db, "t"); n != 1 {
		t.Fatalf("rows after rollback = %d, want 1", n)
	}
	if after := revision(t, b); after != before {
		t.Fatalf("revision moved from %d to %d across a rollback", before, after)
	}
}

// TestVFS_SharedAfterCommitReadsOwnCommit: a connection that keeps SHARED
// after its write transaction (an open read statement) keeps reading the
// snapshot of its own commit, so a commit by another connection in the
// meantime never mixes into its page cache (o7).
func TestVFS_SharedAfterCommitReadsOwnCommit(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	setup(t, "shared.db", plainOpener(b, "shared.db"))
	db1 := openDB(t, "shared.db", "")
	mustExec(t, db1, "create table a(id integer primary key, n integer, v blob)")
	mustExec(t, db1, "create table b(id integer primary key)")
	mustExec(t, db1, "with recursive s(i) as (select 1 union all select i + 1 from s where i < 2000) "+
		"insert into a(n, v) select i, randomblob(400) from s")

	conn, err := db1.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "pragma cache_size=10"); err != nil {
		t.Fatalf("cache_size: %v", err)
	}
	rows, err := conn.QueryContext(ctx, "select id from a")
	if err != nil {
		t.Fatalf("query a: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatalf("no first row: %v", rows.Err())
	}
	if _, err := conn.ExecContext(ctx, "insert into b(id) values(1)"); err != nil {
		t.Fatalf("insert into b: %v", err)
	}

	db2 := openDB(t, "shared.db", "")
	mustExec(t, db2, "delete from a where id % 2 = 0")
	mustExec(t, db2, "update a set n = n + 1000000, v = randomblob(300)")

	var n, sum int64
	if err := conn.QueryRowContext(ctx, "select count(*), sum(n) from a").Scan(&n, &sum); err != nil {
		t.Fatalf("count a under SHARED: %v", err)
	}
	if n != 2000 || sum != 2000*2001/2 {
		t.Fatalf("table a under SHARED after own commit = %d rows, sum %d; want 2000, %d", n, sum, 2000*2001/2)
	}
	var check string
	if err := conn.QueryRowContext(ctx, "pragma integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check under SHARED = %q, %v", check, err)
	}
	var nb int
	if err := conn.QueryRowContext(ctx, "select count(*) from b").Scan(&nb); err != nil || nb != 1 {
		t.Fatalf("table b under SHARED = %d, %v; want its own commit's row", nb, err)
	}
	seen := 1
	for rows.Next() {
		seen++
	}
	if err := rows.Err(); err != nil || seen != 2000 {
		t.Fatalf("open read statement saw %d rows, %v; want 2000", seen, err)
	}
	_ = rows.Close()

	if got := count(t, db2, "a"); got != 1000 {
		t.Fatalf("table a after the other commit = %d rows, want 1000", got)
	}
}

// TestVFS_RollbackAfterCacheSpillWritesNoRevision: a ROLLBACK after the
// page cache spilled into the database file plays the journal back and
// syncs; the restored pages equal the snapshot, so no revision is
// written (o8).
func TestVFS_RollbackAfterCacheSpillWritesNoRevision(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	setup(t, "spill.db", plainOpener(b, "spill.db"))
	db := openDB(t, "spill.db", "")
	mustExec(t, db, "create table t(a integer primary key, b blob)")
	mustExec(t, db, "insert into t(b) values(randomblob(100))")
	before := revision(t, b)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "pragma cache_size=10"); err != nil {
		t.Fatalf("cache_size: %v", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < 200; i++ {
		mustExec(t, tx, "insert into t(b) values(randomblob(1000))")
	}
	if spilled := revision(t, b); spilled != before {
		t.Fatalf("revision moved from %d to %d before the rollback", before, spilled)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "select count(*) from t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after rollback = %d, %v; want 1", n, err)
	}
	if after := revision(t, b); after != before {
		t.Fatalf("revision moved from %d to %d across a rollback after a cache spill", before, after)
	}
}

func TestVFS_ReopenReadsCommittedRows(t *testing.T) {
	b := newBackend(t)
	setup(t, "reopen.db", plainOpener(b, "reopen.db"))
	db := openDB(t, "reopen.db", "")
	mustExec(t, db, "create table t(a integer primary key, b text)")
	mustExec(t, db, "insert into t(b) values('kept'), ('also kept')")
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again := openDB(t, "reopen.db", "")
	if n := count(t, again, "t"); n != 2 {
		t.Fatalf("rows after reopen = %d, want 2", n)
	}
}

func TestVFS_CommitIsOneRevision(t *testing.T) {
	b := newBackend(t)
	const name = "onerev.db"
	setup(t, name, plainOpener(b, name))
	db := openDB(t, name, "")
	mustExec(t, db, "create table t(a integer primary key, b blob)")
	before := revision(t, b)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < 100; i++ {
		mustExec(t, tx, "insert into t(b) values(randomblob(500))")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	after := revision(t, b)
	if after != before+1 {
		t.Fatalf("one write transaction moved the revision from %d to %d, want +1", before, after)
	}
	metaKey, err := sqlitevfs.MetaKey(name)
	if err != nil {
		t.Fatalf("MetaKey: %v", err)
	}
	pages, err := sqlitevfs.PagePrefix(name)
	if err != nil {
		t.Fatalf("PagePrefix: %v", err)
	}
	root := commonPrefix(metaKey, pages)
	keys, err := b.Keys(context.Background(), root, after)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	for _, k := range keys {
		if bytes.Equal(k, metaKey) {
			continue
		}
		if _, err := sqlitevfs.ParsePageKey(name, k); err != nil {
			t.Fatalf("key %q is neither the meta record nor a page (a journal?)", k)
		}
	}
}

func commonPrefix(a, b []byte) []byte {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

func TestVFS_JournalModeWALNotEntered(t *testing.T) {
	b := newBackend(t)
	setup(t, "wal.db", plainOpener(b, "wal.db"))
	db := openDB(t, "wal.db", "")
	mustExec(t, db, "create table t(a)")
	var mode string
	if err := db.QueryRow("pragma journal_mode=WAL").Scan(&mode); err != nil {
		t.Fatalf("pragma journal_mode=WAL: %v", err)
	}
	if strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=WAL answered %q: WAL was entered", mode)
	}
	var now string
	if err := db.QueryRow("pragma journal_mode").Scan(&now); err != nil {
		t.Fatalf("pragma journal_mode: %v", err)
	}
	if now != mode {
		t.Fatalf("journal_mode = %q after the WAL pragma answered %q", now, mode)
	}
	mustExec(t, db, "insert into t values(1)")
}

func TestVFS_BusyOnWriterContention(t *testing.T) {
	b := newBackend(t)
	const name = "busy.db"
	setup(t, name, lockedOpener(b, name, newMemLocker()))
	first := openDB(t, name, "")
	second := openDB(t, name, "")
	mustExec(t, first, "create table t(a)")
	if n := count(t, second, "t"); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	tx, err := first.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	mustExec(t, tx, "insert into t values(1)")
	_, err = second.Exec("insert into t values(2)")
	if c := code(err); c != sqlite3.SQLITE_BUSY {
		t.Fatalf("second writer err = %v (code %d), want SQLITE_BUSY", err, c)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	mustExec(t, second, "insert into t values(2)")
	if n := count(t, first, "t"); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestVFS_SnapshotChangedIsBusySnapshot(t *testing.T) {
	b := newBackend(t)
	const name = "snapshot.db"
	setup(t, name, lockedOpener(b, name, newMemLocker()))
	db := openDB(t, name, "")
	mustExec(t, db, "create table t(a)")
	mustExec(t, db, "insert into t values(1)")
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow("select count(*) from t").Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}
	restore, err := sqlitevfs.NewStore(b, name)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := restore.BumpGeneration(context.Background()); err != nil {
		t.Fatalf("BumpGeneration: %v", err)
	}
	_, err = tx.Exec("insert into t values(2)")
	if c := code(err); c != sqlite3.SQLITE_BUSY_SNAPSHOT {
		t.Fatalf("write after a restore err = %v (code %d), want SQLITE_BUSY_SNAPSHOT", err, c)
	}
}

// fullBackend answers ErrFull from Commit and Publish while full is set.
type fullBackend struct {
	*sqlitevfs.MVCCBackend
	full atomic.Bool
}

func (f *fullBackend) Commit(ctx context.Context, reads []sqlitevfs.ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if f.full.Load() {
		return 0, sqlitevfs.ErrFull
	}
	return f.MVCCBackend.Commit(ctx, reads, puts, deletes)
}

func (f *fullBackend) Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []sqlitevfs.ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if f.full.Load() {
		return 0, sqlitevfs.ErrFull
	}
	return f.MVCCBackend.Publish(ctx, src, dst, expectedKeys, reads, puts, deletes)
}

func TestVFS_FullIsSQLiteFull(t *testing.T) {
	b := &fullBackend{MVCCBackend: newBackend(t)}
	const name = "full.db"
	setup(t, name, plainOpener(b, name))
	db := openDB(t, name, "")
	mustExec(t, db, "create table t(a)")
	mustExec(t, db, "insert into t values(1)")
	before := revision(t, b)
	b.full.Store(true)
	_, err := db.Exec("insert into t values(2)")
	if c := code(err); c != sqlite3.SQLITE_FULL {
		t.Fatalf("write past the quota err = %v (code %d), want SQLITE_FULL", err, c)
	}
	b.full.Store(false)
	if n := count(t, db, "t"); n != 1 {
		t.Fatalf("rows after SQLITE_FULL = %d, want 1", n)
	}
	if after := revision(t, b); after != before {
		t.Fatalf("revision moved from %d to %d across a failed commit", before, after)
	}
	mustExec(t, db, "insert into t values(3)")
}

func TestVFS_VacuumAboveStageThreshold(t *testing.T) {
	b := newBackend(t)
	const name = "vacuum.db"
	setup(t, name, plainOpener(b, name, sqlitevfs.WithStageThreshold(4096)))
	db := openDB(t, name, "")
	mustExec(t, db, "create table t(a integer primary key, b blob)")
	for i := 0; i < 40; i++ {
		mustExec(t, db, "insert into t(b) values(randomblob(2000))")
	}
	mustExec(t, db, "delete from t where a % 2 = 0")
	before := revision(t, b)
	mustExec(t, db, "vacuum")
	if after := revision(t, b); after <= before {
		t.Fatalf("VACUUM committed nothing: revision %d, was %d", after, before)
	}
	if n := count(t, db, "t"); n != 20 {
		t.Fatalf("rows after VACUUM = %d, want 20", n)
	}
	var ok string
	if err := db.QueryRow("pragma integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check = %q, %v", ok, err)
	}
	stages, err := sqlitevfs.StageRoot(name)
	if err != nil {
		t.Fatalf("StageRoot: %v", err)
	}
	keys, err := b.Keys(context.Background(), stages, revision(t, b))
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("%d stage keys live after the staged VACUUM commit", len(keys))
	}
}

func TestVFS_OptimisticDisjointWriters(t *testing.T) {
	b := newBackend(t)
	const name = "optimistic.db"
	setup(t, name, func(params url.Values) (*sqlitevfs.Store, error) {
		if params.Get(ConcurrencyParam) != "optimistic" {
			return nil, fmt.Errorf("%s = %q, want optimistic", ConcurrencyParam, params.Get(ConcurrencyParam))
		}
		return sqlitevfs.NewStore(b, name, sqlitevfs.WithOptimistic())
	})
	const dsn = "&" + ConcurrencyParam + "=optimistic"
	setupDB := openDB(t, name, dsn)
	mustExec(t, setupDB, "create table t1(a)")
	mustExec(t, setupDB, "create table t2(a)")
	mustExec(t, setupDB, "insert into t1 values(0)")
	mustExec(t, setupDB, "insert into t2 values(0)")
	first := openDB(t, name, dsn)
	second := openDB(t, name, dsn)
	tx1, err := first.Begin()
	if err != nil {
		t.Fatalf("begin first: %v", err)
	}
	tx2, err := second.Begin()
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	mustExec(t, tx1, "insert into t1 values(1)")
	mustExec(t, tx2, "insert into t2 values(2)")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("second commit of a disjoint table: %v (code %d)", err, code(err))
	}
	if n1, n2 := count(t, setupDB, "t1"), count(t, setupDB, "t2"); n1 != 2 || n2 != 2 {
		t.Fatalf("rows t1, t2 = %d, %d, want 2, 2", n1, n2)
	}
}

func TestVFS_RegisterTwiceRefused(t *testing.T) {
	setup(t, "twice.db", plainOpener(newBackend(t), "twice.db"))
	err := Register(func(context.Context, string, url.Values) (*sqlitevfs.Store, error) {
		return nil, errors.New("unused")
	})
	if !errors.Is(err, ErrRegistered) {
		t.Fatalf("second Register err = %v, want ErrRegistered", err)
	}
}

// memLocker is an in-memory lock.Locker over a mutex-guarded map, as
// pkg/sqlitevfs's writer tests use: TryLock on a held key fails at once.
type memLocker struct {
	mu    sync.Mutex
	locks map[string]lock.Lock
	next  int64
}

var _ lock.Locker = (*memLocker)(nil)

func newMemLocker() *memLocker {
	return &memLocker{locks: make(map[string]lock.Lock)}
}

func (m *memLocker) Lock(ctx context.Context, opts lock.LockOptions) (*lock.Lock, error) {
	return m.TryLock(ctx, opts)
}

func (m *memLocker) TryLock(_ context.Context, opts lock.LockOptions) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, ok := m.locks[opts.Key]; ok {
		if !opts.Reentrant || held.HolderID != opts.HolderID {
			return nil, &lock.LockHeldError{Key: opts.Key, HolderID: held.HolderID}
		}
		held.AcquireCount++
		m.locks[opts.Key] = held
		out := held
		return &out, nil
	}
	m.next++
	l := lock.Lock{
		Key:          opts.Key,
		FencingToken: m.next,
		HolderID:     opts.HolderID,
		Reentrant:    opts.Reentrant,
		AcquireCount: 1,
	}
	m.locks[opts.Key] = l
	return &l, nil
}

func (m *memLocker) holding(key string, token int64) (lock.Lock, error) {
	l, ok := m.locks[key]
	if !ok {
		return lock.Lock{}, &lock.LockNotFoundError{Key: key}
	}
	if l.FencingToken != token {
		return lock.Lock{}, &lock.NotLockHolderError{Key: key, ExpectedToken: token, ActualToken: l.FencingToken}
	}
	return l, nil
}

func (m *memLocker) Unlock(_ context.Context, key string, token int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.holding(key, token); err != nil {
		return err
	}
	delete(m.locks, key)
	return nil
}

func (m *memLocker) Extend(_ context.Context, key string, token int64) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.holding(key, token)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (m *memLocker) Status(_ context.Context, key string) (*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[key]
	if !ok {
		return nil, nil
	}
	return &l, nil
}

func (m *memLocker) List(_ context.Context, prefix string) ([]*lock.Lock, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*lock.Lock
	for key, l := range m.locks {
		if strings.HasPrefix(key, prefix) {
			l := l
			out = append(out, &l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
