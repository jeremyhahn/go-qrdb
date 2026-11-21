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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

// ---------------------------------------------------------------------------
// iterator.Error() — currently 0% covered
// ---------------------------------------------------------------------------

// TestFileIterator_Error verifies that Error() always returns nil for the
// file iterator since it does not accumulate errors during normal operation.
func TestFileIterator_Error(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	if err := eng.Set([]byte("key1"), []byte("val1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	if err := iter.Error(); err != nil {
		t.Errorf("Error() = %v, want nil", err)
	}

	// Error() should still be nil after iterating.
	iter.SeekToFirst()
	for iter.Next() {
	}
	if err := iter.Error(); err != nil {
		t.Errorf("Error() after iteration = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// iterator.Prev() — 75% (missing the not-initialized path)
// ---------------------------------------------------------------------------

// TestFileIterator_Prev_ReachesStart verifies that iterating backward past the
// first entry sets the iterator invalid.
func TestFileIterator_Prev_ReachesStart(t *testing.T) {
	dir := memdir.TempDir(t)
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = eng.Close() }()

	for _, k := range []string{"a", "b", "c"} {
		if err := eng.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := newIterator(eng, nil)
	defer func() { _ = iter.Close() }()

	// Position at last entry, then walk backward off the front.
	if !iter.SeekToLast() {
		t.Fatal("SeekToLast returned false")
	}
	iter.Prev() // b
	iter.Prev() // a
	if iter.Prev() {
		t.Error("Prev() past start should return false")
	}
	if iter.Valid() {
		t.Error("Valid() after walking off start should be false")
	}
}
