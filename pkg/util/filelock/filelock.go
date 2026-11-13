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

// Package filelock provides an exclusive advisory lock on a file that excludes
// other processes (and other File values in this process) from the same
// critical section. It is the operating system's own mechanism --
// flock(2) on Unix, LockFileEx on Windows -- not a locking protocol of our own:
// the kernel releases the lock when the holder's descriptor closes, so a
// holder that crashes cannot leave it held.
//
// A File value is not itself safe for concurrent use. The OS lock is owned by
// the open file, so two goroutines sharing one File would both "hold" it;
// callers that share a File across goroutines serialize them with a mutex of
// their own, taken before Lock.
package filelock

import "os"

// lockFileMode is the permission a newly created lock file is given. The file
// carries no data, but it sits beside private data and has no reason to be
// readable by anyone else.
const lockFileMode = 0o600

// Error reports a failed lock operation. Op is "open", "lock", "unlock" or
// "close".
type Error struct {
	Op   string
	Path string
	Err  error
}

// Error returns a human-readable description of the failure.
func (e *Error) Error() string {
	return "filelock: " + e.Op + " " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *Error) Unwrap() error {
	return e.Err
}

// File is an open lock file. The zero value is not usable; obtain one from
// Open.
type File struct {
	f *os.File
}

// Open opens the lock file at path, creating it (mode 0600) when it does not
// exist. Opening does not lock it. The file is never removed: deleting a lock
// file while another process holds or waits on it would let a third process
// create a fresh file and lock that one, so two holders could coexist.
func Open(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, lockFileMode) // #nosec G304 -- the caller names its own lock file
	if err != nil {
		return nil, &Error{Op: "open", Path: path, Err: err}
	}
	return &File{f: f}, nil
}

// Lock blocks until it holds the exclusive lock. The lock is not reentrant and
// is not released by a context: a caller that cannot afford to wait indefinitely
// must bound the critical sections of every process that uses the file.
func (l *File) Lock() error {
	if err := lockFile(l.f); err != nil {
		return &Error{Op: "lock", Path: l.f.Name(), Err: err}
	}
	return nil
}

// TryLock takes the exclusive lock if no other open file holds it, and reports
// whether it did; it never waits. A caller that holds the lock for as long as
// it owns a resource uses it to refuse a second owner rather than queue it.
func (l *File) TryLock() (bool, error) {
	locked, err := tryLockFile(l.f)
	if err != nil {
		return false, &Error{Op: "lock", Path: l.f.Name(), Err: err}
	}
	return locked, nil
}

// Unlock releases the lock. If the operating system refuses, the file is
// closed so the kernel releases the lock regardless -- a lock that cannot be
// released would otherwise stall every other process indefinitely -- and the
// File is unusable afterwards (every later Lock fails).
func (l *File) Unlock() error {
	if err := unlockFile(l.f); err != nil {
		_ = l.f.Close()
		return &Error{Op: "unlock", Path: l.f.Name(), Err: err}
	}
	return nil
}

// Close closes the file, releasing the lock if it is held.
func (l *File) Close() error {
	if err := l.f.Close(); err != nil {
		return &Error{Op: "close", Path: l.f.Name(), Err: err}
	}
	return nil
}
