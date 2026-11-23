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
	"errors"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
)

// resultCode is the one error-mapping table of the binding: it turns a page
// store error into the SQLite result code a VFS method returns.
//
//	sqlitevfs.ErrBusy            -> SQLITE_BUSY
//	sqlitevfs.ErrSnapshotChanged -> SQLITE_BUSY_SNAPSHOT
//	sqlitevfs.ErrFull            -> SQLITE_FULL
//	anything else                -> fallback, the SQLITE_IOERR_* code of
//	                                the call (SQLITE_ERROR for
//	                                COMMIT_ATOMIC_WRITE, which must not
//	                                answer an IOERR code)
func resultCode(err error, fallback int32) int32 {
	switch {
	case err == nil:
		return sqlite3.SQLITE_OK
	case errors.Is(err, sqlitevfs.ErrBusy):
		return sqlite3.SQLITE_BUSY
	case errors.Is(err, sqlitevfs.ErrSnapshotChanged):
		return sqlite3.SQLITE_BUSY_SNAPSHOT
	case errors.Is(err, sqlitevfs.ErrFull):
		return sqlite3.SQLITE_FULL
	default:
		return fallback
	}
}
