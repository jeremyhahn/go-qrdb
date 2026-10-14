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

// Package memdir gives a test a scratch directory on a memory-backed
// filesystem, the drop-in for testing.T.TempDir in unit tests of code that
// fsyncs.
//
// The file storage engine fsyncs every record, directory and journal it
// writes. On a real disk those syncs make a unit test's speed a property of
// whatever else the host is writing, and a suite of them on a busy runner
// stalls long enough to miss timeouts that have nothing to do with the code
// under test. On tmpfs an fsync returns at once, so the same tests measure
// the code. Tests whose subject IS the disk -- integration tests and the
// commit benchmarks -- keep testing.T.TempDir.
//
// On Linux the directory is made under /dev/shm and its filesystem is
// checked: a host whose /dev/shm is not tmpfs fails the test rather than
// quietly running it on disk. Elsewhere there is no memory filesystem every
// host carries, and TempDir is testing.T.TempDir.
package memdir

import (
	"fmt"
	"os"
	"testing"
)

// NotMemoryError reports that the directory memdir would create under is not
// on a memory-backed filesystem.
type NotMemoryError struct {
	Path string
}

func (e *NotMemoryError) Error() string {
	return fmt.Sprintf("memdir: %s is not on a memory-backed filesystem", e.Path)
}

// removeAll is os.RemoveAll, indirected so a test can fail the cleanup.
var removeAll = os.RemoveAll

// tempDirIn is TempDir under root, separate so its failure path can be driven
// with a root that is not memory-backed.
func tempDirIn(tb testing.TB, root string) string {
	tb.Helper()
	dir, err := newDir(root)
	if err != nil {
		tb.Fatalf("memdir: %v", err)
		return ""
	}
	tb.Cleanup(func() {
		if err := removeAll(dir); err != nil {
			tb.Errorf("memdir: removing %s: %v", dir, err)
		}
	})
	return dir
}

// newDir creates a fresh directory under root, refusing a root that is not
// memory-backed.
func newDir(root string) (string, error) {
	mem, err := IsMemory(root)
	if err != nil {
		return "", err
	}
	if !mem {
		return "", &NotMemoryError{Path: root}
	}
	return os.MkdirTemp(root, "qrdb-memdir-*")
}
