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
	"fmt"
	"io"
	"math"
)

// Backend is the versioned key-value store the page store reads and commits
// through.
type Backend interface {
	// Revision returns the newest committed revision.
	Revision() int64
	// GetAt returns key's value at revision rev. A key with no live value
	// at rev is found=false with a nil error; err is a failed read.
	GetAt(key []byte, rev int64) (value []byte, found bool, err error)
	// Commit applies every put and every delete at one new revision, or
	// none of them, and returns that revision.
	Commit(puts map[string][]byte, deletes [][]byte) (rev int64, err error)
}

// Store is the page store of one SQLite database file over a Backend.
type Store struct {
	backend Backend
	db      string
	metaKey []byte
}

// NewStore returns the page store of database db over b. A nil backend is
// refused with ErrNilBackend and an invalid name with ErrInvalidDatabaseName.
func NewStore(b Backend, db string) (*Store, error) {
	if b == nil {
		return nil, ErrNilBackend
	}
	metaKey, err := MetaKey(db)
	if err != nil {
		return nil, err
	}
	return &Store{backend: b, db: db, metaKey: metaKey}, nil
}

// DB returns the database name.
func (s *Store) DB() string {
	return s.db
}

// Begin starts a transaction: it pins the backend's current revision and
// reads the meta record at it. An absent meta record is an empty database
// (size 0, page size not yet set).
func (s *Store) Begin() (*Txn, error) {
	rev := s.backend.Revision()
	meta := Meta{FormatVersion: FormatVersion}
	value, found, err := s.backend.GetAt(s.metaKey, rev)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: read meta of %q at revision %d: %w", s.db, rev, err)
	}
	if found {
		if err := meta.UnmarshalBinary(value); err != nil {
			return nil, err
		}
	}
	return &Txn{
		store: s,
		rev:   rev,
		base:  meta,
		meta:  meta,
		floor: meta.PageCount,
		dirty: make(map[uint32][]byte),
	}, nil
}

// Txn is one transaction on a database file: a snapshot at the revision
// Begin pinned plus a buffer of the transaction's own writes. It offers the
// file semantics a VFS binding calls (size, read, write, truncate) and
// commits the buffer as one Backend.Commit. A Txn is not safe for concurrent
// use.
type Txn struct {
	store *Store
	rev   int64
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
	done  bool
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

// ReadAt reads len(p) bytes at offset off from the buffer, else from the
// pages at the pinned revision. A read wholly past the end zero-fills p and
// returns 0 and io.EOF; a read that runs past the end zero-fills the rest of
// p and returns the bytes read and io.ErrUnexpectedEOF (SQLite's
// SQLITE_IOERR_SHORT_READ contract).
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
	value, found, err := t.store.backend.GetAt(key, t.rev)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs: read page %d of %q at revision %d: %w", pgno, t.store.db, t.rev, err)
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
// pinned revision. The transaction is done afterwards, whether or not the
// commit succeeded.
func (t *Txn) Commit() (int64, error) {
	if t.done {
		return 0, ErrTxnDone
	}
	t.done = true
	if !t.wrote {
		return t.rev, nil
	}
	metaValue, err := t.meta.MarshalBinary()
	if err != nil {
		return 0, err
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
	rev, err := t.store.backend.Commit(puts, deletes)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit %q: %w", t.store.db, err)
	}
	return rev, nil
}

// Rollback discards the buffer. The transaction is done afterwards.
func (t *Txn) Rollback() error {
	if t.done {
		return ErrTxnDone
	}
	t.done = true
	t.dirty = nil
	return nil
}
