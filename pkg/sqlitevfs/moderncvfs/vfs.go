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
	"sync"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
)

const (
	// VFSName is the name the binding registers: open
	// file:app.db?vfs=qrdb.
	VFSName = "qrdb"

	// ConcurrencyParam is the URI parameter an Opener reads to choose a
	// Store's concurrency mode (for example qrdb_concurrency=optimistic
	// for sqlitevfs.WithOptimistic). The binding passes every URI
	// parameter to the Opener and interprets none of them.
	ConcurrencyParam = "qrdb_concurrency"

	// sectorSize is the xSectorSize answer.
	sectorSize = 4096
)

var (
	// ErrRegistered is returned by Register when VFS qrdb is already
	// registered in the process.
	ErrRegistered = errors.New("moderncvfs: VFS qrdb is already registered")

	errNilOpener    = errors.New("moderncvfs: nil Opener")
	errNoDefaultVFS = errors.New("moderncvfs: SQLite has no default VFS")
)

// Opener returns the page store of the database file name, the path of
// the file: URI, with the URI's query parameters. The binding calls it
// once per open of a main database file; the Store is the connection's
// until it closes the file.
type Opener func(ctx context.Context, name string, params url.Values) (*sqlitevfs.Store, error)

// reg is the process's registration.
var reg struct {
	mu     sync.Mutex
	open   Opener
	osVFS  uintptr
	osOpen uintptr
}

// Register registers VFS qrdb over the page stores open returns. It
// succeeds once per process; a second call returns ErrRegistered.
func Register(open Opener) error {
	if open == nil {
		return errNilOpener
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.open != nil {
		return ErrRegistered
	}
	osVFS, osOpen, err := installVFS()
	if err != nil {
		return err
	}
	reg.open, reg.osVFS, reg.osOpen = open, osVFS, osOpen
	return nil
}

// Unregister removes VFS qrdb, so tests can register again. No
// connection may have a database open on it.
func Unregister() error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.open == nil {
		return nil
	}
	if err := uninstallVFS(); err != nil {
		return err
	}
	reg.open, reg.osVFS, reg.osOpen = nil, 0, 0
	return nil
}

func osVFSOpen() (osVFS, osOpen uintptr) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.osVFS, reg.osOpen
}

func resultError(call string, rc int32) error {
	return fmt.Errorf("moderncvfs: %s: SQLite result code %d", call, rc)
}

// fileKind is what an xOpen opens.
type fileKind int

const (
	kindMain    fileKind = iota // the database file, over a Store
	kindJournal                 // its rollback journal, in memory
	kindTemp                    // temporary files, on the OS VFS
	kindRefused                 // super-journals and WAL files
)

func classify(flags int32, anonymous bool) fileKind {
	switch {
	case flags&(sqlite3.SQLITE_OPEN_SUPER_JOURNAL|sqlite3.SQLITE_OPEN_WAL) != 0:
		return kindRefused
	case anonymous:
		return kindTemp
	case flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0:
		return kindMain
	case flags&sqlite3.SQLITE_OPEN_MAIN_JOURNAL != 0:
		return kindJournal
	default:
		return kindTemp
	}
}

func openMain(name string, params url.Values) (vfile, int32) {
	reg.mu.Lock()
	open := reg.open
	reg.mu.Unlock()
	if open == nil {
		return nil, sqlite3.SQLITE_CANTOPEN
	}
	s, err := open(context.Background(), name, params)
	if err != nil || s == nil {
		return nil, sqlite3.SQLITE_CANTOPEN
	}
	return &dbFile{store: s}, sqlite3.SQLITE_OK
}

// handles maps the id in each open sqlite3_file to its Go file.
var handles = struct {
	mu   sync.Mutex
	next uintptr
	m    map[uintptr]vfile
}{m: map[uintptr]vfile{}}

func putHandle(f vfile) uintptr {
	handles.mu.Lock()
	defer handles.mu.Unlock()
	handles.next++
	handles.m[handles.next] = f
	return handles.next
}

func getHandle(id uintptr) vfile {
	handles.mu.Lock()
	defer handles.mu.Unlock()
	return handles.m[id]
}

func takeHandle(id uintptr) vfile {
	handles.mu.Lock()
	defer handles.mu.Unlock()
	f := handles.m[id]
	delete(handles.m, id)
	return f
}
