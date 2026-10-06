// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build unix

package clustermig

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// openCredentialDir opens dir itself -- refusing a symlink or anything not a
// directory -- close-on-exec, so the caller can vet the directory it holds and
// open the file relative to it: a directory swapped in after the vetting is
// never the one read.
//
// A package-level var, not a func: it is the seam LocalCredentials.openDir
// opens the directory through, and a test overrides it to inject an
// open failure that chmod cannot reproduce under every uid -- root bypasses
// the DAC check a chmod 0 directory relies on, so "this process cannot open
// its own credentials directory" is otherwise unreproducible when tests run
// as root.
var openCredentialDir = func(dir string) (*os.File, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	return os.NewFile(uintptr(fd), dir), nil
}

// openCredentialAt opens name inside the already-opened directory dir for
// reading, without following a symlink and without blocking on a FIFO,
// close-on-exec. The caller checks what it opened on the descriptor.
func openCredentialAt(dir *os.File, name string) (*os.File, error) {
	path := filepath.Join(dir.Name(), name)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// fileOwner returns the uid that owns fi.
func fileOwner(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
