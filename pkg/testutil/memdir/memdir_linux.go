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

package memdir

import (
	"testing"

	"golang.org/x/sys/unix"
)

// memRoot is the tmpfs mount every Linux host and container carries.
const memRoot = "/dev/shm"

// TempDir returns a new directory on tmpfs, removed when tb and its
// subtests finish. A host without a tmpfs /dev/shm fails tb.
func TempDir(tb testing.TB) string {
	tb.Helper()
	return tempDirIn(tb, memRoot)
}

// IsMemory reports whether path is on tmpfs.
func IsMemory(path string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false, err
	}
	return st.Type == unix.TMPFS_MAGIC, nil
}
