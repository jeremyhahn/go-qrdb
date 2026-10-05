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

package file

import (
	"bytes"
	"os"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// fileEntry holds a key-value pair loaded from the filesystem.
type fileEntry struct {
	key   []byte
	value []byte
}

// fileIterator implements storage.Iterator by reading all keys from the
// directory at creation time, sorting them, and iterating in order.
type fileIterator struct {
	entries     []fileEntry
	pos         int
	valid       bool
	opts        *storage.IteratorOptions
	err         error
	initialized bool
}

// newIterator creates a new iterator over the file engine's directory.
// It eagerly loads all matching entries into memory for sorted iteration.
func newIterator(e *Engine, opts *storage.IteratorOptions) *fileIterator {
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	it := &fileIterator{
		pos:  -1,
		opts: opts,
	}
	it.load(e)
	return it
}

// newIteratorFromEntries creates an iterator from pre-loaded entries,
// used by snapshots to avoid re-reading the filesystem.
func newIteratorFromEntries(entries []fileEntry, opts *storage.IteratorOptions) *fileIterator {
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}
	return &fileIterator{
		entries: entries,
		pos:     -1,
		opts:    opts,
	}
}

// load reads every record under the engine root that matches the iterator
// bounds, loading its key and optionally its value.
//
// Every record's key lives inside its file, not in its path (the filename is
// a hash — see Engine), so unlike the previous layout this must open and
// decode every candidate file to learn its key, whether or not OnlyKeys asks
// for the value too. A read or decode failure aborts the load and is
// reported through Error() rather than silently dropping that entry: a
// corrupt record is a genuine fault (see RecordCorruptError), and a scan
// that silently skipped a key would be a wrong answer instead of a loud one.
func (it *fileIterator) load(e *Engine) {
	var entries []fileEntry
	walkErr := e.walkShardDirs(func(path string, info os.FileInfo, err error) error { // #nosec G122 -- internal storage directory, no symlink risk
		if err != nil {
			// The directory listing that produced path is already stale --
			// a concurrent Set renamed its ".tmp-set-*" file into place and
			// away from this name, or a concurrent Delete removed a
			// record, between walkShardDirs reading the parent directory
			// and stat'ing this entry. Neither is a scan fault --
			// storage.Storage gives Set/Delete on different keys no
			// serialization promise against a concurrent scan -- so skip
			// the vanished entry and keep walking rather than abort the
			// whole scan over it (go-qrdb#592 F2). os.ReadDir(e.dir)
			// failing -- the engine's own root being gone, a real fault --
			// is surfaced directly by walkShardDirs and never reaches this
			// callback at all.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() || !isHashFileName(info.Name()) {
			return nil
		}
		record, readErr := os.ReadFile(path) // #nosec G122 -- internal storage directory
		if readErr != nil {
			if os.IsNotExist(readErr) {
				// Same race, caught one step later: Walk's own stat saw the
				// record, but a concurrent Delete removed it before this
				// read reached it.
				return nil
			}
			return readErr
		}
		storedKey, value, decErr := decodeRecord(path, record)
		if decErr != nil {
			return decErr
		}
		key := append([]byte(nil), storedKey...)
		if len(it.opts.LowerBound) > 0 && bytes.Compare(key, it.opts.LowerBound) < 0 {
			return nil
		}
		if len(it.opts.UpperBound) > 0 && bytes.Compare(key, it.opts.UpperBound) >= 0 {
			return nil
		}
		fe := fileEntry{key: key}
		if !it.opts.OnlyKeys {
			fe.value = append([]byte(nil), value...)
		}
		entries = append(entries, fe)
		return nil
	})
	if walkErr != nil {
		it.err = walkErr
	}

	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})
	if it.opts.Reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	it.entries = entries
}

// SeekToFirst positions the iterator at the first entry.
func (it *fileIterator) SeekToFirst() bool {
	it.initialized = true
	if len(it.entries) == 0 {
		it.valid = false
		return false
	}
	it.pos = 0
	it.valid = true
	return true
}

// SeekToLast positions the iterator at the last entry.
func (it *fileIterator) SeekToLast() bool {
	it.initialized = true
	if len(it.entries) == 0 {
		it.valid = false
		return false
	}
	it.pos = len(it.entries) - 1
	it.valid = true
	return true
}

// Seek positions the iterator at the first key >= target.
func (it *fileIterator) Seek(target []byte) bool {
	it.initialized = true
	idx := sort.Search(len(it.entries), func(i int) bool {
		return bytes.Compare(it.entries[i].key, target) >= 0
	})
	if idx >= len(it.entries) {
		it.valid = false
		return false
	}
	it.pos = idx
	it.valid = true
	return true
}

// Next advances the iterator to the next entry.
func (it *fileIterator) Next() bool {
	if !it.initialized {
		return false
	}
	it.pos++
	if it.pos >= len(it.entries) {
		it.valid = false
		return false
	}
	it.valid = true
	return true
}

// Prev moves the iterator to the previous entry.
func (it *fileIterator) Prev() bool {
	if !it.initialized {
		return false
	}
	it.pos--
	if it.pos < 0 {
		it.valid = false
		return false
	}
	it.valid = true
	return true
}

// Valid reports whether the iterator is positioned at a valid entry.
func (it *fileIterator) Valid() bool { return it.valid }

// Key returns the current key. Returns nil when invalid.
func (it *fileIterator) Key() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.pos].key
}

// Value returns the current value. Returns nil when invalid or OnlyKeys is set.
func (it *fileIterator) Value() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.pos].value
}

// Error returns any accumulated error encountered during iteration.
func (it *fileIterator) Error() error { return it.err }

// Close releases resources held by the iterator.
func (it *fileIterator) Close() error { return nil }
