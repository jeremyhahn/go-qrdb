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

package memory

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestSeekToLast_WithUpperBound_ExactMatch exercises the SeekToLast branch
// where an upper bound is set and the seek lands exactly on the bound,
// requiring a Prev() step to find the last valid entry.
// TestSeekToLast_WithUpperBound_BeyondAllKeys exercises the branch where
// Seek goes past the end, so Last() is used for the candidate.
func TestSeekToLast_WithUpperBound_BeyondAllKeys(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c"} {
		if err := s.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}

	// UpperBound "z" is beyond all keys, so SeekToLast should return "c".
	iter := s.NewIterator(&storage.IteratorOptions{
		UpperBound: []byte("z"),
	})
	defer func() { _ = iter.Close() }()

	if !iter.SeekToLast() {
		t.Fatal("SeekToLast returned false")
	}
	if string(iter.Key()) != "c" {
		t.Errorf("SeekToLast key = %q, want %q", iter.Key(), "c")
	}
}

// TestSeekToLast_BelowLowerBound exercises the path where the candidate
// from SeekToLast falls below the LowerBound.
func TestSeekToLast_BelowLowerBound(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b"} {
		if err := s.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}

	// LowerBound "z" is beyond all keys — no valid result.
	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("z"),
	})
	defer func() { _ = iter.Close() }()

	if iter.SeekToLast() {
		t.Error("SeekToLast should return false when last key is below LowerBound")
	}
}

// TestSeekToLast_ClosedIterator verifies that SeekToLast on a closed iterator
// returns false immediately.
// TestSeekToLast_NoUpperBound verifies SeekToLast without any bounds
// returns the largest key in the store.
// TestSeekToLast_UpperBoundBeforeAllKeys exercises the case where UpperBound
// is before all keys so the candidate from Last() is also above UpperBound.
func TestSeekToLast_UpperBoundBeforeAllKeys(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"m", "n", "o"} {
		if err := s.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}

	// UpperBound "a" is before all keys.
	iter := s.NewIterator(&storage.IteratorOptions{
		UpperBound: []byte("a"),
	})
	defer func() { _ = iter.Close() }()

	if iter.SeekToLast() {
		t.Error("SeekToLast should return false when UpperBound is before all keys")
	}
}
