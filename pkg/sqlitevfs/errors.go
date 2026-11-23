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
	"errors"
	"fmt"
)

var (
	// ErrInvalidDatabaseName is returned for a database name that is empty,
	// longer than MaxDatabaseNameLen bytes, contains '/', a NUL or any other
	// byte below 0x20, or is "." or "..".
	ErrInvalidDatabaseName = errors.New("sqlitevfs: invalid database name")

	// ErrInvalidPageNumber is returned for page number 0 (SQLite numbers
	// pages from 1), a page number past the uint32 range, or a page key
	// that does not encode a page of the database.
	ErrInvalidPageNumber = errors.New("sqlitevfs: invalid page number")

	// ErrInvalidMeta is returned for a meta record of the wrong length, an
	// unknown format version, or a page size SQLite does not allow.
	ErrInvalidMeta = errors.New("sqlitevfs: invalid meta record")

	// ErrUnalignedIO is returned for a write or truncate whose offset or
	// length is not page-aligned once the page size is set: SQLite writes
	// whole pages to the main database file.
	ErrUnalignedIO = errors.New("sqlitevfs: unaligned I/O")

	// ErrTxnDone is returned by every Txn method called after Commit or
	// Rollback.
	ErrTxnDone = errors.New("sqlitevfs: transaction already committed or rolled back")

	// ErrPageSizeChanged is returned for a write of a different page size
	// once the database's page size is set.
	ErrPageSizeChanged = errors.New("sqlitevfs: page size changed")

	// ErrNilBackend is returned when a Store or MVCCBackend is built over a
	// nil backend.
	ErrNilBackend = errors.New("sqlitevfs: nil backend")

	// ErrSnapshotChanged is returned when the database's meta generation
	// changed since this Store's last snapshot pin or this transaction's
	// pin: the database was restored underneath it (Q4 A). The SQLite
	// binding maps it to SQLITE_BUSY_SNAPSHOT.
	ErrSnapshotChanged = errors.New("sqlitevfs: snapshot generation changed")

	// ErrInvalidCommitID is returned for a staging commit id that is not 32
	// lowercase hex characters.
	ErrInvalidCommitID = errors.New("sqlitevfs: invalid commit id")

	// ErrInvalidOption is returned by NewStore for a non-positive stage
	// threshold or stage chunk size or a nil writer lock, and by
	// NewLockerWriter for a nil locker, an empty holder id or a
	// non-positive TTL.
	ErrInvalidOption = errors.New("sqlitevfs: invalid option")

	// ErrOptimisticWithWriterLock is returned by NewStore when both
	// WithOptimistic and WithWriterLock are given: optimistic mode is the
	// no-lock writer (Q3 A), so the two are exclusive. It wraps
	// ErrInvalidOption.
	ErrOptimisticWithWriterLock = fmt.Errorf("%w: optimistic mode and a writer lock are exclusive", ErrInvalidOption)

	// ErrReadCheckUnsupported is returned by a Backend.Publish that cannot
	// evaluate a non-empty read set atomically with the prefix move (the
	// service path: OpMVCCMovePrefix carries no compares). It writes
	// nothing; the check is refused, never approximated.
	ErrReadCheckUnsupported = errors.New("sqlitevfs: backend cannot check a read set atomically with a publish")

	// ErrBusy is returned when the database's writer lock is held by
	// another writer, or when this transaction's lease on it was lost
	// before Commit. The SQLite binding maps it to SQLITE_BUSY.
	ErrBusy = errors.New("sqlitevfs: database is locked by another writer")

	// ErrFull is returned when a commit, a stage write or a publish exceeds
	// the tenant's storage quota. The database is unchanged: the pages and
	// meta are those of the last good commit. The SQLite binding maps it to
	// SQLITE_FULL.
	ErrFull = errors.New("sqlitevfs: database or disk is full")

	// ErrNotReserved is returned by WriteAt or Truncate on a Store with a
	// writer lock before Txn.Reserve succeeded.
	ErrNotReserved = errors.New("sqlitevfs: transaction has not reserved the writer lock")

	// ErrPublishConflict is returned by Backend.Publish when the number of
	// keys live under the source prefix differs from the expected count;
	// the publish writes nothing.
	ErrPublishConflict = errors.New("sqlitevfs: publish source count changed")

	// ErrInvalidPublish is returned by Backend.Publish for an empty source
	// or destination prefix, prefixes where one is a prefix of the other,
	// or an expected key count below one.
	ErrInvalidPublish = errors.New("sqlitevfs: invalid publish")
)
