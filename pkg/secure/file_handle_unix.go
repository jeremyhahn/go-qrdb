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

//go:build unix

package secure

import (
	"os"
	"syscall"
)

// openSecretFile opens path read-only with O_NONBLOCK. An open(2) of a FIFO
// for reading blocks until a writer connects, and a device open may block on
// the device; with O_NONBLOCK both return at once, so checkSecretFile refuses
// them on the descriptor instead of the open stalling startup. O_NONBLOCK has
// no effect on reading a regular file, the only kind ever read.
func openSecretFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // the path is an operator-configured secret reference
}

// checkSecretOwner refuses a secret file, described by info from fstat on its
// open descriptor, that the process user does not own.
func checkSecretOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Getuid(); uid >= 0 && uint32(uid) != stat.Uid { //nolint:gosec // uid is a non-negative process UID
		return &FileOwnerError{Path: path}
	}
	return nil
}
