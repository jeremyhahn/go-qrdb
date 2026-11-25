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

package pebble

import (
	"errors"
	"os"
	"sync"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// NoRecycleFS is a vfs.FS that turns Pebble's WAL recycling off. Pebble
// keeps obsolete logs in its LogRecycler and reuses one by renaming it to the
// new log's name and writing over it WITHOUT truncation, so bytes written
// before a re-encryption would survive in the reused file's tail.
// ReuseForWrite here removes the old log and creates the new one fresh,
// which vfs.FS permits. Remove also wakes every waiter on Removal, so a
// caller retiring obsolete files can wait for Pebble's asynchronous deletion
// without polling. Set it as Config.FS.
type NoRecycleFS struct {
	vfs.FS
	mu      sync.Mutex
	removed chan struct{}
}

// NewNoRecycleFS returns a NoRecycleFS over base; a nil base is Pebble's
// default OS-backed file system.
func NewNoRecycleFS(base filesys.FS) *NoRecycleFS {
	under := vfs.Default
	if base != nil {
		under = VFS(base)
	}
	return &NoRecycleFS{FS: under, removed: make(chan struct{})}
}

// ReuseForWrite removes oldname and creates newname fresh.
func (f *NoRecycleFS) ReuseForWrite(oldname, newname string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if err := f.Remove(oldname); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return f.FS.Create(newname, category)
}

// Remove removes name and wakes every Removal waiter.
func (f *NoRecycleFS) Remove(name string) error {
	err := f.FS.Remove(name)
	f.mu.Lock()
	close(f.removed)
	f.removed = make(chan struct{})
	f.mu.Unlock()
	return err
}

// Removal returns a channel closed at the next Remove.
func (f *NoRecycleFS) Removal() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed
}
