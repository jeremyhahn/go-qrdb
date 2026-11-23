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

package sqlitevfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
)

// Backend is the versioned key-value store the page store reads and commits
// through.
type Backend interface {
	// Every method takes the caller's context first: ctx bounds that one
	// call, and a done ctx fails the call before it reads or writes.

	// Revision returns the newest committed revision; err is a failed
	// read.
	Revision(ctx context.Context) (int64, error)
	// GetAt returns key's value at revision rev. A key with no live value
	// at rev is found=false with a nil error; err is a failed read.
	GetAt(ctx context.Context, key []byte, rev int64) (value []byte, found bool, err error)
	// Commit applies every put and every delete at one new revision, or
	// none of them, and returns that revision. Every read check is
	// evaluated atomically with the write: one that fails is
	// ErrSnapshotChanged and writes nothing.
	Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (rev int64, err error)
	// Publish, at one new revision, puts dst + (key without src) for
	// every key live under prefix src with that key's newest committed
	// value, deletes those source keys, and applies puts and deletes; or
	// does none of it. A live count under src other than expectedKeys is
	// ErrPublishConflict and writes nothing; empty or overlapping src and
	// dst, or expectedKeys < 1, is ErrInvalidPublish. The read checks are
	// Commit's, evaluated atomically with the move: a failed one is
	// ErrSnapshotChanged and writes nothing.
	Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (rev int64, err error)
	// Keys returns every key with a live value at revision rev that
	// starts with prefix, in key order.
	Keys(ctx context.Context, prefix []byte, rev int64) ([][]byte, error)
}

// ReadCheck is one entry of a commit's read set: the commit applies only if
// Key's live state is unchanged since revision Rev. Found is whether Key had
// a live value at Rev: when true, Key must still have a live value whose mod
// revision is at most Rev; when false, Key must still have no live value (an
// absent key stays absent).
type ReadCheck struct {
	Key   []byte
	Rev   int64
	Found bool
}

// Store is the page store of one SQLite database file over a Backend.
type Store struct {
	backend Backend
	db      string
	metaKey []byte

	// stageThreshold is the dirty-page byte total above which a commit
	// goes through staging and one publish (Q2 A); stageChunkBytes bounds
	// the page bytes of each staging Backend.Commit.
	stageThreshold  int
	stageChunkBytes int

	// writer, when set, serializes this database's writers (Q3 A) through
	// the lock named lockKey; nil is the no-lock (optimistic/local) Store.
	writer  WriterLock
	lockKey string

	// optimistic is the opt-in optimistic writer mode (Q3 A): no writer
	// lock, and every commit carries the transaction's read set, so a
	// commit that read a page another writer changed since the snapshot is
	// ErrSnapshotChanged (the binding's SQLITE_BUSY_SNAPSHOT).
	optimistic bool

	// mu guards seenGen and seen: the meta generation of this Store's last
	// snapshot pin, and whether it has pinned one.
	mu      sync.Mutex
	seenGen uint64
	seen    bool
}

const (
	// DefaultStageThreshold is the default dirty-page byte total above
	// which a commit stages its pages before one publish.
	DefaultStageThreshold = 4 << 20
	// DefaultStageChunkBytes is the default page bytes per staging commit:
	// half of go-quicraft's 8 MiB proto.MaxEntrySize per Raft entry, so a
	// staging write with its key overhead fits one entry.
	DefaultStageChunkBytes = 4 << 20
)

// Option configures a Store built by NewStore.
type Option func(*Store) error

// WithStageThreshold sets the dirty-page byte total above which a commit
// goes through staging and one publish. A non-positive value is refused
// with ErrInvalidOption.
func WithStageThreshold(bytes int) Option {
	return func(s *Store) error {
		if bytes <= 0 {
			return fmt.Errorf("%w: stage threshold %d", ErrInvalidOption, bytes)
		}
		s.stageThreshold = bytes
		return nil
	}
}

// WithStageChunkBytes sets the most page bytes one staging commit writes. A
// non-positive value is refused with ErrInvalidOption.
func WithStageChunkBytes(bytes int) Option {
	return func(s *Store) error {
		if bytes <= 0 {
			return fmt.Errorf("%w: stage chunk size %d", ErrInvalidOption, bytes)
		}
		s.stageChunkBytes = bytes
		return nil
	}
}

// WithWriterLock serializes the Store's writers through w (Q3 A): a
// transaction writes only after Txn.Reserve took the lock, and holds it until
// Commit or Rollback. A nil lock is refused with ErrInvalidOption.
func WithWriterLock(w WriterLock) Option {
	return func(s *Store) error {
		if w == nil {
			return fmt.Errorf("%w: nil writer lock", ErrInvalidOption)
		}
		s.writer = w
		return nil
	}
}

// WithOptimistic selects optimistic writer mode (Q3 A, opt-in per
// connection): the Store takes no writer lock, a transaction records every
// page it reads from its snapshot, and Commit sends that read set with the
// write, so a commit that read a page another writer has since changed (or
// created) is ErrSnapshotChanged and writes nothing, while writers whose
// reads and writes are disjoint both commit. NewStore refuses it together
// with WithWriterLock (ErrOptimisticWithWriterLock).
func WithOptimistic() Option {
	return func(s *Store) error {
		s.optimistic = true
		return nil
	}
}

// NewStore returns the page store of database db over b. A nil backend is
// refused with ErrNilBackend and an invalid name with ErrInvalidDatabaseName.
func NewStore(b Backend, db string, opts ...Option) (*Store, error) {
	if b == nil {
		return nil, ErrNilBackend
	}
	metaKey, err := MetaKey(db)
	if err != nil {
		return nil, err
	}
	lockKey, err := WriterLockKey(db)
	if err != nil {
		return nil, err
	}
	s := &Store{
		backend:         b,
		db:              db,
		metaKey:         metaKey,
		lockKey:         lockKey,
		stageThreshold:  DefaultStageThreshold,
		stageChunkBytes: DefaultStageChunkBytes,
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	if s.optimistic && s.writer != nil {
		return nil, ErrOptimisticWithWriterLock
	}
	return s, nil
}

// DB returns the database name.
func (s *Store) DB() string {
	return s.db
}

// readMeta reads the meta record at revision rev. An absent meta record is
// an empty database: found=false and a Meta with only FormatVersion set.
func (s *Store) readMeta(ctx context.Context, rev int64) (Meta, bool, error) {
	meta := Meta{FormatVersion: FormatVersion}
	value, found, err := s.backend.GetAt(ctx, s.metaKey, rev)
	if err != nil {
		return Meta{}, false, fmt.Errorf("sqlitevfs: read meta of %q at revision %d: %w", s.db, rev, err)
	}
	if found {
		if err := meta.UnmarshalBinary(value); err != nil {
			return Meta{}, false, err
		}
	}
	return meta, found, nil
}

// Begin starts a transaction: it pins the backend's current revision and
// reads the meta record at it. An absent meta record is an empty database
// (size 0, page size not yet set). ctx is the context of every read the
// transaction makes (Begin's own, and every page read through ReadAt);
// Reserve, Commit and Rollback use the ctx they are given.
//
// If this Store pinned a different meta generation before, the database
// was restored underneath it (Q4 A): Begin records the new generation and
// returns ErrSnapshotChanged, and the next Begin succeeds. This is where a
// page cache will be dropped once there is one.
func (s *Store) Begin(ctx context.Context) (*Txn, error) {
	rev, err := s.backend.Revision(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: begin %q: %w", s.db, err)
	}
	return s.BeginAt(ctx, rev)
}

// BeginAt is Begin with the snapshot pinned at revision rev instead of the
// backend's current one: a caller that just committed at rev reads its own
// commit's snapshot, whatever committed since. rev must not be ahead of the
// backend's revision (the backend refuses a read there); the generation
// check is Begin's.
func (s *Store) BeginAt(ctx context.Context, rev int64) (*Txn, error) {
	if rev < 0 {
		return nil, fmt.Errorf("sqlitevfs: begin %q at negative revision %d", s.db, rev)
	}
	meta, _, err := s.readMeta(ctx, rev)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := s.seen && s.seenGen != meta.Generation
	s.seenGen = meta.Generation
	s.seen = true
	s.mu.Unlock()
	if changed {
		return nil, fmt.Errorf("%w: %q is at generation %d", ErrSnapshotChanged, s.db, meta.Generation)
	}
	return &Txn{
		store: s,
		ctx:   ctx,
		rev:   rev,
		base:  meta,
		meta:  meta,
		floor: meta.PageCount,
		dirty: make(map[uint32][]byte),
	}, nil
}

// BumpGeneration is the restore hook of Q4 A: it reads the meta record at
// the current revision and commits it with Generation+1 in one
// Backend.Commit, returning that revision. A database with no meta record
// writes nothing and returns the current revision. ctx bounds the read and
// the commit.
func (s *Store) BumpGeneration(ctx context.Context) (int64, error) {
	rev, err := s.backend.Revision(ctx)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: bump generation of %q: %w", s.db, err)
	}
	meta, found, err := s.readMeta(ctx, rev)
	if err != nil {
		return 0, err
	}
	if !found {
		return rev, nil
	}
	meta.Generation++
	value, err := meta.MarshalBinary()
	if err != nil {
		return 0, err
	}
	newRev, err := s.backend.Commit(ctx, nil, map[string][]byte{string(s.metaKey): value}, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: bump generation of %q: %w", s.db, err)
	}
	return newRev, nil
}

// sweepStages deletes, in one Backend.Commit, every staged page key of the
// database live at revision rev. The caller holds the writer lock.
func (s *Store) sweepStages(ctx context.Context, rev int64) error {
	root, err := StageRoot(s.db)
	if err != nil {
		return err
	}
	keys, err := s.backend.Keys(ctx, root, rev)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	_, err = s.backend.Commit(ctx, nil, nil, keys)
	return err
}

// Txn is one transaction on a database file: a snapshot at the revision
// Begin pinned plus a buffer of the transaction's own writes. It offers the
// file semantics a VFS binding calls (size, read, write, truncate) and
// commits the buffer as one Backend.Commit, or, above the stage threshold,
// as staging commits and one Backend.Publish. A Txn is not safe for concurrent
// use.
type Txn struct {
	store *Store
	// ctx is the read context: the context Begin was given, used by every
	// page and meta read the transaction makes.
	// ReadAt is io-shaped (SQLite's xRead) and carries no context of its own.
	ctx context.Context
	rev int64
	// base is the meta record at rev; meta is base with the buffered
	// writes and truncations applied.
	base Meta
	meta Meta
	// floor is the lowest page count this transaction has truncated to:
	// a page above it that is not in dirty reads as zeros and is deleted
	// at commit, since the snapshot's copy is no longer part of the file.
	floor uint32
	dirty map[uint32][]byte
	wrote bool
	// rewrote is set once a page already in dirty is written again or the
	// file shrinks (a cache spill, then the final write, or a journal
	// played back over it and the file truncated to its old size); only
	// then does Commit compare dirty pages with the snapshot.
	rewrote bool
	done    bool
	// reserved is set once Reserve succeeded; token is the writer lock's
	// fencing token while this transaction holds it (held is true).
	reserved bool
	held     bool
	token    int64
	// reads is the read set of an optimistic transaction: every page key
	// read from the snapshot at rev, and whether it had a live value.
	reads map[string]bool
}

// ChangeCounter is the meta change counter of the transaction's
// snapshot: the count of write transactions committed before it.
func (t *Txn) ChangeCounter() uint64 {
	return t.base.ChangeCounter
}

// Revision returns the revision the transaction reads at.
func (t *Txn) Revision() int64 {
	return t.rev
}

// Size returns the file size in bytes, including buffered writes:
// PageCount*PageSize.
func (t *Txn) Size() int64 {
	return int64(t.meta.PageCount) * int64(t.meta.PageSize)
}

// Reserve makes the transaction the database's writer: SQLite's
// xLock(RESERVED). A second call is a no-op. On a Store without a writer lock
// it only marks the transaction reserved (optimistic/local mode). With one it
// takes the lock without blocking: a lock held by another writer is ErrBusy
// at once (the binding maps it to SQLITE_BUSY; SQLite's busy handler
// retries). It then re-reads the meta record at the current revision: if the
// generation or the change counter moved since this transaction's snapshot,
// the snapshot is stale and cannot become the writer, so Reserve releases
// the lock and returns ErrSnapshotChanged (SQLITE_BUSY_SNAPSHOT, as SQLite on
// a file whose change counter moved).
//
// Holding the lock, Reserve sweeps abandoned stages: only the lock holder
// stages pages, so every key under StageRoot seen under the lock was left by
// a writer that crashed or lost its lease, and Reserve deletes them all in
// one Backend.Commit. A failed sweep releases the lock and is returned.
// Every lock call, read and sweep Reserve makes uses its own ctx.
func (t *Txn) Reserve(ctx context.Context) error {
	if t.done {
		return ErrTxnDone
	}
	if t.reserved {
		return nil
	}
	s := t.store
	if s.writer == nil {
		t.reserved = true
		return nil
	}
	token, err := s.writer.Acquire(ctx, s.lockKey)
	if err != nil {
		return err
	}
	t.token = token
	t.held = true
	rev, err := s.backend.Revision(ctx)
	if err != nil {
		_ = t.release(ctx)
		return fmt.Errorf("sqlitevfs: reserve %q: %w", s.db, err)
	}
	current, _, err := s.readMeta(ctx, rev)
	if err != nil {
		_ = t.release(ctx)
		return err
	}
	if current.Generation != t.base.Generation || current.ChangeCounter != t.base.ChangeCounter {
		_ = t.release(ctx)
		return fmt.Errorf("%w: %q moved from generation %d change counter %d to generation %d change counter %d",
			ErrSnapshotChanged, s.db, t.base.Generation, t.base.ChangeCounter, current.Generation, current.ChangeCounter)
	}
	if err := s.sweepStages(ctx, rev); err != nil {
		_ = t.release(ctx)
		return fmt.Errorf("sqlitevfs: sweep abandoned stages of %q: %w", s.db, err)
	}
	t.reserved = true
	return nil
}

// release gives up the writer lock if this transaction holds it.
func (t *Txn) release(ctx context.Context) error {
	if !t.held {
		return nil
	}
	t.held = false
	return t.store.writer.Release(ctx, t.store.lockKey, t.token)
}

// ReadAt reads len(p) bytes at offset off from the buffer, else from the
// pages at the pinned revision. A read wholly past the end zero-fills p and
// returns 0 and io.EOF; a read that runs past the end zero-fills the rest of
// p and returns the bytes read and io.ErrUnexpectedEOF (SQLite's
// SQLITE_IOERR_SHORT_READ contract). ReadAt is io-shaped (SQLite's xRead):
// it takes no context, and its page reads use the context Begin was given.
func (t *Txn) ReadAt(p []byte, off int64) (int, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	if off < 0 {
		return 0, fmt.Errorf("sqlitevfs: negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	size := t.Size()
	if off >= size {
		clear(p)
		return 0, io.EOF
	}
	want := int64(len(p))
	if remain := size - off; want > remain {
		want = remain
	}
	pageSize := int64(t.meta.PageSize)
	n := 0
	for int64(n) < want {
		pos := off + int64(n)
		pgno := uint32(pos/pageSize) + 1 //nolint:gosec // G115: pos < size, so the page number fits PageCount's uint32
		page, err := t.page(pgno)
		if err != nil {
			return n, err
		}
		n += copy(p[n:want], page[pos%pageSize:])
	}
	if n < len(p) {
		clear(p[n:])
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

// page returns page pgno as the transaction sees it: the buffered copy, else
// zeros above the truncation floor, else the page at the pinned revision
// (zeros when absent, a hole).
func (t *Txn) page(pgno uint32) ([]byte, error) {
	if page, ok := t.dirty[pgno]; ok {
		return page, nil
	}
	if pgno > t.floor {
		return make([]byte, t.meta.PageSize), nil
	}
	key, err := PageKey(t.store.db, pgno)
	if err != nil {
		return nil, err
	}
	value, found, err := t.store.backend.GetAt(t.ctx, key, t.rev)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: read page %d of %q at revision %d: %w", pgno, t.store.db, t.rev, err)
	}
	if t.store.optimistic {
		if t.reads == nil {
			t.reads = make(map[string]bool)
		}
		t.reads[string(key)] = found
	}
	if !found {
		return make([]byte, t.meta.PageSize), nil
	}
	if len(value) != int(t.meta.PageSize) {
		return nil, fmt.Errorf("sqlitevfs: page %d of %q is %d bytes, want %d", pgno, t.store.db, len(value), t.meta.PageSize)
	}
	return value, nil
}

// WriteAt buffers a write of p at offset off. The first write to an empty
// database sets the page size to len(p), which must be a page size SQLite
// allows (ErrInvalidMeta) at an offset that is a multiple of it. Every later
// write must be exactly one page at a page-aligned offset: a write of
// another valid page size is refused with ErrPageSizeChanged, any other
// misaligned write with ErrUnalignedIO. A write past the end extends the
// page count.
func (t *Txn) WriteAt(p []byte, off int64) (int, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	if t.store.writer != nil && !t.reserved {
		return 0, ErrNotReserved
	}
	if off < 0 {
		return 0, fmt.Errorf("sqlitevfs: negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > int(MaxPageSize) {
		return 0, fmt.Errorf("%w: write of %d bytes", ErrUnalignedIO, len(p))
	}
	n := uint32(len(p)) //nolint:gosec // G115: len(p) <= MaxPageSize
	if t.meta.PageSize == 0 {
		if !ValidPageSize(n) {
			return 0, fmt.Errorf("%w: page size %d is not a power of two in %d..%d", ErrInvalidMeta, n, MinPageSize, MaxPageSize)
		}
		if off%int64(n) != 0 {
			return 0, fmt.Errorf("%w: offset %d is not a multiple of page size %d", ErrUnalignedIO, off, n)
		}
		t.meta.PageSize = n
	}
	pageSize := int64(t.meta.PageSize)
	if n != t.meta.PageSize {
		if ValidPageSize(n) {
			return 0, fmt.Errorf("%w: write of %d bytes, page size is %d", ErrPageSizeChanged, n, pageSize)
		}
		return 0, fmt.Errorf("%w: write of %d bytes, page size is %d", ErrUnalignedIO, n, pageSize)
	}
	if off%pageSize != 0 {
		return 0, fmt.Errorf("%w: offset %d, page size is %d", ErrUnalignedIO, off, pageSize)
	}
	idx := off / pageSize
	if idx >= math.MaxUint32 {
		return 0, fmt.Errorf("%w: offset %d is past the last page", ErrInvalidPageNumber, off)
	}
	pgno := uint32(idx) + 1 //nolint:gosec // G115: idx < MaxUint32
	if _, ok := t.dirty[pgno]; ok {
		t.rewrote = true
	}
	t.dirty[pgno] = append([]byte(nil), p...)
	if pgno > t.meta.PageCount {
		t.meta.PageCount = pgno
	}
	t.wrote = true
	return len(p), nil
}

// Truncate sets the file size to size, which must be page-aligned
// (ErrUnalignedIO). Pages past the new end are dropped from the buffer and
// deleted at commit; growing the file adds zero pages.
func (t *Txn) Truncate(size int64) error {
	if t.done {
		return ErrTxnDone
	}
	if t.store.writer != nil && !t.reserved {
		return ErrNotReserved
	}
	if size < 0 {
		return fmt.Errorf("sqlitevfs: negative size %d", size)
	}
	if t.meta.PageSize == 0 {
		if size != 0 {
			return fmt.Errorf("%w: size %d with no page size set", ErrUnalignedIO, size)
		}
		return nil
	}
	pageSize := int64(t.meta.PageSize)
	if size%pageSize != 0 {
		return fmt.Errorf("%w: size %d, page size is %d", ErrUnalignedIO, size, pageSize)
	}
	if size/pageSize > math.MaxUint32 {
		return fmt.Errorf("%w: size %d is past the last page", ErrInvalidPageNumber, size)
	}
	count := uint32(size / pageSize) //nolint:gosec // G115: bounded above
	if count < t.meta.PageCount {
		t.rewrote = true
	}
	for pgno := range t.dirty {
		if pgno > count {
			delete(t.dirty, pgno)
		}
	}
	if count < t.floor {
		t.floor = count
	}
	t.meta.PageCount = count
	t.wrote = true
	return nil
}

// Commit sends the buffered pages, the deletes of the pages the transaction
// truncated away, and the new meta record in one Backend.Commit, and returns
// its revision. A transaction with no writes commits nothing and returns the
// pinned revision; so does one whose writes leave the file as the snapshot
// had it: once a page was written twice or the file shrank, the dirty pages equal to the
// snapshot's copies are dropped (dropUnchanged), and with none left, the
// same page size and count and no page truncated away, nothing is written. The transaction is done afterwards, whether or not the
// commit succeeded. A held writer lock is released afterwards, whether or not
// the commit succeeded, with the same ctx; a Release error is ignored (after
// a successful commit the data is committed and the lease expires on its
// own; after a failed one the commit error is the one to report).
//
// Commit first confirms, on a Store with a writer lock, that this
// transaction still holds it: a lost lease is ErrBusy and nothing is
// written. It then re-reads the meta record at the backend's current
// revision: if its generation differs from the one this transaction pinned,
// the database was restored underneath it (Q4 A), and with a writer lock a
// moved change counter means another writer committed since the snapshot;
// either way Commit returns ErrSnapshotChanged and writes nothing. With the
// writer lock held from Reserve to Commit, no other page store writer
// commits between the check and the write. BumpGeneration (restore) does not
// take the lock: a restore racing a commit is caught at the next Begin.
//
// Without a writer lock the meta record Commit re-read is a read check of
// the write itself, so a commit landing between that read and the write is
// ErrSnapshotChanged too. In optimistic mode (WithOptimistic) the write also
// carries every page the transaction read from its snapshot, present or
// absent: a page another writer changed or created since the snapshot is
// ErrSnapshotChanged and nothing is written; a moved page count or page size
// is too, and otherwise the change counter advances from the newest record.
//
// When the dirty pages total more than the Store's stage threshold, the
// pages go through staging and one publish (Q2 A, commitStaged) instead.
// Every read and write Commit makes uses its own ctx, not Begin's.
func (t *Txn) Commit(ctx context.Context) (int64, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	t.done = true
	rev, err := t.commitWrites(ctx)
	_ = t.release(ctx)
	return rev, err
}

// commitWrites is Commit's write path; the caller releases the writer lock.
func (t *Txn) commitWrites(ctx context.Context) (int64, error) {
	if !t.wrote {
		return t.rev, nil
	}
	if t.rewrote {
		if err := t.dropUnchanged(ctx); err != nil {
			return 0, err
		}
	}
	if len(t.dirty) == 0 && t.floor >= t.base.PageCount && t.meta.PageCount == t.base.PageCount && t.meta.PageSize == t.base.PageSize {
		// Every write was undone (a journal played back over its own
		// writes): nothing to commit, no revision.
		return t.rev, nil
	}
	if t.held {
		if err := t.store.writer.Check(ctx, t.store.lockKey, t.token); err != nil {
			return 0, err
		}
	}
	now, err := t.store.backend.Revision(ctx)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit %q: %w", t.store.db, err)
	}
	current, metaFound, err := t.store.readMeta(ctx, now)
	if err != nil {
		return 0, err
	}
	if current.Generation != t.base.Generation {
		return 0, fmt.Errorf("%w: %q moved from generation %d to %d", ErrSnapshotChanged, t.store.db, t.base.Generation, current.Generation)
	}
	if t.store.writer != nil && current.ChangeCounter != t.base.ChangeCounter {
		return 0, fmt.Errorf("%w: %q moved from change counter %d to %d", ErrSnapshotChanged, t.store.db, t.base.ChangeCounter, current.ChangeCounter)
	}
	counter := t.base.ChangeCounter
	if t.store.optimistic {
		// Disjoint writers may commit in any order, so the change
		// counter advances from the newest record; a moved page size or
		// page count is a moved file this snapshot cannot merge into.
		if current.PageSize != t.base.PageSize || current.PageCount != t.base.PageCount {
			return 0, fmt.Errorf("%w: %q moved from %d pages of %d bytes to %d pages of %d bytes",
				ErrSnapshotChanged, t.store.db, t.base.PageCount, t.base.PageSize, current.PageCount, current.PageSize)
		}
		counter = current.ChangeCounter
	}
	reads := t.readSet(now, metaFound)
	t.meta.Generation = t.base.Generation
	t.meta.ChangeCounter = counter + 1
	metaValue, err := t.meta.MarshalBinary()
	if err != nil {
		return 0, err
	}
	var deletes [][]byte
	for pgno := uint64(t.floor) + 1; pgno <= uint64(t.base.PageCount); pgno++ {
		if _, ok := t.dirty[uint32(pgno)]; ok { //nolint:gosec // G115: pgno <= base.PageCount, a uint32
			continue
		}
		key, err := PageKey(t.store.db, uint32(pgno)) //nolint:gosec // G115: as above
		if err != nil {
			return 0, err
		}
		deletes = append(deletes, key)
	}
	dirtyBytes := 0
	for _, page := range t.dirty {
		dirtyBytes += len(page)
	}
	if dirtyBytes > t.store.stageThreshold {
		return t.commitStaged(ctx, reads, metaValue, deletes)
	}
	puts := make(map[string][]byte, len(t.dirty)+1)
	puts[string(t.store.metaKey)] = metaValue
	for pgno, page := range t.dirty {
		key, err := PageKey(t.store.db, pgno)
		if err != nil {
			return 0, err
		}
		puts[string(key)] = page
	}
	rev, err := t.store.backend.Commit(ctx, reads, puts, deletes)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit %q: %w", t.store.db, err)
	}
	return rev, nil
}

// dropUnchanged drops from the write set every dirty page whose bytes equal
// the snapshot's copy at rev (an absent page within the snapshot's page
// count reads as zeros), so a journal played back over the transaction's
// own writes leaves nothing to commit. Pages past the snapshot (or past a
// truncation floor) are new and kept. Reads use ctx, Commit's.
func (t *Txn) dropUnchanged(ctx context.Context) error {
	if t.meta.PageSize != t.base.PageSize {
		return nil
	}
	for pgno, page := range t.dirty {
		if pgno > t.floor || pgno > t.base.PageCount {
			continue
		}
		key, err := PageKey(t.store.db, pgno)
		if err != nil {
			return err
		}
		value, found, err := t.store.backend.GetAt(ctx, key, t.rev)
		if err != nil {
			return fmt.Errorf("sqlitevfs: commit %q: read page %d at revision %d: %w", t.store.db, pgno, t.rev, err)
		}
		if !found {
			value = make([]byte, len(page))
		}
		if bytes.Equal(value, page) {
			delete(t.dirty, pgno)
		}
	}
	return nil
}

// readSet returns the read checks a commit carries. A transaction holding
// the writer lock carries none: no other page store writer commits while it
// holds it. Without one, the meta record read at revision now (found or not)
// must be unchanged at the write, which makes Commit's generation (and, in
// optimistic mode, file size) check atomic with it; in optimistic mode every
// page the transaction read from its snapshot must also be unchanged since
// rev, in key order.
func (t *Txn) readSet(now int64, metaFound bool) []ReadCheck {
	if t.store.writer != nil {
		return nil
	}
	reads := make([]ReadCheck, 0, len(t.reads)+1)
	reads = append(reads, ReadCheck{Key: t.store.metaKey, Rev: now, Found: metaFound})
	keys := make([]string, 0, len(t.reads))
	for key := range t.reads {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reads = append(reads, ReadCheck{Key: []byte(key), Rev: t.rev, Found: t.reads[key]})
	}
	return reads
}

// Rollback discards the buffer and releases a held writer lock. The
// transaction is done afterwards.
func (t *Txn) Rollback(ctx context.Context) error {
	if t.done {
		return ErrTxnDone
	}
	t.done = true
	t.dirty = nil
	return t.release(ctx)
}
