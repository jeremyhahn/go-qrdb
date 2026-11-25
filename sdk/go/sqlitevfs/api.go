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
	pkgvfs "github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	modernc "github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs/moderncvfs"
)

// The page store and its options, as the SDK's surface: a client opens a
// database with NewStore over the Backend NewBackend returns.
type (
	// Backend is the MVCC store a Store keeps its pages in.
	Backend = pkgvfs.Backend
	// Store is one SQLite database's page store.
	Store = pkgvfs.Store
	// Option configures a Store.
	Option = pkgvfs.Option
	// WriterLock is the lock a single-writer Store holds while it writes.
	WriterLock = pkgvfs.WriterLock
	// Opener opens the Store of the database a VFS qrdb URI names.
	Opener = modernc.Opener
)

// ErrInvalidOption reports an option or URI parameter the store refuses.
var ErrInvalidOption = pkgvfs.ErrInvalidOption

// The VFS name, its concurrency URI parameter and the database/sql driver
// name that opens databases through it.
const (
	VFSName          = modernc.VFSName
	ConcurrencyParam = modernc.ConcurrencyParam
	DriverName       = modernc.DriverName
)

// NewStore opens database db's page store over b.
func NewStore(b Backend, db string, opts ...Option) (*Store, error) {
	return pkgvfs.NewStore(b, db, opts...)
}

// WithOptimistic makes the Store's commits check their reads instead of
// holding a writer lock.
func WithOptimistic() Option { return pkgvfs.WithOptimistic() }

// WithWriterLock makes the Store hold w while it writes.
func WithWriterLock(w WriterLock) Option { return pkgvfs.WithWriterLock(w) }

// WithStageThreshold sets the dirty-page bytes above which a commit is
// staged and published in one revision.
func WithStageThreshold(bytes int) Option { return pkgvfs.WithStageThreshold(bytes) }

// ValidateDatabaseName refuses a database name the page store cannot key.
func ValidateDatabaseName(db string) error { return pkgvfs.ValidateDatabaseName(db) }

// Register registers VFS qrdb over open with the modernc SQLite driver.
func Register(open Opener) error { return modernc.Register(open) }
