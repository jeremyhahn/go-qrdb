// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package secure

import (
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/jeremyhahn/go-enclave"
)

// MaxFileHandleSize bounds the secret ReadFileHandle reads. A credential or
// key file is a few hundred bytes; the bound keeps a misconfigured path (a
// log, a disk image) from being locked into memory.
const MaxFileHandleSize = 64 << 10

var (
	// ErrSecretFileNotRegular is the cause of a FileHandleError for a path
	// that is not a regular file.
	ErrSecretFileNotRegular = errors.New("secure: secret file is not a regular file")

	// ErrSecretFileEmpty is the cause of a FileHandleError for a file that
	// holds no material once its trailing line ending is removed.
	ErrSecretFileEmpty = errors.New("secure: secret file is empty")

	// ErrSecretFileTooLarge is the cause of a FileHandleError for a file
	// larger than MaxFileHandleSize.
	ErrSecretFileTooLarge = errors.New("secure: secret file exceeds the size bound")
)

// FileHandleError reports that a secret file could not be read into a handle.
// It names the path, never the contents.
type FileHandleError struct {
	Path string
	Err  error
}

// Error implements the error interface.
func (e *FileHandleError) Error() string {
	return "secure: secret file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *FileHandleError) Unwrap() error { return e.Err }

// FileModeError reports a secret file readable, writable or executable by its
// group or by others.
type FileModeError struct {
	Path string
	Mode os.FileMode
}

// Error implements the error interface.
func (e *FileModeError) Error() string {
	return "secure: secret file " + strconv.Quote(e.Path) + " has mode " + e.Mode.String() + "; it must be accessible only by its owner (0600 or 0400)"
}

// FileOwnerError reports a secret file not owned by the process's user.
type FileOwnerError struct {
	Path string
}

// Error implements the error interface.
func (e *FileOwnerError) Error() string {
	return "secure: secret file " + strconv.Quote(e.Path) + " is not owned by the process user"
}

// ReadFileHandle reads the secret held in the file at path into a memory
// enclave and returns its handle; the caller owns it and destroys it. The
// file is read straight into locked memory, so no copy of the secret is made
// in ordinary memory, and one trailing line ending ("\n" or "\r\n") is not
// part of the secret.
//
// The file must be a regular file of at most MaxFileHandleSize bytes, owned
// by the process user and accessible by nobody else; anything else fails
// closed with a *FileHandleError, *FileModeError or *FileOwnerError naming the
// path. The path is opened without blocking, so a named pipe or device is
// refused at once rather than stalling until a writer connects, and every
// check runs on the opened descriptor, so a file replaced after the checks is
// never the one read. The enclave is admitted against the process
// locked-memory budget like every Buffer.
func ReadFileHandle(path string) (enclave.ByteAccess, error) {
	f, err := openSecretFile(path)
	if err != nil {
		return nil, &FileHandleError{Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	size, err := checkSecretFile(path, f)
	if err != nil {
		return nil, err
	}
	a, err := processAllocator()
	if err != nil {
		return nil, &FileHandleError{Path: path, Err: err}
	}
	read, err := a.NewSized(size)
	if err != nil {
		return nil, &FileHandleError{Path: path, Err: wrapAllocError(size, err)}
	}
	n := 0
	if err := read.WithBytes(func(dst []byte) error {
		if _, rerr := io.ReadFull(f, dst); rerr != nil {
			return rerr
		}
		n = secretLen(dst)
		return nil
	}); err != nil {
		LogRelease("failed ReadFileHandle read", read.Destroy())
		return nil, &FileHandleError{Path: path, Err: err}
	}
	if n == size {
		return read, nil
	}
	defer func() { LogRelease("ReadFileHandle line-ending trim", read.Destroy()) }()
	if n == 0 {
		return nil, &FileHandleError{Path: path, Err: ErrSecretFileEmpty}
	}
	trimmed, err := a.NewSized(n)
	if err != nil {
		return nil, &FileHandleError{Path: path, Err: wrapAllocError(n, err)}
	}
	if err := read.WithBytes(func(src []byte) error {
		return trimmed.WithBytes(func(dst []byte) error {
			copy(dst, src[:n])
			return nil
		})
	}); err != nil {
		LogRelease("failed ReadFileHandle trim", trimmed.Destroy())
		return nil, &FileHandleError{Path: path, Err: err}
	}
	return trimmed, nil
}

// checkSecretFile returns the size of the open secret file f after refusing
// one that is not a regular file, is out of bounds, is accessible by anyone
// but its owner, or is owned by another user. Every fact comes from fstat on
// the open descriptor, never from the path.
func checkSecretFile(path string, f *os.File) (int, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, &FileHandleError{Path: path, Err: err}
	}
	if !info.Mode().IsRegular() {
		return 0, &FileHandleError{Path: path, Err: ErrSecretFileNotRegular}
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return 0, &FileModeError{Path: path, Mode: perm}
	}
	if err := checkSecretOwner(path, info); err != nil {
		return 0, err
	}
	switch size := info.Size(); {
	case size == 0:
		return 0, &FileHandleError{Path: path, Err: ErrSecretFileEmpty}
	case size > MaxFileHandleSize:
		return 0, &FileHandleError{Path: path, Err: ErrSecretFileTooLarge}
	default:
		return int(size), nil
	}
}

// secretLen is the length of b without one trailing "\n" or "\r\n".
func secretLen(b []byte) int {
	n := len(b)
	if n > 0 && b[n-1] == '\n' {
		n--
		if n > 0 && b[n-1] == '\r' {
			n--
		}
	}
	return n
}
