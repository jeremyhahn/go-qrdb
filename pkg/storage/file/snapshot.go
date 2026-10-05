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
	"sort"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// fileSnapshot implements storage.Snapshot by capturing all key-value pairs
// from the file engine at creation time. Subsequent reads are served from
// the in-memory copy, providing isolation from concurrent mutations.
type fileSnapshot struct {
	entries []fileEntry
	closed  atomic.Bool
}

// newSnapshot reads all entries from the engine into a point-in-time snapshot.
//
// it.Error() is checked and propagated: a snapshot is a promise to hold
// every key that was durable when it was taken, so a load the underlying
// walk could not complete must fail loudly rather than return a silently
// truncated snapshot with a nil error (go-qrdb#592 F2).
func newSnapshot(e *Engine) (*fileSnapshot, error) {
	it := newIterator(e, nil)
	if it.Error() != nil {
		return nil, it.Error()
	}
	entries := make([]fileEntry, len(it.entries))
	copy(entries, it.entries)
	return &fileSnapshot{entries: entries}, nil
}

// Get retrieves the value for the given key from the snapshot. Returns nil, nil
// when the key does not exist.
func (s *fileSnapshot) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}}
	}
	if len(key) == 0 {
		return nil, &storage.SnapshotError{Op: "get", Err: storage.ErrEmptyKey}
	}

	idx := sort.Search(len(s.entries), func(i int) bool {
		return bytes.Compare(s.entries[i].key, key) >= 0
	})
	if idx < len(s.entries) && bytes.Equal(s.entries[idx].key, key) {
		cp := make([]byte, len(s.entries[idx].value))
		copy(cp, s.entries[idx].value)
		return cp, nil
	}
	return nil, nil
}

// NewIterator creates an iterator over the snapshot entries filtered by opts.
func (s *fileSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.closed.Load() {
		return newIteratorFromEntries(nil, opts)
	}
	if opts == nil {
		opts = &storage.IteratorOptions{}
	}

	entries := s.entries
	var filtered []fileEntry
	for _, e := range entries {
		if len(opts.LowerBound) > 0 && bytes.Compare(e.key, opts.LowerBound) < 0 {
			continue
		}
		if len(opts.UpperBound) > 0 && bytes.Compare(e.key, opts.UpperBound) >= 0 {
			continue
		}
		filtered = append(filtered, e)
	}
	if opts.Reverse {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}
	return newIteratorFromEntries(filtered, opts)
}

// Close releases resources held by the snapshot. Calling Close on an
// already-closed snapshot is a no-op.
func (s *fileSnapshot) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	return nil
}
