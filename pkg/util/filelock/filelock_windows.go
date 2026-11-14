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

//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockedRegionBytes is the length of the byte range LockFileEx locks. The
// range may lie beyond the end of the (empty) lock file; the lock is on the
// range, not on data.
const lockedRegionBytes = 1

// lockFile takes an exclusive LockFileEx lock on the first byte of f, blocking
// until it is granted.
func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, lockedRegionBytes, 0, ol)
}

// unlockFile releases the LockFileEx lock on the first byte of f.
func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockedRegionBytes, 0, ol)
}
