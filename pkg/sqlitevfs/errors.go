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

import "errors"

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
	// threshold or stage chunk size.
	ErrInvalidOption = errors.New("sqlitevfs: invalid option")
)
