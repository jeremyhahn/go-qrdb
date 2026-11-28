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

// abi.go holds every pointer conversion between Go and the transpiled C
// of modernc.org/sqlite. ccgo v4 passes a C pointer as a uintptr and a C
// function pointer as the funcval pointer of a Go func; the callbacks
// below have the exact Go signatures the transpiled sqlite3_vfs and
// sqlite3_io_methods calls use (the *libc.TLS first, int as int32,
// sqlite3_int64 as int64, pointers as uintptr).

import (
	"math/bits"
	"net/url"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// wordSize is the size of a C pointer.
const wordSize = bits.UintSize / 8

// The VFS name, the sqlite3_vfs and the sqlite3_io_methods SQLite holds
// pointers to. They are package globals: static storage the garbage
// collector never moves or frees.
var (
	cName    = [...]byte{'q', 'r', 'd', 'b', 0}
	cVFS     sqlite3.Tsqlite3_vfs
	cMethods sqlite3.Tsqlite3_io_methods
)

// cptr turns an address SQLite handed to a callback into a pointer.
func cptr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p)) // #nosec G103 -- ccgo passes every C pointer as a uintptr; p is C heap memory SQLite owns for the call or one of this file's static globals, never a movable Go heap object.
}

// addrOf is the C address of a static package global.
func addrOf[T any](p *T) uintptr {
	return uintptr(unsafe.Pointer(p)) // #nosec G103 -- p is a package-level global (cName, cVFS, cMethods), static storage the collector never moves or frees, so SQLite may keep its address.
}

// cbytes is the n bytes at p as a slice, valid for the duration of the
// callback.
func cbytes(p uintptr, n int32) []byte {
	if p == 0 || n <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(cptr(p)), int(n)) // #nosec G103 -- the xRead, xWrite and xFullPathname contract: p points at n bytes SQLite owns for the call.
}

// cfunc is the C function pointer of the top-level Go func f.
func cfunc(f any) uintptr {
	return (*[2]uintptr)(unsafe.Pointer(&f))[1] // #nosec G103 -- ccgo v4 represents a C function pointer as the funcval pointer of a Go func, the data word of an interface holding it; f is a top-level func, so its funcval is static.
}

// callOpen calls the C function pointer fp with the xOpen signature.
func callOpen(tls *libc.TLS, fp, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	return (*(*func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32)(unsafe.Pointer(&struct{ uintptr }{fp})))(tls, pVfs, zName, pFile, flags, pOutFlags) // #nosec G103 -- ccgo v4 calls a C function pointer by viewing the uintptr as a Go func value of the C signature; fp is the default VFS's xOpen.
}

// installVFS registers cVFS with SQLite under the name "qrdb", copying
// the default (OS) VFS's non-file methods, and returns the default VFS
// and its xOpen for temporary files.
func installVFS() (osVFS, osOpen uintptr, err error) {
	tls := libc.NewTLS()
	defer tls.Close()
	if sqlite3.Xsqlite3_vfs_find(tls, addrOf(&cName[0])) != 0 {
		return 0, 0, ErrRegistered
	}
	osVFS = sqlite3.Xsqlite3_vfs_find(tls, 0)
	if osVFS == 0 {
		return 0, 0, errNoDefaultVFS
	}
	os := (*sqlite3.Tsqlite3_vfs)(cptr(osVFS))
	cMethods = sqlite3.Tsqlite3_io_methods{
		FiVersion:               1,
		FxClose:                 cfunc(xClose),
		FxRead:                  cfunc(xRead),
		FxWrite:                 cfunc(xWrite),
		FxTruncate:              cfunc(xTruncate),
		FxSync:                  cfunc(xSync),
		FxFileSize:              cfunc(xFileSize),
		FxLock:                  cfunc(xLock),
		FxUnlock:                cfunc(xUnlock),
		FxCheckReservedLock:     cfunc(xCheckReservedLock),
		FxFileControl:           cfunc(xFileControl),
		FxSectorSize:            cfunc(xSectorSize),
		FxDeviceCharacteristics: cfunc(xDeviceCharacteristics),
	}
	size := os.FszOsFile
	if size < 2*wordSize {
		size = 2 * wordSize
	}
	cVFS = sqlite3.Tsqlite3_vfs{
		FiVersion:          2,
		FszOsFile:          size,
		FmxPathname:        os.FmxPathname,
		FzName:             addrOf(&cName[0]),
		FxOpen:             cfunc(xOpen),
		FxDelete:           cfunc(xDelete),
		FxAccess:           cfunc(xAccess),
		FxFullPathname:     cfunc(xFullPathname),
		FxDlOpen:           os.FxDlOpen,
		FxDlError:          os.FxDlError,
		FxDlSym:            os.FxDlSym,
		FxDlClose:          os.FxDlClose,
		FxRandomness:       os.FxRandomness,
		FxSleep:            os.FxSleep,
		FxCurrentTime:      os.FxCurrentTime,
		FxGetLastError:     os.FxGetLastError,
		FxCurrentTimeInt64: os.FxCurrentTimeInt64,
	}
	if rc := sqlite3.Xsqlite3_vfs_register(tls, addrOf(&cVFS), 0); rc != sqlite3.SQLITE_OK {
		return 0, 0, resultError("sqlite3_vfs_register", rc)
	}
	return osVFS, os.FxOpen, nil
}

// uninstallVFS unregisters cVFS.
func uninstallVFS() error {
	tls := libc.NewTLS()
	defer tls.Close()
	if rc := sqlite3.Xsqlite3_vfs_unregister(tls, addrOf(&cVFS)); rc != sqlite3.SQLITE_OK {
		return resultError("sqlite3_vfs_unregister", rc)
	}
	return nil
}

// setFile points the sqlite3_file at pFile at cMethods and the handle id.
// The layout is {pMethods, id}.
func setFile(pFile, id uintptr) {
	*(*uintptr)(cptr(pFile + wordSize)) = id
	(*sqlite3.Tsqlite3_file)(cptr(pFile)).FpMethods = addrOf(&cMethods)
}

// fileID is the handle id setFile stored in the sqlite3_file at pFile.
func fileID(pFile uintptr) uintptr {
	return *(*uintptr)(cptr(pFile + wordSize))
}

func putInt32(p uintptr, v int32) {
	if p != 0 {
		*(*int32)(cptr(p)) = v
	}
}

func xOpen(tls *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	switch classify(flags, zName == 0) {
	case kindTemp:
		osVFS, osOpen := osVFSOpen()
		return callOpen(tls, osOpen, osVFS, zName, pFile, flags, pOutFlags)
	case kindRefused:
		return sqlite3.SQLITE_CANTOPEN
	case kindJournal:
		setFile(pFile, putHandle(&memFile{}))
	default:
		params := url.Values{}
		for i := int32(0); ; i++ {
			k := sqlite3.Xsqlite3_uri_key(tls, zName, i)
			if k == 0 {
				break
			}
			params.Add(libc.GoString(k), libc.GoString(sqlite3.Xsqlite3_uri_parameter(tls, zName, k)))
		}
		f, rc := openMain(libc.GoString(zName), params)
		if rc != sqlite3.SQLITE_OK {
			return rc
		}
		setFile(pFile, putHandle(f))
	}
	putInt32(pOutFlags, flags)
	return sqlite3.SQLITE_OK
}

// xDelete answers SQLITE_OK: the only files SQLite deletes on this VFS
// are its in-memory main journals, which go away at close.
func xDelete(tls *libc.TLS, pVfs, zName uintptr, syncDir int32) int32 {
	return sqlite3.SQLITE_OK
}

// xAccess answers "does not exist" for every name: no journal outlives
// its connection, so there is never a hot journal to play back.
func xAccess(tls *libc.TLS, pVfs, zName uintptr, flags int32, pResOut uintptr) int32 {
	putInt32(pResOut, 0)
	return sqlite3.SQLITE_OK
}

// xFullPathname keeps the database name as given: it is a page store
// name, not an OS path.
func xFullPathname(tls *libc.TLS, pVfs, zName uintptr, nOut int32, zOut uintptr) int32 {
	name := libc.GoString(zName)
	out := cbytes(zOut, nOut)
	if len(name)+1 > len(out) {
		return sqlite3.SQLITE_CANTOPEN
	}
	copy(out, name)
	out[len(name)] = 0
	return sqlite3.SQLITE_OK
}

func xClose(tls *libc.TLS, pFile uintptr) int32 {
	f := takeHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_OK
	}
	return f.close()
}

func xRead(tls *libc.TLS, pFile, buf uintptr, iAmt int32, iOfst int64) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_READ
	}
	return f.read(cbytes(buf, iAmt), iOfst)
}

func xWrite(tls *libc.TLS, pFile, buf uintptr, iAmt int32, iOfst int64) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_WRITE
	}
	return f.write(cbytes(buf, iAmt), iOfst)
}

func xTruncate(tls *libc.TLS, pFile uintptr, size int64) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_TRUNCATE
	}
	return f.truncate(size)
}

func xSync(tls *libc.TLS, pFile uintptr, flags int32) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_FSYNC
	}
	return f.sync()
}

func xFileSize(tls *libc.TLS, pFile, pSize uintptr) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_FSTAT
	}
	n, rc := f.size()
	if pSize != 0 {
		*(*int64)(cptr(pSize)) = n
	}
	return rc
}

func xLock(tls *libc.TLS, pFile uintptr, level int32) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_LOCK
	}
	return f.lock(level)
}

func xUnlock(tls *libc.TLS, pFile uintptr, level int32) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_IOERR_UNLOCK
	}
	return f.unlock(level)
}

// xCheckReservedLock answers "not reserved": SQLite asks it only to
// decide whether a journal is hot, and no journal ever is.
func xCheckReservedLock(tls *libc.TLS, pFile, pResOut uintptr) int32 {
	putInt32(pResOut, 0)
	return sqlite3.SQLITE_OK
}

func xFileControl(tls *libc.TLS, pFile uintptr, op int32, pArg uintptr) int32 {
	f := getHandle(fileID(pFile))
	if f == nil {
		return sqlite3.SQLITE_NOTFOUND
	}
	return f.control(op, pArg)
}

// pragmaArg reads the pragma name and value from the SQLITE_FCNTL_PRAGMA
// argument, a char*[3] whose value entry is NULL when the pragma has none.
func pragmaArg(arg uintptr) (name, value string, hasValue bool) {
	if arg == 0 {
		return "", "", false
	}
	a := (*[3]uintptr)(cptr(arg)) // #nosec G103 -- the SQLITE_FCNTL_PRAGMA contract: arg points at three char* SQLite owns for the call.
	name = libc.GoString(a[1])
	if a[2] == 0 {
		return name, "", false
	}
	return name, libc.GoString(a[2]), true
}

func xSectorSize(tls *libc.TLS, pFile uintptr) int32 {
	return sectorSize
}

func xDeviceCharacteristics(tls *libc.TLS, pFile uintptr) int32 {
	return sqlite3.SQLITE_IOCAP_BATCH_ATOMIC
}
